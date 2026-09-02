package db_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// These tests need a real Postgres. They are skipped, loudly, when
// FORGE_TEST_DATABASE_URL is unset — a schema test against a hand-written
// CREATE TABLE proves nothing about the schema that actually ships.

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("FORGE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("FORGE_TEST_DATABASE_URL is unset; skipping live-database tests. Run `make db-up` then `make test-integration`.")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, config.DBConfig{
		URL: url, MaxConns: 8, MinConns: 1,
		MaxConnLifetime: time.Hour, MaxConnIdleTime: time.Minute,
		ConnectTimeout: 10 * time.Second,
	}, logx.Discard())
	if err != nil {
		t.Fatalf("cannot reach the test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// freshSchema gives each test an isolated schema so tests cannot see each
// other's DDL. Using search_path rather than separate databases keeps the test
// fast while still being a real, migrated Postgres schema.
func freshSchema(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	name := "forge_test_" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "_"))
	if len(name) > 60 {
		name = name[:60]
	}
	ctx := context.Background()
	if _, err := pool.Exec(ctx, "drop schema if exists "+name+" cascade"); err != nil {
		t.Fatalf("dropping schema: %v", err)
	}
	if _, err := pool.Exec(ctx, "create schema "+name); err != nil {
		t.Fatalf("creating schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "drop schema if exists "+name+" cascade")
	})
	return name
}

