# Spike: exact instancing — one solid, placed many times, exported exactly

**Date:** 2026-09-13 · **Status:** done
**Feeds:** Phase 4 stages K1 (definition cache), K2 (XDE assembly export) and K4 (mesh per definition) in
[`docs/plan-2026-09-13-millions-of-parts.md`](../../plan-2026-09-13-millions-of-parts.md).
**Moved here in K0 (2026-09-14)** from a session scratchpad, so the numbers the plan cites can be re-derived by anyone.

## Summary

- **A placed copy shares the original's exact geometry.** A located build123d/OpenCASCADE solid points at the same
  underlying B-rep (`TShape`). It has the same volume, correct bounds and no duplicated geometry. An exact copy costs a
  placement, not a solid.
- **A flat `Compound(children=…)` is the wrong container for many copies.** In the STEP file it is cheap, about 615
  bytes per copy at 1k–10k copies, against 21.8 kB per copy when each copy is rebuilt. But building the compound grows
  much faster than the copy count: 0.14 s at 1,000 copies, 13.8 s at 10,000.
- **An XDE assembly (one product, N placements) builds in linear time:** 2.7 s for 1,000,000 placements.
- **Exporting a million placements to one STEP file does not fit the node:** 928 s, a 662 MB file and a 5.1 GB peak
  memory. 100,000 placements take 11.5 s, 64 MB and about 1 GB.
- **Placed copies share the definition's mesh.** The definition (7 faces, 120 triangles) is meshed once, and every copy
  reads the same triangles.

## Why this spike

The target decided for this work is a full car of ~30k parts, **exact everywhere** (no tessellated stand-ins), on the
same node under hard limits. That only works if an exact solid can be placed and exported many times without paying
the full cost for every copy. The spike asks whether it can, and at what size it stops working.

## Questions and method

| # | Question | Script |
|---|---|---|
| Q1 | Does a located copy share the B-rep (`TShape`) instead of duplicating it? | `share_and_export.py` |
| Q2 | Is the copy's geometry exact where it is placed (volume, bounds)? | `share_and_export.py` |
| Q3 | How do build time and STEP size grow with N for a flat compound of shared copies? For contrast, how do they grow when every copy is rebuilt (Q3b)? | `share_and_export.py` |
| Q4 | Does an XDE assembly (one product definition, N components) export differently? | `share_and_export.py` |
| A | Does the XDE path stay linear to 1,000,000, and what does export cost in time, bytes and memory? | `to_a_million.py` |
| B | Does a located copy reuse the definition's triangulation? | `to_a_million.py` |

- **Definition:** a 20 × 10 × 4 box minus a Ø4 cylinder (7 faces, 749.7345 mm³).
- **Placement:** copies on a square grid at 30 mm pitch.
- **Timing and memory:** `time.perf_counter` in one process. Peak memory is `ru_maxrss`.
- **STEP writers:** build123d's `export_step` for the flat compound, `STEPCAFControl_Writer` for XDE.

## Raw results

Full output, with only terminal colour codes removed, is in `data/`. OpenCASCADE's own transfer messages are kept there.

**Q1 / Q2**

```
definition faces=7 volume=749.7345
Q1 located copies share the TShape (exact, no duplication): True
Q1 IsPartner(base): True  IsSame(base): False
Q2 copy volume=749.7345 (base 749.7345)  copy bbox min=(90.00,-5.00,-2.00)
```

**Q3 — flat compound of shared copies**

| N | build s | export s | STEP bytes | bytes/copy |
|---:|---:|---:|---:|---:|
| 1 | 0.000 | 0.048 | 20,604 | 20,604 |
| 10 | 0.000 | 0.004 | 25,672 | 2,567 |
| 100 | 0.002 | 0.006 | 77,453 | 775 |
| 1,000 | 0.140 | 0.035 | 610,982 | 611 |
| 10,000 | 13.828 | 0.492 | 6,184,025 | 618 |

**Q3b — every copy rebuilt (no sharing)**

