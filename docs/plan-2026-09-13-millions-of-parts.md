# Millions of parts: implementation plan

**Status: in progress. B0, B0b, S1–S3, D1a, the kernel-mesh placement bug, D1b, D1c-1 (mirror) and D1c-2 (patterns) are in PRs #51–#59, the repeat-id bugfix in #60, K0 (kernel in CI, pinned packages) in #61; D1d (interfaces) in #62; D1e (features in a tree) in #64, D1f (contract) in #65 — Phase 1's document stages are done; S0 (size fence) in #66, K1 (definition cache) in PR #68, K2 (XDE assembly export) in PR #69, K2b (interference broad phase) in PR #72, K3 (kernel pool) in PR #73, K4 (mesh per definition) in PR #75, E2 (structural compare) in PR #74; CI repairs: MinIO fixed on #54, `main`'s red fixed in #63, the script hang on 4+ core machines fixed in #67 and proven on the 4-CPU arm64 runner.**
Written 2026-09-13. Checklist and background:
[`research-2026-09-13-millions-of-parts.md`](research-2026-09-13-millions-of-parts.md).
Live car measurement: [`spikes/2026-09-12-car-ceiling/`](spikes/2026-09-12-car-ceiling/README.md).

## Requirement summary (conversation log)

- 2026-09-12 — "It still can't generate cars, planes, rockets, houses, bridges." → research; live
  car build measured at 28 parts; interference check added.
- 2026-09-13 — "It needs to build millions of parts. Give me a checklist." → 8-phase checklist.
- 2026-09-13 — "Complete all phases." Decisions taken:
  - **first milestone: a full car, ~30k parts**
  - **exact detail everywhere** (every placed copy is an exact solid in the kernel and in exports)
  - **live measurement budget: up to 300k tokens per milestone**, enforced by the harness
  - **resources: same k3s node, hard limits**
- 2026-09-13 — D1 definitions + tree; D2 **AWS S3** (not the recommended "no new table yet");
  D3 fix the repeat bug first as its own PR; D4 one PR per stage.
- 2026-09-13 — S3 specifics: an idempotent `deploy/bootstrap-s3.sh` (with `--dry-run`) that Claude
  runs only after damon approves the dry run and runs `aws login`; **only large immutable blobs** go
  to S3 (the document stays in Postgres); **MinIO** container for local and CI tests; a deployment
  with no bucket **refuses large models loudly**, naming `FORGE_BLOB_BUCKET`.

## What the exploration found (2026-09-13)

Read from code, with two claims confirmed live. Paths are relative to the repo.

| finding | evidence | consequence |
|---|---|---|
| 🐞 **A feature naming a repeated part is never applied** | live probe: `weld: spoke could not be built, so this was not applied`. `cad.BuildDocument` sends `doc.Operations()` from the UNEXPANDED doc while `geometry.Solids` sends expanded `spoke-1…` | "one fuse welds all sixty spokes" (plan-2026-09-09, Stage 3) is false in the kernel and has been since `repeat` shipped; the new interference check then reports the unwelded spokes |
| 🐞 The browser ignores `repeat` | no repeat handling in `forge3d.js`/`workbench.js`; mesh join by `part.id` misses `spoke-1…` | a pattern is drawn once |
| 🐞 `Measure`, `Compare`, `ValidateStates`, `partColour`, `turned.go` labels read unexpanded ids | exploration report §1–2 | copies missing from extents; grey copies; states cannot address copies |
| Expansion is read-time and called in 4 places inconsistently | `Solids`, `Tessellate`, `Faults` (gears+repeats), `ProfileProblems` (gears only) | Phase 1 must make expansion ONE function every reader uses |
| Document stored whole as one `jsonb` row; no size limit | `0011_geometry.sql`, `repository.go` | fine at O(designs); fatal at O(occurrences) |
| No blob / content-addressed store exists | exploration report §3 | Phase 3 either adds one table or avoids needing one |
| Kernel: one process, one mutex, 30 s timeout, lives in `forged` (1 GiB limit); worker has no kernel | `cmd/forged/main.go:139`, `deploy/k8s/30-forged.yaml` | Phase 4 needs a pool and a place with memory |
| Workbench builds are not goals: no tasks, no checkpoints, no budget, no chained audit event | `assemble.go`, `keepGeometry` sets no `GoalID`; `RecordSpend` only in `executor.go` | Phase 2 moves builds onto the engine |
| Engine already has DAG fan-out, leases, heartbeats, checkpoints, per-goal budgets | `engine/queue.go`, `engine/budget.go`, `worker.go` | reuse, do not rebuild |
| Kernel tests never run in CI; build123d unpinned in `Makefile` and `Dockerfile` | `.github/workflows/ci.yml`, `deploy/Dockerfile` | Phase 4/5 acceptance needs a CI kernel job |
| No benchmarks anywhere | no `testing.B` | every scale claim below needs one |
| Viewport: one `drawElements` per part, no instancing, no culling | `forge3d.js:1868, 2037` | Phase 6 |

