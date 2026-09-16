# Measurement: the viewport draws a 30k-occurrence car instanced (W1–W3)

**Date:** 2026-09-15 · **Status:** done, one laptop · **Stages:** Phase 6, W1–W3 of [`docs/plan-2026-09-13-millions-of-parts.md`](../../plan-2026-09-13-millions-of-parts.md)

## Summary

- **A 30,023-occurrence car loads in a real browser and draws in 27–28 calls** (20 definitions, one call per
  definition per winding and level of detail, plus the six translucent windows sorted by copy). Before W1 the same car
  was refused outright: the viewport stopped at 4,096 parts.
- **Frame time at 30k, WebGL2, CPU and GPU together (draw + 1-pixel `readPixels`), median:** 7.5–13.9 ms with the
  level of detail on, 16.1–21.0 ms drawing every copy whole. The same car one draw call per copy — how every part was
  drawn before W1 — took **45.1 ms**.
- **At 99,971 occurrences (just under the new viewport ceiling), WebGL2:** load 341 ms, **23.4 ms** a frame with the
  level of detail, **41.8 ms** whole; one call per copy **147.6 ms**. This is the number `maxViewportParts = 100,000`
  is raised on (see "The limits change").
- **WebGL1 with `ANGLE_instanced_arrays` measured within a few percent of WebGL2** at both sizes (13.8 / 15.9 ms at
  30k, 24.9 / 43.9 ms at 99,971).
- **Per-frame upload is 84 bytes a copy** (21 floats): 2.52 MB at 30k, 8.40 MB at 99,971.

## What was NOT measured

Stated plainly, because the plan's acceptance names more than this run did:

- **Not through the workbench, and not "on `make restart`".** The car was generated in the page and loaded straight
  into the shipped `Forge3D.Studio` (`measure.html`, repository root served statically). No `forged`, no stored
  version, no login. The `make restart` target exists; this run did not use it.
- **Not with a kernel mesh reply.** A 30k design is still past the kernel's 4,096 (`DrawRefusal`), so the workbench
  gets no `definitions`/`instances` for it; every batch here is a primitive. The instanced path over a real K4 reply
  is held by `TestRendererDrawsTheMeshReplysInstancesByTheirMatrices` in node, not timed.
- **Not what a person sees.** The Browser pane was hidden during the runs, so `requestAnimationFrame` intervals are
  throttled and are discarded (the page's `presentedIntervalMs`; the one-call-per-copy run reported 1,026 ms, which
  is the throttle, not the renderer). The synced frame has a floor: an **empty stage measured 6.9 ms** median, so
  numbers near 7 ms say "at least as fast as the readback", not 7 ms of work.
- **Not uncontended.** The laptop was running a live LLM car measurement and a 1M-occurrence kernel spike in other
  worktrees at the same time. The two 30k WebGL2 runs 4 minutes apart differ by up to 2×; treat every number as
  ±50%.
- Not on a discrete GPU, not on macOS or Linux, not in Firefox or Safari, not memory, not the tree browser's UI or a
  real mouse click (picking is fenced in node; see below).
- **Culling saved nothing in these runs:** the camera frames the whole car, so every copy is in view (`culled: 0`).
  The level of detail is what the with/without columns compare.

## Method

`measure.html` loads `scripts/viewport-car.js` — a car written the way D1 lets one be written: four mirrored corners
with polar spokes and lugs, riveted seams in linear patterns, spot welds and battery cells in grids, a harness along
a path, bracketed mounts, seats and translucent windows; 20 definitions — into the shipped `forge3d.js`. The later
runs (B–D) were the same measurement typed into the page's console through the browser tool, taking only the synced
frame. Each row: load timed around `studio.load`, 5 warm-up frames, then 60 frames (20 for 99,971 one call per copy)
turning the camera 0.05 rad a frame, each `studio.draw()` followed by `gl.readPixels(0, 0, 1, 1)`, which does not
return until the GPU has finished the frame. "cpu draw" is `studio.draw()` alone (culling, level-of-detail choice,
writing and uploading instance buffers, issuing calls). One call per copy is WebGL1 with the extension withheld;
WebGL1-instanced is WebGL2 withheld.

