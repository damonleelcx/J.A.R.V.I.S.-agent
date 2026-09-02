package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/memory"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// The operator's view of what FORGE remembers (PRD MEM-02).
//
// # Why these exist beside the HTTP surface rather than instead of it
//
// MEM-02 is a USER requirement — inspect, correct, pin, expire, export, delete —
// and users do those through the API. These are for the questions an operator
// asks from a shell: what is this deployment holding, why did it return that,
// and did the deletion somebody reported actually take. Like `audit verify`,
// they read the database directly with a binary the operator chose.

func memoryService(ctx context.Context, cfg *config.Config, log *logx.Logger) (*memory.Service, *db.Pool, error) {
	pool, err := db.Connect(ctx, cfg.DB, log)
	if err != nil {
		return nil, nil, err
	}
	return memory.NewService(pool, clock.System{}, log), pool, nil
}

// cmdMemoryLayers prints the layer table: what each layer is for, how long it
// lives, and who can read it.
//
// It takes no database, because the question it answers is about this build.
// "Which layer should this go in?" is asked while writing code, not while
// looking at rows.
func cmdMemoryLayers() error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "LAYER\tPRD NAME\tOWNED BY\tDEFAULT LIFETIME\tVISIBLE TO\tFOR")
	for _, l := range memory.Layers() {
		ttl := "never expires"
		if l.DefaultTTL > 0 {
			ttl = l.DefaultTTL.String()
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", l.Scope, l.PRDName, l.Owner, ttl, l.Visibility, l.Gloss)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Println("\nVisibility for `organisation` is declared, not enforced: there is no membership")
	fmt.Println("model yet (it arrives with SEC-02/COL-01). Personal and project scoping ARE enforced.")
	return nil
}

// cmdMemoryList shows what a layer holds for one owner, forgotten rows included.
func cmdMemoryList(ctx context.Context, cfg *config.Config, log *logx.Logger, args []string) error {
	const op = "forgectl.cmdMemoryList"

	fs := newFlagSet("memory list")
	scope := fs.String("scope", "", "layer to read: turn, session, project, user or organisation")
	owner := fs.String("owner", "", "the goal, project or user id the layer hangs off")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *scope == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("usage: forgectl memory list --scope <layer> [--owner <id>]\nRun `forgectl memory layers` to see the layers.")
	}

	svc, pool, err := memoryService(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	items, err := svc.Inspect(ctx, memory.Scope(*scope), *owner)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		fmt.Printf("nothing held in %s memory for %q\n", *scope, *owner)
		return nil
	}

	now := clock.System{}.Now()
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "KEY\tHOW\tSTATE\tSOURCE\tVALUE")
	for i := range items {
		it := items[i]
		state := "live"
		switch {
		case it.Forgotten():
			state = "forgotten " + it.ForgottenAt.UTC().Format("2006-01-02")
		case it.Pinned:
			state = "pinned"
		case it.Live(now) != nil:
			state = "expired"
		case it.ExpiresAt != nil:
			state = "expires " + it.ExpiresAt.UTC().Format("2006-01-02 15:04")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", it.Key, it.How, state, dash(it.Source), truncate(string(it.Value), 48))
	}
	return w.Flush()
}

// cmdMemoryRecall runs the same recall the agent runs, and prints why each item
// came back.
//
// This is the operator's answer to "why did FORGE think that?" — and it is the
// real retrieval path rather than a report about it, so what it prints is what
// the agent would have been given.
func cmdMemoryRecall(ctx context.Context, cfg *config.Config, log *logx.Logger, args []string) error {
	fs := newFlagSet("memory recall")
	goalID := fs.String("goal", "", "goal id, for turn and session memory")
	projectID := fs.String("project", "", "project id, for project memory")
	userID := fs.String("user", "", "user id, for personal memory")
	prefix := fs.String("prefix", "", "only keys starting with this")
	key := fs.String("key", "", "one exact key")
	limit := fs.Int("limit", 50, "maximum items per layer")
	if err := fs.Parse(args); err != nil {
		return err
	}

	svc, pool, err := memoryService(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	rc := memory.Recall{GoalID: *goalID, ProjectID: *projectID, UserID: *userID,
		Prefix: *prefix, Limit: *limit}
	if *key != "" {
		rc.Keys = []string{*key}
	}
	got, err := svc.Recall(ctx, rc)
	if err != nil {
		return err
	}
	if len(got) == 0 {
		fmt.Println("nothing recalled")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "KEY\tLAYER\tHOW\tWHY IT CAME BACK\tVALUE")
	for _, r := range got {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			r.Item.Key, r.Item.Scope, r.Item.How, r.Detail, truncate(string(r.Item.Value), 40))
	}
	return w.Flush()
}

