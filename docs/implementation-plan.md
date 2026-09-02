# Finishing FORGE: what is left, in the order it has to happen

**Written:** 2026-09-02 · **Status:** live — updated as waves land

This exists because "implement everything" is several weeks of work and the worst
possible response to it is fifteen half-built features. Each wave below closes on
its own: it ships with fences that go red when it breaks, and nothing later
depends on a wave being *partly* done.

Requirement IDs are from `docs/prd.md`.

---

## Where the line is today

Built and fenced: the durable engine, the agent loop, tools, identity, the
console, the workbench, and the audit chain. Roughly the whole vertical from
"someone speaks" to "a worker executes a verified task", plus the record of it.

Missing: almost everything that makes it a *platform* — memory, collaboration,
the project graph, enterprise security, and the release apparatus.

The split matters when planning: the core loop is done, so every wave below adds
surface to a working system rather than filling a hole in one.

---

## The dependency order, and why it is this order

```
        ┌─────────────────────────────────────────────┐
        │ WAVE 1  audit chain (SAF-06)          DONE  │
        └───────────────────┬─────────────────────────┘
                            │  everything below is auditable because of it
        ┌───────────────────▼─────────────────────────┐
        │ WAVE 2  claims and units              DONE  │
        │   RSN-05 epistemic labelling                │
        │   WRK-05 unit integrity, per value          │
        └───────────────────┬─────────────────────────┘
                            │  MEM stores claims; VIS renders units
             ┌──────────────┴───────────────┐
             ▼                              ▼
  ┌──────────────────────┐      ┌───────────────────────────┐
  │ WAVE 3  memory       │      │ WAVE 4  workspace model   │
  │   MEM-01 layers      │      │   RSN-01 goals/constraints│
  │   MEM-02 user control│      │   WRK-03 project graph    │
  │   MEM-03 decision log│      │   WRK-04 artifact lifecycle│
  └──────────┬───────────┘      └─────────────┬─────────────┘
             └──────────────┬─────────────────┘
                            ▼
        ┌─────────────────────────────────────────────┐
        │ WAVE 5  containment                         │
        │   SEC-03 secret handles                     │
        │   SAF-07 incident response                  │
        │   NFR-07 recovery drills                    │
        └───────────────────┬─────────────────────────┘
                            ▼
        ┌─────────────────────────────────────────────┐
        │ WAVE 6  enterprise + collaboration          │
        │   SEC-02 SSO / MFA / device trust           │
        │   COL-01 multi-user room                    │
        │   COL-02 handoff                            │
        └───────────────────┬─────────────────────────┘
                            ▼
        ┌─────────────────────────────────────────────┐
        │ WAVE 7  visual completeness                 │
        │   VIS-04 variants side by side              │
        │   VIS-05 parametric export                  │
        └───────────────────┬─────────────────────────┘
                            ▼
        ┌─────────────────────────────────────────────┐
        │ WAVE 8  release (README phase 7)            │
        │   evaluation suites · release pipeline      │
        └─────────────────────────────────────────────┘
```

Two edges are load-bearing and worth stating:

**Wave 1 first** because an audit chain is the one thing that must exist *before*
the events it attests to. Retrofitting it later means every event written in
between is permanently unattestable — the exact gap this deployment now carries
for its pre-migration rows.

**Wave 2 before 3 and 4** because memory stores claims and the workspace model
stores constraints, and both need to say *how a thing is known* before they store
it. Adding the vocabulary afterwards means rewriting every row.

---

## Wave 1 — audit chain · **DONE**

`SAF-06`. Per-goal hash chain over `forge_events`; `forgectl audit verify`.
Fifteen fences, seven against live Postgres performing real UPDATEs and DELETEs.
Tamper-evident, not tamper-proof, and it says so.

## Wave 2 — claims and units · **DONE**

**RSN-05 epistemic labelling.** The PRD names seven ways a thing can be known:
`observed / retrieved / calculated / simulated / inferred / assumed / proposed`.
Today exactly one edge of that exists — the standards detector separating
*recalled* from *chosen* (`internal/agent/standards.go`). The work is to make the
vocabulary explicit, attach it to every claim FORGE emits, and render it.

