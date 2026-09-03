# Exported meshes were inside out, and every viewer said they were fine

**Date:** 2026-09-02 · **Phase:** wave 7 (visual completeness, PRD VIS-05) ·
**Severity:** high — every exported file was affected, and nothing reported it ·
**Owner:** geometry

## Symptom

The first bracket exported by the new OBJ writer looked correct in every viewer
and was structurally wrong. Computing the signed volume of each group in the
exported file:

```
base_plate     signed volume mm^3 = 18000.000
pilot_boss     signed volume mm^3 = -2271.429     <-- negative
stiffening_rib signed volume mm^3 = 4.000
```

A closed mesh whose facets face outward has a positive signed volume. The
cylinder's magnitude was right — π·11²·6 = 2280 mm³, and 2271.4 is the 40-sided
tessellation of it — and its **sign was not**. Boxes were wound outward and
cylinders and spheres inward, in the same file.

## Why nothing caught it

Mesh consumers decide which side of a facet is *outside* from its winding order.
The renderer does not. `forge3d.js` is handed each normal explicitly and draws
with back-face culling off, so a face wound the wrong way is lit correctly and
looks perfectly right on screen. The exporter mirrored the renderer's primitives
term by term — which was the correct instinct, and it faithfully mirrored a
property the renderer never depended on.

So: correct in the viewport, correct in every mesh viewer that also ignores
winding, and wrong at the only point that matters — a slicer or a CAM tool
deciding which side is material. The failure had no symptom until somebody tried
to make the part.

This is the same shape as the defect that produced the "Drawn approximately"
banner: the interface asserting something the system had not done. Here the
assertion was quieter, because the file itself looked like geometry.

## Root cause

`cylinder()` and `sphere()` in `internal/domain/geometry/mesh.go` emit their
triangles in the vertex order `forge3d.js` uses, which happens to be
clockwise-from-outside. `box()` and `plane()` are counter-clockwise. Neither was
wrong in the renderer; only one of them is right in a file.

## The fix, and why it is not per-primitive

The obvious repair — flip the winding in `cylinder()` and `sphere()` — is the
same mistake made available six more times, once per primitive, plus once more
for every primitive added later.

Every facet is now oriented against **its own normal**, which is analytically
outward in all of them (a box face's declared normal, a cylinder wall's
`normalise([cos, slope, sin])`, a sphere point's own position). `orient()` swaps
two corners when the winding disagrees with the normal, and it runs in
`appendNonDegenerate`, which every primitive already goes through. The property
becomes structural rather than a detail somebody has to remember.

Degenerate facets are left alone: a zero-radius cone wall has no meaningful
normal to disagree with, and swapping would be a coin toss. They are dropped
anyway.

The renderer was deliberately **not** changed. Its winding is invisible to it,
the positions it draws are identical, and a change there would risk a visual
regression for no benefit. The two files agree on what matters — the segment
counts, fenced separately — and the difference in winding is stated in the
comment on `appendNonDegenerate`.

## Acceptance

Measured on the artefact, not read from the source. Exported the same variant
again and recomputed:

```
base_plate     signed volume mm^3 = 18000.000
pilot_boss     signed volume mm^3 = 2271.429
stiffening_rib signed volume mm^3 = 4.000
total (STL)    signed volume mm^3 = 20275.429
```

## Fences

- `TestExport_EverySolidIsWoundOutward` — computes each exported group's signed
  volume and fails on anything ≤ 0. Planes are excluded: two triangles enclose
  nothing, and a plane is labelled as unmakeable already.
- `TestExport_TessellatedVolumeIsCloseToTheRealSolid` — a mesh can be wound
  correctly and built wrongly. Each primitive's tessellated volume must be
  within 2% of the analytic solid **and never larger than it**, because an
  inscribed tessellation is always smaller. Too small catches a missing cap;
  larger than analytic means the facets are not chords at all.

Both drilled: removing the `orient()` call turns the first red.

## What to take from it

Reading the primitives did not find this and would not have. The number that
found it took four lines and came from the file that would actually be handed to
a machine. When the output of a system is an artefact, measure the artefact.
