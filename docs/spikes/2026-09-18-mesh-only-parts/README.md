# Mesh-only decorative parts (looks designed, stage E1), measured 2026-09-18

damon's decision, 2026-09-18: a part DECLARED mesh-only may break "every part is an
exact solid". Only `"shape": "lattice"` is mesh-only. Every other part is still an
OCCT solid that exports to STEP.

## Library

`manifold3d==3.5.3` (Apache-2.0, depends only on numpy). Wheels were checked with
`pip download --only-binary=:all:` for manylinux aarch64 (the production image is
python:3.13-slim-bookworm on arm64), manylinux x86_64 and macOS arm64, CPython 3.13
and 3.14. It is pinned in `internal/domain/cad/requirements.txt`, the one list the
image, `make cad-venv` and CI's `kernel` job install from.

What it does here: `Manifold.level_set` samples a triply periodic minimal surface,
thickened to a sheet (`w - |g| > 0`), on a grid of step `edge`. manifold's own
boolean then clips it to its box. None of this touches OCCT.

Not built: an octet strut lattice and a Voronoi foam. Each is a union of thousands
of struts, and nobody measured its cost or how often it fails. They are refused by
name rather than approximated.

## Wall thickness calibration (`measure.py calibrate`)

Near the surface, `|g| / |grad g|` is the distance to it. On the surface,
`|grad g|` averages about `k * G`, where `k = 2 pi / cell`. G was measured with G
forced to 1, on a 60 x 40 x 30 box. The wall was taken as 2 V / (sheet area).

| pattern | G at cell 10, t 1 | G at cell 20, t 2 | G used |
|---|---|---|---|
| gyroid | 1.514 | 1.508 | 1.51 |
| diamond | 1.422 | 1.431 | 1.43 |
| primitive | 1.285 | 1.334 | 1.31 |

With those factors, a wall of cell/10 comes out 3-7% thick (see `sizes.txt`). A
wall of cell/7.5 comes out 9-16% thick: gyroid 2.24, diamond 2.32 and primitive
2.19 where 2 was asked (`TestKernel_ALatticeSitsInItsBoxWithTheWallItAskedFor`
allows 20%). The lattice is decorative, and the contract says so.

## Triangles and build time (`measure.py sizes`, `sizes.txt`)

The box is 60 x 40 x 30 and the wall is cell/10. The sampling step is
min(cell/8, wall/1.5). Build is the time `_lattice_mesh` takes (sample, clip, read
out). The laptop has 16 logical CPUs and was at 2% load before the run. Other
agents' jobs were idle at that moment, but this is a single run and was not
interleaved.

| pattern | cell | triangles | build | Go estimate | at the 200,000 budget |
|---|---|---|---|---|---|
| gyroid | 30 | 23,342 | 0.07 s | 27,000 | built |
| gyroid | 20 | 74,048 | 0.26 s | 91,125 | built |
| gyroid | 15 | 171,588 | 0.55 s | 216,000 | refused by Go (the estimate errs high) |
| gyroid | 10 | 558,124 | 1.78 s | 729,000 | refused |
| gyroid | 7.5 | 1,304,820 | 4.64 s | 1,728,000 | refused |
| diamond | 20 | 92,406 | 0.29 s | 109,350 | built |
| diamond | 15 | 202,728 | 0.68 s | 259,200 | refused |
| primitive | 15 | 108,784 | 0.37 s | 172,800 | built |
| primitive | 10 | 394,560 | 1.33 s | 583,200 | refused |

Triangles go as C · V / (cell · edge²). The measured C is at most 38.9 for gyroid,
46.8 for diamond and 30.3 for primitive. Go's table uses 45, 54 and 36. That
estimate runs 1.15-1.7x above what was built, and never below it. Go refuses past
its estimate and names a cell that fits (`TestLattice_TheCellARefusalNamesFits`).
The kernel also counts what it actually built, and refuses a part past 200,000 by
name (`_LATTICE_BUDGET`, `TestKernel_ALatticePastTheKernelsOwnBudgetIsRefusedByName`).
All mesh-only parts in one design, with every placed copy counted, share 400,000
triangles.

Time is about 3.2-3.6 µs per triangle, so a lattice at the budget takes about
0.7 s. The wall must lie between cell/20 and cell/3. That keeps the sample count
at or below about the triangle count, so the time follows the budget.
