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
the workspace model, containment — secret handles, incident response, and
recovery drills that inject real faults — role-based access with second factors,
the shared-session record and the handoff, and geometry that survives the turn
that proposed it: variants side by side, and mesh export that states what the
conversion cost. Roughly the whole vertical from
"someone speaks" to "a worker executes a verified task", plus the record of it,
what it remembers, what it thinks is true, and what happens when it breaks.

Missing: SSO. Rooms are live and usable in a browser, participants hear each
other, what they say is transcribed into the record, the privacy controls over
that are enforced server-side, and FORGE speaks in the room and stops when
somebody interrupts it (waves 9 to 9.6).
Everything else named in the PRD is
built, fenced, and released by one command.

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
        │ WAVE 5  containment                   DONE  │
        │   SEC-03 secret handles                     │
        │   SAF-07 incident response                  │
        │   NFR-07 recovery drills                    │
        └───────────────────┬─────────────────────────┘
                            ▼
        ┌─────────────────────────────────────────────┐
        │ WAVE 6  enterprise + collab   PARTLY DONE   │
        │   SEC-02 RBAC/MFA/devices ✓  SSO deferred   │
        │   COL-01 the record ✓  transport deferred   │
        │   COL-02 handoff ✓                          │
        └───────────────────┬─────────────────────────┘
                            ▼
        ┌─────────────────────────────────────────────┐
        │ WAVE 7  visual completeness           DONE  │
        │   VIS-04 variants side by side ✓            │
        │   VIS-05 mesh export ✓  parametric refused  │
        └───────────────────┬─────────────────────────┘
                            ▼
        ┌─────────────────────────────────────────────┐
        │ WAVE 8  release (README phase 7)      DONE  │
        │   evaluation suites ✓  drills in CI ✓       │
        │   release pipeline ✓                        │
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

## Wave 5 — containment · **DONE**

Migration `0009_containment`.

### SEC-03 — secret handles

"Model receives scoped handles, not raw secrets." Read literally: the model is
told `secret://github_token` exists and what it is for, it writes that string
wherever the value belongs, and the executor substitutes at the tool boundary —
if and only if that tool is granted that secret.

**The decision worth stating: FORGE brokers secrets, it does not store them.**
The table holds a declaration — name, project, which tools may receive it, and
the environment variable the value is read from. No value lands in Postgres.

The alternative, encrypting values at rest, defends exactly one case (a stolen
backup) and costs three: a key that lives in the same environment an attacker
with the database usually also has; a rotation and re-encryption path; and, most
of all, it makes FORGE a place worth attacking for credentials rather than a
thing that borrows them. Private deployments already run under systemd,
Kubernetes or a vault agent, all of which put secrets in a process environment.
`source` is an enum with one value so a deployment that *does* want FORGE to hold
values has somewhere to add it — with SEC-02's key management in wave 6.

**The half that is easy to forget is the one the mechanism rests on.**
Substituting on the way in is worthless alone, because the tool's output goes
back to the model: a shell that echoes its environment, an HTTP client logging
the request it sent, an error quoting the header it choked on. Each round-trips
the value into the context window and from there into the ledger. So every
resolved value is scrubbed from the tool's output, raw output and error text
before either the model or the database sees it — matching not just the literal
value but the encodings something in the path actually applies (percent, JSON
escape, base64, basic-auth base64). A value that survives anyway is caught by a
second check, and the whole result is discarded rather than handed over: losing a
tool result is recoverable, the other is not.

What is deliberately NOT claimed: a tool legitimately given a value can still
send it somewhere. Scoping decides who receives a credential, not what they do
with it. That is SEC-05, and it is not implemented.

### SAF-07 — incident response

Seven verbs — stop, revoke, quarantine, roll back, preserve evidence, notify,
review — with **one ordering rule that is enforced**: evidence is preserved
before anything destructive. A response that stops the goal, revokes the
credential and rolls the artifact back, then gathers evidence, has gathered the
evidence of its own response. A dry run is always allowed, because it changes
nothing and rehearsing before capturing is exactly right.

"Preserve evidence" preserves something: goal status, task counts, open
approvals, the live handle names, and **the audit chain's verdict at capture
time** — which matters more than it looks, because once the response has written
its own events, "was the chain intact when this started?" is no longer answerable
by running the verifier. The snapshot names handles but never the environment
variables behind them: an incident record is read by several people, and one that
named the variables would be a map.

Three states, not two: `contained` is real, because "the bleeding stopped" and
"we understand what happened" are different days. Containment is refused while
nothing destructive has actually been done — it is a claim about the world, not
about the record. Closing requires a review, in the database as well as the
service.

### NFR-07 — recovery drills

"Preserve state, stop dependents safely, expose partial results, never imply
completion." Four properties that are true right up until something breaks, which
is when nobody is watching. A unit test cannot check them after a fault, because
the fault has to be real.

So `forgectl drill run` injects real faults into real schemas: a worker that dies
holding a lease, a dependency that fails terminally, a goal that settles with
work incomplete, a checkpoint that becomes undecodable. Four scenarios, sixteen
invariants.

**The rule that makes a drill worth running:** a scenario must PROVE its fault
landed before it may assert anything. The failure mode of every drill harness is
an injection that silently did nothing, after which every invariant passes and
the report is a page of green ticks about a system nobody disturbed. So
`FaultInjected` is not a boolean the scenario sets — it carries the evidence, and
a scenario without it is reported VACUOUS and **fails the run**. An empty
selection fails too.

Exit 0 only when every drill injected its fault and every invariant held, so it
belongs in the release checklist beside `audit verify` and `graph review`.

### Surfaces

`forgectl secrets`, `forgectl incidents` and `forgectl drill`. All three are
operator acts: declaring a handle is paired with exporting a variable where the
service starts, responding to an incident is done by whoever holds the pager, and
a drill builds its own schemas. **No HTTP surface was added** — see Carried
forward.

**33 fences** plus 4 drill scenarios carrying 16 invariants; 8 drilled by
mutation, all 8 red. Verified live end to end: a handle declared and granted, a
stop refused before evidence and accepted after, and a closure refused without a
review.

### Two things the drills found about themselves

- **The first drill run failed on the drill, not the system.** The
  worker-death scenario gave its task one attempt, so a dead worker meant
  terminal failure — correct behaviour, and not recovery. And its "completion is
  not implied" check tested `ended_at == nil`, which conflates *not complete*
  with *not ended*; a failed task has legitimately ended. Both were the
  scenario's fault. Fixed, and the drill now covers both paths — and asserts the
  thing the engine does well: a crashed worker is recorded as
  `LEASE_EXPIRED_ATTEMPTS_EXHAUSTED` rather than as the work being wrong, so
  nobody debugs the instruction when the infrastructure died.
- **A mutation drill reported NOT A FENCE twice, and both were the checker.**
  Once because a drill that ERRORs is red and the grep only looked for FAILED;
  once because `go test -run TestExecutorSecrets` matched no tests and exited 0 —
  the executor's redaction path had no fence at all. That gap was real and is now
  closed by `internal/agent/secrets_test.go`.

### Carried forward

- **No HTTP surface for incidents.** Response is an operator act and the CLI is
  the honest surface for it, but "report an incident" is plausibly a user act and
  there is no way to do it from the API today.
- **SEC-04 and SEC-05 are untouched.** Prompt-injection defence (tool output and
  imported results as untrusted input) and exfiltration controls are named in the
  PRD and were not in this wave's scope. SEC-05 in particular is the missing half
  of SEC-03's honesty: scoping says who gets a credential, and nothing yet says
  where it may go.
- **A revoked secret does not stop a task already holding its value.** Revocation
  takes effect at the next resolution. Stopping work mid-flight is the `stop`
  verb's job, and the two are not wired together.

## Wave 6 — enterprise and collaboration · **PARTLY DONE**

Migration `0010_rbac_and_collaboration`. Two of this wave's parts were
deliberately not built; they are named below with what it would take.

### RBAC — the change that unblocked earlier waves

Until now, every authorisation check in the codebase was the same line:
`where p.owner_id = $caller`. One owner, no members, no roles. That is why
memory's organisation layer shipped documented as "declared, not enforced" —
there was no membership model to enforce it with.

**The decision: membership is the single source of truth for access.**
`forge_projects.owner_id` still records who CREATED a project and is no longer
consulted. Keeping it as a second authorisation path would mean two answers to
"may this person read this", and the day they disagree is the day somebody sees
something they should not. Migration 0010 backfills an owner membership for every
existing project, so nothing loses access when the wave lands — fenced against a
project inserted the old way.

Four roles against nine permissions, as a table, printed verbatim by
`forgectl access matrix`. The grid is the artefact people audit, so it is
oriented by permission: "who can decide approvals" is one line to scan.

Three separations are asserted by name, because each is a product decision a
future edit could quietly undo:

- a **contributor** creates work and signs nothing off (SAF-05);
- **planning and starting** are different permissions (AGT-02);
- a **maintainer** runs the work and cannot change who has access.

A project's last owner cannot be removed or demoted. A project with no owner
cannot be administered at all — not even to undo the change that emptied it — so
it is refused inside the same transaction as the write.

`TestNoHandlerAuthorisesByOwnerID` parses the HTTP package's string literals and
fails on any query that authorises by `owner_id`. It parses rather than greps
because the first version flagged the comments explaining the rule, which is the
kind of false positive that gets a fence deleted.

### MFA, and the two hazards it is shaped around

**Lockout.** The obvious design enables a factor the moment somebody enrols,
which locks out every user whose authenticator did not end up with the same
secret — and they cannot sign in to fix it, because fixing it needs a code. So a
factor is `pending` until one correct code proves the enrolment, and only an
`active` factor is ever demanded.

**Device trust as a bypass.** "Remember this device" is a way to opt out of MFA
unless granting it *requires* the second factor at that moment. So trust is
granted by the same call that verifies a code and by nothing else; it expires
after thirty days; and disabling the factor untrusts every device, because a
device trusted under a factor that no longer exists is trusted on the strength of
nothing.

TOTP is thirty lines against RFC 6238's published vectors rather than a
dependency on the authentication path. Codes are single-use inside their own
window — the accepted step is recorded, so a code somebody saw over a shoulder
does not work for the next ninety seconds.

The shared secret is stored, unlike wave 5's brokered credentials, because it
belongs to the user and there is no environment variable an operator could put it
in. It is encrypted with AES-256-GCM (`internal/platform/secretbox`). The honest
claim: this defends a stolen database. An attacker with the database AND the
process environment has both halves.

### COL-01 — the record, not the transport

What is built is who was present, who said what, and which approvals were made
while they were. **Every turn names its speaker**; there is no anonymous option
and no default, enforced in Go and again by a check constraint. FORGE's own turns
are a distinct speaker kind rather than a null one, because "nobody said this"
and "FORGE said this" must not look the same (AUD-05).

Speaker names are recorded AS THEY WERE. A transcript that rendered them by
joining to the accounts table would show a renamed or deleted account's current
state, which is not what was said in the room.

**What is not built: realtime multi-party audio transport.** The plan flagged it
as needing its own budget and it does. The record is transport-agnostic — a turn
arrives with a speaker and text, and where it came from is a field — so a WebRTC
bridge, a phone gateway and somebody typing all write the same row. A transcript
is useful long before its audio is, and it is the part an auditor asks for.

### COL-02 — the handoff

"State, actions, versions, approvals, evidence, open risks, recommended next
work." Every one of those already existed: waves 1–5 built the goal state
machine, the timeline, the artifact lifecycle, the approval gates, and the
graph's risks and evidence. So a handoff is **derived, never stored** — storing
one would create a second truth that goes stale the moment anything moves, which
is the worst possible property for a document whose whole purpose is to be
believed by somebody who was not there.

It inherits NFR-07's rule: a handoff must never imply completion. It leads with
what is unresolved, counts it, and `Complete()` is false while anything is
outstanding — including a goal that has not ended. Recommendations are derived
from state rather than asked of a model, and the document says so.

**48 fences**, 24 against live Postgres, 11 drilled by mutation. Verified live:
the permission matrix through the real binary.

### Two parts NOT built, and why

- **SSO.** OIDC/SAML against an external identity provider. I have no IdP to
  verify against, and this codebase's own history is that a fake matching the
  provider hides real defects. Shipping an unverified authentication path would
  be worse than not shipping one. What it needs: a real IdP tenant to test
  against, and it is then a self-contained piece of work.
