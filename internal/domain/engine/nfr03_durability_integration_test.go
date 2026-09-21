package engine_test

// NFR-03 durability, as a fence rather than a claim.
//
// docs/prd.md:105 — "no acknowledged checkpoint, approved plan, artifact
// version, tool result, or decision is lost". Issue 17 established that the
// implementation already meets this; what was missing was anything that would
// go red if it stopped. This file is that.
//
// # What "acknowledged" means here, because the whole requirement turns on it
//
// A write is acknowledged when FORGE has told somebody it happened: a function
// returned a value rather than an error, a handler answered 200. The promise is
// therefore not "every write survives" — it is "nothing FORGE claimed survives
// less than FORGE claimed it would". That distinction is what makes the
// best-effort iteration checkpoint in agent/executor.go legal (it is
// acknowledged to nobody; see the note there) and what makes acknowledging
// before a commit illegal anywhere.
//
// # The two shapes of fence in here
//
//   - one KILL fence, which writes all five through the real paths in a child
//     process, then writes a MATCHED PAIR — two identical writes, one committed
//     and one left open — and has the child killed outright. It re-reads from a
//     new pool and asserts the five are there with the content that was
//     acknowledged, the committed half of the pair is there, and the uncommitted
//     half is not. The pair is the fence's own proof that it can fail: without
//     the committed half, "absent" could mean the fence is looking in the wrong
//     place; without the uncommitted half, it would pass against a store that
//     kept everything it was ever shown.
//   - five PER-ITEM fences, one per item, which are cheap (write, drop the pool,
//     open a new one) and exist so that a regression in one item names that item
//     instead of producing one red kill fence for all five.
//
// # Two of the five are written by a substitute, and this file pins them
//
// The five production write paths do not all have an exported surface reachable
// from this package:
//
//   - agent.(*Executor).recordToolCall is unexported, and
//   - httpapi.(*GoalHandlers).Decide needs an authenticated request, whose
//     context key (ctxKeyUser in internal/httpapi/auth.go) is unexported, so it
//     cannot be called from outside package httpapi at all.
//
// Moving this file into either package was not an option — the fence has to
// write all five together — so those two are executed here from the SQL
// statement copied verbatim out of the production function. A copy rots, so
// TestNFR03_TheSubstitutedWritesAreStillTheStatementsProductionRuns holds each
// copy byte-for-byte against the file it came from: change the statement in
// production and this file goes red rather than quietly fencing something that
// is no longer there. The other three — SaveCheckpoint, PlanApplier.Apply,
// AppendVersion, RecordDecision — are the production functions themselves.
//
// # What none of this proves
//
// It does not prove Postgres is durable. It proves FORGE acknowledges only what
// Postgres has committed, which is the half FORGE owns.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/agent"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/memory"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// ---------------------------------------------------------------------------
// what is acknowledged
// ---------------------------------------------------------------------------

// The content each write acknowledges. Present-and-empty is not survival: a row
// that came back without what was written to it has lost the thing the caller
// was told was kept, so every assertion below checks a marker and not a count.
const (
	nfr03CheckpointKind   = engine.CheckpointResumeState
	nfr03CheckpointState  = `{"nfr03":"the resume state that was acknowledged"}`
	nfr03PlanRationale    = "nfr03: the rationale that was acknowledged with the plan"
	nfr03PlanTaskKey      = "nfr03-plan-task"
	nfr03PlanTaskTitle    = "nfr03: the task the acknowledged plan created"
	nfr03ApprovalReason   = "nfr03: the reason the reviewer was acknowledged for"
	nfr03VersionDiff      = "nfr03: the diff that was acknowledged with the version"
	nfr03ToolCallKey      = "nfr03-tool-call"
	nfr03ToolCallOutput   = "nfr03: the tool result that was acknowledged"
	nfr03DecisionTitle    = "nfr03: the decision that was acknowledged"
	nfr03DecisionText     = "nfr03: keep what was acknowledged, lose what was not"
	nfr03FixtureToolKey   = "nfr03-fixture-tool-call"
	nfr03CommittedKind    = "nfr03_committed_then_killed"
	nfr03CommittedState   = `{"nfr03":"a transaction that committed before the kill"}`
	nfr03UncommittedKind  = "nfr03_never_committed"
	nfr03UncommittedState = `{"nfr03":"a seventh write that is killed before it commits"}`
)

// Child-process wiring. The marker env var is REQUIRED by the child, so an
// ordinary `go test ./...` skips it rather than running a test that would sit
// for two minutes waiting to be killed.
const (
	nfr03ChildEnv     = "FORGE_NFR03_CHILD"
	nfr03SchemaEnv    = "FORGE_NFR03_SCHEMA"
	nfr03FixtureEnv   = "FORGE_NFR03_FIXTURE"
	nfr03ChildTest    = "TestNFR03_TheChildThatIsKilledWhileItHoldsAnUncommittedWrite"
	nfr03ReadyMarker  = "nfr03-child-ready-backend-pid"
	nfr03ChildFailure = "nfr03-child-failed"
)

// nfr03Fixture is the seeded world both processes address. Passed as JSON in an
// environment variable because the child shares the schema and nothing else:
// re-deriving these ids would mean re-seeding, and a child that seeds is a child
// that could pass against rows it wrote after the kill.
type nfr03Fixture struct {
	UserID     string `json:"user_id"`
	ProjectID  string `json:"project_id"`
	GoalID     string `json:"goal_id"`
	TaskID     string `json:"task_id"`
	ArtifactID string `json:"artifact_id"`
	ApprovalID string `json:"approval_id"`
	ToolCallID string `json:"tool_call_id"`
}

// ---------------------------------------------------------------------------
// the two substituted statements
// ---------------------------------------------------------------------------

