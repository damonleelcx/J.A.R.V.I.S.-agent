# Large designs in the workbench: the defects PR 143 found, fixed and measured

**2026-09-17, one Windows 11 laptop, shared with other agents' builds. Model tokens spent: zero.** Stacked on PR 143
(`viewport/remaining`); its harness (`docs/spikes/2026-09-17-workbench-viewport/harness`) stored the designs: the
30,023-part car and the 1,020,782-part fleet of 34 cars, with `FORGE_GEOMETRY_MAX_OCCURRENCES=1100000` for storage and
`forged` only. Postgres `forge-pg` (55840), MinIO `forge-minio` (55841), a configured CAD kernel
(`FORGE_CAD_PYTHON`, build123d 0.11.1).

Two `forged` ran side by side from [`harness/forged.sh`](harness/forged.sh): **base** = `origin/viewport/remaining`
(PR 143) on 18350, **new** = this branch on 18352. Every before/after below is interleaved, request by request, with
CPU load read at the start and end. The Browser pane reached the new `forged` through PR #133's `pane-proxy.js` on
18353, with a session minted by `mintsession` for a project whose newest variant is the fleet
([`harness/fleetproject.sh`](harness/fleetproject.sh)). All processes were stopped by PID at the end.

## 1. STEP past the 4,096-part request ceiling

[`harness/labels.sh`](harness/labels.sh), as the owner, kernel configured:

| request | base | new |
|---|---|---|
| label, car (30,023) | **501** CONNECTOR_UNAVAILABLE "no CAD kernel configured" | **400** "This design places 30023 parts, and a STEP file written during a request is built from at most 4096 … Use "STEP via worker" (POST /v1/geometry/{id}/exports): forge-worker writes up to 90000 …" |
| export, car | 400, DrawRefusal's general sentence (points nowhere) | 400, the sentence above |
| label, fleet (1,020,782) | 501, no kernel | 400 "places 1020782 parts … "STEP via worker" does not reach it either: an export job writes at most 90000" |
| label, desk lamp (3 parts) | **501**, no kernel | 200, a B-Rep label |

The last row shows the defect was wider than the ceiling: with a kernel, **every** STEP label answered 501, so the
workbench's Export STEP never offered a file. PR 145 (`goals/unverified-paths`) fixed that independently
(`KernelLabelFor`); this branch carries PR 145's hunks byte for byte and adds only the ceiling's own refusal, in a
separate hunk before it. Status stays 400 VALIDATION_FAILED, the status every size refusal in this API answers (the
mesh, the export job, the in-request export, whose fence already held 400).

## 2. `GET /v1/geometry?project_id=` with a million-part design in the project

Profiled first (a CPU profile of the listing's DTO on the fleet): decoding the document took **1.5 ms**; building
the DTO **2.33 s**, all of it `geometry.Measure → bounds → expandAssemblies` placing every occurrence. The listing
no longer measures (`measured` is absent, `measured_note` says `GET /v1/geometry/{id}` carries it, and the workbench
reads it there for the one design it draws); it carries `occurrences`, counted without placing anything.

[`harness/interleave.sh`](harness/interleave.sh), owner's PR 143 project (8 variants incl. the fleet), CPU 19 % → 31 %:

| round | base | new |
|---:|---:|---:|
| 1 | 3.43 s | 0.035 s |
| 2 | 2.97 s | 0.011 s |
| 3 | 2.92 s | 0.011 s |
| 4 | 2.76 s | 0.014 s |
| 5 | 2.75 s | 0.025 s |
| 6 | 2.81 s | 0.010 s |

In the browser the fleet project listed in **18-29 ms**. The single read that measures the fleet took 2.09-3.05 s
(once per open of that design; not changed).

## 5. Browsing the fleet past the viewport's limit

Decision (coordinator, under damon's delegation): the 100,000 limit bounds what is DRAWN at once, not what may be
browsed. In the Browser pane (Chrome, WebGL2), **hidden the whole run** (`document.visibilityState === 'hidden'`),
through the page's own controls (input events on the search box, `click()` on tree rows):

- **Open:** stage says Go's refusal plus "Open, select or isolate a row of the tree to draw that part of it; up to
  100000 parts are drawn at once." Rail: **"1020782 part(s)"** (base: "0 part(s)"). No whole-design mesh request
  (base: one, refused 400). JS heap 4 MB.
- **Search** (whole tree, nothing placed): `rivet` → 50 rows and "856750 more" in 421 ms (945 ms after a reload);
  `car-7/seam-12/rivet-1` → 11 rows in 666 ms (1,047 ms after a reload). Node, uncontended CPU: 137 ms and 234 ms.
- **Select a search hit** `car-25/seam-3/rivet-17`: asked for exactly that path; drawn.
- **Open a car** (`car-7`, `car-9`, `car-30`): each asks for that car, 30,023 parts, 5.6 MB.
- **Isolate** `car-30/seam-12`: covered by the loaded car, no request, 9 ms.
- **The ×34 row "Car"** (1,020,782): refused by name — "The subtree car places 1020782 parts, more than 100000,
  which is the most the FORGE viewport draws at once. Open a row beneath it instead; nothing was sent."
- **Past the limit:** with car-7 and car-9 drawn, opening car-11 and car-13 put car-7 back: 90,069 drawn, then
  90,070 with a rivet.
- **Frames** (60, `Studio.draw` + 1-px `readPixels`, pane hidden so the canvas was 169×480 — not representative of
  a visible pane): seam isolated 6.9 / 7.7 ms median / p95 (91 instances); two cars 14.2 / 21.0 ms (30,415 instances
  after culling, 41 draw calls).

**Found while doing it, fixed:** each subtree request expanded the whole design server-side. One rivet took **5.9 s**
in the browser. `Document.Subtree` now places only the placements that can lead to its path (not when an assembly
has features of its own). [`harness/subtrees.sh`](harness/subtrees.sh), interleaved, CPU 17 % → 16 %, reply bodies
byte-identical base vs new every time:

| subtree | base (3 rounds) | new (3 rounds) |
|---|---|---|
| `car-20/seam-3/rivet-17` (1 part, kernel) | 2.26 / 2.05 / 2.06 s | 3.92 (kernel cold start) / 0.020 / 0.034 s |
| `car-7/seam-12` (201) | 2.18 / 2.09 / 2.11 s | 0.049 / 0.047 / 0.036 s |
| `car-9` (30,023, 5.58 MB) | 2.21 / 2.19 / 2.19 s | 0.166 / 0.192 / 0.161 s |

In the browser after the fix: rivet 23 ms, a whole car 168 ms.

## 6. Hiding and re-showing the pane

**NOT DONE in a real browser.** `tabs_context` reported the pane hidden at the start and it stayed hidden, so no
hide/re-show could be exercised and no presented frame time taken. What was found by reading the renderer: a
resize while the canvas has no size set its buffer to the 640×480 fallback, and nothing redrew on becoming visible.
Both are fixed and held in node (`TestRendererRedrawsWhenItsPaneIsShownAgain`).

## Reproduce

`harness/browse1m.js <worktree>` reads the designs from `scratchpad/vpcheck` (PR 143's `designs.js` output); the
shell scripts carry this laptop's scratchpad paths.
