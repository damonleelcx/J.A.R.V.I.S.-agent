# Looks integration (PRs 154–158) — measurements, 2026-09-19

Branch `looks/integration`: origin/main + spline-outlines + kernel-vocabulary +
category-templates + mesh-only-parts + presentation, then four follow-ups.

## The car's bowed stations (follow-up 3)

Each of the 17 body stations used to be an eight-corner polygon with a rounded corner
at its widest point. It is now six corners with BOTH flanks drawn as one exact circular
arc (`via`, PR 154): the sill (0.85 × half width) to the shoulder (0.955 × half width),
bowed so the arc's outermost point reaches

    reach = half_width × (1 − carFenderFlare + flare(at)),  carFenderFlare = 0.025

with `flare(at)` a Gaussian of width `carFenderReach = 0.07` of the car's length centred
on each axle. So the waist is 97.5 % of the station's half width and the fender over a
wheel is 100 % of it — never wider than the width the model asked for. The bulge (share
of chord) is found by bisection on the arc itself (`carFlankBulge` → `arcReachX`),
because a tilted chord's arc reaches past its via; it is rounded to 1e-4 and written
into the outline as an expression over the car's own parameters, so a respec moves it.

The corner at the widest point is gone, and an arc's ends are left sharp by the outline
vocabulary, so the sill and shoulder corners carry no radius (the roof corners still do).

### The real kernel (build123d 0.11.1, FORGE_CAD_PYTHON, production's 30 s limit)

`go test ./internal/domain/cad -run TestKernel_BuildsTheCarTemplateWithNothingMissingOrBuried`
— 4 fixtures, 34.0 s total (contended laptop), all PASS:

| fixture | solids | interferences | buried | height asked / built | length asked / built |
|---|---|---|---|---|---|
| default | 14 | 0 | 0 | 1250 / 1256.3 (+0.50 %) | 4500 / 4500.0 |
| long-hood-gt | 14 | 0 | 0 | 1300 / 1312.5 (+0.96 %) | 4900 / 4900.0 |
| long-low-hypercar | 19 | 0 | 0 | 1000 / 1010.1 (+1.01 %) | 5200 / 5200.0 |
| short-tall-suv | 12 | 0 | 0 | 1950 / 1962.7 (+0.65 %) | 3900 / 3900.0 |

All inside the fence's tolerances (height within 2 %, length within 0.1 %), 0 skipped
parts, 0 feature failures, a STEP file each. Phases for the default car: shapes 64 ms,
features 442 ms, assembly 231 ms, interferences 1.74 s, export 329 ms.

The car's whole-model z extent is set by its WHEELS, not its body (tyre outside =
track/2 + tyre_width/2), so a width check on `Bounds` measures the wheels: the body's
width is fenced on the outlines instead
(`TestCar_TheFlanksAreExactArcsThatFlareOverTheWheels`).

### The refusal that is no longer reachable

`ExpandTemplates` refuses a car whose station outlines break an outline rule, naming
`edge_radius`. With only the roof corners rounded (and they turn very little), no car
inside the template's ranges trips it: probed over all 4 fixtures × nose_height
{0.15, 0.2, 0.3, 0.5, 0.95} × roof_width {0.3, 0.6, 0.95} × edge_radius {0, 0.02, 0.08}
× belt_height {0.3, 0.6, 0.9} = 540 cars, and every error raised was a different
refusal (ride height against the nose, no sides, edge_radius 0). The check stays as the
net under both the rounding and the bows, and its fence now drives it through
`carSectionProblems`, the seam it is read from.

## The Go exporter's winding (follow-up 4)

PR 157's bugfix doc reported, by reading, that `internal/domain/geometry/mesh.go`'s
`cylinder()` is wound inside out like forge3d.js was. Measured on the FILES instead:
every facet of an STL and an OBJ export of a turned cylinder, a tapered cylinder, a
cone, a sphere, a turned box and a mirrored cylinder has `(B−A)×(C−A)` pointing away
from its part's centre and agreeing with the normal the file states. 0 of 4,000+ facets
are inward in either format. `cylinder()` does list its side clockwise, but every facet
goes through `appendNonDegenerate` → `orient()`, which swaps two corners when the
winding disagrees with the stated outward normal. Nothing was changed in `mesh.go`; the
2026-09-18 bugfix doc carries the correction.

## Screenshots

NOT captured. The Browser pane in this session is shared with other agents' tabs and was
hidden, and a before/after of the viewport needs a `forged` + kernel run (Postgres and
the sidecar) to put a kernel mesh with `mesh_only` parts and `normals` on the stage. The
viewport changes are fenced through the WebGL stub instead
(`TestRendererDrawsMeshOnlyPartsAsMeshOnly`, `TestRendererShadesAKernelMeshWithItsOwnNormals`),
which reads back what reached the GPU per instance: geometry, material, colour and alpha.
