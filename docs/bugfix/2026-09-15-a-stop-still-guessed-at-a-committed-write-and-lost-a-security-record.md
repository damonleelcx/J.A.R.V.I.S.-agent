# A stop still guessed at a committed write, and lost a security record

**Date:** 2026-09-15 · **Status:** fixed on `fix/worker-stop-last-items`, stacked on #109
(`fix/worker-stop-leftovers`), #108 (`fix/worker-stop-hands-task-back`), #105
(`fix/worker-stop-bookkeeping`) and #87 (`fix/engine-release-budget-chain`); `main` gets it when
those merge · **Severity:** low to medium. One of the four loses a security record permanently and
one leaves a succeeded task with no timeline entry saying so; the other two are log noise that
reads like a failure.

## Summary

#87, #105, #108 and #109 made a graceful stop hand the task back, keep the record of what the
worker had done, and stop blaming the database for the stop. #109's "Not in this fix" listed four
things left. This closes all four.

1. **A statement the stop cancelled after Postgres had already committed it.** pgx refuses a
   statement on a context that is already cancelled, and cancels one that is already on the wire —
   and in the second case the server may have committed it first. The caller is told the same
   thing either way: `context canceled`. #109 made a stopping worker skip its failed transitions,
   which is right for the first case and wrong for the second. A task that really did reach
   `succeeded` had no `task.succeeded` event, and nobody ever came back to notice, because nobody
   claims a succeeded task. **The row is now read back**, on a context that outlives the stop.
2. **A `forge.verification.ran` WARN carrying `"context canceled"`.** Not a database error, so
   #109's fences did not see it. It is the same line a verifier that genuinely could produce no
   verdict writes, so every graceful stop during verification read as a verifier failure.
   **Logged only when the stop is not what cancelled it.**
3. **The suspected-injection event.** `Executor.appendInjectionEvent` still wrote on the run
   context, so a stop landing as a tool returned injection-shaped output lost it. The WARN went to
   a log belonging to a process that was exiting, and the goal's timeline said nothing.
   **Written on `outliving(ctx)`, like every other record of something that happened.**
4. **The polling path's three reconciliations** — `reapExpired`, `releaseWaitingGoals` and
   `settleFinishedGoals`. Each ran on the run context, so a stop landing in a sweep rather than in
   the idle sleep failed three queries at once and logged all three as the database being
   unavailable. **Skipped, quietly, when the stop has cancelled them.**

The rule #109 wrote on `outliving` still holds and is what decides each of these: a stop abandons
the attempt, not the record of it. Item 1 adds the missing half of it — before deciding that a
record is not owed, find out whether the thing happened.

## Symptom

All four on #109's code, with only the two new seams added:

```
--- FAIL: TestWorker_ASuccessTheStopCancelledAfterPostgresHadCommittedItIsStillOnTheTimeline
    the timeline records 0 task.succeeded events for a task that really did succeed, want 1; the
    stop cancelled the statement after Postgres committed it, and the worker has to read the row
    back rather than assume the success it cannot see was refused

--- FAIL: TestWorker_AStopDuringVerificationDoesNotWarnThatTheVerifierFailed
    a stopped worker warned forge.verification.ran; the verifier did not fail, it was cancelled
    with the worker, and a reader of this log has no way to tell that from a verifier that could
    produce no verdict:
        level=WARN msg=forge.verification.ran error="context canceled" error_code=INTERNAL
        retryable=true remedy="Retry once. If it persists, quote the request_id …"

--- FAIL: TestWorker_ASuspectedInjectionFoundAsTheWorkerStopsIsStillRecordedOnTheTimeline
    the timeline records 0 security.injection_suspected events for a tool call that returned
    injection-shaped output as its worker stopped, want 1

--- FAIL: TestWorker_ThePollSweepsAStopCancelledAreSkippedQuietlyAndStillRunWhenNothingStoppedThem
    a stopped worker reported the database unavailable; it was only stopping:
        level=WARN msg=forge.worker.reaped error="engine.Queue.ReapExpiredLeases:
            DATABASE_UNAVAILABLE: …: context canceled" error_code=DATABASE_UNAVAILABLE
        level=WARN msg=forge.task.release_failed error="context canceled"
            detail="the release sweep could not run; tasks waiting on finished work may stay pending"
        level=WARN msg=forge.goal.settle_failed error="context canceled"
            detail="the goal reconciliation sweep could not run; finished goals may stay marked active"
```

## Impact

- **A goal's timeline can be missing a success that happened.** This is the only one of the four
  that is permanently unrecoverable by the system itself: the task row says `succeeded`, so no
  worker claims it again, no sweep looks at it, and nothing will ever write the event. Anyone
  reconstructing the goal's history from its timeline sees the task start and then nothing.
- **A suspected injection can vanish from the record.** "Did anything try to steer this agent
  through its own tool output?" is a question asked long after the fact, by somebody reading the
  goal rather than grepping the log of a process that has since exited. A stop in the instant a
  poisoned tool returned left that question answered wrongly.
