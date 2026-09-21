# A truncated cone was built upside down by the CAD kernel

**Found:** 2026-09-21, measuring a 20/5 frustum's centre of volume against the frustum formula.
**Confirmed** against `origin/looks/integration` before the fix, for
`{"shape":"cylinder","radius":20,"radius_top":5,"height":10}` at the origin:

| measured | before | after | what it should be |
|---|---|---|---|
| centre of volume (`Kernel.BuildProperties`) | y = **+1.7857** | y = −1.7857 | −1.7857: the frustum centroid is h(R²+2Rr+3r²)/(4(R²+Rr+r²)) = 3.2143 mm from the **large** base, and the large base is DOWN |
| cross section 3 mm above centre | **907.92** mm² (r=17) | 201.06 mm² (r=8) | 201.06: radius_top is 5 |
| cross section 3 mm below centre | **201.06** mm² (r=8) | 907.92 mm² (r=17) | 907.92: radius is 20 |
| exported STEP, re-imported | centre y = **+1.7857** | y = −1.7857 | −1.7857 |
| volume | 5497.787 mm³ | 5497.787 mm³ | 5497.787 — identical either way up, which is the whole problem |

**Severity:** high, and it reached files. Every truncated cone and every true cone (`radius_top` 0) the kernel built —
and therefore every STEP file downloaded, every mass property, every centre of gravity, every interference check and
every `"top"`/`"bottom"` fillet selector on one — had the part end-for-end against what `mesh.go` drew on screen and
what the document contract says. A plain cylinder is unaffected: it is the same at both ends.
**Owner:** the kernel sidecar (`internal/domain/cad/sidecar.py`, `_shape`).

## Root cause

`_shape` builds a cylinder or cone with build123d's `Cone(radius, radius_top, height)`, which runs along **+Z** with
`radius` at −Z and `radius_top` at +Z, and then turned the local frame with `Plane.XZ`. **`Plane.XZ`'s normal is −Y**,
so it carries the primitive's +Z end to −Y: `radius_top` landed at the BOTTOM.

`internal/domain/geometry/mesh.go` (`func cylinder`) and `internal/httpapi/assets/forge3d.js`
(`cylinderGeometry`) both put `radiusTop` at `+height/2`. They are the contract, and the kernel disagreed with both.

**Classification:** implementation defect, present since the frame correction was written.

**Why it was not caught:** every orientation fence in the repository used a shape that cannot tell.

- `TestKernel_ACylinderPointsTheWayThisSystemDrawsIt` builds a plain cylinder (r 1, h 40) and reads its extent. A
  cylinder is identical end-for-end, so the fence is green with the frame turned either way. Its own comment says the
  drill that justified it removed the axis correction *altogether* — a 90° error, which the extent does see. A 180°
  error it cannot.
- `TestKernel_PlacesAPartWhereTheRendererDrawsIt` compares kernel and renderer **bounding boxes**. A 20/5 frustum's box
  is ±20 across and ±5 tall whichever end is up, so a cone would not have helped this fence either.
