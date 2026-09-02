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
console, the workbench, the audit chain, layered memory with the decision log,
and the workspace model — the project graph and the artifact lifecycle. Roughly
the whole vertical from "someone speaks" to "a worker executes a verified task",
plus the record of it, what it remembers afterwards, and what it thinks is true.

Missing: containment, collaboration, enterprise security, and the release
apparatus.

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
  │ WAVE 3  memory  DONE │      │ WAVE 4  workspace   DONE  │
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

## Wave 3 — memory · **DONE**

`forge_memory` had existed in schema since 0004 with zero Go code, so MEM-01 was
a service over an existing table rather than a new design. Migration `0006_memory`
widened it and added the decision log.

**MEM-01 layered memory.** `internal/domain/memory/model.go`. Five layers, as a
table rather than a switch: turn (15 m), session (7 d), project, personal and org
(durable), each with its owner and its audience. 0004 shipped three of the five;
the two that were missing were the SHORT-lived ones, which is the half that
matters most — without them every passing detail either goes into project
knowledge, where it outlives its relevance and then misleads, or is not written
at all.

Retention is enforced by the READ, not by the sweep. A deployment whose sweep has
not run for a week still must not serve week-old turn context as current, and
`liveClause` in the repository is why it does not. The sweep reclaims space; a
deployment that never runs it is slower, not wrong.

**MEM-02 user control.** Inspect, correct, pin, expire, export, delete — and show
why an item was retrieved. The reason is DERIVED from the predicate that matched
(exact key, prefix, pinned, layer) rather than written by the caller, for the same
reason the claim ledger is derived: a reason composed by the code that wanted the
item is a story, not an answer.

The load-bearing one is deletion. FORGE writes memory on its own initiative, so a
plain DELETE would be undone the next turn it observed the same thing, and nothing
would report it. A forgotten item therefore keeps its row and its key and loses
its value: the key goes on occupying the layer's unique index, and a later write
is refused with `MEMORY_FORGOTTEN`. Purging re-opens the key, is a separate act,
and is logged at WARN.

**MEM-03 decision log.** New table `forge_decisions`: date, author, alternatives
with why each was rejected, rationale, evidence as `[]claim.Claim`, affected
artifacts, supersession. Superseded, never edited — "we changed our minds" is
itself a decision with a date, and editing the old row would erase the fact that
the old answer was ever believed. A decision may be superseded at most once, so
"what do we currently believe?" has exactly one answer; that is enforced twice,
by the service and by a unique index, so a race loses rather than splitting the
chain.

**The enabler.** The epistemic vocabulary moved from `internal/agent` to
`internal/domain/claim`. Memory and the decision log both store claims and both
sit underneath `internal/agent` in the import graph, so it could not stay where it
was. The alternative was a second copy of the seven categories, which is how a
closed vocabulary stops being closed. All twelve wave-2 fences ran unchanged
against the moved code, which is what makes it a move rather than a rewrite.

**Mounted, not merely built.** `memory_recall` and `memory_remember` are
registered tools in `cmd/forge-worker`, so recall and write-back go through the
same registry, ledger and timeline as every other capability. Two rules are
enforced there:

- FORGE is not asked how it knows something. There is no `how` input; a fact the
  model chose to write down is recorded as `inferred`, because that is what it
  is. The wave-2 lesson applied at the one place it would be easiest to break.
- FORGE may write turn, session and project memory only. Personal preference is
  the user's to state, and one goal's conclusion must not become a fact for every
  project in the deployment.

**Surfaces.** `/v1/memory*` and `/v1/decisions*` for the user (MEM-02 is a user
requirement, not an operator one), and `forgectl memory` / `forgectl decisions`
for the operator.

**54 fences**, 39 against live Postgres. Nine were drilled by mutation and all
nine went red. Verified live end to end on the dev database: a project memory was
written by the tool path, forgotten by a user through `forgectl`, and the agent's
next attempt to write the same key was refused by name.

### Two defects found by building it

- **`Contract.InputSchema` is not validated anywhere.** `tool.go` says inputs are
  "already validated against InputSchema"; in this build `registry.go` only hands
  the schema to the model provider. So `"additionalProperties": false` bought
  nothing, and a model sending `how` at `memory_remember` had it discarded in
  silence — the safe label was stored and the model went on believing it had
  recorded a measurement. The memory tools now decode strictly and refuse. **The
  general defect is untouched and affects every tool** — see Carried defects.
