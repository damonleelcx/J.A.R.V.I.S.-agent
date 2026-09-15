package agent_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/agent"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// What a stopping worker does after the task it was running, through the real
// worker, queue and executor on Postgres.
//
// # Why these exist
//
// A graceful stop cancels the worker's context while a task runs. What the end of a
// task sets moving — releasing the tasks it left waiting, settling the goal — ran on
// that cancelled context, failed at once, and was logged as DATABASE_UNAVAILABLE.
// The release fences above read rows, never logs, and none of them stops a worker,
// so they stayed green while every live graceful stop reported a database outage.
// Found stopping a live forge-worker with a console Ctrl-Break (#104).
// docs/bugfix/2026-09-15-a-stopping-worker-reported-its-own-stop-as-a-database-outage.md

// blockingModel answers nothing: it says it was reached and waits for its call to
// be cancelled, the way a real model call is when the worker is stopped under it.
type blockingModel struct{ reached chan struct{} }

func (m *blockingModel) Complete(ctx context.Context, _ llm.Request) (*llm.Response, error) {
	select {
	case m.reached <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (m *blockingModel) ModelFor(llm.Role) string { return "stub" }

// loggingWorker is h.stubWorker with everything it logs also written to out, so a
// test can read what an operator would.
func (h *liveHarness) loggingWorker(t *testing.T, model llm.Client, poll time.Duration, out io.Writer) *agent.Worker {
	t.Helper()
	copied := *h
	copied.log = logx.New(logx.Options{Output: io.MultiWriter(out, &testWriter{t}), Format: "text", Service: "worker-stop"})
	return copied.stubWorker(model, poll)
}

// A worker stopped in the middle of a task does not report the database unavailable
// from what the task's end sets moving: the only thing that happened is that it was
// asked to stop.
func TestWorker_AWorkerStoppedMidTaskDoesNotReportItsBookkeepingAsADatabaseFailure(t *testing.T) {
	skipWithoutDatabase(t)
	h := newGateHarness(t)
	goal := h.createGoal(t, "Stopped mid-task", "the worker is stopped while the model is thinking",
		engine.AutonomySandboxExecute, engine.RiskR1)
	h.seedChain(t, goal)
	model := &blockingModel{reached: make(chan struct{}, 1)}

	var logs bytes.Buffer
	w := h.loggingWorker(t, model, time.Hour, &logs)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = w.Run(ctx) }()

	select {
	case <-model.reached:
	case <-time.After(time.Minute):
		cancel()
		<-done
		t.Fatal("the worker never reached the model")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Minute):
		t.Fatal("the stopped worker did not return")
	}

	// Only the bookkeeping's own events are read here. What happens to the task itself
	// is fenced below, by TestWorker_AWorkerStoppedInsideAModelCallHandsItsTaskBackAtOnce.
	out := logs.String()
	for _, said := range []string{string(logx.EventTaskReleaseFailed), string(logx.EventGoalSettleFailed)} {
		if strings.Contains(out, said) {
			t.Errorf("a worker stopped mid-task logged %s; nothing failed, it was asked to stop", said)
		}
	}
}