// nfr03ApprovalDecisionSQL is the statement httpapi.(*GoalHandlers).Decide runs,
// copied verbatim. See the file comment for why it is copied at all, and
// TestNFR03_TheSubstitutedWritesAreStillTheStatementsProductionRuns for what
// stops the copy from drifting.
const nfr03ApprovalDecisionSQL = `
		update forge_approvals
		   set decision = $2, decided_by = $3, decided_at = $4, decision_reason = $5
		 where id = $1 and decision = 'pending'
		returning task_id, goal_id`

// nfr03ToolLedgerSQL is the statement agent.(*Executor).recordToolCall runs,
// copied verbatim. Same caveat, same pin.
const nfr03ToolLedgerSQL = `
			insert into forge_tool_calls
				(id, task_id, idempotency_key, tool_name, input, status, output,
				 raw_output, error_code, error_detail, started_at, ended_at, duration_ms, created_at,
				 risk_tier, reversibility)
			values ($1,$2,$3,$4,$5,$6,$7,$8,nullif($9,''),nullif($10,''),$11,$11,$12,$11,
				 nullif($13,''),nullif($14,''))
			on conflict (idempotency_key) do nothing`

// ---------------------------------------------------------------------------
// harness helpers
// ---------------------------------------------------------------------------

// nfr03SchemaOf asks the connection which schema it is pinned to.
//
// Recomputing newHarness's naming rule here would be a second copy of it, and a
// copy that drifted would silently point the child at an empty schema — where
// every "it is gone" assertion passes. current_schema() cannot drift.
func nfr03SchemaOf(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var schema string
	if err := pool.QueryRow(context.Background(), "select current_schema()").Scan(&schema); err != nil {
		t.Fatalf("asking the test connection which schema it is on: %v", err)
	}
	if schema == "" {
		t.Fatal("the test connection reports no current schema, so nothing could address the same rows twice")
	}
	return schema
}

// nfr03Open connects a NEW pool to an existing schema. It never migrates and
// never drops: this is the "come back and look" side of every fence here, and a
// pool that could create what it is checking for would prove nothing.
func nfr03Open(t *testing.T, schema string) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("FORGE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("FORGE_TEST_DATABASE_URL is unset; skipping live-database tests. Run `make db-up` then `make test-integration`.")
	}
	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	pool, err := db.Connect(context.Background(), config.DBConfig{
		URL: url + sep + "search_path=" + schema, MaxConns: 4, MinConns: 1,
		MaxConnLifetime: time.Hour, MaxConnIdleTime: time.Minute, ConnectTimeout: 10 * time.Second,
	}, logx.Discard())
	if err != nil {
		t.Fatalf("re-opening a pool on schema %s: %v", schema, err)
	}
	return pool
}

// nfr03Seed adds what the five writes need and nothing they produce: a task, an
// artifact to version, a PENDING approval for a reviewer to answer, and one tool
// call row for the artifact version to name (WRK-04 refuses a version that
// cannot point at the action that made it).
//
// The fixture tool call is deliberately NOT one of the five. Item four writes
// its own, under a different key.
func nfr03Seed(t *testing.T, h *harness) nfr03Fixture {
	t.Helper()
	ctx := context.Background()
	now := h.clk.Now()

	var f nfr03Fixture
	f.GoalID = h.goalID
	if err := h.pool.QueryRow(ctx,
		`select project_id, created_by from forge_goals where id = $1`, h.goalID).
		Scan(&f.ProjectID, &f.UserID); err != nil {
		t.Fatalf("reading back the seeded goal: %v", err)
	}

	task := &engine.Task{
		ID: id.New(id.PrefixTask), GoalID: h.goalID, PlanID: h.planID,
		Title: "nfr03 subject", Instruction: "hold the five writes",
		Status: engine.StatusPending, IdempotencyKey: "nfr03-subject",
		MaxAttempts: 3, NotBefore: now, Priority: 100,
		RiskTier: engine.RiskR1, CreatedAt: now, UpdatedAt: now,
	}
	if err := h.repo.CreateTask(ctx, h.pool, task, nil); err != nil {
		t.Fatalf("seeding the subject task: %v", err)
	}
	f.TaskID = task.ID

	art := &workspace.Artifact{
		ID: id.New(id.PrefixArtifact), ProjectID: f.ProjectID,
		Path: "nfr03/subject.txt", Kind: workspace.ArtifactFile,
		CreatedAt: now, UpdatedAt: now,
	}
	art, err := workspace.NewRepository().FindOrCreateArtifact(ctx, h.pool, art)
	if err != nil {
		t.Fatalf("seeding the artifact: %v", err)
	}
	f.ArtifactID = art.ID

	f.ToolCallID = id.New(id.PrefixToolCall)
	if _, err := h.pool.Exec(ctx,
		`insert into forge_tool_calls (id, task_id, idempotency_key, tool_name, status,
			started_at, ended_at, duration_ms, created_at)
		 values ($1,$2,$3,'nfr03_fixture','succeeded',$4,$4,0,$4)`,
		f.ToolCallID, f.TaskID, nfr03FixtureToolKey, now); err != nil {
		t.Fatalf("seeding the fixture tool call: %v", err)
	}

	f.ApprovalID = id.New(id.PrefixApproval)
	if _, err := h.pool.Exec(ctx,
		`insert into forge_approvals (id, goal_id, task_id, risk_tier, summary, requested_at)
		 values ($1,$2,$3,'r3','nfr03: may this proceed?',$4)`,
		f.ApprovalID, f.GoalID, f.TaskID, now); err != nil {
		t.Fatalf("seeding the pending approval: %v", err)
	}
	return f
}

// ---------------------------------------------------------------------------
// the five writes
// ---------------------------------------------------------------------------

// nfr03Now is the instant every write here is stamped with. Fixed rather than
// wall-clock because two processes must agree on it, and because the application
// clock owns every timestamp in this system.
func nfr03Now() time.Time { return time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC) }

