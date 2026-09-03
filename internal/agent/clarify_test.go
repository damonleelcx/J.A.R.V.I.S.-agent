package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/claim"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Clarification before consequential work (PRD RSN-02).
//
// The gate is a pure function over what the record says, so its policy is tested
// without a database; the recording and answering go through real rows, because
// the whole failure this closes was that the question was never stored.

func TestConsequentialWorkIsHeldAndExplorationIsNot(t *testing.T) {
	held := &clarificationHold{Question: "which bracket material?", Answered: false}

	for _, tc := range []struct {
		tier     engine.RiskTier
		wantHeld bool
	}{
		// "before consequential work" — r2 is consequential digital change, so
		// the boundary is the requirement's own words rather than a judgement.
		{engine.RiskR0, false},
		{engine.RiskR1, false},
		{engine.RiskR2, true},
		{engine.RiskR3, true},
		{engine.RiskR4, true},
	} {
		assumption, err := gateOnClarification(held, &engine.Goal{ID: "gol_1", RiskTier: tc.tier})
		if tc.wantHeld {
			if err == nil {
				t.Errorf("%s: consequential work started with an unanswered question", tc.tier)
			}
			if assumption != "" {
				t.Errorf("%s: held work also produced an assumption to record", tc.tier)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: low-risk exploration was blocked: %v\nA system that stops dead "+
				"whenever it is unsure teaches people to phrase goals so it never asks", tc.tier, err)
		}
		// The second half of the requirement: it proceeds AND the question is
		// labelled, rather than proceeding silently.
		if assumption != held.Question {
			t.Errorf("%s: exploration proceeded without an assumption to label (%q)", tc.tier, assumption)
		}
	}
}

// An answered question stops holding anything, at every tier.
func TestAnAnsweredQuestionReleasesTheGate(t *testing.T) {
	answered := &clarificationHold{Question: "which material?", Answered: true}
	for _, tier := range []engine.RiskTier{engine.RiskR0, engine.RiskR2, engine.RiskR4} {
		assumption, err := gateOnClarification(answered, &engine.Goal{ID: "gol_1", RiskTier: tier})
		if err != nil {
			t.Errorf("%s: an ANSWERED question still held the goal: %v", tier, err)
		}
		if assumption != "" {
			t.Errorf("%s: an answered question was also recorded as an assumption; it is not "+
				"an assumption, somebody answered it", tier)
		}
	}
	// And no question at all holds nothing.
	if _, err := gateOnClarification(nil, &engine.Goal{RiskTier: engine.RiskR4}); err != nil {
		t.Errorf("a goal nobody asked about was held: %v", err)
	}
}

