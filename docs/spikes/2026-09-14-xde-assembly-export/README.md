# Spike: exporting many occurrences as an XDE assembly (K2)

**Date:** 2026-09-14 · **Status:** done · **Stage:** Phase 4, K2 of [`docs/plan-2026-09-13-millions-of-parts.md`](../../plan-2026-09-13-millions-of-parts.md)

## Summary

- **Assembling N located copies with `Compound(children=…)` is quadratic.** At 10,000 occurrences it takes **13.8 s**.
  An XDE assembly of the same copies (one shape label, N components with their own location and name) takes
  **0.26 s**, and it grows linearly: 0.03 s at 1,000, 0.11 s at 4,096, 0.26 s at 10,000.
- **The file is the same either way.** Both write **1** solid B-rep, **2** products (the assembly and the definition)
  and **N** instances. Re-imported, both give N components that refer to **1** shape, keep every occurrence's name, and
  add up to the exact volume.
- **So K2's gain is time, not file structure.** The plan's "re-import and count one product definition" acceptance is
  already true before K2, because build123d's located copies share one TShape and XCAF recognises that. A fence that
  only counts products therefore cannot tell the two paths apart. K2 needs a fence on the time spent assembling.

## Why this spike

K1 found that 64% of a cached 10,000-part build is spent in build123d's `Compound(children=…)`
([K1 spike](../2026-09-14-definition-cache/README.md)). build123d re-builds the whole `TopoDS_Compound` every time a
child is attached (`Compound._post_attach` → `_make_topods_compound_from_shapes`), so N children cost about N²/2 copies.
The plan says K2 exports "via `STEPCAFControl_Writer`, never `Compound(children=…)` for occurrences". Before changing
the sidecar, this measures whether an XDE assembly really removes the quadratic step, and whether the STEP file it
writes carries the same information: solids, instances, names and volume.

## Method

`measure.py` places N copies of one 10 mm box on a 30 mm grid (no two touch) and writes a STEP file two ways:

- **compound**: the sidecar before K2. `Compound(children=copies)`, each child labelled `box-i`, written by
  build123d's `export_step`.
- **xde**: a fresh XCAF document. The box is added once with `AddShape(shape, makeAssembly=False)`. An empty assembly
  label gets one `AddComponent(assembly, definition, location)` per occurrence, each named `box-i` with
  `TDataStd_Name`. It is written by `STEPCAFControl_Writer` with name mode on.

Each file is then checked two independent ways:

- **the STEP text:** counts of `MANIFOLD_SOLID_BREP`, `PRODUCT` and `NEXT_ASSEMBLY_USAGE_OCCURRENCE` entities;
- **a re-import:** `STEPCAFControl_Reader` into a new XCAF document. It counts the components under the top assembly,
  the distinct shape labels they refer to, and the components whose name is one of `box-1 … box-N`, and sums the volume
  of the located components.

"assemble s" is the time from the located copies to a finished assembly (a `Compound`, or an XCAF document). "write s" is
the STEP writer alone.

Environment: M-series laptop, macOS, Python 3.14.6, build123d 0.11.1, cadquery-ocp-novtk 7.9.3.1.1. One run per row;
no variance figure. Measured with no other build running.

## Raw results

```
mode          N assemble s  write s     bytes  breps products instances components   shapes names kept volume exact
compound   1000       0.21     0.03    598802      1        2      1000       1000        1       1000 True
xde        1000       0.03     0.01    599906      1        2      1000       1000        1       1000 True
compound   4096       2.50     0.14   2502587      1        2      4096       4096        1       4096 True
xde        4096       0.11     0.07   2502777      1        2      4096       4096        1       4096 True
compound  10000      13.82     0.38   6147742      1        2     10000      10000        1      10000 True
xde       10000       0.26     0.22   6147737      1        2     10000      10000        1      10000 True
```

## How the conclusions follow

- **Quadratic vs linear.** From 1,000 to 10,000 occurrences (10×), compound assembly rose 66× (0.21 → 13.82 s), which
  is the square law. XDE assembly rose 8.7× (0.03 → 0.26 s), which is linear within the resolution of a 0.03 s reading.
- **Same information.** The breps, products, instances, components, shapes, names and volume columns are equal row by
  row. File sizes differ by under 0.2%, from header and naming bytes.
- **Writing was never the problem.** The STEP writer takes 0.38 s or less on both paths at 10,000 occurrences.

## Caveats

- One definition only. A real document has many definitions plus occurrences that features made unique; each of
  those becomes its own shape label, which is still one `AddShape` apiece.
- Colours and layers were not tested. The sidecar sets neither today.
- The interference check, the other quadratic step in a large build, is not touched here.
- One laptop, one run each.

## Re-running

```bash
.cadvenv/bin/python docs/spikes/2026-09-14-xde-assembly-export/measure.py [N ...]
```
