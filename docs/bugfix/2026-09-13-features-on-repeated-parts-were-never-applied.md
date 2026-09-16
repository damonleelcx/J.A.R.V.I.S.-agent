# Features on repeated parts were never applied by the kernel

**Found:** 2026-09-13, while mapping every reader of `geometry.Document` for the
millions-of-parts plan ([`plan-2026-09-13-millions-of-parts.md`](../plan-2026-09-13-millions-of-parts.md)).
Suspected from reading, then **confirmed against the real kernel** before any change.
**Severity:** high, silent to the model. Every export and every kernel-built view of a model
that fused, cut, filleted or lofted a repeated part was missing that operation. The export
label did list it under failed features — the turn's own checks did not.
**Owner:** CAD kernel adapter (`cad.BuildDocument`) — it assembled the kernel request from two
different readings of one document.

## Symptom

A hub with six repeated spokes and one `fuse` naming the pattern:

| | before | after |
|---|---|---|
| feature failures | `weld: spoke could not be built, so this was not applied` | none |
| solids in the file | 7 (hub + 6 loose spokes) | 1 |
| interference findings | 18 (the loose spokes crossing at the hub) | 0 |
| `Document.Faults()` | 0 | 0 |

The last row is why nothing noticed: the document-level check was right, and the kernel path
disagreed with it.

A repeated **scripted** part failed the same way from the other direction: every copy was left out
as "a scripted part with no script".

## Root cause

`cad.BuildDocument` built the request in two halves:

- **solids** from `geometry.Solids(doc)`, which expands patterns — `spoke` becomes `spoke-1` … `spoke-6`;
- **operations** from `doc.Operations()` on the **authored** document, where the fuse still names `spoke`;
- **scripts** from `scriptFor(doc, id)`, a lookup of the copy's id (`block-2`) in the authored
  document, which only knows `block`.

The sidecar keys its shapes by the solids it is sent, so the operation named a shape it never had.

`expandRepeats` already retargets features to every copy (`retargetFeatures`). `Solids` computed
that and threw it away; `Faults` used it, which is why the document looked sound.

**Classification:** implementation defect, present since `repeat` shipped (2026-09-09, Stage 3).
The design claim — "a feature naming the part acts on every copy so one `fuse` welds all sixty
spokes" — was true of every reader except the one that produces files.

### Why it was not caught before

- The repeat fences (`repeat_test.go`) stop at `Faults` and `Tessellate`, both of which expand
  before reading features. **No kernel test ever built a document with a `repeat` in it.**
- Kernel tests never run in CI (no build123d there), so even a kernel fence would only ever have
  run on a developer machine.
- The interference check added on 2026-09-12 made the defect visible for the first time, as
  "spokes overlapping each other" in a model where they should be welded.

## Fix

- `geometry.SolidsAndOperations(d, unit)` returns the solids **and** the operations from **one**
  expanded value. `Solids` is now a wrapper over it, so every other caller and every existing
  drill anchor is unchanged.
- `Solid.Script` (`json:"-"`) carries a scripted part's source on the solid itself; `scriptFor` is
  deleted. There is no second document left to look anything up in.
- `cad.BuildDocument` uses both. Comments at each change link here.
- The 2026-09-09 plan's Stage 3 claim carries a correction pointing here.

## Regression fences

| fence | runs where | holds |
|---|---|---|
| `TestKernel_AFeatureNamingARepeatedPartIsApplied` | real kernel | fused wheel = 1 solid, 0 feature failures, 0 interferences, spokes in the volume |
| `TestKernel_ARepeatedScriptedPartIsBuiltEveryTime` | real kernel | 3 copies built, volume 3 × 64 mm³ |
| `TestRepeat_TheKernelIsSentFeaturesNamingTheCopiesItIsSent` | CI (no kernel) | every id an operation names is a solid the kernel is sent |
| `TestRepeat_EveryCopyOfAScriptedPartCarriesItsScript` | CI (no kernel) | every scripted copy carries its source |

Drills under "Features on repeated parts" in `scripts/drill-fences.sh`:

- "the kernel reads the operations from the authored document again"
- "the operations are read from the document before it is expanded"
- "a copy of a scripted part reaches the kernel without its script"

Proven red, 2026-09-13 (each mutation applied, the named fence run with `-count=1`, the file restored):

| drill | fence output |
|---|---|
| operations from the authored document again | `the fuse naming the repeated spokes was not applied: [weld: spoke could not be built, so this was not applied]` |
| operations read before expansion | `the weld names 1 tools, want the 8 copies: [spoke]` |
| a copy without its script | `block-1: shape "step", script "" — a copy that reaches the kernel without its script is left out of the file` |

A dry run of the whole drill suite found **0** anchors moved, including the five older drills anchored in
`solid.go` — `Solids` became a wrapper precisely so they would not.

## Not fixed here

- Readers that still use unexpanded ids (`Measure`, `Compare`, `ValidateStates`, `partColour`,
  labels in `turned.go`) and the browser, which ignores `repeat` entirely. These are Phase 1 stage
  D1a of the millions-of-parts plan.
- Fillet and chamfer radii are sent in the document's units, not millimetres. Confirmed separately
  and fixed in its own bugfix.
