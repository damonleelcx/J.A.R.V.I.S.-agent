# Measurement: the kernel build ceiling, through the mesh endpoint

**Date:** 2026-09-15 · **Status:** done, one laptop, shared · **Stage:** Phase 4 follow-up (K1–K4, V1) of
[`docs/plan-2026-09-13-millions-of-parts.md`](../../plan-2026-09-13-millions-of-parts.md)

## Summary

- **The ceiling stays at 4,096.** Nothing measured here is unambiguous enough to raise it, and 32,768 is plainly
  unsupported: **with the shipped 30 s kernel timeout, a 30,023-part car and a 60,640-part barrel get HTTP 501** after
  64–66 s — the kernel is killed at 30 s, restarted, retried, and killed again.
- **The car crosses the timeout between 16,556 and 30,023 parts.** 16,556: 20.5–26.0 s in four runs. 30,023: 40.3 s
  in-process at 23 % machine load, 93–101 s through `forged` at 33–36 %.
- **The barrel crosses it between 30,400 and 60,640** (21.2–29.5 s, then 65–69 s) — but its 8,192-part build took
  **82 s and 37 s** in two runs and 6.6 s in a third: on this shared machine one run can be twelve times another.
- **Interference is the phase that sets the ceiling on the car**: 54–62 % of the build from 8k up (14 s at 16.6k,
  26–58 s at 30k), 92.6 million box tests at 30k, and **truncated by its 2,000-boolean budget at 60k**. The mesh itself is
  under 2 s at every size (K4); shapes and assembly are the rest.
- **Memory fits the pod at every size measured**, which is not what limits it: kernel peak 407 → 472 → 545 → 703 MiB and
  `forged` 66 → 87 → 138 → 252 MiB at 4k → 16k → 30k → 60k. Both are in one container limited to 1 GiB, so 60k is
  955 MiB of it.
- **Payload** grows linearly: 1.7 / 2.1 / 3.3 / 5.2 / 9.6 MB for the car, 0.9 → 13.6 MB for the barrel.
- **Found:** a build past the timeout is reported as `CONNECTOR_UNAVAILABLE` — *"a capability is declared but has no
  working backend"*, `retryable: false` — after twice the timeout, because the one retry meant for a dead process also
  retries a slow one. A slow build reads as a missing kernel. Not changed here (recommendation 2).

## The table

Seconds; phases are the sidecar's own (`forge.geometry.meshed`). "wall" is the HTTP request, sign-in and warm-up
excluded. Memory is the peak working set, MiB, Windows' own `PeakWorkingSetSize` (for a killed kernel, the 100 ms
sample). CPU is the whole machine's over the run. `n1`, `n2`: timeout lifted; `s`: shipped 30 s kernel timeout.

### Car (`scripts/viewport-car.js`, 27 shapes)

| occurrences | run | wall | shapes | assembly | **interference** | mesh | payload MB | forged | kernel | CPU | 30 s timeout |
|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---|
| 4,898 | n1 | 6.3 | 1.7 | 1.1 | 2.8 | 0.5 | 1.67 | 66 | 419 | 49 % | |
| | n2 | 6.6 | 1.8 | 1.1 | 3.0 | 0.6 | | 67 | 419 | 51 % | |
| | s | 6.7 | 1.9 | 1.2 | 2.9 | 0.6 | | 66 | 420 | 55 % | 200 |
| 8,315 | n1 | 12.6 | 2.5 | 1.5 | 7.7 | 0.8 | 2.11 | 69 | 433 | 49 % | |
| | n2 | 13.2 | 2.7 | 1.8 | 7.7 | 0.8 | | 69 | 434 | 50 % | |
| | s | 13.1 | 3.2 | 1.9 | 7.2 | 0.7 | | 69 | 433 | 47 % | 200 |
| 16,556 | n1 | 26.0 | 5.5 | 3.6 | 15.9 | 0.7 | 3.34 | 85 | 472 | 50 % | |
| | n2 | 23.0 | 5.0 | 3.1 | 13.8 | 0.7 | | 76 | 472 | 44 % | |
| | s | 23.1 | 4.8 | 3.4 | 13.9 | 0.7 | | 83 | 472 | 41 % | 200 |
| 30,023 | n1 | 100.9 | 23.3 | 16.5 | 57.9 | 2.0 | 5.22 | 113 | 544 | 33 % | |
| | n2 | 93.4 | 27.9 | 13.6 | 49.3 | 1.3 | | 119 | 542 | 36 % | |
| | s | **66.4** | — | — | — | — | — | 92 | 546 | 41 % | **501** |
| 60,173 | n1 | 103.0 | 39.2 | 10.6 | 49.6 | 1.8 | 9.56 | 189 | 687 | 35 % | |
| | n2 | 93.9 | 15.2 | 11.3 | 60.0 | 5.8 | | 207 | 687 | 36 % | |
| | s | **64.7** | — | — | — | — | — | 175 | 691 | 30 % | **501** |

