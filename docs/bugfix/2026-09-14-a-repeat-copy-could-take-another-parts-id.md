# A repeat copy could take another part's id, and the document was stored anyway

**Date:** 2026-09-14 · **Status:** fixed (stacked on #59) · **Severity:** medium: data is stored ambiguous, no crash

## Summary

A part with `repeat` names its copies `<id>-1 … <id>-n`. When another part already had one of those ids — `rail` ×3
beside a part called `rail-2` — the document placed two parts with the same id, and the storage door stored it. The door
is the one place that refuses an id placed twice (`NewVariant.Validate`, "part id %q appears twice"). It could not see
the collision, because it checked ids **before** repeats were written out.

The fix leaves that one rule and its wording as they were, and changes only what it reads: every part the document
builds (`Document.Expanded()`), repeats included.

## Symptom

- `Document{Parts: [rail (repeat 3), rail-2]}` expands to `rail-1, rail-2, rail-3, rail-2`, i.e. two parts with the id
  `rail-2`.
- `Document.Faults()` reports nothing, and `NewVariant.Validate` returns nil, so the variant is stored.
- A top-level part's repeat colliding with a part the assembly tree places (a child with id `rail-2`) was stored the
  same way.

## Impact

Everything that addresses a part does so by id, so one of the two parts is unreachable, and whichever reader looks first
decides which one:
- **Comparison** matches parts across variants by id, so a side-by-side view silently compares against one of the two.
  That is the reason the duplicate rule exists.
- **Assembly states, features and selection** name a part by id, so they act on one part and cannot reach the other.

It needs a model or a person to write an id of the form `<repeated id>-<n>` next to that repeat. That is unlikely by
accident, but it is exactly the name a model reaches for when it adds "one more rail" by hand.

## Preconditions

- A part with `repeat` (count ≥ 2).
- Another part, anywhere in the document, whose id equals one of the repeat's copy ids.
- Affected on `main` today (`a8993bb`), whose door reads the authored `n.Document.Parts`.
- Affected on the Phase 1 stack (#57–#59), whose door reads `PlacedParts()`.

## Root cause

**Surface.** The duplicate loop in `NewVariant.Validate` iterated over parts whose repeats were not written out.

**Deeper: two sources of part ids, one rule that knew only the first.**

| When | Commit | What changed |
|---|---|---|
| 2026-09-02 | `26a57c9` | The duplicate-id rule is written. Every part id is authored, so reading `n.Document.Parts` is complete. |
| 2026-09-09 | `c92d775` (#43) | `repeat` lands. Its copies' ids exist only after `expandRepeats`, a second source of ids created later in the pipeline. The rule's input is not updated, so from here it is incomplete. |
| 2026-09-14 | `b8a3078` (D1b, #57) | The door is moved to `PlacedParts()`, so that parts placed by an assembly tree are checked. That fixes the tree, not repeats: `expandAssemblies` writes out a *definition's* repeat during the walk, but copies a *top-level* part (and its `repeat`) through unexpanded. |

A collision a pattern makes, including a repeat inside a pattern (`rail-2-1`), was already refused: the tree walk writes
those ids out, and `PlacedParts` reads the walk.

**Constraint level.** This is the same class of defect as
[`2026-09-13-repeat-copies-were-invisible-to-most-readers.md`](2026-09-13-repeat-copies-were-invisible-to-most-readers.md):
a reader works on the authored parts while other readers join against ids that only the expansion produces. That fix
moved the readers to `Expanded()`, but the storage door was not on its list.

**Owner.** The change that added `repeat` (#43). It introduced a second producer of part ids without giving the one rule
about ids that producer's output.

## Why it did not show up before

- No test stored a document mixing a repeat with a part named like one of its copies. The door's duplicate fences only
  used authored or tree-placed ids.
- `Faults()` does not check duplicate ids. The door is the rule's one owner, so nothing earlier in a turn noticed.

## Fix

`internal/domain/geometry/variant.go`: the duplicate loop reads `n.Document.Expanded().Parts` instead of `placed`, with
a Why comment and a link to this document at the loop.

- **Same rule, same message.** No second rule was added.
- **The checks around it keep reading `placed`.** `ValidateStates` must still accept a state that names a repeated part
  by its authored id (`rail`), and the shape and material checks read the placed parts as before.

## Verification

- **Proved red before the fix.** `TestNewVariant_AnIdPlacedTwiceIsRefusedHoweverItWasMade` was written first and run
  against the unchanged door. Three cases stored the duplicate:
  - a flat repeat's copy, then an authored part;
  - the same, in the other order;
  - a top-level repeat's copy against a part the tree places.
  
  The repeat-inside-pattern case was already refused.
- **Green after the fix:** all four cases, plus `TestNewVariant_RepeatCopiesWithTheirOwnIdsAreStored`, the other side
  of the rule. It covers a repeat beside a part it does not name, a repeat inside a pattern, and a repeat of one beside
  a part called like a copy.

## Regression prevention

**Fences:**

| Test | What it holds |
|---|---|
| `TestNewVariant_AnIdPlacedTwiceIsRefusedHoweverItWasMade` | An id placed twice is refused however it was made. |
| `TestNewVariant_RepeatCopiesWithTheirOwnIdsAreStored` | Copies whose ids nothing else holds are still stored. |
| `TestTree_TheStorageDoorChecksThePartsATreePlaces` | The other door checks read the placed parts. This keeps the "reads only top-level parts" drill honest, since the duplicate check alone would now catch a tree clash through `Expanded()`. |

**Drills** (`scripts/drill-fences.sh`, `-count=1`):

| Drill | What it does |
|---|---|
| **the storage door checks ids before repeats are written out** (new) | Puts `placed` back as the loop's input. |
| **the storage door reads only top-level parts** | Its fence regex now includes the test above. |
| **the storage door accepts an id placed twice** | Its fence regex now includes the new duplicate test. |

## Not in this fix

- **`Faults()` still does not report a duplicate id**, so a conversation learns about it at save time, the same as for
  every other door refusal. Adding a second report would be a second owner of the rule. If the repair loop should see it
  earlier, the right change is for `Faults()` to call the door's rule, not to restate it.
- **`main` gets this fix when the Phase 1 stack merges.** A separate `main` fix would have to change `n.Document.Parts`
  to `Expanded()` in the same loop.
