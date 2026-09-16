# A stopped worker lost what it had done, and blamed the database for it

**Date:** 2026-09-15 · **Status:** fixed on `fix/worker-stop-leftovers`, stacked on #108
(`fix/worker-stop-hands-task-back`), #105 (`fix/worker-stop-bookkeeping`) and #87 (`fix/engine-release-budget-chain`);
`main` gets it when those merge · **Severity:** low to medium. Nothing a stop hands back is lost, but the timeline,
the budget and the tool-call ledger could each miss something that really happened, and a missing ledger record
lets a retry repeat a side effect.

## Summary

#108 made a stopped worker hand its task back at once. Its "Not in this fix" left two things:

1. **An approval request could be missing from the timeline.** `checkApproval` wrote the request row and then,
   separately, its `approval.requested` event. A stop between the two kept the row and lost the event. The next
   worker parked on the pending request, so the approvals table and the timeline disagreed for good.
2. **Other writes on the cancelled run context.** Wherever the stop landed, the worker's remaining writes ran on
   the context the stop had cancelled, failed at once, and were logged as DATABASE_UNAVAILABLE. Looking for them
   found that some are not just noise. A few record something that had already happened: tokens a model call
   spent, a tool call that ran, the reaper's recovery of a crashed worker's task. Those were lost. Two more wrote
   what should not have been written: a stopped worker went on to run the model's next tool call, and the ledger
   could not know that a call had already run.

The request and its event are now one transaction. Every other write on a stopping worker is either a **record** of
something that already happened, written on a context that outlives the stop, or a **decision** or a piece of
work the stop cut short, which is skipped.

## Symptom

A worker stopped at the approval gate between the request and its event, on #108's code:

```
--- FAIL: TestWorker_AStopBetweenOpeningAnApprovalRequestAndRecordingItLeavesTheTimelineAndTheApprovalsAgreeing
    a stopped worker reported the database unavailable; it was only stopping:
        level=WARN msg=forge.task.cycle_ended error="engine.Repository.AppendEvent: DATABASE_UNAVAILABLE: ...: context canceled"
        level=WARN msg=forge.task.cycle_ended error="engine.Repository.TransitionTask: DATABASE_UNAVAILABLE: ... (transitioning task ... from running to awaiting_approval): context canceled"
    the task has 1 approval requests and its timeline records 0 approval.requested events; want one of each
```

The same worker stopped at each point in a task, on #108's code (log lines trimmed):

```
--- FAIL: TestWorker_AWorkerStoppedAnywhereInATaskKeepsWhatItDidAndLogsNoDatabaseFailure
    --- PASS: .../before_its_task_starts
    --- FAIL: .../inside_a_model_call
        level=WARN msg=forge.task.lease_expired error="engine.Queue.Heartbeat: DATABASE_UNAVAILABLE: ...: context canceled"
    --- FAIL: .../as_a_model_call_answers_that_the_task_is_done
        level=WARN msg=forge.budget.record_failed error="engine.BudgetGuard.RecordSpend: DATABASE_UNAVAILABLE: ...: context canceled"
        level=WARN msg=forge.task.cycle_ended error="engine.Repository.TransitionTask: DATABASE_UNAVAILABLE: ..."
        level=WARN msg=forge.task.cycle_ended error="engine.Repository.AppendEvent: DATABASE_UNAVAILABLE: ..."
        the goal has 0 tokens spent; the call that answered as its worker stopped spent 10, and a stop does not refund them
    --- FAIL: .../as_a_model_call_answers_that_the_task_is_blocked
        level=WARN msg=forge.task.skipped error="engine.Queue.SkipTasksBlockedByFailure: DATABASE_UNAVAILABLE: ..."
        the goal has 0 tokens spent; ...
    --- FAIL: .../as_a_tool_call_finishes
        level=WARN msg=forge.tool.ledger_write_failed error="db.InTx: DATABASE_UNAVAILABLE: ...: context canceled"
        level=WARN msg=forge.checkpoint.write_failed error="engine.Repository.SaveCheckpoint: DATABASE_UNAVAILABLE: ..."
        the ledger has 0 succeeded records of the tool call that finished as its worker stopped, want 1; without it the next attempt runs the call again
        a stopped worker went on to run the next tool call 1 times; it should hand the task back instead
    --- FAIL: .../inside_a_tool_call
        level=WARN msg=forge.tool.ledger_write_failed ...
        level=WARN msg=forge.checkpoint.write_failed ...
    --- PASS: .../during_verification
    --- FAIL: .../at_the_approval_gate
        level=WARN msg=forge.task.cycle_ended error="engine.Repository.TransitionTask: DATABASE_UNAVAILABLE: ... to awaiting_approval): context canceled"
```

The heartbeat line shows up at any stop point where a beat is in flight. "Before its task starts" and "during
verification" passed on this run only because none was.