// cmdMemoryForget deletes an item on a user's behalf, and makes it stick.
func cmdMemoryForget(ctx context.Context, cfg *config.Config, log *logx.Logger, args []string) error {
	const op = "forgectl.cmdMemoryForget"

	fs := newFlagSet("memory forget")
	as := fs.String("as", "", "the user id asking for the deletion (required)")
	reason := fs.String("reason", "", "why")
	// The id comes first and the flags after it, so the id is taken before
	// parsing: Go's flag package stops at the first non-flag argument, and
	// parsing the whole slice would silently ignore every flag that followed
	// the id — the command then refused for want of the --as it had been given.
	// Same shape as `forgectl approve` (cmd/forgectl/goal.go).
	// See docs/bugfix/2026-09-02-forgectl-memory-forget-ignored-its-flags.md.
	// No test covers this: nothing in the suite invokes the CLI's parsing.
	if len(args) < 1 {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("usage: forgectl memory forget <item-id> --as <user-id> [--reason ...]")
	}
	itemID := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *as == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("--as is required: a deletion has to name who asked, or it cannot be accounted for")
	}

	svc, pool, err := memoryService(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := svc.Forget(ctx, itemID, *as, *reason); err != nil {
		return err
	}
	fmt.Printf("forgotten: %s\n", itemID)
	fmt.Println("The key stays claimed, so FORGE will refuse to learn it again.")
	fmt.Println("Use `forgectl memory purge` to re-open it — that is deliberate and it is logged.")
	return nil
}

