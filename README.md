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
| 2 | Durable engine: goals, task DAG, job queue, leases, checkpoints, timeline | 🔨 In progress |
| 3 | Agent loop: planner/executor split, context assembly, verification, replanning, approval gates | ⏳ Planned |
| 4 | Tools and domain packs: capability registry, risk tiers, safety plane | ⏳ Planned |
| 5 | Persona and console: avatar, character, soul, execution timeline | ⏳ Planned |
| 6 | Observability, evaluation suites, recovery drills, release | ⏳ Planned |

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

See [LICENSE](LICENSE).
