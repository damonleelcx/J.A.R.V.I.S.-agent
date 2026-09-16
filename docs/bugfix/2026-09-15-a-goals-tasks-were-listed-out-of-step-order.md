# A goal's tasks were listed out of step order

**Date:** 2026-09-15 · **Status:** fixed (stacked on #104, build goal UX) · **Severity:** low: the operations console
showed a build's steps scrambled, so the order work runs in could not be read from it

## Summary

`engine.Repository.ListTasks` ordered by `created_at` alone, and `PlanApplier.Apply` writes every task of a plan at one
instant. The order among them was whatever Postgres returned. The operations console, which renders tasks in the order
`GET /v1/goals/{id}` returns them, listed a live five-step build as 1, 2, 5, 3, 4. Plans now write their order into
`created_at`, a microsecond per position. `ListTasks` breaks remaining ties by idempotency key, which for a build step
is the zero-padded `build-step-NN`, and then by id.

## Symptom

Live, 2026-09-15 ([`docs/spikes/2026-09-15-build-goal-exercised`](../spikes/2026-09-15-build-goal-exercised), §1):
the console's goal panel for the desk lamp listed its five SUCCEEDED tasks as steps 1, 2, 5, 3, 4.

## Impact

- **A build's plan could not be read from the console.** Steps depend on the step before, so the listed order was not
  the order they ran in, and a reader matching the timeline to the list was misled.
- **Not only builds.** Any plan's tasks shared one `created_at`. An ordinary plan's order was just as arbitrary, less
  visibly so, because its titles carry no step numbers.
- **Nothing ran out of order.** Readiness comes from the dependency edges, not this listing.

## Preconditions

Any goal with more than one task created in one `Apply`: every plan and every replan's new tasks.

## Root cause

```go
`select `+taskColumns+` from forge_tasks where goal_id = $1 order by created_at asc`
```

with, in `internal/agent/apply.go`, `CreatedAt: now` for every task in the loop. Task ids are ULID-like (a millisecond
prefix, then 80 random bits), so even an order by id would have been random within the one millisecond.

## Why it did not show up before

- **The fences sort before they compare.** `buildgoal_db_test.go`'s `tasks` helper sorts by idempotency key, and the
  HTTP build fence indexed the tasks it got without checking their titles against their positions.
- **Small plans often list in order by luck.** Two or three tasks from one instant come back in insertion order often
  enough that nobody noticed. The live five-step build was the first long build read in the console.

## Fix

- `PlanApplier.Apply` stamps each new task's `created_at` (and `updated_at`) with `now + position µs`: its place in the
  plan, at the column's resolution. It is still the application clock's time; nothing reading a time can see a
  microsecond. A replan's reused tasks keep their original stamps, and its new tasks list after them in plan order.
- `ListTasks` orders by `created_at, idempotency_key, id`. The tie-break is for rows written before the stamp, including
  the live goal: build steps come back in step order, and anything else lists the same way on every read.

## Verification

- `TestListTasks_ABuildsStepsWrittenAtOneInstantAreListedInStepOrder` (internal/domain/engine, Postgres) writes twelve
  `build-step-NN` tasks out of order at one instant and requires them listed 01 to 12.
- `TestApply_APlansTasksAreListedInThePlansOrder` (internal/agent, Postgres) applies an eight-task plan whose keys sort
  differently from its order, and requires the plan's order back.
- `TestGetGoal_ABuildsTasksAreListedInStepOrder` (internal/httpapi, Postgres) plans a twelve-step build through
  `POST /v1/goals` and requires `GET /v1/goals/{id}` to return "Step 1 of 12" to "Step 12 of 12" in order.

## Regression prevention

`scripts/drill-fences.sh`, section "Build goal UX": "tasks written at one instant have no order" (drops the tie-break)
and "a plan's order is not written" (drops the stamp). Both went red.

## Not in this fix

- **The console itself.** It renders what the endpoint returns, and needed no change.
- **Existing ordinary plans.** Rows written before this with non-build keys list by key: stable, but not necessarily
  plan order. Nothing on those rows records the order they were planned in.
- **Timeline ordering** is by `seq` and was never affected.
