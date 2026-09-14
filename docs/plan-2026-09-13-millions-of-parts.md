# Millions of parts: implementation plan

**Status: not executed. Decisions D1–D4 and the S3 questions are taken (2026-09-13); B0 is next.**
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
             └─► K0 ─► K1 ─► K2 ─► K3 ─► K4
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
| B0b radius units | ✅ PR opened (stacked on #52) | 3 fences, 3 drills proven red, full kernel suite ok |
| S1 blob store | ✅ this PR | `internal/platform/blob`: content-addressed `Store`, S3 adapter (conditional write, SHA-256 checksum, verifying reads), refusing store when unconfigured; `FORGE_BLOB_*` config, half-configurations refused at boot; 12 tests incl. 5 against a real MinIO |
| S2 MinIO in CI | ✅ this PR | `make blob-up blob-wait` in CI, image pinned `RELEASE.2025-09-07T16-13-09Z` |
| S4 deploy wiring | not started | ConfigMap values, NetworkPolicy egress to S3, `verify.sh` blob round trip from both pods |
| everything else | not started | |

## What this plan does NOT claim

- Any calendar estimate. This is many PRs.
- That the model decomposes a car into a good tree: A4 measures it.
- That 1M occurrences fit in the current node limits: the scale-up milestone measures it before any
  limit is raised.
