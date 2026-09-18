# B3: bowed section outlines, measured (2026-09-18)

The kernel is build123d 0.11.1 / OCCT (`jarvis-k2b/.cadvenv`), run on Windows 11. `spike.py`
builds these directly in build123d. `formula.py` works out the closed forms.

## Lofts with curved stations (spike.py)

| loft | volume mm³ | valid | faces |
|---|---|---|---|
| 40x20 rectangle -> the same, top bowed through (0,25), h=30, smooth | 26013.830502 | yes | 6 |
| the same, ruled | 26013.830502 | yes | 6 |
| rectangle (4 edges) -> lens (2 arcs) | 18459.663 | yes | 8 |
| lens -> lens | 13200.704 | yes | 4 |
| rect -> bulged -> rect, 3 stations | 53381.896 | yes | 6 |
| bulged station drawn clockwise | 26013.830502 | yes | |
| bulged station starting at another corner | 26013.830502 | yes | |

OCCT (BRepFill_CompatibleWires) matches edge counts, direction and start point by itself.
Every combination tried built a valid solid, and the orientation and start point did not
change the volume. So no correspondence rule is refused. The contract tells the model that
stations may have different point counts, and that a body reads best when every station has
the same points in the same order.

## Against closed forms (formula.py)

- Lens (chord 40, sagitta 8 each way) x 10: formula 4400.234526059956, kernel
  4400.234526059954.
- Loft rectangle -> bowed top: `V = h(A0 + P/6 + S/3)` (derivation in
  `TestKernel_ALoftIntoABulgedStationBlendsExactly`) = 26013.830506, kernel 26013.830502,
  relative error -1.6e-10. The naive mean of the end areas, 26024.78, is 4.2e-4 high.

## Go measurement vs OCCT

The outline is (0,0), (40,0), then (0,30) bowed through (32,22). The arc reaches
x=40.037321, y=31.662321. Before this change Go's `profileExtent` measured the chords and got
x=40.000000, y=31.645983, which is 0.037 mm and 0.016 mm short. It now measures the exact curve
and agrees with OCCT's bounds to within 1e-3 (`TestKernel_GoMeasuresABowedOutlineWhereTheKernelDoes`).
