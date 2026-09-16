package agent_test

import (
	"context"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/agent"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/tools"
)

// A worker carries a plan past its first layer, and stops a goal that is out of
// budget. Two engine defects found building stage A1 of
// docs/plan-2026-09-13-millions-of-parts.md, fenced here through the real worker,
// queue and executor on Postgres, with a model that completes every task at once.
//
// docs/bugfix/2026-09-15-a-finished-task-never-released-the-tasks-waiting-on-it.md
// docs/bugfix/2026-09-15-a-budget-refusal-left-its-task-claimed.md

// doneModel finishes every task it is given, on its first answer.
type doneModel struct {
	mu sync.Mutex
	n  int
}

func (m *doneModel) Complete(context.Context, llm.Request) (*llm.Response, error) {
	m.mu.Lock()
	m.n++
	m.mu.Unlock()
	return &llm.Response{FinishReason: "stop", Usage: llm.Usage{TotalTokens: 10},
		Content: `{"status":"completed","summary":"done","result":{},"evidence":["the stub said so"],"assumptions":[],"blocked_reason":""}`}, nil
}

func (m *doneModel) ModelFor(llm.Role) string { return "stub" }

func (m *doneModel) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.n
}

// stubWorker is a worker whose executor asks model, polling the idle queue every poll.
func (h *liveHarness) stubWorker(model llm.Client, poll time.Duration) *agent.Worker {
	cfg := h.cfg
	cfg.PollInterval = poll
	exec := agent.NewExecutor(model, tools.NewRegistry(), h.repo, h.budget, persona.DefaultCharacter(),
		clock.System{}, h.log, h.pool)
	return agent.NewWorker(agent.WorkerDeps{
		Pool: h.pool, Repo: h.repo, Queue: h.queue, Budget: h.budget,
		Assembler: h.assembler, Executor: exec, Verifier: h.verifier,
		Config: cfg, WorkspaceRoot: h.workspaceRoot, Clock: clock.System{}, Log: h.log,
	})
}

// seedChain plans and starts a goal of two tasks, the second waiting on the first.
func (h *liveHarness) seedChain(t *testing.T, goal *engine.Goal) (first, second *engine.Task) {
	t.Helper()
	plan := &agent.PlanResult{
		Rationale: "two tasks, one after the other",
		Tasks: []agent.PlannedTask{
			{Key: "chain-a", Title: "A", Instruction: "do a", RiskTier: "r1"},
			{Key: "chain-b", Title: "B", Instruction: "do b", RiskTier: "r1", DependsOn: []string{"chain-a"}},
		},
	}
	ctx := context.Background()
	if _, _, err := h.applier.Apply(ctx, h.pool, goal, plan, "planner"); err != nil {
		t.Fatal(err)
	}
	if err := h.applier.Activate(ctx, h.pool, goal, engine.ActorHuman, nil); err != nil {
		t.Fatal(err)
	}
	return h.chainTasks(t, goal.ID)
}

func (h *liveHarness) chainTasks(t *testing.T, goalID string) (first, second *engine.Task) {
	t.Helper()
	tasks, err := h.repo.ListTasks(context.Background(), h.pool, goalID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("the goal has %d tasks, want 2", len(tasks))
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].IdempotencyKey < tasks[j].IdempotencyKey })
	return tasks[0], tasks[1]
}

// runUntilSettled runs w until the goal is no longer active, or a minute passes.
func (h *liveHarness) runUntilSettled(t *testing.T, w *agent.Worker, goalID string) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = w.Run(ctx) }()
	defer func() { cancel(); <-done }()

	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		if status, _, _ := h.goalOutcome(t, goalID); status != string(engine.GoalActive) {
			return status
		}
		time.Sleep(20 * time.Millisecond)
	}
	a, b := h.chainTasks(t, goalID)
	t.Fatalf("the goal did not settle within a minute; its tasks are %s and %s", a.Status, b.Status)
	return ""
}

func skipWithoutDatabase(t *testing.T) {
	t.Helper()
	if os.Getenv("FORGE_TEST_DATABASE_URL") == "" {
		t.Skip("FORGE_TEST_DATABASE_URL is unset")
	}
}

// A task that finishes releases the task waiting on it, before the worker next
// claims. The idle poll is an hour away, so only the release can move it.
func TestWorker_AFinishedTaskReleasesTheTasksWaitingOnIt(t *testing.T) {
	skipWithoutDatabase(t)
	h := newGateHarness(t)
	goal := h.createGoal(t, "A chain", "two tasks, one after the other", engine.AutonomySandboxExecute, engine.RiskR1)
	h.seedChain(t, goal)
	model := &doneModel{}

	status := h.runUntilSettled(t, h.stubWorker(model, time.Hour), goal.ID)

	if status != string(engine.GoalSucceeded) {
		t.Fatalf("the goal ended %s", status)
	}
	if n := model.calls(); n != 2 {
		t.Errorf("the model was asked %d times; each task should have run once", n)
	}
}

// A task a crash left waiting — its dependency finished by a worker that died
// before releasing it — is released on the idle poll.
func TestWorker_ATaskLeftWaitingByACrashIsReleasedOnTheIdlePoll(t *testing.T) {
	skipWithoutDatabase(t)
	h := newGateHarness(t)
	goal := h.createGoal(t, "A stranded chain", "the first task finished, nobody released the second",
		engine.AutonomySandboxExecute, engine.RiskR1)
	first, _ := h.seedChain(t, goal)
	h.markTerminal(t, first, engine.StatusSucceeded)
	model := &doneModel{}

	status := h.runUntilSettled(t, h.stubWorker(model, 20*time.Millisecond), goal.ID)

	if status != string(engine.GoalSucceeded) {
		t.Fatalf("the goal ended %s", status)
	}
	if n := model.calls(); n != 1 {
		t.Errorf("the model was asked %d times; only the released task should have run", n)
	}
}

// A goal that has spent its budget stops: its task fails, the task after it is
// skipped, the timeline says why, and the model is never asked.
func TestWorker_ABudgetRefusalStopsTheGoal(t *testing.T) {
	skipWithoutDatabase(t)
	h := newGateHarness(t)
	goal := h.createGoal(t, "Out of budget", "spent before it started", engine.AutonomySandboxExecute, engine.RiskR1)
	h.seedChain(t, goal)
	if _, err := h.pool.Exec(context.Background(),
		`update forge_goals set max_tokens = 100, tokens_spent = 100 where id = $1`, goal.ID); err != nil {
		t.Fatal(err)
	}
	model := &doneModel{}

	status := h.runUntilSettled(t, h.stubWorker(model, time.Hour), goal.ID)

	if status != string(engine.GoalFailed) {
		t.Fatalf("a goal out of budget ended %s", status)
	}
	if n := model.calls(); n != 0 {
		t.Errorf("the model was asked %d times by a goal with no budget left", n)
	}
	a, b := h.chainTasks(t, goal.ID)
	if a.Status != engine.StatusFailed || b.Status != engine.StatusSkipped {
		t.Errorf("tasks ended %s and %s, want failed and skipped", a.Status, b.Status)
	}
	var refusals int
	if err := h.pool.QueryRow(context.Background(),
		`select count(*) from forge_events where goal_id = $1 and kind = $2`, goal.ID, engine.EventBudgetExceeded).
		Scan(&refusals); err != nil {
		t.Fatal(err)
	}
	if refusals == 0 {
		t.Error("the timeline does not say the budget stopped the goal")
	}
}
