# A stopped worker left its task to run out its lease, and counted the stop as an attempt

**Date:** 2026-09-15 · **Status:** fixed on `fix/worker-stop-hands-task-back`, stacked on #105
(`fix/worker-stop-bookkeeping`) and #87 (`fix/engine-release-budget-chain`); `main` still has the defect and gets
the fix when those merge · **Severity:** medium. Every graceful stop in the middle of a task delayed that task by a
full lease and used up one of its attempts.

## Summary

`Worker.Run` promises that "cancellation is a graceful stop, not a kill: the current task is released back to the
queue rather than abandoned to its lease timeout". For ordinary executor tasks that was never true. When
`forge-worker` was stopped (SIGINT/SIGTERM, Ctrl-Break) while a task ran, the cancelled model call reached
`retryOrFail`, and its event and its `TransitionTask` back to `ready` both ran on the context the stop had just
cancelled. Both failed. The task stayed `running`, leased to a worker that had exited, until the lease expired and
the reaper returned it (3 minutes by default). The attempt its claim counted was never given back, so enough stops
could fail a task that had never failed. A stop at any other point in `runTask` (before the task started, at the
approval gate, during verification) left the task stuck the same way.

The fix hands the task back from `Run` as soon as `runTask` returns, on a context that outlives the stop.
`Queue.Release` now also gives the attempt back and accepts a task in `verifying`, and `retryOrFail` records nothing
when the worker is stopping.

## Symptom

A stopped worker's task stays `running`, with a `lease_owner` naming a process that has exited, for the whole lease.
Then the reaper logs `forge.task.lease_expired` ("the worker holding this task stopped responding") about a worker
that stopped cleanly. The stop itself logs a retry that never happened and two DATABASE_UNAVAILABLE warnings:

```
level=INFO msg=forge.task.retrying   ... cause="context canceled"
level=WARN msg=forge.task.cycle_ended error="engine.Repository.AppendEvent: DATABASE_UNAVAILABLE: ...: context canceled" detail="a timeline event was lost; ..."
level=WARN msg=forge.task.cycle_ended error="engine.Repository.TransitionTask: DATABASE_UNAVAILABLE: ...: context canceled" target=ready
```

#105 named this path in its "Not in this fix". It was reproduced on #105's branch by the fences below, written
before the fix. Against #105's code:

```
--- FAIL: TestWorker_AWorkerStoppedInsideAModelCallHandsItsTaskBackAtOnce
    the task a stopped worker was holding is running; it should be ready for the next worker now, not when its lease runs out
    the task is still leased to ... until 2026-09-15 14:59:18 after its worker stopped
    the task has used 1 attempts, 0 before the stopped worker claimed it; a stop is not an attempt
    the timeline has 0 task.handed_back events for the task, want 1 saying it was handed back
    a worker stopped mid-task reported the database unavailable; it was only stopping
    a worker stopped mid-task logged forge.task.retrying; a stop is not a failed attempt
    the goal did not settle within a minute; its tasks are running and pending
--- FAIL: TestWorker_AWorkerStoppedBeforeItsTaskStartsHandsItBackUnstarted
    the task a stopped worker was holding is claimed; ...
--- FAIL: TestWorker_AWorkerStoppedAtTheApprovalGateHandsItsTaskBackAndTheGateIsOpenedOnce
    the task a stopped worker was holding is running; ...
    the next worker left the handed-back task running; it should have parked it at the gate
--- FAIL: TestQueue_AReleasedTaskIsClaimableAtOnceAndItsAttemptIsNotCounted
    a released task has used 1 attempts; a stop is not an attempt, so it should be back to 0
--- FAIL: TestQueue_ATaskStoppedDuringVerificationCanBeReleased
    releasing a task stopped during verification: engine.Queue.Release: CONFLICT: ...
```

"The goal did not settle within a minute" is the lease showing through. A new worker started straight after the
stop could not claim the task.

## Impact

- **A deploy or restart costs each in-flight task a full lease instead of seconds.** With the default 3-minute
  lease, a rolling restart of N workers mid-plan stalls N tasks, and every task waiting on them, for 3 minutes.
- **A stop uses up an attempt.** `Claim` counts one when it leases the task. Neither the stop nor the reaper gives
  it back, so a long task that lives through `max_attempts` restarts is failed by the reaper with
  `LEASE_EXPIRED_ATTEMPTS_EXHAUSTED` without ever having failed.
