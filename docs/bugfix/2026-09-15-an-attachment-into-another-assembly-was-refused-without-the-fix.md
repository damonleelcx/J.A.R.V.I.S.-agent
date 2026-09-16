# An attachment into another assembly was refused without the fix, and could not be written any other way

**Found:** 2026-09-15, live car-quality run 2 (`docs/spikes/2026-09-15-car-quality`, data in `data/run2`, calls 24–30).
**Severity:** high. The one step in either run that built wheels the way the contract teaches (upright rim and tyre,
an ISO 4032 M12 lug-nut ring off the axis, turned `[0, 0, 90]`) was lost whole, after four fault repairs, and no
build step could have written the attachment it meant in a form FORGE accepts.
**Owner:** agent — `internal/domain/geometry` (`interface.go`, `tree.go`: attachment refusals, root interfaces),
`internal/agent` (`assemble.go`, `stepdeclared.go`, `subtree.go`, `literals.go`, `dimensionrepair.go`, `converse.go`).

## Summary

1. A child `at` path written inside an assembly that reached another subsystem was refused as
   `assembly "wheels" has no child "rear-suspension"`: true, and not something a repair can act on.
2. The define-once-correct form — a child of the ROOT attached at a sibling's interface — needed the build step to
   patch the root, which replaces it whole; run 1 measured that no step does. The fallback FORGE added in #97 placed the
   step's assembly "from the root at its origin", attached to nothing.
3. (Gap, same change) a later step placing a child by the name of a parameter only the model has (not its patch) was
   lost as unreadable, so teaching steps to place by parameter would have taught them to lose the step.
4. (Gap) every live run placed every position by literal arithmetic beside parameters holding the same numbers
   (0 bound positions in four runs); nothing showed a step the values or said so.

## Symptom

```
step 6/8 Wheels and Tires  Step 6 (Wheels and Tires) was left out: it would have broken the model: it had 2 fault(s)
  where the model before it had 0, and these are new: wheels/left-wheel-assembly is attached at
  "rear-suspension/left-hub/hub-face", but assembly "wheels" has no child "rear-suspension"; …
```

The kept calls: the step's reply (call 24) attaches `wheel-unit` twice from inside `wheels`; repair 1 (call 25) moves
the paths to `"left-hub/hub-face"` — still inside `wheels` — and is refused as `has no child "left-hub"`; repair 2
moves them back; the look-driven and interference repairs (calls 29, 30) return the same paths.

## Impact

- Any build whose subsystems mount on each other (wheels on hubs, an engine on its mounts, a body on the chassis) could
  only place by coordinates copied between steps, or lose the step.
- The repair loop spent 4 calls (≈15k completion tokens) on a fault its instruction could not fix.

## Preconditions

1. (1, 2) A tree build with a plan that names a subsystem mounting on an earlier one.
2. (3) A step patch with a child or interface position written as an expression over the model's parameters.

## Root cause

1. `attachments.interfaceIn` resolves a path from the assembly it is written in, which is right (D1d: an assembly may
   be placed many times, so it cannot name anything outside itself); its refusal described the failed lookup rather
   than the rule broken, and nothing said where the attachment belongs.
2. A build step had no way to add a child to the root without restating the root's children, and was never shown
   which frames the root could attach at on siblings — only the root's own interfaces, or those its placements
   already used.
3. `repairPlacements` evaluated an expression over the reply's own `parameters` only.
4. The step view carries derived expressions but not their numbers, and no check compared positions with values.

**Classification:** (1) a refusal without a remedy; (2) a design gap in the build protocol; (3), (4) coverage gaps.

## Why it did not show up before

The D1d fences attach from the root or within one assembly, as the stage decided; no fence placed a subsystem written
in one step on a frame built in another. Run 2 was the first build whose wheel step reached the attachment.

## Fix

