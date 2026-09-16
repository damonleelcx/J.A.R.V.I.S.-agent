# A live car build, verified against four stacked PRs

**Run 2026-09-15. One run, and the last two calls were refused.**
Raw output: [`data/run.log`](data/run.log). The car: [`data/car.json`](data/car.json). Every step's note:
[`data/steps.json`](data/steps.json). Every model call's prompt and reply: [`data/calls.jsonl`](data/calls.jsonl)
(system prompts omitted, they are the source's). Harness: `internal/agent/car_ceiling_live_test.go`.

**Why.** #97, #111, #112 and #92 are stacked, and everything #111 and #112 added was fenced offline only: both PRs
say "Not live-verified" in as many words. This is the one run that asks whether a live model, taught the new
contract, writes `placements`, binds positions to parameters, and whether FORGE's new refusals and placements
behave on a real car. Same prompt as the three earlier runs, so the numbers compare:

> a sports car, in as much mechanical detail as you can manage

**Endpoint.** `token-plan.cn-beijing.maas.aliyuncs.com`, converse `qwen3.7-plus`, vision `qwen3.8-max`, kernel
build123d on Windows 11, `AssembleForTest` (the in-request build loop, not a build goal). The machine was shared
with other agents' work throughout (two other `go` processes, three `python`, Docker and mongod; ~27% CPU at
start), so the latencies below are on a loaded laptop.

## What it cost

**301,142 tokens over 45 calls in 17 minutes 11 seconds**, against a 300,000 ceiling. **The budget ran out**: the
counter refuses a call once the spend is gone, so the last **2 calls were refused** and step 8 never got its look.
Everything below is a floor, not a ceiling.

| | car-tree | quality run 1 | quality run 2 | **this run** |
|---|---|---|---|---|
| tokens / calls | 164,548 / 34 | 229,962 / 36 | 244,264 / 40 | **301,142 / 45 (2 refused)** |
| per call | 4,840 | 6,388 | 6,107 | **6,692** |
| minutes | 7.6 | 12.9 | 14.5 | **17.2** |
| steps planned / run | 7 / 7 | 8 / 8 | 8 / 8 | **8 / 8** |
| steps with a problem | 3 | 1 | 5 | **3** |
| definitions / assemblies / placed / occurrences | 9 / 6 / 36 / 39 | 30 / 7 / 11 / 11 | 23 / 5 / 6 / 6 | **16 / 5 / 54 / 54** |
| assemblies nothing placed | not measured | 5 | 2 | **0** |
| standard parts | 10 | 0 | 0 | **4** (1 design) |
| mirror pairs | 14 | 3 | 1 | **3** |
| positions literal / bound (flat parts) | 36 / 0 | 11 / 0 | 6 / 0 | **52 / 2** |
| children `at` an interface / at coordinates | not measured | 18 / 38 | 10 / 23 | **35 / 23** |
| **bound placements (`position_from`)** | 0 | 0 | 0 | **27** |
| interference pairs / buried | 77 / 53 | 8 / 1 | 8 / 3 | **30 / 5** |
| document faults / builds in OCCT | 0 / yes | 0 / yes | 0 / yes | **0 / yes** |
| mesh triangles | 4,792 | — | 1,068 | **6,632** |

**This is the largest car any live run has finished**: 54 occurrences against 39, 11 and 6. It is also the first to
spend the whole ceiling.

## The steps, and the gate of each one that kept nothing

Eight planned, eight run, **three kept nothing** (`CAR-PASSES with_a_problem=3 of 8`).