- Volume, triangle count and STEP byte count are all unchanged by the flip.
- `testdata/placed_copies.py` and `testdata/interference_prisms.py` both build cones, but both compare the kernel
  against the kernel (a fast path against build123d's own path, a cached clash against an uncached one). A reference
  that is flipped the same way agrees with itself.

## Fix

`Plane.XZ * body` → `Plane.ZX * body` for `cylinder` and `cone`. `Plane.ZX`'s normal is +Y, so `radius_top` lands at
+height/2 where `mesh.go` draws it. It also spins the local x round, which neither a cylinder nor a cone can see —
both are round about their own axis.

`Plane.XZ` is left where it is for the `plane` shape, which is **not** round about its axis: it maps width to X and
depth to Z, matching `mesh.go plane()` (measured: `Plane.XZ * Rectangle(10, 4)` spans 10 on X, 4 on Z, 0 on Y).

## Audit of every other frame turn in sidecar.py

| site | verdict | evidence |
|---|---|---|
| `_shape` cylinder/cone, `Plane.XZ` | **FIXED** → `Plane.ZX` | the table above |
| `_shape` plane, `Plane.XZ * Rectangle(width, depth)` | correct for extent | measured 10 mm on X, 4 mm on Z, 0 on Y — matches `mesh.go plane()`. See "Not fixed here" for its face normal. |
| `_shape` extrusion, `extrude(..., both=True)` | correct | no frame turn: the outline stays in the part's own XY and the extrusion is centred on local Z, so it is symmetric along its own axis |
| `_shape` revolve, `Axis.X`/`Axis.Y` | correct | no frame turn: the outline is already drawn in the part's XY plane and is turned about an axis in it |
| `_shape` sweep, `section_frame` | correct | the frame is decided in Go and obeyed here; nothing in the sidecar has an opinion about it |
| mirroring, `shape.mirror(Plane.YZ)` | correct | reflects x only: measured, a mirrored 20/5 frustum's centre stays at y = −1.7857 |
| `_slabs`, cylinder/cone | correct | only a shape whose `radius_top == radius` is claimed as a prism, and that shape is symmetric along its axis |
| `"top"`/`"bottom"` edge and face selectors (`Axis.Y`) | correct in themselves | plain world Y; they were selecting the physically wide end of a frustum only because the solid was upside down, and now agree with the drawing |

No text, engraving or embossing shape exists in this vocabulary, so there is none to audit.

## Everything else that reads a truncated cone

- **`internal/domain/geometry/overlay.go` (`localBox`, ~line 453)** — not compensating. It returns a box symmetric in
  every axis using `max(radius, radius_top)` and `height/2`, which is the correct bounding box for a frustum either way
  up. Unchanged.
- **STEP export** — the writer is handed the same solids `_shape` built, so it carried the flip and is fixed by the same
  change. Measured by round trip (table above).
- **The browser (`forge3d.js`)** — not compensating. `cylinderGeometry` puts `radiusTop` at `+half`, agreeing with
  `mesh.go`; the level-of-detail path at line 3625 uses `max(radius, radius_top)` for a bounding radius, which is
  orientation-free. Unchanged. The browser was right and the kernel was wrong, so a part looked correct on screen and
  arrived flipped in the downloaded file.
- **Kernel tests that build a cone** — `testdata/placed_copies.py` and `testdata/interference_prisms.py`. Both compare
  the kernel with itself, so neither changed behaviour; both re-run green.

## Fence and drill

`TestKernel_ATruncatedConesRadiusTopIsItsTop` (`internal/domain/cad/truncated_cone_kernel_test.go`,
`testdata/truncated_cone.py`). It builds the 20/5/10 frustum four times through `_build` — upright, turned a half turn
about X, laid down a quarter turn about Z, and mirrored — and for each asserts

- the **centre of volume** against h(R²+2Rr+3r²)/(4(R²+Rr+r²)), placed along that copy's own axis, and
- the **area of the cross section** cut at ±3 and ±4.9 mm along that copy's own axis against πr(t)², with r(t) read off
  the contract (radius at −height/2, radius_top at +height/2).

Both are measured in each copy's OWN frame, so all four owe the same four numbers and a copy that came out end-for-end
cannot borrow another copy's turn to hide in. A last check refuses a fixture whose two ends have stopped differing, so
the frustum cannot quietly decay into a cylinder that would satisfy everything either way up.

Drill: **"the kernel builds a truncated cone end-for-end"** — puts `Plane.XZ` back. Proven red:

```
✅ went red      — the kernel builds a truncated cone end-for-end
   upright: centre of volume [1.8e-15 1.785714285714286 5.7e-16], want -1.7857142857142856 on axis 1.
```

## Not fixed here (reported, another owner)

The `plane` shape's **face normal** disagrees between the kernel and the two renderers. The kernel's mesh for a
`plane` part carries per-vertex normals of `(0, −1, 0)` (measured: `mesh_definitions[0].normals`), because `Plane.XZ`'s
normal is −Y; `mesh.go plane()` and `forge3d.js planeGeometry()` both declare `(0, 1, 0)`. The extent is identical, so
nothing is in the wrong place — only which side of a zero-thickness drawing aid is lit. It is left alone here because
the repo has no settled answer to point at: `mesh.go`'s own quad winding for `plane` is −Y while the normal it declares
is +Y, so "fixing" the kernel to +Y would pick a side that nothing fences. Same family of cause, different question.
