package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/auth"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/access"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/identity"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// A build run as an engine goal. Phase 2, stage A1, and Phase 7, stage E3.
//
// Against real Postgres, because every property here is a property of rows: a
// task's dependencies, a lease that runs out, a spend counter, a version kept,
// an event on a hash chain. A fake store would agree with whatever the code
// under test wrote into it.

const threeSteps = `{"steps":[{"name":"chassis","what":"the chassis"},
	{"name":"wheels","what":"four wheels"},{"name":"body","what":"the body"}]}`

// goalStub answers in order, charges tokens per answer, and can stop answering
// at one call — the way a worker's model call looks from inside a worker that
// has hung.
type goalStub struct {
	mu      sync.Mutex
	replies []string
	asked   []string
	n       int
	tokens  int64
	blockAt int
	reached chan struct{}
}

func (s *goalStub) Complete(ctx context.Context, req llm.Request) (*llm.Response, error) {
	s.mu.Lock()
	s.n++
	n := s.n
	for _, m := range req.Messages {
		if m.Role == llm.User {
			s.asked = append(s.asked, m.Content)
		}
	}
	r := `{"speech":"done"}`
	if n-1 < len(s.replies) {
		r = s.replies[n-1]
	}
	s.mu.Unlock()
	if n == s.blockAt {
		close(s.reached)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return &llm.Response{Content: r, FinishReason: "stop", Usage: llm.Usage{TotalTokens: s.tokens}}, nil
}

// No vision model, for the reason scriptedStub has none.
func (s *goalStub) ModelFor(r llm.Role) string {
	if r == llm.RoleVision {
		return ""
	}
	return "scripted"
}

func (s *goalStub) calls() (int, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n, append([]string(nil), s.asked...)
}

type buildHarness struct {
	pool    *db.Pool
	repo    *engine.Repository
	queue   *engine.Queue
	budget  *engine.BudgetGuard
	applier *PlanApplier
	geo     *geometry.Service
	cfg     config.EngineConfig
	userID  string
	project string
}

func newBuildHarness(t *testing.T) *buildHarness {
	t.Helper()
	url := os.Getenv("FORGE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("FORGE_TEST_DATABASE_URL is unset")
	}
	ctx := context.Background()
	schema := db.UniqueSchema("forge_build_", t.Name())
	dbc := func(u string) config.DBConfig {
		return config.DBConfig{URL: u, MaxConns: 10, MinConns: 1,
			MaxConnLifetime: time.Hour, MaxConnIdleTime: time.Minute, ConnectTimeout: 10 * time.Second}
	}
	admin, err := db.Connect(ctx, dbc(url), logx.Discard())
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	for _, q := range []string{"drop schema if exists " + schema + " cascade", "create schema " + schema} {
		if _, err := admin.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	pool, err := db.Connect(ctx, dbc(url+sep+"search_path="+schema), logx.Discard())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MigrateFS(ctx, pool, db.Files, db.MigrationsDir, logx.Discard()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		if c, err := db.Connect(context.Background(), dbc(url), logx.Discard()); err == nil {
			_, _ = c.Exec(context.Background(), "drop schema if exists "+schema+" cascade")
			c.Close()
		}
	})

	cfg := config.EngineConfig{
		WorkerConcurrency: 1, LeaseDuration: time.Minute,
		// An hour between idle polls, so a build that reaches its next step does
		// so because the finished step released it, not because the poll swept it
		// up (bugfix 2026-09-15). The fence for the poll sets its own.
		// Longer than any test: a hung worker must not keep renewing the lease the
		// test lets run out.
		LeaseHeartbeat: time.Hour, PollInterval: time.Hour,
		MaxAttemptsPerTask: 5, BackoffBase: 10 * time.Millisecond, BackoffMax: 50 * time.Millisecond,
		MaxIterationsPerTask: 12, MaxToolCallsPerIteration: 8,
		MaxTokensPerGoal: 1_000_000, MaxWallClockPerGoal: time.Hour, MaxTaskDepth: 3, MaxTasksPerGoal: 50,
	}
	clk := clock.System{}
	repo, queue, budget := engine.NewRepository(), engine.NewQueue(), engine.NewBudgetGuard(cfg)
	h := &buildHarness{pool: pool, repo: repo, queue: queue, budget: budget, cfg: cfg,
		applier: NewPlanApplier(repo, queue, budget, clk),
		geo:     geometry.NewService(pool, clk, logx.Discard())}

	now := time.Now().UTC()
	hash, err := auth.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	u := &identity.User{ID: id.New(id.PrefixUser), Email: "builder@example.com", Status: identity.StatusActive,
		PasswordHash: hash, PasswordAlgo: auth.AlgoArgon2id, PasswordChangedAt: now, CreatedAt: now, UpdatedAt: now}
	if err := identity.NewRepository().CreateUser(ctx, pool, u); err != nil {
		t.Fatal(err)
	}
	h.userID, h.project = u.ID, id.New(id.PrefixProject)
	if _, err := pool.Exec(ctx, `insert into forge_projects (id, owner_id, name, created_at, updated_at)
		values ($1,$2,'a car',$3,$3)`, h.project, h.userID, now); err != nil {
		t.Fatal(err)
	}
	if err := access.NewService(pool, clk, logx.Discard()).EnsureOwner(ctx, pool, h.project, h.userID); err != nil {
		t.Fatal(err)
	}
	return h
}

// goal writes a draft goal to build a car, with a token ceiling when one is given.
func (h *buildHarness) goal(t *testing.T, maxTokens *int64) *engine.Goal {
	t.Helper()
	now := time.Now().UTC()
	g := &engine.Goal{ID: id.New(id.PrefixGoal), ProjectID: h.project, CreatedBy: h.userID,
		Title: "Build a car", Statement: "a car", Status: engine.GoalDraft,
		Autonomy: engine.AutonomySandboxExecute, RiskTier: engine.RiskR1, CreatedAt: now, UpdatedAt: now}
	g.Budget.MaxTokens = maxTokens
	if _, err := h.pool.Exec(context.Background(), `
		insert into forge_goals (id, project_id, created_by, title, statement, status,
			autonomy, risk_tier, completion_criteria, max_tokens, created_at, updated_at)
		values ($1,$2,$3,$4,$5,'draft',$6,$7,'[]',$8,$9,$9)`,
		g.ID, g.ProjectID, g.CreatedBy, g.Title, g.Statement,
		string(g.Autonomy), string(g.RiskTier), maxTokens, now); err != nil {
		t.Fatal(err)
	}
	return g
}

// plan plans the goal as a build with stub and starts it.
func (h *buildHarness) plan(t *testing.T, goal *engine.Goal, stub llm.Client) {
	t.Helper()
	ctx := context.Background()
	if _, err := planBuildGoal(ctx, h.pool, &Conversation{client: stub}, h.applier, goal, logx.Discard()); err != nil {
		t.Fatal(err)
	}
	if err := h.applier.Activate(ctx, h.pool, goal, engine.ActorHuman, nil); err != nil {
		t.Fatal(err)
	}
	goal.Status = engine.GoalActive
}

func (h *buildHarness) steps(stub llm.Client) *BuildSteps {
	return NewBuildSteps(&Conversation{client: stub}, h.geo, h.repo, h.budget, h.pool, clock.System{}, logx.Discard())
}

func (h *buildHarness) worker(t *testing.T, stub llm.Client) *Worker {
	// Logs go to the test, so a step that stalls says why without a rerun.
	log := logx.New(logx.Options{Output: testLog{t}, Format: "text", Service: "build-goal", Level: slog.LevelWarn})
	return NewWorker(WorkerDeps{Pool: h.pool, Repo: h.repo, Queue: h.queue, Budget: h.budget,
		Assembler: NewAssembler(h.repo, h.queue), Builds: h.steps(stub),
		Config: h.cfg, WorkspaceRoot: t.TempDir(), Clock: clock.System{}, Log: log})
}

type testLog struct{ t *testing.T }

func (w testLog) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// start runs a worker until the returned stop is called.
func start(w *Worker) (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = w.Run(ctx) }()
	return func() { cancel(); <-done }
}

