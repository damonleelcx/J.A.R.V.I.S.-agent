# Measurement: the kernel's last walls — part properties, placement, memory after an export, two fence gaps

**Date:** 2026-09-17 · **Status:** done, one shared laptop + the worker's Linux image · **Base:** `origin/main` f9a503b
· **Follows:** [`2026-09-17-one-million-after`](../2026-09-17-one-million-after/README.md) (#146),
[`2026-09-17-unverified-paths`](../2026-09-17-unverified-paths/README.md) (#145), #134's "Two fence gaps, not fixed here"

## Summary

| item | outcome |
|---|---|
| 1. Part properties ~50 s at 1M | **Taken.** A placed copy's box is read with the one OCCT call build123d makes, without its wrapper; its definition Cleaned once; its centre moved without a generator. **Properties ×0.43-0.58 in all six interleaved pairs, 1M: 46.9 → 20.4 s and 45.4 → 22.0 s**; mesh build ×0.77 / ×0.78 at 1M. Same answer: whole-reply and `part_properties` SHA-256 identical in every pair. Per-definition boxes moved by placement were measured and **not** taken: not the same bits, even for pure translations. |
| 2. Two shape-building wins #146 priced | **Taken, behind switches.** Shapes phase ×0.66-0.81 in all six pairs (1M: 37.5 → 26.4, 34.2 → 23.9 s). Full build ×0.82-0.93 at 90k and 1M; **no gain visible at 302,560** (×1.05, ×1.00 — the interference phase moved more than the shapes gain). Same answer, bit for bit, in every pair. |
| 3. Kernel memory not coming back after an export | **Found and fixed: glibc heap fragmentation.** After a 90,880-occurrence export with the request and reply dropped, glibc holds **681 MiB free** inside the heap and 127 MiB in use. `gc.collect()` finds 0 objects and returns nothing; **`malloc_trim(0)` returns it in 23-29 ms**. Through the real `main()` loop in the worker's image: idle RSS after each of four exports **1,266 / 1,352 / 1,364 / 1,364 MiB on main, 479 / 496 / 513 / 528 MiB with this change**. Also found: every export left an empty document open in the XDE application (fixed; same STEP bytes). |
| 4. `_located`'s `deep` fallback never exercised | **Fence added**, 3 drills red. |
| 5. `_containment_plan`'s reach has no single-copy drill | **It was a missing drill, not a missing fence**: the pair-key fence already goes red on it. 2 drills added, both red. |

No limit changed. `kernel.go` not touched.

## ‼️ What the machine was doing

The laptop was shared with other agents throughout (host CPU 14-69% across runs, in every row of `data/results-*.jsonl`,
with the five busiest processes at each start). Improvements are claimed only from **interleaved** before/after pairs
(main, branch, main, branch at each size, each run a fresh process), never from absolute seconds. The Linux memory
runs were in `forge-linux-test` (python:3.13-slim-bookworm, glibc 2.36, build123d 0.11.1 on cadquery-ocp-novtk
7.9.3.1.1 — the pinned requirements), `--cpus=1 --memory=6g`, amd64 under Docker Desktop's WSL2.

## The harness

[`measure_bounded.py`](measure_bounded.py) is #146's bounded harness unchanged except that each run is
[`measure_hashed.py`](measure_hashed.py): #89's `full` and `mesh` modes, plus a **SHA-256 of the whole reply less
`phases`** (sorted keys) and one of `part_properties` alone. Two sidecars with the same hashes gave the same answer to
every field and every bit. Bounds as #146's: 3,600 s cap per run, run killed past 80% host memory or 24 GB tree RSS,
no run started under 16 GB available (42 GB was free), load sampled every 5 s. [`interleave.sh`](interleave.sh) runs
main then this branch, twice per size. The sidecars measured are frozen copies: `origin/main` f9a503b and this branch's
code before the memory change (which does not touch a build). Host memory peaked at 52%; nothing was stopped.

## 1. Part properties

### Where the 50 s went

[`micro_box.py`](micro_box.py), 20,000 barrel occurrences, uncontended pass (the contended rerun in
`data/micro-box.txt` has the same shape, 2-4× slower): per placed occurrence `_box_of` **22-24 µs**, of which the OCCT
box (`BRepBndLib.AddOptimal`) is **6.3 µs** and `BRepTools.Clean` 3.0 µs. The rest is build123d's `BoundBox`: keyword
parsing, a Clean per call, three `Vector`s built and read back. `_properties` was ~30 µs an occurrence in all
(`_moved_point` 7.1 µs of it).

### Why not per-definition boxes moved by placement

The obvious O(definitions) answer — measure each definition's box once and move it — is exact in real arithmetic only
for rotations by multiples of 90° (V3: a turned box's moved box is looser than its envelope). **It is not the same bits
even there.** [`quarter_turn_boxes.py`](quarter_turn_boxes.py): boxes, cylinders, cones, spheres and an extrusion at all
24 proper quarter-turn rotations × 30 positions: **1,478 of 3,600 differ, by up to 9.1e-13 mm, including pure
translations of a box** (`data/quarter-turn-boxes.txt`). A primitive carries its own location, and OCCT composes the two
translations before it moves a vertex, so `(lo + t)` in Python is not OCCT's `v + (t - w/2)`. The part-properties fence
compares bounds bit for bit, so this was not taken. It would also not have helped the barrel: 1,136 of its 90,880
occurrences (1 in 80) have an axis-aligned rotation; the other 79 sector rotations are arbitrary turns about x.

### What was taken (sidecar.py, `_PROPERTIES_BOX_DIRECT`)

- the box read from the **placed solid**, as before, with the one call build123d makes (`AddOptimal_s(shape, box)`,
  build123d's default arguments, on the same `TopoDS_Shape`), a void box as zeros and any exception as `None`, as
  `_box_of` answers;
- `BRepTools.Clean` once per **definition** instead of once per copy: a copy's faces and edges are its definition's
  TShapes, placed, so after the first copy every later Clean found nothing to remove;
- `_moved_point` reading the same twelve entries and summing in the same order without a generator per call (7.1 → 5.5
  µs). Not switched: the same arithmetic, fenced to the bit against the old function.
- A part a feature changed (no placement) is read through `_box_of` exactly as before.

### Interleaved, `mesh` mode (mesh + part properties, check replaced by the grid), `data/results-*.jsonl`

| occurrences | pair | properties s main → branch | ratio | mesh build s main → branch | answer and properties hashes |
|---:|---:|---:|---:|---:|---|
| 90,880 | 1 | 2.6 → 1.2 | ×0.46 | 7.9 → 6.0 | identical |
| 90,880 | 2 | 2.6 → 1.3 | ×0.50 | 8.1 → 6.7 | identical |
| 302,560 | 1 | 9.2 → 5.3 | ×0.58 | 28.2 → 27.0 | identical |
| 302,560 | 2 | 15.7 → 7.0 | ×0.45 | 45.8 → 31.3 | identical |
| **1,008,160** | 1 | **46.9 → 20.4** | **×0.43** | 141.3 → 109.3 (×0.77) | identical |
| **1,008,160** | 2 | **45.4 → 22.0** | **×0.48** | 135.0 → 104.7 (×0.78) | identical |

Linear: properties k ≈ 1.03 from 302,560 to 1,008,160 on the branch (mean 6.2 → 21.2 s). Peak RSS unchanged
(6.12 GB at 1M). At 1M the mesh path is now shapes 25-29 s, properties 20-22, mesh 20-22, grid 20-22, assembly 10-12.

### Fence and drills

`TestKernel_PartPropertiesReadDirectlyAreTheSameBitsAsBefore` (`testdata/part_properties.py`, `as_today`): the whole
`part_properties` list must be **equal** to the path before this change (box through `_box_of`, centre through the old
`_moved_point`) on part_properties.py's fixture and on placed_copies.py's (every placed kind at quarter turns, -0.0
beside 0, random turns, a part a feature changed) at one and at eight copies. And the direct read must be the path
taken: only the part a feature changed goes through `_box_of`, and Clean runs at most once per definition (8 calls at
58 parts and at 450). The existing `TestKernel_APartsCentreMeasuredPerShapeIsItsSolidsCentre` still passes (against
measuring every solid).

| drill | red because |
|---|---|
| a placed copy's box is read from its definition | block-1's bounds `[-6, -2.5, -15, …]` shipped, `[-6, 7.5, -20, …]` before |
| a placed copy's box goes through build123d again (switch off) | 13 boxes read through `_box_of`; only the 1 part a feature changed should be |
| every placed copy is Cleaned again | 12 Clean calls for 6 definitions and 13 parts |
| a moved centre adds its translation first | ell-1's centre x -54.662406060734035 shipped, -54.66240606073403 before (last bit) |

## 2. The two placement wins #146 priced

- `_origin_point`: `gp_Pnt(x, y, z)` instead of `Vector(*position).to_pnt()` (#146: 0.8 vs 4.3 µs). build123d 0.11.1's
  `Vector.__init__` passes three ints or floats to `gp_Vec` unchanged and `to_pnt()` is `gp_Pnt(vec.XYZ())`, so the
  point holds the same three doubles. Taken only for exactly three ints or floats; anything else still goes through
  `Vector`. Switch `_PLACE_POINT_DIRECT`.
- the moved shape's cast looked up once per definition (`Shape.downcast_LUT[shapetype(...)]`, kept in the build's
  `plans` dict under `("cast", id(shape))`) instead of `downcast()` per copy (#146: 1.7 vs 4.6 µs). `Moved` never
  changes a shape's type. Switch `_LOCATED_OWN_CAST`.

### Interleaved, `full` mode (the build as shipped; part properties are not in this mode, so only item 2 differs)

| occurrences | pair | build s main → branch | ratio | shapes s main → branch | ratio | answer hash |
|---:|---:|---:|---:|---:|---:|---|
| 90,880 | 1 | 5.3 → 4.7 | ×0.89 | 2.0 → 1.5 | ×0.75 | identical |
| 90,880 | 2 | 5.4 → 4.7 | ×0.87 | 2.1 → 1.4 | ×0.67 | identical |
| 302,560 | 1 | 19.8 → 20.8 | ×1.05 | 8.1 → 6.5 | ×0.80 | identical |
| 302,560 | 2 | 27.7 → 27.8 | ×1.00 | 11.3 → 8.2 | ×0.73 | identical |
| **1,008,160** | 1 | 92.7 → 86.4 | ×0.93 | 37.5 → 26.4 | ×0.70 | identical |
| **1,008,160** | 2 | 90.3 → **74.4** | **×0.82** | 34.2 → 23.9 | ×0.70 | identical |

The shapes phase is faster in all six pairs, and the whole build in four; at 302,560 the interference phase (numpy,
untouched) moved by more than the gain (8.4 → 9.6 s and 12.0 → 14.5 s), so **no end-to-end gain is claimed at
302,560**. The shapes gain at 1M (10-11 s) is larger than #146's per-call arithmetic predicted (~6 s); the pairs ran at
50-63% host CPU and the micro-benchmark was taken at 24%, so read it as "about 6-11 s".

**Fence:** `TestKernel_APlacedCopyIsTheCopyBuild123dMade` already compares every placement against build123d's own to
the bit; it now also compares the Python class of each copy's `TopoDS` object. Drills: *a placement's origin loses its
last digits* (red: 346 differences, first the assembly's bounds -859.151036354307 against build123d's
-859.1510363544983), *a moved copy is not cast* (red: 171 differences, "box-0-0: its B-rep is a TopoDS_Shape,
build123d's a TopoDS_Compound").

## 3. Memory after an export

[`export_memory.py`](export_memory.py) runs the export job's request (format `step`, `skip_interferences`) the way
`main()` does, in one process, and reads VmRSS and glibc's `mallinfo2()` after each step. 90,880-occurrence barrel,
88 MB of base64 STEP per reply, main's sidecar (`data/export-memory-remedies.jsonl`):

| step (export 1 of 3) | RSS MiB | heap in use | heap FREE but held |
|---|---:|---:|---:|
| imported | 437 | 124 | 1 |
| after the build, `main()` idle (its `line`, `request`, `reply` still bound) | 1,292 | 242 | 566 |
| request and reply dropped | 1,185 | 127 | **681** |
| + `gc.collect()` | 1,183 | 127 | 681 (found **0** objects) |
| + `malloc_trim(0)` (**23-29 ms**) | **506** | 127 | 681 (the free pages are no longer resident) |

**It is fragmentation, not a leak:** memory in use after the export is back to 127 MiB (import: 124), and 681 MiB the
program freed stays in glibc's heap because live chunks sit above it. Python's collector has nothing to do with it.
Over six exports with the trim, RSS after each is 505, 524, 540, 555, 569, 583 MiB (≈ +15 MiB an export, with heap
in-use flat at 127 MiB); the source of that residue was not found (not the glibc heap in use, not Python-tracked
objects, not the documents below). #145's pod saw its peak level off over four exports; six here do not show whether
this residue levels off.

**The change** (sidecar.py, `_RELEASE_AFTER_REPLY`): after each reply is written and flushed, `main()` drops its
`line`, `request` and `reply` and calls `malloc_trim(0)` through ctypes. It only returns pages that are already free.
Looked up once; on anything but Linux with a `libc.so.6` that has it (musl, macOS, Windows) it does nothing.
Deploy image: `python:3.13-slim-bookworm`, glibc 2.36, the same as measured.

**Through the real loop** ([`sidecar_idle_rss.py`](sidecar_idle_rss.py): the sidecar as a child process fed the request
line, as the worker does; RSS read 2 s after each reply; `data/idle-rss.jsonl`):

| export | main: idle RSS MiB (peak) | this branch: idle RSS MiB (peak) |
|---:|---:|---:|
| 1 | 1,266 (1,375) | **479** (1,373) |
| 2 | 1,352 (1,545) | **496** (1,374) |
| 3 | 1,364 (1,545) | **513** (1,374) |
| 4 | 1,364 (1,551) | **528** (1,435) |

The peak is lower too after the first export (a build starts from a trimmed heap), which is what #145's 2 GiB worker
headroom was measured against.

**Also found: an XDE document leaked per export.** `_step_document` called
`application.NewDocument("MDTV-XCAF", doc)`. Through OCP that out-parameter is not written back: the call opened a
second, empty document inside the application and left `doc` (the one filled and written) unopened. `NbDocuments()`
was 1, 2, 3 … 6 after six exports and none could be closed (`Close` needs the handle; `GetDocument` has the same
out-parameter; `Close(doc)` raises "cannot close a document that has not been opened"). `InitDocument(doc)`, which is
what makes `doc` an XDE document, still runs. Removing the call: **the same STEP body, export for export** (below the
header; the body's hash changes from one export to the next in one process on both sidecars, identically), 0 documents
held instead of 6, and no measurable memory difference (`data/export-memory-documents.jsonl`) — a handle leak, not
the retention.

**Fence:** `TestKernel_TheKernelHandsFreeMemoryBackAfterEachReply` (`testdata/release_memory.py`): the sidecar's own
`main()` on a build, an unreadable line and two STEP exports; the release runs once after each reply, after it is
written, with the reply no longer referenced; it runs exactly where glibc has `malloc_trim` (Windows here: found
False, ran False, nothing raised; the same script in the worker image: found True, ran True on all four); the switch
turns it off; and the XDE application holds as many documents after two exports as before.

| drill | red because |
|---|---|
| the kernel keeps its free memory after a reply | 0 releases for 4 replies |
| the loop holds its reply while memory is released | release 1 ran while the loop still held its reply |
| an export leaves a document open in the application | documents 0 before, 2 after |

## 4. `_located`'s `deep` fallback (#134 gap 1)

No shape the sidecar builds carries an attribute the plan classifies as `deep`, so the fallback never ran.
`placed_copies.py`'s new `deep_fallback()` gives every definition `forge_trace = ["kept", {"n": 1}, <the shape>]` and
builds the fixture build123d's way and the sidecar's. `TestKernel_ALocatedCopyDeepcopiesAnAttributeThePlanDoesNotKnow`:
the fallback ran once per located occurrence (59/59), and on each of 57 copies the attribute equals build123d's, is a
fresh list and dict (never the definition's or another copy's), and its shape element is **the copy itself** (deepcopy's
memo). Drills, all red: *the deep fallback shares its definition's attribute* (212 differences, "forge_trace is its
definition's own object"), *the deep fallback forgets the copy it is making* (`deepcopy(value, {})`; 57 differences,
"forge_trace's shape is another object, not the copy itself"), *the plan assigns an attribute deepcopy would copy*
("the deep fallback ran 0 time(s) for 59 located occurrence(s)").

## 5. `_containment_plan`'s reach (#134 gap 2)

Doubling or halving the reach in `_containment_plan` alone turns `TestKernel_APairKeyWithoutLocationsIsTheKeyBuild123dGave`
red: "prisms seed 1: 6158 key(s) differ with the memo and the slide, 0 without the slide, 0 without the memo, 0 with
containment per pair" — the fence already compares the grouped path with containment per pair. The rail fence stays
green on it. So the gap was the drill, now added twice (*grouped containment takes a box's whole length for its reach*:
6,158 keys differ; *… a quarter of a box for its reach*: 9,822), against the pair-key fence only.

All 14 new drills and the one re-anchored (*a copy's centre stays where its shape was built*, whose anchor was
`_moved_point`'s old return) were seen red one at a time, tree restored byte-identical: `data/drills.log`.

## What this does NOT establish

- arm64, or the worker pod under Kubernetes: the memory runs are amd64 containers with `--memory=6g`, not the 2 GiB pod.
- Where the ≈15 MiB-per-export residue after a trim comes from, or whether it levels off.
- An idle-machine number: every timing row was contended; the claims are the interleaved ratios and the hashes.

## Reproduce

```sh
export GOWORK=off PYTHONUTF8=1
PY=C:/Users/damon/Downloads/agents/jarvis-k2b/.cadvenv/Scripts/python.exe
D=<dir with #146's barrel-9/30/100.json>   # TestScaleUp_MeasureAirframeBarrel, see #146's README
git show f9a503b:internal/domain/cad/sidecar.py > $D/sidecar-main.py; cp internal/domain/cad/sidecar.py $D/sidecar-after.py
bash docs/spikes/2026-09-17-kernel-last-walls/interleave.sh $D sidecar-main.py sidecar-after.py 9,30 full,mesh results-90k-300k.jsonl
bash docs/spikes/2026-09-17-kernel-last-walls/interleave.sh $D sidecar-main.py sidecar-after.py 100 full,mesh results-1m.jsonl
python docs/spikes/2026-09-17-kernel-last-walls/table.py
$PY docs/spikes/2026-09-17-kernel-last-walls/quarter_turn_boxes.py internal/domain/cad/sidecar.py
$PY docs/spikes/2026-09-17-kernel-last-walls/micro_box.py $D/sidecar-main.py $D/barrel-9.json 20000
docker run --rm --cpus=1 --memory=6g -v <this dir>:/h:ro -v $D:/d:ro forge-linux-test \
  python3 /h/export_memory.py /d/sidecar-main.py /d/barrel-9.json --exports 3 --remedy trim   # none|gc|trim|both
docker run ... forge-linux-test python3 /h/sidecar_idle_rss.py /d/sidecar-after.py /d/barrel-9.json --exports 4
```

2026-09-17, Windows 11, Intel Core i7-12650H (16 logical), 64 GB, Python 3.13.9, build123d 0.11.1,
cadquery-ocp-novtk 7.9.3.1.1, Go 1.26.5; Linux: forge-linux-test, Python 3.13.15, glibc 2.36.
