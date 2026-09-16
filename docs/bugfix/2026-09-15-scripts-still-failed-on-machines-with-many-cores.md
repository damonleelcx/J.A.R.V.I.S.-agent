# Scripts still failed on machines with many cores

**Date:** 2026-09-15 · **Status:** fixed (PR against `main`, follows #67) · **Severity:** high: a model-written scripted part fails on any node with many CPUs, even with #67's fix

## Summary

A scripted part runs build123d under a 10 s CPU limit and a 1 GiB address-space cap. #67 ran the script's native thread pools on one thread (OpenMP, OpenBLAS, TBB and MKL) after a 4-CPU runner hung, and that was proven on the 4-CPU arm64 runner. On a 16-CPU amd64 machine the same gear still failed in about 7 s:

> the script was stopped before it finished: it used more time or memory than a single part is allowed, or the kernel could not build what it described.

The threads were still there. build123d runs every boolean in parallel, and OpenCASCADE runs parallel work on **its own** thread pool, sized to the processors the machine has online. No environment variable sizes that pool, so #67's four did not reach it. Sixteen threads contended for the address space under the 1 GiB cap until the CPU limit killed the process.

The fix sizes OpenCASCADE's default thread pool to one thread inside `script.py`, before any boolean runs. The 1 GiB cap and the CPU limit are unchanged.

## Symptom

- `TestScript_BuildsWhatTheVocabularyCannot` (a 20-tooth gear) failed in 6.4 s in a 16-CPU amd64 Linux container (`python:3.13-slim-bookworm`, build123d 0.11.1, cadquery-ocp-novtk 7.9.3.1.1) with #67 and #63 merged.
- The same test passes on GitHub's 4-CPU arm64 runner with #67 (3.93 s), and on 2-CPU machines without it.

## Evidence

Measured in that container with a probe that runs the real `script.py` exactly as `script.go` does (`python -I`, `scriptEnv`'s environment, FORGE's limits read from `script.go`), sampling `/proc` every 50 ms.

On #67's `script.py`:

| run | result |
|---|---|
| FORGE's limits (10 s CPU, 1 GiB address space) | **SIGKILL** at 7.7 s wall, 10.6 s CPU; **16 threads**, VmPeak 1,008,040 kB; most threads in the kernel's `vm_mmap_pgoff` / `__vm_munmap` / `lock_mm_and_find_vma` |
| CPU limit lifted, 1 GiB cap kept | **abort**: `cannot allocate memory for thread-local data`, 16 threads |
| 1 GiB cap lifted, CPU limit kept | builds (VmPeak 1.86 GB, 16 threads, 4.9 s CPU) |
| all limits lifted | builds (VmPeak 1.90 GB, 16 threads) |
| FORGE's limits, `MALLOC_ARENA_MAX=1` | still 16 threads; killed at 10.5 s CPU |
| FORGE's limits, `NUMEXPR_MAX_THREADS=1` | still 16 threads; killed |
| FORGE's limits, OpenCASCADE's global parallel mode off (`BOPAlgo_Options::SetParallelMode(false)`) | still 16 threads; killed — build123d sets `SetRunParallel(True)` on each boolean, which overrides it |
| **FORGE's limits, OpenCASCADE's default pool sized to 1** | **builds: 1 thread, VmPeak 749,568 kB, 4.1 s CPU** |
| container pinned to 4 CPUs (`--cpuset-cpus=0-3`) | identical to the rows above: still 16 threads |

In the container `OSD_Parallel::NbLogicalProcessors()` is 16 and the default pool has 16 threads. The loaded thread-pool libraries are `libgomp` (twice), `libscipy_openblas`, `libTKernel` and `libTKBO`. The process had 3 open file descriptors, so `RLIMIT_NOFILE` (64) was ruled out.

With the fix, in the same container, the probe on the fixed `script.py` under FORGE's limits builds the gear (volume 43,345 mm³) in 4.9 s: **1 thread, VmPeak 749,880 kB, 4.8 s CPU**. `TestScript_BuildsWhatTheVocabularyCannot` passes in **4.31 s**, and every `TestScript_*` test in `internal/domain/cad` passes.

## Impact

- **Product:** a scripted part whose build uses booleans fails on any node whose online processor count makes OpenCASCADE's pool outgrow the 1 GiB cap, whatever CPU limit the pod has. A pinned cpuset or a Kubernetes CPU limit does not help: the pool is sized from processors **online**, not the CPUs the process may use. #67 closed the case that was measured (a 4-CPU runner) but not this one.
- **CI:** a 4-CPU runner passes, so CI would not have shown it. It was found by running the kernel suite in a 16-CPU container.

## Root cause

- **Surface:** `script.py` loads build123d, whose booleans call `SetRunParallel(True)` (`build123d/topology/shape_core.py`, `build123d/topology/utils.py`). OpenCASCADE then runs them on `OSD_ThreadPool::DefaultPool()`, which it sizes to `OSD_Parallel::NbLogicalProcessors()`.
- **Deeper:** #67's diagnosis was right about the mechanism — per-thread address space under a fixed cap — but assumed every native pool reads a thread-count environment variable. OpenCASCADE's does not, and `TBB_NUM_THREADS` is not a variable oneTBB reads either.
- **Why it hid:** the machines it was measured on (4 CPUs with #67, 2 CPUs before it) have too few online processors for the pool to reach the cap, and a CPU-limited container on a larger host still reports the host's processors.
- **Owner:** the script sandbox from #43, and #67's fix, which named the pools it knew about.

## Fix

`internal/domain/cad/script.py`: `_one_kernel_thread()` sizes OpenCASCADE's default pool to one thread (`OSD_ThreadPool.DefaultPool_s(1)`, and `Init(1)` if the pool already exists, so import order cannot undo it). `namespace()` calls it right after importing build123d and before any script runs. The import comes first so a machine without a kernel still fails with `No module named 'build123d'`, the reason the tests skip on. The success reply reports `kernel_threads`, carried as `ScriptResult.KernelThreads`. #67's environment variables stay: OpenMP and OpenBLAS are loaded too.

Not chosen:

- **raising the address-space cap:** the pool grows with the node, so any fixed cap fails again on a larger one, and a model-written script would get more memory;
- **OpenCASCADE's global parallel mode off:** build123d overrides it per boolean (measured above);
- **limiting CPUs with a cpuset:** the pool ignores it (measured above).

## Verification

- `TestScript_RunsOpenCascadeOnOneThread` runs a boolean in a script and requires `KernelThreads == 1`. It skips on a one-CPU machine, where it could not fail.
- The drill "OpenCASCADE's own pool starts a thread per core again" puts back the default pool size, and the fence goes red.
- In the 16-CPU container: the fence, the gear test, #67's environment fence and `TestScript_StopsOneThatWillNotFinish` pass, and so does every `TestScript_*` test.

## Related

- #67 and `docs/bugfix/2026-09-14-scripts-hung-on-machines-with-four-or-more-cores.md`, which this completes.
- #61 (K0), whose kernel job runs on a 4-CPU runner and so cannot show this; `make cad-script-timing` there is the probe's ancestor.
- Probe lesson, recorded because it cost a round: a probe that reads the child's output only after it exits reports every successful build as a hang, because a STEP reply outgrows the pipe buffer. The first version of this probe did, with the thread in `anon_pipe_write`. #67's probe notes the same trap.