// The refusal has to be actionable: it names the question and how to answer it.
func TestTheRefusalSaysWhatToDo(t *testing.T) {
	_, err := gateOnClarification(
		&clarificationHold{Question: "aluminium or steel?"},
		&engine.Goal{ID: "gol_abc", RiskTier: engine.RiskR3})
	if err == nil {
		t.Fatal("no refusal")
	}
	for _, want := range []string{"aluminium or steel?", "forgectl goal answer gol_abc", "r3"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

// The question survives as state, which is the whole failure this closes.
func TestTheQuestionIsRecordedAndAnswerable(t *testing.T) {
	f := newRecoveryFixture(t) // goal fixture against a real database
	ctx := context.Background()

	if hold, err := clarificationFor(ctx, f.pool, f.goalID); err != nil || hold != nil {
		t.Fatalf("a fresh goal already had a question: %+v (%v)", hold, err)
	}

	if err := recordQuestion(ctx, f.pool, f.goalID, "which bracket material?"); err != nil {
		t.Fatal(err)
	}
	hold, err := clarificationFor(ctx, f.pool, f.goalID)
	if err != nil {
		t.Fatal(err)
	}
	if hold == nil || hold.Question != "which bracket material?" || hold.Answered {
		t.Fatalf("the question was not recorded as outstanding: %+v", hold)
	}

	// Answering an unasked question is refused. An answer stored against no
	// question would satisfy the gate for the NEXT one.
	//
	// A second goal in the SAME schema rather than a second fixture: the harness
	// names its schema after nothing in particular and drops it on entry, so
	// building two would delete the first one's rows underneath this test.
	unasked := f.goalLike(t, "Another goal")
	if err := AnswerClarification(ctx, f.pool, unasked, "steel"); err == nil {
		t.Error("an answer was accepted for a goal that was never asked anything")
	}

	if err := AnswerClarification(ctx, f.pool, f.goalID, "steel"); err != nil {
		t.Fatal(err)
	}
	hold, err = clarificationFor(ctx, f.pool, f.goalID)
	if err != nil {
		t.Fatal(err)
	}
	if hold == nil || !hold.Answered {
		t.Fatalf("the answer did not release the hold: %+v", hold)
	}
}

// A plan that no longer asks clears the old question AND its answer.
//
// Without this a goal keeps a stale question forever: answered once, replanned,
// and the next gate reads an answer to a question nobody is asking any more.
func TestPlanningWithoutAQuestionClearsTheOldOne(t *testing.T) {
	f := newRecoveryFixture(t)
	ctx := context.Background()

	if err := recordQuestion(ctx, f.pool, f.goalID, "which material?"); err != nil {
		t.Fatal(err)
	}
	if err := AnswerClarification(ctx, f.pool, f.goalID, "steel"); err != nil {
		t.Fatal(err)
	}
	// A later plan asks nothing.
	if err := recordQuestion(ctx, f.pool, f.goalID, ""); err != nil {
		t.Fatal(err)
	}
	hold, err := clarificationFor(ctx, f.pool, f.goalID)
	if err != nil {
		t.Fatal(err)
	}
	if hold != nil {
		t.Fatalf("a stale question survived a plan that did not ask one: %+v", hold)
	}
	// And the answer went with it, so a NEW question starts unanswered.
	if err := recordQuestion(ctx, f.pool, f.goalID, "which finish?"); err != nil {
		t.Fatal(err)
	}
	hold, err = clarificationFor(ctx, f.pool, f.goalID)
	if err != nil {
		t.Fatal(err)
	}
	if hold == nil || hold.Answered {
		t.Fatalf("a new question was born already answered by the previous one's answer: %+v\n"+
			"That is the failure mode a clarification gate cannot have", hold)
	}
}

// The gate is wired into the path a goal actually starts through.
//
// # Why this exists on top of the tests above
//
// Those call gateOnClarification directly, so they pass whether or not anything
// calls it. SAF-02 taught this the hard way in the same package: a drill deleted
// the call from the planner and every test stayed green, because the fences
// guarded a function rather than a behaviour.
//
// Activate is the one place both forgectl and the HTTP API pass through, so this
// drives it and asserts the refusal — and, at low risk, that the work proceeds
// AND the assumption is written into the project graph, which is the half of
// RSN-02 that makes the gate bearable.
func TestActivateHoldsConsequentialWorkAndLabelsExploration(t *testing.T) {
	f := newRecoveryFixture(t)
	ctx := context.Background()
	applier := NewPlanApplier(engine.NewRepository(), engine.NewQueue(),
		engine.NewBudgetGuard(config.EngineConfig{}), clock.System{}).
		WithWorkspace(workspace.NewService(f.pool, clock.System{}, logx.Discard()), logx.Discard())

	// A draft goal with a task, so the no-tasks refusal is not what we measure.
	start := func(t *testing.T, tier engine.RiskTier, question string) (*engine.Goal, error) {
		t.Helper()
		goalID := f.goalLike(t, "Work at "+string(tier))
		if _, err := f.pool.Exec(ctx,
			`update forge_goals set risk_tier = $2 where id = $1`, goalID, string(tier)); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(ctx, `
			insert into forge_tasks (id, goal_id, plan_id, title, instruction, status, risk_tier,
				idempotency_key, created_at, updated_at)
			values ($1,$2,$3,'do the work','do it','pending','r1',$1,now(),now())`,
			id.New(id.PrefixTask), goalID, f.planID); err != nil {
			t.Fatal(err)
		}
		if question != "" {
			if err := recordQuestion(ctx, f.pool, goalID, question); err != nil {
				t.Fatal(err)
			}
		}
		var project string
		if err := f.pool.QueryRow(ctx,
			`select project_id from forge_goals where id = $1`, goalID).Scan(&project); err != nil {
			t.Fatal(err)
		}
		goal := &engine.Goal{ID: goalID, ProjectID: project, Status: engine.GoalDraft, RiskTier: tier}
		return goal, applier.Activate(ctx, f.pool, goal, engine.ActorHuman, nil)
	}

	// Consequential work is held, through the real activation path.
	goal, err := start(t, engine.RiskR3, "aluminium or steel?")
	if err == nil {
		t.Fatal("Activate started an r3 goal with an unanswered question.\n" +
			"gateOnClarification may be correct and simply not called — which is the mutation " +
			"that left SAF-02's entire package green")
	}
	if !strings.Contains(err.Error(), "aluminium or steel?") {
		t.Errorf("the refusal does not carry the question: %v", err)
	}
	var status string
	if err := f.pool.QueryRow(ctx, `select status from forge_goals where id = $1`, goal.ID).
		Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != string(engine.GoalDraft) {
		t.Errorf("the goal is %s after a refused activation; it must not have started", status)
	}

	// Low-risk exploration proceeds, and the assumption is filed.
	explore, err := start(t, engine.RiskR1, "aluminium or steel?")
	if err != nil {
		t.Fatalf("an r1 goal was blocked by an unanswered question: %v", err)
	}
	g, err := workspace.NewService(f.pool, clock.System{}, logx.Discard()).Load(ctx, explore.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	var labelled bool
	for _, n := range g.Nodes {
		if n.Kind == workspace.KindAssumption && strings.Contains(n.Body, "aluminium or steel?") {
			labelled = true
			if n.How != claim.Assumed {
				t.Errorf("the assumption is labelled %q, not %q", n.How, claim.Assumed)
			}
		}
	}
	if !labelled {
		t.Error("low-risk work started on an unanswered question and nothing was written down.\n" +
			"That is the state RSN-02's second half exists to prevent: the exploration is " +
			"permitted, resting on something nobody recorded is not")
	}

	// And an answered question lets consequential work through.
	answered := f.goalLike(t, "Answered work")
	if _, err := f.pool.Exec(ctx, `
		insert into forge_tasks (id, goal_id, plan_id, title, instruction, status, risk_tier,
			idempotency_key, created_at, updated_at)
		values ($1,$2,$3,'do the work','do it','pending','r1',$1,now(),now())`,
		id.New(id.PrefixTask), answered, f.planID); err != nil {
		t.Fatal(err)
	}
	if err := recordQuestion(ctx, f.pool, answered, "which finish?"); err != nil {
		t.Fatal(err)
	}
	if err := AnswerClarification(ctx, f.pool, answered, "anodised"); err != nil {
		t.Fatal(err)
	}
	goal3 := &engine.Goal{ID: answered, Status: engine.GoalDraft, RiskTier: engine.RiskR3}
	if err := applier.Activate(ctx, f.pool, goal3, engine.ActorHuman, nil); err != nil {
		t.Errorf("an answered r3 goal was still held: %v", err)
	}
}

// Planning a goal PERSISTS the question, through the path planning takes.
//
// # Why this exists on top of TestTheQuestionIsRecordedAndAnswerable
//
// That test calls recordQuestion directly, so it passes whether or not the
// planner's caller ever does. A drill proved it: deleting the recordQuestion
// call from Intake.Plan left the package green.
//
// That is the second time in this package that a fence guarded a function
// instead of a behaviour — SAF-02's coverage check had the identical hole. The
// pattern is now specific enough to name: when the fix is "call X from Y", a
// test of X is not a test of the fix, and the drill to run is "delete the call".
func TestPlanningAGoalPersistsTheQuestionItAsks(t *testing.T) {
	f := newRecoveryFixture(t)
	ctx := context.Background()

	const asks = `{"rationale":"cannot proceed","clarification_needed":"aluminium or steel?","tasks":[]}`
	const answers = `{"rationale":"clear enough","clarification_needed":"","tasks":[
		{"key":"draw","title":"draw it","instruction":"draw the bracket","inputs":{},
		 "expected_output":{"description":"a drawing"},"depends_on":[],"risk_tier":"r1"}]}`

	goalID := f.goalLike(t, "Ambiguous work")
	var project, owner string
	if err := f.pool.QueryRow(ctx,
		`select project_id, created_by from forge_goals where id = $1`, goalID).
		Scan(&project, &owner); err != nil {
		t.Fatal(err)
	}
	goal := &engine.Goal{ID: goalID, ProjectID: project, CreatedBy: owner,
		Status: engine.GoalDraft, RiskTier: engine.RiskR3}

	intake := NewIntake(&planStub{plan: asks}, persona.DefaultCharacter(),
		config.EngineConfig{}, clock.System{})
	out, err := intake.Plan(ctx, f.pool, goal)
	if err != nil {
		t.Fatal(err)
	}
	if out.ClarificationNeeded == "" {
		t.Fatal("the plan outcome carries no question")
	}

	// The row, not the return value. A question that is only returned is a
	// question that vanishes when the process does — which is exactly the state
	// this requirement was in before.
	hold, err := clarificationFor(ctx, f.pool, goalID)
	if err != nil {
		t.Fatal(err)
	}
	if hold == nil {
		t.Fatal("planning asked a question and stored nothing.\n" +
			"`goal replan` would ask the model again, and a second roll that did not ask " +
			"would produce a plan built on the ambiguity nobody resolved")
	}
	if hold.Question != "aluminium or steel?" || hold.Answered {
		t.Fatalf("the stored question is wrong: %+v", hold)
	}

	// Answer it, then plan again with a planner that no longer asks: the stale
	// question and its answer both go, so a later question starts unanswered.
	if err := AnswerClarification(ctx, f.pool, goalID, "steel"); err != nil {
		t.Fatal(err)
	}
	clear := NewIntake(&planStub{plan: answers}, persona.DefaultCharacter(),
		config.EngineConfig{}, clock.System{})
	if _, err := clear.Plan(ctx, f.pool, goal); err != nil {
		t.Fatal(err)
	}
	hold, err = clarificationFor(ctx, f.pool, goalID)
	if err != nil {
		t.Fatal(err)
	}
	if hold != nil {
		t.Fatalf("a plan that asked nothing left the old question standing: %+v", hold)
	}
}
