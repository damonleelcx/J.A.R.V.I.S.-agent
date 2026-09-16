package agent_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/agent"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// A worker's identity is what its lease rows name, and every lease guard —
// Claim writes it, Heartbeat and Release compare against it — can only tell two
// workers apart by it. Two workers that share one are one worker to the queue: a
// worker whose lease lapsed and was reclaimed by its sibling could still extend
// it or hand it back.
//
// forge-worker starts FORGE_WORKER_CONCURRENCY workers in one loop, in the same
// millisecond, so "started together" is the production case, not a corner.
// docs/bugfix/2026-09-15-workers-started-together-shared-one-lease-identity.md
func TestNewWorker_WorkersStartedTogetherHaveDistinctIdentities(t *testing.T) {
	seen := map[string]int{}
	for i := 0; i < 64; i++ {
		w := agent.NewWorker(agent.WorkerDeps{})
		if prev, dup := seen[w.ID]; dup {
			t.Fatalf("workers %d and %d, started together, are both %q: their leases cannot be told apart",
				prev, i, w.ID)
		}
		seen[w.ID] = i
	}
}

// The same property, where it matters: on the real queue, a worker cannot
// extend or hand back a lease its sibling holds — neither while the sibling's
// lease is live, nor after its own lapsed and the sibling reclaimed the task.
//
// # Why through NewWorker and not hand-written ids
//
// The queue's guards were already fenced (TestHeartbeatRefusesAStolenLease), with
// "worker-slow" and "worker-fast" written by the test, which are distinct by
// construction. That is exactly why this defect was never seen: the guard was
// right and the names it compared were not. Only ids minted by NewWorker, the
// way forge-worker mints them, test what production compares.
func TestWorker_ASiblingCannotExtendOrReleaseALeaseItDoesNotHold(t *testing.T) {
	if os.Getenv("FORGE_TEST_DATABASE_URL") == "" {
		t.Skip("FORGE_TEST_DATABASE_URL is unset")
	}
	h := newGateHarness(t)
	ctx := context.Background()

	goal := h.createGoal(t, "Contested lease", "one task, two workers from one process",
		engine.AutonomySandboxExecute, engine.RiskR1)
	plan := &agent.PlanResult{
		Rationale: "one task",
		Tasks:     []agent.PlannedTask{{Key: "contested", Title: "Contested", Instruction: "do it", RiskTier: "r1"}},
	}
	if _, _, err := h.applier.Apply(ctx, h.pool, goal, plan, "planner"); err != nil {
		t.Fatal(err)
	}
	if err := h.applier.Activate(ctx, h.pool, goal, engine.ActorHuman, nil); err != nil {
		t.Fatal(err)
	}

	// Made one after the other, as forge-worker makes them.
	first := agent.NewWorker(agent.WorkerDeps{})
	sibling := agent.NewWorker(agent.WorkerDeps{})

	const lease = time.Minute
	// Microseconds, because that is what Postgres stores: a nanosecond `now`
	// compared against a stored expiry is off by the truncation.
	now := time.Now().UTC().Truncate(time.Microsecond)

	task, err := h.queue.Claim(ctx, h.pool, first.ID, lease, now)
	if err != nil {
		t.Fatal(err)
	}
	if task == nil {
		t.Fatal("nothing was claimed although the goal has a ready task")
	}

	// While the first worker's lease is live, its sibling may not touch it.
	if err := h.queue.Heartbeat(ctx, h.pool, task.ID, sibling.ID, 10*lease, now); !errs.Is(err, errs.CodeConflict) {
		t.Fatalf("worker %q extended a lease held by its sibling %q (err = %v)", sibling.ID, first.ID, err)
	}
	if err := h.queue.Release(ctx, h.pool, task.ID, sibling.ID, now); !errs.Is(err, errs.CodeConflict) {
		t.Fatalf("worker %q released a task its sibling %q holds (err = %v)", sibling.ID, first.ID, err)
	}
	h.requireLease(t, task.ID, first.ID, now.Add(lease))

	// The first worker wedges past its lease; the reaper returns the task and the
	// sibling claims it. The first worker then wakes and tries to carry on.
	later := now.Add(2 * lease)
	if _, err := h.queue.ReapExpiredLeases(ctx, h.pool, later, 10); err != nil {
		t.Fatal(err)
	}
	if reclaimed, err := h.queue.Claim(ctx, h.pool, sibling.ID, lease, later); err != nil {
		t.Fatal(err)
	} else if reclaimed == nil || reclaimed.ID != task.ID {
		t.Fatalf("the sibling did not reclaim the lapsed task (got %v)", reclaimed)
	}

	if err := h.queue.Heartbeat(ctx, h.pool, task.ID, first.ID, 10*lease, later); !errs.Is(err, errs.CodeConflict) {
		t.Fatalf("worker %q, whose lease lapsed, extended the lease its sibling %q reclaimed (err = %v): "+
			"two workers now run one task and the lease will not lapse again", first.ID, sibling.ID, err)
	}
	if err := h.queue.Release(ctx, h.pool, task.ID, first.ID, later); !errs.Is(err, errs.CodeConflict) {
		t.Fatalf("worker %q, whose lease lapsed, released the task its sibling %q is running (err = %v)",
			first.ID, sibling.ID, err)
	}
	h.requireLease(t, task.ID, sibling.ID, later.Add(lease))
}

// requireLease fails unless the task is still leased to owner until expires.
func (h *liveHarness) requireLease(t *testing.T, taskID, owner string, expires time.Time) {
	t.Helper()
	got, err := h.repo.GetTask(context.Background(), h.pool, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LeaseOwner == nil || *got.LeaseOwner != owner {
		t.Fatalf("task %s is leased to %v, want %q", taskID, got.LeaseOwner, owner)
	}
	if got.LeaseExpiresAt == nil || !got.LeaseExpiresAt.Equal(expires) {
		t.Fatalf("task %s's lease expires at %v, want %v: someone who does not hold it moved it",
			taskID, got.LeaseExpiresAt, expires)
	}
}