### Barrel (#89's `airframeBarrel`, `barrel.go`, 4 shapes, long stringers)

| occurrences | run | wall | shapes | assembly | **interference** | mesh | payload MB | forged | kernel | CPU | 30 s timeout |
|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---|
| 4,096 | n1 | 9.7 | 3.6 | 2.4 | 3.3 | 0.2 | 0.92 | 66 | 407 | 31 % | |
| | n2 | 14.1 | 4.8 | 2.9 | 5.7 | 0.3 | | 66 | 407 | 37 % | |
| | s | 3.6 | 1.1 | 0.8 | 1.5 | 0.1 | | 66 | 407 | 40 % | 200 |
| 8,192 | n1 | **81.9** | 34.1 | 16.7 | 26.7 | 1.7 | 1.83 | 69 | 429 | 41 % | |
| | n2 | **37.2** | 16.9 | 9.0 | 10.5 | 0.2 | | 70 | 429 | 44 % | |
| | s | 6.6 | 2.3 | 1.5 | 2.4 | 0.2 | | 69 | 430 | 43 % | 200 |
| 16,256 | n1 | 13.5 | 4.8 | 3.2 | 4.7 | 0.3 | 3.64 | 86 | 467 | 46 % | |
| | n2 | 13.5 | 4.8 | 3.1 | 4.8 | 0.3 | | 85 | 466 | 46 % | |
| | s | 12.0 | 4.3 | 2.7 | 4.2 | 0.3 | | 87 | 467 | 32 % | 200 |
| 30,400 | n1 | 29.5 | 9.2 | 6.8 | 11.6 | 0.8 | 6.82 | 136 | 543 | 53 % | |
| | n2 | 27.3 | 9.4 | 6.3 | 10.1 | 0.7 | | 138 | 543 | 48 % | |
| | s | 23.6 | 7.9 | 5.5 | 8.7 | 0.6 | | 136 | 545 | 32 % | 200 |
| 60,640 | n1 | 66.5 | 21.3 | 12.6 | 29.6 | 1.0 | 13.62 | 249 | 703 | 55 % | |
| | n2 | 68.9 | 21.3 | 14.0 | 30.4 | 1.1 | | 252 | 698 | 55 % | |
| | s | **65.1** | — | — | — | — | — | 183 | 669 | 32 % | **501** |

### The interference check, in-process (`build.go`, `cad.Kernel.BuildMesh`, machine at 23 % when it started)

| design | wall | interference s | box tests | overlapping pairs | booleans | reused | clashes | truncated |
|---|---:|---:|---:|---:|---:|---:|---:|---|
| car 16,556 | 20.5 | 12.8 | 47.8 M | 4,042 | 855 | 3,187 | 3,566 | no |
| car 30,023 | 40.3 | 26.3 | 92.6 M | 29,606 | 1,619 | 27,987 | 4,057 | no |
| car 60,173 | 79.1 | 48.1 | 199.6 M | 115,941 | **2,000** | 558 | 2,131 | **yes** |
| barrel 30,400 | 21.2 | 7.9 | 19.3 M | 65,884 | 438 | 65,446 | 52,800 | no |

