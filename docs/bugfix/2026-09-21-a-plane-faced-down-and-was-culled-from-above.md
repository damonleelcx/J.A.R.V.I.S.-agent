# A plane faced down in the kernel, and was culled from above in the viewport

**Found:** 2026-09-21, reading `_shape` after PR 167 turned the cone the right way up. The line below the cone's had
the same `Plane.XZ` in it.
**Severity:** high on screen, and in every STEP file with a plane in it. A plane is a ground, table, datum or
reference surface; the default and "top" cameras look down on the model, and that is the one view it was not drawn in.
**Owners:** all three builders of a plane — `internal/domain/cad/sidecar.py`, `internal/domain/geometry/mesh.go`,
`internal/httpapi/assets/forge3d.js`.

## What was measured, before the fix

A plane part, width 10 and depth 4, at the origin. build123d 0.11.1, node 20, `go test`.

| builder | declared normal | winding, `(B-A)x(C-A)` | agrees with itself? |
|---|---|---|---|
| kernel, `sidecar.py _shape` (`Plane.XZ * Rectangle`) | **-Y** | **-Y** | yes |
| exporter, `mesh.go func plane` | **+Y** | **+Y** as emitted | yes — see below |
| browser, `forge3d.js planeGeometry` | **+Y** | **-Y** | **no** |

Kernel, verbatim: `mesh_definitions[0].normals` = `[0,-1,0]` per vertex, `vertices`
`[-5,0,-2, -5,0,2, 5,0,-2, 5,0,2]`, `triangles` `[3,1,0, 3,0,2]`, whose cross products are both `(0,-40,0)`.
Browser, verbatim: `positions` `[-5,0,-2, 5,0,-2, 5,0,2, -5,0,2]`, `normals` `[0,1,0, ...]`,
`indices` `[0,1,2, 0,2,3]`, whose cross products are both `(0,-40,0)` against normals that say `+Y`.

### One correction to the brief this started from

`mesh.go` does **not** contradict itself. The corner order written in `plane()` is the -Y one, but every facet leaves
through `appendNonDegenerate`, which calls `orient()`, and `orient` swaps two corners of any facet wound against its
stated normal. Measured, not read: `plane(10, 4)` returns two triangles that declare `+Y` and are wound `+Y`. So the
exported STL and OBJ have always faced up, and this is the same fact recorded on
`docs/bugfix/2026-09-18-cylinders-cones-and-spheres-were-drawn-inside-out.md` on 2026-09-19 — `orient()` is the one
place winding is decided in that package. The exporter needed no fix. Its corner order was corrected anyway so that
nothing relies on the repair, and so that the three builders read alike.

## What it did

The browser's `plane` was wound face-down while its normals said face-up. The model pass runs with
`gl.enable(gl.CULL_FACE)` and `gl.frontFace(gl.CCW)` (`forge3d.js`), and the rasteriser believes the winding, so
**every plane was culled whenever the camera was above it** and drawn only from underneath, lit with a normal pointing
away from the viewer. Not "lit wrongly" — not drawn at all, in the view it exists for.

Where a CAD kernel answered, the browser shades a kernel mesh with the kernel's own per-vertex normals (PR 157/159).
Those said -Y, so the same plane was a coherent downward sheet: culled from above for the same reason, and lit from
below. The two paths were wrong in the same direction by different routes, which is why nothing looked inconsistent
enough to chase.

In the exported STEP the plane's face carried OCCT's -Y orientation, against the `+Y` every STL and OBJ of the same
part states.

## The decision: one-sided, facing +Y

Decided from the renderer, not from taste.

- **The viewport is one-sided and cannot cheaply be otherwise.** `CULL_FACE` is per-PASS GL state in `forge3d.js`,
  not per-batch; there is no material flag that would make one part two-sided without new plumbing through
  `_drawBatch`. PR 157's passes do not change this: the shadow capture and the ground composite disable culling for
  their own reasons (so a plane still casts its contact shadow either way up), and the model pass — the one that
  lights — has it on.
- **Even with culling off the back would be wrong.** The part fragment shader lights with the interpolated normal as
  it arrives: no `gl_FrontFacing`, no `faceforward`, no `abs`. A double-sided plane would be lit from underneath as
  if its top faced the viewer. An honest one-sided sheet beats a two-sided one lit from the wrong side.
- **Up is the side that is looked at.** A plane is a ground, table, datum or reference surface. The default hero
  camera and the "top" view both look down.
- **Up is what the rest of the vocabulary already means by up:** the box's +Y face, and a cone's `radius_top` at
  `+height/2` (PR 167).
- Nothing is lost from below. Picking is already two-sided — `nearestTriangle` tests `Math.abs(det) < 1e-12` with no
  sign test — so a plane is still selectable from underneath, and the contact shadow pass draws it either way.

The convention now lives in one place, `func plane` in `internal/domain/geometry/mesh.go`, with the reasons above
written beside it. The kernel and `forge3d.js` carry a pointer to it and nothing else.

## The fix

- `sidecar.py`, `_shape`, `kind == "plane"`: `Plane.XZ * Rectangle(width, depth)` →
  `Plane(origin=(0,0,0), x_dir=+X, z_dir=+Y) * Rectangle(width, depth)`. **Not** `Plane.ZX`, which is the cone's fix:
  `Plane.ZX`'s x is +Z and its y is +X, so it would face the right way and lay WIDTH along Z. A cylinder or a cone
  is round about its own axis and cannot see the spin; a rectangle can. The frame needed here is the one that turns
  +Z to +Y and leaves x alone, so width stays on X and depth runs along -Z, which a centred rectangle cannot tell
  from +Z. Measured after: normals `+Y`, both triangles wound `+Y`, bounds unchanged at `[-5,0,-2, 5,0,2]`.
