# FORGE — Product Requirements (implementation reference)

> **Source of truth.** This file is the *implementation-facing* index of
> `forge_voice_engineering_agent_prd.docx` (v1.0, Product & Applied AI).
> It preserves every requirement ID verbatim so that code, tests, and commit
> messages can cite them. Where this file and the source document disagree,
> the source document wins — raise the conflict rather than editing around it.

## Thesis

Give every builder a persistent technical partner that can discuss ideas and
actively co-build engineering artifacts — while the human directs intent,
reviews evidence, and controls consequential action.

**North star:** increase the rate of *verified* engineering progress produced by
human–AI teams, without weakening human understanding, authority,
accountability, or safety.

## Product principles → what they force in code

| Principle | Implementation consequence |
|---|---|
| Conversation is the control plane | Speech/interruption drive work; visuals carry precision |
| Think and build together | Elicit constraints, propose alternatives, critique, then create |
| Show the reasoning that matters | Assumptions, sources, diffs, results, confidence, risks are first-class records |
| Verification before confidence | Fluent output and photorealistic renders are never treated as proof |
| Agency is permissioned | Consequential change requires preview → approval → rollback |
| Domain depth over generic theater | Domain-specific models, units, standards; capability limits admitted |
| Calm under interruption | Barge-in, noise, partial info, resumable work without state loss |
| Safety is a product behavior | Risk language, boundaries, uncertainty, escalation are visible UX |

## Requirement index

### AUD — Voice and audio
- **AUD-01** Full-duplex conversation; barge-in without losing project state
- **AUD-02** Median end-of-utterance → first audio ≤700 ms (≤1.5 s with retrieval); show retrieval when slower
- **AUD-03** Mic selection, echo cancellation, noise suppression, speaker separation, domain vocabulary, unit-aware transcription, push-to-talk/wake
- **AUD-04** Technical readback of numbers, units, tolerances, IDs, coordinates
- **AUD-05** Voice identity and tone; always identifies itself as AI
- **AUD-06** Accessibility: captions, transcript search, keyboard-only, screen reader, adjustable rate, non-audio path for every critical interaction
- **AUD-07** Always-visible mute, stop-speaking, pause, end-recording, delete-session

### RSN — Discussion and reasoning
- **RSN-01** Editable model of goals, requirements, constraints, assumptions, decisions, risks, success criteria — **separate from the transcript**
- **RSN-02** Socratic clarification before consequential work; labeled assumptions permitted for low-risk exploration
- **RSN-03** Materially different options with tradeoffs and stated criteria
- **RSN-04** Constructive dissent; intensity configurable, but **safety-critical dissent cannot be disabled**
- **RSN-05** Epistemic labeling: observed / retrieved / calculated / simulated / inferred / assumed / proposed
- **RSN-06** Uncertainty behavior; **no fabricated measurements, standards, citations, imported results, user actions, or completion claims**
- **RSN-07** Resume from a **structured checkpoint**, not a conversation summary

### WRK — Multimodal workspace
- **WRK-01** Shared canvas: code, CAD/EDA previews, diagrams, telemetry, requirements, diffs, simulations, test results
- **WRK-02** Grounded reference; ask when reference confidence is below threshold
- **WRK-03** Project graph across requirements, components, interfaces, files, tests, hazards, decisions, owners, evidence
- **WRK-04** Artifact lifecycle inside an authorized boundary; every change identifies initiator, agent, tool, inputs, diff, verification state, human disposition
- **WRK-05** Unit and coordinate integrity: units, precision, frames, tolerance, timestamp, calibration, source

### AGT — Agentic work and human control
- **AGT-01** Capability registry: read / write / execute / simulate / export / deploy / transact / control declared **separately**, with undo, evidence, and approval semantics
- **AGT-02** Scoped plan and preview before material action
- **AGT-03** Least privilege: project-scoped, role-based, revocable, time-bound; credentials isolated from model context
- **AGT-04** Progressive autonomy; **never silently raises its own autonomy level**
- **AGT-05** Interrupt, rollback, recovery; partial failure leaves a truthful recovery plan
- **AGT-06** Tool-grounded verification; preserve raw outputs; never claim verification beyond a method's validated scope
- **AGT-07** Approval and release: consequential transitions require the named human authority
- **AGT-08** **Truthful action state** — proposed, approved, running, failed, completed, verified, accepted, released are distinct and never implied falsely

