# The sigil was black, because an `<img>` cannot see the stylesheet

**Found:** 2026-09-09, reported as *"can't see logo clearly in dark mode"*.
**Severity:** high, and higher than the report. The mark was not merely
low-contrast: it was rendering with **no colour and no state at all**, in both
themes, on the workbench header, the orb badge and every console goal row,
since the sigil endpoint shipped.
**Owner:** httpapi (assets) — the mark's server/client contract.

## Symptom

The FORGE mark in the workbench header was a black silhouette on the near-black
header. In dark mode it was effectively invisible; in light mode it was a black
blob with none of the character's colours and no ring.

## Root cause

`persona.AvatarSVG` deliberately carries **no paint of its own**. Every colour
is a class resolved by `avatar.css` against theme tokens, and every one of the
six states is an animation in that same stylesheet:

```html
<stop offset="0%" class="fa-wing-lit"/>          <!-- var(--wing-lit) -->
<circle class="fa-ring" ... stroke-width="3"/>   <!-- var(--gold)     -->
```

That is the right design: one server-side implementation of the mark renders
correctly in both themes without the server needing to know which theme is on
screen.

It carries one hard constraint, and both consumers broke it. `workbench.js` and
`console.js` placed the mark like this:

```js
badge.innerHTML = '<img src="/v1/meta/sigil?state=' + want + '&size=64" …>';
```

**An SVG loaded through `<img>` is an isolated document.** It cannot see the
host page's stylesheet, so not one of those classes resolved:

| element | intended | actual behind `<img>` |
|---|---|---|
| blade gradient | white → gold | both stops fall back to black |
| `.fa-ring` | gold stroke | `stroke` has no initial colour → **not drawn at all** |
| `.fa-core` | periwinkle gem | black disc |
| six state animations | breathe / arc / gate | **none run** |

Measured in the browser, before vs after, on the real stylesheets:

```
before (<img>):   animations 0
after  (inline):  idle 1 · thinking 2 · working 2 (arc) · blocked 2 (gate)
                  failed 0 (✕, red ring) · done 0 (✓, green ring)
                  ring stroke rgb(217,178,92) dark / rgb(138,109,33) light
```

## Why it cost more than it looked like

The failure is silent in the worst way: the request succeeds, the SVG is valid,
the element is the right size, nothing errors, and no console message appears.
So it presented as a **styling nit** — "the logo is hard to see" — when the
actual loss was the status indicator. `avatar.go` states the sigil's purpose as
carrying state at 24px *because a portrait cannot*; behind an `<img>` it was
carrying no state in any theme. The report was about dark mode; the defect was
not theme-specific at all.

The console page masked it further: its topbar uses `{{.Avatar}}`, the portrait
PNG, which rendered perfectly. Same page family, same session — only the
surfaces that went through `<img src="/v1/meta/sigil">` were black.

## Fix

`assets/sigil.js` — one shared placer that inlines the same server markup into
the document, where the stylesheet reaches it.

- `ForgeSigil.place(el, state, size)` for a live element (workbench header, orb
  badge).
- `ForgeSigil.slot(state, size, cls)` + `ForgeSigil.hydrate(root)` for callers
  that build rows as strings and assign once (console). The slot reserves the
  final box, so hydration causes no reflow, and keeps `aria-hidden="true"` so
  decorative rows announce exactly what they did before.
- Responses are cached per `state:size`, so a page cycling idle → thinking →
  idle fetches twice, ever. Request count is unchanged from the `<img>` it
  replaced.

The server stays the single implementation of the mark and of the state rules.
Only *where the markup lands* changed.

## Verification

Both themes, on the real `shell.css` + `avatar.css` and the real handler output,
side by side with the old `<img>` as the control: the inlined mark shows gold
ring, lit blades and periwinkle core; the `<img>` control reproduces the black
silhouette from the report exactly. All six states then checked for the class,
the state-specific element (`.fa-arc`, `.fa-gate`, `.fa-mark`), the resolved ring
stroke, and the running animation count — table above.

## Prevention

`internal/httpapi/sigil_paint_fence_test.go`:

- **`TestSigilIsNeverConsumedAsAnImage`** — fails when any asset script builds an
  `<img>` around `/v1/meta/sigil`. Proven to go red by reinstating the old line
  in `workbench.js`; it reported `workbench.js:1110`. Scoped to `<img>` and not
  to the endpoint, because fetching the endpoint is what `sigil.js` must do.
- **`TestSigilMarkupHasNoPaintOfItsOwn`** — pins the premise the rule rests on.
  If `AvatarSVG` ever gains real `fill`/`stroke`/`stop-color` attributes, the
  `<img>` route stops being wrong and the fence above becomes cargo. This test is
  what tells the next reader the premise changed, instead of leaving an
  apparently arbitrary rule to be deleted.
- **`TestStripJSCommentsKeepsCode`** — the first fence reads comment-stripped
  source, because `sigil.js` explains the rule by quoting the shape it forbids
  and tripped itself. A stripper that ate real code would leave the fence
  passing while checking nothing, so this asserts the stripped source still
  contains each call site.

## Related

- `docs/bugfix/2026-09-08-csp-blocked-her-own-voice.md` — the same shape: a
  browser-level isolation rule broke a feature with no error anywhere, and the
  visible symptom pointed at the wrong subsystem.