The car's box tests are 2,900–3,300 per part at every size: the grid does not narrow a car's long body panels (4.4 m,
two per seam), the case #89 found quadratic in the barrel and fixed on a branch this one does not include. The barrel's
stringers are only one to six bays long at these sizes, so it makes 635 per part. At 60k the car hits the boolean
budget, so its interference phase stops growing — which is why 60k is no slower than 30k above, and why that plateau
is not a result.

## The decision, and its basis

**No limit changes.** The owner rule is that a limit is raised only on a measured number, and a raise to any value has to
fit the 30 s kernel timeout and the 1 GiB pod on the numbers:

- **32,768:** refused by the measurement itself. The car's build at 30,023 is 40–101 s; the shipped path answered 501.
- **16,384:** the car's four runs are 20.5–26.0 s — under the timeout, by 1.15–1.5×, on a 16-thread laptop, against a
  production pod limited to **1 CPU** (`deploy/k8s/30-forged.yaml`) on a different architecture that was not measured.
  That margin is inside the spread this machine produced for a single design (the 8,192 barrel: 6.6 s to 82 s).
- **8,192:** the car is 12.6–13.2 s in three runs; the barrel took 37 s and 82 s in two of three. One of the two
  families timed out twice at this size on this machine, so this is not unambiguous either.

Memory would allow any of them (≤ 559 MiB at 16k, forged and kernel together). STEP export was not measured and stays at
4,096 in any case; so do `BuildProperties` and the Go mesh exports, which share the same ceiling.

## Recommendations

1. **Re-measure 8,192 and 16,384 on the production image** (arm64, 1 CPU, 1 GiB) on a quiet machine — the only
   measurement that could make a raise unambiguous. `measure.py` runs anywhere `forged` does, given the Windows-only
   memory reader is swapped for `/proc`.
2. **Do not retry a build that timed out, and say that it timed out.** Today a 31 s build costs the person 60+ s and
   reads *"no working backend in this deployment"*, `retryable: false`. The retry in `cad.Kernel.build` is meant for a
   process that died between requests (its comment says so); a timeout is the kernel still working.
