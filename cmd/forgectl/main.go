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

Memory:
  memory layers                        What each layer is for, how long it lives, who sees it
  memory list --scope <layer> [--owner <id>]
                                       Everything held in a layer, forgotten rows included
  memory recall [--goal|--project|--user <id>] [--key|--prefix ...]
                                       Run the agent's own recall and show WHY each item came back
  memory forget <item-id> --as <user-id> [--reason ...]
                                       Delete an item on a user's behalf. The key stays claimed,
                                       so FORGE will not learn it again.
  memory purge <item-id> [--dry-run]   Remove a forgotten item and re-open its key. Deliberate.
  memory export --scope <layer> [--owner <id>]
                                       The layer as JSON, forgotten items included and marked
  memory sweep [--dry-run]             Reclaim expired rows. Reads already exclude them.

Decisions:
  decisions list --project <id> [--current]   The decision log, marking what was superseded
  decisions show <decision-id>                One decision with its whole supersession chain

Workspace model:
  graph kinds                          The node and edge vocabularies, and what may connect to what
  graph show --project <id> [--kind ...]
                                       A project's requirements, risks, components and how they relate
  graph review --project <id> [--quiet] [--gaps=false]
                                       What the graph CONTRADICTS (exit 1) and what it LACKS (exit 0).
                                       Gaps never affect the exit status: every project in progress has them.
  artifacts show <artifact-id>         One artifact's lifecycle: WRK-04's seven facts per version
  artifacts show <path> --project <id>

Geometry (variants and export):
  geometry formats                             What this build can and cannot write, and why not
  geometry list --project <id> [--limit n]     Variants a project has accumulated, newest first
  geometry show <version-id>                   One variant with everything VIS-04 makes it link to
  geometry compare <version-id> <version-id>...
                                               Side by side, with what differs — and what could not
                                               be compared, which is a separate answer
  geometry export <version-id> [--format obj|stl] [--out file] [--dry-run]
                                               Write a mesh. Prints what the conversion loses BEFORE
                                               it prints the path. Parametric formats (STEP, KCL,
                                               IGES) are declared and refused: there is no CAD kernel
                                               here, and a STEP file of tessellated facets would be
                                               treated downstream as an exact solid.

Access (RBAC):
  access matrix                                Who may do what, as a grid
  access members --project <id>                Who is in a project and how they got there
  access grant --project <id> --user <id> --role <role> --as <user-id>
  access revoke --project <id> --user <id> --as <user-id>
      Membership decides access; forge_projects.owner_id records only who created it.
      A project's last owner cannot be removed or demoted.

Collaboration:
  rooms show <room-id>                         A session's transcript, every turn attributed
  handoff <goal-id>                            State, actions, versions, approvals, evidence,
                                               open risks and recommended next work

Containment:
  secrets list --project <id> [--revoked]      Declared handles and which tools may use them
  secrets declare --project <id> --name <n> --env-var <VAR> [--description ...] --as <user-id>
  secrets grant <secret-id> --tool <name> --as <user-id>
  secrets revoke <secret-id> --as <user-id> [--reason ...]
      FORGE brokers secrets; it never stores a value. The declaration names the
      environment variable the value is read from at the moment a granted tool needs it.

  incidents list --project <id> [--open]       Incident records (PRD SAF-07)
  incidents show <incident-id>                 One incident with every action taken
  incidents open --project <id> --title ... --statement ... --as <user-id> [--goal <id>] [--severity ...]
  incidents preserve <incident-id> --as <user-id>
      Capture the state BEFORE anything destructive. Stop, revoke, quarantine and
      roll back are refused until this has happened.
  incidents act <incident-id> --kind <verb> --target <what> --as <user-id> [--outcome done|partial|failed|dry_run]
  incidents close <incident-id> --as <user-id> --review "..."

Evaluation (PRD §7 — how the MODEL behaves inside the harness):
  eval list                                    Every case, what it exists because of, and its floors
  eval run [--only a,b] [--repeats n] [--verbose] [--json report.json]
                                               Run the suite against a real model. Exit 1 if any
                                               scorer falls below its floor.
      Calls a real model: it costs money and takes minutes, and it is deliberately not part of
      "make test". Every scorer is deterministic Go — nothing here asks a model to grade a model.
      Rates are measurements, not guarantees: the same prompt has produced a correct standards
      figure and a fabricated one four runs apart.

