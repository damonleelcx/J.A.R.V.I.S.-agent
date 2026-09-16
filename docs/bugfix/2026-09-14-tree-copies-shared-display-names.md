# Parts a tree placed could share a display name

**Date:** 2026-09-14 · **Status:** fixed · **Area:** `internal/domain/geometry/tree.go`, `internal/httpapi/assets/forge3d.js`

## Summary

A document tree (Phase 1, stages D1a–D1c of `docs/plan-2026-09-13-millions-of-parts.md`) could give several
parts the same name while their ids stayed distinct. Two unnamed children placing a repeated "Ell" wrote
"Ell 1", "Ell 2", "Ell 3" twice, and every occurrence of a wheel sub-assembly wrote "Hub". The Studio Parts panel
(which shows `name || id`) and the STEP file's product and instance names both showed the duplicates.

A tree part is now named by its **occurrence path**: every child above it (its name, or its id when it has none,
with its pattern number), then its own name, joined with `" / "`. Examples: "left / Ell 1", "row 2 / Pin",
"Front wheel / hub / Hub".

## Symptom

Found while writing the K2 STEP re-import test (`internal/domain/cad/assembly_export_kernel_test.go`, branch
`kernel/xde-assembly`). A rack with children `left` and `right` placing the definition `ell` (Name "Ell", repeat ×3)
exported six instances named "Ell 1", "Ell 2", "Ell 3", "Ell 1", "Ell 2", "Ell 3".

Verified before changing code by expanding six trees and listing each part's `Label()`:

| tree | names before | duplicate |
|---|---|---|
| unnamed children `left`, `right` of repeated "Ell" | Ell 1, Ell 2, Ell 3, Ell 1, Ell 2, Ell 3 | yes |
| unnamed children `a`, `b` of plain "Pin" | Pin, Pin | yes |
| unnamed patterned child `row` ×3 of "Pin" | Pin, Pin, Pin | yes |
| "Front wheel" and "Rear wheel" placing an assembly whose child `hub` places "Hub" | Hub, Hub | yes |
| named patterned child "Row" ×3 | Row 1, Row 2, Row 3 | no |
| named children "Left", "Right" of repeated "Ell" | Left 1…3, Right 1…3 | no |

## Impact

- **Who sees it:** anyone viewing or exporting a tree that places the same definition more than once without naming
  every child, or that places a sub-assembly more than once. That is the common case the tree exists for.
- **What goes wrong:** the Parts panel lists indistinguishable entries, and a STEP file opened in another CAD tool
  shows several parts with the same name. Selection and features were not affected, because they use ids.
- **Severity:** moderate. No geometry was wrong, but the names a person uses to tell parts apart could not do it.

## Root cause

Tree expansion (`expandAssemblies` in `tree.go`, mirrored in `forge3d.js`) built a part's **id** from its full
occurrence path (`front/hub`, `left-1`), but gave it a **name** only when the child placing it had a `Name`, and
only from that one child:

```go
childName := c.Name                      // "" for an unnamed child
if childName != "" { q.Name = childName ... }  // otherwise the definition's own name stays
```

So:
- an unnamed child's parts kept the definition's names;
- an unnamed pattern dropped its copy number;
- a sub-assembly's parts kept their inner names in every occurrence, whatever the outer child was called.

**Owner:** the tree stages D1a–D1c. This is a design gap, not a regression: ids and names were derived from
different information, and nothing tested names across occurrences. Flat documents never had it, because a flat
repeat's names come with its copy numbers and the person names the rest.

## Fix

**Decision (damon, 2026-09-14):** name tree parts by the occurrence path, strict. Every level appears, even when a
child's id repeats the part's name ("Front wheel / hub / Hub").

Rejected alternatives:
- **Fall back to the child id but keep "a named child replaces the part's name".** It puts raw ids in names and
  loses the definition's name under a named child.
- **Add a suffix only when names collide.** A name would depend on the whole document, so adding a second wheel
  would rename the first wheel's hub.
- **Drop a level whose id repeats the part's name.** Shorter, but it is an exception that Go and the browser would
  both have to keep identical.

**How:**
- `walk` carries `names` beside `path`. Each child adds `name-or-id [pattern number]`.
- A placed part is named `strings.Join(names + [its own label], NameSeparator)`.
- The browser does the same with `NAME_SEPARATOR`.
- The Why comment is at `NameSeparator` in `tree.go`.

Effects to know:
- **Names change for trees only.** A named child's parts go from "Left 1" to "Left / Ell 1". Flat documents are
  unchanged.
- **Messages that name a part by its label now carry the path.** A validation error says `part "slot / blank"
  has no shape`, which names both the occurrence and the definition.
- **Uniqueness holds whenever sibling labels are distinct.** Sibling ids always are. Two siblings given the same
  `Name` still produce the same names, because that is what the person wrote.

## Regression fences

- `internal/domain/geometry/tree_names_test.go`: `TestTree_EveryPartItPlacesIsNamedByItsOccurrence` checks exact
  names, and that no two are equal, for the six trees above.
- `internal/httpapi/tree_fence_test.go`: `TestRendererFlattensATreeLikeTheExporter` compares every label the
  browser draws with Go's. Its fixtures include unnamed children, sub-assemblies placed twice, patterns, repeats and
  a named grid, so the browser cannot keep the old names.
- Updated expectations: `TestPattern_CopiesOfANamedChildAreNumbered`, `TestTree_ARepeatInsideADefinitionTurnsWithThePlacement`,
  `TestTree_TheStorageDoorChecksThePartsATreePlaces`.
- Drills in `scripts/drill-fences.sh`, section "A tree part is named by its occurrence path":
  - Go names a part by its own label only;
  - Go drops the id of an unnamed child;
  - the browser drops the path.

## Related

- [`2026-09-14-a-repeat-copy-could-take-another-parts-id.md`](2026-09-14-a-repeat-copy-could-take-another-parts-id.md):
  the id side of repeat copies.
- `docs/plan-2026-09-13-millions-of-parts.md`, Phase 1 (document tree) and Phase 4 K2 (STEP names).
