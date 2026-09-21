package db

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// ‼️ Two test runs against one database must not share a schema name.
//
// # What went wrong
//
// Every integration harness in this repository named its schema after the test:
// "forge_geo_" + t.Name(). It then did `drop schema if exists <name> cascade`
// before creating it, which is right for a re-run and catastrophic for a
// neighbour. Several worktrees of this repository are checked out on the same
// machine and share one Postgres, so `go test ./...` in two of them runs the
// same test name at the same time: the second run drops the first run's schema
// out from under it, mid-transaction. What that looks like from inside the first
// run is "relation does not exist" on a table its own migration just created —
// a failure that is not reproducible, points at the wrong code, and has cost
// this project several rounds of investigation.
//
// # The rule
//
// A schema name is <prefix><test name>_<run id>, where the run id is random and
// is fixed for the lifetime of THIS PROCESS. Per process rather than per test so
// that:
//
//   - a package's harnesses, subtests and helpers all agree on one name without
//     passing anything around;
//   - a run's schemas are recognisable as one run's when they have to be cleaned
//     up by hand;
//   - re-running a single test still reuses that run's own name, so the
//     `drop ... if exists` at the start still does its job, which is to clear a
//     schema left by a previous run of THE SAME process lineage.
//
// Go runs one test binary per package, so "per process" is per package per run:
// two packages in the same `go test ./...` get different ids, which is harmless,
// and two worktrees never collide, which is the point.
//
// # What this costs
//
// A run that is killed (^C, a crashed process, a timeout) leaves its schemas
// behind, where before it left one schema per test name that the next run would
// reuse. They are small and empty of interest; `make db-clean-test-schemas`
// removes every one of them. This is the trade the fence in
// migrate_integration_test.go accepts on purpose: junk that can be swept is
// better than a run that fails somebody else's.
//
// Fence: TestUniqueSchema_TwoRunsDoNotSeeEachOthersTables.

// runID is this process's share of every schema name it creates.
var runID = newRunID()

func newRunID() string {
	var b [5]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	// crypto/rand does not fail on any platform this runs on, and if it ever
	// does, a clock-derived id is still better than the shared name that caused
	// the problem. Never a constant fallback: that would silently restore the
	// collision on the one machine where it failed.
	return fmt.Sprintf("%010x", uint64(time.Now().UnixNano())&0xffffffffff)
}

// RunID identifies this test process's schemas.
//
// Exported so a harness can print it: "which run left this schema behind?" is
// otherwise unanswerable, and the id is the only thing in the name that is not
// the test's.
func RunID() string { return runID }

// UniqueSchema names a schema for one test in this process.
//
// prefix is the package's own short tag, ending in an underscore
// ("forge_geo_"); name is the test's name, or anything else that distinguishes
// one schema in this process from another.
func UniqueSchema(prefix, name string) string { return schemaWithRun(prefix, name, runID) }

// maxSchemaName is Postgres's identifier limit. A longer name is TRUNCATED by
// the server rather than refused, which would quietly put two tests back in one
// schema — the failure this file exists to prevent, arriving by another door.
const maxSchemaName = 63

// schemaWithRun is UniqueSchema with the run id given, so the fence can hold two
// runs side by side in one process.
func schemaWithRun(prefix, name, run string) string {
	head := strings.ToLower(prefix + name)
	var b strings.Builder
	for _, r := range head {
		switch {
		case ('a' <= r && r <= 'z') || ('0' <= r && r <= '9') || r == '_':
			b.WriteRune(r)
		default:
			// '/' from a subtest, '-' from a branch name, a space from a test
			// name written in prose. All of them would need quoting in the DDL
			// these harnesses build by concatenation.
			b.WriteByte('_')
		}
	}
	head = b.String()
	// A name may not START with a digit unquoted, and "forge_" prefixes mean
	// this never happens in practice — but a caller passing an empty prefix
	// would get one, and the failure would be a syntax error in generated DDL.
	if head == "" || ('0' <= head[0] && head[0] <= '9') {
		head = "forge_test_" + head
	}
	// The run id is what makes the name unique, so the TRUNCATION takes it off
	// the head and never off the suffix. Truncating the finished name is the
	// bug this comment exists to stop somebody reintroducing: it would shorten
	// two long, different test names down to the same string again.
	suffix := "_" + run
	if len(head)+len(suffix) > maxSchemaName {
		head = head[:maxSchemaName-len(suffix)]
	}
	return head + suffix
}

// DropTestSchema removes a schema a test run created.
//
// It opens a connection of its own, because it is called from a t.Cleanup that
// has usually just closed the pool it would otherwise have used, and it reports
// nothing: a cleanup that fails the test it is cleaning up after turns one
// failure into two, and the schema it could not drop is swept by
// `make db-clean-test-schemas`.
//
// This became necessary with the run id. Before it, a harness that never dropped
// its schema left ONE schema per test name, which the next run reused; now every
// run's names are its own, so a harness that never drops leaks a schema per run
// until the disk notices.
func DropTestSchema(url, schema string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := Connect(ctx, config.DBConfig{
		URL: url, MaxConns: 1, MinConns: 0,
		MaxConnLifetime: time.Minute, MaxConnIdleTime: time.Minute,
		ConnectTimeout: 10 * time.Second,
	}, logx.Discard())
	if err != nil {
		return
	}
	defer pool.Close()
	_, _ = pool.Exec(ctx, "drop schema if exists "+schema+" cascade")
}