// settle runs a worker until the goal is no longer active.
func (h *buildHarness) settle(t *testing.T, w *Worker, goalID string) string {
	t.Helper()
	stop := start(w)
	defer stop()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		var status string
		if err := h.pool.QueryRow(context.Background(), `select status from forge_goals where id = $1`, goalID).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != string(engine.GoalActive) {
			return status
		}
		time.Sleep(20 * time.Millisecond)
	}
	var where []string
	for _, task := range h.tasks(t, goalID) {
		where = append(where, fmt.Sprintf("%s %s %s", task.IdempotencyKey, task.Status, task.ErrorDetail))
	}
	t.Fatalf("the goal did not settle within a minute; its steps: %s", strings.Join(where, "; "))
	return ""
}

// tasks is the goal's tasks in step order.
func (h *buildHarness) tasks(t *testing.T, goalID string) []*engine.Task {
	t.Helper()
	tasks, err := h.repo.ListTasks(context.Background(), h.pool, goalID)
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].IdempotencyKey < tasks[j].IdempotencyKey })
	return tasks
}

func (h *buildHarness) variants(t *testing.T) []geometry.Variant {
	t.Helper()
	v, err := h.geo.List(context.Background(), h.project, 50)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// A build's plan is one task per step, and each waits for the step before it.
func TestBuildGoal_EachStepIsATaskThatWaitsForTheOneBefore(t *testing.T) {
	h := newBuildHarness(t)
	goal := h.goal(t, nil)
	h.plan(t, goal, &goalStub{replies: []string{threeSteps}})

	tasks := h.tasks(t, goal.ID)
	if len(tasks) != 3 {
		t.Fatalf("a three-step build made %d task(s)", len(tasks))
	}
	for i, task := range tasks {
		in, ok := buildStepOf(task)
		if !ok || in.N != i+1 || in.Of != 3 || in.Asked != "a car" {
			t.Errorf("task %d is not step %d of the build: %s", i, i+1, task.Inputs)
		}
		deps, err := h.repo.ListDependencies(context.Background(), h.pool, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 && len(deps) != 0 {
			t.Errorf("the first step waits for %v", deps)
		}
		if i > 0 && (len(deps) != 1 || deps[0] != tasks[i-1].ID) {
			t.Errorf("step %d waits for %v, not the step before it; two steps at once would each "+
				"build on a model without the other", i+1, deps)
		}
	}
	if tasks[0].Status != engine.StatusReady || tasks[1].Status != engine.StatusPending {
		t.Errorf("statuses %s, %s: only the first step may be claimable", tasks[0].Status, tasks[1].Status)
	}
}

// Every model call a build makes, planning included, is charged to its goal.
func TestBuildGoal_EveryModelCallIsChargedToTheGoal(t *testing.T) {
	h := newBuildHarness(t)
	goal := h.goal(t, nil)
	stub := &goalStub{tokens: 100, replies: []string{threeSteps,
		wholeDoc("chassis", "Chassis"), addPart("wheels", "Wheels"), addPart("body", "Body")}}
	h.plan(t, goal, stub)

	if status := h.settle(t, h.worker(t, stub), goal.ID); status != string(engine.GoalSucceeded) {
		t.Fatalf("the build ended %s", status)
	}
	n, _ := stub.calls()
	var spent int64
	if err := h.pool.QueryRow(context.Background(), `select tokens_spent from forge_goals where id = $1`, goal.ID).Scan(&spent); err != nil {
		t.Fatal(err)
	}
	if spent != int64(n)*100 {
		t.Errorf("the goal was charged %d tokens for %d calls of 100; a budget that does not see a "+
			"build's calls is not a budget", spent, n)
	}
	if v := h.variants(t); len(v) != 3 || len(v[0].Document.Parts) != 3 {
		t.Errorf("kept %d version(s), the newest with %d part(s); want 3 and 3", len(v), partsOf(v))
	}
}

func partsOf(v []geometry.Variant) int {
	if len(v) == 0 {
		return 0
	}
	return len(v[0].Document.Parts)
}

// A worker that stops answering mid-step loses that step and nothing else.
func TestBuildGoal_AWorkerThatHangsIsResumedFromTheLastStepKept(t *testing.T) {
	h := newBuildHarness(t)
	goal := h.goal(t, nil)
	hung := &goalStub{tokens: 1, blockAt: 4, reached: make(chan struct{}),
		replies: []string{threeSteps, wholeDoc("chassis", "Chassis"), addPart("wheels", "Wheels")}}
	h.plan(t, goal, hung)

	stopHung := start(h.worker(t, hung))
	select {
	case <-hung.reached:
	case <-time.After(60 * time.Second):
		stopHung()
		t.Fatal("the first worker never reached step 3")
	}
	// Its lease runs out, as it does for a worker that died.
	if _, err := h.pool.Exec(context.Background(), `update forge_tasks set lease_expires_at = now() - interval '1 minute'
		where goal_id = $1 and status in ('claimed','running')`, goal.ID); err != nil {
		t.Fatal(err)
	}

	next := &goalStub{tokens: 1, replies: []string{addPart("body", "Body")}}
	status := h.settle(t, h.worker(t, next), goal.ID)
	stopHung()

	if status != string(engine.GoalSucceeded) {
		t.Fatalf("the resumed build ended %s", status)
	}
	n, asked := next.calls()
	if n != 1 {
		t.Errorf("the next worker asked the model %d times; steps 1 and 2 were kept and must not be built again", n)
	}
	if len(asked) == 0 || !strings.Contains(asked[0], "Chassis") || !strings.Contains(asked[0], "Wheels") {
		t.Errorf("step 3 did not start from the model steps 1 and 2 kept:\n%.400s", strings.Join(asked, "\n"))
	}
	if v := h.variants(t); len(v) != 3 || partsOf(v) != 3 {
		t.Errorf("kept %d version(s), the newest with %d part(s); want 3 and 3", len(v), partsOf(v))
	}
}

// A worker told to stop hands its step back rather than sitting on the lease.
func TestBuildGoal_AStoppedWorkerHandsItsStepBack(t *testing.T) {
	h := newBuildHarness(t)
	goal := h.goal(t, nil)
	stub := &goalStub{tokens: 1, blockAt: 3, reached: make(chan struct{}),
		replies: []string{threeSteps, wholeDoc("chassis", "Chassis")}}
	h.plan(t, goal, stub)

	stop := start(h.worker(t, stub))
	select {
	case <-stub.reached:
	case <-time.After(60 * time.Second):
		stop()
		t.Fatal("the worker never reached step 2")
	}
	stop()

	step2 := h.tasks(t, goal.ID)[1]
	if step2.Status != engine.StatusReady || step2.LeaseOwner != nil {
		t.Errorf("step 2 is %s held by %v after its worker stopped; it should be ready for the next worker "+
			"now, not after its lease runs out", step2.Status, step2.LeaseOwner)
	}
}

// A step that kept its model is not built a second time.
func TestBuildGoal_AStepKeptBeforeItsWorkerStoppedIsNotBuiltAgain(t *testing.T) {
	h := newBuildHarness(t)
	goal := h.goal(t, nil)
	stub := &goalStub{tokens: 1, replies: []string{threeSteps, wholeDoc("chassis", "Chassis"), wholeDoc("chassis", "Again")}}
	h.plan(t, goal, stub)
	first := h.tasks(t, goal.ID)[0]
	in, _ := buildStepOf(first)
	b := h.steps(stub)

	once, err := b.run(context.Background(), goal, first, in)
	if err != nil {
		t.Fatal(err)
	}
	// The worker stopped here: the model is kept and the task was never marked done.
	again, err := b.run(context.Background(), goal, first, in)
	if err != nil {
		t.Fatal(err)
	}

	if n, _ := stub.calls(); n != 2 {
		t.Errorf("the model was asked %d times; the second run of a kept step must not ask at all", n)
	}
	if string(once.Result) != string(again.Result) {
		t.Errorf("the second run kept %s, the first %s", again.Result, once.Result)
	}
	if v := h.variants(t); len(v) != 1 {
		t.Errorf("%d versions kept for one step", len(v))
	}
}

// Past its ceiling, a build stops between steps, keeps what it built, and ends failed.
func TestBuildGoal_ABudgetRefusalStopsTheGoalCleanly(t *testing.T) {
	h := newBuildHarness(t)
	// 30,000, since calls reserve before they are placed (2026-09-17): the plan reserves
	// the documented default of 12,000 and fits; after its 12,000, step 1's call may cost
	// 15,000 and fits; after step 1, step 2's does not. In thousands because the plan
	// reserves rather than being free (2026-09-20) — see engine.FirstCallReserve.
	ceiling := int64(30_000)
	goal := h.goal(t, &ceiling)
	stub := &goalStub{tokens: 12_000, replies: []string{threeSteps,
		wholeDoc("chassis", "Chassis"), addPart("wheels", "Wheels"), addPart("body", "Body")}}
	h.plan(t, goal, stub) // 12,000 tokens: under the ceiling, so step 1 runs

	if status := h.settle(t, h.worker(t, stub), goal.ID); status != string(engine.GoalFailed) {
		t.Fatalf("a build past its budget ended %s", status)
	}
	if n, _ := stub.calls(); n != 2 {
		t.Errorf("%d model calls; the plan and step 1 fit, and nothing after them may be asked", n)
	}
	tasks := h.tasks(t, goal.ID)
	got := []engine.TaskStatus{tasks[0].Status, tasks[1].Status, tasks[2].Status}
	want := []engine.TaskStatus{engine.StatusSucceeded, engine.StatusFailed, engine.StatusSkipped}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("steps ended %v, want %v", got, want)
			break
		}
	}
	var refusals int
	if err := h.pool.QueryRow(context.Background(), `select count(*) from forge_events where goal_id = $1 and kind = $2`,
		goal.ID, engine.EventBudgetExceeded).Scan(&refusals); err != nil {
		t.Fatal(err)
	}
	if refusals == 0 {
		t.Error("the timeline does not say the budget stopped the build")
	}
	if v := h.variants(t); len(v) != 1 {
		t.Errorf("%d version(s) kept; step 1 was built before the ceiling and must survive it", len(v))
	}
}

