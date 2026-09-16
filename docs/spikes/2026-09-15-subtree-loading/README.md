# Measurement: a stored 30k-occurrence car in the real workbench, loaded a subtree at a time (W2)

**Date:** 2026-09-15 · **Status:** done, one laptop · **Stage:** Phase 6, W2 of [`docs/plan-2026-09-13-millions-of-parts.md`](../../plan-2026-09-13-millions-of-parts.md)

## Summary

- **The acceptance run the plan asks for, this time through the real stack:** `forged` built from this branch, a user
  signed up and signed in over the API, a 30,023-occurrence car stored, and the stored variant opened in the shipped
  workbench page (`/workbench?project=…`) in Chrome 152. #88 loaded the car into the Studio from a static page.
- **First view, lazy:** the page listed all 30,023 occurrences, uploaded none of them, and asked for the **12 top-level
  subtrees that place no more than 4,096 parts** (corners, pan, battery modules, harness, mounts, seats, windows,
  lamps, exhaust — 4,697 occurrences). **The CAD kernel built all 12**, one after another on its single process: the first
  view was complete **17.2 s** after navigation with the kernel cold (9.3 s of it the first request, which started the
  kernel), and each subtree took **1.6–3.4 s** on a warm kernel. The 126 seam slots (25,326 riveted parts) stayed
  translucent boxes.
- **Opening the Seam row** asked for exactly `seam`. It places 25,326 parts, past the kernel's ceiling, so **the Go
  tessellator served it**: 197 ms on the server, 3.55 MB, and 86 ms in the browser to draw it (`addSubtree`).
