package engine_test

import (
	"context"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
)

// TestHalfPropagatedFailureDoesNotUnblockDownstream covers the promote-side half
// of the skip-reason rule.
//
// # Why this state is reachable
//
// SkipTasksBlockedByFailure propagates in a loop of separate statements, one
// level per pass, because each pass depends on the previous one's writes. It is
// therefore NOT atomic across the whole chain: a worker that dies between passes
// leaves the graph half-propagated — `mid` skipped as unreachable, `leaf` still
// pending. On restart, PromoteReadyTasks may run before propagation resumes.
//
// If promotion treated every 'skipped' dependency as satisfied, `leaf` would
// become ready at that moment and the engine would resume work past a failure it
// was supposed to be blocked by. The guard in PromoteReadyTasks exists for
// exactly this window.
//
// An earlier version of this test ran full propagation first and was vacuous:
// propagation had already skipped `leaf`, so promotion never considered it and
// the test passed against deliberately broken code.
func TestHalfPropagatedFailureDoesNotUnblockDownstream(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	bad := h.addTask(t, "bad", engine.StatusReady)
	mid := h.addTask(t, "mid", engine.StatusPending, bad.ID)
	leaf := h.addTask(t, "leaf", engine.StatusPending, mid.ID)

	h.finish(t, bad, engine.StatusFailed)

	// Simulate a crash after the FIRST propagation pass: mid is skipped as
	// unreachable, leaf has not been reached yet.
	if _, err := h.pool.Exec(ctx, `
		update forge_tasks
		   set status='skipped', ended_at=$2, error_code=$3,
		       error_detail='half-propagated: worker died between passes'
		 where id=$1`, mid.ID, h.clk.Now(), engine.SkipReasonDependencyFailed); err != nil {
		t.Fatal(err)
	}
	if got := h.status(t, leaf.ID); got != engine.StatusPending {
		t.Fatalf("precondition: leaf should still be pending, got %q", got)
	}

	// Restart: promotion runs before propagation resumes.
	if _, err := h.queue.PromoteReadyTasks(ctx, h.pool, h.goalID, h.clk.Now()); err != nil {
		t.Fatal(err)
	}

	if got := h.status(t, leaf.ID); got == engine.StatusReady {
		t.Fatal("a task whose dependency was skipped as UNREACHABLE became ready. " +
			"The engine would resume work past a failure it was supposed to be blocked by.")
	}
	if claimed, err := h.queue.Claim(ctx, h.pool, "worker", 60_000_000_000, h.clk.Now()); err != nil {
		t.Fatal(err)
	} else if claimed != nil && claimed.ID == leaf.ID {
		t.Fatal("the blocked task was handed to a worker")
	}
}
