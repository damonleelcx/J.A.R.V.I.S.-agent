# A goal's planner and verifier calls were never charged to it

**Date:** 2026-09-17 · **Status:** fixed on `goals/unverified-paths` · **Severity:** medium — every ordinary goal's `tokens_spent` was short, and its token ceiling never saw planning or verification

## Summary

A goal's spend is what `BudgetGuard.RecordSpend` has been told. The executor records after each of its own calls, and
a build's `chargedClient` (#85) records every call a build makes, planning included. Nothing else did. An ordinary
(non-build) goal's planning call went through `Intake.Plan` → `Planner.Plan` on the plain client, and every
verification went through `Verifier.Verify` on the plain client, which filled `Verdict.Usage` and nothing read it.

## Symptom

Found by running an r2 goal through the real `forged` and `forge-worker` against a stand-in model that logs every call
it serves (`docs/spikes/2026-09-17-unverified-paths`, scenario `approval-approve`, tag `appr1`):

| served by the stand-in | tokens |
|---|---:|
| planning | 450 |
| executor, 2 tasks | 1,200 |
| verifier, the r2 task | 500 |
| **served** | **2,150** |
| **the goal's `tokens_spent`** | **1,200** |

The same goal with the fix (tag `appr3`): 2,150 served, 2,150 spent.

## Impact

- `tokens_spent` on every ordinary goal left out its planning, and on every r2+ task left out its verification.
- The per-goal ceiling (`FORGE_MAX_TOKENS_PER_GOAL`, or `max_tokens` on `POST /v1/goals`) could be passed by exactly
  those calls without a refusal.
- Build goals were not affected by the planning half (#85 charged them); an r2+ task in any goal was affected by the
  verifier half. Build steps are r1 and not verified.

## Root cause

`chargedClient` was introduced for builds, and the note on it says so: "a build's Conversation is handed this client".
The two other callers of the model that act for a goal, the planner and the verifier, were never given one.

## Fix

- `Intake.Plan` plans with a copy of the planner whose client is `chargeTo(...)` the goal (the shared planner is not
  changed).
- `Verifier.chargedTo` returns a copy of the verifier with a charged client; `Worker.completeTask` verifies through it.

Both record on a context that outlives a stop (`chargedClient` already does), and both refuse a call once the goal's
ceiling is reached, as a build's calls are refused.

## Verification

- `TestIntake_AnOrdinaryGoalsPlanningIsChargedToTheGoal` — planning with a stub that reports 450 tokens leaves the goal
  at 450 (was 0).
- `TestWorker_TheVerifiersCallIsChargedToTheGoal` — two r2 tasks through the real worker on Postgres: 2 × (10 executor +
  7 verifier) = 34 (was 20).
- Live, the stand-in's served total equals `tokens_spent` for the ordinary goal (above) and for every build and stop run
  in the spike.

## Regression prevention

Drills in `scripts/drill-fences.sh`: "an ordinary goal's planning is not charged to it" and "the verifier's call is not
charged to the goal", both seen red.