### VIS — 3D prototype generation
- **VIS-01** Generate/revise geometry from voice, text, sketch, requirements, images, CAD
- **VIS-02** Orbit, pan, zoom, section, explode, assembly states, annotations, materials, scale
- **VIS-03** Engineering overlays without confusing appearance with validated data
- **VIS-04** Variants side by side; each render links to geometry version, inputs, units, assumptions, generator, verification status
- **VIS-05** Preview meshes and, where adapters permit, editable parametric export; label tessellation, inference, lossy conversion
- **VIS-06** **Render safety label** — photorealism never implies manufacturability, structural adequacy, compliance, or clinical suitability

### MEM / COL — Memory and collaboration
- **MEM-01** Layered memory: turn context, session notes, project knowledge, org knowledge, personal preferences — distinct retention and sharing
- **MEM-02** User-editable: inspect, correct, pin, expire, export, delete; show why an item was retrieved
- **MEM-03** Decision log with date, author, alternatives, rationale, evidence, affected artifacts, supersession
- **COL-01** Multi-user voice room with identified speakers and a record of who approved what
- **COL-02** Handoff: state, actions, versions, approvals, evidence, open risks, recommended next work

### SAF — Safety
- **SAF-01** Dynamic risk classification; tier rises with permissions, irreversibility, deployment context
- **SAF-02** Hazard-aware planning for R3–R4
- **SAF-03** **Independent verification** — a method independent of the generative path
- **SAF-04** Policy-enforced refusal **below the model layer** where feasible
- **SAF-05** Human authority named; *"the AI approved it"* is never acceptable authority
- **SAF-06** Tamper-evident audit of inputs, plans, tool calls, versions, approvals, policies, evidence
- **SAF-07** Incident response: stop, revoke, quarantine, roll back, preserve evidence, notify, review

### SEC — Privacy and security
- **SEC-01** Data boundary; customer content not used for training absent affirmative opt-in
- **SEC-02** Encryption in transit and at rest; SSO, MFA, device trust, RBAC
- **SEC-03** **Secret isolation** — model receives scoped handles, not raw secrets
- **SEC-04** **Prompt-injection defense** — documents, pages, code comments, CAD metadata, tool output and imported results are untrusted input
- **SEC-05** Exfiltration controls: DLP, export control, redaction, egress allowlists
- **SEC-06** Audio privacy: visible recording state, retention-free mode, independent audio deletion
- **SEC-07** Regulated deployments stay inside validated intended use

### NFR — Nonfunctional
- **NFR-01** 99.9% monthly; mute/stop/end remain available during cloud degradation
- **NFR-02** Visual update within 300 ms of a relevant speech event; long jobs report progress at least every 10 s
- **NFR-03** **Durability** — no acknowledged checkpoint, approved plan, artifact version, tool result, or decision is lost
- **NFR-04** Scale: 1–20 identified participants; millions of indexed objects via scoped retrieval
- **NFR-05** Observability across latency, model selection, retrieval, plans, tool calls, renders, policy, approvals, failures
- **NFR-06** Portability: desktop and web first; platform-independent contracts and state
- **NFR-07** **Graceful degradation** — preserve state, stop dependents safely, expose partial results, never imply completion

## Risk tiers

| Tier | Examples | Default controls |
|---|---|---|
| **R0** General discussion | Explain a concept | Disclosure, source, uncertainty |
| **R1** Reversible draft | Sandbox code, concept CAD, render | Scoped workspace, visible changes, easy undo |
| **R2** Consequential digital | Merge code, update baseline, costly simulation | Preview + explicit approval, rollback, evidence, named owner |
| **R3** Release preparation | Issue drawings, manufacturing package, powered test | Qualified review, authoritative procedures, independent verification, two-person gate |
| **R4** Safety-critical | Patient-specific plan; structural, flight, launch, infrastructure | Approved intended use, qualified authority, validated tools, interlocks, full traceability |
| **R5** Prohibited | Bypass safeguards; unsupervised surgery, hazardous actuation, launch, weapons | **Refuse, enter safe state, preserve evidence, direct to authorized procedure** |

## Domain packs

