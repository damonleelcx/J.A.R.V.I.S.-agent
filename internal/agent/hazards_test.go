package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// Hazard-aware planning for R3–R4 (PRD SAF-02).

type fakeGraph struct {
	graph *workspace.Graph
	err   error
	calls int
}

func (f *fakeGraph) Load(context.Context, string) (*workspace.Graph, error) {
	f.calls++
	return f.graph, f.err
}

func graphWith(nodes ...workspace.Node) *workspace.Graph {
	return &workspace.Graph{ProjectID: "prj_1", Nodes: nodes}
}

func haz(id, title string, status workspace.Status) workspace.Node {
	return workspace.Node{ID: id, Kind: workspace.KindHazard, Title: title, Status: status}
}

// The tier gate. Loading hazards into every plan would put them in front of
// discussion and sandbox drafts, where "this does not address the hazard" is the
// correct answer — and a check that fires on work it does not apply to is one
// people satisfy with boilerplate.
func TestHazardsAreLoadedOnlyForR3AndAbove(t *testing.T) {
	for _, tc := range []struct {
		tier engine.RiskTier
		want bool
	}{
		{engine.RiskR0, false},
		{engine.RiskR1, false},
		{engine.RiskR2, false},
		{engine.RiskR3, true},
		{engine.RiskR4, true},
	} {
		if got := hazardsApply(tc.tier); got != tc.want {
			t.Errorf("hazardsApply(%s) = %v, want %v", tc.tier, got, tc.want)
		}
	}
}

// A closed hazard is a record of a decision, not an open obligation. Demanding
// a plan address one would make the check impossible to satisfy on any project
// that has been running for a while.
func TestOnlyLiveHazardsMustBeAddressed(t *testing.T) {
	g := graphWith(
		haz("wsn_open", "sharp edge on the bracket", workspace.StatusOpen),
		haz("wsn_accepted", "pinch point at the hinge", workspace.StatusAccepted),
		haz("wsn_retired", "old coolant line", workspace.StatusRetired),
		haz("wsn_rejected", "was not actually a hazard", workspace.StatusRejected),
		workspace.Node{ID: "wsn_risk", Kind: workspace.KindRisk, Title: "schedule risk", Status: workspace.StatusOpen},
	)
	got := liveHazards(g)
	if len(got) != 2 {
		t.Fatalf("live hazards = %d, want 2 (%+v)", len(got), got)
	}
	// A risk is not a hazard. The model keeps them apart deliberately — the
	// hazard is the sharp edge, the risk is the chance somebody touches it — and
	// collapsing them here would quietly redefine both.
	for _, h := range got {
		if h.ID == "wsn_risk" {
			t.Error("a risk was treated as a hazard")
		}
	}
}

// The check, not the prompt, is the mechanism.
//
// This is the test that distinguishes SAF-02 from "we mentioned hazards to the
// model". A plan that ignores a live hazard is refused.
func TestAPlanThatIgnoresAHazardIsRefused(t *testing.T) {
	hs := []hazard{
		{ID: "wsn_a", Title: "sharp edge on the bracket"},
		{ID: "wsn_b", Title: "pinch point at the hinge"},
	}
	tasks := []PlannedTask{
		{Key: "guard", Title: "add a guard", Addresses: []string{"wsn_a"}},
		{Key: "ship", Title: "cut the release"},
	}

	err := checkHazardCoverage(tasks, hs)
	if err == nil {
		t.Fatal("a plan that addresses one of two hazards was accepted.\n" +
			"If this passes, SAF-02 is a sentence in a prompt rather than a rule")
	}
	// The refusal has to name what was missed, and all of it: a replan that
	// fixes one hazard and trips on the next is the shape that makes people
	// disable a check.
	msg := err.Error()
	if !strings.Contains(msg, "wsn_b") || !strings.Contains(msg, "pinch point") {
		t.Errorf("the refusal does not name the hazard that was missed: %s", msg)
	}
	if strings.Contains(msg, "wsn_a") {
		t.Errorf("the refusal names a hazard that WAS addressed: %s", msg)
	}

	// And it passes once both are claimed.
	tasks[1].Addresses = []string{"wsn_b"}
	if err := checkHazardCoverage(tasks, hs); err != nil {
		t.Errorf("a plan addressing both hazards was refused: %v", err)
	}
}

// Nothing is required when nothing was recorded, so a project with no hazards
// plans exactly as it did before.
func TestNoHazardsMeansNoNewRequirement(t *testing.T) {
	if err := checkHazardCoverage([]PlannedTask{{Key: "ship"}}, nil); err != nil {
		t.Errorf("a plan was refused on a project with no recorded hazards: %v", err)
	}
	if brief := hazardBrief(nil); brief != "" {
		t.Errorf("an empty hazard list still produced prompt text: %q", brief)
	}
}

