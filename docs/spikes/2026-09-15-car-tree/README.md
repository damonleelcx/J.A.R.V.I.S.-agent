# A live car build, measured as a tree

**Run 2026-09-15. One run, complete, under a 300k ceiling.**
Raw output: [`data/run.log`](data/run.log). Harness: `internal/agent/car_ceiling_live_test.go`, the A4 harness
(`car_tree_measure_test.go`), stacked on stages A2 (subtree context) and A3 (standard parts and patterns).

**Why it was run.** Phase 2's live milestone: after the contract learned trees, standard parts and patterns,
does a live model use them, what does the car cost, and does it build? Same prompt as the 2026-09-12 run
([`../2026-09-12-car-ceiling`](../2026-09-12-car-ceiling)), so the numbers compare.

**Endpoint.** Production's: `token-plan.cn-beijing.maas.aliyuncs.com`, converse `qwen3.7-plus`, vision
`qwen3.8-max`, kernel build123d 0.11.1 on Windows. The in-request build loop (`AssembleForTest`), not a build goal:
stage A1's goal path is on a sibling branch (#85).

## What it cost

**164,548 tokens over 34 calls in 7 minutes 39 seconds**, against a 300,000 ceiling. Nothing was refused.

| | 2026-09-12 | **2026-09-15** |
|---|---|---|
| tokens / calls | 151,638 / 17 (budget ran out) | **164,548 / 34** (54% of the ceiling) |
| steps planned / run | 7 / 6 (+1 refused) | **7 / 7** |
| per call | 8,920 | **4,840** |
| parts (occurrences) | 28 | **39** |
| definitions / assemblies | — (flat) | **9 / 6** |
| tokens per design | ~5,400 per part | **18,283 per definition**; 4,219 per occurrence |
| standard parts | — | **10** |
| mirror pairs | 0 | **14 (28 of 39 parts)** |
| document faults | 0 | **0** |
| builds in OCCT | yes | **yes** — 283,876,617 mm³, nothing skipped |
| mesh | 3,584 triangles | 4,792 triangles |

## What came out

- **It used the tree.** 9 definitions placed 36 times through 6 assemblies. The suspension was built once and
  mirrored (`right-suspension-asm/left-suspension-mirror/...`), and lug nuts are placed by a pattern
  (`lug-nut-pattern-3`).
- **It used standard parts.** 10 of 39 parts are catalogue designations (A3). The run log does not name them.
- **It did not enumerate repetition.** `could_be_patterns=0`: no run of four or more identical siblings was
  written out by hand.
- **It built.** The finished tree went through the real kernel with nothing skipped, and the interference check
  covered **80 of 80 pairs**, untruncated (V1/V2).

## What went wrong

### Interference: 77 overlapping pairs, 53 buried

Every one of the ten shown is a lug nut **100% inside** the hub, the rim and the tyre at once (1,809 mm³ each), on
both the left wheel and its mirror. A nut on a stud passes through a rim's bolt circle, so some contact is right.
Being wholly inside the tyre is not: the pattern's copies sit inside the wheel's solids rather than on the face.
This is the first live measurement of V1 on a real model, and it reports exactly the kind of fault
`document_faults=0` hides.

The other 67 findings are not in the log, which prints ten.

### Three of seven steps had a problem

```
step 1/7 Chassis Frame       parts=0  produced no geometry
step 4/7 Drivetrain Assembly parts=4  was left out: it would have broken the model
step 5/7 Braking System      parts=4  was left out: it would have broken the model
```

So **the car has no chassis, no drivetrain and no brakes**. What it has is two suspension corners with wheels and
some body panels.

### The visual check saw floating parts, four times

Steps 2, 3, 6 and 7 each drew "floating in space, not mounted", twice as *could not correct* and twice as
*corrected*. The absent chassis is the likely cause: step 1 built nothing, so nothing afterwards had hardpoints to
mount to. This is inference from the notes, not measured.

### Every position is literal

`literal_positions=36 bound_positions=0`: every placement is arithmetic the model did in its head, which is Wall A
of the 2026-09-12 run, unchanged.

## What this run establishes

1. **The contract changes what a live model writes.** From flat parts to 9 definitions in 6 assemblies, a mirrored
   corner, a lug-nut pattern and catalogue parts, all in one run with no prompting beyond the contract.
2. **Per-call cost halved** (8,920 → 4,840 tokens). Consistent with A2's subtree context, though one run cannot
   separate A2 from other causes, and the calls doubled.
3. **A 7-step car fits well inside 300k.** It used 165k, so the approved ceiling has headroom for a longer plan or
   for repairs.
4. **The interference check earns its place.** On the first live tree it found nuts buried in wheels that the
   document's own faults, the kernel build and the visual check all passed.

## What it does NOT establish

- **n=1.** One sample is not a rate, and the step failures (1, 4, 5) may not repeat.
- **The car document was not kept.** The harness wrote `car.json` into the test's temp directory, which Go
  removes when the test ends, so the lug-nut placement cannot be re-examined. The harness now writes it to the
  system temp directory instead (this commit). The next run keeps it.
- **Not a build goal.** Resume, per-step versions and goal budget accounting (A1, #85) were not exercised live.
- **Scripts.** They were enabled in the harness, but the Windows script runner is known to fail
  (`TestScript_*`). A step that reached for a script would have failed its check here, and nothing in the log shows
  whether any did.
- **No chassis means no mounting.** Whether the floating-parts findings come from step 1 alone, or would persist
  with a chassis, is not measured.