A pack is **not a prompt**. It bundles schemas, terminology, standards access,
tool adapters, artifact validators, 3D conventions, evaluation suites, safety
policies, data-handling rules, and qualified-review requirements.

Each pack carries a **ceiling**: the highest §8.1 tier work may reach inside it in
this build. The ceiling is not the pack's safety boundary in principle — it is
what this deployment can honestly enforce. See "Ceilings, not availability" under
Implementation carve-outs.

| Industry (selector) | Pack | Ceiling | Safety boundary |
|---|---|---|---|
| Mechanical engineering | `mechanical` | R1 | Render is not proof; drawing release, tooling, fabrication, certification need qualified review |
| Manufacturing | `manufacturing` | R1 | Process concepts and tooling studies are drafts; a released process changes what gets built |
| Automotive | `automotive` | R1 | Concept and packaging work is reversible; anything touching a vehicle safety function is not |
| Aerospace | `aerospace` | R1 | No unsupervised hazardous procedure, flight command, launch decision, or release authority |
| Civil engineering | `civil` | R1 | Licensed engineer owns calculations, issued drawings, compliance, field direction |
| Electrical engineering | `electrical` | R1 | High-voltage, RF, battery, bench actuation, procurement, compliance are gated |
| Construction | `construction` | R1 | Sequencing and concept work is reversible; issued documents and field direction are not |
| Product design | `product-design` | R1 | Concepts and revisions are drafts; releasing one to tooling commits money and time |
| Architecture | `architecture` | R1 | Massing and layout studies are drafts; issued drawings and permit submissions carry legal weight |
| Other | `general` | R2 | Unknown domains or missing standards **lower** autonomy and trigger expert review |
| *(not offered)* | `software` | R2 | Sandbox by default; review before merge/deploy; secrets and production stricter |
| *(not offered)* | `robotics` | **none** | Simulation first; physical motion needs bounded mode, interlocks, clearance, human control |
| *(not offered)* | `medical` | **none** | Regulated intended use only; clinician approves patient-specific output; **no autonomous diagnosis, treatment, or instrument actuation** |

A ceiling of **none** means no project may be created in that pack at all.

## Explicit non-goals

- Sentient or human-equivalent general intelligence
- Unsupervised control of weapons, hazardous industrial systems, clinical treatment, surgery, vehicles, launch systems, or critical infrastructure
- Replacing licensed professional judgment, engineering sign-off, or certification authority
- Guaranteeing correctness from model output alone
- A universal UI replacing expert tools
- Unreviewed release, certification, manufacturing, construction, flight, or clinical use of agent-generated output

## Implementation carve-outs (honest scope)

These are deviations from the source PRD, recorded here rather than discovered
later.

| Area | Position |
|---|---|
| Heavy tool adapters (CAD kernel, SPICE, FEA, DICOM) | Declared in the capability registry, but **fail with `CONNECTOR_UNAVAILABLE`** where no real backend exists. Per RSN-06 and AGT-08, a fabricated solver result is the single most dangerous output this system could produce, so unavailability is reported, never simulated. |
| AUD-02 latency targets | Implementable, but **cannot be certified** without real users on real hardware in the target environment. Reported as *unverified*, never as passing. |
| Medical pack | Educational and device-concept scope only. Patient-specific use requires a separately validated deployment and is **not** enabled by this codebase. |
| Ceilings, not availability (2026-09-04) | A pack used to be available or not, and six of eight were not. That refused a whole domain because its *riskiest* action could not be gated: `mechanical` was closed because this build cannot gate drawing release (R3), which also closed concept CAD, renders and revisions (R1) — the work this product exists to do. Packs now carry a **tier ceiling** instead. The boundary is unchanged (nothing reaches R2 in an engineering pack) and is now enforced where risk actually lives — per action, via `tools.Grant`, at the moment the work is attempted rather than at the door. `Requires` reads as *what would raise the ceiling*. |
| Industry list (2026-09-04) | The nine engineering packs plus `general` are the industries the product's selector offers, and the pack table is the single producer of that list. Packs the selector does **not** offer (`software`, `robotics`, `medical`) carry no industry label. `robotics` and `medical` remain uncreatable: neither is offered to users, and widening them is not something a change about industry coverage may do quietly. |
