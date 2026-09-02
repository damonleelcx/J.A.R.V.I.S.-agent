// Package db owns Postgres connectivity and schema migration.
package db

import (
	"context"
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
