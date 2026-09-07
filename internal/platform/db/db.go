// Package db owns Postgres connectivity and schema migration.
package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Pool is the process-wide Postgres connection pool.
type Pool = pgxpool.Pool

// Querier is the subset of pgx used by repositories.
//
// Why an interface: every repository method must be callable both on the pool
// (autocommit) and inside a transaction, without duplicating the method. A
// durable engine claims a job, writes a checkpoint, and appends a timeline
// event as one atomic unit — if those were three separate autocommit writes,
// a crash between them would leave state the recovery path cannot interpret.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Connect opens and verifies a pool. It fails rather than returning a pool that
// has never successfully talked to the database: a lazily-failing pool turns a
// misconfiguration into a mystery at first request instead of at startup.
func Connect(ctx context.Context, cfg config.DBConfig, log *logx.Logger) (*Pool, error) {
	const op = "db.Connect"

	pcfg, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeConfigInvalid, err).
			WithDetail("FORGE_DATABASE_URL is not a valid Postgres connection string")
	}
	pcfg.MaxConns = cfg.MaxConns
	pcfg.MinConns = cfg.MinConns
	pcfg.MaxConnLifetime = cfg.MaxConnLifetime
	pcfg.MaxConnIdleTime = cfg.MaxConnIdleTime
	pcfg.ConnConfig.ConnectTimeout = cfg.ConnectTimeout
	// UTC everywhere. A session in local time would silently store shifted
	// timestamps, and every lease and expiry decision in this system is a
	// timestamp comparison.
	pcfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	pcfg.ConnConfig.RuntimeParams["application_name"] = "forge"

	log.Info(ctx, logx.EventDBConnecting, "max_conns", cfg.MaxConns)

	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		log.ErrorWith(ctx, logx.EventDBConnectFailed, err)
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		log.ErrorWith(ctx, logx.EventDBConnectFailed, err)
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err).
			WithDetail("connected to the pool but the first ping failed")
	}

	var serverVersion string
	if err := pool.QueryRow(pingCtx, "select current_setting('server_version')").Scan(&serverVersion); err != nil {
		// Non-fatal: we have a working connection, we just could not label it.
		// Warned rather than swallowed, per the logging convention.
		log.WarnWith(ctx, logx.EventDBConnected, err, "detail", "connected but server_version could not be read")
	}
	log.Info(ctx, logx.EventDBConnected, "server_version", serverVersion)
	return pool, nil
}

// WaitForConnect is Connect, retrying while nothing is answering yet.
//
// # Why this is not simply what Connect does
//
// Connect is fail-fast on purpose, and around thirty call sites depend on that.
// Every forgectl subcommand is a person waiting at a terminal, where "the
// database is down" has to be a sentence within a second rather than a command
// that appears to hang; and the recovery drills probe connectivity with a
// deliberate failure they need answered immediately. So waiting is opt-in, and
// the process that wants it asks for it.
//
// # The defect this exists for
//
// forge-worker connects at boot and exited 1 when the database was not up yet,
// leaving Kubernetes to restart it. Observed in production on 2026-09-07: the
// worker lost a race with postgres by roughly six hundred milliseconds, died,
// and came back healthy on the restart one second later.
//
// Nothing was broken by that — the restart is the recovery — but it is the
// wrong recovery. CrashLoopBackOff is exponential and reaches minutes, so a
// database that is one second late can cost far more than a second; the restart
// counter climbs for a non-event, which is noise in the one signal an operator
// uses to spot a real crash loop; and the pod's logs begin with a stack of
// failures that read like an outage.
//
// # What it retries, and what it refuses to
//
// A retry loop that swallows a wrong password turns a five-second
// misconfiguration into a process that never starts and never says why — which
// is a worse failure than the crash it replaces, because the crash at least
// said what was wrong. So the discriminator is whether ANYTHING answered:
//
//   - Nothing answered. Connection refused, DNS failure, timeout. The database
//     may simply not be up yet, which is the whole case this exists for. Retry.
//   - Postgres answered and refused. A bad password (28P01), no such database
//     (3D000), too many connections (53300). It will refuse identically in ten
//     minutes, so waiting only delays the report. Return now.
//   - Postgres answered "the database system is starting up" (57P03). The one
//     server-side answer that means "not yet" rather than "no". Retry.
//   - The connection string does not parse. There is nothing to wait for.
//
// Each retry is logged, so a slow start is visible as a slow start rather than
// as a process that sat silent and then worked.
//
// The line above is held by TestWaitForConnectDoesNotWaitOutARefusal. If that
// goes red, credential mistakes have started presenting as a boot that hangs
// with no explanation — do not relax it, fix worthWaitingFor.
func WaitForConnect(ctx context.Context, cfg config.DBConfig, log *logx.Logger, limit time.Duration) (*Pool, error) {
	const (
		firstDelay = 250 * time.Millisecond
		maxDelay   = 5 * time.Second
	)
	deadline := time.Now().Add(limit)
	delay := firstDelay

	for attempt := 1; ; attempt++ {
		pool, err := Connect(ctx, cfg, log)
		if err == nil {
			return pool, nil
		}
		if !worthWaitingFor(err) {
			return nil, err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 || ctx.Err() != nil {
			return nil, err
		}
		// Never sleep past the deadline: the caller asked to wait `limit`, and
		// overshooting it by most of a backoff step is a different promise.
		sleep := delay
		if sleep > remaining {
			sleep = remaining
		}
		log.WarnWith(ctx, logx.EventDBConnectRetry, err,
			"attempt", attempt,
			"retry_in", sleep.String(),
			"giving_up_in", remaining.Round(time.Second).String())
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(sleep):
		}
		if delay < maxDelay {
			delay *= 2
		}
	}
}

