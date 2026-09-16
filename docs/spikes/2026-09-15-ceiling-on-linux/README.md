# Measurement: the kernel build ceiling and FORGE_CAD_POOL, on Linux under the pods' limits

**Date:** 2026-09-15 · **Status:** done, one laptop, shared · **Follows:**
[`2026-09-15-kernel-build-ceiling`](../2026-09-15-kernel-build-ceiling/README.md) (#95, Windows) and #90's
`docs/spikes/2026-09-15-worker-kernel-memory` (branch `agent/build-goal-entry`) · **Built on:** #100 (a timeout is a
504, not a retried crash), with #94 (large-box index) and #106 (STEP export scaling) merged in

## Summary

- **The kernel builds a VIEW of 8,192 parts now; everything else stays at 4,096.** Through `GET /v1/geometry/{id}/mesh`
  on `forged` in a Linux container limited like its pod (**1 CPU, 1 GiB**), with the shipped 30 s kernel timeout, the
  8,315-part car took **12.1–13.0 s** and the 8,192-part barrel **5.7–8.8 s**, three runs each, all 200: at least
  **2.3× under the timeout**. The container peaked at **372 MiB**.
- **16,384 is not raised to.** The 16,556-part car took **17.3–21.5 s** (1.4–1.7× under the timeout); the 16,256-part
  barrel 13.2–15.8 s (1.9×). They succeed every time, but not with the 2× margin the rule asks for.
- **30k is at the timeout.** The 30,023-part car: 504 `CAD_KERNEL_TIMEOUT` at 30.3 and 30.5 s, and a 200 at 30.8 s in
  the third run; 35.4 s with the timeout lifted. The 30,400-part barrel fits: 20.6–25.1 s.
- **Memory is not what limits the ceiling.** At 30k the whole container peaked at 511–536 MiB of 1 GiB: kernel
  VmHWM 614–618 MiB, forged 85–138 MiB.
- **FORGE_CAD_POOL stays 1.** Two concurrent 4,000-occurrence build goals on forge-worker (2 GiB, 1 CPU) peaked at
  **790–817 MiB with pool 2** (416–420 MiB with pool 1), no OOM kill in any run. Memory would allow it. But on the
  pod's one CPU, pool 2 finished **slower**: 34–42 s against 29–32 s, throttled 33–37 s against 10–12 s. And this
  branch's forge-worker does not run a kernel at all (that is A1, not merged here).

Machine: 16 logical processors; host CPU 40–64 % mean over every run from other agents' work, the Docker VM 6–12 %.
The container's CPU quota is the binding limit: a 16k car was throttled for 3.8–7.5 s of its build.

## The build ceiling: `forged`, 1 CPU, 1 GiB, 30 s kernel timeout

Seconds; phases are the sidecar's own (`forge.geometry.meshed`). "wall" is the HTTP request; sign-in and a four-part
warm-up are excluded. Runs 1–3 are the shipped 30 s timeout; "lifted" is one run with the timeout at 20 minutes. MiB:
`VmHWM` is the process's own peak resident set, read every 250 ms from a container sharing forged's PID namespace;
"cgroup peak" is the container's `memory.peak` (anonymous memory's sampled peak in brackets). "throttled" is the
container's `cpu.stat` throttled time during the request. Host CPU is the Windows laptop's mean; VM is the Docker VM's.

