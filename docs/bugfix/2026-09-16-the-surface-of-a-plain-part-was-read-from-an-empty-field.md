# The surface of a plain part was read from an empty field, so two kernel fences failed on every document without a feature

**Date:** 2026-09-16 · **Status:** fixed (found merging main into #85; the defect is main's) · **Severity:** medium — two fences red on `main`, and no product behaviour wrong

## Summary

Stage K4 (#75, `kernel/mesh-per-definition`) changed the shape of a mesh reply. An untouched copy of a shape built
once is now sent as a *definition plus a matrix*, and `Build.Mesh` holds only the parts a feature changed. Every part's
surface in assembly coordinates is `Build.WorldMeshes()`, whose own doc comment says it "is what Mesh held before stage
K4, for a reader that draws parts rather than instances".

Two tests were not moved to that accessor and kept reading `Build.Mesh`. For a document whose parts are all plain —
no cut, no fillet, nothing that changes a shape — every part is an instance, so `Mesh` is **empty** and the tests see
no surface at all.

## Symptom

On the `kernel` job (Linux, real build123d):

- `TestKernel_ReturnsTheSurfaceOfTheSolidItBuilt` — `cad_test.go:1904: two parts were built and 0 meshes came back`
- `TestKernel_AScriptedPartIsExportedAndMeshed`, both subtests — `scripted_export_test.go:94: the built mesh has no
  surface for the scripted part (skipped []). The viewport would draw the assembly without it.`

## Impact

Fences only. Nothing in production reads `Mesh` directly for this purpose: the viewport and the agent's checks go
through `WorldMeshes()` (`internal/agent/cadbridge`), and `mesh_per_definition_kernel_test.go` — added by #75, using
`WorldMeshes()` — passes. So the mesh contract itself is sound and no drawing or export was ever wrong. What was wrong
is that two of the fences that exist to catch a wrong drawing could not report on one.

## Preconditions

A document whose parts are all untouched copies — `plate()`, and the scripted-part documents. A document with a feature
puts the changed part in `Mesh`, which is why no other kernel test noticed.

## Root cause

`_mesh` in `internal/domain/cad/sidecar.py` splits the solids: those with a `placed` entry become
`mesh_definitions`/`mesh_instances`, the rest become `mesh`. Both tests asserted against `mesh` alone.

## Why it did not show up before

- **These two tests are on the known Windows-only failure list**, so no developer running the suite locally sees them
  either way.
- **The `kernel` job only reached `main` today**, from #61 (`ci/kernel-in-ci`). #75 landed before the job existed, so
  nothing ever ran these two tests against the new reply shape. `main` was already red the moment both were present:
  run 35076985557 on `96fe0ee` shows `check: SUCCESS`, `kernel: FAILURE` with exactly these three failures.

## Fix

Both tests read `Build.WorldMeshes()`. Every assertion is unchanged — two meshes for a two-part document, triangles
present, indices addressing real vertices, a surface for the scripted part. Only the field they read moved, to the one
that holds what they are about.

## Verification

The `kernel` job. It cannot be reproduced on Windows, where both tests fail for unrelated reasons.

## Not in this fix

- **This is `main`'s defect, not stage A1's.** It was found merging `origin/main` into #85 and is reproducible on
  `origin/main` alone; every file the two tests exercise is byte-identical between `main` and the merge. It is fixed
  here because #85 has to be green to merge, and it belongs on `main` in its own right.
- **No drill.** A drill mutates production code to prove a fence red; the defect here was in the fences themselves,
  and the mutation that would catch it — sending every part as an instance — is what the kernel already does.
