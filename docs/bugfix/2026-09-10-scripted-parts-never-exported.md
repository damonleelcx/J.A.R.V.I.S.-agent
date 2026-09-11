# Every scripted part was left out of every export

**Found:** 2026-09-10, while measuring the new `"gear"` shape against the script
path with a live A/B (`TestLiveGearMeasure`).
**Severity:** high on the scripted path — on any deployment with scripts on, a
part the turn said was built was absent from the STEP file and from the
viewport's built mesh, silently in the common case.
**Owner:** CAD kernel sidecar (`internal/domain/cad/sidecar.py`), introduced by
c92d775 (#43). Secondary: `cad.go`, whose error hid the cause.

## Symptom

In ten live gear requests on the pre-gear tree, the script path behaved like this:

| stage | result |
|---|---|
| the model wrote a script | ✅ 10 of 10 |
| the turn's own check (`RunScript`) built it, repairing where needed | ✅ 6 of 10 — the turn said *"it builds now"* |
| `BuildDocument` / `BuildMesh` built the document containing it | ❌ **0 of 10** |

Every one of those six failed with *"the CAD kernel could not build this
assembly: no part could be built"* — a sentence that names no part and no reason.

## Root cause

`geometry.Solids` turns a scripted part into a solid of shape `"step"`, and
`cad.Kernel.BuildDocument` runs the script and fills in its STEP. The sidecar then
builds each solid through `_shape(solid)` — and `_shape` had **no `"step"` case**.
It raised `unsupported shape 'step'`.

The import that was meant to handle it existed. It had been written into
`_apply(op, shapes)` — the FEATURE function, which applies cuts, fuses and lofts —
directly after the `return` of the cut/fuse branch. There, `kind` is an
operation name that is never `"step"`, and `solid` is not even defined. It was
dead code from the day it landed.

Reproduced outside every harness: a script that builds a valid 7344.95 mm³ solid,
whose STEP round-trips through build123d unchanged, is refused by the sidecar's
own `_build` with `skipped: ["Gear Body: unsupported shape 'step'"]`.

**Classification:** an implementation defect that landed with the feature (#43),
not a later regression.

## Why nothing caught it

- **Every script test stopped at `RunScript`.** The sandbox, the manifest, the
  refusals, the name suggestions and the repair loop were all tested thoroughly —
  every one of them proves that a script *runs*. No test built a document
  containing a scripted part through `BuildDocument` or `BuildMesh`, which is the
  only path a person downloads or looks at.
- **The turn's own check is `RunScript` too** (`scriptrepair.go`), so the turn
  truthfully reported that the script built, and the reply said so.
- **Every earlier gear rate was measured at `RunScript`** ("six gears in ten"), so
  the rate was always a "the script ran" number, never "the part reached the
  file".
- **`cad.go` dropped the sidecar's reasons.** The sidecar returns `skipped` with
  every refused part and why. When nothing at all was built, `BuildDocument`
  returned only `res.Error` — "no part could be built" — so the one sentence that
  named the defect reached nobody.

## Impact

- **STEP export:** the scripted part is missing. With other parts beside it the
  export succeeds without it and the part is listed only in `Skipped`; alone, the
  export fails with a message that says nothing.
- **Viewport built mesh** (`GET /v1/geometry/{id}/mesh` → `BuildMesh`): the same.
- **The picture `look()` judges** (`agent/render.go` → `httpapi/kernelSolids` →
  `BuildMesh`), from reading the code:
  - scripted part alone — `BuildMesh` errors, the render falls back to the
    described one, and the scripted part is drawn as a block *with* the note that
    says so;
  - scripted part beside others — `BuildMesh` succeeds without it, the sheet is
    marked `FromKernel`, so **no note** is added and the vision check is shown an
    assembly with the part simply gone.

## Fix

- `sidecar.py`: the `"step"` import moved into `_shape`, with its Why comment and
  the reason it lives there; the dead copy in `_apply` deleted.
- `cad.go`: a refused assembly's error now carries the sidecar's per-part
  reasons — *"no part could be built — Sliver: …"*.

Nothing else changed: no prompt, no contract, no Go dispatch.

## Regression tests

- `internal/domain/cad/scripted_export_test.go`
  - `TestKernel_AScriptedPartIsExportedAndMeshed` — a scripted block beside a box
    and on its own, through `BuildDocument` (STEP) and `BuildMesh`: every part
    present, volume and bounds exact, a mesh surface for the scripted part.
    Red before the fix (`skipped [Scripted Block: unsupported shape 'step']`).
  - `TestKernel_ARefusedAssemblyNamesWhatItRefused` — a zero-width box the kernel
    refuses; the error must name it. Red before the fix.
- Drills in `scripts/drill-fences.sh`, section "The kernel":
  *"a scripted part is unreachable in the shape dispatch"* and *"a refused
  assembly hides which part it refused"*.

## Lessons

- A feature's test has to run on the path its USER takes, not the path its author
  built first. The script path was tested to its own boundary and not one step
  past it.
- A rate is only as good as the point it is measured at. Measure where the person
  downloads, not where the code first succeeds.
- An error that drops the reasons it was given converts a one-line diagnosis into
  a day of searching.

## Related

- `docs/plan-2026-09-09-complex-prototypes.md`, Stage 11 — the A/B that found it.
- `docs/bugfix/2026-09-09-the-scripts-error-never-reached-the-model.md` — why the
  turn runs scripts at all.
