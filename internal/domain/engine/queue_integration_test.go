package engine_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/auth"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/identity"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

type harness struct {
	pool   *pgxpool.Pool
	repo   *engine.Repository
	queue  *engine.Queue
	clk    *clock.Fake
	goalID string
	planID string
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	url := os.Getenv("FORGE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("FORGE_TEST_DATABASE_URL is unset; skipping live-database tests. Run `make db-up` then `make test-integration`.")
	}
	ctx := context.Background()

	schema := "forge_eng_" + strings.ToLower(strings.NewReplacer("/", "_", "-", "_").Replace(t.Name()))
	if len(schema) > 60 {
		schema = schema[:60]
	}

	admin, err := db.Connect(ctx, config.DBConfig{
		URL: url, MaxConns: 4, MinConns: 1,
		MaxConnLifetime: time.Hour, MaxConnIdleTime: time.Minute, ConnectTimeout: 10 * time.Second,
	}, logx.Discard())
	if err != nil {
		t.Fatalf("cannot reach the test database: %v", err)
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
		URL: url + sep + "search_path=" + schema, MaxConns: 16, MinConns: 2,
		MaxConnLifetime: time.Hour, MaxConnIdleTime: time.Minute, ConnectTimeout: 10 * time.Second,
	}, logx.Discard())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MigrateFS(ctx, pool, db.Files, db.MigrationsDir, logx.Discard()); err != nil {
		t.Fatalf("migrating the test schema: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		cleanup, err := db.Connect(context.Background(), config.DBConfig{
			URL: url, MaxConns: 2, MinConns: 1,
			MaxConnLifetime: time.Minute, MaxConnIdleTime: time.Minute, ConnectTimeout: 10 * time.Second,
		}, logx.Discard())
		if err == nil {
			_, _ = cleanup.Exec(context.Background(), "drop schema if exists "+schema+" cascade")
			cleanup.Close()
		}
	})

	h := &harness{
		pool:  pool,
		repo:  engine.NewRepository(),
		queue: engine.NewQueue(),
		clk:   clock.NewFake(time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)),
	}
	h.seed(t)
	return h
}

