package engine_test

import (
	"context"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
)

// The fence issue 12 asks for: a plan with independent steps runs them in
// parallel, and a dependent one still serialises.
//
// # Why this did not exist
//
// Every piece of the concurrency was already tested on its own.
// TestConcurrentWorkersNeverShareATask proves two workers never get the SAME
// task; TestDependenciesGateReadiness proves a blocked task is not promoted.
// Neither proves the thing the product claims, which is the opposite of the
// first and stronger than the second: that two DIFFERENT tasks with no path
// between them are in flight AT THE SAME MOMENT. A queue that handed out one
// task at a time — a forgotten `limit 1` turned into a global lock, a promotion
// that serialised the whole goal, a claim that refused a second task while any
// task was claimed — would keep both existing tests green and make the task DAG
// decorative again from the other end.
//
// So this holds both halves in one test, because they are one claim. The
// planner's half of the same claim lives in internal/agent/plandeps_test.go, and
// internal/eval/scorers.go's someTasksCanStartAtOnce() measures the shape of
// real plans against it.
//
// Helpers here are prefixed `par` so this file can sit beside others in the
// package without colliding; the harness itself is the shared newHarness.

// TestQueue_TwoTasksWithNoPathBetweenThemAreHeldAtOnceAndAChainIsNot is the
// whole invariant, stated twice in the same schema so the two halves are
// measured against identical conditions.
func TestQueue_TwoTasksWithNoPathBetweenThemAreHeldAtOnceAndAChainIsNot(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	now := h.clk.Now()

	// ---------------------------------------------------------------------
	// Half one: no path between them, so both are held at once.
	// ---------------------------------------------------------------------
	//
	// Seeded 'pending' with no dependencies and promoted, rather than seeded
	// 'ready' directly. Promotion is half of what is under test: a
	// PromoteReadyTasks that refused to move two tasks in one pass, or that
	// gated on something other than forge_task_deps, would serialise a goal
	// before any worker ever saw it.
	free1 := h.addTask(t, "free-one", engine.StatusPending)
	free2 := h.addTask(t, "free-two", engine.StatusPending)

	promoted, err := h.queue.PromoteReadyTasks(ctx, h.pool, h.goalID, now)
	if err != nil {
		t.Fatal(err)
	}
	if promoted != 2 {
		t.Fatalf("promoting a goal whose two tasks depend on nothing made %d of them ready, want 2; "+
			"a plan with independent steps cannot run them in parallel if only one is ever offered",
			promoted)
	}

	// Two DIFFERENT lease owners, and the first lease is never released. If the
	// queue only ever allows one task in flight, the second claim comes back nil
	// here and the whole DAG is decorative.
	firstHolder, err := h.queue.Claim(ctx, h.pool, "worker-alpha", time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if firstHolder == nil {
		t.Fatal("nothing was claimable although two tasks are ready")
	}
	secondHolder, err := h.queue.Claim(ctx, h.pool, "worker-beta", time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if secondHolder == nil {
		t.Fatal("a second worker could claim nothing while the first still holds a task, although " +
			"the two tasks have no dependency between them. Independent work is being serialised, " +
			"and every plan costs as much wall clock as a chain")
	}
	if firstHolder.ID == secondHolder.ID {
		t.Fatalf("both workers were handed task %s", firstHolder.ID)
	}
	if !parIsOneOf(firstHolder.ID, free1.ID, free2.ID) || !parIsOneOf(secondHolder.ID, free1.ID, free2.ID) {
		t.Fatalf("the claimed tasks %s and %s are not the two that were seeded",
			firstHolder.ID, secondHolder.ID)
	}

	// "At once" is a claim about a MOMENT, not about a sequence of calls. Both
	// leases are read back from the database and checked to be live, held by
	// different owners, at one instant.
	if !parLeaseHeldNow(t, h, firstHolder.ID, "worker-alpha", now) ||
		!parLeaseHeldNow(t, h, secondHolder.ID, "worker-beta", now) {
		t.Fatal("the two tasks are not both under a live lease at the same instant; nothing here " +
			"shows independent work actually overlapping")
	}

	// Finish them so the chain below starts from a quiet queue.
	h.finish(t, firstHolder, engine.StatusSucceeded)
	h.finish(t, secondHolder, engine.StatusSucceeded)

	// ---------------------------------------------------------------------
	// Half two: a chain still serialises.
	// ---------------------------------------------------------------------
	//
	// Without this half the test above could be satisfied by deleting the
	// dependency gate entirely, which is the worse bug: two steps that edit the
	// same model running at the same time corrupt it silently. buildgoal.go
	// builds literal chains on purpose for exactly that reason.
	first := h.addTask(t, "chain-first", engine.StatusPending)
	second := h.addTask(t, "chain-second", engine.StatusPending, first.ID)

	if _, err := h.queue.PromoteReadyTasks(ctx, h.pool, h.goalID, h.clk.Now()); err != nil {
		t.Fatal(err)
	}
	if got := h.status(t, second.ID); got != engine.StatusPending {
		t.Fatalf("the second link of a chain is %q before the first has even been claimed, want "+
			"pending; a dependency that does not gate is a dependency that does nothing", got)
	}

	held, err := h.queue.Claim(ctx, h.pool, "worker-gamma", time.Minute, h.clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	if held == nil || held.ID != first.ID {
		t.Fatalf("claimed %v, want the first link of the chain", held)
	}

	// The decisive one: a second worker, while the first link is in flight, must
	// come away with nothing.
	blocked, err := h.queue.Claim(ctx, h.pool, "worker-delta", time.Minute, h.clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	if blocked != nil {
		t.Fatalf("worker-delta claimed %q while the task it depends on is still running. Two steps "+
			"that edit the same thing are now doing it at the same time, and the second will "+
			"overwrite or contradict the first with nothing reporting it", blocked.Title)
	}

	// And it becomes claimable the moment — and only the moment — the first one
	// finishes and promotion runs.
	h.finish(t, held, engine.StatusSucceeded)
	if _, err := h.queue.PromoteReadyTasks(ctx, h.pool, h.goalID, h.clk.Now()); err != nil {
		t.Fatal(err)
	}
	released, err := h.queue.Claim(ctx, h.pool, "worker-delta", time.Minute, h.clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	if released == nil || released.ID != second.ID {
		t.Fatalf("the second link is still not claimable after the first succeeded, so a chain "+
			"deadlocks rather than serialises: %v", released)
	}
}

// parIsOneOf keeps the identity checks above readable.
func parIsOneOf(id string, want ...string) bool {
	for _, w := range want {
		if id == w {
			return true
		}
	}
	return false
}

// parLeaseHeldNow re-reads the task from the database and asks whether that
// worker's lease is live at `now`.
//
// Re-read rather than trusting the struct the claim returned, because the claim
// returns what it wrote and the question here is what the DATABASE thinks — a
// claim that reported a lease it never persisted would otherwise pass.
func parLeaseHeldNow(t *testing.T, h *harness, taskID, worker string, now time.Time) bool {
	t.Helper()
	task, err := h.repo.GetTask(context.Background(), h.pool, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != engine.StatusClaimed {
		t.Errorf("task %s is %q, want claimed", taskID, task.Status)
		return false
	}
	return task.LeaseHeldBy(worker, now)
}
