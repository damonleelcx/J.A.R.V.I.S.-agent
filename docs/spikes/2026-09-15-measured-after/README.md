# Measurement: the 1M build after #121, STEP after #114, and FORGE_CAD_POOL on Linux for both processes

**Date:** 2026-09-15 · **Status:** done, one shared laptop, four other agents working · **Follows:** `2026-09-15-last-hot-spots` (#121), on #115 → #114 →
#113 → #106 → #94 → #89, and #110's `2026-09-15-ceiling-on-linux` (branch `kernel/ceiling-on-linux`) and #90's
`2026-09-15-worker-kernel-memory` (branch `agent/build-goal-entry`)

This measures the three things the scale work left open, and corrects two numbers the earlier spikes are cited as
saying. Every row carries the machine load it was taken at: four other agents were working throughout, so the host
sat at 40–63% CPU and never went quiet. A figure labelled with its load is the measurement; an unmeasured cell is
not a better one.

## Corrections to what the earlier spikes are cited as saying

These two came out of reading the spikes rather than measuring, and they change what the baselines mean.

**1. `1M full build 102 s, check 52.8 s` is #114's, and it is in `2026-09-15-check-profile`, not
`2026-09-15-mesh-per-definition`.** The pair is in check-profile's "After, and before beside it" section, from #89's
`measure.py` in `full` mode against two frozen sidecars back to back. `mesh-per-definition` is the K4 mesh spike; it
records no 1M figure of any kind and carries no PR number — its numbers are 30,000-occurrence single-box mesh rows
(tessellation 129 s → 0.73 s). Anyone comparing a 1M build against "the mesh-per-definition numbers" is comparing
against numbers that do not exist.

Two qualifications travel with the pair, both from check-profile itself:

- It is **one run a size**, not a mean of repeats.
- `52.8 s` is the **build's** interference phase. The same spike's check-alone profile gives **51.2 s** for the same
  work at the same size. They are two different measurements, and quoting either as "the check at 1M" without saying
  which one is being quoted invites a false comparison of ±1.6 s.

**2. #121's `36.5 GB` is machine-wide memory used, not a python RSS.** #121's "What this does NOT establish" reads
that the 1M keying profile "was killed part-way (the machine is shared and its python was holding 36.5 GB)". The
recorded sample behind that sentence is `data/load-profiles.log:87` —
`cpu 36.5% mem 36.5 GB used | System Idle Process 64.2%, python.exe 6.2%, …` — which is **total memory used on the
host**, and the 36.5 also appears there as a CPU percentage. No process RSS of 36.5 GB was measured. Two further
points: what was killed was the **keying profile** (`profile_keys.py`), not a build, and that file's data has rows
for 90,880 and 302,560 only; and the largest 1M RSS actually recorded anywhere in this work is **5.78 GB** (#89's
`mesh` mode). This matters because `measure.py`'s own RSS cap is 24 GB — a cap that a 36.5 GB process would trip and
a 5–6 GB one would not, so mistaking the figure for an RSS makes 1M look unmeasurable when it is not.

## 1. The 1M barrel after #121, in `full` and `mesh`

**How to read these rows: as ratios and phase shares, NOT as seconds comparable with #114's.** The machine was not
merely busy during these runs, it was starved. The first 1M `full` run took 1,208 s of wall while its kernel child
accumulated only a few hundred CPU-seconds — roughly a tenth of one core — because four other agents were working
and one of them was running a CAD kernel of its own. An absolute second from these rows put beside #114's 102 s
would say "#121 made the 1M build twelve times slower", which is false.

Two things establish that this is starvation and not a regression in any phase:

1. **The inflation is uniform across phases.** Every phase is ~8–15× #114's — shapes 306.3 s against 38.6, assembly
   53.3 against 6.5, interference 808.3 against 52.8. A change that had slowed the build would have slowed the phase
   it touched, not all of them by about the same factor. Proportional inflation across unrelated phases is what
   losing a fixed share of the CPU looks like.
2. **The build computed exactly what #113 and #114 computed.** The volume is `3643661325.509079`, the same value
   #113 records after its change, and the reply is 1,686,237 B against #114's 1,686,206 B — with the same
   1,760,000 clashes found, 10,000 listed and not truncated, 15 booleans, 2,191,333 reused and 4,812,088 box tests
   in **both** `full` runs. The work is identical; only the time taken for it is not. Without that match a reader
   would have no reason to trust a ratio taken from a starved run.
3. **Peak memory matches the baselines to 0.01 GB.** `full` peaked at **5.35 GB** against #114's 5.35, and `mesh`
   at **5.69 GB** against #113's 5.68. A build doing less work, or a different build, would not land on the same
   high-water mark; a starved one lands on exactly it, because starvation costs time and not memory.

A starved absolute number presented as comparable would be worse than no number at all. These rows are therefore
reported with the load each was taken at, and compared within themselves rather than against other spikes.

1,008,160 occurrences, this branch's sidecar, **no cProfile**, `--rss-cap-gb 24`. Nothing was stopped by the cap:
the highest RSS seen was 5.69 GB, 24% of it.

| mode | run | build s | shapes | assembly | interference | properties | mesh | peak GB | host CPU | other processes |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---|
| full | 1 | **1,208.2** | 306.3 | 53.3 | 808.3 | — | — | 5.35 | 49.3% | agent.test, go, python |
| full | 2 | **242.8** | 140.2 | 9.3 | 86.5 | — | — | 5.35 | 55.1% | agent.test, go, python |
| mesh | 1 | **165.8** | 50.5 | 10.3 | 24.3 † | 49.8 | 23.9 | 5.69 | 59.3% | agent.test, go, python |
| mesh | 2 | *not measured* | | | | | | | | cut deliberately — see below |

† a `mesh` run replaces the check with V1's grid, so that column is not an interference-check figure.

Both `full` runs found 1,760,000 clashes of 2,191,348 pairs (10,000 listed, not truncated, 15 booleans); the `mesh`
run produced 60 triangles across 4 definitions, which is K4's per-definition meshing, not a mesh of 1M solids.

Beside the recorded baselines, as **wall ratios only** — the baselines were taken at 17–20% host CPU and these at
49–59%, so these ratios measure this machine's contention at least as much as they measure the code:

| | #114 after | #113 after | here, best run | ratio to #114 | ratio to #113 |
|---|---:|---:|---:|---:|---:|
| `full` build | 102.0 | 173.9 | **242.8** | 2.38× | 1.40× |
| `full` interference | 52.8 | 120.6 | **86.5** | 1.64× | 0.72× |
| `mesh` build | — | 110.8 | **165.8** | — | 1.50× |

**Run 1 against run 2 is the clearest statement of what the contention was worth**: the same build, same sidecar,
same inputs, 1,208.2 s and then 242.8 s — a **5.0× spread between two runs of one cell**, with run 1 inflated
7.9–15.3× across every phase and run 2 inflated 1.4–3.6×. The coordinator throttled a second agent's CAD kernel
between them. No conclusion about #121's effect on the 1M build can be drawn from either number, and none is drawn
here.

**What was cut, and why.** `mesh` run 2 was not measured. With four agents working and no quiet window available,
the remaining wall-clock was spent on the STEP and pool cells, which were wholly unmeasured, rather than on a second
repetition of a cell already measured once. A 1M repeat would not have become comparable with #114's by being taken
later in the same evening.

## 2. STEP after #114 and #121, at 90,880 and 302,560

`step` mode, one run a size, plus one repeat of 90,880 taken when the machine was quieter.

| occurrences | run | build s | shapes | assembly | grid | **export** | peak GB | file | host CPU |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 90,880 | 1 | 73.4 | 19.3 | 1.1 | 2.3 | **49.4** | 1.29 | 62.3 MB | 49.8% |
| 90,880 | 2 | **12.3** | 2.9 | 0.7 | 1.4 | **6.8** | 1.30 | 62.3 MB | **28%** |
| 302,560 | 1 | 85.3 | 43.4 | 2.7 | 5.9 | **31.8** | 3.37 | 212.4 MB | 47.1% |

**STEP has not regressed after #114 and #121.** Against #106's after-numbers — 90k export 7.5–9.5 s, 300k
31.8–39.2 s — the quiet 90k run exports in **6.8 s** and 300k in **31.8 s**, at or just inside the fast end of both.
The files are byte-for-byte the sizes #106 recorded (62.3 MB, 212.4 MB) and peak RSS matches (1.29 GB, 3.37 against
3.38), so this is the same export doing the same work.