| step | gate | what happened |
|---|---|---|
| 1 Chassis Frame | — | accepted. Expressions arrived in the place of numbers and were **read**, and a child's/interface's position expression was **kept as `position_from`**. |
| 2 Suspension Uprights and Hubs | **faults-added** | `suspension-mounts is attached at "chassis/cockpit-floor", but assembly "chassis" has no child "chassis"`. See the defect below. |
| 3 Powertrain Block | — | accepted. `FORGE placed the assembly "powertrain-core" from the root "chassis" where this step declared it: "powertrain" at "engine-mount-front"`. |
| 4 Drivetrain Connections | **faults-added** | declared `at: "powertrain/transmission-output"`; `assembly "powertrain-core" declares no interface "transmission-output" (it declares front-mount, rear-mount)`. |
| 5 Suspension Arms and Dampers | — | accepted, placed where declared (`"suspension-linkage" at "chassis/front-suspension-left"`). One part could not be built; FORGE corrected it and re-checked. |
| 6 Braking System | — | accepted, placed where declared (`"brakes" at "cockpit-floor"`). |
| 7 Wheels and Tires | **faults-added** | `Performance Tire the corner radii at outline points 1 and 2 need 30.36 of the 28.28 between them, so the two arcs would overlap` (4 faults). Model geometry, not FORGE. |
| 8 Body Panels and Aerodynamics | — | accepted, placed where declared (`"bodywork-assembly" at "cockpit-floor"`). **No look**: the budget was spent. |

So the car has a chassis, a powertrain, suspension arms, brakes and bodywork, and **no uprights, no hubs, no
driveshafts and no wheels**.

## The tree

`definitions=16 assemblies=5 placed=54 occurrences=54 standard_parts=4 could_be_patterns=1`, root `chassis`,
units mm, **0 top-level parts**. 18,821 tokens per design, 5,576 per occurrence.
Assemblies: `chassis`, `powertrain-core`, `suspension-linkage`, `brakes`, `bodywork`.
Shapes: `cylinder=32 box=15 standard=4 extrusion=3`; features: `fuse=1`. Nothing hollow: `parts_with_holes=0
cut_features=0 of parts=54`, unchanged from every earlier run.

**Nothing was left unplaced.** `CAR-UNPLACED assemblies=0`, against 5 in run 1 and 2 in run 2.

## Attachment and binding — the point of the run

```
CAR-ATTACH children=58 attached_at_an_interface=35 at_coordinates=23 interfaces_declared=20 bound_placements=27
CAR-PLACEMENT literal_positions=52 bound_positions=2 id_prefixes=19
```

- **`placements` is written and used.** **7 of the 8 steps** (2–8) sent a `placements` block. **4 were used** and
  FORGE named where each went (steps 3, 5, 6, 8, quoted above). 2 were refused (steps 2 and 4), 1 step was refused
  for unrelated geometry. **None was reported unused.** In the three earlier runs FORGE placed an orphan assembly
  at the root's *origin*; here every placed subsystem went to a named frame.
- **Positions are bound to parameters, for the first time in any live run.** **27 bound placements** — 15 children
  and 12 interfaces carrying `position_from` — where car-tree, run 1 and run 2 each measured **0**. They are real
  expressions over the model's own parameters, not copies of numbers:

  ```
  chassis/lower-longitudinal-left   {"y": "ride_height", "z": "-chassis_width / 2"}      -> [0, 120, -250]
  chassis/upper-longitudinal-right  {"y": "ride_height + 400", "z": "chassis_width / 2 - 50"} -> [0, 520, 200]
  chassis/front-cross-lower         {"x": "-half_wheelbase + 100", "y": "ride_height"}   -> [-1225, 120, 0]
  ```

  `half_wheelbase` is a *derived* value (`wheelbase / 2`), so a derived name was used in a placement. The flat
  `CAR-PLACEMENT` count (52 literal / 2 bound) counts flattened parts, which carry none of this; the tree's
  placements are the 27.
- **`root_interfaces` was shown on steps 4–8**, and absent on 1–3 because the root placed nothing yet. Step 4 was
  shown `{"at":"powertrain/front-mount",…},{"at":"powertrain/rear-mount",…}` and asked for
  `powertrain/transmission-output` anyway — the view worked and the model ignored it.