- `forge3d.js`, `planeGeometry`: `indices` `[0,1,2, 0,2,3]` → `[0,2,1, 0,3,2]`. Positions and normals untouched, so
  picking, the level-of-detail proxy and the vertex count are unchanged.
- `mesh.go`, `plane`: the corner order rewritten as the box's +Y face flattened to `y = 0`, started at the corner
  that gives the quad the same diagonal `forge3d.js` splits on. The two emitted facets are now the same two facets,
  not merely two facets facing the same way. Output is otherwise unchanged (the same two triangles in the other
  order), because `orient()` was already producing them.

## Fences and drills

Three, one per builder, all written against the same sentence.

- `TestKernel_APlaneFacesUp` (`internal/domain/cad`, over `testdata/plane_up.py`): a 10 x 4 plane built four times
  through `_build` — upright, turned a half turn about X, laid a quarter turn about Z, and mirrored. Every mesh
  definition's vertex normals and both triangles' windings are `+Y` in the definition's own frame, every placed
  solid's B-rep face normal equals that copy's own up in world coordinates, and the upright copy's bounds are
  `[-5,0,-2, 5,0,2]` so a frame that faced the right way by spinning width onto Z is caught too.
  Drill **"the kernel builds a plane facing down"**.
- `TestAPlaneFacesUp` (`internal/domain/geometry`): `plane(10, 4)` and the same part through `Tessellate` — declared
  normal `+Y`, winding `+Y`, and 40 mm² of rectangle actually covered. Green on both sides of this change, and says
  so in its own comment: `orient()` had already been doing the work. What it holds is the declared normal, which is
  what `orient()` obeys. Drill **"the exporter's plane faces down"** turns that normal over and takes the winding
  with it.
- `TestTheRendererDrawsAPlaneFacingUp` (`internal/httpapi`): the shipped `forge3d.js` driven in node through the real
  `buildGeometry` dispatch — normals `+Y`, winding `+Y`, and then compared facet for facet with what
  `geometry.Tessellate` produces for the same part, so the two copies cannot drift apart while each stays internally
  tidy. Drill **"the browser winds a plane face-down"**.

## Why no existing fence could have caught it

Every winding fence in this system is a volume or a distance from a centre, and a plane has neither.

- `TestExport_EverySolidIsWoundOutward` computes each group's signed volume — and skips `plane` **by name**: "Not a
  solid: two triangles enclosing nothing. It has no inside to be turned out."
- `TestExport_EveryPrimitiveIsWoundOutwardInTheFile` requires every facet to face away from its part's centre. A
  plane's centre is on it.
- `TestRendererPrimitivesFaceOutward` requires a positive enclosed volume and no facet wound against its normal. Its
  fixture list is box, cylinder, cone, sphere, extrusion, revolve, gear: the volume half would fail on a plane, so
  only the second half is meaningful for one and the shape was never added.
- `TestKernel_AMeshCarriesSmoothNormalsAndKeepsHardEdges` asks a cylinder's normals to be radial or along its axis,
  which a flat sheet is not.

A zero-thickness face has no interior to appeal to, so the side it faces cannot be derived from its own geometry. It
has to be **named**, and then held to the name. That is the general lesson, and it is why the convention is now a
sentence in one place rather than three coincidences.

`TestExport_EverySolidIsWoundOutward`'s own doc comment carried a stale sentence that helped this live — "the
renderer ... draws with back-face culling off". It has not for some time. Corrected in the same change.

## Checked: nothing else was compensating, and nothing else has the same split

- **Nothing compensated.** There is no `shape == "plane"` normal flip and no zero-area special case anywhere in
  `sidecar.py`, `internal/domain/cad`, `mesh.go` or `forge3d.js`. The only plane-specific code is the shape
  dispatch, the "two triangles with no thickness" export note, the overlay extent, the dims sent to the kernel, and
  the rule that only a `plane` or a `section` may be thickened.
- **`thicken` was immune, not compensating.** `_thickened` calls `thicken(face, amount=thickness/2, both=True)`.
  Because it grows both ways the surface's orientation cannot change the result, which is why
  "a thickened plane" in `looks_kernel_test.go` was green throughout.
- **Readers of a plane's normal:** STL writes `t.Normal` verbatim and OBJ writes one `vn` per facet, both in stored
  order, so both stated the exporter's `+Y`. The section cut is a position test. Ambient occlusion rebuilds the
  normal from depth and forces it toward the camera. `featureEdges` compares adjacent facets and draws a plane's
  four open edges regardless. `smoothNormals` re-faces its result to the builder's declared normal and does not run
  on a plane in any case. The simplifier never sees one (`SIMPLE_MIN_TRIANGLES` is 48; a plane is 2). Picking is
  two-sided. So the visible consequences were the two above: culled from above, and lit from the wrong side when
  seen from below.
- **`section` is the only other zero-thickness shape, and it does NOT have this split.** The kernel builds it as a
  face in the part's own XY plane, orientation forward, normals and winding agreeing. `mesh.go` abstains entirely —
  `case "section"` returns no triangles and says why — so there is no third opinion to disagree. `forge3d.js` draws
  it through `extrusionGeometry(profile, 0, holes)`, whose cap loop emits a `+Z` set wound CCW **and** a reversed
  `-Z` set, each agreeing with its own winding: genuinely double-sided, and correctly so, because a loft station is
  looked at from both sides and has no "up". `lattice` is a thickened sheet solid, mesh-only, with normals derived
  from winding. There is no other sheet or single-face shape in the vocabulary.
