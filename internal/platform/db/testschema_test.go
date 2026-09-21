package db

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// The name, with no database: it is the name that does the work, and everything
// below depends on it being different in two processes and the same within one.
func TestUniqueSchema_IsOneNamePerRunAndNeverTwoTestsInOne(t *testing.T) {
	// Stable within this process. A harness that calls this twice for the same
	// test — once to create, once to drop — must get the same schema back.
	if a, b := UniqueSchema("forge_x_", "SomeTest"), UniqueSchema("forge_x_", "SomeTest"); a != b {
		t.Errorf("one process named the same test %q and then %q", a, b)
	}
	if !strings.HasSuffix(UniqueSchema("forge_x_", "SomeTest"), "_"+RunID()) {
		t.Errorf("%q does not end in this run's id %q", UniqueSchema("forge_x_", "SomeTest"), RunID())
	}

	// Different between processes. This is the whole fix: two worktrees running
	// the same test at the same time must not write the same name.
	if a, b := schemaWithRun("forge_x_", "SomeTest", "aaaaaaaaaa"),
		schemaWithRun("forge_x_", "SomeTest", "bbbbbbbbbb"); a == b {
		t.Fatalf("two runs both named their schema %q, which is the collision this exists to stop", a)
	}

	// A legal, unquoted identifier: these names are concatenated into DDL.
	for _, name := range []string{"Test/Sub case-1", "Test With Spaces", "Ünïcødé", ""} {
		got := schemaWithRun("forge_x_", name, "aaaaaaaaaa")
		for _, r := range got {
			ok := ('a' <= r && r <= 'z') || ('0' <= r && r <= '9') || r == '_'
			if !ok {
				t.Errorf("%q (from %q) carries %q, which would need quoting in DDL", got, name, r)
				break
			}
		}
		if len(got) > maxSchemaName {
			t.Errorf("%q is %d bytes; Postgres truncates at %d and two names would merge", got, len(got), maxSchemaName)
		}
	}

	// ‼️ Truncation takes the head, never the run id. Truncating the FINISHED
	// name is the subtle way to reintroduce the collision: two long test names
	// sharing a prefix would come back identical, run id and all.
	long := strings.Repeat("verylongtestname", 6)
	a := schemaWithRun("forge_x_", long+"_one", "aaaaaaaaaa")
	b := schemaWithRun("forge_x_", long+"_two", "bbbbbbbbbb")
	if a == b {
		t.Errorf("two long names truncated to the same schema %q", a)
	}
	if !strings.HasSuffix(a, "_aaaaaaaaaa") || !strings.HasSuffix(b, "_bbbbbbbbbb") {
		t.Errorf("truncation ate the run id: %q and %q", a, b)
	}
}

