# Kernel-built parts were placed twice in the browser

**Found:** 2026-09-13, while wiring kernel meshes to repeat copies in Phase 1 stage D1a
([`2026-09-13-repeat-copies-were-invisible-to-most-readers.md`](2026-09-13-repeat-copies-were-invisible-to-most-readers.md)).
Suspected from reading `forge3d.js`, then **confirmed against the real kernel and the real renderer**
before the fix.
**Severity:** high, visible, and silent. Every part drawn from the CAD kernel's mesh that was not at the
origin appeared somewhere it is not — moved again by its position and turned again by its rotation — in
the view a person uses to judge the model. The contact sheet the vision check reads was correct, so no
automated check disagreed with the screen.
**Owner:** the browser renderer (`forge3d.js` `draw`) — one transform rule applied to two kinds of
geometry with different frames.

## Symptom

A 20 mm box at x=100, turned 30° about z:

| | where it is | where the browser drew it |
|---|---|---|
| exporter / kernel / contact sheet | centre (100, 0, 0), turned 30° | — |
| primitive (no kernel) | — | (100, 0, 0), turned 30° ✅ |
| **kernel-built** | — | **≈ (186.6, 50, 0), turned 60°** — usually outside the frame |

Measured by the fence with the fix removed: *"the kernel-built part is drawn centred at
[186.60253882408142 50.00000000000001 0]; the exporter puts it at [100 -2.22e-16 0] — it was placed
twice"*.

## Root cause

The sidecar tessellates each solid **after** placing it (`_placement(s) * shape`), so the mesh arrives in
assembly coordinates — confirmed with the real kernel: that box's vertices span x 86.34 to 113.66, and an
unrotated box at x=100 spans x 95 to 105. `kernelGeometry` used those vertices as they came, and `draw`
applied `translation(position) · rotationXYZ(rotation)` to every part alike, primitive or kernel-built.

**Classification:** implementation defect in the kernel-built view (2026-09-08,
`plan-2026-09-08-solids-the-viewport-can-show.md`). **Why it was not caught:** the renderer's fences
compare *geometry* (outlines, tessellation, gears) with Go, never *placement*; the Go contact sheet —
the picture every live check looked at — draws the kernel mesh untransformed and was correct; and a part
at the origin with no rotation looks right either way.

## Fix

- `modelMatrix(part, displacement)` is the one place a drawn part's transform is decided. A primitive is
  turned and moved to its position; a part drawn **from a kernel mesh** gets only the displacement the
  view adds (an exploded view's gap, an assembly state's offset).
- `partsToDraw` marks `fromKernel` for a part carrying a mesh; `Studio.load` sets it from what was
  **built**, so a mesh this browser cannot index falls back to its primitive and is placed.
- Comments at the change link here.

## Regression fence

`TestRendererDoesNotPlaceAKernelMeshTwice` (node, runs in CI): a mesh already in assembly coordinates —
Go's own placed tessellation of the part, since CI has no build123d — must be drawn centred where the
exporter puts it, the same part as a primitive must be drawn there too, and a view displacement must still
move the kernel-built part.

Drill under "A kernel mesh is already placed" in `scripts/drill-fences.sh` — remove the kernel branch —
proven red 2026-09-13 with the output above (mutation checked to be valid JS first). Whole-suite drill dry
run: 0 anchors moved.

## Live check

In a browser, against the real renderer and the real kernel mesh of that box (a static page loading
`forge3d.js`, framed identically for both versions):

| renderer | page output | what is on screen | console |
|---|---|---|---|
| before the fix (D1a) | `drew 4 part(s)` | the kernel-built box sits well away from the primitive, turned differently | no errors |
| after the fix | `drew 4 part(s); kernel-built part fromKernel=true` | the kernel-built box sits exactly where the primitive is | no errors |
