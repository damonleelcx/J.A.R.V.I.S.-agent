# Millions of parts: the checklist

**Status: research and checklist. Nothing here is decided or built.**
Written 2026-09-13, after the live car build (28 parts) and the interference
check. Builds on
[`research-2026-09-12-vehicles-aircraft-and-structures.md`](research-2026-09-12-vehicles-aircraft-and-structures.md)
and [`spikes/2026-09-12-car-ceiling/`](spikes/2026-09-12-car-ceiling/README.md).

**Asked for:** FORGE needs to build prototypes of millions of parts. How, as a
checklist.

---

## Summary

- **FORGE cannot get there by building more parts per step.** Authoring is
  about 30k tokens per step for a handful of parts. Authoring a million parts
  one at a time is billions of tokens.
- **Nobody builds millions of parts one at a time, including human engineers.**
  A 747 has about six million parts, half of them fasteners
  ([Boeing via Lufthansa](https://magazin.lufthansa.com/xx/en/fleet/boeing-747-8-en/one-plane-six-million-parts/)).
  Large-assembly CAD handles that with a product structure, instancing,
  lightweight representations and load-on-demand
  ([JT overview](https://cadexchanger.com/blog/3d-formats-overview-jt/),
  [large-assembly strategies](https://novedge.com/blogs/design-news/large-scale-assembly-performance-strategies-for-modern-cad-workflows)).
- **So the model writes STRUCTURE and RULES, and FORGE expands them.** This is
  the `"gear"` lesson (0/10 → 10/10) applied to the whole product: the model
  says what, FORGE draws how many.
- Every layer has to change: document, agent, storage, kernel, verification,
  viewport. Each has at least one hard limit below a thousand parts today.

---

## First: "millions of parts" is three different numbers

This has to be decided first, because each number breaks a different layer.

| number | example | what it stresses |
|---|---|---|
| **unique designs** | a 747 might have tens of thousands | authoring (model calls), kernel builds |
| **placed occurrences** | 6 million, including 3 million fasteners | storage, viewport, verification |
| **solids on screen at once** | whatever the camera sees | viewport memory and frame rate |

A realistic target is **millions of occurrences of thousands of designs**. A
million distinct, individually designed parts is not how any real product is
made, and it is not a sensible goal.

**Also decide:** which object first, and at what fidelity. A 30,000-part car
([~30k parts](https://knowhow.napaonline.com/how-many-parts-are-in-a-car/)) is a
very different milestone from a six-million-part aircraft.

---

## Where the current limits are (read from code, 2026-09-13)

| layer | limit today | where |
|---|---|---|
| document | flat `parts` list, each part with an absolute position; `repeat` ≤ 512 copies | `geometry/document.go`, `repeat.go:139` |
| agent | the **whole document** is sent to the model on every step; 2–10 step plan, max 12 | `agent/assemble.go:50, 208` |
| cost | ~30k tokens per step, measured | `spikes/2026-09-12-car-ceiling` |
| storage | the whole model is **one `jsonb` value** in one row | `platform/db/sql/0011_geometry.sql:108` |
| kernel | one Python process, requests serialised, 30 s timeout, everything built into one compound | `cad/cad.go:67,78`, `cad/sidecar.py` |
| mesh | 400k triangle budget | `cad/sidecar.py:553` |
| interference | all-pairs loop in Python, 2,000 boolean budget | `cad/sidecar.py` |
| viewport | one `drawElements` call per part, **no GPU instancing**, 16-bit indices by default | `httpapi/assets/forge3d.js:1868, 2037` |
| vision check | one four-view contact sheet for the whole model | `agent/look.go:16` |

At about 550 bytes per part, a million-part flat document would be ~550 MB of
JSON: too big for any prompt, and a single row Postgres has to read whole every
time.

---

## The checklist

Each phase closes on its own with an automatic acceptance test, per this
repository's rules. Phases are ordered by dependency.

### Phase 0 — Decisions (blocking)

- [ ] **Target**: which object, unique designs vs occurrences, and fidelity.
- [ ] **Milestone ladder**. Recommended: 1k occurrences → 30k (a full car) →
      1M (an airframe section or building) → 10M.
- [ ] **Document model change** (Phase 1) is a shipped contract. `careful-api-creation` applies.
- [ ] **New storage tables** (Phase 3). `careful-table-creation` applies, and
      forge.heros-agent.space is already live.
- [ ] **Deployment**: FORGE shares one k3s node with three other products.
      Million-part jobs need their own CPU/memory limits or a separate node.

### Phase 1 — Document: define once, place many times

**Why:** without this, part count is authoring cost. With it, the stored size
and the model's work scale with unique designs and rules, not occurrences.

- [ ] **Definition vs occurrence split.** A part is designed once and placed by
      reference. This is how a bill of materials works.
- [ ] **Assembly tree with nested frames.** A sub-assembly has its own
      coordinate frame and is placed as a whole. This also fixes the floating
      parts from the car run.
- [ ] **Mirror, pattern and array on sub-assemblies**, not just on solids.
      Patterns along a path, on a grid, on a surface.
- [ ] **Generators.** Rules that expand deterministically, like `gear.go`:
      "rivets every 25 mm along this seam", "a fastener at every joint of this
      type", "a rebar cage for this beam".
- [ ] **Hierarchical stable ids** (`fuselage/section-41/frame-12/rivet-340`) so
      edit, compare and audit keep working.
- [ ] **Interfaces/attachment points** on sub-assemblies, so children attach to
      named places instead of global coordinates.
- [ ] **Remove the 512 cap** for generated occurrences; bound by budget, not by
      viewport.

**Accept when:** one definition × 1,000,000 occurrences stores in
O(definitions + rules), expands to the same result every time, and survives an
edit round-trip.

### Phase 2 — Agent: the model writes structure, not parts

**Why:** at ~30k tokens per step, part-by-part authoring cannot scale.

- [ ] **Recursive decomposition**: system → subsystem → assembly → part,
      replacing the flat 2–10 step plan capped at 12.
- [ ] **Each step sees only its subtree** plus the interfaces it attaches to,
      never the whole model (today the whole document goes into every step).
- [ ] **Standard parts library** referenced by spec (fasteners, bearings,
      standard sections), not drawn. About half of a 747's parts are fasteners.
- [ ] **Generators preferred over enumeration** in the contract, with a
      deterministic check that enumerated repetition gets flagged.
- [ ] **Durable, resumable build jobs on `forge-worker`**, not one HTTP turn.
      Parallel subtrees, a per-job token budget, visible progress.
- [ ] **Token-per-unique-design measurement** (`make measure-*`) at each milestone.

**Accept when:** a build of N sub-assemblies runs in parallel within a fixed
token budget, survives a worker kill and resumes, and token cost grows with
unique designs, not occurrences.

### Phase 3 — Storage

**Why:** one `jsonb` row per model means every read and every edit touches the
whole model.

- [ ] **Store definitions, tree and generators separately**, content-addressed,
      so unchanged parts are shared across versions.
- [ ] **Occurrences are derived**, not stored, or cached as a rebuildable projection.
- [ ] **Versioning by structural diff**, not full copies.
- [ ] **A replacement for today's replay guarantee.** The migration comment says
      the document is stored whole "because a reconstructed one could differ
      from what the person actually saw". Splitting storage breaks that, so it
      needs a new guarantee, e.g. content hashes plus deterministic expansion.
- [ ] Idempotent, dialect-safe migrations for the new schema.

**Accept when:** a sub-tree loads without loading the model, a one-part edit
writes O(1) rows, and an old version replays byte-identical.

### Phase 4 — Kernel

**Why:** one serialised process with a 30 s timeout cannot build more than a
small assembly.

- [ ] **Build each unique definition once**, cache B-rep + tessellation by content hash.
- [ ] **Instances are transforms**, never rebuilt solids.
- [ ] **A pool of kernel workers**, with per-job queues and timeouts.
- [ ] **Export by structure**: per-definition STEP plus assembly references,
      not one monolithic compound. Evaluate glTF instancing, USD and JT for
      lightweight delivery.
- [ ] Mesh budget per definition and per level of detail, not per model.

**Accept when:** 1M occurrences of 1k definitions builds in bounded time, and a
rebuild after a one-part change rebuilds one definition.

### Phase 5 — Verification at scale

**Why:** the six promises have to still be true at a million parts. A four-view
contact sheet is unreadable well before that, and all-pairs interference is
quadratic.

- [ ] **Interference with a spatial index** (BVH/octree), per sub-assembly and at
      interfaces, instead of all-pairs with a 2,000 budget.
- [ ] **Generator rule checks** (spacing, edge distance, count) — deterministic.
- [ ] **Mass, centre of gravity and envelope** rolled up through the tree.
- [ ] **Vision check per sub-assembly**, not on the whole model.
- [ ] **Coverage is reported**: "checked X of Y". An unmeasured check must not
      look like a clean one.
- [ ] Checks on a definition apply to every instance; checks on placement apply
      per occurrence.

**Accept when:** seeded defects deep in the tree are found, and the report says
how much of the model was checked.

### Phase 6 — Viewport

**Why:** one draw call per part without instancing stops being interactive long
before a million parts.

- [ ] **GPU instancing**: one draw call per definition.
- [ ] **Level of detail, frustum culling, load-on-demand** sub-trees.
- [ ] **32-bit indices** where a definition needs them.
- [ ] **Tree browser**: search, isolate, hide, section by sub-assembly.
- [ ] Streamed lightweight format (glTF with `EXT_mesh_gpu_instancing`, or USD).

**Accept when:** 1M instances stay interactive at a stated frame rate on stated
hardware.

### Phase 7 — Edit, compare, audit

- [ ] `prototype_edit` addresses a sub-tree path.
- [ ] Editing a definition reports every occurrence it changes.
- [ ] Compare is a structural diff: "section 41: 240 rivets moved".
- [ ] Audit entries per sub-tree change, on the existing tamper-evident chain.

**Accept when:** one definition edit propagates, compares and audits correctly
across a million occurrences.

---

## Order

```
Phase 0 decisions
      │
Phase 1 document  ──────────────┐
      │                         │
 ┌────┼─────────┐               │
Phase 2       Phase 3        Phase 4
agent         storage        kernel
 └────┬─────────┘               │
      │                         │
Phase 5 verification ◄──────────┘
      │
Phase 6 viewport
      │
Phase 7 edit/compare/audit
```

The research doc's earlier items fit inside this: mirror, frames and sections
are part of Phase 1; interference is part of Phase 5.

---

## Conflicts with existing decisions (to resolve, not blend)

| existing decision | why it breaks at millions |
|---|---|
| "Store the document whole, so replay cannot differ" (`0011_geometry.sql`) | a whole-model row cannot hold or serve a million parts; needs a new replay guarantee |
| "One render per turn", whole-model vision check (Stage 7) | a whole-model picture cannot be read; checks must run per sub-tree |
| Build as one HTTP turn with a 30-minute budget | millions-part builds are hours of work; they must be durable worker jobs |
| Every step sees the whole document (`assemble.go`) | cannot fit in any context window; steps must see a sub-tree |
| Shared k3s node for four products | a large build would starve the others |

## What this does NOT claim

- Any timeline or cost estimate for the phases.
- That the model can reliably decompose a whole aircraft into sub-assemblies and
  generators. Phase 2 must be measured at each milestone, like every earlier stage.
- That FORGE's industry ceiling changes. Concept work at any scale is still R1.