**The 49.4 s first run was a contention transient, not a finding.** It is recorded because it was measured, but it
is not evidence: export at 90,880 cannot honestly be 49.4 s while export at 302,560 — 3.3× the work, same code path
— is 31.8 s in the same minutes. That inversion is physically implausible, which is what prompted the repeat; at
28% CPU the same cell gives 6.8 s. Take 6.8 s as the number and 49.4 s as a measure of the machine.

Worth noting beside #106: the *build* around the export is much faster now. 90,880 builds in 12.3 s here against
#106's 47.5–57.0 s, with shapes at 2.9 s against ~21 s, which is #113/#114/#121's work on the shapes and check
phases showing up in the STEP path even though the export itself is unchanged.

## 3. FORGE_CAD_POOL on Linux, in both processes' pods

### (a) `forged`, 1 CPU / 1 GiB, concurrent mesh requests of 8,192 parts

The lifted binary (see "What this does NOT establish"). Every request returned 200 in every cell; no OOM kill
anywhere. Two runs a cell.

| pool | at once | run | total wall s | slowest | fastest | kernel procs | kernel VmHWM MiB | forged VmHWM | cgroup anon peak MiB | throttled s | host CPU |
|---:|---:|---:|---:|---:|---:|---:|---|---:|---:|---:|---:|
| 1 | 2 | 1 | **4.12** | 4.10 | 2.57 | 1 | 513 | 65 | 349 | 0.1 | 47.8% |
| 1 | 2 | 2 | **3.12** | 3.10 | 2.44 | 1 | 513 | 65 | 346 | 0.1 | 41.6% |
| 2 | 2 | 1 | **7.74** | 7.70 | 5.03 | 2 | 506, 502 | 66 | 640 | 7.7 | 54.6% |
| 2 | 2 | 2 | **5.61** | 5.58 | 2.96 | 2 | 506, 501 | 65 | 634 | 7.3 | 42.1% |
| 1 | 4 | 1 | **5.10** | 5.07 | 1.50 | 1 | 516 | 87 | 365 | 0.2 | 44.3% |
| 1 | 4 | 2 | **4.96** | 4.93 | 1.39 | 1 | 516 | 81 | 371 | 0.2 | 42.4% |
| 2 | 4 | 1 | **8.36** | 8.33 | 2.95 | 2 | 514, 503 | 85 | 655 | 13.5 | 45.5% |
| 2 | 4 | 2 | **8.77** | 8.68 | 3.20 | 2 | 517, 502 | 86 | 660 | 13.1 | 43.9% |