// A graph that cannot be read STOPS the plan.
//
// Every other optional read in this codebase falls back and logs, because an
// outage over a tone setting is worse than the setting being wrong. This one is
// the exception and the exception is the point: falling back would plan an r3
// release as though the project had recorded no hazards, which is
// indistinguishable from a project that genuinely has none.
func TestAnUnreadableGraphStopsPlanningRatherThanAssumingSafety(t *testing.T) {
	p := NewPlanner(nil, persona.DefaultCharacter()).
		WithHazards(&fakeGraph{err: errs.New("test", errs.CodeDatabaseUnavail)}, nil)

	_, err := p.hazardsFor(context.Background(),
		&engine.Goal{ID: "gol_1", ProjectID: "prj_1", RiskTier: engine.RiskR3})
	if err == nil {
		t.Fatal("planning continued after the project graph could not be read.\n" +
			"An r3 plan built as though there were no hazards is the failure this guards")
	}
	if !strings.Contains(err.Error(), "rather than proceeding as though there were none") {
		t.Errorf("the error does not explain what was refused and why: %v", err)
	}

	// Below r3 the same unreadable graph is never consulted, so it cannot fail.
	src := &fakeGraph{err: errs.New("test", errs.CodeDatabaseUnavail)}
	p2 := NewPlanner(nil, persona.DefaultCharacter()).WithHazards(src, nil)
	if _, err := p2.hazardsFor(context.Background(),
		&engine.Goal{ID: "gol_2", ProjectID: "prj_1", RiskTier: engine.RiskR1}); err != nil {
		t.Errorf("an r1 goal was stopped by a hazard read it should never have made: %v", err)
	}
	if src.calls != 0 {
		t.Errorf("the graph was loaded %d times for an r1 goal; SAF-02 covers r3 and above", src.calls)
	}
}

// The brief names ids, because the coverage check matches on them.
func TestTheBriefGivesTheModelWhatTheCheckWillMatchOn(t *testing.T) {
	brief := hazardBrief([]hazard{{ID: "wsn_a", Title: "sharp edge", Body: "on the outer bracket"}})
	for _, want := range []string{"wsn_a", "sharp edge", "on the outer bracket", "addresses", "refused"} {
		if !strings.Contains(brief, want) {
			t.Errorf("the hazard brief does not mention %q:\n%s", want, brief)
		}
	}
}

// planStub is an llm.Client that returns a fixed plan.
type planStub struct {
	plan   string
	system string
}

func (p *planStub) Complete(_ context.Context, req llm.Request) (*llm.Response, error) {
	for _, m := range req.Messages {
		if m.Role == llm.User {
			p.system = m.Content
		}
	}
	return &llm.Response{Content: p.plan, FinishReason: "stop"}, nil
}

func (p *planStub) ModelFor(llm.Role) string { return "stub" }

// The check is wired into the path a plan actually takes.
//
// # Why this exists on top of TestAPlanThatIgnoresAHazardIsRefused
//
// That test calls checkHazardCoverage directly, so it passes whether or not
// anything calls it. A drill proved the point: deleting the call from
// Planner.Plan left the entire package green, which is a fence guarding a
// function rather than a behaviour — the exact shape this codebase keeps finding
// in itself.
//
// This one goes through Plan, so removing the check from the path turns it red.
func TestPlanRefusesAnR3PlanThatIgnoresAHazardEndToEnd(t *testing.T) {
	const ignores = `{"rationale":"ship it","clarification_needed":"","tasks":[
		{"key":"ship","title":"cut the release","instruction":"tag and publish",
		 "inputs":{},"expected_output":{"description":"a tag"},"depends_on":[],"risk_tier":"r3"}]}`
	const addresses = `{"rationale":"guard then ship","clarification_needed":"","tasks":[
		{"key":"guard","title":"fit the guard","instruction":"fit a guard over the edge",
		 "inputs":{},"expected_output":{"description":"a fitted guard"},"depends_on":[],
		 "risk_tier":"r3","addresses":["wsn_a"]}]}`

	src := &fakeGraph{graph: graphWith(haz("wsn_a", "sharp edge on the bracket", workspace.StatusOpen))}
	goal := &engine.Goal{ID: "gol_1", ProjectID: "prj_1", Title: "Release", Statement: "ship v2",
		RiskTier: engine.RiskR3, Autonomy: engine.AutonomyApprovalGated}

	stub := &planStub{plan: ignores}
	_, err := NewPlanner(stub, persona.DefaultCharacter()).WithHazards(src, nil).Plan(
		context.Background(), goal, nil, "")
	if err == nil {
		t.Fatal("Plan accepted an r3 plan that ignores a recorded hazard.\n" +
			"checkHazardCoverage may be correct and simply not called — which is what a " +
			"drill on this exact mutation found")
	}
	if !strings.Contains(err.Error(), "wsn_a") {
		t.Errorf("the refusal does not name the hazard: %v", err)
	}
	// And the hazard reached the prompt, so the model had what the check matches on.
	if !strings.Contains(stub.system, "wsn_a") || !strings.Contains(stub.system, "sharp edge") {
		t.Error("the hazard was enforced but never shown to the planner, which makes the " +
			"refusal unactionable: the model cannot address what it was not told about")
	}

	// The same goal plans fine once a task claims the hazard.
	ok, err := NewPlanner(&planStub{plan: addresses}, persona.DefaultCharacter()).
		WithHazards(src, nil).Plan(context.Background(), goal, nil, "")
	if err != nil {
		t.Fatalf("a plan that addresses the hazard was refused: %v", err)
	}
	if len(ok.Tasks) != 1 || len(ok.Tasks[0].Addresses) != 1 {
		t.Errorf("the addresses field did not survive decoding: %+v", ok.Tasks)
	}

	// And an r1 goal is untouched by any of it.
	low := *goal
	low.RiskTier = engine.RiskR1
	if _, err := NewPlanner(&planStub{plan: ignores}, persona.DefaultCharacter()).
		WithHazards(src, nil).Plan(context.Background(), &low, nil, ""); err != nil {
		t.Errorf("an r1 plan was refused for not addressing a hazard: %v", err)
	}
}
