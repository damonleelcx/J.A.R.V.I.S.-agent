# A build step's edit replaced the model's root, and a child's position expression lost the reply

**Found:** 2026-09-15, live car-quality run 2 (`docs/spikes/2026-09-15-car-quality`, data in `data/run2`).
**Severity:** high. The first defect destroyed the car twice in one run — the finished model was six cockpit parts
after eight steps — and it was introduced on this branch, by teaching `"root"` in an edit's patch. The second is the
measured cause of the lost first step in run 2, after run 1's cause was fixed.
**Owner:** agent — `internal/agent/assemble.go` (build loop, shared with the build goal), `internal/agent/dimensionrepair.go`
(reading a reply), `internal/agent/converse.go` (contract).

## Summary

1. A build step's `prototype_edit` could set `"root"` to the assembly it had just built. `Edit.Apply` sets the root
   from a patch, so every placement under the old root was left unplaced.
2. A tree child's `"position": ["-half_wheelbase + 200", 0, 0]` discarded the whole reply: a placement has no
   `position_from`, and the dimension repair had nowhere to move the expression.
3. (Gap, same change) build steps were sent `geometryContract` alone, whose material paragraph ends mid-sentence
   before the finish list and the density rule that `converseFraming` splices in.

## Symptom

```
step 1/8 Chassis Frame  came back unreadable: its JSON does not parse: "prototype.definitions.size" is a JSON string …
step 3/8 Rear Suspension  Step 3 (Rear Suspension) removed Lower Wishbone, Shock Absorber, Steering Knuckle, Tie Rod End, Upper Wishbone.
step 8/8 Interior and Controls  Step 8 (Interior and Controls) removed Differential Housing, Rear Brake Disc, … bodywork (4 parts), powertrain (3 parts).
CAR-TREE definitions=23 assemblies=5 placed=6
```

The kept replies: step 3's patch had `"root": "rear-suspension"`, step 8's `"root": "cockpit"`, each with only its own
assembly. Step 1's note quotes the strict parse's first complaint (a definition's size, which the repair now reads);
parsed after the repair, the reply still failed on `prototype.assemblies.children.position`:
`{"id":"front-cross","position":["-half_wheelbase + 200", …`.

## Impact

- Any tree build could lose every subsystem before a step whose patch named a root; the new vanished-placements note
  said so (that note is how it was seen), but the step was accepted.
- A first step that places its chassis members by parameter expressions was lost whole.
- Build steps never read the density rule, so a mass report on a built model was not asked for in the terms the
  validator checks.

## Preconditions

1. (1) A model that already has a root, and a step patch with a different `"root"`.
2. (2) A reply with a string in a child's or an interface's `position`.

## Root cause

1. The contract's patch listing gained `"root"` in this branch's first commit ("when this edit makes the model a tree");
   a live model read it as "the assembly this edit is about". `Edit.Apply` honours a patch root unconditionally, which is
   right for a conversational edit that means to change the design and wrong for a pass that adds a subsystem.
2. `repairDimensions` relocates expressions into `_from` twins; a placement has none, by design of the tree (D1b), so
   the one honest reading — the expression's value over the reply's own parameters — was not made.
3. `buildOneStep` composed its system prompt from `geometryContract`, which is only the first half of what
   `converseFraming` sends.

**Classification:** (1) a regression from a prompt change, caught by the live run it was made for; (2) a coverage gap;
(3) prompt drift between two composers of the same contract.

## Why it did not show up before

(1) did not exist before this branch. (2) was named in "Not in this fix" of the previous bugfix doc as unmeasured; run 2
measured it. (3) The build-step fences checked that the contract is sent, not that it is whole.

## Fix

- `buildOneStep` clears a patch's `"root"` when the model already has a different one, says so, and places the named
  assembly from the existing root (`placeStepAssembly`), as well as the assembly the plan names.
- The contract's patch listing: `"root": "ONLY when the model has no root yet: …"`.
- `repairPlacements` (in `dimensionrepair.go`, run only after a strict parse fails): a quoted number in a child's or an
  interface's position is read as the number; an expression is evaluated by FORGE's own binder (a probe part bound to
  it) over the reply's `parameters` and `derived`; anything that does not evaluate is left to fail by name. The reply's
  note says the placement was read at its value and will not follow the parameter.
- `buildContract = geometryContract + FinishGuide() + densityContract`, sent by every build step.

## Verification

- `TestAssemble_AStepsEditDoesNotReplaceTheModelsRoot` (the sent root is the step's assembly, and another assembly).
- `TestParseReply_ReadsAnExpressionInAChildsPositionAtItsValue`, `TestParseReply_AChildPositionItCannotEvaluateIsNotGuessed`.
- `TestAssemble_EveryStepIsTaughtFinishesAndDensity`.
- Drilled in `scripts/drill-fences.sh`, "Live car findings, run 2", all red.
- **Not live-verified**: the two live runs allowed for this change were run 1 and run 2; these fixes came after run 2.

## Regression prevention

The root rule is an invariant of the build loop, not a sentence the model must obey. The placement reading goes through
`geometry.Document.Bind`, so it cannot disagree with how a bound dimension evaluates.

## Not in this fix

- A patch root on the conversational path: an edit there may mean a different design, and is left as it was.
- `"at"` paths that reach into another assembly (run 2's wheels: `"rear-suspension/left-hub/hub-face"` from inside
  `wheels`). Refused by name, repaired unsuccessfully four times; attaching across assemblies is not expressible
  today and is left for a design decision.
- Expressions in rotations, or positions whose parameters live only in the model on screen (a later step's patch that
  names parameters it did not send): left to fail by name.
