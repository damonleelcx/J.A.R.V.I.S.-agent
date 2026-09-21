# NFR-03 durability: the five items, and a fence that kills a process mid-write

`docs/prd.md:105` — **NFR-03 Durability**: "no acknowledged checkpoint, approved
plan, artifact version, tool result, or decision is lost".

GitHub issue 17 established that the implementation already meets this. Two
things were missing and this spike adds them: a citation at each write path
saying what the promise is and what would break it, and a fence that asserts
durability as a requirement rather than as a comment.

## What "acknowledged" means, because the requirement turns on it

A write is acknowledged when FORGE has told somebody it happened: a function
returned a value rather than an error, a handler answered 200. The promise is
therefore not "every write survives". It is "nothing FORGE claimed survives less
than FORGE claimed it would".

That is the distinction that makes the best-effort iteration checkpoint legal,
and the one that makes acknowledging before a commit illegal anywhere.

## The five items and their stores

| Item | Production write path | Table |
|---|---|---|
| Acknowledged checkpoint | `engine.(*Repository).SaveCheckpoint` — `internal/domain/engine/repository.go` | `forge_checkpoints` |
| Approved plan | `agent.(*PlanApplier).Apply` — `internal/agent/apply.go` | `forge_plans`, `forge_tasks`, `forge_task_deps` |
| …and its acknowledgement | `httpapi.(*GoalHandlers).Decide` — `internal/httpapi/goals.go` | `forge_approvals` |
| Artifact version | `workspace.(*Repository).AppendVersion` — `internal/domain/workspace/repository.go` | `forge_artifact_versions` |
| Tool result | `agent.(*Executor).recordToolCall` — `internal/agent/executor.go` | `forge_tool_calls` |
| Decision | `memory.(*Service).RecordDecision` — `internal/domain/memory/decision.go` | `forge_decisions` |

"Approved plan" is two writes, not one. `Apply` writes the plan and its tasks in
one transaction; `Decide` is where a human's answer to a gate becomes durable.
Both are cited and both are fenced.

`engine.CheckpointApprovalGranted` (`model.go:206`) has **zero call sites**. It
is a name for a checkpoint kind that nothing writes: an approval is recorded in
`forge_approvals` by `Decide`, and the task is returned to the queue rather than
resumed from a checkpoint, so the kind never became real. Dead, but harmless —
and NOT a hole in NFR-03, because the durable record of an approval is the
`forge_approvals` row and not a checkpoint. Deleting the constant belongs to
whoever owns `model.go`; it was left alone here.

## The fence

`internal/domain/engine/nfr03_durability_integration_test.go`.

### What it kills, and when

`TestNFR03_NothingAcknowledgedIsLostWhenTheProcessThatWroteItIsKilled`:

1. builds a schema with the package's existing `newHarness`, seeds a goal, a
   task, an artifact, a pending approval and one fixture tool call;
2. launches a **child process** — `os.Args[0] -test.run=^TestNFR03_TheChildThatIsKilledWhileItHoldsAnUncommittedWrite$`
   — pointed at the **same schema**, with a marker environment variable the child
   requires (so the child never runs during an ordinary `go test ./...`);
3. the child performs all five writes and they are all acknowledged — every one
   of them returns without error — and then performs a **matched pair**: two
   identical writes through `SaveCheckpoint`, in two caller-owned transactions,
   one committed and one left open;
4. the child prints a marker carrying the Postgres backend pid holding the open
   transaction, and blocks for at most two minutes so an orphan cannot outlive
   the run;
5. the parent reads that marker under a **bounded** wait (two minutes, and it
   bails if the pipe closes because the child died);
6. the parent calls `cmd.Process.Kill()` — `TerminateProcess` on Windows,
   `SIGKILL` elsewhere. No signal handler, no deferred function, no pgx
   terminate message. This is the process dying mid-write;
7. the parent waits, bounded to 30s, for that backend pid to leave
   `pg_stat_activity`. Asserting absence earlier would pass for the wrong
   reason — the row would be invisible because the transaction was still open,
   not because it was rolled back;
