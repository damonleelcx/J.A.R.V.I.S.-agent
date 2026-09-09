# The body used its length twice, and the wheels were turned 117°

**Found:** 2026-09-09, on the live site, by looking at the car and asking whether
it looked like a sports car. It did not.
**Severity:** high on the conversational path — the geometry was wrong, the
render showed it, and nothing on screen said why.
**Owner:** agent (the contract) and geometry (an undeclared unit).

## Symptom

The live "Sports Car Concept" was a red slab with a black block on top and four
dark discs lying at an angle. Asked to make the body less boxy, FORGE returned a
body that was worse, and the parts panel read:

```
Main Body   extrusion · ? × ? × 4500 mm
```

## Two root causes

### 1. The outline landed facing across the car

An outline is drawn facing the viewer: its own x is the assembly's X, its own y
is Y, and the shape then travels along Z — an extrusion's `depth`, or a sweep's
path. So whatever is drawn WIDE in the outline comes out wide ACROSS the
assembly.

A car's side elevation — the roofline, the taper, everything "less boxy" means —
is 4500 mm across the page. Drawn as an outline, the car's LENGTH lands on the X
axis and the body comes back 4500 wide and 1900 long: length and width swapped,
2.4x on the worst axis.

The first diagnosis in this document was wrong and is kept here because the
wrong version is instructive. Reading only the reply ("a swept profile") and the
first failing document (an `extrusion` with `depth` equal to its outline's span)
suggested "the length was used twice". That is one way it shows up. The general
fault is the orientation, and the fix aimed at the specific one did nothing —
see the measurements below.

### 2. `rotation` was in radians and nothing said so

Found while writing the fix for (1): the first draft of the new guidance said
`rotation [0, 90, 0]`, which under the shipped convention meant **90 radians**.

`RotationMatrix` takes radians, correctly and by design — the kernel and the
renderer must read the same nine numbers rather than each implementing the same
trigonometry. The mistake was that the DOCUMENT was in those radians too, and the
document is written by a model. A model asked for a wheel writes 90 for a quarter
turn, every time.

90 radians is 90 − 14×2π = 2.04 rad = **116.8°**. The wheels were tilted 27° off
vertical, which is exactly what they looked like.

Measured across the whole deployment: every non-zero rotation ever stored was
`[0, 0, 90]` on a wheel — eight values across two documents — and not one of them
meant radians. **The declared unit had zero correct producers.**

## Fix

### What did not work: three attempts at saying it

Measured against `qwen3.7-plus` on the live car, "make the body less boxy",
10 runs each, counting revisions that came back turned on their side:

| contract | turned on its side |
|---|---|
| no guidance at all | 2/10 |
| "the outline is the CROSS-SECTION, not the side view" | 3/10 |
| "an outline is drawn FACING YOU", with the fix spelled out for both extrusion and sweep | 2/10 |

**Not one beat saying nothing.** And the two survivors of the last attempt had
FOLLOWED it: one used the `rotation [0, 90, 0]` it was told to use and left
`depth` at the length; the other sent the path along X as instructed and put the
4500 along it. The model obeys the letter and lands somewhere else, which is
what an axis convention does to prose.

The guidance is kept — it costs nothing and it is true — but it is not the fix
and is not credited as one.

### What was done instead

1. **The size is checked** (`turned.go`). Every whole-prototype revision is
   measured part by part against the model the server holds, through
   `geometry.PartExtents` — the same triangles the viewport draws, so an
   extrusion reports its real size rather than just its `depth`.

   A part whose dimensions came back as a PERMUTATION of the ones it had is
   named as such: "those are the same three dimensions in a different order, so
   the part has been turned on its side: the outline is facing across the
   assembly instead of along it." The model is handed the numbers and the
   mechanism rather than the rule — the same reason `georepair.go` hands back
   the builder's own words.

2. **It gets one chance to put it back**, through the existing repair
   round-trip, taken only if it neither breaks the build nor leaves the part
   turned.

3. **The reader is told either way.** A silent correction is a reply that was
   wrong once and is now quietly right, and the next thing a person does is
   trust the first draft of the next one.

   The tolerance is deliberately generous (1.5x): a rounder body really is a bit
   smaller, and a check that fires on the right answer is one that gets switched
   off. The measured failures are 2.4x.

4. **The document's rotation is degrees** (`Part.RotationRadians`), converted
   once per surface: both Go builders call that method and the renderer calls
   `Forge3D.rotationRadians`. `RotationMatrix` still takes radians and is
   unchanged — the internal convention was never the problem.

   This is the same shape as `depth` on a cylinder (`sizeSynonyms`, mesh.go):
   read what was meant. It re-interprets the eight stored values, all of which
   become correct — they were written as degrees.

5. **The panel says how big an extrusion is.** `? x ? x 4500 mm` showed the one
   number that was right, so a wrong body looked identical to a correct one. The
   panel now measures the outline through `Forge3D.outlineExtent`.

## Why the panel change is part of the fix and not a separate nicety

The wording reduces how often this happens; it cannot stop it. When it happens
anyway, the only thing standing between a wrong body and a person is what the
interface tells them — and what it told them was the one dimension that was
correct. This is the same two-part shape as the previous fix in this area: ask
the model not to, and report it when it does.

## Verification and defence against regression

| Fence | What it holds |
|---|---|
| `TestAQuarterTurnIsNinety` | 90 is a quarter turn, and it reaches the geometry — a d700×250 cylinder turned 90 about Z measures 250 across X |
| `TestTheRendererTurnsDegreesLikeTheBuilderDoes` | the viewport and the builder turn a part by the same angle — run against each other over real angles, not string-matched |
| `TestEnvelopeGrowth_SeesTheLengthCountedTwice` | the live square slab is caught, a correct restyle is not, and an unmeasurable part reports a miss |
| `TestTurned_CatchesTheLengthLandingAcrossTheCar` | the real failing sweep is flagged, with its numbers and the word "turned on its side" |
| `TestTurned_LeavesARealRestyleAlone` | a correctly oriented restyle is not flagged — a check that fires on the right answer gets switched off |
| `TestTurned_SaysNothingAboutAPartThatWent` | a deleted part is vanished.go's, not this one's; two notices for one event teaches people to skip both |
| `TestTurned_ReportsWhatItCouldNotFix` | when the repair fails, the reply says so |
| `TestTurned_TakesAWorkingRepair` | when it succeeds, the fix is taken and said out loud |
| `scripts/extrusion-size-check.js` | the panel reports an extrusion's real size, and still says "?" for an outline it cannot resolve |
| `TestLiveRevisionKeepsTheRestOfTheModel` | the live rate, skipped without `FORGE_LIVE_LLM_TESTS` |

Every fence was proven red by deleting the thing it guards, with mutations that
COMPILE — a first attempt at the rotation drill went red on an unused import,
which proves nothing about the assertion.

### The browser-side fences had never run

`scripts/echo-guard-check.js` and `scripts/voice-fallback-check.js` existed only
in `make check`, which CI does not call. From the day each was written until
2026-09-09, no run of CI had executed either. A fence that CAN go red is not a
fence until something makes it. CI now runs all three in one step.

## Related

- `docs/bugfix/2026-09-09-a-revision-deleted-the-parts-nobody-mentioned.md` — the
  previous fix in this area, and the same ask-then-report shape.
- `sizeSynonyms` in `internal/domain/geometry/mesh.go` — `depth` on a cylinder,
  the precedent for reading what the model meant.

## Open: corner radii on a rounded outline

The 10-run measurement of the finished mechanism had 6/10 revisions build,
against 9/10 and 10/10 on earlier runs of the same request. Every failure was
the same kind and none of them is touched by anything in this change:

```
Main Body — crosses itself once its arcs are drawn
Main Body — the corner radii at outline points 1 and 2 need 2823 of the 1254 between them
```

That is `profile.go` validating a two-dimensional outline, before any placement,
rotation or size check runs. "Less boxy" makes the model round the corners of the
outline, and it picks radii larger than the edges they sit on.

At n=10 this is not distinguishable from variance, and it is NOT claimed to be
either caused or cured here. It is a real and separate failure mode — the repair
round-trip is given the builder's exact words and still does not fix it — and it
is written down so the next person measuring this request knows it is there.
