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

// TestMigrationsRunInTwoIsolatedSchemas is the regression fence for the failure
// CI caught and a developer machine structurally cannot.
//
// The bug: 0001 ran `create extension if not exists citext` and 0002 declared
// email columns as citext. CREATE EXTENSION IF NOT EXISTS is evaluated
// DATABASE-wide but installs the type into ONE schema. With the suite creating
// several schemas concurrently, the first run created citext inside its own
// schema; every later run saw "already exists", did nothing, and then failed
// with `type "citext" does not exist`.
//
// Locally it never appeared, because an earlier `make migrate` had put citext
// in `public`, and `public` is on the test search path. CI's database was fresh.
//
// # Why this test migrates TWICE, into two different schemas
//
// A single isolated schema does not reproduce it: with the extension absent
// everywhere, that schema simply creates its own and succeeds. The defect only
// exists in the gap between the two — a database-scoped object created by the
// first run, needed but not re-creatable by the second.
//
// Running the chain into schema A and then into schema B, both with search_path
// excluding public, reproduces exactly that. It generalises beyond citext to any
// dependency on a database-wide object that one schema creates and another
// assumes.
//
// Verified: with citext restored, the second migration fails here.
func TestMigrationsRunInTwoIsolatedSchemas(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	url := os.Getenv("FORGE_TEST_DATABASE_URL")
	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}

	migrations, err := db.LoadMigrations(db.Files, db.MigrationsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) == 0 {
		t.Fatal("no migrations loaded; this fence would pass vacuously")
	}

	// Deliberately not derived from t.Name(): an earlier version of this test
	// used the test name as the schema name, and its own guard then matched the
	// word "public" inside it.
	for i, suffix := range []string{"iso_a", "iso_b"} {
		schema := "forge_test_" + suffix
		if _, err := pool.Exec(ctx, "drop schema if exists "+schema+" cascade"); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, "create schema "+schema); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), "drop schema if exists "+schema+" cascade")
		})

		// No ",public": anything the chain needs, the chain must create.
		scoped, err := db.Connect(ctx, config.DBConfig{
			URL: url + sep + "search_path=" + schema, MaxConns: 4, MinConns: 1,
			MaxConnLifetime: time.Hour, MaxConnIdleTime: time.Minute, ConnectTimeout: 10 * time.Second,
		}, logx.Discard())
		if err != nil {
			t.Fatalf("schema %s: connecting: %v", schema, err)
		}

		// Prove the isolation before trusting the result. Compare entries, not
		// substrings — a schema name can contain the word "public".
		var searchPath string
		if err := scoped.QueryRow(ctx, "show search_path").Scan(&searchPath); err != nil {
			t.Fatal(err)
		}
		for _, entry := range strings.Split(searchPath, ",") {
			if strings.Trim(strings.TrimSpace(entry), `"`) == "public" {
				t.Fatalf("search_path is %q and still includes public; this test cannot detect the bug it exists for", searchPath)
			}
		}

		if _, err := db.Migrate(ctx, scoped, migrations, logx.Discard()); err != nil {
			scoped.Close()
			t.Fatalf("migration run %d of 2 (schema %s) failed: %v\n\n"+
				"The chain depends on something outside its own schema. If run 1 succeeded and "+
				"run 2 failed, the dependency is a DATABASE-scoped object that the first run "+
				"created and the second could not re-create — a CREATE EXTENSION IF NOT EXISTS "+
				"behaves exactly this way. A developer database hides it because earlier runs "+
				"leave the object in public.", i+1, schema, err)
		}
		// Re-runnable under isolation too.
		if _, err := db.Migrate(ctx, scoped, migrations, logx.Discard()); err != nil {
			scoped.Close()
			t.Fatalf("schema %s: second pass failed: %v", schema, err)
		}
		scoped.Close()
	}
}

// TestNoExtensionsAreRequired states the resulting rule as a check rather than
// a comment. An extension is a database-wide object with a per-schema install
// location, which makes it the specific hazard this chain now avoids.
func TestNoExtensionsAreRequired(t *testing.T) {
	migrations, err := db.LoadMigrations(db.Files, db.MigrationsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) == 0 {
		t.Fatal("no migrations loaded; this fence would pass vacuously")
	}
	for _, m := range migrations {
		for _, line := range strings.Split(m.SQL, "\n") {
			trimmed := strings.TrimSpace(strings.ToLower(line))
			if strings.HasPrefix(trimmed, "--") {
				continue
			}
			if strings.Contains(trimmed, "create extension") {
				t.Errorf("%04d_%s creates an extension:\n  %s\n\n"+
					"CREATE EXTENSION IF NOT EXISTS is evaluated database-wide but installs into "+
					"ONE schema, so it silently does nothing when the extension already exists "+
					"elsewhere — leaving its types unresolvable. If an extension is genuinely "+
					"required, install it as a deployment step and schema-qualify every use.",
					m.Version, m.Name, strings.TrimSpace(line))
			}
		}
	}
}

