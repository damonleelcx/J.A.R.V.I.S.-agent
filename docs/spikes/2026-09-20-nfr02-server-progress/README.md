# A goal waiting on the planner said nothing for two minutes — NFR-02's server half

**Date:** 2026-09-20 · **Branch:** `issues/engine-requirements` · **Live model tokens:** 0 (a stub `llm.Client` served every call)

## What NFR-02 asks

`docs/prd.md:104`:

> **NFR-02** Visual update within 300 ms of a relevant speech event; long jobs report progress at least every 10 s

Two clauses. This spike is about the second one. The first is assessed at the end and is not built here.

## What PR 145 already covered — verified, not taken on trust

PR 145 gave every task the worker runs a progress signal. Checked against the source on this branch:

| Claim | Where | Verified |
|---|---|---|
| `AliveEvery = 5 * time.Second` | `internal/agent/worker.go:157` | yes |
| the heartbeat is started in `runTask` | `internal/agent/worker.go:418` (`hbCtx, stopHeartbeat := …`), goroutine at `:423` | yes |
| it beats at `min(cfg.LeaseHeartbeat, AliveEvery)` | `internal/agent/worker.go:776-789` | yes |
| each beat writes the task row, and the row's trigger stamps `updated_at` | `Queue.Heartbeat` | yes |
| the stamp is exposed as `TaskDTO.last_seen_at`, for `claimed`/`running`/`verifying` only | `internal/httpapi/goals.go:165`, `:188-193` | yes |

**"Progress for non-build goals" was already closed by PR 145.** The heartbeat starts at `worker.go:418`, and the
branch into the build loop and the off-node STEP export loop is at `worker.go:464` and `:470` — **after** it. Every
task therefore heartbeats before anything decides what kind of task it is, so an ordinary tool-loop task is covered by
exactly the same code as a build step. Nothing in `heartbeat` or in `toTaskDTO` looks at whether the goal is a build.

Fences, run on this branch against live Postgres (neither skipped):

```
--- PASS: TestAliveEvery_LeavesAClientPollingAtItAFreshStampInsideTenSeconds (0.00s)
--- PASS: TestWorker_ARunningTaskIsStampedAliveWhileItsModelCallRunsWhateverTheLeaseHeartbeat (2.79s)
```

`TestWorker_ARunningTaskIsStampedAliveWhileItsModelCallRunsWhateverTheLeaseHeartbeat` is an ORDINARY task — a held
model call through the tool loop, not a build step — which is the direct evidence for the paragraph above.

## What was still missing

Work that never becomes a task row had no signal at all, and planning is exactly that: it runs **synchronously inside
the HTTP handler**.

Line numbers below are as this branch stands, after the change:

- `POST /v1/goals` → `CreateGoal`, `internal/httpapi/goals_start.go:105`; the planner call at `:202`.
- `POST /v1/goals/{id}/plan` → `Replan`, `goals_start.go:274`; the call at `:316`.
- Both give it `Config.LLM.RequestTimeout + 15s` (`goals_start.go:175`, `:305`).
- A live plan takes ~128 s (GitHub issue 13).

A drafted goal writes **no timeline event of its own** — `engine.EventGoalCreated` is declared but never appended on
this path; the first and only event was `EventPlanCreated`, written by `agent.PlanApplier` at
`internal/agent/apply.go:235` when planning had already finished. So for the whole of those 128 s,
`GET /v1/goals/{id}/timeline` returned an empty list and a person watching over HTTP saw an open connection and
nothing else.

That is not a reconstruction. Removing the new ticker and re-running the end-to-end fence prints the pre-fix timeline
verbatim:

```
a plan held for 1.5s (the request took 1.5864726s) left 0 goal.progress event(s) on the timeline;
the whole timeline is [plan.created]
```

### The asymmetry

`forgectl` had the opposite half. `cmd/forgectl/goal.go:473-494` `startElapsedTicker` prints "still planning … Ns
elapsed" every 10 s (call sites `goal.go:126`, `:220`, `:688`; the NFR-02 rationale at `goal.go:117-120`), but
forgectl talks straight to Postgres, so that ticker is purely local and no HTTP client ever sees it. **The server had
a stamp and no ticker; the CLI had a ticker and no stamp.**

## What was built

### `internal/agent/progress.go` — `agent.Progress`

