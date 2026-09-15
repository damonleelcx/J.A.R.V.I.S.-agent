# A kernel build that ran out of time was reported as no kernel

**Status:** fixed on `main` (this change). The same defect was fixed on the millions-of-parts stack by PR #100
(`kernel/timeout-is-not-a-crash`, 51f64fe), where it lives in the kernel pool; this is the port to `main`'s single
process.
**Found:** 2026-09-15, measuring the kernel build ceiling through the mesh endpoint on the stack
(`docs/spikes/2026-09-15-kernel-build-ceiling`, PR #95).
**Severity:** medium, visible. No wrong geometry. A design too big to build in 30 s cost the person twice that and told
them the deployment had no CAD kernel, which points at configuration that was fine.
**Owner:** the CAD kernel (`internal/domain/cad/cad.go`: `Kernel.BuildDocument`, `Kernel.roundTrip`).

## Summary

A build still running when its time ran out was handled like a crashed kernel. The process was killed, the one retry
meant for dead processes started a fresh one, the same build ran again and was killed again, and the caller got
`CONNECTOR_UNAVAILABLE`: HTTP 501, *"A capability is declared but has no working backend in this deployment"*,
`retryable: false`. Now a build that runs out of time is stopped once and not retried. The caller gets
`CAD_KERNEL_TIMEOUT` (HTTP 504, *"The CAD kernel took too long…"*, not retryable) after about one limit, and the kernel
starts a fresh process for the next build.

## Symptom

Measured on the stack by the ceiling spike, `GET /v1/geometry/{id}/mesh` on the shipped 30 s limit:

| design | status | wall | log |
|---|---|---|---|
| car, 30,023 parts | **501** | 66.4 s | `forge.cad.restarted detail="reading from the kernel: EOF"` at +30 s |
| car, 60,000 parts | **501** | 64.7 s | same |
| barrel, 60,640 parts | **501** | 65.1 s | same |

The body in each case: `CONNECTOR_UNAVAILABLE`, *"no working backend in this deployment"*, detail *"the CAD kernel did not
answer, and restarting it did not help"*. The kernel was working the whole time.

On `main`, reproduced with the fences below before the fix (fake kernel, 500 ms limit): `CONNECTOR_UNAVAILABLE`, detail
*"the CAD kernel did not answer, and restarting it did not help"* wrapping `reading from the kernel: EOF`, **2 processes started,
1.05 s for a 500 ms limit**; with the caller's deadline instead, `CONNECTOR_UNAVAILABLE` ending in *"context deadline
exceeded"*; on the wire, **501 `CONNECTOR_UNAVAILABLE`** from both the mesh and the STEP export.

## Impact

- **Every endpoint that builds with the kernel.** On `main` that is `GET /v1/geometry/{id}/mesh` and
  `GET /v1/geometry/{id}/export?format=step` (there is no mass endpoint on `main`). Both hand the kernel's error to
  `WriteError`, so both said 501 "no working backend".
- **Time:** one limit became two plus a process start, about 60+ s instead of 30 s. `main` serialises builds behind one
  mutex, so every other build in the deployment waited that whole time.
- **What people were told:** 501 and "no working backend" is also what a deployment with no `FORGE_CAD_PYTHON` returns.
  An operator reading it goes looking for a configuration fault that does not exist.
- **The next build after one:** the second kill left the kernel marked started with dead pipes. The next build used up its
  one retry finding that out, so if its own fresh process then crashed, it failed too.
- **Not affected:** scripts (`RunScript` has its own timeout and its own process), and any caller that falls back on any
  error (the workbench's mesh fetch draws primitives on a non-OK reply).

## Preconditions

- A kernel is configured (`FORGE_CAD_PYTHON`).
- One build takes longer than `buildTimeout` (30 s), or the caller's context deadline ends first. The ceiling spike
  reached it at about 30k parts on a busy laptop. A busy machine can push a smaller design past it.

## Root cause

`Kernel.roundTrip` enforces the deadline with a goroutine that kills the process, because a blocking read on a pipe does
not observe a context. The read then returns `EOF`, and that `EOF` looks exactly like the one a crashed process gives. The
goroutine did not record why it killed the process, so `BuildDocument` could not tell *"killed because time ran out"* from
*"died"*, and did for both what is right only for the second: `stopLocked()`, one retry on a fresh process, then
`CONNECTOR_UNAVAILABLE`.

The retry's own comment gives its reason: *"a process that died between requests — a machine asleep, an OOM, somebody's
pkill"*. A timeout is none of those. The process was alive and building, and a fresh one given the same design takes as
long again.

**Classification:** implementation defect, error classification. Two different failures reached the same branch because
the only thing that could tell them apart (which `select` case fired) was thrown away.

## Why it did not show up before

- `buildTimeout` was chosen against a 46 ms bracket (*"three orders of magnitude of headroom"*). No test and no live run
  built anything close to 30 s until the ceiling spike, the first run that went past the limit on purpose.
- `TestRetryAfterTheProcessDies` kills the process **by hand**, which is the case the retry is for. No fence let the
  timer do the killing, and every kernel fence needed build123d, so none could be slow or crash on cue.
- 501 is a status the product returns on purpose for a missing capability, so a 501 in a log did not look wrong.

## Fix

Adapted to `main`'s shape: one process behind `Kernel.mu`, no pool, no slots. None of the stack's pool code
(`Kernel{slots chan *sidecar}`, `sidecar_process.go`) is brought over.

- `roundTrip`'s deadline goroutine stores a `lateError` before it kills the process: `{limit}` when the kernel's limit
  fires, `{caller: ctx.Err()}` when the caller's context ends. If the read then fails, `roundTrip` returns that instead of
  the I/O error.
- `BuildDocument` retries only errors that are **not** a `lateError`. A `lateError`, from the first attempt or from the
  retry after a real crash, is not retried. Instead:
  - the kernel is reset at once: `stopLocked()` reaps the killed process and clears `started`, so the next build starts a
    fresh process instead of using its retry to find out this one is dead;
  - the timeout is logged as the new `forge.cad.timed_out`, not `forge.cad.restarted`;
  - `lateRefusal` returns **`CAD_KERNEL_TIMEOUT`**. The detail names which time ran out: *"this build took longer than the
    CAD kernel's 30s limit, so it was stopped and not retried…"* or *"the request's own deadline ended before the CAD
    kernel finished this build…"*. A caller that **cancelled** has gone. It keeps `CONNECTOR_UNAVAILABLE` with *"the request
    ended before the CAD kernel finished, so the build was stopped"*. Nobody reads that reply.
- `CAD_KERNEL_TIMEOUT` is a new registry code (`internal/platform/errs/code.go`): category external, **HTTP 504**, cause
  *"The CAD kernel took too long… The kernel itself is working."*, remedy naming the 30 s limit and saying to build a
  smaller part of the design or split the assembly.
- **Why 504:** the kernel is the upstream this server waited on, and it did not answer in time. 5xx `detail` is still
  withheld by `WriteError`, so the registry's cause and remedy are what a person reads. Both say it took too long, and the
  remedy names the limit.
- **Why not retryable** (`TestRetryabilityIsDeliberate` now lists it). Build time depends on the design: the spike's 30k
  car took 40–101 s on every run. The engine worker (`errs.IsRetryable`) would spend an attempt and 30 s of the kernel
  process on every retry, and a person offered "retry" on an export would wait another 30 s for the same answer. The
  exception is a build that crossed the limit only because the machine was busy. It is rare, and a person can still ask
  again.
- `Kernel.timeout` (default `buildTimeout`) exists so the fences can use a 500 ms limit. It is not a setting. The limit is
  still the constant.

## Verification

Reproduced first, on `main`, with the fences below written against the unfixed code: see Symptom. The three
`TestKernel_*` timeout fences for the limit, the caller's deadline and the next build failed, and the HTTP fence got 501
on both endpoints. `TestKernel_AProcessThatDiesMidBuildIsStillRetriedOnce` passed before and after, as it should: it holds
the retry the fix must keep.

After the fix all are green. The kernel-limit build takes about one limit and starts one process, and both endpoints
return 504 `CAD_KERNEL_TIMEOUT`. `go vet ./...` passes. `go test -count=1 ./internal/domain/cad/ ./internal/httpapi/
./internal/platform/errs/ ./internal/platform/logx/` was run on Windows 11 with a real kernel and the test database.
`errs` and `logx` pass. In `cad`, the only failures were the known Windows-only ones: the 15 `TestScript_*` tests and
`TestKernel_AScriptedPartIsExportedAndMeshed` (the script runner looks for a Unix venv layout). In `httpapi`, the only
failure was `TestATurnIsKeptAndComesBack`, whose setup timed out pinging the shared test database; it passes when run on
its own and does not touch the kernel.

## Regression prevention

| fence | runs where | holds |
|---|---|---|
| `TestKernel_ABuildThatRunsOutOfTimeIsNotRetriedAndSaysSo` | CI, no Python (fake kernel) | for the kernel's limit and for the caller's deadline: `CAD_KERNEL_TIMEOUT`, not retryable, detail names what ran out, **one** process started, under two limits; the registry's remedy names `buildTimeout` |
| `TestKernel_AfterATimeoutTheKernelStartsAFreshProcessForTheNextBuild` | CI, no Python | after a timeout the next build survives one crash of its new process (its retry was not spent on the killed one), and the kernel no longer holds the killed pid |
| `TestKernel_AProcessThatDiesMidBuildIsStillRetriedOnce` | CI, no Python | a process that dies is still retried, exactly once, and is not reported as a timeout |
| `TestAPI_AKernelBuildThatTakesTooLongIsA504ThatSaysSo` | CI with the test database, no Python | mesh and STEP export each return 504 `CAD_KERNEL_TIMEOUT`, a message saying it took too long, not "no working backend", `retryable: false` |

The fake kernel is `internal/domain/cad/cadtest`, the same helper as on the stack. The test binary runs itself as the
kernel's "python" (each package's `TestMain` calls `cadtest.RunIfAsked`), and a part whose id is `fake-kernel-slow`,
`fake-kernel-crash` or `fake-kernel-crash-once` makes it hang or exit. `TestRetryAfterTheProcessDies` is unchanged and
still holds the retry against build123d.

Drills under "Added 2026-09-15 (kernel timeout is not a crash)" in `scripts/drill-fences.sh`, all eight red: a timed-out
build is retried like a crashed one; the kill does not record the kernel's limit; a timeout leaves the killed process in
the kernel; the kernel's limit is reported as no working backend; the caller's deadline is reported as no working
backend; a kernel timeout is offered as retryable; a kernel timeout is a 501; a process that dies mid-build is not
retried.

## Not in this fix

- **Independent of the other open PRs against `main`** (#87, #91, the lease-identity PR): it touches none of their files'
  logic and does not depend on them.
- **A start the caller's deadline interrupts** (`startLocked`, `ctx.Done()`) still returns `ctx.Err()`, which is retried
  and ends as `CONNECTOR_UNAVAILABLE`. It needs a context that ends during the 2.5 s import.
- **`startTimeout`** (60 s without a ready banner) is still retried once. A kernel that cannot import in a minute is closer
  to broken than to slow.
- **Waiting for the mutex:** a build queued behind a slow one waits on `Kernel.mu` without observing its context. That is
  `main`'s serialisation, unchanged here; the stack's pool replaced it.
- **`RunScript`'s own timeout** returns `EXTERNAL_UNAVAILABLE`, which is retryable. That is a separate path with its own
  limit and was not changed.
- **The limit itself:** 30 s is still a constant, not a setting.
- **Starting the replacement process early:** the next build starts it, so that build pays the ~2.5 s import.
