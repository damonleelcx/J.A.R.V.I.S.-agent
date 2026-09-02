# FORGE

**A durable, long-running AI engineering partner.**

FORGE is built on one governing principle:

> **A long-running agent must not be a long-running LLM call.**

It is a durable workflow system in which a model repeatedly wakes up,
reconstructs its state from a database, performs a *bounded* amount of work,
persists the result, and safely continues later — across crashes, restarts,
deploys, and model failures.

```
User Goal → Planner → Durable Task Store → Queue/Scheduler → Agent Worker
              ↑                                                   ↓
              └──────── Replan ←── Checkpoint ←── Verify ←──── Tools
```

`Agent = Model + Harness + Durable State + Workflow Engine + Tools + Scheduler + Recovery + Evaluation + Observability`

FORGE's product definition — voice-first collaboration, the project model,
risk tiers, domain packs, and the human-authority model — is specified in
[`docs/prd.md`](docs/prd.md). Its architecture is in
[`docs/architecture.md`](docs/architecture.md).

> 中文版：[README.zh-CN.md](README.zh-CN.md)

---

## Status

FORGE is under active construction and is delivered in phases. Each phase is
independently tested and pushed. **Nothing below is claimed until its tests run
green in CI against a live Postgres.**

| Phase | Scope | State |
|-------|-------|-------|
| 0 | Foundation: config, database, migrations, error registry, event registry, structured logging, identifiers, CI | ✅ Done |
| 1 | Identity: sign up, sign in, verify email, reset password, sessions, HTTP surface | ✅ Done |
| 2 | Durable engine: goals, task DAG, job queue, leases, checkpoints, timeline | ✅ Done |
| 3 | Agent loop: planner/executor/verifier, context assembly, budgets, approval gates, persona | ✅ Done |
| 4 | Tools: capability registry, sandbox, honest unavailable connectors | ✅ Done |
| 5 | Console: goal management, execution timeline, approvals UI | ✅ Done |
| 6 | Workbench: voice conversation, 3D studio, provenance | ✅ Done |
| 7 | Evaluation suites, recovery drills, release | ⏳ Next |

---

## Quick start

**Requirements:** Go 1.26+, Docker (for the local Postgres), `make`.

```bash
git clone https://github.com/damonleelcx/J.A.R.V.I.S.-agent.git
cd J.A.R.V.I.S.-agent

cp .env.example .env
# Fill in the two required secrets:
#   FORGE_SESSION_SECRET   openssl rand -base64 48
#   FORGE_LLM_API_KEY      your DashScope (Qwen) API key

make db-up          # start Postgres on :55840
make migrate        # apply the schema (idempotent — safe to re-run)
make health         # confirm connectivity
make check          # formatting, vet, and the full test suite
```

`make help` lists every target.

---

## Design decisions worth knowing before you read the code

### The migration chain re-runs on every boot

`db.Migrate` applies **every** migration on **every** start, rather than
skipping those the ledger says are applied.

*Why.* The schema in the database is the truth; the ledger is a record, not an
authority. A ledger row that says "applied" when the DDL was rolled back would
permanently suppress a real migration — silently, and unrecoverably without
manual surgery. Re-running everything also means idempotency is exercised on
every single boot, so a migration that is not re-runnable fails the first time
anyone restarts, rather than months later during an incident.

The cost is a few milliseconds of no-op DDL per boot. `TestMigrationsAreIdempotent`
runs the chain three times against a live Postgres and compares full schema
snapshots — it catches both "errors on re-run" and the subtler "succeeds twice
but leaves different state".

### The verifier is a different model family from the executor

PRD **SAF-03** requires that a high-risk conclusion be checked by a method
*independent of the generative path*. A model grading its own output is not
independent. So `FORGE_LLM_VERIFIER_MODEL` defaults to a different vendor family
than `FORGE_LLM_EXECUTOR_MODEL`, and `config.Load` emits a warning when they
share one. This is a safety control expressed as configuration, not a cost
optimisation.

### Error codes and event names live in exactly one place

Every operator-visible failure carries a stable code, a cause, and — mandatorily
— a **remedy**. A failure that cannot tell the reader what to do next is a dead
end, and dead ends are how a long-running agent strands a human at 3am.

Both registries are fenced by tests that *parse this repository's source* rather
than enumerating what they check. A fence that lists its own subjects is
vacuous: deleting an entry would just make the loop shorter.

### Sign-up reveals; password reset does not

Sign-up returns `EMAIL_ALREADY_REGISTERED` for a taken address. Password reset
returns the same 202 whether or not the address exists, and sign-in returns the
same `INVALID_CREDENTIALS` whether the account is unknown or the password wrong.

