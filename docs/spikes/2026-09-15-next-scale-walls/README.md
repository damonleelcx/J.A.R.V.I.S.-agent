# Measurement: the next walls at 1,000,000 occurrences

**Date:** 2026-09-15 · **Status:** done · **Follows:** [`2026-09-15-step-export-scaling`](../2026-09-15-step-export-scaling/README.md)
(#106), on [`2026-09-15-large-box-index`](../2026-09-15-large-box-index/README.md) (#94) and
[`2026-09-15-one-million-occurrences`](../2026-09-15-one-million-occurrences/README.md) (#89)

## Summary

- **The 1M shipped build: 515–566 s → 174 s, with every one of its 1,760,000 clashes found.** Shapes 273 → 42 s,
  assembly 177 → 7 s. The 1M mesh path: 480 s → 111 s. Before today at 37% machine CPU, after at 20%; the counts
  below do not depend on load.
- **The reply at 1M: 312.2 MB → 1.69 MB.** Go decoded it in 2.54 s with 921 MiB allocated; now 15 ms and 4.2 MiB.
  The repair's first step found 768,000 buried clashes (108 MB of prompt lines); now at most 10,000 (1.4 MB). A reply
  lists the worst 10,000 clashes and always counts them all, and a list cut to that bound says so in the reply, in
  `cad.Build` and in the turn. It is never read as a truncated check.
- **A clash now slides along a cylinder's length and an extrusion's depth.** It is proved as #94's box slide is, and
  checked against every pair measured on four randomized fixtures. On a shaft and a girder, 386 clashes cost 7
  booleans where they cost 197.
- **Shapes and assembly were linear and large, not a curve.** At 1M: 112 s building a `Plane` per occurrence, 108 s
  in `Shape.moved` (64 s of it a B-rep copy thrown away), 173 s integrating every copy's volume. Now a `Plane` per
  distinct rotation, no discarded copy, a volume per definition. The copies are build123d's to the bit, the bounds
  are identical, and the volume matches to 1e-11. Placing 8× the occurrences asks build123d for no more copies,
  `Plane`s or integrals.
- ‼️ **Found on the way: a curved clash's volume moves up to 1.7e-5 when the pair moves rigidly.** OCCT is
  bit-reproducible and stable to 1e-10 along a slide. #94's same-pose cache already inherits this.
- **The next wall at 1M is the interference check again: 121 s of the 174 s build (69%)**, unchanged by this work.
  Nothing measured here raises a limit.

## Why this measurement

\#94 left three open items on the 1,008,160-occurrence airframe barrel:

1. **The interference reply.** The full build now returns every one of its 1,760,000 clashes. Its JSON size, the
   sidecar's encode, Go's decode and what each product path does with the list were not measured.
2. **Clash reuse beyond boxes.** `_INTERFERENCE_SLIDE` slides a clash along a box only. A cylinder along its length
   and an extrusion along its depth are prisms too, and got nothing.
3. **Shapes and assembly were 77% of the 1M build** (234 s and 161 s of 515 s).

## 1. The interference reply

### Before: what 1,760,000 listed clashes cost

The 1,008,160-occurrence barrel, `full` mode, the sidecar as #106 left it (frozen copy), one run at 37% machine CPU
with no other go/node/test processes present (`data/measure-before.jsonl`), then Go on the reply it wrote
(`data/go-reply-before-1M.json`, `TestScaleUp_MeasureTheInterferenceReply`).

| what | measured |
|---|---|
| build | 565.6 s: shapes 272.9, assembly 176.7, interference 111.1; 5.44 GB peak |
| clashes | 1,760,000 found and listed, of 2,191,348 pairs; 15 booleans, not truncated |
| reply line | **312,200,381 bytes**, of which the interference list is 312,199,840 (all but 541) |
| sidecar encode (`json.dumps`) | 2.21 s |
| Go read of the line | 0.10 s |
| Go `json.Unmarshal` into `reply` | **2.54 s, 921 MiB allocated, 299 MiB held after GC** |
| `buildOf` (reply → `cad.Build`) | < 0.1 ms (the slice is shared, not copied) |
| `geometry.InterferenceProblems` (the repair's first step) | 0.25 s: **768,000 buried** (the stringer rivets, 82% inside) |
| the repair prompt's problem lines from those | **108,288,000 bytes** |

What each product path does with the list, read from the code on this branch:

| path | uses | at 1.76 M entries |
|---|---|---|
| agent turn, `repairIfPartsOverlap` | names 3 (`list`); sends every buried one to `repairGeometry` as a prompt line | a 108 MB prompt |
| sub-assembly looks (`look.go`) | marks a group as clashing if any finding names it | a walk of the list |
| render sheet, `kernelSolids.BuildSurface` | carries the list | the 299 MiB held |
| mesh endpoint, STEP export | read nothing from it | still pay the decode inside `cad.Build` |

### After

`TestScaleUp_MeasureTheInterferenceReply` on the replies the after runs wrote (`data/go-reply-after-*.json`):

| occurrences | found | listed | reply | Go decode | allocated | held | buried → problem lines |
|---:|---:|---:|---:|---:|---:|---:|---:|
| 90,880 | 158,400 | 10,000 | 1,686,203 B | 12.1 ms | 4.2 MiB | 2.1 MiB | 10,000 → 1.41 MB |
| 302,560 | 528,000 | 10,000 | 1,686,206 B | 12.5 ms | 4.2 MiB | 2.1 MiB | 10,000 → 1.41 MB |
| 1,008,160 | 1,760,000 | 10,000 | 1,686,205 B | 15.4 ms | 4.2 MiB | 2.1 MiB | 10,000 → 1.41 MB |

At 1M: 185× smaller, 165× faster to decode, 219× less allocated. The sidecar's encode went from 2.21 s to 0.012 s.

‼️ The mesh and export paths never show interference, and still decoded all of it. A 1M design does not reach any
product path today (the 4,096 ceiling refuses it before the kernel), so none of this was a live failure; it is what
the ceiling would meet first when raised.

### The change

**The check is unchanged; what is bounded is how many of its answers are written down.**

- **Sidecar.** `_interferences` keeps each clash as a tuple, not the reply's dict, and lists the worst
  `_INTERFERENCE_LIST_LIMIT` (10,000) with `heapq.nsmallest`. The order is by fraction, descending, ties in the order
  the pairs were found, which is exactly what the old stable sort gave, so a list under the bound is byte-for-byte
  the old one. The reply gains `interferences_found` (every clash, counted) and `interferences_summarized` (true
  when the list is shorter than the count).
- **Go.** `cad.Build` gains `InterferencesFound` and `InterferencesSummarized`. `buildOf` never believes a count
  below the list, and treats a list shorter than its count as a summary even if the flag is missing, so a reader
  that sees N findings and a count of N can trust nothing was left out. A reply without the field (an older sidecar)
  reads as whole.
- **Turn.** `agent.Built` and the sheet carry `Found`. `coverageNote` adds "FORGE found N pairs of parts sharing
  material and lists the M that share the most; the rest were counted, not listed" whenever `Found` exceeds the
  list. "And N more" counts from `Found`.
- **Not a truncation.** `InterferencesTruncated` still means "not every pair was checked", and a summarized list does
  not set it: every pair was checked, and saying "not known to be clear" about a model with 1.76 M known clashes
  would be false. The two are said separately, and both can be said at once.

**What stays the same for each reader.** The worst clash is always first, so the turn's three names are the same
three. A repair gets at most 10,000 buried problems instead of 768,000. The render sheet holds at most 10,000.

**Not changed, and a consequence.** A sub-assembly look marks a group as clashing only if a *listed* clash names it,
so beyond 10,000 a group whose only clashes rank below the bound is not moved to the front. The looks still run. At
most `maxSubAssemblyLooks` groups are looked at, and nothing is dropped.

**Why 10,000.** The largest clash count any fence produces is 2,400, so every existing fence stays whole. At 177
bytes a listed clash (the 1M reply's average), 10,000 is about 1.8 MB; measured, the after replies are 1.69 MB. That is a per-entry cost
times a chosen count, not a measured optimum. It bounds the reply, the decode and the sheet. It does not make a
10,000-line repair prompt small (see Recommendations).

## 2. Clashes slid along cylinders and extrusions

### The argument, and what holds it

The argument below is held by `TestKernel_AClashSlidAlongAPrismIsTheClashMeasuredAgain` (every pair measured
against reuse, four randomized seeds) and `TestKernel_CollarsAlongAShaftAndCleatsAlongAGirderPayForOneBooleanEach`.
Four drills show each goes red: sliding across a round section, a cone taken for a cylinder, and either slab left
unclaimed.

\#94's proof never used that the frame's solid is a box. It used that the solid is a slab S intersected with a set R
that does not change when moved along S's axis. If the other solid P lies wholly inside S, then P ∩ (S ∩ R) = P ∩ R,
and moving P along the axis moves it within R's invariance, so the common volume is one number for every placement
along that axis that keeps P inside S.

- A box is S ∩ (the other two slabs).
- A cylinder, which `_shape` builds only when the top radius equals the radius, is |y| ≤ h/2 ∩ (a round prism along
  its own y).
- An extrusion is |z| ≤ d/2 ∩ (its outline's prism along its own z). `_shape` extrudes half the depth each way;
  holes and islands are prisms too, and a mirrored extrusion is reflected in x, which leaves the slab alone.

So `_slabs` returns a half-length on each axis the shape is a slab on and None on the others, and `_inside` never
marks a None axis. Everything else in #94's key is unchanged: containment by the other solid's own box plus a
1e-4 mm margin, slabs trusted only when measured bounds agree with dims to a micron on the slab axes, marks carried
from the other frame when an axis lines up to 1e-9, and the frame that marks more translations keys the pair.

**Not claimed:** a cone (radius_top ≠ radius), a sphere, a revolve, a sweep (even a straight one), an imported STEP
part, and rotation of a cylinder about its own axis. Each is keyed by its full pose, as before.

### ‼️ A curved clash's volume moves with where the pair sits, slide or no slide

The first run of the all-pairs fence failed on the randomly turned shaft: reused and measured volumes differed by up
to 1.5e-5, and one cross pin that sits *outside* the slab (so was never slid) differed too. `boolean_noise.py` took
one shaft and pin pair and changed nothing about the clash itself (`data/boolean-noise.txt`):

| same relative pose, and | leaning pin (mm³) | cross pin (mm³) |
|---|---|---|
| the boolean repeated 5 times | identical to the bit | identical to the bit |
| the pair slid together along the shaft, 5 positions | spread 1e-10 | — |
| the pair moved by 5 unrelated rigid motions (unturned frame) | 381.67564 → 381.681559 | 201.086964 – 201.090378, spread 1.7e-5 |

- **OCCT is deterministic here**, despite build123d running booleans with `SetRunParallel(True)`.
- **Sliding along the axis is exact to 1e-10**, so the slide adds no error of its own.
- **A rigid motion of the whole pair moves a curved clash by up to 1.7e-5.** The intersection of two skew cylinders
  is approximated in world coordinates. Every reuse inherits this, including #94's same-pose cache, which already
  reuses a clash measured at one world placement for another. The box fences never saw it: planar booleans are exact.
- **So the prism fence compares at 1e-4 relative**, above the measured floor. A wrong slide is orders of magnitude
  larger: a chord pin at a different depth shares 8 mm³ where the axis pin shares 31, and a ring higher up a cone
  shares less by the change in radius. The drills below show each goes red.

## 3. Shapes and assembly

### Where the time went (before)

`profile_phases.py` on the frozen #106 sidecar, interference replaced by a no-op, one run a size at 45–47% machine
CPU (`data/profile-phases.jsonl`, `data/load-before.log`). Every call below is counted and timed where `_build`
makes it; "µs" is per call.

| step | 90,880 | 302,560 | 1,008,160 | µs each (1M) |
|---|---:|---:|---:|---:|
| **shapes phase** | **26.4 s** | **88.9 s** | **291.1 s** | |
| `_placement` (a build123d `Plane`, then `Location(plane)`) | 10.0 | 33.9 | 112.5 | 112 |
| `location * shape` (`Shape.moved`) | 10.0 | 33.3 | 108.2 | 107 |
| └ of which `BRepBuilderAPI_Copy`, made and thrown away | 5.9 | 19.8 | 64.3 | 64 |
| `_shape_key` (K1's JSON key) | 1.1 | 3.6 | 11.6 | 12 |
| `_shape` (K1 misses: 4 builds at every size) | 0.01 | 0.00 | 0.00 | |
| **assembly phase** | **17.2 s** | **56.2 s** | **180.5 s** | |
| `Compound.volume` | 16.4 | 54.2 | 172.9 | |
| └ `get_type(Solid)`: a Python `Solid` per child | 7.4 | 23.2 | 83.1 | 82 |
| └ `compute_mass`: an OCCT volume integral per child | 9.0 | 30.7 | 89.0 | 88 |
| `Compound.bounding_box` | 0.66 | 1.69 | 6.58 | |
| `Compound(built)` | 0.10 | 0.33 | 0.93 | |
| garbage collection (gen-2 collections) | 0.8 (3) | 2.5 (5) | 9.7 (7) | |
| peak working set | 0.82 GB | 1.81 GB | 5.14 GB | |

- **Nothing grows faster than the model.** Every per-call cost is flat across the three sizes, and from 90k to 1M
  shapes grow k ≈ 0.99 and assembly k ≈ 0.98. The wall is a large constant per occurrence, not a curve.
- **K1's cache is not the cost.** 4 misses and 1,008,156 hits at 1M. What each hit then does is the cost.
- **Four calls are 95% of the two phases at 1M.** `_placement` 112 s, `Shape.moved` 108 s, and `Compound.volume`
  173 s account for 393 s of 472 s. `_shape_key` is 12 s, and the rest of the shapes loop (appends, dict lookups,
  the profiler's own wrappers) about 59 s.
- **Two of those four do work nobody uses.** `Shape.moved` deep-copies the shape, which runs a full
  `BRepBuilderAPI_Copy` of its B-rep, then overwrites the copy's `wrapped` with the original moved. K1's sharing
  never used the copy (#89 measured the copies sharing one TShape). And `Compound.volume` integrates each child in
  its placed coordinates, where a rigid motion cannot change the answer.
- **`_placement` rebuilds a `Plane` for every occurrence**, though the barrel has 80 distinct rotations. A `Plane`
  normalises its axes through several `Vector`s and an OCCT axis system. Only its last step, `Location(plane)`,
  depends on the origin.
- **GC is 2% at 1M.** Seven gen-2 collections, 7.7 s. Not a wall, and not changed.
- **Compound construction and the bounding box are 7.5 s at 1M.** Not changed: a box read from definitions would not be
  bit-identical for turned curved parts, and it is not where the time is.

### The change

Three replacements, each producing what build123d produced. Each has a module switch that restores build123d's
call, kept so the fence has a reference to compare against.

1. **`_located(shape, location)` replaces `location * shape`** (`_PLACE_WITHOUT_COPYING`). It runs
   `Shape.__deepcopy__`'s own attribute loop, with the moved `TopoDS_Shape` put where the discarded
   `BRepBuilderAPI_Copy` would have gone. Same class, same attributes (each its own deep copy), same `TShape` as the
   definition, and the same location. Measured at 17 µs a call against 212 µs (`data/placement-variants.txt`).
2. **`_placement(solid, frames)` builds a `Plane` once per distinct matrix.** A `Plane`'s directions do not depend on
   its origin, so each occurrence repeats only `Location(plane)`'s last step: `gp_Ax3` at the occurrence's origin
   with the cached directions, `SetTransformation`, `Invert`. Keyed by `repr(matrix)`, which tells −0.0 from 0. Bit
   for bit the same transformation in 5,075 of 5,075 placements (quarter turns, −0.0, 200 random turns, positions
   from 1e-9 to 1e6), at 12 µs against 92 µs.
3. **`_assembly_volume` counts each definition's volume once** (`_ASSEMBLY_VOLUME_PER_DEFINITION`). The count comes
   from `Compound([definition]).volume`, the same rule `Compound.volume` applies to each child. A part a feature
   changed is counted as the solid it is. ‼️ Equal to about 1e-12, not to the bit: OCCT integrates a moved solid in
   its moved coordinates. Fenced at 1e-9 of the whole.

**Not changed:** `_shape_key` (12 s at 1M, JSON of up to ten fields; a faster key would be a second definition of
"the same shape"), `Compound(built)` and its bounding box (7.5 s at 1M; a box from moved definition boxes is not
the tight box of a turned curved part), and garbage collection (10 s at 1M).

## Before and after

### The phases alone (`profile_phases.py`, interference replaced by a no-op)

One run a size, the frozen sidecar before (#106) and after (this branch, sha1 checked equal to the committed file),
`data/profile-phases.jsonl`.

| occurrences | code | shapes s | assembly s | `_placement` µs | copy µs | volume | CPU |
|---:|---|---:|---:|---:|---:|---|---:|
| 90,880 | before | 26.4 | 17.2 | 110 | 110 (`Shape.moved`) | 333,596,945.2232791 | 47% |
| 90,880 | after | **4.9** | **0.68** | 19 | 26 (`_located`) | 333,596,945.2234033 | 47% |
| 302,560 | before | 88.9 | 56.2 | 112 | 110 | 1,097,457,956.066155 | 46% |
| 302,560 | after | **15.5** | **2.2** | 18 | 26 | 1,097,457,956.067266 | 44% |
| 1,008,160 | before | 291.1 | 180.5 | 112 | 107 | 3,643,661,325.542418 | 45% |
| 1,008,160 | after | **46.3** | **6.5** | 16 | 23 | 3,643,661,325.509079 | 30% |

- **Shapes 6.3×, assembly 28× faster at 1M;** the two phases went from 472 s to 53 s.
- **Counts, which load cannot move:** at every size, after, `BRepBuilderAPI_Copy` and `Shape.moved` ran once (the
  first definition built through build123d's own path) and `compute_mass` 4 times, where before each ran once per
  occurrence.
- **The bounds are identical to the bit at every size. The volume differs by 3.7e-13, 1.0e-12 and 9.1e-12 of
  itself**, which is OCCT integrating each copy in placed coordinates before and each definition once after.
- **Still linear:** shapes k = 0.94 and assembly k = 0.94 from 90k to 1M after, with 30–47% CPU across the runs.
- **What is left of the two phases at 1M:** `_located` 23 s, `_placement` 16 s, `_shape_key` 6 s, the bounding box
  5.5 s, GC 5.5 s, `Compound` 0.9 s.

### The whole build (#89's `measure.py`)

`full` is `_build` as shipped; `mesh` has format mesh with part properties and the interference check replaced by
V1's grid (#89's method). One run a size, `data/measure-before.jsonl` and `data/measure-after.jsonl`, load once a
minute in `data/load-before.log` and `data/load-after.log`. "#94" rows are copied from its table.

| occurrences | mode | code | build s | shapes | assembly | interference | reply | peak GB | CPU |
|---:|---|---|---:|---:|---:|---:|---:|---:|---:|
| 90,880 | full | #94 (best) | 68.5 | 34.3 | 20.0 | 13.6 | not measured | 0.84 | 56% |
| 90,880 | full | **after** | **16.7** | 3.8 | 0.6 | 11.9 | 1.69 MB | 0.84 | 34% |
| 302,560 | full | #94 (best) | 229.5 | 103.7 | 69.9 | 54.0 | not measured | 1.88 | 56% |
| 302,560 | full | **after** | **59.7** | 13.7 | 2.2 | 42.6 | 1.69 MB | 1.86 | 38% |
| 1,008,160 | full | #94 (best) | 515.4 | 234.2 | 161.3 | 115.1 | not measured | 5.44 | 17% |
| 1,008,160 | full | before, today | 565.6 | 272.9 | 176.7 | 111.1 | **312.2 MB** | 5.44 | 37% |
| 1,008,160 | full | **after** | **173.9** | 41.9 | 7.1 | 120.6 | **1.69 MB** | 5.34 | 20% |
| 90,880 | mesh | #89 (best) | 41.2 | 20.8 | 13.8 | 1.6 | | 0.88 | 15% |
| 90,880 | mesh | **after** | **9.7** | 3.3 | 0.6 | 1.5 | | 0.87 | 16% |
| 302,560 | mesh | #89 (best) | 141.7 | 70.8 | 48.0 | 6.6 | | 2.00 | 19% |
| 302,560 | mesh | **after** | **33.0** | 11.6 | 1.9 | 5.3 | | 1.97 | 19% |
| 1,008,160 | mesh | #89 (best) | 479.8 | 241.5 | 166.2 | 18.8 | | 5.78 | 22% |
| 1,008,160 | mesh | **after** | **110.8** | 38.7 | 6.3 | 18.4 | | 5.68 | 18% |

Every `full` run found every clash (158,400, 528,000 and 1,760,000) of every pair with 15 booleans, not truncated,
and listed 10,000 as a summary. The `mesh` runs had 4 definitions and as many instances and part properties as
occurrences. Properties took 2.6, 9.2 and 28.9 s and meshing 1.3, 3.8 and 14.6 s, unchanged from #89 (32.8 and
15.9 s at 1M).

- **The 1M shipped build: 515–566 s → 174 s. The 1M mesh path: 480 s → 111 s.**
- **The reply at 1M: 312.2 MB → 1.69 MB**, encoded in 0.012 s where it took 2.2 s. The same 1.69 MB at every size,
  because every size lists 10,000.
- **Peak memory barely moved (5.44 → 5.34 GB).** The copies discarded were freed as they were made; what is held is
  a million located shapes and their Python wrappers.
- ‼️ **The interference check is now 69% of the 1M build** (121 s of 174 s), unchanged by this work: 111–121 s
  before and after, at 37% and 20% CPU.

## Fences

`go test ./internal/domain/cad -run 'TestBuildOf_|TestKernel_'` with `FORGE_CAD_PYTHON` set, and
`go test ./internal/agent -run 'TestInterference_|TestRender_'`. New:

| fence | holds | measured |
|---|---|---|
| `TestKernel_AListCutToItsBoundIsTheWorstAndSaysHowManyThereWere` | `testdata/interference_list_limit.py`: bound far above, exactly at, one below and at 7. Same count and pairs every time; listed and summarized as expected; a cut list is the whole list's head, in order; the fixture has tied fractions | 51 clashes; reply 7,367 bytes whole, 1,490 at 7 |
| `TestKernel_AReplyWithMoreClashesThanItListsCountsThemAll` | through Go at the shipped bound: 150 blocks 0.02 mm apart, every pair clashes. 11,175 found, 10,000 listed, summarized, not truncated, fractions 0.998 → 0.796 | 149 booleans, 11,026 reused |
| `TestBuildOf_AListShorterThanItsCountIsASummary` | Go never believes a count below the list, and a short list is a summary even without the flag | 6 cases |
| `TestInterference_ASummarizedListSaysHowManyWereFound` | the turn says "found N … lists the M", "and N−1 more" counts from N, a summary never reads as "not known to be clear", and a whole list is not called a summary | |
| `TestKernel_AClashSlidAlongAPrismIsTheClashMeasuredAgain` | `testdata/interference_prisms.py`, 4 seeds, cache off (every pair its own boolean) against on: the same pairs, the same clashes, volumes to 1e-4 (see the noise note) | 275 parts a seed; 585–611 clashes of 649–677 pairs; 394–404 pairs reused |
| `TestKernel_CollarsAlongAShaftAndCleatsAlongAGirderPayForOneBooleanEach` | 193 collars round a turned 4 m shaft (2 half over the ends), 98 cleats on an extruded L girder (2 over the ends), 95 leaning pins: all 386 found, at most 7 booleans, end clashes half the inside volume, leaning pins one volume | 7 booleans, 379 reused |
| `TestKernel_APlacedCopyIsTheCopyBuild123dMade` | `testdata/placed_copies.py`: 7 shape kinds × 8 matrices (quarter turns, −0.0, random) + a cut and an uncut plate, in `""`, `mesh` and `step`. Build123d's path against the sidecar's: same class, attributes, own (not shared) mutable attributes, placement to the bit, B-rep shared with its own definition, same bounds, interferences, mesh, part properties, STEP body; volume to 1e-9 | 174 solids, 0 differences |
| `TestKernel_PlacingMoreCopiesCopiesNoBRepsAndBuildsNoMorePlanes` | a count, not a time: 8× the occurrences asks build123d for no more B-rep copies, `Plane`s or volume integrals than 1× | build123d: 63 → 455 copies, 67 → 459 planes, 58 → 450 integrals. Sidecar: 4, 16, 9 at both |

Extended: `TestRender_CarriesHowMuchTheCheckCovered` also carries `Found`.

The whole packages, 16:48–16:59 on this branch's code: `go vet ./...` clean; `internal/agent`, `internal/httpapi` and
`internal/domain/geometry` pass. `internal/domain/cad` passes except for failures known on Windows and not caused
here: `TestScript_*` (15), `TestKernel_ReturnsTheSurfaceOfTheSolidItBuilt`, and the scripted-part kernel tests
(`ARepeatedScriptedPartRunsItsScriptOnce`, `ARepeatedScriptedPartIsBuiltEveryTime`, `AScriptedPartIsExportedAndMeshed`).
Every fence named above passed in that run.

‼️ `TestKernel_APlacedCopyIsTheCopyBuild123dMade` first reported 345 differences, and every one was the fence's own
mistake. It compared B-reps and attribute identity *across* the two runs, and each run builds its own definitions,
so nothing could ever match. It now compares each copy with its own run's definition. The attribute check's first
form could not have gone red at all.

## Drills

`scripts/drill-fences.sh`, section "Next scale walls", run through a temporary runner with the V2 section "A check
that covered part of the model says how much", 16:42–16:48. Dry run first: 0 anchors moved. **23 went red, 0
stayed green, 0 unproven**, and the tree was byte-identical afterwards (`data/drills.log`). One V2 anchor was
re-pointed, because the render line now carries `Found`.

| drill | red fence | went red on |
|---|---|---|
| a list is cut without saying how many there were | AListCutToItsBound… | one below the bound: found 50 of 51 |
| a cut list says it is whole | AListCutToItsBound… | listed 50, summarized=false |
| a cut list keeps the first found, not the worst | AListCutToItsBound… | the whole list is not worst first at 12 |
| Go believes a count smaller than the list | BuildOf_AListShorter… | found 1 for a list of 3 |
| Go reads a list shorter than its count as whole when the flag is missing | BuildOf_AListShorter… | found 10, summarized=false |
| a summarized list reads as a whole one in the turn | ASummarizedListSays… | no "found 11175 pairs" |
| "and N more" counts only the list | ASummarizedListSays… | no "and 11174 more" |
| the render drops how many were found | Render_CarriesHowMuch… | the sheet says 0 found, the kernel 11,175 |
| a cylinder slides across its round section | AClashSlidAlongAPrism… | a chord pin reused 15.70 mm³, measured 14.98 |
| a cone is taken for a cylinder | AClashSlidAlongAPrism… | a ring reused 910 mm³, measured 5,615 |
| a cylinder's slab is not claimed | CollarsAlongAShaft… | 197 booleans |
| an extrusion's slab is not claimed | CollarsAlongAShaft… | 196 booleans |
| a located copy copies its B-rep again | PlacingMoreCopies… | 63 → 455 B-rep copies |
| a placement builds a Plane per occurrence again | PlacingMoreCopies… | 67 → 459 planes |
| the frame cache is keyed by part of the matrix | APlacedCopyIs… | 45 differences: a box turned the wrong way |
| a cached placement is not inverted | APlacedCopyIs… | 355 differences, bounds elsewhere |
| a located copy shares its definition's attributes | APlacedCopyIs… | 492 differences |
| the assembly integrates every copy again | PlacingMoreCopies… | 58 → 450 volume integrals |
| the assembly volume forgets all but the last copy | APlacedCopyIs… | volume 28,800 against 93,534 |

Also red, V2, re-run beside them: a truncated check reads as a clean one; truncation is not what the note is about;
a part that was never built is never mentioned; the render drops how much was checked.

‼️ Two of these would have stayed green on the fences' first versions. The attribute-sharing drill was only visible
once the placed-copies fence compared each copy with its own run's definition (see Fences). The cone drill was only
visible because the fixture puts rings at different heights on a real cone; a cone with a ring at one height shares
one volume either way.

## What this establishes

- **What a 1.76 M-clash reply cost before the bound**, measured end to end: 312 MB, 2.2 s to encode, 2.5 s and
  921 MiB to decode in Go, 768,000 buried problems (108 MB) handed to a repair.
- **A reply now lists at most 10,000 clashes and always counts them all.** A list cut to the bound is the head of the
  whole list in the old order, and says it is a summary in the reply, in `cad.Build` and in the turn. A summary is
  never read as a truncated check, and a truncated check is never read as a summary. Fenced at 51 and 11,175 clashes,
  and drilled.
- **A clash slides along a cylinder's length and an extrusion's depth, and nowhere else.** On four randomized fixtures
  with end, near-margin, off-axis, turned, leaning, chord, cone and short-extrusion cases, every reused volume matched
  every-pair measurement to 1e-4. On a shaft and a girder, 386 clashes cost 7 booleans where they cost 197 before.
- **OCCT's common volume of a curved clash is reproducible to the bit, stable to 1e-10 when slid, and moves up to
  1.7e-5 when the pair moves rigidly.** Measured on one pair (`data/boolean-noise.txt`).
- **The shapes and assembly phases were linear before this change** (k ≈ 0.99 from 90k to 1M), with the time in four
  build123d calls per occurrence rather than in K1's cache, garbage collection or the compound.
- **The sidecar's placed copies are build123d's**, in class, attributes, B-rep sharing, placement to the bit, bounds,
  mesh, part properties, interferences and STEP body, and its volume matches `Compound.volume` to 1e-9. Placing 8×
  the occurrences asks build123d for no more B-rep copies, `Plane`s or volume integrals. Fenced as counts, and
  drilled.
- **Before and after at 90k, 300k and 1M**, one run a size with load recorded: the full build 4.1×, 3.8× and 3.0–3.3×
  faster, the mesh path 4.2×, 4.3× and 4.3× (see Before and after). Counts behind it, which load cannot move: B-rep
  copies, `Plane`s and volume integrals no longer grow with the occurrences, booleans stay 15, and the listed
  clashes stay 10,000.

## What this does NOT establish

- **A 10,000-line repair is a good repair.** The bound takes the barrel's repair prompt from 768,000 buried problems
  to at most 10,000. That is still a prompt no model should be sent, and `repairGeometry` has no bound of its own.
  Not measured, and not changed here (see Recommendations).
- **The best list bound.** 10,000 is a chosen count, above every fence's 2,400, priced at the 1M reply's measured
  177 bytes an entry. No reader was measured at 10,000.
- **Sub-assembly ordering past the bound.** A look group whose only clashes rank below the 10,000th is no longer put
  first. Read from `look.go`, not measured on a model with that many clashes.
- **What sliding along prisms saves on the barrel.** Its cylinders are rivets, 14 mm long, with nothing inside their
  length. The prism slide is fenced on shafts and girders, not measured on a real model that has them.
- **Every prism.** A cone, a sphere, a revolve, a sweep (even a straight one), an imported STEP part, and a cylinder
  turned about its own axis are not slid. Each is keyed by its full pose, as before.
- **Browser draw.** Still not in this branch.
- **Memory on the cluster.** One Python process's peak working set on Windows, not the arm64 Linux image or a pod
  limit.
- **STEP at 1M.** Still refused before the kernel (owner decision). Not run.
- **Timing variance.** One run a size, on a shared laptop, load recorded per run. The counts (B-rep copies, planes,
  volume integrals, booleans, clashes, bytes) are load-free; the seconds are estimates.

## Recommendations (nothing here is changed)

1. **Bound what a repair is sent, in `repairGeometry`.** The list bound takes the barrel's buried problems from
   768,000 (108 MB of prompt lines, measured) to at most 10,000, and 10,000 lines is still no prompt to send a
   model. The worst few, with a count of the rest, is the shape the turn's note already uses. Basis: the measured
   prompt size above. The right number was not measured.
2. **Let the mesh endpoint and the STEP export skip the interference list.** Neither reads it, and at 1M Go spent
   2.5 s and 921 MiB decoding it inside `cad.Build`. The bound already shrinks that to at most 10,000 entries, so
   this is only worth doing if a path ever needs the unbounded list back.
3. **Keep `FORGE_GEOMETRY_MAX_OCCURRENCES` at 100,000, the 4,096 build and export ceilings, and the 2,000-boolean
   budget.** Nothing measured here is a basis for moving any of them. Browser draw is still unmeasured, and STEP at
   1M stays refused.
4. **Treat a reused curved clash as accurate to about 2e-5, not to the bit.** Measured: OCCT's common volume of one
   skew-cylinder clash moves up to 1.7e-5 under a rigid motion of the pair. That is far below what the turn prints
   (whole mm³ and whole percent). Any future fence on curved reuse should compare above that floor, as the prism
   fence does.
5. **Extend the slide to a straight sweep or a revolve only with a measured need** and its own proof. Neither is a
   prism cut by a slab as `_shape` builds it today.
6. **The next wall at 1M is the interference check: 121 s of 174 s.** Box tests and booleans are already linear
   and bounded (#94); what is left is per pair: 2,191,348 pairs, each keyed by `_pair_key` in Python (two
   `Location` inverses, two poses, containment on both frames) and 15 of them measured. That is an inference from
   the phase split and #94's counts, not a profile of the check. Profile it before changing it.
7. **Then `_located` and `_placement` (39 s at 1M).** They are now mostly build123d's `Location` and attribute
   deep-copy in Python. Placing copies without a Python shape object each would change what every reader of
   `built` receives, and needs its own equivalence fence.

## Re-running

```bash
export GOWORK=off D=/some/dir PY=.cadvenv/bin/python
# Requests, as #89 wrote them.
FORGE_SCALE_MEASURE_OUT=$D FORGE_SCALE_MEASURE_BAYS=9,30,100 go test -count=1 -timeout 30m \
  -run TestScaleUp_MeasureAirframeBarrel ./internal/domain/geometry
# Freeze the sidecar to measure, so edits in the tree cannot reach a run.
mkdir -p $D/frozen && cp internal/domain/cad/sidecar.py $D/frozen/sidecar.py
# Where the shapes and assembly phases go, one size at a time.
$PY docs/spikes/2026-09-15-next-scale-walls/profile_phases.py $D/frozen/sidecar.py $D/barrel-100.json $D/profile.jsonl 1M
# The whole build, with the reply's size and encode, and the reply written for Go.
$PY docs/spikes/2026-09-15-one-million-occurrences/measure.py --dir $D --bays 9,30,100 --modes full \
  --repeat 1 --cap 2400 --sidecar $D/frozen/sidecar.py --write-reply
$PY docs/spikes/2026-09-15-one-million-occurrences/measure.py --dir $D --bays 9,30,100 --modes mesh \
  --repeat 1 --cap 2400 --sidecar $D/frozen/sidecar.py
# Go's decode of each reply-full-<bays>.json; writes go-reply-*.json beside it.
FORGE_SCALE_MEASURE_REPLY=$D go test -count=1 -run TestScaleUp_MeasureTheInterferenceReply ./internal/domain/cad
# The fences.
FORGE_CAD_PYTHON=$PY go test -count=1 -run \
  'TestBuildOf_|TestKernel_(AListCutToItsBound|AReplyWithMoreClashes|AClashSlidAlongAPrism|CollarsAlongAShaft|APlacedCopyIs|PlacingMoreCopies)' \
  ./internal/domain/cad
go test -count=1 -run 'TestInterference_|TestRender_' ./internal/agent
```

For the "before" rows, freeze `internal/domain/cad/sidecar.py` as it is at #106's head
(`git show origin/kernel/step-export-scaling:internal/domain/cad/sidecar.py`) and run the same commands.
`measure.py`'s new `--sidecar` and `--write-reply` flags work on either.
