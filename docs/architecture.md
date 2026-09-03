# FORGE architecture

## The governing constraint

> A long-running agent must not be a long-running LLM call.

Everything structural in FORGE follows from that sentence. An LLM request is a
fragile, time-boxed, stateless thing. A goal that takes three weeks is none of
those. So the model never *holds* the work — the database does. The model is
invoked repeatedly, each time reconstructing what it needs from durable state,
doing a bounded amount of work, and persisting the result before it exits.

```
                    ┌──────────────────────────────────────────┐
                    │            Durable Task Store            │
   User Goal ──────▶│  goals · plans · tasks(DAG) · runs        │◀──────┐
                    │  checkpoints · events · approvals         │       │
                    │  memory · evidence · budgets              │       │
                    └───────────────┬──────────────────────────┘       │
                                    │                                   │
                    ┌───────────────▼──────────┐                        │
                    │   Queue / Scheduler      │  leases, backoff,      │
                    │   (wake-ups, timers)     │  dependency gating     │
                    └───────────────┬──────────┘                        │
                                    │ claim (leased)                    │
                    ┌───────────────▼──────────────────────────┐        │
                    │            Agent Worker                  │        │
                    │  observe → plan → execute → verify →     │ persist│
                    │  persist → continue                      │────────┘
                    └───────┬─────────────────────┬────────────┘
                            │                     │
                    ┌───────▼──────┐      ┌───────▼────────┐
                    │  Tool runtime │      │  Verification  │
                    │  typed, scoped│      │  independent   │
                    │  timeouts,    │      │  model family  │
                    │  idempotent   │      │  + tool truth  │
                    └───────────────┘      └────────────────┘
```

## Why the planner and the executor are different components

A single model asked to both *decide what to do* and *do it* will reliably drift
toward whatever it can do next, rather than whatever the goal needs. Separating
them makes the plan an inspectable artifact that outlives any one execution.

- The **planner** reads the goal and the current world-state and writes tasks
  into the store. It never calls a tool.
- The **executor** claims one task, does bounded work, and reports truthfully.
  It cannot create arbitrary new work — only propose it back to the planner,
  bounded by `MaxTaskDepth` and `MaxTasksPerGoal`.
- The **verifier** runs on a **different model family** than the executor
  (PRD SAF-03), because a model grading its own output is not an independent
  check.

## Why context is reconstructed, never carried

The model's context window is not memory. It is a cache that is destroyed at the
end of every request, and it is not durable, not queryable, and not auditable.

So each execution cycle assembles context *from the database*:
the goal, the current task, the relevant slice of recent history, retrieved
knowledge, and the active constraints. Nothing is assumed to survive between
cycles. This is what makes crash recovery, pause/resume, and multi-day
execution the *same* code path rather than three special cases.

The practical test: **killing a worker mid-task and restarting it must produce
the same outcome as never having killed it.** Anything held only in a process's
memory breaks that property, so nothing is.

## The seven bounds

An agent runs away along seven independent axes. Bounding one is not bounding.

| Axis | Setting | Why it alone is insufficient |
|---|---|---|
| Iterations per task | `MaxIterationsPerTask` | A task can loop cheaply forever |
| Tool calls per iteration | `MaxToolCallsPerIteration` | One iteration can fan out unboundedly |
| Tokens per goal | `MaxTokensPerGoal` | Many cheap tasks still cost real money |
| Cost per goal | `MaxCostCentsPerGoal` | Token counts differ in price by model |
| Wall clock per goal | `MaxWallClockPerGoal` | A stalled goal consumes attention, not tokens |
| Task depth | `MaxTaskDepth` | Recursive decomposition is the classic runaway |
| Tasks per goal | `MaxTasksPerGoal` | Breadth-first explosion evades a depth limit |

## Truthful state

PRD **AGT-08** requires that these never be conflated:

```
proposed → approved → running → { failed | completed } → verified → accepted → released
```

Each is a distinct persisted state with a distinct writer. The agent may write
up to `completed`. Only a *tool with evidence* may write `verified`. Only a
*named human* may write `accepted` or `released`. This is enforced at the
storage layer, not by prompt instruction, because a prompt is not an access
control.

## Idempotency

Every task carries an idempotency key. Every side-effecting tool call records
its key before execution and checks it after. A retry that finds a completed
record for its key returns the recorded result instead of acting again.

This is what makes "retry on failure" safe in a system that can send email,
write files, open pull requests, and spend money.

## Failure isolation

- A failing task fails *itself*. Its dependents are blocked, not corrupted; its
  completed siblings are untouched.
- A failing **tool** is data, not a crash: the executor sees a typed error and
  may choose an alternative, retry, or escalate.
- A failing **model** is retried with backoff, then routed to an alternative,
  then surfaced as an explicit failure state — never silently as "done".
- A failing **side path** (observability, memory writes, notifications) must
  never take down the main path. These are best-effort and log at WARN.

## Layered memory

| Layer | Lifetime | Rebuilt from |
|---|---|---|
| Turn context | One model call | Assembled per cycle; never persisted as authority |
| Task state | Task lifetime | The task row and its checkpoints |
| Episodic history | Goal lifetime | The event timeline, compacted by the summarizer |
| Project knowledge | Project lifetime | Decisions, evidence, artifacts, the project graph |
| Preferences | Account lifetime | User-editable memory records |

Compaction summarises *episodic* history only. Decisions and evidence are never
summarised away, because they are what an auditor reconstructs the run from.

## Component map

| Component | Responsibility |
|---|---|
| `platform/config` | Load and validate configuration; refuse to boot on a bad value |
| `platform/db` | Pool, transactions, embedded migration chain re-run every boot |
| `platform/errs` | Central error-code registry: code, category, status, cause, remedy, retryability |
| `platform/logx` | Structured logging; central event-name registry; correlation ids |
| `platform/id` | Prefixed, time-sortable identifiers |
| `platform/clock` | Injectable time, so lease and expiry logic is testable without sleeping |
| `domain/*` | Aggregates: identity, goal, plan, task, run, approval, memory, policy, pack |
| `domain/geometry` | Units and frames, the proposed-shape document, variants (an artifact version), derived comparison, and mesh export with its conversion label |
| `engine/*` | Queue, worker loop, planner, executor, verifier, context assembly, budgets, scheduler |
| `tools/*` | Capability registry and typed tool contracts |
| `persona/*` | Avatar, character, and the durable value set |
| `httpapi/*` | Public API and console surface |

## Testing posture

Three rules, learned the hard way:

1. **A fence that enumerates what it checks is vacuous.** The registry fences
   parse this repository's source instead of iterating a hand-written list.
2. **A fence is not proven until it has been seen to go red.** Every fence in
   this repo has been mutation-drilled; the drill asserts the mutation actually
   applied before trusting the result. (An early drill here "passed" only
   because `gofmt` had realigned the line the mutation targeted.)
3. **Schema tests run against the real migrated schema**, never an inline
   `CREATE TABLE`. A fixture that approximates production tests the fixture.