The asymmetry is deliberate. Hiding it at sign-up costs a user who forgot they
had an account a confusing dead end — success, no usable link, no hint to sign in
instead — and discloses little for a product where accounts use work addresses.
Hiding it at reset is not optional: that endpoint is unauthenticated, and *"which
of these leaked addresses has an account?"* is exactly the question asked before a
credential-stuffing run. Sign-in even performs a dummy hash when no account
matches, so "unknown address" is not measurably faster than "wrong password".

### One clock owns every compared timestamp

Session `created_at` originally defaulted to the *database's* `now()` while
`password_changed_at` was written from the *application's* clock — and
`Session.Live` compares them. Any skew between the two machines was a live bug:
an app clock a second ahead made every fresh session look like it predated the
last password change, signing users out the instant they signed in.

Every timestamp compared against another is now supplied by the application
clock, and the repository refuses a zero value rather than silently falling back
to the database default.

### Email links are not consumed by a GET

Mail scanners, link previewers, and corporate security gateways follow every URL
in an incoming message. If `GET /auth/verify-email` redeemed the token, users
would routinely arrive at "already used" — used by a robot seconds after
delivery. The GET renders a page; redemption happens on the POST the reader
triggers.

### `skipped` needed two meanings, so the reason is recorded

A task can end `skipped` for two opposite reasons, and everything downstream
depends on which:

- **Removed by a replan** — the work is no longer needed, so tasks waiting on it
  should proceed. Otherwise deleting one task deadlocks everything after it.
- **Unreachable because a dependency failed** — tasks waiting on it must also be
  skipped. Otherwise the failure stops propagating one level down and leaves a
  tail of permanently pending tasks, which looks exactly like a goal still working.

The first version of the engine had only the status, so it could satisfy one
rule or the other but never both — propagation stopped after a single level. The
reason now lives in `error_code`, and both the promotion and propagation queries
branch on it. Each half is fenced by its own test, and each test was drilled
against the other half's implementation.

### The workbench streams speech before it finishes thinking

A conversational turn that produces geometry takes ~13 seconds end to end on this
provider. PRD **AUD-02** asks for first audio inside 700ms. Waiting for the
closing brace of a JSON reply makes that impossible by construction.

So `/v1/converse` is Server-Sent Events: the `speech` field is emitted the moment
it closes — measured at **266–595ms** in the browser — and the geometry follows
seconds later. The structured tail is still applied only when the *complete*
document parses, so streaming buys the latency without letting a half-finished
parts array reach the viewport.

The workbench displays the measured figures, never the target. A target asserted
without measurement is a marketing claim.

### A render says what it does not establish

The provenance banner is not dismissible. PRD **VIS-06**: photorealism must never
imply manufacturability, structural adequacy, or compliance — and a render is
persuasive in inverse proportion to how much has actually been checked.

Three things feed it: what the model says it did not verify, what it assumed
rather than was told, and **what the renderer could not draw faithfully**. That
last one exists because an early version silently substituted a box for an
unsupported shape, producing a parts list that said `triangle-prism` beside a
render showing a rectangular block. Nobody was told. That is the same class of
failure as reporting an unverified task as verified.

### Forgetting has to hold against an agent that keeps learning

FORGE writes its own memory. So a user deleting an item cannot be a `DELETE`: the
next cycle that observed the same thing would write it straight back, and nothing
would report that the deletion had been undone.

A forgotten item therefore keeps its row and its key and loses its value. The key
goes on occupying its layer's unique index, and a later write to it is refused
with `MEMORY_FORGOTTEN` — by the agent's own tool as much as by the API. What is
kept is only that somebody asked, when, and why; the content itself is cleared,
because that is what was asked for.

Re-opening the key is a separate command (`forgectl memory purge`), logged at
WARN, and it refuses to act on an item whose deletion was never recorded —
otherwise it would just be an unrecorded delete with a different name.

### FORGE is not asked how it knows something

The `memory_remember` tool has no input for the epistemic label, and this is the
whole design rather than an omission. A tool that accepted `"how": "observed"`
would get it, and the label would be exactly as reliable as the fabricated NEMA
17 bolt pattern that caused the vocabulary to exist — a component cannot be its
own guard.

So the label is derived from the only thing structurally true at that moment: a
fact the model chose to write down is a conclusion it drew from context, which is
`inferred`. Recall then hands back `may_be_acted_on` per item rather than leaving
the model to interpret its own labels. A stronger label has to come from
something checkable — a tool's actual output, a file actually read — and that
will be a different tool, not a wider input schema.

### A node's kind never changes

An assumption that turns out to be true does not become a requirement. It would
be one edit and it would be wrong, because the entire value of having labelled it
an assumption is that somebody can later ask **what did we build on top of a
guess?** — and answering that means the assumption has to still be there.