// TestTwoSchemasGetTheSameObjects compares what the chain actually BUILT in two
// schemas, not merely that it reported success in both.
//
// # The bug this exists for
//
// TestMigrationsRunInTwoIsolatedSchemas asserts that the chain runs twice
// without error. It did — and the second schema was still missing every
// updated_at trigger and one foreign key, because the guards were written as:
//
//	if not exists (select 1 from pg_trigger where tgname = 'forge_x_updated_at')
//
// pg_trigger is a per-DATABASE catalogue. Once any schema holds a trigger by
// that name the guard is true everywhere, so every schema after the first
// silently gets none and the migration reports success.
//
// Production has one schema and was never affected. What was affected is this
// test harness, which builds a schema per test in a shared database: every
// integration test in this repository has been running against a schema whose
// triggers did not exist. Nothing went falsely green — the application clock
// sets updated_at explicitly on almost every path — but the fixture was not the
// production schema, which is the thing the harness claims to be.
//
// So the fence compares catalogues rather than exit codes. "It ran" was already
// checked and was already true.
//
// See docs/bugfix/2026-09-02-trigger-guards-were-not-schema-scoped.md.
func TestTwoSchemasGetTheSameObjects(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	url := os.Getenv("FORGE_TEST_DATABASE_URL")
	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	migrations, err := db.LoadMigrations(db.Files, db.MigrationsDir)
	if err != nil {
		t.Fatal(err)
	}

	// catalogue reads everything the chain is supposed to have created in one
	// schema, as comparable strings.
	catalogue := func(schema string) map[string][]string {
		out := map[string][]string{}
		for name, query := range map[string]string{
			"tables": `select c.relname from pg_class c join pg_namespace n on n.oid = c.relnamespace
			            where n.nspname = $1 and c.relkind = 'r' order by 1`,
			"triggers": `select c.relname || '.' || t.tgname from pg_trigger t
			              join pg_class c on c.oid = t.tgrelid
			              join pg_namespace n on n.oid = c.relnamespace
			             where n.nspname = $1 and not t.tgisinternal order by 1`,
			"constraints": `select c.relname || '.' || con.conname || ':' || con.contype::text from pg_constraint con
			                 join pg_class c on c.oid = con.conrelid
			                 join pg_namespace n on n.oid = c.relnamespace
			                where n.nspname = $1 order by 1`,
			"indexes": `select c.relname from pg_class c join pg_namespace n on n.oid = c.relnamespace
			             where n.nspname = $1 and c.relkind = 'i' order by 1`,
		} {
			rows, err := pool.Query(ctx, query, schema)
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var s string
				if err := rows.Scan(&s); err != nil {
					rows.Close()
					t.Fatal(err)
				}
				out[name] = append(out[name], s)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
		}
		return out
	}

	schemas := []string{"forge_test_same_a", "forge_test_same_b"}
	for _, schema := range schemas {
		if _, err := pool.Exec(ctx, "drop schema if exists "+schema+" cascade"); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, "create schema "+schema); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), "drop schema if exists "+schema+" cascade")
		})
		scoped, err := db.Connect(ctx, config.DBConfig{
			URL: url + sep + "search_path=" + schema, MaxConns: 4, MinConns: 1,
			MaxConnLifetime: time.Hour, MaxConnIdleTime: time.Minute, ConnectTimeout: 10 * time.Second,
		}, logx.Discard())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Migrate(ctx, scoped, migrations, logx.Discard()); err != nil {
			scoped.Close()
			t.Fatalf("schema %s: %v", schema, err)
		}
		scoped.Close()
	}

	first, second := catalogue(schemas[0]), catalogue(schemas[1])
	for _, what := range []string{"tables", "triggers", "constraints", "indexes"} {
		a, b := first[what], second[what]
		if len(a) == 0 {
			t.Fatalf("the first schema has no %s at all; this fence would pass vacuously", what)
		}
		if strings.Join(a, "\n") == strings.Join(b, "\n") {
			continue
		}
		// Name what is missing rather than dumping both lists: the failure is
		// always "the second schema is short of something", and the reader needs
		// to know which object and therefore which guard.
		have := map[string]bool{}
		for _, s := range b {
			have[s] = true
		}
		var missing []string
		for _, s := range a {
			if !have[s] {
				missing = append(missing, s)
			}
		}
		t.Fatalf("the same migration chain produced different %s in two schemas.\n"+
			"Missing from %s: %v\n\n"+
			"This is what a database-scoped guard looks like: an object created in the first "+
			"schema makes the guard true for every later one. Attach triggers with "+
			"`drop trigger if exists ... on <table>` followed by an unconditional create, "+
			"and constraints with `drop constraint if exists` followed by add.",
			what, schemas[1], missing)
	}
}

