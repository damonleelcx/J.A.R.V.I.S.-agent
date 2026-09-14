# Repeat copies were invisible to most readers, including the viewport

**Found:** 2026-09-13, while mapping every reader of `geometry.Document` for Phase 1 of
[`plan-2026-09-13-millions-of-parts.md`](../plan-2026-09-13-millions-of-parts.md) (stage D1a).
Each defect below was **confirmed failing before any fix** by the fence that now holds it.
**Severity:** high for the viewport (the person saw one spoke of sixty), medium for the rest (wrong
overall size, grey parts in the picture the vision check reads, unaddressable copies, an unnamed part
in a repair prompt).
**Owner:** the geometry reading layer — expansion was a private step of three readers, and every
other reader, including the browser's copy of the geometry, read the parts as written.

## Symptom

A part with `repeat` is written out as copies `spoke-1 … spoke-N` by the kernel, the mesh and `Faults`.
Every other reader saw only `spoke`:

| reader | before (confirmed) | after |
|---|---|---|
| browser (`forge3d.js`) | no repeat expansion at all — a pattern drawn **once** | every copy drawn, placed and turned exactly as the exporter does |
| `Measure` | five 10 mm boxes 100 mm apart measured **10.000 mm** wide | 410 mm |
| contact sheet colours | copy `block-3` drawn **grey** `[0.7 0.7 0.72]` ("no such part") | its part's colour |
| assembly states | hiding `block-3` refused as "not a part of this assembly" | a state may name the pattern or one copy |
| resize check (`turned.go`) | copies reported as **`" is now 40 x 10 x 10 mm…"`** — no name | `"Block 1 is now …"` |
| kernel mesh join (workbench) | meshes joined to authored ids, so no copy ever got its built solid | joined by the drawn id |

## Root cause

`expandGears` → `expandRepeats` were called privately inside `Solids`, `Tessellate` and `Faults`. No
exported way to read the expanded document existed, so every reader outside those three read the
authored list and then compared it with ids only the expanded readers produce. The browser holds its
own copy of the geometry (gear, arcs, retired words) and had never been given one of `repeat.go`.

**Classification:** design gap, present since `repeat` shipped (2026-09-09). **Why it was not caught:**
no test outside the geometry package built a document with a `repeat` in it, and no browser fence
compared a repeated part with the exporter.

## Fix

- `Document.Expanded()` (`geometry/expanded.go`): the same two expansions, exported for readers outside
  the package. `Solids`, `Tessellate` and `Faults` keep their own calls, because each is held by a
  drill that proves *it* expands.
- `bounds` expands **repeats only**: expanding gears too would measure a gear's faceted outline
  (±21.989) instead of its exact tip circle (±22) — caught by the existing gear fences on the first try.
- `partColours` expands each authored part on its own, so every copy of a pattern shares its colour
  and no second copy of the `-N` naming rule exists.
- `ValidateStates` accepts copy ids as well as authored ids.
- `turned.go` labels from the expanded document.
- `forge3d.js`: `partsToDraw` ports `repeat.go` term for term (straight and circular placement,
  turning, partial sweeps, count < 2 drawn once, count > 512 dropped, cut/loft tools retargeted) and is
  the one list `Studio.load` draws. Drawn parts carry `repeatOf`, so states and selection that name a
  pattern reach every copy. Kernel meshes attach by the drawn id and travel on the wrapper, never
  written into the stored document.
- `workbench.js` joins kernel meshes by the drawn id.

## Regression fences

| fence | holds |
|---|---|
| `TestRendererExpandsARepeatLikeTheExporter` (node, 6 cases) | browser ids, names, positions, rotations and removed tools equal the exporter's |
| `TestMeasure_IncludesEveryCopyOfARepeatedPart` | overall size includes every copy |
| `TestContactSheet_ColoursACopyLikeItsPart` | a copy is its part's colour |
| `TestValidateStates_NamesACopyOrThePattern` | pattern and copy ids accepted; a copy that does not exist refused |
| `TestTurned_NamesACopyOfARepeatedPart` | a resized copy is named |
| `TestExpanded_WritesOutCopiesAndLeavesTheAuthoredDocumentAlone` | expansion does not change the stored document |

Existing fences that caught a mistake on the way: `TestAGearIsDrawnAndMeasuredAtItsOwnSize` and
`TestAGearFollowsItsParameters` (the gear tip circle), `TestRendererKnowsWhatMaterialIsBeingRemoved`.

Drills under "Every reader sees repeat copies" in `scripts/drill-fences.sh`, each proven red on
2026-09-13 with a mutation that **compiles** (a build failure also exits non-zero and would pass for a
working fence):

| drill | fence output |
|---|---|
| Measure reads the parts as written again | `five boxes 100 mm apart measure 10.000 mm wide, want 410` |
| copies are coloured from the authored list again | `copy block-3 is drawn in [0.7 0.7 0.72], want its part's [1 0 0]` |
| a state cannot name a copy again | `a state hiding "block-3" was refused` |
| a resized copy is reported without its name again | `a copy was reported without its name: " is now 40 x 10 x 10 mm…"` |
| the browser draws each repeated part once again | `the browser draws 1 parts, the exporter builds 4` |
| the browser turns a copy differently from the exporter | `spoke-2 rotation[1]: browser [10 0.8975979010256552 0], exporter [10 51.42857142857143 0]` |

A dry run of the whole drill suite after the change moved no anchors.

## Not fixed here

- **The browser places a kernel-built part twice** (the mesh is already in assembly coordinates and the
  draw transform applies position and rotation again). Confirmed separately; fixed in its own bugfix,
  stacked on this one. Copies now reach that same path, so both land together.
- `Compare` still matches variants by authored id. That is deliberate: it compares what was authored.