3. **Interference is the ceiling's first wall on a car**: re-run this after #89's long-box fix lands, and consider
   whether a mesh request should run the check at all when the viewport does not show it (it is "always, not on
   request" by design — a decision, not an accident, so this is for the owner).
4. The subtree path is unaffected: it builds subtrees of ≤ 4,096 parts (1.6–3.4 s warm in #93's run) and sends larger ones
   to the Go tessellator.

## What was NOT measured

- The production image, architecture and CPU limit; a quiet machine. ‼️ Other agents ran a 1M-occurrence interference
  measurement (one Python process of ~4.5 GB) and model runs throughout, which is the likeliest cause of the 8,192
  barrel's 82 s.
- **STEP export**, `BuildProperties` (mass), and the Go mesh exports past 4,096 — they keep the ceiling.
- A design with features (neither family has any), a pool of more than one kernel, concurrent requests.
- Drawing a whole 30k mesh in the browser from this reply (W1's run drew the car; this measures the server).
- Compression: the endpoint sends identity-encoded JSON.

## Method

- **Designs.** The car at targets 4,096 / 8,192 / 16,384 / 30,000 / 60,000 (4,898 / 8,315 / 16,556 / 30,023 / 60,173
  occurrences: its fixed rows alone place 4,697), with a `not_verified` line added because the storage door requires one;
  the barrel at 4,096 / 8,192 / 16,256 / 30,400 / 60,640 (`barrel.go -out DIR`).
- **Binaries.** `forged` built from this branch with `geometry/limits.go maxDrawnParts = 100_000`, and again with
  `cad/cad.go buildTimeout = 20 * time.Minute` as well; `build.go` built with both. Never committed: `git checkout` put
  both files back straight after each `go build`.
- **Runs.** `measure.py` stores each design once through `docs/spikes/2026-09-15-subtree-loading/store.go`, then per run
  starts `forged` afresh (`FORGE_CAD_POOL=1`, local Postgres, model endpoint on a dead port), signs in over the API, warms
  the kernel with a four-part design, and requests `GET /v1/geometry/{id}/mesh` with the session as a Bearer token. A
  thread samples the working set of `forged` and of every process it started every 100 ms; Windows' peak working set is
  read at the end. Timeout-lifted runs set `FORGE_HTTP_WRITE_TIMEOUT=30m`; shipped runs keep 5 min.

```bash
go run docs/spikes/2026-09-15-kernel-build-ceiling/barrel.go -out DOCS
python docs/spikes/2026-09-15-kernel-build-ceiling/measure.py --binary forged-lifted-notimeout.exe --label notimeout \
  --docs DOCS --store store.exe --python $FORGE_CAD_PYTHON --database $FORGE_DATABASE_URL --write-timeout 30m \
  --runs 2 --out results.jsonl car-4096 car-8192 car-16384 car-30000 car-60000 \
  barrel-4096 barrel-8192 barrel-16256 barrel-30400 barrel-60640
python …measure.py --binary forged-lifted.exe --label shipped-timeout --write-timeout 5m --runs 1 …
FORGE_CAD_PYTHON=… build.exe DOCS/car-16384.json DOCS/car-30000.json DOCS/car-60000.json DOCS/barrel-30400.json
```

## Conditions

Intel Core i7-12650H (16 logical processors), 64 GB, Windows 11, Python 3.13.9, build123d 0.11.1, Go 1.26, Postgres 17
in Docker, 2026-09-15 12:32–13:01 EDT. Machine CPU 30–55 % over the runs from other agents' work.

## Also in this change (the W2 run's viewport findings)

- A mesh reply is in millimetres and the stage is in the document's unit; `drawBatches` now converts, for the whole
  reply and a subtree's alike ([bugfix](../../bugfix/2026-09-15-mesh-replies-were-drawn-in-millimetres-on-a-stage-in-the-documents-units.md)).
- Search rows show the occurrence path after the name.
- The provenance banner folds its details under its headline.

Checked in the Claude desktop Browser pane (Chrome) on `forged` built from this branch, the stored 30k car, viewport
emulated at 800 × 700 (the narrow layout; canvas 800 × 547). Coverage is the share of a 10-px grid of canvas points where
`elementFromPoint` lands on the banner rather than the canvas:

| banner | size | canvas covered | shows |
|---|---|---:|---|
| folded (default) | 776 × 44 px | **7.1 %** | "This is a proposal, not a verified design." and "Details (13)" |
| opened with the real mouse | 776 × 219 px | 39 % | all 13 notes, `aria-expanded="true"`; "Hide details" folds it back to 44 px |

Search "Rivet 1" in the Work tab: 50 rows and "13936 more — narrow the search", each reading e.g. "Rivet 1 seam-1/rivet-1",
the path in its own span at x 633–705 of 800 px, not truncated. Frame times were not taken.

Fences: `TestMeshSubtree_ADesignInInchesOrMetresIsDrawnWhereItsPrimitivesAre`,
`TestWorkbenchSearchRowsSayWhereEachOccurrenceIs`, `TestWorkbenchProvenanceBannerFoldsItsDetailsOffTheStage`. Drills (all
red, 2026-09-15): the eight new ones in `scripts/drill-fences.sh`, and the four existing drills on the unchanged build
ceiling (`TestLimits_ThirtyThousandOccurrencesAreStoredButNotDrawn` ×3, `TestKernel_ADesignTooLargeToBuildIsRefusedAndSaysWhy`)
re-run: 12 went red, 0 stayed green, 0 anchors moved.
