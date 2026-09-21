# What a manufacturability pass costs, and what a strength check would take

2026-09-20. Measurements behind the check added for issue 6 (the kernel builds
geometry but never evaluates it). The budget in `sidecar.py`
(`_MANUFACTURABILITY_BUDGET`) is chosen from the numbers below.

## How it was measured

`TestCost_Manufacturability` in `internal/domain/cad/mfg_cost_kernel_test.go`,
run with `FORGE_MFG_COST=1`. Each row builds one document THREE times, back to
back: plain, with the pass, plain again. Interleaved deliberately — this laptop
runs other agents' builds in parallel, and a single before/after pair here would
be a measurement of the machine's mood. The plain runs on either side of each
checked one are reported so a reader can see the drift.

Machine: Windows 11, the repo's pinned kernel venv (build123d 0.11.1, OCCT via
OCP). Other FORGE work was running throughout; the plain-build columns show how
much that moved.

## The numbers

| parts | shapes | manufacturability | faces measured | reused | plain shapes phase (before / after) |
|---|---|---|---|---|---|
| 512 | one shape, 512 copies | **32 ms** | 6 | 511 of 512 | 26.8 ms / 23.3 ms |
| 512 | 512 distinct shapes | **10.67 s** | 3,072 | 0 of 512 | 500 ms / 408 ms |
| 4,096 | one shape, 4,096 copies | **145 ms** | 6 | 4,095 of 4,096 | 114 ms / 223 ms |
| 4,096 | 4,096 distinct shapes | **did not finish** | — | — | — |

The 4,096-distinct case ran past the kernel's own 30 s build limit and was
stopped. Extrapolating the measured per-face rate it would be about 85 s.

So the cost is **per distinct face, at about 3.5 ms**, and it is flat in the
number of occurrences. That is the shape FORGE's own models have: a definition
built once and placed many times. The check is close to free on the models the
plan is about (Phase 4, stage K1: each distinct shape built once) and expensive
only on a model where every part is a different shape.

### Which rule costs what

Profiled separately over 200 boxes (1,200 faces, 2,400 edges), same kernel:

| step | time | share |
|---|---|---|
| concave-edge test (`ChFi3d::DefineConnectType` per edge) | 6.92 s | 76% |
| edge feature sizes (`BRepAdaptor_Curve` per edge) | 1.26 s | 14% |
| wall rays (`BRepIntCurveSurface_Inter` per face) | 0.47 s | 5% |
| cylindrical-face test | 0.41 s | 5% |
| 3x3 normal samples per face | 0.23 s | 3% |

The expensive rule is the one about sharp internal corners, and it is expensive
because it asks OCCT's own fillet builder about **every edge**. The wall ray —
the measurement that looked most alarming before it was taken — is 5% of it.

Two cheaper intersectors were measured and rejected:
`IntCurvesFace_ShapeIntersector` with `Load` + `Perform` is 1.6x faster than
`BRepIntCurveSurface_Inter.Init` per face, but **only when a fresh intersector is
built per solid**; reused across solids it silently answered from the previously
loaded shape (a 10.199 mm box measured 0.0995 mm). With a fresh one per solid the
saving is 0.1 s of a 9 s profile, so the simpler call was kept.

## The budget, and the note when it binds

`_MANUFACTURABILITY_BUDGET = 600` faces — about 2.1 s at the measured rate, a
fourteenth of the kernel's 30 s build limit and the same order as the
interference check's own worst case. A hundred distinct six-faced parts fit
inside it; any number of COPIES of them does.

It is a chosen bound from a measured rate, not an optimum: no model was measured
being read with and without a truncated check.

When it binds, the reply carries `manufacturability_truncated`, how many parts
there were and how many were measured, and the turn says:

> FORGE measured 100 of 101 part(s) for manufacturability and stopped there, so
> the rest are not known to be makeable.

Fenced by `TestKernel_TheFaceBudgetStopsTheMeasurementAndSaysSo` (through the real
kernel, 101 distinct parts) and `TestManufacturability_ATruncatedCheckSaysSoInTheTurn`.

## Strength: what was measured, and what a real check would need

Issue 6 asks for a stress check. This branch does not build one, on purpose.

**What is here** is the part of a beam calculation that is pure geometry and
exact: for a plane cut somebody names, the area, the centroid, the second moments
of area about the section's own centroid, the extreme fibre distances and the
section moduli. Checked against `b*h^3/12` on a rectangle
(`TestKernel_ANamedSectionIsMeasuredAgainstTheRectangleFormula`): a 20 x 10 mm
section measures 200 mm², 1666.67 mm⁴ and 6666.67 mm⁴, to 1e-3.

**What a real stress check would need**, none of which FORGE has:

1. **A load.** Magnitude, direction, where it is applied, and whether it is
   static, cyclic or an impact. Nothing in a FORGE document says what a part is
   for, let alone what pushes on it.
2. **Boundary conditions.** Which faces are held, and how — bolted, welded,
   resting, free. An assembly's parts are placed, not constrained: there are no
   joints in the document, which is the same gap `assembly.go` names as "no
   kinematics".
3. **Material properties beyond density.** Young's modulus, Poisson's ratio, and
   a yield or ultimate strength with a temperature and a condition. `Material`
   carries a name, a finish and a density; a modulus for "aluminium" without a
   temper is not a number anybody should compute a margin from.
4. **A mesh and a solver.** A tetrahedral mesh with convergence checked (a single
   mesh's answer is not a result), and a solver. OCCT has no FEA; this would be a
   new dependency — Calculix, Code_Aster or similar — with its own runtime, its
   own failure modes and its own ceiling at part counts this plan cares about.
5. **A way to say the answer is provisional.** A stress number is the most
   dangerous value this product could emit. The contract already refuses to
   invent a tolerance for exactly this reason: "a tolerance is read as an
   instruction to a machinist". A von Mises stress is read as permission to build.

Items 1–3 are not solver problems; they are **vocabulary** problems. Until a
document can say what holds a part and what pushes on it, an FEA here would be a
precise answer to a question nobody asked. Section properties need none of them,
which is why they are what this branch measures — and why the turn's note says,
every time, that they are geometry and not a stress.