// seed creates the owner, project, goal and plan that tasks hang off. Built
// through the real identity repository rather than raw INSERTs, so the fixture
// exercises the same constraints production does.
func (h *harness) seed(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	now := h.clk.Now()

	hash, err := auth.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	user := &identity.User{
		ID: id.New(id.PrefixUser), Email: "owner@example.com", Status: identity.StatusActive,
		PasswordHash: hash, PasswordAlgo: auth.AlgoArgon2id,
		PasswordChangedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := identity.NewRepository().CreateUser(ctx, h.pool, user); err != nil {
		t.Fatal(err)
	}

	projectID := id.New(id.PrefixProject)
	if _, err := h.pool.Exec(ctx,
		`insert into forge_projects (id, owner_id, name, created_at, updated_at)
		 values ($1,$2,$3,$4,$4)`, projectID, user.ID, "test project", now); err != nil {
		t.Fatal(err)
	}

	h.goalID = id.New(id.PrefixGoal)
	if _, err := h.pool.Exec(ctx,
		`insert into forge_goals (id, project_id, created_by, title, statement, status, started_at, created_at, updated_at)
		 values ($1,$2,$3,$4,$5,'active',$6,$6,$6)`,
		h.goalID, projectID, user.ID, "test goal", "do the thing", now); err != nil {
		t.Fatal(err)
	}

	h.planID = id.New(id.PrefixPlan)
	if _, err := h.pool.Exec(ctx,
		`insert into forge_plans (id, goal_id, version, created_at) values ($1,$2,1,$3)`,
		h.planID, h.goalID, now); err != nil {
		t.Fatal(err)
	}
}

// addTask creates a task in the given status with optional dependencies.
func (h *harness) addTask(t *testing.T, title string, status engine.TaskStatus, deps ...string) *engine.Task {
	t.Helper()
	now := h.clk.Now()
	task := &engine.Task{
		ID: id.New(id.PrefixTask), GoalID: h.goalID, PlanID: h.planID,
		Title: title, Instruction: "do " + title,
		Status: status, IdempotencyKey: "key-" + title,
		MaxAttempts: 3, NotBefore: now, Priority: 100,
		RiskTier: engine.RiskR1, CreatedAt: now, UpdatedAt: now,
	}
	if err := h.repo.CreateTask(context.Background(), h.pool, task, deps); err != nil {
		t.Fatalf("creating task %q: %v", title, err)
	}
	return task
}

func (h *harness) status(t *testing.T, taskID string) engine.TaskStatus {
	t.Helper()
	task, err := h.repo.GetTask(context.Background(), h.pool, taskID)
	if err != nil {
		t.Fatal(err)
	}
	return task.Status
}

// ---------------------------------------------------------------------------
// leases
// ---------------------------------------------------------------------------

// TestConcurrentWorkersNeverShareATask is bullet B22, and the single most
// important property of the queue.
//
// If two workers can claim one task, everything downstream is unsound: a task
// runs twice, its side effects happen twice, and its two results race to write
// the same row. The guarantee comes from `FOR UPDATE SKIP LOCKED` inside the
// claiming UPDATE, so it can only be tested by claiming concurrently for real.
func TestConcurrentWorkersNeverShareATask(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	const taskCount = 40
	for i := 0; i < taskCount; i++ {
		h.addTask(t, fmt.Sprintf("t%02d", i), engine.StatusReady)
	}

	const workers = 12
	var wg sync.WaitGroup
	var mu sync.Mutex
	claimed := map[string]string{} // task id -> worker that claimed it
	duplicates := []string{}
	start := make(chan struct{})

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			workerID := fmt.Sprintf("worker-%02d", w)
			<-start
			for {
				task, err := h.queue.Claim(ctx, h.pool, workerID, time.Minute, h.clk.Now())
				if err != nil {
					t.Errorf("%s: claim failed: %v", workerID, err)
					return
				}
				if task == nil {
					return // queue drained
				}
				mu.Lock()
				if prev, seen := claimed[task.ID]; seen {
					duplicates = append(duplicates,
						fmt.Sprintf("task %s claimed by both %s and %s", task.ID, prev, workerID))
				}
				claimed[task.ID] = workerID
				mu.Unlock()
			}
		}(w)
	}
	close(start)
	wg.Wait()

	if len(duplicates) > 0 {
		t.Fatalf("tasks were claimed by more than one worker:\n  %s", strings.Join(duplicates, "\n  "))
	}
	if len(claimed) != taskCount {
		t.Errorf("claimed %d of %d tasks; the rest were never handed out", len(claimed), taskCount)
	}
}