So promotion creates a requirement, draws a `derives_from` edge back to the
assumption, and leaves the assumption in place, retired rather than deleted: no
longer in force, still readable. Both things are true at once, which is what
actually happened.

There is no `kind` field on the edit endpoint and no `UpdateKind` in the
repository. The refusal names the alternative rather than merely declining.

It is the same rule as decision supersession, and as the audit chain: history is
added to, never rewritten.

### "The tests passed" and "a person accepted it" are different columns

Every artifact version carries both a verification state, which is what a machine
found, and a human disposition, which is what a person decided. Nothing derives
one from the other, and `Usable()` requires both.

One column for both would be simpler right up until the day a passing test suite
set it to "accepted", at which point the record would assert that somebody signed
off on something nobody looked at. PRD **SAF-05** says the AI's approval is never
acceptable authority; this is that requirement in storage.

The same split runs through the graph's review, which reports what a project
**contradicts** separately from what it **lacks**. Only contradictions fail the
check. Every real project has requirements nothing verifies yet, and a check that
is always red is a check somebody turns off in a week.

### The model never holds a credential, and never reads one back

FORGE brokers secrets rather than storing them. The database holds a declaration
— a handle name, which tools may receive it, and the environment variable the
value is read from — and the value itself never lands in Postgres. Whatever
already puts secrets in a process environment stays the custodian.

The model is told `secret://github_token` exists and what it is for. It writes
that string where the value belongs, and the executor substitutes at the tool
boundary, only for a tool that has been granted it. A handle that cannot be
resolved is **refused**, never passed through: a request that goes out with
`Authorization: Bearer secret://github_token` fails for a reason that has nothing
to do with credentials, and the model then debugs the wrong thing for the rest of
the run.

Substitution on the way in is the obvious half and is worth nothing alone,
because the tool's output goes back to the model — a shell echoing its
environment, an HTTP client logging the request it sent, an error quoting the
header it choked on. So every resolved value is scrubbed out of the output, the
raw output and the error text before either the model or the ledger sees them,
including the encodings something in the path actually applies. A value that
survives anyway is caught by a second check and the whole result is discarded:
losing a tool result is recoverable, and the other is not.

What this does **not** do is stop a tool that was legitimately given a value from
sending it somewhere. Scoping decides who receives a credential, not what they do
with it.

### Evidence is preserved before anything is stopped

An incident response that halts the goal, revokes the credential and rolls the
artifact back, and then gathers evidence, has gathered the evidence of its own
response. So stop, revoke, quarantine and roll back are **refused** on an
incident that has preserved nothing — a refusal rather than a warning, because
the moment it fires is the moment nobody is reading warnings. A dry run is always
allowed: it changes nothing, and rehearsing before capturing is exactly right.

What gets captured includes the audit chain's verdict at that instant. Once the
response has written its own events, "was the chain intact when this started?" is
no longer a question the verifier can answer.

### A drill has to prove its fault landed

`forgectl drill run` injects real faults — a worker that dies holding a lease, a
dependency that fails terminally, a checkpoint that becomes undecodable — and
then asks NFR-07's questions of what is left.

The failure mode of every drill harness is an injection that silently did
nothing, after which every invariant passes and the report is a page of green
ticks about a system nobody disturbed. So a scenario does not get to say it
injected a fault; it carries the evidence that proved it, and one without that
evidence is reported VACUOUS and fails the run. So does a selection that matched
no scenarios.

### `owner_id` is not who may see it

Authorisation reads project membership. `forge_projects.owner_id` records who
*created* a project and is deliberately never consulted.

Two authorisation paths means two answers to "may this person read this", and the
day they disagree is the day somebody sees something they should not. So there is
one path, one function, and a test that parses the HTTP package's SQL and fails on
any query that authorises by that column — because the likely mistake is not
malice, it is somebody adding an endpoint and copying the shape of its neighbour.

A project's last owner cannot be removed or demoted. A project with no owner
cannot be administered at all, including to undo the change that emptied it.

### A second factor that cannot lock you out, and device trust that is not a bypass

Enrolling a second factor does not enable it. It stays pending until one correct
code proves the authenticator actually holds the same secret — because the user
whose app silently failed to save it cannot sign in to fix that, and the fix
requires a code.

"Remember this device" is granted by the same call that verifies a code, and by
nothing else. Any other route would make it a way to opt out of MFA permanently
after one password. Trust expires after thirty days, and disabling the factor
untrusts every device: a device trusted under a factor that no longer exists is
trusted on the strength of nothing.

### Every turn in a room names who said it

There is no anonymous speaker and no default — enforced in the domain and again by
a database constraint. Six months later, "somebody said the tolerance was fine" is
worth nothing, and "Priya said it at 14:02, and Tom approved the change at 14:05"
is the entire point of keeping the record.

