# A budget refusal left its task claimed, so a goal past its budget never stopped

**Date:** 2026-09-15 · **Status:** fixed on main (found building stage A1, #85) · **Severity:** high — a spent goal stays active and its task is claimed again every lease

## Summary

`Worker.runTask` checks the goal's budget before any work, right after claiming the task. On a breach it wrote the
`budget.exceeded` event and called `failTask`, which transitions the task to `failed`. But the task was still
`claimed`, and `claimed → failed` is not a legal transition (claimed may go to running, ready or cancelled). The
transition was refused, the refusal was only logged, and the task stayed claimed until its lease ran out — when the
reaper returned it to `ready`, a worker claimed it, and the same refusal happened again.

The fix moves the task to `running` first, then fails it, so the refusal takes effect: the task is failed, the tasks
after it are skipped, and the goal settles `failed`.

## Symptom

- Found by stage A1's fence: a build over its token ceiling ended with steps
  `succeeded; claimed; pending` and never settled; the log carried
  `"claimed" → "failed" is not a legal transition` once per lease.
- The goal stays `active` forever. The budget event is written again on every reclaim.

## Impact

PRD's budget guarantee — a goal that has spent its allowance stops — did not hold for any goal that crossed its ceiling
between tasks, which is where `CheckGoal` runs. The worker kept claiming the task (bounded only by `max_attempts`, each
reclaim waiting a full lease).

## Root cause

The breach branch in `runTask` predates the state machine's rule that a claimed task must start running before it can
end. `failTask` ignores `transition`'s error by design (it logs), so nothing surfaced.

## Why it did not show up before

No test drove a worker into a budget breach. `budget_test.go` tests `CheckGoal`'s arithmetic, not what the worker does
with its answer.

## Fix

In the breach branch, transition `claimed → running`, then `failTask`. The task never did work; `running` is the state
the machine requires before `failed`, and `started_at` records when the refusal happened.

## Verification

`TestWorker_ABudgetRefusalStopsTheGoal` (internal/agent/worker_release_test.go) plans two tasks, spends the goal's
token ceiling before the worker starts, and runs the real worker on Postgres: the goal must end `failed`, the tasks
`failed` and `skipped`, `budget.exceeded` must be on the timeline, and the model must never be asked. Without the fix
it times out with the first task claimed.

## Regression prevention

A drill in `scripts/drill-fences.sh` ("a budget refusal fails a task that is only claimed") removes the transition; the
fence goes red.

## Not in this fix

The stacked branches carry the same change in #85 (stage A1).
