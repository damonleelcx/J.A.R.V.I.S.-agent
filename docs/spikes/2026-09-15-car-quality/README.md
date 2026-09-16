# Live car findings, fixed and measured again

**Runs 2026-09-15, two, each under a 300k ceiling.** Same prompt as the earlier car runs: "a sports car, in as much
mechanical detail as you can manage". In-request build loop (`AssembleForTest`), converse `qwen3.7-plus`, vision
`qwen3.8-max` on `token-plan.cn-beijing.maas.aliyuncs.com`, kernel build123d 0.11.1 on Windows.
Data: [`data/run1`](data/run1) and [`data/run2`](data/run2) — each holds the log, the car, every step's note
(`steps.json`) and every model call's prompt and reply (`calls.jsonl`; system prompts omitted, they are the source's).
Harness: `internal/agent/car_ceiling_live_test.go`.

**Why.** Two live builds on 2026-09-15 — the in-request run ([`../2026-09-15-car-tree`](../2026-09-15-car-tree)) and the
build-goal run (`docs/spikes/2026-09-15-build-goal-live` on `agent/build-goal-entry`) — lost their first steps with
nothing to say why, refused two steps without naming a fault, buried every lug nut, laid wheels flat and placed every
part by literal arithmetic.

**Order of work.** First set of fixes → run 1 → fixes for what run 1 diagnosed → run 2 → fixes for what run 2
diagnosed. The last set is fenced and drilled but **not measured live**: two runs were the allowance.

## What changed, per finding

