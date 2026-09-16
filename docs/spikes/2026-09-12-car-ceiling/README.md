# How far a live car build actually gets

**Run 2026-09-12. One run, partial, and the partiality is deliberate.**
Raw output: [`data/run-1.log`](data/run-1.log). Harness:
`internal/agent/car_ceiling_live_test.go`, run with `make measure-car`.

**Why it was run.** `docs/research-2026-09-12-vehicles-aircraft-and-structures.md`
listed eight walls read out of the code. Reading is how this repository has
repeatedly been wrong — Stage 4 found freeform surfaces mostly already existed,
Stage 11 found the control's zero was a sidecar bug and not the model. So the
walls were measured before anything was built against them.

**What it cost, and why it stopped.** 151,638 tokens over 17 calls in 9 minutes 16
seconds, against a hard 150,000 budget enforced in the harness before each call.
Two calls were refused when it ran out. **This is a floor on the ceiling, not the
ceiling** — step 7 never ran.

Per placed call: **8,920 tokens**. Per productive step: **~30,300**. A full
ten-step build is therefore ~300k, which is the estimate this run was authorised
against and confirms.

## What was asked

> a sports car, in as much mechanical detail as you can manage

The same prompt Stage 2 used, so the part count is comparable.

## What came out

| | |
|---|---|
| steps planned / run / productive | 7 / 7 / **5** |
| parts | **28** |
| features | 7, all `fuse` |
| wall clock | 9.3 min |
| document faults | **0** |
| **builds in the real OCCT kernel** | **yes** — 526,715,693 mm³, nothing skipped, no feature failures |
| mesh | 3,584 triangles |

It built. That is worth saying first and plainly: the finished assembly went
through `BuildDocument` in the real kernel with nothing skipped and no part
missing from the file. The rest of this document is about what "it built" does
not mean.

## Wall by wall, against what was predicted

### ✅ C — nothing is hollow. Confirmed, and worse than stated.

```
CAR-HOLLOW parts_with_holes=0 cut_features=0 of parts=28
CAR-FEATURES fuse=7
```

**Zero** holes. **Zero** cuts. Not one of the 28 parts has a wall, a bore or a
void. The seven features are all `fuse` — the only feature op used in the whole
build.

The volume makes it concrete: 526,715,693 mm³ is **0.53 m³ of solid**. As steel
that is about 4.1 tonnes, and the point is not the exact figure — no material was
declared — but that every part is a solid lump. A monocoque, a brake rotor's
vanes, a wheel rim, a tyre, an exhaust: all of them are *defined* by being
hollow.

### ✅ B — no mirror, and it costs two thirds of the car.

```
CAR-SYMMETRY mirror_pairs=9 parts_in_a_pair=18 of 28
```

**18 of 28 parts (64%) were authored twice** as left/right pairs — left damper
and right damper, left upright and right upright, and so on. Every one of them is
a second chance to type a different number, and the run took one of those chances
(below).

### ✅ F — verification does not see interference. Confirmed, with named parts.

```
CAR-FAULTS document_faults=0
CAR-KERNEL builds=yes
CAR-OVERLAP-BBOX-PROXY pairs=4 of parts=28
  master-cylinder is 100% inside engine-block
  pinion-gear     is  88% inside central-tunnel
  left-upright    is  70% inside left-front-rotor
  right-upright   is  70% inside right-front-rotor
```

**This is the most important line in the run.** Faults: zero. Kernel: builds.
Vision check: passed on these. And the master cylinder is *entirely* inside the
engine block.

Two of the four are arguably legitimate — a pinion inside a tunnel is roughly
where a pinion goes. The uprights inside the rotors and the master cylinder
inside the engine block are not. **No check in FORGE today can see any of them**,
and that is stated in the code rather than inferred (`geometry/assembly.go`: no
interference test, no clearance, no kinematics).

Note the bounding-box overlap is a *proxy*, named as one in the log line. It is
not a solid-solid interference test. It found real defects anyway.