- **The record is wrong.** The log shows a retry, a lost timeline event and a database outage, and then a lease
  expiry that blames a crash. None of that happened. The worker was stopped, and nothing on the timeline says so.
- Nothing is lost or duplicated. The reaper does recover the task, and a task that is re-run resumes from its last
  checkpoint.

## Preconditions

A worker whose context is cancelled (any graceful stop) while `runTask` holds a task in `claimed`, `running` or
`verifying`. An idle worker is unaffected. So is a task already parked at the approval gate, which holds no lease.

## Root cause

`internal/agent/worker.go`, as #105 left it:

```go
w.runTask(ctx, task)   // ctx is cancelled by the stop
w.afterTask(ctx, goalID)
```

`runTask` has no stop path. Every write after the stop runs on `ctx`, which pgx refuses before touching a
connection. On the model-call path, `Execute` returns the cancellation, `retryOrFail` treats it as a retryable
failure, and its `appendEvent` and `transition(ready)` fail. Before the task starts, the `TransitionTask` to
`running` fails and the task is "abandoned" while still `claimed`. At the gate, the approval row is written and
then the park in `awaiting_approval` fails. In every case the worker returns still holding the lease.

`Queue.Release`, the call that exists for exactly this ("used when a worker shuts down cleanly mid-task"), had no
caller on the executor path and no test. It kept the counted attempt and refused a task in `verifying`.

