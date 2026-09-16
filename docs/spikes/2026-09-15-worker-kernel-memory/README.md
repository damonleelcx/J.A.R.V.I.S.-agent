# forge-worker's own memory while it builds, and what FORGE_CAD_POOL should be

**Run 2026-09-15, on the Windows 11 laptop, against local Postgres.** Samples: [`data/`](data). Harness: [`harness/`](harness).

**Why.** Since stage A1 forge-worker holds its own CAD kernel for a build's steps. Its pod has a 2Gi limit and
`FORGE_CAD_POOL` stayed 1 because nobody had measured what a kernel costs inside that process.

## How it was measured

- The **real `forge-worker` binary** (this branch), started fresh for each run, with the kernel
  (`build123d 0.11.1`, the `jarvis-k2b` venv), scripts off.
- A **stub OpenAI-compatible endpoint** ([`harness/stub.go`](harness/stub.go)) plans a three-step build and answers
  each step with a whole tree that grows a third per step to the size the goal's statement names; every vision call
  gets `{"problems":[]}`. So each step went through the whole gauntlet: kernel surface build (`BuildMesh`) with the
  interference check, the look at the whole model **and** its sub-assemblies (6 vision calls per build), and the
  repair asks for the overlaps the kernel found. The kernel really built the trees: step 3 of the 4,000 run reported
  "and 5,985 more" interference findings.
- Two tree shapes: **instanced** (a corner assembly of upright, hub, wheel and five lugs, patterned along x: 5
  definitions for 33 / 393 / 3,993 occurrences) and **distinct** (every corner its own assembly with its own wheel
  and hub definition: 101 definitions for 393 occurrences).
- A PowerShell sampler ([`harness/sampler.ps1`](harness/sampler.ps1)) summed **working set** and **private bytes**
  over forge-worker and every descendant (the venv `python.exe` launcher and the real interpreter under it).
- Goals ran in their own schema (`forge_a1b_mem`), never in the shared data.

## Numbers

Peak over each run; M = MiB. "Two at once" is two build goals running concurrently on one worker.

| run | pool | task loops | builds | occurrences each | peak working set | peak private | kernel WS | kernel private | Go WS |
|---|---|---|---|---|---|---|---|---|---|
| p1-40 | 1 | 1 | 1 | 33 | 462M | 1,311M | 385M | 1,247M | 76M |
| p1-400 | 1 | 1 | 1 | 393 | 436M | 1,327M | 388M | 1,249M | 48M |
| p1-4000 | 1 | 1 | 1 | 3,993 | 506M | 1,399M | 411M | 1,272M | 97M |
| p1d-400 (distinct) | 1 | 1 | 1 | 393 / 101 defs | 446M | 1,338M | 402M | 1,264M | 43M |
| **p1c2-4000** | **1** | **2** | **2 at once** | 3,993 | **530M** | **1,422M** | 414M | 1,276M | 119M |
| p2-40 | 2 | 2 | 2 at once | 33 | 806M | 2,561M | 769M | 2,493M | 37M |
| p2-400 | 2 | 2 | 2 at once | 393 | 838M | 2,591M | 776M | 2,498M | 62M |
| **p2-4000** | **2** | **2** | **2 at once** | 3,993 | **1,016M** | **2,770M** | 819M | 2,542M | 197M |
| p2d-400 (distinct) | 2 | 2 | 2 at once | 393 / 101 defs | 872M | 2,625M | 804M | 2,526M | 68M |

Every goal succeeded (3 of 3 steps kept, `runs.log`). Whole runs took 7–19 s. Machine load while sampling: 25–55%
CPU average (other agents' tests and kernels were running).

## What the numbers say

1. **A kernel process is a fixed cost, not a per-model cost, at these sizes.** One kernel is ~385–415M working set and
   ~1.25G private whatever it builds: 33 occurrences and 3,993 differ by 26M of kernel working set, and 101 distinct
   definitions cost what 5 do. The import of build123d/OCCT is the memory.
2. **Pool size multiplies it.** Pool 2 is two of those: ~770–820M of kernel working set, ~2.5G private.
3. **Task loops do not, with pool 1.** Two builds at once on one kernel (`p1c2-4000`) peaked at 530M, 24M over one
   build: the second waits for the slot. The pod runs the default `FORGE_WORKER_CONCURRENCY=4` with pool 1, and that
   is safe on memory; it is serialised on the kernel instead.
4. **Go's own working set** grew with model size (48M → 197M for two 4,000-occurrence builds), still small.

## Recommendation: keep `FORGE_CAD_POOL=1` in the 2Gi pod for now

Not changed in `deploy/k8s` — the measurement is not unambiguous for the pod:

- By **working set**, pool 2 fits: 1,016M peak for two 4,000-occurrence builds at once, half the limit.
- By **private bytes**, it does not: 2,770M. On Windows that is commit charge — memory the interpreter reserved and
  may never touch — and a Linux cgroup counts pages touched, not commit. So the Linux number is very likely nearer the
  working set, but this laptop cannot show that, and **OOMKill is the failure** if it is not.
- The sampler takes a process snapshot every ~1.3 s (a CIM query on a loaded machine), so a spike shorter than that is
  not in these peaks.

So: pool 1 is measured safe (530M worst case here, including two builds contending). **Pool 2 is likely to fit**; raise
it only after the same harness runs inside the worker image on Linux (`ps -o rss` / cgroup `memory.peak` summed over
the pod), with two 4,000-occurrence builds at once. If that peak stays under ~1.2G, pool 2 leaves the pod ~800M of
headroom. The comment in `deploy/k8s/31-worker.yaml` now cites this spike.

## What this does NOT establish

- **Linux.** Everything here is Windows accounting.
- **Larger distinct models.** A step is shown at most 64 KiB of tree (A2), which caps how many distinct definitions
  a stub step can carry; the distinct run is 101 definitions. A live car was 9. Whether memory grows at thousands of
  distinct B-reps is not measured here (the S0 build ceiling measurements are the reference for that).
- **Scripts.** Off, because the Windows script runner is known broken. A script check starts no further process, but
  runs build123d code in the same kernel.
- **Long-lived growth.** Each run was a fresh worker; a kernel that has served hundreds of steps was not sampled.