**Done.** `internal/agent/epistemic.go`. Closed vocabulary, derived ledger,
rendered per turn. Twelve fences. Verified live: a real turn produced retrieved,
assumed and proposed claims, and no observed, calculated or simulated ones.

**WRK-05 unit integrity.** Units are declared once per assembly
(`"units": "mm"`) rather than per value. The Zoo spike showed the alternative —
every number carrying its unit (`60mm`, not `60`) — and why it matters: a
dimension that travels without its unit is a dimension that will eventually be
read in the wrong one. Also needs coordinate frame, precision, and tolerance.

**Done.** `internal/agent/units.go`. `Quantity` cannot be rendered without its
unit; an unrecognised unit is recorded as unspecified rather than guessed at, and
every dimension then says so. Seven fences. Verified live: parts render as
`60 mm × 60 mm × 3 mm`, not `60×60×3 mm`.

Not done in this wave, and carried to Wave 4 with the workspace model: tolerance,
calibration and timestamp per value. `Frame` exists with one value, named rather
than implied.

## Wave 3 — memory

`forge_memory` **already exists in schema** (0004) with zero Go code, so MEM-01
is a service over an existing table rather than a new design.

- **MEM-01** layered: turn / session / project / org / personal, with distinct
  retention and sharing. Scope column is already there.
- **MEM-02** user-editable: inspect, correct, pin, expire, export, delete, and
  **show why an item was retrieved** — the `source` column exists for this.
- **MEM-03** decision log: date, author, alternatives, rationale, evidence,
  affected artifacts, supersession. Needs a table.

*Closes when:* retrieval shows its reason, a user can delete an item and it stays
deleted, and a decision can supersede another with both readable.

## Wave 4 — workspace model

- **RSN-01** goals, requirements, constraints, assumptions, decisions, risks and
  success criteria as an editable structure **separate from the transcript**.
  Today they exist only as prose inside replies.
- **WRK-03** project graph across requirements, components, interfaces, files,
  tests, hazards, decisions, owners, evidence.
- **WRK-04** artifact lifecycle: every change identifies initiator, agent, tool,
  inputs, diff, verification state, human disposition.

The largest wave. WRK-04 is where the audit chain pays off — the events exist,
they just have nothing to point at yet.

## Wave 5 — containment

- **SEC-03** secret handles: the model receives scoped handles, never raw
  secrets. Touches `internal/tools` and the executor.
- **SAF-07** incident response: stop, revoke, quarantine, roll back, preserve
  evidence, notify, review. `forgectl` verbs over the existing engine.
- **NFR-07** recovery drills: prove degradation preserves state and never implies
  completion. This is the drill harness the README's phase 7 calls for.

## Wave 6 — enterprise and collaboration

- **SEC-02** SSO, MFA, device trust, RBAC. Largest external surface.
- **COL-01** multi-user voice room with identified speakers and a record of who
  approved what.
- **COL-02** handoff: state, actions, versions, approvals, evidence, open risks,
  recommended next work.

COL-01 needs realtime multi-party transport, which nothing in the current
architecture has. Budget for that specifically.

## Wave 7 — visual completeness

- **VIS-04** variants side by side, each linking geometry version, inputs, units,
  assumptions, generator, verification status.
- **VIS-05** parametric export. The spike settles the shape: no CAD kernel here,
  so this is a `CONNECTOR_UNAVAILABLE` boundary plus mesh export, unless the Zoo
  decision is revisited (`docs/spikes/2026-09-02-zoo-text-to-cad/`).

## Wave 8 — release

Evaluation suites, recovery drills wired to CI, release pipeline. The README
already calls this phase 7.

---

## Carried defects

- **Planner latency.** ~180 s observed against a 3 m 15 s handler budget; one run
  came within 15 s of the ceiling. If it trips, the caller gets an error and a
  draft goal with no tasks. Either raise the budget, stream the planner, or make
  the draft resumable — currently there is no replan command.
- **Pre-migration events are unattestable.** 11 events on the dev database
  predate the audit chain and always will. Expected, reported, never backfilled.