- **Refused with the fix** (`geometry.leaves`, `outsideProblem`): a path on a child of a non-root assembly whose first
  segment is none of that assembly's children (or a lone interface only the root declares) is refused as
  `is attached at "…", outside its assembly "wheels"; attach it from the root "rear-suspension" instead, as a child there
  with "at": "left-hub/hub-face"` — a leading root id dropped — and, when that path would fail from the root too, why
  (run 2's hubs are parts: "wrap the part in a one-child assembly that declares the frame"). It is a fault's `Detail`,
  so it reaches the step note and the fault repair unchanged. Expansion and the browser are unchanged.
- **Declared placements** (`stepdeclared.go`): a step may send `"placements"` beside `"patch"`: children of the root,
  `ref` defaulting to its assembly, `at` a path from the root. FORGE appends them to the root for an assembly nothing
  places, says where (`"left-wheel" at "suspension-left/hub"`), and reports a declaration it did not use. Without one,
  the origin fallback is as before.
- **Root interfaces in the step view** (`geometry.Document.InterfacesFromRoot`, `subtree.go`): every interface on
  what the root places, by the path a root child writes (three placements deep, 48 at most), with its frame in the
  root, resolved by the expansion's own code so a listed path attaches.
- **Contract and prompts**: an `at` never leaves its assembly, and one subsystem mounts on another from the root, with
  `{"id": "left-wheel", "ref": "wheel", "at": "suspension-left/hub", "rotation": [0, 0, 90]}`; the step prompt teaches
  `placements` and `root_interfaces`; the contract shows a definition bound with `size_from` and a child placed at
  `["half_wheelbase", 0, 0]`; the first step is asked to declare the dimensions later steps place against.
- **Binding**: a patch's placement expression is read over the model's parameters too (`withBaseParameters`,
  `placementsOverModel`, on build steps and conversational edits); a step is shown every parameter and derived value
  with its number; an accepted step that typed three or more positions equal to a length parameter's value is told
  which, by name, in document order, and never refused (`literals.go`).

## Verification

- Geometry: `TestInterface_AnAttachmentThatLeavesItsAssemblyIsRefusedWithTheFix`,
  `TestInterface_OnlyAPathThatLeavesItsAssemblyIsToldToAttachFromTheRoot`,
  `TestInterfacesFromRoot_ListsWhereAChildOfTheRootCanAttach`.
- Browser parity: `TestRendererFlattensATreeLikeTheExporter`, case "a subsystem placed from the root at a mirrored
  sibling's nested interface" (it already passed: the expansion and `forge3d.js` handled nested root paths).
- Agent: `TestAssemble_AStepsDeclaredPlacementAttachesItsAssemblyFromTheRoot`, `…DeclaredPlacementsAreReadWhereverTheStepPutThem`,
  `…ADeclaredPlacementThatCannotBeUsedIsNotUsedAndSaysSo`, `…AStepIsShownTheInterfacesItCanAttachAtFromTheRoot`,
  `…AStepIsTaughtToAttachFromTheRootAndToBindPositions`, `TestTheContractSaysAnAtNeverLeavesItsAssemblyAndItsExampleAttaches`,
  `TestTheContractShowsASizeAndAPositionWrittenWithAParameter`, `TestAssemble_AStepIsShownTheParametersItCanBindTo`,
  `…AStepThatRetypesAParametersValueIsToldWhichParameter`, `…AStepsPlacementExpressionReadsTheModelsParameters`.
- Replays of run 2's saved replies (`internal/agent/testdata/run2-*`): the wheel step is refused with the fix in its
  note and its repair prompt; repair 1's document is told the same; the wheel rewritten as the contract teaches is
  attached from the root on a suspension that declares hub faces, and refused naming the hub with no frame on run 2's own.
- Drilled in `scripts/drill-fences.sh`, "Cross-assembly attach and bound positions", all red.
- **Not live-verified.** A live run waits on damon's token go-ahead.

## Regression prevention

The refusal is produced where the expansion refuses, so every reader (step note, repair, export notes) says the same
thing; the listed root interfaces are resolved by the same resolver a placement uses; the contract's examples are
decoded and expanded by the fences rather than trusted as prose.

## Not in this fix

- FORGE does not move an outside-pointing child to the root itself: which assembly a child belongs in is the design.
- `position_from` on a child or an interface: a placement's expression is still stored as its number and does not
  follow a later parameter change; bound positions in a tree remain definitions' and parts'.
- The literal note compares exact values only (not `2 * half_track`), lengths only, and new or moved positions only.
