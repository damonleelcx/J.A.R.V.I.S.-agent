# The view buttons were bound to the whole workbench

**Found:** 2026-09-08, reported as *"these buttons doesn't work"* against
Iso / Front / Top / Side.
**Severity:** high on the product's primary surface — the only way to turn a
proposed shape, and PRD §1.2's whole premise is "talk while looking at the
thing".
**Owner:** httpapi (workbench controls).

## Symptom

Clicking Iso, Front, Top or Side did nothing. The camera never moved, and the
button never took the pressed state.

## Root cause

`data-view` means **two unrelated things** in this page:

- a **camera** — the four buttons in `.viewctl`;
- a **pane** — the narrow layout's Talk/Stage/Work tabs, and, critically,
  `.wb-body` itself, where `mobileNav` writes the current pane.

`initControls` bound its click handler with a document-wide selector:

```js
document.querySelectorAll('[data-view]')      // .wb-body matches this
```

So the handler was attached to the **entire workbench body**. Every click
anywhere in the workbench bubbled up to it and called
`studio.viewFrom('stage')` — a pane name, not a camera — while setting
`aria-pressed="false"` on all four camera buttons.

The buttons therefore did work, and were then immediately undone: the button's
own handler ran, the click bubbled to `.wb-body`, and the body's copy of the
same handler overwrote the result. Measured before the fix — after clicking
**Front**, `front` was `false` *and* `iso` had been cleared to `false`: a
handler had plainly run and left nothing selected.

`mobileNav`'s own comment states the intent this broke:

> The selection is held in an attribute on .wb-body rather than in a variable,
> so the CSS is the only thing that acts on it

This was the second thing that acted on it.

## Why it survived

The mistake is invisible at the call site. `[data-view]` reads as "the view
buttons" and is wrong only because of a second, distant use of the same
attribute added later — `f77e7b8` introduced the narrow-layout switcher, which
is when these buttons stopped working. Nothing in either change looks wrong on
its own; they are only wrong together.

## The fix

```js
var cameraButtons = document.querySelectorAll('.viewctl [data-view]');
```

Both selectors scoped, and the list captured once — a document-wide clear would
also stamp `aria-pressed` on the mobile tabs, which are a different control
answering a different question.

## Verification

On a live server, against the real page:

| Clicked | Camera pressed | Mobile tab |
|---|---|---|
| front | `[front]` | `stage` |
| top | `[top]` | `stage` |
| side | `[side]` | `stage` |
| iso | `[iso]` | `stage` |

and clicking elsewhere in the body no longer disturbs the selection. The camera
visibly moves: **Front** renders the 1900 × 800 face, **Top** the 1900 × 4500
footprint — different silhouettes, so it is really moving.

Fences: `TestCameraButtonsAreScopedToTheirContainer` forbids the document-wide
selector and requires the scoped one; `TestTheViewBarStillHoldsTheCameraButtons`
holds the `.viewctl` container the scoping depends on, because renaming it would
leave the handler bound to nothing — the buttons would go quiet again, silently
this time rather than by being overridden. Proven red by restoring the unscoped
selector.

## Two measurement traps hit while diagnosing this

Both wasted time and both are worth remembering.

1. **`canvas.toDataURL()` is not a measurement here.** The renderer uses
   `alpha:false` and does not preserve the drawing buffer, so it returns a
   constant blank image — identical hashes before and after a camera move, and
   identical between broken and fixed builds. Assert on camera/DOM state, or
   compare screenshots.
2. **HTTP 200 proved a server was listening, not that MY server was.** An
   orphaned `forged` still held `:8099`, the new process failed to bind, and the
   stale build answered every request — so a "verified" fix was verified against
   the code it replaced. The log said `address already in use` and was not read.
   Check the served asset contains the change, not merely that the port answers.
