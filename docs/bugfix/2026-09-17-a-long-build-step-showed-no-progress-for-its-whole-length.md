# A long build step showed no progress for its whole length

**Date:** 2026-09-17 · **Status:** fixed on `goals/unverified-paths` · **Severity:** medium — PRD NFR-02 ("long jobs report progress at least every 10 s") did not hold for any step longer than 10 s

## Summary

While a build step runs, nothing a client can read about its goal changes: the goal, its tasks and its timeline are
written when the step starts and when it ends. The only write in between is the lease heartbeat, every
`FORGE_LEASE_HEARTBEAT` (default 20 s), and nothing exposed it. A step is a model call, the kernel, a look and any
repairs; live steps have taken 5-15 s each and a whole live build averaged over a minute per step.

## Symptom

A 10-step build through the real `forged` and `forge-worker`, the stand-in model taking 12 s a step, polled once a
second over `GET /v1/goals/{id}` and `GET /v1/goals/{id}/timeline` (`docs/spikes/2026-09-17-unverified-paths`, tag
`long1`): the longest time with nothing changed was **13.08 s**, and 10 of 12 gaps were 11.7-13.1 s.

## Fix

- The worker's heartbeat runs at least every `agent.AliveEvery` (5 s), whatever `FORGE_LEASE_HEARTBEAT` says. Each beat
  writes the task's row, and the row's `forge_set_updated_at` trigger stamps `updated_at`. Renewing a lease more often
  than needed is harmless: one UPDATE of one row per running task.
- `TaskDTO.last_seen_at` shows that stamp, for a task a worker holds (`claimed`, `running`, `verifying`) and for no
  other: a stamp on a finished or waiting task would read as a worker still busy with it.

5 s rather than 10 so that a client polling every few seconds sees a stamp younger than 10 s.

No migration: the stamp is the column and trigger the table already has.

## Verification

- Live, same build (tag `long2`): the longest gap is **5.34 s**; every task in `running` carries a `last_seen_at` that
  advances by 5 s.
- `TestWorker_ARunningTaskIsStampedAliveWhileItsModelCallRunsWhateverTheLeaseHeartbeat` — a worker with a 1 h lease
  heartbeat, blocked inside a model call, stamps its task at least 4 times in 600 ms (the beat shortened to 40 ms).
- `TestAliveEvery_LeavesAClientPollingAtItAFreshStampInsideTenSeconds` — `2 × AliveEvery ≤ 10 s`.
- `TestTaskDTO_AHeldTaskSaysWhenItsWorkerWasLastSeenAndOtherTasksDoNot`.

## Not done

The workbench's goal card does not show `last_seen_at` yet (the card is other work in progress); the API has it. The
stamp says the step is still being worked on, not which phase (model, kernel, look) it is in.

## Regression prevention

Drills: "a running task is stamped alive only as often as its lease heartbeat", "a running task is stamped alive every
20 s", "a held task does not say when its worker was last seen", "every task says when a worker was last seen, held or
not" — all seen red.