- **Realtime multi-party audio transport** (COL-01's other half), as above.
  Wave 9 has since built the live session spine; the media plane is still open.

Both are named here rather than quietly folded into "done".

### One thing the drills found

Two mutation drills on the turn-attribution rule came back green, which looked
like a missing fence and was not: the property is enforced in Go AND by a check
constraint, so removing either leaves the other. Removing both goes red. The
fence itself was still improved — the original test's cases were all caught by
the label half, so a case that only the id half can refuse was added.

## Wave 7 — visual completeness · **DONE**

### What had to exist first: something to compare

Geometry lived only in the browser tab. Ask for a bracket, ask for a taller one,
and the first was gone — so VIS-04 had no "variant" to put beside another and no
"geometry version" to link to. The wave therefore starts with persistence, and
the decision that shapes everything else is what it persists *into*.

**A variant IS an artifact version.** Migration `0011_geometry` adds no variants
table. Five of the six things VIS-04 requires a render to link to are already
columns on `forge_artifact_versions` (WRK-04): version, inputs, agent,
verification state, human disposition. Only the geometry itself had nowhere to
live, so `forge_geometry` is a 1:0..1 extension keyed on the version id — a row
exists exactly when a version has geometry, and its absence is a fact rather than
a gap. A `content` column on the shared table was rejected: a file artifact's
content lives on disk, so that column would be empty for nearly every row, and an
empty content column cannot be told apart from "the content is elsewhere".

The consequence worth stating: **comparison is derived, never stored**, the same
rule COL-02's handoff follows. A saved comparison goes stale the moment a variant
is verified or ruled on, and it is exactly the document somebody leans on to
choose between designs.

**Who writes geometry.** The server, inside `/v1/converse`, because that is the
only place that knows the prompt, the model and the shape at the same moment. A
client posting geometry would be naming its own generator and its own inputs —
a fabricated provenance record, the same reason `RecordChange` is not on the HTTP
surface. There is no `POST /v1/geometry`.

**A new actor.** `planner | executor | verifier | human | scheduler | system` was
written when every actor was part of the goal engine. The workbench conversation
is none of them: `human` would credit a person with a shape FORGE drew, and
`system` would attribute a proposal to infrastructure. `converse` was added to
both places that spell the vocabulary out, because a version and its event are
documented as agreeing about who acted.

### VIS-04 — variants side by side · done

`internal/domain/geometry`. Comparison in the API, in `forgectl geometry
compare`, and in the workbench as N live viewports with each column's provenance
directly beneath it.

Two refusals carry the requirement's weight:

- **Units are converted before anything is called a difference.** 60 mm and 6 cm
  are the same length; 60 mm and a bare 60 are *not comparable*, and that pair
  goes in its own list rather than being reported as agreement. Calling them
  equal is the wrong answer with the most convincing appearance.
- **A part matched across variants by NAME says so.** Nothing in this system
  keeps a part id stable between turns — see the findings below — so that pairing
  is a guess, in a third list of its own. "These differ" is a finding, "these
  could not be compared" is a judgement withheld, and "these were matched by
  name" is a judgement qualified. Three lists, never merged.

### VIS-05 — export · done, and the boundary is the feature

The spike's shape held. STEP, IGES and KCL are **declared and refused** with
`CONNECTOR_UNAVAILABLE` and a reason — the same shape as the unavailable
connectors, and for the same reason: leaving them out is what invites tessellated
facets in a `.step` file, which everything downstream treats as an exact solid.

OBJ and STL are real. Every export states its cost as a number rather than an
adjective — a ⌀22 mm cylinder at the renderer's 40 segments is *"the exported
surface lies up to 0.034 mm inside the one described"* — and the exporter
tessellates with the renderer's own counts, fenced by a test that parses
`forge3d.js`. Defaulted dimensions, substituted shapes and an unmodelled tube
bore are all labelled as inference.

An assembly with no convertible unit is **refused**. On screen an unstated unit
survives because the number is printed beside `(unit not stated)`; in a
downloaded file the label cannot travel to the machine at the other end.

### Three things the wave found

**The exported mesh was inside out.** The box was wound outward and the cylinder
and sphere inward, in the same file. It renders correctly everywhere — the
viewport is handed each normal explicitly and draws with back-face culling off —
and misbehaves only at the point where somebody tries to make the thing. Found by
computing the signed volume of an exported OBJ, not by reading the code. Fixed
structurally rather than per-primitive: every facet is oriented against its own
analytic normal. `docs/bugfix/2026-09-02-exported-meshes-were-inside-out.md`.

**Matching parts by id does not survive a real conversation.** Propose a bracket,
say "make the base plate thicker", and the model renames every part id. The first
comparison rendered each part twice — once as "only in column 1", once as "only
in column 2" — which reads as two unrelated designs when what happened was a
revision. Now: ids first, then names among what the ids left over, with the basis
reported. The model is also asked to keep ids stable, and the matcher does not
depend on it doing so.

**`forgectl` had the same argument-parsing bug a third time.** Flags after a
positional id were silently ignored, which shipped twice before
(`docs/bugfix/2026-09-02-forgectl-memory-forget-ignored-its-flags.md`). The
carried defect said "nothing exercises the CLI's argument parsing" — that is now
false for `geometry export`, whose parsing is a separate function with four
fences over it. The two older commands still have the shape by convention.

### Fences

46 over the new code, 10 of them against live Postgres, **11 mutation-drilled** — every
one of the eleven went red when the property it claims was broken, and every
mutated file was restored. Including the two that catch what reading could not:
the signed-volume fence over exported solids, and the fence that parses the
renderer's tessellation constants out of the embedded asset.

### One thing this wave surfaced and did not decide

**A superseded variant cannot be accepted.** Proposing a new version of the same
assembly marks the previous one `superseded` automatically (the artifact
lifecycle's rule, correct for a file), and `SetDisposition` only touches rows that
are still `pending`. So a person who compares v1 and v3 and prefers v1 has no way
to say so: the comparison view will show them the choice and the disposition
endpoint will refuse it.

That is the artifact lifecycle (a linear history where the newest supersedes) and
VIS-04 (variants as alternatives you choose between) contradicting each other.
The comparison view states the situation rather than hiding it — the disposition
row says what `superseded` means and what to do instead — but the underlying
question is a decision, not an implementation detail. Options are in the carried
defects below.

## Wave 8 — release · **DONE**

### Evaluation suites — measuring the MODEL, not the harness

Everything else in this repository proves the harness is correct. Nothing proved
the model behaves acceptably inside it, and that is where the real damage has
come from: a fabricated NEMA 17 bolt pattern, dimensions travelling without their
unit, geometry offered with nothing said about what it does not establish, part
ids renamed between turns.

`internal/eval` runs fixed conversations against a real model and scores them
with deterministic Go. Four rules hold the whole package up:

- **Nothing grades its own homework.** Every scorer is Go over the reply. There
  is no model judging a model and no place to put one. Where a scorer needs a
  fact about the world — the published dimensions of a NEMA 17 face — that fact
  is written down in the suite with its source named, exactly as the Zoo spike
  wrote its reference figures into `analyse.py`.
- **No fakes.** `NewRunner(nil, …)` is refused. A stub returning canned replies
  would measure the stub, and this codebase has already been caught by exactly
  that.
- **The output is a RATE, never a pass.** The same prompt produced a correct
  standards figure and a fabricated one four runs apart. Each case runs N times
  and the report is k/n, with every reply kept so the scoring can be re-judged.
- **Floors are observations, not targets**, and each carries the measurement it
  came from. A floor with no measurement behind it gets lowered the first time it
  fails, because nobody can say why it was there.

Measured against qwen-plus, 4 repeats, 2026-09-03: standards labelled 4/4,
quoted NEMA 17 figures correct 4/4, convertible units 4/4, model-written
`not_verified` 4/4, no geometry on a scheduling question 4/4, speech short 4/4.

**And one finding.** Part-id stability is **1 of 4**. Wave 7 added a clause to
`converse.go` asking the model to keep ids stable across turns; it does not
reliably work — the model keeps the base plate's id and renames most of the rest.
So the comparison view's name fallback is **load-bearing, not a safety net**, and
now there is a number saying so.

That case is marked `Tracked` rather than floored, which is a distinction the
package makes explicitly: some properties are requirements and some are things
this build measures but does not require. A tracked scorer reports its rate and
never fails the run. Without the distinction it would sit permanently red until
somebody lowered a number to make the red go away, which is how every floor in a
suite eventually stops meaning anything. A fence refuses a scorer that is neither
floored nor explicitly tracked, so a zero floor cannot be acquired by accident.

### Recovery drills in CI · done

`forgectl drill run` existed since wave 5 and CI never ran it. It does now, on
every commit and inside the release gate. NFR-07 is a claim about what happens
when things BREAK, and nothing else in the suite breaks anything.

### The release pipeline · done

`make release-check` — formatting, vet, the whole suite against live Postgres,
the recovery drills, and a clean build. One command, run identically by a person
and by `.github/workflows/release.yml`; the workflow does not re-implement the
steps in YAML, because a workflow that lists its own checks drifts from the
Makefile and then a release passes a gate nobody can run locally.

`make dist` cross-compiles all three binaries for darwin and linux on amd64 and
arm64 — pure Go, CGO off, so a release is not something only one laptop can cut —
and writes `SHA256SUMS`.

`make dist-verify` checks **the artefact, not the recipe**: it runs the built
binary and reads the version it reports. A binary compiled without `-ldflags`
says `dev` and is indistinguishable from a release once it has left the machine.
Drilled by building `forgectl` without ldflags into `dist/` — the check goes red
and names the cause.

Every release's notes carry what the build cannot do, and point at the carried
defects below. A download page that reads as a finished product is its own kind
of false claim.

**`make db-wait` now asks the database, not Docker.** It checked the daemon and a
container by name, which is the right diagnosis on a laptop and the wrong
question in CI, where Postgres is a service with no such container — so every
target depending on it was unusable there, and `release-check` could not have
been one command. It probes the connection and falls back to the Docker
diagnostics only when the probe fails.

### Fences

22 over the new code, **11 mutation-drilled**. One drill came back green and the
fence was real: two phrases in the table both match the sentence, so removing
either leaves the other. Removing both goes red. Worth recording, because a
single-point mutation over defence in depth reports a working fence as vacuous —
the same shape as the wave 6 turn-attribution drill.

### The suite's own first defect

It fabricated two findings on its first live run, scoring correct model prose as
a fabricated standards figure — `"…42.3 mm square face with 31 mm spaced mounting
holes…"` had 31 mm matched to *faceplate width*, because the word `face` appeared
earlier in the sentence than `holes`.
`docs/bugfix/2026-09-03-the-evaluation-suite-fabricated-its-first-findings.md`.

The suite was written to hold the model to a standard of evidence and had to be
held to the same one first. A checker reaching for the nearest available word is
doing what a model does when it reaches for the nearest plausible figure.

### What evaluation does NOT cover

The **planner**. Whether it refuses to guess when a goal is underspecified, and
whether the tasks it emits are genuinely independent, needs a project, a goal row
and a database — a different harness rather than a longer list. Every observed
defect this suite was built from happened on the conversation surface. Named here
rather than papered over with a case that would not run.

---

## Wave 9 — the live session spine · **DONE**

COL-01 was built as a record and reachable only from `forgectl`: an operator
could read a transcript, and the people in the meeting had no way to be in one.
This wave makes a room something two browsers can share.

### What had to be settled first: what "audio transport" carries

Three shapes were possible — signalling only, signalling plus a server-side
recognition fallback, or a full WebRTC/SFU media plane. **Full media transport
was chosen.** It is the most expensive option and the one that makes AUD-03's
speaker separation structural rather than inferred, because each participant
arrives on their own RTP stream.

Both of its premises were proven before anything was built on them —
`docs/spikes/2026-09-03-webrtc-sfu/`:

- **pion/webrtc negotiates and forwards RTP**, pure Go, `CGO_ENABLED=0`. The
  cost is 20 newly linked modules in a repository that has three direct
  dependencies, recorded so it is a decision rather than a surprise.
- **Server-side ASR exists but not where expected.** The OpenAI-style
  `/audio/transcriptions` route returns 404 on the configured provider;
  transcription runs through `/chat/completions` with `input_audio`.

The spike also found a defect that changes wave 9.4's design: **the ASR drops
decimal points in realistic engineering sentences** — "two point five" comes back
as "two five", reproducibly, 5 of 5. Domain vocabulary alone does not fix it. It
transcribes a bare "zero point five" correctly and corrupts the same value inside
a sentence, so a naive smoke test passes. The system context is therefore a
correctness requirement, not a tuning parameter.

### What is built

Rooms over HTTP — open, list, read, join, leave, speak, close — and
`GET /v1/rooms/{id}/events`, a Server-Sent Events stream carrying turns and
presence to everybody in the room. SSE rather than a WebSocket, matching
`converse.go`: the traffic is one-directional and low-rate, and it needs no
second connection primitive.

**The hub is derived state and never authoritative.** Every write reaches
Postgres first and is published second. The inverse ordering — publish first so
the room feels faster — produces a room where somebody saw a turn that is not in
the transcript, which is the exact failure COL-01 exists to prevent. The hub
lives on the *service* rather than the handler, so a turn written by `forgectl`,
or later by the SFU, reaches subscribers too; publishing from the handler would
have given two answers to "was that delivered".

A subscriber that stops reading is **disconnected with a reason** rather than
waited for or silently skipped. A stalled browser must not slow down the person
talking, and a client told it fell behind re-reads the record — which is the
truth anyway. One that is silently skipped renders a transcript with a hole in it
and cannot find out.

Permissions reuse `PermProjectRead`; no new constant was introduced. A room is a
meeting, not project content. What that does not relax is consequence: approvals
made in a room remain gated by `PermApprovalDecide`. Being in the room is not
authority.

### One bug this wave found in itself

The first hub notified a lagging subscriber by sending a final event to its
channel after releasing the lock. A client closing itself in that window closed
the channel first, and the next send would panic with "send on closed channel" —
taking the server down, from nothing worse than a browser tab shutting at an
unlucky moment. The reason is now recorded in a flag and nothing is written to a
subscription outside the lock.

Its first fence was **vacuous** and said so only under mutation: a subscriber
reaches the lagging path only when its buffer is already full, and the drill
closed subscriptions long before 32 events accumulated, so the broken line never
ran. The fence now fills the buffer to exactly capacity first, and the mutation
panics as it should.

### Fences

**24 cases** across 12 test functions — 6 on the hub, 18 on the HTTP surface,
counting subtests, 9 of them against live Postgres.

3 were drilled by mutation and all 3 went red: the send-outside-the-lock panic,
the authorisation boundary (4 of 4 subtests), and route mounting — which catches
a handler that exists but was never wired, the one failure every handler test
would pass straight through.

---

## Wave 9.2 — the SFU media plane · **DONE**

Two people in a room now hear each other. `internal/media` forwards RTP between
participants; `POST /v1/rooms/{id}/media/offer` and `.../answer` carry the
signalling, and server-initiated offers ride the SSE stream wave 9 built.

### The exchange, and why it needs two directions

A participant offers once and the server answers. That establishes their
**uplink** and nothing more, because an answer can only describe media sections
the offer already contained. Their **downlinks** — one per other participant —
arrive by renegotiation, which the server initiates whenever the set of tracks a
peer should receive changes. That is every join and every departure.

```
client  POST /v1/rooms/{id}/media/offer   ──▶  uplink established
server  SSE  event: media-offer           ──▶  downlinks added
client  POST /v1/rooms/{id}/media/answer  ──▶  exchange closed
```

Server-initiated offers were the reason wave 9 needed an SSE stream before this
wave could exist. They also forced two pieces of care:

- **Downlinks are attached when the peer reports connected**, not before the
  answer is returned. Offering earlier races the answer over a different
  connection, and the client rejects a renegotiation for a session it has not
  finished establishing.
- **One exchange at a time.** WebRTC permits a single offer/answer in flight; a
  second is refused, and the track that prompted it would then never arrive —
  somebody in the room silently inaudible. An offer raised mid-exchange is
  deferred and sent when the peer is stable again.

### Identity is carried by the transport

Each forwarded track names its source: the track id is the connection, the
stream id is the person. A receiving client labels the speaker from the
transport rather than inferring it from audio. This is what makes AUD-03's
speaker separation structural — and it is the reason an SFU was chosen over a
mixer, which would have summed the streams and made attribution unrecoverable.

Unchanged and still true: separation is per CONNECTION. Four people around one
microphone are one speaker here. Diarization is not built and the docs do not
claim it.

### Signalling is addressed to a connection, never to a person

One person with two tabs is two peers with two different sets of tracks. Streams
therefore carry an id of their own (`stm_`, distinct from the `ses_` of an
authenticated session — a different thing with a different lifetime), minted by
the SSE stream and handed to the client in a new `hello` frame.

A caller may only drive a stream that is **theirs and in this room**. Both halves
are enforced: naming another member's stream would let you rearrange what they
hear or hang up their microphone, and a stream id learned in one room grants
nothing in another.

### Off by default

`FORGE_MEDIA_ENABLED` defaults to false. Turning it on binds a UDP port range and
begins accepting media; an upgrade that silently started doing that would be a
network change nobody asked for, on every deployment at once. Asking for audio
while it is off returns `MEDIA_DISABLED` and names the variable — kept distinct
from a media plane that is *on and broken*, because those need opposite
responses. A broken one is logged at ERROR and the server still serves rooms:
audio is an addition to the main path, and losing it must not take text with it.

Startup refuses a port range narrower than the room ceiling. That would otherwise
fail exactly when the room fills, and present as a media bug rather than the
configuration mistake it is.

### Fences

**9 cases.** 4 in `internal/media` against real peer connections — real DTLS,
real SRTP, real Opus payloads, nothing stubbed but the channel the renegotiation
offer travels on — and 5 on the HTTP surface.

3 drilled by mutation, all 3 red: NFR-04's ceiling (removing it admits the
twenty-first participant), stream ownership (weakening it to mere existence lets
one member drive another's audio), and room scoping (removing it lets a stream id
from one room be used in another).

`CGO_ENABLED=0` cross-compiles verified for linux/amd64, linux/arm64,
darwin/arm64 and windows/amd64, so `make dist` is unaffected by the new
dependency.

---

## Wave 9.3 — spoken turns in the record · **DONE**

What is said aloud in a room now lands in its transcript, attributed, through the
same `collab.Service.Say` a typed turn goes through. A spoken turn differs only
in its channel, so there are not two ways to write one down.

### Nothing is decoded, and that is a hard constraint rather than an optimisation

Transcription repackages the forwarded RTP into an **Ogg Opus** container and
sends that. The obvious alternative — decode to PCM — needs libopus through cgo,
which would break the `CGO_ENABLED=0` cross-compile `make dist` depends on
across four platforms. Verified against the real provider first: it accepts Ogg
Opus and returns the same text it returns for WAV.

### Segmentation, and what it is NOT

A segment closes when packets stop for `SilenceGap` (800 ms) or after
`MaxSegment` (15 s), whichever comes first.

This is **not** voice activity detection, and the docs say so rather than
implying an accuracy it does not have. Without decoding it can only watch packet
arrival. It works because WebRTC clients stop sending during silence, and the
maximum-duration rule is what keeps it from depending on that — a client that
streams comfort noise continuously still produces transcript, in fixed-length
pieces. **A sentence longer than `MaxSegment` is split across two turns.** That
was observed, not theorised: at a 5 s cap the acceptance test's own ~5 s utterance
came back as "…plus or minus." with the value missing.

### Failure here must never silence anybody

Transcription hangs off the media path, not in it. A provider that is down, a
queue that is full, a segment that will not package — none of them stop people
hearing each other. What is lost is transcript content, and every loss is logged
at WARN rather than absorbed: a transcript with a hole nobody was told about is
worse than no transcript.

The queue is bounded and **drops** rather than blocks, because blocking would
back pressure into the media path and cut somebody off mid-sentence. Dropping is
the lesser harm and the louder one.

### Fences, and the two-fixture finding

The decimal defect from the wave 9 spike is now fenced against **real speech and
the real provider** — `make test-asr`. It cannot be faked: the defect is a
property of the model, and a stub returning "two point five" would pass forever
while production wrote "two five" into engineering transcripts.

Two audio fixtures are committed, and **neither alone is sufficient** — this was
established by mutation, not guessed:

| fixture | empty context | vocabulary only | full context |
|---|---|---|---|
| long, "two point five"  | passes | **FAILS** | passes |
| short, "one point five" | **FAILS** | passes | passes |

A fence built on the long fixture alone stays green when somebody deletes the
ASR system context outright — the likeliest mutation of all. One built on the
short fixture alone stays green when the decimal rule is dropped and the
vocabulary kept. The first version of this fence used only the long fixture and
**passed against a deleted context**; the pair was the fix.

The end-to-end fence transcribes the container **this pipeline produced from live
RTP**, not a re-encode: a fake transcriber would pass happily on a malformed Ogg,
which is the one thing that could be wrong. It asserts head and tail, because a
container that loses its last pages transcribes the opening perfectly and stops,
which reads like a quiet speaker rather than dropped audio.

**7 cases** across 5 test functions — 5 in `internal/llm` against the provider,
2 in `internal/media` through the whole pipeline. Drilled by mutation: attribution dropped (turn arrives unattributed
→ red), transcription unwired from the media path (no turn ever recorded → red),
and both ASR context mutations above. One attempted drill — leaving the container
unfinalised — did *not* go red, and on inspection is not a defect: `oggwriter`
writes pages as it goes and loses at most the final one.

### AUD-01's other half is NOT built, and needs a decision

Speech activity is published (`speaking` events), which is the signal an
interruption would be detected from and what a "who is talking" indicator reads.

But **FORGE has no voice in a room.** It speaks in the workbench, through the
browser's own synthesiser, where barge-in is already handled locally. Nothing in
a room can be interrupted because nothing in a room is speaking. Cross-participant
barge-in is therefore signal-complete and act-incomplete, and finishing it means
deciding something first: whether FORGE joins rooms as a media participant with
server-side speech. That is its own slice, not a loose end of this one.

---

## Wave 9.4 — controls and privacy · **DONE**

### The fact that reshaped SEC-06

**No audio is stored anywhere.** The media plane forwards RTP; the transcriber
buffers a few seconds in memory, sends it to the provider, and drops it. Nothing
in wave 9.x ever added a place to keep audio.

That makes SEC-06's three requirements land differently from how they read, and
implementing them literally would have produced controls that lie:

| the requirement | what it can honestly mean here |
|---|---|
| retention-free mode | audio retention is already zero. What a room can choose is whether it is **transcribed**, since the transcript is the only thing that persists |
| independent audio deletion | there is no audio to delete separately. What deletes independently is the **voice-derived half of the transcript**, leaving typed turns and the room intact |
| visible recording state | the honest sentence is not "recording" at all: audio is forwarded live, is sent to a speech provider while transcribing is on, and is never stored |

A "delete my audio" button over a system that stores no audio would be theatre.
What is built deletes the thing that actually persists.

### Deletion is redaction, and the fact survives

SEC-06 says a person may erase what they said. COL-01 says the room record is an
auditable account of who said what while approvals were being made. Those pull
against each other and this is where they are reconciled rather than averaged:
**the content goes, the fact does not.**

A redacted turn keeps its sequence, speaker and timestamp and loses its text, so
the transcript can say "Priya spoke at 14:02, and that turn was deleted by Priya
at 15:10". Deleting the row would leave a gap an auditor reads as silence, which
is a different and worse untruth.

`scope=me` is the case the requirement is really about. `scope=room` erases
everybody's speech and needs authority over the project, not merely a seat in the
room — deleting what other people said is not a participant's decision.

### The controls are enforced, not displayed

Mute and pause drop the packets **at the server**, in `forward`, at the one place
every packet passes. A mute that only stops the browser sending is a picture of a
mute: undone by a bug, a stale tab, or anybody who edits the page. The person
relying on it should not have to trust software running on everybody else's
machine.

Both halves of "off" are the same decision. Dropping after transcription would
still write down what was said; dropping before forwarding would still be
audible. One check, one meaning.

Room transcription **fails closed**: a room the media plane has not been told
about is not transcribed. Forgetting the flag loses transcript, which is visible
in the room immediately; the opposite default would write down a conversation
somebody chose to keep off the record, and they would never find out.

Turning transcription off **flushes** what was already captured rather than
dropping it. Stopping is not retroactive, and silently losing a sentence somebody
expected to be in the record would be its own dishonesty.

### Two of AUD-07's five are not what they sound like

- **end-recording** is implemented as *stop transcribing*. The noun differs on
  purpose: nothing is recorded, and calling it recording would promise a deletion
  of audio that was never kept. Written as **end-recording(stop transcribing)**
  wherever both names are needed.
- **stop-speaking** has nothing to act on in a room. FORGE speaks in the
  workbench, through the browser's own synthesiser, where barge-in is already
  handled locally in `voice.js`. It has no voice in a room — the gap wave 9.3
  named — so there is nothing there to stop.
- **delete-session** is `POST /close` (end the session) plus
  `DELETE /voice` (erase what was said), which are separate because ending a
  meeting and erasing it are different decisions and one should not imply the other.

### Fences

**12 cases** — 4 in `internal/media`, 5 on the HTTP surface, 3 route mountings.

4 drilled by mutation, all red: mute not enforced in the forwarding path (a muted
participant is still heard), the off-the-record guard removed (audio reaches the
provider from a room that opted out), deletion removing the row instead of
redacting it (the turn vanishes from the transcript), and the room-wide erasure
permission removed (a viewer erases everybody's speech).

The migration is re-executable — verified by running the SQL itself twice, not
just by the tracker skipping it the second time.

---

## Wave 9.5 — the browser client · **DONE**

`GET /rooms/{id}` is a working shared session: transcript, roster, microphone
selection, the AUD-07 controls, and SEC-06's state in a sentence at the top.

### A separate surface, not the workbench

The plan said "workbench.js". It is a separate page instead, and the reason is
failure isolation: the workbench is one person building with FORGE and it works
today, while a room carries a live media connection, a microphone and a privacy
state. Folding one into the other would put every room defect inside the
product's primary surface.

The client is a module (`assets/room.js`) with no dependency on the page that
hosts it, so the workbench can mount it later without either being rewritten.
Evolutionary order: make it work where it cannot break anything, then place it.

### AUD-03's capture half

`assets/audio-input.js` is the one place a microphone is opened. Today's other
call site asked for `{audio: true}` and accepted whatever the browser felt like.
It now asks explicitly for echo cancellation, noise suppression and gain control,
and **reports what the device actually granted** rather than what was requested.

Echo cancellation is not an audio-quality setting here. Every participant plays
everybody else's audio through speakers their own microphone can hear, and the
server transcribes each stream separately — so without it the transcript fills
with each person repeating what the others just said, attributed to the wrong
speaker. It is what keeps attribution true.

### Six defects, and five of them were invisible to the Go tests

This is the wave that justified four phases of "no browser has connected to
this". Every one of these was found by looking at the running page:

1. **The page rendered in a column the width of its longest word.** `shell.css`
   centres every body with `place-items: center`; that survives a change of
   `display`. Fixed the way `workbench.css` already did.
2. **`room.css` was embedded but not in the handler's allowlist** — served 404,
   page unstyled, every fence green. The asset fence named two files it knew were
   fine; it now walks the embedded filesystem, and going back to the allowlist
   makes it red.
3. **An unauthenticated visitor retried forever in silence.** EventSource reports
   no status code, so a 401 is indistinguishable from a dropped network and the
   browser reconnects indefinitely. The client now reads the room first, which
   turns it into a sentence.
4. **A deletion reached only the person who made it.** `RedactVoice` wrote the
   redaction and published nothing, so every other open transcript went on
   showing the deleted words indefinitely — and the person who asked for them to
   be gone could not tell. **This was a live SEC-06 failure**, seen in one browser
   while another had already deleted. Now published; fenced both ways.
5. **Content was clipped, not wrapped, at narrow widths** — grid and flex children
   will not shrink below their content unless told to, and `overflow: hidden`
   then cut text off with no scrollbar.
6. **The controls were raw browser defaults.** They were written with `.node`,
   the workbench's circular icon-button class, which lives in a stylesheet this
   page does not load. Reported by the user from a screenshot.

Number 6 produced the fence worth keeping: no page may use a class that is
styled **only** in a stylesheet it does not load. It immediately found a second
instance nobody had noticed (`.top-sigil`, same cause).

### The header now carries the avatar

The standalone sigil is gone from every page header — room, workbench, console
and the emailed pages — replaced by FORGE's portrait (`persona.AvatarHTML`). The
sigil remains where it means something: badged onto the portrait on the workbench
stage, where it is a state indicator. In a header beside a wordmark it was an
abstract mark saying nothing the wordmark did not, and it is not what people
recognise FORGE as.

### Verified in a real browser

Two clients in one room: turns delivered live with speaker labels and
spoke/typed marks; roster updating on presence; the privacy sentence changing on
both clients the moment either changed it; a deletion propagating to an open
transcript with no reload; the typed path (AUD-06) sending and rendering with no
microphone at all. Desktop and 375px.

**Not verified: audio actually flowing through a browser.** The Browser pane
blocks device capture, so `getUserMedia` never resolves there. The signalling,
the peer connection and the SFU are exercised by the Go tests against real peer
connections; what remains unproven is a real microphone in a real browser, and a
media path across a real NAT.

### Fences

**6 new cases**: the class-reachability fence above, both halves of the redaction
propagation (it publishes when something was deleted, and stays quiet when
nothing was), the strengthened embedded-asset walk, and the caching contract.
5 drilled by mutation, all red.

### Still open

- **A real microphone, and a real NAT.** Both need a person at a machine.
### Asset caching · fixed

Found during this wave and fixed in it. Assets were served
`public, max-age=300` from a URL with nothing version-like in it and no ETag, so
for five minutes after every deploy browsers ran the **previous** build's CSS and
JavaScript against the new HTML — silently, with nothing to indicate it. The
comment in `Assets` claimed they were "versioned by the build". They were not,
and that claim is what kept anybody from noticing; it cost real time here, where
a stylesheet change appeared simply not to work.

Asset URLs now carry a **content hash** — `/assets/room.css?v=d0558f0a0e00` —
rendered by an `asset` template function. A content hash rather than the build
version, because a build version expires every asset on every release including
the ones that did not change, while a content hash changes exactly when the bytes
do.

Three cases, and the middle one is the bug:

| request | policy |
|---|---|
| version matches the current build | `max-age=31536000, immutable` — the URL names these bytes, so it can never be stale |
| **hard cache on an unversioned URL** | **the original defect. Impossible now** |
| unversioned, or a version this build does not have | `no-cache` + ETag — revalidates into a 304 |

The portraits take the third route: `persona.PortraitURL` builds a bare path and
knows nothing about hashing, so they revalidate rather than being cached hard.
That is one round trip, not a defect, and they carry an ETag so it is a 304
rather than a PNG.

Fenced with the properties that make a hard cache safe, each asserted rather than
assumed: the version is a real content hash (two assets cannot share one, and it
must equal the hash of the bytes served), every asset a page references is either
versioned-and-immutable or unversioned-and-revalidating, and a matching
`If-None-Match` really returns 304. Drilled both ways — restoring the hard cache
on unversioned URLs goes red, and so does making the version stop depending on
contents.

---

## Wave 9.6 — FORGE's voice in a room · **DONE**

FORGE speaks in a room, is interrupted when somebody else does, and can be
stopped. AUD-01's barge-in and AUD-07's stop-speaking had nothing to act on
before this; they do now.

### The decision, and what forced it

The provider streams speech in about **0.6 s**, faster than real time thereafter
— but as **raw 16-bit PCM at 24 kHz**. The SFU forwards Opus and never encodes,
which is what has kept the media plane pure Go across four platforms. And **there
is no usable pure-Go Opus encoder**: `pion/opus` ships a decoder only, and every
working encoder is cgo bindings to libopus.

So server-side speech was either lower-fidelity or cgo. Three shapes, put to the
user with measurements rather than opinion (`docs/spikes/2026-09-03-forge-voice-in-a-room/`):

| | quality | pure Go | one voice, one instant |
|---|---|---|---|
| **G.711 track in the SFU** ← chosen | 8 kHz | **yes** | **yes** |
| Opus track via cgo | 48 kHz | no | yes |
| each browser speaks the text | browser's own | yes | **no** |

Client-side synthesis was the tempting one — no server audio, no bill, no code.
It fails the requirement in a way that is easy to miss: everybody would hear a
different voice, starting at different moments, and an interruption would stop
one person's playback while the others talked on. That is not a participant
speaking; it is several recordings of the same sentence.

The cost of G.711 was measured, not asserted. The same utterance through the
exact transform this code applies transcribes identically to the 24 kHz original:
**telephone quality loses timbre, not numbers.** And numbers are the thing this
product cannot afford to lose — wave 9.3 exists because of a dropped decimal.

### Barge-in reads the browser, not the packet flow

"A packet arrived" is true continuously: WebRTC clients transmit through silence,
so treating arrival as speech would interrupt FORGE the instant it opened its
mouth and it could never finish a sentence. The RFC 6464 audio-level extension
carries the browser's **own** voice-activity flag per packet, and the browser has
already done the detection properly.

Absent the extension the answer is no: FORGE finishing its sentence is a smaller
harm than FORGE never being able to speak, and the explicit stop control works
either way.

### One bug the reference check found

The µ-law encoder was the textbook truncating algorithm, and cross-checking it
against ffmpeg disagreed on 496 samples in 24000. Decoding both candidates
settled it: for an input of 124, truncation lands on 132 and the nearest code on
120 — errors of 8 and 4. Truncation is systematically worse on the quiet samples
where µ-law's resolution is finest and speech spends most of its time. It now
builds a nearest-code table by inverting the decoder.

Chasing byte-for-byte agreement afterwards was a **mistake worth recording**:
ffmpeg quantises to 14 bits before its lookup, because G.711 is specified on
14-bit values, so exactness would have meant adopting its bit depth to satisfy a
test rather than because it is better. The fence asserts the property that
matters instead — every sample encodes **at least as accurately** as the
reference — which no sign inversion or exponent error survives.

### A producer, not just a mechanism

`POST /v1/rooms/{id}/ask` is how FORGE is made to speak. Something had to be, or
the voice underneath would be machinery nothing ever calls — this repository's
recurring failure mode. Deciding when FORGE should *interject* between several
people is a real product question and is deliberately not answered by guessing;
somebody asks, FORGE answers.

The question is recorded, the answer is recorded, and then it is spoken — the
record before the audio, so nobody can hear something that is not in the
transcript. FORGE's turn names FORGE and carries no user, which the record has
supported since wave 6 for exactly this moment (AUD-05).

Redacted turns are dropped from the history sent to the model. That is the one
path where SEC-06's deletion could have leaked: content somebody deleted, sent to
a provider in the next prompt, while the room showed "deleted" the whole time.

### Fences

**21 cases** — 18 in `internal/media`, 1 on the HTTP surface, 2 route mountings,
counting subtests. Drilled by mutation, all red: barge-in in **both** directions (always
firing means FORGE can never speak; never firing means it talks over everybody —
a one-directional test passes on the opposite bug), an interruption that does not
cancel the provider stream, and deleted speech reaching the model.

The end-to-end fence runs the whole path against the real provider — speech
model, resampling, G.711 — then asks the transcriber what it heard. Asked to say
"Set the wall thickness to two point five millimetres", the room produced exactly
that. It is also the standing check on the G.711 decision.

`CGO_ENABLED=0` cross-compiles re-verified for all four platforms, which was the
whole point of the choice.

### Still open

Nothing in AUD-01, AUD-05 or AUD-07 that a room needs. What remains is a product
question rather than a gap: **when FORGE should speak without being asked.**

---

## Wave 9.7 — the shared canvas · **DONE**

WRK-01 asks the workspace to carry "code, CAD/EDA previews, diagrams, telemetry,
requirements, diffs, simulations, test results". The stage carried one of them.
Everything else the system records — every version of every file, every diff,
what a machine found, what a person decided — was in the database, in the API,
in `forgectl`, and nowhere a person building something was looking.

The stage now has a tab strip and five panels: **Model**, **Files**, **Checks**,
**EDA**, **Simulation**. The last two are empty in this build and say so.

### What made this possible, and what it is still missing

The immediate blocker was closed in the commit before this one: every artifact is
anchored into its project graph as it is written, so "list this project's files"
has an answer. Files reads the graph, then each artifact's own history; no
project-wide endpoint was added, because the fan-out is bounded by the number of
files in one project and an endpoint is a public contract to maintain forever.

**Not covered here:** diagrams and telemetry, which WRK-01 also names. Both
landed in wave 9.8 — and the reason given at the time for skipping them, that
neither had a producer, was **wrong for diagrams and only half right for
telemetry**. See wave 9.8 for what was actually there.

### Why the two empty panels exist at all

This is `tools.Unavailable`'s argument one layer up. A panel that is absent reads
as a feature nobody got to, and the reader fills the gap with a guess — usually a
generous one. A panel that is present and says no solver was ever linked cannot
be misread, and it says so at the moment somebody went looking for a simulation,
which is the moment it matters.

The text is not written in the browser. Each empty panel NAMES the connectors
whose absence empties it (`fea_solve`, `spice_simulate`) and shows their own
`UnavailableReason`, so there is one copy of the fact rather than two. The fence
that holds it goes red the day somebody links a solver — because on that day the
Simulation panel would otherwise go on telling every reader that no solver exists,
in text nobody would think to go and look for.

### Two decisions in the layout

**The model layer is hidden with `visibility`, never `display`.** The WebGL
viewport sizes itself from the canvas's client box, and a `display:none` ancestor
makes that box zero: switching away and back would return a 640×480 fallback
viewport with the wrong aspect. Verified on the running app — 620×602 CSS,
1240×1204 buffer, unchanged across a round trip through four other panels.

**The voice surface is not a panel and no panel hides it.** It docks instead, the
same dock geometry already used when geometry appears, because AUD-06 requires a
non-audio path at all times and AUD-07 requires stop-speaking to keep its
position — including while somebody is reading a diff. The panels carry scroll
runway under it so nothing is permanently covered.

### One defect this surfaced

`usable_why` was `err.Error()`, so every version carried
`workspace.Version.Usable: VALIDATION_FAILED: One or more request fields failed
validation` ahead of the sentence that says what is actually true — and that
generic half is false here, since a version nobody has looked at is not a failed
request field. It went unnoticed because nothing rendered the field; the Checks
panel shows it against every version at once, which is where two lines of
machinery repeated seven times became the panel. It now returns the error's
detail. Fenced on both halves: the sentence must be there, and the machinery must
not.

### Six fences, each drilled red

`TestEveryDeclaredPanelIsOnThePage` (a declared panel with no markup),
`TestAnEmptyPanelNamesConnectorsThatAreActuallyRefused` (a cited connector that
became available), `TestAnEmptyPanelSaysWhyOnThePage` (the reason never reaching
the page), `TestEveryVerificationAndDispositionHasAChip` (a new ledger state
rendering unstyled — held against the domain's own vocabularies),
`TestScriptsOnlyWriteClassesTheStylesheetsDefine` (the half of the interface the
existing class fence cannot see, since it reads rendered HTML and most of this
page is written by a script), and
`TestTheReasonAVersionIsNotUsableIsWrittenForAPerson`.

Each was confirmed to fail under a mutation of the thing it guards, with
`-count=1`, before being kept.

### Verified on the running application

Against the live database: seven files listed with their version counts; a
version showing all seven WRK-04 facts, a `passed` verification with its note
beside a `pending` disposition that it does not imply; a unified diff rendered
with its added line coloured; `superseded` reading "version 1 was replaced before
anybody ruled on it". A conversation turn then produced
`geometry/spacer-ring.forge.json` and the panels went six files to seven —
through the live write path, not a reload. Keyboard: arrows, Home and End move
and activate; no console errors.

---

## Wave 9.8 — diagrams and telemetry · **DONE**

The last two things WRK-01 names that the stage did not carry. Two more panels:
**Diagram** and **Telemetry**.

### The premise of wave 9.7 was wrong, and worth recording

Wave 9.7 skipped both on the grounds that neither had a producer. That was a
claim made without looking hard enough:

- **Diagrams had a producer all along.** WRK-03's project graph is a diagram —
  typed nodes, typed edges, and a sentence for each relation the server already
  composes. `GET /v1/workspace/graph` had been returning all of it, and wave
  9.7's own Files panel was calling that endpoint and throwing the edges away.
- **Telemetry had half a producer.** Every turn already reports `first_token_ms`,
  `total_ms`, `model` and `tokens`, and the browser already measures barge-in.
  What is missing is not the measurement but a *store*: the server writes those
  numbers to its log and has no endpoint that reads them back.

The correct half of the claim survives in the panel: telemetry says, in the
panel, that it holds this browser session and nothing before it.

### Diagram — deterministic, and that is the point

No force simulation. A physics layout puts the same graph somewhere different
every time it is opened, which makes *"has anything changed?"* — the question
somebody actually brings to a diagram — unanswerable by looking. Columns come
from the vocabulary's own grouping; order within a column is kind then name.
Same graph, same picture, every time.

**Boxes are HTML, lines are SVG.** Text in SVG neither wraps nor ellipsises, so a
node title would have to be truncated by counting characters and guessing at the
font. HTML buttons over an SVG that draws only the edges also bring keyboard
focus and hit-testing with them, which AUD-06 would otherwise have to be built by
hand.

**The columns are declared in Go**, beside the vocabulary they group, and
rendered into the page. A kind added to `workspace.Kinds()` with no column would
otherwise be drawn *nowhere* — in a picture that still looks complete, which is
this repository's recurring failure in the one form where it is hardest to spot.
`TestEveryNodeKindHasAColumn` holds it, and a kind that matches no column still
lands in a visibly odd "not in any column" column rather than vanishing.

### What the Diagram immediately showed, and what was NOT done about it

On a real project: **8 nodes, 0 relations.** Geometry generated *from* a
requirement — the workbench's "Build from" flow, which reads the requirement's
own words into the turn — records no edge back to that requirement. The graph
therefore cannot answer "what satisfies this requirement", and the panel draws
what is true: a column of requirements and a column of files with nothing between
them.

**Left alone deliberately at the time**, and closed in wave 9.9 once it had been
asked for: writing an edge on every turn is a change to the main conversational
path, it changes the shape of the graph that `graph review` reasons over, and it
decides which relation was meant. The panel names the gap either way: nodes
joined to nothing are listed under their own heading, because a node with no
edges is invisible *as an absence* — it looks exactly like a node whose relations
are off screen.

### Telemetry — two clocks, kept apart

The server measures request arrival → first speech token: the model's part. The
browser measures Send → speech actually audible: the person's part, which also
contains the network, the parse, and the synthesiser starting.

Measured live on one turn: **first token 699 ms (server), first audio 1103 ms
(browser).** Averaging them would produce a number that is neither — and 699 ms
alone would have read as comfortably inside AUD-02's 700 ms while what a person
actually waited for was 1103 ms. They are separate columns with separate names,
and every figure says which clock produced it.

**No pass/fail against AUD-02.** Its threshold is end-of-utterance → first audio,
and nothing here measures end-of-utterance: the browser's clock starts at Send,
and a typed turn has no utterance to end. The threshold is shown for scale. A
tick against the wrong metric is worse than no tick.

**Nothing renders as zero.** A figure that was not measured is an em dash *and a
reason in the same box* — "no reply has been spoken aloud", "nobody has
interrupted". A metric nobody collected, drawn as 0 ms, looks better than any
real measurement. The panel also names what this build does not collect at all:
history across reloads, retrieval time, tool calls, other people's turns.

### A fence from wave 9.7 was vacuous, and the drill found it

`TestEveryVerificationAndDispositionHasAChip` used `strings.Contains`. Renaming
`.wbn-account` to `.wbn-accountable` in the equivalent new fence left it **green**
— a substring match answers "does some class start with this" and reports a
missing rule as a present one, which is the direction a fence must never fail in.
Both fences now require the class to END, and both were re-drilled against
rename-to-a-superstring and against deletion.

### Fences, each confirmed red under a mutation

`TestEveryNodeKindHasAColumn` (a kind with no column; a kind in two columns),
`TestEveryDiagramColumnHasANodeColour` (a colour deleted; a colour renamed to a
superstring; the columns never reaching the page), and the corrected
`TestEveryVerificationAndDispositionHasAChip`.

### Verified on the running application

A project with 8 nodes and 7 relations across 5 edge kinds drew all five columns,
with anchored nodes carrying the server's own fallback label rather than one
composed in the browser. Selecting a node lit its 3 relations, dimmed the other
4, and filtered the sentence list to match. A live turn moved Telemetry from four
em dashes to 699 ms / 1103 ms / 2525 tokens / "quoted memory", updating while the
panel was open. No console errors.

---

## Wave 9.9 — the build-from flow records where the shape came from · **DONE**

Wave 9.8's Diagram drew a project as **8 nodes, 0 relations**. Geometry generated
*from* a requirement recorded nothing that joined the two, so "what was built
from this requirement?" and "where did this shape come from?" were answerable
only by reading every version's `inputs` blob by hand. They are edges now.

### The one judgement call: `derives_from`, never `satisfies`

`satisfies` is the tempting edge and it is the wrong one. Nothing checked that
the geometry meets the requirement — a model was told what the requirement said
and proposed a shape. `satisfies` reads *"this meets that"*, so recording it
would have the system assert an unverified claim **on its own behalf**: exactly
what the split between `Verification` and `Disposition` exists to prevent, what
SAF-05 means by "the AI approved it is never acceptable authority", and what
RSN-06 forbids in so many words. Every later traversal would read the requirement
as met because somebody once described it out loud.

`derives_from` is the provenance edge and says only what happened. If the shape
does turn out to meet the requirement, a person or a test says so, with the edge
that means it. `TestProvenanceIsDerivedFromAndNeverSatisfies` holds it, with that
reasoning in the failure message.

### Where it is written, and the trap that decided it

`workspace.Change` gained `DerivedFrom`, so the edges are drawn in
`RecordChangeIn` — the same transaction as the version, the timeline event and
the anchor. Same argument as the anchor in wave 9.7: provenance that is *usually*
recorded is worse than provenance that is either recorded or absent, because
nobody checks the former.

That forced `Repository.EnsureEdge`. `CreateEdge` inserts and recovers from the
unique violation by reporting a conflict — correct on its own connection, and
**wrong inside a transaction**: in Postgres a failed statement aborts the whole
transaction, so by the time the caller decides the conflict was harmless, the
version it was writing is already gone. This is the trap `EnsureAnchor` was
written for, one edge over, and it would have fired on the **second build from
any requirement** — the common case, not an edge case. `ON CONFLICT DO NOTHING`.

Proven live rather than argued: a second turn against the same assembly and the
same requirement left **2 versions and still exactly 1 edge**. Under the drill
that swaps `EnsureEdge` for `CreateEdge`, that same second turn fails with
`CONFLICT` and takes its version with it.

### Client ids are resolved before they are trusted

The node ids come from a browser. One naming a node in another project — or one
deleted between the tick and the save — cannot be linked truthfully at all, and
attempting it would fail a foreign key or the cross-project check *inside the
transaction carrying the artifact version*. A stray id would then cost somebody
the work they had just done.

So the ids are resolved in one read first (`NodeFilter.IDs`), only nodes of this
project are linked, and what was dropped is logged as
`forge.node.provenance_unresolved` rather than passing in silence. The artifact's
own anchor is skipped for the same reason — `forge_edges` forbids a self-edge,
and a violated check is a failed statement.

### Six fences, each confirmed red under a mutation of what it guards

The edge never drawn · `satisfies` instead of `derives_from` · `CreateEdge`
instead of `EnsureEdge` (fails with the version lost, exactly as described) ·
client ids trusted unresolved · the self-edge skip removed · and, through
`POST /v1/converse` with a stub model that actually returns geometry, the handler
never populating `DerivedFrom`. That last one exists because the domain fences
all pass whether or not anything ever *sets* the field — the same shape as the
defect already recorded in `converse_requirements_test.go`.

### One bug this found in wave 9.8, live

`ForgeStage.changed()` re-read the project for a hardcoded pair of panels —
`files` and `checks` — which was already stale when the Diagram was added. A turn
that drew a new relation left the picture on screen showing the project as it was
one turn ago, with nothing saying so. It reads the same `NEEDS_PROJECT` table
`select()` does now, so the two cannot answer differently. Verified live: the
Diagram went 9 nodes → 10 while it was open, without switching panels.

**Not fenced**, and worth stating: this repository has no JavaScript test
harness, so the guard is structural (one table, two readers) rather than
automated.

### Verified on the running application

Ticking "Bracket must bolt to a 40mm hole pattern" and asking for a mounting
plate produced `geometry/mounting-plate.forge.json` joined to that requirement by
one `derives_from` edge, drawn on the Diagram with its arrowhead into the
requirement and reading *"geometry/mounting-plate.forge.json derives from Bracket
must bolt to a 40mm hole pattern — generated from this at the workbench"*. A
second turn on the same plate added a version and no second edge. A third turn
with nothing ticked added a node and no edge, which is also correct.

---

## Wave 9.10 — what was said is kept · **DONE**

The workbench conversation was never written down. `history` was posted BY THE
BROWSER on every turn and the only turn table in the schema was
`forge_room_turns`, which belongs to rooms. So a reload lost the thread while the
work it produced survived: the variants came back, the requirements came back,
the project came back, and the conversation that made all three did not.

PRD RSN-07 asks to resume from a **structured checkpoint, not a conversation
summary**. The agentic side has had exactly that since wave 1 — checkpoints,
resume state, a recovery drill that kills a worker mid-task. The conversational
side, which is the surface a person actually uses, had nothing.

### One table, and no conversations table beside it

A conversation IS its turns. It has no title, no members, no lifecycle and no
state of its own, so a row holding an id and an owner would be a second place for
those two facts to disagree with the turns. A conversation therefore exists
exactly when it has a turn, its owner is its turns' owner, and deleting its turns
deletes it — the same reasoning that kept a `forge_variants` table out of the
geometry package.

`project_id` is nullable and recorded **per turn**, because a project is created
by the first thing worth keeping rather than by the first sentence. Early turns
genuinely belong to no project, which is a fact about the conversation rather
than a gap in it.

### The id is minted by the server, and a client may not name one

An id a client chooses is one it can choose twice, guess, or aim at somebody
else's record. So the rule has no exceptions: an id that already exists must
belong to the caller, and an id that does not exist is **refused rather than
created**. A refusal does not distinguish "somebody else's" from "never existed"
— both are `NOT_FOUND`, because the difference is exactly the fact a stranger
would be probing for.

It is also announced **before the reply**, not with it. A turn can fail; if the
id only arrived with a successful answer, a failed first turn would leave a
record in the database that the browser cannot name, and the next reload would
start a second conversation with the first unreachable forever.

### Resume means the history, not the picture

Painting the transcript and sending an empty history would be the worst of both:
the page would look resumed while FORGE had no idea what had been said, and the
person would find out by being asked something they had already answered. The
restored turns go back into the history the model is given, in the recorded order
and the recorded words.

Verified live: *"Remember this: the project code name is Blue Heron"* → reload →
*"What is the project code name?"* → **"The project code name is Blue Heron."**

### A restored turn is marked, and the mark is not decoration

A live reply arrives with its epistemic labels, the standards it quoted from
memory, and the provenance of anything it drew. Those are DERIVED as the reply
lands; they are not stored, so they do not come back. A restored turn rendered as
an ordinary one would therefore say *"FORGE made no claims here"* — which is a
different statement from *"nobody kept them"*, and a false one. So restored turns
carry `from the record`, and the response says in words what the record leaves
out.

### Retention, and the control that makes it true

MEM-01 asks each layer to state its retention. This layer's is **until the person
deletes it** — no expiry, because an expiry nobody chose is a promise the schema
cannot keep without a sweeper that exists and runs. That is only a real statement
if deleting works, and AUD-07 requires deletion to be reachable at all times, so
the control is beside the conversation rather than in an operator's console. Two
presses, because it cannot be undone.

It deletes the **record**. The variants, the graph and the artifacts the
conversation produced are work rather than transcript, and the message says so
rather than leaving somebody to guess. Verified live: 0 turns left, 9 artifacts
untouched.

### A failed write is reported, never silent

A person has their answer; losing it because the transcript could not be written
would trade the thing they asked for against the record of having asked. But a
turn that was not kept is invisible until somebody reloads and finds a gap, which
is the worst moment to learn it — so it travels on the stream the way a variant
that could not be saved does, and it goes in the log with a reason.

### Seven fences, each confirmed red under a mutation

Nothing ever recorded · the id arriving only with the reply · reads not scoped to
the owner (drilled at both the domain and the endpoint) · deletes not scoped to
the owner · a client naming any conversation it likes · and the schema's role
constraint dropped, which is the schema-code coherence rule this repository
already had for verification states.

### Raised here, closed in 9.11

The server held the turns and the client still SENT its own `history`, so a
caller could still put words in FORGE's mouth for the duration of one turn. That
was flagged rather than slipped in, because it changes what the model sees. It
was then asked for; see wave 9.11.

---

## Wave 9.11 — the history comes from the record · **DONE**

The client sent the conversation's history and the server used it verbatim. So a
caller could include an `assistant` turn saying whatever it liked — *"I already
approved this"* — and steer the next reply with a conversation that never
happened. PRD SEC-04 treats documents, tool output and imported results as
untrusted input; a transcript asserted by the caller is the same kind of thing,
and it was the one place taken at face value.

The room path had been doing this correctly since wave 9: `roomHistory` builds
`[]agent.Turn` from the room's own record. The workbench was the exception, and
it was the exception because until wave 9.10 there was no record to build from.

### The field is removed, not ignored

`converseRequest` has no `History` field. A client still sending one is refused
by the strict decoder — loudly, with the field named — rather than having it
quietly dropped, which would leave somebody sending a history for months in the
belief that it mattered. Held by a structural fence in the same shape as the one
that keeps requirement TEXT out of the request: the moment somebody adds the
field back for convenience the guarantee is gone, and a behavioural test would
still pass for every client that happens not to lie.

### Two things that fail silently, and are fenced accordingly

**The order of the read and the write.** This turn's message is recorded BEFORE
the model is called, so a turn that then fails is still in the record. The
history must therefore be read BEFORE that write — read after it, and the model
is handed the same sentence twice, once as something already said and once as the
thing to answer. Nothing errors; it reads as the person repeating themselves.

**The speaker mapping.** The record says `human` / `forge`; the model loop says
`user` / `forge`; and `buildMessages` maps anything that is not `forge` onto the
user role. So a wrong mapping does not fail — it silently reassigns every one of
FORGE's own turns to the person, and the model reads back a transcript in which
it never spoke.

### The window is now load-bearing, so it says so

`HistoryWindow` (16) was a silent trim inside `buildMessages`, and it barely
mattered when the history was one browser session long. A persisted conversation
meets it routinely. A model handed the last sixteen turns with nothing said
otherwise will answer *"we have not discussed that"* about something the record
plainly contains — a fabricated claim about the person's own history, which is
what RSN-06 forbids. The server now counts what it did not send and tells the
model, in a labelled block, the same way `requirementsFor` announces the
requirements it injects.

The constant is exported and read by both the caller and `buildMessages`, so the
two cannot disagree about how far back a turn can see.

### Deliberately not changed here, and changed in 9.12

The model was given the **speech** of each recorded turn, which is exactly what
the client used to send. Folding in the `detail` would have changed what the
model is given at the same time as changing where it comes from, and only one of
those was this change. It was then asked for; see wave 9.12.

### Five fences, each confirmed red under a mutation

The record never read · the history read AFTER this turn is recorded, which puts
the message being answered in its own history twice · the speaker mapping
inverted · trimming not announced · and the `History` field put back.

The ordering drill is worth noting: the first mutation written for it did not
compile, which is a red test that proves nothing. It was rewritten to move the
`keepSaid` call above the read instead — the same defect, in a form that builds —
and it then failed with the sentence the fence exists to catch.

### Verified on the running application

The request body now carries `message`, `project_id`, `conversation_id` and
`on_screen`, and no history at all. A hand-made request carrying one is refused
with `VALIDATION_FAILED`. And across a reload, with the page sending nothing:
*"Remember: the fixture is called Kestrel"* → reload → *"What is the fixture
called?"* → **"The fixture is called Kestrel."**

---

## Wave 9.12 — FORGE can say why it said that · **DONE**

A reply has two halves: the **speech**, kept short because it is spoken aloud,
and the **detail**, which is where the reasoning goes because the screen can
carry it (PRD §5.3). Only the speech reached the next turn. So FORGE could set
out three materials with a trade-off for each, and then, one question later, have
no idea what it had said about any of them — *"why did you say 3mm?"*, the most
ordinary follow-up there is, was the one thing it could not answer about its own
answer.

Both halves travel now.

### The label is load-bearing, not tidiness

They arrive as two halves, marked: the speech, then
`[Shown on screen with that reply, not spoken aloud: …]`.

What keeps FORGE's speech short is largely its sense of how it spoke last time. A
model whose own previous turns arrive as long paragraphs learns that long SPOKEN
replies are normal — and speech staying short is a product rule the evaluation
suite floors at 70 words. Running the two halves together would have traded a
measured behaviour for an unmeasured one.

### One producer, because this has gone wrong before

`agent.HistoryContent` is called by the workbench (from its record) and by the
evaluation harness (from the reply it just received). An eval that assembles a
turn's history differently from the product is measuring a different system —
which has already happened here once, expensively: the on-screen note was added
to the product, the harness kept scoring a model that had never been shown it,
and part-id stability measured 1 run in 4 for reasons that had nothing to do with
the clause being measured.

### The suite could not see the risk this change introduced

`a-revision-is-recognisable-as-one` is the **only multi-turn case in the suite**,
and it did not carry the `spoken reply stays short` scorer. Every case that did
was single-turn — which is to say, every case that could measure whether history
lengthens speech had no history at all. The scorer is on the multi-turn case now.
That is not a bonus; without it this change would have been unmeasurable.

### Measured, before and after

Three repeats of the multi-turn case against `qwen-plus`, with the new scorer in
place both times and the only difference being whether the detail travelled:

| | part ids (tracked) | unit declared (floor 100%) | spoken reply short (floor 90%) |
|---|---|---|---|
| **before** — speech only | 3/3 | 3/3 | 3/3 |
| **after** — detail folded in | 3/3 | 3/3 | 3/3 |

Longest spoken replies after the change: **37, 33 and 27 words**, against a
ceiling of 70.

**What this does not establish:** the before run was not `--verbose`, so its word
counts were not captured and there is no numeric comparison — only that the
ceiling held on both sides and that the after side is nowhere near it. Three runs
of one case on one model is a measurement of this run, which is what the suite
says about itself.

### One detail is bounded, and says when it was cut

`HistoryWindow` bounds how many turns come back; nothing bounded how LONG one of
them could be, and the record is permanent — so one reply carrying a long table
would ride along in every request for the rest of the conversation. Two thousand
runes, and the truncation is marked.

By **runes**, not bytes: cutting a UTF-8 sequence in half produces bytes that are
not text, and the model would receive a replacement character where a dimension
used to be. (`forLedger` in httpapi had the same flaw on the stored inputs; fixed
in wave 9.13, along with the rule itself moving somewhere both callers read it.)

### Five fences, each confirmed red under a mutation

The detail never travelling · the two halves run together unlabelled · truncation
by bytes instead of runes · the harness dropping the detail while the product
carries it · and the product dropping it end to end, from the record to the
model.

### Verified on the running application

FORGE was asked for three materials with the comparison on screen. It spoke one
sentence — *"aluminium, steel, and brass, each balancing weight, strength, and
corrosion resistance"* — and put a table in the detail, where alone it named
**1045** steel and called it prone to corrosion. After a reload: *"Which of those
three did you say was prone to corrosion, and which alloy did you name for it?"*
→ **"Steel is prone to corrosion, and I named 1045 steel as the example alloy."**
Neither fact was ever spoken.

---

## Wave 9.13 — a cut that lands between characters · **DONE**

`forLedger` shortens what a person said before it goes into a variant's `inputs`
— WRK-04's record of what a change was made from. It counted **bytes**, while the
constant beside it said characters and always had.

Two consequences, and the second is the one that damages the record:

- A message in any script that is not ASCII was cut at **a third** of the stated
  length. 2000 bytes of Chinese is about 666 characters.
- The cut landed wherever the 2000th byte fell, which for UTF-8 is usually the
  middle of a character. Nothing failed loudly — `json.Marshal` substitutes a
  replacement character for the broken sequence and Postgres stores it happily —
  so a dimension or a word ends its life as `` in a row kept for provenance.

### The rule moved to where both callers read it

Wave 9.12 wrote the same thing correctly in `internal/agent` and left this one
alone. Two copies of a four-line rule, one of them wrong, is the argument for
`internal/platform/text.Clip`: count characters, cut on a boundary, say how much
was lost and say it in the same unit as the limit. Both callers now go through
it, and each keeps its own comment about WHY it wants a bound.

This is past the "extract on the third occurrence" line rather than short of it —
see the survey below.

### Measured on the running application

A 3002-character Chinese request that produced geometry:

| | stored |
|---|---|
| characters kept | **2000**, plus a 46-character notice |
| bytes stored | 6048 |
| tail | `…外径… [truncated; 3002 characters in the original]` |
| replacement characters anywhere in `forge_artifact_versions.inputs` | **0** |

Under the old code the same message would have been cut to about 666 characters,
ending mid-character, and the notice would have claimed "9006 characters".

A second request of 1622 characters — 4866 bytes — is now stored **whole**. The
old code would have truncated it, because it was over the limit in the only unit
the code was actually measuring.

### Five fences, each confirmed red under a mutation

Cutting by bytes · counting the limit in bytes · truncating silently · the ledger
caller hand-rolling it again · and the ledger caller's own end-to-end check
against the shared rule.

Three of the five mutations did not compile on the first attempt, which is a red
test that proves nothing — an unused import each time. Rewritten to compile, they
failed with the sentences the fences exist to catch.

### The same defect elsewhere, surveyed here and fixed in 9.14

A survey of every byte-slice truncation in the tree found three more of the same
bug — `agent/untrusted.go` `excerpt`, `tools/workspace.go` `truncateStr` and
`llm/llm.go` `truncate`. They were raised rather than swept up with this one, and
then asked for; see wave 9.14.

Two more looked like the same thing and are **correct**: `identity/service.go`
slices a `[]rune`, and `geometry/repository.go` slices a slug that
`strings.Map` has already reduced to `[a-z0-9-]`.

---

## Wave 9.14 — the other three cuts · **DONE**

The three byte-slice truncations surveyed in 9.13, all now `text.Clip`.

| where | what it truncates | what a broken cut costs |
|---|---|---|
| `agent/untrusted.go` `excerpt` | suspected injection content, into the log and the timeline | SEC-04's record of an attack ends in a character the document never contained, so it cannot be searched for, quoted, or matched back |
| `tools/workspace.go` `truncateStr` | a timed-out command's partial output, quoted **to the model** | a replacement character where a value used to be is a value the model reads as something else |
| `llm/llm.go` `truncate` | a provider's response, quoted into an `errs.Error` detail | that detail is marshalled into an HTTP response, so the operator reading it to diagnose a failure sees a symbol the provider never sent |

### One deliberate change of unit

`llm.truncate` reported `(N bytes)`, which was at least honest about what it
counted. It reports characters now, because that is what the limit counts and one
number describing the other is what caused this whole thread. A body that is not
valid UTF-8 at all — a binary payload on a JSON path, which `transcribe.go` can
genuinely meet — is unharmed: each invalid byte counts as one and nothing new is
broken. Fenced.

### Raised here, closed in 9.15: `limitedWriter`

It also cuts on a byte boundary, and its budget is genuinely in bytes. Named
rather than folded into a change about character limits, because the fix is
different in kind; then asked for. See wave 9.15.

### Three fences, each confirmed red under a mutation

Each helper is package-private, so each package holds its own: put the byte slice
back and the excerpt, the quoted output and the quoted response each fail with
what that particular string costs when it breaks.

---

## Wave 9.15 — the last cut, and the one place the budget stays in bytes · **DONE**

`limitedWriter` caps how much of a command's output is held in memory: 64 KiB of
stdout, 32 KiB of stderr. It clipped at a byte offset, and the offset can land
inside a character — so the tail of that character was dropped, never completed,
and the output handed to the MODEL ended in a sequence that is not text.

### The budget stays in bytes, and that is the point

Every other truncation in waves 9.13 and 9.14 had a limit that meant characters
and counted bytes. This one genuinely means bytes: it is bounding memory while
output streams in, and a character-counting budget would have to decode every
chunk to know how full it was. The notice — *"kept N bytes, dropped at least N
more"* — is true and stays.

So the fix is not the unit. It is that what was kept must still be text.

### Why the trim is at the read and not in `Write`

`Write` carries the scars of two earlier bugs (a short return that killed the
command with `EPIPE`; a notice appended from inside `Write` that never fired when
output arrived in one burst). It is also the wrong place: a chunk boundary can
split a character too, harmlessly, because the next chunk completes it. Deciding
inside `Write` would mean knowing whether the BUILDER currently ends
mid-character.

After the command exits there is no next chunk, and the question is simply
whether the end is broken. `limitedWriter.text()` is now the one way to read the
result — trim, then notice — and the caller reads it once instead of appending
the notice into the builder and calling `String()` in four places.

### Only when this writer did the cutting

A command that emits a partial character of its own is relaying its own data, and
quietly editing that is a different and worse problem than the one being solved.
The trim is conditional on `truncated`: **we do not create a broken ending, and
we do not repair one either.** Fenced in both directions.

The bytes the trim removes move from the kept side of the notice to the dropped
side, so the two numbers still describe the string beside them. At most three.

### `text.TrimPartialRune`

Walks back at most three bytes to the last start byte and drops the sequence if
it does not decode. Invalid bytes in the MIDDLE of the output are left exactly
where they are — that is data, not damage this code caused. A tail of four or
more continuation bytes cannot come from a cut sequence, so it is left alone too.

### Four fences, each confirmed red under a mutation

No trim at all (the defect) · trimming output that was never clipped · the notice
disappearing, which is the invariant `TestLimitedWriterAlwaysReportsTruncation`
has held since CI found it · and `TrimPartialRune` looking back only one byte,
which fixes a two-byte cut and misses a one-byte one.

That last mutation is the useful one: a version of this that only checks the
final byte passes a naive test and leaves exactly half the real cases broken.

---

## Wave 10 — the parametric document · **DONE**

**Why now.** `docs/spikes/2026-09-05-parametric-cad-kernel/` asked two questions
the 2026-09-02 Zoo spike never did: can a real CAD kernel run here at all
(**yes** — build123d on OpenCASCADE, a valid B-Rep in **46 ms**), and can our
model drive one (**structure yes, figures no**). The second answer is what this
wave is about.

The spike swept a bracket through nine parameter changes. Eight held. The one
that broke had `rib_length` as an independent 52 mm while `plate_size` moved: the
ribs overhung the plate and the kernel refused the fillet. Deriving it held the
same model across a 3.4× size range.

> Naming a parameter is not enough. What survives a change is the RELATIONSHIP
> being the thing recorded.

**What was built.**

- `geometry.Parameter` and `geometry.Derived` on the document, with `Resolve`
  evaluating the expressions, ordering them by dependency, and reporting what
  does not resolve. A value that cannot be computed is ABSENT, never zero — a
  silent 0 turns a broken document into a plausible one.
- `expression.go`: a closed grammar over the document's own names. No general
  evaluator: these strings arrive from a model, over the network, into something
  that is also persisted and replayed. Trigonometry is deliberately absent —
  degrees and radians agree only at zero, and the wrong one produces a plausible
  number rather than an error.
- `standards_typed.go`: the honesty machinery, extended from prose into the
  typed fields, **and along the dependency edges**. A figure derived from a
  recalled parameter is exactly as unchecked as the parameter, so the claim
  travels with it. That is how a number the model never stated becomes
  attributable to the standard it came from.
- Resolution problems reach the reader through `NotVerified`, the same door the
  dropped tolerances and the unconvertible unit already use.

### The defect the live run found, which was mine

Three runs against the real contract, not the spike's probe prompt.

The first two produced parameters and derived values — and put the NEMA figures
(`42.3`, `31.0`, `3.2`) in `derived` as **bare constants**, where there is no
`how` and no `source` and nothing can check them. Three of four derived values in
each run, including a correctly-recalled `31.0` that therefore reached the reader
unchecked.

The spike's own probe never did this — 0 bare constants and 3 `how: "standard"`
parameters across all three of its runs — so this was the contract's doing, not
the model's. The difference was one clause. The probe stated a sufficient
condition, *"every number a person could change is a PARAMETER with a unit"*; I
had written a definition:

> "parameters" are the numbers somebody could change

A NEMA 17 bolt pitch is not something anybody can change, so the definition
excludes it and the sufficient condition does not. The model read mine correctly
and used the only other bucket. **A rule written as a category test gets applied
as one**, and the category excluded exactly the values the provenance system
exists for.

Reframed as "EVERY fixed number this design rests on… a recalled figure is a
parameter too, with `how: standard`", the next run produced:

```
nema17_face_size   = 42.3 mm   how=standard  source="NEMA ICS 16-2001"
nema17_bolt_circle = 31   mm   how=standard  source="NEMA ICS 16-2001"
```

That is the first correct NEMA 17 bolt figure in this investigation, and it
**does not overturn the spike's 0/3**: one run is not a rate, the two prompts
differ in many ways besides that clause, and the probe put its figures in the
right field and still got them wrong. The reframe fixed the placement defect it
was aimed at. Whether it moves the figure rate is unmeasured.

### What is NOT done, and is not pretended to be

- ~~**Parameters do not drive the geometry.**~~ **Done in wave 11.** A part
  dimension can now name an expression, the boundary evaluates it, and
  `POST /v1/geometry/{id}/respec` re-derives a whole variant from a changed
  parameter.
- ~~**Parametric export stays refused** (VIS-05).~~ **Done in wave 14.**
- **A recalled figure written as a bare constant in `derived` is still
  invisible to the provenance check.** It is reported as a fixed number with no
  unit and no source, which is true and is not the same as being checked. Two of
  three derived values in the last live run were still this. Attributing them
  would mean guessing which standard an unlabelled number came from, which is the
  failure `standards.go` was written to avoid.
- **No measured floor for a "the parametric model resolves" scorer.** One live
  run is not a rate. Adding a scorer with a chosen floor would break this suite's
  own rule that floors are measured.

## Wave 11 — the parameters drive the shape · **DONE**

**Why.** Wave 10 left the worst of the three available positions: a document that
DESCRIBED a relationship while the geometry ignored it. A reader seeing named
parameters beside a shape reasonably concludes that changing one would move it,
and nothing did.

**What was built.**

- `Part.SizeFrom` and `Part.PositionFrom` — a dimension names an expression over
  the document's parameters. `Bind` evaluates them into `Size` and `Position` at
  the conversation boundary, so `mesh.go`, `compare.go`, `overlay.go` and the
  exporter are untouched: by the time they see a Part it is plain floats, exactly
  as before.
- `Document.WithParameters` — the payoff. Hand it `plate_size = 80` and every
  derived value and every bound dimension is recomputed. It is the operation the
  2026-09-05 spike performed by hand nine times.
- `Service.Respec` and `POST /v1/geometry/{id}/respec` — the producer, so this is
  reachable from the product rather than only from a conversation turn. It
  appends a version of the SAME artifact, which is what makes the before and
  after comparable side by side (VIS-04) and leaves the original exactly as the
  model produced it.

**Three decisions worth naming.**

- **The expression wins when it disagrees with the stated number**, because the
  expression is the relationship and the number is a snapshot of it. Never
  silently: agreement says nothing, disagreement is reported.
- **Rotation cannot be bound.** Every parametric part in the spike drives sizes
  and positions and not one drives an angle. A binding nobody has needed is one
  that gets designed wrong.
- **An override naming a derived value is refused**, not ignored. It would be
  recomputed from its expression a moment later, producing a version identical to
  the old one — which reads as success and is not.

### The defect the live run found, which was again mine

The first live respec of a seven-part bracket produced **eight warnings**, every
one of the form *"states width = 60 but its own expression works out to 90"* —
after `plate_size` had been set to 90 on purpose. The stated numbers are stale BY
CONSTRUCTION once a parameter moves. The disagreement check is meaningful only on
a document as the model produced it, so `WithParameters` no longer performs it,
and a fence holds that shut.

A second, smaller version of the same mistake: the warning for a bare constant in
`derived` used to end *"it will not follow when anything else changes"*, and
fired twice on `motor_centre_x = 0` — a centre at the origin that is CORRECT not
to follow anything. It now states the remedy instead of predicting breakage.

### What the model actually produced

Two live runs against the shipping contract. The second bound **7 of 7 parts and
36 dimensions**, and changing `plate_size` re-derived the geometry.

And it produced, in the run before it, a clean example of the hazard the spike
named third and nothing here checks:

```
nema17_face_size = 42.3 mm            how=standard      ← the figure is CORRECT
motor_mount_x    = nema17_face_size / 2 - mount_hole_offset
holes positioned at (±motor_mount_x, ±motor_mount_x)    ← a 42.3 mm square
```

NEMA 17's bolt pattern is 31 mm square. The recalled figure is right, the
relationship built on it is wrong, and the result is four holes at the frame
corners. **A wrong number can be checked against a published figure; a wrong
RELATIONSHIP produces plausible numbers from correct inputs**, and no part of
this system examines one.

### What is NOT done

- ~~**No UI.**~~ **Done in wave 12.**
- ~~**Parametric export still refused** (VIS-05).~~ **Done in wave 14.**
- ~~**Nothing checks a relationship.**~~ **Partly done in wave 13** — the
  measurable case is covered; see there for what still is not.
- **Rotation is unbindable**, by decision.

## Wave 12 — a person can turn the knob · **DONE**

Waves 10 and 11 built the whole re-derivation path and no surface in the product
could reach it. The variant rail now says how many parameters a design has and
offers them: each is an editable number with its unit, the derived values sit
beneath as EXPRESSIONS rather than controls, and Re-derive posts only what
actually changed.

Three things worth keeping.

- **Derived values are shown, not editable.** Setting one changes nothing —
  it is recomputed from its expression a moment later — so offering it would read
  as working. What the panel shows instead is the relationship, which is the
  thing a person needs in order to decide which parameter to change.
- **Only the CHANGED parameters are sent.** Sending all of them would rewrite
  each one's provenance to "chosen", including recalled figures nobody touched.
- **A recalled figure is marked AT THE CONTROL**, not only in the provenance
  banner further up the page. This is where somebody changes one.

**Found while writing it:** `workbench.css` referenced `var(--line)`, a custom
property this codebase has never defined, with no fallback — so the declaration
was dropped and the panel would have had no border. Every custom property the
stylesheet reads is now audited.

The geometry HTTP surface had **no tests at all**. It has four now, against real
Postgres, covering the exact response shape the panel dereferences by name: a
renamed field breaks the browser and nothing else, and nothing else would notice.

## Wave 13 — the relationship, not the figure · **DONE**

Wave 11's live run produced a document in which every figure was correct and the
part could not be bolted to the motor:

```
nema17_face_size = 42.3 mm   how=standard   ← correct: NEMA 17's frame IS 42.3
motor_mount_x    = nema17_face_size / 2
holes at (±motor_mount_x, ±motor_mount_x)   ← a 42.3 mm square
```

NEMA 17's bolt pattern is 31 mm square. No check of the INPUTS can find this.

`Document.Spans` measures what the placements actually describe, and the honesty
machinery scores that instead. The result catches the defect above and accepts
the same document built from `nema17_bolt_circle`.

### Why this is not the guessing standards.go refuses

The grouping is read from the BINDINGS, never from the geometry. Parts whose
position on one axis is computed from the same parameters are related because the
document says so — nothing here decides that four cylinders near each other must
be a bolt pattern. What names the group is the parts' own shared id, and if that
id says nothing the dimension table recognises, the figure is **not scored**
rather than guessed at.

Two narrowings, both from things that went wrong while building it:

- **Exactly two distinct positions, or nothing.** With two, the extent between
  them is unambiguously the spacing. With three in a row it is some multiple of
  the pitch and nothing here knows which.
- **"mount hole" was added to the dimension table and reverted.** It matched
  `mount_hole_x_OFFSET`, which is half a pitch, and scoring that against the
  published 31 mm is exactly the fabricated finding this suite's own fences
  exist to catch — and they caught it, immediately.

### What is still NOT checked

- **A relationship whose result nothing can name.** `motor_mount_x` on its own
  names no dimension, and if the parts it places are called something the table
  does not recognise, the span goes unreported. Under-reporting is the safe
  direction and this takes it.
- **A relationship with no binding.** Holes at hardcoded coordinates describe no
  pattern to measure. Wave 13 checks what is bound.
- **Anything that is not a distance between two positions.** An angle, a ratio,
  a wall thickness derived wrongly — none of these have a measurable result to
  compare against a published figure.

## Wave 14 — the kernel · **DONE**

`docs/spikes/2026-09-05-parametric-cad-kernel/` proved a CAD kernel could run
here — build123d on OpenCASCADE, a valid 37-face B-Rep in **46 ms**, real
ISO-10303-21 out — against the 2026-09-02 Zoo spike's estimate, which had been
measuring an agent thinking rather than a kernel working. Wave 14 wires one in.

**What it is.** `internal/domain/cad` is a long-running Python sidecar. The costs
are nothing alike — importing build123d takes 2.5 s and building the part takes
46 ms — so it is imported once and kept warm. Measured here at **0.9 ms** for the
second build, which is faster than the network hop that asked for it.

`POST`-nothing: the same `GET /v1/geometry/{id}/export?format=step` that used to
refuse now returns a real B-Rep, with a fence asserting the file contains
`CYLINDRICAL_SURFACE` and no `TRIANGULATED_FACE_SET`.

**Absent by default, and absent loudly.** `FORGE_CAD_PYTHON` is unset unless a
deployment sets it, and without it STEP is declared and refused exactly as
before — the same discipline as the vision model. The formats list follows the
deployment rather than the build. IGES and KCL stay refused either way, because
nothing here writes them and marking one available because a neighbour became so
is how a capability list starts lying.

### One convention, not two

The kernel and the renderer have to agree about what a rotation MEANS (the Euler
order, and that the angles are RADIANS), which dimension each shape reads, and
what a missing one defaults to. Implemented twice, those agree until somebody
edits one.

So `geometry.RotationMatrix` and `geometry.Solids` are now the single source of
both, the sidecar receives numbers and a matrix and decides nothing, and the
fence is `TestKernel_PlacesAPartWhereTheRendererDrawsIt`: the same document is
tessellated by mesh.go and built by the kernel, and their extents must match to
1e-6.

### Three fences that were vacuous, found by drilling them

- **Orientation was not asserted at all.** Removing the sidecar's axis correction
  — so every cylinder pointed along +Z where this system draws them along +Y —
  left every test green. Volume is identical however a solid is turned, and the
  STEP file still contained a cylindrical surface. Fixed by having the sidecar
  report the assembly's extent, which is the only value in the reply that can
  show a part is turned wrongly.
- **The restart test did not test the restart.** It called `Close`, which resets
  the kernel's own state, so the next build simply started a fresh process and
  the retry never ran. Removing the retry outright left it green. The crash path
  now has an internal test that kills the process without telling the kernel.
- **Rows versus columns survived a single-axis rotation.** Reading the matrix's
  rows applies the inverse, and for one axis and a symmetric part that differs
  only in sign — same bounding box. The fence now uses a COMPOUND rotation and a
  box with three different dimensions.

### A pre-existing defect this uncovered

`CONNECTOR_UNAVAILABLE` is a 501, and `WriteError` withheld `detail` for every
5xx. So every "this deployment cannot do that" refusal in the product arrived as
the registry's generic line: the vision model's `FORGE_LLM_VISION_MODEL`
sentence, the old STEP refusal's reason, all of it. The whole
unavailable-connector discipline said what was missing everywhere **except in the
reply to the person who asked for it.**

Narrowed rather than removed: that one code's detail passes through, because it
does not report a failure — it reports a capability that is declared and absent
here, its text names configuration rather than internal structure, and the same
sentence is already served unauthenticated by `GET /v1/geometry/formats`.

### What is NOT done

- **The kernel builds; it does not check.** No interference test, no stress
  analysis, no manufacturability review. VIS-06's banner says so and is now
  worded to stay true whether or not a kernel is configured.
- ~~**No booleans.**~~ **Done in wave 15.**
- **A tube's bore is still not modelled**, because `Part.Size` has no inner
  radius to model it from. The mesh path substitutes a cylinder and so does this,
  and the label says so.
- **A zero-radius part builds a solid of volume 0** rather than being refused —
  measured against build123d 0.11.1, which accepts it. It is in the file and
  invisible. The mesh path behaves identically.
- **One request at a time.** A pool would be a wrong answer to a question nobody
  has asked.

## Wave 15 — a hole is not a part · **DONE**

Wave 14 gave this deployment a real kernel and then asked it to build a bag of
primitives. Every solid it produced was a box sitting next to a cylinder — a
genuine B-Rep, and not a thing anybody machines. The spike's own reference
bracket needs two operations the vocabulary could not express: four holes
subtracted through a plate, and a fillet.

**A hole is not a part. It is the ABSENCE of one**, and a document that can only
add material describes a bracket with no way to bolt it to anything.

**What was built.** `Document.Features` — `cut`, `fuse`, `fillet`, `chamfer` —
validated in Go and performed by the kernel. The reference bracket now builds as
ONE solid with four bores, at exactly `60×6×60 − 4πr²t`.

### Three decisions

- **A hole is an existing part used as a TOOL.** The obvious design gives a hole
  its own vocabulary — position, diameter, depth, axis — and was rejected: that
  is a second way to say "a cylinder somewhere", and the two drift the day
  somebody adds a size key to one of them. A cut names parts that already exist
  and are already bound to parameters, so a hole that follows `plate_size` does
  so with no new machinery at all. The tool is CONSUMED, or the hole would be
  filled by the thing that made it.
- **Edges are selected by a RULE and never by an index**, per the spike: *"an
  index would silently select a different edge the moment a parameter changed."*
  There is nowhere in the schema to write a number, and a fence asserts that
  `"3"`, `"first"` and `"edge-2"` are all refused.
- **A feature that does not check out is DROPPED, not approximated.** An assembly
  missing a hole is wrong in a way a reader is told about; one where the hole
  landed somewhere else is wrong in a way nobody notices.

### The divergence this creates, and why it is stated rather than fixed

The renderer draws primitives and has no boolean operations, so a part used to
cut a hole appears as a small solid **standing in** the plate rather than as a
void **through** it. The exported B-Rep has the hole; the viewport does not.

That is two things this product shows the same person, and `FeatureNotes` says so
in the banner — the same stance as "Drawn approximately". Teaching the
tessellator to do booleans costs a CSG engine in JavaScript, which is the kernel
this product just stopped pretending it could do without.

### What the live run found

Asked for a bracket with four clearance holes and a rounded edge, qwen-plus
emitted **5 features, 5 valid** on the first attempt — four cuts and a fillet —
and the document built as one part with 36 KB of real STEP.

OCCT refused the fillet: 5 mm on a 6 mm plate. Correctly, and it exposed the
worst gap in wave 14's export path. **A bracket whose fillet was refused looks
like a bracket, downloads like a bracket, and has square corners where the design
says rounded** — and the download label said nothing. Dropped features now come
FIRST in that header, because a header is read left to right and this is the half
that changes what somebody does with the file.

### What is still NOT done

- **The renderer still cannot draw a hole.** Still true; the framing is not —
  see "Carried defects", updated 2026-09-06. The exported MESH cannot draw one
  either, and as of wave 31 both say so. What the renderer no longer does is draw
  one as a solid POST, which is the opposite of a hole: a cut tool renders as a
  faint ghost in the warning gold this interface already uses for "quoted from
  memory, not checked".
- ~~**No sketches or extrusions.**~~ **Done in wave 17.** ~~Revolves and sweeps
  are still absent.~~ **Revolves landed in wave 18 and sweeps in wave 19**; this
  line said otherwise until 2026-09-06, which is four waves of a list nobody
  re-read. A "still NOT done" list is a claim with a shelf life, and one that
  outlives its subject is worse than no list: it is the reason somebody plans a
  wave that has already shipped.
- ~~**No `max_fillet`.**~~ **Done in wave 26.** OCCT still refuses a radius the
  geometry cannot take — correctly — but the refusal now names the largest one
  that DOES build, found by asking the kernel and reporting a radius it watched
  succeed.
- ~~**A tube's bore is still unmodelled**~~ **Retired in wave 27.** `tube` was
  never a shape the document could keep its promise about: its size keys are
  `radius` and `height`, so a wall thickness could not be stated. It is no longer
  offered, it still reads out of the ledger as the cylinder it always was, and it
  says so.

## Wave 16 — two defects found by asking whether it worked · **DONE**

Both were in wave 14, both shipped, and neither was found by a test. They were
found by asking "is the kernel actually working completely?" and checking the two
things nobody had checked.

### A 2 inch cube exported as a 2 mm cube

A STEP file DECLARES its unit, and build123d writes `SI_UNIT(.MILLI.,.METRE.)`
unconditionally. Documents in this system can be in mm, cm, m or in, and the
numbers went through unconverted. So a bracket designed in inches arrived as a
file confidently stating it was **25.4× smaller**, in a format everything
downstream treats as exact.

```
declared: 2 in cube = 50.8 mm/side          volume back: 8.00   bounds: -1.00 .. 1.00
STEP declares: SI_UNIT(.MILLI.,.METRE.)
```

`geometry.Solids` now converts every length to millimetres, and an unconvertible
unit builds NOTHING rather than a file at a guessed scale — the same rule the
rest of this codebase already applies to units, which the kernel had quietly
stepped outside of.

**Why no test caught it:** every fixture was already in millimetres. A suite that
only ever speaks one unit cannot see a conversion that is missing.

### The kernel had no producer in the product

The variant rail listed OBJ and STL as two literal buttons. A deployment with a
kernel configured could build a real B-Rep **that no button ever asked for** —
STEP was reachable from the API and from nowhere a person could click.

The rail now reads `GET /v1/geometry/formats` and renders one button per format
the DEPLOYMENT can write, with the unavailable ones shown disabled carrying the
server's own reason. Which formats exist is a property of the deployment, and
that endpoint is the one place that knows it.

**This is the third time in this session** the same failure has appeared in a
different place: `WithParameters` had no caller until wave 11 added one, the
parameter panel had none until wave 12, and export had none until now. The
pattern is always the same — capability built, wired to an interface, and never
connected to the thing a person touches.

## Wave 17 — the shape that is not a primitive · **DONE**

Every solid this system could describe started from a box, a cylinder, a cone, a
sphere or a plane. That is enough for a plate with holes in it and nothing else:
an L-bracket, a T-section, a channel, a gusset — the ordinary cross-sections most
fabricated parts actually are — could not be said at all.

A part can now be an **extrusion**: a closed outline of at least three points in
the part's own XY plane, swept along local Z. It composes with everything else —
the outline follows parameters through expressions, and cut, fuse and fillet
apply to the result like any other part.

### Where the outline lives, and the one deliberate inconsistency

Profile coordinates are LOCAL and are **not** re-centred, so the part's position
places the outline's origin rather than its centre — which is how every other
shape here behaves.

That is on purpose. A profile's coordinates are written by hand: somebody says
the corner is at (0, 0) and the flange runs to (40, 0), and then positions a bolt
hole against those numbers. Re-centring on the outline's own bounding box would
move every one of them by an amount that depends on the outline's SHAPE — so
adding a point to the far end of a flange would silently shift the holes.
Centring is right for a box, whose dimensions are symmetric by construction. It
is wrong for a drawing. The extrude direction IS centred, like a box's height.

The cost is that extents could no longer be `position ± half`, so `halfExtent`
became `localBox` returning two corners. Exact for both.

### Ear clipping, in two languages, and what is actually shared

The kernel needs no triangles. The viewport and the mesh exporters do, and
neither can call a kernel — one is JavaScript in a browser and the other must
work where no kernel is configured.

A triangle fan is four lines and is WRONG for any concave outline. An L-bracket
is concave by definition, and a fan across its inner corner draws triangles
outside the part: the first shape anybody makes with this feature would be drawn
wrong. So both implementations ear-clip.

What is shared is the PROPERTY, not the code: **any correct triangulation of an
outline covers exactly the outline's area**, so the two agree about the shape
however they each cut it up. That is not true of curve tessellation, which is why
the segment counts are fenced across the boundary and this is not. Both were run
against the same six outlines and agree to 1e-6, including the clockwise case and
the bow-tie refusal.

### Four defects found while building it

- **A triangle fan hidden inside the API.** `triangulate` normalised the winding
  internally and returned indices into the CALLER's array, so the caps came out
  normalised and the side walls — built by walking the caller's points — did not.
  A clockwise L-bracket tessellated inside out, and the only clue was a negative
  volume. It now returns the ordering it used, so the mistake is unavailable.
- **Ear clipping "succeeds" on a bow-tie.** It consumes every vertex and produces
  triangles covering twice the enclosed area. The termination guard caught the
  case where no ear is found and said nothing about this one, so outlines are now
  checked for self-intersection up front — the property itself rather than a
  proxy for it.
- **Two vacuous fences**, found by drilling: the millimetre conversion test used
  a box and so never touched a profile, and no `Measure` test had an extrusion in
  it. Both now have their own case.
- **Two wrong assertions of mine**, both times against a kernel that was right: a
  T-section's area is 1000 and not 850, and a bore rotated onto the extrude axis
  passes through 20 mm of depth rather than 8 mm of flange.

### What the live run produced

Asked for a steel angle bracket with a bolt hole through each leg, qwen-plus
produced a **6-point outline** 60 mm deep and two cut features, and the document
built into one solid with 27 KB of STEP — an L-section with two bolt holes,
first attempt.

### What is still NOT done

- ~~**No revolves.**~~ **Done in wave 18.**
- ~~**No sweeps along a path.**~~ **Done in wave 19.**
- ~~**No arcs.**~~ **Done in wave 20**, as a corner radius.
- ~~**One loop per outline.**~~ **Done in wave 21.**
- ~~**No closed paths.**~~ **Done in wave 22.**

## Wave 18 — the turned part · **DONE**

An outline could be extruded and nothing else. Turning one about an axis is where
every turned part comes from — a shaft, a boss, a flange, a pulley, a dome — so
`revolve` joins `extrusion` as the second thing a profile can become.

It reuses everything: the same outline, the same expression-driven coordinates,
the same features on the result. The only new field is `axis`, which is `"y"` (up,
the default) or `"x"` — the two axes IN the outline's own plane, because turning
it about Z would sweep it out of that plane and produce a shape nobody means.

### Two decisions

- **Full circle only.** A sector is a revolve with something cut out of it, which
  needs no vocabulary of its own — the same reasoning that makes a hole a cut
  rather than a kind of part. Partial revolves would also make the measured
  extents wrong unless computed specially, since the swept bound depends on the
  angle.
- **Every point on one side of the axis.** Touching is fine and usual — a dome's
  outline meets the axis at its apex — but points on both sides sweep through
  each other. OCCT refuses that with `BRep_API: command not done`, which names
  nothing, so it is caught earlier where BOTH offending points can be named. One
  is arbitrary: the fault is that two of them disagree.

### Three fences that were vacuous, and one comment that was false

- **The shape DISPATCH was untested**, for revolves and for extrusions both. The
  unit tests called `extrusion()` and `revolved()` directly, so removing the case
  from `partTriangles` left them green while every outline was silently drawn and
  exported as a bounding box. There is now a test that goes through `Tessellate`.
- **`Measure` had no extrusion or revolve case**, so `localBox` could be disabled
  without a red test.
- **A comment claimed a measurement that never happened.** A V-belt pulley failed
  to parse, truncation was the first suspicion, and `converseMaxTokens` was
  raised from 6000 with a comment saying the reply "ran past 6000 tokens and was
  cut off". Three further runs came back at about 1300 tokens with
  `finish_reason: "stop"` — the reply was simply malformed, intermittently. The
  headroom is worth having and the comment now says it is a **precaution**, not a
  measurement. In a codebase built around not fabricating figures, that one was
  about to go into a code comment.

### What came out of that investigation instead

A reply the model could not finish, or finished badly, used to be printed to the
person AS SPEECH — so a failed parse showed them `{ "speech": "Here's a V-belt
pulley..."` and several hundred characters of machinery. `unreadableReply` now
says which of the two happened, because a person who was cut off can ask for less
and a person whose reply was garbled can only ask again. Speaking genuine PROSE
is kept: that is the case the fallback exists for.

### What the live runs found

Asked for a stepped bush and then for a vee-groove pulley, qwen-plus described
both with **primitives and cuts rather than a revolve**, and both BUILT — the
pulley into one solid with 108 KB of STEP. Reaching for the simpler vocabulary
when it suffices is the right instinct, and a revolve is not obligatory for
anything a cone and a cut can express.

**So whether the revolve contract lands is unmeasured.** The capability is proven
end to end against the kernel, the renderer and the measurement path; what is not
known is how often a model chooses it. That is a rate, and rates belong in the
eval suite against a measured floor.

## Wave 19 — the part that bends · **DONE**

An outline could be swept along a straight line perpendicular to itself, or
turned about an axis. Between them they cover the part with one cross-section all
the way through, and the part that is turned. What neither covers is the part
that BENDS — a pipe run, a handrail, a cable tray, a roll cage member, a wire
form — so `sweep` joins them as the third thing a profile can become.

It reuses everything again: the same outline, the same expression-driven
coordinates, the same features on the result. The new field is `path`, an open
line of at least two points with an `x`, a `y` and a `z` in the part's own frame.
There is no `depth`: the path says how far it goes.

**A sweep along a straight path up local Z is the extrusion**, facet for facet,
and that is asserted rather than asserted-of. If a straight path produced a solid
merely CLOSE to the extrusion — section rotated, caps elsewhere, winding reversed
— this would be a second subtly different way to say the same thing, and the two
would drift the first time either was edited.

### Three decisions

- **The outline's origin rides the path, and nothing is centred.** An extrusion
  centres its depth, because depth is a size like a box's height. A path is
  DRAWN, like an outline, and drawn things are used where they were drawn. The
  consequence worth having: an outline drawn away from its origin sweeps at that
  offset, which is how an eccentric section or a rail carried beside its own
  centreline is described.
- **Corners are mitred**, which is what a fabricated bend is. It has one property
  worth having: the wedge cut from the inside of a bend exactly equals the wedge
  added outside, so a swept solid whose outline is centred on its path encloses
  `area × path length` EXACTLY, however many times it bends. That identity is
  what the tests on all three implementations assert — arithmetic a reader can
  check, the same reason the revolve figures come from Pappus.
- **The section frame travels to the kernel** rather than being derived there.
  Which way up the section starts and how it is carried round a bend both have
  several defensible answers, and two builders deciding separately agree until
  one is edited. It is computed once and sent as a matrix, the same bargain the
  placement matrix already makes.

### What the kernel spike settled, and what it cost to find out

Measured 2026-09-05 against build123d 0.11.1, three things about OCCT that no
amount of reading the API would have given:

- **`Transition.RIGHT` is not a preference.** OCCT's default, `TRANSFORMED`,
  returned **1600 mm³** for an elbow whose correct volume is 5000. `ROUND`
  returned 4946 by rounding a corner nobody asked to have rounded.
- **A section is swept in its own plane, wherever the path points.** A path
  setting off along +X sweeps an XY-plane section FLAT — volume 0, no error. That
  is why the frame is applied by placing the face before the sweep.
- **OCCT ignores where the path sits.** A path from z=10 to z=30 swept a section
  at z=0 into a solid spanning z=0..20. Placing the face at the path's first
  point is what makes the coordinates mean what they say.

### The fault OCCT does not catch

A repeated path point comes back as `BRep_API: command not done`, which names
nothing. A 180° reversal comes back with an **empty message**. Both are refused
in Go, where the offending points can be named — the same reasoning as the
revolve's axis crossing.

The third one is different and is why there is a check nothing else here has:
**a bend tighter than its own outline is wide is not refused by OCCT at all.** It
returned a plausible solid of 14546 mm³ for a shape whose surface folds back
through itself. So the fold is detected directly, and exactly — for every point
of the outline, the ring must ADVANCE along each segment; one that goes backwards
IS the fold — and the part is left out of the file with both path points named.

### The drill found two fences that could not fail

Sixteen mutations, one at a time, each naming the test that had to go red — now
committed as `make drill-fences` (`scripts/drill-fences.sh`), and run last in
`make check`. It edits the source in place, because there is no other way to find
out whether the real test sees the change, and restores it from a checksummed
backup on the way out — including on an interrupt. It reports three outcomes and
not two: a fence that skipped is UNPROVEN rather than passed, since a skipped
test prints `ok` and a drill that cannot tell those apart would report a fence
that never ran as one that held.

Two did not go red:

- **The twist test asserted a property that could not fail.** It bent the path
  once, in one plane — and re-framing each segment from scratch (the plausible
  wrong implementation) AGREES with carrying the frame for a single bend. It
  disagrees only when a second bend leaves the first's plane, because rotations
  do not commute. Nothing else notices either: a rolled section has the same
  area, so the volume is identical, the mitres still close, and a symmetric
  outline has the same extents. Every test in the package stayed green. It now
  asserts the property itself — the smallest rotation leaves the bend's own axis
  alone, so a carried frame keeps its component along it — on a path whose two
  bends are in different planes.
- **The kernel test could not tell whether the frame was used.** Its path set off
  up local Z, which is framed by the identity, so a kernel ignoring the frame
  entirely built the right solid. Its path now sets off along +X.

The other fourteen went red, including all four against the browser copy.

There are no drills for the extrusion or the revolve fences. That is a gap, and
it is stated in the script rather than left to be discovered: waves 17 and 18
shipped without drills, and the dispatch fence they both rely on turned out to
be vacuous.

### The third implementation is now checked against the first

`forge3d.js` has always been a second implementation of the tessellation, and
what was shared with the Go one was the PROPERTY rather than the code: any
correct triangulation covers the outline's area. That works for an extrusion,
whose only convention is the outline, and for a revolve, whose only choice is an
axis.

It does not work for a sweep, which has three conventions and none of them
changes the volume. A browser copy that rolled the section as it went would draw
a rail with its flat face pointing somewhere nobody asked for, export a different
solid, and pass every volume test on both sides.

So the renderer's own builder is now run in **node** and compared with
`geometry.Tessellate`, facet for facet, INCLUDING the normals — on a bent,
out-of-plane path with an asymmetric section. They agree exactly, because a swept
polygon has no curved surface anywhere on it and the two are the same polyhedron.
It skips without node, the same bargain the kernel tests make with
`FORGE_CAD_PYTHON`.

### One thing found in passing

`forge3d.js` exported a `supportedShapes` list that went stale the moment
outlines arrived: `extrusion` and `revolve` were drawn correctly and named
nowhere, so a list called "supported shapes" said the opposite of the truth about
them. Nothing consumes it. It now names all three.

## Wave 20 — the drawing gets a radius · **DONE**

An outline and a path were both straight lines between points. Everything people
actually make is full of radii: a plate has rounded corners, a slot ends in a
semicircle, and a bent tube has a BEND RADIUS — not a decoration but the number
the bender is set to, and the thing that decides whether the tube survives being
bent at all.

A point may now carry a `radius`, which rounds that corner: an arc of that
radius, tangent to both edges meeting there. The same field on an outline point
and on a path point, because it is the same idea in both.

### Why a corner radius and not an arc segment

The obvious contract is "this edge is an arc of radius r". **A radius and two
endpoints do not determine an arc in three dimensions** — they determine one per
plane through the chord. Measured 2026-09-05: build123d's `RadiusArc`, given two
points in the XZ plane, returned an arc leaning out of that plane entirely
(tangent `(0.267, 0.535, 0.802)` where `(0, 0, 1)` was wanted), the path it built
was not tangent to the line before it, and the sweep along it missed its own
volume by 5% with the section visibly distorted. Nothing was wrong with OCCT. The
request was ambiguous and it answered a different question.

A corner radius is not ambiguous: "round this corner with R25" names one arc,
tangent to both edges, in the plane those edges already define. It is also the
number a person actually has, and it covers more than it sounds — **a slot is a
rectangle whose radius is half its width**, where the two arcs at each end meet
and the straight between them vanishes. Stadiums, racetracks, D-sections and
rounded gussets are all the same one number.

What it cannot say is an arc that does NOT meet its neighbours smoothly: a
crescent, a lens, a bulged edge. Those need a vocabulary with a plane in it —
**which wave 29 gave them**, as a `via`: one more point, and the edge arriving at
it is the circular arc through it. Three points fix a circle completely, which is
the same reasoning that chose a corner radius over an arc-with-a-radius here.

### It overlaps `fillet`, and the overlap is named rather than tidied away

`fillet` already rounds edges of the built solid, and on an extrusion
`edges: "vertical"` rounds the outline's corners all the way through. For that
one case there are now two ways to say the same thing, and the difference is
real: a corner radius is part of the DRAWING, so it follows the section round
every bend of a sweep and all the way round a revolve, while a fillet is an
operation on the finished solid chosen by rule — and there is no rule that names
one corner of an outline.

### The arc is built twice, at two fidelities, on purpose

- **The kernel gets the true arc**, as three points on it, so a rounded corner
  exports as a real `CYLINDRICAL_SURFACE` rather than a forty-sided prism. Three
  points fix the plane, so the ambiguity above cannot come back. This required a
  new shape on the Go→kernel wire: a `Curve` — a start and a run of edges, each a
  line or an arc — which replaced the list of points for outlines AND paths, so
  the kernel now has ONE wire builder instead of three inline ones.
- **The tessellators flatten it** into chords at the same count a cylinder gets,
  and report the deviation, in the assembly's own units. That is the bargain
  every curved shape here already makes.

So a sweep with sharp corners is still exactly the same polyhedron in the kernel
and in the viewport; a sweep with a bend radius is the viewport INSIDE the built
solid by no more than the number stated on the export.

### What the drills found this time

Twenty-six mutations. Two stayed green, and both were the same mistake in
different clothes — **a test whose case could not tell the right answer from the
wrong one**:

- **Every rounded-corner test used a right angle.** The arc centre is set back
  `r/cos(θ/2)`; swapping that for `r/sin(θ/2)` is a NO-OP at 90°, because
  `sin 45° = cos 45°`. Every test in the file was a rectangle or a slot, so the
  swap passed everything. There is now an obtuse case — the 135° turn round the
  sharp point of a gusset — and it fails immediately.
- **Nothing tested that a corner radius is converted to millimetres.** The
  existing conversion tests had no radius in them. The hidden failure is
  specific: an outline correctly scaled, with its corner rounded to a radius
  still in inches — 25.4 times sharper than drawn, which on a part meant to be
  bent is the difference between a radius the material tolerates and one that
  cracks it.

### And one place the browser copy is now allowed to differ

The renderer/exporter fence compared facets one for one. With arcs it went red on
a CAP: **two correct ear clippings of one outline are not the same triangles.**
That is the property this codebase deliberately shares instead of the code, and
it never came up before because both sides made identical decisions from
identical arithmetic. A rounded corner puts forty nearly-collinear points on an
outline, where an ear test turns on a cross product of about 1e-16 and the two
can legitimately part company.

So the fence now consumes the facets that match — the WALLS, where every
convention worth guarding lives, still match to the last bit — and requires what
is left to be two triangulations of the same region: same vertices, same total
area, few enough to be caps. It also compares NORMALS numerically rather than as
text, after a wall whose normal is exactly `(0, 1, 0)` in Go came back as
`(-4.12e-16, 1, 0)` from the browser.

## Wave 21 — a hole in the section · **DONE**

An outline was one closed loop, and a hole in a part was made by cutting a
cylinder out of it with a feature. That rule was written down, defended, and is
**narrowed rather than reversed** here.

It was right when the only outline shape was an extrusion, where a loop and a cut
are interchangeable. **They are not interchangeable for a sweep: you cannot cut a
bent bore with a cylinder.** The bore of a bent tube turns the corner with the
tube; it is not a cylinder and not any other shape the feature vocabulary can
place in space. A hollow tube that bends — which is most tube — is expressible
only as a section with a hole in it. So:

- a hole in the **section** is a loop, and follows the drawing wherever it goes;
- a hole through the **solid** is a cut, and is placed in space.

A bolt hole through a plate is still the second one, and the contract says so.

### What it took

- **Ear clipping cannot see a hole**, so each one is spliced into the outline by
  a BRIDGE — a segment to a visible outer vertex, walked out and back — which
  turns a ring-with-holes into one ring that touches itself. Exact: the bridge is
  traversed in both directions and encloses nothing. Twice, in Go and in the
  browser, held together by the facet-for-facet fence.
- **The winding does the work.** The outline is normalised counter-clockwise and
  every hole the other way, which makes ONE wall-normal formula point out of the
  material on both — nothing has to know which loop it is walking.
- **A hole is checked against the FLATTENED outline.** A hole tucked into the
  corner of a plate can sit inside the drawn rectangle and poke out through the
  rounded one, and it is the rounded one that is the part.
- **Every loop is carried by the same frames** along a sweep's path. A bore
  carried by frames of its own would drift out of the wall around it as the path
  bends, and the wall thickness would vary for no reason visible in the drawing.

### Two things the drills found

- **A wall built over the merged ring passes every volume test.** The bridge is
  walked twice in opposite directions, so a flat fin standing inside the solid
  along it contributes EXACTLY ZERO volume. Every figure stays correct. The tests
  now assert the SURFACE AREA as well — two caps plus the depth times every
  loop's perimeter — which is the thing a fin does change.
- **A box carrying holes and nothing else was silently ignored.** The check that
  refuses an outline on a primitive looked at the profile and the path and not at
  the holes, so the part fell through and was built as a plain box with its voids
  dropped.

### And a defect in the drill script itself

A drill was added against `profile.go` without adding that file to the list the
script backs up. The mutation was applied, never restored, and the script
reported the tree byte-identical — which was true of every file it knew about.
It was caught by the test suite two commands later, having survived a full
drill run.

`drill()` now REFUSES to touch a file it has no backup of, so the list being out
of date is a loud stop rather than a permanent edit to the working tree.

## Wave 22 — a path that comes back · **DONE**

A sweep's path was open: it had a start, an end, and a cap on each. A ring, a
hoop, a frame, a gasket, a roll bar have none of those. `"path_closed": true`
joins the last point to the first — no ends, no caps, and the seam is a corner
like any other, so it may carry a bend radius too.

A flag rather than repeating the first point at the end, because an outline here
is closed IMPLICITLY and repeating its first point is an error. One idea should
not have two spellings depending on which list it is in.

### The thing that nearly shipped as "free"

A closed sweep carries its section round the loop with a rotation-minimising
frame. **That frame does not generally come back to itself.** Carrying a frame
round a closed curve rotates it by the area its tangents enclose on the sphere,
and the section arrives at the seam turned by that angle — so the two ends meet
at an angle and what it encloses is not a solid.

The first three loops tried all came back at **0.000°**: a planar square, a
square with one corner lifted, a zigzag ring. They were symmetric enough for the
rotation to cancel, and it looked like a proof that closed paths were free. The
control that settled it is the octant — +X, +Y, +Z, back to +X — which must give
exactly 90 and does. Measured over **2000 random closed polylines: every single
one came back rotated**, by up to 179.8°.

So the closure is CHECKED, and a loop that does not close is refused with the
angle named — "it returns rotated by 9.8°" rather than "it does not close",
because a tenth of a degree is worth chasing and ten degrees is a different
drawing. Planar loops always close, in any plane, and the tests say so.

It could instead be FIXED, by spreading the residual twist along the path so the
seam meets. That is what some CAD does, and it means the section is twisted the
whole way round by an angle nobody asked for — invisible on a round tube and a
helical rail on anything else. Refusing says what is wrong and leaves the shape
to the person who drew it.

### Where a closed run starts, and the 0.3% it costs to get wrong

A closed path's first point is a corner, so it may be rounded — and then the
flattened run begins PART-WAY ALONG an arc, where the direction of travel is a
chord rather than the straight the arc is tangent to. The exact curve the kernel
builds has the true tangent there. The two disagree by half a chord's turn: 4.5°
at this fineness for a right angle.

Measured 2026-09-05: a 60×60 ring with R15 corners came back from the kernel at
**21358.7 mm³ against an arithmetic 21424.8**, spanning −5.36..65.36 where a
10-wide section can only reach −5..65. The whole solid was tilted by the section
frame being square to a chord.

Both representations now start where the seam's arc ENDS, on the straight that
follows, where they agree exactly. Rotating costs nothing — a closed run has no
first point, only a place we chose to start writing it down.

### And two drills that could not fail, both for the same reason

Every closed-path fixture had a SQUARE first corner. So the drill that disabled
the browser's seam rule, and the one that stopped the path being marked closed
before flattening, both passed: with a sharp seam there is no arc to start in the
middle of. Both fixtures now round every corner including the first.

### What the live runs found, and the one thing they changed

Asked for a bent coolant line — the same prompt as wave 19's, against the richer
contract — **qwen-plus reached for a `sweep` in five runs out of six.** Before
these three waves it described the same part with three extrusions butted end to
end in both of its runs. That is a rate, and rates belong in the eval suite
against a measured floor, which still has no geometry cases in it; what is
reported here is six runs, not a number to rely on.

Two of the six produced a **correct, buildable bent tube that FORGE threw away**,
for one reason: the model puts a `radius` on EVERY path point, ends included, and
a radius on an end was a refusal.

It is now a warning, and the part is built. The reasoning is the one already
written down for a box carrying a profile: an ERROR is a part that could not be
read, and a WARNING is something in it that was ignored. A radius at an end has
exactly one reading — there is no corner there — unlike a z on an outline point,
which could be a coordinate in the wrong column or a sweep whose path was never
written. Losing a whole part to an inert number is the wrong trade.

That also turned up a smaller lie: `Solids` appended "so it is not in this file"
to EVERY problem, including ones that do not remove the part. It now says that
only of errors.

## Wave 23 — the eval suite gets the drawing vocabulary · **DONE**

Waves 17 to 22 proved six pieces of vocabulary against the kernel, the renderer
and the measurement path, and each one closed with the same sentence: **how often
a model reaches for it is a rate, and rates belong in the eval suite.** Three
waves later that gap was the oldest thing in this document. It is now four cases.

### A third kind of case

`KindCapability`. They are not regressions — a model describing a stepped bush as
two cylinders is RIGHT, and there is no defect to trace. They are not industry
coverage — that kind is pinned to the selector and counted one per entry. So they
are named, exactly as coverage was named when it did not fit the regression rule,
and the report prints the three apart.

Four prompts cover six pieces of vocabulary, because a bent hollow tube exercises
the sweep, the holes and the bend radius at once — which is also how a person
meets all three. None of them names a field: "design a bent coolant line" is a
part, and "use a sweep" would measure instruction-following, which is not in
doubt and is not what ships.

### What the first run measured

Against qwen-plus, four cases over six runs each, 2026-09-05:

| what | rate |
| --- | --- |
| an L-section is drawn as an **extrusion** | 6/6 |
| a bent line is drawn as a **sweep** | 6/6 |
| a bent-from-one-length loop is drawn as a **closed path** | 6/6 |
| a vee-groove pulley is drawn as a **revolve** | 4/6 |
| a walled tube is drawn with **holes** in its section | 4/6 |
| a corner is given a **radius** | 8/12 |

The vocabulary lands. Two numbers are worth reading past:

- **The radius splits 6/6 against 2/6.** Six of six where the prompt NAMED a
  corner radius; two of six where the part was only described as bent. A model
  that rounds a corner when told to and not otherwise is one drawing welded
  elbows where a bent tube was asked for.
- **Holes at 4 of 6.** The other two reached for a `cut` feature — which on a
  part that turns a corner is a straight hole through a bent tube, and is the
  exact thing holes were added because you cannot do.

### And the finding the suite exists to produce

**17 of 24 drawings resolved.** Seven were refused outright, so the part was not
in the file — and **six of the seven are one thing**:

> A loop that repeats its first point to close itself.

It is the GeoJSON and WKT convention, and every polygon format a model has read.
This contract asks the opposite — *"the outline is closed for you; do not repeat
the first point at the end"* — and the model reaches for what it knows. It turned
up on outlines and on closed paths alike, sometimes with a corner radius attached
to the repeated point.

The seventh is a `radius` on a path point whose neighbours are **in line**: no
corner there, so the number changes nothing — the same inertness wave 22 stopped
being fatal at a path's END, one point along.

Every one of those has exactly one reading, and the repeated point is redundant
rather than meaningful. **Whether to read them that way is a decision, and it has
not been taken** — so the scorer reports the rate rather than flooring it. A floor
here would measure whether the decision had been made, not whether the model
draws buildable parts.

## Wave 24 — reading what the model actually writes · **DONE**

The eval suite's first run found that **17 of 24 drawings resolved**, and that
six of the seven refusals were one thing. Both fixes are now in, and both are the
same idea: *a drawing that says something inert should not cost the whole part.*

### A loop may close itself the way every polygon format closes one

Every format a model has read — GeoJSON, WKT, shapefiles — closes a ring by
repeating its first point. This contract asks the opposite, and the model reaches
for what it knows. It has exactly one reading: a closed loop returning to its own
first point has a final edge of zero length, which is never a shape, so the
repeated point is redundant. It is dropped, on outlines, holes and closed paths
alike, and the reader is told.

**The radius moves with it.** The repeated point often carries the corner radius
while the original does not — the model writes the corner once as a destination
and once as a corner. Dropping the point and leaving the radius behind would
MITRE a corner somebody asked to be bent: same silhouette, volume within a
fraction, different part, made a different way, silently. Two copies carrying
DIFFERENT radii is the one case with no single reading, and is refused.

What is not relaxed is a duplicate anywhere else. That is a copied line somebody
forgot to edit, it has no single reading, and it stays refused.

### An inert radius is ignored, not fatal

A radius on a point whose neighbours run straight through names no corner, so it
changes nothing. It is now a warning and the part is built — the same treatment
wave 22 gave a radius on a path's END, and the two are now ONE mechanism in
`roundedCorners` rather than two spellings of one rule in two files.

Still refused: a negative radius, one on a point sitting on its neighbour, one on
a reversal, and two that need more edge than there is between them. Those cannot
be read as anything.

### What the re-measurement says, and what it does not

**13 of 14, and incomplete.** The provider account stopped accepting requests
part-way through the re-run — first `Arrearage` (overdue payment), then
`Model not exist` — so ten of the twenty-four runs never happened, and
`draws-a-closed-loop`, the case where the closing convention appeared most often,
is among them.

> **Corrected 2026-09-06.** The two failures above are not one thing and reading
> them as one cost a day. `Arrearage` was real and was settled. `Model not exist`
> was NOT the same outage returning: the provider had retired `qwen-plus`, and
> the account was answering normally throughout — `GET /models` returns 200 and
> lists twelve. Wave 25 has the whole of it. The lesson is narrow and worth
> keeping: two failures in sequence from one endpoint are not evidence of one
> cause, and the second one here had a completely different remedy from the first.

 The scorer stays TRACKED: a floor set from a partial
re-measurement is exactly the target-dressed-as-an-observation this suite is
arranged against.

What IS established without a model: both drawings the suite lost are transcribed
into `TestKernel_TheDrawingsTheEvalSuiteLostNowBuild` and come back from OCCT
with the right volume — including the one whose repeated closing point carried
the bend radius, checked against the arithmetic for a loop that really is bent
rather than mitred.

The one refusal that survived is neither pattern: an outline crossing its own
revolve axis, which is a drawing that genuinely is not a shape.

## Wave 25 — the model that was not there · **DONE**

The implementation plan carried this under "Operational — blocking": *the model
provider account is in arrears*, and it named that as the reason nothing in the
evaluation section could be measured. It was wrong. `GET /models` on the same
host with the same key answers 200 and lists twelve models; what had gone was
`qwen-plus`.

Every other default in `config.go` had already been moved to the 3.8 generation.
`Converse` had not — so the only role a person actually WAITS on was the only one
pointing at a model that no longer existed. Two more roles were in the same state
and nothing had noticed, because their tests only run when an API key is present.

### The three things that came out of it

**The message now names the survivors.** A 404 asks the endpoint what it serves
— one GET on a path that has already failed permanently — and says so. The same
sentence is used by the transcription and speech paths, which classify their own
errors and would otherwise have been left with "Model not exist." and nothing to
act on. That single change is the difference between a day and ten minutes.

**The conversation role was thinking out loud.** Every model this endpoint now
serves is a reasoning model, and every one deliberates by default. Measured on
the eval suite's own bracket prompt, time to the first CONTENT token:
qwen3.7-plus 28 693 ms thinking against 735 ms not; qwen3.8-flash 3 140 against
507; qwen3.6-flash 15 538 against 270; qwen3.8-max 4 119 against 486. AUD-02 asks
for 700 ms. Nothing was broken — the tokens were produced, they arrived on
`reasoning_content`, which this client has always ignored and should keep
ignoring. The reply just took half a minute to begin, silently.

`deliberation.go` turns it off for the conversation role and only for it, keyed
on the role rather than on a setting: llm.go's comment already said the rule
("an executor should think hard and may take a minute; a conversation is someone
waiting mid-sentence") and nothing enforced it. And only at endpoints known to
understand the field, because it is a provider extension and a strict endpoint
rejects the whole request — trading a slow conversation for no conversation.

**This deployment has no voice, and that is now stated rather than broken.** The
endpoint serves no ASR or TTS model reachable through the chat surface. That is
a deployment decision — a different endpoint and its own key, or accept a
text-only installation — and it is carried below rather than guessed at.

`docs/bugfix/2026-09-06-the-provider-retired-three-models.md`.

## Wave 26 — the largest radius that would have worked · **DONE**

Carried since wave 15 as "No `max_fillet`": OCCT refuses a radius the geometry
cannot take, correctly, because the alternative is a self-intersecting solid —
and it refuses it with an EMPTY message. A `Standard_Failure` carrying no text
(measured 2026-09-05 against build123d 0.11.1), so the person who asked for R45
on a 60 mm plate was told `Standard_Failure`: a word with no number in it, about
a number.

### Why bisection and not a formula

There is a closed form for the largest fillet on one convex edge between two
planes, and it is useless here. `edges` is a RULE that selects many edges at
once, the limit is whichever of them is tightest, and edges interact — two
fillets that each fit alone do not fit together when their faces meet. The only
authority on "does this radius work" is the kernel, so it is asked, ten times,
on a path that has already failed.

### Why the answer is always one that was BUILT

The number goes into a sentence telling somebody what to type next, and a
suggestion that then fails is worse than no suggestion. So it is the lower bound
of the bisection — a radius the search watched OCCT accept — rounded down. On the
reference plate, whose true limit is exactly 30, it reports 29.97.

Edges that take no fillet at ANY radius are a different fact and are reported as
one: there is nothing to suggest, and suggesting something very small would send
somebody round the loop again for nothing.

### Why not build123d's own `Shape.max_fillet`

OCCT's refusal literally recommends it, and it was read first. It searches 0 to
2× the bounding box DIAGONAL and gives up after ten iterations with a
`RuntimeError` — on the 60 mm plate that window is 170 mm wide and ten halvings
land short of its own tolerance, so the common case is an exception where a
number was wanted. It also has no chamfer, and raises where "these edges take no
fillet at all" is worth saying differently. Searching only up to the radius
somebody ASKED for needs no luck: the answer is inside the window by
construction.

## Wave 27 — `tube` is retired · **DONE**

Carried since wave 15 as "a tube's bore is still unmodelled". It was never
modelled and never could be: a tube's size keys are `radius` and `height`, and
there has never been an inner one — so the document had nowhere to say a wall
thickness. Every consumer drew and exported a SOLID cylinder and apologised for a
void that was never stated anywhere.

That is not a defect to repair. Repairing it means adding a size key, and the
document can already say a bored tube two ways, both of which model the void
properly: a cylinder with a cylinder cut from it, and — since wave 21 — a section
carrying a `holes` loop, which is the only one of the two that can turn a corner
with a bent tube. The word carried no information the vocabulary did not already
carry better, and it carried a claim the document could not keep.

### Retired from the contract, still READ from the ledger

The model is no longer offered the word, and is told what to write instead —
silently removing it would leave the model to guess, and the guess it made before
this vocabulary existed was three extrusions butted end to end.

Every variant already stored with a `tube` in it still opens, still renders and
still exports. The ledger is append-only and a part that stops drawing is a
revision history that lies about what was proposed. So the word resolves, in ONE
table (`retired.go`), to the shape it always actually was — and says so, in the
same channel every other substitution uses.

Not rewritten at the document boundary, though that would be tidier: what is
persisted is exactly what the model said, so a replay cannot differ from what the
person saw. A stored document that says `tube` goes on saying `tube`; every
reader resolves it the same way, through one function.

The kernel's `tube` case was REMOVED rather than left as a belt and braces. If
the resolution ever stopped happening, the sidecar now refuses the part by name,
loudly, instead of quietly building the right solid for the wrong reason.

## Wave 28 — telemetry has a store · **DONE**

NFR-05 was the one PRD line with a real measurement behind it and no way to look
at it. Every turn was already timed — request in to first speech token, the whole
turn, the tokens, which model answered — and every measurement went to the log
and nowhere else. So the Telemetry panel could only show what the browser tab in
front of you had watched happen: it emptied on reload, knew nothing about
yesterday, and its own "Not measured here" list said so in the first line.

What was missing was never the measurement. It was a store.

### Why these are COLUMNS and not a `forge_turn_telemetry` table

There is already exactly one row per FORGE turn. The timings are facts ABOUT that
turn — same turn, same owner, same moment — and a second table keyed on the same
thing would be a second place for "which turn" to be wrong, plus a second thing
to delete.

That last part is not a convenience. AUD-07 requires deletion to always be
reachable, and `DELETE /v1/conversations/{id}` is the path. Timings on the turn
are deleted BY the path that already exists, on the day it is written, with no
second sweeper to forget.

### Why every column is nullable, and what that buys

Absence has to keep meaning absence. A default of 0 would make an unmeasured turn
indistinguishable from an instantaneous one — and the panel's own rule, written
long before this existed, is that a missing measurement renders as an em dash and
a reason, never as a zero, because zero is a number somebody reads as "instant".
That rule only holds if the wire and the column can carry the difference, so
`Timing`'s fields are pointers all the way to the JSON.

### One thing the first attempt got wrong

The partial index and the query were first keyed on `first_token_ms`. The handler
times itself unconditionally, but time-to-first-token is absent whenever the
model did not STREAM — so a deployment whose model returns whole replies would
have had an empty panel and no way to tell that from an idle one. Keyed on
`round_trip_ms`, which is the figure that always answers "was anything measured
about this turn".

### What is still NOT stored

A turn that FAILED. Nothing is written for one: the record is appended when a
reply lands, and a turn that produced no reply has nothing to append — the
table's own constraint refuses a turn that said nothing, which is correct for a
record of what was SAID. Those are in the log with their error code, and the
panel says so, because a latency history that silently omitted its failures would
read best exactly when things were worst.

## Wave 29 — an arc that is not tangent to its neighbours · **DONE**

`curve.go` has said since wave 20 what a corner radius cannot say: "an arc that
is NOT tangent to its neighbours: a crescent, a lens, an arc meeting a straight
edge at an angle. Those need a vocabulary with a plane in it, and no model has
been asked for one yet." This is that vocabulary.

A point may carry a `via`: one more point, and the edge ARRIVING at it is the
circular arc that passes through the via on the way. A cam lobe, a hook, a
D-shaped shaft, the leading edge of an aerofoil, the belly of a bracket that
clears something — all of them are one edge that BOWS, and none is a rounded
corner.

### The same answer arrived at twice

Wave 20 already explains why a radius plus two endpoints does not determine an
arc in three dimensions: it names one per plane through the chord, and that
ambiguity was measured producing a visibly wrong solid. Three points fix a
circle completely — plane, centre, size and which way round — so there is nothing
left to guess.

It is also, already, what the kernel is sent: `CurveEdge` has carried To and Via
since wave 20, because that is how a rounded corner reaches OCCT. So this adds a
way for a PERSON to say what OCCT could always build, rather than a new thing for
it to learn.

### The invariant that makes the rest simple

A vertex with an arc on either side is SHARP, and a radius written there is
ignored with a warning. Rounding an arc into an arc is a fillet between two
curves: the tangent point is no longer a fixed distance along a straight, and the
construction here has no answer for it. Ignoring rather than refusing follows the
rule wave 24 arrived at after refusing radii cost two whole parts for numbers
that meant nothing.

What that buys is worth stating on its own — **an arc edge always has sharp
ends** — so an arc's endpoints are exactly the two points that were drawn, and
nothing has to be trimmed against a corner arc that moved one of them.

### Three rules that had to change, and one that did not

**Three points is no longer the minimum.** A crescent is two arcs between TWO
points; a circular segment is one arc and one straight between the same two. Both
enclose area, and neither can be drawn with three points without inventing one
that is not part of the shape. So the floor is a property of the drawing: two
when something bends, three when nothing does.

**"Encloses no area" and "crosses itself" move to the flattened drawing when an
edge bows.** A crescent's three points are in line and a two-point lens has no
polygon at all, so the polygon's area is not the shape's. And an arc that bulges
far enough crosses an edge the straight version cleared by a mile — invisible to
a check on the points, which is why the check on the points is kept for the
common case, where it can still name the two points that cross.

**A via converts with everything else.** Left in inches while its endpoints
became millimetres it describes an arc bulging 25 times too far — and unlike a
wrong radius, which OCCT eventually refuses, a wrong via still builds. It builds
the wrong shape.

**A via on a repeated closing point travels to entry 0**, like the radius, and
with a cleaner argument: it describes the edge arriving at the repeated point,
which once the duplicate is dropped is exactly the closing edge. Same edge,
renumbered.

### What the fence found, and what it was right not to fence

The renderer/exporter comparison was first written to compare FACETS, the way the
sweep fence does. It went red on a crescent — with the two flattened outlines
byte-identical and the two solids the same solid. The difference was the ear
clipping: two triangulations of one planar polygon, which is the same surface and
has no observable consequence.

So the fence compares the DRAWING, which is what this wave decides and what is a
property of the shape: which way round each arc goes, how finely it is stepped,
where a closed run starts, and what happens to a via on a repeated closing point.
A facet comparison would have asserted all of that AND an implementation detail
besides, which is how a fence starts going red for reasons nobody can act on.

Noted in passing and not fixed: the existing sweep fence is stricter than it
needs to be for the same reason, and will eventually go red on a triangulation
change that means nothing.

### One error in a fence, which the kernel caught

`TestKernel_ABowedEdgeIsARealArc` first hard-coded the circle through its three
points and got it wrong — centre (0, −12.5) and r 37.5, where the real one is
(0, −17.5) and r 42.5. It failed against a kernel that was right to seven
significant figures. The constants are now DERIVED in the test. A constant nobody
can re-derive is a fence that eventually asserts an arithmetic slip and gets
"fixed" by changing the code.

## Wave 30 — an island in a hole · **DONE**

`profile.go` refused this in as many words: *"hole %d is inside hole %d. An
island in a hole is a second outline, and there is no vocabulary for one here."*
The vocabulary turned out to be one the drawing already had.

A loop contained in an ODD number of others is a void; in an EVEN number it is
solid. That is how TrueType glyphs, SVG paths, DXF and shapefiles all represent
it, and it needs no new field — which is the point. A `holes` entry inside
another `holes` entry is the post in an annular slot, the bar of a letter A, a
lug in the bottom of a pocket. It keeps going: a hole inside an island is a bore
through the post, which is an ordinary machined part rather than an edge case, so
the depth is not limited.

### Why the nesting is READ rather than declared

A new field would be a second way to say something the drawing already says, and
the two could then disagree. It is also not a guess: the loops are already known
not to cross — `holesFit` refuses that before this is reached — so containment is
a fact about the drawing and one point per loop settles it.

What the reader IS owed is to be told, and is. A hole drawn inside another is two
things at once: a real shape, and a bolt hole somebody put inside a pocket by
mistake. They are spelled identically and this build cannot tell them apart, so
it takes the reading the drawing supports and names it — "read as an ISLAND —
solid material standing in the void… if it was meant as a second bore, move it
outside hole 1".

### Where it had to be taught, and what kept the change small

Four implementations decide what a section is, and all four had to agree.

The Go tessellator and the browser both keep `merged` (one point list) and
`tris` (one triangle list). Making those a list of regions would have reached
into the extrusion, the revolve and the sweep — the three most fenced builders
here, each compared facet-for-facet against the other implementation. Instead
each solid area is bridged and clipped on its own and the results are
CONCATENATED, with the triangle indices offset. Every caller still sees one list
of points and one list of triangles, and none of them learned about nesting.

The walls are wound by PARITY rather than by position. `sectionLoops` winds
everything after the first loop clockwise, which is right for a hole and points
an island's wall INTO the material — invisible in a silhouette and wrong in
every file.

The kernel is TOLD. `Face(outer, inners)` treats every inner wire as a hole, so
an island handed to OCCT that way is cut away, and what comes back is a closed
valid solid with a piece missing — nothing but the volume distinguishes it. Each
solid area is its own face, and the solids they produce are fused; the areas are
disjoint by construction, so the union is exact. The nesting travels as
`hole_parents` rather than being recomputed there, because the kernel holds the
drawing as CURVES and containment is a question about polygons: it would have to
flatten them again at a fineness of its own choosing, and a kernel that nested
differently from the picture is the failure the section frame is also sent to
prevent.

### A drill this change made vacuous, and what that revealed

`sectionLoops` used to wind the first loop counter-clockwise and every other one
clockwise, and a drill inverted that to check the fence went red. After the first
version of this wave it STAYED GREEN — because `nestLoops` was re-winding
everything by parity afterwards, so the mutation had no effect and
`sectionLoops`' winding had quietly become dead code.

Two implementations of one rule, one of them unreachable, and the only thing that
noticed was a drill that stopped being able to fail. It also meant the REVOLVE,
which still called `sectionLoops` directly, would have wound an island's wall the
wrong way — a real defect that had not shipped only because nothing had drawn one
yet.

So the winding moved INTO `sectionLoops`, which is now the one place that decides
it, and `nestLoops` trusts what it is handed. The drill points at the surviving
copy and goes red again.

### What the fences compare, and why not facets

By VOLUME, through the divergence theorem, in both implementations. The
60×60 plate with a 40×40 pocket and a 20×20 post is 12 000 mm³ read correctly,
10 000 with the island cut away, and NEGATIVE if a wall faces the wrong way — so
one number separates all three failures. Facet-for-facet would have added an
assertion about ear clipping, which wave 29 established is not a property of the
shape.

## Wave 31 — the mesh says what it did not do · **DONE**

Found while looking at what a CSG engine in the browser would take. `Tessellate`
draws PARTS and has never performed a feature — a cut, a fuse, a fillet need a
kernel and this is a triangle builder. What it also did was say nothing.

So an OBJ or an STL of a bracket with four bolt holes contained four solid POSTS
standing on the plate, and the file carried no hint that the four cylinders in it
are the exact opposite of what they represent. The viewport has drawn a cut tool
as a warning-gold ghost since wave 15 for precisely this reason; the exported
mesh — the artefact somebody takes away and opens somewhere else, where FORGE
cannot caption anything — said nothing at all.

It is now reported twice, deliberately: once per FEATURE ("the material Hole 0
removes is NOT removed in this file… the cut is performed by the CAD kernel and
appears in the STEP export") and once per TOOL PART ("Hole 0 is a cut TOOL and is
in this file as a solid"), because the parts list and the group names in the file
are where somebody looks when they are wondering what a cylinder is doing there.

A document with no features says nothing about them. A note on every export is
noise, and noise is what makes a real warning invisible.

## Wave 32 — the planner gets a harness, and it found something · **DONE**

`cases.go` excluded the planner from the evaluation suite in a note: *"its
evaluation — does it refuse to guess when a goal is underspecified, does it
produce tasks that are actually independent — needs a project, a goal row and a
database, so it is a different harness rather than a longer list."*

Half of that was true and the important half was not. `Planner.Plan` takes a goal
STRUCT. Everything about it that is backed by a database — the project's
character, what a person has already settled, the recorded hazards — is optional
and nil unless somebody wires it. Both questions the note itself posed are
answerable with no database at all.

### What it found on its first run

**Every plan is a chain. 0 of 3.**

Asked to take a bracket from proposal to a released drawing, qwen3.7-plus
produced three, then three, then four tasks — and in every single plan exactly
ONE task could start. Each of the rest waited on the one before it.

A chain is a legitimate plan for genuinely sequential work, which is why the
scorer is tracked rather than floored. But a planner that always emits one has a
task DAG that is decorative: nothing proceeds in parallel, and one stuck task
stops everything behind it. That is the exact question the note posed when it
left the planner out, and nothing in this repository could see the answer until
now.

### And a floor that the measurement refused

`aWellSpecifiedGoalIsNotQuestioned` was written as a floor of 1 — obviously a
goal stating its own completion criteria should be planned rather than
questioned — and it measured **4 of 6**.

The reason is about the FIXTURE, not the model, and is worth keeping. Both goals
say what done looks like and neither says where the existing bracket IS, and
every question the planner asked was that one: *"which bracket, and where is its
geometry"*. That is a fair question about a goal a person would also have to
answer. A floor there would have demanded that the planner stop asking a
reasonable thing — which is exactly what this suite's rule about floors exists to
prevent, caught by the rule working.

### What IS floored, and why that one

`noTaskExceedsTheGoalsRiskCeiling`, at 1, measured 6 of 6 across an r2 goal and
an r1 one. Not a judgement about quality: a goal's ceiling is set by a person,
and a task above it is work the executor refuses — so the plan stops half way
waiting for an approval that cannot be given. There is no acceptable rate above
zero, and the measurement says the model is not near it.

### A meta-fence that had to change, and how narrowly

`TestCapabilityRatesAreTrackedAndOnlyTheRequirementsAreFloored` required every
capability case to carry a floored SHARED HONESTY scorer — `speechIsShort` or
`standardsAreLabelled`. That is really "the case must be able to fail", written
when every case was a conversation. A planner case has no speech and names no
standards, so the rule would have forced it to carry a scorer measuring nothing.
The exemption is written as what it means — a planner case must still carry at
least one scorer that can fail — rather than as a hole.

### And an operational finding, which is why the run took 21 minutes

The planner role runs on `qwen3.8-max` with deliberation ON, correctly: llm.go
says an executor should think hard, and only conversation is latency-bound. But
the planner does NOT stream — a plan is useless until its last task arrives — so
the whole call has to fit inside `FORGE_LLM_REQUEST_TIMEOUT`, which defaults to
3m.

Measured 2026-09-07: one plan of this repository's own reference goal took **128
seconds** for 2982 reasoning tokens and 4493 completion tokens — 71% of the
budget — and the first attempt at this run hit the timeout on three consecutive
retries and gave up. The same call with deliberation off took 17 seconds.

The default is left at 3m and the measurement is written into `.env.example`
beside it. Raising it would hide the shape of the problem: a planner that takes
two minutes is one a person is waiting on.

### What is still NOT measured, and where a database would come in

**Hazard coverage.** SAF-02 requires an r3+ plan to account for every recorded
hazard, and `checkHazardCoverage` is real. It reads the project graph, so it
needs a workspace, a project and rows in it. A case here plans with no hazards,
which means the rule is vacuously satisfied and is not under test.

**Planning on top of what a person settled** (RSN-02, RSN-03). Also rows.

Both are stated rather than approximated. A case that invented a fake hazard in
memory would be measuring the fixture.

## Carried defects

Most of what is carried here is closed. What remains is not an oversight: each
entry says what it would actually take, and several are decisions rather than
gaps — kept so nobody re-opens them as work.

Two of those closures are from 2026-09-06 and one of them changed what the
others mean: the entry filed under "Operational — blocking" said the model
provider account was in arrears, and it was not. Nothing was blocked. See wave 25
— and note that "blocked on X" is a claim with a shelf life exactly like a "still
NOT done" list, and this one had gone stale in the direction that stops work.

### Closed

- **Tool inputs are now validated against their declared schema.** They were
  documented as checked and never were, on every tool — including `shell_run`,
  which requires a `reason` for the audit trail that nothing enforced. A bounded
  JSON Schema subset validates at the executor boundary, and a schema using a
  keyword this build cannot enforce is REFUSED AT REGISTRATION rather than
  half-checked. `docs/bugfix/2026-09-03-tool-inputs-were-never-validated.md`
- **Prompt-injection defence (SEC-04) exists**, as mitigation and detection —
  which is what is honestly available and what `internal/agent/untrusted.go`
  says. Tool output is framed in an envelope naming its source and stating that
  nothing inside is an instruction; content cannot forge the fence and escape
  into the frame; a `secret://` handle inside untrusted content is neutralised
  (the one transformation with no false-positive reading); and suspected
  directives are recorded in the log and on the timeline rather than stripped.
  Nothing is rewritten: the safe rewrite of arbitrary prose is not defined, it
  would destroy the tool's purpose, and it would leave the caller believing the
  content was now trustworthy.
- **A superseded variant can be chosen.** `POST /v1/geometry/{id}/adopt`,
  `forgectl geometry adopt`, and a button in the comparison view. It APPENDS the
  chosen geometry as the current version, whose inputs name the variant it came
  from, and the person rules on that — so the history stays append-only,
  `superseded` keeps meaning what it says, and "we went back to v1" is a fact
  the ledger records rather than one it hides. Option 1 of the three; option 3
  was rejected on sight for splitting the lifecycle's meaning by artifact kind.
- **Planning failures are recoverable.** `forgectl goal replan` and
  `POST /v1/goals/{id}/plan` plan a draft that has none — the state a tripped
  planner leaves, since Draft commits the goal and Apply writes nothing. Only a
  draft with no tasks: replacing a plan that already has tasks needs a way to
  retire them and this build has none, so the refusal says that rather than
  adding a second plan beside the first. The no-tasks guard also moved DOWN into
  `PlanApplier.Activate`; it lived only in the HTTP handler, so `forgectl goal
  start` went past it and produced the state the handler existed to prevent.
- **`updated_at` triggers are asserted.** The tables are enumerated out of
  `pg_trigger` rather than a list — so a table that attaches the trigger is
  covered the day it is added — and the fence performs an update that does not
  mention the column, because setting it by hand would test the UPDATE rather
  than the trigger.
- **Organisation memory's audience is enforced**, and the defect's premise was
  stale: SEC-02 brought PROJECT membership, not an organisation entity, and
  there is still no table to check one against. Naming an audience the system
  cannot identify was the claim, not the gap. The audience is now stated as what
  it is — everybody with an account in this deployment, which is correct for a
  private single-tenant installation — and refused to an unscoped caller, which
  previously read org knowledge with no identity at all. A table maps every
  declared visibility to the check that enforces it, and a fence holds the two in
  step. **What still does not exist** is a boundary between two organisations
  sharing one deployment; a multi-tenant installation needs an organisation model
  and this layer would hang off it.
- **The two CLI commands are fenced.** `memory forget` and `memory purge` now
  parse through functions separate from the doing, with six fences over the shape
  that shipped broken twice. All four commands with a positional-then-flags shape
  are now held by a fence rather than by convention.
- **Part ids survive a revision — measured, and the fix was measured too.** The
  on-screen note listed part NAMES, so the model had never been shown the ids
  `converse.go` asks it to reuse. Adding them took the evaluation suite's tracked
  rate from **1 of 4 to 4 of 4**, every id carried over in every run. Still
  tracked rather than floored: that is one run of one model, and a floor set from
  a single good measurement is a target dressed as an observation. The
  comparison's match-by-name fallback stays either way — it reports which basis
  it used, so it is honest whichever way the number goes.

- **The ten industries are selectable, and the domain pack is finally read**
  (2026-09-04). The product's industry selector offers ten domains; five had no
  pack at all, four had one that refused every project at the door, and the
  column recording the choice was **written by EnsureProject and read by
  nothing** — while both of its writers passed a constant, so every project in
  every deployment was `software` or `general`.

  Three changes, in dependency order. **Availability became a tier ceiling**: a
  pack limits how far work goes rather than whether it starts, because
  `mechanical` was closed over drawing release (R3) and that also closed concept
  CAD (R1), which is the work. **The pack got producers** — `goal new
  --industry`, `project industry --set`, and `industry` on `POST /v1/goals` — and
  an unstated one resolves to `general`, the pack that *means* unknown, rather
  than to the old `"software"` constant or to a guess. **The pack got readers**:
  `grantFor` takes the lower of the goal's tier and the pack's, and the
  conversation is framed in the domain's units and vocabulary.

  Two things fell out of it. The fabrication guard was **mechanical and
  electrical only**, so nine new industries would have shipped with it silent —
  extending it found a truncation bug (`IEC 61508` reported as the non-existent
  `IEC 615`). And the safety fence asserting those packs were unavailable had to
  be **restated rather than removed**: it now holds that no engineering pack
  reaches R2, that medical and robotics remain uncreatable at every tier, and
  that no row can authorise R5 — strictly more than it held before. Every fence
  was mutation-drilled.
  `docs/bugfix/2026-09-04-the-pack-was-written-and-never-read.md`.

- **The domain pack is now a rule set rather than a label** (2026-09-04, second
  wave). Six things the PRD says a pack bundles were absent; five landed and the
  sixth was deliberately not built.

  **Schema, geometry frame, data-handling rules and required adapters** are
  declarations on the table, each with a consumer: the schema drives a validator
  in `graph review`, the frame and the handling rules reach the model on every
  turn, and the adapters let a refusal name which solver a domain wanted. All
  four are plain strings so the pack stays a leaf — workspace, geometry and tools
  import it — with a fence in each of those packages holding the vocabularies
  together.

  **Validators** reuse `Review`'s existing Gaps rather than a new channel:
  "incomplete" is a property of a graph IN A DOMAIN, and gaps are already
  "expected, worth showing, never a failure". Geometry deliberately got none —
  its frame reaches the model and its units are already fenced, and a warnings
  channel with one consumer is surface invented ahead of a need.

  **Qualified review** is the only piece that widens what the system will do, and
  it is documented on its own in `docs/qualified-review.md`. A named, attributed
  authority raises an engineering domain's ceiling to r2; nothing else does; and
  every surface says "recorded, not verified" because this build cannot check a
  licence. Three mutation drills, each confirmed red then restored.

  **Tool adapters were NOT wired to real backends**, and that is a decision
  rather than a gap — see below.

### Not closed, and what each would take

- **SSO is out of scope, by decision** (2026-09-04). Email and password, with
  RBAC, MFA and device trust behind them, is the authentication this product
  ships. SEC-02 names SSO and this build will not have it; that is a stated
  position rather than a gap, and the entry is kept here so nobody re-opens it as
  one. The original entry follows for context: it was blocked on an IdP tenant to
  verify against, because building it against a fake is the one thing that must
  not happen — this codebase's own history is that a stub matching a provider hid
  five shipped defects, and an unverified authentication path is worse than none.
- **FORGE now has a voice in a room** (wave 9.6), so this entry is closed. What
  remains is a product question, not a gap: when FORGE should speak without being
  asked. The original
  entry follows for context: wave 9 built the live session spine —
  rooms over HTTP, presence, and an SSE stream that delivers a turn to everybody
  in the room — so COL-01 is no longer record-only. What is still missing is
  audio itself: the SFU, per-speaker streams, transcription into the record, and
  the privacy surface SEC-06 asks for (visible recording state, retention-free
  mode, independent audio deletion). The premises are proven rather than
  assumed — see `docs/spikes/2026-09-03-webrtc-sfu/`. **What it needs:** waves
  9.2 to 9.5, in that order.
- **Re-measured against qwen3.7-plus, and it produced the finding the suite
  exists for** (2026-09-06). The 2026-09-04 run below stands as a record of
  qwen-plus; that model no longer exists at this endpoint (wave 25), so the
  suite was re-run whole against its successor: **180 of 198 scorer-runs held,
  every FLOORED scorer at 100%**, and eighteen tracked scorer-runs short across
  four cases.

  Every one of the eighteen was **one defect**, and not a capability going dead:
  an expression written where a number was expected, which discarded the entire
  reply. `draws-a-turned-part` scored 0 of 3 and `covers-product-design` 0 of 3
  entirely from it. It is also the unexplained "simply malformed, intermittently"
  recorded against the V-belt pulley on 2026-09-05 — the same case, four weeks of
  contract growth and two models apart.
  `docs/bugfix/2026-09-06-an-expression-where-a-number-was-expected.md`.

  **`draws-a-closed-loop` now has scored runs.** It was carried here with none,
  and it is the case the wave 24 fixes most affect.

  **The planner is now in the suite too** (wave 32), which closes the last piece
  of this entry that was outstanding: its exclusion rested on needing a database,
  and it does not. Its first run found that every plan it produces is a CHAIN,
  0 of 3 — the exact question the exclusion note posed.

  **The floors are still Tracked, and after this they should stay that way for
  longer than one more run.** A model change moved eighteen scorer-runs; a floor
  set from a single good measurement against a model that can be retired
  underneath you is a target dressed as an observation, twice over.

  The original entry follows for context. Against **qwen-plus at `--repeats 3`**,
  all fourteen cases scored **3/3 on every scorer** — four regressions and ten
  coverage cases. The coverage scorers stay **Tracked**: one clean run of one
  model is exactly the measurement that must not become a floor.

  The domain conventions demonstrably reach the model. Architecture replies used
  *massing, circulation, core, envelope, daylight*; every industry's terms
  scorer held in all three runs. `covers-other` — the control, whose `general`
  pack asserts no vocabulary — still answered substantively rather than
  deflecting, which is what makes the other nine readable as a comparison.

  **The unresolved part:** the first full run reported "at least one scorer fell
  below its floor", and the evidence was destroyed before it could be read — the
  run was piped through `tail -120`, which discarded eleven of fourteen cases and
  also swallowed the real exit code (the pipeline reported `tail`'s 0, not the
  eval's 1). Re-running those eleven produced 3/3 throughout, so the failure did
  **not** reproduce.

  Two explanations fit and this build cannot distinguish them: model variance on
  a floor that requires 3/3 over three runs (`speechIsShort`, at 90%, is the
  most fragile), or a transient API error — an errored run is deliberately kept
  apart from the scores, and `Met()` on zero scored runs is false, so an outage
  reads as below-floor. It is recorded here rather than closed, because "it
  passed the second time" is not a diagnosis.

  **What would settle it:** always run with `--json`, never through a pipe. A
  second full run would give a second sample; it would not recover the lost one.

  **Since closed structurally, not by diagnosis.** `EVAL_REPORT` in the Makefile
  writes the full report on EVERY run, so the irrecoverable-by-accident state
  cannot happen again whatever the terminal does. Two further full runs have
  since been taken (2026-09-06) and neither reproduced it. The original failure
  remains undiagnosed and is left recorded, because "it passed three more times"
  is still not a diagnosis — but nothing further can be learned about it, and the
  thing that made it unlearnable is fixed.

- **No real FEA or SPICE backend, by decision — and a real CAD one since wave
  14.** Packs now declare which
  adapters their domain needs and every one is `CONNECTOR_UNAVAILABLE`. Wiring a
  real one was considered and rejected against this repository's own completed
  research: `docs/spikes/2026-09-02-zoo-text-to-cad` concludes "do not integrate
  Zoo now… that is not a component you drop in; it is a subsystem, and it
  replaces the thing our product is currently for", and puts the cost at a
  websocket a browser cannot open, a stateful project mirror and a 56 MB CLI to
  export. **What it would take:** the spike is the estimate. Revisit when the
  product needs a manufacturable artefact rather than a shape to talk about.

  **Amended 2026-09-05.** That conclusion was about integrating *Zoo*, and it
  stands. It is no longer the whole answer about a CAD kernel:
  `docs/spikes/2026-09-05-parametric-cad-kernel/` installed build123d on
  OpenCASCADE here and built a valid 37-face B-Rep, exporting real ISO-10303-21
  STEP, in **46 ms** — against the 180 s the Zoo spike measured, which was the
  agent thinking rather than the kernel working. The latency argument for keeping
  a build out of a conversational turn is therefore **wrong**, and only the 2.5 s
  import cost is real. What is still undone is the wiring, not the feasibility.

  **Amended again 2026-09-06.** The wiring is done, and has been since wave 14:
  the sidecar is a long-running process, `make test-cad` runs against a real
  OpenCASCADE, and waves 26 and 29 both added kernel behaviour with kernel tests
  behind it. The *entry* is still open and still means what it says — no FEA and
  no SPICE. Every adapter a pack declares is `CONNECTOR_UNAVAILABLE` and fails
  loudly, by decision.

- **The industry IS now inferred — as a suggestion.** The planner returns what
  domain it made of a goal on the reply it was already producing, and Intake
  writes it into the project graph as an assumption that changes nothing. The
  original entry read "never inferred, only stated"; that is now half true and
  deliberately so. **What is still not done:** nothing acts on the suggestion
  automatically, and nothing should — a guessed domain that became the rule set
  is the defect this whole area removed.

- **The renderer still cannot draw a cut hole, and the honest question is now a
  different one** (updated 2026-09-06). Wave 15 recorded this as "cannot without
  a CSG engine in JavaScript". That framing predates wave 14's kernel and this
  repository's own conclusion about duplicated derivations, and following it
  would mean writing a boolean engine in ES5 that must agree with — nothing. The
  Go tessellator has no CSG either, so a browser that cut holes would show a
  shape the exported OBJ and STL do not contain, which is the reverse of the
  property this layer exists to keep.

  **What changed today (wave 31):** the mesh export now SAYS which features it
  did not perform and which parts are tools. The viewport already said it. So the
  two agree, and both are honest, which is the state wave 15 described but only
  half achieved.

  **The two ways forward, and why this is a decision rather than a task.**
  Writing CSG twice — once in Go and once in JavaScript, kept in step by a fence
  — is a large, numerically delicate subsystem, and this codebase has recorded
  what happens to duplicated derivations more than once. Asking the SERVER to
  tessellate the solid OCCT already builds is far smaller and has one producer,
  but it puts a network round trip and a kernel dependency behind the viewport,
  and a deployment with no kernel configured is a supported configuration that
  would fall back to the ghost. Neither is obviously right; both change the shape
  of the product, so neither was chosen unilaterally.

- **This deployment has no voice, and that is a deployment decision** (new
  2026-09-06). The provider retired the models behind the transcriber and speaker
  roles along with the conversation one (wave 25). The conversation role had a
  successor on this endpoint; the audio roles do not. It offers
  `qwen-audio-3.0-realtime-plus`, which answers the chat-completions
  transcription shape with `{"status_message":"Success"}` and no transcript, and
  `qwen-audio-3.0-tts-plus`, which returns 500 to it.

  So `make test-asr` cannot pass here and FORGE cannot speak or listen on this
  installation. **What it needs is a decision, not code:** either point
  `FORGE_LLM_BASE_URL` at an endpoint that serves an ASR and a TTS model — with
  the key issued for that host, because this one is host-specific — or accept
  that this deployment is text-only. Nothing has been substituted in the
  meantime, which is the same discipline the vision model already follows: an
  absent capability that says it is absent.

  Note what this does NOT block. The workbench, the evaluation suite, the kernel
  and every fence run without it; wave 9.6's "FORGE has a voice in a room" is
  about the session spine, which is unaffected. What is unavailable is audio in
  and audio out at this endpoint.

- **Pre-migration events are unattestable.** 11 events on the dev database
  predate the audit chain. This is permanent BY DESIGN and must not be "fixed":
  backfilling would mean minting attestations for events nothing attested, which
  is forging exactly the evidence the chain exists to provide. `audit verify`
  reports them as unattestable rather than passing over them, which is the
  correct behaviour and the only one available.
