# Scripts hung on machines with four or more cores

**Date:** 2026-09-14 · **Status:** fixed (PR against `main`) · **Severity:** high: a model-written scripted part fails after 30 s on any node with 4+ CPUs

## Summary

A scripted part runs build123d under two safety limits: 10 s of CPU and a 1 GiB address-space cap. OpenCASCADE and the
maths libraries under it start a thread per core. On a 4-CPU machine, a boolean-heavy script started enough threads to
reach the address-space cap. One thread could not get memory, and the others waited for it forever. Because they
waited **asleep**, the CPU limit never fired, and the part failed at the 30 s wall clock:

> the script did not finish within 30s. A drawing this expensive is one nobody can wait for

That message blames the drawing, but the drawing builds in 3.5 s.

The fix starts the script process with one thread for every native thread pool: OpenMP, OpenBLAS, TBB and MKL. The
1 GiB cap is unchanged. Decided 2026-09-14.

## Symptom

- CI's kernel job: `TestScript_BuildsWhatTheVocabularyCannot` (a 20-tooth gear) failed at 30.04 s. It did so twice on
  `ubuntu-latest` (x86_64) and once on `ubuntu-24.04-arm` (arm64).
- The same script built in 2 s on a laptop, and in 2.0 to 3.1 s in the arm64 production image on a 2-CPU VM, with or
  without FORGE's limits.

## Evidence

Measured on GitHub's `ubuntu-24.04-arm` runner (4 CPUs, no rlimits of its own) by `scripts/cad_script_timing.py`,
which runs the gear through the real `script.py`:

| run | result |
|---|---|
| FORGE's limits (10 s CPU, 1 GiB address space) | **hung**: at 20 s it had 7 threads, VmSize 1,032,152 kB (= the cap), state sleeping |
| address-space cap only | **hung**: the same |
| CPU limit only | built, 3.4 s |
| no limits | built, 3.4 s |
| FORGE's limits, `OMP`/`OPENBLAS`/`TBB`/`MKL_NUM_THREADS=1` | built, 3.5 s |
| a single box, FORGE's limits | built, 2.2 s |

Ruled out earlier on a 2-CPU VM:
- the address-space cap alone (the gear builds at 1 GiB, and fails to import at 512 MB);
- the BLAS/OpenMP thread count *setting* (2 to 16 threads all built).

That VM never had the CORES to start the threads that fill the address space.

## Impact

- **Product:** a scripted part whose build is multi-threaded fails on any node with 4 or more CPUs, after a 30 s wait,
  with a message that blames the drawing. It has done so since scripted parts landed (#43).
- **CI:** the kernel job could never be green, which kept stage K0 unproven.

## Root cause

- **Surface:** the script process environment set no thread counts, so each native pool sized itself from the CPU
  count.
- **Deeper:** the address-space cap bounds virtual memory, and every thread reserves some, so the room left for real
  work shrinks as core count grows. The limit was right for a single-threaded build and wrong for a multi-threaded
  one, and nothing pinned the build to one thread.
- **Why it hid:** every machine it was tried on locally had too few cores to reach the cap, or did not enforce the
  cap at all. macOS refuses `RLIMIT_AS` outright; `script.py` logs "limits unavailable".
- **Owner:** the script sandbox from #43, whose limits assumed a build that would not multiply its address space by
  the core count.

## Fix

`internal/domain/cad/script.go`: the environment is built by `scriptEnv(dir)`, which keeps what it already carried
(`PATH`, `HOME`, `LC_ALL` and nothing the server holds) and adds `OMP_NUM_THREADS`, `OPENBLAS_NUM_THREADS`,
`TBB_NUM_THREADS` and `MKL_NUM_THREADS`, all `1`. The code carries the Why and a link here. Python's `-I` isolated
mode ignores `PYTHON*` variables only, so the native libraries still read these.

Not chosen:
- **raising the cap:** the threads a process starts grow with core count, so any fixed cap hangs again on a bigger
  node, and a model-written script would get more memory;
- **adding a stall detector:** a second mechanism for a cause this removes.

## Verification

- `TestScriptEnv_RunsTheKernelOnOneThread` checks that every thread setting is `1` and that the environment holds
  nothing outside the allowed keys.
- The drill "a script's kernel starts a thread per core again" removes one setting, and the fence goes red.
- **On the machine that hung:** this fix merged into #61 so its 4-CPU arm64 kernel job runs
  `TestScript_BuildsWhatTheVocabularyCannot` with it. That result is recorded in #61.

## Related

- `docs/plan-2026-09-13-millions-of-parts.md`, stage K0 and the execution record's open item.
- PR #61 (K0), which carries `make cad-script-timing`, the probe that found this.
