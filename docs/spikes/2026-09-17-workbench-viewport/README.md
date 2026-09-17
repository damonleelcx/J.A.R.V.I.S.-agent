# The workbench's open viewport items, checked through the real stack

**2026-09-17, one Windows 11 laptop. Model tokens spent: zero.** The build goal card's replies came from
`docs/spikes/2026-09-15-card-checked/harness/stand-in.js`. `FORGE_LLM_BASE_URL` pointed at that local port, and
nothing else listened there. The laptop was shared with other agents' builds: CPU load was 11 % at the start and
49 % at the end. Every figure below is one run.

## How the page was reached

- Binaries: `forged`, `forge-worker` and `forgectl` were built from this branch, with Postgres and MinIO from
  `make db-up` / `make blob-up`. Configuration: [`harness/env.sh`](harness/env.sh). The bucket was made by
  [`harness/bucket.go`](harness/bucket.go).
- Principals: an owner and a viewer, both from `docs/spikes/2026-09-15-card-checked/harness/mintsession`. That tool
  uses the repo's own constructors and mints a session row. No password was typed.
- The viewer role on the owner's project: granted through `access.Service.SetRole`
  ([`harness/store.go -viewer`](harness/store.go)).
- The Browser pane reached `forged` through `pane-proxy.js`, one proxy per principal. The page never held a
  credential.
  - **Artefact of the proxy:** it strips cookies, so the theme choice does not survive a reload there. The product
    is not at fault.
- Designs: [`harness/designs.js`](harness/designs.js) wrote two, stored by `harness/store.go`:
  - `car30k.json`: `carDocument(30000)`, 30,023 occurrences.
  - `fleet1m.json`: 34 of those cars, 1,020,782 occurrences. `carDocument(1000000)` cannot be stored, because one
    of its patterns places 4,952 copies and a pattern may place at most 512.
- Storage limit: raised to 1,100,000 for `store.go` and `forged` only (`FORGE_GEOMETRY_MAX_OCCURRENCES`). That is
  the only way a 1M design exists. The viewport limit is a constant and was not changed.
- Browser: Chrome in the Claude desktop Browser pane, WebGL2.
  - **The pane was hidden for the whole run** (`document.visibilityState === 'hidden'`), so no presented-frame
    (`requestAnimationFrame`) interval could be taken.
  - Frame rows are `Studio.draw()` called synchronously, each followed by a 1-px `readPixels`: 5 warm-up frames,
    then 60 frames turning 0.05 rad. The Studio was reached by wrapping `Forge3D.Studio.prototype` methods.
  - Canvas: 1000×683 device pixels at 800×700 CSS, DPR 1.25.

## 1. The 30,023-part car through the real workbench (#88: "generated in a test page")

Stored, listed, opened at `/workbench?project=…`.

**First view:** loaded lazily. It asked for exactly the 12 top-level rows of at most 8,192 parts, and the kernel
built all 12. Requests went out 4.6 s after navigation, because the project listing took 4 s (see §3). They
finished 11.1–13.4 s later, so the first view was complete at about 18 s.

**Opening Seam:** a real click on its ▸ asked for `seam` only. The Go tessellator answered in 493 ms with 3.92 MB.

| state (800×700, pane hidden) | copies | draw calls | LOD on: median / p95 | LOD off: median / p95 |
|---|---:|---:|---|---|
| lazy first view (+126 boxes) | 4,823 | 46 | **6.8 / 7.3 ms** | 12.3 / 13.1 ms |
| Seam opened (all loaded) | 30,023 | 42 | **14.1 / 19.0 ms** | 27.7 / 31.3 ms |
| a search hit selected | 30,023 | 42 | 13.9 / 16.4 ms | — |
| `seam-14` isolated | 201 (29,822 hidden) | 2 | 7.0 / 7.3 ms | — |
| 420×760 (DPR 2, 840×1100 buffer), all loaded | 30,023 | 41 | 20.9 / 28.3 ms | — |

These agree with #93's 7.0 / 14.1 ms for the same states.

