// Command forgectl is FORGE's operator CLI.
//
// It exists so that every operational action — migrating, checking health,
// inspecting configuration — is a repeatable command rather than a snippet
// someone pastes into a terminal. Anything an operator must do at 3am belongs
// here, with a --dry-run where the action is expensive to reverse.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

const usage = `forgectl — FORGE operator CLI

Usage:
  forgectl <command> [flags]

Commands:
  migrate           Apply the schema migration chain (idempotent; safe to re-run)
  migrate --dry-run List the migrations that would run, without touching the database
  health            Check database connectivity and report latency
  config            Print the effective configuration with secrets redacted
  version           Print build information

Configuration is read from the environment. See .env.example for every variable,
its default, and what breaks when it is wrong.
`

// build metadata, injected at link time by the release script.
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1], os.Args[2:]); err != nil {
		// Operator-facing failure: print the code, the cause, and the remedy.
		// A CLI that prints only a stack trace makes the reader guess.
		fmt.Fprintln(os.Stderr, "\nforgectl failed.")
		code := errs.CodeOf(err)
		fmt.Fprintf(os.Stderr, "  error : %v\n", err)
		if d, ok := errs.Lookup(code); ok {
			fmt.Fprintf(os.Stderr, "  code  : %s (%s)\n", d.Code, d.Category)
			fmt.Fprintf(os.Stderr, "  cause : %s\n", d.Cause)
			fmt.Fprintf(os.Stderr, "  fix   : %s\n", d.Remedy)
		}
		os.Exit(1)
	}
}

func run(ctx context.Context, cmd string, args []string) error {
	if cmd == "version" {
		fmt.Printf("forgectl %s (commit %s, built %s)\n", version, commit, date)
		return nil
	}

	cfg, warnings, err := config.Load()
	if err != nil {
		return err
	}
	log := logx.New(logx.Options{
		Level:   parseLevel(cfg.Log.Level),
		Format:  cfg.Log.Format,
		Service: "forgectl",
	})
	for _, w := range warnings {
		log.Warn(ctx, logx.EventConfigDefault, "warning", w)
	}

	switch cmd {
	case "config":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(cfg.Redacted())

	case "migrate":
		dryRun := hasFlag(args, "--dry-run")
		ms, err := db.LoadMigrations(db.Files, db.MigrationsDir)
		if err != nil {
			return err
		}
		if dryRun {
			fmt.Printf("%d migration(s) in the chain (dry run — nothing applied):\n", len(ms))
			for _, m := range ms {
				mode := "tx"
				if !m.InTransaction {
					mode = "no-tx"
				}
				fmt.Printf("  %04d_%s [%s] sha256=%s\n", m.Version, m.Name, mode, m.Checksum[:12])
			}
			return nil
		}
		pool, err := db.Connect(ctx, cfg.DB, log)
		if err != nil {
			return err
		}
		defer pool.Close()
		results, err := db.Migrate(ctx, pool, ms, log)
		if err != nil {
			return err
		}
		fmt.Print(db.FormatResults(results))
		return nil

	case "health":
		pool, err := db.Connect(ctx, cfg.DB, log)
		if err != nil {
			return err
		}
		defer pool.Close()
		latency, err := db.HealthCheck(ctx, pool, 5*time.Second)
		if err != nil {
			return err
		}
		fmt.Printf("database  ok  (%s)\n", latency.Round(time.Microsecond))
		stats, _ := json.MarshalIndent(db.Stat(pool), "", "  ")
		fmt.Printf("pool      %s\n", stats)
		return nil

	default:
		fmt.Fprint(os.Stderr, usage)
		return errs.New("forgectl.run", errs.CodeValidationFailed).
			WithDetail("unknown command %q", cmd)
	}
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func parseLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
