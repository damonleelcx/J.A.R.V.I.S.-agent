package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/claim"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// The operator's view of the workspace model (PRD RSN-01, WRK-03, WRK-04).
//
// `graph review` is the one to run from cron beside `audit verify`: it exits 1
// when the graph contradicts itself, and 0 when it is merely incomplete — which
// every real project always is.

func workspaceService(ctx context.Context, cfg *config.Config, log *logx.Logger) (*workspace.Service, *db.Pool, error) {
	pool, err := db.Connect(ctx, cfg.DB, log)
	if err != nil {
		return nil, nil, err
	}
	return workspace.NewService(pool, clock.System{}, log), pool, nil
}

// cmdGraphKinds prints the node and edge vocabularies.
//
// No database: the question it answers is about this build. "Which kind should
// this be, and what may it connect to?" is asked while modelling, not while
// looking at rows.
func cmdGraphKinds() error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "KIND\tPRD NAME\tCONTENT\tMAY BE KNOWN AS\tFOR")
	for _, d := range workspace.Kinds() {
		content := "in the graph"
		if d.Anchor != workspace.AnchorNone {
			content = "elsewhere (" + string(d.Anchor) + ")"
		}
		labels := "any of the seven"
		if len(d.Allowed) > 0 {
			names := make([]string, 0, len(d.Allowed))
			for _, e := range d.Allowed {
				names = append(names, string(e))
			}
			labels = strings.Join(names, ", ")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", d.Kind, d.PRDName, content, labels, d.Gloss)
	}
	fmt.Fprintln(w, "\nEDGE\tFROM\tTO\tMEANS")
	for _, d := range workspace.EdgeKinds() {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", d.Kind, kindList(d.From), kindList(d.To), d.Gloss)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Println("\nA node's kind never changes. To turn an assumption into a requirement, promote it:")
	fmt.Println("the requirement is created and derives_from the assumption, so both stay readable.")
	return nil
}

func kindList(ks []workspace.Kind) string {
	if len(ks) == 0 {
		return "any"
	}
	out := make([]string, 0, len(ks))
	for _, k := range ks {
		out = append(out, string(k))
	}
	return strings.Join(out, "/")
}

// cmdGraphShow prints a project's graph: what it holds and how it is connected.
func cmdGraphShow(ctx context.Context, cfg *config.Config, log *logx.Logger, args []string) error {
	const op = "forgectl.cmdGraphShow"

	fs := newFlagSet("graph show")
	project := fs.String("project", "", "project id (required)")
	kind := fs.String("kind", "", "only this node kind")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *project == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("usage: forgectl graph show --project <id> [--kind <kind>]")
	}

	svc, pool, err := workspaceService(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	g, err := svc.Load(ctx, *project)
	if err != nil {
		return err
	}
	if len(g.Nodes) == 0 {
		fmt.Println("this project's graph is empty")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tKIND\tSTATUS\tHOW\tTITLE")
	for _, n := range g.Nodes {
		if *kind != "" && string(n.Kind) != *kind {
			continue
		}
		title := n.Title
		if title == "" {
			if ref, ok := n.AnchorRef(); ok {
				title = "→ " + ref
			}
		}
		// A label a reader cannot act on is marked, so a graph full of guesses
		// looks like one at a glance rather than reading as settled.
		mark := " "
		if !(claim.Claim{How: n.How, Source: n.Source}).Actionableish() {
			mark = "!"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s%s\t%s\n", n.ID, n.Kind, n.Status, mark, n.How, truncate(title, 52))
	}
	if err := w.Flush(); err != nil {
		return err
	}

	if len(g.Edges) > 0 {
		fmt.Println()
		ew := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(ew, "RELATION\t\t")
		for _, e := range g.Edges {
			def, _ := workspace.EdgeKindOf(e.Kind)
			fmt.Fprintf(ew, "%s\t\n", fmt.Sprintf(def.Reads,
				truncate(g.Title(e.FromID), 32), truncate(g.Title(e.ToID), 32)))
		}
		if err := ew.Flush(); err != nil {
			return err
		}
	}
	fmt.Println("\n! marks a node whose epistemic label means a reader should check before acting on it.")
	return nil
}

// cmdGraphReview reports what a project's graph contradicts and what it lacks.
//
// # Exit status is the point
//
// 0 when the graph is consistent, 1 when it contradicts itself. GAPS DO NOT
// AFFECT THE EXIT STATUS: every real project has requirements nothing verifies
// yet, and a check that fails on those is a check somebody turns off in a week.
// Same reasoning as `audit verify`, which does not fail on events that predate
// the chain.
func cmdGraphReview(ctx context.Context, cfg *config.Config, log *logx.Logger, args []string) error {
	const op = "forgectl.cmdGraphReview"

	fs := newFlagSet("graph review")
	project := fs.String("project", "", "project id (required)")
	quiet := fs.Bool("quiet", false, "print nothing when the graph is consistent; the exit status is the answer")
	gaps := fs.Bool("gaps", true, "also list what is missing (never affects the exit status)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *project == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("usage: forgectl graph review --project <id> [--quiet] [--gaps=false]")
	}

	svc, pool, err := workspaceService(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	rev, err := svc.Review(ctx, *project)
	if err != nil {
		return err
	}
	if rev.Sound() && *quiet {
		return nil
	}

	fmt.Printf("%s\n", rev.Summary())
	if len(rev.Defects) > 0 {
		fmt.Println("\nCONTRADICTIONS — the graph asserts something that cannot be true:")
		for _, d := range rev.Defects {
			fmt.Printf("  [%s] %s\n", d.Problem, d.Detail)
		}
	}
	if *gaps && len(rev.Gaps) > 0 {
		fmt.Println("\nGAPS — expected on any project still in progress:")
		for _, g := range rev.Gaps {
			fmt.Printf("  [%s] %s\n", g.Problem, g.Detail)
		}
	}
	if !rev.Sound() {
		// Same contract as `audit verify`: cron and the release checklist read
		// the exit code, not the text.
		os.Exit(1)
	}
	return nil
}

// cmdArtifact prints an artifact's lifecycle: WRK-04's seven facts per version.
func cmdArtifactHistory(ctx context.Context, cfg *config.Config, log *logx.Logger, args []string) error {
	const op = "forgectl.cmdArtifactHistory"

	fs := newFlagSet("artifacts show")
	project := fs.String("project", "", "project id, when addressing by path")
	if len(args) < 1 {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("usage: forgectl artifacts show <artifact-id>\n   or: forgectl artifacts show <path> --project <id>")
	}
	target := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	svc, pool, err := workspaceService(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	artifactID := target
	if *project != "" {
		a, err := svc.Repo().FindArtifactByPath(ctx, pool, *project, target)
		if err != nil {
			return err
		}
		artifactID = a.ID
	}
	hist, err := svc.History(ctx, artifactID)
	if err != nil {
		return err
	}

	fmt.Printf("%s  (%s)  %s\n\n", hist.Artifact.Path, hist.Artifact.Kind, hist.Artifact.ID)
	for _, v := range hist.Versions {
		marker := "  "
		if v.Version == hist.Versions[0].Version {
			marker = "> "
		}
		fmt.Printf("%sv%d  %s\n", marker, v.Version, v.CreatedAt.UTC().Format("2006-01-02 15:04"))
		fmt.Printf("     initiator  : %s\n", v.InitiatorID)
		fmt.Printf("     agent      : %s\n", v.Agent)
		fmt.Printf("     tool       : %s\n", derefOrDash(v.ToolCallID))
		fmt.Printf("     inputs     : %s\n", truncate(string(v.Inputs), 60))
		fmt.Printf("     diff       : %s\n", dashIfEmpty(truncate(v.Diff, 60)))
		// The two facts are printed on separate lines, labelled by who decided
		// them, because the whole design turns on them not being confused.
		fmt.Printf("     machine    : %s%s\n", v.Verification, noteSuffix(v.VerificationNote))
		fmt.Printf("     person     : %s%s\n", v.Disposition, dispositionBy(&v))
		fmt.Printf("     timeline   : %s\n", derefOrDash(v.EventID))
		// The reason, not just the code. "Why can I not use this?" is the whole
		// question an operator opens this for, and Usable() already answers it
		// in a sentence — printing only the code throws that away.
		if err := v.Usable(); err != nil {
			fmt.Printf("     usable     : no — %s\n", detailOf(err))
		} else {
			fmt.Printf("     usable     : yes — verified by a machine AND accepted by a person\n")
		}
		fmt.Println()
	}
	return nil
}

// detailOf strips the registry preamble and returns the specific sentence.
// The code and its generic cause are already on screen elsewhere; what is
// wanted here is the part about THIS version.
func detailOf(err error) string {
	msg := err.Error()
	if i := strings.LastIndex(msg, "("); i >= 0 && strings.HasSuffix(msg, ")") {
		return msg[i+1 : len(msg)-1]
	}
	return msg
}

func derefOrDash(p *string) string {
	if p == nil || *p == "" {
		return "—"
	}
	return *p
}

func dashIfEmpty(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func noteSuffix(note string) string {
	if strings.TrimSpace(note) == "" {
		return ""
	}
	return " — " + note
}

func dispositionBy(v *workspace.Version) string {
	if v.DispositionedBy == nil {
		return ""
	}
	out := " by " + *v.DispositionedBy
	if strings.TrimSpace(v.DispositionReason) != "" {
		out += " — " + v.DispositionReason
	}
	return out
}
