# A stopping worker reported its own stop as a database outage, and could leave the next task pending

**Date:** 2026-09-15 · **Status:** fixed on the #87 branch (`fix/engine-release-budget-chain`), found by #104 ·
**Severity:** low: a misleading warning on every graceful stop mid-task, and a task released late when the one before
it finished as the stop arrived

## Summary

When `forge-worker` is stopped gracefully (SIGINT/SIGTERM, or Ctrl-Break on Windows) while a task runs, `Worker.Run`
carried on to what the end of a task sets moving, `releaseWaiting` and `settleGoal`, on the context the stop had just
cancelled. Both database calls failed immediately and each was logged as `DATABASE_UNAVAILABLE`. The same two calls,
for a task that had *finished* in the instant the stop arrived, failed the same way, so the tasks waiting on it stayed
`pending` until some worker's idle poll released them, and a goal whose last task it was stayed `active` until some
worker's idle sweep settled it.

The fix runs both on a context that outlives the stop (`context.WithoutCancel`, bounded at 10 s), in a new
`Worker.afterTask`.

## Symptom

Seen live on the stacked stage A1 branch (#104), stopping a real `forge-worker` with a console `CTRL_BREAK_EVENT` 14 s
into step 2 of a build goal (`docs/spikes/2026-09-15-build-goal-exercised/data/worker-1-stub-stopped.log` there):

```
14:10:37.475 INFO forge.worker.stopping  detail="finishing in-flight tasks; they are released back to the queue ..."
14:10:37.481 WARN forge.task.release_failed error="engine.Queue.PromoteReadyTasks: DATABASE_UNAVAILABLE: The database could not be reached or refused the connection.: context canceled"
14:10:37.481 WARN forge.goal.settle_failed  error="engine.Queue.Depth: DATABASE_UNAVAILABLE: The database could not be reached or refused the connection.: context canceled"
14:10:37.481 INFO forge.worker.stopped
```

The database was up throughout: a new worker claimed the step 7 s later through the same database.

Reproduced on this branch by `TestWorker_AWorkerStoppedMidTaskDoesNotReportItsBookkeepingAsADatabaseFailure`, which
stops a real worker blocked inside an executor model call. Without the fix it logs the same two lines:

```
level=WARN msg=forge.task.release_failed error="engine.Queue.PromoteReadyTasks: DATABASE_UNAVAILABLE: ...: context canceled"
level=WARN msg=forge.goal.settle_failed  error="engine.Queue.Depth: DATABASE_UNAVAILABLE: ...: context canceled"
```

## Impact

- **Every graceful stop during a task logs two warnings that name the wrong cause**, with the remedy "Verify the
  database is running and FORGE_DATABASE_URL is correct". A rolling deploy of N workers mid-plan writes 2N of them. An
  operator reading the log after a deploy is sent to look at a healthy database.
- **A task that finished as the stop arrived left its dependents pending, and its goal active.** Nothing is lost — the
  idle poll's `releaseWaitingGoals` and `settleFinishedGoals` sweeps catch both — but only once a worker is idle, so a
  busy fleet can take much longer than one poll interval to start the next task of a plan or report the goal done.

## Preconditions

A worker whose context is cancelled while `runTask` is in progress (any graceful stop that lands during a task). An
idle worker is unaffected: it returns from its sleep without reaching these calls.

## Root cause

`internal/agent/worker.go` `Run`, as #87 left it:

```go
w.runTask(ctx, task)
w.releaseWaiting(ctx, goalID)
w.settleGoal(ctx, goalID)
```

`ctx` is the worker's own, and a graceful stop cancels it. pgx returns `context canceled` before touching the
connection, and `db` classifies every connection-level failure as `DATABASE_UNAVAILABLE`. `settleGoal` has run on this
context since goal settlement was added; #87 put `releaseWaiting` beside it in the same shape.

## Why it did not show up before

- **No fence stopped a worker and read what it logged.** #87's release fences run a worker until its goal settles and
  read rows; the settlement fences call `SettleGoalForTest` on a live context. The warnings went to `t.Log` only.
- **A graceful stop had never been exercised live.** The only live stop before #104 was `taskkill /F`, which kills the
  process before any of this runs.
- **The finished-at-the-stop case needs a stop inside a few milliseconds** between the task's last write and the
  release. No test or run aimed at that window.

## Fix

- `Worker.afterTask(ctx, goalID)` does what the end of a task sets moving, on
  `context.WithTimeout(context.WithoutCancel(ctx), afterTaskTimeout)` (10 s). The bound keeps "outlives the stop" from
  becoming "outlives the process": forge-worker gives its loops 30 s to return.
- `Run` calls it after every `runTask`. Neither `releaseWaiting` nor `settleGoal` changed: for a task that did not
  finish, `PromoteReadyTasks` promotes nothing and `settleGoal` sees outstanding work and returns, exactly as they do
  for any unfinished task.
- `Worker.AfterTaskForTest` exposes it, beside `SettleGoalForTest`, so a fence can hand it the cancelled context a stop
  leaves `Run` holding instead of racing a stop against a task's last write.

This is #104's fix, ported to #87's `Run`, which releases through the executor path rather than build steps.

## Verification

- `TestWorker_AWorkerStoppedMidTaskDoesNotReportItsBookkeepingAsADatabaseFailure` (internal/agent/worker_stop_test.go)
  plans a two-task chain, runs a real worker whose model blocks until its call is cancelled, stops the worker there,
  and requires no `forge.task.release_failed` or `forge.goal.settle_failed` in what it logged.
- `TestWorker_ATaskFinishedAsTheStopArrivesStillReleasesItsDependentsAndSettlesItsGoal` finishes the first task, calls
  `afterTask` with an already-cancelled context and requires the second task `ready`; finishes the second, calls it
  again and requires the goal `succeeded`; and requires nothing logged as `DATABASE_UNAVAILABLE`.
- Both were written first and went red against #87's code (the first with no change at all; the second through the
  `afterTask` seam with the context passed straight through), and both pass with the fix. #87's
  `TestWorker_AFinishedTaskReleasesTheTasksWaitingOnIt`, `TestWorker_ATaskLeftWaitingByACrashIsReleasedOnTheIdlePoll`
  and `TestWorker_ABudgetRefusalStopsTheGoal` still pass.

## Regression prevention

Drill "a stopping worker's bookkeeping runs on the cancelled context" in `scripts/drill-fences.sh` replaces the
outliving context with `ctx`; both fences go red. #87's drill "a finished task releases nothing" was re-anchored to the
call's new place in `afterTask` and still goes red.

## Not in this fix

- **The task itself is not handed back on this branch.** `Run`'s comment says a stop releases the current task to the
  queue; on the executor path it does not. The cancelled model call reaches `retryOrFail`, whose event and
  `TransitionTask` back to `ready` run on the same cancelled context, fail, and are logged as `DATABASE_UNAVAILABLE`
  too (`forge.task.cycle_ended`); the task stays `running` until its lease expires and the reaper recovers it. The
  stage A1 branch hands a build step back with `WithoutCancel` in `runBuildStep`; the executor path has no equivalent.
  That is `runTask`'s stop path, not the bookkeeping after it, and is left for its own change. Fixed on the branch
  stacked on this one (`fix/worker-stop-hands-task-back`):
  docs/bugfix/2026-09-15-a-stopped-worker-left-its-task-to-run-out-its-lease.md.
- **The classification.** A cancelled context is still reported as `DATABASE_UNAVAILABLE` wherever else it reaches
  `db`. Mapping `context.Canceled` to its own code would be a wider change to `internal/platform/db`.