Pool 1 does serialise the requests — at four at once the fastest returns in ~1.4 s and the slowest in ~5.0 s, which
is the queue #93's subtree requests sit in. **Pool 2 does not fix it; it makes it worse.** A second kernel is slower
in *every* run at both concurrencies (7.74/5.61 against 4.12/3.12 at two; 8.36/8.77 against 5.10/4.96 at four),
because both kernels share one core's quota: throttling rises from 0.1–0.2 s to 7.3–13.5 s. The queue is a CPU
limit, not a pool limit.

Memory is the second reason. Each kernel's high-water mark is ~500–517 MiB, so two of them is ~1,010 MiB against the
pod's **1 GiB total** — and that total is shared with the API and the audio server. The sampled cgroup anon peak
nearly doubles, 346–371 MiB to 634–660 MiB.

### (b) `forge-worker`, 2 GiB, two concurrent 4,000-occurrence build goals, at 1 and 2 CPUs

The A1 worker binary (`agent/build-goal-entry` `917d9a2`), which holds a kernel. All goals succeeded 2/2 in every
cell; `oom_kill` is 0 everywhere. Two runs a cell.

| CPUs | pool | run | s | goals | cgroup memory.peak MiB | anon MiB | sum RSS MiB | kernel VmHWM MiB | worker RSS | throttled s | oom_kill | host CPU |
|---:|---:|---:|---:|---|---:|---:|---:|---|---:|---:|---:|---:|
| 1 | 1 | 1 | **19.7** | 2/2 | 423 | 401 | 613 | 481 | 133 | 10.6 | 0 | 27.6% |
| 1 | 1 | 2 | **19.7** | 2/2 | 422 | 400 | 612 | 481 | 133 | 9.9 | 0 | 27.1% |
| 1 | 2 | 1 | **22.1** | 2/2 | 811 | 782 | 1199 | 480, 479 | 240 | 28.1 | 0 | 26.5% |
| 1 | 2 | 2 | **22.1** | 2/2 | 818 | 784 | 1195 | 479, 478 | 237 | 28.7 | 0 | 26.7% |
| 2 | 1 | 1 | **17.3** | 2/2 | 419 | 396 | 611 | 484 | 126 | 3.7 | 0 | 28.3% |
| 2 | 1 | 2 | **17.4** | 2/2 | 422 | 395 | 607 | 481 | 126 | 3.8 | 0 | 29.4% |
| 2 | 2 | 1 | **12.6** | 2/2 | 782 | 756 | 1171 | 479, 478 | 214 | 6.3 | 0 | 34.2% |
| 2 | 2 | 2 | **15.5** | 2/2 | 788 | 742 | 1157 | 479, 478 | 199 | 9.0 | 0 | 42.7% |