// Inside a step, a call past the ceiling is refused, and the step fails for it.
func TestBuildGoal_AStepStopsAskingOnceTheBudgetIsSpent(t *testing.T) {
	h := newBuildHarness(t)
	// 12,000 — exactly engine.FirstCallReserve, so the step's own call is placed and
	// its repair, which would reserve 15,000 on top, is not.
	ceiling := int64(12_000)
	goal := h.goal(t, &ceiling)
	// Written directly rather than planned, so the ceiling is untouched when the
	// step starts.
	if _, _, err := h.applier.Apply(context.Background(), h.pool, goal,
		buildPlan("a car", []buildTask{{Name: "chassis", What: "the chassis"}, {Name: "body", What: "the body"}}), "planner"); err != nil {
		t.Fatal(err)
	}
	// A sweep whose outline is a line: faulty, so the step asks for a repair.
	broken := `{"speech":"x","prototype":{"name":"m","units":"mm","parts":[
	  {"id":"chassis","name":"Chassis","shape":"sweep","profile":[{"x":0,"y":0},{"x":10,"y":0}],
	   "path":[{"x":0,"y":0,"z":0},{"x":0,"y":0,"z":100}]}]}}`
	stub := &goalStub{tokens: 12_000, replies: []string{broken, broken, broken}}
	first := h.tasks(t, goal.ID)[0]
	in, _ := buildStepOf(first)

	_, err := h.steps(stub).run(context.Background(), goal, first, in)

	if n, _ := stub.calls(); n != 1 {
		t.Errorf("the step made %d model calls; the first spent the budget, so its repair must not be asked", n)
	}
	if err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("a step refused by the budget returned %v", err)
	}
	if errs.IsRetryable(err) {
		t.Error("a budget refusal is retryable, so the step would be tried again against the same ceiling")
	}
}

