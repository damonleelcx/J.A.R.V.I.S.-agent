# Measurement: indexing the interference check's large boxes

**Date:** 2026-09-15 · **Status:** done · **Follows:** [`2026-09-15-one-million-occurrences`](../2026-09-15-one-million-occurrences/README.md)
(#89), the scale-up milestone of [`docs/plan-2026-09-13-millions-of-parts.md`](../../plan-2026-09-13-millions-of-parts.md)

## Summary

- **The 1M barrel's shipped build now finishes, with a whole answer: 515 s and 688 s, 5.44 GB peak.** The interference
  check took 115–122 s, found all 1,760,000 clashes (17,600 × 100 bays) with 15 booleans, and was not truncated. #89's
  run of the same request was stopped by the 40-minute cap.
- **Box tests grow linearly: 425,480 → 1,432,752 → 4,812,088 at 90k, 300k and 1M (k = 1.01 both steps).** #89's were
  144 M, 1.49 B and 16.2 B (k = 2.0).
- **300k is no longer truncated.** 230–242 s build and a 54–56 s check with 528,000 of 528,000 clashes, where #89 took
  450 s and a 315 s check that stopped at 316,976. Booleans are **15 at every size** from 90k to 1M. #89's grew about
  96 a bay: 234 at 1 bay, 1,050 at 9, and past the 2,000 budget near 20.
- **Two changes did it.** (1) A grid level per axis for every box, instead of testing long boxes against every box.
  (2) The pose cache reuses a clash along a box the other solid lies wholly inside. That is provable for a finite box
  and is only claimed when containment is shown. The 2,000-boolean budget and every limit are unchanged.
- **Truncation was already visible on every product path.** The one path where a truncated or absent check read as
  clean was #89's `measure.py`, which printed no interference answer and recorded a check that never ran as
  `truncated: false, found: 0`. It now says which.
- ‼️ **The machine was loaded.** 17–61% system CPU over the runs, and the two 1M runs differ 1.33× (46% and 17%). The
  interference phase grew k = 0.63 between the best 300k and 1M runs, which is load, not a sublinear check. Against
  today's 300k run at 32% CPU (before the carried slide, same index) it is k = 1.03.
- **Nothing measured here raises a limit.** 515 s is not an interactive build, browser draw is still unmeasured, and
  the reply now carries 1.76 M interference entries, whose size and Go-side cost were not measured.

## Why this measurement

\#89 found the first wall a stored design's build meets at 1M occurrences: V1's interference check, which `_build`
always runs. It has two parts.

1. **Box tests.** A box longer than four grid cells was tested against every box. The barrel's skin panels, frame
   segments and stringers are all such boxes, 80 × (2 × bays + 2) of them. So tests grew k = 2.0: 1.49 billion at
   300k and 16.2 billion at 1M, priced at ~2,650 s. The 1M build hit its 40-minute cap.
2. **Booleans.** The pose cache keys a clash by (shape, shape, relative pose). Every stringer rivet sits at a different
   pose against the one barrel-long stringer, so booleans grew about 96 a bay. The 2,000 budget truncated the answer
   from ~20 bays: 316,976 of 528,000 clashes at 300k.

## The change

### 1. A grid level per axis (`_candidate_pairs`)

‼️ One coarser cubic grid does not fix the box tests. A 50 m stringer needs a 50 m cell, every rivet in the barrel
shares that cell with all 80 stringers, and that is 80 million tests at 1M. Long parts are long on ONE axis.

So each box gets a level per axis. Level l's cells are 4^l finest cells long on that axis, and a box's level is the
first whose cells it crosses at most five of. The finest cell is still the median box's longest side (14 mm, a
rivet). Some levels on the barrel:

- a rivet is (0, 0, 0);
- a stringer at 100 bays is (5, 0, 0), cells 14 m along the barrel and 14 mm across it;
- a skin panel is (2, 0, 1) or near it, depending on its turn round the barrel.

Boxes with the same levels form a group. Group (0, 0, 0) is V1's grid, box for box: the same tests, counted the same
way.

A pair from groups A and B is tested in the grid whose level on each axis is the larger of theirs; both boxes cross
at most five of its cells a side. Each pair of groups is one pass: the smaller group is filed, and the other looks up
the cells it crosses. A pair belongs to exactly one pass.

Cell indices are integers, computed once at the finest level. A coarser index is that integer shifted right by 2l,
which is exact: floor(floor(x/c)/4^l) = floor(x/(c·4^l)). A pair is tested only in its home cell, the larger of its
two low-corner indices shifted. That cell is in both boxes' ranges whenever they overlap (floor is monotone), and it
is one cell, so a pair is tested and counted once. Pairs are still sorted into index order, so a truncated answer is
the truncated answer every-pair gives.

