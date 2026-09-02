package agent_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/agent"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/auth"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/identity"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/tools"
)

// TestLiveAgentLoop drives the whole system against a real model.
//
// # Why this test exists in this shape
//
// Every layer below it is tested in isolation, and every one of those tests uses
// a fake for the layer above. That leaves exactly one thing unproven: whether the
// pieces compose into an agent that actually does work. A plan the planner
// produces has to be executable by the executor, whose output has to be
// judgeable by the verifier, whose verdict has to be persistable by the worker —
// and a fake at any of those boundaries makes the test agree with itself.
//
// So this one runs the real planner, the real executor with real tools writing
// to a real filesystem, the real verifier on a different model family, and the
// real durable queue on live Postgres. The assertion at the end is not about a
// status column: it reads the files off disk.
//
// Skipped without FORGE_LIVE_LLM_TESTS so CI stays hermetic and free.
func TestLiveAgentLoop(t *testing.T) {
	if os.Getenv("FORGE_LIVE_LLM_TESTS") == "" || os.Getenv("FORGE_LLM_API_KEY") == "" {
		t.Skip("set FORGE_LLM_API_KEY and FORGE_LIVE_LLM_TESTS=1 to run the live agent loop")
	}
	h := newLiveHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	// A goal small enough to finish quickly and specific enough to check
	// objectively: the assertion is that named files exist with the right
	// contents, not that a model said it was done.
	goal := h.createGoal(t,
		"Create a project README and a version file",
		"In the workspace, create two files. "+
			"First: README.md, containing a level-1 markdown heading with the exact text "+
			"'Blueprint Service' and one sentence beneath it describing a service that stores blueprints. "+
			"Second: VERSION, containing exactly the text 0.1.0 and nothing else. "+
			"Verify both files exist and have the right contents before reporting completion.",
		engine.AutonomySandboxExecute, engine.RiskR1)

	// --- plan ---------------------------------------------------------------
	t.Log("planning against", h.client.ModelFor(llm.RolePlanner))
	plan, err := h.planner.Plan(ctx, goal, nil, "")
	if err != nil {
		t.Fatalf("planning failed: %v", err)
	}
	if plan.ClarificationNeeded != "" {
		t.Fatalf("the planner asked for clarification on an unambiguous goal: %s", plan.ClarificationNeeded)
	}
	t.Logf("plan: %d task(s) — %s", len(plan.Tasks), plan.Rationale)
	for _, pt := range plan.Tasks {
		t.Logf("  %-28s %-6s depends_on=%v", pt.Key, pt.RiskTier, pt.DependsOn)
	}

	dbPlan, created, err := h.applier.Apply(ctx, h.pool, goal, plan, "planner")
	if err != nil {
		t.Fatalf("applying the plan failed: %v", err)
	}
	t.Logf("applied plan v%d: %d task(s) written", dbPlan.Version, len(created))

	if err := h.applier.Activate(ctx, h.pool, goal); err != nil {
		t.Fatalf("activating the goal failed: %v", err)
	}

	// --- execute ------------------------------------------------------------
	// One worker, driven by hand so the test controls the loop rather than
	// racing a background goroutine.
	worker := agent.NewWorker(agent.WorkerDeps{
		Pool: h.pool, Repo: h.repo, Queue: h.queue, Budget: h.budget,
		Assembler: h.assembler, Executor: h.executor, Verifier: h.verifier,
		Config: h.cfg, WorkspaceRoot: h.workspaceRoot, Clock: clock.System{}, Log: h.log,
	})

	workerCtx, stopWorker := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = worker.Run(workerCtx)
	}()

	deadline := time.Now().Add(6 * time.Minute)
	var settled bool
	for time.Now().Before(deadline) {
		time.Sleep(3 * time.Second)
		depth, err := h.queue.Depth(ctx, h.pool, goal.ID)
		if err != nil {
			t.Fatal(err)
		}
		outstanding := 0
		for status, n := range depth {
			if status.Active() {
				outstanding += n
			}
		}
		if outstanding == 0 {
			settled = true
			break
		}
	}
	stopWorker()
	<-done

	// --- what actually happened --------------------------------------------
	tasks, err := h.repo.ListTasks(ctx, h.pool, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("final task states:")
	for _, task := range tasks {
		detail := ""
		if task.ErrorCode != "" {
			detail = " — " + task.ErrorCode + ": " + truncate(task.ErrorDetail, 160)
		}
		t.Logf("  %-10s %-40s attempts=%d verified=%v%s",
			task.Status, truncate(task.Title, 40), task.AttemptCount, task.Verified(), detail)
	}

	timeline, err := h.repo.Timeline(ctx, h.pool, goal.ID, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("timeline: %d event(s)", len(timeline))
	for i := len(timeline) - 1; i >= 0; i-- {
		e := timeline[i]
		t.Logf("  %3d [%-9s] %-26s %s", e.Seq, e.Actor, e.Kind, truncate(e.Summary, 90))
	}

	var calls int
	if err := h.pool.QueryRow(ctx,
		`select count(*) from forge_tool_calls tc join forge_tasks t on t.id = tc.task_id
		  where t.goal_id = $1`, goal.ID).Scan(&calls); err != nil {
		t.Fatal(err)
	}
	var spent int64
	if err := h.pool.QueryRow(ctx,
		`select tokens_spent from forge_goals where id = $1`, goal.ID).Scan(&spent); err != nil {
		t.Fatal(err)
	}
	t.Logf("tool calls: %d · tokens spent: %d", calls, spent)

	if !settled {
		t.Fatalf("the goal did not settle within the deadline")
	}

	// --- the assertion that matters -----------------------------------------
	//
	// Not "the task says succeeded" — the files on disk. A status column is the
	// agent's claim about the world; this is the world.
	ws := filepath.Join(h.workspaceRoot, goal.ID)

	readme, err := os.ReadFile(filepath.Join(ws, "README.md"))
	if err != nil {
		t.Fatalf("README.md was not created in the workspace: %v\nworkspace contents: %v", err, listDir(ws))
	}
	if !strings.Contains(string(readme), "# Blueprint Service") {
		t.Errorf("README.md does not contain the required heading:\n%s", readme)
	}

	versionFile, err := os.ReadFile(filepath.Join(ws, "VERSION"))
	if err != nil {
		t.Fatalf("VERSION was not created: %v\nworkspace contents: %v", err, listDir(ws))
	}
	if strings.TrimSpace(string(versionFile)) != "0.1.0" {
		t.Errorf("VERSION contains %q, want 0.1.0", strings.TrimSpace(string(versionFile)))
	}

	// Spend was recorded. A budget counting zero is a budget that cannot bind.
	if spent == 0 {
		t.Error("the goal recorded zero token spend; every ceiling would be unreachable")
	}
	// Tools genuinely ran. If the model produced the answer without touching the
	// filesystem, the files above could not exist — but assert it anyway, because
	// that is the claim the ledger is supposed to make checkable.
	if calls == 0 {
		t.Error("no tool calls were recorded, yet files exist; the ledger is not capturing them")
	}

	t.Logf("workspace: %v", listDir(ws))
}

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

type liveHarness struct {
	pool          *pgxpool.Pool
	repo          *engine.Repository
	queue         *engine.Queue
	budget        *engine.BudgetGuard
	assembler     *agent.Assembler
	executor      *agent.Executor
	verifier      *agent.Verifier
	planner       *agent.Planner
	applier       *agent.PlanApplier
	client        llm.Client
	cfg           config.EngineConfig
	workspaceRoot string
	log           *logx.Logger
	projectID     string
	userID        string
}

func newLiveHarness(t *testing.T) *liveHarness {
	t.Helper()

	url := os.Getenv("FORGE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("FORGE_TEST_DATABASE_URL is unset")
	}
	ctx := context.Background()
	schema := "forge_live_agent"

	admin, err := db.Connect(ctx, config.DBConfig{
		URL: url, MaxConns: 4, MinConns: 1,
		MaxConnLifetime: time.Hour, MaxConnIdleTime: time.Minute, ConnectTimeout: 10 * time.Second,
	}, logx.Discard())
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err := admin.Exec(ctx, "drop schema if exists "+schema+" cascade"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "create schema "+schema); err != nil {
		t.Fatal(err)
	}

	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	pool, err := db.Connect(ctx, config.DBConfig{
		URL: url + sep + "search_path=" + schema, MaxConns: 10, MinConns: 2,
		MaxConnLifetime: time.Hour, MaxConnIdleTime: time.Minute, ConnectTimeout: 10 * time.Second,
	}, logx.Discard())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MigrateFS(ctx, pool, db.Files, db.MigrationsDir, logx.Discard()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	// Logs go to the test output so a failure is diagnosable from the test log
	// alone, rather than requiring the run to be repeated with logging on.
	log := logx.New(logx.Options{
		Output: &testWriter{t}, Format: "text", Service: "live-agent",
	})

	cfg := config.EngineConfig{
		WorkerConcurrency:        1,
		LeaseDuration:            3 * time.Minute,
		LeaseHeartbeat:           20 * time.Second,
		PollInterval:             time.Second,
		MaxAttemptsPerTask:       3,
		BackoffBase:              2 * time.Second,
		BackoffMax:               30 * time.Second,
		MaxIterationsPerTask:     12,
		MaxToolCallsPerIteration: 8,
		MaxTokensPerGoal:         400_000,
		MaxCostCentsPerGoal:      500,
		MaxWallClockPerGoal:      time.Hour,
		MaxTaskDepth:             3,
		MaxTasksPerGoal:          20,
	}

	llmCfg := config.LLMConfig{
		BaseURL:        envOr("FORGE_LLM_BASE_URL", "https://dashscope.aliyuncs.com/compatible-mode/v1"),
		APIKey:         os.Getenv("FORGE_LLM_API_KEY"),
		Planner:        envOr("FORGE_LLM_PLANNER_MODEL", "qwen3.8-max"),
		Executor:       envOr("FORGE_LLM_EXECUTOR_MODEL", "qwen3.8-max"),
		Verifier:       envOr("FORGE_LLM_VERIFIER_MODEL", "deepseek-v4-pro"),
		Summarizer:     envOr("FORGE_LLM_SUMMARIZER_MODEL", "qwen3.8-flash"),
		RequestTimeout: 3 * time.Minute,
		MaxRetries:     2,
	}

	clk := clock.System{}
	client := llm.NewOpenAICompatible(llmCfg, log, clk)
	repo := engine.NewRepository()
	queue := engine.NewQueue()
	budget := engine.NewBudgetGuard(cfg)
	char := persona.DefaultCharacter()

	registry := tools.NewRegistry()
	registry.MustRegister(tools.ListTool{})
	registry.MustRegister(tools.ReadTool{})
	registry.MustRegister(tools.WriteTool{})
	registry.MustRegister(tools.ShellTool{})
	for _, tool := range tools.StandardUnavailableConnectors() {
		registry.MustRegister(tool)
	}

	root := t.TempDir()
	h := &liveHarness{
		pool: pool, repo: repo, queue: queue, budget: budget,
		assembler: agent.NewAssembler(repo, queue),
		executor:  agent.NewExecutor(client, registry, repo, budget, char, clk, log, pool),
		verifier:  agent.NewVerifier(client, char),
		planner:   agent.NewPlanner(client, char),
		applier:   agent.NewPlanApplier(repo, queue, budget, clk),
		client:    client, cfg: cfg, workspaceRoot: root, log: log,
	}
	h.seed(t)
	return h
}

func (h *liveHarness) seed(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()

	hash, err := auth.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	user := &identity.User{
		ID: id.New(id.PrefixUser), Email: "live@example.com", Status: identity.StatusActive,
		PasswordHash: hash, PasswordAlgo: auth.AlgoArgon2id,
		PasswordChangedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := identity.NewRepository().CreateUser(ctx, h.pool, user); err != nil {
		t.Fatal(err)
	}
	h.userID = user.ID

	h.projectID = id.New(id.PrefixProject)
	if _, err := h.pool.Exec(ctx,
		`insert into forge_projects (id, owner_id, name, pack, created_at, updated_at)
		 values ($1,$2,$3,'software',$4,$4)`, h.projectID, user.ID, "live agent test", now); err != nil {
		t.Fatal(err)
	}
}

func (h *liveHarness) createGoal(t *testing.T, title, statement string, autonomy engine.Autonomy, tier engine.RiskTier) *engine.Goal {
	t.Helper()
	now := time.Now().UTC()
	g := &engine.Goal{
		ID: id.New(id.PrefixGoal), ProjectID: h.projectID, CreatedBy: h.userID,
		Title: title, Statement: statement, Status: engine.GoalDraft,
		Autonomy: autonomy, RiskTier: tier, CreatedAt: now, UpdatedAt: now,
	}
	criteria, _ := json.Marshal([]engine.CompletionCriterion{})
	if _, err := h.pool.Exec(context.Background(), `
		insert into forge_goals (id, project_id, created_by, title, statement, status,
			autonomy, risk_tier, completion_criteria, created_at, updated_at)
		values ($1,$2,$3,$4,$5,'draft',$6,$7,$8,$9,$9)`,
		g.ID, g.ProjectID, g.CreatedBy, g.Title, g.Statement,
		string(autonomy), string(tier), criteria, now); err != nil {
		t.Fatal(err)
	}
	return g
}

// testWriter routes structured logs into the test output.
type testWriter struct{ t *testing.T }

func (w *testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

func listDir(dir string) []string {
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		out = append(out, rel)
		return nil
	})
	return out
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// TestApprovalGateBlocksConsequentialWork proves the gate is a gate.
//
// A goal at R2 must PAUSE before its work runs, not after. Asking afterwards
// would mean the consequential thing already happened and the human is being
// invited to review a fait accompli — which is the failure mode an approval gate
// exists to prevent, and which looks identical in a log to a working gate.
//
// The test does not use a model. The gate is enforced by the engine before any
// model call, and involving one would make the test slower and its result less
// specific.
func TestApprovalGateBlocksConsequentialWork(t *testing.T) {
	if os.Getenv("FORGE_TEST_DATABASE_URL") == "" {
		t.Skip("FORGE_TEST_DATABASE_URL is unset")
	}
	h := newGateHarness(t)
	ctx := context.Background()

	goal := h.createGoal(t, "Consequential change", "Do something at R2.",
		engine.AutonomyApprovalGated, engine.RiskR2)

	// A plan written by hand: this test is about the gate, not the planner.
	plan := &agent.PlanResult{
		Rationale: "single consequential task",
		Tasks: []agent.PlannedTask{{
			Key: "consequential-step", Title: "Change the baseline",
			Instruction: "This must not run without a human.",
			RiskTier:    "r2",
		}},
	}
	if _, created, err := h.applier.Apply(ctx, h.pool, goal, plan, "planner"); err != nil {
		t.Fatal(err)
	} else if len(created) != 1 {
		t.Fatalf("created %d tasks, want 1", len(created))
	}
	if err := h.applier.Activate(ctx, h.pool, goal); err != nil {
		t.Fatal(err)
	}

	tasks, err := h.repo.ListTasks(ctx, h.pool, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	task := tasks[0]
	if !task.RequiresApproval {
		t.Fatal("an R2 task was created without an approval requirement")
	}

	// Run one worker pass. The task should be claimed and immediately parked.
	worker := agent.NewWorker(agent.WorkerDeps{
		Pool: h.pool, Repo: h.repo, Queue: h.queue, Budget: h.budget,
		Assembler: h.assembler, Executor: h.executor, Verifier: h.verifier,
		Config: h.cfg, WorkspaceRoot: h.workspaceRoot, Clock: clock.System{}, Log: h.log,
	})
	runCtx, stop := context.WithTimeout(ctx, 12*time.Second)
	done := make(chan struct{})
	go func() { defer close(done); _ = worker.Run(runCtx) }()
	<-done
	stop()

	after, err := h.repo.GetTask(ctx, h.pool, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != engine.StatusAwaitingApproval {
		t.Fatalf("task is %q, want awaiting_approval; consequential work ran without a human", after.Status)
	}
	// A parked task must NOT hold a lease. A gate can stay open for days, and a
	// lease held across it either expires and looks like a crashed worker, or
	// occupies a worker slot doing nothing.
	if after.LeaseOwner != nil {
		t.Error("a task waiting for approval is still holding a worker lease")
	}

	// The gate exists as a durable row a human can act on, with a preview.
	var decision, summary string
	var preview []byte
	if err := h.pool.QueryRow(ctx,
		`select decision, summary, preview from forge_approvals where task_id = $1`, task.ID).
		Scan(&decision, &summary, &preview); err != nil {
		t.Fatalf("no approval row was opened: %v", err)
	}
	if decision != string(engine.ApprovalPending) {
		t.Errorf("decision = %q, want pending", decision)
	}
	if !strings.Contains(summary, "will not run until you approve") {
		t.Errorf("the approval summary does not tell the reviewer what is being asked: %q", summary)
	}
	if len(preview) == 0 || string(preview) == "{}" {
		t.Error("the approval carries no preview; a reviewer would be approving a title")
	}

	// The timeline records the request, attributed to the executor asking — not
	// to a human, and not to nobody.
	timeline, err := h.repo.Timeline(ctx, h.pool, goal.ID, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	var found *engine.Event
	for _, e := range timeline {
		if e.Kind == engine.EventApprovalRequested {
			found = e
		}
	}
	if found == nil {
		t.Fatal("no approval-requested event was recorded")
	}
	if found.Actor != engine.ActorExecutor {
		t.Errorf("the approval request is attributed to %q", found.Actor)
	}

	// And nothing further happens while it waits: a second worker pass must not
	// pick it up, because a parked task is not claimable.
	claimed, err := h.queue.Claim(ctx, h.pool, "another-worker", time.Minute, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if claimed != nil {
		t.Fatalf("a task awaiting approval was handed to a worker (%s)", claimed.ID)
	}
}

// newGateHarness builds a harness with no live model. The gate is enforced
// before any model call, so involving one would only make the test slower and
// its failure less specific.
func newGateHarness(t *testing.T) *liveHarness {
	t.Helper()
	t.Setenv("FORGE_LLM_API_KEY", "unused-by-this-test")
	t.Setenv("FORGE_LIVE_LLM_TESTS", "1")
	h := newLiveHarness(t)
	return h
}
