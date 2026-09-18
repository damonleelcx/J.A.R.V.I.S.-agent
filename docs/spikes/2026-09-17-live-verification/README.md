# Live verification runs, 2026-09-17

**Hard cap: 300,000 tokens across every run below. Spent: 223,490 (74.5%), over 48 answered model calls. Nothing
overspent.** Approved by damon on 2026-09-17. Branch `live/verification-2026-09-17`, from `main` at f9a503b.

**Endpoint.** `token-plan.cn-beijing.maas.aliyuncs.com` (production token plan). Converse `qwen3.7-plus`, vision
`qwen3.8-max`. Kernel: build123d 0.11.1 on Windows 11 (`jarvis-k2b/.cadvenv`). Local Postgres `forge-pg` (:55840,
schema `forge_liveverify`), local MinIO `forge-minio` (:55841). No AWS, no cluster, nothing deployed. The key was
loaded only into the shell that ran each harness, from `jarvis-a4/.env`. Every file here was scanned for the key's
value and for `sk-` before it was committed: 0 hits.

The laptop was shared with other agents' builds throughout, so the times below are on a loaded machine.

## Budget split (written before any run) and what each run spent

| run | what | cap | spent | calls | time |
|---|---|---:|---:|---:|---:|
| 1 | car with patterns, `TestLiveCarCeiling` (`make measure-car`'s test) | 165,000 | **145,259** | 23 (5 refused) | 7 m 28 s |
| 2 | respec of run 1's car, over HTTP, and of run 3's wheel | 0 | **0** | 0 | 0.04 s |
| 3 | build goal over HTTP, local forged + forge-worker, real model | 90,000 (goal ceiling 70,000) | **65,294** | 16 | 2 m 34 s |
| 4 | V4 vision per sub-assembly on two kept cars | 25,000 | **12,937** | 9 | 41 s |
| | slack | 20,000 | | | |
| | **total** | **300,000** | **223,490** | **48 answered** | |

How each cap was enforced:

- **Runs 1 and 4:** the harness meter, `FORGE_MEASURE_TOKEN_BUDGET`. **This PR changes it** (see Defects, 1). It now places a
  call only if the spend so far, plus the largest call seen with a quarter on top for each call in flight, still fits.
  #118's run spent 301,142 of a 300,000 cap. Run 1 stopped at 145,259 of 165,000: the next call might have cost
  20,045.
- **Run 3:** the goal's own ceiling, `"max_tokens": 70000` on `POST /v1/goals`. The engine refuses a call once the
  ceiling is reached, so one call can go past it. The 20,000 above the ceiling was room for that one call. The driver
  also had a watchdog that would kill forge-worker at 85,000. Neither was reached: the goal finished at 65,294.
- Before each run the ledger was checked: 0 → 145,259 → 210,553 → 223,490. No cap had to be lowered.

## Run 1: the car with patterns

`TestLiveCarCeiling` is `make measure-car`'s test. It was run directly with the same environment, because the
Makefile's default `FORGE_CAD_PYTHON` is a `bin/python` path that does not exist on Windows. The prompt was the same as
in the four earlier runs: *"a sports car, in as much mechanical detail as you can manage"*. Data:
[`data/run1/`](data/run1): `run.log`, `car.json`, `steps.json`, and `calls.jsonl` with every call's prompt and reply
(system prompts left out).

```
CAR-SPEND tokens=145259 of 165000 calls=23 refused=5 seconds=448
CAR-TREE definitions=11 assemblies=4 placed=45 occurrences=45 standard_parts=0 could_be_patterns=1
CAR-KERNEL builds=yes volume=294974318 skipped=0 feature_failures=0 parts_not_in_file=0
CAR-INTERFERENCE pairs=15 buried=4 truncated=false of parts=45
CAR-COVERAGE checked=33 of pairs=33 skipped_parts=0 truncated=false
CAR-ATTACH children=35 attached_at_an_interface=6 at_coordinates=29 interfaces_declared=20 bound_placements=13
CAR-UNPLACED assemblies=0
CAR-PASSES with_a_problem=4 of 8
```

| step | tokens (calls) | what happened |
|---|---|---|
| plan | 742 (1) | 8 steps |
| 1 Chassis Frame | 20,019 (3) | kept. The expressions it wrote were read, and child positions were kept as `position_from`. The look saw floating braces and could not correct them. |
| 2 Front Suspension Uprights | 23,321 (4) | kept. Placed where declared (`suspension-left` at `chassis/front-suspension-left`, the right one mirrored in z). |
| 3 Rear Suspension Assembly | 34,919 (6) | kept. The look's repair was kept. 6 ball joints are 63% inside their arms; the interference repair was tried and not kept ("no fewer"). |
| 4 Powertrain Block | 41,286 (7) | kept, placed at `chassis/engine-mount-front` |
| 5 Drivetrain Connections | 24,972 (2) | **refused, `faults-added`:** attached at `powertrain-unit/transmission-output`, which the assembly does not declare. This is the same mistake as #118's step 4, and the refusal names the interfaces that do exist. |
| 6 Braking, 7 Wheels and Tires, 8 Body Panels | 0 | **refused by the budget:** "145259 of 165000 tokens used and a call may cost 20045" |

- **Did a polar pattern reach the car (lug nuts)? No.** The car never got to its wheels: step 7 was refused by the
  budget. No child in the car has a `pattern`. The repetition warning fired again (`4 × engine-mount-bracket in
  powertrain`). Run 3 answers the lug-nut question.
- **Cylinder axis:** nothing in this car needs one checked. Run 3 checks it.
- **Buried parts (kernel):** 4 of 15 pairs. `mount-fl` and `mount-fr` are 100% inside the engine block; the other
  pairs are the rear ball joints at 50% inside their arms. There are no lug nuts to bury.
- **The literal-position note (#142) did not fire (0 of 5 steps).** The steps bound their positions instead: 13
  `position_from` placements (children and interfaces). The flat `literal_positions=45` counts expanded parts, which
  carry no bindings.
- **Failed steps and why:** step 5 used an interface that does not exist (the gate was named, with the remedy).
  Steps 6–8 were refused by the budget.
- **Cost per step is the problem.** Each step prompt is 12–15k tokens before any repair, and steps 3–4 cost 35–41k
  with the look and repairs. The car-tree run of 2026-09-15 spent 4,840 per call. This run spent 6,315 per call.
- **Found (fixed here, defect 2):** every step printed `parts=0`. `BuildStep.Parts` was `len(doc.Parts)`, and a tree
  has no top-level parts.

## Run 2: respec, no model

**Run 1's car over HTTP.** FORGE has no endpoint that stores a client's geometry, so the car was stored with
`geometry.Service.Save`, using the `2026-09-17-unverified-paths` principals harness. Then `POST /v1/geometry/{id}/respec {"parameters": {"wheelbase":
2800, "front_track": 1650}}` returned **201 in 0.04 s**, 0 caveats. It made **v2 on the same artifact**. The two
versions were compared by [`harness/moved/main.go`](harness/moved/main.go) and both were built in the kernel
([`data/respec/car-moved.log`](data/respec/car-moved.log)):

```
PARAMETER front_track 1550 -> 1650
PARAMETER wheelbase 2600 -> 2800
RESPEC placed_before=45 placed_after=45 moved=38 resized=5 unchanged=6
KERNEL before builds=yes volume=294974318 interference_pairs=15 buried=4
KERNEL after  builds=yes volume=295703168 interference_pairs=15 buried=4
```

- **What moved:** 38 of 45 placed parts.
  - Both front suspension corners moved by (−100, 0, ±50): half the wheelbase change, and half the track change on
    the z interface.
  - Both rear corners moved by (+100, 0, 0). Rear track was unchanged.
  - The cross members and diagonal braces moved by ±100 in x.
  - The rails moved ±30 in z, through the model's own derived `half_frame_width`.
- **What did not move, correctly:** the 6 powertrain parts. They hang from `engine-mount-front`, whose position is a
  literal `[-300, 50, 0]` bound to nothing.
- 5 parts were resized (the cross members follow the frame width). No fault was added, and the interference did not
  change.

**Run 3's wheel, in Go, through `Document.WithParameters`** (the function the endpoint calls). The change was
`bolt_circle_diameter` 100 → 120 and `hub_height` 40 → 50
([`data/respec/wheel-moved.log`](data/respec/wheel-moved.log)). **All 5 patterned lug nuts moved** (for example
`nut-1` [100, 20, 20] → [120, 25, 25]), and so did the rim and tyre (y 20 → 25, through the bound `wheel-face`). The
hub body resized. 7 moved and 1 resized, of 8. So a bound position on a **patterned** child follows a respec. That had
not been shown live before.

## Run 3: a build goal over HTTP against the real model

Setup: `forged` and `forge-worker` built from this branch's base (f9a503b) and run natively. `FORGE_CAD_POOL=1`,
scripts off, one worker, polling every 2 s. The driver is [`harness/run3.py`](harness/run3.py). It polls `GET
/v1/goals/{id}` once a second, as a client would. Data: [`data/run3/`](data/run3).

- `POST /v1/goals {"build": true, "max_tokens": 70000, …}` returned **201 in 3.0 s**: 3 steps, 508 tokens for the plan,
  and `max_tokens` echoed as 70,000. `POST /v1/goals/{id}/start` returned **200**.
- Statement: *"a road-car wheel: a rim, a tyre, a hub, and five lug nuts on a polar pattern around the hub's axis.
  Build it in at most three steps."* The plan made **3 steps, the stated maximum**. #104 found 5 steps against a
  stated 3; that did not repeat here.
- **The goal succeeded in 154 s: 65,294 tokens.** 3 of 3 tasks succeeded, each on its first attempt. The watchdog did
  not fire.
- **Progress:** 45 changes were seen by the polling client. **The longest stretch with nothing changing was 5.15 s,
  and none was over 10 s.** `last_seen_at` moved every ~5 s while a step ran (NFR-02).
- **The spend reconciles exactly with the model's reported usage.** The plan call (forged: 363 + 145 = 508) plus
  forge-worker's 15 `forge.llm.completed` calls (64,786) come to **65,294**, the goal's `tokens_spent`.
  - Step 1: 16,203 over 3 calls.
  - Step 2: 19,441 over 4 calls.
  - Step 3: 29,142 over 8 calls. The step prompt is ~13k each time.
- **Versions: 2, on one artifact** (#124): `art_01M2S46YXRDXMAZ8MWX5184R3R` v1 and v2, one per step that kept a model.
  Step 3 kept nothing, and the timeline shows two `artifact.changed` events.
  - The earlier respec of run 1's car also landed on its own artifact as v2. So the database holds 2 artifacts for
    2 designs.
- Step 3 was refused with `faults-added`. It tried to put a new root `wheel` over the `hub` root. FORGE's fault gave
  the remedy: *"attach it from the root "wheel" instead, as a child there with "at": "hub/wheel-face""*. The goal
  still succeeded with the model step 2 kept. A refused step is a succeeded task whose note says it was left out.
  This is by design, but it means **"succeeded" does not mean every step kept something**.

**The lug nuts, pattern and cylinder axis (the run 1 questions, answered here):**

```
hub:  body   -> hub-body  cylinder r=35 h=40, rotation 0              (axis Y, per the contract)
      nut    -> lug-nut   ISO 4032 M12, position [50,20,0] from {x: bolt_circle_radius, y: hub_height/2},
                          rotation [90,0,0], pattern {"kind":"polar","about":"y","count":5}
      wheel-outer -> rim-tyre at "hub/wheel-face"
```

- **A polar pattern reached the model**, about the right axis: `y`, which is the hub cylinder's own axis. Its first
  copy's position is bound to parameters.
  - The count, 5, is a literal beside the model's own `nut_count = 5`. Pattern counts cannot be bound (the open item
    from #112).
- **The lug nuts are buried, and the kernel says so.** After step 2, 5 of 6 interference pairs are lug nuts 91%
  inside the tyre (1,650 mm³ each). The interference repair was tried and not kept. The look on step 1 saw "lug nuts
  floating in the air around the hub body".
- **Why (found, not fixed — follow-up):** the model wrote the lug nut's position **twice**, once on the *definition*
  (`position [50,20,0]`, `rotation [90,0,0]`, the same `position_from`) and once on the *child*.
  - A definition's own position is an offset inside its frame, so the two compose. The first copy lands at child +
    Rx(90)·definition = [100, 20, 20]: a ring of radius **102 mm round a 35 mm hub**, straight through the tyre, not
    the 50 mm bolt circle.
  - The rotations compose too (90° + 90° = 180° about x), which turns each nut's axis away from the hub axis.
  - FORGE's expansion is doing what the document says. Nothing tells the model it placed a thing twice, and the look
    and the interference repair each saw only a symptom ("floating", "91% inside").
  - Suggested follow-up: a note when a definition carries a non-zero position or rotation equal to the position of a
    child that places it. It can never be right to write the same offset at both levels.

## Run 4: V4 vision per sub-assembly

[`look_kept_car_live_test.go`](../../../internal/agent/look_kept_car_live_test.go) runs the build's own `look` (the
whole model, then each sub-assembly the root places, drawn alone, at most 4) on kept documents. The render is the
kernel's (`from_kernel=true`). Data: [`data/run4/run.log`](data/run4/run.log).

| document | occurrences | looked closely | calls | tokens | problems | naming where (`in <path>:`) |
|---|---:|---:|---:|---:|---:|---:|
| run 1's car | 45 | **3 of 3** | 4 | 5,447 | 6 | 6 of 6 |
| #118's car + run 1's three sub-assemblies placed beside it | 75 | **4 of 7** | 5 | 7,490 | 12 | 9 of 12 |

- **"looked closely at X of Y": yes, 4 of 7.**
  - It can only appear when the root places more than four multi-part sub-assemblies. **No live car has placed that
    many**: #118's car places 4 and run 1's places 3.
  - So the second document is #118's kept car with run 1's front suspension, rear suspension and powertrain placed
    beside it ([`data/run4/car-merged.json`](data/run4/car-merged.json)). It is assembled from two live cars and was
    not built by the model.
  - The four chosen were `suspension-linkage`, `brakes`, `r1-rear-suspension-beside` and `powertrain`, clashing ones
    first, as designed.
  - The coverage values are what `repairIfItLooksWrong` turns into the note *"FORGE looked closely at 4 of 7
    sub-assemblies; the rest were seen only in the picture of the whole model"*. That sentence itself is fenced
    offline (V4); the harness prints what it would read.
- **Does a problem name where it is? Yes.** Every problem found in a sub-assembly picture begins `in <path>:`, for
  example *"in powertrain-unit: powertrain-unit / mount-fl / Engine Mount Bracket: The mount bracket is floating clear
  of the engine block"*. The 3 without it came from the whole-model picture, which has no one place.
- **The findings are mostly right.**
  - The engine mounts are placed at the engine's corners, *inside* it: the kernel says 100%, and the vision model says
    "floating". It saw the symptom and named the wrong one.
  - The rear hub carrier is a cylinder turned `[0, 0, 90]`, so its axis runs along X. This car's length runs along X
    (the wheelbase is in x and the track in z), so the hub's axis points along the car when it should point across
    it, along Z. That is a real cylinder-axis mistake. Vision flagged the axis as wrong but called it "vertical".
    The contract's own example ("an axle that runs across the model, along X, is turned [0, 0, 90]") assumes a car
    laid out across X. The model copied that turn into a car laid out along X.
- Cosmetic: the path appears twice (`in X: X / part / Name: …`), because the vision model already echoes the part's
  label and FORGE prefixes the path.
- Mean 3.5 s a vision call, max 5.8 s. The per-step looks in run 1 add 10 more calls: 4 whole-model and 6
  sub-assembly, 13,936 tokens, 27 findings.

## Defects found

1. **Fixed: the live harness's budget let the last call land past the ceiling.** #118's run spent 301,142 of 300,000.
   The meter now reserves the largest call seen (+25%) for each call in flight (`car_ceiling_live_test.go`).
   - Fences: `TestCarMeter_NeverSpendsPastItsBudget` and `TestCarMeter_CallsInFlightReserveTheirShare`.
   - Drills: "the car meter places a call whenever anything is left" and "calls in flight reserve nothing", both
     seen red.
   - Run 1 is the live proof: it stopped at 145,259 of 165,000.
2. **Fixed: a build step reported `len(doc.Parts)`, which is 0 for every tree.**
   - Every step of run 1 printed `parts=0`.
   - Every task of run 3's goal said *"Step 1 of 3 (Hub Assembly): 0 part(s), kept as version …"* in its stored
     result and outcome.
   - It is the same mistake the workbench rail fixed today (`TestWorkbenchRailCountsWhatADesignPlaces`), in
     `assemble.go` (`BuildStep.Parts`) and in `buildgoal.go` (the step result and its summary).
   - Both now use `partsPlaced` = `Document.Occurrences()`, which walks definitions rather than placements.
   - Fences: `TestAssemble_AStepOfATreeReportsThePartsItPlaces` and `TestBuildGoal_AStepOfATreeSaysThePartsItPlaces`
     (DB).
   - Drills: "a build step counts a tree's top-level parts" and "a build goal's step counts a tree's top-level
     parts", both seen red.
3. **Not fixed, follow-up: a definition and its child can carry the same offset.** A doubled placement is silent, and
   run 3's lug nuts went through the tyre because of it (above).
4. **Not fixed, follow-up: the engine's goal ceiling has the same overshoot the harness had.**
   - `agent/spend.go` `chargedClient` refuses only once `spent >= max_tokens`, so one call (up to ~16k) can land past
     a goal's ceiling.
   - Run 3 did not hit it (65,294 of 70,000), so it was not observed live today. It is read from the code, and #104's
     harness notes it.
   - The same reserve rule would close it. It is a product behaviour change (a goal would stop earlier), so it is left
     for a decision.

## Not established

- **n = 1 for each run.**
- **A polar pattern on a car.** Run 1 ran out of budget before the wheels (step 7). The pattern was verified in a wheel
  goal (run 3), not on a car.
- **The literal-position note live.** It did not fire in run 1 (the model bound its positions) or in run 3.
- **The HTTP respec of the patterned wheel.** It was done in Go through `WithParameters`, because forged had been
  stopped by then. The car respec was done over HTTP.
- **"looked closely at X of Y" on a car a model built.** No live car has placed more than 4 multi-part
  sub-assemblies.

## Harness

- [`harness/run1.sh`](harness/run1.sh): run 1.
- [`harness/env.sh`](harness/env.sh), [`harness/migrate.sh`](harness/migrate.sh),
  [`harness/principals.sh`](harness/principals.sh), [`harness/run3.sh`](harness/run3.sh) and
  [`harness/run3.py`](harness/run3.py): run 3.
- [`harness/respec.py`](harness/respec.py) and [`harness/moved/main.go`](harness/moved/main.go): run 2.
- [`harness/merge.py`](harness/merge.py) and [`harness/run4.sh`](harness/run4.sh): run 4.

Session tokens and the session secret stayed in the scratchpad.
