# Spike: STEP export past the build ceiling

**Date:** 2026-09-15 · **Status:** done · **Follows:** [K2's XDE export](../2026-09-14-xde-assembly-export/README.md) and
[the million-occurrence measurement](../2026-09-15-one-million-occurrences/README.md) (#89)

## Summary

- **The superlinear step is OCCT's `STEPCAFControl_Writer.Transfer`, and inside it the validation-property walk.** On
  the airframe barrel, with the sidecar's shipped writer settings, Transfer took **0.61–0.84 s at 10,240
  occurrences, 3.19–3.43 s at 30,400 and 19.7–20.7 s at 90,880** (k ≈ 1.65 from 30k to 90k). Everything FORGE does
  around it is linear: building the XCAF document (AddShape, AddComponent, names) took 0.15 → 0.46 → 1.3 s (k ≈ 0.99),
  and writing the file 0.27 → 0.73 → 2.3 s (k ≈ 1.05).
- **Cause: props mode, on by default.** OCCT 7.9's `WritePropsForLabel` visits an assembly's children as
  `for i = 1 .. NbChildren(): FindChild(i)`, and `NbChildren` and `FindChild` both walk the label's child list from
  its head, so N components under one assembly cost ~N² steps. It runs whether or not any label carries a property,
  and FORGE sets none. Switching props mode off alone: Transfer **0.28 s, 1.00 s, 3.32 s** (k ≈ 1.10). Every other
  writer mode switched off one at a time left it quadratic (17.9–18.2 s at 90k).
- **The fix is one writer setting** (`_STEP_WRITE_PROPS = False` → `writer.SetPropsMode(False)`), and the file does
  not change: at 10,240 barrel occurrences, written from two fresh processes, the files are byte-identical below the
  header; at 32,768 and 65,536 fence occurrences they are identical once instance ids are blanked and wrapped lines
  joined (next bullet); read back at 8,192, every instance, name and placement is there.
- **Found on the way, not changed:** the id string of each `NEXT_ASSEMBLY_USAGE_OCCURRENCE` is a counter OCCT keeps
  per PROCESS. Three exports of the same 32,768 occurrences in one process started at `'1'`, `'32769'` and `'65537'`.
  The sidecar is long-lived, so two exports of one design already differed in those strings before this change —
  and, because a longer id moves where the writer wraps a line, in 135,200 of 430,238 lines as text. With wrapped
  lines joined and the ids blanked, all three exports (props walk off, off, then on) matched in every one of 360,968
  lines.
- **Before and after through #89's `measure.py step`:** at **302,560 occurrences, export 307–391 s → 32–39 s**; at
  90,880, 21–32 s → 7.5–9.5 s. Same 212 MB and 62 MB files, same 3.4 GB and 1.3 GB peaks. From 90k to 300k export now
  grows **k ≈ 1.20** (was k ≈ 2.2). All runs on a shared laptop at 28–58% machine CPU, recorded per row.

## Why this spike

K2 moved STEP export to an XDE assembly and fenced it from 512 to 4,096 occurrences, where it was linear. #89 then
measured the barrel's export at 4.3 s (10k), ~21 s (90k) and 307–391 s (300k, 212 MB, 3.4 GB peak): k ≈ 2.2 from 90k
to 300k. The export is single-threaded, so load alone was an unlikely cause, but #89 had one pair of sizes and no
split of the phase. This splits it into its OCCT steps at several sizes, finds the step that grows, and tests
candidates rather than assuming one.

## Method

`profile_export.py <sidecar.py> <barrel-N.json> <out.jsonl> <variant,...>` runs the sidecar's `_build` on a barrel
request (format `""`, so no export, with the interference check replaced by a no-op that captures the kept solids and
their names), then, in the same process, exports those solids once per variant with the sidecar's own steps, each
timed:

- **document:** `TDocStd_Document`, `NewDocument`, `ShapeTool`, the root label;
- **the loop,** split per call and accumulated: `AddShape` of the unlocated shape, naming the definition label,
  `AddComponent` with the occurrence's location, naming the component; and the loop's time per tenth of the
  occurrences, so a per-call cost that grows shows as later tenths taking longer;
- `UpdateAssemblies`; writer setup (the sidecar's settings); `STEPCAFControl_Writer.Transfer`; `Write`; reading the
  file back and base64-encoding it (what `_build` returns).

Variants change one thing each: writer modes (`SetNameMode`, `SetColorMode`/`SetLayerMode`, `SetPropsMode`,
`SetSHUOMode`, `SetDimTolMode`, `SetMaterialMode`), the assembly's shape (**chunked**: the same components under
sub-assemblies of 4,096), and the writer (**plain_writer**: `STEPControl_Writer` on the compound `UpdateAssemblies`
built, assembly mode on, no XCAF layer). Requests are `TestScaleUp_MeasureAirframeBarrel`'s, at 1, 3 and 9 bays.

The OCCT source read for the cause is tag `V7_9_3` (`src/STEPCAFControl/STEPCAFControl_Writer.cxx`:
`transfer`, `writeValProps`, `WritePropsForLabel`; `src/XCAFDoc/XCAFDoc_ShapeTool.cxx`: `AddShape`, `AddComponent`,
`UpdateAssemblies`). The wheel is cadquery-ocp-novtk 7.9.3.1.1; the source was read, not stepped through.

py-spy was tried for a native profile of Transfer and refused: on Windows it cannot collect native stacks through a
venv launcher (`--subprocesses`). The variants above located the step instead.

Environment: Intel Core i7-12650H (16 logical processors), 64 GB, Windows 11, Python 3.13.9, build123d 0.11.1,
cadquery-ocp-novtk 7.9.3.1.1. ‼️ **The laptop was shared** with other agents' Go tests, live model runs and a kernel
build-ceiling measurement; every row carries the whole machine's CPU over that export ("CPU").

## Results: where the export's time goes

### Shipped settings, two runs a size (`data/profile-modes.jsonl`, variant `baseline`)

| occurrences | run | XCAF loop | UpdateAssemblies | **Transfer** | Write | read + base64 | total | CPU |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 10,240 | 1 | 0.17 | 0.017 | **0.84** | 0.36 | 0.75 | 2.15 | 74% |
| 10,240 | 2 | 0.15 | 0.014 | **0.61** | 0.27 | 0.97 | 2.02 | 50% |
| 30,400 | 1 | 0.46 | 0.050 | **3.19** | 0.73 | 0.94 | 5.40 | 50% |
| 30,400 | 2 | 0.49 | 0.052 | **3.43** | 0.82 | 1.89 | 6.72 | 56% |
| 90,880 | 1 | 1.36 | 0.149 | **20.69** | 2.31 | 0.94 | 25.52 | 51% |
| 90,880 | 2 | 1.25 | 0.115 | **19.72** | 2.32 | 0.96 | 24.43 | 42% |

Seconds. The loop's tenths were flat at every size (90k: first tenth 0.12 s, last 0.13–0.17 s), so no per-call cost
in FORGE's loop grows. "read + base64" varies 0.75–2.9 s at 10k with no trend in size; it is a 6.8 MB file read on a
machine running an antivirus, and is not what grows.

### Transfer by writer setting, one run each (`data/profile-writer-modes.jsonl`, `data/profile-modes.jsonl`)

A "no …" row is the shipped settings with that one mode off; "bare" is names, colours and layers off.

| writer | 10,240 | 30,400 | 90,880 | k, 30k→90k |
|---|---:|---:|---:|---:|
| shipped | 0.61–0.84 | 3.19–3.43 | 19.72–20.69 | 1.65 |
| no names | 0.57 | 3.13 | 18.16 | 1.60 |
| no colours, no layers | 0.55 | 3.10 | 20.35 | 1.72 |
| bare | 0.41–0.53 | 2.31–2.89 | 17.30–18.18 | 1.75 |
| no SHUO | 0.44 | 2.58 | 17.94 | 1.77 |
| no GD&T | 0.51 | 2.78 | — | |
| no material | 0.45 | 2.48 | — | |
| **no props** | **0.28** | **1.00** | **3.32** | **1.10** |
| every mode off | 0.23 | 0.74 | 2.26 | 1.02 |

CPU over these runs: 38–74% (profile-modes), 22–36% (writer modes). `SetVisualMaterialMode` and `SetMetadataMode`
are not in this OCP build; those two variants ran the shipped settings and are left out of the table.

### Transfer by assembly shape and writer, one run each (`data/profile-structure.jsonl`)

| export | 10,240 | 30,400 | 90,880 | k, 30k→90k |
|---|---:|---:|---:|---:|
| bare, one flat assembly | 0.44 | 2.33 | 17.30 | 1.83 |
| bare, sub-assemblies of 4,096 | 0.44 | 1.39 | 4.12 | 0.99 |
| `STEPControl_Writer`, no XCAF layer | 0.21 | 0.59 | 1.85 | 1.04 |

CPU 30–37%.

## How the conclusions follow

- **It is Transfer.** Of the 90k export's 25 s, Transfer is 20 s; the loop, `UpdateAssemblies`, `Write` and encoding
  together are ~5 s and grow k ≈ 1. From 30k to 90k (3×) Transfer grew 6.1×.
- **It is the XCAF writer, not the STEP actor under it.** `STEPControl_Writer` transfers the same compound, as an
  assembly, linearly (k ≈ 1.04).
- **It is quadratic in the components under ONE assembly.** The same components split into sub-assemblies of 4,096
  transfer linearly (k ≈ 0.99). That is what a walk of one label's children costs, and not what a cost per
  definition or per entity in the model would cost (the split changes neither).
- **It is props mode.** Of the modes, only props mode off makes it near-linear (k ≈ 1.10; every mode off, 1.02), and
  the source shows why: `WritePropsForLabel` recurses into every child of an assembly by index,
  `theLabel.FindChild(aChildInd)` for `aChildInd <= theLabel.NbChildren()`. The attributes it then looks for
  (`XCAFDoc_Area`, `XCAFDoc_Volume`, `XCAFDoc_Centroid`) are never set by the sidecar, so it writes nothing.
- **The file is the same.** With nothing to write, props mode off cannot change the file, and measured it does not:
  10,240 barrel occurrences from two fresh processes, byte-identical below the header (same 6,755,223 bytes); the
  fence fixture at 32,768 and 65,536 occurrences, identical after instance ids are blanked and wrapped lines joined;
  and an 8,192 export read back with every instance, both shapes, every unique name and the build's extent (below —
  the read-back is smaller because OCCT's reader is quadratic).
- **#89's k ≈ 2.2 from 90k to 300k** is the same walk further along its curve: the quadratic term is ~17 s of 20 s at
  90k and would be ~190 s of Transfer at 300k by N² — an inference from these three sizes, checked by the
  measurement below, not a fit.

## Results: before and after, #89's `measure.py step`

Barrel requests regenerated by `TestScaleUp_MeasureAirframeBarrel` (same generator, same sizes as #89). "after" is this
branch's sidecar; "before" is `verify/large-box-index`'s sidecar, run through a copy of `measure.py` whose only change
is the `SIDECAR` path. "Transfer" is the new `export_transfer` phase, which the before sidecar does not report. Rows
are `data/measure-step-after.jsonl` and `data/measure-step-before.jsonl`; "#89" rows are copied from
[its table](../2026-09-15-one-million-occurrences/README.md).

| occurrences | sidecar | run | export | Transfer | build | peak GB | file | CPU | other processes |
|---:|---|---:|---:|---:|---:|---:|---:|---:|---|
| 90,880 | #89 (before) | 1 | 21.6 | — | 58.8 | 1.29 | 62 MB | 20% | |
| 90,880 | #89 (before) | 2 | 21.0 | — | 58.7 | 1.29 | | 23% | |
| 90,880 | before, rerun here | 1 | **31.8** | — | 89.2 | 1.29 | 62.3 MB | 58% | agent.test, cad.test, go, python |
| 90,880 | **after** | 1 | **9.5** | 4.9 | 57.0 | 1.29 | 62.3 MB | 41% | agent.test, httpapi.test, go, python |
| 90,880 | **after** | 2 | **7.5** | 3.0 | 47.5 | 1.29 | 62.3 MB | 28% | agent.test, go, python |
| 302,560 | #89 (before) | 1 | 391.1 | — | 543.0 | 3.39 | 212 MB | 39% | |
| 302,560 | #89 (before) | 2 | 306.6 | — | 465.2 | 3.38 | | 32% | |
| 302,560 | **after** | 1 | **39.2** | 21.9 | 213.4 | 3.38 | 212.4 MB | 42% | agent.test, go, python |
| 302,560 | **after** | 2 | **31.8** | 13.0 | 226.7 | 3.38 | 212.4 MB | 55% | agent.test, cad.test, httpapi.test, node, go, python |

Seconds; "CPU" is the whole machine's over the run, "other processes" the go/node/python/test processes present
(`cad.test` was another session's, not this branch's). The before sidecar was not rerun at 300k: #89's two runs
already took 307–391 s, and an hour of export on a shared machine would not change the comparison.

- **300k: 307–391 s → 32–39 s** (~9.6× on the faster runs); **90k: 21–32 s → 7.5–9.5 s** (~2.8×). File size and peak
  memory are unchanged at both sizes.
- **Growth, 90k → 300k (3.33×), faster run each:** export 7.5 → 31.8 s, **k ≈ 1.20** (#89: k ≈ 2.2). Transfer
  3.0 → 13.0 s, k ≈ 1.22. Not exactly linear; see "What is left" below.
- The rerun of the before sidecar at 90k took 31.8 s where #89 took 21 s, at 58% CPU against 20–23%: this machine's
  load moves a single-threaded export by 1.5×, which is why each after row carries its load.

### What is left of Transfer's growth

With props mode off, each other writer mode switched off in turn (`data/profile-residual.jsonl`, `profile_export.py`
variants `fix`, `fix_no_*`, `all_off`). Transfer, seconds, one run each (`fix` twice):

| writer, props off | fixture 4,096 | fixture 65,536 | barrel 30,400 | barrel 90,880 |
|---|---:|---:|---:|---:|
| as shipped (`fix`) | 0.20–0.25 | 3.21–3.80 | 1.19–1.23 | 3.22–3.81 |
| and no names | 0.18 | 3.07 | 1.04 | 3.73 |
| and no colours, no layers | 0.17 | 3.01 | 1.01 | 3.08 |
| and no SHUO | 0.18 | (10.25, load spike) | 1.25 | 3.56 |
| and no GD&T, no material | 0.19 | 3.37 | 1.25 | 3.83 |
| every mode off | 0.16 | 3.40 | 0.98 | 2.73 |
| machine CPU | 57–80% | 39–72% | 47–53% | 33–54% |

- **What is left is linear, and no mode owns it.** 16× the fixture is 13–19× the Transfer; 3× the barrel is 2.6–3.2×.
  Switching every mode off takes 0–25% off at each size, and the same share at both.
- **The "no SHUO" row at 65,536 is load, not SHUO:** that export's own Python loop (AddShape, names, AddComponent) took
  10.2 s where every other row took 1.1–1.5 s, at the lowest CPU reading of the set.
- **`measure.py`'s Transfer k ≈ 1.22 from 90k to 300k** is not explained by these rows, which are linear to 90k. At
  300k the process holds 3.4 GB; cache and allocator effects are a plausible cause and were not measured.

Measured again after the fence moved to `export_transfer`: the fence's own calibration run (below) had Transfer
0.150 s at 4,096 and 4.84 s at 65,536, 32× under 47% CPU, where these rows give 13–19×. That spread is why the fence's
floor and bound are what they are.

## Fences

- `TestKernel_ExportTimeGrowsLinearlyPastTheBuildCeiling` — through `testdata/step_export_scaling.py`, which calls
  `_build` directly (Go refuses more than 4,096 parts): 4,096 and 65,536 occurrences of two definitions in one flat
  assembly. Transfer alone, reported by the sidecar as the `export_transfer` phase, must grow at most 40× for 16× the
  occurrences (the small size floored at 0.25 s), and the walked writer (props mode back on) at 65,536 must take at
  least 2× the shipped one in the same run, or the fixture is too small to see the walk. Each size is the faster of
  two runs. Also fails if the sidecar ships with props mode on.

  Calibration, one run of the script each (seconds; each size the faster of two runs, the walked writer one run):

  | sidecar | CPU before → after | 4,096 export | 65,536 export | 4,096 Transfer | 65,536 Transfer | walked Transfer, 65,536 |
  |---|---|---:|---:|---:|---:|---:|
  | fixed | — → 47% | 0.97 | 10.68 | 0.150 | 4.84 | 12.95 |
  | before the fix | 56% → 36% | 1.42 | 18.18 | (not reported) | (not reported) | (export 18.69) |

  The whole export phase does not separate the two writers at this size (11.0× and 12.8× for 16× the occurrences):
  writing and encoding 42 MB are linear and large. Transfer does.

  The fences' passing run, `go test` at 14:16–14:20, machine CPU 52% → 35%: Transfer **0.14 s** at 4,096, **3.05 s** at
  65,536 (12.2× against the floor), walked **13.07 s** (4.3× the shipped writer); whole export 0.85 s, 8.11 s, 18.04 s.
  `TestKernel_ExportTimeGrowsLinearlyPastTheBuildCeiling` 181 s, `TestKernel_ALargeExportIsTheFileThePropertyWalkWrote`
  16 s (one script run, shared), `TestKernel_AnExportedFilePlacesEveryPartWhereTheBuildDid` 9 s.

  Bounds: the small transfer is floored at **0.25 s** and the ratio must be **≤ 40×**. Shipped, 4,096 → 65,536
  measured 13–32× (the top with a 0.15 s small reading under load, 19× against the floor); the walk measured 12.9 s at
  65,536, 52× the floor. The walked writer must also take **≥ 2×** the shipped one's Transfer at 65,536 in the same
  run (measured 2.7×). ‼️ The before-fix sidecar reports no `export_transfer`, so the fence fails on it by "not
  reported" rather than by the ratio; the drills below are what show each bound going red.
- `TestKernel_ALargeExportIsTheFileThePropertyWalkWrote` — at 65,536 the shipped file equals the walked file below
  the header with instance ids blanked and wrapped lines joined; at **8,192** (a separate export in the same script),
  read back with `testdata/step_reimport.py`, it holds 8,192 components of 2 shapes, 8,192 distinct names and the
  build's extent to 0.01 mm. The read-back is smaller because the reader is quadratic (next section).

### ‼️ Reading a large file back is quadratic, in OCCT's reader

The first version of the second fence read the 65,536-occurrence file back and was killed by `go test`'s 20-minute
timeout; the two reader processes were still running 18 minutes after they started. Timed on the fence fixture
(`data/reimport-sizes.jsonl`; each size exported once, then read by a timed copy of `step_reimport.py` and by the
script itself, capped at 300 s):

| occurrences | file | ReadFile | reader Transfer | walk of components | whole script |
|---:|---:|---:|---:|---:|---:|
| 4,096 | 2.5 MB | 0.7 s | 3.8 s | 0.05 s | 5.5–6.0 s |
| 8,192 | 5.1 MB | 6.6 s | 19.0 s | 0.09 s | 19.4–27.0 s |
| 16,384 | 10.3 MB | 3.2 s | 81.7 s | 0.10 s | 66.9–86.4 s |
| 32,768 | 21.0 MB | — | — | — | stopped at 300 s |

Machine CPU 38–63%. `STEPCAFControl_Reader.Transfer` grows ~4.3× per doubling (k ≈ 2.1); the component walk the test
does is linear. **Not investigated and not changed here:** which reader step is quadratic, and whether a reader
setting avoids it. It matters to anyone importing FORGE's large exports — a CAD tool built on OCCT would take minutes
to open a 32k-part file this branch writes in seconds.

K2's `TestKernel_ExportingManyOccurrencesGrowsLinearly` (512 → 4,096) stays; it was green on the quadratic writer.

## Drills

In `scripts/drill-fences.sh`, "A STEP export stays linear past the build ceiling". Run through a temporary runner
(the script's header, this section and its footer) at 14:21–14:36, machine CPU 52% at the start: **4 went red, 0
stayed green, 0 anchors moved**, tree byte-identical afterwards.

| drill | fence | went red on |
|---|---|---|
| the writer walks every label for validation properties again | `ExportTimeGrowsLinearly…` | ships with the walk on (Transfer 0.20 s → 11.43 s, walked 11.51 s) |
| the props-mode setting never reaches the writer | `ExportTimeGrowsLinearly…` | 54.2× the transfer for 16× the occurrences (0.20 s → 13.55 s) |
| the fixed writer stops writing names the walking writer wrote | `ALargeExportIsTheFile…` | the file differs from the walked one; an instance read back named `"204801"` |
| a large export writes its occurrences without their placement | `ALargeExportIsTheFile…` | rerun 14:37–14:41 (CPU 24%): the file's extent is one stud at the origin (±2, ±3, ±5 mm), the build's reaches 3,061.5 × 21.5 × 749 mm |

‼️ The placement drill first went red on the names only: a file written without placements also reads back with
numbered instance names, and the name check stopped the test before the extent was compared. The extent check was
moved first and the drill run again (row above).

## What this establishes

- Where the barrel's STEP export time grows: `STEPCAFControl_Writer.Transfer`'s props-mode walk, quadratic in the
  components of one assembly, on OCCT 7.9.3 as wheeled by cadquery-ocp-novtk 7.9.3.1.1.
- That FORGE's own steps (XCAF document, `UpdateAssemblies`, writing, encoding) are linear from 10k to 90k.
- That switching props mode off leaves the sidecar's file unchanged: as bytes below the header at 10k (barrel) and
  65k (fixture, ids blanked), and read back at 8k.

## What this does NOT establish

- **STEP at 1M.** Not attempted; it stays refused (owner decision) until an off-node export job exists.
- **A new ceiling.** Nothing here changes the 4,096 build ceiling; see Recommendations.
- **Every OCCT version.** Read and measured on 7.9.3. `WritePropsForLabel` may differ in 7.8 or 8.x.
- **Nested assemblies.** The sidecar writes one flat assembly; the walk's cost in a deep tree was not measured.
- **Memory.** Peak working set was recorded per row in the data files, not analysed; the 300k peak is in the table
  above.
- **The per-process instance-id counter's consequences.** Recorded, not investigated: whether any consumer relies on
  the id strings is unknown.
- **Variance.** One or two runs a size on a shared laptop.

## Recommendations

1. **Keep the 4,096 build ceiling here** (owner decision); if STEP export's cost is what holds it, the before/after
   numbers above are the measured basis to revisit it on.
2. **Give the off-node export job this setting** — it removes the quadratic term the job would otherwise pay at 1M.
3. **If the sidecar ever writes validation properties** (area, volume, centroid), do not simply turn props mode back
   on: write them in a way that does not walk an assembly's children by index, and re-measure.
4. **Decide whether instance ids should be stable across exports.** They are not today, before or after this change.
5. **Measure OCCT's STEP reader on large flat assemblies** before promising that a large export can be re-imported by
   an OCCT-based tool: 82 s at 16,384 and over 300 s at 32,768 here.

## Re-running

```bash
export GOWORK=off D=/some/dir
FORGE_SCALE_MEASURE_OUT=$D FORGE_SCALE_MEASURE_BAYS=1,3,9,30 go test -count=1 -run TestScaleUp_MeasureAirframeBarrel ./internal/domain/geometry
.cadvenv/bin/python docs/spikes/2026-09-15-step-export-scaling/profile_export.py internal/domain/cad/sidecar.py \
  $D/barrel-9.json $D/profile.jsonl baseline,no_props,bare,chunked,plain_writer
.cadvenv/bin/python docs/spikes/2026-09-15-one-million-occurrences/measure.py --dir $D --bays 9,30 --modes step --repeat 2
FORGE_CAD_PYTHON=.cadvenv/bin/python go test -count=1 -run 'TestKernel_(ExportTimeGrows|ALargeExport)' ./internal/domain/cad
```