| N | build s | export s | STEP bytes | bytes/copy |
|---:|---:|---:|---:|---:|
| 1 | 0.003 | 0.004 | 20,611 | 20,611 |
| 100 | 0.228 | 0.065 | 2,086,181 | 20,862 |
| 1,000 | 2.235 | 0.870 | 21,790,633 | 21,791 |

**Q4 / A — XDE assembly (the A rows ran in a separate process)**

| N | build s | export s | STEP bytes | bytes/copy | peak RSS MB |
|---:|---:|---:|---:|---:|---:|
| 1 | 0.000 | 0.001 | 20,673 | 20,673 | — |
| 10 | 0.000 | 0.001 | 25,804 | 2,580 | — |
| 100 | 0.000 | 0.002 | 78,069 | 781 | — |
| 1,000 | 0.002 | 0.020 | 612,934 | 613 | — |
| 10,000 (Q4) | 0.023 | 0.251 | 6,192,977 | 619 | — |
| 10,000 (A) | 0.023 | 0.258 | 6,181,869 | 618 | 515 |
| 100,000 | 0.260 | 11.505 | 63,934,897 | 639 | 1,000 |
| 1,000,000 | 2.696 | 928.327 | 662,161,181 | 662 | 5,149 |

**B — shared triangulation**

```
meshing the definition took 0.0067 s
definition: faces with triangulation, triangles = (7, 120)
located copy (never meshed itself): (7, 120)
```

## How the conclusions follow from the numbers

- **Sharing (Q1).** The copies' `TShape` equals the base's. `IsPartner` is true and `IsSame` is false, which means
  same geometry at a different location.
- **Exact placement (Q2).** The copy's volume equals the base's to four decimals. Its bounds moved by exactly the
  placement: the base's min x of −10 plus 100 gives 90.
- **The file stores geometry once (Q3 vs Q3b).** With sharing, bytes per copy settle near 615, which is a reference and
  a placement per copy. Rebuilt copies cost 21,791 bytes each at N = 1,000, about 35× more, because every copy writes
  its geometry again.
- **Flat compounds build superlinearly (Q3).** From 1,000 to 10,000 copies, 10× more, build time rose from 0.140 s to
  13.828 s, about 99×. That growth comes from building the `Compound` itself, since export stayed small. So K2 must not
  use `Compound(children=…)` for occurrences.
- **XDE builds linearly (A).** 0.023 → 0.260 → 2.696 s, about 11× then 10× for each 10× in N.
- **XDE export is superlinear, and 1M exceeds the worker.** Export time grew 44.6× (10k → 100k), then 80.7× (100k → 1M).
  Peak memory went 515 → 1,000 → 5,149 MB. The plan gives the worker 2 GiB (stage K3), so one STEP file for a million
  placements does not fit it; 100k at ~1 GB does.
- **Mesh once per definition (B).** A copy that was never meshed reports the definition's triangle count, nonzero and
  equal.

## Caveats

- One run of each script, on one machine: macOS arm64, Python 3.14.6, build123d 0.11.1, cadquery-ocp-novtk 7.9.3.1.1.
  There are no repeats and no variance figure.
- One small definition (7 faces). A real part with thousands of faces raises the per-definition cost. The
  per-placement cost is what was measured here.
- Peak RSS is the process's high-water mark, and all three `A` sizes ran in one process, so each row is an upper bound
  that includes the rows before it.
- The 1M result shows that **one STEP file** of a million placements does not fit the node. It does not show that a
  million parts cannot be modelled. Split export and per-definition meshes are separate stages.
- The scripts are kept exactly as they ran, including a comment that corrects itself mid-sentence.

## Re-running

```bash
make cad-venv
.cadvenv/bin/python docs/spikes/2026-09-13-exact-instancing/share_and_export.py
.cadvenv/bin/python docs/spikes/2026-09-13-exact-instancing/to_a_million.py   # ~16 min, needs ~6 GB free memory
```