// cmdMemoryPurge removes a forgotten item outright, re-opening its key.
func cmdMemoryPurge(ctx context.Context, cfg *config.Config, log *logx.Logger, args []string) error {
	const op = "forgectl.cmdMemoryPurge"

	fs := newFlagSet("memory purge")
	dryRun := fs.Bool("dry-run", false, "show what would be purged and change nothing")
	// Positional first, then flags — same reason as memory forget above.
	if len(args) < 1 {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("usage: forgectl memory purge <item-id> [--dry-run]")
	}
	itemID := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	svc, pool, err := memoryService(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	item, err := svc.Repo().FindByID(ctx, pool, itemID)
	if err != nil {
		return err
	}
	if *dryRun {
		fmt.Printf("would purge %s (key %q in %s memory, forgotten %s)\n",
			item.ID, item.Key, item.Scope, forgottenWhen(item))
		fmt.Println("After this the key is free and FORGE may learn it again.")
		return nil
	}
	if err := svc.Purge(ctx, itemID); err != nil {
		return err
	}
	fmt.Printf("purged %s; the key %q may be learned again\n", item.ID, item.Key)
	return nil
}

// cmdMemoryExport writes a layer's contents as JSON (PRD MEM-02).
func cmdMemoryExport(ctx context.Context, cfg *config.Config, log *logx.Logger, args []string) error {
	const op = "forgectl.cmdMemoryExport"

	fs := newFlagSet("memory export")
	scope := fs.String("scope", "", "layer to export")
	owner := fs.String("owner", "", "the goal, project or user id the layer hangs off")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *scope == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("usage: forgectl memory export --scope <layer> [--owner <id>] > memory.json")
	}

	svc, pool, err := memoryService(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	export, err := svc.ExportLayer(ctx, memory.Scope(*scope), *owner)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(export)
}

// cmdMemorySweep reclaims expired rows.
//
// Reads already exclude them, so this frees space rather than enforcing
// retention — which is why a deployment that never runs it is slower, not
// wrong. It belongs in cron next to `audit verify`.
func cmdMemorySweep(ctx context.Context, cfg *config.Config, log *logx.Logger, args []string) error {
	fs := newFlagSet("memory sweep")
	dryRun := fs.Bool("dry-run", false, "count what would be reclaimed and change nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}

	svc, pool, err := memoryService(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	if *dryRun {
		var n int64
		err := pool.QueryRow(ctx, `
			select count(*) from forge_memory
			 where forgotten_at is null and pinned = false
			   and expires_at is not null and expires_at <= $1`, time.Now().UTC()).Scan(&n)
		if err != nil {
			return errs.Wrap("forgectl.cmdMemorySweep", errs.CodeDatabaseUnavail, err)
		}
		fmt.Printf("%d expired item(s) would be reclaimed (dry run — nothing removed)\n", n)
		return nil
	}
	n, err := svc.Sweep(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("%d expired item(s) reclaimed\n", n)
	return nil
}

// cmdDecisions lists a project's decision log, marking what has been superseded.
func cmdDecisions(ctx context.Context, cfg *config.Config, log *logx.Logger, args []string) error {
	const op = "forgectl.cmdDecisions"

	fs := newFlagSet("decisions")
	project := fs.String("project", "", "project id (required)")
	currentOnly := fs.Bool("current", false, "hide decisions that something later replaced")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *project == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("usage: forgectl decisions list --project <id> [--current]")
	}

	svc, pool, err := memoryService(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	list, err := svc.ListDecisions(ctx, memory.DecisionFilter{ProjectID: *project, CurrentOnly: *currentOnly})
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Println("no decisions recorded for this project")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tDECIDED\tSTATE\tTITLE\tDECISION")
	for _, d := range list {
		state := "current"
		if !d.Current() {
			state = "superseded by " + *d.SupersededByID
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			d.ID, d.DecidedAt.UTC().Format("2006-01-02"), state, d.Title, truncate(d.Decision, 48))
	}
	return w.Flush()
}

// cmdDecisionShow prints one decision with its whole supersession chain, so the
// answer that was believed and the one that replaced it are read together.
func cmdDecisionShow(ctx context.Context, cfg *config.Config, log *logx.Logger, args []string) error {
	const op = "forgectl.cmdDecisionShow"

	if len(args) != 1 {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("usage: forgectl decisions show <decision-id>")
	}

	svc, pool, err := memoryService(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	chain, err := svc.DecisionChain(ctx, args[0])
	if err != nil {
		return err
	}
	for i, d := range chain {
		marker := "  "
		if d.ID == args[0] {
			marker = "> "
		}
		state := "CURRENT"
		if !d.Current() {
			state = "superseded"
		}
		if i > 0 {
			fmt.Println("        ↓ superseded by")
		}
		fmt.Printf("%s%s  %s  [%s]\n", marker, d.ID, d.DecidedAt.UTC().Format("2006-01-02"), state)
		fmt.Printf("   %s\n", d.Title)
		fmt.Printf("   decided : %s\n", d.Decision)
		if d.Rationale != "" {
			fmt.Printf("   because : %s\n", d.Rationale)
		}
		for _, a := range d.Alternatives {
			fmt.Printf("   not     : %s — %s\n", a.Option, a.WhyNot)
		}
		for _, e := range d.Evidence {
			mark := " "
			if !e.Actionableish() {
				// The reader must be able to see, at a glance, which evidence
				// they may act on and which was recalled from model weights.
				mark = "!"
			}
			fmt.Printf("   %s evidence: %s [%s: %s]\n", mark, e.Statement, e.How, e.How.Gloss())
		}
		if len(d.Affected) > 0 {
			fmt.Printf("   affects : %s\n", strings.Join(d.Affected, ", "))
		}
	}
	return nil
}

func forgottenWhen(i *memory.Item) string {
	if i.ForgottenAt == nil {
		return "not forgotten"
	}
	return i.ForgottenAt.UTC().Format(time.RFC3339)
}

func dash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