FORGE's own turns are a distinct kind rather than a turn with no user, so a
transcript can never suggest a person said what the system said. Speaker names are
stored as they were at the time: rendering them later from the accounts table
would show who somebody is now, not who spoke.

What is NOT built is realtime multi-party audio. The record is
transport-agnostic and a transcript is useful long before its audio is.

### The voice surface moves; it does not shrink

The workbench has one voice component with two placements. Before there is
anything to look at it is the centre of the stage at full size — orb, caption,
state word, microphone and text box — because at that moment the conversation
*is* the product. The first time geometry arrives it moves to the bottom-left
corner and the model takes the stage.

Everything reachable in one placement is reachable in the other: the microphone,
the text box, hands-free, and stop-speaking. PRD **AUD-06** requires a non-audio
path for every critical interaction and **AUD-07** requires stop to be always
reachable, so a compact placement must not be a reduced one. It is the same DOM
element with a different `data-place`, not two components to keep in sync.

At the centre of it is FORGE herself — the character portrait from
`assets/portrait/`, with the state sigil badged on it, both served by the
application so `persona.ExpressionFor` stays the only thing that decides which
face belongs to which state. The canvas behind her draws the **aura only**: glow,
waveform, and the two rings. An earlier version drew the sigil's three blades as
vector paths on that canvas; that was a second hand-maintained copy of an
identity with exactly one source, and it was removed.

The badge and the state word beneath say different things on purpose. The badge
carries FORGE's own state in the sigil's vocabulary — `thinking` while a model
call is in flight, `blocked` while a proposal waits on a decision, `idle`
otherwise. `working` is never used here: it means a tool is running outside this
process, and nothing on this page does that. The word underneath carries the
voice surface's state — listening, speaking — which is not the same vocabulary
and does not borrow from it.

The waveform is driven by a real measurement — an `AnalyserNode` on the
microphone — but **only while listening**. In every other state there is nothing
to measure, so the motion is a slow symmetric breath rather than the ragged band
speech produces: decoration must not be drawn in the shape of a reading. The
meter's stream is opened and closed with each listen rather than held, so the
operating system's microphone indicator stays off when nobody is holding the
button. A visual nicety must not be the reason a privacy property stops being
true.

### Starting work from the workbench takes two presses

A conversation can propose work. It cannot start it.

"Start this" calls `POST /v1/goals`, which writes a **draft** goal and plans it.
Nothing is claimable, no worker can touch it, and the plan — every task, its risk
tier, and which ones need an approval gate — comes back and is shown. Only then
does a second button call `POST /v1/goals/{id}/start`.

The obvious single endpoint was rejected: PRD **AGT-02** requires a scoped plan
and preview before material action, and **AGT-04** forbids autonomy being raised
without the person seeing it. One call would create the plan and start it inside
one press, and the person who pressed would have authorised a list of tasks they
never read.

`forgectl goal new` and `forgectl goal start` are the same two steps, and both
surfaces run the same `agent.Intake` underneath, so the terminal and the browser
cannot drift apart about what an unspecified goal means. Activation is recorded
on the timeline as `goal.activated` by `human` with the account id — PRD
**AGT-07**: a consequential transition carries the named human authority. Before
this existed, that transition wrote nothing at all.

An active goal still only executes while a worker is running. The interface says
so rather than letting "active" imply progress.

### Seven limits, not one

An agent can run away along seven independent axes — iterations, tool calls,
tokens, cost, wall-clock, task depth, and total task count. A bound on one of
them is not a bound. All seven are in `EngineConfig` and validated at startup.

---

## Repository layout

```
cmd/forgectl/            Operator CLI: migrate, health, config, version
internal/platform/       Cross-cutting infrastructure
  config/                Environment loading and validation
  db/                    Postgres pool, transactions, migration runner
  db/sql/                The migration chain (embedded into the binary)
  errs/                  Central error-code registry
  logx/                  Structured logging and the event-name registry
  id/                    Prefixed, time-sortable identifiers
  clock/                 Injectable time source
docs/                    PRD, architecture, decision records
```

Migrations are embedded into the binary (`//go:embed`) and live in exactly one
directory. A binary that needs a sibling directory in order to migrate is a
binary that can be deployed in a state where it cannot start.

---

## Building inside a Go workspace

If you check this repository out inside a directory tree containing a parent
`go.work`, plain `go build ./...` fails:

```
directory ... is contained in a module that is not one of the workspace modules
```

The `Makefile` sets `GOWORK=off`, so `make` targets work either way. For ad-hoc
commands, prefix them: `GOWORK=off go test ./...`

---

## Licence

Apache License 2.0 — see [LICENSE](LICENSE) and [NOTICE](NOTICE).

The character artwork under `internal/httpapi/assets/portrait/` was supplied by
the project owner and is **not** covered by that grant.
