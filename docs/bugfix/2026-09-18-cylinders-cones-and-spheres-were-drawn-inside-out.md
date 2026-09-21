# Cylinders, cones and spheres were drawn inside out

**Found:** 2026-09-18 (looks, stage A), looking at why the gear fixture's hub read as an open tube. **Confirmed** in node
through origin/main's `forge3d.js` before the fix: the signed volume of every `cylinderGeometry` and `sphereGeometry` is
negative (a unit cylinder of radius 1, height 2: −6.257), and every one of a cylinder's 160 triangles is wound against
its own normal (a sphere: 992 of 1,024).
**Severity:** high on screen, nothing stored. The viewport culls back faces, so for every cylinder, cone and sphere the
stage drew the INSIDE of the far wall and cap, lit as if it faced the viewer: a wheel, hub, shaft, bolt shank or
headlight read as a hollow shell, and a cylinder standing on a plate showed the plate through its top. Boxes,
extrusions, revolves, sweeps and gears were wound correctly.
**Owner:** the browser renderer (`forge3d.js cylinderGeometry`, `sphereGeometry`).

## Root cause

The side quads were written (top θ, bottom θ, bottom θ+1) — clockwise seen from outside — the caps' fan the same way
round, and the sphere's quads likewise; each vertex's NORMAL points outward, so the lighting code looked right while
the rasteriser culled the outside. The cap `flip` flag was applied to the wrong cap for the same reason.

**Classification:** implementation defect. **Why it was not caught:** the W3 fences check every instance's matrix,
colour and the winding FLAG for mirrored copies, not the winding of the triangles themselves; nothing compared a
builder's triangle order with its own normals.

## Fix

The three index orders reversed (`cylinderGeometry` sides and caps, `sphereGeometry`). Positions, normals, counts and
therefore picking and the level-of-detail meshes are unchanged.

## Fence and drills

`TestRendererPrimitivesFaceOutward`: box, cylinder, cone, sphere, extrusion (with a rounded corner), revolve and gear each
enclose a positive volume and no triangle is wound against its normal. Drills: "a cylinder is wound inside out",
"a sphere is wound inside out".

## Not fixed here (another owner)

`internal/domain/geometry/mesh.go cylinder()` writes the side in the same order (topA, botA, botB) while its comment says
"counter-clockwise from outside" — found by reading, not measured: it is the order proven inward in the browser. An exported STL carries explicit outward normals, so most viewers are unaffected, but
a tool that trusts winding (a slicer computing volume, a mesh repair) sees it inside out. That package is outside this
stage; it is reported, not changed.

## Checked 2026-09-19: the Go exporter was NOT inside out (looks/integration)

Measured on the files, not read off `cylinder()`. The order above is written, but every facet
goes through `appendNonDegenerate`, which calls `orient()`: a facet whose winding disagrees with
its stated outward normal has two corners swapped before it is kept. So every STL and OBJ facet is
wound outward. `TestExport_EveryPrimitiveIsWoundOutwardInTheFile` (geometry) parses both files for a
cylinder (turned), a tapered cylinder, a cone, a sphere, a turned box and a mirrored cylinder, and
requires every facet's `(B-A)x(C-A)` to point away from its part's centre and to agree with the
normal the file states. It passes on main's code; with `orient()` bypassed it fails (drill "the
exporter keeps a facet wound against its normal"). Nothing in `mesh.go` was changed. The misleading
order in `cylinder()` is left as it is, because `orient()` is the one place winding is decided.
