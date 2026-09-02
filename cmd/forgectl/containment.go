package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/incident"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/secrets"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// The operator's view of containment (PRD SEC-03, SAF-07).
//
// Both live here rather than on the HTTP surface because both are operator acts:
// declaring a handle is paired with exporting a variable where the service
// starts, and responding to an incident is done by whoever is holding the pager.

func brokerFor(ctx context.Context, cfg *config.Config, log *logx.Logger) (*secrets.Broker, *db.Pool, error) {
	pool, err := db.Connect(ctx, cfg.DB, log)
	if err != nil {
		return nil, nil, err
	}
	return secrets.NewBroker(pool, secrets.EnvLookup{}, clock.System{}, log), pool, nil
}

func incidentsFor(ctx context.Context, cfg *config.Config, log *logx.Logger) (*incident.Service, *db.Pool, error) {
	pool, err := db.Connect(ctx, cfg.DB, log)
	if err != nil {
		return nil, nil, err
	}
	return incident.NewService(pool, clock.System{}, log), pool, nil
}

// cmdSecretsList shows a project's handles, which tools may use them, and
// whether the variable behind each one is actually set in THIS process.
//
// The last column is the one that matters: a handle whose variable is missing
// looks perfectly healthy in the database and fails the first time an agent
// reaches for it. Checking it here turns that into something an operator finds
// before a run does.
func cmdSecretsList(ctx context.Context, cfg *config.Config, log *logx.Logger, args []string) error {
	const op = "forgectl.cmdSecretsList"

	fs := newFlagSet("secrets list")
	project := fs.String("project", "", "project id (required)")
	revoked := fs.Bool("revoked", false, "include revoked handles")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *project == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("usage: forgectl secrets list --project <id> [--revoked]")
	}

	broker, pool, err := brokerFor(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	list, err := broker.List(ctx, *project, *revoked)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Println("this project declares no secrets")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tHANDLE\tREADS\tSET HERE?\tUSABLE BY\tSTATE")
	for i := range list {
		s := list[i]
		set := "no — nothing would resolve"
		if _, ok := os.LookupEnv(s.EnvVar); ok {
			set = "yes"
		}
		state := "live"
		if s.Revoked() {
			state = "revoked " + s.RevokedAt.UTC().Format("2006-01-02")
			set = "—"
		}
		usable := "nothing (no grants)"
		if len(s.Tools) > 0 {
			usable = strings.Join(s.Tools, ", ")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", s.ID, s.Handle(), s.EnvVar, set, usable, state)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Println("\nFORGE stores no values. The model sees only the handle; the value is read from the")
	fmt.Println("named variable at the moment a granted tool needs it, and is redacted out of that")
	fmt.Println("tool's output before the model or the ledger sees the result.")
	return nil
}

// cmdSecretsDeclare registers a handle.
func cmdSecretsDeclare(ctx context.Context, cfg *config.Config, log *logx.Logger, args []string) error {
	const op = "forgectl.cmdSecretsDeclare"

	fs := newFlagSet("secrets declare")
	project := fs.String("project", "", "project id (required)")
	name := fs.String("name", "", "handle name, lowercase (required)")
	envVar := fs.String("env-var", "", "the environment variable the value is read from (required)")
	description := fs.String("description", "", "what it is for; shown to the model beside the handle")
	as := fs.String("as", "", "the user id declaring it (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *project == "" || *name == "" || *envVar == "" || *as == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("usage: forgectl secrets declare --project <id> --name <n> --env-var <VAR> --as <user-id> [--description ...]")
	}

	broker, pool, err := brokerFor(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	s, err := broker.Declare(ctx, &secrets.Secret{
		ProjectID: *project, Name: *name, EnvVar: *envVar,
		Description: *description, CreatedBy: *as,
	})
	if err != nil {
		return err
	}
	fmt.Printf("declared %s  (%s)\n", s.Handle(), s.ID)
	if _, set := os.LookupEnv(*envVar); !set {
		// Said now rather than at the first failed run.
		fmt.Printf("\n%s is not set in this process. Nothing will resolve until it is exported\n", *envVar)
		fmt.Println("where the service starts — FORGE reads it there, it does not store the value.")
	}
	fmt.Println("\nNo tool may use it yet. Grant one:")
	fmt.Printf("  forgectl secrets grant %s --tool <tool-name> --as %s\n", s.ID, *as)
	return nil
}

// cmdSecretsGrant permits one tool to receive a secret.
func cmdSecretsGrant(ctx context.Context, cfg *config.Config, log *logx.Logger, args []string) error {
	const op = "forgectl.cmdSecretsGrant"

	fs := newFlagSet("secrets grant")
	tool := fs.String("tool", "", "tool name (required)")
	as := fs.String("as", "", "the user id granting it (required)")
	if len(args) < 1 {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("usage: forgectl secrets grant <secret-id> --tool <name> --as <user-id>")
	}
	secretID := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *tool == "" || *as == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("--tool and --as are both required: a grant names a tool and the person who made it")
	}

	broker, pool, err := brokerFor(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := broker.Grant(ctx, secretID, *tool, *as); err != nil {
		return err
	}
	fmt.Printf("%s may now receive %s\n", *tool, secretID)
	return nil
}

// cmdSecretsRevoke withdraws a secret from every tool at once — one of SAF-07's
// seven verbs, reachable without opening an incident because sometimes the right
// first move is to turn it off.
func cmdSecretsRevoke(ctx context.Context, cfg *config.Config, log *logx.Logger, args []string) error {
	const op = "forgectl.cmdSecretsRevoke"

	fs := newFlagSet("secrets revoke")
	as := fs.String("as", "", "the user id revoking it (required)")
	reason := fs.String("reason", "", "why")
	if len(args) < 1 {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("usage: forgectl secrets revoke <secret-id> --as <user-id> [--reason ...]")
	}
	secretID := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *as == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("--as is required: a revocation must name the person who made it")
	}

	broker, pool, err := brokerFor(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := broker.Revoke(ctx, secretID, *as, *reason); err != nil {
		return err
	}
	fmt.Printf("revoked %s\n", secretID)
	fmt.Println("The declaration stays so that \"when did this stop being usable, and who stopped it\"")
	fmt.Println("has an answer. Rotate the credential itself where it actually lives.")
	return nil
}

// ---------------------------------------------------------------------------
// Incidents
// ---------------------------------------------------------------------------

func cmdIncidentsList(ctx context.Context, cfg *config.Config, log *logx.Logger, args []string) error {
	const op = "forgectl.cmdIncidentsList"

	fs := newFlagSet("incidents list")
	project := fs.String("project", "", "project id (required)")
	openOnly := fs.Bool("open", false, "only incidents that are not closed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *project == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("usage: forgectl incidents list --project <id> [--open]")
	}

	svc, pool, err := incidentsFor(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	list, err := svc.List(ctx, *project, *openOnly)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Println("no incidents recorded for this project")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tOPENED\tSEVERITY\tSTATUS\tTITLE")
	for _, i := range list {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			i.ID, i.OpenedAt.UTC().Format("2006-01-02"), i.Severity, i.Status, truncate(i.Title, 48))
	}
	return w.Flush()
}

func cmdIncidentShow(ctx context.Context, cfg *config.Config, log *logx.Logger, args []string) error {
	const op = "forgectl.cmdIncidentShow"

	if len(args) != 1 {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("usage: forgectl incidents show <incident-id>")
	}
	svc, pool, err := incidentsFor(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	inc, err := svc.Find(ctx, args[0])
	if err != nil {
		return err
	}
	fmt.Printf("%s  %s\n%s\n\n%s\n\n", inc.ID, inc.Title, inc.Summary(), inc.Statement)
	if len(inc.Actions) == 0 {
		fmt.Println("No actions taken yet. Nothing destructive is permitted until evidence is preserved:")
		fmt.Printf("  forgectl incidents preserve %s --as <user-id>\n", inc.ID)
		return nil
	}
	for _, a := range inc.Actions {
		fmt.Printf("  %2d  %-18s %-9s %s\n", a.Seq, a.Kind, a.Outcome, a.Target)
		fmt.Printf("      %s · %s\n", a.TakenAt.UTC().Format("2006-01-02 15:04"), a.TakenBy)
		if a.Detail != "" {
			fmt.Printf("      %s\n", indent(truncate(a.Detail, 600), "      "))
		}
	}
	if inc.Status != "closed" {
		fmt.Printf("\nClose it with a review:\n  forgectl incidents close %s --as <user-id> --review \"...\"\n", inc.ID)
	}
	return nil
}

func cmdIncidentOpen(ctx context.Context, cfg *config.Config, log *logx.Logger, args []string) error {
	const op = "forgectl.cmdIncidentOpen"

	fs := newFlagSet("incidents open")
	project := fs.String("project", "", "project id (required)")
	title := fs.String("title", "", "one line (required)")
	statement := fs.String("statement", "", "what happened, in your own words (required)")
	goal := fs.String("goal", "", "the goal it concerns, if there is one")
	severity := fs.String("severity", "medium", "low|medium|high|critical")
	as := fs.String("as", "", "the user id opening it (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *project == "" || *title == "" || *statement == "" || *as == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("usage: forgectl incidents open --project <id> --title \"...\" --statement \"...\" --as <user-id> [--goal <id>] [--severity ...]")
	}

	svc, pool, err := incidentsFor(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	in := &incident.Incident{
		ProjectID: *project, Title: *title, Statement: *statement,
		Severity: incident.Severity(*severity), OpenedBy: *as,
	}
	if *goal != "" {
		in.GoalID = goal
	}
	inc, err := svc.Open(ctx, in)
	if err != nil {
		return err
	}
	fmt.Printf("opened %s\n\n", inc.ID)
	fmt.Println("Preserve evidence before stopping, revoking, quarantining or rolling anything back —")
	fmt.Println("those are refused until you have. Evidence gathered afterwards is evidence of the response.")
	fmt.Printf("  forgectl incidents preserve %s --as %s\n", inc.ID, *as)
	return nil
}

func cmdIncidentPreserve(ctx context.Context, cfg *config.Config, log *logx.Logger, args []string) error {
	const op = "forgectl.cmdIncidentPreserve"

	fs := newFlagSet("incidents preserve")
	as := fs.String("as", "", "the user id capturing it (required)")
	if len(args) < 1 {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("usage: forgectl incidents preserve <incident-id> --as <user-id>")
	}
	incidentID := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *as == "" {
		return errs.New(op, errs.CodeValidationFailed).WithDetail("--as is required")
	}

	svc, pool, err := incidentsFor(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	action, ev, err := svc.PreserveEvidence(ctx, incidentID, *as)
	if err != nil {
		return err
	}
	fmt.Printf("captured as action %d (%s)\n\n%s\n", action.Seq, action.Outcome, action.Detail)
	if len(ev.Incomplete) > 0 {
		fmt.Printf("\nPART OF THE SYSTEM COULD NOT BE READ:\n")
		for _, w := range ev.Incomplete {
			fmt.Printf("  - %s\n", w)
		}
		fmt.Println("The snapshot is partial and is recorded as such.")
	}
	return nil
}

func cmdIncidentAct(ctx context.Context, cfg *config.Config, log *logx.Logger, args []string) error {
	const op = "forgectl.cmdIncidentAct"

	fs := newFlagSet("incidents act")
	kind := fs.String("kind", "", "stop|revoke|quarantine|roll_back|notify (required)")
	target := fs.String("target", "", "what it acted on (required for most verbs)")
	detail := fs.String("detail", "", "what was done")
	outcome := fs.String("outcome", "done", "done|partial|failed|dry_run")
	as := fs.String("as", "", "the user id who took it (required)")
	if len(args) < 1 {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("usage: forgectl incidents act <incident-id> --kind <verb> --target <what> --as <user-id> [--outcome ...]")
	}
	incidentID := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *kind == "" || *as == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("--kind and --as are required. The verbs are: %s", strings.Join(actionNames(), ", "))
	}

	svc, pool, err := incidentsFor(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	action, err := svc.Act(ctx, incidentID, &incident.Action{
		Kind: incident.ActionKind(*kind), Target: *target, Detail: *detail,
		Outcome: incident.Outcome(*outcome), TakenBy: *as,
	})
	if err != nil {
		return err
	}
	fmt.Printf("recorded action %d: %s %s (%s)\n", action.Seq, action.Kind, action.Target, action.Outcome)
	return nil
}

func cmdIncidentClose(ctx context.Context, cfg *config.Config, log *logx.Logger, args []string) error {
	const op = "forgectl.cmdIncidentClose"

	fs := newFlagSet("incidents close")
	as := fs.String("as", "", "the user id closing it (required)")
	review := fs.String("review", "", "what happened, what was done, what would prevent it (required)")
	if len(args) < 1 {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("usage: forgectl incidents close <incident-id> --as <user-id> --review \"...\"")
	}
	incidentID := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *as == "" {
		return errs.New(op, errs.CodeValidationFailed).WithDetail("--as is required")
	}

	svc, pool, err := incidentsFor(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := svc.Close(ctx, incidentID, *as, *review); err != nil {
		return err
	}
	fmt.Printf("closed %s\n", incidentID)
	return nil
}

func actionNames() []string {
	out := make([]string, 0, 7)
	for _, k := range incident.Actions() {
		out = append(out, string(k))
	}
	return out
}

func indent(s, prefix string) string {
	return strings.ReplaceAll(s, "\n", "\n"+prefix)
}
