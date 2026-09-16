# Measurement: what a repair asks, and where the 1M interference check goes

**Date:** 2026-09-15 · **Status:** done · **Follows:** [`2026-09-15-next-scale-walls`](../2026-09-15-next-scale-walls/README.md)
(#113), on #106, [`2026-09-15-large-box-index`](../2026-09-15-large-box-index/README.md) (#94) and
[`2026-09-15-one-million-occurrences`](../2026-09-15-one-million-occurrences/README.md) (#89)

## Summary

- **The 1M shipped build: 168 s → 102 s. The interference check inside it: 119 s → 53 s**, with every clash found and a
  check answer byte-identical to the old one at 90k, 300k and 1M. Both at 18–20% machine CPU, back to back.
- **The check was a Python keying loop, not geometry.** At 1M it keys 2,191,348 candidate pairs to find 15 booleans'
  worth of distinct clashes. About 93 s of the 117 s went to `_pair_key`: four build123d `Location`s a pair
  (8.8 million `Location.__init__` calls), 24 `round()` calls a pair, and generator expressions. The broad phase was
  11 s and the per-occurrence boxes 11 s.
- **The keys are now made from the same OCCT calls without the Python objects, and the relative rotation is kept per
  pair of rotations** (the barrel has 80 rotations, and 224 pairs of them among 197,356 candidate pairs at 90k). Every
  key of every pair of solids on eight fixtures is the old key to the bit, and the check built on them is the same
  check. Keying 8× the pairs builds 0 `Location`s where build123d's path built 1,616 → 12,480.
- **An overlap repair on the barrel asked a model 1,410,000 bytes of problem lines. It now asks 942 bytes**, the same at
  every size: the counts (1,760,000 found, 10,000 listed, 10,000 buried), the worst clash by id, and one group, "Rivet
  (definition "rivet", placed by bay/sector/stringer-rivets) is 82% inside Stringer". A model with few buried clashes
  is asked exactly what it was asked before.
- **Nothing measured here raises a limit.** The check is now 52% of the 1M build, and shapes are 38%.

## Why this measurement

\#113 left two open items on the 1,008,160-occurrence airframe barrel:

1. **A repair prompt.** The kernel's list is bounded to the worst 10,000 clashes, but an overlap repair still sent a
   model one line per buried clash in it: 10,000 lines, 1.41 MB. Unbounded, the barrel's 768,000 buried clashes would
   have been 108 MB.
2. **The interference check was 121 s of the 174 s 1M build**, and had never been profiled. #113's recommendation 6
   guessed at `_pair_key` from the phase split and said to profile before changing anything.

## 1. What an overlap repair asks

### The rule

`repairAsks` (`internal/agent/interference.go`) decides what `repairGeometry` is sent. `repairIfPartsOverlap` still
judges a repair by every buried clash in the kernel's list; only what the model is **asked** changed.

- **Buried clashes whose lines fit `maxRepairProblemBytes` (16 KiB) are sent as before, line for line.**
- **Past it, a summary**, grouped by the pair of *placements* that made each clash. A part id is traced through the
  document's tree: pattern and repeat copy numbers dropped, the definition named
  (`bay-3/sector-7/stringer-rivets-12` → `bay/sector/stringer-rivets`, definition `rivet`). A group is a count, the
  range of depths, and 3 clashes by id. That is what a model can change: a child's placement, not rivet 12 of sector 7.
- **It always says:** how many pairs were found, how many the kernel listed, how many of those are buried, how many
  groups there are, and how many clashes the groups it describes cover. A last line counts the groups and clashes it did
  not describe. How many unlisted clashes are buried is not known in Go, and is never claimed.
- **The worst clash is always named second, by id**, whatever the budget.
- **Deterministic:** groups are ordered deepest, then largest, then first listed; never a map's order.
- **Every line is at most 1 KiB, cut on a character boundary**, so no label can spend the budget.

### Why 16 KiB

About 4,000 tokens at ~4 bytes a token (not measured with a tokenizer). The largest context a turn already sends beside
a document is `maxStepContextBytes`, 64 KiB (`assemble.go`); the problems should not outweigh the document they come
with, so a quarter of that. It holds ~110 of the barrel's 141-byte lines, and the live car that found this defect class
buried at most 4 (`2026-09-12-car-ceiling`), so every model FORGE has built live is asked as before. **A chosen bound,
not a measured optimum:** no model was measured repairing from either form.

### Three kinds of "not everything", kept apart

| said where | means | number |
|---|---|---|
| `coverageNote`, "checked X of Y" (V2) | pairs never checked: the model is not known to be clear | `Truncated`, `Checked`, `Pairs` |
| `coverageNote`, "found N … lists the M" (#113) | clashes found and counted, not listed | `Found`, the list |
| the repair prompt (this) | listed clashes not spelled out to a model | groups described, clashes covered |

### Measured on the kernel's replies

`TestScaleUp_MeasureTheRepairPrompt` (`internal/agent`) on the after runs' replies (`measure.py --write-reply`) and the
barrel documents (`TestScaleUp_MeasureAirframeBarrel` now writes `barrel-doc-<bays>.json`); `data/repair-*.json`,
`data/repair-prompt-100.txt`.

| occurrences | found | listed | buried listed | one line each | asked now | lines | groups | Go time |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 90,880 | 158,400 | 10,000 | 10,000 | 1,410,000 B | **942 B** | 3 | 1 | 10.5 ms |
| 302,560 | 528,000 | 10,000 | 10,000 | 1,410,000 B | **942 B** | 3 | 1 | 10.5 ms |
| 1,008,160 | 1,760,000 | 10,000 | 10,000 | 1,410,000 B | **943 B** | 3 | 1 | 9.8 ms |

The barrel's worst 10,000 are all stringer rivets (82%) in their stringers: one placement against one, which is one
group. The synthetic fences reach many groups (600 tied groups, a budget-filling sweep).

## 2. The interference check at 1M

### Where it went (before)

`profile_check.py`: `_build` as shipped up to the check, the check's arguments captured, then on the same solids in the
same process the check uninstrumented and timed, its first two steps alone, and the check under cProfile. The sidecar
is #113's head, frozen (sha1 `25c3c4a`). One run a size, 16–17% machine CPU (`data/profile-check-before.jsonl`,
`data/profile-before-*.txt`, `data/load.log`).

| step | 90,880 | 302,560 | 1,008,160 |
|---|---:|---:|---:|
| **the check, uninstrumented** | **10.6 s** | **35.4 s** | **117.1 s** |
| `_measures` (a moved box per occurrence) | 0.97 | 3.41 | 11.25 |
| `_candidate_pairs` (the grid) | 1.02 | 3.41 | 11.16 |
| garbage collection during the check | 0.03 | 0.43 | 1.24 |
| candidate pairs / booleans / clashes | 197,356 / 15 / 158,400 | 657,508 / 15 / 528,000 | 2,191,348 / 15 / 1,760,000 |
| the check under cProfile | 21.3 s | 69.2 s | 230.1 s |

cProfile at 1M, by own time (the profiler roughly doubles a Python call, so shares, not seconds):

| calls | own s | cumulative s | what |
|---:|---:|---:|---|
| 8,765,392 | 25.5 | 40.1 | build123d `Location.__init__`: 9 `kwargs.pop`s and 4 `isinstance`s around one OCCT object |
| 52,592,352 | 14.4 | 14.4 | `round()`, 24 a pair |
| 1 | 13.5 | 28.9 | `_candidate_pairs` |
| 78,888,560 | 7.6 | 7.6 | `dict.pop` (inside `Location.__init__`) |
| 65,741,060 | 7.4 | 7.4 | `isinstance` |
| 4,382,696 | 7.2 | 27.8 | `Location.__mul__` |
| 4,382,696 | 4.9 | 10.5 | `_carried` (generator expressions) |
| 2,191,348 | 4.3 | | `Location.inverse` |
| 4,382,696 | 2.0 | 7.8 | `_marks` (a generator) |

The old keying loop split by subtraction, uninstrumented, at 1M (each loop does what the one before did and one step
more; run inside the after profile, which still carries the old `_pair_key`, `_pose` and `_inside`; the
`split` field of `data/profile-check-after.jsonl`): relative `Location`s 19.8 s, + two `_pose`s 62.1 s, + two
`_inside`s 85.7 s, `_pair_key` whole **93.2 s**; the cache lookups 0.4 s, and 15 distinct keys. Under the profiler,
`heapq.nsmallest` for the worst 10,000 was 0.4 s at 1M. JSON is written after the check and took 0.012 s in #113.

- **The keying loop was ~80% of the check.** Booleans are 15 at every size, the cache holds 15 keys, and sorting and
  JSON are under a second.
- **Linear, not a curve.** Every step grows with the pairs (k ≈ 1.0 from 90k to 1M).
- **Most of a key was Python around OCCT, not OCCT.** Measured one call at a time: `Inverted()` 0.65 µs, a product
  0.70 µs, `Transformation()` 0.57 µs, one `Value()` 0.37 µs; a build123d `Location` around a product 2.25 µs.

### The change

Three replacements, each with a module switch restoring the old path, kept as the reference the fence compares against.

1. **`_pair_keys` (`_PAIR_KEY_DIRECT`).** The same `Inverted()`, product and `Transformation()` on the same
   `TopLoc_Location`s, with no build123d `Location` around them; a placement's inverse taken once per solid
   (`TopLoc_Location` is immutable); the pose's rounding, containment sums, carried axes and marks written out in the
   old order, without generators; and an early return when neither frame contains the other, which is what `_slid`
   and `_marks` would have given.
2. **The relative rotation once per pair of rotations** (inside `_pair_keys`). A `gp_Trsf` product's rotation is
   computed from the factors' matrices, forms and scale factors, never from a translation; so for placements that are
   one datum at power 1 with scale 1 (what `_placement` makes), the relative rotation, raw and rounded, is a function
   of the two rotations. Checked, not assumed: at 90k, all 197,356 pairs both ways gave the same bits for the same two
   rotations (224 pairs of 80). Chains, powers, other scales and two solids sharing one datum are keyed without it;
   the memo holds at most 65,536 entries. A prototype of it, on the 90k barrel's candidate pairs: 22.0 µs a key →
   14.2 µs, 0 keys different.
3. **`_moved_box_direct` (`_MOVED_BOX_DIRECT`).** The same corner sums in the same order from entries read once, min
   and max of the eight at once. `_measures` also hands each solid's rotation bits to the memo, read there anyway.

### After, and before beside it

**The check alone** (`profile_check.py`, frozen sidecar sha1 `14a49b1` = the committed file;
`data/profile-check-after.jsonl`, `data/profile-after-*.txt`):

| occurrences | check before | check after | `_measures` | `_candidate_pairs` | CPU before / after |
|---:|---:|---:|---:|---:|---:|
| 90,880 | 10.6 s | **4.4 s** | 0.97 → 0.66 | 1.02 → 1.00 | 16% / 18% |
| 302,560 | 35.4 s | **15.7 s** | 3.41 → 2.17 | 3.41 → 3.25 | 17% / 22% |
| 1,008,160 | 117.1 s | **51.2 s** | 11.25 → 7.44 | 11.16 → 10.85 | 16% / 17% |

**The same answer** (`data/check-identity.txt`): `_interferences`' whole return (the 10,000 listed, the flag, box
tests, pairs, booleans, reuses, count, summarized) as JSON is byte-identical before and after at all three sizes
(1,685,751, 1,685,752 and 1,685,755 bytes).

**The whole build** (#89's `measure.py`, `full` mode, the two frozen sidecars back to back, 17:54–18:01;
`data/measure-before.jsonl`, `data/measure-after.jsonl`):

| occurrences | code | build s | shapes | assembly | interference | reply | peak GB | CPU |
|---:|---|---:|---:|---:|---:|---:|---:|---:|
| 90,880 | before | 14.8 | 3.3 | 0.6 | 10.5 | 1,686,204 B | 0.84 | 18% |
| 90,880 | **after** | **8.8** | 3.3 | 0.6 | **4.5** | 1,686,201 B | 0.84 | 18% |
| 302,560 | before | 49.9 | 11.3 | 1.9 | 35.5 | 1,686,202 B | 1.86 | 19% |
| 302,560 | **after** | **30.4** | 11.6 | 1.9 | **15.7** | 1,686,206 B | 1.86 | 20% |
| 1,008,160 | before | 168.2 | 38.8 | 6.4 | 119.0 | 1,686,207 B | 5.34 | 18% |
| 1,008,160 | **after** | **102.0** | 38.6 | 6.5 | **52.8** | 1,686,206 B | 5.35 | 20% |

Every run found every clash (158,400, 528,000, 1,760,000) of every pair with 15 booleans, not truncated, and listed
10,000. The reply differs by a few bytes between runs because it carries phase timings.

- **Same-run ratios, which load moves less:** the check after is 0.43, 0.44 and 0.44 of before at the three sizes; the
  build 0.59, 0.61 and 0.61.
- **Still linear:** the check after grows k = 1.02 from 90k to 1M.
- **Load:** `data/load.log`, every 10 s, 17:16–18:13. Mean CPU 17.8% during the before profile, 18.4% and 19.7%
  during the before and after builds, 16.9% during the after profile. No go, test or node process above 1% in any
  sample; the rest was the editor (~5%) and idle.
- **What is left of the check at 1M:** `_candidate_pairs` 10.9 s, `_measures` 7.4 s, and ~32 s of keys; under the
  profiler, `_inside_split` 10.0 s, `_pose_of` 5.9 s, `_carried_fast` 5.3 s, `_moved_box_direct` 4.2 s.

## Fences

`go test ./internal/agent -run TestInterference_` and, with `FORGE_CAD_PYTHON` set,
`go test ./internal/domain/cad -run 'TestKernel_(APairKeyWithoutLocations|KeyingMorePairs)'`. New:

| fence | holds |
|---|---|
| `TestInterference_ARepairOfTenThousandClashesIsAskedWithinItsBudget` | 10,000 barrel-like clashes, 1,760,000 found: one call, under 16 KiB, every count exact and consistent, the worst named by id, groups by placement |
| `TestInterference_ARepairSummaryIsTheSamePromptEveryTime` | 600 tied groups, 20 runs: the same prompt, groups in listed order, counts add up |
| `TestInterference_TheWorstClashIsNamedWhateverTheBudget` | the worst last in the list, alone in its group, with a 6 KB label, at two lengths one byte apart: named second, valid UTF-8, under budget |
| `TestInterference_ARepairSummaryNeverPassesItsBudget` | 400 label lengths: never over 16 KiB, and within 128 bytes of it, so an overrun would show |
| `TestInterference_AFewBuriedClashesAreAskedAboutAsBefore` | the live car's four and the longest list that fits: the old lines exactly; one more clash is summarized |
| `TestInterference_ARepairSummaryDoesNotGrowWithTheClashes` | 300, 10,000 and 100,000 clashes: under budget, totals exact, worst named |
| `TestInterference_AClashIsTracedToThePlacementThatMadeIt` | pattern copies, repeat copies, a child whose id looks like a copy, a top-level part, unknown ids |
| `TestKernel_APairKeyWithoutLocationsIsTheKeyBuild123dGave` | every pair of solids (not only candidates) on 8 fixtures: the four prism seeds, rails and turned crossbars, tied pins, 150 blocks (a summarized list), a turned barrel; slide on and off, memo on and off, and on the barrel with shared and chained locations: every key equal by `repr`; every moved box equal; the whole check's return equal |
| `TestKernel_KeyingMorePairsBuildsNoMoreLocations` | a count: 1 and 8 bays, 404 and 3,120 pairs: 0 `Location`s both times; build123d's path 1,616 and 12,480 |

`TestScaleUp_MeasureTheRepairPrompt` is a measurement and skips unless `FORGE_SCALE_MEASURE_REPAIR` is set.

The whole packages, 18:34–18:45 on this branch's code, after the drills: `go vet ./...` clean;
`internal/agent` and `internal/domain/geometry` pass. `internal/domain/cad` passes except for failures known on
Windows and not caused here: `TestScript_*` (15), `TestKernel_ReturnsTheSurfaceOfTheSolidItBuilt`, and the
scripted-part kernel tests (`ARepeatedScriptedPartRunsItsScriptOnce`, `ARepeatedScriptedPartIsBuiltEveryTime`,
`AScriptedPartIsExportedAndMeshed`), the same set as #113. Every interference fence, old and new, passed in that run.

## Drills

`scripts/drill-fences.sh`, section "Repair bound and check profile", through a temporary runner, 18:16–18:33. Dry run
first: 0 anchors moved. **22 went red, 1 stayed green, 0 unproven**, and the tree was byte-identical afterwards
(`data/drills.log`). The green one was removed (below), so the section as committed has 22 drills.

| drill | red fence | went red on |
|---|---|---|
| an overlap repair is asked about every buried clash however many | ARepairOfTenThousandClashes… | 1,271,000 bytes asked, over 16,384 |
| a model whose clashes fit is summarized anyway | AFewBuriedClashesAreAskedAboutAsBefore | the car's four were not asked as before |
| the summary counts the list as everything found | ARepairOfTenThousandClashes… | no "found 1760000 pairs" |
| the worst clash is the first listed, not the deepest | TheWorstClashIsNamed… | the bearing, last in the list, not named |
| groups are told in a map's order, with no tiebreak | ARepairSummaryIsTheSamePromptEveryTime | run 0 asked something different |
| clashes are grouped by their labels, not their placements | ARepairOfTenThousandClashes… | no group placed by bay/sector/stringer-rivets |
| a pattern copy is not traced to the child that placed it | AClashIsTracedToThePlacement… | stringer-rivets-12 traced to itself |
| a child's own id is read as a copy number | AClashIsTracedToThePlacement… | stringer-rivets-2 read as a copy of stringer-rivets |
| the shorter of two matching child ids wins | AClashIsTracedToThePlacement… | stringer-rivets-2-5 traced to stringer-rivets |
| a line is cut inside a character | TheWorstClashIsNamed… | "éé\xc3…" |
| the budget keeps no room for the header's count | ARepairSummaryNeverPassesItsBudget | 16,418 bytes |
| the budget keeps no room for the last line | ARepairSummaryNeverPassesItsBudget | 16,453 bytes |
| a direct key rounds its translation like its rotation | APairKeyWithoutLocations… | 20,732 keys differ on seed 1 |
| the rotation memo is keyed by one placement's rotation | APairKeyWithoutLocations… | 33,700 keys differ |
| a rotation is told apart by its first row alone | APairKeyWithoutLocations… | 14,906 keys differ |
| a placement's inverse is cached under the other solid | APairKeyWithoutLocations… | 31,154 keys differ |
| a direct key forgets what the other frame carries | APairKeyWithoutLocations… | 3,377 keys differ |
| a direct key is taken in the frame that marks fewer | APairKeyWithoutLocations… | 1,810 keys differ |
| direct containment takes a box's whole length for its reach | APairKeyWithoutLocations… | 6,158 keys differ |
| a pair inside only the other frame is keyed as unmarked | APairKeyWithoutLocations… | 3,239 keys differ |
| a moved box forgets its far corner | APairKeyWithoutLocations… | 28 of 275 boxes differ |
| the check keys a pair through build123d again | KeyingMorePairsBuildsNoMoreLocations | the counting run failed |

‼️ **Stayed green, and removed: "two solids at one location are keyed from the memo".** The memo is skipped when both
solids' placements are one datum, because `TopLoc_Location` cancels that product to the exact identity, while the memo
would hand over the relative rotation of two different datums with the same rotation. That rotation is the identity
off by ~1e-16 on its diagonal. It rounds to the same pose, and reaches the key only through containment, at exactly
its boundary. No fixture can put a solid there, because bounds carry OCCT's tolerance. The guard is kept so the key
stays build123d's to the bit in that case; **nothing measured here shows it is needed**, and the script says so where
the drill was.

## What this establishes

- **What the 1M interference check spends, by step**, at three sizes, uninstrumented and under a profiler: ~80% keying
  candidate pairs through build123d `Location`s, ~10% each the broad phase and the per-occurrence boxes, with 15
  booleans and under a second of sorting.
- **The check is 2.3× faster at every size (119 → 53 s at 1M) and gives the same answer to the byte** at 90k, 300k and
  1M; every pair key and moved box is the old one to the bit on eight fixtures that reach slides, carried axes, a
  summarized list, shared and chained locations. Fenced, and drilled.
- **A repair on the barrel is asked 942 bytes instead of 1.41 MB**, with exact counts and the worst clash named; a model
  with few buried clashes is asked what it was before. Fenced, and drilled.

## What this does NOT establish

- **That a model repairs well from a summary.** Offline, with a stub model. No model was sent either form; whether
  "fix the placement bay/sector/stringer-rivets" leads to a better repair than 110 rivet lines is not known.
- **The right budget.** 16 KiB is reasoned from the step context bound and the live car, not measured. The token
  estimate was not measured with a tokenizer.
- **That a repair on the barrel can ever be accepted.** `repairIfPartsOverlap` accepts a repair when the kernel lists
  fewer buried clashes after it. With a list bounded to 10,000, a repair that removes half of 768,000 buried clashes
  still lists 10,000 and is refused. Read from the code; not changed here (see Recommendations).
- **The rotation memo's argument for every placement.** It rests on OCCT's `gp_Trsf` product, read not proved, and is
  guarded to single-datum, power-1, scale-1 placements. It is checked on the barrel at 90k and on the eight fixtures,
  including shared and chained locations. Placements of other kinds are keyed without the memo; none from an imported
  STEP part was tested.
- **That the memo's same-datum guard is needed.** Its drill stayed green (see Drills).
- **Mesh and STEP modes.** They replace or skip the check; not re-measured.
- **Timing variance.** One run a size, back to back on a quiet laptop with load logged. Counts, byte identity and
  same-run ratios are the load-robust results.

## Recommendations (nothing here is changed)

1. **Judge an overlap repair by the kernel's count, not the listed buried count.** Past 10,000 listed, "fewer buried
   listed" cannot fall. `Found` (and a buried count from the sidecar, if one is added) would.
2. **Keep the 4,096 build and export ceilings, the 2,000-boolean budget, 10,000 listed clashes and 100,000
   occurrences.** Nothing measured here is a basis for moving them.
3. **The next walls at 1M are shapes (39 s, 38% of the build) and what is left of the check (53 s):** keys ~32 s,
   the broad phase 11 s, boxes 7 s. Keying pairs in bulk (per group of candidate pairs with one pair of definitions and
   rotations, with arrays) is the next step for the check; it needs its own equivalence fence like this one.
4. **Measure a live repair from a summary** before relying on it: one barrel-like document, one call, within the A4
   token cap.

## Re-running

```bash
export GOWORK=off D=/some/dir PY=.cadvenv/bin/python
FORGE_SCALE_MEASURE_OUT=$D FORGE_SCALE_MEASURE_BAYS=9,30,100 go test -count=1 -timeout 30m \
  -run TestScaleUp_MeasureAirframeBarrel ./internal/domain/geometry        # barrel-<bays>.json, barrel-doc-<bays>.json
mkdir -p $D/after && cp internal/domain/cad/sidecar.py $D/after/sidecar.py  # freeze; before: #113's head
$PY docs/spikes/2026-09-15-check-profile/load_log.py $D/load.log &          # load every 10 s
for b in 9 30 100; do
  $PY docs/spikes/2026-09-15-check-profile/profile_check.py $D/after/sidecar.py $D/barrel-$b.json $D/profile $b
done
$PY docs/spikes/2026-09-15-one-million-occurrences/measure.py --dir $D --bays 9,30,100 --modes full \
  --repeat 1 --cap 2400 --sidecar $D/after/sidecar.py --write-reply
FORGE_SCALE_MEASURE_REPAIR=$D go test -count=1 -run TestScaleUp_MeasureTheRepairPrompt ./internal/agent
```