// TestEveryUpdatedAtColumnHasItsTrigger checks a freshly migrated schema against
// a RULE rather than against another schema.
//
// # Why this exists beside TestTwoSchemasGetTheSameObjects
//
// That test compares two schemas to each other, and a mutation drill showed it
// cannot catch the bug it was written for: both of its schemas are created after
// `public` already holds every trigger name, so a database-scoped guard skips
// the triggers in BOTH and the two compare equal. A fence that is blind in
// exactly the direction of the defect is not a fence.
//
// This one states the invariant instead: an `updated_at` column is a promise
// that the row records when it last changed, and the trigger is the only thing
// that keeps that promise. A table with the column and no trigger has a column
// that lies whenever anything updates the row without setting it by hand.
//
// It is checked in a fresh schema, so it fails whether that schema is the first
// in the database or the fiftieth.
//
// See docs/bugfix/2026-09-02-trigger-guards-were-not-schema-scoped.md.
func TestEveryUpdatedAtColumnHasItsTrigger(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	url := os.Getenv("FORGE_TEST_DATABASE_URL")
	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	migrations, err := db.LoadMigrations(db.Files, db.MigrationsDir)
	if err != nil {
		t.Fatal(err)
	}

	schema := "forge_test_updated_at_rule"
	if _, err := pool.Exec(ctx, "drop schema if exists "+schema+" cascade"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "create schema "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "drop schema if exists "+schema+" cascade") })

	scoped, err := db.Connect(ctx, config.DBConfig{
		URL: url + sep + "search_path=" + schema, MaxConns: 4, MinConns: 1,
		MaxConnLifetime: time.Hour, MaxConnIdleTime: time.Minute, ConnectTimeout: 10 * time.Second,
	}, logx.Discard())
	if err != nil {
		t.Fatal(err)
	}
	defer scoped.Close()
	if _, err := db.Migrate(ctx, scoped, migrations, logx.Discard()); err != nil {
		t.Fatal(err)
	}

	rows, err := pool.Query(ctx, `
		select c.relname,
		       (select count(*) from pg_trigger g
		         where g.tgrelid = c.oid and not g.tgisinternal
		           and g.tgname = c.relname || '_updated_at')
		  from pg_class c
		  join pg_namespace n on n.oid = c.relnamespace
		  join pg_attribute a on a.attrelid = c.oid and a.attname = 'updated_at' and a.attnum > 0
		 where n.nspname = $1 and c.relkind = 'r'
		 order by c.relname`, schema)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var checked int
	var missing []string
	for rows.Next() {
		var table string
		var triggers int
		if err := rows.Scan(&table, &triggers); err != nil {
			t.Fatal(err)
		}
		checked++
		if triggers == 0 {
			missing = append(missing, table)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	// Without this the test passes on an empty result set, which is precisely
	// how a fence over "every X" goes quietly vacuous.
	if checked == 0 {
		t.Fatal("no table in the migrated schema has an updated_at column; this fence would pass vacuously")
	}
	if len(missing) > 0 {
		t.Fatalf("%d of %d tables have an updated_at column and nothing to maintain it: %v\n\n"+
			"The column is a promise that the row records when it last changed. Attach the trigger with\n"+
			"  drop trigger if exists <table>_updated_at on <table>;\n"+
			"  create trigger <table>_updated_at before update on <table> for each row execute function forge_set_updated_at();\n"+
			"Do NOT guard on `select from pg_trigger where tgname = ...`: that catalogue is per DATABASE, "+
			"so the guard is true for every schema after the first one.",
			len(missing), checked, missing)
	}
}
