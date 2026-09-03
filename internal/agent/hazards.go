package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Hazard-aware planning for R3–R4 (PRD SAF-02).
//
// # What the project already knew and the planner did not
//
// A hazard is a first-class node in the project graph — "the sharp edge", kept
// distinct from a risk, which is the chance somebody touches it. People record
// them. The planner read none of it: it saw the goal statement, the completion
// criteria and two ceilings, and nothing about what in this project can hurt
// somebody. A plan for a release could therefore be perfectly reasonable about
// everything except the thing the team had already written down as dangerous.
//
// # Why only R3 and above
//
// Because that is what the requirement says, and because the alternative is
// worse than it sounds. Loading hazards into every plan would put them in front
// of r0 discussion and r1 sandbox drafts, where the honest answer to "does this
// address the hazard" is "it does not, and it should not have to" — and a
// requirement that fires constantly on work it does not apply to is a
// requirement people learn to satisfy with boilerplate.
//
// R3 is release preparation and R4 is safety-critical support. Those are the
// tiers where an unaddressed hazard is a decision rather than an omission.
//
// # Why coverage is checked rather than requested
//
// Putting the hazards in the prompt is necessary and is not sufficient. A prompt
// is an instruction to a model, and this codebase does not accept an instruction
// as a control anywhere else — the plan is checked, and one that leaves a live
// hazard unaddressed is refused with the hazards named. That refusal is the
// mechanism; the prompt is what makes it usually unnecessary.

// hazardSource is the part of workspace.Service this needs.
//
// An interface rather than the concrete service so the planner's dependency is
// "something that can load a project graph" — which is what a test can supply
// without a database, and what keeps this from importing the world.
type hazardSource interface {
	Load(ctx context.Context, projectID string) (*workspace.Graph, error)
}

// hazard is one live source of harm, as the planner needs it.
type hazard struct {
	ID    string
	Title string
	Body  string
}

// hazardsApply reports whether this goal is at a tier SAF-02 covers.
func hazardsApply(tier engine.RiskTier) bool { return tier.AtLeast(engine.RiskR3) }

// liveHazards returns the hazards a plan for this project must account for.
//
// Retired and rejected hazards are excluded: a hazard somebody has closed is a
// record of a decision, not an open obligation, and demanding a plan address one
// would make the check impossible to ever satisfy on a mature project.
func liveHazards(g *workspace.Graph) []hazard {
	var out []hazard
	for _, n := range g.Nodes {
		if n.Kind != workspace.KindHazard {
			continue
		}
		if n.Status == workspace.StatusRetired || n.Status == workspace.StatusRejected {
			continue
		}
		out = append(out, hazard{ID: n.ID, Title: n.Title, Body: n.Body})
	}
	// Stable order: the hazard list is part of a prompt prefix, and an unstable
	// order invalidates the model's cache on every plan for no reason.
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// hazardBrief renders the hazards into the planner's prompt.
//
// Ids are included because they are what the plan has to name back. Asking the
// model to match on titles would make the coverage check a string-similarity
// problem, and a safety gate decided by fuzzy matching is a gate that fails in
// the direction of passing.
func hazardBrief(hs []hazard) string {
	if len(hs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## Hazards recorded on this project\n\n")
	b.WriteString("This goal is at r3 or above, so the plan must account for every hazard " +
		"below. For each one, the task or tasks that address it must list its id in " +
		"\"addresses\". A plan that leaves one unaddressed is refused.\n\n")
	b.WriteString("Addressing a hazard does not always mean eliminating it. A task that " +
		"contains it, tests for it, or documents the residual risk addresses it; a task that " +
		"happens to be near it does not.\n\n")
	for _, h := range hs {
		fmt.Fprintf(&b, "- %s — %s\n", h.ID, h.Title)
		if strings.TrimSpace(h.Body) != "" {
			fmt.Fprintf(&b, "  %s\n", truncate(h.Body, 400))
		}
	}
	return b.String()
}

// unaddressedHazards returns the hazards no task claims to address.
func unaddressedHazards(tasks []PlannedTask, hs []hazard) []hazard {
	claimed := map[string]bool{}
	for _, t := range tasks {
		for _, id := range t.Addresses {
			claimed[strings.TrimSpace(id)] = true
		}
	}
	var out []hazard
	for _, h := range hs {
		if !claimed[h.ID] {
			out = append(out, h)
		}
	}
	return out
}

// checkHazardCoverage refuses a plan that ignores a live hazard.
//
// The error names every hazard that was missed rather than the first, because a
// replan that fixes one and trips on the next is the shape that makes people
// disable a check.
func checkHazardCoverage(tasks []PlannedTask, hs []hazard) error {
	missed := unaddressedHazards(tasks, hs)
	if len(missed) == 0 {
		return nil
	}
	var names []string
	for _, h := range missed {
		names = append(names, h.ID+" ("+h.Title+")")
	}
	return errs.New("agent.Planner.Plan", errs.CodeInvariantViolated).
		WithDetail("this goal is at r3 or above and the plan does not address %d recorded "+
			"hazard(s): %s.\nEvery live hazard needs at least one task naming its id in "+
			"\"addresses\". Containing it, testing for it, or documenting the residual risk "+
			"all count; ignoring it does not.", len(missed), strings.Join(names, ", "))
}

// hazardsFor loads the live hazards for a goal, or nothing when SAF-02 does not
// apply.
//
// A failure to read the graph is NOT swallowed. Everywhere else in this codebase
// an optional read falls back and logs, because the alternative is an outage
// over a tone setting or a stale cache. This one is different: falling back here
// would plan an r3 release as though the project had recorded no hazards, which
// is indistinguishable from a project that genuinely has none. The safe failure
// for a safety check is to stop.
func (p *Planner) hazardsFor(ctx context.Context, goal *engine.Goal) ([]hazard, error) {
	if p.hazards == nil || !hazardsApply(goal.RiskTier) {
		return nil, nil
	}
	g, err := p.hazards.Load(ctx, goal.ProjectID)
	if err != nil {
		return nil, errs.Wrap("agent.Planner.Plan", errs.CodeDatabaseUnavail, err).
			WithDetail("this goal is at %s, where the plan must account for the project's "+
				"recorded hazards, and the project graph could not be read. Planning stopped "+
				"rather than proceeding as though there were none.", goal.RiskTier)
	}
	hs := liveHazards(g)
	if p.log != nil {
		p.log.Info(ctx, logx.EventPlanHazardsLoaded,
			"goal_id", goal.ID, "risk_tier", string(goal.RiskTier), "hazards", len(hs))
	}
	return hs, nil
}