- **Frame time, WebGL2, synced (draw + 1-px `readPixels`), median / p95, pane visible:**

  | state | copies drawn | LOD + hierarchy | LOD, flat culling | whole (LOD off) | cpu draw | upload/frame |
  |---|---|---|---|---|---|---|
  | lazy first view | 4,697 + 126 boxes | **7.0 / 7.8 ms** | 6.9 / 7.5 ms | 12.9 / 15.4 ms | 1.0 ms | 0.39 MB |
  | eager, all 30,023 | 30,023 | **14.1 / 27.7 ms** | 13.9 / 21.1 ms | 19.3 / 27.2 ms | 4.9 ms | 2.41 MB |

  Presented frame interval (`requestAnimationFrame`, orbiting): **13.9 ms** median in both states with LOD on (the
  display's refresh), **20.9 ms** lazy / **20.8 ms** eager with LOD off.
- **Loading the Studio, CPU, same page:** `loadLazy` (list 30,023 occurrences, box 750 top-level slots) **96–130 ms**;
  `load` (upload all 30,023, the pre-W2 path) **152–227 ms**. Both before any mesh reply.
- **Culling:** with the whole car in view the hierarchy saves nothing (4.9 ms against 4.5 ms flat — every node is
  opened or accepted, and every copy is still written). **Turned away from the car** it visits **10 nodes** and culls all
  30,023 copies in under 0.1 ms, against 0.5 ms testing each.
- **Search:** building the index took **43–50 ms** on the first search (1,024 distinct three-letter runs). `seam-12/rivet-1`
  read **412 of 30,023** occurrences in **0.06 ms** against the scan's 1.38 ms, with the same 111 results; a one-letter
  query reads them all (0.46 ms against 0.84 ms).

## What a person saw

Screens described, not committed (binaries stay out of the repository):

1. **Before the fix below, an empty grid.** The stored car was listed in the Variants rail as "0 part(s)" and the stage
   showed nothing. See "Found by this run".
2. **After loading:** a grid with the car as translucent grey boxes — one per top-level slot, 750 of them — which turned
   into shaded parts as each kernel subtree arrived (the 126 seam slots render as a long translucent smear down the
   middle). The provenance banner then listed every loaded subtree: "front-left (30 parts) — built by the CAD kernel", …
3. **Work tab → Assembly:** 13 rows (front-left, front-right, rear-left, rear-right, Seam ×126, Floor pan ×2, Module ×5,
   Harness ×400, Mount ×200, Seat ×4, Window ×6, Headlamp ×2, Exhaust), each with ▸ and Isolate. Opening Seam listed
   Seam 1 … Seam 126 and loaded the seams (above).
4. **Search** `seam-12/rivet-1`: 50 rows reading "Rivet 1", "Rivet 10", … "Rivet 19", "Rivet 100", and "61 more — narrow
   the search".
5. **Isolate** on a seam slot (the click landed on Seam 14 — the list had scrolled under the pointer): 201 parts drawn in
   2 calls, 29,822 hidden, "Show all" shown, nothing requested (the seam was already loaded).
6. **Click on the model** (a rivet at the canvas edge): selected `seam-14/rivet-130`, one copy lit, nothing requested.

## Found by this run

- **Fixed: a stored design written as a tree was never put back on the stage.** A tree has no top-level parts and was
  sent as `"parts": null`; the workbench's restore required `parts`, and `renderParts` threw on `null` inside a promise
  whose `catch` is silent — so no model and no Assembly tree. The variant DTO now sends `[]`
  (`TestVariantDTO_ATreeWithNoTopLevelPartsSendsAnEmptyList`), and the restore accepts a root
  (`TestWorkbenchLoadsALargeTreeASubtreeAtATime`).
- **Not fixed: the provenance banner covers most of the stage** on this 800-px-wide pane — 70 of the 73 on-screen
  copies of the isolated seam could not be clicked, and the loaded-subtree list makes the banner longer.
- **Not fixed: search results name a part, not where it is** ("Rivet 1" ×126 seams), and the Parts panel shows only its
  heading for a tree design.

## What was NOT measured

- **Frame times after the tree exercise began.** Midway through, the Browser pane went hidden (`visibilityState:
  hidden`), so the seam load, search, isolate and click were exercised functionally and timed only on the server and in
  synchronous in-page CPU — no frame or presented-interval numbers were taken while hidden. Every frame row above was
  taken with `visibilityState: visible` and rAF at 13.9 ms.
- **Eager load through a page reload.** "Eager" rows are the same page and the same stored document loaded with
  `studio.load` (the call the workbench makes for a design it does not load lazily); the `forge.viewport.eager`
  localStorage switch in the workbench was not used for a timed reload.
- **Frame time with the seams loaded after opening the row** (pane hidden by then); the eager rows are the same 30,023
  copies.
- **The kernel under contention it did not choose:** other agents' kernel measurements and a Go test run shared the
  machine; kernel subtree times are one run each.
- A second browser, a discrete GPU, macOS/Linux, memory, and anything past 30k (the viewport limit is unchanged).

## Conditions

Claude desktop Browser pane, `Chrome/152.0.7977.76`, `ANGLE (Intel, Intel(R) UHD Graphics (0x000046A3) Direct3D11 vs_5_0
ps_5_0, D3D11)`, WebGL2, page 799×694, canvas 998×676 device pixels, Windows 11. `forged` built from this branch with a
local development configuration: Postgres in Docker, `FORGE_CAD_PYTHON` set (`FORGE_CAD_POOL` 1), the model endpoint
pointed at an unused local port (no conversation was held). The pane reached `forged` through a local proxy that added
the test user's session as a Bearer header, so the page never held a credential.

## Method

1. `go build ./cmd/forged`; start it with the configuration above; `POST /v1/auth/sign-up` and `/sign-in`.
2. `node -e` → `scripts/viewport-car.js carDocument(30000)` as JSON; store it with `store.go` beside this file. There is
   no HTTP endpoint that stores a client's geometry, on purpose (see `store.go`), so this is the one call `/v1/converse`
   makes, `geometry.Service.Save`, with the signed-up user as initiator.
3. Open `/workbench?project=<id>`; read request timings from `performance.getEntriesByType('resource')` and which
   tessellator answered from the `forge.geometry.meshed` log line (`source=kernel|go`).
4. Frame rows: the page's Studio, reached by wrapping `Forge3D.Studio.prototype.draw`; 5 warm-up frames, then 60 frames
   turning 0.05 rad, each `draw()` followed by a 1-px `readPixels`; presented intervals from 120 `requestAnimationFrame`
   callbacks each drawing.
5. The tree: the page's own controls with a real mouse and keyboard — ▸ Seam, the search box, Isolate, a click on the
   canvas.

```bash
FORGE_DATABASE_URL=… go run docs/spikes/2026-09-15-subtree-loading/store.go -user <usr_…> -design car30k.json
make test-viewport
```
