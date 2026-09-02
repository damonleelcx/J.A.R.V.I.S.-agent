package db

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// advisoryLockKey serialises migration across processes. Two workers booting
// at once must not both try to create the same table; the loser waits.
// The value is arbitrary but must never change, or two versions of the binary
// would take different locks and stop excluding each other.
const advisoryLockKey int64 = 7_314_902_551_001

// noTransactionDirective opts a migration out of the per-file transaction.
// Needed for statements Postgres refuses inside one, principally
// CREATE INDEX CONCURRENTLY. Such a migration must be written so that a
// half-applied run is still safe to re-run, because nothing will roll it back.
const noTransactionDirective = "-- forge:no-transaction"

// Migration is one versioned schema change.
type Migration struct {
	Version int
	Name    string
	SQL     string
	// Checksum detects post-hoc edits to an already-applied migration.
	Checksum string
	// InTransaction is false when the file carries the no-transaction directive.
	InTransaction bool
}

// MigrationResult reports what one migration did on this run.
type MigrationResult struct {
	Migration Migration
	// FirstApply is true when this run recorded the migration for the first time.
	FirstApply bool
	Duration   time.Duration
}

// LoadMigrations reads and parses migrations from an embedded filesystem.
//
// File naming is <version>_<name>.sql with a zero-padded integer version, e.g.
// 0001_identity.sql. Versions must be unique and are applied in numeric order.
func LoadMigrations(fsys fs.FS, dir string) ([]Migration, error) {
	const op = "db.LoadMigrations"

	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeMigrationFailed, err).
			WithDetail("cannot read migrations directory %q", dir)
	}

	var out []Migration
	seen := map[int]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ".sql")
		parts := strings.SplitN(base, "_", 2)
		if len(parts) != 2 {
			return nil, errs.New(op, errs.CodeMigrationFailed).
				WithDetail("migration %q must be named <version>_<name>.sql", e.Name())
		}
		version, convErr := strconv.Atoi(parts[0])
		if convErr != nil {
			return nil, errs.Wrap(op, errs.CodeMigrationFailed, convErr).
				WithDetail("migration %q has a non-numeric version prefix", e.Name())
		}
		if prev, dup := seen[version]; dup {
			return nil, errs.New(op, errs.CodeMigrationFailed).
				WithDetail("version %d is claimed by both %q and %q; versions are registry-wide and must be unique", version, prev, e.Name())
		}
		seen[version] = e.Name()

		raw, readErr := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if readErr != nil {
			return nil, errs.Wrap(op, errs.CodeMigrationFailed, readErr).
				WithDetail("cannot read %q", e.Name())
		}
		sum := sha256.Sum256(raw)
		body := string(raw)
		out = append(out, Migration{
			Version:       version,
			Name:          parts[1],
			SQL:           body,
			Checksum:      hex.EncodeToString(sum[:]),
			InTransaction: !strings.Contains(body, noTransactionDirective),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	if len(out) == 0 {
		return nil, errs.New(op, errs.CodeMigrationFailed).
			WithDetail("no .sql files found in %q; the binary was built without its migrations", dir)
	}
	return out, nil
}

// Migrate applies every migration, in order, on every start.
//
// Why re-run everything instead of skipping applied versions:
//
//   - The schema in the database is the truth; the tracking table is a record,
//     not an authority. A tracking row that says "applied" when the DDL was
//     rolled back would permanently suppress a real migration, and that failure
//     is silent and unrecoverable without manual surgery.
//   - Requiring every migration to be idempotent, and then exercising that
//     property on every single boot, means idempotency cannot quietly rot. A
//     migration that is not re-runnable fails the first time anyone restarts,
//     not months later during an incident recovery.
//
// The cost is a few milliseconds of no-op DDL per boot, which is the cheapest
// insurance in this system.
func Migrate(ctx context.Context, pool *Pool, migrations []Migration, log *logx.Logger) ([]MigrationResult, error) {
	const op = "db.Migrate"

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err).WithDetail("cannot acquire a connection for migration")
	}
	defer conn.Release()

	// Serialise across processes. pg_advisory_lock blocks until granted and is
	// released when the session ends, so a crashed migrator cannot deadlock the
	// next one.
	if _, err := conn.Exec(ctx, "select pg_advisory_lock($1)", advisoryLockKey); err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err).WithDetail("cannot take the migration advisory lock")
	}
	defer func() {
		if _, unlockErr := conn.Exec(context.WithoutCancel(ctx), "select pg_advisory_unlock($1)", advisoryLockKey); unlockErr != nil {
			log.WarnWith(ctx, logx.EventMigrationFailed, unlockErr, "detail", "advisory unlock failed; it will release when the connection closes")
		}
	}()
	log.Info(ctx, logx.EventMigrationAdvisoryOK, "lock_key", advisoryLockKey)

	// The ledger is bootstrapped by hand because it is the one table that
	// cannot be created by a migration that needs it to exist.
	const createLedger = `
create table if not exists forge_schema_migrations (
    version      integer     primary key,
    name         text        not null,
    checksum     text        not null,
    first_applied_at timestamptz not null default now(),
    last_run_at  timestamptz not null default now(),
    run_count    bigint      not null default 1
)`
	if _, err := conn.Exec(ctx, createLedger); err != nil {
		return nil, errs.Wrap(op, errs.CodeMigrationFailed, err).WithDetail("cannot create the migration ledger")
	}

	results := make([]MigrationResult, 0, len(migrations))
	for _, m := range migrations {
		var knownChecksum string
		scanErr := conn.QueryRow(ctx,
			`select checksum from forge_schema_migrations where version = $1`, m.Version).Scan(&knownChecksum)
		known := scanErr == nil
		if scanErr != nil && !IsNoRows(scanErr) {
			return nil, errs.Wrap(op, errs.CodeMigrationFailed, scanErr).
				WithDetail("cannot read ledger row for version %d", m.Version)
		}

		// Checksum drift means someone edited a migration that has already run
		// somewhere. That is how two deployments silently diverge, so it is
		// surfaced loudly rather than tolerated. It is a warning, not a failure:
		// refusing to boot would strand an operator mid-incident, and the
		// idempotent re-run below still converges the schema.
		if known && knownChecksum != m.Checksum {
			log.Warn(ctx, logx.EventMigrationSkipped,
				"version", m.Version, "name", m.Name,
				"recorded_checksum", knownChecksum, "file_checksum", m.Checksum,
				"detail", "an already-applied migration file has changed since it was first applied; deployments may have diverged")
		}

		log.Debug(ctx, logx.EventMigrationApplying, "version", m.Version, "name", m.Name, "in_transaction", m.InTransaction)
		start := time.Now()

		if m.InTransaction {
			// Postgres DDL is transactional, so a failed migration leaves no
			// partial schema. This is strictly better than statement-by-statement
			// application, which can strand the schema half-changed.
			tx, txErr := conn.Begin(ctx)
			if txErr != nil {
				return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, txErr)
			}
			if _, execErr := tx.Exec(ctx, m.SQL); execErr != nil {
				_ = tx.Rollback(context.WithoutCancel(ctx))
				log.ErrorWith(ctx, logx.EventMigrationFailed, execErr, "version", m.Version, "name", m.Name)
				return nil, errs.Wrap(op, errs.CodeMigrationFailed, execErr).
					WithDetail("migration %04d_%s failed and was rolled back", m.Version, m.Name)
			}
			if commitErr := tx.Commit(ctx); commitErr != nil {
				return nil, errs.Wrap(op, errs.CodeMigrationFailed, commitErr).
					WithDetail("migration %04d_%s could not be committed", m.Version, m.Name)
			}
		} else {
			if _, execErr := conn.Exec(ctx, m.SQL); execErr != nil {
				log.ErrorWith(ctx, logx.EventMigrationFailed, execErr, "version", m.Version, "name", m.Name)
				return nil, errs.Wrap(op, errs.CodeMigrationFailed, execErr).
					WithDetail("migration %04d_%s failed outside a transaction and may be partially applied; it is written to be re-runnable, so retry after fixing the cause", m.Version, m.Name)
			}
		}

		elapsed := time.Since(start)
		if _, err := conn.Exec(ctx, `
            insert into forge_schema_migrations (version, name, checksum)
            values ($1, $2, $3)
            on conflict (version) do update
              set last_run_at = now(),
                  run_count   = forge_schema_migrations.run_count + 1,
                  checksum    = excluded.checksum,
                  name        = excluded.name`,
			m.Version, m.Name, m.Checksum); err != nil {
			return nil, errs.Wrap(op, errs.CodeMigrationFailed, err).
				WithDetail("migration %04d_%s applied but its ledger row could not be written", m.Version, m.Name)
		}

		results = append(results, MigrationResult{Migration: m, FirstApply: !known, Duration: elapsed})
		if !known {
			log.Info(ctx, logx.EventMigrationApplied,
				"version", m.Version, "name", m.Name, "duration_ms", elapsed.Milliseconds(), "first_apply", true)
		} else {
			log.Debug(ctx, logx.EventMigrationApplied,
				"version", m.Version, "name", m.Name, "duration_ms", elapsed.Milliseconds(), "first_apply", false)
		}
	}
	return results, nil
}

// MigrateFS is the convenience entry point used by the binaries.
func MigrateFS(ctx context.Context, pool *Pool, fsys embed.FS, dir string, log *logx.Logger) ([]MigrationResult, error) {
	ms, err := LoadMigrations(fsys, dir)
	if err != nil {
		return nil, err
	}
	return Migrate(ctx, pool, ms, log)
}

// FormatResults renders a human-readable migration summary for CLI output.
func FormatResults(rs []MigrationResult) string {
	var b strings.Builder
	firsts := 0
	for _, r := range rs {
		if r.FirstApply {
			firsts++
		}
	}
	fmt.Fprintf(&b, "%d migration(s) present, %d applied for the first time\n", len(rs), firsts)
	for _, r := range rs {
		marker := "  ="
		if r.FirstApply {
			marker = "  +"
		}
		fmt.Fprintf(&b, "%s %04d_%s (%dms)\n", marker, r.Migration.Version, r.Migration.Name, r.Duration.Milliseconds())
	}
	return b.String()
}
