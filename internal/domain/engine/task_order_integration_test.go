package engine_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
)

// A build's tasks written at one instant are listed in step order.
//
// Rows planned before PlanApplier stamped plan order into created_at share one
// timestamp, and the console listed a live build as 1, 2, 5, 3, 4. Twelve steps
// written out of order at one instant: the chance of listing in order by luck is
// one in 479,001,600.
// docs/bugfix/2026-09-15-a-goals-tasks-were-listed-out-of-step-order.md
func TestListTasks_ABuildsStepsWrittenAtOneInstantAreListedInStepOrder(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	now := h.clk.Now()

	for _, n := range []int{7, 3, 12, 1, 9, 5, 11, 2, 8, 4, 10, 6} {
		task := &engine.Task{
			ID: id.New(id.PrefixTask), GoalID: h.goalID, PlanID: h.planID,
			Title: fmt.Sprintf("Step %d of 12", n), Instruction: "build",
			Status: engine.StatusPending, IdempotencyKey: fmt.Sprintf("build-step-%02d", n),
			MaxAttempts: 3, NotBefore: now, Priority: 100,
			RiskTier: engine.RiskR1, CreatedAt: now, UpdatedAt: now,
		}
		if err := h.repo.CreateTask(ctx, h.pool, task, nil); err != nil {
			t.Fatal(err)
		}
	}

	tasks, err := h.repo.ListTasks(ctx, h.pool, h.goalID)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, task := range tasks {
		got = append(got, task.IdempotencyKey)
	}
	for i, task := range tasks {
		if want := fmt.Sprintf("build-step-%02d", i+1); task.IdempotencyKey != want {
			t.Fatalf("a build's steps are listed out of order: %v", got)
		}
	}
}