// A task that finished in the instant the stop arrived still releases the task
// waiting on it and, once it was the last, settles its goal — on the context the
// stop has already cancelled, which is what Run is holding when it gets there.
func TestWorker_ATaskFinishedAsTheStopArrivesStillReleasesItsDependentsAndSettlesItsGoal(t *testing.T) {
	skipWithoutDatabase(t)
	h := newGateHarness(t)
	goal := h.createGoal(t, "Finished as the stop arrived", "each task ends in the instant its worker is stopped",
		engine.AutonomySandboxExecute, engine.RiskR1)
	first, _ := h.seedChain(t, goal)

	var logs bytes.Buffer
	w := h.loggingWorker(t, &doneModel{}, time.Hour, &logs)
	stopped, cancel := context.WithCancel(context.Background())
	cancel()

	// The first task finished, exactly as runTask leaves it, and the stop arrived.
	h.markTerminal(t, first, engine.StatusSucceeded)
	w.AfterTaskForTest(stopped, goal.ID)
	if _, second := h.chainTasks(t, goal.ID); second.Status != engine.StatusReady {
		t.Errorf("the second task is %s after the worker that finished the first stopped; it should be ready "+
			"for the next worker now, not when some worker's idle poll finds it", second.Status)
	}

	// The last task finished, and the stop arrived again.
	_, second := h.chainTasks(t, goal.ID)
	h.markTerminal(t, second, engine.StatusSucceeded)
	w.AfterTaskForTest(stopped, goal.ID)
	if status, _, _ := h.goalOutcome(t, goal.ID); status != string(engine.GoalSucceeded) {
		t.Errorf("the goal is %s after its last task finished as the worker stopped; it should have settled "+
			"succeeded then, not on some worker's idle sweep", status)
	}

	if out := logs.String(); strings.Contains(out, "DATABASE_UNAVAILABLE") {
		t.Errorf("a stopping worker reported the database unavailable; it was only stopping:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// the task itself, handed back
// ---------------------------------------------------------------------------

// What a stopped worker does with the task it was holding, through the real worker,
// queue and executor on Postgres.
//
// # Why these exist
//
// Run has always promised that a stop hands the current task back rather than leaving
// it to its lease. On the executor path it never did: the cancelled model call reached
// retryOrFail, whose writes ran on the cancelled context and failed, and the task
// stayed running under its lease until the reaper recovered it minutes later, with the
// stop counted as one of its attempts. The fences above read the bookkeeping after the
// task; none of them read the task.
// docs/bugfix/2026-09-15-a-stopped-worker-left-its-task-to-run-out-its-lease.md

// stopOnLog stops the worker the moment it logs a line containing event, so a stop can
// land where no model call is waiting: before the task starts, or at the approval gate.
// slog writes a record in one Write, under the handler's lock, so the stop lands
// before the worker's next statement.
type stopOnLog struct {
	event []byte
	stop  context.CancelFunc
	once  sync.Once
}

func (s *stopOnLog) Write(p []byte) (int, error) {
	if bytes.Contains(p, s.event) {
		s.once.Do(s.stop)
	}
	return len(p), nil
}

func stopWhenLogged(event logx.Event, stop context.CancelFunc) *stopOnLog {
	return &stopOnLog{event: []byte("msg=" + string(event)), stop: stop}
}

// failingModel fails every call retryably, the way an endpoint that refuses the
// connection does, and counts them.
type failingModel struct{ doneModel }

func (m *failingModel) Complete(ctx context.Context, r llm.Request) (*llm.Response, error) {
	_, _ = m.doneModel.Complete(ctx, r)
	return nil, errors.New("the model endpoint refused the connection")
}

// startWorker runs w until ctx is cancelled; the channel closes when Run returns.
func startWorker(ctx context.Context, w *agent.Worker) <-chan struct{} {
	done := make(chan struct{})
	go func() { defer close(done); _ = w.Run(ctx) }()
	return done
}

func awaitStop(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Minute):
		t.Fatal("the worker was not stopped within a minute, or did not return")
	}
}

func (h *liveHarness) taskEvents(t *testing.T, taskID, kind string) int {
	t.Helper()
	var n int
	if err := h.pool.QueryRow(context.Background(),
		`select count(*) from forge_events where task_id = $1 and kind = $2`, taskID, kind).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// requireHandedBack reads, the moment its worker has returned, a task the worker was
// holding when it was stopped: ready and runnable now, held by nobody, the stopped
// attempt not counted, nothing recorded as a failure, and the timeline saying why.
func (h *liveHarness) requireHandedBack(t *testing.T, taskID string, attemptsBefore int) {
	t.Helper()
	task, err := h.repo.GetTask(context.Background(), h.pool, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != engine.StatusReady {
		t.Errorf("the task a stopped worker was holding is %s; it should be ready for the next worker now, "+
			"not when its lease runs out", task.Status)
	}
	if task.LeaseOwner != nil {
		t.Errorf("the task is still leased to %s until %v after its worker stopped", *task.LeaseOwner, task.LeaseExpiresAt)
	}
	if task.NotBefore.After(time.Now()) {
		t.Errorf("the task is hidden from the queue until %s; a stop is not a failure to back off from", task.NotBefore)
	}
	if task.AttemptCount != attemptsBefore {
		t.Errorf("the task has used %d attempts, %d before the stopped worker claimed it; a stop is not an attempt",
			task.AttemptCount, attemptsBefore)
	}
	if task.ErrorCode != "" {
		t.Errorf("the task records the error %s: %s; its worker was only stopped", task.ErrorCode, task.ErrorDetail)
	}
	if n := h.taskEvents(t, taskID, engine.EventTaskHandedBack); n != 1 {
		t.Errorf("the timeline has %d %s events for the task, want 1 saying it was handed back", n, engine.EventTaskHandedBack)
	}
	for _, kind := range []string{engine.EventTaskRetrying, engine.EventTaskFailed} {
		if n := h.taskEvents(t, taskID, kind); n != 0 {
			t.Errorf("the timeline records %d %s for a task whose worker was only stopped", n, kind)
		}
	}
}

// A worker stopped inside a model call hands the task back at once, and the next
// worker runs it on what is still its first attempt.
func TestWorker_AWorkerStoppedInsideAModelCallHandsItsTaskBackAtOnce(t *testing.T) {
	skipWithoutDatabase(t)
	h := newGateHarness(t)
	goal := h.createGoal(t, "Handed back from the model", "the worker is stopped while the model is thinking",
		engine.AutonomySandboxExecute, engine.RiskR1)
	first, _ := h.seedChain(t, goal)
	model := &blockingModel{reached: make(chan struct{}, 1)}

	var logs bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := startWorker(ctx, h.loggingWorker(t, model, time.Hour, &logs))
	select {
	case <-model.reached:
	case <-time.After(time.Minute):
		cancel()
		<-done
		t.Fatal("the worker never reached the model")
	}
	cancel()
	awaitStop(t, done)

	h.requireHandedBack(t, first.ID, first.AttemptCount)
	out := logs.String()
	if strings.Contains(out, "DATABASE_UNAVAILABLE") {
		t.Errorf("a worker stopped mid-task reported the database unavailable; it was only stopping:\n%s", out)
	}
	if strings.Contains(out, string(logx.EventTaskRetryingLog)) {
		t.Errorf("a worker stopped mid-task logged %s; a stop is not a failed attempt", logx.EventTaskRetryingLog)
	}

	// The idle poll is an hour away, so only a task that is claimable now can run.
	restarted := &doneModel{}
	if status := h.runUntilSettled(t, h.stubWorker(restarted, time.Hour), goal.ID); status != string(engine.GoalSucceeded) {
		t.Fatalf("the goal ended %s after a new worker took the handed-back task", status)
	}
	if n := restarted.calls(); n != 2 {
		t.Errorf("the new worker asked the model %d times; each task should have run once", n)
	}
	if a, _ := h.chainTasks(t, goal.ID); a.AttemptCount != 1 {
		t.Errorf("the handed-back task finished on attempt %d; the stopped attempt should not have counted", a.AttemptCount)
	}
}

// A worker stopped after claiming a task but before starting it hands it back
// unstarted, and nothing asks the model.
func TestWorker_AWorkerStoppedBeforeItsTaskStartsHandsItBackUnstarted(t *testing.T) {
	skipWithoutDatabase(t)
	h := newGateHarness(t)
	goal := h.createGoal(t, "Handed back unstarted", "the worker is stopped between the claim and the start",
		engine.AutonomySandboxExecute, engine.RiskR1)
	first, _ := h.seedChain(t, goal)
	model := &doneModel{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := startWorker(ctx, h.loggingWorker(t, model, time.Hour, stopWhenLogged(logx.EventTaskCycleStarted, cancel)))
	awaitStop(t, done)

	h.requireHandedBack(t, first.ID, first.AttemptCount)
	if n := model.calls(); n != 0 {
		t.Errorf("the model was asked %d times by a task stopped before it started", n)
	}
	if a, _ := h.chainTasks(t, goal.ID); a.StartedAt != nil {
		t.Errorf("the task records starting at %s; it was stopped before it started", a.StartedAt)
	}
}

// A worker stopped at the approval gate, after opening the request and before parking
// the task on it, hands the task back; the next worker parks it on that same request
// rather than opening a second, and the model is never asked.
func TestWorker_AWorkerStoppedAtTheApprovalGateHandsItsTaskBackAndTheGateIsOpenedOnce(t *testing.T) {
	skipWithoutDatabase(t)
	h := newGateHarness(t)
	goal := h.createGoal(t, "Handed back at the gate", "the worker is stopped as it asks for approval",
		engine.AutonomySandboxExecute, engine.RiskR2)
	h.seedChain(t, goal)
	if _, err := h.pool.Exec(context.Background(),
		`update forge_tasks set risk_tier = 'r2', requires_approval = true where goal_id = $1`, goal.ID); err != nil {
		t.Fatal(err)
	}
	first, _ := h.chainTasks(t, goal.ID)
	model := &doneModel{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := startWorker(ctx, h.loggingWorker(t, model, time.Hour, stopWhenLogged(logx.EventApprovalOpened, cancel)))
	awaitStop(t, done)
	h.requireHandedBack(t, first.ID, first.AttemptCount)

	next, stopNext := context.WithCancel(context.Background())
	defer stopNext()
	nextDone := startWorker(next, h.stubWorker(model, time.Hour))
	deadline := time.Now().Add(time.Minute)
	for {
		a, _ := h.chainTasks(t, goal.ID)
		if a.Status == engine.StatusAwaitingApproval {
			break
		}
		if time.Now().After(deadline) {
			stopNext()
			<-nextDone
			t.Fatalf("the next worker left the handed-back task %s; it should have parked it at the gate", a.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
	stopNext()
	awaitStop(t, nextDone)

	var requests int
	if err := h.pool.QueryRow(context.Background(),
		`select count(*) from forge_approvals where task_id = $1`, first.ID).Scan(&requests); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Errorf("the task has %d approval requests; the stopped worker's one should have been reused", requests)
	}
	if a, _ := h.chainTasks(t, goal.ID); a.Status != engine.StatusAwaitingApproval || a.LeaseOwner != nil {
		t.Errorf("a task parked at the gate is %s (leased: %t) after its worker stopped; a gate waiting on a "+
			"human is not the worker's to hand back", a.Status, a.LeaseOwner != nil)
	}
	if n := h.taskEvents(t, first.ID, engine.EventTaskHandedBack); n != 1 {
		t.Errorf("the timeline has %d hand-back events; only the first stop was holding the task", n)
	}
	if n := model.calls(); n != 0 {
		t.Errorf("the model was asked %d times by a task that was never approved", n)
	}
}

// A task that genuinely fails, with nobody stopping its worker, is still retried with
// its attempt counted, and failed when none remain. The hand-back is for stops only.
func TestWorker_ATaskThatFailsWhileItsWorkerRunsIsStillRetriedAndThenFailed(t *testing.T) {
	skipWithoutDatabase(t)
	h := newGateHarness(t)
	goal := h.createGoal(t, "Fails for real", "the model endpoint refuses every call",
		engine.AutonomySandboxExecute, engine.RiskR1)
	first, _ := h.seedChain(t, goal)
	if _, err := h.pool.Exec(context.Background(),
		`update forge_tasks set max_attempts = 2 where id = $1`, first.ID); err != nil {
		t.Fatal(err)
	}
	model := &failingModel{}

	if status := h.runUntilSettled(t, h.stubWorker(model, 20*time.Millisecond), goal.ID); status != string(engine.GoalFailed) {
		t.Fatalf("a goal whose only model refuses every call ended %s", status)
	}
	a, b := h.chainTasks(t, goal.ID)
	if a.Status != engine.StatusFailed || b.Status != engine.StatusSkipped {
		t.Errorf("tasks ended %s and %s, want failed and skipped", a.Status, b.Status)
	}
	if a.AttemptCount != 2 {
		t.Errorf("the failed task used %d attempts, want both of its 2 counted", a.AttemptCount)
	}
	if n := model.calls(); n != 2 {
		t.Errorf("the model was asked %d times; want once per attempt", n)
	}
	for kind, want := range map[string]int{
		engine.EventTaskRetrying: 1, engine.EventTaskFailed: 1, engine.EventTaskHandedBack: 0,
	} {
		if n := h.taskEvents(t, a.ID, kind); n != want {
			t.Errorf("the timeline has %d %s events for the failing task, want %d", n, kind, want)
		}
	}
}
