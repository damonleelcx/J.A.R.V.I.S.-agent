# Measurement: an airframe barrel of 1,000,000 occurrences through the kernel

**Date:** 2026-09-15 · **Status:** done · **Stage:** Scale-up milestone of [`docs/plan-2026-09-13-millions-of-parts.md`](../../plan-2026-09-13-millions-of-parts.md)

## Summary

- **1,008,160 occurrences go through the kernel's mesh path, twice, in 480 s and 493 s at 5.8 GB peak.** Shapes
  (K1: 4 shape builds, then a placement per occurrence) 242–249 s, assembly (K2) 166–170 s, V1's grid 19 s, part
  properties (V3) 33–34 s, mesh (K4: 4 definitions, 1,008,160 instances) 16 s. From 300k to 1M every one of these
  grows linearly (k ≈ 0.97–1.03; memory k ≈ 0.88).
- **The first wall at 1M is V1's interference check, which `_build` always runs.** Boxes longer than four grid cells
  (every skin panel, frame segment and stringer: 16,160 at 1M) are tested against every box. That is N² — 1.49 billion
  tests at 300k, 16.2 billion at 1M (k = 2.0) — priced by the grid's own sample at 2,620–2,680 s at 1M before a single
  boolean. **The shipped build at 1M was stopped by the 40-minute cap** (2,400 s, 5.2 GB polled peak, 21% system CPU); the killed child reports no phases, so which phase it was in is inferred from that price, not measured.
- **At 300k the shipped build finishes but its answer is truncated.** 450 s (interference 315 s, of which the
  large-box loop is ~290 s by the same sample), 1.8 GB, and the 2,000-boolean budget stops it at 316,976 of the
  528,000 clashes, marked truncated. At 90k it found all 158,400 with 1,050 booleans. Each stringer rivet sits at a
  different pose against the barrel-length stringer, so pose reuse saves nothing there: the booleans needed grow
  about 96 a bay (234 at 1 bay, 1,050 at 9) — inferred from those two counts — and pass 2,000 near 20 bays.
- **STEP export is not linear at this scale.** 4.3 s (10k), 21 s (90k), 307–391 s (300k, 212 MB, 3.4 GB): k ≈ 2.2
  from 90k to 300k. Both 300k runs were under the heaviest load of the day (32% and 39% system CPU) and the export is
  single-threaded, so load alone is not a plausible 7× — but this is one pair of sizes, not a curve. STEP at 1M was
  not attempted (owner decision) and is refused before the kernel; see below.
- **Go is not the wall.** At 1M: counting occurrences < 1 ms, expanding 2.4–2.9 s (2.8 GiB allocated), the kernel
  request 289 MB encoded in 2.5 s, V3's roll-up 2.5–3.1 s, and the mesh endpoint's payload 228 MB, decoded in 3.7 s
  and re-encoded in 1.0 s.
- **No limit is raised.** Nothing measured here gives a basis: the one path that reaches 1M is not the path a design
  takes (it replaces V1), the shipped build is truncated from ~20 bays and N² beyond, and browser draw (W1) is not in
  this branch and was not measured.

## Why this measurement

The milestone reads: a synthetic generator document (an airframe barrel with rivet generators) through K1–K4, V1 and
W1, benchmarks recorded, limits raised only on measured numbers. `cad.BuildDocument` refuses more than 4,096 parts
(stage S0) and the storage door refuses more than 100,000 occurrences, so, like the K1, K2b, K4 and V1 spikes, the
kernel numbers come from the sidecar's own `_build`, fed the request Go writes.

## The document

`airframeBarrel(bays, sectors)` in `internal/domain/geometry/barrel_scale_test.go`. Four definitions (rivet, skin panel,
stringer, frame segment) and four assemblies (bay, ring, frame, barrel). The rivet is defined once and placed by three
grid patterns in a bay; the bay is turned round the barrel by a polar pattern of 80; the ring is repeated along the
barrel by a linear pattern. Occurrences are `80 × (126 × bays + 2)`: 10,240 at 1 bay, 90,880 at 9, 302,560 at 30,
1,008,160 at 100. The stored document is 2.0–2.1 KiB at every size.

In a bay's own frame, x runs along the barrel, y points out through the skin and z runs round it.

| part | where | clashes with |
|---|---|---|
| skin panel 498 × 2 × 146 | y ∈ [R, R+2] | its rivets |
| stringer (barrel length) × 20 × 20 | y ∈ [R−20.5, R−0.5], z ∈ [−10, 10] | its rivets |
| frame segment 6 × 95 × (chord − 4) | y ∈ [R−120, R−25] | nothing |
| rivet r 2.4 × 14 | y ∈ [R−12, R+2] | skin (2 mm); stringer rivets also stringer (11.5 mm) |

