# Live findings fixed, and one re-check (2026-09-17)

Stacked on PR 148 (`docs/spikes/2026-09-17-live-verification`). This fixes that PR's follow-ups 3, 4 and 5, the
step that read "succeeded" having kept nothing, and `make measure-car` on Windows. Then one live re-check of
PR 148's run 3 build goal was made, with a goal ceiling of 60,000 tokens.

## The re-check

- **Setup.** Local `forged` and `forge-worker` built from this branch, talking over HTTP. Postgres `forge-pg`
  :55840 on a fresh schema `forge_livefix` (migrations 0001–0025). MinIO :55841. The real kernel (build123d
  0.11.1). Endpoint `token-plan.cn-beijing.maas.aliyuncs.com` (converse `qwen3.7-plus`).
- **The goal.** PR 148's run 3 statement, word for word: *a road-car wheel: a rim, a tyre, a hub, and five lug
  nuts on a polar pattern around the hub's axis. Build it in at most three steps.* It was created with
  `"build": true, "max_tokens": 60000`. `FORGE_MAX_TOKENS_PER_GOAL` was also 60,000.
- **The key.** Loaded only into the shell that started the two processes, from `jarvis-a4/.env`. It was never
  printed. The committed files were scanned for the key's value and for the provider's key prefix: 0 hits.

| | |
|---|---:|
| tokens spent | **43,717 of 60,000** |
| model calls | 4: the plan (506) in forged, then 13,761, 14,913 and 14,537 in the worker |
| spend reconciles | 506 + 13,761 + 14,913 + 14,537 = 43,717 = `tokens_spent` |
| largest call stored | 14,913 (`largest_call_tokens`) |
| time | 68 s, longest poll with no change 5.3 s |
| end state | **failed**: a budget stop in step 3, with 16,283 tokens left |

**The ceiling held, and nothing overshot.** Step 3's first call (14,537) was placed at 29,180 + 18,641 ≤ 60,000.
Its next call, a repair, would have been placed under the old rule, because 43,717 < 60,000, and could have
landed at 58,000 or more. It was not placed. The timeline says:

> Budget exhausted on tokens: used 43717 tokens of 60000. 16283 tokens were left, and the next model call was not
> placed because it may cost 18641 (the largest call this goal has made, 14913 tokens, and a quarter more), which
> would pass the ceiling. The goal stops here rather than spend past it.

**Steps 1 and 2 were refused and kept nothing, and the goal said so.** Both replies were unreadable JSON, a failure
PR 148's run did not hit:

- **Step 1** wrote a standard designation as a parameter's value:
  `{"name": "lug_nut_standard", "value": "ISO 4032 M12"}`. A parameter's value is a number.
- **Step 2** wrote `"radius_from": "hub_center_diameter / 2"` inside `"size"`. The contract's key for that is
  `"size_from"`.