**At the pod's own CPU limit of 1, pool 2 is slower in both runs** (22.1 s against 19.7 s), with throttling at
28.1–28.7 s against 9.9–10.6 s — #110's result, reproduced against a worker that actually holds a kernel.

**At 2 CPUs, pool 2 is faster in both runs** (12.6 and 15.5 s against 17.3 and 17.4 s) and still fits: cgroup
`memory.peak` 782–788 MiB of 2,048, about 38%, with no OOM kill. That is precisely the condition #110 named as what
would revisit its decision — "A1 merged, and the same harness showing pool 2 faster than pool 1 with the worker's
CPU limit at 2" — and it is now measured true.

## The decisions, and their basis

The rule this spike holds itself to, from the brief: **pool 2 goes into a pod's manifest only if it fits that pod's
limit with headroom in every run AND is faster.** If it needs more memory than the pod has, the memory number is
proposed and no manifest changes. Either way the manifest is committed, never applied. #110 raised the same question
and answered it "stays 1" on a CPU number, not a memory one: pool 2 fit 2 GiB at 817 MiB but took 34–42 s against
pool 1's 29–32 s, because both kernels shared one core's quota (throttled 33–37 s against 10–12 s). It named what
would revisit that: A1 merged, and the same harness showing pool 2 faster with the worker's CPU limit at 2.

**`FORGE_CAD_POOL` stays 1 for both pods. No manifest is changed, and nothing is applied.**

- **`forged` (`deploy/k8s/30-forged.yaml`, 1 CPU / 1 GiB): pool 2 fails the rule on both halves.** It is slower in
  all four of its runs, not one, and two kernels at ~505–517 MiB each come to ~1,010 MiB against a 1 GiB limit that
  also has to hold the API and the audio server. Neither "faster" nor "with headroom" is satisfied.