96 stringer rivets and 28 frame rivets per bay per sector: **220 clashes per bay per sector, 17,600 × bays expected**.
Panels and frame segments are 4 mm narrower than their pitch; the stringer clears the skin by 0.5 mm and the frame by
4.5 mm; rivets clear each other by at least 3.2 mm.

Two fences hold the generator (`go test ./internal/domain/geometry -run TestBarrel_`): at 2 bays it has 4 definitions,
4 assemblies, the rivet placed only by 3 patterned children, 20,320 occurrences counted and expanded, and
`Document.EnumeratedRepetition()` reports nothing; and the request `barrelSolids` writes is `SolidsAndOperations`'
request wherever that one answers (1,024 occurrences).

## Method

1. **Go half.** `FORGE_SCALE_MEASURE_OUT=D go test ./internal/domain/geometry -run TestScaleUp_MeasureAirframeBarrel`
   writes each size's kernel request (`barrel-<bays>.json`) and times the storage door, expansion, the kernel request
   encode and V3's mass roll-up on synthetic measures. Rerun with `FORGE_SCALE_MEASURE_NO_REQUESTS=1` once
   `measure.py` has written `mesh-<bays>.json`, and it also decodes that as `cad` does and re-encodes it as the mesh
   endpoint's `definitions` + `instances`.