## Impact

- **The approvals table and the timeline disagree.** Someone reading the goal's history sees the task parked at
  the gate with nothing saying a human was asked.
- **A stop can undercount the budget.** Tokens from a model call that answered as the stop arrived were never
  recorded. The goal's token ceiling is checked against persisted counters, so each such stop gives the goal
  unpaid tokens.
- **A retry can repeat a side effect.** A tool call that finished as the stop arrived was left out of the
  idempotency ledger, so the next attempt found no record and ran it again. Separately, a stopped worker kept
  running the model's remaining tool calls, and their ledger reads failed on the cancelled context and looked
  like "never ran".
- **The log blames the database for every stop.** Each of the lines above is a WARN with
  `error_code=DATABASE_UNAVAILABLE` and a remedy telling the operator to check the database.
- Nothing is lost from the task itself. #108 hands it back either way.

## Preconditions

A worker whose context is cancelled (any graceful stop) while it holds a task, or while its poll is recovering a
crashed worker's task. The approval gap also needs a task that requires approval and a stop in the instant between
the request's insert and its event.

## Root cause

All of the worker's writes took the run context, and nothing distinguished what the stop should cancel from what
it should not. pgx refuses a cancelled context before it touches a connection, so every write after the stop
failed. #105 moved the end-of-task bookkeeping off that context, and #108 did the same for the hand-back. The
writes in between were left:

- `checkApproval`: the request insert and `appendEvent(approval.requested)` were two statements, not a
  transaction, followed by the park transition.
- `transition`, `failTask` (with `SkipTasksBlockedByFailure`) and the heartbeat logged every error, including
  the stop's own.
- `appendEvent`, `Executor.Execute`'s `RecordSpend`, `recordToolCall` and `SaveCheckpoint` ran on the run
  context.
- `Execute`'s tool loop did not check for a stop between calls. A call the stop cut short reached
  `recordToolCall(failed)`, and had that write succeeded, the failed row would have taken the call's idempotency
  key (the column is unique), so the next attempt's success could never be recorded.
- `completeTask` wrote `task.succeeded` and `verification.passed` whether or not the transition to `succeeded`
  had been written.

## Why it did not show up before

- **#108's fences read the task, not the timeline, the budget or the ledger.** They stop a worker in a blocking
  model call, before the start and after the gate log line, where the only lost writes are ones `handBack` made
  harmless.
- **The harness heartbeat is 20 s,** so a beat never overlapped a stop in a test.
- **The stub worker has no tools** and its model never answers as the stop arrives, so no fence ever stopped a
  worker with a model call's tokens, a tool result or a checkpoint still to write.
- **The noisy lines look like a real outage** in a log from a restart. Nobody reads them as a bug.

## Fix

The rule, in `outliving`'s comment in `internal/agent/worker.go`: a stop abandons the attempt, not the record of
it.

| Write | On a stopping worker | Why |
|---|---|---|
| approval request + `approval.requested` | **one transaction** (`db.InTx`) | a stop or crash leaves both or neither; with neither, the next worker opens the gate afresh |
| `appendEvent` (every timeline event) | written on `outliving(ctx)` | an event records what already happened |
| `RecordSpend` after a model call | written on `outliving(ctx)` | the tokens were spent |
| `recordToolCall` for a call that ran or was refused | written on `outliving(ctx)` | the ledger is what stops a retry repeating it |
| `transition` | skipped quietly (logged only when not stopping) | a decision; `handBack` gives the task to a worker that decides again |
| `failTask` (and `SkipTasksBlockedByFailure`) | skipped | the failure is the stop's, or loses to it, as in `retryOrFail` |
| a `blocked` answer that arrives with the stop | skipped | its `task.failed` event outlives the stop and `failTask` does not |
| `task.succeeded` / `verification.passed` | written only if the transition to `succeeded` was | an event that outlives the stop must not record a success it refused |
| the next tool call | not started | its ledger read would fail and read as "never ran" |
| `recordToolCall` for a call the stop cut short | skipped | it did not fail, and a failed row would hold its idempotency key |
| `SaveCheckpoint` after an iteration the stop cut short | skipped | its last tool result may be only the stop; the previous checkpoint is the resume point |
| heartbeat error | returns quietly when its context is done | the task ended or is being handed back; no lease was lost |
| `Claim` error in `Run` | loops to the stop check | a stopped claim is not an idle-queue error |

`outliving` is `context.WithTimeout(context.WithoutCancel(ctx), afterTaskTimeout)`, the same bound #105 gave
`afterTask`.

### Why a transaction for the gate, and not the alternatives

- **Writing both on a context that outlives the stop** makes a stop harmless, but not a crash. It also turns
  opening the gate, which is a decision, into something a stopping worker finishes.
