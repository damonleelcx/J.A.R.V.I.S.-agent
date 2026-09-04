package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
)

// What a person settled reaches the plan (PRD RSN-02, RSN-03).
//
// RSN-02 shipped with the answer stored, the gate released, and `goal answer`
// telling people to replan so the plan is built on it — and nothing on the
// planning path read the column. Everything about the feature was observable
// except the part that was the point: the replan asked the model the same
// ambiguous question again, and whether that roll asked again or quietly guessed
// was luck.
//
// So these go through Intake.Plan, the path both forgectl and the HTTP surface
// plan on, and assert what the model was actually handed.

// The answer somebody gave reaches the planner.
func TestTheAnswerToAQuestionReachesThePlan(t *testing.T) {
	f := newRecoveryFixture(t)
	ctx := context.Background()

	const plan = `{"rationale":"clear now","clarification_needed":"","tasks":[
		{"key":"cut","title":"cut the bracket","instruction":"cut it","inputs":{},
		 "expected_output":{"description":"a bracket"},"depends_on":[],"risk_tier":"r1"}]}`

	goalID := f.goalLike(t, "Build the bracket")
	var project, owner string
	if err := f.pool.QueryRow(ctx,
		`select project_id, created_by from forge_goals where id = $1`, goalID).
		Scan(&project, &owner); err != nil {
		t.Fatal(err)
	}
	if err := recordQuestion(ctx, f.pool, goalID, "aluminium or steel?"); err != nil {
		t.Fatal(err)
	}
	if err := AnswerClarification(ctx, f.pool, goalID,
		"steel, because it is going outdoors"); err != nil {
		t.Fatal(err)
	}

	stub := &planStub{plan: plan}
	goal := &engine.Goal{ID: goalID, ProjectID: project, CreatedBy: owner,
		Status: engine.GoalDraft, RiskTier: engine.RiskR1}
	if _, err := NewIntake(stub, persona.DefaultCharacter(), config.EngineConfig{}, clock.System{}).
		Plan(ctx, f.pool, goal); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(stub.system, "steel, because it is going outdoors") {
		t.Fatalf("the planner never saw the answer.\n"+
			"`goal answer` tells people to replan so the plan is built on it; without this the "+
			"replan asks the same ambiguous question again and a roll that happens not to ask "+
			"produces a plan built on the ambiguity nobody resolved.\nPrompt was:\n%s", stub.system)
	}
	// The question too. An answer with no question is a sentence with no
	// subject: "steel" means nothing to a model that cannot see what it replies to.
	if !strings.Contains(stub.system, "aluminium or steel?") {
		t.Errorf("the answer reached the plan without the question it answers:\n%s", stub.system)
	}
}

// An UNANSWERED question is not settled, and must not read as though it were.
//
// The planner is the thing that asked it. Told "you asked this and nobody
// replied", a model either ignores it or reads it as permission to proceed on a
// guess — and proceeding on a guess is the entire failure RSN-02 exists to
// prevent. Left out, it sees the same ambiguity and asks again, which is right:
// at r2 and above the goal is held, and below it the question is already
// recorded as a labelled assumption.
func TestAnUnansweredQuestionIsNotPresentedAsSettled(t *testing.T) {
	f := newRecoveryFixture(t)
	ctx := context.Background()

	const asksAgain = `{"rationale":"still unclear","clarification_needed":"aluminium or steel?",
		"tasks":[]}`

	goalID := f.goalLike(t, "Build the bracket")
	var project, owner string
	if err := f.pool.QueryRow(ctx,
		`select project_id, created_by from forge_goals where id = $1`, goalID).
		Scan(&project, &owner); err != nil {
		t.Fatal(err)
	}
	if err := recordQuestion(ctx, f.pool, goalID, "aluminium or steel?"); err != nil {
		t.Fatal(err)
	}

	stub := &planStub{plan: asksAgain}
	goal := &engine.Goal{ID: goalID, ProjectID: project, CreatedBy: owner,
		Status: engine.GoalDraft, RiskTier: engine.RiskR1}
	if _, err := NewIntake(stub, persona.DefaultCharacter(), config.EngineConfig{}, clock.System{}).
		Plan(ctx, f.pool, goal); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stub.system, "has been answered") {
		t.Errorf("an outstanding question was put to the planner as settled:\n%s", stub.system)
	}
}

// The brief is composed from both, so neither requirement can silently lose its
// half of the prompt.
func TestBothHalvesOfWhatWasSettledAreCarried(t *testing.T) {
	settled := &Settled{
		Clarification: &clarificationHold{
			Question: "aluminium or steel?", Answer: "steel", Answered: true},
		Choice: &optionHold{
			Set:      &storedSet{Question: "how?", Options: tradeoff().Options},
			Criteria: twoCriteria(),
			Chosen:   "in-place",
		},
	}
	brief, unreadable := settled.Brief()
	if unreadable {
		t.Error("a readable choice was reported unreadable")
	}
	for _, want := range []string{"aluminium or steel?", "steel", "ALTER the live tables"} {
		if !strings.Contains(brief, want) {
			t.Errorf("the brief does not carry %q:\n%s", want, brief)
		}
	}
	// A goal nobody decided anything about contributes nothing.
	if brief, _ := (&Settled{}).Brief(); brief != "" {
		t.Errorf("a goal with nothing settled produced a brief: %q", brief)
	}
	if brief, _ := (*Settled)(nil).Brief(); brief != "" {
		t.Errorf("a nil settled read produced a brief: %q", brief)
	}
}

// The store answers for a goal that has neither, which is most goals.
func TestAGoalWithNothingSettledReadsCleanly(t *testing.T) {
	f := newRecoveryFixture(t)
	ctx := context.Background()

	settled, err := NewSettledStore(f.pool).For(ctx, f.goalID)
	if err != nil {
		t.Fatal(err)
	}
	if settled.Clarification != nil || settled.Choice != nil {
		t.Fatalf("a fresh goal reported something settled: %+v", settled)
	}
	if brief, _ := settled.Brief(); brief != "" {
		t.Errorf("a fresh goal produced a brief: %q", brief)
	}
	// And an unwired store is a deployment without one, not an error.
	if s, err := (*SettledStore)(nil).For(ctx, f.goalID); err != nil || s != nil {
		t.Errorf("a nil store did not answer as absent: %+v (%v)", s, err)
	}
	if _, err := NewSettledStore(nil).For(ctx, f.goalID); err != nil {
		t.Errorf("a store built on no pool did not answer as absent: %v", err)
	}
}
