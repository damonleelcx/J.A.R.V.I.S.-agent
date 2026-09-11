# Edited, repaired and built documents were never settled

**Found:** 2026-09-10, while adding the `"gear"` shape: an outline written onto a
part at bind time would have been missing on every repaired turn, because
repairs do not bind.
**Severity:** medium, silent. The model on screen drew the numbers the model
typed rather than the relationships it stated, and the note that would have said
so is written by the very step that was skipped.
**Owner:** agent (conversation) — the reply pipeline had one settling step and
four producers of the document it settles.

## Symptom

A body whose width is `size_from: body_width` with `body_width = 2000`, and the
number typed beside it `1900`:

| how the document reached the reply | width drawn | before the fix |
|---|---|---|
| the model's first whole prototype | 2000 | ✅ |
| a `prototype_edit` revision | 1900 | ❌ |
| any of the four repairs (fault, turned, look, sketch) | 1900 | ❌ |
| a pass of a multi-pass build | 1900 | ❌ |

And with it, every other rule the first document is held to — the unit note, the
tolerance rule on overlays (PRD VIS-03), materials, states, the not-verified
fallback (VIS-06), id/position/rotation defaults, and the parameter, outline and
feature notes — was skipped for the other three.

## Root cause

`Reply.validate()` holds the document rules and runs **once**, early, on both reply
paths. Three things install a document into the reply AFTER it:

- **`resolveEdit`** — both paths call `validate()` before it, and it sets
  `r.Prototype = &applied`. For an edit, `validate()` had seen no prototype at all.
- **`repairGeometry`** — the one producer behind `repairIfFaulty`,
  `repairIfTurned`, `repairIfItLooksWrong` and `repairAgainstSketch`. It sends the
  whole document and parses a whole document back.
- **a build pass** (`assemble.go`) — `parseReply` and `resolveEdit`, never
  `validate()`, and the finished model is installed whole.

Each was added on its own, later than `validate()`, and each reused the parser but
not the rules. "One choke point" had quietly become one of four.

**Classification:** a design gap — the rules lived in a step named for the reply,
not in something every producer of a document passes through.

## Fix

The document rules moved out of `validate()`, unchanged, into
`settleDocument` (`internal/agent/settledoc.go`), and every producer settles what
it produces:

| producer | where |
|---|---|
| the model's whole prototype | `validate()` |
| an edit | `resolveEdit` |
| every repair | `repairGeometry`, before its own no-worse / nothing-removed checks |
| a build pass | `assemble.go`, after the pass's edit is resolved |

Settling at the producer rather than once at the end was a decision: the checks
that run between repairs — the turned-on-its-side measurement, the look render,
the sketch comparison, the script run — then read bound numbers instead of
judging stale ones.

**It is idempotent, and has to be.** A repair is sent the whole document,
`not_verified` included, and sends it back; settling what comes back would repeat
every note. Identical notes are dropped, and the "unitless" note is not added to a
document that already carries one — the second pass would otherwise read the
cleared unit as a new, differently-worded problem.

No prompt, contract or pipeline order changed.

## Regression tests

`internal/agent/repair_binding_test.go` and `internal/agent/settledoc_test.go`:

- `TestEdit_TheEditedDocumentIsBound` — red before the fix (1900).
- `TestRepair_TheRepairedDocumentIsBound` — red before the fix (1900).
- `TestAssemble_APassIsSettled` — a build pass's bound width is drawn.
- `TestSettle_IsIdempotent` — settling twice leaves the document, notes included,
  exactly as settling once.

Drills: section *"Every document a turn installs is settled"* in
`scripts/drill-fences.sh` — one per producer, one that removes the
de-duplication, and one that lets a second settle add the reworded unit note.
`TestSettle_IsIdempotent` was red before that guard existed: the second pass
added *"No unit was stated…"* beneath *"The unit "furlongs" is not one FORGE can
convert…"*, because the first pass had cleared the unit.

## Related

- `docs/bugfix/2026-09-10-scripted-parts-never-exported.md` — found the same day,
  by the same measurement.
- `internal/domain/geometry/gear.go` — why the browser holds its own copy of a
  gear's outline rather than reading one written at bind time.