8. the parent opens a **new pool** on the same schema and asserts, item by item,
   that each of the five is present **with the content that was acknowledged**,
   that the committed half of the pair is present, and that the uncommitted half
   is absent.

The child is always killed, including on every failure path, from a `t.Cleanup`
registered before anything that can fail. No orphan survived any run of this.

### Why the matched pair rather than a single absence check

A fence that only checks presence passes against a store that kept everything it
was ever shown. A fence that only checks absence passes when it is looking at the
wrong schema, the wrong task, or through a pool that sees nothing — and then
every "it survived" assertion beside it is worth the same nothing.

The pair differs in exactly one respect: one transaction commits and one does
not. Both halves are asserted, so the fence carries its own proof that it can
fail instead of that proof being something somebody demonstrated once by hand.

### Two of the five are written by a substitute

Not every production path has a surface this package can call:

- `agent.(*Executor).recordToolCall` is unexported.
- `httpapi.(*GoalHandlers).Decide` needs a request carrying an authenticated user
  under `ctxKeyUser`, which is unexported in `internal/httpapi/auth.go`, so it
  cannot be called from outside package `httpapi` at all.

The fence has to write all five together, so it could not move into either
package. Those two are executed from the SQL statement copied **verbatim** out of
the production function, and the copies are pinned:
`TestNFR03_TheSubstitutedWritesAreStillTheStatementsProductionRuns` holds each
copy byte-for-byte against the file it came from, so changing the production
statement turns the fence red rather than leaving it green behind a moved target.

What those two substitutes therefore do NOT exercise: the permission check and
timeline event that surround the approval UPDATE in the handler, and the
outliving-context wrapper around the ledger write in the executor. Both belong to
fences that already exist elsewhere (the access fences in `internal/httpapi`, the
stop fences in `internal/agent`).

The other four — `SaveCheckpoint`, `Apply`, `AppendVersion`, `RecordDecision` —
are the production functions themselves.

### The per-item fences

Five cheaper fences, one per item: write it, drop the pool that wrote it, open a
new one on the same schema, look. Closing a pool is a polite crash, so these are
a weaker statement than the kill — but when one item regresses they name that
item instead of producing a single red kill fence for all five.

## Evidence

```
$ export GOWORK=off
$ export FORGE_TEST_DATABASE_URL='postgres://forge:forge_dev_pw@localhost:55840/forge?sslmode=disable'
$ go test -p 1 -count=1 -v -run 'TestNFR03' ./internal/domain/engine/
=== RUN   TestNFR03_NothingAcknowledgedIsLostWhenTheProcessThatWroteItIsKilled
    nfr03_durability_integration_test.go:685: killed process 22108 (wait reported exit status 1); it held Postgres backend 3427 on a write it had begun and never committed
--- PASS: TestNFR03_NothingAcknowledgedIsLostWhenTheProcessThatWroteItIsKilled (3.52s)
=== RUN   TestNFR03_TheChildThatIsKilledWhileItHoldsAnUncommittedWrite
    nfr03_durability_integration_test.go:734: child half of the NFR-03 kill fence; it runs only as a subprocess of TestNFR03_NothingAcknowledgedIsLostWhenTheProcessThatWroteItIsKilled
--- SKIP: TestNFR03_TheChildThatIsKilledWhileItHoldsAnUncommittedWrite (0.00s)
=== RUN   TestNFR03_AnAcknowledgedCheckpointSurvivesThePoolThatWroteIt
--- PASS: TestNFR03_AnAcknowledgedCheckpointSurvivesThePoolThatWroteIt (1.75s)
=== RUN   TestNFR03_AnApprovedPlanSurvivesThePoolThatWroteIt
--- PASS: TestNFR03_AnApprovedPlanSurvivesThePoolThatWroteIt (1.75s)
=== RUN   TestNFR03_AnAcknowledgedApprovalSurvivesThePoolThatWroteIt
--- PASS: TestNFR03_AnAcknowledgedApprovalSurvivesThePoolThatWroteIt (1.23s)
=== RUN   TestNFR03_AnArtifactVersionSurvivesThePoolThatWroteIt
--- PASS: TestNFR03_AnArtifactVersionSurvivesThePoolThatWroteIt (1.15s)
=== RUN   TestNFR03_AnAcknowledgedToolResultSurvivesThePoolThatWroteIt
--- PASS: TestNFR03_AnAcknowledgedToolResultSurvivesThePoolThatWroteIt (1.22s)
=== RUN   TestNFR03_ADecisionSurvivesThePoolThatWroteIt
--- PASS: TestNFR03_ADecisionSurvivesThePoolThatWroteIt (1.44s)
=== RUN   TestNFR03_TheSubstitutedWritesAreStillTheStatementsProductionRuns
--- PASS: TestNFR03_TheSubstitutedWritesAreStillTheStatementsProductionRuns (0.00s)
=== RUN   TestNFR03_EveryWritePathStillSaysWhatItPromises
--- PASS: TestNFR03_EveryWritePathStillSaysWhatItPromises (0.00s)
PASS
ok  	github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine	14.307s
```

