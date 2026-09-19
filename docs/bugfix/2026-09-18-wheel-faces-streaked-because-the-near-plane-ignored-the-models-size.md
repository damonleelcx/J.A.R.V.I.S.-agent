# Wheel faces streaked because the near plane ignored the model's size

**Found:** 2026-09-14 as a suspicion in the "looks designed" research ("near plane fixed at 0.05 … suspected z-fighting
streaks on wheel faces — NOT verified"). **Reproduced and proven** 2026-09-18 (looks, stage A4) in Chrome 152 (WebGL2 /
ANGLE / Intel UHD, 24-bit depth) through the shipped `forge3d.js` on two documents in millimetres before any fix; see
[`docs/spikes/2026-09-18-presentation`](../spikes/2026-09-18-presentation/README.md).
**Severity:** medium, visible on every design in millimetres larger than a hand tool. Nothing stored or exported was
wrong; only the picture.
**Owner:** the browser renderer (`forge3d.js Studio.draw`, the wheel/pinch zoom).

## Symptom

A car in millimetres, framed whole, showed ragged, striped edges where two faces lie close together: a rim 2.5 mm proud
of its tyre, a hub 6 mm proud of the rim, the ghost of a cut tool 25 mm outside the wheel it clears
(`internal/agent/testdata/finished-car.json`, a live model's car). Close-ups: `shots/a4-before-*-wheel.png`.

## Proof that the near plane is the cause

The same page, the same document, the same camera, with ONE change to origin/main's `forge3d.js`: the near plane
`0.05` replaced by `camera.distance × 0.05` (far plane untouched). The streaks vanish
(`shots/a4-only-near-changed-*-wheel.png` beside `shots/a4-before-*-wheel.png`, both documents). Nothing else in the
frame differs.

The arithmetic agrees. A perspective depth buffer of b bits resolves, at view distance z, about
z²·(f − n) / (f · n · 2^b). The car framed whole put the camera ~11,000 mm away (span × 2.4) with n = 0.05 and
f = 8 × 11,000: at the model that is **~140 mm** per depth step — every pair of faces closer than that fought for the
pixel. With the planes now taken from the model it is **~0.001 mm** (`TestRendererDepthResolvesTheModelAtAnyScale`
checks the framed car below 0.5 mm, and 600 random cameras round models 1 to 100,000 units across below span / 10,000).

## Root cause

`perspective(FOV_DEGREES, aspect, 0.05, Math.max(200, distance × 8))`: fixed numbers in a renderer that draws every
design in its own unit (millimetres, mostly). A near plane of 0.05 is right for a model a few units across and wasteful
by five orders of magnitude for a car in millimetres.

The same assumption sat in the zoom: the wheel and pinch clamped the camera distance to **0.4 … 400 units**, so the first
wheel tick on a car in millimetres (framed at ~11,000) jumped the camera to 400 — inside the body.

**Classification:** implementation defect (scale-blind constants). **Why it was not caught:** every viewport fence runs
in node against a stub context that has no depth buffer, and the earlier screenshots were of models a few hundred
millimetres across or of whole cars too small on screen for a 140 mm fight to read as more than a jagged edge.

## Fix

`clipPlanes(eye, target, bounds, explode)` (forge3d.js): the near plane is as far out as the eye can be without cutting
into anything drawn — the model's box, an exploded view's spread, the overlays — and never nearer than 1 % of the eye's
distance to its target, so a camera zoomed into a part keeps a usable ratio too; the far plane takes the whole model and
the floor grid. `zoomLimits(span)`: the zoom runs from span × 0.02 to span × 100.

## Fences and drills

- `TestRendererDepthResolvesTheModelAtAnyScale` — random cameras on models 1 … 100,000 units: no corner of the model
  clipped when the eye is outside it, depth step at the target ≤ span / 10,000; the framed presentation car ≤ 0.5 mm
  and its frame projected with those planes. Drills: "the near plane is 0.05 whatever the model", "the far plane stops
  at what the camera looks at", "the frame projects with a fixed near plane".
- `TestRendererZoomsInTheModelsOwnUnits` — one wheel tick moves a millimetre car's camera by the tick; the limits are
  span × 0.02 and × 100. Drill: "zoom stops at 0.4 and 400 units".

## Found alongside

Cylinders, cones and spheres were wound inside out, which is why the "before" wheels read as open tubes:
[`2026-09-18-cylinders-cones-and-spheres-were-drawn-inside-out.md`](2026-09-18-cylinders-cones-and-spheres-were-drawn-inside-out.md).