## The spike that decides "exact everywhere"

Question: can a copy be an EXACT solid without being built again? Measured 2026-09-13 with the
repository's own build123d venv (`scratchpad` scripts, to be moved into `docs/spikes/` in Stage K0).

| | STEP bytes / copy | 10,000 copies |
|---|---|---|
| every copy built separately | 20,862 | ~22 s build |
| shared B-rep, build123d `Compound` | 618 | ⚠️ 13.8 s — **super-linear**, must be avoided |
| shared B-rep, OCCT XDE assembly | 619 | **0.023 s build, 0.25 s export, linear** |

A located copy shares its `TShape` with the definition (`IsPartner` true), with identical volume at
its new position. So **"exact everywhere" is achievable: one exact B-rep per definition, every
occurrence an exact placement of it**, written as a STEP assembly.

Scaling the XDE path, measured 2026-09-13:

| occurrences | assembly build | STEP write | STEP size | peak memory |
|---|---|---|---|---|
| 10,000 | 0.023 s | 0.25 s | 6.2 MB | — |
| 100,000 | 0.26 s | 11.5 s | 64 MB | 1.0 GB |
| 1,000,000 | 2.7 s | ⚠️ **928 s** | **662 MB** | ⚠️ **5.1 GB** |

- Building the assembly stays linear. **Writing STEP does not** (46× for 10× at 100k, 80× at 1M).
- A definition's triangulation is shared by every located copy (a copy never meshed itself returns
  the definition's 7 faces / 120 triangles), so meshes are computed once per design.
- ‼️ **At the 30k-car milestone this is comfortable (~19 MB). At 1M it does not fit** the node
  (t4g.large, 8 GB shared by four products; `forged` 1 GiB, `forge-worker` 2 GiB). The 1M milestone
  needs a streaming or chunked STEP writer, or exports built off-node, before its limits are raised.

Consequence recorded here because it is easy to undo by accident: **the kernel must not assemble
occurrences with build123d `Compound(children=…)`**; it goes through XDE.

---

## Decisions 🔴 (blocking the stages that name them)

| # | decision | recommended | alternatives | blocks |
|---|---|---|---|---|
| D1 | document schema | ✅ **additive `definitions` + `assemblies` + `root`; `parts` stays as root-level inline parts** | rejected: `ref`/`parent` per occurrence (O(occurrences)); external format as truth |
| D2 | storage | ✅ **AWS S3 for large immutable blobs; the document stays one `jsonb` row in Postgres** (no new table) | rejected: blob table; files on the RWO PVC |
| D2a | who creates AWS resources | ✅ idempotent `deploy/bootstrap-s3.sh --dry-run`; Claude runs it after damon's OK + `aws login` | rejected: damon runs it; shared bucket |
| D2b | testing | ✅ MinIO container locally and as a CI service, same SDK path | rejected: real test bucket |
| D2c | no bucket configured | ✅ large models refused loudly, naming `FORGE_BLOB_BUCKET` | rejected: Postgres fallback (needs a table, diverges from prod) |
| D3 | repeat/feature bug | ✅ fix first, own bugfix PR | rejected: fold into Phase 1 |
| D4 | delivery | ✅ interference work as its own PR, then one PR per stage | rejected: one PR at the end |

---

## Stages

Each stage closes on its own: it ships behind nothing, keeps every existing test green, and has an
automatic acceptance test named below. No mocks for the thing under test (kernel stages run the real
kernel; storage stages run real Postgres via `make test-integration`).

### B0 — Bugfix: features on repeated parts (D3)

- `cad.BuildDocument` and `geometry.Solids` take features from the SAME expanded document
  (`expandRepeats` already produces `retargetFeatures`; `Solids` discards them today).
- `scriptFor` looks up the authored part of an expanded occurrence.
- Bugfix doc `docs/bugfix/2026-09-13-features-on-repeated-parts-were-never-applied.md`, code comment
  with Why + link.
- **Accept:** `TestKernel_AFeatureNamingARepeatedPartIsApplied` (the live probe, as a fence: fused
  wheel = 1 part, 0 feature failures, 0 interferences); drill proves it red.

### Phase 1 — Document: define once, place many (D1)

- **D1a Occurrence iterator.** One `geometry.Occurrences(doc)` that yields `{path, definition,
  world matrix}` lazily. Gears, repeats and the new tree all expand through it. `Solids`,
  `Tessellate`, `Faults`, `ProfileProblems`, `Measure`, `ValidateStates`, `partColour`,
  `turned.go` all read through it. Flat documents produce exactly today's output.
  **Accept:** byte-identical `Solids`/`Tessellate` for every existing fixture; new fences that
  `Measure`, states and colours see repeat copies (closes the three 🐞 read-side bugs).
