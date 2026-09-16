# A finished task never released the tasks waiting on it, so every plan with a dependency stopped after its first layer

**Date:** 2026-09-15 · **Status:** fixed on main (found building stage A1, #85) · **Severity:** high: a goal stalls silently and stays active forever

## Summary

A plan's tasks start `pending`, and `PromoteReadyTasks` is what makes a pending task `ready` once everything it depends
on has finished. It was called in exactly one production place: `PlanApplier.Apply`, when the plan is written. That
promotes the first layer (the tasks that wait on nothing), and nothing ever promoted again. `Claim` takes only `ready`
tasks, so a task depending on another stayed `pending` after its dependency succeeded, and no worker would ever take it.

The fix asks `PromoteReadyTasks` again at the two moments the answer can change: right after the worker finishes a
task, and on the idle poll, beside the sweep that settles finished goals.

## Symptom

- A goal planned as `a → b → c`: `a` runs and succeeds; `b` and `c` stay `pending` indefinitely.
- The goal stays `active` with outstanding work. `settleGoal` never settles it, because work is outstanding. The
  wall-clock budget never stops it, because `CheckGoal` runs only when a task is claimed.
- Measured on the first build run as a goal (stage A1): after 60 s, `build-step-01 succeeded; build-step-02 pending;
  build-step-03 pending`.

## Impact

Every goal whose plan has a `depends_on` edge does its first layer and then nothing, with no error, no event and no log
line. That covers the executor path's planner-written plans and, from stage A1, every build, whose steps are a chain.

## Preconditions

A plan with at least one dependency, run by `forge-worker`. A plan of independent tasks is unaffected, because all of
its tasks are promoted by `Apply`.

## Root cause

`internal/agent/worker.go` `Run` claims, runs and settles; `runTask` transitions the task. None of them calls
`PromoteReadyTasks`, and `TransitionTask` is a single-row update with no promotion in it. There is no database trigger
either: the only triggers maintain `updated_at`.

## Why it did not show up before

- **The worker tests seed independent tasks.** `seedTwoTasks` says so in its name. The settle and approval-gate tests
  drive transitions by hand with `markTerminal` and never need a dependent task to be claimed.
- **The drill harness promotes for itself.** `internal/drill/harness.go` and `scenarios.go` call `PromoteReadyTasks`
  directly, so the drills exercised a promotion the worker never performed.
- **The engine's own promotion tests call it explicitly.** `queue_integration_test.go` and `promo_test.go` do, and they
  are right to, because they test the query. Nothing tested that the worker asks it.

## Fix

- `Worker.releaseWaiting(goalID)` calls `PromoteReadyTasks` after every `runTask`, before the next claim. This is the
  fast path; on the idle poll alone, a worker kept busy by other goals would never reach it.
- `Worker.releaseWaitingGoals` runs on the idle poll for active goals that still have pending tasks. It is the guarantee
  for a worker that dies between a task's last write and the release, by the same rule that put `settleFinishedGoals`
  beside `settleGoal`.
- Readiness is still decided by `PromoteReadyTasks` from the edges, including its refusal to treat a dependency skipped
  as unreachable as satisfied. The fix only calls it.
- New log event `forge.task.release_failed`.

## Verification

- `TestWorker_AFinishedTaskReleasesTheTasksWaitingOnIt` (internal/agent/worker_release_test.go) plans two tasks, the
  second depending on the first, and runs them through the real worker, queue and executor on Postgres with a model that
  completes every task at once. The idle poll is set to an hour, so the second task runs only if the first released it.
  Without the fix it times out with the second task pending.
- `TestWorker_ATaskLeftWaitingByACrashIsReleasedOnTheIdlePoll` finishes the first task by hand, as a worker that died
  before releasing would, and requires the idle poll to release and run the second.

## Regression prevention

Two drills in `scripts/drill-fences.sh` ("a finished task releases nothing" and "the idle poll releases nothing"). Each
removes one call, and each fence above goes red.

## Not in this fix

- **The stacked branches carry the same change** in #85 (stage A1), where the fences run through build goals.
- **No real model has driven a dependent plan through this fix.** The fences use a stub model that completes every task;
  the planner and executor paths are otherwise the production ones.