- **`forgectl memory forget <id> --as <user>` ignored its flags.** Go's `flag`
  stops at the first positional, so `--as` was never parsed and the command
  failed with a usage error naming the flag that had been supplied. Fixed to the
  shape `forgectl approve` already used. Found by running the command, not by a
  test — nothing in the suite invokes the CLI's argument parsing.

### Carried to wave 4

`Item.Value` is opaque JSON. Once WRK-03's project graph exists, memory should be
able to point at a component or a requirement rather than hold a key that happens
to name one.

## Wave 4 — workspace model · **DONE**

Migration `0007_workspace_model`, plus `0008_schema_scoped_triggers` to repair a
defect this wave uncovered (below).

### The decision that made this tractable

The plan called this the largest wave. It was, until the two requirements were
read side by side:

- **RSN-01** names goals, requirements, constraints, assumptions, decisions,
  risks, success criteria.
- **WRK-03** names requirements, components, interfaces, files, tests, hazards,
  decisions, owners, evidence.

Both lists contain **requirements** and **decisions**. Two systems would hold two
copies of each from the first day and they would disagree as soon as anybody
edited one — and each system would be blamed for it separately. So it is **one
graph**: a node table with a closed kind vocabulary, a typed edge table, and a
table saying which edge kinds may connect which node kinds. RSN-01 is the
reasoning half of the vocabulary, WRK-03 the structural half plus the edges.

WRK-04 joins the same graph rather than sitting beside it, because WRK-03's
"files" and WRK-04's "artifacts" are the same thing seen from two angles.

### Anchors, and why the graph has real foreign keys

Goals, decisions, owners and artifacts already have tables. A node is therefore
one of two things: **owned** (the graph holds the content) or an **anchor** (the
content lives elsewhere and the row is an identity peg with a real FK and a real
cascade).

The alternative was polymorphic `(kind, id)` endpoints, which no database can
enforce: every edge to a deleted goal would survive as a pointer to nothing and
the graph would fill with them. Anchoring costs one find-or-create on a necessary
path and buys referential integrity for the whole graph. Fenced: deleting a goal
takes its anchor and every edge touching it.

### RSN-01 — a structure separate from the transcript

Ten owned kinds and four anchors. Risks and hazards are kept apart because the
PRD names both and they are different things — a hazard is the sharp edge, a risk
is the chance somebody touches it.

**A node's kind never changes.** An assumption that turns out to be true does not
become a requirement; a requirement is created that `derives_from` it, and both
stay readable. The entire value of having labelled something an assumption is
that somebody can later ask what was built on top of a guess, and mutating the
kind erases the question. There is no `kind` field on the edit endpoint and no
`UpdateKind` in the repository.

Each kind declares which epistemic labels it may carry (PRD RSN-05). An
assumption may only be `assumed`; evidence may not be `assumed` at all. That is
the claim vocabulary from wave 2 earning its keep a third time.

### WRK-03 — typed edges

Eight relations, each with a rule about which kinds it may connect. The point is
that "test verifies requirement" and "requirement verifies test" are not both
sentences: a graph that accepts either has an arbitrary direction on every edge,
and every query over it is wrong half the time.

`satisfies` and `verifies` are separate relations, for the same reason AGT-08
keeps completion and verification apart on tasks: building a thing is not
checking it.

### The review — where a graph stops being decorative

`forgectl graph review` reports what a project's graph **contradicts** and what
it **lacks**, and the two are separate lists:

- **Defects** are contradictions. A `depends_on` cycle; something *accepted*
  whose every input is an assumption — a commitment that reads as settled and
  rests on nobody having said otherwise.
- **Gaps** are absences: nothing verifies this requirement, nothing mitigates
  this risk, nobody owns this component.

Only defects affect the exit status. Every real project has gaps, and a check
that is always red is a check somebody turns off in a week — the same reasoning
that keeps pre-chain events out of `audit verify`'s findings.

### WRK-04 — the artifact lifecycle, and the audit chain's payoff

"Every change identifies initiator, agent, tool, inputs, diff, verification
state, human disposition." Read literally: a version missing any of the seven is
refused rather than stored with a blank.

