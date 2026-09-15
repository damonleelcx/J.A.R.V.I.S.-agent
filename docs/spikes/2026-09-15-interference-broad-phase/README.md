# Measurement: the interference broad phase as a sweep (K2b)

**Date:** 2026-09-15 · **Status:** done · **Stage:** Phase 4, K2b of [`docs/plan-2026-09-13-millions-of-parts.md`](../../plan-2026-09-13-millions-of-parts.md)

## Summary

- **At 10,000 parts the interference phase fell from 10.4 s to 2.1 s**, and from 69% of the build to 30%. The whole
  build fell from 15.1 s to 6.9 s.
- **Box tests fell from 49,995,000 to 495,000.** What is left of the phase is reading each solid's bounds and volume
  once, which grows linearly with the part count.
- **The sweep is linear only when parts spread along one axis.** On this square grid it costs about √n box tests per
  part (15,128 → 129,024 → 495,000), because every column of the grid is open at once. The fence grid, 8 studs across
  and up to 512 along, costs 28 per row and grows exactly 8× for 8× the parts. A car is long in one axis; a floor
  plan or a panel of rivets is not, and an index over all three axes is Phase 5's V1.
- **The results are the same as comparing every pair.** `TestKernel_TheBroadPhaseFindsWhatEveryPairFinds` and
  `TestKernel_ATruncatedBroadPhaseStopsWhereEveryPairStops` compare the sidecar's check with the old loop on the same
  solids, including a check stopped by the pair budget.

## Why this measurement

K1 found that 27% of a cached 10,000-part build was the interference check's every-pair bounding-box scan
([K1 spike](../2026-09-14-definition-cache/README.md)), and K2 removed the other quadratic step. The plan's K2b
acceptance is a counted number of pairs compared that grows about linearly on a spread grid; this records what the
sweep costs past S0's 4,096-part build ceiling, where the Go path cannot reach.

## Method

`measure.py` calls the sidecar's own `_build` with the request K1's spike used: N copies of a 4 × 6 × 8 mm box on a
square grid 30 mm apart, so no two boxes meet and none pays for a boolean. Each N is built twice:

- **sweep:** the sidecar as shipped;
- **every-pair:** `_candidate_pairs` replaced by the scan it replaced, every pair's boxes compared in index order.

"interference s" is the sidecar's own `phases.interferences`; "box tests" is `interference_box_tests`.

Environment: Intel Core i7-12650H (16 logical processors), Windows 11, Python 3.13.9, build123d 0.11.1,
cadquery-ocp-novtk 7.9.3.1.1. One run per row; no variance figure. Measured with no other build running.

## Raw results

```
      N mode         build s  interference s  share    box tests found
   1000 sweep           0.63           0.185    29%        15128 0
   1000 every-pair      0.69           0.251    36%       499500 0
   4096 sweep           2.66           0.820    31%       129024 0
   4096 every-pair      4.00           2.052    51%      8386560 0
  10000 sweep           6.87           2.051    30%       495000 0
  10000 every-pair     15.09          10.375    69%     49995000 0
```

## How the conclusions follow

- **The every-pair scan was quadratic.** From 1,000 to 10,000 parts, its box tests rose 100× and its phase 41×.
- **The sweep phase is linear in practice.** It rose 11× over the same 10× (0.185 → 2.051 s), while its box tests rose
  33×. The per-solid bounds and volume dominate: the fence's 4,096-part grid, at 14,336 box tests, also spent
  0.8–0.9 s in the phase.
- **Box tests on a square grid are n^1.5.** A grid of side s is s columns of s parts, each column open together, so
  s · s(s−1)/2 tests: 64 · 2,016 = 129,024 at 4,096 and 100 · 4,950 = 495,000 at 10,000. 1,000 parts on a side of
  32 make 8 columns of 32 and 24 of 31: 8 · 496 + 24 · 465 = 15,128.

## Caveats

- One definition, no overlaps, so the narrow phase (the booleans) is not measured. It was not changed.
- One laptop, one run each, Windows rather than the production arm64 Linux image.
- The pair budget (2,000 booleans) is unchanged, and a dense model still stops at it.

## Re-running

```bash
.cadvenv/bin/python docs/spikes/2026-09-15-interference-broad-phase/measure.py
```