| design | run | status | wall | shapes | assembly | interference | mesh | kernel VmHWM | forged VmHWM | cgroup peak (anon) | throttled | host CPU % | VM CPU % |
|---|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| car 4,898 | 1 | 200 | 10.2 | 2.1 | 1.1 | 4.2 | 0.7 | 492 | 78 | 383 (347) | 1.5 | 58 | 6.6 |
| | 2 | 200 | 11.3 | 2.9 | 1.3 | 4.9 | 0.6 | 492 | 63 | 361 (326) | 1.9 | 64 | 6.8 |
| | 3 | 200 | 6.0 | 1.4 | 0.8 | 3.1 | 0.5 | 493 | 63 | 364 (332) | 1.2 | 40 | 7.9 |
| **car 8,315** | 1 | 200 | **12.9** | 3.1 | 1.7 | 6.4 | 0.6 | 520 | 65 | 372 (342) | 5.3 | 57 | 7.3 |
| | 2 | 200 | **12.1** | 3.2 | 1.6 | 5.8 | 0.7 | 519 | 65 | 372 (342) | 3.2 | 53 | 12.3 |
| | 3 | 200 | **12.1** | 2.8 | 1.5 | 6.1 | 0.6 | 520 | 66 | 372 (342) | 5.0 | 50 | 7.0 |
| car 16,556 | 1 | 200 | 20.9 | 5.7 | 3.4 | 9.3 | 0.7 | 558 | 82 | 425 (395) | 5.1 | 56 | 7.1 |
| | 2 | 200 | 21.5 | 6.3 | 3.3 | 8.6 | 0.8 | 556 | 77 | 425 (387) | 5.9 | 55 | 10.9 |
| | 3 | 200 | 17.3 | 5.2 | 2.9 | 7.3 | 0.7 | 556 | 73 | 426 (394) | 3.8 | 46 | 7.4 |
| | lifted | 200 | 19.2 | 5.8 | 3.2 | 8.3 | 0.8 | 556 | 78 | 430 (390) | 7.5 | 52 | 7.4 |
| car 30,023 | 1 | **504** | 30.5 | — | — | — | — | 614 | 85 | 511 (475) | 10.3 | 57 | 7.6 |
| | 2 | **504** | 30.3 | — | — | — | — | 615 | 86 | 512 (476) | 6.6 | 51 | 9.7 |
| | 3 | 200 | 30.8 | 9.2 | 5.6 | 13.0 | 1.1 | 615 | 104 | 510 (475) | 6.5 | 49 | 7.5 |
| | lifted | 200 | 35.4 | 10.9 | 6.3 | 13.7 | 1.2 | 616 | 116 | 509 (473) | 7.7 | 55 | 7.0 |
| barrel 4,096 | 1 | 200 | 3.5 | 1.7 | 0.7 | 0.5 | 0.1 | 478 | 63 | 350 (318) | 0.0 | 55 | 7.0 |
| | 2 | 200 | 3.0 | 1.4 | 0.7 | 0.5 | 0.1 | 479 | 63 | 353 (318) | 0.0 | 46 | 7.7 |
| | 3 | 200 | 3.2 | 1.4 | 0.8 | 0.6 | 0.1 | 477 | 63 | 351 (318) | 0.0 | 48 | 7.6 |
| **barrel 8,192** | 1 | 200 | **7.5** | 3.0 | 1.9 | 1.1 | 0.2 | 496 | 66 | 369 (333) | 0.0 | 53 | 6.7 |
| | 2 | 200 | **5.7** | 2.6 | 1.5 | 1.1 | 0.1 | 496 | 65 | 366 (330) | 0.0 | 48 | 7.2 |
| | 3 | 200 | **8.8** | 3.0 | 1.7 | 1.1 | 0.2 | 498 | 66 | 366 (331) | 0.0 | 53 | 5.6 |
| barrel 16,256 | 1 | 200 | 13.4 | 5.8 | 3.4 | 2.5 | 0.3 | 541 | 82 | 421 (386) | 0.1 | 54 | 7.7 |
| | 2 | 200 | 13.2 | 4.9 | 2.8 | 2.3 | 0.3 | 542 | 84 | 427 (392) | 0.0 | 49 | 6.6 |
| | 3 | 200 | 15.8 | 5.3 | 3.1 | 2.3 | 0.3 | 542 | 87 | 430 (398) | 0.0 | 50 | 5.9 |
| | lifted | 200 | 14.4 | 5.6 | 3.2 | 2.6 | 0.3 | 541 | 81 | 419 (383) | 0.1 | 54 | 8.0 |
| barrel 30,400 | 1 | 200 | 25.1 | 9.8 | 6.1 | 4.7 | 0.8 | 617 | 133 | 535 (491) | 0.1 | 51 | 7.2 |
| | 2 | 200 | 20.6 | 9.2 | 5.9 | 3.2 | 0.8 | 616 | 138 | 536 (487) | 0.1 | 46 | 7.2 |
| | 3 | 200 | 25.1 | 10.2 | 5.1 | 4.2 | 0.7 | 617 | 135 | 535 (492) | 0.1 | 50 | 6.5 |
| | lifted | 200 | 26.0 | 10.5 | 6.4 | 4.2 | 0.7 | 618 | 127 | 527 (495) | 0.1 | 53 | 6.7 |

