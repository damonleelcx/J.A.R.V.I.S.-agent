# An overlap repair was judged by a list that could not shrink

**Date:** 2026-09-15 · **Status:** fixed (stacked on #113, #114) · **Severity:** high at scale: no repair of a large
model could ever be kept, and a repair that made it worse was not detected either

## Summary

`repairIfPartsOverlap` keeps a repair only when the kernel finds fewer **buried** clashes after it than before. It
counted them from the kernel's clash **list**. #113 cut that list to the worst `_INTERFERENCE_LIST_LIMIT` (10,000) and
made the reply carry how many were found; the repair's acceptance rule was not moved onto the count. Past 10,000 buried
clashes the list holds 10,000 before a repair and 10,000 after it, whatever the repair did, so the comparison
`len(after) < len(before)` is `10000 < 10000` — false, always.

The fix gives the kernel a **buried count** beside its found count, and judges the repair by that count, falling back to
the list only when a reply does not carry it — which is the whole count only when the list is the whole list.

## Symptom

On the 1,008,160-occurrence airframe barrel (1,760,000 clashes found, 768,000 of them buried, 10,000 listed):

- A repair that moved every buried rivet out of its stringer was **refused**, and the turn said "Parts are inside each
  other and FORGE could not correct it".
- A repair that buried **more** parts was refused with the identical note, so the two were indistinguishable.
- The wasted model call happened every turn, and the document was never improved.

Under 10,000 buried clashes nothing was wrong: the list is the whole list, so its length is the count.

## Impact

- **A large model could not be repaired at all.** The check still reported the clashes, so a reader was told the truth
  about the model; what was lost is the repair.
- **A worsening repair was not detected.** It was refused for the same reason a good one was — the counts were equal —
  rather than because it was worse. Nothing was installed, so no document was damaged.
- Only the repair's acceptance was affected. `repairAsks` (#114), the coverage notes and the kernel's own answer were
  correct throughout.

## Preconditions

- A kernel render (`FromKernel`), so the findings exist.
- More than `_INTERFERENCE_LIST_LIMIT` (10,000) **buried** clashes, i.e. the list is cut *and* every listed clash is
  buried. 10,000 buried clashes need about 10,000 pairs of parts placed inside each other, which is a patterned
  sub-assembly — on the barrel, rivets inside stringers.
- Affected on `scale/repair-bound-and-check-profile` (#114, `ba06f96`) and on #113, which introduced the cut list.
- Not affected before #113: the list was every clash, so its length was the count.

## Root cause

**Surface.** `repairIfPartsOverlap` compared `len(geometry.InterferenceProblems(after.Interferences))` with
`len(problems)`, both lengths of a **bounded** list.

**Deeper: a bound was added to a list that two readers used as a count.** #113 bounded the list and added
`InterferencesFound` for exactly this reason, and moved the readers it knew about — the turn's note (`coverageNote`,
"found N … lists the M") and `list`'s "and N more". The acceptance rule is a third reader of the same list, and it was
left counting.

| When | Commit | What changed |
|---|---|---|
| 2026-09-12 | car ceiling | The check and the repair land. The list is every clash, so `len(list)` is the count and the acceptance rule is right. |
| 2026-09-15 | `f96059f` (#113) | The list is cut to the worst 10,000 and the reply carries `interferences_found`. The note and "and N more" move onto the count; the acceptance rule is not named. |
| 2026-09-15 | `ba06f96` (#114) | The repair **prompt** is bounded, and its comment states "the repair is still judged by every buried clash listed" — the defect, written down as an invariant. #114's spike recorded it as recommendation 1. |

**Why the count alone was not enough.** `InterferencesFound` counts every clash, buried or not. A repair is judged by
the buried ones, and how many of the unlisted clashes are buried was not knowable in Go — so the kernel now counts them
where all the clashes are still held (`sidecar.py`, `_BURIED_FRACTION`, the same 0.5 and the same `>=` as
`geometry.BuriedFraction`).

**Owner.** #113, for bounding a list without moving its third reader onto the count it added.

## Why it did not show up before

- **Every fence was under the bound.** The largest clash count in any agent fence is 10,000 exactly
  (`TestInterference_ARepairOfTenThousandClashesIsAskedWithinItsBudget`), and that one fences the **prompt**, not the
  verdict: it never re-checks a repair. The verdict's fences used 1 to 3 clashes, where the list is the whole list.
- **The measurements never judged a repair.** `TestScaleUp_MeasureTheRepairPrompt` measures what is asked at 90k, 300k
  and 1M and stops there; no measurement re-built a repaired barrel.
- **It cannot be seen offline with a stub model,** which is why #114 read it out of the code rather than hitting it:
  a stub's answer is accepted or refused by the same broken comparison, and the live car buries at most four clashes.

## Fix

**The kernel counts the buried clashes** (`internal/domain/cad/sidecar.py`): `_interferences` returns `buried` beside
`found`, and the reply carries `interferences_buried`. `_BURIED_FRACTION` is `geometry.BuriedFraction`, with a ‼️ note
that the two move together — a count taken at a different line would judge a repair by clashes the turn does not call
buried.

**Go carries it and never over-claims** (`internal/domain/cad/cad.go`): `Build.InterferencesBuried` and
`InterferencesBuriedCounted`. `buildOf` applies #113's rule to the buried count as well — never fewer than the list
holds — and a reply that does not carry the count leaves the list's own buried clashes, which is the whole count only
when the list is not a summary. `internal/agent/render.go` and `internal/httpapi/scriptrunner.go` pass both to the
sheet the turn reads.

**The verdict is the totals** (`internal/agent/interference.go`): `tallyOf` turns a sheet into
`{buried, listed, found, counted}`, and `repairVerdict` keeps a repair when the kernel re-built it, it added no
document faults, and the buried count **fell**. The note states both totals either way, and says "no fewer" or
"more, not fewer" when it refused.

‼️ **The count after a repair must be a total.** The count before it may be a floor, because fewer than a floor is
fewer than what it floors; the other way round, 10,000 listed after a repair is what 768,000 buried and 10,000 buried
both list, and a repair would be kept on nothing. A re-check that did not count is refused, saying so.

**A truncated re-check (Phase 5, V2) is judged as before, by what it checked**, and never reads as clean: when it found
none buried, the note says "none buried in the pairs it checked" instead of "moved them apart", beside the existing
"not known to be clear".

## Verification

**Proved red before the fix.** The four new agent fences were written first and run against the unchanged rule: the
three cut-list cases and the uncounted re-check failed, and so did the truncated re-check's note. The small uncut cases
passed unchanged, which is the point of them.

**Green after:** `internal/agent` and `internal/domain/cad` (except the failures known on Windows: `TestScript_*`, the
scripted-part kernel tests, `TestKernel_ReturnsTheSurfaceOfTheSolidItBuilt` and
`TestKernel_ExportingManyOccurrencesGrowsLinearly`). `go vet ./...` is clean.

**On the real kernel:** 150 blocks 0.02 mm apart find 11,175 clashes, list 10,000 and count **11,175 buried**; 30 blocks
0.7 mm apart find 315, list all of them, and count **182 buried** — exactly the list's own buried clashes, which is what
keeps the sidecar's threshold and `geometry.BuriedFraction` the same line.

## Regression prevention

**Fences:**

| Test | What it holds |
|---|---|
| `TestInterference_ARepairPastTheListBoundIsJudgedByTheKernelsTotal` | 10,000 listed before and after, totals 768,000 → 400,000 (kept), → 900,000 (refused, "more, not fewer"), → 768,000 (refused, "no fewer"), with the totals in the note |
| `TestInterference_ARepairIsNotKeptOnACountTheKernelDidNotTake` | a re-check with a cut list and no count is refused, and says it did not count |
| `TestInterference_ARepairReCheckedOnlyInPartNeverReadsAsClean` | a truncated re-check never says the parts were moved apart, and still says "checked X of Y" |
| `TestInterference_ASmallRepairIsJudgedAsItAlwaysWas` | five before/after pairs under the bound, from a builder that counts and one that does not: the same verdicts as before |
| `TestBuildOf_ABuriedCountIsNeverBelowTheListAndSaysWhetherItIsAll` | six replies: never below the list, counted only when it is all of them |
| `TestKernel_AReplyWithMoreClashesThanItListsCountsThemAll` | extended: 11,175 buried counted behind a list of 10,000 |
| `TestKernel_TheBuriedCountIsTheClashesTheTurnCallsBuried` | the kernel's count is the turn's own definition, on a whole list |
| `TestRender_CarriesHowMuchTheCheckCovered` | extended: the buried count reaches the sheet |

**Drills** (`scripts/drill-fences.sh`, section "An overlap repair judged by the kernel's total, not by the list it
cut"): **13 went red, 0 stayed green, 0 unproven, 0 anchors moved**, and the tree was byte-identical afterwards. They
cover: judging by the listed count again; a cut list read as a whole count; a re-check taken on trust; a repair that
leaves as many buried; a kept repair's note without the totals; a refused repair that does not say why; a truncated
re-check that says the parts were moved apart; the render dropping the count; `buildOf` ignoring it, believing one below
the list, or calling a cut list counted; and the kernel counting only what it listed, or at a different threshold than
the turn.

## Not in this fix

- **Whether a model repairs a barrel well.** Offline only, with a stub model: no model was sent either form of the
  prompt. #114's recommendation 4 (measure one live repair from a summary) stands.
- **A truncated re-check is still judged by what it checked.** Its counts are floors, so "fewer" is not proof when the
  pair budget stopped the check; that it stopped is said, and never read as clean. Refusing a repair on that alone would
  change behaviour no measurement here justifies.
- **`internal/httpapi/scriptrunner.go`'s passthrough has no fence of its own**, the same as #113's `Found`. Dropping the
  buried count there makes a re-check read as uncounted, which refuses repairs rather than keeping bad ones.
- **No limit changed:** the 10,000-clash list bound, the 16 KiB repair budget, the 2,000-boolean budget, the 4,096 build
  and export ceilings and 100,000 occurrences all stay.
