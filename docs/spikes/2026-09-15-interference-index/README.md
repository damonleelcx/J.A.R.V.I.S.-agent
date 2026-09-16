# Measurement: interference at scale (V1)

**Date:** 2026-09-15 · **Status:** done · **Stage:** Phase 5, V1 of [`docs/plan-2026-09-13-millions-of-parts.md`](../../plan-2026-09-13-millions-of-parts.md)

## Summary

- **The plan's acceptance holds: 30,000 occurrences are checked in full in 1.69 s.** 10,000 plates each with two pins through them are 20,000 clashes. All 20,000 are found with **2 booleans** (one per pose), 19,998 answers reused, and the check is not truncated.
- **Without reuse the same model stops at the budget.** 2,000 booleans cost 8.4 s and find 2,000 of the 20,000 clashes, marked truncated. The same happens at 9,000 occurrences.
- **A plane of parts costs a fraction of a box test each.** 30,000 studs spread evenly over a plane need 7,413 box tests (0.25 a part) and 0.57 s. K2b's one-axis sweep needed 495,000 tests and 2.05 s for 10,000 parts on a plane.
- **Full coverage depends on repetition.** 20,000 clashes at distinct poses would still pay 20,000 booleans and stop at the budget; V2 is the stage that makes a truncated check say "checked X of Y".

## Why this measurement

V1's acceptance is "30k occurrences finish in a recorded time with full coverage; the existing five kernel fences still pass". `cad.BuildDocument` refuses more than 4,096 parts (stage S0), so the numbers come from the sidecar's own `_build`, as K1's, K2b's and K4's did. The five fences, K2b's three and V1's three pass through the Go path at up to 4,096 parts.

## Method

`measure.py` calls `_build` with plain requests:

- **pinned:** N/3 plates (20 × 4 × 20 mm), each with two pins (radius 2, height 10) standing through it 5 mm either side of centre, cells 40 mm apart. Every pin shares 40% of itself with its plate. Run with `_INTERFERENCE_CACHE` on (as shipped) and off.
- **plane:** N studs (4 × 6 × 8 mm) on a square grid, 10 mm apart along x and 20 mm along z. Nothing touches.

"interference s" is the sidecar's `phases.interferences`. "box tests", "pairs", "booleans" and "reused" are the reply's `interference_box_tests`, `interference_pairs`, `interference_booleans` and `interference_reused`.

Environment: Intel Core i7-12650H (16 logical processors), Windows 11, Python 3.13.9, build123d 0.11.1, cadquery-ocp-novtk 7.9.3.1.1. One run per row; no variance figure.

‼️ **A first run of this measurement was discarded.** It started while a mutation drill had `sidecar.py` rewritten in the same checkout — the drill passes the interference check no placements, which turns reuse off — and it reported 0 reused and a truncated check at 9,000. The drill script restores the file byte for byte, but anything that reads the file during the drill reads the mutation. The numbers below are from a rerun with nothing else touching the checkout.

## Raw results

```
      N mode            build s interference s  box tests     pairs  booleans   reused   found truncated
   3000 pinned cached      1.54          0.131       2000      2000         2     1998    2000     False
   3000 pinned uncached     8.94          7.663       2000      2000      2000        0    2000     False
   3000 plane              1.64          0.156        709         0         0        0       0     False
   9000 pinned cached      4.60          0.530       6000      6000         2     5998    6000     False
   9000 pinned uncached    13.36          9.270       6000      6000      2000        0    2000      True
   9000 plane              4.88          0.277       2179         0         0        0       0     False
  30000 pinned cached     15.56          1.688      20000     20000         2    19998   20000     False
  30000 pinned uncached    23.03          8.397      20000     20000      2000        0    2000      True
  30000 plane             16.20          0.566       7413         0         0        0       0     False
```

## How the conclusions follow

- **A boolean costs about 3.8 ms here** (7.66 s for 2,000 at N = 3,000), so reuse is what makes 20,000 clashes affordable: 2 booleans against a budget of 2,000.
- **The broad phase is linear.** Box tests on the pinned model equal the overlapping pairs (each plate against its two pins), and on the plane they grow 709 → 2,179 → 7,413 for 3k → 9k → 30k. With reuse on, the interference phase grows 0.13 → 0.53 → 1.69 s for 10× the parts.
- **The rest of the build** (15.6 s at 30k) is building shapes and assembling them, which K1 and K2 own.

## Caveats

- Two definitions and two poses: the best case for reuse. A real car has hundreds of definitions and many distinct poses, and a clash measured once per pose helps only where poses repeat.
- No features: a part a feature was applied to is always measured on its own.
- One laptop, one run each, Windows rather than the production arm64 Linux image.

## Re-running

```bash
.cadvenv/bin/python docs/spikes/2026-09-15-interference-index/measure.py [N ...]
```