Each 504 logged `forge.cad.timed_out` ("the kernel did not answer within 30s") once, and was not retried: #100's fix
holds on Linux. Payload: car 1.7 / 2.1 / 3.3 / 5.2 MB, barrel 0.9 / 1.8 / 3.6 / 6.8 MB.

Against #95 on Windows (no CPU limit, 30–55 % machine load, before #94): the 16,556-part car's interference phase is
7.3–9.3 s here against 13.8–15.9 s there, and its build 17.3–21.5 s against 20.5–26.0 s — one core is enough to beat an
unlimited but loaded laptop once the large-box index is in. The 8,192-part barrel, 37 s and 82 s in two of #95's
three runs, is 5.7–8.8 s in all three here. Kernel memory reads higher here (VmHWM 520 MiB at 8k against Windows'
433 MiB working set), which is accounting, not growth: both grow ~100 MiB from 4k to 30k.

## Pool memory: `forge-worker`, 1 CPU, 2 GiB

Two build goals of 4,000 occurrences at once (`FORGE_WORKER_CONCURRENCY=2`), each a three-step build through the kernel
with the interference check and six vision calls answered by #90's stub. `memory.peak` is the container cgroup's over
the whole run; "sum RSS" is forge-worker plus every python process, sampled together every 250 ms.

| run | pool | occurrences | s | goals | cgroup memory.peak MiB | anon peak | sum RSS peak | kernel VmHWM MiB | worker RSS | throttled s | oom_kill |
|---|---|---:|---:|---|---:|---:|---:|---|---:|---:|---:|
| p1c2-4000-r1 | 1 | 4,000 ×2 | 28.8 | 2/2 succeeded | 416 | 390 | 608 | 483 | 127 | 11 | 0 |
| p1c2-4000-r2 | 1 | 4,000 ×2 | 31.8 | 2/2 succeeded | 420 | 393 | 608 | 483 | 132 | 10 | 0 |
| p1c2-4000-r3 | 1 | 4,000 ×2 | 29.5 | 2/2 succeeded | 417 | 395 | 609 | 482 | 128 | 12 | 0 |
| **p2-4000-r1** | 2 | 4,000 ×2 | 34.2 | 2/2 succeeded | **817** | 773 | 1,190 | 481, 480 | 228 | 35 | 0 |
| **p2-4000-r2** | 2 | 4,000 ×2 | 34.9 | 2/2 succeeded | **814** | 788 | 1,206 | 481, 480 | 245 | 33 | 0 |
| **p2-4000-r3** | 2 | 4,000 ×2 | 41.5 | 2/2 succeeded | **790** | 764 | 1,182 | 480, 479 | 223 | 37 | 0 |
| p2d-400-r1 (distinct) | 2 | 400 ×2, 101 defs | 43.7 | 2/2 succeeded | 622 | 589 | 1,005 | 477, 473 | 58 | 54 | 0 |

"sum RSS" counts the shared pages of two identical interpreters twice; the cgroup does not, which is why it is lower
and is the number the limit is enforced on. #90 on Windows had pool 2 at 1,016 MiB working set and 2,770 MiB private:
the cgroup's 790–817 MiB confirms that Linux charges what is touched, near the working set, not the commit charge.

## The decisions, and their basis

The owner's rule: a limit is raised only on an unambiguous measurement. For this task that was made concrete as
"every run completes, with at least 2× margin under the 30 s timeout, inside the pod's CPU and memory limits".

**The kernel's build ceiling for a view: 4,096 → 8,192** (`geometry/limits.go maxBuiltParts`).

- 8,192: the car (8,315) and the barrel (8,192) completed in 6 of 6 runs, slowest 12.95 s = 2.32× under 30 s, the
  container at ≤ 372 MiB of 1,024. Met.
