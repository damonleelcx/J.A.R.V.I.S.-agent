# Workers started together shared one lease identity, so no lease guard could tell them apart

**Date:** 2026-09-15 · **Status:** fixed (off-node STEP export, stacked on #90) · **Severity:** high — a worker whose lease lapsed could still extend, release or finish a task its sibling had reclaimed

## Summary

`agent.NewWorker` names a worker `host/pid/<8 characters>`, and the eight characters were `id.New(id.PrefixRun)[4:12]`:
the first eight characters of a fresh identifier. An identifier's body starts with its 48-bit millisecond timestamp
(`internal/platform/id`, the ULID layout), so those eight characters are the timestamp's top 38 bits, which change once
every 1,024 ms. Every worker one process created within the same second got the **same** identity.

forge-worker creates `FORGE_WORKER_CONCURRENCY` workers (4 by default) in one loop, in the same millisecond. So in
every deployment, all of a pod's workers wrote one name into `forge_tasks.lease_owner`, and every guard that compares
against it — `Queue.Heartbeat`, `Queue.Release`, and now the export job's record — accepted any sibling as the owner.

The fix takes the **last** eight characters, which are random.

## Symptom

- Found by the off-node STEP export's fence
  (`TestStepExport_AWorkerKilledMidExportLeavesItRetryableAndNeverReportedSucceeded`): worker A's lease lapsed and was
  reaped, worker B claimed the task and was running it, and then A finished and recorded its file under B's running
  task — although the record checks `lease_owner = <this worker>` in the same transaction. Both workers were
  `MSI/36792/01M2K0X1`.
- `TestNewWorker_WorkersStartedTogetherHaveDistinctIdentities` showed it directly: workers 0 and 1, made one after the
  other, had equal ids.

## Impact

Within one forge-worker process, the lease was not a lease between workers:

- A worker wedged past its lease, whose task a sibling reclaimed, could keep heartbeating it (`lease_owner = $2`
  matched), so the lease never lapsed again while two workers ran it.
- `Release` by one worker could hand back a task a sibling was running.
- Logs and `lease_owner` said "which worker holds this" with a name four workers shared, which is the question the
  identity's own comment says it exists to answer.

Across processes the pid still differed, so a dead **pod's** tasks were recovered correctly — which is the case every
earlier fence exercised, and why this was not seen.

## Root cause

The slice was chosen as "a short random-looking suffix" without reading the layout it slices: in a ULID the
**prefix** of the body is time, and the entropy is the tail. `id.go` states the layout; `worker.go` never referred to
it.

## Why it did not show up before

No test made two workers in one process and compared them, and every lease-reclaim drill (`internal/drill`) reclaims
with a worker id the test writes by hand. The export job is the first code that checks lease ownership inside its own
transaction, and its fence the first to run two real `Worker`s against one task.

## Fix

`NewWorker` takes `run[len(run)-8:]` of a fresh id: 40 random bits, so two workers started together collide with
probability about 2⁻⁴⁰ per pair, and the host and pid still say where each runs.

## Verification

- `TestNewWorker_WorkersStartedTogetherHaveDistinctIdentities` makes 64 workers in a loop and requires 64 identities.
  Red before the fix, green after.
- `TestStepExport_AWorkerKilledMidExportLeavesItRetryableAndNeverReportedSucceeded` requires the lapsed worker to
  record nothing while its sibling runs the task. Red before the fix, green after.

## Regression prevention

A drill in `scripts/drill-fences.sh` ("workers started together share one identity") puts the old slice back; both
fences go red.

## Not in this fix

**Main has the same line** (`internal/agent/worker.go`, since 5d06c2d, 2026-09-02) and needs the same change in its
own PR. Tasks already holding a shared identity need nothing: a lease names its owner only until it ends.