Nothing the Go side reads was renamed. `interference_box_tests` still counts box-versus-box comparisons; cell lookups
are not counted, as they were not before. Cost is a few lookups per box per group it meets. The barrel has fewer than
20 groups. A model with hundreds of distinct anisotropic size classes would pay more passes, and that was not
measured.

### 2. A clash inside a box is the same clash wherever it slides (`_INTERFERENCE_SLIDE`)

‼️ Keying a rivet's pose modulo the stringer's translation is wrong: a stringer is finite, and a rivet over its end
shares less. What is provable is narrower.

- **Inside the frame's box.** A box is the intersection of three slabs, one per local axis. If solid P lies wholly
  inside the box's slab on axis u, then P ∩ box = P ∩ (the other two slabs), and those do not change when P moves
  along u. So among placements that differ only along u, **and are all inside that slab**, the common volume is one
  number. Marked `inf` in the key.
- **Carried from the other box.** If the frame's box lies wholly inside the other box's slab on the other's own axis
  v, moving the other along v changes nothing. When v lies along the frame's axis w, that is a move along w, marked
  `-inf`. "Lies along" means the rotation entry is ±1 and the rest of that column is 0, to the 1e-9 the pose is
  compared to. Without this, a skin panel keyed the barrel-long stringer by where along it the panel sat: one boolean
  a bay again.
- **Why marks combine.** Each containment is an interval on ONE translation component in the frame. Two placements
  with equal keys differ only in marked components, each inside its interval in both. Intervals are convex, so moving
  one component at a time from one placement to the other stays inside every interval, and no move changes the
  volume. The sign records which containment marked a component, so both placements slide for the same reason.

Containment is shown with the other solid's own bounding box, which is never smaller than the solid, plus a 1e-4 mm
margin. So a pose near an end keeps its full key and is measured. A box's slabs come from its dims, and are trusted
only when its measured bounds agree with them to a micron. Of the two frames, the one that marks more translations
keys the pair, so a leaning rivet is keyed in its stringer's frame.

**Boxes only.** A cylinder along its axis and an extrusion along its depth are prisms too. They are not used: nothing
measured needs them, and each would need its own proof and fence.

The rotation part of the key is compared to 1e-9, as V1 already did, and a slide multiplies a 1e-9 rotation
difference by the slide length (1e-4 mm over 100 m). That is the same class of approximation V1's pose rounding
already made, not a new one.

## Truncation, end to end

Where a truncated check is reported, checked against the code on this branch:

| path | carries truncation? | evidence |
|---|---|---|
| kernel reply | `interferences_truncated`, and pairs, booleans, reused | `sidecar.py` `_build` |
| `cad.Build` | `InterferencesTruncated`, `InterferencePairs`/`Booleans`/`Reused` | `internal/domain/cad/cad.go` |
| agent turn | "FORGE checked X of Y pairs … not known to be clear" | `internal/agent/interference.go` `coverageNote`; fence `TestInterference_ATruncatedCheckSaysSoInTheTurn` (V2) |
| render sheet | `Truncated`, `Checked`, `Pairs`, `Skipped` | `internal/agent/render.go`, `internal/httpapi/scriptrunner.go`; fence `TestRender_CarriesHowMuchTheCheckCovered` |
| car measurement tests | `truncated=` printed | `car_tree_measure_test.go`, `car_ceiling_live_test.go` |
| mesh endpoint | carries **no** interference, so nothing to read as clean | `internal/httpapi/geometry.go` (parts, definitions, instances, skipped) |
| STEP export | carries no interference; its label says nothing was analysed or checked | `internal/httpapi/geometry.go` `exportParametric` |
| **#89's `measure.py`** | **did not.** The console line printed no interference answer, and `mesh`/`step` rows recorded `interferences_truncated: false, found: 0` for a check that never ran | closed here: every run prints `interference: [TRUNCATED — ]X of Y pairs checked, …`, `NOT CHECKED` or `NO ANSWER`, and rows carry `interference_checked` |

No product path needed a change.

## Method

1. **Requests.** #89's `TestScaleUp_MeasureAirframeBarrel` wrote `barrel-<bays>.json` for 1, 9, 30 and 100 bays
   (10,240, 90,880, 302,560 and 1,008,160 occurrences), unchanged.
2. **Before, same day.** #89's code, with the sidecar copied aside before any edit, through #89's `measure.py` in
   `full` mode at 10k and 90k (`data/before-results.jsonl`). That puts a 90k comparison on the same machine the same
   afternoon, not only against #89's rows. 300k and 1M "before" are #89's rows.
