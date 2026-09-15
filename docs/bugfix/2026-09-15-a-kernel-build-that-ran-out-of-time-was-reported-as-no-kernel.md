# A kernel build that ran out of time was reported as no kernel

**Found:** 2026-09-15, measuring the kernel build ceiling through the mesh endpoint
([`docs/spikes/2026-09-15-kernel-build-ceiling`](../spikes/2026-09-15-kernel-build-ceiling/README.md), PR #95).
**Severity:** medium, visible. No wrong geometry. A design too big to build in 30 s cost the person twice that and told
them the deployment had no CAD kernel, which points at configuration that was fine.
**Owner:** the CAD kernel pool (`internal/domain/cad`: `Kernel.build`, `sidecar.roundTrip`).

## Summary

A build still running when its time ran out was handled like a crashed kernel. The process was killed, the one retry
meant for dead processes started a fresh one, the same build ran again and was killed again, and the caller got
`CONNECTOR_UNAVAILABLE`: HTTP 501, *"A capability is declared but has no working backend in this deployment"*,
`retryable: false`. Now a build that runs out of time is stopped once and not retried. The caller gets
`CAD_KERNEL_TIMEOUT` (HTTP 504, *"The CAD kernel took too long…"*, not retryable) after about one limit, and the slot gets
a fresh process for the next build.

**`main` has the same defect.** `git show origin/main:internal/domain/cad/cad.go`: `roundTrip`'s deadline goroutine
calls `killLocked()` on `ctx.Done()` or on `buildTimeout`, and `BuildDocument` treats any `roundTrip` error the same way:
`stopLocked()`, one retry, then `errs.Wrap(op, errs.CodeConnectorUnavailable, err)` with *"the CAD kernel did not answer,
and restarting it did not help"*. `main` has one process behind a mutex instead of a pool, so the same fix applies to one
process. This fix is not on `main`; that is for the owner to decide.

## Symptom

From the spike's `data/results.jsonl`, label `shipped-timeout`, `GET /v1/geometry/{id}/mesh` on the shipped 30 s limit:

| design | status | wall | kernel pids during | log |
|---|---|---|---|---|
| car, 30,023 parts | **501** | 66.4 s | 4 (2 before) | `forge.cad.restarted slot=0 detail="reading from the kernel: EOF"` at +30 s |
| car, 60,000 parts | **501** | 64.7 s | 4 (2 before) | same |
| barrel, 60,640 parts | **501** | 65.1 s | 4 (2 before) | same |

The body in each case: `CONNECTOR_UNAVAILABLE`, *"no working backend in this deployment"*, detail *"the CAD kernel did not
answer, and restarting it did not help"*. The same build measured directly took 40–101 s. The kernel was working the whole
time.

## Impact

- **Every endpoint that builds with the kernel:** the whole-design mesh, the STEP export, and mass (`BuildProperties`).
  All three hand the kernel's error to `WriteError`, so all three said 501 "no working backend".
- **Time:** one limit became two plus a process start, so about 60–66 s instead of 30 s. That whole time one pool process
  was held, and with the default pool of one, every other build in the deployment waited behind it.
- **What people were told:** 501 and "no working backend" is also what a deployment with no `FORGE_CAD_PYTHON` returns.
  An operator reading it goes looking for a configuration fault that does not exist.
- **The next build after one:** the second kill left the slot marked started with dead pipes. The next build on that slot
  used up its one retry finding that out, so if its own new process then crashed, it failed too.
- **Not affected:** the subtree path (≤ 4,096 parts, and a kernel failure there falls back to the Go tessellator with the
  reason in `source_note`), the agent's render (any failure falls back to the described render), and scripts (`RunScript`
  has its own timeout and does not go through the pool).

## Preconditions

- A kernel is configured (`FORGE_CAD_PYTHON`).
- One build takes longer than `buildTimeout` (30 s), or the caller's context deadline ends first. The ceiling spike
  reached it at about 30k parts on a busy laptop. A busy machine can push a smaller design past it.

## Root cause

