# Spike: the definition cache (K1), measured past the build ceiling

**Date:** 2026-09-14 · **Status:** done · **Stage:** Phase 4, K1 of [`docs/plan-2026-09-13-millions-of-parts.md`](../../plan-2026-09-13-millions-of-parts.md)

## Summary

- **K1's acceptance holds at 10,000 occurrences.** One definition placed 10,000 times builds with **one** shape build,
  and the volume is exact per occurrence.
- **For primitives, the cache saves only ~13% of the time.** 10,000 boxes take 19.3 s cached against 22.3 s uncached.
  Building a box or a cylinder is cheap, so building it once changes little. Expensive shapes (outlines, sweeps,
  imported scripts) are where the cache pays, and a repeated scripted part now runs its script once instead of once per
  copy.
- **The 10k build time is spent elsewhere**, profiled below:
  - **64%** goes to assembling `Compound(children=…)`, which build123d re-attaches child by child, so the cost grows with
    the square of the part count;
  - **27%** goes to the interference check's all-pairs bounding-box scan, which is also quadratic.

  Neither is K1's to fix. The first is stage **K2** (an XDE assembly, never `Compound(children=…)` for occurrences). The
  second needs a spatial index in the interference check.

## Why this spike

K1's acceptance says "1 definition × 10k occurrences builds with one shape build (counted), exact volume per occurrence".
Stage S0 refuses to build more than 4096 parts through the Go path (`cad.BuildDocument`). So the Go kernel test proves K1
at the ceiling (4096 occurrences, one build), and this script measures past it, calling the sidecar's `_build` directly
with the request the kernel would send.

## Method

`measure.py` builds requests of N occurrences of one primitive on a 30 mm grid, far enough apart that no two bounding
boxes meet. It runs each N twice:

- **cached**: the sidecar as shipped;
- **uncached**: `_shape_key` made unique per occurrence, which is the sidecar before K1.

It reports wall time, the reply's `shape_builds` and `parts`, and whether the volume is exactly N × one solid's volume.
The profile is a single `cProfile` run of the cached 10,000-box build.

Environment: M-series laptop, macOS, Python 3.14.6, build123d 0.11.1, cadquery-ocp-novtk 7.9.3.1.1. One run per row;
no variance figure.

## Raw results

```
shape           N mode        build s  shape_builds   parts volume exact
box          1000 cached         0.36             1    1000 True
box          1000 uncached       0.46          1000    1000 True
box          4096 cached         3.50             1    4096 True
box          4096 uncached       3.91          4096    4096 True
box         10000 cached        19.25             1   10000 True
box         10000 uncached      22.26         10000   10000 True
cylinder     1000 cached         0.37             1    1000 True
cylinder     1000 uncached       0.54          1000    1000 True
cylinder     4096 cached         3.93             1    4096 True
cylinder     4096 uncached       4.52          4096    4096 True
cylinder    10000 cached        18.82             1   10000 True
cylinder    10000 uncached      21.82         10000   10000 True
```

Profile of the cached 10,000-box build (27.2 s under the profiler), by cumulative time:

| function | cumulative | what it is |
|---|---:|---|
| `sidecar._build` | 27.2 s | the whole request |
| `Compound.__init__` → `_post_attach` | 17.5 s | build123d attaching 10,000 children one at a time |
| `_make_topods_compound_from_shapes` (10,002 calls) | 9.8 s | rebuilding the compound on every attach |
| `sidecar._interferences` | 7.5 s | all-pairs scan: 49,995,000 `_boxes_miss` calls |
| `Compound.volume` | 1.3 s | the reply's volume |
| `sidecar._placement` (10,000 calls) | 0.7 s | locating the one shape 10,000 times |

## How the conclusions follow

- **One build, exact volume.** `shape_builds` is 1 in every cached row and N in every uncached row. The volume is
  exact in all twelve rows, so located copies of one build are exact.
- **The cache saves little for primitives.** Cached vs uncached differ by 0.1 to 3 s at every N. That is the cost of
  building N primitives, and it is small next to the whole.
- **The remaining time is quadratic.** From 4,096 to 10,000 occurrences (2.4×), the cached time rose from 3.5 s to
  19.3 s (5.5×). The profile names the two quadratic steps: compound attachment (10,002 compound rebuilds) and the
  all-pairs box scan (n(n−1)/2 = 49,995,000 comparisons).

## Caveats

- Primitives only. A shape with a detailed outline or a scripted import costs much more to build, so the cache saves a
  larger share there. That is not measured here.
- The occurrences never touch, so the interference check does box comparisons and no booleans. Touching parts would
  add boolean work on top of the quadratic scan.
- One laptop, one run each.

## Re-running

```bash
.cadvenv/bin/python docs/spikes/2026-09-14-definition-cache/measure.py
```