- 16,384: the car (16,556) completed in 4 of 4 runs, slowest 21.5 s = 1.40×. Not met.
- It applies to what was measured: `cad.Kernel.BuildMesh` — the whole design's mesh, a subtree's mesh (the subtree path
  sends a row of up to 8,192 parts to the kernel) and a script check. **STEP export, mass properties, a build with no
  format, the Go tessellator and every Go mesh export keep `maxDrawnParts = 4096`**: none was measured past it here.
  The browser's `LAZY_OCCURRENCES` (forge3d.js) moves with it, so a design of 4,097–8,192 parts is loaded whole again
  and one past 8,192 a subtree at a time.

**FORGE_CAD_POOL stays 1** — `deploy/k8s/31-worker.yaml` is not changed.

- Memory: pool 2 stayed well under the 2 GiB limit in every run (≤ 817 MiB, 40 %), no OOM kill. Met.
- But under the pod's one CPU a second kernel buys nothing: two concurrent builds took 34–42 s with pool 2 against
  29–32 s with pool 1, because both kernels share one core's quota (throttled 33–37 s against 10–12 s). A setting
  measured to make the pod slower is not raised on a memory number.
- And on this branch forge-worker holds no kernel (A1 is `agent/build-goal-entry`, not merged here), so the setting
  would do nothing yet. The measurement used A1's worker, built from 917d9a2.
- **What would change it:** A1 merged, and the same harness showing pool 2 faster than pool 1 with the worker's CPU
  limit at 2. The memory number above already says 2 GiB holds two kernels.

## What this does NOT establish

- **arm64, and the real node.** Production runs on an arm64 node; this is amd64 (i7-12650H) under Docker Desktop's WSL2
  VM with the same cgroup v2 CPU quota and memory limit. An arm64 core that is slower at OCCT could erase the 2.3×.
  Re-run `harness/ceil_measure.py` on the node before trusting the margin there.
- **The production image.** This used `forge-linux-test` (python:3.13-slim, Python 3.13.15, build123d 0.11.1 and
  cadquery-ocp-novtk 7.9.3.1.1 pinned, the same OCCT X/GL libraries). `deploy/Dockerfile` installs `build123d` unpinned.
- **Concurrent users.** One request at a time. With `FORGE_CAD_POOL=1`, a second 8k view waits for the first
  (≈ 12 s) before its own build starts; its 30 s kernel timeout starts when it reaches the kernel, but its caller's
  wait is the sum.
- **forged's other tenants.** Media and the audio server were off; production's 1 GiB is shared with them.
- **STEP export, mass properties and the Go exports past 4,096** — they keep the old ceiling.
- **Designs with features or scripts**; neither family has any.
- **Drawing an 8k kernel mesh in a browser** from this reply (W1 measured instanced drawing to 100k; this measures the
  server).
- **A quiet machine.** Host CPU 40–64 % from other agents throughout. The CPU quota, not the host, was the limit
  (throttling in every car run), which is why these numbers are tighter than #95's, but it is still one laptop.
- **Long-lived kernels.** Every run started forged or forge-worker afresh.

## Method