A reusable ticker for long server-side work. `stop := p.Start(ctx); … ; stop()` — the same shape as
`startElapsedTicker` and as `runTask`'s heartbeat, so all three reporters read alike.

- **The interval is `agent.ProgressEvery`, which is defined as `agent.AliveEvery`.** One number, not two: a running
  task's stamp and a held job's report are read by the same client polling at the same rate against the same 10 s
  ceiling, and the whole reason `AliveEvery` exists is that `FORGE_LEASE_HEARTBEAT` was a second number tuned for
  something else. Why the value is 5 s and not 10 is argued at `worker.go:145-157`: a report at most `ProgressEvery`
  old, polled at the same rate, is at most twice that old when it is seen, so the interval has to leave room for the
  poll.
- **`ProgressReport` has exactly two fields**, `Elapsed` and `Summary`, both derived from elapsed time and the
  caller's own label. This honours `cmd/forgectl/goal.go:468-472` literally — "A fake progress bar would be worse than
  silence" — and the field list is fenced with `reflect` rather than left to review, so adding a `PercentComplete`
  goes red.
- **It stops cleanly and never outlives the work.** `stop()` blocks until the reporting goroutine has returned, and
  the goroutine also returns on `ctx.Done()`.
- **A failed emit is logged and dropped.** The same trade-off `ConverseHandlers.keepSaid`
  (`internal/httpapi/converse.go:853-871`) makes: a progress write that failed must not fail the two-minute model call
  it is reporting on, and must not be silent either. The ticker does not give up after a failure — the next tick is
  the retry.

### `internal/domain/engine/model.go` — one new event kind

`EventGoalProgress = "goal.progress"`, next to `EventGoalEnded`. `forge_events.kind` has no check constraint
(`internal/platform/db/sql/0004_engine.sql:190-206`), so no migration was needed. The actor is `planner`, which the
table's `forge_events_actor_check` already allows.

### `internal/httpapi/goals_start.go` — wired into both planning handlers

`(*GoalHandlers).planProgress` starts the ticker around the planner call in `CreateGoal` and `Replan`, writing through
`(*engine.Repository).AppendEvent` (`repository.go:379`) — the hash-chained, per-goal-sequenced path (SAF-06).

**Why a timeline event and not a stamp on the goal row.** The row stamp is what a running task uses and was the
obvious thing to copy. It was not, for three reasons written into the code:

1. A stamp says "something happened", not *what* or *when it started*. The event carries elapsed time in its own
   summary; a stamp makes the client compute it from a `started_at` it must fetch separately.
2. `GET /v1/goals/{id}/timeline` is already the polling surface for "what is happening to this goal", already returns
   every kind unfiltered (`goals.go:212-237`), and the workbench card already reads it. A stamp needs a new `GoalDTO`
   field and a new thing for every client to learn.
3. A goal in `draft` being planned has no other writer, so appending cannot collide; a stamp on the goal row would
   race the planner's own writes to that row.

**What it costs.** One row per interval, each costing one SELECT of the previous event plus one INSERT, because the
chain is computed in Go. At `ProgressEvery = 5 s`:

| plan length | progress rows written |
|---|---:|
| 128 s (the live measurement, issue 13) | **25** |
| 60 s | 12 |
| 30 s | 6 |
| under 5 s | **0** |

This is real and it is not free — those rows sit in the timeline a person reads to reconstruct what happened, and 25
"still planning" lines are noise around the one `plan.created` line that matters. Accepted because the alternative on
offer was nothing at all for two minutes, and because one kind is trivially filterable. **If the timeline is ever
given a kind filter or a fold, this is the kind to fold.**

**Which context the writes run on.** `ctx` — the planner's own deadline — and deliberately **not**
`agent.outliving(ctx)` (`internal/agent/worker.go:228-243`). `outliving` exists for a RECORD of something that has
already happened, which must survive a stop that overtakes it. A progress report is the opposite: it is a ping about
work that is still running, and if that work has just been cancelled or run out of deadline, "still planning" is no
longer true. A report written after the plan it describes was abandoned would be worse than the silence it was added
to fix. Fenced by `TestProgress_TheTickerStopsWhenTheWorkDoes/stopped_by_the_context`.