| finding | diagnosis | fix | fences |
|---|---|---|---|
| a step "produced no geometry" | offline, **seven** causes gave that one sentence (unreadable JSON kept as words by `parseReply`, an edit on the empty model, a rootless tree, `build_in_passes` only…) | each refusal names its gate and why, bounded; `StepGateOf` reads it back; the harness keeps every reply | `TestAssemble_AFailedStepSaysWhichGateRefusedIt`, `…NoteIsBounded`, `TestCarMeasure_ARefusedStepIsNamedByItsGate` |
| the empty first step, run 1 | gate `unreadable`: `"size": {"width": "beam_width"}` on a **definition**; the dimension repair read only top-level parts | the repair reads definitions and an edit's patch | `TestParseReply_ReadsAnExpressionInADefinitionsSize`, `…InAnEditsDefinitionAndPart` |
| the empty first step, run 2 | gate `unreadable` again; after the repair, `"position": ["-half_wheelbase + 200", 0, 0]` on a **child**, which has no `position_from` | a placement's expression is evaluated by FORGE's binder over the reply's parameters (never guessed; noted as unbound) | `TestParseReply_ReadsAnExpressionInAChildsPositionAtItsValue`, `…CannotEvaluateIsNotGuessed` |
| subsystems missing, run 1 | 5 of 8 steps built their sub-assembly and **never placed it**; one wrote `root_children` into its patch; not a fault, no note | the loop places the plan's assembly from the root when nothing does, and says so | `TestAssemble_ANewAssemblyTheStepDidNotPlaceIsPlacedFromTheRoot`, `…PlacedItselfIsLeftAlone` |
| the car wiped, run 2 | steps 3 and 8 patched `"root"` to their new assembly — taught by this branch's own contract change — unplacing everything before | a build step cannot replace an existing root; what it named is placed from it; contract: patch `root` only when there is none | `TestAssemble_AStepsEditDoesNotReplaceTheModelsRoot` |
| a new assembly could drop the rest (offline) | the step was shown a count of the root's children, a root patch replaces it whole, and `vanishedParts` compared a tree's empty top-level parts | the view shows `root_children`; a tree's lost placements are reported (this note is how run 2's wipe was seen) | `TestAssemble_AStepThatDropsAPlacementSaysSo`, `TestSubtreeModel_ANewAssemblyIsShownWhatTheRootAlreadyPlaces` |
| flat car, no tree (goal run) | the first-step prompt showed only `{"parts": [...]}` beside a contract that tells a car to set `build_in_passes` | first step asked for a rooted tree with interfaces and told this pass is the build; every step told its assembly id, to attach `at`, strict JSON | `TestAssemble_TheFirstStepIsAskedForATreeWithInterfaces`, `…IsToldTheAssemblyItBuilds` |
| wheels on their side (goal run) | the contract never said a cylinder's axis; tyres were turned `[90,0,0]` | the contract says a cylinder stands on its own Y and an axle across the car is `[0,0,90]`, checked against the drawing | `TestTheContractSaysACylinderStandsOnItsOwnY` |
| buried lug nuts (car-tree run) | the one polar example was a flat flange; the contract never said the axis passes through the frame's origin, that the child must sit off it, or that `about` is the wheel's own axis. The expansion is as documented (`pattern.go`) and the browser mirrors it; unchanged. That car was not kept, so contract-vs-placement is **inferred** | the contract says all three with a wheel example; a buried tree occurrence tells the repair which child and which pattern placed it | `TestTheContractSaysWhereAPolarPatternTurns`, `TestInterference_ARepairIsToldWhichChildPlacesABuriedCopy`, `TestPlacedBy_*` |
| refused steps ("would have broken the model") | named now. Run 1: none. Run 2: brakes (a rotor's hole outside its outline — model geometry) and wheels (`at` path into another assembly, below). Both went to the fault repair first (2 and 4 calls) and it did not fix them | none for the model's geometry slip; the cross-assembly `at` is a design gap, left open | — |
| literal positions | a tree child has no `position_from`; `at` is the tree's binding | prompts say attach `at` and bind definitions; the harness counts `CAR-ATTACH` | — (measured) |
| build steps' contract incomplete | `geometryContract` ends mid-sentence before the finish list and density rule `converseFraming` adds | `buildContract` sends them | `TestAssemble_EveryStepIsTaughtFinishesAndDensity` |
| harness measured a different product | `WithSolids` never wired: steps checked no interference and looked at the described model | renders through the kernel as production; `liveSolids` world meshes with coverage | `TestLook_ASubAssemblyLookOpensTheWayTheMeasurementReadsIt` |

29 drills in `scripts/drill-fences.sh` ("Live car findings", "…run 1", "…run 2"), all red. Bugfix docs:
[`a-failed-build-step-said-no-geometry…`](../../bugfix/2026-09-15-a-failed-build-step-said-no-geometry-whatever-refused-it.md),
[`an-expression-in-a-definition-lost-the-whole-reply`](../../bugfix/2026-09-15-an-expression-in-a-definition-lost-the-whole-reply.md),
[`a-build-steps-edit-replaced-the-models-root`](../../bugfix/2026-09-15-a-build-steps-edit-replaced-the-models-root.md).

## The runs against the earlier ones

| | car-tree (in-request, before) | build goal (before) | **run 1** (first fixes) | **run 2** (+ run-1 fixes) |
|---|---|---|---|---|
| tokens / calls | 164,548 / 34 | 172,348 / 27 | **229,962 / 36** | **244,264 / 40** |
| of which repairs / looks | not split | not split | 14 / 13 | 18 / 13 |
| minutes | 7.6 | 9.2 | **12.9** | **14.5** |
| steps planned / with a problem | 7 / 3 | 8 / 2 | **8 / 1** | **8 / 5** |
| problem steps, by gate | 1 "no geometry", 4–5 "broken" — undiagnosed | 1–2 "no geometry" — undiagnosed | **1 unreadable** (expression in a definition's size) | **1 unreadable** (expression in a child's position); **3, 8** removed earlier subsystems (patch replaced the root); **5** faults-added (rotor hole outside outline); **6** faults-added (`at` into another assembly) |
| FORGE placed a step's assembly | — | — | (not yet) | **2** (powertrain, bodywork) |
| definitions / assemblies / placed / occurrences | 9 / 6 / 36 / 39 | flat, 36 parts | **30 / 7 / 11 / 11** | **23 / 5 / 6 / 6** |
| assemblies nothing placed | not measured | — | **5** (traced from replies) | **2** (orphaned by step 8's root) |
| standard parts | 10 | 0 | **0** | **0** (the refused wheel step had ISO 4032 M12 nuts) |
| mirror pairs | 14 | 0 | **3** | **1** |
| positions literal / bound | 36 / 0 | not measured | **11 / 0** | **6 / 0** |
| children attached `at` / at coordinates / interfaces | not measured | — | **18 / 38 / 20** | **10 / 23 / 16** |
| document faults / builds in OCCT | 0 / yes | — / — | **0 / yes** | **0 / yes** |
| interference pairs / buried (final car) | 77 / 53 (lug nuts in hub, rim, tyre) | rims in tyres, calipers in rotors (notes) | **8 / 1** (driveshaft 75% in transmission) | **8 / 3** (cockpit: wiring 62% in a seat, steering column and dash 50% in both seats) |
| wheels | lug nuts buried | 2 wheels, lying flat | no wheel step planned | **built as the contract teaches, then refused** (below) |

Neither run's finished car is better than the car-tree run's by parts placed: run 1 lost five subsystems to the
placement defect, run 2 lost everything before its last step to the root defect. Both defects are FORGE's, found only
because failed steps now say why and replies are kept.

Tokens are not comparable with the earlier runs on repairs: the harness now renders through the kernel, so every
step's interference check and whole-document repairs ran (a repair returns the whole document; the largest answered
with 7,469 completion tokens in run 1).

### Run 2's wheel step, against the contract

The step that built wheels (refused for its `at` path) wrote them exactly as the new contract sentences say: rim and
tyre as revolves about their own `y`; lug nuts as one child `{"ref": "lug-nut" (ISO 4032 M12), "position": [60, 0, 0],
"pattern": {"kind": "polar", "count": 5, "about": "y"}}` — off the axis, about the wheel's own axis — and each wheel
placed with `"rotation": [0, 0, 90]`, its axle across the car. It then attached each wheel `at`
`"rear-suspension/left-hub/hub-face"` from inside its own `wheels` assembly, which can name only its own interfaces or a
sibling's; `rear-suspension` is the root, not a sibling, and `left-hub` places a definition, which has no interfaces.
Refused as `faults-added`, repaired four times without success.

## The visual check, sub-assembly by sub-assembly (stage V4, live)

| | run 1 | run 2 |
|---|---|---|
| vision calls | 13 (7 whole model, 6 sub-assembly) | 13 (7 whole model, 6 sub-assembly) |
| sub-assemblies looked at | `drivetrain-assembly` ×6 — the only assembly the root placed | `powertrain` ×4, `brakes` ×1, `bodywork` ×1 |
| findings | 21 | 35 |
| latency per vision call | mean 2.4 s, max 3.3 s | mean 3.2 s, max 4.5 s |
| vision tokens | 15,494 | 16,300 |

Sub-assembly looks see only what the root places, so their coverage followed the placement defects: run 1 looked at one
assembly because only one was placed. Findings that could be checked against the kept car were right — run 1: "The
requested brake calipers are not present", "The steering rack and tie rods … are completely missing" (never placed);
run 2: "The driveshaft is floating clear below the engine" — and the whole-document repairs driven by them did not fix
the placement defects behind them.

## What this establishes

1. **A failed build step is diagnosable from its note and its kept reply.** Every lost or refused step in both runs
   (six) named its gate and cause; diagnosing each took reading one saved reply.
2. **Both measured empty first steps were FORGE failing to read a correct reply**, not a model declining: an expression
   in a definition's size (run 1), then an expression in a child's position (run 2). Both are fixed; the second is
   not live-verified.
3. **The placement invariant works live**: in run 2 FORGE placed two assemblies their steps left unplaced.
4. **The contract's new axis and polar-pattern sentences reached a live model once**: run 2's wheel step wrote upright
   wheels turned `[0,0,90]` and a standard-part ring of nuts about the wheel's own axis, off it. n=1, and that step
   was refused for something else.
5. **V4 runs live at about 3 s and 1.2k tokens per vision call**, six sub-assembly looks per build, and its coverage is
   bounded by what the tree actually places.

## What it does NOT establish

- **n=2**, and the runs had different fixes. Nothing here is a rate, and the plans differed (run 1 had no wheel step).
- **That the last fixes work live**: the root rule, the child-position reading and the build steps' density rule came
  after run 2.
- **That a car now comes out better.** Both finished cars are poorer than the car-tree run's; the defects that made
  them so are fixed offline only.
- **That lug nuts no longer bury.** No wheel reached a finished car in either run.
- **Cross-assembly attachment.** An `at` that reaches into another assembly is not expressible; run 2's wheels hit it.
  Left open as a design question.
- **Bound positions.** Still 0 in both runs; `at` attachments rose from unmeasured to 18 and 10.
- **The build goal.** Neither run used the A1 path; `buildOneStep` is shared, so every fix reaches it, untested live.
- **Scripts.** Enabled in the harness; the Windows script runner is known broken, and no step reached for one.
