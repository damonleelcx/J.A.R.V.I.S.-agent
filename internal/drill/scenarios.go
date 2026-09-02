package drill

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// The drills. Each injects a real fault into a real schema and then asks NFR-07's
// four questions of what is left: was state preserved, were dependents stopped
// safely, are partial results visible, and does anything claim completion.

func init() {
	Register(Scenario{
		Name: "worker-dies-holding-a-lease",
		Describes: "A worker claims a task, writes a checkpoint, and disappears. With attempts left " +
			"the task must return to the queue with its checkpoint intact; with none left it must " +
			"fail saying the WORKER stopped responding rather than that the work was wrong. " +
			"Nothing may report the work as done in either case.",
		Run: workerDies,
	})
	Register(Scenario{
		Name: "dependency-fails-terminally",
		Describes: "A task fails with no attempts left. Everything downstream must be stopped rather " +
			"than left pending forever, the goal must settle as failed, and the work that DID " +
			"succeed must still be readable.",
		Run: dependencyFails,
	})
	Register(Scenario{
		Name: "goal-settles-with-a-failure",
		Describes: "A goal whose tasks did not all succeed must never settle as succeeded, and its " +
			"outcome must say how much finished rather than only that it failed.",
		Run: goalNeverClaimsFalseSuccess,
	})
	Register(Scenario{
		Name: "checkpoint-is-unreadable",
		Describes: "A checkpoint is corrupted between attempts. The task must still be recoverable, " +
			"the corruption must be visible rather than silently treated as an empty state, and " +
			"nothing must report the task as complete.",
		Run: checkpointCorrupted,
	})
}

// workerDies claims a task, checkpoints, then abandons the lease.
func workerDies(ctx context.Context, h *Harness) (*Result, error) {
	res := &Result{Scenario: "worker-dies-holding-a-lease"}
	repo, queue := engine.NewRepository(), engine.NewQueue()

	// Two tasks, because a worker dying means two different things depending on
	// whether anything is left to retry with, and only one of them is recovery.
	retryable, err := h.addTaskWithAttempts(ctx, "worked on, with attempts left", nil, 3)
	if err != nil {
		return nil, err
	}
	lastChance, err := h.addTaskWithAttempts(ctx, "worked on, with no attempts left", nil, 1)
	if err != nil {
		return nil, err
	}
	if _, err := queue.PromoteReadyTasks(ctx, h.Pool, h.GoalID, h.Now); err != nil {
		return nil, err
	}

	claimed := map[string]bool{}
	for i := 0; i < 2; i++ {
		task, err := queue.Claim(ctx, h.Pool, fmt.Sprintf("worker-that-will-die-%d", i), time.Minute, h.Now)
		if err != nil {
			return nil, err
		}
		if task == nil {
			break
		}
		claimed[task.ID] = true
		state, _ := json.Marshal(map[string]any{"step": 2, "note": "half way through " + task.Title})
		if _, err := repo.SaveCheckpoint(ctx, h.Pool, task.ID,
			engine.CheckpointIterationEnd, state, h.Now); err != nil {
			return nil, err
		}
	}
	if !claimed[retryable.ID] || !claimed[lastChance.ID] {
		return nil, errs.New("drill.workerDies", errs.CodeInvariantViolated).
			WithDetail("only %d of 2 tasks could be claimed, so the drill could not begin", len(claimed))
	}

	// The fault: both workers are gone. Time passes and the leases expire.
	h.Advance(2 * time.Minute)
	reaped, err := queue.ReapExpiredLeases(ctx, h.Pool, h.Now, 10)
	if err != nil {
		return nil, err
	}
	// PROVE it landed. A reaper that found nothing means the leases never
	// expired, and every check below would be about an undisturbed system.
	if len(reaped) == 0 {
		return res, nil // no evidence → Passed() is false, and the report says why
	}
	res.FaultEvidence = fmt.Sprintf("%d lease(s) expired and were reaped after their workers stopped responding",
		len(reaped))

	recovered, err := repo.GetTask(ctx, h.Pool, retryable.ID)
	if err != nil {
		return nil, err
	}
	exhausted, err := repo.GetTask(ctx, h.Pool, lastChance.ID)
	if err != nil {
		return nil, err
	}
	ckpt, ckptErr := repo.LatestCheckpoint(ctx, h.Pool, retryable.ID)

	res.Checks = append(res.Checks,
		check("state is preserved",
			ckptErr == nil && ckpt != nil && len(ckpt.State) > 0,
			"the checkpoint written before the worker died is %s", describeCheckpoint(ckpt, ckptErr)),
		check("work is recoverable",
			recovered.Status == engine.StatusReady || recovered.Status == engine.StatusPending,
			"with attempts left, the task is %s, so another worker can pick it up", recovered.Status),
		check("no lease is still held",
			recovered.LeaseOwner == nil && exhausted.LeaseOwner == nil,
			"both leases are released"),
		check("completion is not implied",
			recovered.Status != engine.StatusSucceeded && exhausted.Status != engine.StatusSucceeded,
			"the tasks are %s and %s; neither claims to have finished the work",
			recovered.Status, exhausted.Status),
		// The sibling of "never imply completion": do not imply the wrong CAUSE.
		// A crashed worker recorded identically to a genuine work failure sends
		// the next person to debug the instruction rather than the infrastructure.
		check("the cause is not misattributed",
			exhausted.Status == engine.StatusFailed &&
				strings.Contains(exhausted.ErrorCode, "LEASE_EXPIRED"),
			"out of attempts it failed as %q — the worker stopping is recorded as that, "+
				"rather than as the work being wrong", exhausted.ErrorCode),
	)
	return res, nil
}