- **`forge-worker` (`deploy/k8s/31-worker.yaml`, 1 CPU / 2 GiB): pool 2 fits but is slower, so it fails the rule.**
  Memory is comfortable — 811–818 MiB of 2,048, no OOM kill — but at the pod's CPU limit of 1 it takes 22.1 s
  against pool 1's 19.7 s in both runs. A setting measured to make the pod slower is not raised on a memory number.
  This is the same conclusion #110 reached, now on the A1 worker rather than a branch where the setting was inert.

**Proposed, not changed: raising the worker to 2 CPUs and the pool to 2, together.** At 2 CPUs pool 2 finishes two
concurrent 4,000-occurrence builds in 12.6–15.5 s against 17.3–17.4 s, a 1.2–1.4× improvement, while peaking at
782–788 MiB of the pod's existing 2 GiB. The memory limit does not need to move; the **CPU limit does**, from
`cpu: "1"` to `cpu: "2"`. Pool 2 without that CPU change is a regression, so the two are one decision and not two.
It is left as a proposal because the brief's rule is evaluated at the pod's current limits, and because the CPU
number that makes it pay was measured on amd64 — the production node is arm64, where a core slower at OCCT could
erase a 1.2–1.4× win. Whoever takes it should re-measure there first.

Because no manifest value changed, nothing here needed a new fence or drill: `scripts/drill-fences.sh` guards
source, and this spike changed none.

## What this does NOT establish

- **The arm64 production node.** There is no arm64 hardware here. Every container figure is amd64 under Docker
  Desktop on WSL2. #110 said the same thing and it is still the load-bearing gap: an arm64 core slower at OCCT could
  erase a CPU win measured here, and the pool decision in particular turns on CPU rather than memory.
- **The production image.** `deploy/Dockerfile` installs `build123d` **unpinned** (`pip install build123d`, no
  version), so the kernel it ships is whatever PyPI resolves on the day it is built. Everything here ran against
  build123d 0.11.1 with cadquery-ocp-novtk 7.9.3.1.1 — pinned by the image used, not by the repository. There is no
  `internal/domain/cad/requirements.txt` on any branch to pin it (`internal/domain/cad/builders.txt` is a different
  file), so the production kernel's version is unmeasured and unfixed by anything in this spike.
- **A quiet machine.** Four other agents worked throughout, one of them running its own CAD kernel. Host CPU was
  40–63% for every row. Absolute seconds here are not comparable with #114's or #106's, which is the same caveat
  #121 recorded about its own before-rows being 1.22–1.31× slower than #114's for the *same* frozen sidecar. Only
  same-run ratios and counts are load-robust.
- **`forged` serving 8,192-part meshes as shipped.** Stage 3's forged cells ran a **lifted** binary: on this base
  branch `maxDrawnParts` is 4,096 and there is no `maxBuiltParts`, because #110's 8,192 view ceiling lives on
  `kernel/ceiling-on-linux` and is not merged here. `BuildMesh` refuses through `Document.DrawRefusal()`, so as
  shipped an 8,192-part mesh request is refused by the storage door before the kernel sees it. The edit
  (`maxDrawnParts = 100_000`) was applied to build `forged-lifted` and **never committed**; the tree is verified
  byte-identical afterwards. The mesh numbers therefore describe the kernel pool at that size, not a request this
  branch would accept today — they are **not production-ceiling numbers**. The cells ran that lifted binary in a
  container at **1 CPU / 1 GiB** (`--memory-swap` equal to `--memory`), and the sign-up and sign-in path was
  exercised as a pre-flight beforehand so an auth or config failure could not be mistaken for a pool result.
- **The real pods.** Nothing here was applied to a cluster. Container limits were set to match
  `deploy/k8s/30-forged.yaml` (1 CPU, 1Gi) and `deploy/k8s/31-worker.yaml` (1 CPU, 2Gi) with `--memory-swap` equal to
  `--memory`, which is the pods' shape, not the pods.