// E3: each kept step writes artifact.changed on the goal's chain, naming its task.
func TestBuildGoal_AStepKeptInsideAGoalWritesAChainedArtifactEvent(t *testing.T) {
	h := newBuildHarness(t)
	goal := h.goal(t, nil)
	stub := &goalStub{tokens: 1, replies: []string{threeSteps,
		wholeDoc("chassis", "Chassis"), addPart("wheels", "Wheels"), addPart("body", "Body")}}
	h.plan(t, goal, stub)
	if status := h.settle(t, h.worker(t, stub), goal.ID); status != string(engine.GoalSucceeded) {
		t.Fatalf("the build ended %s", status)
	}

	rows, err := h.pool.Query(context.Background(), `select task_id from forge_events
		where goal_id = $1 and kind = $2 order by seq`, goal.ID, engine.EventArtifactChanged)
	if err != nil {
		t.Fatal(err)
	}
	var named []string
	for rows.Next() {
		var task *string
		if err := rows.Scan(&task); err != nil {
			t.Fatal(err)
		}
		if task == nil {
			named = append(named, "")
		} else {
			named = append(named, *task)
		}
	}
	rows.Close()
	tasks := h.tasks(t, goal.ID)
	if len(named) != 3 {
		t.Fatalf("%d artifact.changed event(s) for three kept steps", len(named))
	}
	for i, task := range tasks {
		if named[i] != task.ID {
			t.Errorf("step %d's version names task %q on the timeline, want %q", i+1, named[i], task.ID)
		}
	}
	report, err := h.repo.VerifyChain(context.Background(), h.pool, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Intact() || report.Chained != report.Events {
		t.Errorf("the goal's chain: %s", report.Summary())
		for _, f := range report.Findings {
			t.Logf("  seq %d %s: %s (%s)", f.Seq, f.Kind, f.Problem, f.Detail)
		}
	}
}

// A task a crash left waiting is released on the idle poll. Bugfix 2026-09-15.
func TestWorker_ATaskLeftWaitingByACrashIsReleasedOnTheIdlePoll(t *testing.T) {
	h := newBuildHarness(t)
	h.cfg.PollInterval = 20 * time.Millisecond
	goal := h.goal(t, nil)
	stub := &goalStub{tokens: 1, replies: []string{
		`{"steps":[{"name":"chassis","what":"the chassis"},{"name":"body","what":"the body"}]}`,
		wholeDoc("body", "Body")}}
	h.plan(t, goal, stub)

	// Step 1 finished by a worker that died before it released step 2.
	first := h.tasks(t, goal.ID)[0]
	for _, next := range []engine.TaskStatus{engine.StatusClaimed, engine.StatusRunning, engine.StatusSucceeded} {
		if err := h.repo.TransitionTask(context.Background(), h.pool, first, next, time.Now().UTC(), engine.TaskMutation{}); err != nil {
			t.Fatal(err)
		}
	}

	if status := h.settle(t, h.worker(t, stub), goal.ID); status != string(engine.GoalSucceeded) {
		t.Fatalf("the goal ended %s", status)
	}
	if n, _ := stub.calls(); n != 2 {
		t.Errorf("%d model calls; step 2 should have been released and built once", n)
	}
}
