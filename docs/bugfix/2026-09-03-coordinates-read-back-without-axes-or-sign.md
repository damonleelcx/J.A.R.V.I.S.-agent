# A coordinate was read back as three loose numbers

**Date:** 2026-09-03 · **Phase:** 9.6 (FORGE speaks in a room) · **Severity:**
medium — AUD-04 unmet on both speech surfaces, silently · **Owner:** the
readback rules, `internal/media/readback.go` and `assets/voice.js`

## Symptom

FORGE says, of a part it has just compared: *"Bracket origin is at (12.5 mm,
0 mm, -40 mm)."*

Out of the speakers that is: **"twelve point five millimetres, zero
millimetres, forty millimetres."**

Two things are wrong with it and neither is visible.

1. **The axes are gone.** Parentheses and commas are silent, so the listener
   hears three numbers in a row with nothing to say which one is X. A
   coordinate you cannot assign to an axis is not a coordinate.
2. **The sign is gone.** `-` is silent too. The part is reported 80mm from
   where it actually is, and it is reported confidently.

The second is worse than the first. A listener who cannot tell which axis is
which knows they are confused and asks. A listener who hears "forty
millimetres" for `-40 mm` writes down a plausible number and stops.

As with the sibling defect below, nothing else looked wrong: the screen was
correct, the transcript was correct, the audio arrived inside the AUD-02
budget, and the utterance logged normally.

## Root cause

PRD **AUD-04** reads "technical readback of numbers, units, tolerances, IDs,
coordinates". The rules implemented the first four. **Coordinates were never
implemented at all** — not in the browser copy, not in the server copy that
was added earlier the same day.

This is not drift between the two copies, and the fence that guards drift
(`TestTheReadbackRulesHaveNotDrifted`) could not have caught it: both copies
were equally incomplete, so they agreed. The fence compares the copies to each
other. Nothing compared either copy to the requirement.

That is the general shape worth remembering: **a consistency fence cannot find
a requirement neither side implements.** It measures agreement, and two wrongs
agree.

Why it survived: the readback rules were written from the failures somebody had
actually heard — `2.5mm`, `±0.1`, `v0.2.0`, `req_A1B2C3`. Coordinates are
produced by a different part of the system (`position()` in
`internal/domain/geometry/compare.go`) and reach the voice only when a
comparison is spoken aloud, which is a narrower path than the ones that got
attention.

## Fix

One rule added to each copy, in the same position in the sequence:

- `internal/media/readback.go` — `coordinateExpr` plus a table entry
- `internal/httpapi/assets/voice.js` — `COORD` plus the matching `.replace()`

A parenthesised triple of signed numbers, each optionally carrying its unit, is
spoken with its axes named and its signs said:

> `(12.5 mm, 0 mm, -40 mm)` → *"X 12.5 millimetres, Y 0 millimetres, Z minus 40
> millimetres"*

Three decisions inside that are deliberate and are commented at the code:

- **Triples only.** `(1, 2)` in a sentence is a list, not a position, and
  nothing in this domain has a two-axis position. Matching pairs would put axis
  names into ordinary prose.
- **The minus is spelled only inside a coordinate.** A general rule would also
  rewrite dates (`2026-09-03`) and ranges (`5-10mm`), where the hyphen is not a
  sign.
- **The datum frame is NOT added.** It is not in the text, and this layer
  rewrites what it is handed rather than inventing premises (**RSN-06**: no
  fabricated measurements). A coordinate that has to carry "assembly origin, Y
  up" must carry it from its producer — which is what `Frame` in
  `internal/domain/geometry/units.go` already says, and which belongs to
  **WRK-05** ("units, precision, **frames**, tolerance…"), not to AUD-04.

## Verification

- `TestReadbackMakesTextTranscribable` — six cases added: axes named, sign
  kept, no-unit coordinate, coordinate alongside a tolerance, and two negative
  cases proving prose is untouched (a pair, and words in parentheses). 23
  subtests run.
- `TestTheReadbackRulesHaveNotDrifted` — **drill run**: deleting the new
  `.replace()` from `voice.js` alone turns it red ("the browser applies 11
  readback substitutions and the server applies 12"). The file was restored
  byte-identical afterwards.
- **The browser copy was executed, not just counted.** The 23 cases were
  extracted from the Go table and run through the real `readable()` from
  `voice.js` under Node: all 23 agree with the Go copy. The drift fence counts
  rules and cannot check that matching rules *mean* the same thing; this
  covered that gap once, by hand, at the point the rule was added.
- Full suite: 29 packages pass.

## Prevention

The fence stays as it is — it guards the failure it was built for. What this
defect shows is the failure *next to* it, and there is no cheap fence for
"neither copy implements the requirement": the only thing that catches it is
reading AUD-04's five nouns against the rule table. They are listed in the
header of `readback.go` for that reason.

The one durable improvement is the cross-check above. It is not wired into CI
(there is no JavaScript test harness in this repo, and adding one for a single
function is not the trade to make today) — so it is written down here, with its
method, as the thing to re-run when either copy changes.

## Related

- `docs/bugfix/2026-09-03-a-room-read-measurements-back-raw.md` — the sibling:
  the same requirement, unmet on the server surface because the rules existed
  in only one runtime. That one was drift; this one was absence.
- PRD **AUD-04**, **RSN-06**, **WRK-05**
