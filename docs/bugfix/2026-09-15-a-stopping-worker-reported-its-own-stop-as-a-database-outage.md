# A stopping worker reported its own stop as a database outage, and could leave the next step pending

**Date:** 2026-09-15 · **Status:** fixed (stacked on #90, stage A1 follow-ups) · **Severity:** low: a misleading
warning on every graceful stop mid-task, and a step released late when a task finished as the stop arrived

## Summary

When `forge-worker` is stopped gracefully (SIGINT/SIGTERM, or Ctrl-Break on Windows) while a task runs, the task is
handed back to the queue at once — that part works. `Worker.Run` then carried on to what the end of a task sets
moving, `releaseWaiting` and `settleGoal`, on the context the stop had just cancelled. Both database calls failed
immediately and each was logged as `DATABASE_UNAVAILABLE`. The same two calls, for a task that had *finished* in the
instant the stop arrived, failed the same way, so the tasks waiting on it stayed `pending` until some worker's idle
poll released them.

The fix runs both on a context that outlives the stop (`context.WithoutCancel`, bounded at 10 s), in a new
`Worker.afterTask`.

## Symptom

Seen live, stopping a real `forge-worker` with a console `CTRL_BREAK_EVENT` 14 s into step 2 of a build goal
([`docs/spikes/2026-09-15-build-goal-exercised`](../spikes/2026-09-15-build-goal-exercised),
`data/worker-1-stub-stopped.log`):

```
14:10:37.475 INFO forge.worker.stopping  detail="finishing in-flight tasks; they are released back to the queue ..."
14:10:37.481 WARN forge.task.release_failed error="engine.Queue.PromoteReadyTasks: DATABASE_UNAVAILABLE: The database could not be reached or refused the connection.: context canceled"
14:10:37.481 WARN forge.goal.settle_failed  error="engine.Queue.Depth: DATABASE_UNAVAILABLE: The database could not be reached or refused the connection.: context canceled"
14:10:37.481 INFO forge.worker.stopped
```

The database was up throughout: the step had been handed back 6 ms earlier through the same pool, and a new worker
claimed it 7 s later.

## Impact

- **Every graceful stop during a task logs two warnings that name the wrong cause**, with the remedy "Verify the
  database is running and FORGE_DATABASE_URL is correct". A rolling deploy of N workers mid-build writes 2N of them.
  An operator reading the log after a deploy is sent to look at a healthy database.
- **A task that finished as the stop arrived left its dependents pending.** Nothing is lost — the idle poll's
  `releaseWaitingGoals` sweep releases them — but only once a worker is idle, so a busy fleet can take much longer
  than one poll interval to start the next step of a build.

## Preconditions

A worker whose context is cancelled while `runTask` is in progress (any graceful stop that lands during a task).
An idle worker is unaffected: it returns from its sleep without reaching these calls.

## Root cause

`internal/agent/worker.go` `Run`:

```go
w.runTask(ctx, task)
w.releaseWaiting(ctx, goalID)
w.settleGoal(ctx, goalID)
```

`runBuildStep` already hands its task back on `context.WithoutCancel(ctx)`, because a hand-back that honours the
cancellation cannot be written. The two calls after it were never given the same treatment. pgx returns
`context canceled` before touching the connection, and `db` classifies every connection-level failure as
`DATABASE_UNAVAILABLE`.

## Why it did not show up before

- **The stop fence reads rows, not logs.** `TestBuildGoal_AStoppedWorkerHandsItsStepBack` checks that step 2 is
  `ready` with no lease owner, which it is. The warnings went to `t.Log` and were visible only under `-v`.
- **A graceful stop had never been exercised live.** The only live stop so far
  ([`2026-09-15-build-goal-live`](../spikes/2026-09-15-build-goal-live)) was `taskkill /F`, which kills the process
  before any of this runs; that spike records that a console signal could not be sent from its driver.
- **The finished-at-the-stop case needs a stop inside a few milliseconds** between the task's last write and the
  release. No test or run aimed at that window.

## Fix

- `Worker.afterTask(ctx, goalID)` does what the end of a task sets moving, on
  `context.WithTimeout(context.WithoutCancel(ctx), afterTaskTimeout)` (10 s). The bound keeps "outlives the stop"
  from becoming "outlives the process": forge-worker gives its loops 30 s to return.
- `Run` calls it after every `runTask`. Neither `releaseWaiting` nor `settleGoal` changed: for a task that was handed
  back, `PromoteReadyTasks` promotes nothing and `settleGoal` sees outstanding work and returns, exactly as they do
  for any unfinished task.

## Verification

- `TestWorker_AStoppingWorkerStillReleasesWhatItsLastTaskLeftWaiting` (internal/agent/worker_stop_db_test.go) finishes
  step 1 of a three-step build, calls `afterTask` with an already-cancelled context, and requires step 2 to be `ready`
  and nothing logged as `DATABASE_UNAVAILABLE`.
- `TestBuildGoal_AStoppedWorkerDoesNotReportTheDatabaseUnavailable` stops a real worker blocked inside step 2's model
  call and requires no `forge.task.release_failed`, `forge.goal.settle_failed` or `DATABASE_UNAVAILABLE` in its
  warnings, and step 2 handed back.
- Both went red with the fix reverted (drill below), both pass with it, and the rest of `TestBuildGoal_*` and
  `TestWorker_ATaskLeftWaitingByACrashIsReleasedOnTheIdlePoll` still pass.

## Regression prevention

Drill "a stopping worker's bookkeeping runs on the cancelled context" in `scripts/drill-fences.sh` replaces the
outliving context with `ctx`; both fences go red. The existing drill "a finished task releases nothing" was
re-anchored to the call's new place in `afterTask` and still goes red.

## Not in this fix

- **Main.** `origin/main`'s `Run` calls `settleGoal(ctx, goalID)` straight after `runTask` in the same way, so a
  graceful stop mid-task there is expected to log the settle warning too. Not run on main.
- **Other calls on the stop path.** `runTask`'s own heartbeat and event writes after cancellation were not audited;
  the live stop logged nothing else.
- **The classification.** A cancelled context is still reported as `DATABASE_UNAVAILABLE` wherever else it reaches
  `db`. Mapping `context.Canceled` to its own code would be a wider change to `internal/platform/db`.
