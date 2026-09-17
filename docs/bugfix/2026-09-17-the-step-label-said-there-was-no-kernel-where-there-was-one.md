# The STEP label said there was no kernel where there was one

**Date:** 2026-09-17 · **Status:** fixed on `goals/unverified-paths` · **Severity:** high for the workbench — in a deployment with a CAD kernel its Export STEP button never offered a file

## Summary

`GET /v1/geometry/{id}/export/label?format=step` called `geometry.LabelFor`, which resolves the format from the static
table, where STEP is unavailable. It never asked whether this deployment has a kernel. `GET /v1/geometry/formats`
(`geometry.Formats(hasKernel)`) and `GET .../export?format=step` (`exportParametric`) both do.

## Symptom

Asking every export route as a viewer through the real `forged`, which has a kernel
(`docs/spikes/2026-09-17-unverified-paths`, scenario `authz-http`):

| request | answer |
|---|---|
| `GET /v1/geometry/formats` | step `available: true`, note "This deployment has a CAD kernel" |
| `GET .../export?format=step` (4,096 parts) | 200, the STEP file |
| `GET .../export/label?format=step` | **501** CONNECTOR_UNAVAILABLE "This deployment has no CAD kernel configured" |

## Impact

The workbench fetches the label BEFORE it shows a download link (`showExportLabel`, workbench.js: "a label a person
reads on their way out of the page is a label they have already acted without"). So in a deployment with a kernel the
Export STEP button is enabled, and pressing it shows the 501 refusal and no link. Present since the parametric wave
(b33ac95) added the kernel path to `Export` and `Formats` but not to the label.

## Fix

`ExportLabel` answers STEP with `geometry.KernelLabelFor` when the deployment has a kernel: the same unit and size
refusals as the mesh label, no tessellation and no triangles, a headline that says B-Rep, and losses that are STEP's
(millimetres always, as `_step_document` writes; no colour; names kept but notes, assumptions and the unverified list
not in the file; an unbuildable part left out and named in the download's header). Without a kernel the 501 stands.

No workbench change: the page renders whatever label the server returns.

## Verification

`TestAPI_TheSTEPLabelIsTheKernelsWhereThereIsAKernelAndARefusalWhereThereIsNone` — with a kernel: 200, `format_kind`
parametric, B-Rep headline, no tessellation, 0 triangles, millimetres named, no mesh losses, the unverified list kept;
without: 501.

## Regression prevention

Drills "the STEP label ignores the kernel" and "the STEP label's headline calls a B-Rep tessellated", seen red.