**Verification state and human disposition are different columns, forever.** One
is what a machine found; the other is what a person decided. A single column
would eventually be set to "accepted" by a passing test suite, and the row would
then assert that somebody signed off on something nobody looked at (PRD SAF-05).
`Usable()` requires both and says which is missing.

Versions append; the current one is derived from the version numbers rather than
stored in a flag, so there is no second source of truth to disagree. A pending
version becomes `superseded` when a newer one lands; one a person already ruled
on is left alone, because tidying a queue must not erase a human decision.

The plan said this is where the audit chain pays off, and it is: a change writes
its timeline event and its version **in one transaction**, and the version points
at the event. Fenced both ways — the chain still verifies over an artifact
change, and a refused change writes neither.

### Surfaces

`/v1/workspace/*` for the people doing the work (RSN-01 asks for an *editable*
structure), `forgectl graph` and `forgectl artifacts` for the operator.
`graph review` exits 1 on a contradiction, so it belongs in cron beside
`audit verify`.

Note what has no endpoint: **recording an artifact version**. WRK-04's seven
include the tool call that made the change, and a client naming one would be
putting a fabricated row in the idempotency ledger. Only the disposition — what a
person decided — is exposed.

**48 fences**, 30 against live Postgres, 8 drilled by mutation. Two of those
drills came back green and both fences were rewritten; see below. Verified live
end to end on the dev database: a graph built through the service, reviewed
clean, an artifact change recorded, and `audit verify` still intact over it.

### Three defects found by building it

- **Trigger guards were not schema-scoped**, so every integration test in this
  repository has been running against a schema with no `updated_at` triggers, and
  wave 4's artifact foreign key was missing too. Production was never affected —
  it has one schema. Fixed by 0008 and by dropping the guard pattern entirely.
  Full write-up: `docs/bugfix/2026-09-02-trigger-guards-were-not-schema-scoped.md`.
- **Two fences did not fire under mutation.** `TestTwoSchemasGetTheSameObjects`
  compares two schemas, and the bug broke both identically — so it was replaced
  by a rule-based check. The edge-pairing fence was held by the FROM list alone,
  so disabling the TO list left it green; assertions were added for each half
  separately. Both now go red.
- **Changing a node's kind reported the wrong reason.** A caller changing a kind
  usually changes the epistemic label with it, and validation ran first — so they
  were told "a requirement cannot be assumed", a true statement about the wrong
  problem, and met the real refusal only on the second attempt. The kind is now
  checked first, against the stored row. Found by running the thing: the fence
  changed the label too, so it took the path that already worked.

### Carried forward

- **WRK-05's remainder** — tolerance, calibration and timestamp per value — is
  still open. Wave 2 shipped units, precision and a named frame; the workspace
  model gives those values somewhere to live, but nothing populates them yet.
- **Memory items still hold opaque JSON.** Wave 3 carried this here: a memory
  item should be able to point at a component or a requirement rather than a key
  that happens to name one. The anchor mechanism now makes that a small change,
  and it was not made — no consumer asks for it yet.

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
- **Tool inputs are not validated against their declared schema.** `Contract`
  documents `InputSchema` as checked before a tool runs and `Invocation.Input` as
  "already validated"; nothing in this build does it. The schema reaches the model
  provider (`registry.go`) and no further, so an unexpected field arrives at
  `Run` and `encoding/json` discards it silently. Every tool is affected. The two
  memory tools defend themselves with a strict decoder; `workspace` and `shell` do
  not. Fixing it properly means either a schema validator in the executor or
  strict decoding as a rule, and it is a decision about every tool rather than
  about memory, so it was left rather than widened into.
- **`updated_at` is maintained by a trigger nothing asserts.** The trigger is now
  attached in every schema, and no test checks that a row's `updated_at` actually
  moves when something updates it without setting it by hand. The one path that
  relies on this is `identity.MarkEmailVerified`.
- **Nothing exercises the CLI's argument parsing.** The `memory forget` flag bug
  was found by running the binary; the test suite calls handlers and services and
  never `forgectl` itself. Two commands still have the same shape by convention
  rather than by a fence.
- **Organisation memory has no audience to enforce.** `Visibility` declares it and
  says so; there is no membership model until SEC-02/COL-01. Personal and project
  scoping ARE enforced, and `/v1/memory/layers` reports which is which rather than
  letting a client assume.