- **A stop during verification reads as a verifier failure.** The operator reading a restart's log
  sees a WARN saying verification ran and errored, with a remedy telling them to retry.
- **A stop during a sweep reads as a database outage**, three lines of it, one of them
  `DATABASE_UNAVAILABLE` with a remedy telling the operator to check the database.
- Nothing is lost from the task itself in any of the four. #108's hand-back still applies.

## Preconditions

A worker whose context is cancelled — any graceful stop — and:

1. a transition (the one to `succeeded` is the one that matters) already on the wire when the stop
   lands, committed by Postgres before its cancel request arrived;
2. a task at R2 or above, stopped inside the verifier's model call;
3. a tool returning output matching a known injection shape in the instant the stop arrives;
4. a stop landing in the polling path's sweeps rather than in the idle sleep between them.

## Root cause

**1.** `transition` read the error and not the row. Its #109 guard — skip quietly when
`ctx.Err() != nil`, because `handBack` passes the decision to the next worker — assumed a failed
write means an unmade decision. That is true of a statement pgx refused before sending and false
of one it cancelled in flight, and the error does not distinguish them. `completeTask`'s rule
("only a success that was written is recorded") then correctly declined to write an event for a
success it believed had been refused.

**2.** `completeTask` logged every `Verify` error at WARN, and `rawToolOutput` logged every failed
read the same way. Neither asked whether the stop was the cause. #109's fences check for
`DATABASE_UNAVAILABLE`, and this is `INTERNAL`.

**3.** `appendInjectionEvent` was written before #109's `outliving` existed and was not on the list
of writes #109 moved onto it. Every other timeline event goes through `Worker.appendEvent`, which
was moved; this one calls `repo.AppendEvent` directly from the executor.

**4.** `Run` calls all three sweeps with the run context, because until #105 nothing distinguished
a stop from an outage anywhere. They are the last callers left holding it.

## Why it did not show up before

- **#109's fences read the timeline, the budget and the ledger, but not against a write that
  succeeded.** Producing item 1 requires the cancel request and the commit to cross on the wire,
  which no test can arrange by racing. Nothing short of a seam reaches it, and #109 had no reason
  to add one.
- **Items 2 and 4 are not database errors.** `requireNoDatabaseFailure` greps for
  `DATABASE_UNAVAILABLE`, and `forge.verification.ran` carries `INTERNAL`, as do two of the three
  sweep lines.
- **A stop almost always lands in the idle sleep, not in a sweep.** The sweeps are three indexed
  queries against a mostly-empty result; the sleep is a whole poll interval. Item 4 is rare in
  proportion to that ratio, which is why it reads as an occasional unexplained outage line rather
  than as a bug.
- **No fence ever stopped a worker with a tool that returned injection-shaped output.** The stub
  worker's tools return `{}`.

## Fix

| Write | On a stopping worker | Why |
|---|---|---|
| a transition whose error arrived with the stop | **the row is read back** (`stopLanded`) | the error cannot say whether Postgres committed it; the row can |
| `task.succeeded` after such a transition | written when the read says it landed | the success happened, and nothing will ever come back to a succeeded task to say so |
| a transition the read says did not land | skipped quietly, as #109 | the decision was not made; the next worker makes it from the row |
| a read-back that cannot be made | one WARN saying the outcome is **UNKNOWN**, in those words | neither a success nor a failure was established, and claiming either would be a lie |
| `forge.verification.ran` on a `Verify` error | logged only when the stop did not cause it | a cancelled verifier did not fail; it stopped |
| `forge.verification.ran` on the raw-tool-output read | same | no verdict is being reached on thinner evidence: the verifier call after it is cancelled too |
| `security.injection_suspected` | written on `outliving(ctx)` | a record of something that happened, and the only copy that outlives the process |
| `reapExpired`, `releaseWaitingGoals`, `settleFinishedGoals` | skipped, and silent about it | a reconciliation is neither a record nor a decision: it converges, it has no deadline, and the next poll does it |

### Why read the row back, and not the alternatives

The brief for item 1 was the smallest honest handling, and never to claim a success or a failure
that is not known. Three were on the table:

- **A clear log saying the outcome is unknown** is honest but leaves the timeline wrong for good,
  and puts the work on a human reading a restart's log.
- **Reconcile on the next claim** does not reach this case at all. The task is `succeeded`; there
  is no next claim. It would work for a claim the stop cancelled in flight, which the reaper
  already covers.
- **An idempotent write** — writing `task.succeeded` regardless and deduplicating — records a
  success that may not have been written, which is precisely the thing not to do.

Reading the row is smaller than any of them and is the only one that ends up knowing. It is one
indexed read, only on the stop path, only after a transition has already failed. It is kept for
the case it was built for: a "no" records nothing, which is the side to be wrong on, and a read
that cannot be made at all falls back to the honest log.

`stopLanded` sets `task.Status` on a yes, which is what `TransitionTask` does on its own success
and what every caller reads afterwards.