Environment: Claude desktop Browser pane, `Chrome/152.0.7977.76`, `ANGLE (Intel, Intel(R) UHD Graphics (0x000046A3)
Direct3D11 vs_5_0 ps_5_0, D3D11)`, canvas 940×675 (958×675 in the first row), Windows 11. One run per row.

## Raw results

Frame times in ms, median / p95. "boxes" is copies drawn as their batch's box by the level of detail.

```
run  when (UTC)  occurrences  path              lod  load   synced frame   cpu draw      calls   boxes  upload/frame
A    13:26       30,023       webgl2            3    111.6    7.5 / 14.7    3.4 /   4.1      28  27,477    2.52 MB
A    13:26       30,023       webgl2            0    129.2   16.1 / 22.3    3.4 /   4.2      27       0    2.52 MB
A    13:26       30,023       webgl1-per-copy   0    117.3   45.1 / 67.5   80.2 / 183.4  30,023       0       0
B    13:30       30,023       webgl2            3    137.4   13.9 / 14.6    4.6 /   5.3      28  27,530    2.52 MB
B    13:30       30,023       webgl2            0     82.6   21.0 / 28.1    4.8 /   6.2      27       0    2.52 MB
C    13:32       empty stage  webgl2            -        -    6.9 /  8.0        -             0       -          -
C    13:32       99,971       webgl2            3    340.5   23.4 / 29.7   12.7 /  16.4      28  97,130    8.40 MB
C    13:32       99,971       webgl2            0    236.9   41.8 / 49.3   10.6 /  15.7      27       0    8.40 MB
C    13:32       99,971       webgl1-per-copy   0    660.3  147.6 /198.4  134.4 / 182.0  99,971       0       0
D    13:33       30,023       webgl1-instanced  3    124.3   13.8 / 15.0    3.8 /   4.5      28  27,530    2.52 MB
D    13:33       30,023       webgl1-instanced  0     72.8   15.9 / 26.0    3.8 /   4.8      27       0    2.52 MB
D    13:33       99,971       webgl1-instanced  3    407.6   24.9 / 32.3   13.5 /  20.7      28  97,130    8.40 MB
D    13:33       99,971       webgl1-instanced  0    313.5   43.9 / 51.0   11.7 /  15.3      27       0    8.40 MB
```

Every run: `glError` 0, no refusal. `carDocument(100000)` rounds its seam count up and places more than 100,000
parts; it was refused whole in the viewport's words, which is how the 99,971 row was chosen.

## The limits change

`geometry.maxViewportParts` (and `MAX_VIEWPORT_PARTS` in `forge3d.js`) goes from 4,096 to 100,000, the default
occurrence bound, on rows C and D: a 99,971-occurrence car loads in 0.3–0.4 s and draws in 23–25 ms a frame with
the level of detail on this iGPU. That is a usable viewport, not 60 frames a second, and it is one run on a shared
machine. **The kernel's ceiling (`maxDrawnParts`, 4,096) is unchanged**: building past it has only been measured
through the sidecar directly.

## What the node stub adds

`make test-viewport` (`scripts/viewport-instancing-check.js`) drives the same car through `scripts/webgl-stub.js`, a
context that records calls and reads every instance back out of the buffers by location, stride, offset and divisor.
It checks one batch per definition, at most four opaque calls per batch, every copy drawn/culled/hidden exactly once
and no WebGL state a browser refuses, over WebGL2, WebGL1+ANGLE and WebGL1 without it. Its milliseconds are CPU in
node against a context that draws nothing and are **not** a frame time.

## Re-running

```bash
python -m http.server 8765 --bind 127.0.0.1     # from the repository root
# open http://127.0.0.1:8765/docs/spikes/2026-09-15-instanced-viewport/measure.html, keep the tab in front
make test-viewport
```
