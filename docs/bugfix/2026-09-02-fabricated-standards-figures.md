# FORGE quoted a standard's dimension and got it wrong, in the honesty field

**Date:** 2026-09-02 · **Phase:** 6 (workbench) · **Severity:** high — a specific,
actionable, wrong number presented as fact · **Owner:** agent/converse

## Symptom

Asked for a NEMA 17 motor mount, FORGE wrote this in its `assumptions` list:

> "Base plate is 50x50x5 mm with centered NEMA 17 bolt pattern (holes at ±20.5 mm
> on both axes)"

±20.5 mm is a **41 mm** bolt pattern. NEMA 17 is **31 mm square**. The figure is
wrong, it is specific enough to be cut metal from, and it appeared in the one
field whose entire purpose is to separate what FORGE knows from what it chose.

Across four runs of the identical prompt the same model produced 31 mm correctly
twice and the fabricated figure once. Raw captures and the analysis that produces
these numbers: `docs/spikes/2026-09-02-zoo-text-to-cad/`.

## Why nothing caught it

Everything below the model was working. The reply parsed, `validate()` passed,
the geometry rendered, and the provenance banner said what it always says.

The gap was conceptual: **two different things were being written to one field.**

- A dimension FORGE *chose* — "you did not say, so I picked 60 mm" — is an
  assumption, and belongs there.
- A figure FORGE *recalled* from a published standard — "NEMA 17 is 31 mm" — is
  not an assumption at all. It is a claim about the world, recalled from model
  weights, with no reference source anywhere in this deployment to check it
  against.

Collapsing the second into the first laundered a factual claim into a stated
preference. A reader scanning `assumptions` sees "things FORGE decided", and
decisions are not the kind of thing that can be *wrong about NEMA 17*.

## Why the prompt was not the fix

The rule already existed. `persona.Soul`'s `no-fabrication` commitment says,
verbatim:

> "I do not invent measurements, citations, standards, file contents, test
> results, or the outcome of anything I did not observe."

It is marked immutable and it is shown to the model on every call. It was not
followed, because from the inside a fabricated figure is indistinguishable from a
remembered one — there is no felt difference between recalling 31 and recalling
20.5. Adding a firmer sentence would have been asking the failing component to
detect its own failure.

## Fix

Three parts, only one of which the model can affect.

**1. The framing now names the distinction** (`internal/agent/converse.go`).
`assumptions` is defined as what FORGE *chose*; a figure from a published
standard is explicitly excluded from it, with the reason stated — that FORGE is
recalling rather than reading, and that a wrong figure attached to a real
standard is more dangerous than no figure.

**2. A detector, server-side, that the model cannot skip**
(`internal/agent/standards.go`). It scans the reply's own text — speech, detail,
assumptions, part notes — for references to named standards, from a table of
families (NEMA, ISO, DIN, ANSI, ASME, ASTM, JIS, EN, SAE, BS, GB, UL, metric and
unified threads, pipe threads, IP ratings, AWG, bearing series). It runs inside
`Reply.validate()`, which is the single choke point both the buffered and the
streamed reply path go through: a rule enforced in one of two paths holds only
until somebody uses the other one.

**3. The reader is told.** The claims travel as a `recalled` stream event and are
rendered twice — as a block on the turn that made the claim, carrying each
sentence verbatim, and as a summary line in the provenance banner. Gold, not red:
most citations are right, and styling this as an error would teach people to
dismiss the one that is not.

### Two things it deliberately does not do

**It does not block or rewrite.** Refusing the turn would stop the main flow over
a sentence; deleting the number would destroy information the person may need.
The system says what it did, in the same shape as "Drawn approximately" for
shapes the renderer cannot draw.

**It does not pair a figure with a standard.** The first live run rendered
`M3 — 42.3 mm, ±0.1 mm, 31 mm, 3.2 mm` from a sentence mentioning both M3 screws
and a NEMA 17 face — an M3 screw is not 42.3 mm, so the panel had invented a
pairing while warning about invented numbers. Attributing each figure to its
nearest standard was considered and rejected: proximity in prose is a guess, and
a guessed pairing is the same failure one step quieter. A claim now reports only
what is known — *this sentence names these standards and contains these
figures* — and carries the sentence so the reader pairs them by reading it.

## What it does not catch, stated because partial coverage that says so beats
total coverage that is asserted

A figure recalled from an **unnamed** source — "a typical stepper flange is about
42 mm" — names no standard and is not flagged. The banner claims only what the
detector does.

## Regression fences

`internal/agent/standards_test.go`, built from the **actual captured text**, not
from invented examples:

| test | what it holds |
|---|---|
| `CatchesTheFabricatedNema17Pattern` | the original sentence is flagged, in `assumption`, carrying `±20.5 mm` |
| `DoesNotFlagAChosenDimension` | "Base plate is 50x50x5 mm" stays silent — if everything is flagged the flag means nothing |
| `CatchesAFigureWithNoGeometry` | a standard quoted in prose is caught with no prototype present |
| `FlagsBareConformance` | "matches the NEMA 17 footprint" is a claim even with no number |
| `DoesNotPairAFigureWithTheWrongStandard` | one sentence, two standards → one claim carrying both, never `M3 — 42.3 mm` |
| `CatchesFastenerAndRatingFamilies` | M3, bearing series, IP rating, ISO designation |
| `QuietOnOrdinaryProse` | three ordinary sentences flag nothing |
| `PopulatedByValidate` | the scan runs on the path both reply modes share |
| `IsDeterministic` | two scans of one reply agree |

**Mutation drills run 2026-09-02**, each reproducing a real way the fix could rot:

- detector stops scanning `assumptions` → `CatchesTheFabricatedNema17Pattern`
  fails with *"the fabricated NEMA 17 bolt pattern was not flagged at all"*
- figures dropped from claims → the same test fails with *"figures [] do not
  carry the number that was wrong"*
- `validate()` stops populating `Recalled` → `PopulatedByValidate` fails

All three go green again on restore.

## A bug the fences found while being written

`splitSentences` originally split on `.` with `strings.FieldsFunc`, which cut
"42.3 mm" into "42" and "3 mm". A bare "42" carries no unit, so the figure
regexp matched nothing and the claim was reported with **no number attached** —
the failure mode being fixed, reintroduced inside the fix. Caught by
`CatchesAFigureWithNoGeometry`, which is why that test uses a decimal dimension
rather than a round one.

## Acceptance

Live on `/workbench` against `qwen-plus`. Prompt: *"A motor mount bracket for a
NEMA 17 stepper … Give the bolt pattern and the fastener size."* Eight claims
flagged across all four locations — assumption, detail, part note, spoken — each
with its verbatim sentence, and summarised in the provenance banner.

## Related

- `internal/persona/soul.go` — `no-fabrication`, `truthful-state`
- `docs/prd.md` RSN-06, AGT-08, VIS-06
- `docs/spikes/2026-09-02-zoo-text-to-cad/` — the measurements that found it
