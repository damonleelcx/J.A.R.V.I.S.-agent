# A found floor was printed as a total

**Date:** 2026-09-15 · **Status:** fixed (stacked on #113, #115, #121) · **Severity:** low — **latent**: no reply the
shipped kernel writes today produces it. Found while implementing "a repair may not add contacts", which is the change
that would have made it matter.

## Summary

`buriedTally.String()` writes a repair's note as "N buried clash(es) among M pair(s) sharing material", where M is the
tally's found total. #115 gave the buried count an explicit `counted` bit, because a count taken from a cut list is a
floor and not a total. It gave the FOUND total no such bit — and found can be a floor too. When it is, the sentence
states it as a total, and the two numbers in one sentence contradict each other:

> 768000 buried clash(es) among 10000 pair(s) sharing material

A check cannot find fewer pairs sharing material than it says are buried. The sentence is arithmetic nonsense on its
face, and it is a claim about the model that the kernel never made.

## Symptom

A reply that carries `interferences_buried` and not `interferences_found`. `buildOf` leaves the found total at the
list's length (#113's rule: never fewer found than listed) and raises the buried count to the kernel's own, so a 1M
barrel arrives as 768,000 buried, 10,000 found, 10,000 listed. Every note `repairVerdict` writes about that check —
kept, refused, or "fewer could not be shown" — carries the sentence above.

## Impact

- **Notes only, before this change.** #115 compared buried totals and never read the found total, so no repair was
  accepted or refused because of it. What a reader was told was wrong; what FORGE did was not.
- **It would have decided repairs, after this change.** Judging the found total against a floor of 10,000 refuses every
  repair of a large model for contacts it never added — the shape of the defect #115 itself fixed, one field over.

## Preconditions

- A kernel render (`FromKernel`), so the tally exists at all.
- A reply carrying the buried count and not the found count. The shipped `sidecar.py` sends both from `_interferences`
  (#115), so this is reachable through `buildOf`'s documented fallback and through `Built`'s ("Zero from a builder that
  does not count reads as the length of the list"), not through the current kernel: a replaced or older check, or a
  summarized reply whose found count went missing.
- Needs the buried count to exceed the list, i.e. more than `_INTERFERENCE_LIST_LIMIT` (10,000) buried clashes.

## Root cause

**Surface.** `String()` branched on `t.counted` alone, and `t.counted` is about the BURIED number. The found number was
printed unconditionally in the branch that number's flag was true in.

**Deeper: a floor was flagged in one field and not in the field beside it.** #115 reasoned carefully that a count from a
cut list is a floor, and wired `InterferencesBuriedCounted` end to end for it. The found total has the same property for
the same reason and got no flag, because at the time nothing compared it — so the asymmetry cost nothing and was
invisible. The moment a second reader arrived (the found test), it was a rule about to compare a floor.

## The fix

`buriedTally` grows `foundCounted`, set where the floor is provable: `found >= buried`. `String()` gets a branch for it
that says what the check actually said — the buried count, the listed count, and that the check did not say how many
pairs share material in all. `found` itself is NOT raised to the buried count: a floor is reported as the floor it is
and flagged, because inventing "at least this many pairs share material" would hand the rule and the reader a number
the kernel never took.

`repairVerdict` then declines to compare an uncomparable found total, accepts on the buried total alone, and says so.

## Fences

- `TestInterference_AFoundTotalTheKernelDidNotTakeIsNeitherComparedNorCalledUnchanged` — the floor before the repair and
  the floor after it, both kept on the buried total alone, both saying the found totals were not comparable, and neither
  note containing "among 10000 pair(s)".

## Drills

Three of the five in the `a repair may not add contacts` section of `scripts/drill-fences.sh` hold this: forcing
`foundCounted` true, comparing when only one side is a total, and removing `String()`'s branch for a floor. Each went
red.