- **Every parameter's value was shown on steps 2–8** (7 of 8; step 1 has no parameters yet).
- **The literal note never fired**, in either #111's exact-value form or #112's 2–4× form. Nothing in this run
  exercised it.

## Standard parts, repetition, density, mirrors (#92)

- **Standard parts: 1 design, 4 occurrences** — `mount-bolt`, `ISO 4762 M12x40`, the only `standard` shape.
- **The repetition warning fired**: `could be one pattern: 4 × mount-bolt in powertrain-core`. Those four bolts
  were written out by hand, which is exactly what the warning is for. **No `pattern` appears anywhere in the
  finished car** (`children with pattern: 0`).
- **Density was taught and used**: **10 of 16 definitions carry a density**, all in kg/m³ and all plausible —
  aluminium 2700 (×3), steel 7850 (×2), cast iron 7200, rubber 1100, CFRP 1600 (×3). **No definition made the
  g/cm³ mistake the contract warns about.** Six definitions carry no material at all.
- **Mirror pairs: 3** by bounding box, but only **one child** is written with `mirror`; the rest are authored twice.

## Interference

```
CAR-INTERFERENCE pairs=30 buried=5 truncated=false of parts=54
CAR-COVERAGE checked=47 of pairs=47 skipped_parts=0 truncated=false
```

**The list was not cut** — every pair was checked and every finding reported. Ten are printed, "… and 20 more".
What is buried:

| pair | fraction | volume |
|---|---|---|
| `suspension-linkage/rear-right-damper` in `powertrain/transmission` | **100%** | 589,049 mm³ |
| `suspension-linkage/rear-right-pushrod` in `powertrain/transmission` | 85% | 68,647 mm³ |
| `suspension-linkage/rear-right-upper-arm` in `powertrain/transmission` | 71% | 339,411 mm³ |
| `powertrain/bolt-1…4` in `powertrain/engine` | 50% each | 3,789 mm³ each |
| `brakes/line-fl`, `-fr`, `-rl` in their rotors | 32% each | 9,048 mm³ each |

The rear-right suspension corner is inside the gearbox: the suspension was placed at
`chassis/front-suspension-left` and its rear corner lands on the transmission. The four bolts 50% into the engine
are a bolt seated in its hole and are probably right; the brake lines 32% into the rotors are not.

## The visual check and the repairs

```
CAR-LOOKS-TOTAL calls=19 whole=8 sub_assemblies=11 findings=35 tokens=23,318 mean_seconds=2.9 max_seconds=6.0
```

19 vision calls — 8 of the whole model, **11 of sub-assemblies**, against 6 in each earlier run. The sub-assemblies
looked at were `powertrain` (×5), `suspension-linkage` (×4), `brakes` (×2) and `wheel-front-left` (×1), so coverage
followed the placements: with nothing unplaced, the looks reached four subsystems instead of one.

**Repair verdicts, and what each was judged on.** Five calls were shown interference text. Two kinds of evidence
drive them, and they are judged separately:

- **the look** (a named part and a sentence): step 1 *could not correct* "Cross Member Tube … disconnected dashes
  floating in empty space"; step 3 *corrected* "Mounting Bolt … oversized, floating linear structures"; step 5
  *corrected* "Control Arm … floating in space"; step 6 *corrected* "Brake Rotor … floating in space".
- **the kernel's interference list** (a named pair, a percentage and a volume): step 3 *"Parts were sitting inside
  each other. FORGE moved them apart and re-checked with the kernel"* — the only successful interference repair,
  and it was re-checked against the kernel rather than declared. Steps 5, 6 and 8 all *could not correct it*, each
  naming the same rear-right damper/pushrod/upper-arm in the transmission, with "and 14 / 26 / 27 more".

So the same three overlaps survived from step 5 to the end: once the suspension was placed inside the gearbox, no
later repair moved it.

## The kernel