Each step's task reads `Step 1 of 3 (Hub Assembly): refused, kept nothing, and nothing is kept yet. Step 1 … came
back unreadable: …`. The goal's outcome reads `… 2 build step(s) were refused, kept nothing: Step 1 of 3 — Hub
Assembly; Step 2 of 3 — Rim and Tyre.` Before this branch, both would have read as ordinary successes.

**Not answered by this run:**

- **Whether the lug nuts are buried.** No step kept a wheel. Nothing reached the kernel's interference check or
  the look.
- **Whether the doubled-offset note fires live.** An unreadable reply never reaches assembly, so the check had no
  document to read. The note is proven on run 3's kept wheel (`TestDoubledOffsets_…`, below), not on a new live
  reply.

**Budget for today.** PR 148 spent 223,490 of 300,000. This run spent 43,717, so the total is 267,207 and 32,793
are left. No retry was made:

- the failure was the model's, not the infrastructure's;
- 43,717 + 60,000 would pass the 76,510 that remained.

## What changed

1. **Doubled offsets** (`internal/agent/doubled.go`).
   - **The rule.** A child that places a *definition* is reported when, on some axis, the definition's position
     and the child's are both non-zero and the same amount (the same number, or the same `position_from`
     expression). It is also reported when both carry the same non-zero rotation.
   - **What the expansion does.** It composes the two as child position + child rotation · definition position.
     So run 3's nut, written `[50, 20, 0]` turned `[90, 0, 0]` in both places, lands at `[100, 20, 20]`. The
     note works that number out with the expansion itself.
   - **What is not reported.** A different offset in each place, or an offset in only one place (the contract's
     spoke), is ordinary and never reported.
   - **Who is told.** The turn and the step say it, with where the part lands and the fix. The next step's prompt
     and the next turn's prompt carry it too. Nothing is rewritten.
   - **The contract** now says a placement goes in one place, using that example.
2. **A goal does not spend past its ceiling** (`engine.BudgetGuard.CheckCall`, `agent/spend.go`, migration
   0025).
   - **The reservation.** Every build call reserves the largest call the goal has made, plus a quarter, for itself
     and for each call in flight. This is PR 148's meter rule, applied to the product.
   - **Stored, not held in memory.** The largest call is kept on the goal and written in the same statement as
     the spend. A step on another worker, or after a restart, reads it.
   - **The stop.** It uses the existing budget-stop text and adds how much was left and why. A step stopped
     part-way now writes `budget.exceeded` on the timeline, like a step stopped before it began.
   - **The card.** `plainStopText` is unchanged, and the card shows the reason (fenced).
   - **The one call it cannot bound.** A goal's first call reserves nothing, because nothing is known yet. That
     call is the plan's: 506 tokens here.
3. **The wheel's turn is taught from the car's own forward axis, both ways.**
   - Forward along Z: the axles run along X, and a wheel is turned `[0, 0, 90]`.
   - Forward along X: the axles run along Z, and a wheel is turned `[90, 0, 0]`.
   - Both rotations are measured against what FORGE draws.
4. **A refused step is said.**
   - **On the step.** `buildStepResult.Refused` is set, and the step's summary reads `refused, kept nothing`. It
     never says "kept as version", so the card does not count the step before's version twice.
   - **On the goal.** The outcome lists the refused steps.
   - **On the card.** A note names each refused step.
   - **The goal still succeeds when an earlier step kept a valid model.** It fails with `BUILD_KEPT_NOTHING` when
     every step was refused, because then there is no model.
5. **`make measure-car`.**
   - `CAD_PYTHON` is `.cadvenv/Scripts/python.exe` on Windows and `.cadvenv/bin/python` elsewhere.
   - `FORGE_CAD_PYTHON` is still used first when it is set.

## Fences and drills

Every drill below was run on its own and seen red: **21 red, 0 green, 0 unproven**.

| drill | fence |
|---|---|
| a doubled offset is not named | `TestDoubledOffsets_ADefinitionAndItsChildCarryingTheSamePositionAreNamed` |
| a step does not say what it doubled; the next step is not told what the model so far doubles | `TestAssemble_AStepThatDoublesAnOffsetIsToldWhereThePartLands` |
| a turn / a streamed turn does not say what it doubled; the next turn is not told | `TestConverse_ATurnThatDoublesAnOffsetSaysWhereThePartLands` |
| the contract no longer says a placement goes in one place | `TestTheContractSaysAPlacementGoesInOnePlace` |
| the contract teaches one layout's wheel turn | `TestTheContractTeachesAWheelsTurnFromTheCarsForwardAxis` |
| measure-car looks for bin/python on Windows | `TestMeasureCar_PicksTheVenvPythonForThisOS` |
| a refused step reads as kept; a goal does not say a step was refused | `TestBuildGoal_ARefusedStepIsSaidOnTheStepAndTheGoal` (Postgres) |
| the card does not say a step was refused | `TestWorkbench_TheCardSaysAStepWasRefusedAndKeptNothing` |
| a goal call reserves nothing; goal calls in flight reserve nothing; a goal stop does not say what was left | `TestCheckCall_AGoalNeverPlacesACallItsCeilingCannotPay` |
| the build client refuses only once the ceiling is reached; a step the budget stopped part-way says nothing on the timeline | `TestBuildGoal_AStepDoesNotPlaceARepairItsCeilingCannotPay` (Postgres) |
| a goal's largest call is not stored | `TestBuildGoal_AGoalStopsBeforeACallThatWouldPassItsCeiling` (Postgres) |
| the card shows a budget stop with its plumbing | `TestWorkbench_TheCardSaysWhyABuildStoppedWithTokensLeft` |

Two existing drills were re-anchored because the new notes follow the code they point at:

- "the turn does not say what could be one pattern"
- "an ordinary turn is not told what could be one pattern"

Both were seen red again.

`TestBuildGoal_ABudgetRefusalStopsTheGoalCleanly` now uses a ceiling of 250 instead of 150. Under the reservation,
150 would refuse step 1 before its call, which is exactly what the change is for. The test still asserts what it
did: the plan and step 1 fit, and nothing after them is asked.

## Found, not fixed

- **Two new ways a live step came back unreadable:** a string as a parameter's value, and `"<dim>_from"` inside
  `"size"`. Both are shapes `dimensionrepair.go` could read rather than refuse, as it already does for an
  expression in a numeric slot.
- **An ordinary (non-build) goal's executor does not reserve.** `executor.go` calls its model directly and
  records the spend after the call. The worker checks the ceiling only before a task, so an executor task can
  still pass the ceiling by a task's worth of calls. This branch covers build goals, the path named in the
  finding.

## Files

- `data/`: the driver's log, the goal and its tasks as the API returned them, the progress a 1 s poller saw, the
  goal's timeline and tasks read from Postgres, and the `forge.llm.completed`, task, goal and budget lines from
  both logs. Session tokens were scrubbed, and no prompts are kept.
- `harness/`: the scripts that ran it (`prep.sh`, `run3.sh`, `run3.py`, `env.sh`, `collect.py`).