// worthWaitingFor reports whether an error might resolve itself.
//
// See WaitForConnect for why the line is drawn at "did anything answer".
func worthWaitingFor(err error) bool {
	// A connection string that does not parse will not start parsing.
	if errs.Is(err, errs.CodeConfigInvalid) {
		return false
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		// 57P03 is cannot_connect_now — postgres is up but still starting, or
		// is in recovery. Written as the literal SQLSTATE rather than pulling in
		// jackc/pgerrcode for one constant.
		return pg.Code == "57P03"
	}
	// Nothing answered: dial error, DNS failure, timeout.
	return true
}

// InTx runs fn inside a transaction, committing on success and rolling back on
// error or panic.
//
// The panic path matters: a panic mid-transaction that left the connection with
// an open transaction would poison that pooled connection for every later user.
func InTx(ctx context.Context, pool *Pool, fn func(tx pgx.Tx) error) (err error) {
	const op = "db.InTx"

	tx, beginErr := pool.Begin(ctx)
	if beginErr != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, beginErr)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
			panic(p)
		}
		if err != nil {
			// WithoutCancel: if the caller's context was cancelled, the rollback
			// itself would fail too, leaving the transaction to time out on the
			// server and hold its locks.
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()

	if err = fn(tx); err != nil {
		return err
	}
	if commitErr := tx.Commit(ctx); commitErr != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, commitErr).WithDetail("commit failed")
	}
	return nil
}

// HealthCheck reports whether the database is reachable and responsive.
//
// Why it returns latency: a health endpoint that answers only yes/no cannot
// distinguish "healthy" from "answering in four seconds", and the second one is
// what precedes an outage.
func HealthCheck(ctx context.Context, pool *Pool, timeout time.Duration) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	var one int
	if err := pool.QueryRow(ctx, "select 1").Scan(&one); err != nil {
		return time.Since(start), errs.Wrap("db.HealthCheck", errs.CodeDatabaseUnavail, err)
	}
	if one != 1 {
		return time.Since(start), errs.New("db.HealthCheck", errs.CodeStateCorrupt).
			WithDetail("select 1 returned %d", one)
	}
	return time.Since(start), nil
}

// Stat returns pool statistics for the observability surface.
func Stat(pool *Pool) map[string]any {
	s := pool.Stat()
	return map[string]any{
		"acquired_conns":     s.AcquiredConns(),
		"idle_conns":         s.IdleConns(),
		"total_conns":        s.TotalConns(),
		"max_conns":          s.MaxConns(),
		"acquire_count":      s.AcquireCount(),
		"acquire_duration":   s.AcquireDuration().String(),
		"canceled_acquires":  s.CanceledAcquireCount(),
		"empty_acquire_wait": s.EmptyAcquireCount(),
	}
}

// ErrNoRows re-exports pgx.ErrNoRows so repositories need not import pgx merely
// to check for absence.
var ErrNoRows = pgx.ErrNoRows

// IsNoRows reports whether err means "the row does not exist".
func IsNoRows(err error) bool {
	return err == pgx.ErrNoRows || fmt.Sprint(err) == pgx.ErrNoRows.Error()
}
