# The workbench hid half of itself on a phone

**Found:** 2026-09-07, while adding a light theme and a mobile layout.
**Severity:** high on the affected devices — the product's primary surface lost
its primary control path, silently.

## What happened

Opened at 375px, the workbench was a header and a stage. Nothing else.

Gone, with no affordance suggesting anything was missing:

- the conversation transcript — everything FORGE had said in text;
- the **Delete** control for the conversation, which the markup's own comment
  calls out as required to be reachable at all times (PRD AUD-07);
- Parts, Variants, Industry, People;
- **Proposed work** — the accept and reject controls for work FORGE proposes.

The product's model is "talk while looking at the thing" (PRD §1.2, §5.3). On a
phone it kept the thing and dropped the talking.

## Root cause

Two breakpoints in `internal/httpapi/assets/workbench.css`, and nothing that
undid either:

```css
@media (max-width: 1200px) { .wb-right { display: none; } }
@media (max-width:  820px) { .wb-left  { display: none; } }
```

This is not a layout that degrades — it is content deletion by viewport width.
The rails were the only route to those controls, so hiding them removed the
capability rather than relocating it.

There was also a middle band, 820–1200px, where the right rail was already gone
while the layout still looked deliberate. A laptop at 1150px lost Proposed work
and gave no sign of it.

**Why it survived this long:** every rule involved is correct in isolation and
the page looks composed at every width. Nothing errors, nothing logs, and a
reviewer reading the diff that introduced those two lines sees a reasonable
narrow-screen simplification. It is only visible if somebody opens the workbench
on a phone and goes looking for something they know should be there.

## The fix

One breakpoint, one behaviour. Below 1200px the three regions share a single
grid cell and a **Talk / Stage / Work** switcher selects between them
(`.wbmobile` in `workbench.css`, wired by `mobileNav()` in `workbench.js`).
Nothing is hidden by width any more; everything is one tap away.

Two constraints shaped it, and both are stated in the stylesheet at the rules
that depend on them:

- **The stage is never `display: none`.** It holds the WebGL canvas, and a
  `display: none` ancestor collapses that canvas's client box to zero — the
  viewport returns at 640×480 with the camera elsewhere. The rails overlay it
  instead.
- **The console bar leaves the stage.** The microphone, the text field and
  stop-speaking live in `.voice`, a child of `.stage`; leaving it there would
  have made it disappear whenever the reader opened the conversation, which is
  the same defect in a smaller box (AUD-06, AUD-07). At this width it is
  absolutely positioned to the bottom of the stage, above the switcher, so it is
  present in all three sections.

Fixed alongside, all found by measuring rather than by looking:

| Defect | Fix |
|---|---|
| `.wb { height: 100vh }` with `overflow: hidden` — the bottom of the app was under the phone's address bar with no way to scroll to it | `100dvh`, with `100vh` kept above it as the fallback |
| `.stagetabs { flex: 0 0 auto }`, no overflow — two of the six stage tabs were off-screen and unreachable | `min-width: 0` + `overflow-x: auto` |
| `.viewctl`, `.sliders` and `.states` all anchored near `top: 12px`; the sliders sat on top of the view buttons | the two panels move below, side by side, at ≤700px |
| `.wb-top` and `.topbar` — no-wrap flex rows holding script-filled identifiers with no width bound | `flex-wrap` plus ellipsis |
| Touch targets from 18px (remove-attachment) to 38px | a `pointer: coarse` block bringing them to 44px |
| `.provenance { bottom: 168px }` — a guess at the console bar's height | `--wb-dock-h`, measured by `ResizeObserver` |
| `.event` in `console.css` never collapsed its 74px actor column | stacks below 560px |

## A second defect this uncovered

`Forge3D.Studio` never painted on mount. It is a draw-on-demand renderer and
nothing demanded a draw until geometry arrived, so a freshly opened workbench
had a WebGL canvas that had never been cleared.

That was invisible for as long as there was one theme: a canvas created with
`alpha: false` and never drawn composites as opaque **black**, which is
indistinguishable from this viewport's intended near-black ground. Against the
light ground it is a black rectangle in the middle of the page.

It also only reproduced on a settled window — any resize calls `draw()`, so
anyone who dragged a window edge, or ran a test that emulated a viewport, saw
the correct ground and could not reproduce it. Confirmed by reading the canvas
back with `gl.readPixels`, which returned `[0, 0, 0, 255]`: never drawn, not
drawn wrong.

Fixed by drawing once at the end of the constructor
(`internal/httpapi/assets/forge3d.js`).

## Regression cover

- `TestNoPageUsesAClassStyledOnlyInAStylesheetItDoesNotLoad` — extended to the
  landing page, which was outside it until it grew classes of its own.
- `TestStylesheetsDefineColoursAsTokens` and
  `TestEveryLightDarkTokenHasAPlainFallback` in
  `internal/httpapi/theme_fence_test.go` — both drilled by mutation on
  2026-09-07 and confirmed to go red.

The layout itself is not unit-testable from Go: it is a media query, and no
assertion available here can tell whether a rule matched at a given width. It
was verified in a browser at 375×780 and at 1440×860, in both themes, by reading
computed `display` values for `.wb-left`, `.stage`, `.wb-right` and `.wbmobile`
rather than by looking at screenshots — a check that would have caught the
original defect and does not depend on anybody noticing an absence.

**The check to repeat before a release:** open `/workbench` at 375px wide and
confirm all three of Talk, Stage and Work reach content, and that the
microphone and text field are present in each.