- **Tree.** `kernel/ceiling-on-linux` = #100 (`kernel/timeout-is-not-a-crash`) + `origin/kernel/step-export-scaling`
  (#106 on #94), merged with one conflict in `scripts/drill-fences.sh`'s FILES list, resolved by keeping both sides.
- **Binaries** ([`harness/build.sh`](harness/build.sh)): `git archive` of the merge commit (so no CRLF reaches Linux),
  built with Go 1.26.5 inside `forge-linux-test`: `forged-lifted` with `maxDrawnParts = 100_000` (then the only
  ceiling) and the shipped `buildTimeout = 30 s`; `forged-notimeout` with the timeout at 20 min as well; `store` from
  `docs/spikes/2026-09-15-subtree-loading/store.go`. Neither edit was committed. forge-worker, forgectl and #90's
  `stub.go` from `agent/build-goal-entry` 917d9a2.
- **Designs.** The car from `scripts/viewport-car.js carDocument(4096 | 8192 | 16384 | 30000)` with a `not_verified`
  line ([`harness/car.js`](harness/car.js)); the barrel from #95's `barrel.go`.
- **Ceiling runs** ([`harness/ceil_measure.py`](harness/ceil_measure.py)): per run, `docker run --cpus 1 --memory 1g
  --memory-swap 1g --user 10001` of forged (`FORGE_CAD_POOL=1`, Postgres `forge-pg` in its own schema
  `forge_ceil_linux` over `host.docker.internal`, the model endpoint a dead port), sign in, warm the kernel with four
  boxes, then one mesh request. Runs 1–3 interleaved across the eight designs.
- **Pool runs** ([`harness/pool_measure.py`](harness/pool_measure.py)): per run, two `forgectl goal new --build
  --start` in schema `forge_ceil_mem`, then `docker run --cpus 1 --memory 2g --memory-swap 2g --user 10001` of
  forge-worker with `FORGE_CAD_POOL=1|2`, `FORGE_WORKER_CONCURRENCY=2`, scripts off, until no goal is active.
- **Samplers**, for both: a container sharing the target's PID namespace and the host cgroup namespace reads every
  forged / forge-worker / python process's VmRSS and VmHWM, the container cgroup's `memory.current` and `memory.stat`,
  and `/proc/stat` every 250 ms; `memory.peak`, `memory.events` and `cpu.stat` are read at the end; psutil samples the
  Windows host's CPU every second. (`docker stats` was also streamed; its output did not parse and is not used.)
- Tables: `python harness/summarize.py data/results.jsonl data/pool.jsonl`.

```bash
python harness/ceil_measure.py --label shipped --bin BIN --docs DOCS --runs 3 --interleave --out data/results.jsonl \
  car-4096 car-8192 car-16384 car-30000 barrel-4096 barrel-8192 barrel-16256 barrel-30400
python harness/ceil_measure.py --label notimeout --binary forged-notimeout --write-timeout 30m --runs 1 ... \
  car-16384 car-30000 barrel-16256 barrel-30400
python harness/pool_measure.py --label p2-4000-r1 --pool 2 --conc 2 --size 4000 --copies 2 --bin BIN --out data/pool.jsonl
```

2026-09-15 15:07–15:31 EDT, Windows 11, Docker Desktop (WSL2 kernel 6.18, cgroup v2), 16 logical processors, 64 GB.

## The change

- `geometry/limits.go`: `maxBuiltParts = 8192` and `Document.BuildRefusal` ("…the most the FORGE CAD kernel builds at
  once for a view"). `maxDrawnParts = 4096` and `DrawRefusal` unchanged, now documented as everything but a view.
- `cad.Kernel.build`: a `"mesh"` build answers `BuildRefusal`; every other format and `BuildProperties` answer
  `DrawRefusal`. `SolidsAndOperations` cuts at `BuildRefusal`, the widest bound anything is built to.
- `geometry/subtree.go`: `Buildable` and `MaxBuiltParts` use `maxBuiltParts`.
- `forge3d.js`: `LAZY_OCCURRENCES = 8192`.

Fences:

- `TestLimits_TheKernelBuildsAViewOf8192PartsAndNothingElsePast4096`: 8,192 accepted for a view and 8,193 refused by
  name; the Go mesh and a mesh file of 8,192 refused at 4,096.
- `TestKernel_BuildsAViewOf8192PartsAndRefusesEveryOtherBuildPast4096`: against cadtest's fake kernel, a view of 8,192
  reaches the kernel; 8,193 is refused; a STEP export, a mass report and a build of no format of 8,192 are refused at
  4,096.
- `TestRendererLoadsLazilyExactlyPastTheKernelsViewCeiling`: forge3d.js loads 8,192 whole and 8,193 lazily.
- Moved: the W2 lazy-load fixture gains a second rivet row (4,835 → 8,835 parts) to stay past the ceiling; the subtree
  fence expects "8192" in its note; `TestLimits_ThirtyThousandOccurrencesAreStoredButNotDrawn` expects the build
  refusal from `SolidsAndOperations`.

Drills: a new section in `scripts/drill-fences.sh`, "The kernel builds a view of 8192 parts, and nothing else past
4096" — twelve drills (the ceiling back at 4096 or raised to 16384; every build, a mass report or a view at the wrong bound in `cad.go`; the kernel request, the Go mesh and a mesh file at the wrong bound; `LAZY_OCCURRENCES` at 4096 or 16384; a subtree past the ceiling sent to the kernel; `MaxBuiltParts` still returning the old constant). Dry run: 0 anchors moved. Real run (2026-09-15): **12 went red, 0 stayed green, 0 unproven**, and the tree was restored byte-identical.
