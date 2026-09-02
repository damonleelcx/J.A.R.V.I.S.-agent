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
	"flag"
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

Operations:
  migrate             Apply the schema migration chain (idempotent; safe to re-run)
  migrate --dry-run   List the migrations that would run, without touching the database
  health              Check database connectivity and report latency
  config              Print the effective configuration with secrets redacted
  version             Print build information

Goals:
  goal new            Plan a goal. Writes a draft plan; does not start it.
      --title, --statement, --owner   (required)
      --autonomy discuss|draft|sandbox_execute|approval_gated   (default sandbox_execute)
      --risk r0|r1|r2|r3|r4                                     (default r1)
      --project <id>      reuse an existing project
      --start             activate immediately instead of leaving it a draft
  goal start <id>     Activate a drafted goal so workers can claim its tasks
  goal show <id>      Current state, tasks, pending approvals, and the timeline

Approvals:
  approve <approval-id> --as you@example.com [--reason ...]
  reject  <approval-id> --as you@example.com [--reason ...]

Audit:
  audit verify <goal-id>   Check a goal's timeline against its hash chain
  audit verify --all       Check every goal
      --quiet              Print nothing on success; the exit status is the answer

  Exit status is 0 when every chain holds and 1 when one does not, so this
  belongs in cron and in the release checklist. It is tamper-EVIDENT, not
  tamper-proof: it catches silent edits, not an attacker who owns the database.

Planning and starting are separate steps on purpose: the plan is worth reading
before any work happens, and a command that plans-and-runs gives nobody the
chance. Use --start when you have already decided.

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

	// Load .env if present, without overriding anything already exported.
	// Missing file is normal: production sets real environment variables.
	if err := config.LoadDotEnv(".env"); err != nil {
		return errs.Wrap("forgectl.run", errs.CodeConfigInvalid, err).
			WithDetail(".env exists but could not be read")
	}

	// Each command declares only the configuration it actually needs. Running a
	// migration must not require an LLM API key — demanding one would turn a
	// routine database task into a credential request.
	cfg, warnings, err := config.Load(sectionsFor(cmd)...)
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
	case "goal":
		if len(args) == 0 {
			fmt.Fprint(os.Stderr, usage)
			return errs.New("forgectl.run", errs.CodeValidationFailed).
				WithDetail("goal needs a subcommand: new, start or show")
		}
		switch args[0] {
		case "new":
			return cmdGoalNew(ctx, cfg, log, args[1:])
		case "start":
			return cmdGoalStart(ctx, cfg, log, args[1:])
		case "show":
			return cmdGoalShow(ctx, cfg, log, args[1:])
		default:
			return errs.New("forgectl.run", errs.CodeValidationFailed).
				WithDetail("unknown goal subcommand %q; expected new, start or show", args[0])
		}

	case "audit":
		if len(args) == 0 {
			return errs.New("forgectl.run", errs.CodeValidationFailed).
				WithDetail("audit needs a subcommand: verify")
		}
		switch args[0] {
		case "verify":
			return cmdAuditVerify(ctx, cfg, log, args[1:])
		default:
			return errs.New("forgectl.run", errs.CodeValidationFailed).
				WithDetail("unknown audit subcommand %q; expected verify", args[0])
		}
	case "approve":
		return cmdApprove(ctx, cfg, log, args, true)
	case "reject":
		return cmdApprove(ctx, cfg, log, args, false)

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

// sectionsFor maps a subcommand to the configuration it requires.
func sectionsFor(cmd string) []config.Section {
	switch cmd {
	case "migrate", "health":
		return []config.Section{config.SectionDB}
	case "goal":
		// Planning calls a model; showing and starting do not. Requiring the LLM
		// section for all three keeps one rule rather than three, and an
		// operator running `goal show` already has the key in their .env.
		return []config.Section{config.SectionDB, config.SectionLLM, config.SectionEngine}
	case "approve", "reject":
		return []config.Section{config.SectionDB, config.SectionEngine}
	case "config":
		// `forgectl config` is a diagnostic: it must be able to print a partial
		// or broken configuration, which is exactly when someone runs it. So it
		// requires nothing and reports whatever is there.
		return []config.Section{config.SectionNone}
	default:
		return config.AllSections()
	}
}

// newFlagSet returns a flag set that reports errors through the command rather
// than calling os.Exit, so a usage mistake produces the same shaped output as
// every other failure.
func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	return fs
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
