# An L20x20x3 angle's toe radius was 2 mm, and EN 10056-1's is 1.75 mm

**Date:** 2026-09-15 · **Status:** fixed (stacked on #86) · **Severity:** low: a drawn corner 0.25 mm too large, no crash

## Summary

Stage A3 (#84) added a catalogue of standard parts whose figures were typed from memory of the nominal tables, and its
PR said they had not been checked against the documents. Checking them found one figure that is wrong. The toe radius
of the equal-leg angle `EN 10056 L20x20x3` was 2 mm. EN 10056-1 does not tabulate a toe radius. Its Table 1 note says
the sectional properties assume a toe radius of half the root radius. The L20x20x3 root radius is 3.5 mm, so its toe
radius is 1.75 mm.

The other three angles already had half their root radius (L30x30x3 2.5, L40x40x4 3, L50x50x5 3.5). Every other figure
in the catalogue matched its source (see the source notes in `internal/domain/geometry/standard.go`).

## Symptom

- `{"shape": "standard", "standard": "EN 10056 L20x20x3", "size": {"length": ...}}` was drawn with both toes rounded
  at r = 2 mm.
- The browser's copy (`EN10056` in `forge3d.js`) had the same row, so the parity fence
  `TestRendererDrawsTheSameStandardPartAsTheExporter` agreed with it, as a copy of a wrong row should.

## Impact

- The section area is 111.91 mm² drawn, against 112.31 mm² with the published radii: 0.36 % less steel, about
  0.003 kg/m in S235. The standard's printed area (1.12 cm²) hides the difference at its three figures.
- The corner is drawn 0.25 mm larger. No clearance or fit in FORGE reads a toe radius, so nothing downstream decided
  anything from it.
- The real harm is the kind of claim. The catalogue's figures are ASSERTED to the model and to the person, not labelled
  as recalled, so a wrong row is a wrong fact FORGE states with authority in every design that uses it.

## Preconditions

- A part naming `EN 10056 L20x20x3`. Only that designation.
- Affected since A3, commit `cdedb6f` (#84), on every branch stacked on it.

## Root cause

**Surface.** The row `{20, 3, 3.5, 2}` in `en10056` (Go) and `[20, 3, 3.5, 2]` (JS).

**Deeper.** The table was typed, not read. EN 10056-1's dimension table has no toe-radius column. The radius is given
as a rule in a note under the table, so recalling "the table" produces a plausible round number for r2. The rule gives
1.75. The three larger angles have root radii that halve to round numbers, which is why only the smallest was wrong.

**Constraint level.** Nothing required a catalogue row to name where it was read, so a figure from memory and a figure
from the standard looked the same in review. A3's PR said so ("A reviewer with the tables should check them").

## Fix

- The row is now `{20, 3, 3.5, 1.75}` in Go and `[20, 3, 3.5, 1.75]` in forge3d.js.
- Every family in `standard.go` now names the table its figures were read from, and marks the one family that could not
  be read from the standard itself (ISO 15 bearings, checked against SKF's designations).

## Fences

- `TestStandard_EveryFamilyCarriesTheFiguresItsSourcePublishes` asserts the published figures per family. For angles
  that means leg, thickness and root radius from EN 10056-1 Table 1, and a toe radius of half the root radius for every
  row.
- `TestKernel_EveryStandardFamilyBuildsTheSolidItsFiguresDescribe` builds an L20x20x3 in the real kernel and compares
  its volume with the analytic section area from the published radii, within 0.01 %. The 0.36 % difference above fails
  it.

## Drills

- "an L20 angle's toe radius is 2 again" (Go), red on the catalogue fence and on the kernel fence.
- "the browser's L20 angle's toe radius is 2 again", red on the parity fence.

## Sources

- EN 10056-1:1998, Table 1 and its Note 1 (the DIN EN 10056-1:1998-10 edition).
- RoyMech's table of equal-leg angles to EN 10056-1 gives r2 = 1.75 for 20x20x3, a second reading of the same rule.
- The 2017 edition was not read. A search summary of BS EN 10056-1:2017 gives the same root radius (3.5 mm) and the
  same toe-radius rule for 20x20x3. That is second hand and is not relied on.