The stage A1 build-goal branches (#85) hand a build step back in `runBuildStep` with
`Release(context.WithoutCancel(ctx), …)`. Ordinary executor tasks never got the equivalent.

## Why it did not show up before

- **No fence stopped a worker and read the task.** #87's fences run a worker until its goal settles. #105's fence
  stops a worker but reads only the log lines of the bookkeeping after the task, and says in a comment that the
  task's own writes are a separate path.
- **The reaper hides it.** The task does come back, just late, and the log line blames a crashed worker, which is
  what a reader expects to see after a restart.
- **The only live stops before #104 were `taskkill /F`,** where abandoning the task to its lease is the correct
  outcome.

## Fix

- **`Worker.handBack(ctx, task)`**, called by `Run` after `runTask` whenever `ctx` is cancelled. It runs
  `Queue.Release` on `context.WithTimeout(context.WithoutCancel(ctx), afterTaskTimeout)`, then logs
  `forge.task.handed_back` and writes a `task.handed_back` timeline event recording the state the task was stopped
  in. It is one call after `runTask` rather than one per stop point because `Release` decides from the row: only a
  task this worker still holds under a lease is handed back. A task that finished, failed or parked at the gate, or
  that the reaper gave to another worker, comes back as a CONFLICT and nothing happens. A release that cannot be
  written logs `forge.task.hand_back_failed` and leaves the task to the reaper, where it was before.
- **`Queue.Release` gives the attempt back** (`attempt_count = greatest(attempt_count - 1, 0)`) **and accepts
  `verifying`.** The state graph already allows `verifying → ready`, and the reaper already makes that move when a
  lease lapses. A task that is stopped over and over is still bounded by its goal's wall-clock and token budget,
  which a stop does not refund.
- **`retryOrFail` records nothing when `ctx` is cancelled.** The error that brings a stopping worker there is the
  stop itself. It no longer logs a retry, loses an event and fails a transition before `Run` hands the task back.
- **`runTask`'s transition to `running`** returns quietly when it failed because of the stop, instead of logging
  the task as "abandoned" with a DATABASE_UNAVAILABLE reason.

What each stop point now does:

| Stopped | Task afterwards | Why |
|---|---|---|
| between the claim and the start | `ready`, unstarted, attempt not counted | nothing ran |
| inside a model call, or during verification | `ready`, attempt not counted | the next worker resumes from the checkpoint |
| at the gate, after the request was opened but before parking | `ready`; the next worker parks it on the **same** request | `insert … on conflict do nothing` and the pending read reuse it |
| already parked at the gate | untouched, `awaiting_approval` | holds no lease; it waits for a human, not a worker |
| after the task's last write | untouched (`succeeded`/`failed`/retry `ready`) | Release finds nothing held |
| as a genuine failure came back | `ready`, attempt not counted | the stop wins; the next worker meets the real failure again and counts it |

On this branch there is no `runBuildStep`. When #85's branches are rebuilt on this one, `runBuildStep`'s own
`Release` becomes redundant: whichever release runs first wins and the other gets a CONFLICT, and the timeline
event is written only by `handBack`. The port should drop `runBuildStep`'s release and return, so that `Run` hands
back build steps the same way. A build step also stops counting the stopped attempt, through `Release`.

## Verification

Written first, on #105's worker harness (real worker, queue and executor on Postgres, stub model), red against
#105's code as shown above, green with the fix:

- `TestWorker_AWorkerStoppedInsideAModelCallHandsItsTaskBackAtOnce` (internal/agent/worker_stop_test.go). The model
  blocks until its call is cancelled and the worker is stopped there. The moment `Run` returns, the task must be
  `ready`, unleased, runnable now, on its original attempt count, with no error, one `task.handed_back` and no
  `task.retrying`/`task.failed`, and the log must hold no DATABASE_UNAVAILABLE and no `forge.task.retrying`. A new
  worker whose idle poll is an hour away then runs the goal to `succeeded`, with the handed-back task finishing on
  attempt 1.
- `TestWorker_AWorkerStoppedBeforeItsTaskStartsHandsItBackUnstarted` stops the worker when it logs
  `forge.task.cycle_started`. The task is handed back with no `started_at`, and the model is never asked.
- `TestWorker_AWorkerStoppedAtTheApprovalGateHandsItsTaskBackAndTheGateIsOpenedOnce` stops the worker when it logs
  `forge.approval.opened`. The task is handed back, the next worker parks it in `awaiting_approval` on the one
  existing request, and stopping that worker leaves the parked task alone.
- `TestWorker_ATaskThatFailsWhileItsWorkerRunsIsStillRetriedAndThenFailed` covers the path that must not change.
  With no stop, a model that refuses every call is retried once with the attempt counted and then failed. The
  timeline has one `task.retrying`, one `task.failed` and no `task.handed_back`. This passed before and after.
- `TestQueue_AReleasedTaskIsClaimableAtOnceAndItsAttemptIsNotCounted`,
  `TestQueue_ATaskStoppedDuringVerificationCanBeReleased` and `TestQueue_AWorkerCannotReleaseATaskItDoesNotHold`
  (internal/domain/engine/queue_integration_test.go) fence `Release` itself. The last one passed before and after.
- #87's and #105's worker fences still pass. `go vet ./...` and
  `go test -count=1 ./internal/agent/ ./internal/domain/engine/` are clean.

## Regression prevention

Drills in `scripts/drill-fences.sh`, section "A stopped worker hands its task back". Each was run and went red:

- "a stopped worker leaves its task to its lease" removes `Run`'s `handBack` call.
- "a stop still counts as an attempt" (engine, and again through the worker) removes the attempt give-back from
  `Release`.
- "a task stopped in verification cannot be released" drops `verifying` from `Release`.
- "a stop is recorded as a failed attempt" removes `retryOrFail`'s stop guard.
- "a genuine failure is dropped as if it were a stop" makes that guard unconditional.

## Not in this fix

- **The approval request's timeline event can be lost.** A stop that lands after `checkApproval` inserts the
  request, but before it writes `approval.requested`, loses that event. The request row survives and the next
  worker parks on it, but the timeline has no `approval.requested` for it. Fixed on `fix/worker-stop-leftovers`:
  docs/bugfix/2026-09-15-a-stopped-worker-lost-what-it-had-done-and-blamed-the-database.md.
- **Other failure writes on a stopping worker.** `failTask`'s callers (an unreadable goal, an approval insert, the
  workspace, the pack) do not check for a stop. If the stop is what made them fail, their own writes fail on the
  cancelled context and may log DATABASE_UNAVAILABLE, and `Run` then hands the task back. The outcome is right and
  the log is noisy, as #105's note on the classification of `context.Canceled` already says. Fixed on
  `fix/worker-stop-leftovers`, along with the records a stop lost with them (spent tokens, a tool call that ran).
- **Work repeated after a hand-back.** The next attempt resumes from the last iteration checkpoint, so anything
  after it runs again. That is what lease-expiry recovery already did; it just happens sooner now.
