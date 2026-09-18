package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
)

// ‼️ A build goal stops BEFORE a call that would pass its ceiling, not after it
// (2026-09-17; docs/spikes/2026-09-17-live-verification, follow-up 4).
//
// Every call here costs 100 and the ceiling is 350. The plan and steps 1 and 2 spend
// 300. The old rule refused only at 350, so step 3's call was placed and the goal
// ended at 400. Now step 3's call may cost 125 (the largest call, 100, and a quarter),
// 50 are left, and it is not placed: the goal stops at 300 with the budget-stop text
// saying how much was left and why.
func TestBuildGoal_AGoalStopsBeforeACallThatWouldPassItsCeiling(t *testing.T) {
	h := newBuildHarness(t)
	ceiling := int64(350)
	goal := h.goal(t, &ceiling)
	stub := &goalStub{tokens: 100, replies: []string{threeSteps,
		wholeDoc("chassis", "Chassis"), addPart("wheels", "Wheels"), addPart("body", "Body")}}
	h.plan(t, goal, stub)

	if status := h.settle(t, h.worker(t, stub), goal.ID); status != string(engine.GoalFailed) {
		t.Fatalf("a build stopped by its budget ended %s", status)
	}
	var spent, largest int64
	if err := h.pool.QueryRow(context.Background(), `select tokens_spent, largest_call_tokens from forge_goals where id = $1`,
		goal.ID).Scan(&spent, &largest); err != nil {
		t.Fatal(err)
	}
	if spent > ceiling {
		t.Fatalf("the goal spent %d of a %d ceiling", spent, ceiling)
	}
	if n, _ := stub.calls(); n != 3 || spent != 300 || largest != 100 {
		t.Errorf("%d call(s), %d spent, largest %d; want the plan and two steps (3 calls, 300) and step 3 never asked",
			n, spent, largest)
	}
	tasks := h.tasks(t, goal.ID)
	if tasks[0].Status != engine.StatusSucceeded || tasks[1].Status != engine.StatusSucceeded ||
		tasks[2].Status != engine.StatusFailed {
		t.Fatalf("steps ended %s, %s, %s", tasks[0].Status, tasks[1].Status, tasks[2].Status)
	}
	why := "50 tokens were left, and the next model call was not placed because it may cost 125 " +
		"(the largest call this goal has made, 100 tokens, and a quarter more), which would pass the ceiling. " +
		"The goal stops here rather than spend past it."
	if !strings.Contains(tasks[2].ErrorDetail, "goal budget exhausted on tokens: used 300 tokens of 350. "+why) {
		t.Errorf("step 3 failed with %q; it must say how much was left and why the call was not placed", tasks[2].ErrorDetail)
	}
	var said string
	if err := h.pool.QueryRow(context.Background(), `select summary from forge_events where goal_id = $1 and kind = $2`,
		goal.ID, engine.EventBudgetExceeded).Scan(&said); err != nil {
		t.Fatalf("the timeline does not say the budget stopped the build: %v", err)
	}
	if said != "Budget exhausted on tokens: used 300 tokens of 350. "+why {
		t.Errorf("the timeline says %q", said)
	}
}

// Inside a step, a call that cannot fit is not placed, and the step fails for it with
// the stop's text: the reservation is the client's, not only the worker's before a step.
func TestBuildGoal_AStepDoesNotPlaceARepairItsCeilingCannotPay(t *testing.T) {
	h := newBuildHarness(t)
	ceiling := int64(180)
	goal := h.goal(t, &ceiling)
	if _, _, err := h.applier.Apply(context.Background(), h.pool, goal,
		buildPlan("a car", []buildTask{{Name: "chassis", What: "the chassis"}, {Name: "body", What: "the body"}}), "planner"); err != nil {
		t.Fatal(err)
	}
	// A sweep whose outline is a line: faulty, so the step asks for a repair.
	broken := `{"speech":"x","prototype":{"name":"m","units":"mm","parts":[
	  {"id":"chassis","name":"Chassis","shape":"sweep","profile":[{"x":0,"y":0},{"x":10,"y":0}],
	   "path":[{"x":0,"y":0,"z":0},{"x":0,"y":0,"z":100}]}]}}`
	stub := &goalStub{tokens: 100, replies: []string{broken, broken, broken}}
	if err := h.applier.Activate(context.Background(), h.pool, goal, engine.ActorHuman, nil); err != nil {
		t.Fatal(err)
	}
	if status := h.settle(t, h.worker(t, stub), goal.ID); status != string(engine.GoalFailed) {
		t.Fatalf("a build stopped by its budget ended %s", status)
	}

	// The step's call: nothing known yet, so it is placed. Its repair may cost 125 with
	// 80 left: the old rule (100 < 180) placed it and the goal ended at 200 of 180.
	if n, _ := stub.calls(); n != 1 {
		t.Errorf("the step made %d model calls; its repair cannot fit and must not be asked", n)
	}
	why := "80 tokens were left, and the next model call was not placed because it may cost 125"
	if first := h.tasks(t, goal.ID)[0]; first.Status != engine.StatusFailed || !strings.Contains(first.ErrorDetail, why) {
		t.Errorf("the step ended %s with %q", first.Status, first.ErrorDetail)
	}
	// And the timeline says so, as it does for a step stopped before it began: the card
	// shows this summary as the reason the build stopped.
	var said string
	if err := h.pool.QueryRow(context.Background(), `select summary from forge_events where goal_id = $1 and kind = $2`,
		goal.ID, engine.EventBudgetExceeded).Scan(&said); err != nil {
		t.Fatalf("the timeline does not say the budget stopped the step part-way: %v", err)
	}
	if !strings.HasPrefix(said, "Budget exhausted on tokens: used 100 tokens of 180. "+why) {
		t.Errorf("the timeline says %q", said)
	}
}