// dependencyFails exhausts a task's attempts and checks what happens downstream.
func dependencyFails(ctx context.Context, h *Harness) (*Result, error) {
	res := &Result{Scenario: "dependency-fails-terminally"}
	repo, queue := engine.NewRepository(), engine.NewQueue()

	done, err := h.addTask(ctx, "work that finishes", nil)
	if err != nil {
		return nil, err
	}
	doomed, err := h.addTask(ctx, "work that fails", nil)
	if err != nil {
		return nil, err
	}
	downstream, err := h.addTask(ctx, "work that waits on the failure", []string{doomed.ID})
	if err != nil {
		return nil, err
	}

	if _, err := queue.PromoteReadyTasks(ctx, h.Pool, h.GoalID, h.Now); err != nil {
		return nil, err
	}
	if err := h.finish(ctx, repo, queue, done.ID, engine.StatusSucceeded); err != nil {
		return nil, err
	}

	// The fault: a task fails with nothing left to retry.
	if err := h.finish(ctx, repo, queue, doomed.ID, engine.StatusFailed); err != nil {
		return nil, err
	}
	failed, err := repo.GetTask(ctx, h.Pool, doomed.ID)
	if err != nil {
		return nil, err
	}
	if failed.Status != engine.StatusFailed {
		return res, nil // the injection did not take; no evidence
	}
	res.FaultEvidence = fmt.Sprintf("task %q reached %s terminally", failed.Title, failed.Status)

	skipped, err := queue.SkipTasksBlockedByFailure(ctx, h.Pool, h.GoalID, h.Now)
	if err != nil {
		return nil, err
	}
	blocked, err := repo.GetTask(ctx, h.Pool, downstream.ID)
	if err != nil {
		return nil, err
	}
	survivor, err := repo.GetTask(ctx, h.Pool, done.ID)
	if err != nil {
		return nil, err
	}

	res.Checks = append(res.Checks,
		check("dependents are stopped safely",
			blocked.Status == engine.StatusSkipped,
			"the task waiting on the failure is %s (%d skipped in this sweep)", blocked.Status, skipped),
		check("dependents are not left waiting forever",
			blocked.Status.Terminal(),
			"its status %s is terminal, so nothing is waiting on work that will never come", blocked.Status),
		check("partial results survive",
			survivor.Status == engine.StatusSucceeded,
			"the task that finished before the failure is still %s", survivor.Status),
		check("completion is not implied",
			blocked.Status != engine.StatusSucceeded,
			"the skipped task is %s, which is distinguishable from succeeded", blocked.Status),
	)
	return res, nil
}

