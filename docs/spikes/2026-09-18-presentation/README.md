# Presentation: light, materials, grounding, camera (looks, stage A)

**Date:** 2026-09-18 · **Branch:** `looks/presentation` · **Stage:** "looks designed", stage A (A1–A7), browser half only.

damon, 2026-09-18: "looks designed" is now a FORGE goal — FORGE's output should read as a designed product, not boxes
and sticks. This stage is the renderer's share of it, all in `internal/httpapi/assets/forge3d.js`. It changes how a
design is **shown**, never what it is: no shape, placement, export or document changes, and the defect check
(`internal/agent/look.go`, `sketch.go`) stays closed to styling.

## Before / after

Same fixed documents, same page, same canvas (960 × 600, one pixel per CSS pixel), both themes, each renderer's own
default view. Taken with [`shoot-all.html`](shoot-all.html) in Chrome 152 (WebGL2, ANGLE / Intel UHD). **Every picture is
PRIMITIVES ONLY and says so**: a static page has no CAD kernel; the workbench draws the kernel-built mesh whenever one
exists (A1 below).

| document | dark | light |
|---|---|---|
| car ([`data/car.json`](data/car.json), mm) | [before](shots/before-car-dark.png) · [after](shots/after-car-dark.png) | [before](shots/before-car-light.png) · [after](shots/after-car-light.png) |
| gear ([`data/gear.json`](data/gear.json)) | [before](shots/before-gear-dark.png) · [after](shots/after-gear-dark.png) | [before](shots/before-gear-light.png) · [after](shots/after-gear-light.png) |
| bracket ([`data/bracket.json`](data/bracket.json)) | [before](shots/before-bracket-dark.png) · [after](shots/after-bracket-dark.png) | [before](shots/before-bracket-light.png) · [after](shots/after-bracket-light.png) |
| feature lines on (car) | — | [after](shots/after-car-lines-light.png) |

## What each item is, and its evidence

