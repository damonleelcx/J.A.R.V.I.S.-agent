# A build planned more steps than the person allowed

**Date:** 2026-09-15 · **Status:** fixed (stacked on #104, build goal UX) · **Severity:** low: a longer and costlier
build than asked for, started only after the person saw the plan

## Summary

A build's planner was never told a limit the person stated. Asked for a desk lamp "three steps at most", the live
planner returned five steps, and the plan went to the card as five. `planBuild` now reads a stated maximum from the
request. It tells the planner, and if the plan still comes back over the limit, folds the extra steps into the last
step allowed. The plan's rationale says so.

## Symptom

Live, 2026-09-15 ([`docs/spikes/2026-09-15-build-goal-exercised`](../spikes/2026-09-15-build-goal-exercised), §1).
The request: *"…a round weighted base, a two-segment arm with a hinge, and a conical shade. Keep it simple, three steps
at most."* After ticking "Build it as a model" and pressing Start this, the card read "R1 · PLANNED, NOT RUNNING" with
**5 steps** (Base, Lower Arm Segment, Elbow Joint, Upper Arm Segment, Lampshade). The button was **Start it — run 5
tasks**.

## Impact

- **Cost.** A build step on this deployment is ~11,000 tokens with its look, so two unrequested steps are ~22,000
  tokens. On the live run that was a quarter of the exercise's cap.
- **Trust in the plan.** The card is where a person authorises work (PRD AGT-02). A plan that ignores what they said
  makes them read it for what it dropped, rather than for what it will do.

## Preconditions

A build (workbench "Build it as a model", `POST /v1/goals` with `"build": true`, `forgectl goal new --build`, or the
workbench's in-turn assembly) whose request states a maximum number of steps, and a planner that returns more.

## Root cause

`internal/agent/assemble.go`:

```go
{Role: llm.User, Content: "Plan the build of: " + asked},
```

`planSystem` says "Between 2 and 10 steps", and the only cap in code was `assemblyBudget` (12). Nothing read the request
for a limit, so the model saw "three steps at most" only as part of the object's description and was free to plan past
it.

## Why it did not show up before

- **Every planning fence gives the planner a request with no limit** ("a car", "a sports car") and a stub that returns
  2–3 steps, and asserts the steps come through unchanged.
- **The first builds were exercised through the API** with no stated limit. The workbench run was the first to phrase a
  build the way a person would.

## Fix

- `statedStepLimit(asked)` reads a maximum said about steps, passes or stages. It accepts digits or words up to twelve,
  in forms like "at most three steps", "no more than 3 steps", "5 steps max", "fewer than four steps" and "a maximum of
  6 build steps". It reads nothing else: "in three steps" describes and does not limit, and "three parts at most" is not
  about steps. With two limits, the smaller wins.
- `planBuildNoted` (`planBuild` keeps its signature) adds "The person asked for at most N steps. Plan no more than N…"
  to the request.
- If the reply is still over the limit, `combineSteps` folds steps N onward into step N. The names are joined with " +
  " and the sentences kept in order. The assembly is kept only when every folded step named the same one; otherwise the
  step is shown the model so far. **‼️ Combined, not cut:** cutting the lamp's plan at three would have dropped the
  shade, which the person asked for as surely as the limit.
- The note reaches the reader. `planBuildGoal` appends it to the plan's rationale, which the card, the `POST /v1/goals`
  reply and the `plan.created` timeline entry show. `assemble` adds it to the turn's notes.
- The conversation contract asks that a proposed goal's `statement` carry any limit the person stated, in their words,
  because the statement is all the planner reads.

## Verification

- `TestStatedStepLimit_ReadsAMaximumAndNothingElse`: eight phrasings read, four non-limits refused.
- `TestPlanBuild_AStatedMaximumOfStepsIsHonouredAndSaid` answers the live request with the live five steps. It requires
  the prompt to say "at most 3 steps", three steps with the hinge, upper arm and shade all in step 3 and no single
  assembly on it, and a note naming 3 and 5. A plan within its limit comes back unchanged and unannotated.
- `TestPlanBuildGoal_AStatedMaximumOfStepsIsTheGoalsPlanAndItsRationaleSaysSo` (Postgres) requires the goal's plan to be
  three tasks, the third "Upper Arm Segment + Lampshade", and the rationale to say the steps were combined.

## Regression prevention

`scripts/drill-fences.sh`, section "Build goal UX": "a plan over the stated limit is kept whole" (skips the combine) and
"the planner is not told the limit" (drops the sentence from the request). Both went red.

## Not in this fix

- **Whether the live planner now keeps to the limit on its own.** Not run live (no model calls in this change). The cap
  holds either way.
- **Whether the conversation model carries the limit into the proposal's statement.** A contract sentence, unmeasured.
  If the statement drops it, the planner never sees the limit, and nothing here can recover it from the conversation.
- **Merged steps' quality.** A combined step asks one model call for more, and whether it builds all of it as well as
  separate steps would is not measured.
- **Limits in other units** ("under 50k tokens", "keep it under a minute") are not read; a token ceiling now has its own
  field (`max_tokens`).