`sidecar.roundTrip` enforces the deadline with a goroutine that kills the process, because a blocking read on a pipe does
not observe a context. The read then returns `EOF`, and that `EOF` looks exactly like the one a crashed process gives. The
goroutine did not record why it killed the process, so `Kernel.build` could not tell *"killed because time ran out"* from
*"died"*, and did for both what is right only for the second: `s.stop()`, one retry on a fresh process, then
`CONNECTOR_UNAVAILABLE`.

The retry's own comment gives its reason: *"a process that died between requests — a machine asleep, an OOM, somebody's
pkill"*. A timeout is none of those. The process was alive and building, and a fresh one given the same design takes as
long again.

**Classification:** implementation defect, error classification. Two different failures reached the same branch because
the only thing that could tell them apart (which `select` case fired) was thrown away.

## Why it did not show up before

- `buildTimeout` was chosen against a 46 ms bracket (*"three orders of magnitude of headroom"*). No test and no live run
  built anything close to 30 s until the ceiling spike. That spike was the first run that went past the limit on purpose.
- K3's fences (`TestRetryAfterTheProcessDies`, `TestRetryAfterEveryProcessInThePoolDies`) kill the process **by hand**,
  which is the case the retry is for. No fence let the timer do the killing.
- 501 is a status the product returns on purpose for a missing capability, so a 501 in a log did not look wrong.

## Fix

- `roundTrip`'s deadline goroutine stores a `lateError` before it kills the process: `{limit}` when `buildTimeout` fires,
  `{caller: ctx.Err()}` when the caller's context ends. If the read then fails, `roundTrip` returns that instead of the
  I/O error.
- `Kernel.build` retries only errors that are **not** a `lateError`. A `lateError`, from the first attempt or from the
  retry after a real crash, is not retried. Instead:
  - the slot is restored at once: `s.stop()` reaps the killed process and clears `started`, so the next build on that slot
    starts a fresh process instead of using its retry to find out this one is dead. The pool's size is unchanged: the slot
    is released by the same `defer` as always.
  - the timeout is logged as the new `forge.cad.timed_out` (slot and detail), not `forge.cad.restarted`.
  - `lateRefusal` returns **`CAD_KERNEL_TIMEOUT`**. The detail names which time ran out: *"this build took longer than the
    CAD kernel's 30s limit, so it was stopped and not retried…"* or *"the request's own deadline ended before the CAD
    kernel finished this build…"*. A caller that **cancelled** has gone. It keeps `CONNECTOR_UNAVAILABLE` with *"the request
    ended before the CAD kernel finished"*, the code `acquire` already uses when a caller leaves. Nobody reads that reply.
- `CAD_KERNEL_TIMEOUT` is a new registry code (`internal/platform/errs/code.go`): category external, **HTTP 504**, cause
  *"The CAD kernel took too long: it was still building this design when the time a build is allowed ran out… The kernel
  itself is working."*, remedy naming the 30 s limit and saying to build part of the design at a time.
- **Why 504:** the kernel is the upstream this server waited on, and it did not answer in time. 5xx `detail` is still
  withheld by `WriteError`, so the registry's cause and remedy are what a person reads. Both say it took too long and name
  the limit.
- **Why not retryable** (`TestRetryabilityIsDeliberate` now lists it). Build time depends on the design: the spike's 30k
  car took 40–101 s on every run. For each caller that reads the flag:
  - the engine worker (`errs.IsRetryable`) would spend an attempt and 30 s of a kernel process on every retry;
  - a person offered "retry" on an export would wait another 30 s for the same answer.

  Callers that never read the flag: the workbench's mesh fetch falls back to primitives on any non-OK reply, and the
  agent's render falls back to the described render on any error. The exception is a build that crossed the limit only
  because the machine was busy. It is rare, and a person can still ask again.
- `Kernel.timeout` (default `buildTimeout`) is carried to each `sidecar`, so the fences can use a 500 ms limit. It is not
  a setting. The limit is still the constant.

## Verification

Reproduced first. The fences below were written against the unfixed code, and on this laptop they failed like this:
- the kernel's-limit case: `CONNECTOR_UNAVAILABLE`, *"did not answer, and restarting it did not help"*, **2 processes
  started, 1.04 s for a 500 ms limit**;