## Method

- **Tree.** `scale/measured-after`, branched from `origin/scale/last-hot-spots` (#121, `8f6036e`). The 1M and STEP
  runs use this branch's `internal/domain/cad/sidecar.py` in the working tree — no frozen copy, because nothing else
  edits this worktree.
- **Sizes.** `FORGE_SCALE_MEASURE_OUT=$D FORGE_SCALE_MEASURE_BAYS=9,30,100 go test -count=1 -run
  TestScaleUp_MeasureAirframeBarrel ./internal/domain/geometry` writes the kernel request Go would send:
  **90,880**, **302,560** and **1,008,160** occurrences (25.8 MB, 86.4 MB and 288.5 MB of JSON).
- **1M and STEP** ([#89's `measure.py`](../2026-09-15-one-million-occurrences/measure.py), unmodified): each run is a
  child process the parent watches, kills over the RSS cap, and records `system_cpu_pct` and the other python/go
  processes for. **No cProfile** — that is what turned #114's 1M check into 230 s and what #121's killed profile was
  doing. `--rss-cap-gb 24`, so a run that grows past 24 GB is stopped and recorded as stopped.
- **Binaries for the container runs** ([`harness/build.sh`](harness/build.sh)): cross-compiled on the Windows host
  (`GOOS=linux GOARCH=amd64 CGO_ENABLED=0`), because this `forge-linux-test` image has **no Go module cache and no
  reachable Go proxy**, so #110's build-inside-the-image is not available here. Only the finished ELF binaries are
  bind-mounted (`-v $BIN:/opt/forge:ro`), so the CRLF working tree still never reaches Linux — the reason #110 used
  `git archive`. `forged` and `forged-lifted` from this branch; `forge-worker-a1` and `forgectl-a1` from
  `agent/build-goal-entry` (`917d9a2`), the branch where the worker holds a kernel; `store` from #110's `store.go`;
  the planning `stub` from #90's `stub.go`.
- **Designs.** The 8,192-occurrence barrel from #95's `barrel.go` (four definitions, patterned), stored through
  `geometry.Service.Save` because FORGE deliberately has no endpoint that stores geometry a client sends.
- **forged cells** ([`harness/mesh_pool_measure.py`](harness/mesh_pool_measure.py)): per run, a fresh forged at
  `--cpus 1 --memory 1g --memory-swap 1g --user 10001`, sign in, warm the kernel with four boxes, then **N mesh
  requests released together off a barrier**, with each request's start and end offset from the first recorded so
  queueing is visible rather than inferred.
- **worker cells** ([`harness/pool_measure.py`](harness/pool_measure.py)): #110's harness, with this spike's
  container and schema names, at `--cpus 1` and `--cpus 2`. Two `forgectl goal new --build --start` of 4,000
  occurrences, then a fresh forge-worker with the pool and concurrency asked, until no goal is active.
- **Samplers**, for both: a container sharing the target's PID namespace and the host cgroup namespace reads every
  forged / forge-worker / python process's VmRSS and VmHWM and the cgroup's `memory.current`/`memory.stat` every
  250 ms; `memory.peak`, `memory.events` (`oom_kill`) and `cpu.stat` (throttling) are read at the end; psutil samples
  the host's CPU every second.
- **Postgres** `forge-pg` over `host.docker.internal:55840`, in this spike's own schemas `forge_meas_forged` and
  `forge_meas_pool`, so nothing here touches another agent's data.
- Tables: `python harness/summarize.py data/forged.jsonl data/worker.jsonl data/results.jsonl`.

2026-09-15, Windows 11, Docker Desktop (WSL2, cgroup v2), Intel Core i7-12650H (16 logical processors), 64 GB,
Python 3.13.9 / build123d 0.11.1 / cadquery-ocp-novtk 7.9.3.1.1 on the host and Python 3.13.15 / build123d 0.11.1 in
the container, Go 1.26.5.