// 1. Acknowledged checkpoint — the production path.
func nfr03WriteCheckpoint(ctx context.Context, ex db.Querier, f nfr03Fixture) error {
	_, err := engine.NewRepository().SaveCheckpoint(ctx, ex, f.TaskID,
		nfr03CheckpointKind, json.RawMessage(nfr03CheckpointState), nfr03Now())
	return err
}

// 2a. Approved plan — the production path.
//
// The goal is built from the seeded ids rather than read back, because the
// engine repository has no goal reader; Apply uses only ID, RiskTier, Budget and
// Spend, and all four are what the seeded row holds.
func nfr03WritePlan(ctx context.Context, pool *db.Pool, f nfr03Fixture) error {
	applier := agent.NewPlanApplier(
		engine.NewRepository(), engine.NewQueue(),
		engine.NewBudgetGuard(config.EngineConfig{}),
		clock.NewFake(nfr03Now()))
	goal := &engine.Goal{
		ID: f.GoalID, ProjectID: f.ProjectID, CreatedBy: f.UserID,
		Status: engine.GoalActive, RiskTier: engine.RiskR1,
	}
	_, _, err := applier.Apply(ctx, pool, goal, &agent.PlanResult{
		Rationale: nfr03PlanRationale,
		Tasks: []agent.PlannedTask{{
			Key: nfr03PlanTaskKey, Title: nfr03PlanTaskTitle,
			Instruction: "exist after the process that planned you is killed",
			RiskTier:    string(engine.RiskR1),
		}},
		// "planner" and not a marker of this test's own: forge_plans_author_check
		// permits only 'planner' or 'human', because "the AI approved it" is never
		// acceptable authority (PRD SAF-05). The rationale carries the marker.
	}, "planner")
	return err
}

// 2b. The acknowledged approval — SUBSTITUTE.
//
// ‼️ This is not httpapi.(*GoalHandlers).Decide. It is the statement Decide
// runs, verbatim, because Decide needs a request carrying an authenticated user
// under an unexported context key and so cannot be called from this package at
// all. The permission check and the timeline event that surround it in the
// handler are NOT exercised here — this fence is about the decision surviving,
// not about who was allowed to make it (that is the access fence's job).
func nfr03DecideApproval(ctx context.Context, ex db.Querier, f nfr03Fixture) error {
	var taskID, goalID string
	return ex.QueryRow(ctx, nfr03ApprovalDecisionSQL,
		f.ApprovalID, string(engine.ApprovalApproved), f.UserID, nfr03Now(), nfr03ApprovalReason).
		Scan(&taskID, &goalID)
}

// 3. Artifact version — the production path.
func nfr03WriteVersion(ctx context.Context, ex db.Querier, f nfr03Fixture) error {
	toolCall := f.ToolCallID
	return workspace.NewRepository().AppendVersion(ctx, ex, &workspace.Version{
		ID: id.New(id.PrefixVersion), ArtifactID: f.ArtifactID,
		InitiatorID: f.UserID, Agent: workspace.AgentExecutor, ToolCallID: &toolCall,
		Inputs: json.RawMessage(`{"nfr03":true}`), Diff: nfr03VersionDiff,
		Verification: workspace.Unverified, Disposition: workspace.Pending,
		CreatedAt: nfr03Now(),
	})
}

// 4. Tool result — SUBSTITUTE.
//
// ‼️ This is not agent.(*Executor).recordToolCall, which is unexported. It is
// the statement recordToolCall runs, verbatim, inside db.InTx exactly as the
// executor wraps it. What is NOT exercised here is the executor's use of an
// outliving context — that belongs to the stop fences in package agent.
func nfr03WriteToolCall(ctx context.Context, pool *db.Pool, f nfr03Fixture) error {
	now := nfr03Now()
	return db.InTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, nfr03ToolLedgerSQL,
			id.New(id.PrefixToolCall), f.TaskID, nfr03ToolCallKey,
			"nfr03_tool", json.RawMessage(`{"nfr03":true}`), string(engine.ToolSucceeded),
			nil, nfr03ToolCallOutput, "", "", now, int64(0), string(engine.RiskR1), "")
		return err
	})
}

// 5. Decision — the production path.
func nfr03WriteDecision(ctx context.Context, pool *db.Pool, f nfr03Fixture) error {
	svc := memory.NewService(pool, clock.NewFake(nfr03Now()), logx.Discard())
	goalID := f.GoalID
	_, err := svc.RecordDecision(ctx, &memory.Decision{
		ProjectID: f.ProjectID, GoalID: &goalID, AuthorID: f.UserID,
		Title: nfr03DecisionTitle, Decision: nfr03DecisionText,
		Rationale: "because a decision log that can forget is worth less than none",
		DecidedAt: nfr03Now(),
	})
	return err
}

