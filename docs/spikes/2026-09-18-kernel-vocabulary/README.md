# Measurement: the kernel half of "looks designed" — fillets, edge rules, shells, perforation, tessellation

**Date:** 2026-09-18/19 · **Status:** done, one shared Windows laptop · **Base:** `origin/main` 82c9e55
· **Decision this serves:** damon, 2026-09-18 — "looks designed" is a FORGE goal; every part not declared
mesh-only stays an exact OCCT solid that exports to STEP; the defect check stays closed to styling.

## ‼️ What the machine was doing

Other agents were running heavy builds on the same laptop throughout: host CPU 30-100% before each run
(recorded in every row of `data/*.jsonl`). The laptop also slept once mid-run (a Go test reported a 34 h
"duration"). Every comparison below is **interleaved** (alternating order, a fresh process per run where
noted) and read as a ratio, never as an absolute time. Seconds vary 3-10x between rounds of the same thing.

## B6 — perforation: the boolean cost

[`holes.py`](holes.py), [`holes_run.sh`](holes_run.sh): a 1000×5×600 mm panel with N holes of R3 (smaller
when N is large) on a grid, cut **one tool at a time** (what `_apply` did) or as **one boolean with every
tool** (`Shape.cut(*tools)`). Volumes checked against `W·H·D − N·π·r²·H` in every run: all exact.

| N | one at a time | one boolean |
|---:|---:|---:|
| 100 | 1.96, 2.02 s | 0.12, 0.11 s (uncontended); 0.25-0.28 s (contended) |
| 1,000 | **288.4 s** (one run; the second was not waited for) | 1.95, 1.61 s; 4.5-8.1 s contended |
| 5,000 | not run (quadratic: hours) | **40.5-45.7 s**, 3 runs |
| 2,000 | — | **4.14, 3.98, 4.25 s** (1,000 interleaved: 1.55, 1.62, 1.59 s; host 32-43%) |

So: the kernel now cuts a multi-tool `cut` as one boolean (the 1,000-hole case went from past the 30 s
build limit to seconds), and one `cut` may consume at most `geometry.MaxCutTools` = 2,000 tools, refused by
name past it (5,000 is past the build limit even as one boolean). A chain of several cuts on one part is
not budgeted as a whole; it is still bounded by the build timeout and reported by it.

## A5 — tessellation

Findings, both measured with build123d 0.11.1 ([`tess.py`](tess.py), [`remesh.py`](remesh.py)):

1. **There WAS an angular limit** — build123d's `tessellate()` defaults to 0.1 rad — but it was implicit and it
   binds: a cylinder of R5, R50 or R500 is 500 triangles at every deflection from R/1000 to R/10. The research
   note's "no angular limit" was wrong; the real problem was the next one.
2. **The budget search never coarsened anything.** `Shape.mesh()` meshes only "if none exists": it keeps any
   triangulation already finer than asked. A box with a bore: 520 triangles at deflection 0.01, **still 520**
   when asked again at 10 with 1.0 rad, 68 once `BRepTools.Clean` runs first. So every "coarser" retry got the
   first try's mesh back; a model over the 400,000-triangle budget failed after six identical tries while
   `mesh_simplified` said it had been simplified. Fixed: clean then mesh on every try, and coarsen the angle
   with the deflection (to at most 45°). Fence `TestKernel_AMeshOverTheBudgetIsReallyCoarsened`
   (ten cylinders under a 1,500 budget: 5,000 → 800 triangles, angle 0.1 → 0.625 rad).
3. The angle is now `_MESH_ANGLE = 0.1` rad explicitly, sent back as `mesh_angular` (and `angular` in the
   mesh endpoint).

**Normals.** Each vertex now carries the surface's own normal at that node (OCCT
`BRepLib_ToolTriangulatedShape::ComputeNormals`), rounded to 4 decimals. Vertices already belong to one
face each, so a hard edge keeps split normals and a curved face shades smoothly.

Reply size and time at 4,096 parts ([`mesh_one.py`](mesh_one.py), [`mesh_run.sh`](mesh_run.sh), fresh
process per run, 3 interleaved rounds, `data/mesh_compare.jsonl`):

| case | main | branch, normals off | branch, normals on |
|---|---:|---:|---:|
| 4,096 copies of 16 shapes (43,070 triangles) — bytes | 2,715,111 | 2,715,176 | **3,279,956 (+20.8%)** |
| same — mesh phase s, rounds 1/2/3 | 4.12 / 0.95 / 0.79 | 4.11 / 0.69 / 0.58 | 6.77 / 0.78 / 2.64 |
| 256 distinct cylinders (128,000 triangles) — bytes | 7,861,423 | 7,861,492 | **10,344,435 (+31.6%)** |
| same — mesh phase s, rounds 1/2/3 | 16.7 / 3.9 / 13.8 | 14.5 / 2.3 / 11.6 | 16.4 / 2.8 / 10.3 |

Bytes are exact and repeatable. Time is inside the noise of this machine: normals-on is slower in 3 of 6
pairs and faster in 3; no claim is made either way. A first, unpaired attempt at **4,096 distinct**
cylinders (`data/mesh_size_first_attempt.jsonl`, 327,680 triangles) gave 20.6 MB → 27.3 MB (+32%) and
310 s → 407 s under 57-100% host load; it was stopped as too slow to interleave. `_MESH_NORMALS = False`
sends exactly the old payload.

## B2 — edge rules on known solids

[`probe2.py`](probe2.py): OCCT's `ChFi3d::DefineConnectType` on a 100×20×50 box (12 convex), an L extruded
30 deep (17 convex, 1 concave), a plate with 4 through holes (20 convex, 4 seams, 8 rims on inner wires) and a
rib fused on a plate (20 convex, 4 concave). These are the exact counts the kernel fence asserts.

## B4 — shells

`offset(solid, -t, openings=[...])`: a 100×60×40 box open at the top, 5 mm → 91,500 mm³ (formula
100·60·40 − 90·55·30); a cylinder R30 H80 open at the top, 4 mm → π(30²·80 − 26²·76) to 1e-9. **A shell with no
open face** returns one solid whose volume is the CAVITY's (135,000 for the box, walls are 105,000), so FORGE
refuses a shell with no open face.