func scopedPool(t *testing.T, schema string) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("FORGE_TEST_DATABASE_URL")
	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	// search_path pins DDL to the test schema; public stays on the path so the
	// citext extension (installed once, database-wide) remains resolvable.
	scoped := url + sep + "search_path=" + schema + ",public"
	pool, err := db.Connect(context.Background(), config.DBConfig{
		URL: scoped, MaxConns: 4, MinConns: 1,
		MaxConnLifetime: time.Hour, MaxConnIdleTime: time.Minute,
		ConnectTimeout: 10 * time.Second,
	}, logx.Discard())
	if err != nil {
		t.Fatalf("cannot open a schema-scoped pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestMigrationsAreIdempotent is the load-bearing test of this package.
//
// db.Migrate re-runs the entire chain on every boot, deliberately, so that
// idempotency cannot quietly rot. That design is only safe if the property
// actually holds — so this runs the chain twice against a live database and
// asserts the second run neither errors nor changes the schema.
func TestMigrationsAreIdempotent(t *testing.T) {
	pool := testPool(t)
	schema := freshSchema(t, pool)
	scoped := scopedPool(t, schema)
	ctx := context.Background()

	migrations, err := db.LoadMigrations(db.Files, db.MigrationsDir)
	if err != nil {
		t.Fatalf("loading migrations: %v", err)
	}
	if len(migrations) == 0 {
		t.Fatal("zero migrations loaded; this test would pass vacuously")
	}

	first, err := db.Migrate(ctx, scoped, migrations, logx.Discard())
	if err != nil {
		t.Fatalf("first migration run failed: %v", err)
	}
	for _, r := range first {
		if !r.FirstApply {
			t.Errorf("%04d_%s reported FirstApply=false on a fresh schema", r.Migration.Version, r.Migration.Name)
		}
	}
	snapshotA := schemaSnapshot(t, scoped, schema)

	second, err := db.Migrate(ctx, scoped, migrations, logx.Discard())
	if err != nil {
		t.Fatalf("second migration run failed; the chain is NOT idempotent: %v", err)
	}
	for _, r := range second {
		if r.FirstApply {
			t.Errorf("%04d_%s reported FirstApply=true on the second run", r.Migration.Version, r.Migration.Name)
		}
	}
	snapshotB := schemaSnapshot(t, scoped, schema)

	if snapshotA != snapshotB {
		t.Errorf("the schema changed between run 1 and run 2 — a migration is not idempotent.\n--- after run 1 ---\n%s\n--- after run 2 ---\n%s", snapshotA, snapshotB)
	}

	// A third run, to catch anything that only diverges after the first repeat.
	if _, err := db.Migrate(ctx, scoped, migrations, logx.Discard()); err != nil {
		t.Fatalf("third migration run failed: %v", err)
	}
	if s := schemaSnapshot(t, scoped, schema); s != snapshotA {
		t.Error("the schema changed on the third run")
	}
}

// schemaSnapshot renders every column, type, nullability, default, constraint
// and index in the schema as a stable string. Comparing snapshots is how we
// detect a migration that "succeeds" twice but leaves different state.
func schemaSnapshot(t *testing.T, pool *pgxpool.Pool, schema string) string {
	t.Helper()
	ctx := context.Background()
	var b strings.Builder

	rows, err := pool.Query(ctx, `
		select table_name, column_name, data_type, is_nullable, coalesce(column_default,'')
		  from information_schema.columns
		 where table_schema = $1
		 order by table_name, column_name`, schema)
	if err != nil {
		t.Fatalf("snapshot columns: %v", err)
	}
	defer rows.Close()
	cols := 0
	for rows.Next() {
		var tbl, col, typ, nullable, def string
		if err := rows.Scan(&tbl, &col, &typ, &nullable, &def); err != nil {
			t.Fatalf("scan: %v", err)
		}
		b.WriteString("col " + tbl + "." + col + " " + typ + " null=" + nullable + " default=" + def + "\n")
		cols++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("snapshot columns iteration: %v", err)
	}
	if cols == 0 {
		t.Fatal("snapshot found zero columns; the comparison below would be vacuously equal")
	}

	idx, err := pool.Query(ctx, `
		select indexname, indexdef from pg_indexes
		 where schemaname = $1 order by indexname`, schema)
	if err != nil {
		t.Fatalf("snapshot indexes: %v", err)
	}
	defer idx.Close()
	for idx.Next() {
		var name, def string
		if err := idx.Scan(&name, &def); err != nil {
			t.Fatalf("scan index: %v", err)
		}
		b.WriteString("idx " + name + " " + def + "\n")
	}
	if err := idx.Err(); err != nil {
		t.Fatalf("snapshot index iteration: %v", err)
	}

	cons, err := pool.Query(ctx, `
		select c.conname, pg_get_constraintdef(c.oid)
		  from pg_constraint c
		  join pg_namespace n on n.oid = c.connamespace
		 where n.nspname = $1 order by c.conname`, schema)
	if err != nil {
		t.Fatalf("snapshot constraints: %v", err)
	}
	defer cons.Close()
	for cons.Next() {
		var name, def string
		if err := cons.Scan(&name, &def); err != nil {
			t.Fatalf("scan constraint: %v", err)
		}
		b.WriteString("con " + name + " " + def + "\n")
	}
	if err := cons.Err(); err != nil {
		t.Fatalf("snapshot constraint iteration: %v", err)
	}
	return b.String()
}

// TestMigrationLedgerCountsRuns proves the ledger is a record of what happened
// rather than a gate on what may happen. run_count rising on every boot is the
// visible evidence that the "re-run everything" model is actually in force.
func TestMigrationLedgerCountsRuns(t *testing.T) {
	pool := testPool(t)
	schema := freshSchema(t, pool)
	scoped := scopedPool(t, schema)
	ctx := context.Background()

	migrations, err := db.LoadMigrations(db.Files, db.MigrationsDir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := db.Migrate(ctx, scoped, migrations, logx.Discard()); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}
	var runCount int64
	if err := scoped.QueryRow(ctx,
		`select run_count from forge_schema_migrations where version = 1`).Scan(&runCount); err != nil {
		t.Fatalf("reading ledger: %v", err)
	}
	if runCount != 3 {
		t.Errorf("run_count = %d, want 3; the chain is not being re-run on every boot", runCount)
	}
}

// TestChecksumDriftIsReportedNotFatal covers the case where someone edits a
// migration that already ran. Refusing to boot would strand an operator
// mid-incident; silently ignoring it is how two deployments diverge unnoticed.
// The chosen behaviour is: warn loudly, converge anyway.
func TestChecksumDriftIsReportedNotFatal(t *testing.T) {
	pool := testPool(t)
	schema := freshSchema(t, pool)
	scoped := scopedPool(t, schema)
	ctx := context.Background()

	migrations, err := db.LoadMigrations(db.Files, db.MigrationsDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Migrate(ctx, scoped, migrations, logx.Discard()); err != nil {
		t.Fatal(err)
	}

	// Simulate an edited file by rewriting the recorded checksum.
	if _, err := scoped.Exec(ctx,
		`update forge_schema_migrations set checksum = 'drifted' where version = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Migrate(ctx, scoped, migrations, logx.Discard()); err != nil {
		t.Errorf("checksum drift must not be fatal, but migration failed: %v", err)
	}
	var checksum string
	if err := scoped.QueryRow(ctx,
		`select checksum from forge_schema_migrations where version = 1`).Scan(&checksum); err != nil {
		t.Fatal(err)
	}
	if checksum == "drifted" {
		t.Error("the ledger checksum should be reconciled to the file's actual checksum after the run")
	}
}

func TestVersionCollisionIsRefused(t *testing.T) {
	// Two files claiming one version is how a migration silently never runs.
	fsys := fstest.MapFS{
		"sql/0001_a.sql": &fstest.MapFile{Data: []byte("select 1;")},
		"sql/0001_b.sql": &fstest.MapFile{Data: []byte("select 1;")},
	}
	_, err := db.LoadMigrations(fsys, "sql")
	if err == nil {
		t.Fatal("two migrations claiming version 1 must be refused")
	}
	if !strings.Contains(err.Error(), "unique") {
		t.Errorf("the error should explain that versions are registry-wide and unique: %v", err)
	}
}

func TestEmptyMigrationDirIsRefused(t *testing.T) {
	// A binary built without its migrations must fail at startup, not silently
	// run against an empty schema.
	_, err := db.LoadMigrations(fstest.MapFS{
		"sql/readme.txt": &fstest.MapFile{Data: []byte("not sql")},
	}, "sql")
	if err == nil {
		t.Fatal("a migration directory with no .sql files must be refused")
	}
}
