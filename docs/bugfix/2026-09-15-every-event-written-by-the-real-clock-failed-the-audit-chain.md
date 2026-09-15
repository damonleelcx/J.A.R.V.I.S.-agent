# Every event written by the real clock failed the audit chain

**Date:** 2026-09-15 · **Status:** fixed (stacked on #82, stage A1/E3) · **Severity:** high — the tamper-evident timeline (PRD SAF-06) reported tampering on untouched rows

## Summary

`AppendEvent` hashes each event over its fields, including `created_at` rendered to the nanosecond, and writes the row.
`forge_events.created_at` is `timestamptz`, which keeps microseconds. `clock.System` reads nanoseconds. So the hash was
computed over a timestamp the database cannot give back, and `VerifyChain`, recomputing from the stored row, reported
`content-altered` on every event whose timestamp had any digits below a microsecond — which, from the real clock, is
nearly all of them.

The fix truncates the timestamp to a microsecond before it is hashed and written, so the chain covers what is stored.

## Symptom

- Found by stage E3's fence: a three-step build run as a goal wrote 12 events; `VerifyChain` reported
  `12 event(s): 10 PROBLEM(S), first at seq 1`, with nothing having edited a row.
- `forgectl audit` (and anything else that verifies a goal's timeline) reports the same on any goal a real worker or
  server wrote.

## Impact

A check that fails on untouched data cannot detect tampering: every report is red, so a real edit reads exactly like
the noise. SAF-06's promise was unmet on every deployment using the system clock.

## Root cause

`EventHash` formats `e.CreatedAt.UTC().Format(time.RFC3339Nano)`. `AppendEvent` set `e.CreatedAt = now` straight from
the caller's clock. Postgres rounds on storage, so the value read back differs from the value hashed.

## Why it did not show up before

The chain's integration tests append events with `clock.Fake`, which starts on a whole second and advances in whole
units; nothing below a microsecond ever reached the hash. No test verified a timeline written with `clock.System`.

## Fix

`AppendEvent` truncates `now` to `time.Microsecond` before hashing and inserting. One place, because it is the only
place events are written.

## Verification

- `TestAuditChain_AnEventStampedAtNanosecondsVerifies` appends events stamped at `.123456789 s` and requires the chain
  to verify; it fails without the fix.
- `TestBuildGoal_AStepKeptInsideAGoalWritesAChainedArtifactEvent` verifies a build goal's whole timeline written by a
  real worker on the system clock.

## Regression prevention

A drill in `scripts/drill-fences.sh` ("an event is hashed at a precision it is not stored at") removes the truncation
and runs the engine fence, which goes red (measured: `3 event(s): 3 PROBLEM(S), first at seq 1`).

## Not in this fix

- **Rows already written are not repaired.** Their nanoseconds are gone, so their recorded hashes cannot be recomputed;
  they will keep reporting `content-altered`. Re-attesting them would mean minting a chain over rows nobody can vouch
  for, which the audit design refuses to do for pre-chain rows too.
- **Main has the same defect** and needs the same line in its own PR.
