# Workers started together shared one lease identity, so no lease guard could tell them apart

**Date:** 2026-09-15 · **Status:** fixed on main (found building the off-node STEP export, #99) · **Severity:** high — a
worker whose lease lapsed could still extend or release a task its sibling had reclaimed

## Summary

`agent.NewWorker` names a worker `host/pid/<8 characters>`, and the eight characters were `id.New(id.PrefixRun)[4:12]`:
the first eight characters of a fresh identifier's body. An identifier's body starts with its 48-bit millisecond
timestamp (`internal/platform/id`, the ULID layout), so those eight characters are the timestamp's top 38 bits, which
change once every 1,024 ms. Every worker one process created within the same second got the **same** identity.

forge-worker creates `FORGE_WORKER_CONCURRENCY` workers (4 by default) in one loop, in the same millisecond. So in
every deployment, all of a pod's workers wrote one name into `forge_tasks.lease_owner`, and every guard that compares
against it — `Queue.Heartbeat`, `Queue.Release` — accepted any sibling as the owner.

The fix takes the **last** eight characters, which are random.

## Symptom

- Found by #99's off-node STEP export fence
  (`TestStepExport_AWorkerKilledMidExportLeavesItRetryableAndNeverReportedSucceeded`): worker A's lease lapsed and was reaped, worker B claimed the task and was
  running it, and then A finished and recorded its file under B's running task — although the record checks
  `lease_owner = <this worker>` in the same transaction. Both workers were `MSI/36792/01M2K0X1`.
- On main, `TestNewWorker_WorkersStartedTogetherHaveDistinctIdentities` shows it directly: workers 0 and 1, made one
  after the other, were both `MSI/36392/01M2K3MK`.
- On main, `TestWorker_ASiblingCannotExtendOrReleaseALeaseItDoesNotHold` shows what it does on Postgres: a worker
  heartbeated the lease its sibling held and got no error, because both were `MSI/36392/01M2K3MN`.

## Impact

Within one forge-worker process, the lease was not a lease between workers:

- A worker wedged past its lease, whose task a sibling reclaimed, could keep heartbeating it (`lease_owner = $2`
  matched), so the lease never lapsed again while two workers ran it.
- `Release` by one worker could hand back a task a sibling was running, and a worker's graceful stop could return a
  sibling's task to `ready` mid-run.
- Once #99's export job exists, a worker whose export lease lapsed could record its file under the export its
  sibling had reclaimed, because the record's `lease_owner = <this worker>` check matched the sibling.
- Logs and `lease_owner` said "which worker holds this" with a name four workers shared, which is the question the
  identity's own comment says it exists to answer.

Across processes the pid still differed, so a dead **pod's** tasks were recovered correctly — which is the case every
earlier fence exercised, and why this was not seen.

## Preconditions

- Two or more workers in one process, created within the same 1,024 ms window. forge-worker meets this on every start
  with `FORGE_WORKER_CONCURRENCY` above 1 (the default is 4).
- A lease that lapses or is released while a sibling could act on the same task: a worker stalled past
  `LeaseDuration`, or a shutdown mid-task.

## Root cause

The slice was chosen as "a short random-looking suffix" without reading the layout it slices: in a ULID the
**prefix** of the body is time, and the entropy is the tail. `id.go` states the layout; `worker.go` never referred to
it.

## Why it did not show up before

No test made two workers in one process and compared them. The queue's own guard fence,
`TestHeartbeatRefusesAStolenLease`, uses worker ids the test writes by hand (`worker-slow`, `worker-fast`), distinct by
construction — the guard was right and the names it compared in production were not. #99's export job was the first
code to check lease ownership inside its own transaction, and its fence the first to run two real `Worker`s against
one task.

## Fix

`NewWorker` takes `run[len(run)-8:]` of a fresh id: 40 random bits, so two workers started together collide with
probability about 2⁻⁴⁰ per pair, and the host and pid still say where each runs. One line, the same change as #99.

## Verification

- `TestNewWorker_WorkersStartedTogetherHaveDistinctIdentities` makes 64 workers in a loop and requires 64 identities.
  Red on main before the fix, green after.
- `TestWorker_ASiblingCannotExtendOrReleaseALeaseItDoesNotHold` (Postgres) mints two workers through `NewWorker`, has
  one claim a task, and requires the other's heartbeat and release to be refused with CONFLICT; then lapses and reaps
  the lease, has the sibling reclaim it, and requires the first worker's heartbeat and release to be refused, with the
  lease's owner and expiry unmoved. Red on main before the fix, green after.
- `TestStepExport_AWorkerKilledMidExportLeavesItRetryableAndNeverReportedSucceeded` (#99, Postgres) requires the
  lapsed worker to record nothing while its sibling runs the export. Red on #99's branch before the fix, green after.

## Regression prevention

Two drills in `scripts/drill-fences.sh` ("Workers in one process", added 2026-09-15): one puts the old slice back, one
names every worker by host and pid alone. Both fences go red under each, and since #99 merged, so does its export
fence, which both drills also run.

## Not in this fix

- #99 (`export/off-node`) carried the same one-line change and its own copy of this document; it merged main after this
  fix, kept this document and main's test file, and applied the change once (the two lines were identical).
- Tasks already holding a shared identity need nothing: a lease names its owner only until it ends, and a restart mints
  new identities.