// nfr03WriteTheFive performs all five in the order their dependencies require
// and returns the first failure. Every one of them is acknowledged — by
// returning nil — before the caller is allowed to say it is ready to be killed.
func nfr03WriteTheFive(ctx context.Context, pool *db.Pool, f nfr03Fixture) error {
	steps := []struct {
		what string
		run  func() error
	}{
		{"acknowledged checkpoint", func() error { return nfr03WriteCheckpoint(ctx, pool, f) }},
		{"approved plan", func() error { return nfr03WritePlan(ctx, pool, f) }},
		{"acknowledged approval", func() error { return nfr03DecideApproval(ctx, pool, f) }},
		{"artifact version", func() error { return nfr03WriteVersion(ctx, pool, f) }},
		{"tool result", func() error { return nfr03WriteToolCall(ctx, pool, f) }},
		{"decision", func() error { return nfr03WriteDecision(ctx, pool, f) }},
	}
	for _, s := range steps {
		if err := s.run(); err != nil {
			return fmt.Errorf("%s: %w", s.what, err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// what must be there afterwards
// ---------------------------------------------------------------------------

// nfr03AssertTheFiveSurvived reads every item back through a pool that had
// nothing to do with writing it, and checks the CONTENT rather than the count.
//
// Each failure says why the invariant matters, because the person reading it
// will be reading it months from now with no idea what NFR-03 is.
func nfr03AssertTheFiveSurvived(t *testing.T, pool *pgxpool.Pool, f nfr03Fixture) {
	t.Helper()
	ctx := context.Background()

	var state []byte
	if err := pool.QueryRow(ctx,
		`select state from forge_checkpoints where task_id = $1 and kind = $2`,
		f.TaskID, nfr03CheckpointKind).Scan(&state); err != nil {
		t.Errorf("the acknowledged checkpoint is gone (%v). SaveCheckpoint returned a checkpoint, "+
			"so a worker is entitled to resume from it; if it is not there the worker restarts the task "+
			"and redoes work that was already paid for.", err)
	} else if !strings.Contains(string(state), "the resume state that was acknowledged") {
		t.Errorf("the checkpoint survived but its state is %q, not what was acknowledged. "+
			"A resume point that lost its state is a row, not a resume point.", string(state))
	}

	var plans int
	if err := pool.QueryRow(ctx,
		`select count(*) from forge_plans
		  where goal_id = $1 and rationale = $2 and superseded_at is null`,
		f.GoalID, nfr03PlanRationale).Scan(&plans); err != nil {
		t.Fatalf("reading forge_plans back: %v", err)
	}
	if plans != 1 {
		t.Errorf("the approved plan is not live and intact (%d rows match its rationale, want 1). "+
			"Apply returned a plan, so the goal is running against it; a goal whose plan cannot be "+
			"read back is running work nobody can explain.", plans)
	}
	var planTasks int
	if err := pool.QueryRow(ctx,
		`select count(*) from forge_tasks where goal_id = $1 and idempotency_key = $2 and title = $3`,
		f.GoalID, nfr03PlanTaskKey, nfr03PlanTaskTitle).Scan(&planTasks); err != nil {
		t.Fatalf("reading the plan's tasks back: %v", err)
	}
	if planTasks != 1 {
		t.Errorf("the acknowledged plan's task is missing (%d rows, want 1). A plan without its tasks "+
			"is a goal that will never do anything and never say why.", planTasks)
	}

	var decision, decidedBy, reason string
	if err := pool.QueryRow(ctx,
		`select decision, coalesce(decided_by,''), decision_reason from forge_approvals where id = $1`,
		f.ApprovalID).Scan(&decision, &decidedBy, &reason); err != nil {
		t.Fatalf("reading the approval back: %v", err)
	}
	if decision != string(engine.ApprovalApproved) || decidedBy != f.UserID || reason != nfr03ApprovalReason {
		t.Errorf("the acknowledged approval reads back as (%q, by %q, %q), want (approved, %q, %q). "+
			"The reviewer was told their answer was recorded; losing it asks them the same question "+
			"again, which reads to them as the system ignoring a decision they already made.",
			decision, decidedBy, reason, f.UserID, nfr03ApprovalReason)
	}

	var versions int
	if err := pool.QueryRow(ctx,
		`select count(*) from forge_artifact_versions where artifact_id = $1 and diff = $2`,
		f.ArtifactID, nfr03VersionDiff).Scan(&versions); err != nil {
		t.Fatalf("reading the artifact version back: %v", err)
	}
	if versions != 1 {
		t.Errorf("the acknowledged artifact version is not there with its diff (%d rows, want 1). "+
			"AppendVersion returned nil, so something downstream believes this change is on the "+
			"artifact's history; a gap in that history is a change nobody can trace.", versions)
	}

	var status, rawOutput string
	if err := pool.QueryRow(ctx,
		`select status, coalesce(raw_output,'') from forge_tool_calls where idempotency_key = $1`,
		nfr03ToolCallKey).Scan(&status, &rawOutput); err != nil {
		t.Errorf("the acknowledged tool result is gone (%v). The ledger is what makes a retry safe: "+
			"without this row the next attempt runs the tool again and repeats whatever it did to "+
			"the world.", err)
	} else if status != string(engine.ToolSucceeded) || rawOutput != nfr03ToolCallOutput {
		t.Errorf("the tool result survived as (%q, %q), want (succeeded, %q). A ledger row that "+
			"lost its output cannot short-circuit the retry it exists to short-circuit.",
			status, rawOutput, nfr03ToolCallOutput)
	}

	var decided string
	if err := pool.QueryRow(ctx,
		`select decision from forge_decisions where project_id = $1 and title = $2`,
		f.ProjectID, nfr03DecisionTitle).Scan(&decided); err != nil {
		t.Errorf("the acknowledged decision is gone (%v). RecordDecision returned it, and the "+
			"decision log is the only place the reasoning behind a choice survives; the timeline "+
			"keeps the consequence and not the reason.", err)
	} else if decided != nfr03DecisionText {
		t.Errorf("the decision survived but says %q, not what was acknowledged", decided)
	}
}

// nfr03AssertTheCommitLineHeld is the half that makes the other half mean
// something, and it is built as a MATCHED PAIR rather than as a single "it is
// not there" check.
//
// The child performs two writes that differ in exactly one respect: same
// function, same table, same task, same transaction shape — one commits and one
// does not, and then the process dies with no chance to tidy up. Both halves are
// asserted here. Without the committed half, "absent" could mean the fence is
// looking in the wrong schema, at the wrong task, or through a pool that sees
// nothing, and every "it survived" assertion above would be worth the same
// nothing. Without the uncommitted half, the fence would pass against a store
// that kept everything it was ever shown.
//
// So this pair is the fence's own proof that it can fail, kept in the fence
// rather than demonstrated once by hand and thrown away.
func nfr03AssertTheCommitLineHeld(t *testing.T, pool *pgxpool.Pool, f nfr03Fixture) {
	t.Helper()
	ctx := context.Background()

	var committed int
	if err := pool.QueryRow(ctx,
		`select count(*) from forge_checkpoints where task_id = $1 and kind = $2`,
		f.TaskID, nfr03CommittedKind).Scan(&committed); err != nil {
		t.Fatalf("looking for the write that did commit: %v", err)
	}
	if committed != 1 {
		t.Errorf("the transaction the child DID commit is not readable (%d rows of kind %q, want 1). "+
			"This is the control: if a committed write cannot be found here, this pool is not looking "+
			"where the child wrote, and every assertion about the five items above is vacuous.",
			committed, nfr03CommittedKind)
	}

	var uncommitted int
	if err := pool.QueryRow(ctx,
		`select count(*) from forge_checkpoints where task_id = $1 and kind = $2`,
		f.TaskID, nfr03UncommittedKind).Scan(&uncommitted); err != nil {
		t.Fatalf("looking for the uncommitted write: %v", err)
	}
	if uncommitted != 0 {
		t.Errorf("a write that was never committed is readable anyway (%d rows of kind %q, want 0). "+
			"It differs from the committed one beside it only in that nobody committed it, so this "+
			"says FORGE is persisting work it never acknowledged.", uncommitted, nfr03UncommittedKind)
	}
}

// ---------------------------------------------------------------------------
// the kill fence
// ---------------------------------------------------------------------------

// TestNFR03_NothingAcknowledgedIsLostWhenTheProcessThatWroteItIsKilled is the
// durability fence NFR-03 asks for, with a real process death in the middle.
//
// # Why a child process rather than a simulated crash
//
// Closing a pool is a polite crash: Go runs deferred functions, pgx sends a
// terminate message, and Postgres ends the session tidily. None of that happens
// when a machine dies. The child here is killed with os.Process.Kill —
// TerminateProcess on Windows, SIGKILL elsewhere — which runs no deferred
// function, flushes nothing and sends no terminate message. The five writes and
// the committed half of the matched pair must already be in Postgres; the
// uncommitted half must not be.
//
// # Why the child is a test in this same binary
//
// Nothing in this repository spawns a FORGE subprocess, and adding a binary to
// build and find would make the fence fragile on three platforms. os.Args[0] is
// the test binary, which is already built and already knows how to connect.
//
// # What it deliberately does not do
//
// It does not kill Postgres, and it does not prove Postgres survives a machine
// loss. It proves FORGE acknowledges only what Postgres has committed.
func TestNFR03_NothingAcknowledgedIsLostWhenTheProcessThatWroteItIsKilled(t *testing.T) {
	h := newHarness(t)
	schema := nfr03SchemaOf(t, h.pool)
	f := nfr03Seed(t, h)

	fixture, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^"+nfr03ChildTest+"$", "-test.v", "-test.timeout=5m")
	cmd.Env = append(os.Environ(),
		nfr03ChildEnv+"=1",
		nfr03SchemaEnv+"="+schema,
		nfr03FixtureEnv+"="+string(fixture),
	)
	// An os.Pipe rather than cmd.StdoutPipe, because both streams go down one
	// pipe: a child that dies on a panic says so on stderr, and losing that
	// would turn every child failure into "it exited before it was ready".
	// cmd.Wait would close a StdoutPipe under the reader; this one is ours.
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		pw.Close()
		pr.Close()
		t.Fatalf("starting the child that will be killed: %v", err)
	}
	// The parent's own handle on the write end is dropped here, so the pipe
	// reaches EOF when the child's handle goes — which is the moment it dies.
	pw.Close()
	t.Cleanup(func() { pr.Close() })

	// ‼️ Registered immediately, and before anything that can fail. A t.Fatal
	// between here and the kill would otherwise leave a child sitting on an open
	// transaction in a schema this test is about to drop — the drop would block,
	// and the run would look like a hang rather than a failure.
	killed := false
	kill := func() {
		if !killed && cmd.Process != nil {
			killed = true
			_ = cmd.Process.Kill()
		}
	}
	t.Cleanup(func() {
		kill()
		_ = cmd.Wait()
	})

	// The child's output is read on a goroutine so the wait below can be bounded
	// by a timer AND by the pipe closing. A child that dies before it is ready
	// closes the pipe, which ends the wait with the output that explains why.
	lines := make(chan string, 64)
	var mu sync.Mutex
	var transcript []string
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			mu.Lock()
			transcript = append(transcript, line)
			mu.Unlock()
			select {
			case lines <- line:
			default: // the parent stopped reading; keep draining so the child never blocks
			}
		}
	}()
	childOutput := func() string {
		mu.Lock()
		defer mu.Unlock()
		return strings.Join(transcript, "\n")
	}

	// Bounded. Two minutes is generous for five writes against a local Postgres
	// and short enough that a wedged child fails the run instead of owning the
	// machine. Never an unbounded receive: a child that hangs on a lock would
	// otherwise hang this test until the whole `go test` timeout.
	deadline := time.NewTimer(2 * time.Minute)
	defer deadline.Stop()

	backendPID := 0
waiting:
	for {
		select {
		case line, open := <-lines:
			if !open {
				t.Fatalf("the child exited before it was ready to be killed, so nothing was "+
					"acknowledged and this fence tested nothing. Its output was:\n%s", childOutput())
			}
			if strings.Contains(line, nfr03ChildFailure) {
				t.Fatalf("the child could not perform the five acknowledged writes: %s\n\nfull output:\n%s",
					line, childOutput())
			}
			if idx := strings.Index(line, nfr03ReadyMarker); idx >= 0 {
				fields := strings.Fields(line[idx:])
				if len(fields) < 2 {
					t.Fatalf("the child's ready marker carried no backend pid: %q", line)
				}
				backendPID, err = strconv.Atoi(fields[1])
				if err != nil {
					t.Fatalf("the child's ready marker carried %q, which is not a backend pid", fields[1])
				}
				break waiting
			}
		case <-deadline.C:
			t.Fatalf("the child never reported that it had acknowledged five writes and was holding "+
				"one more open. Its output so far was:\n%s", childOutput())
		}
	}

	// The kill. No signal, no handler, no deferred cleanup: on Windows this is
	// TerminateProcess, and everywhere else SIGKILL.
	childPID := cmd.Process.Pid
	kill()
	waitErr := cmd.Wait()
	// Logged so a green run still shows it did the thing it is named for. A
	// fence that kills nothing passes just as quietly as one that does.
	t.Logf("killed process %d (wait reported %v); it held Postgres backend %d on a write it had "+
		"begun and never committed", childPID, waitErr, backendPID)

	// A killed process's socket closes when the OS reaps it, and the Postgres
	// backend holding the uncommitted transaction only aborts when it notices.
	// Asserting absence before that would pass for the wrong reason — the row
	// would be invisible because the transaction is still OPEN, not because it
	// was rolled back. So wait for the backend itself to be gone, bounded.
	probe := nfr03Open(t, schema)
	t.Cleanup(probe.Close)
	gone := false
	for i := 0; i < 300 && !gone; i++ {
		var n int
		if err := probe.QueryRow(context.Background(),
			`select count(*) from pg_stat_activity where pid = $1`, backendPID).Scan(&n); err != nil {
			t.Fatalf("asking whether the killed child's backend is gone: %v", err)
		}
		if n == 0 {
			gone = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !gone {
		t.Fatalf("backend %d still exists 30s after the child holding it was killed; the "+
			"uncommitted write may still be live, and 'it is not there' would mean nothing", backendPID)
	}

	after := nfr03Open(t, schema)
	t.Cleanup(after.Close)
	nfr03AssertTheFiveSurvived(t, after, f)
	nfr03AssertTheCommitLineHeld(t, after, f)

	if t.Failed() {
		t.Logf("the child's output was:\n%s", childOutput())
	}
}

// TestNFR03_TheChildThatIsKilledWhileItHoldsAnUncommittedWrite is not a test of
// anything on its own. It is the victim half of the fence above.
//
// It refuses to run unless FORGE_NFR03_CHILD is set, so an ordinary
// `go test ./...` skips it rather than sitting here for two minutes.
//
// ‼️ Everything it reports goes to os.Stdout directly. testing buffers t.Log
// until a test ENDS, and this one is killed, so anything logged the usual way
// would die with it and the parent would see an empty pipe.
func TestNFR03_TheChildThatIsKilledWhileItHoldsAnUncommittedWrite(t *testing.T) {
	if os.Getenv(nfr03ChildEnv) != "1" {
		t.Skip("child half of the NFR-03 kill fence; it runs only as a subprocess of " +
			"TestNFR03_NothingAcknowledgedIsLostWhenTheProcessThatWroteItIsKilled")
	}
	say := func(format string, args ...any) {
		fmt.Fprintf(os.Stdout, format+"\n", args...)
		_ = os.Stdout.Sync()
	}
	fail := func(format string, args ...any) {
		say(nfr03ChildFailure+": "+format, args...)
		t.Fatalf(format, args...)
	}

	var f nfr03Fixture
	if err := json.Unmarshal([]byte(os.Getenv(nfr03FixtureEnv)), &f); err != nil {
		fail("the fixture the parent passed is not readable: %v", err)
	}
	schema := os.Getenv(nfr03SchemaEnv)
	if schema == "" {
		fail("no schema was passed, so this child would write somewhere the parent cannot look")
	}

	// Deliberately NOT newHarness: the harness creates and drops a schema, and a
	// child that created its own would write rows the parent could never see.
	pool := nfr03Open(t, schema)
	ctx := context.Background()

	if err := nfr03WriteTheFive(ctx, pool, f); err != nil {
		fail("one of the five acknowledged writes did not happen: %v", err)
	}

	// The matched pair. Two transactions, identical in every respect that could
	// matter — same function, same table, same task, same process, both killed
	// moments later — except that the first commits and the second does not.
	// The parent asserts both, so "the uncommitted one is gone" cannot be
	// explained by the parent looking in the wrong place.
	kept, err := pool.Begin(ctx)
	if err != nil {
		fail("beginning the transaction that will commit: %v", err)
	}
	if err := nfr03WriteInATransaction(ctx, kept, f, nfr03CommittedKind, nfr03CommittedState); err != nil {
		fail("performing the write that commits: %v", err)
	}
	if err := kept.Commit(ctx); err != nil {
		fail("committing the control write: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		fail("beginning the transaction that will never commit: %v", err)
	}
	if err := nfr03WriteInATransaction(ctx, tx, f, nfr03UncommittedKind, nfr03UncommittedState); err != nil {
		fail("performing the write that will never commit: %v", err)
	}
	var backendPID int
	if err := tx.QueryRow(ctx, "select pg_backend_pid()").Scan(&backendPID); err != nil {
		fail("asking which backend holds the uncommitted transaction: %v", err)
	}

	say("%s %d", nfr03ReadyMarker, backendPID)

	// Bounded. If the parent fails to kill this process for any reason, it dies
	// by itself rather than living on as an orphan holding an open transaction
	// against a schema somebody is trying to drop.
	time.Sleep(2 * time.Minute)
	fail("this child was never killed; the fence above did not do the thing it is named for")
}

// nfr03WriteInATransaction performs one write inside a caller-owned transaction,
// through the same production function the first of the five uses.
//
// The same function for both halves of the matched pair on purpose: if the
// uncommitted write were a hand-rolled INSERT, "it is not there" could be
// explained by the hand-rolled statement being wrong rather than by the commit
// never happening.
func nfr03WriteInATransaction(ctx context.Context, tx pgx.Tx, f nfr03Fixture, kind, state string) error {
	_, err := engine.NewRepository().SaveCheckpoint(ctx, tx, f.TaskID,
		kind, json.RawMessage(state), nfr03Now())
	return err
}

// ---------------------------------------------------------------------------
// per-item fences
// ---------------------------------------------------------------------------
//
// Cheaper than the kill fence and far more specific. Each writes ONE item, drops
// the pool that wrote it, opens a new one, and looks. A killed process is the
// strongest statement of "the writer went away"; closing the pool is the weakest
// one that still means it, and it is enough to name which item regressed.

// nfr03AfterTheWriterIsGone runs write, throws the pool away, and hands back a
// pool that shares nothing with it but the schema.
func nfr03AfterTheWriterIsGone(t *testing.T, write func(pool *db.Pool, f nfr03Fixture) error) (*pgxpool.Pool, nfr03Fixture) {
	t.Helper()
	h := newHarness(t)
	schema := nfr03SchemaOf(t, h.pool)
	f := nfr03Seed(t, h)
	if err := write(h.pool, f); err != nil {
		t.Fatalf("the write under test failed before anything could be lost: %v", err)
	}
	h.pool.Close()
	after := nfr03Open(t, schema)
	t.Cleanup(after.Close)
	return after, f
}

func TestNFR03_AnAcknowledgedCheckpointSurvivesThePoolThatWroteIt(t *testing.T) {
	after, f := nfr03AfterTheWriterIsGone(t, func(pool *db.Pool, f nfr03Fixture) error {
		return nfr03WriteCheckpoint(context.Background(), pool, f)
	})
	var state []byte
	if err := after.QueryRow(context.Background(),
		`select state from forge_checkpoints where task_id = $1 and kind = $2`,
		f.TaskID, nfr03CheckpointKind).Scan(&state); err != nil {
		t.Fatalf("SaveCheckpoint returned a checkpoint and the row is not there (%v). A worker is "+
			"entitled to resume from an acknowledged checkpoint; without it the task restarts.", err)
	}
	if !strings.Contains(string(state), "the resume state that was acknowledged") {
		t.Fatalf("the checkpoint survived with state %q, not what was acknowledged; a resume point "+
			"that lost its state cannot be resumed from", string(state))
	}
}

func TestNFR03_AnApprovedPlanSurvivesThePoolThatWroteIt(t *testing.T) {
	after, f := nfr03AfterTheWriterIsGone(t, func(pool *db.Pool, f nfr03Fixture) error {
		return nfr03WritePlan(context.Background(), pool, f)
	})
	var rationale string
	if err := after.QueryRow(context.Background(),
		`select rationale from forge_plans where goal_id = $1 and superseded_at is null`,
		f.GoalID).Scan(&rationale); err != nil {
		t.Fatalf("Apply returned a plan and no live plan is there (%v). The goal is running against "+
			"a plan nobody can read back.", err)
	}
	if rationale != nfr03PlanRationale {
		t.Fatalf("the live plan's rationale is %q, not the one Apply was given (%q). An approved plan "+
			"that lost why it was approved cannot be reviewed, replanned against, or explained.",
			rationale, nfr03PlanRationale)
	}
	var tasks int
	if err := after.QueryRow(context.Background(),
		`select count(*) from forge_tasks where goal_id = $1 and idempotency_key = $2`,
		f.GoalID, nfr03PlanTaskKey).Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	if tasks != 1 {
		t.Fatalf("the acknowledged plan's task is not there (%d rows, want 1); a plan without its "+
			"tasks is a goal that will never do anything and never say why", tasks)
	}
}

func TestNFR03_AnAcknowledgedApprovalSurvivesThePoolThatWroteIt(t *testing.T) {
	after, f := nfr03AfterTheWriterIsGone(t, func(pool *db.Pool, f nfr03Fixture) error {
		return nfr03DecideApproval(context.Background(), pool, f)
	})
	var decision, decidedBy, reason string
	if err := after.QueryRow(context.Background(),
		`select decision, coalesce(decided_by,''), decision_reason from forge_approvals where id = $1`,
		f.ApprovalID).Scan(&decision, &decidedBy, &reason); err != nil {
		t.Fatal(err)
	}
	if decision != string(engine.ApprovalApproved) {
		t.Fatalf("the approval reads back as %q after the writer went away, want approved. The "+
			"reviewer was told their answer was recorded; asking them again reads as the system "+
			"ignoring a decision they already made.", decision)
	}
	if decidedBy != f.UserID || reason != nfr03ApprovalReason {
		t.Fatalf("the approval survived as (by %q, %q), want (%q, %q). PRD SAF-05: an approval that "+
			"cannot name the person who made it is not an approval.",
			decidedBy, reason, f.UserID, nfr03ApprovalReason)
	}
}

func TestNFR03_AnArtifactVersionSurvivesThePoolThatWroteIt(t *testing.T) {
	after, f := nfr03AfterTheWriterIsGone(t, func(pool *db.Pool, f nfr03Fixture) error {
		return nfr03WriteVersion(context.Background(), pool, f)
	})
	var version int
	var diff, initiator, agentName string
	if err := after.QueryRow(context.Background(),
		`select version, diff, initiator_id, agent from forge_artifact_versions where artifact_id = $1`,
		f.ArtifactID).Scan(&version, &diff, &initiator, &agentName); err != nil {
		t.Fatalf("AppendVersion returned nil and the version is not there (%v). Something downstream "+
			"believes this change is on the artifact's history.", err)
	}
	if version != 1 || diff != nfr03VersionDiff || initiator != f.UserID ||
		agentName != string(workspace.AgentExecutor) {
		t.Fatalf("the version survived as (v%d, diff %q, initiator %q, agent %q), want "+
			"(v1, %q, %q, executor). WRK-04's seven facts are the version; a row that kept some of "+
			"them is an audit trail with holes in it.",
			version, diff, initiator, agentName, nfr03VersionDiff, f.UserID)
	}
}

func TestNFR03_AnAcknowledgedToolResultSurvivesThePoolThatWroteIt(t *testing.T) {
	after, f := nfr03AfterTheWriterIsGone(t, func(pool *db.Pool, f nfr03Fixture) error {
		return nfr03WriteToolCall(context.Background(), pool, f)
	})
	_ = f
	var status, rawOutput string
	if err := after.QueryRow(context.Background(),
		`select status, coalesce(raw_output,'') from forge_tool_calls where idempotency_key = $1`,
		nfr03ToolCallKey).Scan(&status, &rawOutput); err != nil {
		t.Fatalf("the ledger row is not there (%v). The ledger is what makes a retry safe: without "+
			"it the next attempt runs the tool again and repeats whatever it did to the world.", err)
	}
	if status != string(engine.ToolSucceeded) || rawOutput != nfr03ToolCallOutput {
		t.Fatalf("the tool result survived as (%q, %q), want (succeeded, %q). A ledger row that lost "+
			"its output cannot short-circuit the retry it exists to short-circuit.",
			status, rawOutput, nfr03ToolCallOutput)
	}
}

func TestNFR03_ADecisionSurvivesThePoolThatWroteIt(t *testing.T) {
	after, f := nfr03AfterTheWriterIsGone(t, func(pool *db.Pool, f nfr03Fixture) error {
		return nfr03WriteDecision(context.Background(), pool, f)
	})
	var decided, rationale string
	if err := after.QueryRow(context.Background(),
		`select decision, rationale from forge_decisions where project_id = $1 and title = $2`,
		f.ProjectID, nfr03DecisionTitle).Scan(&decided, &rationale); err != nil {
		t.Fatalf("RecordDecision returned a decision and the row is not there (%v). The decision log "+
			"is the only place the reasoning behind a choice survives — the timeline keeps the "+
			"consequence and not the reason.", err)
	}
	if decided != nfr03DecisionText || rationale == "" {
		t.Fatalf("the decision survived as (%q, rationale %q), want (%q, non-empty). A decision "+
			"without its reasoning is the consequence again, which the timeline already had.",
			decided, rationale, nfr03DecisionText)
	}
}

// ---------------------------------------------------------------------------
// fences on the fences
// ---------------------------------------------------------------------------

// nfr03Source reads a file from the tree and normalises line endings.
//
// core.autocrlf=true on Windows means the working copy has CRLF, while a Go raw
// string literal never does (the scanner discards \r inside one). Comparing them
// without this would fail on Windows and pass on Linux, which is the worst kind
// of fence.
func nfr03Source(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

// TestNFR03_TheSubstitutedWritesAreStillTheStatementsProductionRuns is why the
// two copied statements above are allowed to exist.
//
// Two of the five write paths cannot be called from this package (see the file
// comment), so the fence runs their statements instead of them. A copy that
// drifts fences nothing — worse, it fences something that looks right. This
// holds each copy byte-for-byte against the file it came from, so changing the
// production statement turns this red and whoever changed it updates the copy or
// deletes the fence, rather than leaving a green test behind a moved target.
func TestNFR03_TheSubstitutedWritesAreStillTheStatementsProductionRuns(t *testing.T) {
	for _, c := range []struct {
		what, path, statement string
	}{
		{
			what:      "the acknowledged-approval write in httpapi.(*GoalHandlers).Decide",
			path:      "../../httpapi/goals.go",
			statement: nfr03ApprovalDecisionSQL,
		},
		{
			what:      "the tool-result ledger write in agent.(*Executor).recordToolCall",
			path:      "../../agent/executor.go",
			statement: nfr03ToolLedgerSQL,
		},
	} {
		if !strings.Contains(nfr03Source(t, c.path), c.statement) {
			t.Errorf("%s no longer matches the copy this package's NFR-03 fence runs.\n\n"+
				"%s has changed, so the fence is now exercising a statement production does not.\n"+
				"Update the constant in nfr03_durability_integration_test.go to the new text — or, "+
				"if the write moved somewhere this package can call directly, call it and delete the "+
				"copy. The statement the fence holds is:\n%s", c.what, c.path, c.statement)
		}
	}
}

// TestNFR03_EveryWritePathStillSaysWhatItPromises holds the citations in place.
//
// A citation is a comment, so nothing else can make it go red — which is exactly
// why it needs its own fence: the next person to simplify one of these paths
// reads the comment or reads nothing. Each phrase below is the part of the
// citation that names what would BREAK, not the requirement number; a citation
// trimmed back to "NFR-03" warns nobody.
func TestNFR03_EveryWritePathStillSaysWhatItPromises(t *testing.T) {
	for _, c := range []struct{ path, at, phrase string }{
		{"repository.go", "engine.(*Repository).SaveCheckpoint",
			"acknowledging earlier than the row"},
		{"../workspace/repository.go", "workspace.(*Repository).AppendVersion",
			"loses part of an acknowledged version"},
		{"../../agent/apply.go", "agent.(*PlanApplier).Apply",
			"plan nobody can read back"},
		{"../../httpapi/goals.go", "httpapi.(*GoalHandlers).Decide",
			"acknowledged approval be lost"},
		{"../../agent/executor.go", "agent.(*Executor).recordToolCall",
			"make an acknowledged tool result losable"},
		{"../../agent/executor.go", "the best-effort iteration checkpoint in agent.(*Executor)",
			"NFR-03 durability is not violated here"},
		{"../memory/decision.go", "memory.(*Service).RecordDecision",
			"the deferred Rollback throws away"},
	} {
		src := nfr03Source(t, c.path)
		if !strings.Contains(src, "NFR-03") {
			t.Errorf("%s no longer cites NFR-03 at all (%s)", c.path, c.at)
			continue
		}
		if !strings.Contains(src, c.phrase) {
			t.Errorf("the NFR-03 citation at %s (%s) no longer says what would break it.\n"+
				"The missing phrase is %q. It is there so the next person to simplify this path is "+
				"warned about the specific thing they would lose, rather than being told a "+
				"requirement number they would have to go and look up.", c.at, c.path, c.phrase)
		}
	}
}