**→ Closed the same day.** `geometry/interference.go`, `cad/sidecar.py` and
`agent/interference.go` now compute shared material on the solids that survive
the features, report it in the turn, and repair the buried cases. The proxy in
this harness has been replaced by the kernel's own answer, and the harness now
prints **every** finding with its fraction — because `geometry.BuriedFraction`
was chosen without live data, and this run's distribution is what should decide
it. The next `make measure-car` produces that distribution.

The harness also **saves the finished document** now. This run did not, which is
why the threshold could not be calibrated against the only real car this system
has ever built without paying for another one.

### ✅ H — the vocabulary ceiling, visible in one line.

```
CAR-SHAPES box=13 cylinder=11 extrusion=3 gear=1
```

Twenty-four of 28 parts are boxes and cylinders. **Zero** lofts, revolves,
sweeps, sections or scripts. Zero fillets and zero chamfers. A car body is a loft
through sections and Stage 4 established that `loft` works and blends smoothly —
it was not reached for once.

One bright spot, and it is Stage 11's: `gear=1`. The pinion was built with the
`"gear"` shape. **A named shape in the vocabulary gets used**, unprompted, inside
a build about something else entirely. That is the strongest available evidence
that the "put it in the vocabulary" lever generalises.

### ⚠️ A — the prediction was half wrong, and the correction matters

The research doc said parts are placed by "arithmetic the model did in its head".
Measured:

```
CAR-PLACEMENT literal_positions=7 bound_positions=21 id_prefixes=12
```

**21 of 28 positions were bound to expressions**, not typed as numbers. The model
does use `position_from`. That half of the claim was wrong and is corrected in the
research doc.

The wall is real, but it is not arithmetic — it is that there is nothing to
attach *to*. Three of the five productive steps ended with a part floating in
space, each caught by the vision check in the same words:

| step | what the vision check said |
|---|---|
| 2 Suspension | *"The damper is floating in the air near the front left corner, not connected to the chassis or the control arm"* — corrected |
| 4 Braking | *"the Left Front Brake Line … floating in the air between the master cylinder and the left caliper, not physically connected to either"* — **could not be corrected** |
| 5 Steering | *"the Left Tie Rod … a thin line floating in space, not visually connected to the steering rack or the upright"* — corrected |

All three are the same defect: a part that exists to *connect two other parts*,
with no way to say so. An expression over global parameters can put a damper at a
coordinate; it cannot say "this end is on the control arm".

### ✅ G — the budget, and the car has no wheels

```
step 6/7 Wheels and Tires  parts=28  Step 6 was left out: it would have broken the model.
step 7/7 Body Panels       parts=28  … the measurement's token budget is spent
```

Step 6 was refused by the build loop's own guard — a pass that increases the
fault count is dropped and the previous state kept. That guard did exactly its
job, and the result is a sports car with **no wheels, no tyres and no body**.

Step 7 was lost to the spending ceiling, which is this run's artefact and not a
product limit.

### Not exercised

D (members along a curve) and E (section catalogue) were not reached — nothing
swept, nothing needed a standard section. Their absence is consistent with the
shape histogram but this run does not measure them. A bridge or a house would.

## What this run establishes

1. **Complexity is not the binding constraint at 28 parts.** It built, cleanly,
   in the real kernel. The constraints that bit are *fidelity* ones: nothing
   hollow, nothing curved, nothing connected, nothing checked for interference.
2. **The most dangerous number here is `document_faults=0`.** A model that builds,
   reports no faults, passes the visual check and has a master cylinder inside the
   engine block is exactly the failure mode this product is built to refuse. It is
   not lying — nothing asked the question.
3. **A named shape gets used.** `gear=1` inside a car build is the evidence for
   the strategy the research doc recommends.
4. **~30k tokens per step** is the real figure to plan spends against.

## What it does NOT establish

- **n=1, and partial.** One run, two steps short. Stage 10's warning applies:
  measured before believed, and one sample is not a rate.
- Nothing here says a mirror, a shell or a frame would raise any of these
  numbers. It says what today's numbers are.
- The overlap figure is a bounding-box proxy. A real interference test would find
  a different set — probably more.