// TestClaimIncrementsAttemptAndTakesALease covers the bookkeeping a claim must
// do, because a claim that forgets to increment attempts makes max_attempts
// unenforceable and a crash-looping task immortal.
func TestClaimIncrementsAttemptAndTakesALease(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	task := h.addTask(t, "solo", engine.StatusReady)
	now := h.clk.Now()

	claimed, err := h.queue.Claim(ctx, h.pool, "worker-a", 2*time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil {
		t.Fatal("nothing was claimed although a ready task exists")
	}
	if claimed.ID != task.ID {
		t.Fatalf("claimed %s, want %s", claimed.ID, task.ID)
	}
	if claimed.Status != engine.StatusClaimed {
		t.Errorf("status = %q, want claimed", claimed.Status)
	}
	if claimed.AttemptCount != 1 {
		t.Errorf("attempt_count = %d, want 1; without this, max_attempts is unenforceable", claimed.AttemptCount)
	}
	if !claimed.LeaseHeldBy("worker-a", now) {
		t.Error("the claiming worker does not hold the lease")
	}
	if claimed.LeaseHeldBy("worker-b", now) {
		t.Error("a different worker appears to hold the lease")
	}

	// The queue is now empty.
	again, err := h.queue.Claim(ctx, h.pool, "worker-b", time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if again != nil {
		t.Errorf("a claimed task was handed out again to %s", "worker-b")
	}
}

// TestCrashedWorkerTaskIsRecovered is bullet B28: kill a worker, verify the
// agent resumes. A crash is simulated by claiming and then never heartbeating,
// which is exactly what a killed process looks like from the database's side.
func TestCrashedWorkerTaskIsRecovered(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	task := h.addTask(t, "orphan", engine.StatusReady)

	claimed, err := h.queue.Claim(ctx, h.pool, "doomed-worker", 30*time.Second, h.clk.Now())
	if err != nil || claimed == nil {
		t.Fatalf("claim: %v", err)
	}

	// Nobody reaps yet: the lease is still live, so the task must stay hidden.
	reaped, err := h.queue.ReapExpiredLeases(ctx, h.pool, h.clk.Now(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(reaped) != 0 {
		t.Fatalf("reaped %d tasks while the lease was still valid", len(reaped))
	}

	// The worker dies. Time passes.
	h.clk.Advance(31 * time.Second)

	reaped, err = h.queue.ReapExpiredLeases(ctx, h.pool, h.clk.Now(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(reaped) != 1 || reaped[0].ID != task.ID {
		t.Fatalf("expected to reap the orphaned task, got %d", len(reaped))
	}
	if got := h.status(t, task.ID); got != engine.StatusReady {
		t.Errorf("status after reaping = %q, want ready", got)
	}

	// And a healthy worker picks it straight back up.
	recovered, err := h.queue.Claim(ctx, h.pool, "healthy-worker", time.Minute, h.clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	if recovered == nil || recovered.ID != task.ID {
		t.Fatal("the recovered task was not handed to a healthy worker")
	}
	if recovered.AttemptCount != 2 {
		t.Errorf("attempt_count = %d, want 2 (the crashed attempt must count)", recovered.AttemptCount)
	}
}

// TestCrashLoopingTaskEventuallyFails is the other half of recovery. A task that
// kills its worker every time — an out-of-memory input, a panic-inducing payload
// — must not cycle forever taking a worker down on each pass.
func TestCrashLoopingTaskEventuallyFails(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	task := h.addTask(t, "poison", engine.StatusReady)

	for attempt := 1; attempt <= task.MaxAttempts; attempt++ {
		claimed, err := h.queue.Claim(ctx, h.pool, fmt.Sprintf("worker-%d", attempt), 10*time.Second, h.clk.Now())
		if err != nil {
			t.Fatal(err)
		}
		if claimed == nil {
			t.Fatalf("attempt %d: nothing to claim, but the task should still be runnable", attempt)
		}
		h.clk.Advance(11 * time.Second) // the worker dies
		if _, err := h.queue.ReapExpiredLeases(ctx, h.pool, h.clk.Now(), 10); err != nil {
			t.Fatal(err)
		}
	}

	if got := h.status(t, task.ID); got != engine.StatusFailed {
		t.Fatalf("after %d crashed attempts the task is %q, want failed; "+
			"a task that crashes its worker every time would otherwise cycle forever", task.MaxAttempts, got)
	}
	final, err := h.repo.GetTask(ctx, h.pool, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.ErrorCode == "" {
		t.Error("a failed task must record why")
	}
	if final.EndedAt == nil {
		t.Error("a failed task must record when it ended")
	}
	// And it must not be handed out again.
	if again, _ := h.queue.Claim(ctx, h.pool, "worker-x", time.Minute, h.clk.Now()); again != nil {
		t.Error("a terminally failed task was handed out again")
	}
}

// TestHeartbeatRefusesAStolenLease is the guard that stops two workers from both
// believing they own a task. A worker whose lease lapsed and was reclaimed must
// be told, so it abandons its attempt rather than finishing it.
func TestHeartbeatRefusesAStolenLease(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	task := h.addTask(t, "contested", engine.StatusReady)

	if _, err := h.queue.Claim(ctx, h.pool, "worker-slow", 10*time.Second, h.clk.Now()); err != nil {
		t.Fatal(err)
	}
	// While the lease is live, heartbeating works.
	if err := h.queue.Heartbeat(ctx, h.pool, task.ID, "worker-slow", 10*time.Second, h.clk.Now()); err != nil {
		t.Fatalf("heartbeat on a live lease failed: %v", err)
	}
	// Somebody else's heartbeat must not.
	if err := h.queue.Heartbeat(ctx, h.pool, task.ID, "worker-other", 10*time.Second, h.clk.Now()); err == nil {
		t.Fatal("a worker that does not hold the lease was able to extend it")
	}

	// The slow worker stalls; the reaper reclaims; a new worker takes it.
	h.clk.Advance(11 * time.Second)
	if _, err := h.queue.ReapExpiredLeases(ctx, h.pool, h.clk.Now(), 10); err != nil {
		t.Fatal(err)
	}
	if _, err := h.queue.Claim(ctx, h.pool, "worker-fast", time.Minute, h.clk.Now()); err != nil {
		t.Fatal(err)
	}

	// The slow worker wakes up and tries to keep going. It must be refused.
	err := h.queue.Heartbeat(ctx, h.pool, task.ID, "worker-slow", 10*time.Second, h.clk.Now())
	if err == nil {
		t.Fatal("a worker whose lease was reclaimed could still extend it; two workers now believe they own the task")
	}
	if errs.CodeOf(err) != errs.CodeConflict {
		t.Errorf("code = %v, want CONFLICT", errs.CodeOf(err))
	}
	if !strings.Contains(err.Error(), "Abandon") {
		t.Errorf("the error should tell the worker to abandon its attempt: %v", err)
	}
}

// TestPausedGoalStopsProducingWork is bullet B10. Pause must take effect on the
// next claim, with no in-flight window — which is why the filter lives in the
// claim query rather than in the worker.
func TestPausedGoalStopsProducingWork(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.addTask(t, "a", engine.StatusReady)
	h.addTask(t, "b", engine.StatusReady)

	if _, err := h.pool.Exec(ctx, `update forge_goals set status='paused' where id=$1`, h.goalID); err != nil {
		t.Fatal(err)
	}
	claimed, err := h.queue.Claim(ctx, h.pool, "worker", time.Minute, h.clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	if claimed != nil {
		t.Fatalf("a paused goal handed out task %s", claimed.ID)
	}

	if _, err := h.pool.Exec(ctx, `update forge_goals set status='active' where id=$1`, h.goalID); err != nil {
		t.Fatal(err)
	}
	if claimed, err = h.queue.Claim(ctx, h.pool, "worker", time.Minute, h.clk.Now()); err != nil {
		t.Fatal(err)
	}
	if claimed == nil {
		t.Fatal("resuming the goal did not make work available again")
	}
}

// TestNotBeforeDefersWork is bullet B11: retry backoff, timers, and external
// waits all reduce to setting not_before.
func TestNotBeforeDefersWork(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	task := h.addTask(t, "deferred", engine.StatusReady)

	future := h.clk.Now().Add(10 * time.Minute)
	if _, err := h.pool.Exec(ctx, `update forge_tasks set not_before=$2 where id=$1`, task.ID, future); err != nil {
		t.Fatal(err)
	}

	claimed, err := h.queue.Claim(ctx, h.pool, "worker", time.Minute, h.clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	if claimed != nil {
		t.Fatal("a task scheduled for the future was claimed early")
	}

	h.clk.Advance(11 * time.Minute)
	if claimed, err = h.queue.Claim(ctx, h.pool, "worker", time.Minute, h.clk.Now()); err != nil {
		t.Fatal(err)
	}
	if claimed == nil {
		t.Fatal("the task did not become claimable after its scheduled time")
	}
}

func TestPriorityOrdersTheQueue(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	low := h.addTask(t, "low", engine.StatusReady)
	high := h.addTask(t, "high", engine.StatusReady)
	if _, err := h.pool.Exec(ctx, `update forge_tasks set priority=10 where id=$1`, high.ID); err != nil {
		t.Fatal(err)
	}

	first, err := h.queue.Claim(ctx, h.pool, "worker", time.Minute, h.clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	if first == nil || first.ID != high.ID {
		t.Fatalf("expected the higher-priority task first, got %v", first)
	}
	second, err := h.queue.Claim(ctx, h.pool, "worker", time.Minute, h.clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	if second == nil || second.ID != low.ID {
		t.Fatal("the lower-priority task did not come second")
	}
}

// ---------------------------------------------------------------------------
// dependency DAG
// ---------------------------------------------------------------------------

// finish drives a task to a terminal state through the real transition path,
// so the test exercises the same code the engine does rather than a shortcut.
func (h *harness) finish(t *testing.T, task *engine.Task, final engine.TaskStatus) {
	t.Helper()
	ctx := context.Background()
	cur, err := h.repo.GetTask(ctx, h.pool, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Walk a legal path to the requested terminal state.
	path := map[engine.TaskStatus][]engine.TaskStatus{
		engine.StatusSucceeded: {engine.StatusReady, engine.StatusClaimed, engine.StatusRunning, engine.StatusSucceeded},
		engine.StatusFailed:    {engine.StatusReady, engine.StatusClaimed, engine.StatusRunning, engine.StatusFailed},
		engine.StatusSkipped:   {engine.StatusSkipped},
		engine.StatusCancelled: {engine.StatusCancelled},
	}[final]

	for _, next := range path {
		if cur.Status == next {
			continue
		}
		if !engine.CanTransition(cur.Status, next) {
			continue
		}
		mut := engine.TaskMutation{}
		if next == engine.StatusFailed {
			mut.ErrorCode = "TEST_FAILURE"
			mut.ErrorDetail = "deliberate failure for a dependency test"
		}
		if err := h.repo.TransitionTask(ctx, h.pool, cur, next, h.clk.Now(), mut); err != nil {
			t.Fatalf("transitioning %s %s → %s: %v", task.ID, cur.Status, next, err)
		}
	}
	if got := h.status(t, task.ID); got != final {
		t.Fatalf("task %s ended %q, want %q", task.ID, got, final)
	}
}

// TestDependenciesGateReadiness is bullet B12: a blocked task waits until its
// prerequisites are complete.
func TestDependenciesGateReadiness(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	//   a ──┐
	//        ├──> c
	//   b ──┘
	a := h.addTask(t, "a", engine.StatusReady)
	b := h.addTask(t, "b", engine.StatusReady)
	c := h.addTask(t, "c", engine.StatusPending, a.ID, b.ID)

	// Nothing satisfied yet: c must not become ready.
	n, err := h.queue.PromoteReadyTasks(ctx, h.pool, h.goalID, h.clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("promoted %d tasks with unsatisfied dependencies", n)
	}
	if got := h.status(t, c.ID); got != engine.StatusPending {
		t.Fatalf("c is %q before its dependencies finished", got)
	}

	// One of two is not enough.
	h.finish(t, a, engine.StatusSucceeded)
	if _, err := h.queue.PromoteReadyTasks(ctx, h.pool, h.goalID, h.clk.Now()); err != nil {
		t.Fatal(err)
	}
	if got := h.status(t, c.ID); got != engine.StatusPending {
		t.Fatalf("c became %q with only one of two dependencies satisfied", got)
	}

	// Both satisfied: now it runs.
	h.finish(t, b, engine.StatusSucceeded)
	n, err = h.queue.PromoteReadyTasks(ctx, h.pool, h.goalID, h.clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("promoted %d tasks, want 1", n)
	}
	if got := h.status(t, c.ID); got != engine.StatusReady {
		t.Fatalf("c is %q after both dependencies succeeded, want ready", got)
	}
	claimed, err := h.queue.Claim(ctx, h.pool, "worker", time.Minute, h.clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil || claimed.ID != c.ID {
		t.Fatal("c did not become claimable")
	}
}

// TestFailurePropagatesWithoutLosingCompletedWork is bullet B17: a partial
// failure must not corrupt the workflow or discard work already done.
//
// The alternative — leaving downstream tasks 'pending' forever — is worse than
// it sounds. A goal full of permanently pending tasks looks identical to one
// still working, which is exactly the shape a silently stalled agent takes.
func TestFailurePropagatesWithoutLosingCompletedWork(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	//   done  (succeeds, must stay succeeded)
	//   bad ──> mid ──> leaf     (all three must end skipped/failed)
	//   other (independent, must stay runnable)
	done := h.addTask(t, "done", engine.StatusReady)
	bad := h.addTask(t, "bad", engine.StatusReady)
	mid := h.addTask(t, "mid", engine.StatusPending, bad.ID)
	leaf := h.addTask(t, "leaf", engine.StatusPending, mid.ID)
	other := h.addTask(t, "other", engine.StatusReady)

	h.finish(t, done, engine.StatusSucceeded)
	h.finish(t, bad, engine.StatusFailed)

	n, err := h.queue.SkipTasksBlockedByFailure(ctx, h.pool, h.goalID, h.clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	// Both mid and leaf: the propagation must be transitive, which is why it
	// loops until it stops changing anything.
	if n != 2 {
		t.Errorf("skipped %d tasks, want 2 (the failure must propagate transitively)", n)
	}
	if got := h.status(t, mid.ID); got != engine.StatusSkipped {
		t.Errorf("mid is %q, want skipped", got)
	}
	if got := h.status(t, leaf.ID); got != engine.StatusSkipped {
		t.Errorf("leaf is %q, want skipped; propagation stopped one level short", got)
	}

	// Completed work is untouched.
	if got := h.status(t, done.ID); got != engine.StatusSucceeded {
		t.Errorf("a completed task became %q when an unrelated task failed", got)
	}
	// Independent work is unaffected.
	if got := h.status(t, other.ID); got != engine.StatusReady {
		t.Errorf("an independent task became %q", got)
	}
	claimed, err := h.queue.Claim(ctx, h.pool, "worker", time.Minute, h.clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil || claimed.ID != other.ID {
		t.Fatal("independent work stopped being claimable after an unrelated failure")
	}

	// The skipped tasks say why.
	skipped, err := h.repo.GetTask(ctx, h.pool, leaf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if skipped.ErrorCode != engine.SkipReasonDependencyFailed {
		t.Errorf("error_code = %q, want %q", skipped.ErrorCode, engine.SkipReasonDependencyFailed)
	}
}

// TestSkippedDependencySatisfiesDownstream — a replan that removes work must not
// deadlock everything downstream of it. 'skipped' counts as satisfied; 'failed'
// does not.
func TestSkippedDependencySatisfiesDownstream(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	removed := h.addTask(t, "removed", engine.StatusPending)
	dependent := h.addTask(t, "dependent", engine.StatusPending, removed.ID)

	h.finish(t, removed, engine.StatusSkipped)

	if _, err := h.queue.PromoteReadyTasks(ctx, h.pool, h.goalID, h.clk.Now()); err != nil {
		t.Fatal(err)
	}
	if got := h.status(t, dependent.ID); got != engine.StatusReady {
		t.Errorf("dependent is %q after its dependency was skipped, want ready; "+
			"a replan that removes a task would otherwise deadlock everything after it", got)
	}
}

// TestDeepChainSettlesInOnePass proves the propagation loop actually converges
// rather than stopping at some fixed depth.
func TestDeepChainSettlesInOnePass(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	const depth = 20
	head := h.addTask(t, "chain00", engine.StatusReady)
	prev := head
	var chain []*engine.Task
	for i := 1; i < depth; i++ {
		next := h.addTask(t, fmt.Sprintf("chain%02d", i), engine.StatusPending, prev.ID)
		chain = append(chain, next)
		prev = next
	}

	h.finish(t, head, engine.StatusFailed)
	n, err := h.queue.SkipTasksBlockedByFailure(ctx, h.pool, h.goalID, h.clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(depth-1) {
		t.Errorf("skipped %d of %d downstream tasks; propagation did not reach the end of the chain", n, depth-1)
	}
	for _, task := range chain {
		if got := h.status(t, task.ID); got != engine.StatusSkipped {
			t.Errorf("%s is %q, want skipped", task.Title, got)
		}
	}
}

// TestSelfDependencyIsRefused — a task depending on itself is a deadlock the
// engine cannot detect at run time without a full cycle walk.
func TestSelfDependencyIsRefused(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	now := h.clk.Now()

	task := &engine.Task{
		ID: id.New(id.PrefixTask), GoalID: h.goalID, PlanID: h.planID,
		Title: "ouroboros", Instruction: "depend on myself",
		Status: engine.StatusPending, IdempotencyKey: "self", MaxAttempts: 3,
		NotBefore: now, Priority: 100, RiskTier: engine.RiskR1,
		CreatedAt: now, UpdatedAt: now,
	}
	err := h.repo.CreateTask(ctx, h.pool, task, []string{task.ID})
	if err == nil {
		t.Fatal("a self-dependency was accepted")
	}
	if errs.CodeOf(err) != errs.CodeInvariantViolated {
		t.Errorf("code = %v, want INVARIANT_VIOLATED", errs.CodeOf(err))
	}
}

// TestDuplicateIdempotencyKeyIsRefused is bullet B8 at the task level: the same
// logical work must not be created twice within a goal.
func TestDuplicateIdempotencyKeyIsRefused(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.addTask(t, "unique", engine.StatusPending)

	now := h.clk.Now()
	dup := &engine.Task{
		ID: id.New(id.PrefixTask), GoalID: h.goalID, PlanID: h.planID,
		Title: "unique-again", Instruction: "same work",
		Status: engine.StatusPending, IdempotencyKey: "key-unique", MaxAttempts: 3,
		NotBefore: now, Priority: 100, RiskTier: engine.RiskR1,
		CreatedAt: now, UpdatedAt: now,
	}
	err := h.repo.CreateTask(ctx, h.pool, dup, nil)
	if err == nil {
		t.Fatal("a duplicate idempotency key was accepted")
	}
	if errs.CodeOf(err) != errs.CodeConflict {
		t.Errorf("code = %v, want CONFLICT", errs.CodeOf(err))
	}
}

// ---------------------------------------------------------------------------
// transitions, checkpoints, timeline
// ---------------------------------------------------------------------------

// TestTransitionReleasesTheLease — a task leaving a lease-holding state must
// give the lease up, or it stays invisible to the queue forever.
func TestTransitionReleasesTheLease(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.addTask(t, "leased", engine.StatusReady)

	claimed, err := h.queue.Claim(ctx, h.pool, "worker", time.Minute, h.clk.Now())
	if err != nil || claimed == nil {
		t.Fatal(err)
	}
	if claimed.LeaseOwner == nil {
		t.Fatal("precondition: the claimed task should hold a lease")
	}

	if err := h.repo.TransitionTask(ctx, h.pool, claimed, engine.StatusRunning, h.clk.Now(), engine.TaskMutation{}); err != nil {
		t.Fatal(err)
	}
	running, err := h.repo.GetTask(ctx, h.pool, claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if running.LeaseOwner == nil {
		t.Error("running is a lease-holding state; the lease was dropped")
	}

	if err := h.repo.TransitionTask(ctx, h.pool, running, engine.StatusSucceeded, h.clk.Now(), engine.TaskMutation{}); err != nil {
		t.Fatal(err)
	}
	done, err := h.repo.GetTask(ctx, h.pool, claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.LeaseOwner != nil {
		t.Error("a terminal task still holds a lease; it would stay invisible to the queue forever")
	}
	if done.EndedAt == nil {
		t.Error("a terminal task must record when it ended")
	}
}

// TestTransitionRefusesAStaleRead is the compare-and-set guard. Between reading
// a task and writing it, the reaper may have moved the row; overwriting blindly
// would clobber another worker's state.
func TestTransitionRefusesAStaleRead(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	task := h.addTask(t, "raced", engine.StatusReady)

	claimed, err := h.queue.Claim(ctx, h.pool, "worker-a", 10*time.Second, h.clk.Now())
	if err != nil || claimed == nil {
		t.Fatal(err)
	}
	// Somebody else moves the row behind our back.
	h.clk.Advance(11 * time.Second)
	if _, err := h.queue.ReapExpiredLeases(ctx, h.pool, h.clk.Now(), 10); err != nil {
		t.Fatal(err)
	}

	// The stale worker tries to advance from the status it last saw.
	err = h.repo.TransitionTask(ctx, h.pool, claimed, engine.StatusRunning, h.clk.Now(), engine.TaskMutation{})
	if err == nil {
		t.Fatal("a stale read was allowed to overwrite the row")
	}
	if errs.CodeOf(err) != errs.CodeConflict {
		t.Errorf("code = %v, want CONFLICT", errs.CodeOf(err))
	}
	if got := h.status(t, task.ID); got != engine.StatusReady {
		t.Errorf("the reaped task is %q; the stale write took effect anyway", got)
	}
}

// TestCheckpointsAreAppendOnlyAndResumable is bullet B7. A crash halfway through
// overwriting one mutable state row leaves nothing to resume from; an append
// leaves the previous checkpoint intact, which is the entire point.
func TestCheckpointsAreAppendOnlyAndResumable(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	task := h.addTask(t, "checkpointed", engine.StatusReady)

	// No checkpoint yet is normal on a first attempt, and must not be an error.
	cp, err := h.repo.LatestCheckpoint(ctx, h.pool, task.ID)
	if err != nil {
		t.Fatalf("reading a checkpoint before any exists should not error: %v", err)
	}
	if cp != nil {
		t.Fatal("a checkpoint appeared before one was written")
	}

	for i := 1; i <= 5; i++ {
		state := []byte(fmt.Sprintf(`{"iteration":%d,"notes":"step %d"}`, i, i))
		saved, err := h.repo.SaveCheckpoint(ctx, h.pool, task.ID, engine.CheckpointIterationEnd, state, h.clk.Now())
		if err != nil {
			t.Fatalf("checkpoint %d: %v", i, err)
		}
		if saved.Seq != i {
			t.Errorf("checkpoint %d got seq %d", i, saved.Seq)
		}
		h.clk.Advance(time.Second)
	}

	latest, err := h.repo.LatestCheckpoint(ctx, h.pool, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Seq != 5 {
		t.Errorf("latest seq = %d, want 5", latest.Seq)
	}
	// Parse rather than substring-match: Postgres jsonb normalises key order and
	// whitespace, so `{"iteration":5,...}` comes back as `{"notes": ..., "iteration": 5}`.
	var state struct {
		Iteration int    `json:"iteration"`
		Notes     string `json:"notes"`
	}
	if err := json.Unmarshal(latest.State, &state); err != nil {
		t.Fatalf("checkpoint state is not valid JSON: %v (%s)", err, latest.State)
	}
	if state.Iteration != 5 {
		t.Errorf("latest checkpoint holds iteration %d, want 5", state.Iteration)
	}

	// Every earlier checkpoint is still there. That is what makes a resume
	// possible when the newest one turns out to be the corrupt one.
	var count int
	if err := h.pool.QueryRow(ctx,
		`select count(*) from forge_checkpoints where task_id=$1`, task.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 5 {
		t.Errorf("%d checkpoints survive, want 5; they are being overwritten rather than appended", count)
	}
}

func TestEmptyCheckpointIsRefused(t *testing.T) {
	h := newHarness(t)
	task := h.addTask(t, "empty-cp", engine.StatusReady)
	_, err := h.repo.SaveCheckpoint(context.Background(), h.pool, task.ID, "test", nil, h.clk.Now())
	if err == nil {
		t.Fatal("a checkpoint with no state was written; it cannot be resumed from")
	}
}

// TestTimelineIsOrderedAndAttributed is bullet B26. Every entry must say who
// caused it, or "Forge proposed" and "human approved" become indistinguishable.
func TestTimelineIsOrderedAndAttributed(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	task := h.addTask(t, "traced", engine.StatusReady)

	entries := []struct {
		kind  string
		actor engine.Actor
	}{
		{engine.EventTaskCreated, engine.ActorPlanner},
		{engine.EventTaskClaimed, engine.ActorExecutor},
		{engine.EventToolCalled, engine.ActorExecutor},
		{engine.EventApprovalRequested, engine.ActorExecutor},
		{engine.EventApprovalGranted, engine.ActorHuman},
		{engine.EventVerificationOK, engine.ActorVerifier},
	}
	for _, e := range entries {
		ev := &engine.Event{
			GoalID: h.goalID, TaskID: &task.ID, Kind: e.kind, Actor: e.actor,
			Summary: string(e.kind),
		}
		if err := h.repo.AppendEvent(ctx, h.pool, ev, h.clk.Now()); err != nil {
			t.Fatalf("appending %s: %v", e.kind, err)
		}
		h.clk.Advance(time.Second)
	}

	timeline, err := h.repo.Timeline(ctx, h.pool, h.goalID, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(timeline) != len(entries) {
		t.Fatalf("timeline has %d entries, want %d", len(timeline), len(entries))
	}
	// Newest first, strictly decreasing sequence.
	for i := 1; i < len(timeline); i++ {
		if timeline[i-1].Seq <= timeline[i].Seq {
			t.Fatalf("timeline is not ordered: seq %d followed by %d", timeline[i-1].Seq, timeline[i].Seq)
		}
	}
	// The human approval must be attributable to a human, not to the agent.
	var approval *engine.Event
	for _, e := range timeline {
		if e.Kind == engine.EventApprovalGranted {
			approval = e
		}
	}
	if approval == nil {
		t.Fatal("the approval event is missing")
	}
	if approval.Actor != engine.ActorHuman {
		t.Errorf("the approval is attributed to %q; 'the AI approved it' is never acceptable authority", approval.Actor)
	}
}

func TestUnattributedEventIsRefused(t *testing.T) {
	h := newHarness(t)
	err := h.repo.AppendEvent(context.Background(), h.pool, &engine.Event{
		GoalID: h.goalID, Kind: "some.thing", Actor: "nobody",
	}, h.clk.Now())
	if err == nil {
		t.Fatal("an event with an unrecognised actor was accepted")
	}
	if !strings.Contains(err.Error(), "who did this") {
		t.Errorf("the error should explain why attribution matters: %v", err)
	}
}
