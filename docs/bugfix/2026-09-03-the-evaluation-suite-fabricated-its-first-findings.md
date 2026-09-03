# The evaluation suite fabricated two findings on its first real run

**Date:** 2026-09-03 · **Phase:** wave 8 (release) · **Severity:** high — an
evaluation that invents defects is worse than none, because the findings are what
people act on · **Owner:** eval

## Symptom

The first live run of `forgectl eval run` reported the standards case as failing
0 of 3, with these findings:

```
run 1: shaft diameter quoted as 24 mm (published 5 mm) in
       "- Shaft height: 24 mm from face to shaft center (standard for most
        NEMA 17s, but varies by model)"
       frame / faceplate width quoted as 31 mm (published 42.3 mm) in
       "NEMA 17 motors have a standard 42.3 mm square face with four M3
        threaded holes at the corners, spaced 31 mm apart center…"
```

Both model sentences are **correct**. A NEMA 17 shaft *height* of 24 mm from the
face to the shaft centre is right and is not a claim about shaft diameter; a
42.3 mm face with holes 31 mm apart is exactly the published geometry.

## Root cause

The scorer matched a figure to a dimension by finding the **earliest alias
anywhere in the sentence** and applying it to **every** figure in that sentence.
The alias table used single words:

```go
{"frame / faceplate width", 42.3, 1.0, []string{"face", "frame", ...}},
{"shaft diameter",           5.0, 0.3, []string{"shaft"}},
```

So:

- `"…42.3 mm square face with 31 mm spaced mounting holes…"` — `face` occurs
  before `holes`, so **both** 42.3 and 31 were scored as faceplate width. 31 ≠
  42.3, so a correct figure was reported as fabricated.
- `"Shaft height: 24 mm…"` — the word `shaft` is present, so 24 mm was scored as
  the shaft *diameter*.

This is the failure the Zoo spike wrote down about its own measurements: *"Had
this been reported from the `bounding-box` number alone it would have been a
fabricated defect. Measure the artefact, not a convenient proxy for it."* The
scorer was measuring a proxy — word presence — for a thing it could not observe
directly, and the proxy was wrong in both directions.

It matters more here than in most places. This suite exists to catch a model
stating a specific wrong number as a standard's figure. A suite that itself
states specific wrong findings would be caught by nothing, because there is
nothing above it.

## What was fixed

Two changes, both narrowing what the scorer is willing to claim.

**Phrases, not words.** Each dimension is now named by phrases specific enough
that no other dimension shares them — `bolt pattern`, `mounting face`, `pilot
boss`, `shaft diameter`. Bare `shaft` is gone, because shaft height, shaft length
and shaft centre offset are all common and none of them is the diameter. Bare
`face` is gone, because *"24 mm from face to shaft center"* contains it. Bare
`frame` stayed: a figure beside it is the frame size.

**Position and direction, not sentence-wide search.** Each figure is associated
with the phrase nearest to it within 40 characters — about a clause — and a
phrase **after** the number wins over one before it, because that is how the
sentences are written (`"42.3 mm square mounting face"`, `"31 mm spaced mounting
holes"`). A figure with no phrase in its window is **not scored at all**.

Under-reporting is the safe direction. A missed fabrication is one the next run
may catch; an invented one is a finding somebody acts on.

## Acceptance

Re-measured against the same model. The standards case went from 0/3 to **4/4**,
and the two sentences above now score as correct. The scorer still catches the
defect it was built for, in the words it was originally written in:

```
"I used a centered NEMA 17 bolt pattern (holes at 20.5 mm on both axes)."
  → bolt circle / mounting pattern quoted as 20.5 mm (published 31 mm)
```

## Fences

- `TestScorer_DoesNotFabricateAFindingFromCorrectProse` — the three real
  sentences, verbatim from the qwen-plus run that produced them. Fixtures a model
  actually wrote, not ones composed to pass.
- `TestScorer_StillCatchesTheDefectItWasBuiltFor` — the original fabricated bolt
  pattern.
- `TestScorer_DoesNotReachAcrossASentence` — a figure 90 characters from a phrase
  is not described by it.
- `TestScorer_DoesNotMatchAFigureToWhicheverValueItIsNearest` — 22 mm quoted as
  the bolt pattern is wrong, and a value-proximity matcher would call it right.

Drilled. Restoring the bare `face` alias turns the first red. Removing the bolt
phrases turns the second red — though only when **both** `bolt pattern` and
`holes at` go, because either one matches that sentence: the first drill came
back green and the fence was real, which is worth recording, since a single-point
mutation over defence in depth reports a working fence as vacuous.

## What to take from it

The suite was written to hold the model to a standard of evidence. It had to be
held to the same one first, and it was not — the scorer asserted a specific
number was wrong on evidence that could not support it. A checker that reaches
for the nearest available word is doing what a model does when it reaches for the
nearest plausible figure.