The whole package still passes with the fences in it, and the child still does
not run on its own:

```
$ go test -p 1 -count=1 ./internal/domain/engine/
ok  	github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine	80.616s
```

An earlier build of the fence was observed going red on the half that matters, by
making the child commit the write it is supposed to abandon:

```
    nfr03_durability_integration_test.go:684: a write that was never committed is readable anyway (1 rows of kind "nfr03_never_committed"). Either the fence is not testing what it claims — in which case the five assertions above prove nothing either — or FORGE is persisting work it never acknowledged.
--- FAIL: TestNFR03_NothingAcknowledgedIsLostWhenTheProcessThatWroteItIsKilled (4.80s)
```

That was a one-off check on a throwaway edit, and it is exactly the kind of proof
that does not keep. It is why the fence now carries the matched pair instead.
**The drills in `scripts/drill-fences.sh` are what actually proves these; they
are owned and run elsewhere, and nothing here claims to have run them.**

## The best-effort checkpoint, and why it is not a violation

`internal/agent/executor.go` saves the per-iteration checkpoint best-effort: a
failure logs `EventCheckpointFailed` at WARN and the loop continues.

It does not violate NFR-03, because NFR-03 protects *acknowledged* checkpoints
and this one is acknowledged to nobody — no caller is told a resume point exists,
and the WARN says so. Two things make that safe:

- **tool results are not carried by this checkpoint.** They are written on their
  own path, `recordToolCall`, on a context that outlives a stop. Losing a resume
  point loses at most the model's working messages for one iteration.
- **`idempotency_key` is unique** — `internal/platform/db/sql/0004_engine.sql:47`
  and `:107` (`forge_tasks`, unique per goal) and `:233` (`forge_tool_calls`,
  unique across the whole table, deliberately not per task, so the same logical
  action deduplicates even after a replan moved it). Work replayed after a lost
  checkpoint deduplicates instead of repeating a side effect.

The argument is recorded in a comment at the site, because it is exactly what a
future refactor would break in either direction: promoting the WARN to a hard
failure would discard work that actually succeeded, and quietening it would
remove the only signal that the next crash now costs more than one iteration.

## What the fence does not prove

- **It does not prove Postgres is durable.** It does not kill the database, pull
  power, or test `fsync`. A machine loss that takes committed WAL with it is
  outside what this measures.
- It proves the half FORGE owns: **FORGE acknowledges only what Postgres has
  committed.** Everything it told somebody about survived the process that told
  them, and the write it had not committed did not.
- It does not exercise the permission check around the approval write or the
  outliving-context wrapper around the ledger write — see the substitutes above.
- Per-item fences drop a pool rather than killing a process. That is a weaker
  statement than the kill fence makes, and it is made on purpose: they exist to
  name which item regressed, not to restate the kill.