3. **After.** The committed sidecar, copied to a frozen directory before the runs (sha1 checked equal) so that test
   edits during the runs could not reach the kernel. It ran through `measure.py` as amended here, `full` mode, 2 runs
   a size, 40-minute cap, 24 GB RSS cap (`data/results.jsonl`, `data/run.log`). Each row carries the system CPU over
   the run and the other go/node/python processes present. A separate sampler logged CPU load once a minute
   (`data/load.log`).
4. **Probe.** The level-per-axis index with the slide not yet carried, at 10k, 90k and 300k
   (`data/probe-results.jsonl`). It is the only 300k run of the index under moderate load.
5. **Where the booleans go.** A scratch script (not committed) wrapped `_pair_key` and counted the distinct keys that
   paid for a boolean, by shape pair and by which translations were marked, at 1 and 9 bays with sliding on and off
   (`data/booleans-by-key.txt`).

Environment as #89: Intel Core i7-12650H (16 logical processors), 64 GB, Windows 11, Python 3.13.9, build123d 0.11.1,
cadquery-ocp-novtk 7.9.3.1.1, Go 1.26.5.

‼️ **The laptop was shared** with other agents' Go and node tests, a browser session and live model runs. This
worktree's own kernel fences and one drill run also overlapped the 90k runs. Where two runs differ, the faster is the
estimate. That is an inference, not an idle-machine measurement.

## Results

### `full`: `_build` as shipped, before and after

| occurrences | code | run | build s | shapes | assembly | interference | box tests | booleans | answer | peak GB | CPU |
|---:|---|---:|---:|---:|---:|---:|---:|---:|---|---:|---:|
| 10,240 | #89 (today) | 1 | 6.2 | 2.2 | 1.5 | 2.4 | 3,242,256 | 234 | 17,600 of 17,600 | 0.44 | 20% |
| 10,240 | probe | 1 | 5.2 | 2.3 | 1.6 | 1.3 | 39,056 | 14 | 17,600 of 17,600 | 0.44 | 33% |
| 90,880 | #89 | 1–2 | 202–239 | 78–85 | 42–44 | 74–115 | 144,288,640 | 1,050 | 158,400 of 158,400 | 0.84 | 16–18% |
| 90,880 | #89 (today) | 1 | 71.4 | 20.3 | 14.0 | 36.6 | 144,288,640 | 1,050 | 158,400 of 158,400 | 0.84 | 18% |
| 90,880 | probe | 1 | 46.6 | 21.7 | 14.7 | 9.8 | 425,480 | 30 | 158,400 of 158,400 | 0.84 | 22% |
| 90,880 | **this** | 1 | 75.3 | 35.0 | 23.7 | 16.0 | 425,480 | 15 | 158,400 of 158,400 | 0.84 | 61% |
| 90,880 | **this** | 2 | 68.5 | 34.3 | 20.0 | 13.6 | 425,480 | 15 | same | 0.84 | 56% |
| 302,560 | #89 | 1 | 450.5 | 81.5 | 52.9 | 314.5 | 1.49 B | 2,000 | **truncated**: 316,976 of 528,000 | 1.83 | 22% |
| 302,560 | probe | 1 | 175.3 | 82.7 | 57.8 | 33.2 | 1,432,752 | 72 | 528,000 of 528,000 | 1.89 | 32% |
| 302,560 | **this** | 1 | 242.4 | 108.8 | 75.6 | 56.1 | 1,432,752 | 15 | 528,000 of 528,000 | 1.89 | 59% |
| 302,560 | **this** | 2 | 229.5 | 103.7 | 69.9 | 54.0 | 1,432,752 | 15 | same | 1.88 | 56% |
| 1,008,160 | #89 | 1 | **stopped at 2,400** | — | — | — | (16.2 B priced) | — | none | 5.17 polled | 21% |
| 1,008,160 | **this** | 1 | 687.8 | 363.4 | 197.3 | 121.5 | 4,812,088 | 15 | 1,760,000 of 1,760,000 | 5.45 | 46% |
| 1,008,160 | **this** | 2 | **515.4** | 234.2 | 161.3 | 115.1 | 4,812,088 | 15 | same | 5.44 | 17% |

"Answer" is found of expected clashes; every run's pairs checked equalled its pairs (booleans + reused = pairs). Pairs
were 197,356, 657,508 and 2,191,348: boxes that overlap without sharing material (a rivet's box and its neighbour's)
are pairs too. The sidecar parsed the 289 MB request in 3.5–5.1 s at 1M, outside the build times.