- the caller-deadline case: `CONNECTOR_UNAVAILABLE`;
- the slot fence: failed at its first assertion, since the slow build did not time out as a timeout;
- the HTTP fence: **501 CONNECTOR_UNAVAILABLE** on mesh, STEP export and mass.

After the fix all are green. The kernel-limit build takes about one limit and starts one process, and the three endpoints
return 504 `CAD_KERNEL_TIMEOUT`. `go vet ./...` passes. `go test -count=1 ./internal/domain/cad/ ./internal/httpapi/` was run with a real kernel and the
test database. In `cad`, the only failures were the known Windows-only ones: `TestScript_*`, the scripted-part kernel
tests, `TestKernel_ReturnsTheSurfaceOfTheSolidItBuilt` and `TestKernel_ExportingManyOccurrencesGrowsLinearly`. In
`httpapi`, 15 database tests (conversations, members, projects, review authority) failed while migrating ("schema
already exists", "no schema has been selected"), because another agent was migrating the same fixed test schemas at the
same moment. All 15 pass when re-run on their own, and none of them touches the kernel.

## Regression prevention

| fence | runs where | holds |
|---|---|---|
| `TestKernel_ABuildThatRunsOutOfTimeIsNotRetriedAndSaysSo` | CI, no Python (fake kernel) | for the kernel's limit and for the caller's deadline: `CAD_KERNEL_TIMEOUT`, not retryable, detail names what ran out, **one** process started, under two limits; the registry's remedy names `buildTimeout` |
| `TestKernel_AfterATimeoutTheSlotHasAFreshProcessForTheNextBuild` | CI, no Python | after a timeout all slots are free, the next build survives one crash of its new process (its retry was not spent on the killed one), and the slot no longer holds the killed pid |
| `TestKernel_AProcessThatDiesMidBuildIsStillRetriedOnce` | CI, no Python | a process that dies is still retried, exactly once, and is not reported as a timeout |
| `TestAPI_AKernelBuildThatTakesTooLongIsA504ThatSaysSo` | CI with the test database, no Python | mesh, STEP export and mass each return 504 `CAD_KERNEL_TIMEOUT`, a message saying it took too long, not "no working backend", `retryable: false` |

The fake kernel is `internal/domain/cad/cadtest`. The test binary runs itself as the kernel's "python" (each package's
`TestMain` calls `cadtest.RunIfAsked`), and a solid whose id is `fake-kernel-slow`, `fake-kernel-crash` or
`fake-kernel-crash-once` makes it hang or exit. K3's real-kernel fences (`TestRetryAfterTheProcessDies`,
`TestRetryAfterEveryProcessInThePoolDies`) are unchanged and still hold the retry against build123d.

Drills under "Added 2026-09-15 (kernel timeout is not a crash)" in `scripts/drill-fences.sh`, all red: a timed-out build
is retried like a crashed one; the kill does not record the kernel's limit; a timeout leaves the killed process in its
slot; the kernel's limit is reported as no working backend; the caller's deadline is reported as no working backend; a
timeout is offered as retryable; a timeout is a 501; a process that dies is not retried.

## Not in this fix

- **`main`**: same defect, see Summary. Not changed here.
- **A pool that stays busy past the caller's deadline:** `acquire` still returns `CONNECTOR_UNAVAILABLE` *"no CAD kernel
  process became free before the request ended"*. That is also a "took too long", but it is about waiting for a process,
  not a build. The HTTP handlers set no deadline, so only a caller with its own context reaches it.
- **A start the caller's deadline interrupts** (`sidecar.start`, `ctx.Done()`) still returns `ctx.Err()`, which is retried
  and ends as `CONNECTOR_UNAVAILABLE`. It needs a context that ends during the 2.5 s import.
- **`startTimeout`** (60 s without a ready banner) is still retried once. A kernel that cannot import in a minute is closer
  to broken than to slow.
- **`RunScript`'s own timeout** returns `EXTERNAL_UNAVAILABLE`, which is retryable. That is a separate path with its own
  limit and was not changed.
- **The limit itself:** 30 s is still a constant, not a setting. Whether to raise it is the spike's first recommendation,
  and that needs a measurement on the production image.
- **Starting the replacement process early:** the restored slot starts its process on the next build, as every slot does,
  so that build pays the ~2.5 s import.