// ‼️ Two runs in parallel do not see each other's tables (2026-09-20).
//
// Every integration harness here opened with `drop schema if exists <name>
// cascade`, and the name was the test's. Two worktrees of this repository on one
// machine share one Postgres, so `go test ./...` in both ran the same test name
// at the same time and the second run dropped the first run's schema mid-test.
// The first run then failed on "relation does not exist" for a table its own
// migration had just created — not reproducible, and pointing at the wrong code.
//
// This is that situation, in one process: run A migrates and writes a row, run B
// then performs a harness's ENTRY SEQUENCE in full — the drop, the create, the
// migration — and A's schema has to come through it untouched.
func TestUniqueSchema_TwoRunsDoNotSeeEachOthersTables(t *testing.T) {
	url := os.Getenv("FORGE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("FORGE_TEST_DATABASE_URL is unset; skipping live-database tests. Run `make db-up` then `make test-integration`.")
	}
	ctx := context.Background()
	cfg := func(u string) config.DBConfig {
		return config.DBConfig{URL: u, MaxConns: 4, MinConns: 1,
			MaxConnLifetime: time.Hour, MaxConnIdleTime: time.Minute, ConnectTimeout: 10 * time.Second}
	}
	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}

	admin, err := Connect(ctx, cfg(url), logx.Discard())
	if err != nil {
		t.Fatalf("cannot reach the test database: %v", err)
	}
	defer admin.Close()

	// The same test name, in two runs. Only the run id differs — exactly what
	// two worktrees produce.
	nameA := schemaWithRun("forge_par_", t.Name(), "run"+RunID())
	nameB := schemaWithRun("forge_par_", t.Name(), "two"+RunID())
	if nameA == nameB {
		t.Fatalf("both runs named their schema %q; nothing below can distinguish them", nameA)
	}

	// enter is one harness's opening: the destructive drop, the create, the
	// migration chain, and a pool scoped to the schema.
	enter := func(schema string) *pgxpool.Pool {
		if _, err := admin.Exec(ctx, "drop schema if exists "+schema+" cascade"); err != nil {
			t.Fatalf("dropping %s: %v", schema, err)
		}
		if _, err := admin.Exec(ctx, "create schema "+schema); err != nil {
			t.Fatalf("creating %s: %v", schema, err)
		}
		t.Cleanup(func() { DropTestSchema(url, schema) })
		pool, err := Connect(ctx, cfg(url+sep+"search_path="+schema), logx.Discard())
		if err != nil {
			t.Fatalf("connecting to %s: %v", schema, err)
		}
		t.Cleanup(pool.Close)
		if _, err := MigrateFS(ctx, pool, Files, MigrationsDir, logx.Discard()); err != nil {
			t.Fatalf("migrating %s: %v", schema, err)
		}
		return pool
	}

	aPool := enter(nameA)
	if _, err := aPool.Exec(ctx, "create table run_marker (who text primary key)"); err != nil {
		t.Fatalf("creating A's marker: %v", err)
	}
	if _, err := aPool.Exec(ctx, "insert into run_marker (who) values ('a')"); err != nil {
		t.Fatalf("writing A's marker: %v", err)
	}

	// The second worktree starts while A is still running.
	bPool := enter(nameB)

	// A's table survived B's entry. Before the run id this is the assertion that
	// failed, with "relation \"run_marker\" does not exist".
	var who string
	if err := aPool.QueryRow(ctx, "select who from run_marker").Scan(&who); err != nil {
		t.Fatalf("A's own table is gone after B started: %v", err)
	}
	if who != "a" {
		t.Errorf("A's marker reads %q", who)
	}

	// And B cannot see it: the isolation is real, not an accident of ordering.
	var n int
	if err := bPool.QueryRow(ctx,
		"select count(*) from information_schema.tables where table_schema = $1 and table_name = 'run_marker'",
		nameB).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("B's schema %s already holds %d run_marker table(s); the two runs share storage", nameB, n)
	}
	if err := bPool.QueryRow(ctx, "select count(*) from run_marker").Scan(&n); err == nil {
		t.Errorf("B read A's table and found %d row(s)", n)
	}

	// B wrote its own, and A still reads only its own.
	if _, err := bPool.Exec(ctx, "create table run_marker (who text primary key)"); err != nil {
		t.Fatalf("creating B's marker: %v", err)
	}
	if _, err := bPool.Exec(ctx, "insert into run_marker (who) values ('b')"); err != nil {
		t.Fatalf("writing B's marker: %v", err)
	}
	if err := aPool.QueryRow(ctx, "select who from run_marker").Scan(&who); err != nil || who != "a" {
		t.Errorf("A now reads %q (%v) — the two runs are in one schema", who, err)
	}

	// Cleanup still works: a unique name is no use if it is never dropped.
	DropTestSchema(url, nameB)
	if err := admin.QueryRow(ctx,
		"select count(*) from information_schema.schemata where schema_name = $1", nameB).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%s survived DropTestSchema; every run would leave one behind", nameB)
	}
	if err := aPool.QueryRow(ctx, "select who from run_marker").Scan(&who); err != nil || who != "a" {
		t.Errorf("dropping B's schema took A's with it: %q (%v)", who, err)
	}
}
