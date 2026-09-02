# Character portrait assets

FORGE's visual identity has two forms, and they do different jobs.

**The sigil** (`internal/persona/avatar.go`) is drawn as inline SVG: three swept
blades around a glowing cyan core, taken from the character's hair ornament. It
is the *working* mark — it carries state, and it stays legible at 22px in a table
row, which a detailed illustration cannot.

**The portrait** is *presence*. It goes where there is room for her: the console
header, a goal page, the sign-in screen.

## Expected files

Drop these in beside this README. Every one is **optional at runtime** — a
missing portrait falls back to the sigil, because a decorative asset must never
be able to take out a status indicator.

| File | Expression | What the crop should show | Shown when |
|---|---|---|---|
| `calm.png` | Calm | Head and shoulders, level and unhurried. The default presence. | Idle, Stopped |
| `thoughtful.png` | Thoughtful | Considering — hand near the chin. | Thinking, Waiting for you |
| `focused.png` | Focused | Narrowed and deliberate. | Working |
| `bright.png` | Bright | Open smile. | Done |

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

## Palette

Taken from the character and used across the whole product, so a reader learns it
once:

| Token | Value | Where |
|---|---|---|
| `--shell` | `#f7f8fa` | The uniform white; blade highlights |
| `--gold` | `#d9b25c` | Trim; the sigil's blade shadow and ring |
| `--core` | `#4fd8e8` | The ornament, collar gem, and wrist display. **Means "active" everywhere** — the sigil's centre, a focused input, a primary action. |