- **D1b Schema.** `definitions[]` (a part without placement), `assemblies[]` with `children[]`
  (`{id, ref, position, rotation}`), `root`. Hierarchical ids `a/b/c`. Validation: cycles, missing
  refs, depth limit, id uniqueness per assembly. `Validate`, `settleDocument`, `Bind` extended.
  **Accept:** a 3-level document round-trips through `Save`/`Find`; cycle and missing-ref refusals;
  a flat document is unchanged in storage.
- **D1c Patterns and mirror on children.** `pattern: linear | polar | grid | path | mirror` on a
  child, applied to a definition OR a sub-assembly. `repeat` becomes the single-part special case
  and keeps its wire form. **Accept:** a mirrored suspension corner = 2 occurrences of 1 assembly;
  `expandRepeats` fixtures unchanged.
- **D1d Interfaces.** Named frames on an assembly; a child may be placed `at: "asm/interface"`.
  **Accept:** moving an interface moves every child attached to it.
- **D1e Features in a tree.** A feature is scoped to an assembly; a `fuse`/`cut` across children
  produces a derived definition. **Accept:** the B0 wheel expressed as a sub-assembly builds
  identically.
- **D1f Contract.** Optional new fields in `geometryContractTemplate`; `prototype_edit` addresses
  `path`. **Accept:** contract example unmarshals and builds (the existing fence pattern).

### Phase 3 — Storage (D2: S3 for blobs) — runs in parallel with Phase 4 once D1b lands

What goes where, and why:

| content | store | why |
|---|---|---|
| document: definitions, tree, generators, parameters | Postgres `forge_geometry.document` (unchanged column) | saves, compare, audit and replay stay one transaction; size is O(designs) after Phase 1 |
| per-definition mesh, B-rep cache, STEP export | S3, key = `sha256` of content | large, immutable, shared across versions; a lost object is rebuilt from the document |
| anything the browser loads | served by FORGE's own API from S3 | a browser cannot load S3 URLs under this CSP (the same trap that made media look like a codec bug) |

- **S0 Size fence.** Stored document size and definition count logged and bounded; `Validate` refuses
  above a configured ceiling with a message naming the threshold. **Accept:** integration test with a
  30k-occurrence car document; stored size recorded.
- **S1 Blob store.** A `BlobStore` port (`Put`/`Get`/`Has` by content hash, idempotent) with an S3
  adapter on aws-sdk-go-v2; endpoint override for MinIO. Config `FORGE_BLOB_BUCKET`,
  `FORGE_BLOB_REGION`, `FORGE_BLOB_ENDPOINT` (optional). No static credentials in production: the
  instance role via IMDS. Absent bucket → large-model paths refuse, naming `FORGE_BLOB_BUCKET`.
  **Accept:** tests against a real MinIO (`make blob-up`); put/get round trip, hash mismatch refused,
  second put is a no-op.
- **S2 MinIO in CI.** A MinIO service in `.github/workflows/ci.yml`; push over SSH (the PAT lacks the
  `workflow` scope).
- **S3 AWS bootstrap.** `deploy/bootstrap-s3.sh [--dry-run]`: bucket `forge-geometry-373468206837`
  (us-east-1), Block Public Access fully on, SSE-S3, an inline policy on role `heros-vm` scoped to that
  bucket. Idempotent. **Run only after damon approves the dry run.** ‼️ Verify before relying on it:
  `forge-worker` is not `hostNetwork`, and an IMDSv2 hop limit of 1 would hide the role from it.
- **S4 Deploy wiring.** ConfigMap values, NetworkPolicy egress to S3, `deploy/verify.sh` check that a
  blob round-trips from inside both pods.

### Phase 4 — Kernel (exact everywhere)

- **K0 Spike → fixture.** Move the instancing spikes into `docs/spikes/2026-09-13-exact-instancing/`.
  Pin build123d in `Makefile`, `Dockerfile`, CI. Add a CI job that installs the venv and runs
  `make test-cad` (kernel tests never run in CI today).
- **K1 Definition cache.** Build each definition once per build, key = hash of its canonical JSON;
  occurrences are `TopLoc_Location`s. **Accept:** 1 definition × 10k occurrences builds with one
  shape build (counted), exact volume per occurrence.
- **K2 XDE assembly export.** STEP via `STEPCAFControl_Writer`, never `Compound(children=…)` for
  occurrences. **Accept:** re-import the STEP, count instances and one product definition; a
  benchmark fence that 10k occurrences export in linear time.
- **K2b Interference broad phase** *(added 2026-09-14)*. Replace `_interferences`' all-pairs bounding-box scan
  (27% of a 10k build, quadratic) with a sort-and-sweep broad phase. **Accept:** the interference list is identical
  to all-pairs on the kernel fixtures; the number of pairs compared is counted and grows ~linearly on a spread grid.