Drills:
  drill list                                   What each recovery drill proves (PRD NFR-07)
  drill run [--only a,b] [--verbose] [--keep]  Inject real faults; exit 1 if any invariant broke
                                               OR if a drill disturbed nothing.

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

	case "memory":
		if len(args) == 0 {
			fmt.Fprint(os.Stderr, usage)
			return errs.New("forgectl.run", errs.CodeValidationFailed).
				WithDetail("memory needs a subcommand: layers, list, recall, forget, purge, export or sweep")
		}
		switch args[0] {
		case "layers":
			return cmdMemoryLayers()
		case "list":
			return cmdMemoryList(ctx, cfg, log, args[1:])
		case "recall":
			return cmdMemoryRecall(ctx, cfg, log, args[1:])
		case "forget":
			return cmdMemoryForget(ctx, cfg, log, args[1:])
		case "purge":
			return cmdMemoryPurge(ctx, cfg, log, args[1:])
		case "export":
			return cmdMemoryExport(ctx, cfg, log, args[1:])
		case "sweep":
			return cmdMemorySweep(ctx, cfg, log, args[1:])
		default:
			return errs.New("forgectl.run", errs.CodeValidationFailed).
				WithDetail("unknown memory subcommand %q; expected layers, list, recall, forget, purge, export or sweep", args[0])
		}

	case "decisions":
		if len(args) == 0 {
			return errs.New("forgectl.run", errs.CodeValidationFailed).
				WithDetail("decisions needs a subcommand: list or show")
		}
		switch args[0] {
		case "list":
			return cmdDecisions(ctx, cfg, log, args[1:])
		case "show":
			return cmdDecisionShow(ctx, cfg, log, args[1:])
		default:
			return errs.New("forgectl.run", errs.CodeValidationFailed).
				WithDetail("unknown decisions subcommand %q; expected list or show", args[0])
		}

	case "graph":
		if len(args) == 0 {
			fmt.Fprint(os.Stderr, usage)
			return errs.New("forgectl.run", errs.CodeValidationFailed).
				WithDetail("graph needs a subcommand: kinds, show or review")
		}
		switch args[0] {
		case "kinds":
			return cmdGraphKinds()
		case "show":
			return cmdGraphShow(ctx, cfg, log, args[1:])
		case "review":
			return cmdGraphReview(ctx, cfg, log, args[1:])
		default:
			return errs.New("forgectl.run", errs.CodeValidationFailed).
				WithDetail("unknown graph subcommand %q; expected kinds, show or review", args[0])
		}

	case "artifacts":
		if len(args) == 0 {
			return errs.New("forgectl.run", errs.CodeValidationFailed).
				WithDetail("artifacts needs a subcommand: show")
		}
		switch args[0] {
		case "show":
			return cmdArtifactHistory(ctx, cfg, log, args[1:])
		default:
			return errs.New("forgectl.run", errs.CodeValidationFailed).
				WithDetail("unknown artifacts subcommand %q; expected show", args[0])
		}

	case "geometry":
		if len(args) == 0 {
			fmt.Fprint(os.Stderr, usage)
			return errs.New("forgectl.run", errs.CodeValidationFailed).
				WithDetail("geometry needs a subcommand: formats, list, show, compare or export")
		}
		switch args[0] {
		case "formats":
			return cmdGeometryFormats()
		case "list":
			return cmdGeometryList(ctx, cfg, log, args[1:])
		case "show":
			return cmdGeometryShow(ctx, cfg, log, args[1:])
		case "compare":
			return cmdGeometryCompare(ctx, cfg, log, args[1:])
		case "export":
			return cmdGeometryExport(ctx, cfg, log, args[1:])
		default:
			return errs.New("forgectl.run", errs.CodeValidationFailed).
				WithDetail("unknown geometry subcommand %q; expected formats, list, show, compare or export", args[0])
		}

	case "access":
		if len(args) == 0 {
			fmt.Fprint(os.Stderr, usage)
			return errs.New("forgectl.run", errs.CodeValidationFailed).
				WithDetail("access needs a subcommand: matrix, members, grant or revoke")
		}
		switch args[0] {
		case "matrix":
			return cmdAccessMatrix()
		case "members":
			return cmdAccessMembers(ctx, cfg, log, args[1:])
		case "grant":
			return cmdAccessGrant(ctx, cfg, log, args[1:])
		case "revoke":
			return cmdAccessRevoke(ctx, cfg, log, args[1:])
		default:
			return errs.New("forgectl.run", errs.CodeValidationFailed).
				WithDetail("unknown access subcommand %q; expected matrix, members, grant or revoke", args[0])
		}

	case "rooms":
		if len(args) == 0 {
			return errs.New("forgectl.run", errs.CodeValidationFailed).
				WithDetail("rooms needs a subcommand: show")
		}
		if args[0] != "show" {
			return errs.New("forgectl.run", errs.CodeValidationFailed).
				WithDetail("unknown rooms subcommand %q; expected show", args[0])
		}
		return cmdRoomShow(ctx, cfg, log, args[1:])

	case "handoff":
		return cmdHandoff(ctx, cfg, log, args)

	case "secrets":
		if len(args) == 0 {
			fmt.Fprint(os.Stderr, usage)
			return errs.New("forgectl.run", errs.CodeValidationFailed).
				WithDetail("secrets needs a subcommand: list, declare, grant or revoke")
		}
		switch args[0] {
		case "list":
			return cmdSecretsList(ctx, cfg, log, args[1:])
		case "declare":
			return cmdSecretsDeclare(ctx, cfg, log, args[1:])
		case "grant":
			return cmdSecretsGrant(ctx, cfg, log, args[1:])
		case "revoke":
			return cmdSecretsRevoke(ctx, cfg, log, args[1:])
		default:
			return errs.New("forgectl.run", errs.CodeValidationFailed).
				WithDetail("unknown secrets subcommand %q; expected list, declare, grant or revoke", args[0])
		}

	case "incidents":
		if len(args) == 0 {
			fmt.Fprint(os.Stderr, usage)
			return errs.New("forgectl.run", errs.CodeValidationFailed).
				WithDetail("incidents needs a subcommand: list, show, open, preserve, act or close")
		}
		switch args[0] {
		case "list":
			return cmdIncidentsList(ctx, cfg, log, args[1:])
		case "show":
			return cmdIncidentShow(ctx, cfg, log, args[1:])
		case "open":
			return cmdIncidentOpen(ctx, cfg, log, args[1:])
		case "preserve":
			return cmdIncidentPreserve(ctx, cfg, log, args[1:])
		case "act":
			return cmdIncidentAct(ctx, cfg, log, args[1:])
		case "close":
			return cmdIncidentClose(ctx, cfg, log, args[1:])
		default:
			return errs.New("forgectl.run", errs.CodeValidationFailed).
				WithDetail("unknown incidents subcommand %q; expected list, show, open, preserve, act or close", args[0])
		}

	case "eval":
		if len(args) == 0 {
			fmt.Fprint(os.Stderr, usage)
			return errs.New("forgectl.run", errs.CodeValidationFailed).
				WithDetail("eval needs a subcommand: list or run")
		}
		switch args[0] {
		case "list":
			return cmdEvalList()
		case "run":
			return cmdEvalRun(ctx, cfg, log, args[1:])
		default:
			return errs.New("forgectl.run", errs.CodeValidationFailed).
				WithDetail("unknown eval subcommand %q; expected list or run", args[0])
		}

	case "drill":
		if len(args) == 0 {
			return errs.New("forgectl.run", errs.CodeValidationFailed).
				WithDetail("drill needs a subcommand: list or run")
		}
		switch args[0] {
		case "list":
			return cmdDrillList()
		case "run":
			return cmdDrillRun(ctx, cfg, log, args[1:])
		default:
			return errs.New("forgectl.run", errs.CodeValidationFailed).
				WithDetail("unknown drill subcommand %q; expected list or run", args[0])
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
	case "access", "rooms", "handoff":
		// Access, transcripts and handoffs are read straight from the database.
		// Requiring an LLM key to see who has access to a project would turn an
		// audit into a credential request.
		return []config.Section{config.SectionDB}
	case "secrets", "incidents":
		// Containment is read and written straight from the database. Requiring
		// an LLM key to revoke a credential during an incident would be the
		// worst possible moment to ask for one.
		return []config.Section{config.SectionDB}
	case "eval":
		// Evaluation calls a real model and touches no database. Requiring a
		// database URL to measure how the model behaves would make the suite
		// unrunnable in exactly the place it is most useful — a laptop, against
		// a candidate model, before anything is deployed.
		return []config.Section{config.SectionLLM}
	case "drill":
		// Drills build their own schemas from the migration chain and need no
		// model. Requiring an LLM key to prove the system degrades safely would
		// make the check harder to run than the thing it checks.
		return []config.Section{config.SectionDB}
	case "geometry":
		// Variants are read straight from the database, and export is pure
		// computation over what is already stored. Requiring an LLM key to look
		// at a shape FORGE already proposed would turn an inspection into a
		// credential request.
		return []config.Section{config.SectionDB}
	case "graph", "artifacts":
		// The workspace model is read straight from the database. Requiring an
		// LLM key to look at a project's requirements would turn an inspection
		// into a credential request.
		return []config.Section{config.SectionDB}
	case "memory", "decisions":
		// Memory and the decision log are read and written straight from the
		// database. Requiring an LLM key to look at what FORGE remembers would
		// turn an inspection into a credential request.
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
