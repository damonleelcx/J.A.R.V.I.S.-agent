<div align="center">

<img src="internal/httpapi/assets/portrait/calm.png" width="168" alt="FORGE">

# FORGE

**A durable engineering partner.**

*Talk through an idea. Watch it get built. See the evidence before you believe it.*

[Quick start](#quick-start) · [What it promises](#the-six-promises) ·
[Product requirements](docs/prd.md) · [Architecture](docs/architecture.md) ·
[Design decisions](docs/design-decisions.md) · [中文](README.zh-CN.md)

</div>

---

## What FORGE is

Most AI tools answer you. FORGE **works with you** — over hours and days, on one
project, holding on to what was decided and why.

You describe a bracket, a chassis, a studio massing. It asks the questions an
engineer would ask before drawing anything, proposes a couple of genuinely
different approaches, and then builds — geometry you can orbit, a diagram, files,
checks, telemetry. When it is not sure, it says so. When it has not verified
something, it refuses to call it verified.

The whole product rests on one line:

> **A long-running agent must not be a long-running LLM call.**

So FORGE is not a chat window with a long memory. It is a durable system in which
a model wakes up, rebuilds its state from a database, does a *bounded* piece of
work, saves the result, and safely continues later — through crashes, restarts,
deploys, and model failures. Close the tab mid-sentence and come back tomorrow;
it resumes from a structured checkpoint, not from a summary of the chat.

## What a session actually looks like

**You talk.** Full-duplex voice — interrupt it mid-sentence and it stops, keeps
the project state, and picks up your point. Everything is also reachable by
keyboard and text; the voice surface *moves* on small screens, it does not shrink
into a lesser version.

**It pushes back before it builds.** It asks what the thing is for, what it
attaches to, what the load is. For low-risk exploration it will proceed on
*labelled assumptions* rather than interrogating you — and the label stays
attached to what it produced.

**The work appears beside the conversation.** One canvas, several panels:

| Panel | What lands there |
|---|---|
| **Model** | Live 3D — orbit, section, explode, measure, compare variants side by side |
| **Diagram** | Structure and relationships |
| **Files** | What it wrote, with the diff |
| **Checks** | What was actually run, and what the raw output said |
| **Telemetry** | Numbers, with units and frames attached |
| **EDA / Simulation** | Declared, and honest about which adapters this deployment has |

**Nothing gets a nicer word than it earned.** *Proposed*, *approved*, *running*,
*failed*, *completed*, *verified*, *accepted* and *released* are eight different
states, and FORGE never implies one it is not in. A photorealistic render is
labelled as a render: pretty is not manufacturable, structurally adequate, or
compliant, and it says so on the picture.

**You hold the authority.** Anything consequential goes preview → approval →
rollback, and the approval names a *person*. "The AI approved it" is never an
acceptable answer to who signed off.

## The six promises

These are product behaviour, not aspirations — each one is enforced in code and
fenced by a test that fails if it stops being true.

| | Promise | What it rules out |
|---|---|---|
| 1 | **It says how it knows.** Every claim is labelled observed, retrieved, calculated, simulated, inferred, assumed or proposed. | A confident sentence that is actually a guess. |
| 2 | **It does not invent.** No fabricated measurements, standards, citations, imported results, actions you took, or completion claims. | "Per ISO 2768" when it never read ISO 2768. |
| 3 | **Verification is independent.** A result is verified only by a method independent of the one that generated it — and a different model family checks the work. | A model grading its own homework. |
| 4 | **It disagrees with you.** Dissent intensity is configurable; **safety-critical dissent cannot be turned off.** | An agreeable assistant that lets a bad idea through. |
| 5 | **It admits limits, and names who could.** A refusal says which authority would permit the work, not just "no". | A silent downgrade, or a dead end. |
| 6 | **Everything is auditable.** Inputs, plans, tool calls, versions, approvals, policies and evidence are tamper-evident. | "It worked yesterday" with nothing to read. |

## What it will not do

A project names its **industry**, and the industry sets a **ceiling** on how far
work can go inside it — not whether work can start.

- Ten domains are offered: mechanical, manufacturing, automotive, aerospace,
  civil, electrical, construction, product design, architecture, and *Other*.
- **R1 — concept work happens.** Sketching a bracket, a render, a revision.
- **R2 and above is refused**, naming the authority that would permit it.
- A named, attributed review authority raises a domain to R2 — and the refusal
  and the output both say **"RECORDED, NOT VERIFIED"** in those words, because
  this build has no registry to check a licence against.
- **Medical and robotics permit nothing at all.** Neither is in the selector.

This is deliberate. An earlier build refused *every* engineering pack because it
could not gate drawing release — which also blocked the concept CAD this product
is actually for. A mechanical engineer could not sketch a bracket because the
system could not certify one.

## The character

FORGE has a face and a mark, and they do different jobs.

<div align="center">

<img src="internal/httpapi/assets/portrait/calm.png" width="96" alt="Calm">
<img src="internal/httpapi/assets/portrait/thoughtful.png" width="96" alt="Thoughtful">
<img src="internal/httpapi/assets/portrait/focused.png" width="96" alt="Focused">
<img src="internal/httpapi/assets/portrait/bright.png" width="96" alt="Bright">

*Calm · Thoughtful · Focused · Bright*

</div>

The **portrait** is presence — who you are working with. The **sigil**, three
swept blades around a lit core, is the working mark: it carries *state*, and it
stays readable at 22px in a table row where a face becomes a smudge.

| State | The mark does | It means |
|---|---|---|
| Idle | Core breathes slowly | Nothing to do |
| Thinking | Faster pulse, blades lift | A model call is in flight |
| Working | An arc travels the ring | A tool is running *outside* this process |
| **Waiting for you** | The ring becomes a dashed gate | Stopped at the boundary — **it needs you** |
| Stopped | Core goes dark, ✕ | The goal ended badly |
| Done | Settled, ✓ | Complete |

Every state is distinguishable **without colour** — motion, dash pattern and a
glyph carry the meaning. It always identifies itself as an AI.

> The one that matters most is *Waiting for you*: an agent waiting unnoticed
> looks exactly like one that died, so it is the most visually distinct state on
> purpose.

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
| 7 | Evaluation suites, recovery drills in CI, release pipeline | ✅ Done |

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

## Under the hood

```
User Goal → Planner → Durable Task Store → Queue/Scheduler → Agent Worker
              ↑                                                   ↓
              └──────── Replan ←── Checkpoint ←── Verify ←──── Tools
```

`Agent = Model + Harness + Durable State + Workflow Engine + Tools + Scheduler + Recovery + Evaluation + Observability`

The interesting parts are not the boxes. They are the ~35 decisions underneath —
why the migration chain re-runs on every boot, why the verifier is a different
model family from the executor, why forgetting has to survive an agent that keeps
re-learning the thing you deleted, why a drill must prove its own fault landed.

**→ [docs/design-decisions.md](docs/design-decisions.md)** — each one states what
it replaced and what would have gone wrong the other way.

| Document | What it is for |
|---|---|
| [`docs/prd.md`](docs/prd.md) | Product requirements, with the IDs code and tests cite |
| [`docs/architecture.md`](docs/architecture.md) | How the pieces fit |
| [`docs/design-decisions.md`](docs/design-decisions.md) | Why they fit that way |
| [`docs/security-promises.md`](docs/security-promises.md) | What is guaranteed, and what is not |
| [`docs/bugfix/`](docs/bugfix) | Every defect, its root cause, and the fence that stops it returning |

---

## Cutting a release

```bash
make release-check    # formatting, vet, full suite on live Postgres, recovery drills, build
make dist             # cross-compiled binaries for darwin and linux, amd64 and arm64
make dist-verify      # run the built binary and read the version IT reports
```

`.github/workflows/release.yml` runs the same `make release-check` rather than
re-listing its steps in YAML — a workflow that keeps its own copy of the checks
drifts from the Makefile, and then a release passes a gate nobody can run
locally.

`dist-verify` checks the **artefact, not the recipe**. A binary compiled without
`-ldflags` reports `dev` and is indistinguishable from a release once it has left
the machine, so the check runs the binary and reads what it says about itself.

Release notes carry what the build cannot do and point at the carried defects. A
download page that reads as a finished product is its own kind of false claim.

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