- **K3 Pool and placement.** N sidecars behind the existing `BuildDocument` API; heavy builds run on
  `forge-worker` (2 GiB) with its own pool; k8s limits set. **Accept:** concurrent builds do not
  serialise (timed test); a killed sidecar is replaced (extends `TestRetryAfterTheProcessDies`).
- **K4 Mesh per definition.** Tessellate a definition once; the mesh endpoint returns definitions +
  instance matrices; budget per definition. **Accept:** 30k-occurrence car mesh payload size and time
  recorded; triangles counted once per definition.

### Phase 2 — Agent: structure, not parts (after D1b–D1d, K3)

- **A1 Build as a goal.** A car build creates an engine goal; subsystems are tasks with deps; the
  worker gets a geometry tool and a kernel. Tokens recorded with `RecordSpend` on every call,
  including repairs and vision. **Accept:** kill the worker mid-build, it resumes from checkpoint;
  budget refusal stops the goal cleanly.
- **A2 Subtree context.** Each task sees its subtree and the interfaces it attaches to, never the
  whole document. **Accept:** prompt size bounded and asserted as the document grows.
- **A3 Standard parts + generators in the contract.** Fasteners, bearings, sections referenced by
  spec; generators preferred; enumerated repetition flagged deterministically.
- **A4 Live milestone (≤ 300k tokens).** `make measure-car` extended to the tree: parts, definitions,
  occurrences, tokens per definition, interference, coverage. Result written to a spike doc.

### Phase 5 — Verification at scale (after K1, D1a)

- **V1 Spatial index interference.** Broad phase over occurrence AABBs (definition box × matrix)
  with a sweep-and-prune or grid; narrow phase booleans cached per `(defA, defB, relative pose)`.
  **Accept:** 30k occurrences finish in a recorded time with full coverage; the existing five
  kernel fences still pass.
- **V2 Coverage report.** Every check reports "checked X of Y"; a truncated check can never read as
  clean. **Accept:** fence that a truncated interference run says so in the turn.
- **V3 Mass, centre of gravity, envelope** rolled up through the tree (volume-weighted when no
  material density is declared, and said so).
- **V4 Vision per sub-assembly**, bounded count per turn.

### Phase 6 — Viewport (after K4)

- **W1 Instanced drawing** (`ANGLE_instanced_arrays` / WebGL2): one draw call per definition.
- **W2 Culling, LOD, lazy subtree loading**; tree browser with search/isolate.
- **W3 Parity fences** extended: the browser expands patterns exactly as Go does (node-based tests
  like `TestRendererDrawsTheSameGearAsTheExporter`).
- **Accept:** Chrome MCP run on `make` restart: 30k-occurrence car loads, frame time recorded.

### Phase 7 — Edit, compare, audit (after D1f, A1)

- **E1** `prototype_edit` by path; editing a definition reports every occurrence it changes.
- **E2** Structural compare.
- **E3** Geometry saves inside a goal write the chained `artifact.changed` event (today they write
  none, because `keepGeometry` sets no `GoalID`).

### Scale-up milestone: 1,000,000 occurrences

Synthetic generator document (an airframe barrel with rivet generators) through K1–K4, V1, W1.
Benchmarks recorded; limits raised only on measured numbers.

## Order (DAG)

```
decisions D1–D4
      │
     B0 ── (bugfix, independent, first)
      │
    D1a ─► D1b ─► D1c ─► D1d ─► D1e ─► D1f
             │                    │       │
             ├─► S0 ─► S1?        │       │
             │                    │       │
             └─► K0 ─► K1 ─► K2 ─► K2b ─► K3 ─► K4
                        │            │     │
                        └─► V1 ─► V2 │     └─► W1 ─► W2 ─► W3
                              V3, V4 │
                                     └─► A1 ─► A2 ─► A3 ─► A4
                                                 │
                                           E1 ─► E2 ─► E3
                                                 │
                                        1M scale-up milestone
```

## Execution record