### What grows

| step | box tests k | interference s (best) | build s (best) | peak GB k |
|---|---:|---|---|---:|
| 90k → 300k | 1.01 | 13.6 → 54.0 (56% and 56% CPU) | 68.5 → 229.5 | 0.67 |
| 300k → 1M | 1.01 | 54.0 → 115.1 (56% and 17% CPU): k = 0.63; from the 32% probe, k = 1.03 | 229.5 → 515.4 | 0.88 |

‼️ Box tests are the load-free count, and they are linear. The seconds are not a clean curve: the 300k and 1M best
runs were at 56% and 17% system CPU. At 1M, shapes (234 s) and assembly (161 s) are 77% of the best build. The check is
22%.

### Where the 15 booleans go

From `data/booleans-by-key.txt`: distinct keys that paid for a boolean at 9 bays, final code. "Marked" is which
translations the key leaves free: inside the frame's box (+inf) or carried from the other box (−inf).

| shape pair (keyed in the frame of the first) | slide on | slide off | marked, and why the count does not grow |
|---|---:|---:|---|
| rivet, stringer | 3 | 900 | along the barrel (carried). One for the stringer rivets, which are inside the stringer's depth too; one for the frame rivets each side of it |
| skin panel, rivet | 1 | 124 | along and round the barrel: every rivet is inside the panel's length and width |
| frame segment, stringer | 3 | 10 | round the barrel. Two at the end stations, where the stringer ends; one for every interior station, carried along |
| skin panel, stringer | 1 | 9 | round the barrel, and along it (carried) |
| rivet, rivet | 4 | 4 | nothing (a cylinder does not slide): neighbouring rivets whose boxes overlap, the same 4 poses at every size |
| skin panel, frame segment | 2 | 2 | round the barrel; the stations at each end of a panel |
| frame segment, frame segment | 1 | 1 | nothing: neighbours in a ring |
| **total** | **15** | **1,050** | |

At 1 bay it is 14 with sliding on (one fewer frame–stringer key, as both stations are ends) and 234 off. The stringer–
rivet row is #89's "about 96 a bay": 100 keys a bay, 96 stringer rivets and 4 frame rivets beside the stringer.
Measured at 1 and 9 bays; that 30 and 100 bays are also 15 is the `results.jsonl` count, not this breakdown.

## Fences and drills

`go test ./internal/domain/cad -run TestKernel_` with `FORGE_CAD_PYTHON` set. New, in
`interference_large_box_kernel_test.go`:

| fence | holds | measured |
|---|---|---|
| `TestKernel_BoxTestsGrowLinearlyWhenTheLongPartsGrowWithTheModel` | a deck whose panels and lane-long rails grow with its bays: 4× the bays is at most 6× the box tests, and at most 2 tests a part | 12 bays, 820 parts: 648 tests; 48 bays, 3,268 parts: 2,604 (0.8 a part both). Testing the large boxes against every box would be ~41,000 → ~620,000 |
| `TestKernel_LongBoxesAreFoundAsEveryPairFindsThem` | fixture `long` in `testdata/interference_all_pairs.py` (rails along each axis, turned rails, panels, a stringer with rivets and one over its end, large boxes face to face, a box around everything): the same pairs as every pair, in order, none twice, and the same interferences. Also three box-only sets of 1,500 boxes with sizes spread over four decades per axis | 165 parts, 309 box tests (every pair 13,530), 220 interferences; box-only sets 63,822–96,177 tests of 1,248,990 |
| `TestKernel_PinsAlongARailPayForOneBooleanAndTheEndsAreMeasured` | pins through a rail, crossbars through a second (the carried case), pins leaning 35° in a third, and a pin or bar half over each end: all 384 found, at most 6 booleans, and every end clash has its own, smaller volume | 6 booleans, 378 reused |

Extended:

- `scatter` and `dense` now also compare the candidate pair list itself with every pair (`compareWithEveryPair`).
- `testdata/interference_cache.py` gained pins and crossbars along straight and turned rails, pins over an end and
  standing higher, and crossbars turned a quarter about x standing partly off the rail by different amounts. A slide
  carried to the wrong axis gives those crossbars one key.

Changed: `TestKernel_RepeatedClashesPayForOneBooleanEachPose` expected 2 booleans for pins left and right of a plate's
centre. Both lie inside the plate's width and depth, so it is 1 now, and the comment says why.

Drills (`scripts/drill-fences.sh`, section "The interference check's large boxes"), each red:

| drill | red fence |
|---|---|
| a long box is tested against every box of another size again | BoxTestsGrowLinearly…: 36,456 → 546,732 tests (44.5 → 167.3 a part) |
| one level for all three axes, a cubic cell as long as the longest side | BoxTestsGrowLinearly…: 5.1 a part |
| a pair from two groups is tested where it does not begin | LongBoxes…: 32 of 241 pairs |
| a box is filed without the last cell it reaches | LongBoxes…: 218 of 241 pairs |
| a clash is slid along a box it is not inside | PinsAlongARail…: an end pin reused the inside volume |
| containment is checked at one end of the box only | PinsAlongARail…: the right-end pin reused it |
| a pin along a rail is measured at every pose again | PinsAlongARail…: booleans not bounded |
| a slide seen only from the other box is not carried | PinsAlongARail…: 99 booleans |
| a carried slide ignores a rotation that does not line the axes up | AReusedClash…: a turned crossbar reused a wrong volume (1,600 for 2,200 mm³) |
| a clash is keyed in the frame that slides less | PinsAlongARail…: the leaning pins paid a boolean each |

Two older drills' anchors moved with the code (the home-cell rule and the large-box loop) and were re-pointed. With
the rest of the K2b and V1 sections, 19 of 19 are red.

‼️ The frame-choice drill stayed green on the first run. Once slides are carried, both frames mark the same
translations whenever the axes line up, so only a solid whose axes do not line up with the box's (the leaning pins)
tells the frames apart. The girder of leaning pins was added for that.

## What this establishes

- On this barrel, V1's check is linear in box tests from 90k to 1M. The shipped `full` build of 1,008,160 occurrences
  finishes in 515–688 s at 5.44 GB with every clash found.
- The pairs the index returns are exactly every pair's, in order, once. This is fenced on solids with long boxes and
  on 4,500 synthetic anisotropic boxes.
- For untouched copies of box primitives, a clash's volume is reused along the axes the key marks, and nowhere else.
  The ends and partly-outside poses are measured, as the fences show.
- The barrel needs 15 booleans at every size measured, so the 2,000 budget is no longer what limits it.
- Truncation reaches every product reader that shows interference; the measurement script now says it too.

## What this does NOT establish

- **A real airframe or car.** Four definitions, all boxes and one cylinder, on a regular lattice: the best case for
  both the level groups and the slide. A model with many size classes, or clashes against cylinders and extrusions
  (which do not slide), was not measured.
- **The reply at 1M.** The interference list now has 1.76 M entries. Its JSON size, the Go decode of it and what an
  agent turn does with it were not measured. #89's Go numbers did not include it, because the check never finished.
- **Timing variance.** Two runs a size at 17–61% system CPU on a shared laptop. The seconds are estimates; the box
  tests and booleans are counts.
- **Memory on the cluster.** One Python process's peak working set on Windows, not the arm64 Linux image.
- **STEP, mesh and properties at 1M.** Not re-run here (STEP stays refused at 1M); #89's numbers stand.
- **Browser draw.** Still not in this branch.

## Recommendations (nothing here is changed)

1. **Keep the 2,000-boolean budget.** The barrel now needs 15, and nothing here measures what a boolean costs at scale,
   so there is no basis for moving it either way.
2. **Keep `FORGE_GEOMETRY_MAX_OCCURRENCES` at 100,000 and the 4,096 build and draw ceiling.** A 1M build is no longer
   stopped, but at 515 s it is not interactive, and browser draw is unmeasured.
3. **Measure the interference reply at 1M before any product path is sent one.** The kernel already knows those
   1.76 M clashes are 15 distinct answers, so a summary (per answer, a count and an example) is likely far smaller.
   That is an inference, not a measurement.
4. **The next wall is shapes and assembly, 77% of the 1M build:** a placement per occurrence and one compound, both in
   Python. #89 recommended the same.
5. **Extend the slide to cylinders or extrusions only with a measured need**, each with its own proof, fence and drill.

## Re-running

```bash
export GOWORK=off D=/some/dir
FORGE_SCALE_MEASURE_OUT=$D FORGE_SCALE_MEASURE_BAYS=9,30,100 go test -count=1 -timeout 30m \
  -run TestScaleUp_MeasureAirframeBarrel ./internal/domain/geometry
.cadvenv/bin/python docs/spikes/2026-09-15-one-million-occurrences/measure.py --dir $D --bays 9,30,100 --modes full --repeat 2 --cap 2400
```

For the "before" rows, run the same `measure.py` against `internal/domain/cad/sidecar.py` as it was at #89's head
(`git show origin/scale/one-million:internal/domain/cad/sidecar.py`).