2. **Kernel half.** `measure.py` runs each build in a child process the parent watches (tree RSS polled every 0.25 s,
   stopped past 24 GB or the time cap) and appends one row per run to `results.jsonl`, with the system CPU over the run
   and the other go/node/python processes present. Three modes:
   - `full` — `_build` as shipped, format `""`: shapes (K1), features, assembly (K2), interference (V1).
   - `mesh` — format `"mesh"` with part properties (K4, V3), and the interference check replaced by the grid part of
     V1's broad phase only; the boxes larger than 4 grid cells, which the shipped loop tests against every box, are
     counted, and three of them are timed against every box to price the rest.
   - `step` — format `"step"` (K2's XDE export) with the same replacement.

Environment: Intel Core i7-12650H (16 logical processors), 64 GB, Windows 11, Python 3.13.9, build123d 0.11.1,
cadquery-ocp-novtk 7.9.3.1.1, Go 1.26.5. ‼️ **The laptop was shared** with other agents' Go/node tests and a live LLM
measurement that ends in a kernel build; every row below carries the system CPU over its run. Where a size ran twice,
the faster run is taken as the best uncontended estimate — that is an inference, not a measurement of an idle machine.

## Results: the kernel

Every run, from `data/results.jsonl`. Seconds are the sidecar's own phases; "CPU" is the whole machine's over the run.
No row was dropped: the 300k `mesh` and `step` runs were started by an earlier session that was stopped, ran to
completion on their own and wrote whole rows.

### `full` — `_build` as shipped

| occurrences | run | build | shapes | assembly | interference | peak GB | CPU | interference answer |
|---:|---:|---:|---:|---:|---:|---:|---:|---|
| 10,240 | 1 | 19.4 | 7.0 | 4.6 | 7.7 | 0.44 | 16% | 17,600 of 17,600, 234 booleans |
| 10,240 | 2 | 19.0 | 7.2 | 4.3 | 7.5 | 0.44 | 16% | same |
| 90,880 | 1 | 238.7 | 78.4 | 44.2 | 115.1 | 0.84 | 16% | 158,400 of 158,400, 1,050 booleans |
| 90,880 | 2 | 202.4 | 85.2 | 42.4 | 74.1 | 0.84 | 18% | same |
| 302,560 | 1 | 450.5 | 81.5 | 52.9 | 314.5 | 1.83 | 22% | **truncated**: 316,976 of 528,000, 2,000 booleans, 1.49 B box tests |
| 1,008,160 | 1 | **stopped at 2,400 s** | — | — | — | 5.17 (polled) | 21% | none: killed at the 40-minute cap, no phases reported. The Go payload measurement ran during its first ~25 s. |

### `mesh` — mesh and part properties, interference = V1's grid only

| occurrences | run | build | shapes | assembly | grid | properties | mesh | peak GB | CPU | reply |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 10,240 | 1 | 13.1 | 6.9 | 4.5 | 0.4 | 0.8 | 0.5 | 0.45 | 15% | 2.7 MB |
| 10,240 | 2 | 12.8 | 6.5 | 4.6 | 0.4 | 0.9 | 0.5 | 0.45 | 15% | |
| 90,880 | 1 | 70.8 | 49.5 | 14.6 | 1.7 | 3.0 | 1.6 | 0.88 | 25% | 24.3 MB |
| 90,880 | 2 | 41.2 | 20.8 | 13.8 | 1.6 | 3.0 | 1.5 | 0.88 | 15% | |
| 302,560 | 1 | 151.5 | 76.2 | 49.3 | 7.4 | 11.9 | 5.2 | 2.00 | 25% | 81.4 MB |
| 302,560 | 2 | 141.7 | 70.8 | 48.0 | 6.6 | 9.9 | 4.9 | 2.00 | 19% | |
| 1,008,160 | 1 | 479.8 | 241.5 | 166.2 | 18.8 | 32.8 | 15.9 | 5.78 | 22% | 271.9 MB |
| 1,008,160 | 2 | 492.9 | 249.3 | 169.8 | 19.2 | 33.6 | 16.1 | 5.78 | 23% | |

Every run reported 4 shape builds and 4 mesh definitions, and as many instances and part properties as occurrences.

### `step` — STEP export, interference = V1's grid only

| occurrences | run | build | shapes | assembly | grid | export | peak GB | CPU | file |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 10,240 | 1 | 15.9 | 6.5 | 4.5 | 0.6 | 4.3 | 0.50 | 16% | 6.8 MB |
| 10,240 | 2 | 24.5 | 10.7 | 8.6 | 0.7 | 4.4 | 0.50 | 16% | |
| 90,880 | 1 | 58.8 | 21.2 | 14.0 | 1.6 | 21.6 | 1.29 | 20% | 62 MB |
| 90,880 | 2 | 58.7 | 21.6 | 14.0 | 1.6 | 21.0 | 1.29 | 23% | |
| 302,560 | 1 | 543.0 | 81.0 | 60.8 | 8.4 | 391.1 | 3.39 | 39% | 212 MB |
| 302,560 | 2 | 465.2 | 96.9 | 53.6 | 6.7 | 306.6 | 3.38 | 32% | |
| 1,008,160 | — | not run | | | | | | | refused before the kernel (below) |

‼️ **System CPU does not capture all the contention.** At 90k, shapes took 78–85 s in `full` and 21 s in `mesh` and
`step` at similar system CPU, and the 10k `step` runs differ 1.5× at the same 16%. Where two runs differ, the faster
is the estimate used below — an inference, not an idle-machine measurement.

### The large boxes V1 tests against every box

From `broad_phase_only`, which runs the grid as shipped and times three large boxes against every box to price the
rest. Large boxes are the skin panels, frame segments and stringers: 80 × (2 × bays + 2).

| occurrences | large boxes | their tests | priced at | grid tests | grid s |
|---:|---:|---:|---:|---:|---:|
| 10,240 | 320 | 3.2 M | 1–5 s | 16,816 | 0.1–0.2 |
| 90,880 | 1,600 | 144 M | 23–25 s | 161,440 | 0.5 |
| 302,560 | 4,960 | 1.49 B | 257–328 s | 538,976 | 2.3–3.1 |
| 1,008,160 | 16,160 | 16.2 B | 2,624–2,680 s | 1,798,912 | 5.7–5.9 |

Grid tests grow k = 1.02 over the four sizes; large-box tests k = 2.0. The 300k price (257–328 s) is most of the
shipped check's 315 s there.

## Results: Go

From `data/go-requests-<bays>.json` (the run that wrote the requests) and `data/go-mesh-<bays>.json` (the rerun that
read the kernel's mesh replies). Two expansions and two roll-ups per run; ranges are across both runs.

| occurrences | storage door (defaults) | count | expand | allocated | kernel request | V3 roll-up | mesh endpoint payload | decode / encode |
|---:|---|---:|---:|---:|---:|---:|---:|---:|
| 10,240 | accepted | < 1 ms | 15–26 ms | 26 MiB | 2.9 MB, 16 ms | 18–23 ms | 2.3 MB | 33 / 11 ms |
| 90,880 | accepted | < 1 ms | 145–175 ms | 238 MiB | 25.8 MB, 136 ms | 156–219 ms | 20.4 MB | 332 / 84 ms |
| 302,560 | refused | < 1 ms | 0.55–0.85 s | 882 MiB | 86.4 MB, 0.81 s | 0.74–0.96 s | 68.3 MB | 1.15 / 0.29 s |
| 1,008,160 | refused | < 1 ms | 2.35–2.91 s | 2,828 MiB | 288.5 MB, 2.51 s | 2.46–3.13 s | 228.3 MB | 3.73 / 0.99 s |

The sidecar's side of the request is `decode_s` in `results.jsonl`: Python parses the 289 MB request in 3.7–3.9 s at
1M and the 86 MB one in 1.1–2.0 s at 300k, outside the build times above.

`EnumeratedRepetition()` (A3) reports nothing at every size, and the stored document is 2,048–2,113 bytes. 100,960
occurrences (10 bays) is refused by the door too: 960 over the bound.

## Where 1M stops

1. **The storage door.** `NewVariant.Validate` refuses the 1M barrel at the default 100,000 occurrences
   (`FORGE_GEOMETRY_MAX_OCCURRENCES`), counted without expanding it. A 1M design cannot be stored without raising that
   setting.
2. **The drawing and building ceiling.** `DrawRefusal()` is non-empty at every size here (> 4,096), and
   `cad.BuildDocument` and `geometry.Export` return it as VALIDATION_FAILED before expanding anything. **This is how
   STEP at 1M is refused**: the export request never reaches the kernel. The sidecar has no size check of its own — a
   direct `_build` of 1M with format `"step"` would try (read from the code; not run, by owner decision, until an
   off-node export job exists).
3. **Past both, the first wall in the kernel is V1** (`full` above): the large-box loop is N² and priced at ~2,650 s
   at 1M, and from about 20 bays the 2,000-boolean budget truncates the answer. Everything else `_build` does reaches
   1M in 480–493 s and 5.8 GB.

## What this establishes

- A generator document of four definitions describes 1,008,160 occurrences in 2.1 KiB, with the rivet placed by
  patterns and nothing for A3 to report (fenced: `TestBarrel_PlacesItsRivetsByPatternsNotByListing`).
- K1 builds 4 shapes at every size; K2's assembly, K4's per-definition mesh, V3's properties and V1's grid are linear
  from 300k to 1M on one node, in under 6 GB.
- V1's check against large boxes is quadratic, and its boolean budget truncates a barrel from ~20 bays: the two walls
  for a stored design's build.
- XDE STEP export grew k ≈ 2.2 from 90k to 300k on this machine under load.
- Go's side (count, expand, request, roll-up, mesh payload) is seconds and a few GiB at 1M.

## What this does NOT establish

- **Browser draw.** W1 (instanced drawing) is not in this branch, and nothing was drawn. The only browser-side number
  is the size of what the endpoint would send: 228 MB of JSON at 1M.
- **Cluster node memory.** Peak working set of one Python process on Windows; not the production arm64 Linux image,
  not a pod limit, not the Go process and kernel together.
- **STEP export at 1M.** Refused, not attempted. The 300k number is not extrapolated into a 1M figure here.
- **Pool concurrency.** One build at a time, one kernel process.
- **A real airframe.** Four definitions and two clash families: the best case for K1 and K4 reuse, and a bad case for
  V1's pose reuse against long parts. No features.
- **Variance.** At most two runs a size on a shared laptop.

## Recommendations (nothing here is changed)

1. **V1: take large boxes out of the every-box loop** — file them in a coarser grid, or sweep them along their long
   axis — and re-measure `full` at 300k and 1M. Basis: 16.2 B tests priced at ~2,650 s at 1M, and ~290 of the 315 s
   the shipped check took at 300k.
2. **Keep the boolean budget; make its truncation visible (V2).** 300k stopped at 316,976 of 528,000. Nothing here
   measures what a boolean costs at this scale, so there is no basis for a larger budget.
3. **Keep `FORGE_GEOMETRY_MAX_OCCURRENCES` at 100,000 and the 4,096 build/draw ceiling.** Go could hold 300k (882 MiB,
   under a second), but the only build such a design would get is 450 s with a truncated answer, and browser draw is
   unmeasured.
4. **Keep STEP refused at 1M; export belongs off-node.** 300k already takes 307–391 s and 3.4 GB, and grew faster
   than linearly from 90k.
5. **If a 1M build must be interactive,** shapes and assembly are 410 s of it: a placement per occurrence and one
   compound, both in Python. That is an inference from the phase split, not a measured alternative.

## Re-running

```bash
export GOWORK=off D=/some/dir
FORGE_SCALE_MEASURE_OUT=$D FORGE_SCALE_MEASURE_BAYS=1,9,30,100 go test -count=1 -timeout 30m \
  -run TestScaleUp_MeasureAirframeBarrel ./internal/domain/geometry
.cadvenv/bin/python docs/spikes/2026-09-15-one-million-occurrences/measure.py --dir $D --bays 1,9,30 --modes full,mesh,step
.cadvenv/bin/python docs/spikes/2026-09-15-one-million-occurrences/measure.py --dir $D --bays 100 --modes mesh --cap 2400
FORGE_SCALE_MEASURE_OUT=$D FORGE_SCALE_MEASURE_NO_REQUESTS=1 go test -count=1 -run TestScaleUp_MeasureAirframeBarrel ./internal/domain/geometry
```