### Two new seams

- `OnTransitionWrittenForTest(func(to) error)` runs after a transition has been written and lets
  the fence replace its result. Which of the cancel request and the commit reaches Postgres first
  is not something a test can decide, so this produces the shape — a committed transition whose
  caller sees a cancellation — and nothing else.
- `PollSweepsForTest(ctx)` runs the three polling-path reconciliations on a given context, so a
  fence can hand them the cancelled context `Run` holds instead of racing a stop against three
  indexed queries. `AfterTaskForTest` exists for the same reason.

Both are nil or unused outside the fences.

## Verification

Written first on #109's code with only the two seams added, red as shown above, green with the
fix. All four are in `internal/agent/worker_stop_test.go`, on #109's harness: the real worker,
queue and executor on Postgres, a stub model, a 2 ms heartbeat and a fresh schema per test.

- `TestWorker_ASuccessTheStopCancelledAfterPostgresHadCommittedItIsStillOnTheTimeline` stops the
  worker through the transition seam as its transition to `succeeded` commits, and hands it the
  error pgx returns for a cancelled statement. The task row must be `succeeded`, its timeline must
  hold exactly one `task.succeeded`, and — because the task had already succeeded — no
  `task.handed_back`.
- `TestWorker_AStopDuringVerificationDoesNotWarnThatTheVerifierFailed` stops the worker inside the
  verifier's model call. The task is handed back and the log holds no `level=WARN
  msg=forge.verification.ran`. The level is part of the assertion: a verifier that really ran
  writes the same event at INFO.
- `TestWorker_ASuspectedInjectionFoundAsTheWorkerStopsIsStillRecordedOnTheTimeline` runs a tool
  that stops the worker and returns output matching two injection patterns. The task is handed
  back and the timeline holds exactly one `security.injection_suspected`.
- `TestWorker_ThePollSweepsAStopCancelledAreSkippedQuietlyAndStillRunWhenNothingStoppedThem` gives
  the sweeps real work — a lease a crashed worker left behind, and a task pending on it — and runs
  them on a cancelled context. Nothing is logged at WARN for `forge.worker.reaped`,
  `forge.task.release_failed` or `forge.goal.settle_failed`, no `DATABASE_UNAVAILABLE`, and the
  pending task is still pending: a sweep that is skipped is skipped, not half-applied. It then
  runs the same sweeps on a live context and requires the release to happen, so the guard cannot
  be a sweep that never runs.

#87's, #105's, #108's and #109's fences all still pass. `go vet ./...` and
`go test -count=1 ./internal/agent/ ./internal/domain/engine/` are clean.

## Regression prevention

Drills in `scripts/drill-fences.sh`, section "The last four things a stop got wrong". Each was run
and went red. Every mutation keeps the code compiling, so a red comes from the fence and not from
the build. `internal/agent/settle.go` joins the script's FILES list.

- "a transition the stop cancelled in flight is assumed refused" removes the `stopLanded` call.
- "a transition the stop cancelled in flight is assumed to have landed" makes `stopLanded` ignore
  the status it read — caught by #109's own "as a model call answers that the task is done", where
  the transition genuinely was refused.
- "a stop during verification is logged as a verifier failure" unguards the `forge.verification.ran`
  WARN.
- "a suspected injection is recorded on the context the stop cancelled" puts
  `appendInjectionEvent` back on the run context.
- "the lease reaper runs on the context the stop cancelled", "the release sweep …" and "the settle
  sweep …" each restore one sweep's pre-fix behaviour: no skip, and the failure logged.
- "the release sweep is skipped even when nothing stopped it" and "the settle sweep is skipped even
  when nothing stopped it" make each guard unconditional. The first is caught by the new fence's
  live half, the second by `TestReconciliationSweepSettlesWhatTheEventMissed`, which is what says
  the guard did not turn a reconciliation into nothing.

## Not in this fix

- **A claim the stop cancels in flight after it committed.** The task is left claimed under a
  lease the stopped worker no longer holds, and the reaper returns it when the lease expires. That
  is a delay and not a divergence: nothing is lost and nothing untrue is recorded. The same is
  true of `runTask`'s transition to `running`, where the row lands on `running`, `handBack`'s
  `Release` returns it, and the next attempt starts it properly. Only the succeeded case had a
  permanent gap, because only a succeeded task is never looked at again.
- **`settleGoal`'s own update, cancelled in flight after it committed.** A goal settled that way
  keeps its terminal status and loses its `goal.ended` event. It is the same class as item 1 and
  the same read-back would close it; it is left because a goal is settled again by the idle sweep
  only while it is still `active`, so the fix there is a different shape and wants its own fence.
- **An event written before its decision.** `budget.exceeded` and `verification.failed` are still
  written before the transition they lead to, as #109 records.
- **Other reads a stop makes fail** — the character fallback and the secret handles still warn
  about the read. They are one line each and neither claims a failure of the thing being read.
- **Work repeated after a hand-back**, as #108 records.
