# The workbench's remaining slow paths on a 1,020,782-part design

**2026-09-17. One Windows 11 laptop, shared with other agents' builds. Model tokens spent: zero.**
This follows PR 147 (`docs/spikes/2026-09-17-large-designs`) and takes the items it recorded as
"Found, not changed", plus the build card's `last_seen_at` (PR 145).

## Harness

The same one as PR 147:
- Postgres `forge-pg` (55840) and MinIO `forge-minio` (55841).
- A configured CAD kernel (build123d 0.11.1, `FORGE_CAD_POOL=1`, the same as `deploy/k8s/20-config.yaml`).
- The stand-in model endpoint (nothing listens on it, and nothing here asks a model anything).
- A session minted with the repo's `mintsession` for `vplarge-owner@forge.local`. Its project's newest variant is
  the fleet: 34 cars, 1,020,782 parts, `ver_01M2QSPA7PK86JYT2XJ4MSBAH1`.

Two `forged` ran side by side from [`harness/omb_forged.sh`](harness/omb_forged.sh):
- **base**: `origin/main` at `f9a503b`, on 18360.
- **new**: this branch, on 18362.

The new `forged` applied migration 0025 on start. The base binary ignores the new column.

The Browser pane reached each `forged` through PR #133's `pane-proxy.js`: base on 18361, new on 18363. **The pane
was hidden the whole time** (`tabs_context`: "The Browser pane is currently hidden"), so the canvas had no size. None
of the figures below is a frame time.

CPU load was read before and after each run.

## 1. `GET /v1/geometry/{id}` no longer places every part

The design's corners (what `bounds` finds) are worked out once, when a version is stored, and kept in
`forge_geometry.extent` (migration 0025). A read measures from them with `geometry.MeasureFrom`, which writes the same
overlays `Measure` writes. A row stored before 0025 has no extent: its first read measures it the old way and keeps the
result.

Why not bound the tree from definitions × placements? `bounds` ignores a part's own rotation but not a placement's.
The box of a rotated sub-assembly is therefore not the rotated box of its parts, and any composed shortcut would answer
differently from today for every rotated child. Keeping what `bounds` found is identical by construction.

[`harness/omb_get.sh`](harness/omb_get.sh), the fleet, interleaved, CPU 4% → 5%:

| round | base | new | `measured` identical | whole `variant` identical |
|---:|---:|---:|---|---|
| 1 | 3.32 s | 3.45 s (the one-time measure of a pre-0025 row, then kept) | yes | yes |
| 2 | 3.29 s | 0.015 s | yes | yes |
| 3 | 3.27 s | 0.029 s | yes | yes |
| 4 | 4.51 s | 0.017 s | yes | yes |
| 5 | 3.35 s | 0.008 s | yes | yes |
| 6 | 3.19 s | 0.069 s | yes | yes |

Both answered `measured-x` 4550, `measured-y` 2153.362 and `measured-z` 81460. A design stored by the new `forged` keeps
its extent when it is stored, so it never pays round 1.

Fences:
- `TestGetMeasuresALargeDesignWithoutPlacingIt` (DB). Reading a 100,000-part design allocates within about twice what
  reading a 400-part one does; the drill "a read measures a design by placing every part again" allocated 804,217
  times against 8,931. Answers equal `geometry.Measure` on a flat design, a big one and a tree, both freshly stored and
  stored before 0025. Every stored row keeps an extent.
- `TestExtent_MeasuresExactlyAsMeasureDoes` covers every design shape the package measures: flat, repeated, gear,
  outline, a tree turned, mirrored and patterned at each level, a tree that places nothing, and nothing at all. The
  extent goes through its JSON. An extent from another revision is not trusted.

## 2. A browsed design's search reads an index built once

On its first search, a browsed design walks the tree once. The walk is `eachTreeOccurrence`, which `searchTree` now
reads too, so the two cannot name an occurrence differently. It writes one line per occurrence into a single string:

- the lowercased path;
- the lowercased display path;
- the path, if its case differs;
- the label.

Each query is then a run of `String.indexOf` over that string. The first match in a line falls in its first two
fields exactly when the walk matches that occurrence, and the next search starts at the following line, so each
occurrence is counted once. The tree's search box now waits 150 ms for typing to pause.

**Browser, hidden pane, the fleet** ([`harness/browser.js`](harness/browser.js)):

- Typing "rivet" as five input events:
  - base: the five events took 992 ms, because each character searched;
  - new: one search, for "rivet", 881 ms, which includes building the index once.
- Then three rounds of the same queries on each side, run through the page's own `findOccurrences`:

| query (total) | base | new |
|---|---|---|
| `rivet` (856,800) | 175-203 ms | 89-98 ms |
| `car-7/seam-12/rivet-1` (111) | 257-275 ms | 22-23 ms |
| `seam 3` (75,174) | 273-299 ms | 29-32 ms |
| `wheel` (3,944) | 269-273 ms | 7-8 ms |
| `zzz` (0) | 259-273 ms | 6 ms |

In the new tab, all 15 answers were JSON-identical to `searchTree`'s.

In node ([`harness/searchidx.js`](harness/searchidx.js)), index and walk gave identical answers for 8 queries on the
car and 8 on the fleet. The fleet's index took 0.86-1.9 s to build: 1,020,782 lines, 73.1 M characters.

**Memory, stated.**
- Heap after collection: 76 MB with the index against 7 MB in the base tab. The index is ~71 MB in node.
- The first build leaves ~300 MB of garbage until the collector runs: 333-368 MB was read right after it.
- The index is held only once something has been searched, and is dropped when another design loads.