**Search `seam-12/rivet-1`** (the page's own input event): 72.8 ms including the first index build. It returned
50 rows ("Rivet 1 · seam-12/rivet-1 · Isolate", …) and "61 more — narrow the search".

**Select and isolate**, through the page's own click handlers:

- Select: 14.8 ms.
- Isolate `seam-14`: 3.9 ms.
- Show all: 9.0 ms.
- No new subtree was requested, because Seam was already loaded.

## 2. Hiding and re-showing the pane (#93, #104: "not exercised")

**Not exercised as asked.** This agent cannot show the Browser pane, and it stayed hidden from start to end.

What was established instead:

- **The viewport draws on demand.** `forge3d.js` has no `requestAnimationFrame` loop and no `visibilitychange`
  handler. Its only redraw triggers are input, `resize`, theme changes and loads. Hiding a pane therefore changes
  no code path, and a draw after re-show costs what the table above measures.
- **Tab switch and resize:** switching to Work, firing `resize` while there, and switching back left the drawing
  buffer matching the canvas (1000×683 at 800 CSS px; 840×1100 at 420 CSS px, DPR 2). The GL context was not lost.
- **Not seen:** what a real hide does to the canvas size in the desktop app (for example, a 0-width pane). If it
  sets clientWidth to 0, `_resize` falls back to 640×480 until the next `resize` event.

## 3. A 1,000,000-part stored design in the workbench (#89: "browser draw at 1M not measured")

The fleet (1,020,782 occurrences) was the newest variant, so the workbench opened it.

| what | measured |
|---|---|
| Stage | "This design places more than 100000 parts, which is the most the FORGE viewport draws at once. It is stored as it is; nothing was drawn." Nothing was uploaded; JS heap 4 MB. |
| Mesh request | one `GET /v1/geometry/{id}/mesh` (whole design) sent anyway and refused **400** in 13 ms |
| Assembly tree | listed: one "Car" row; opening it listed 34 rows in 0.9 ms |
| Search "rivet" | "Nothing drawn has that in its name or path." (0.3 ms): the studio holds nothing to search |
| `GET /v1/geometry?project_id=` | **3.4–4.1 s** server-side, 16.9 kB, 4 runs, with this design in the project |

**It does not list and lazy-load.** Past the 100,000 viewport limit the design is refused whole, by the kept
decision. It lists the tree from the document, but has nothing to draw or search, and no row can load. Reported,
not changed: lazy-loading past the limit would contradict the limit that was just re-affirmed.

Two findings, both **not fixed**:

1. The page asks for a whole-design mesh it has already refused to draw.
2. Listing a project that holds a 1M design takes about 4 s. That delays every workbench open of that project,
   including the 30k car's first view above.

## 4. Off-node STEP export from the workbench (#99: "no UI")

The viewer (`vpcheck-viewer@forge.local`, role viewer) used the new **STEP via worker** button on the 30,023-part
car v2, with a real click.

**Requests:**

- `POST …/exports?format=step` answered **202**.
- `GET /v1/geometry/exports/exp_…` was polled 5 times, at 4.0 s intervals.
- The job succeeded about 20 s after the click, and nothing was polled after it settled (checked 9 s later).

**Panel, in order:**

1. "forge-worker is writing the STEP file (attempt 1 of 3)."
2. On success: "This STEP file is an unverified proposal. B-Rep, not tessellated. … no interference check ran for
   this file. 30023 parts · 19.4 MB · sha256 2a8a790e66ac…"
3. Then the link "Download instanced-viewport-car-v2.step →".

**The download, fetched as the viewer:** 200, 20,354,358 bytes, starting `ISO-10303-21;`.

- The body's SHA-256 equals `X-Forge-Export-SHA256`.
- `X-Forge-Export-Label` begins "unverified proposal; B-Rep, not tessellated; … no interference check ran".

**Close and reopen:** the panel closed. On reopening, `POST` answered **200** with the same job, and no second job
was made.

**The 1M fleet — found by this check, fixed:**

- At first the panel read "Not exported. One or more request fields failed validation. — Correct the fields named
  in the details array and resubmit."
- That is the error code's general wording. The sentence written for this refusal is in `error.details.detail`.
- The panel now shows that sentence: "This design places more than 90000 parts, the most FORGE writes as STEP in an
  export job: … no job was queued and nothing was built." (`refusalText`, re-checked after a rebuild.)

**Found, not fixed (Go):** the request-path "Export STEP" label for the 30k car answers **501 CONNECTOR_UNAVAILABLE**.

- The detail says "This deployment has no CAD kernel configured". But `FORGE_CAD_PYTHON` was set, the kernel had
  just built 12 subtrees, and `/v1/geometry/formats` said STEP is available.
- The real reason is the 4,096-part request ceiling. The page now shows the server's detail, so it shows that
  wrong sentence word for word.

## 5. The build goal card: light theme and other viewports (#119: "not exercised")

A turn to the stand-in model, then "Build it as a model" → **Start this** → **Start it — run 3 tasks**, all with
real clicks. The build succeeded: "3 of 3 steps done · 3,700 tokens · 3 versions kept · Finished."

The card was checked in each state it reached (proposed, planned, finished) at:

- 1000×760 light;
- 420×800 light and dark;
- 800×700 light and dark;
- 1440×900 light and dark.

The export panel was checked alongside.

**Contrast:** computed for every text node against its composited background, WCAG AA. Nothing failed.

**Overflow:** none in the card or panel, and none on the document once the strip below was fixed.

**Found, fixed:**

- **At 420 px the page was 561 px wide.** `.stagetabs` has `flex: 0 0 auto`, and the narrow-width correction's
  `min-width: 0; overflow-x: auto` cannot shrink an item that may not shrink. The strip stayed 548 px wide, so a
  phone showed the workbench zoomed out and scrolling sideways. After `flex-shrink: 1`: document 420 px, strip
  396 px, scrolling its 526 px of tabs.
- **The section-cut picker on the light theme was a black box** (background `#0f131b` written in a style attribute
  in `pages.go`, text rgb(26,26,32), about 1.1:1). It now has a themed rule: light rgb(255,255,255) /
  rgb(26,26,32), dark rgb(23,23,27) / rgb(243,243,246).

**Not changed:** at 420 px the header wraps the theme button onto a second line, which looks intended. The rail
says "0 part(s)" for a tree design (every variant here), because the DTO's `parts` counts top-level parts only.

## 6. The provenance banner on an 800 px pane (#93)

Already folded on main (#95). Measured here at 800×700: 776×44 px, **7.8 %** of the 800×547 canvas.

## Reproduce

```bash
. docs/spikes/2026-09-17-workbench-viewport/harness/env.sh
go build -o $L/bin/ ./cmd/forged ./cmd/forge-worker ./cmd/forgectl
go build -o $L/bin/mintsession.exe ./docs/spikes/2026-09-15-card-checked/harness/mintsession
$L/bin/forgectl.exe migrate && go run docs/spikes/2026-09-17-workbench-viewport/harness/bucket.go
$L/bin/mintsession.exe -email vpcheck-owner@forge.local -project "viewport check" -out $L/owner.json
$L/bin/mintsession.exe -email vpcheck-viewer@forge.local -project "viewer own" -out $L/viewer.json
node docs/spikes/2026-09-17-workbench-viewport/harness/designs.js $L
go run docs/spikes/2026-09-17-workbench-viewport/harness/store.go -user <owner> -project <project> -viewer <viewer> -design $L/car30k.json
FORGE_GEOMETRY_MAX_OCCURRENCES=1100000 go run docs/spikes/2026-09-17-workbench-viewport/harness/store.go -user <owner> -project <project> -design $L/fleet1m.json
FORGE_GEOMETRY_MAX_OCCURRENCES=1100000 harness/forged.sh & harness/worker.sh &
node docs/spikes/2026-09-15-card-checked/harness/pane-proxy.js 18331 18330 $L/none $L/owner-proxy.log $L/owner.json &
node docs/spikes/2026-09-15-card-checked/harness/pane-proxy.js 18332 18330 $L/none $L/viewer-proxy.log $L/viewer.json &
node docs/spikes/2026-09-15-card-checked/harness/stand-in.js 18339 $L/stand-in.jsonl &
```