| stage | state | notes |
|---|---|---|
| decisions D1–D4, S3 specifics | ✅ taken 2026-09-13 | |
| instancing spikes (10k–1M) | ✅ measured 2026-09-13 | 1M STEP write 928 s / 5.1 GB — see above |
| interference check (prerequisite, from 2026-09-12) | PR opened 2026-09-13 | branch `interference-check` |
| B0 | ✅ PR #52 (stacked on #51) | commit `baa7aba`; 4 fences, 3 drills proven red, whole-suite dry run 0 anchors moved |
| B0b radius units | in progress, branch `fix/feature-radius-units` | confirmed live: a cm model's 1 cm fillet reaches the kernel as 1 mm (999,744 vs 975,587 mm³); branches from B0 (both touch `solid.go`) |
| S3 AWS bootstrap | ✅ applied 2026-09-13 (damon approved the dry run) | bucket `forge-geometry-373468206837`: Block Public Access all on, ACLs off, SSE-S3, TLS-only; inline `ForgeGeometryBlobs` on role `heros-vm` (Get/Put under `blobs/`, List on that prefix). Re-run converged. IMDS hop limit already 2 — no node change. Script not yet committed (lands with S1). |
| B0b radius units | ✅ PR #53 (stacked on #52) | commit `08b9f9b`; 3 fences, 3 drills proven red, full kernel suite ok |
| S1 blob store | ✅ this PR | `internal/platform/blob`: content-addressed `Store`, S3 adapter (conditional write, SHA-256 checksum, verifying reads), refusing store when unconfigured; `FORGE_BLOB_*` config, half-configurations refused at boot; 12 tests incl. 5 against a real MinIO |
| S2 MinIO in CI | ✅ this PR | `make blob-up blob-wait` in CI, image pinned `RELEASE.2025-09-07T16-13-09Z` |
| S4 deploy wiring | not started | ConfigMap values, NetworkPolicy egress to S3, `verify.sh` blob round trip from both pods |
| D1a one expansion for every reader | ✅ PR #55 (stacked on #53) | commit `6aa0b37`; `Document.Expanded()`, readers + browser `partsToDraw`; 6 fences confirmed failing first, 6 drills red; live browser: 2 parts drawn before, 13 after |
| B1 kernel mesh placed twice (found in D1a) | ✅ PR #56 (stacked on #55) | commit `52bc542`; confirmed with the real kernel mesh; `modelMatrix`; fence + drill red at (186.6, 50, 0) vs (100, 0, 0); live browser before/after |
| D1b-1 schema + flattening (Go) | ✅ PR #57 (stacked on #56) | commit `b8a3078`; `definitions`/`assemblies`/`root`; `EulerDegreesFromMatrix` + `placeInFrame` (5,000 random round trips exact); flattened ids are child-id paths; refusals as faults and at the storage door; `clone`/`Edit.Apply` deep-copy the tree (found: a respec would have written into the original's definitions); `bindPart` binds definitions; real kernel builds a nested tree |
| D1b-2 browser mirror | ✅ PR #57 | term-for-term JS port; 7 parity cases, rotations compared as matrices; live browser: 1 part before, 29 after |
| D1b-3 storage round trip | ✅ PR #57 | real Postgres: tree unchanged after save/load; flat document gains no fields; broken tree refused |
| ‼️ new limit | recorded | `maxTreeParts = 4096` — a PRE-INSTANCING ceiling; raise on measured numbers after K1 (one build per design) and W1 (instanced drawing) |
| carried to D1f | recorded | the agent's own "no parts" checks (`CurrentModel`, `assemble`, `resolveEdit`, `settleDocument`) and `Spans` — they matter once the model can write a tree |
| D1c decisions | ✅ taken 2026-09-14 | mirror = **true reflection**; child patterns = **new `pattern` object now** (linear, polar, grid, path) |
| D1c-1 mirror | ✅ PR #58 (stacked on #57) | commit `0a82e65`; placements carry a reflection; stored as `Part.Mirrored` ("negate local x, then rotate"); every reader honours it; found: build123d's `mirror()` operation makes the assembly Compound report volume 0 — the sidecar uses `shape.mirror(Plane.YZ)`; 11 drills red; live browser before/after |
| D1c-2 pattern object | ✅ PR #59 (stacked on #58) | commit `a63b265`; `Child.pattern` linear / polar (repeat's `sweepAngle`) / grid (row-major) / path (equal arc length; `align` = smallest turn of local +X onto the outgoing segment); each copy = pattern transform in the PARENT frame ∘ the child's own placement, so sub-assemblies pattern whole; copy ids `child-n`; refuses kind/offset/axis/grid offsets/short or zero-length path/corner radius/`via`/expressions/>512; pattern of one = warning, drawn once. Duplicate ids: the storage door (`variant.go`) stays the ONE owner — a second rule in the walk was written and removed. Kernel: 2×3 grid + ring of four = 10 cubes, 10000 mm³. 17 drills red; live browser 4 → 32 parts. **Found, pre-existing:** a `repeat` copy could take another part's id and be stored (the door read tree placements, not repeat copies) — fixed in **PR #60** (stacked on #59; `main` has the same bug until the stack merges). Not yet: the model contract does not describe `pattern` (D1f) |
| D1d decisions | ✅ taken 2026-09-14 | `at` may name an interface on the child's PARENT (`at: "mount"`) or on a SIBLING's placement (`at: "front-left/hub"`, a path of child ids — pattern copies by their copy id, `bolt-3/seat` — ending in an interface id), so it always names ONE placement (an assembly id alone would be ambiguous once the assembly is placed twice); the child's position, rotation, mirror and pattern are measured in the interface's frame; interfaces are declared on assemblies only (a part that needs one is wrapped in a one-child assembly) |
| D1d interfaces | ✅ PR #62 (stacked on #60) | `Assembly.interfaces` + `Child.at`; paths resolve the sibling's own attachment ∘ its pattern slot ∘ its placement (mirror included) ∘ the interface, recursively; refusals by name (unknown interface/child, a part segment, a patterned sibling without a copy id, loops, empty segments, bad or repeated interface ids); acceptance: moving an interface moves every attached child in every occurrence by exactly that occurrence's turn of the move, nothing else; real kernel: hub z=50→80 moves the attached cube's top to the hub's z; 12 drills red (plus a vacuous browser drill removed with the redundant line it tested); D1c-1/D1c-2 drills re-anchored; Mirror+Patterns+Interfaces sections 40/40 red; live browser before/after |
| D1e decisions | ✅ taken 2026-09-14 | a feature is declared ON AN ASSEMBLY and its `of`/`with` are PATHS from that assembly (a child, a pattern copy, a whole pattern = every copy, a definition's repeat copy, a sub-assembly = all its parts, deeper paths), the path syntax `at` uses; written once and applied in EVERY occurrence; each occurrence builds its own result — building it once and placing it many times is K1's definition cache, so D1e is about meaning and K1 about cost; top-level features unchanged |
| D1e features in a tree | ✅ PR #64 (stacked on #62) | `Assembly.features`, `of`/`with` paths from the assembly; the depth-first walk returns path → contiguous part range, so resolution is structural; features written out per occurrence after the assembly's children (inner before outer), ids scoped (`fl/weld`); `of` on a group = first part, `with` = all; unknown paths refused by name; expansion copies `d.Features` before appending; clone copies assembly features; browser parity now also compares removed (ghosted) parts; **acceptance on the real kernel: the B0 wheel as a sub-assembly builds to the same solid count, volume and bounds as the flat document; placed twice = two welded wheels**; 14 drills red (33/33 across Designs, browser tree, Interfaces, Features); live browser before/after |
| D1f decisions | ✅ taken 2026-09-14 | a `prototype_edit` changes a tree by patching the DESIGN by id — `patch.definitions` / `patch.assemblies` replace whole entries (the rule parts already follow) and `remove` gains `definitions`, `assemblies` and `children` (`"wheel/spoke"` = assembly id + child id) — so editing a definition changes every occurrence and define-once holds; the contract tells the model to use a tree when a part or sub-assembly REPEATS or parts MATE to named frames, and flat parts stay the default for a single part or a small assembly |
| D1f contract | ✅ PR #65 (stacked on #64) | the contract offers definitions / assemblies (interfaces, children with ref/position/rotation/mirror/at/pattern, features) / root, the tree edit (patch definitions and assemblies by id; remove definitions, assemblies, children `assembly/child`), a paragraph on when and how to write a tree, and a worked example (two welded wheels on an axle) that a fence decodes with unknown fields refused and builds (15 parts, both welds, no faults, a clean kernel request); the six agent checks that read `len(Parts) == 0` read `HasGeometry` (settleDocument used to DROP a tree-only design); CurrentModel renders the tree; Edit applies tree removals and patches without touching the base; Spans reads definitions in their own frame; 11 drills red (the first run exposed a fence that removed the LAST child and so could not see an in-place filter) |
| CI found broken while proving K0 | 2026-09-14 | (1) #54's MinIO image was pulled from Docker Hub, where `minio/minio` no longer exists — S2 was never green in CI; fixed on #54 (quay.io, same digest) and CI now starts MinIO; (2) `main` red since #49: script refusals imported build123d to word their hints, and nine new tests needed a kernel without skipping — fixed in **PR #63** against `main`; (3) `TestScript_BuildsWhatTheVocabularyCannot` exceeds the 30 s script limit in CI — the kernel job runs on `ubuntu-24.04-arm` (production's architecture), where it ALSO times out; root cause and fix in the 🔴 row below |
| S0 decisions | ✅ taken 2026-09-14 | (1) the 4096-part tree ceiling moves out of STORAGE and into DRAWING and BUILDING: a 30k-occurrence design can be saved and versioned now, and the viewport mesh and the kernel refuse above 4096 with the existing 'until instanced drawing and one build per design land' message, shown loudly with the saved version; (2) storage is bounded by S0's ceilings: 2 MiB stored document and 2000 definitions by default, both configurable, logged on every save, refused naming the threshold and the setting. Measured before deciding: a car of 150 definitions and 10 assemblies stored as a tree is ~96 KiB; written flat it would be ~10 MiB at 30k. **Flagged, not yet decided:** storage still needs an occurrence bound counted arithmetically before expanding — patterns nest 16 deep at up to 512 copies, so a few hundred bytes can describe more occurrences than can be expanded, and the storage door expands to check a design |
| S0 size fence | ✅ PR #66 (stacked on #65) | storage door: 2 MiB / 2000 definitions / 100,000 occurrences COUNTED before expanding, each configurable and refused naming its setting, size logged on every save; the 4096 ceiling moved to drawing and building (kernel request, BuildDocument, Tessellate, Export refuse whole; the browser expands in parity and Studio.load reports the refusal instead of silently drawing the first 4096); process-wide limits set at start (flagged in the PR); acceptance on Postgres: a 30,060-occurrence car stored, read back and logged with its size; 17 drills red (the first run exposed a parity case that reached the ceiling without a pattern) |
| K1 definition cache | ✅ PR #68 (on #66) | sidecar builds each distinct shape once per request (key: the fields that decide the solid, mirror included) and places located copies; Go runs each distinct script once; Build reports ShapeBuilds and ScriptRuns; kernel acceptance at S0's ceiling: 4096 occurrences = 1 shape build, exact volume; 10k measured against the sidecar directly (`docs/spikes/2026-09-14-definition-cache`): 1 build, exact volume, but only ~13% faster for primitives — 64% of a 10k build is build123d's quadratic `Compound(children=…)` (K2's to replace) and 27% the interference check's all-pairs box scan (needs a spatial index, not yet staged); a drill found `shape_builds` read back as the cache's size, which cannot show the cache bypassed — it now counts `_shape` calls |
| K2 XDE assembly export | ✅ PR #69 (on #68) | spike first (`docs/spikes/2026-09-14-xde-assembly-export`): assembling N located copies with build123d's `Compound(children=…)` is quadratic (13.8 s at 10k) and an XDE assembly is linear (0.26 s), and the FILE IS THE SAME (1 B-rep, N instances, names, exact volume) — so the plan's "count one product definition" acceptance was already true before K2 and cannot fence it; the sidecar now assembles in one pass (`Compound(list)`), writes STEP from its own XDE document (one label per definition, a located named component per occurrence, same header/precision/names as `export_step`), and reports seconds per phase (`Build.Phases`, logged on export and on a new `forge.geometry.meshed`); fence at 4,096: one B-rep, N instances, assembly+export linear (ratio 512→4,096 and an absolute ceiling) |
| K2 decisions | ✅ taken 2026-09-14 | the linear-time fence reads phase times the sidecar reports (not a whole-build timer, which would include the still-quadratic interference check, and not structure alone, which pre-K2 already passed); the interference broad phase is its own stage K2b right after K2 |
| K2b interference broad phase | ✅ PR #72 (on #69) | measured past S0's ceiling (`docs/spikes/2026-09-15-interference-broad-phase`): at 10k the interference phase went 10.4 s → 2.1 s (69% → 30% of the build) and box tests 49,995,000 → 495,000; what remains is reading each solid's bounds and volume, which is linear; the sidecar sorts boxes along the axis their centres spread furthest, tests each only against the boxes still open, and hands the pairs to the unchanged narrow phase in index order, so a budget-truncated answer is the same one; `Build.InterferenceBoxTests` reports the count; fences: box tests 1,792 → 14,336 for 512 → 4,096 parts (8×; every pair is 64×), the same grid standing along z, and the sweep equal to every pair on a 118-part scatter (156 interferences) and on a dense cluster stopped by the budget (`testdata/interference_all_pairs.py`); 5 drills red. **Found:** a one-axis sweep costs √n per part on parts spread evenly over a plane (495,000 tests for 100 × 100), so it is linear only along one axis; a three-axis index stays V1 |
| K3 pool and placement | ✅ PR #73 (on #72) | a Kernel is `FORGE_CAD_POOL` processes (default 1) behind a FIFO channel: the process moved into an unexported `sidecar` type with its own lock around `cmd` (the deadline goroutine's kill could race `stop`); a build takes a free process waiting with its ctx, retries once on THAT slot and gives it back; a restart logs its slot; Close still waits for builds in flight; fences: two 2,048-part builds at once vs one alone 1.11× (fails above 1.6×), every process in a pool of two killed behind the kernel's back and each slot replaced, pool < 1 refused by name; 3 drills red (pool forced to one process measured 2.14×). **Found:** `31-worker.yaml` said the worker holds a warm kernel; it constructs none — corrected. **Not done:** the worker's own pool arrives with A1 (nothing there builds yet), and forged stays at one process until a sidecar's memory is measured on the arm64 node |
| K4 mesh per definition | ✅ PR #75 (on #73) | measured first past S0's ceiling (`docs/spikes/2026-09-15-mesh-per-definition`): at 30,000 occurrences the mesh phase went 129 s → 0.73 s, triangles 360,000 → 12 and the mesh payload 21.0 MB → 4.6 MB (the rest is one ~150-byte JSON instance per occurrence — revisit in W1); the sidecar tessellates each untouched shape once in its own frame and sends `mesh_definitions` + `mesh_instances` (4×4 column-major matrices), parts a feature was applied to keep their own placed mesh; `cad.Build.WorldMeshes()` expands them for agent pictures and `Forge3D.expandMeshInstances` for the workbench, so the renderer is unchanged until W1; fences: 512 copies = 1 definition and one stud's triangles, every instance matches the per-solid mesh's nodes, triangle count, area and signed volume (turned, mirrored L, cut plate), Go/node parity; 5 drills red. **Found:** OCCT triangulates a shape placed and unplaced to identical nodes but may pick different diagonals, so meshes are compared as surfaces, not index lists |
| E1 edit by path | ✅ PR #76 (on #72) | per the 2026-09-15 decision a PATH resolves to the definition or assembly it places (D1f's define-once holds): wherever an edit names a design id (`patch.definitions[].id`, `patch.assemblies[].id`, `remove.definitions`, `remove.assemblies`) it may name a placed path (`front-left/hub`, `bolt-3`, `a-2`) — no new wire keys, an existing id still means that id, unresolvable/ambiguous paths refused by name; `Edit.ApplyAndReport` returns each changed design with the placed part ids that show it and `resolveEdit` says so in the turn (≤8 ids, then "and N more"); the contract teaches the path form; 10 drills red. Open for review: silent when a change reached exactly the part it named; parameter/feature edits not traced to parts. **Found:** a drill anchor broken by K2b's refactor (fixed on #72) |
| E2 structural compare | ✅ PR #74 (on #72) | `Comparison.Structure` (nil for flat documents): root, definitions (missing-from, changed fields, occurrence counts per variant by a memoised tree walk — O(tree), never expanded), assemblies (children, interfaces, features added/removed/changed); HTTP body built by a pure `comparisonBody`, `forgectl geometry compare` STRUCTURE section, workbench Structure table; flat documents compare byte-identically; 12 fences, 8 drills red. Open for review: a definition's own `repeat` is reported as a changed field rather than multiplied into its count |
| decisions for the remaining stages | ✅ taken 2026-09-15 | (1) **keep stacking**: each stage branches from the previous one, one PR per stage, nothing merged without damon; (2) **E1: a path resolves to the definition it places** and the edit reports every occurrence it changes — D1f's define-once holds, no per-occurrence overrides; (3) **A4 live run approved, capped at 300k tokens** (the `measure-car` default of 400k is lowered to match); (4) **cluster changes are committed, not applied**: manifests, the forge-worker egress NetworkPolicy and a `verify.sh` blob round-trip check are written and fenced, and damon applies them. Defaults adopted from scoping, each revisitable in its stage's PR: kernel pool 1 process in forged (each holds its own build123d under a shared 1Gi) with the worker's own pool arriving with A1; mesh endpoint gains definitions + instance matrices additively (4×4 column-major, JSON) while `parts` stays for feature-modified solids; interference budget counts booleans actually paid for; mass reported only when every part has a density, otherwise volume-weighted and said so; vision looks at ≤4 sub-assemblies per turn plus the whole sheet; WebGL2 with the WebGL1 `ANGLE_instanced_arrays` path kept; STEP export at 1M refused until an off-node export job exists |
| 🔴 ROOT CAUSE FOUND: scripts hang on 4+ core machines | 2026-09-14 | measured on the 4-CPU arm64 runner by `make cad-script-timing`: the gear script HANGS under FORGE's limits (7 threads, VmSize 1,032,152 kB = the 1 GiB address-space cap, state sleeping) and under the address-space cap alone, and builds in 3.4 s with the CPU limit only or no limits, and in 3.5 s under FORGE's limits with one thread (OMP/OpenBLAS/TBB/MKL = 1). So: the address-space cap plus a multi-threaded kernel exhausts address space, a thread cannot get memory and the rest wait forever, asleep, so the 10 s CPU limit never fires and the part dies at the 30 s wall clock. A 2-CPU VM starts fewer threads and stays under the cap, which is why it never reproduced locally. **A product bug since scripts landed (#43), not only CI**: any node with 4+ cores. ✅ Fixed in **PR #67** (decision 2026-09-14: run scripts single-threaded, keep the 1 GiB cap) and merged into #61 at 31ebf2c; proven on that run's 4-CPU arm64 kernel job: `TestScript_BuildsWhatTheVocabularyCannot` PASS in 3.93 s, job green (run 34821438386) |
| K0 kernel in CI + pinned build123d | ✅ PR #61 (on #54) | one pinned list `internal/domain/cad/requirements.txt` read by `make cad-venv`, the Dockerfile and a new CI `kernel` job (Python 3.13); checked first that the deployed image and the dev venv already ran build123d 0.11.1 on OCP 7.9.3.1.1 (no drift); proved on fresh 3.13 and 3.14 venvs (74 kernel tests each, 0 skipped) and in the image (47/47 packages match); instancing spikes moved to `docs/spikes/2026-09-13-exact-instancing/`; the CI job itself is proven by its first run on the PR | branches from #54 (it changes `Makefile` and `ci.yml`) |
| everything else | not started | |

## What this plan does NOT claim

- Any calendar estimate. This is many PRs.
- That the model decomposes a car into a good tree: A4 measures it.
- That 1M occurrences fit in the current node limits: the scale-up milestone measures it before any
  limit is raised.