```
CAR-FAULTS document_faults=0
CAR-KERNEL builds=yes volume=342,819,291 skipped=0 feature_failures=0 parts_not_in_file=0
CAR-MESH triangles=6,632
```

It builds, with nothing skipped and no part missing from the file — and with a suspension corner inside the
gearbox, which is the gap between `document_faults=0` and a model, restated for the fourth run running.

## Which of the four PRs' fixes are shown to work live

**Shown.**
- **#97** — a failed step names its gate (three did: `faults-added` ×3, read back by `StepGateOf`); an expression in
  a definition is read rather than losing the reply (step 1); an assembly a step built gets placed (4 times, and
  `unplaced=0` against 5 and 2); **a step cannot replace the root** — the root stayed `chassis` through all eight
  steps and nothing was wiped, which is precisely what destroyed run 2.
- **#111** — a step declares `placements` and FORGE places the assembly there and says so (4 times, 7 steps wrote
  one); the step view lists `root_interfaces` (steps 4–8); the step is shown every parameter's value (steps 2–8).
- **#112** — **children and interfaces keep `position_from`**: 27 bound placements against 0 in all four earlier
  runs, including a derived value in a placement. This is the clearest live result in the run.
- **#92** — standard parts read from the catalogue (ISO 4762 M12x40, 4 occurrences); the repetition warning fired
  (4 × mount-bolt); **density taught and used correctly** in kg/m³ by 10 of 16 definitions.

**Still unexercised.**
- **#111's cross-assembly refusal with the root-level instruction.** Neither refusal produced the new wording
  (`outside its assembly …; attach it from the root … instead`). Both got the old `has no child` sentence — see the
  defect below. The fix that #111 was largely written for is **not** shown to work, and this run is evidence
  against it covering the case that actually arose.
- **The literal-position note**, in both #111's exact form and #112's 2–4× form: never fired.
- **Patterns and the polar-pattern contract sentence** (#97): no `pattern` in the finished car; the wheel step was
  refused before any polar ring survived, so lug nuts not burying is again unmeasured.
- **The cylinder-axis sentence**: no wheel reached the finished car, so it is untested for the third run running.
- `size_from`/`position_from` surviving a **respec** live: bound placements exist now, but nothing re-specified them.

## New defects this run exposed

1. **A declared placement whose `at` starts with the root's own id is refused, and #111's leading-root-id drop does
   not cover it.** Step 2 declared `{"id":"suspension-mounts","ref":"suspension-mounts","at":"chassis/cockpit-floor"}`
   with root `chassis`. #111 says "A leading root id is dropped"; it was not dropped here, and the step was refused
   `assembly "chassis" has no child "chassis"`. The step's own reply also wrote four correct sibling paths
   (`chassis/front-suspension-left`, …), so the model was consistent and FORGE was not. **Cost: the whole uprights
   and hubs step, and with it the wheels that depended on those hubs.**
2. **The fault names no child.** The repair was shown `- is attached at "chassis/cockpit-floor", but …` — an empty
   name before "is attached", so neither the model nor a reader can tell which child is meant.
3. **The refusal carries no remedy.** Because the new wording did not fire, the fault repair was never told to
   attach from the root, and both repairs on steps 2 and 4 failed.

## What one run cannot establish

- **n=1, and it overspent.** Two calls were refused and step 8 never got its look, so this is a floor. The
  three problem steps may not repeat, and the 27 bound placements are one model's behaviour on one prompt.
- The comparison table mixes runs with different fixes *and* different plans (7 steps vs 8). Per-call cost has risen
  every run (4,840 → 6,692), but so has what each call is shown — nothing here separates the two.
- **A bigger car is not a better car.** 54 occurrences with a suspension corner inside the gearbox and no wheels is
  not obviously better than car-tree's 39. Parts placed is the number that moved; fidelity is not measured here.
- Nothing says the bound placements would survive a respec, or that the repetition warning would have been acted on
  had the run not ended.
- The run shared a loaded laptop, so latencies are indicative only.
