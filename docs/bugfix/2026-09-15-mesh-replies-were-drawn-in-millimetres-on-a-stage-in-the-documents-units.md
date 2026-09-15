# Mesh replies were drawn in millimetres on a stage in the document's units

**Found:** 2026-09-15, checking what a design that is not in millimetres does through the lazy subtree path
(Phase 6, W2 — [`docs/spikes/2026-09-15-subtree-loading`](../spikes/2026-09-15-subtree-loading/README.md) left it
unchecked). Found by reading, then **confirmed in node through the shipped `forge3d.js`** by the fence below before the
fix: every drawn box of an inch design loaded with its mesh reply was 25.4 times its primitive's, and a metre design's
1,000 times.
**Severity:** medium, visible. Any design in cm, m or inches, once the kernel (or, since W2, the Go tessellator) answered
for it, was drawn at the wrong scale relative to everything else on its stage. Nothing was written wrong: exports,
STEP files, mass properties and the stored document are untouched.
**Owner:** the browser renderer (`forge3d.js drawBatches`), shared by the whole mesh (`Studio.load`) and a subtree's
(`Studio.addSubtree`).

## Symptom

A design stated in inches, drawn by the workbench:

| path | what the stage showed |
|---|---|
| whole mesh (`refineWithBuiltSolid` → `studio.load(proto, reply)`) | the built parts 25.4× their authored size and distance from the origin; a cut tool's ghost (no mesh, so its primitive) and every dimension overlay at authored size, so a hole's ghost sat far from its hole and dimensions floated at 1/25.4 of the model |
| a subtree at a time (`loadLazy` → `addSubtree`) | each subtree 25.4× the translucent box that stood for it, landing outside it; the other slots' boxes, and framing computed from them, at authored size |

A design in millimetres was drawn correctly in both, which is why nothing showed: every workbench and viewport fixture
was in millimetres.

## Root cause

Every mesh reply is in millimetres by contract: `geometry.SolidsAndOperations` converts the document to millimetres
before the kernel builds it, and `geometry.TessellateInstances` states its reply in millimetres "as the kernel's reply
is, so the browser draws both the same way". The browser's stage is in the document's unit: `placementMatrix` places a
primitive by its authored position, overlays carry authored coordinates, and `loadLazy` computes its placeholder boxes
from primitives. `drawBatches` used a reply's definition vertices and instance matrices as they arrived. Before Phase 6,
W1 the workbench expanded the reply onto the parts and drew those the same way (checked in `b03c52d^`): this was never
converted, so it is not a regression of W1 or W2.

**Classification:** implementation defect — the third of its class after
[`2026-09-13-feature-radii-were-sent-in-the-documents-units.md`](2026-09-13-feature-radii-were-sent-in-the-documents-units.md):
a length that crossed between the document's unit and millimetres without going through the conversion. **Why it was not
caught:** no renderer fence loaded a design in anything but millimetres with a mesh reply.

## Fix

- `drawBatches` takes `opts.toMM` (`unitToMM` of the document's units) and puts a reply in the document's unit once:
  an instance's translation is divided by it, and `kernelGeometry` scales the definition's (or a placed part's) vertices
  by the same factor. `Studio.load` and `Studio.addSubtree` both pass it, so the two paths cannot disagree.
- A design with no convertible unit gets no reply at all (the mesh endpoint refuses it, both whole and subtree), so the
  factor is 1 only where nothing is scaled.
- Nothing on the server changes: the reply stays in millimetres, which is what `cad.Build` and the Go tessellator say.

## Regression fences

| fence | runs where | holds |
|---|---|---|
| `TestMeshSubtree_ADesignInInchesOrMetresIsDrawnWhereItsPrimitivesAre` | CI (node) | for an inch, a metre and a millimetre design: every part drawn from the whole reply, and from each subtree reply, occupies its primitive's box; each subtree lands inside the box that stood for it |

Drill under "Added 2026-09-15 (kernel build ceiling and viewport follow-ups)" in `scripts/drill-fences.sh`:
"a mesh reply is drawn in millimetres on a stage in the document's unit" → red.
