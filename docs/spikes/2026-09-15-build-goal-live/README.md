# A live car built as an engine goal

**Run 2026-09-15, 11:42–11:51 local. One run, complete, under a 300k ceiling.**
Data: [`data/`](data) — the final model ([`car.json`](data/car.json), 12.7 KB), per-step figures
([`steps.json`](data/steps.json)), the driver's and the progress log, and forge-worker's log filtered to model
calls, tasks, saves and kernel lines. Harness: [`harness/run-live.sh`](harness/run-live.sh),
[`harness/steps.py`](harness/steps.py).

**Why.** Stage A1 (#85) made a build a goal — one task per step, each kept as a version, every call charged, a
stopped worker resumed from the last step kept — and nothing live had run it. This is that run, with the prompt of
the in-request run in [`2026-09-15-car-tree`](../2026-09-15-car-tree) (branch `agent/measure-car-tree`).

**Setup.** Local Postgres (a local-only owner `a1b-live-build@forge.local`, project in the Automotive pack),
production model endpoint (`token-plan.cn-beijing.maas.aliyuncs.com`, converse `qwen3.7-plus`, vision
`qwen3.8-max`), `forgectl goal new --build --start` from this branch, then one `forge-worker` with the kernel
(build123d 0.11.1 on Windows, `FORGE_CAD_POOL=1`, scripts off, one task loop, lease 60 s). The goal's
`max_tokens` was set to 300,000 before any worker ran, and `FORGE_MAX_TOKENS_PER_GOAL=300000` covered the planning
call. A watchdog in the driver would have killed the worker at 255,000 spent; it never fired. Other agents' tests and
kernels were running on the machine throughout (one database ping timed out, below).

## What it cost

**172,348 tokens: the planning call and 26 step calls, in 9 minutes 14 seconds.** 57% of the ceiling; nothing refused.

Every call was charged: the model calls forge-worker logged (171,627 prompt + completion tokens) plus the planning
call (721) are exactly the goal's `tokens_spent`.

| step | what | calls | tokens | took | kept |
|---|---|---|---|---|---|
| plan | 8 steps | 1 | 721 | 6 s | — |
| 1 | Chassis Frame | 1 | 11,716 | 23 s | **nothing** — "produced no geometry" |
| 2 | Suspension Uprights | 1 | 12,637 | 34 s | **nothing** — "produced no geometry" |
| 3 | Drivetrain Assembly | 7 (4 + 3) | 28,510 (15,677 lost + 12,833) | 2 m 11 s | 4 parts |
| 4 | Suspension Arms | 2 | 12,765 | 16 s | 13 parts |
| 5 | Braking System | 3 | 18,006 | 42 s | 20 parts |
| 6 | Wheels and Tires | 4 | 25,830 | 80 s | 24 parts |
| 7 | Steering Rack | 4 | 29,077 | 96 s | 30 parts |
| 8 | Body Panels | 4 | 33,086 | 119 s | 36 parts |

Six versions kept, one per step that built something, each named on the timeline by the step that made it. Calls per
step grow with the model: every step after 3 was shown the whole model (it is flat, so there is no subtree to narrow
to) and was looked at.

## Resume: stopped once, on purpose

The driver killed forge-worker (`taskkill /F`, which is a SIGKILL, not a graceful stop) at 11:44:09, 49 s into step
3, after that step's fourth call. A new worker started 6 s later.

- Its first database ping timed out under the machine's load and it retried (11:44:34).
- At **11:45:07, 58 s after the kill**, it found step 3's lease expired, returned it to the queue and claimed it:
  attempt 2. The lease was 60 s.
- **Steps 1 and 2 were not built again** (no calls for them in the second worker's log).
- Attempt 2 started from the model the step before kept — which was empty, because steps 1 and 2 kept nothing.
- **15,677 tokens** spent by attempt 1 stayed charged to the goal and bought nothing: what a hard stop costs is the
  step it lands in, as designed. A call cut off in flight by the kill would have been paid for and not recorded; the
  last recorded call finished 7 s before the kill, so one may have been.

A graceful stop (the step handed straight back) was not exercised live: this laptop cannot send forge-worker a
console signal from the driver. The fence `TestBuildGoal_AStoppedWorkerHandsItsStepBack` covers it.

## What came out

[`data/car.json`](data/car.json): **36 parts, flat** — no definitions, no assemblies, no patterns or mirrors, no
features. Cylinders 16, boxes 10, extrusions 4, spheres 2, and 4 `tube`s (a retired word, drawn as solid cylinders).

It has what the in-request run lacked: chassis frame rails, an engine block, transmission, differential and drive
shaft, uprights, four control arms and two shocks, two brake rotors and calipers with a master cylinder and lines, a
steering rack with tie rods and ball joints, and body panels (nose, side pods, engine cover, rear deck, diffuser).

What is wrong with it, from the steps' own notes:

- **Two wheels.** Step 6 built front left and front right only.
- **Wheels on their side.** The look saw a tyre "oriented horizontally like a plate" (step 6) and a rim "standing on
  its edge" (step 7) and could not correct either; a rotor was oriented wrongly too (step 5).
- **Rims 100% inside tyres**, calipers 47% inside rotors, control arms 32% inside the frame rails — found by the
  kernel's interference check, not corrected.
- **Steps 1 and 2 produced no geometry.** The chassis rails in the final model came from step 4, not step 1.

## Against the in-request run (2026-09-15, car-tree)

| | in-request (`AssembleForTest`) | **build goal (this run)** |
|---|---|---|
| tokens / calls | 164,548 / 34 | **172,348 / 27** |
| steps planned / kept something | 7 / 6 | **8 / 6** |
| steps with no geometry | 1 (step 1) | **2 (steps 1, 2)** |
| steps left out as breaking the model | 2 (drivetrain, brakes) | **0** |
| parts | 39 occurrences of 9 definitions in 6 assemblies | **36 flat parts** |
| mirror pairs / standard parts | 14 / 10 | **0 / 0** |
| chassis / drivetrain / brakes | none / none / none | **yes / yes / yes** |
| wall clock | 7 m 39 s | **9 m 14 s** (58 s of it the deliberate stop) |
| survives a killed worker | no — the build is in one request | **yes — lost one step's attempt (15,677 tokens)** |

## What this run establishes

1. **A build runs end to end as a goal on a live model.** Planned, activated, every step claimed in order, six
   versions kept, the goal settled `succeeded`.
2. **The budget sees every call.** Logged calls plus planning equal `tokens_spent` exactly, repairs and looks included.
3. **A killed worker costs one step's attempt, and only that.** The kept steps were not rebuilt; the lost step was
   reclaimed on lease expiry and finished.
4. **A car fits the 300k ceiling as a goal too**, at 57% with a lost attempt included.

## What it does NOT establish

- **n=1, and a different plan.** Eight steps here against seven, and the model is sampled. The goal path did not
  cause the flat tree or the missing definitions: both runs use the same step prompts and contract, and one run each
  cannot separate the path from the sample.
- **Why steps 1 and 2 built nothing.** The same failure as step 1 of the in-request run. A step's reply is not kept
  when it produces no geometry, so this run cannot say whether the reply was unparseable, an edit to an empty model,
  or no prototype. Worth fixing before the next live run (keep the rejected reply on the task's result).
- **A graceful stop, live.** Only the hard kill was exercised.
- **Scripts.** Off (the Windows script runner is known broken).
- **The HTTP entry point, live.** This run started from `forgectl`; `POST /v1/goals` with `build: true` is fenced
  against Postgres with a stub model, not run live.
