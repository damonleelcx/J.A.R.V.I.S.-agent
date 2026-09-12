# Character portrait assets

FORGE's visual identity has three forms, and they do different jobs.

**The sigil** (`internal/persona/avatar.go`) is drawn as inline SVG: three swept
blades around a glowing cyan core, taken from the character's hair ornament. It
is the *working* mark — it carries state, and it stays legible at 22px in a table
row, which a detailed illustration cannot.

**The portrait** is *presence*. It goes where there is room for her: the console
header, a goal page, the sign-in screen.

**The figure** (`figure.png`) is the landing page's *subject*. Full length, three
quarter view, standing at the right of the page with the copy to its left. It is
the only one of the three that is cut out of its ground — see "The figure" below.

## The files

These are generated from the character sheet, not hand-cut:

```bash
go run ./tools/portraitcrop -sheet path/to/golden-hair-ai-agent-character-sheet.png
```

One run writes all five assets — the four expressions and the figure.

The crop regions live in `tools/portraitcrop/main.go` as fractions of the sheet,
so a re-render at a different resolution still lands correctly. Run with
`-contact` to write only a contact sheet to `$TMPDIR` and check the crops before
overwriting the assets — coordinates picked by eye off a scaled preview are wrong
about as often as they are right, and a portrait cropped through the forehead is
exactly the kind of thing that ships because nobody looked.

Every file is **optional at runtime** — a missing portrait falls back to the
sigil, because a decorative asset must never be able to take out a status
indicator.

| File | Expression | What the crop should show | Shown when |
|---|---|---|---|
| `calm.png` | Calm | Head and shoulders, level and unhurried. The default presence. | Idle, Stopped |
| `thoughtful.png` | Thoughtful | Considering — hand near the chin. | Thinking, Waiting for you |
| `focused.png` | Focused | Narrowed and deliberate. | Working |
| `bright.png` | Bright | Open smile. | Done |
| `figure.png` | — | Full length, three-quarter view, ground removed. | The landing page, always |

`persona.PortraitManifest()` is the source of truth for this table; the test
`TestEveryExpressionHasAPortraitEntry` fails if code can ask for an expression
that names no file.

## Notes on the crops

- **Square, centred on the face**, roughly head-and-shoulders. The frame is a
  circle, so anything in the corners is lost.
- **512×512 minimum.** Displayed at 72px in the console header and larger on a
  goal page.
- **Transparent or pale background.** The console is dark; a hard white rectangle
  will read as a pasted sticker.
- Four expressions, not six. `Waiting for you` reuses *thoughtful* and `Stopped`
  reuses *calm* deliberately — inventing a distressed expression would dramatise
  a state that is completely normal, and a level waiting look is the truthful one.

## The figure

`figure.png` is cut differently from the other four and it is worth knowing why
before re-cutting it.

**It has an alpha channel, and it has to.** The expressions are opaque squares
shown inside a circle, so the sheet's ground is simply hidden by the frame. The
figure stands on the page with nothing masking it, on a dark theme and a light
one, so the ground has to come off.

**The ground cannot be removed by colour alone.** On this sheet her white uniform
is `(244,235,229)` and the ground behind it is `(246,240,237)`. Six units apart.
Anything that keys out "near white" takes her jacket and her boots with it. What
separates them is *reachability*: the ground is one connected region touching the
frame, so the key floods in from the border and is stopped by distance from a
per-row model of the ground and by any real edge it meets. An enclosed white — a
sleeve, a boot — is never reached even though its colour would have qualified.

**The cast shadow goes too.** She is lit standing on a floor, and the floor is
not part of her. The shadow is too dark to match the ground and too smooth to be
stopped as an edge, so it is keyed separately as *the ground scaled down*: same
hue, less light. Without that it survives as a grey smear pooling at her boots —
invisible on the light theme, obvious on the dark one.

**Judge it, do not assume it.** `-contact` writes
`$TMPDIR/forge-figure-contact.png`, which is the cut composited over both the
dark and the light ground side by side. A fringe that has kept the sheet's warm
white is invisible on one and a halo on the other, so a single-ground proof
proves nothing. The tool also refuses to write the asset if the key removed less
than 25% or more than 80% of the crop — too little means the ground model missed,
too much means it ate the subject.

`TestTheFigureHasNoGroundLeftOnIt` in `internal/httpapi` reads the shipped bytes
and fails on an opaque corner, so a re-cut that goes wrong cannot reach a user
quietly.

## Where the figure is placed

On a wide window the landing page reads left to right as copy, then the field's
raymarched white mass, then her. On a phone there is no room for that, so it
stacks: the mass above, the copy over it on its scrim, and her at the foot of the
first screen. Either way none of the three may overlap another.

That cannot be a tuned offset. Her width comes from her height, so the room left
for the mass depends on the window's proportions and not just its width — and on
a phone the free room runs the other way entirely. So the page measures the free
*rectangle* from its own layout and hands it to the shader (the `band` function
where `portal-field.js` is mounted, and `uBand` inside it). The mass is placed at
its centre, shrunk to fit its tighter axis, and its scroll orbit is clipped to
its height so it cannot swing down through her; if there is no room worth drawing
in, it is not drawn.

Her own outline carries a small zero-offset shadow (`--figure-halo`) so that a
bright wavefront passing behind her never leaves that edge with nothing between
two bright things. `TestTheFigureKeepsItsEdgeAgainstTheField` fails if it goes.

## Palette

Taken from the character and used across the whole product, so a reader learns it
once:

| Token | Value | Where |
|---|---|---|
| `--shell` | `#f7f8fa` | The uniform white; blade highlights |
| `--gold` | `#d9b25c` | Trim; the sigil's blade shadow and ring |
| `--core` | `#4fd8e8` | The ornament, collar gem, and wrist display. **Means "active" everywhere** — the sigil's centre, a focused input, a primary action. |
