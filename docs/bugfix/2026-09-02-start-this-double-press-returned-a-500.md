# Pressing "Start this" twice told the user to file a bug report

**Date:** 2026-09-02 · **Phase:** 6 (workbench) · **Severity:** medium — no data
loss, but the interface accused the user of a defect for double-clicking ·
**Owner:** httpapi / engine state machine

## Symptom

`POST /v1/goals/{id}/start` on a goal that was already active returned **HTTP
500**:

```json
{"code":"INVARIANT_VIOLATED","category":"data",
 "message":"An operation would have broken a domain invariant and was refused.",
 "remedy":"Report this with the surrounding request_id: it indicates a logic defect, not a user error."}
```

The goal was fine. The user had clicked Start twice.

## How it was found

By clicking Start twice, on the live workbench, against the real database,
immediately after the endpoint first worked. It was not found by reading the
code, and no unit test would have found it: every layer behaved exactly as
designed.

## Root cause

Three correct decisions composing into a wrong answer:

1. `engine.ValidateGoalTransition` rejects `active → active`. Correct — the
   state machine's job is to refuse transitions that are not in the graph.
2. That refusal carries `CodeInvariantViolated`. Correct — from inside the
   domain, a transition that is not in the graph *is* an invariant violation.
3. `errs` maps `INVARIANT_VIOLATED` to 500 with the remedy *"Report this … it
   indicates a logic defect, not a user error."* Correct — when the domain
   catches an impossible state, that is exactly what has happened.

The defect was at the **HTTP boundary**, which passed a user's second click
straight into the domain and then rendered the domain's answer to the user. A
second press is not a second transition request; it is the same request arriving
twice.

## Why it matters more than it looks

The message is not merely unhelpful, it is **false**. It tells the person that
the system has a logic defect and asks them to report it, for an action that
worked. Anyone who trusts our error text now distrusts it.

## Fix

`StartGoal` resolves the goal's status before touching the state machine
(`internal/httpapi/goals_start.go`):

| status on arrival | result |
| --- | --- |
| `draft` | activate, write the `goal.activated` timeline event, 200 |
| `active` | 200, *"Already running — this goal was started earlier. Nothing changed."* No second event. |
| anything else | 409 `CONFLICT`, naming the state it is actually in |

This is the same reasoning already applied to `POST /v1/auth/sign-out` in this
package: *a request that arrives with nothing left to do has already achieved
its purpose.* The comment there was the precedent; this endpoint simply had not
inherited it.

A genuinely concurrent double-start still conflicts — `Activate`'s UPDATE is
guarded by `and status = $3`, so the loser affects zero rows and gets a 409. Two
presses are idempotent; two simultaneous presses are a race, and those are
different facts.

## Regression fence

`internal/httpapi/goals_start_test.go` ·
`TestStartGoal_AlreadyActiveIsNotAnError` — presses Start twice against live
Postgres, requires 200 both times, **and** asserts exactly one `goal.activated`
event, so a fix that made the call succeed by activating twice would still fail.

**Mutation drill run 2026-09-02.** Collapsing the switch back to
`case engine.GoalDraft, engine.GoalActive:` reproduces the original 500 and the
test goes red with the original error text. Restored, it passes. The fence can
go red for the reason it claims to.

## Related

- `docs/prd.md` AGT-08 — proposed, running and completed are distinct states,
  never implied falsely. An already-running goal reported as a failure blurs
  exactly that line.