| item | what | fences (all in `internal/httpapi`) |
|---|---|---|
| A1 kernel mesh by default | **Already on main**: a stored/opened variant asks for its mesh as it loads (`loadPrototype` → `refineWithBuiltSolid`), a live turn asks on its `variant` event, and the reply is drawn instanced (`studio.load(proto, b)`). Main's evidence: `TestWorkbenchAsksNoWholeMeshForADesignItRefused` (a design within the limit asks once), `TestWorkbenchDrawsTheMeshReplyInstanced`, `TestRendererDrawsTheMeshReplysInstancesByTheirMatrices`. The live-turn path had no fence; added. The preview pages here say PRIMITIVES ONLY on the page and in every PNG. **Not changed:** the compare view (`poolStudio` in workbench.js) draws its variants from primitives only. | `TestWorkbenchRefinesALiveTurnWithTheKernelMesh` |
| A2 lighting and materials | A studio environment (overhead soft box, key and fill strips, rim panel, cove) written as a function of direction, pre-filtered in JS for four roughnesses into one 128 × 256 RGBM texture, and its diffuse irradiance as 9 SH coefficients — no file, no fetch. Metal/roughness/clear-coat per finish from ONE table (`MATERIALS`; names are Go's `FinishNames`, unchanged). Split-sum specular with Karis's analytic BRDF, one GGX key light, colours sRGB → linear per vertex, ACES filmic (Narkowicz) then the exact sRGB curve. **WebGL1 draws exactly the same shading** (the part program is still GLSL ES 1.00, one source for both paths); what WebGL1 lacks is occlusion and the multisampled target (it keeps the canvas's own antialiasing). | `TestRendererHasAMaterialForEveryFinishGoAccepts`, `TestRendererToneMapsWithTheCurveItsShaderIsWrittenFrom`, `TestShadersCompileInARealBrowser`, `TestRendererDrawsItsPassesInOrderOnEveryPath` |
| A3 grounding | Contact shadow: the model drawn from under the floor into a 256² target (depth keeps each column's lowest surface; darkness falls off with height), blurred twice, laid on the floor — captured only when what is drawn changes (load, explode, state, isolate, transparency), never on a camera move. Occlusion (WebGL2): the frame is drawn into a 4× multisampled target, its depth resolved to a texture, 8-sample hemisphere SSAO at half size with a 4 × 4 rotation tile the composite's box averages away, multiplied over the opaque picture **before** anything translucent, then resolved to the canvas. The grid fades out round the model and no longer writes depth. | `TestRendererCastsAContactShadowOnlyWhenTheModelChanges`, `TestRendererDrawsItsPassesInOrderOnEveryPath` |
| A4 near/far | Reproduced first (below), then fixed: clip planes from the model's size and the eye; zoom limits in the model's units. | `TestRendererDepthResolvesTheModelAtAnyScale`, `TestRendererZoomsInTheModelsOwnUnits`; bugfix doc |
| A5 (browser half) | A curved primitive batch is drawn with 1, 2, 4 or 8 × its export count of segments — the fewest that keeps the chord sag under 0.5 px on its largest copy on screen; the export count (TESSELLATION, held to Go's) is the minimum and stays what picking and the export read. Extrusions, revolves, sweeps, sections and gears are shaded smooth across facets that meet within 35°. | `TestRendererDrawsCurvesAsFineAsTheScreenNeeds`, `TestRendererShadesCurvedPrimitivesSmooth` |
| A6 feature lines | Edges where facets turn by more than 35° (and open edges), per batch, drawn instanced over copies drawn whole; `studio.setFeatureLines(true)`, off by default; instanced paths only. | `TestRendererFindsTheFeatureEdgesOfAShape`, `TestRendererDrawsItsPassesInOrderOnEveryPath` |
| A7 hero camera, backdrop | Default and reset view yaw 0.62, pitch 0.34 (was 0.7 / 0.5), framed on the model's bounding sphere; a cove backdrop (floor → wall → ceiling, from each pixel's view direction) per theme. The `iso` preset is unchanged; `hero` added. A design browsed a row at a time keeps the old loose framing (its later rows arrive round the first). | `TestRendererDrawsItsPassesInOrderOnEveryPath` |
| found | **Cylinders, cones and spheres were wound inside out** on main (the "before" wheels, hubs and headlights read as hollow shells). | `TestRendererPrimitivesFaceOutward`; bugfix doc |

## A4: the streaks, reproduced before any fix

`finished-car.json` (a live model's car, mm) and the car fixture, origin/main's renderer, a close-up of a front wheel:
[`a4-before-finished-car-wheel.png`](shots/a4-before-finished-car-wheel.png),
[`a4-before-car-wheel.png`](shots/a4-before-car-wheel.png) — ragged, striped faces. The same, with ONLY the near plane
changed (0.05 → distance × 0.05, a copy of main's file with that one expression edited):
[`a4-only-near-changed-finished-car-wheel.png`](shots/a4-only-near-changed-finished-car-wheel.png),
[`a4-only-near-changed-car-wheel.png`](shots/a4-only-near-changed-car-wheel.png) — clean. That is the proof the fixed
near plane causes them; the arithmetic (~140 mm per depth step at the framed car) is in
[`docs/bugfix/2026-09-18-wheel-faces-streaked-because-the-near-plane-ignored-the-models-size.md`](../../bugfix/2026-09-18-wheel-faces-streaked-because-the-near-plane-ignored-the-models-size.md).
These four were taken before the car fixture was turned to face +Z (its nose was at −Z; the wheels are identical). The
before/after pair from `shoot-all.html` is [`before-a4-*`](shots/before-a4-car-wheel.png) /
[`after-a4-*`](shots/after-a4-car-wheel.png).

## Frame cost at 30,023 occurrences

[`measure.html`](measure.html): origin/main's renderer and this branch evaluated side by side in one page, the W1 car
(`scripts/viewport-car.js`), 940 × 560, synced frames (`draw()` + 1-pixel `readPixels`), configurations interleaved
round by round (4 rounds × 40 frames each). Chrome 152, ANGLE / Intel UHD, **Browser pane hidden and the laptop heavily
contended** by other agents' builds — the absolute numbers are 4–5× W1's 7.5–13.9 ms; compare the rows, not with W1.

```
config            median   p95    calls  post
before (main)      49.9    83.0     27   (canvas MSAA)
after              57.2    71.5     28   msaa4+ssao
after-no-ao        49.9    67.1     28   none
after-no-extras    50.5    66.8     28   none (no occlusion, no contact shadow)
```

A first run (3 rounds × 30, 12-sample occlusion, a 16-tap composite): before 49.4, after 64.8, no-AO 49.8. The occlusion
was then cut to 8 samples and a 4-tap composite. So: **everything but the occlusion costs nothing measurable per frame**
(the shadow is captured on load, not per frame); **the occlusion costs ~7 ms here (+15 %)**, a fixed full-screen cost
independent of the occurrence count, and can be switched off (`setAmbientOcclusion(false)`). The W1 fences
(`make test-viewport`, `node scripts/viewport-instancing-check.js`) all pass: one batch per definition, at most six
opaque calls per definition; the framed car now makes 31 calls in node (27 before) because the hero framing is closer,
so more copies are drawn at the simplified level rather than as boxes.

Load: the environment (texture + SH) is built once per page — ~115 ms in node; in the contended hidden pane the first
Studio's load was ~0.4–0.6 s longer than main's. The contact shadow's capture at load draws every copy once more.

## Not done / not measured

- **Kernel meshes in the screenshots.** Every shot is primitives only; no `forged` + kernel ran for this spike.
- **WebGL1 screenshots.** The WebGL1 path is compiled and drawn in headless Chrome by `TestShadersCompileInARealBrowser`
  (no GL error), not photographed.
- **Real GPU numbers uncontended**, other GPUs/browsers, the workbench itself after `make restart` (the preview and
  measurement pages load the shipped file directly).
- Frame cost with feature lines on at 30k (they are off by default).
- The SSAO shows a faint halo along thin plates (the bracket's gusset); a bilateral blur would remove it.

## Files

- `preview.html` — one document, one theme, one renderer (`?doc=car&theme=light&src=…`), label on the page.
- `shoot-all.html` — every shot above in one run; POSTs PNGs to a local harness server (a 30-line node static server
  with `POST /save?name=` writing into `shots/`; not committed — python's `http.server` serves the pages but cannot save).
- `measure.html` — the frame-cost table. `before-main-forge3d.js` — `git show origin/main:internal/httpapi/assets/forge3d.js`,
  the "before" renderer both pages load.
- `data/` — the fixed car, gear and bracket.
