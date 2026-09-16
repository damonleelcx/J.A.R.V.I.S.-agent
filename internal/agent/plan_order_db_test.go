package agent

import (
	"context"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
)

// A plan's tasks are listed in the order they were planned, whatever their keys.
//
// A planner's keys are kebab identifiers with no order of their own, so the order
// has to be written when the plan is applied. These keys sort backwards: listed
// by key, or however Postgres returns a tie, the plan comes back scrambled.
// docs/bugfix/2026-09-15-a-goals-tasks-were-listed-out-of-step-order.md
func TestApply_APlansTasksAreListedInThePlansOrder(t *testing.T) {
	h := newBuildHarness(t)
	goal := h.goal(t, nil)
	keys := []string{"survey-the-site", "pour-the-footings", "frame-the-walls", "close-the-roof",
		"clad-the-outside", "wire-the-rooms", "board-the-walls", "add-the-doors"}
	plan := &PlanResult{Rationale: "a house, in order"}
	for i, k := range keys {
		pt := PlannedTask{Key: k, Title: k, Instruction: "do " + k, RiskTier: string(engine.RiskR1)}
		if i > 0 {
			pt.DependsOn = []string{keys[i-1]}
		}
		plan.Tasks = append(plan.Tasks, pt)
	}
	if _, _, err := h.applier.Apply(context.Background(), h.pool, goal, plan, "planner"); err != nil {
		t.Fatal(err)
	}

	tasks, err := h.repo.ListTasks(context.Background(), h.pool, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, task := range tasks {
		got = append(got, task.IdempotencyKey)
	}
	if len(got) != len(keys) {
		t.Fatalf("%d task(s) listed for a plan of %d", len(got), len(keys))
	}
	for i := range keys {
		if got[i] != keys[i] {
			t.Fatalf("the plan is listed out of its order:\n got %v\nwant %v", got, keys)
		}
	}
}
