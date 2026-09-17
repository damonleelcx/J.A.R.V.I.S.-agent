# Kernel pool deadlines, and the first subtree queue (2026-09-17)

This covers the kernel items PR #100 left open, plus #93's deferred item: "one kernel process serves all of forged's
subtree requests". This PR does not change `sidecar.py`.

## Which items were still open on main (81c6263)

| item | on main | evidence |
|---|---|---|
| A caller's deadline ends while every process is busy | **open**: `acquire`'s `ctx.Err()` became CONNECTOR_UNAVAILABLE (501) | `cad.go` `buildWith`, "no CAD kernel process became free" |
| A start is cut off by the caller's deadline | **already done by PR #137** | `roundTrip` returns a `lateError` when `ctx` ends before or during `start`; fence `TestKernel_ACallerWhoseDeadlineEndsWhileTheKernelStartsIsNotRetried`; drill "a deadline that ends while the kernel starts is retried as a crash" |
| The 60 s start limit is retried | **open**: `start` returned a plain error, and `buildWith` treated it as a crash | `sidecar_process.go` `case <-time.After(startTimeout)` |
| The 30 s build limit is a constant | **open** | `cad.go` `const buildTimeout` |
| Subtree requests queue behind one process | open, but #125 decided to keep the queue | see below |

## Decisions

- **Busy pool past the deadline:** now CAD_KERNEL_TIMEOUT (504). The detail says the request waited for a free
  process, that all N processes were busy, and that the build never started. A caller that *cancelled* still gets
  CONNECTOR_UNAVAILABLE, the same answer `lateRefusal` gives.
- **Start past `startTimeout`:** it is not retried, and the refusal is CAD_KERNEL_TIMEOUT with a detail saying it
  did not start within the limit. The process was slow, not dead, so a second start waits just as long. A request
  in forged has no deadline of its own, so the retry made a person wait two minutes. A process that *exits* while
  starting is still retried once, because that fails in milliseconds.
- **Build limit:** now set by `FORGE_CAD_BUILD_TIMEOUT` (default 30s, unchanged).
  - It is refused when it is ≤ 0, not a duration, or longer than `FORGE_HTTP_WRITE_TIMEOUT`.
  - `forge.config.loaded` now prints `cad_build_timeout`, `cad_pool` (which was not printed before) and
    `cad_prestart`.
  - `cad.FromConfig` is now the one constructor both forged and forge-worker use.
  - The off-node export job keeps its own 5 min limit.

## The subtree queue (#93, #125)

#125 measured the queue at forged's pod limits (1 CPU / 1 GiB):
- Pool 1 serialises four 8,192-part meshes: the fastest returns in 1.4 s, the slowest in 5.0 s.
- Pool 2 was slower in every run (8.4–8.8 s against 5.0–5.1 s), with CPU throttling of 13 s against 0.2 s.
- Two kernels need about 1,010 MiB, and the pod has 1 GiB in total.

The queue is limited by CPU, so the options for one process are these:

| option | what it would change | why not / why |
|---|---|---|
| a second process | slower at 1 CPU, and does not fit in memory (#125) | refused |
| merge identical in-flight subtree requests | only duplicates; the browser already skips a path that is loaded or on its way (`Studio._covered`), and #93's 12 subtrees were all different | nothing measured to gain |
| answer from the Go tessellator when the queue is long | total CPU is the same; the person gets surfaces without holes and never gets the kernel's surface afterwards | loses accuracy to save time |
| **start the process at boot** | removes build123d's import from in front of the first queue: **9.3 s of #93's 17.2 s** cold first view | **done** (`FORGE_CAD_PRESTART`, default true) |

A process that has started is kept for the life of forged. Starting it at boot therefore changes *when* forged
takes that memory, not how much it holds at steady state. A deployment that wants the kernel started lazily can
set `FORGE_CAD_PRESTART=false`. Serving does not wait for the start. If the start fails, it is logged as
`forge.cad.prestart_failed`, and the first build starts a process as before.

### Numbers

**Fence** (`TestKernel_APrestartedKernelAnswersTheFirstQueueWithoutPayingForAStart`): a fake kernel with a 2 s
start, a 50 ms build and 12 concurrent subtree meshes.

| run | first answered | last answered |
|---|---|---|
| cold | 2.07 s | 2.62 s |
| prestarted | 0.051 s | 0.62 s |

Both runs used one process. The last answer is still about 12 × 50 ms after the start, so the queue itself is kept.
The assertions are written in terms of the start and the build time, not the machine's speed.

**Real kernel start on this laptop** (build123d 0.11.1, `.cadvenv`), from spawn to the ready banner of
`sidecar.py`. Six runs in a row, with other agents' builds on the machine; host CPU was measured in the second
before each run.

| run | s | host CPU |
|---:|---:|---:|
| 1 | 5.33 | 35% |
| 2 | 4.49 | 24% |
| 3 | 4.37 | 18% |
| 4 | 4.37 | 30% |
| 5 | 4.62 | 27% |
| 6 | 4.30 | 24% |

This is the time a prestarted forged takes off the first person to open a design. #93 measured 9.3 s for the same
step, but that figure included the first subtree build, under heavier load.

## A cancelled caller no longer kills its process

Before this change, a caller that cancelled mid-build had its process killed. Examples are a closed tab, or a
navigation away while a design loads a subtree at a time. The next viewer then paid a kernel start.

Now:
- **The caller** gets its answer at once, still `CONNECTOR_UNAVAILABLE`.
- **The build** finishes for nobody. Its one reply line is read and discarded before the slot goes back to the
  pool (`sidecar.abandon`). The protocol is one request and one line on one pipe, so the slot must not be reusable
  while that line is unread.
- **The build limit still kills it.** A process that crashes still resets the slot.
- **Close** kills an abandoned build instead of waiting out its limit.
- **A caller whose DEADLINE ends** is unchanged: the process is killed and the answer is `CAD_KERNEL_TIMEOUT`.

## Not established

- The effect on the arm64 node, and forged's memory from boot to the first view. The steady state is unchanged by
  construction, but it has not been measured.
- The in-browser first view was not re-measured with prestart.
- The `forged` main wiring (`go cadKernel.Prestart(ctx)` under `cfg.CAD.Prestart`) has no fence. `cmd/forged` has
  no tests. `Prestart` and `FromConfig` each have a fence and a drill.
