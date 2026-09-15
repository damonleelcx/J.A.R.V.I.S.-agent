# Measurement: a mesh tessellated once per definition (K4)

**Date:** 2026-09-15 · **Status:** done · **Stage:** Phase 4, K4 of [`docs/plan-2026-09-13-millions-of-parts.md`](../../plan-2026-09-13-millions-of-parts.md)

## Summary

- **At 30,000 occurrences, tessellating fell from 129 s to 0.73 s**, and the whole mesh build from 154 s to 26 s.
- **Triangles are counted once per definition:** 12 for 30,000 copies of one box, against 360,000 when every copy was
  meshed on its own.
- **The mesh payload fell from 21.0 MB to 4.6 MB.** What remains is one instance per occurrence: its id, label and
  16-number matrix as JSON, about 150 bytes each. Binary instance data would shrink that further; the plan's decision
  was JSON until a number demands otherwise, and at 30k this is the number to revisit when the viewport (W1) loads it.
- **Per-definition meshing grows linearly with occurrences** (0.03 → 0.11 → 0.28 → 0.73 s for 1k → 4k → 10k → 30k),
  because what grows is writing instance matrices, not tessellating.

## Why this measurement

The plan's K4 acceptance is "30k-occurrence car mesh payload size and time recorded; triangles counted once per
definition". `cad.BuildDocument` refuses more than 4,096 parts (stage S0), so the 30k number is taken through the
sidecar's own `_build`, the way K1's and K2b's spikes were.

## Method

`measure.py` sends `_build` N copies of one 4 × 6 × 8 mm box on a square grid 30 mm apart with `format: "mesh"`, twice:

- **per-definition:** the sidecar as shipped;
- **per-solid:** `_MESH_PER_DEFINITION` off, which is the tessellation K4 replaced.

"mesh s" is the sidecar's own `phases.mesh`. "payload MB" is the JSON of the reply's `mesh`, `mesh_definitions` and
`mesh_instances`, which is what the mesh endpoint forwards.

Environment: Intel Core i7-12650H (16 logical processors), Windows 11, Python 3.13.9, build123d 0.11.1,
cadquery-ocp-novtk 7.9.3.1.1. One run per row; no variance figure. No other build ran during the measurement.

## Raw results

```
      N mode              build s   mesh s  triangles  payload MB  defs error
   1000 per-definition       0.82    0.031         12        0.15     1
   1000 per-solid            4.43    3.677      12000        0.65     0
   4096 per-definition       3.52    0.112         12        0.62     1
   4096 per-solid           20.46   17.086      49152        2.79     0
  10000 per-definition       9.19    0.278         12        1.52     1
  10000 per-solid           48.74   40.366     120000        6.91     0
  30000 per-definition      25.78    0.730         12        4.60     1
  30000 per-solid          154.18  129.127     360000       21.00     0
```

## How the conclusions follow

- **Tessellation.** Per solid, the mesh phase is about 4.3 ms a copy at every size (3.7 s / 1,000, 129 s / 30,000).
  Per definition it is one box's tessellation plus writing each instance, 24 µs a copy at 30k.
- **Payload.** Per solid, 12 triangles and 24 vertices per copy in JSON: 700 bytes each. Per definition, one small
  mesh plus ~150 bytes per instance.
- **The rest of the build** (26 s at 30k) is shapes, assembly and the interference check, which K1, K2 and K2b and
  V1 own; the mesh is no longer a meaningful part of it.

## Caveats

- One definition, no features. A real car has hundreds of definitions and some feature-modified parts, which are
  still meshed one by one in assembly coordinates.
- The deflection is the assembly's, so a larger assembly tessellates each definition more coarsely; on this grid a
  box is 12 triangles at any deflection.
- One laptop, one run each, Windows rather than the production arm64 Linux image.
- The JSON is measured, not the HTTP response: the endpoint adds a few small fields and compresses nothing.

## Re-running

```bash
.cadvenv/bin/python docs/spikes/2026-09-15-mesh-per-definition/measure.py [N ...]
```
