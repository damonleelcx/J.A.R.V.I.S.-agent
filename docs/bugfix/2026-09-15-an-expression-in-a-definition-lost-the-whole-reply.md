# An expression in a definition's size lost the whole reply, and a step's new assembly was left unplaced

**Found:** 2026-09-15, live car-quality run 1 (`docs/spikes/2026-09-15-car-quality`), the first run whose failed
steps named their gate (`docs/bugfix/2026-09-15-a-failed-build-step-said-no-geometry-whatever-refused-it.md`) and
whose replies were kept.
**Severity:** high. The first defect is the measured root cause of a lost first step (the chassis) — the step
everything else mounts to. The second left five of eight subsystems out of the car with no fault and no note.
**Owner:** agent — `internal/agent/dimensionrepair.go` (reading a reply) and `internal/agent/assemble.go` (the build loop,
shared with the build goal).

## Summary

1. `repairDimensions` read expressions out of numeric slots in `prototype.parts` only. A tree's parts are its
   `definitions`, and every build step after the first is a `prototype_edit`, so neither was read; one expression in
   one definition discarded the whole reply.
2. A build step that created the sub-assembly its plan names, and did not place it from the root, was accepted as it
   was. An unplaced assembly is not a fault.

## Symptom

```
step 1/8 Chassis Frame  parts=0  Step 1 (Chassis Frame) came back unreadable: its JSON does not parse:
  "prototype.definitions.size" is a JSON string where a float64 was expected, at character 1733:
  …e": "box", "size": { "width": "beam_width", "height"…
```

And at the end of the same run: `definitions=30 assemblies=7 placed=11`. Tracing the root assembly through every kept
reply: steps 4–7 patched only `front-suspension`, `rear-suspension`, `brakes` and `steering`; step 8 wrote its root
children under a key `root_children` (the name of the field it had been shown, not a field of an edit). The root
never placed any of them, and each step's look said so in its own words — "The requested brake calipers are not
present in the model", "The steering rack and tie rods … are completely missing" — which the repair, sent the whole
document, did not fix.

## Impact

- A tree-shaped first step with one parameter name typed as a size is lost entirely (about 12k tokens), and every
  later step has no chassis to mount to.
- The same slip in any later step's patch loses that step.
- A build can pay for most of its subsystems and deliver a car without them.

## Preconditions

1. (1) A reply whose parts are definitions or an edit's patch, with a string where a dimension number goes.
2. (2) A build on a tree whose plan names a new assembly for a step, and a step reply that builds it without adding a
   child to the root.

## Root cause

1. `repairDimensions` was written (2026-09-06) when a document had only `parts` and a build had not yet moved to
   edits. Trees (D1b) and edit-shaped build steps added two more places a part can arrive, and the reading was not
   extended to them.
2. Placing an assembly from the root is a whole-assembly patch the model has to compose (restating the root's
   children). The prompt asked for it; measured, the model did not do it in five of five steps, and nothing in the
   loop checked that the assembly the plan named was placed.

**Classification:** (1) a coverage gap left behind by a later shape; (2) a missing invariant in the build loop,
exposed by the first live run shown `root_children`.

## Why it did not show up before

The dimension repair's fences use flat prototypes. Before this change a lost step said "produced no geometry" and no
reply was kept, so nobody could see which field refused it. The unplaced assemblies did not appear in the 2026-09-15
car-tree run: its plan did not name an assembly per step, so every step saw the whole model.

## Fix

- `repairDimensions` reads `prototype.parts`, `prototype.definitions`, `prototype_edit.patch.parts` and
  `prototype_edit.patch.definitions`, with the same two readings as before (a quoted number, or an expression moved to
  its `_from` twin), and only after a strict parse has failed.
- `placeStepAssembly` (`internal/agent/stepplace.go`), called by `buildOneStep` after the step is settled: when the
  assembly the plan names for this step exists and nothing places it, FORGE adds it as a child of the root at the
  root's origin (or makes it the root when there is none) and the step's note says so. It never moves a placement the
  step made and never places an assembly the plan did not name.
- The step prompt says `root_children` is not a field of an edit, and that an unplaced assembly will be placed at the
  root's origin.
- The live measurement reports `CAR-UNPLACED`.

## Verification

- `TestParseReply_ReadsAnExpressionInADefinitionsSize`, `TestParseReply_ReadsAnExpressionInAnEditsDefinitionAndPart`.
- `TestAssemble_ANewAssemblyTheStepDidNotPlaceIsPlacedFromTheRoot`, `TestAssemble_AnAssemblyTheStepPlacedItselfIsLeftAlone`.
- `TestCarMeasure_CountsAssembliesNothingPlaces`.
- Drilled in `scripts/drill-fences.sh`, "Live car findings", all red.
- Live: run 2 in the spike README.

## Regression prevention

The reading is driven by one list of the places parts arrive; a fourth place is one line and the fence names the two
new ones. The placement is an invariant of the loop rather than an instruction in a prompt, so it holds whatever the
model does with the prompt.

## Not in this fix

- Strings in a tree child's `position`: a child has no `position_from`, so there is nowhere honest to move the
  expression; that reply is still refused, now by name.
- Assemblies no plan named and nothing places (a helper assembly the model forgot to use): reported by the
  measurement, not placed.
- Whole-document repairs dropping a placement: not observed in run 1 (every repair kept the root's children), so not
  guarded here.