- **Reconciling on the next claim** (write the missing event when a handed-back task finds an open request) is a
  second place that records a request, and the gap stays until some worker comes back to the task.
- The transaction is smaller than either: the insert moves into `db.InTx`, and the event is
  `repo.AppendEvent(ctx, tx, …)`, the one place events are written. A stop inside it rolls both back, and
  `failTask`'s stop guard keeps the rollback from being logged.

`OnApprovalRowWrittenForTest` sets a hook that runs between the insert and the event, the way `AfterTaskForTest`
and `SettleGoalForTest` expose their seams. It is nil outside the fence.

## Verification

Written first on #108's branch, with only the seam added, red as shown above, green with the fix:

- `TestWorker_AStopBetweenOpeningAnApprovalRequestAndRecordingItLeavesTheTimelineAndTheApprovalsAgreeing`
  (internal/agent/worker_stop_test.go) stops the worker through the seam. The task is handed back, nothing
  reports the database unavailable, and once the next worker has parked it at the gate the task has exactly one
  approval request and exactly one `approval.requested` event.
- `TestWorker_AWorkerStoppedAnywhereInATaskKeepsWhatItDidAndLogsNoDatabaseFailure` runs a worker whose heartbeat
  beats every 2 ms, in a fresh schema per stop point, and stops it:
  - before its task starts;
  - inside a model call;
  - as a model call answers that the task is done (tokens counted, no `task.succeeded`);
  - as a model call answers that the task is blocked (tokens counted, no `task.failed`);
  - as a tool call finishes (one `succeeded` ledger record, and the model's next call never runs);
  - inside a tool call (no ledger record, no checkpoint);
  - during verification;
  - at the approval gate.

  At every point the task is handed back (#108's `requireHandedBack`, which now also requires no
  `task.succeeded`) and the log holds no DATABASE_UNAVAILABLE.
- `TestWorker_AWorkerStoppedAsItRecoversACrashedWorkersTaskRecordsTheRecoveryAndLogsNoDatabaseFailure` stops
  the worker when it logs `forge.task.lease_expired` for a task whose lease a crashed worker left behind. The
  `task.lease_expired` event is still written, and the claim the stop refuses logs nothing.
- #87's, #105's and #108's fences still pass. `go vet ./...` and
  `go test -count=1 ./internal/agent/ ./internal/domain/engine/` are clean.

## Regression prevention

Drills in `scripts/drill-fences.sh`, section "A stopped worker keeps what it did". Each was run and went red.
Every mutation keeps the code compiling, so a red comes from the fence and not from the build:

- "the approval request and its event are written apart" moves the insert out of the transaction.
- "a failure reached while stopping is still written" removes `failTask`'s stop guard.
- "a decision the stop refused is logged as a database failure" logs every failed transition.
- "a heartbeat the stop cancelled reports a lost lease" removes the heartbeat's guard.
- "a blocked answer that arrives with the stop is recorded as a failure" removes the blocked guard.
- "a success the stop refused is recorded" ignores the transition error before `task.succeeded`.
- "an event is written on the context the stop cancelled" gives `appendEvent` a context cancelled with the run's.
- "a claim the stop refused is logged as a database failure" removes `Run`'s claim guard.
- "the tokens a stopped call spent go uncounted" does the same to `RecordSpend`.
- "a tool call that ran is lost from the ledger" does the same to `recordToolCall`.
- "a stopped worker runs its next tool call" removes the tool loop's guard.
- "a tool call the stop cut short is recorded as failed" removes the skip in `runTool`.
- "a checkpoint is saved from an iteration the stop cut short" removes the checkpoint guard.

The executor is now in the script's FILES list.

## Not in this fix

- **A statement the stop cancels in flight.** A stop that lands while a transition or a claim is on the wire
  cancels it, and Postgres may already have committed it. The worker then sees an error. A transition to
  `succeeded` that committed that way has no `task.succeeded` event. A claim that committed that way is left to
  the reaper. Only the approval request and its event are atomic against this.
- **An event written before its decision.** `budget.exceeded` and `verification.failed` are written before the
  transition they lead to. A stop between the two keeps the event, which is true, and skips the decision. The
  next attempt can record the event again.
- **Warnings that are not database failures.** A stop during verification still logs `forge.verification.ran`
  with `error="context canceled"` (code INTERNAL). A stop can make a read fail, for example the character
  fallback, the secret handles or the raw tool output, and those still warn about the read.
- **The suspected-injection event** (`Executor.appendInjectionEvent`) still writes on the run context. A stop
  right after a tool returns injection-shaped output loses the timeline event. The WARN log line is still
  written.
- **The idle sweep.** `reapExpired`'s own error, `releaseWaitingGoals` and `settleFinishedGoals` log as before if a
  stop lands during them rather than in the idle sleep, which is where a stop almost always lands.
- **Work repeated after a hand-back**, as #108 records.