Fences:
- `TestRendererSearchesABrowsedDesignThroughAnIndexLikeTheWalk`. The design has mixed-case ids, a named child, a
  repeating definition and a repeating top-level part. For each of 15 queries, the index answers what the walk answers
  and what Go places. The index is built once, and a second design gets its own.
- `TestWorkbenchTreeSearchWaitsForTypingToPause`. Five characters render the tree once, for the last one.

## 3. Opening a row draws its first rows, and offers the rest

**Decision.** Opening a row is how a person walks down the tree (car, then seam, then rivet) as much as how they look
at a thing. So opening now applies the first view's own policy beneath the row:

1. every child row that fits whole, in the order the tree lists them;
2. then the copies of a pattern that did not fit, in order;
3. up to 8,192 parts (`FIRST_VIEW_OCCURRENCES`) or 24 requests.

A row the open did not finish shows **Draw all N**, which asks for the whole row as before. Select and Isolate still
draw the whole row: they mean "this thing". A row within the budget is asked for whole, as before. A row where no child
fits (the ×34 "Car") is asked for whole and refused by name, as before.

The rejected alternative was to stream every child until the row is whole. It costs the same parts and the same room
in the end, and a person walking through a car to one seam would pay for the whole car anyway.

**Measured, browser, hidden pane**, three cars in turn, base and new interleaved car by car:

| | base: whole car | new: first rows |
|---|---|---|
| requests | 1 | 24 |
| bytes | 5.58-5.64 MB | 2.66-2.67 MB |
| parts drawn per car | 30,023 | 7,109 (every kind of thing in the car; 8 of 126 seams) |
| first rows on screen | 669-769 ms (all at once) | 524-544 ms |
| all asked for arrived | 669-769 ms | **4.07-4.24 s** |
| surfaces | Go primitives (past the kernel's ceiling) | the CAD kernel's, holes cut, **one at a time** on the kernel pool of 1 |
| drawn after 3 cars | 90,069 (a 4th car puts the first back) | 21,327 (nothing put back) |
| Draw all on car-9 | n/a | 1 request, 5.58 MB, 710 ms; replaces the open's rows; 44,241 counted = 44,241 drawn |

**The trade-off.** First rows arrive slightly sooner and the open is a quarter of the parts and half the bytes. But the
last of the open's 24 rows lands about 4 s later than base's whole car. Every opened row is small enough for the
kernel, and the kernel builds one at a time (`FORGE_CAD_POOL=1`, as in production): the four suspension corners took
about 450 ms each. That is the existing W2 policy for small subtrees, the same one the first view follows. If the
coordinator prefers the whole car at once, "Draw all" is one click, or `OPEN_OCCURRENCES` can be raised.

**Found and fixed while doing it:** a row that arrived under a row drawn meanwhile was never subtracted from what the
viewport counted as drawn. `_makeRoom` also ignored rows still on their way that the new row replaces. Both are needed
once "Draw all" can follow an open before its rows arrive.

Fences:
- `TestRendererOpensARowItsFirstRowsFirstAndTheRestWhenAsked`:
  - the exact paths asked for, and exactly Go's parts under them;
  - "Draw all" drawing the whole block once;
  - a "Draw all" racing the open's rows;
  - a near-limit case where only the replaced-rows accounting avoids putting a row back;
  - a small row, and the ×34 refusal.
- `TestWorkbenchOpensARowItsFirstRowsFirstAndOffersTheRest` (the toggle calls `openRow`; the row offers "Draw all 30023"
  and its click asks for the row).
- `TestWorkbenchLoadsALargeTreeASubtreeAtATime` now requires `openRow` for opening.

## 4. The build card shows when its worker was last seen

A running step shows "Now: … — running · worker last seen 4 s ago". Past 30 s (six missed 5 s beats), the card instead
says: "Worker last seen 45 s ago: it may have stopped. The step goes to another worker when its lease runs out."

The age is measured against the goal reply's `Date` header (the server's clock, which also wrote `last_seen_at`). The
browser's clock is used only when the header is absent. A step no worker holds, and a finished goal, say nothing about a
worker.

Fence (node): `TestGoalCardSaysWhenTheWorkerWasLastSeen`. Its polled case gives a 2020 server `Date` while node's own
clock says 2026, so it fails if the browser's clock is used.

## 5. Hiding and re-showing the pane: NOT DONE

The Browser pane was opened for this run, and `tabs_context` reported it hidden at every check. The only
pane-showing tool (`show_pane`) offers diff, file, terminal, pr, tasks, plan and artifact, but not the browser. So:
- no hide or re-show was exercised;
- no frame time was taken on a visible pane.

PR 147's renderer fix (`TestRendererRedrawsWhenItsPaneIsShownAgain`) is unchanged.

## Reproduce

- [`harness/omb_forged.sh`](harness/omb_forged.sh) `<exe> <port> <proxyport>` starts a `forged`.
- [`harness/omb_list.sh`](harness/omb_list.sh) `<port>` lists the fleet project.
- [`harness/omb_get.sh`](harness/omb_get.sh) `18360 18362 <version> 6` runs the item-1 table.
- [`harness/searchidx.js`](harness/searchidx.js) and [`harness/openplan.js`](harness/openplan.js) `<worktree>` read the
  designs from the scratchpad's `vpcheck` (PR 143's `designs.js` output).
- The shell scripts carry this laptop's scratchpad paths.