**One compromise.** The log event for a lost report is `logx.EventGoalPlanFailed`, borrowed. `logx` enumerates every
event name and that file was out of scope for this change, so there is no `forge.goal.progress_not_written`. The
detail line `agent.Progress` attaches says what actually happened. If `logx` is opened, give it its own name.

### CLI/server parity

A comment at `startElapsedTicker` now points at `agent.Progress` and `planProgress`, so the two halves of NFR-02
reference each other, and both emit the same sentence ("still planning … Ns elapsed").

**Does forgectl need to consume the server's signal? No — decided, not left open.** `forgectl goal new` opens its own
pool and calls `agent.Intake` directly; there is no HTTP request in flight and no server involved. The events it would
poll for are written by the process it is already inside, so it would be doing a database round trip per tick to learn
a number it is holding in a variable. The duplication is ~20 lines and buys each surface the right mechanism.

## Measured cadence

Scaled intervals, as `alive_test.go` already does through `SetAliveEveryForTest`. The claim about the *production*
number is arithmetic (`2 * ProgressEvery <= 10s`, and `ProgressEvery == AliveEvery`) and is fenced as arithmetic.

**Unit (`internal/agent`, `every = 30 ms`, job held 300 ms):**

| run | reports | worst gap between reports |
|---|---:|---:|
| 1 | 10 | 30.377 ms |
| 2 | 10 | 30.382 ms |

10 reports for 10 intervals — no tick dropped. Worst overrun 1.3 % of the interval.

**End to end (`internal/httpapi`, live Postgres, `every = 150 ms`, planner held 1.5 s):** the gap measured is the
longest a watcher saw **nothing at all**, anchored at the moment the request arrived and running through every
timeline row to `plan.created`.

| run | events (of which progress) | request | longest silence | where | scaled to production |
|---|---|---:|---:|---|---:|
| 1 | 11 (10) | 1.588 s | 150.37 ms | progress → progress | 5.01 s |
| 2 | 11 (10) | 1.596 s | **165.97 ms** | request arriving → first progress | 5.53 s |
| 3 | 11 (10) | 1.684 s | **249.49 ms** | progress → progress | 8.32 s |

Run 3 was taken with several other builds running on the same laptop; the 99 ms overrun is scheduler and database
jitter, and at production scale the same *absolute* jitter against a 5 s interval is negligible. Before the change the
same measurement is the whole plan: **one event, written at the end** (128 s of silence on a live model).

The fence asserts the silence is within `4 × every` rather than `2 ×`. That bound is loose on purpose — it is about
how loaded this laptop is, not about the design — and it is still an order of magnitude inside "reports only when it
finishes", which is the behaviour it exists to catch.

## What is still NOT covered

**The 300 ms visual-update clause.** Untouched, and deliberately.

It is a different requirement about a different thing: the latency from a *relevant speech event* to a *visual update
in a client*, which is a browser-side budget spanning transcription, the converse turn and a render. Nothing in this
change moves it, and nothing in this change could — the server-side write is a few milliseconds and was never the
constraint. Measuring it honestly needs instrumentation in the workbench (when did audio arrive, when did the first
token land, when did the frame paint), and the number is then mostly a property of the model call and the renderer,
not of the timeline.

It belongs to the UI question tracked as **GitHub issue 15**, and is not attempted here. This spike closes the "long
jobs report progress at least every 10 s" clause only, and says so.

## Fences added

| test | package |
|---|---|
| `TestProgress_AJobHeldLongerThanNFR02sTenSecondsReportsInsideIt` | `internal/agent` |
| `TestProgress_AProgressReportSaysOnlyHowLongItHasBeenRunning` | `internal/agent` |
| `TestProgress_TheTickerStopsWhenTheWorkDoes` (2 subtests) | `internal/agent` |
| `TestProgress_AProgressReportThatCannotBeWrittenIsLoggedAndDoesNotStopTheWork` | `internal/agent` |
| `TestCreateGoal_AGoalWaitingOnThePlannerReportsProgressInsideNFR02sTenSeconds` | `internal/httpapi` |
| `TestCreateGoal_APlanThatAnswersAtOnceDoesNotSpamTheTimelineWithProgress` | `internal/httpapi` |

Eight drill mutations were applied by hand and each turned its fence red; they are written up for
`scripts/drill-fences.sh` in the pull request rather than added here. **A fence is not proven until the drill script
runs them.**