// goalNeverClaimsFalseSuccess settles a goal whose work did not all succeed.
func goalNeverClaimsFalseSuccess(ctx context.Context, h *Harness) (*Result, error) {
	res := &Result{Scenario: "goal-settles-with-a-failure"}
	repo, queue := engine.NewRepository(), engine.NewQueue()

	good, err := h.addTask(ctx, "work that finishes", nil)
	if err != nil {
		return nil, err
	}
	bad, err := h.addTask(ctx, "work that fails", nil)
	if err != nil {
		return nil, err
	}
	if _, err := queue.PromoteReadyTasks(ctx, h.Pool, h.GoalID, h.Now); err != nil {
		return nil, err
	}
	if err := h.finish(ctx, repo, queue, good.ID, engine.StatusSucceeded); err != nil {
		return nil, err
	}
	if err := h.finish(ctx, repo, queue, bad.ID, engine.StatusFailed); err != nil {
		return nil, err
	}

	depth, err := queue.Depth(ctx, h.Pool, h.GoalID)
	if err != nil {
		return nil, err
	}
	if depth[engine.StatusFailed] == 0 {
		return res, nil // nothing failed; there is no degradation to observe
	}
	res.FaultEvidence = fmt.Sprintf("the goal finished with %d failed and %d succeeded task(s)",
		depth[engine.StatusFailed], depth[engine.StatusSucceeded])

	// Settle it the way the worker does, then read the goal back.
	if err := h.settle(ctx, depth); err != nil {
		return nil, err
	}
	goal, err := h.goal(ctx)
	if err != nil {
		return nil, err
	}

	res.Checks = append(res.Checks,
		check("completion is not implied",
			goal.Status != engine.GoalSucceeded,
			"the goal settled as %s", goal.Status),
		check("the outcome is stated",
			goal.OutcomeSummary != "",
			"outcome_summary is %q", goal.OutcomeSummary),
		check("partial results are exposed",
			containsCount(goal.OutcomeSummary, depth[engine.StatusSucceeded]),
			"the summary says how much succeeded rather than only that the goal failed: %q",
			goal.OutcomeSummary),
		check("the failure is attributable",
			goal.FailureCode != "",
			"failure_code is %q", goal.FailureCode),
	)
	return res, nil
}

// checkpointCorrupted writes an undecodable checkpoint and checks the task is
// still recoverable and still not reported complete.
func checkpointCorrupted(ctx context.Context, h *Harness) (*Result, error) {
	res := &Result{Scenario: "checkpoint-is-unreadable"}
	repo, queue := engine.NewRepository(), engine.NewQueue()

	task, err := h.addTask(ctx, "the one with a bad checkpoint", nil)
	if err != nil {
		return nil, err
	}
	if _, err := queue.PromoteReadyTasks(ctx, h.Pool, h.GoalID, h.Now); err != nil {
		return nil, err
	}
	state, _ := json.Marshal(map[string]any{"messages": []string{"a"}})
	if _, err := repo.SaveCheckpoint(ctx, h.Pool, task.ID,
		engine.CheckpointIterationEnd, state, h.Now); err != nil {
		return nil, err
	}

	// The fault: the stored state stops being what the code expects. jsonb will
	// not hold invalid JSON, so the corruption is a VALID document of the wrong
	// shape — which is the realistic failure anyway: a schema change, a rollback
	// to an older binary, a bug in whatever wrote it.
	tag, err := h.Pool.Exec(ctx,
		`update forge_checkpoints set state = '"not the shape anybody expects"'::jsonb where task_id = $1`,
		task.ID)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return res, nil // nothing was corrupted; no evidence
	}
	res.FaultEvidence = fmt.Sprintf("%d checkpoint(s) for %q were rewritten into a shape the executor cannot resume from",
		tag.RowsAffected(), task.Title)

	ckpt, ckptErr := repo.LatestCheckpoint(ctx, h.Pool, task.ID)
	after, err := repo.GetTask(ctx, h.Pool, task.ID)
	if err != nil {
		return nil, err
	}
	var decoded struct {
		Messages []any `json:"messages"`
	}
	decodeErr := error(nil)
	if ckpt != nil {
		decodeErr = json.Unmarshal(ckpt.State, &decoded)
	}

	res.Checks = append(res.Checks,
		check("the corruption is visible",
			ckptErr != nil || decodeErr != nil,
			"reading it back %s, rather than yielding an empty state that looks like a fresh start",
			describeDecode(ckptErr, decodeErr)),
		check("work is recoverable",
			after.Status == engine.StatusReady || after.Status == engine.StatusPending,
			"the task is %s, so it can be attempted again from its instruction", after.Status),
		check("completion is not implied",
			after.Status != engine.StatusSucceeded,
			"the task is %s", after.Status),
	)
	return res, nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func describeCheckpoint(c *engine.Checkpoint, err error) string {
	if err != nil {
		return "unreadable: " + err.Error()
	}
	if c == nil {
		return "GONE"
	}
	return fmt.Sprintf("still there (%d bytes, seq %d, kind %s)", len(c.State), c.Seq, c.Kind)
}

func describeLease(t *engine.Task) string {
	if t.LeaseOwner == nil {
		return "released"
	}
	return "still held by " + *t.LeaseOwner
}

func describeDecode(readErr, decodeErr error) string {
	switch {
	case readErr != nil:
		return "failed: " + readErr.Error()
	case decodeErr != nil:
		return "failed to decode: " + decodeErr.Error()
	default:
		return "succeeded, which it should not have"
	}
}

// containsCount reports whether a summary mentions a number, so "partial results
// are exposed" is checked against the text a person actually reads rather than
// against a field nobody renders.
func containsCount(summary string, n int) bool {
	return contains(summary, fmt.Sprintf("%d succeeded", n))
}

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }
