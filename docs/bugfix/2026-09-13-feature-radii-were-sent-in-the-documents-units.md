# Fillet and chamfer radii were sent in the document's units, not millimetres

**Found:** 2026-09-13, reading `Operations()` while fixing features on repeated parts
([`2026-09-13-features-on-repeated-parts-were-never-applied.md`](2026-09-13-features-on-repeated-parts-were-never-applied.md)).
Suspected from reading, **confirmed against the real kernel** before any change.
**Severity:** high, silent. Every STEP export and kernel-built view of a model written in cm, m or
inches built its fillets and chamfers 10×, 1000× or 25.4× too small — in a file that declares
millimetres — and nothing reported it: the fillet applied, just at the wrong size.
**Owner:** geometry → kernel request (`geometry.SolidsAndOperations`). Every length in `Dims`,
outlines and paths was converted; the one length inside a feature was not.

## Symptom

The same physical part — a 100 mm cube with a 10 mm fillet on every edge — written three ways:

| written in | before | after |
|---|---|---|
| mm (`side 100, radius 10`) | 975,587 mm³ | 975,587 mm³ |
| cm (`side 10, radius 1`) | **999,744 mm³** — a 1 mm fillet | 975,587 mm³ |
| m (`side 0.1, radius 0.01`) | a 0.01 mm fillet | 975,587 mm³ |

And when a radius did not fit, the kernel's suggestion quoted its own millimetre numbers with **no
unit** (*"cannot take a radius of 200 here … the largest that DOES build is 49.804"*), so a reader of a
cm model was offered numbers in a unit they never used.

## Root cause

`Document.Operations()` resolves `radius` / `radius_from` in the document's own units — correctly,
because it is also a validation pass over the authored document. `Solids` converts every *shape*
dimension to millimetres (the 2026-09-05 fix for inch models exporting as millimetres), but the
operations were passed to the kernel untouched.

**Classification:** implementation defect, the same class as the 2026-09-05 unit bug: a length that
did not go through the conversion. **Why it was not caught:** every kernel fixture with a fillet or
chamfer was written in millimetres, where the factor is 1 — exactly how the 2026-09-05 bug hid.

## Fix

- `geometry.SolidsAndOperations` multiplies every operation's radius by the unit's millimetre factor,
  in the same function that converts the solids. Only the kernel path: `Tessellate` draws in the
  authored unit and reads no radius, so the viewport is unchanged.
- `cad/sidecar.py` states `mm` in the "largest that fits" message.
- Comments at both changes link here.

## Regression fences

| fence | runs where | holds |
|---|---|---|
| `TestSolids_ConvertsAFeatureRadiusToMillimetres` | CI (no kernel) | mm, cm, in and m radii reach the kernel in mm |
| `TestKernel_AFilletIsTheSameSizeInEveryUnit` | real kernel | the same filleted cube has the same volume in mm, cm and m |
| `TestKernel_ARadiusThatDoesNotFitIsReportedInMillimetres` | real kernel | the failure message carries its unit |

Drills under "Feature radii are lengths" in `scripts/drill-fences.sh`, proven red 2026-09-13
(mutation applied, fence run with `-count=1`, file restored):

- "a feature radius is sent in the document's units again" → red
- "the kernel builds a cm fillet ten times too small again" → red
- "the kernel quotes radii without a unit" → red: *"the failure quotes kernel numbers without their
  unit: … the geometry cannot take a radius of 200 here. The largest that DOES build on these edges
  is 49.804"*

Also verified: full real-kernel suite `ok` (218 s), geometry / agent / httpapi green, whole-suite drill
dry run 0 anchors moved.
