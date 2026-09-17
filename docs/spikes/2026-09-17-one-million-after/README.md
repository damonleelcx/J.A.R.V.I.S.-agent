# Measurement: the kernel at 1,000,000 occurrences after #121, #126 and #128

**Date:** 2026-09-17 · **Status:** done, one shared laptop · **Base:** `origin/main` 81c6263 (sidecar sha1 `afed90a`)
· **Follows:** [`2026-09-16-interference-approach`](../2026-09-16-interference-approach/README.md) (#128),
[`2026-09-15-measured-after`](../2026-09-15-measured-after/README.md) (#125),
[`2026-09-15-last-hot-spots`](../2026-09-15-last-hot-spots/README.md) (#121),
[`2026-09-15-next-scale-walls`](../2026-09-15-next-scale-walls/README.md) (#113),
[`2026-09-15-one-million-occurrences`](../2026-09-15-one-million-occurrences/README.md) (#89)

## Summary

- **The 1,008,160-occurrence barrel builds with its full interference check in 93.1–111.3 s on main** (two
  interleaved runs at 41–61% host CPU; two earlier runs were 103.8 s and 259.5 s), finding all 1,760,000 clashes
  with 15 booleans, at **5.90 GB peak RSS**. The mesh path (#89's `mesh` mode: mesh + part properties, check replaced
  by the grid) is 151.3 / 161.2 s at 6.12 GB. Host memory never passed 51%; no run was stopped by the watchdog.
- **Everything is linear from 302,560 to 1,008,160:** the full build k = 0.94 (30.1 → 93.1 s, fastest each), peak RSS
  k = 0.89 (2.02 → 5.90 GB).
- **The 1M interference reply is bounded and cheap:** 2,231,837 bytes, Go decode 24.7 ms, 4.75 MiB allocated, 2.64
  MiB held, sidecar encode 0.03 s. Already done on main by #113 (list bounded to the worst 10,000 with exact counts)
  — nothing to change.
- **Cylinders and extrusions keep their reuse when placed by patterns, at every size:** a yard of shafts, girders,
  collars, cleats and leaning pins placed only by linear, polar and grid patterns, 3,104 → 310,400 occurrences, pays
  **11 booleans at every size**, finds exactly the expected 3,088 → 308,800 clashes, and the check is linear (k =
  1.02). The array narrow phase answers byte-identically to the per-pair loop at all four sizes. Without the slide
  (switch off, budget lifted) the smallest yard pays 390 booleans and 13× the time. Already done on main by #113 —
  nothing to extend.
- **STEP is linear now:** export 8.1–8.7 s at 90,880 and 25.0–25.8 s at 302,560, k = 0.92 (#89 measured k ≈ 2.2);
  66.1 MB / 225.2 MB files, 1.35 / 3.55 GB peak. 1M stays refused, as decided.
- **One change, behind a switch: a build runs with Python's cycle collector paused** (`_BUILD_WITHOUT_GC`). Profiled at
  90,880 and 302,560, the collector ran 500 / 1,665 times during a build and took 0.91 s / 3.23 s of it, and
  `gc.collect()` after a paused build finds **0** unreachable objects. Interleaved before/after, full build:
  **1,008,160: ×0.85 and ×0.88** (111.3 → 94.6 s, 93.1 → 81.5 s); 302,560 ×0.94 / ×0.95; 90,880 ×0.91 / ×1.00.
  The shapes phase is ×0.80–0.91 in every pair and `features` ×0.29–0.51. Same found, listed, booleans, reuses, pairs,
  box tests and volume in every pair, peak RSS within 1 MB.
- **The full `internal/domain/cad` package completed** on 93d84ce (the code commit; this README and the log are the
  only later changes) with `-timeout 30m`, in 1,048 s: 121 PASS, 12 SKIP, **11 FAIL, all the known Windows script-runner
  class** — every one fails before or inside `RunScript` with "Python was not found" (the Microsoft Store stub; the
  runner looks for `.cadvenv/bin/python`) or with a scripted part missing for that reason: 8 `TestScript_*`,
  `TestKernel_ARepeatedScriptedPartRunsItsScriptOnce`, `TestKernel_ARepeatedScriptedPartIsBuiltEveryTime`,
  `TestKernel_AScriptedPartIsExportedAndMeshed`. `data/cad-package.log`.

**No limit is changed.** Nothing here argues for raising the 4,096 build ceiling or the 100k storage door (a 1M
build is still 80–110 s and 6 GB against the shipped 30 s view kernel timeout and 1–2 GiB pods), and STEP stays refused at 1M (by
linear extrapolation about 85 s of export and ~10 GB, which no pod has).

## ‼️ What the machine was doing

The laptop was shared with other agents throughout. Host CPU over each run is in every row (`host_cpu_mean_pct`,
sampled every 5 s) with the five busiest processes at its start (`busiest_at_start`). Two examples of how much this
matters, both kept:

- The first 1M `full` run on main took **259.5 s** (shapes 131.1 s, check 90.1 s) at 36% mean host CPU; the next,
  eight minutes later, **103.8 s** (shapes 45.6 s) at 58%. Same sidecar, same request, same answer and the same peak
  RSS to the megabyte. Host CPU% does not capture it (thermal state, memory bandwidth, other kernels do).
- This session's 90,880 shapes phase ran 7.1–8.8 s in the first set and 3.0–3.6 s an hour later.

So **improvements are claimed only from interleaved before/after pairs** (B A B A, same size back to back), and
absolute seconds are quoted with their load.

## 1. 1M after-timings, on main

[`measure_bounded.py`](measure_bounded.py) runs #89's `measure.py --child` unchanged, so phases and answers are read
exactly as every earlier spike read them, and adds: a hard cap per run (3,600 s), a **host memory watchdog** that
kills the run past 80% of RAM (64 GB machine), a tree-RSS cap of 24 GB, a refusal to start with less than 16 GB
available (49.7 GB was free at the start), load sampling, and runs interleaved by mode. **No profiler at 1M.** The
sidecar measured is a frozen copy of main's (`sidecar-main-81c6263.py`, sha1 `afed90a`).

`full` = `_build` as shipped (format `""`): shapes, features, assembly, interference. `mesh` = #89's mode: mesh +
part properties, the check replaced by the grid.

### `full`

| occurrences | build s | shapes | features | assembly | interference | peak GB | host CPU | answer |
|---:|---:|---:|---:|---:|---:|---:|---:|---|
| 90,880 | 19.0 | 7.1 | 0.4 | 1.4 | 9.5 | 0.88 | 23% | 158,400 found, 10,000 listed, 15 booleans |
| 90,880 | 15.0 | 7.3 | 0.4 | 1.5 | 5.5 | 0.89 | 15% | same |
| 90,880 † | 8.6 | 3.5 | 0.4 | 0.9 | 3.6 | 0.89 | 46% | same |
| 90,880 † | 8.6 | 3.6 | 0.5 | 0.9 | 3.4 | 0.89 | 48% | same |
| 302,560 | 50.2 | 24.3 | 1.3 | 5.1 | 18.4 | 2.02 | 26% | 528,000 found, 15 booleans |
| 302,560 | 50.6 | 25.1 | 1.3 | 4.7 | 18.4 | 2.02 | 29% | same |
| 302,560 † | 30.1 | 12.4 | 1.3 | 2.8 | 12.8 | 2.02 | 52% | same |
| 302,560 † | 32.1 | 14.1 | 1.5 | 3.0 | 12.7 | 2.02 | 61% | same |
| **1,008,160** | 259.5 | 131.1 | 9.4 | 23.9 | 90.1 | 5.90 | 36% | **1,760,000 found, 15 booleans, 2,191,348 pairs, not truncated** |
| **1,008,160** | 103.8 | 45.6 | 4.6 | 8.9 | 41.1 | 5.90 | 58% | same |
| **1,008,160** † | 111.3 | 47.7 | 4.3 | 10.6 | 45.7 | 5.90 | 61% | same |
| **1,008,160** † | **93.1** | 36.3 | 4.5 | 8.6 | 40.9 | 5.90 | 41% | same |

† the "before" runs of section 5's interleaved pairs, same frozen main sidecar. `data/results-main.jsonl`,
`data/results-gc.jsonl`. Every 1M run: volume 3,643,661,325.509079 mm³, 4,812,088 box tests, 2,191,333 reused.

**Against the recorded 1M builds** (one run each unless stated, load differs, so read as a trajectory not a ratio):
#89 stopped at 2,400 s; #94 515–688 s; #113 174 s; #114 102.0 s (check 52.8 s); #125 1,208 / 242.8 s under starvation;
**now 93.1 s at best (check 40.9 s), 81.5 s with section 5's change.** At the fastest main run the build is shapes
39%, interference 44%, assembly 9%, features 5%.

### `mesh` (mesh + part properties, check = grid only)

| occurrences | build s | shapes | assembly | grid | properties | mesh | peak GB | host CPU | reply MB | mesh payload MB |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 90,880 | 32.6 | 8.8 | 2.0 | 5.2 | 11.2 | 4.3 | 0.91 | 23% | 50.3 | 28.1 |
| 90,880 | 29.9 | 7.4 | 1.5 | 2.9 | 10.9 | 6.5 | 0.91 | 27% | | |
| 302,560 | 81.4 | 28.6 | 4.7 | 9.3 | 26.3 | 10.4 | 2.10 | 28% | 168.6 | 94.1 |
| 302,560 | 86.4 | 26.3 | 4.8 | 14.0 | 27.9 | 11.0 | 2.10 | 35% | | |
| **1,008,160** | **151.3** | 43.3 | 10.1 | 18.6 | 49.7 | 22.2 | **6.12** | 55% | 564.6 | 314.6 |
| **1,008,160** | 161.2 | 45.2 | 11.1 | 24.3 | 50.0 | 23.2 | 6.12 | 59% | | |

#89's 1M mesh path was 480 s; #113 recorded 111 s. **Part properties are now the largest phase of this mode (50 s at
1M)**: `_properties` reads each occurrence's box from the placed solid itself (`_box_of`, one OCCT bounding box per
occurrence — deliberately, see its comment: a turned box's moved box is looser than its envelope). They are asked for
only by `BuildProperties` (mass roll-up), not by the viewport mesh. Not changed here; noted as the next thing on that
path. The mesh reply carries the 1M `part_properties` list, hence 565 MB.

### Go at 1M (`TestScaleUp_MeasureAirframeBarrel`, `data/go-*.json`)

| occurrences | expand ms (alloc MiB) | mass roll-up ms | mesh endpoint: decode / encode ms, bytes |
|---:|---:|---:|---:|
| 90,880 | 247–266 (267) | 278 | 391 / 103, 24.2 MB |
| 302,560 | 845–914 (980) | 946–966 | 1,230 / 349, 81.0 MB |
| 1,008,160 | 3,148–3,243 (3,155) | 3,300–3,646 | 4,653 / 1,189, 271.0 MB |

The storage door refuses 302,560 and 1,008,160 by name (100,000 limit), as before.

## 2. STEP after #106, #114 and #121

`step` mode (#89's: format `step`, check replaced by the grid), 90,880 and 302,560, interleaved with section 5's change
(the change does not touch the export). `data/results-step.jsonl`.

| occurrences | sidecar | build s | shapes | export (transfer) | file MB | peak GB | host CPU |
|---:|---|---:|---:|---:|---:|---:|---:|
| 90,880 | main | 15.1 | 3.3 | 8.7 (3.3) | 66.09 | 1.346 | 37% |
| 90,880 | this branch | 15.2 | 2.8 | 9.2 (3.6) | 66.09 | 1.346 | 36% |
| 90,880 | main | 13.7 | 2.9 | 8.1 (3.2) | 66.09 | 1.346 | 27% |
| 90,880 | this branch | 13.5 | 2.4 | 8.8 (3.2) | 66.09 | 1.346 | 30% |
| 302,560 | main | 44.1 | 10.1 | 25.0 (10.5) | 225.24 | 3.549 | 29% |
| 302,560 | this branch | 42.7 | 8.2 | 26.2 (11.2) | 225.24 | 3.548 | 30% |
| 302,560 | main | 45.7 | 10.2 | 25.8 (10.2) | 225.24 | 3.547 | 34% |
| 302,560 | this branch | 39.8 | 7.8 | 23.9 (10.1) | 225.24 | 3.550 | 24% |

- **Export is linear: k = 0.92** between the sizes (main, mean of two). #89 measured k ≈ 2.2 (307 s at 300k under
  heavy load); #125 6.8 s / 31.8 s (k ≈ 1.28, one run at 300k). Memory k = 0.80.
- ‼️ **The files are 6% larger than #125's** (62,292,213 → 66,086,244 and 212,395,320 → 225,236,274 bytes) with the
  same generator. Not investigated; recorded so it is not mistaken for a change here (this branch writes the same
  byte count as main).
- **STEP at 1M stays refused.** Extrapolated linearly: ~85 s export and ~10 GB peak; no pod has that.

## 3. The 1M interference reply

`TestScaleUp_MeasureTheInterferenceReply` on the replies `measure.py --write-reply` wrote (`data/go-reply-*.json`):

| occurrences | bytes | found / listed | Go decode | decode alloc | held after decode | problem lines |
|---:|---:|---:|---:|---:|---:|---:|
| 90,880 | 2,231,833 | 158,400 / 10,000 | 22.5 ms | 4.71 MiB | 2.65 MiB | 1,955,599 B in 4.4 ms |
| 1,008,160 | **2,231,837** | 1,760,000 / 10,000 | **24.7 ms** | 4.75 MiB | 2.64 MiB | 1,955,599 B in 4.5 ms |

The reply is the same size at every size: #113 bounded the list to the worst 10,000 with exact `found`, `buried`
and a `summarized` flag, and #114 bounded the repair prompt. **#94's open item is closed on main**; nothing here
changes it. The reply is 32% larger than #113 measured (1,686,205 B) because each entry now carries `a_label` and
`b_label` (the placement names); still bounded, still 25 ms.

## 4. Cylinders and extrusions placed by patterns

#94 measured reuse only on a box lattice. #113 extended the slide to a cylinder's length and an extrusion's depth and
fenced it on one 386-part drive (`TestKernel_CollarsAlongAShaftAndCleatsAlongAGirderPayForOneBooleanEach`). This
measures it at scale, placed only by patterns.

**The fixture** (`internal/domain/geometry/prism_yard_scale_test.go`, `prismYard`): that fence's drive — a 4 m cylinder
shaft with 191 collars by a linear pattern and one collar half over each end, a 4 m extruded L girder with 96 cleats
by a linear pattern and one half over each end, 95 pins leaning 35° by a linear pattern — as a sub-assembly; a ring of
8 drives by a polar pattern about y, 8 m out, so the drive sits at eight rotations; a side × side grid pattern of
rings 24 m apart. 388 occurrences and 386 clashes a drive. Requests come from the real expansion
(`solidsAndOperations`), and `TestPrismYard_PlacesItsPrismsByPatternsNotByListing` holds the generator (no enumerated
repetition, 32 extrusions, 3,072 cylinders, 9,312 boxes at 2 × 2).

**The check** ([`yard_check.py`](yard_check.py)): `_build` with the check's arguments captured, then the check on
those solids — warm (discarded, #128's cold-boolean effect), shipped (array narrow phase), per-pair loop, shipped again.

| occurrences | pairs | found (expected) | booleans | reused | shipped s | loop s | shipped == loop | host CPU |
|---:|---:|---:|---:|---:|---:|---:|---|---:|
| 3,104 | 4,614 | 3,088 (3,088) | **11** | 4,603 | 0.13 / 0.13 | 0.16 | identical | 48% |
| 27,936 | 42,231 | 27,792 (27,792) | **11** | 42,220 | 0.72 / 0.73 | 1.33 | identical | 17% |
| 111,744 | 167,412 | 111,168 (111,168) | **11** | 167,401 | 3.07 / 3.10 | 4.87 | identical | 21% |
| 310,400 | 464,224 | 308,800 (308,800) | **11** | 464,213 | 8.37 / 8.62 | 13.92 | identical | 19% |

- **Reuse holds: 11 booleans whatever the size**, not truncated, every expected clash found. The check is linear from
  27,936 to 310,400 (k = 1.02), and the array path is ×0.55–0.63 of the loop with the same bytes.
- **What the slide saves:** at 3,104 occurrences with `_INTERFERENCE_SLIDE` off and the boolean budget lifted, the
  check pays **390 booleans and 1.74 s** against 11 and 0.13 s — the pose cache still reuses across the eight turned
  copies of a drive, but not along a shaft or girder.
- ‼️ **A fixture error, caught by the counts:** the first run spaced rings 20 m apart and found 36 / 180 / 540 more
  clashes than expected at 3 × 3 / 6 × 6 / 10 × 10 — exactly 3 per pair of neighbouring rings, whose drives reach
  10.83 m from the ring centre. The generator was wrong, not the kernel; rings are now 24 m apart and every count
  matches. The first run is kept as `data/results-yard-20m.jsonl` (booleans were 13 there, same identity).

Nothing to extend, so no kernel change for this item.

## 5. Shapes + assembly: the change taken

### Where it goes now

[#121's `profile_shapes.py`](../2026-09-15-last-hot-spots/profile_shapes.py), unchanged, on the frozen main sidecar,
check replaced by a no-op (`data/profile-shapes.jsonl`, `data/profile-shapes-9.txt`, `-30.txt`):

| occurrences | build s | shapes | features | assembly | **cycle collector** | host CPU |
|---:|---:|---:|---:|---:|---:|---:|
| 90,880 | 5.01 | 3.55 | 0.46 | 0.74 | **0.91 s, 500 collections** | 52% |
| 302,560 | 19.71 | 14.22 | 1.40 | 3.20 | **3.23 s, 1,665 collections** | 63% |

Per call (instrumented, 90,880 / 302,560): `_placement` 27.1 / 20.6 µs, `_located` 19.5 / 13.9 µs, `_shape_key`
2.0 / 1.5 µs. cProfile at 90,880 by own time: `Vector.__init__` 0.92 s, `Vector.to_pnt` 0.67 s,
`BRepBndLib.AddOptimal_s` 0.51 s in 5 calls (the assembly's bounding box), `repr` 0.38 s (6 per occurrence),
`BRepTools.Clean_s` 0.22 s.

**The collector is a sixth of the build and finds nothing.** A build allocates a few objects per occurrence and keeps
all of them to its end, so every generation-2 pass traverses live objects. [`gc_probe.py`](gc_probe.py)
(`data/gc-probe.jsonl`, 90,880, one process each): with the collector paused the build is 4.38 / 4.57 s against
6.65 / 6.48 s, and `gc.collect()` straight after finds **0 unreachable objects** in every run, check or no check, with
the same peak RSS to 1 MB. The probe runs are single pairs and its ×0.68 is larger than the interleaved harness shows
below; the harness is the claim.

### The change

`sidecar.py`: `_build` pauses the cycle collector for the duration of one request when it was running, and restores it
in a `finally` (`_BUILD_WITHOUT_GC = True`; the body is now `_build_collected`). Reference counting frees everything as
before. What it gives up: a reference cycle made during a build would be collected after it, not during it; none of
the measured fixtures makes any.

### Measured, interleaved B A B A (`interleave.sh`, `data/results-gc.jsonl`)

| occurrences | pair | before build (shapes, features) | after build (shapes, features) | ratio | host CPU b / a |
|---:|---:|---:|---:|---:|---:|
| 90,880 | 1 | 8.6 (3.5, 0.4) | 7.8 (3.0, 0.1) | ×0.91 | 46 / 46% |
| 90,880 | 2 | 8.6 (3.6, 0.5) | 8.6 (3.0, 0.1) | ×1.00 | 48 / 54% |
| 302,560 | 1 | 30.1 (12.4, 1.3) | 28.2 (10.9, 0.6) | ×0.94 | 52 / 60% |
| 302,560 | 2 | 32.1 (14.1, 1.5) | 30.6 (11.9, 0.6) | ×0.95 | 61 / 66% |
| **1,008,160** | 1 | 111.3 (47.7, 4.3) | 94.6 (38.0, 2.2) | **×0.85** | 61 / 60% |
| **1,008,160** | 2 | 93.1 (36.3, 4.5) | **81.5** (33.2, 1.8) | **×0.88** | 41 / 39% |

Shapes ×0.80–0.91 in all six pairs; the interference phase does not move beyond noise (numpy-heavy, few Python
objects). **Identity:** in every pair found, listed, booleans (15), reused, pairs, box tests and volume are equal, and
peak RSS is within 1 MB (5.900–5.902 GB at 1M). STEP rows in section 2: shapes ×0.76–0.85, export unchanged.

### The fence and drills

`TestKernel_ABuildWithTheCycleCollectorPausedAnswersTheSameAndRestoresIt` (`testdata/build_without_gc.py`): #121's
placed-copies fixture at 8 copies (every placed shape kind, quarter and random turns, a feature cut) plus 40 pins through
a deck (clashes), an unbuildable part and a feature naming a missing part, built with the switch off and on in format
`""`, `mesh` + properties, `step` and the export job's `skip_interferences`. Replies are compared **whole** (less
`phases`, and STEP below its header), each request built once first so #128's cold-boolean bits are not what is
compared. It also requires: the collector ran 0 times during a paused build and more than 0 with it running (so the
count can see one), 0 unreachable objects after a paused build, the collector running after a build that returned
and after one that raised, and still paused after a build whose caller had paused it.

| drill | went red because |
|---|---|
| a build leaves the cycle collector running | collector ran 6 / 13 times during the "paused" builds |
| a build that raised leaves the collector paused | "still paused after a build raised" |
| a build turns on a collector its caller paused | "turned the cycle collector on when its caller had paused it" |
| a paused build answers differently (`parts=-1` on the paused path) | replies not identical |
| the prism yard lists its leaning pins (yard generator) | 9,472 solids, want 12,416 |

All five red, tree restored byte-identical (`data/drills.log`). ‼️ The yard drill goes red on the solid count, not on
`EnumeratedRepetition`; the listing half of that guard is asserted but not shown breakable by this drill.

### Not taken, priced

[`micro_placement.py`](micro_placement.py) (`data/micro-placement.json`, 24% host CPU, two passes):

- `Vector(*position).to_pnt()` 4.29–4.30 µs against `gp_Pnt(*position)` 0.82–0.90 µs, same bits;
- `downcast(Moved)` 4.59–4.67 µs against the definition's own cast 1.70–1.71 µs.

About 6.3 µs an occurrence, ~6 s at 1M (~8% of the 81.5 s build). Both look bit-identical by construction and the
placed-copies fence would see a wrong placement, but neither was measured end to end, so neither is claimed. The
assembly's bounding box (0.5 s at 90,880, one OCCT call over the compound) cannot be replaced by moved definition boxes
without changing `bounds`, so it is not a safe win.

## What this does NOT establish

- Browser draw at 1M, cluster node memory, or anything on Linux/arm64: laptop only.
- An idle-machine number: every row was contended; the 259.5 s run shows how far one can drift.
- That no future build makes reference cycles: the fence counts them on its fixtures, and a build that made cycles
  per occurrence would hold them until it returned.

## Reproduce

```sh
export GOWORK=off PYTHONUTF8=1
PY=C:/Users/damon/Downloads/agents/jarvis-k2b/.cadvenv/Scripts/python.exe   # build123d 0.11.1
D=<scratch dir>
FORGE_SCALE_MEASURE_OUT=$D FORGE_SCALE_MEASURE_BAYS=9,30,100 go test -count=1 -run TestScaleUp_MeasureAirframeBarrel ./internal/domain/geometry
FORGE_SCALE_MEASURE_OUT=$D FORGE_SCALE_YARD_SIDES=1,3,6,10 go test -count=1 -run TestScaleUp_MeasurePrismYard ./internal/domain/geometry
cp internal/domain/cad/sidecar.py $D/sidecar-after.py   # and main's as $D/sidecar-main-81c6263.py
$PY docs/spikes/2026-09-17-one-million-after/measure_bounded.py --dir $D --sidecar $D/sidecar-main-81c6263.py --bays 9,30,100 --modes full,mesh --repeat 2 --write-reply --results results-main.jsonl
FORGE_SCALE_MEASURE_REPLY=$D go test -count=1 -run TestScaleUp_MeasureTheInterferenceReply ./internal/domain/cad
FORGE_SCALE_MEASURE_NO_REQUESTS=1 FORGE_SCALE_MEASURE_OUT=$D FORGE_SCALE_MEASURE_BAYS=9,30,100 go test -count=1 -run TestScaleUp_MeasureAirframeBarrel ./internal/domain/geometry
bash docs/spikes/2026-09-17-one-million-after/interleave.sh $D sidecar-main-81c6263.py sidecar-after.py 9,30,100 full results-gc.jsonl
bash docs/spikes/2026-09-17-one-million-after/interleave.sh $D sidecar-main-81c6263.py sidecar-after.py 9,30 step results-step.jsonl
bash docs/spikes/2026-09-17-one-million-after/yard.sh $D sidecar-after.py results-yard.jsonl 1,3,6,10
$PY docs/spikes/2026-09-15-last-hot-spots/profile_shapes.py $D/sidecar-main-81c6263.py $D/barrel-9.json $D/prof 9
```

2026-09-17, Windows 11, Intel Core i7-12650H (16 logical processors), 64 GB, Python 3.13.9, build123d 0.11.1,
cadquery-ocp-novtk 7.9.3.1.1, Go 1.26.5.
