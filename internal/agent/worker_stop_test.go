package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/agent"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/tools"
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
	for _, kind := range []string{engine.EventTaskRetrying, engine.EventTaskFailed, engine.EventTaskSucceeded} {
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

// ---------------------------------------------------------------------------
// what a stop leaves on the timeline and in the log
// ---------------------------------------------------------------------------

// What a stopped worker writes on its way out, wherever in its task the stop lands.
//
// # Why these exist
//
// The fences above read the task a stopped worker hands back. Two things they did not
// read were left over from that fix. A stop between opening an approval request and
// recording it left a request the timeline never mentions. And at the other stop
// points, writes still attempted on the cancelled context failed and were logged as
// the database being unavailable, while a few that record what had already happened (a
// spent token, a tool call that ran) were lost with them.
// docs/bugfix/2026-09-15-a-stopped-worker-lost-what-it-had-done-and-blamed-the-database.md

// stageModel answers the nth call with answer, so a fence can put the stop inside the
// executor, its tool loop or the verifier.
type stageModel struct {
	mu     sync.Mutex
	n      int
	answer func(ctx context.Context, n int, r llm.Request) (*llm.Response, error)
}

func (m *stageModel) Complete(ctx context.Context, r llm.Request) (*llm.Response, error) {
	m.mu.Lock()
	m.n++
	n := m.n
	m.mu.Unlock()
	return m.answer(ctx, n, r)
}

func (m *stageModel) ModelFor(llm.Role) string { return "stub" }

func answered(content string) *llm.Response {
	return &llm.Response{FinishReason: "stop", Usage: llm.Usage{TotalTokens: 10}, Content: content}
}

const (
	completedAnswer = `{"status":"completed","summary":"done","result":{},"evidence":["the stub said so"],"assumptions":[],"blocked_reason":""}`
	blockedAnswer   = `{"status":"blocked","summary":"cannot","result":{},"evidence":[],"assumptions":[],"blocked_reason":"a human has to say which"}`
)

// stageTool runs run, and counts how often it was asked to.
type stageTool struct {
	name string
	run  func(ctx context.Context) (*tools.Result, error)
	mu   sync.Mutex
	n    int
}

func (s *stageTool) Contract() tools.Contract {
	return tools.Contract{
		Name: s.name, Description: "a step in a fence",
		InputSchema:   json.RawMessage(`{"type":"object"}`),
		Capabilities:  []tools.Capability{tools.CapRead},
		RiskTier:      engine.RiskR0,
		Reversibility: tools.ReversibleNone,
		Timeout:       time.Minute, Idempotent: true, Available: true,
	}
}

func (s *stageTool) Run(ctx context.Context, _ tools.Invocation) (*tools.Result, error) {
	s.mu.Lock()
	s.n++
	s.mu.Unlock()
	return s.run(ctx)
}

func (s *stageTool) runs() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

func registryOf(t *testing.T, ts ...tools.Tool) *tools.Registry {
	t.Helper()
	r := tools.NewRegistry()
	for _, tool := range ts {
		if err := r.Register(tool); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func callTools(names ...string) *llm.Response {
	resp := &llm.Response{FinishReason: "tool_calls", Usage: llm.Usage{TotalTokens: 10}}
	for _, name := range names {
		resp.ToolCalls = append(resp.ToolCalls, llm.ToolCall{ID: "call-" + name, Type: "function",
			Function: llm.FunctionCall{Name: name, Arguments: `{}`}})
	}
	return resp
}

// stopMidTask runs one worker on the first of a fresh two-task chain at tier (gated:
// behind the approval gate) and returns once the worker has returned. arm is handed the
// worker's stop and builds the model, the tools and an extra log writer that between
// them decide where the stop lands.
//
// A harness of its own each time, because a worker claims any ready task in its schema,
// and the task an earlier stop handed back is one.
//
// The heartbeat beats every few milliseconds, so a stop also lands on a heartbeat in
// flight, the way it can on a real worker whose task outlives one beat.
func stopMidTask(t *testing.T, tier engine.RiskTier, gated bool,
	arm func(stop context.CancelFunc) (llm.Client, *tools.Registry, io.Writer)) (h *liveHarness, goal *engine.Goal, first *engine.Task, logged string) {
	t.Helper()
	h = newGateHarness(t)
	goal = h.createGoal(t, "Stopped part-way", "the worker is stopped part-way through its task",
		engine.AutonomySandboxExecute, tier)
	h.seedChain(t, goal)
	if _, err := h.pool.Exec(context.Background(),
		`update forge_tasks set risk_tier = $2, requires_approval = $3 where goal_id = $1`,
		goal.ID, string(tier), gated); err != nil {
		t.Fatal(err)
	}
	first, _ = h.chainTasks(t, goal.ID)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	model, registry, trip := arm(cancel)
	var logs lockedBuffer
	awaitStop(t, startWorker(ctx, h.stoppableWorker(t, model, registry, io.MultiWriter(&logs, trip))))
	return h, goal, first, logs.String()
}

// stoppableWorker is a worker whose executor and verifier both ask model, whose
// executor has registry's tools, and whose log is also written to out.
func (h *liveHarness) stoppableWorker(t *testing.T, model llm.Client, registry *tools.Registry, out io.Writer) *agent.Worker {
	t.Helper()
	cfg := h.cfg
	cfg.PollInterval = time.Hour
	cfg.LeaseHeartbeat = 2 * time.Millisecond
	log := logx.New(logx.Options{Output: io.MultiWriter(out, &testWriter{t}), Format: "text", Service: "worker-stop"})
	exec := agent.NewExecutor(model, registry, h.repo, h.budget, persona.DefaultCharacter(), clock.System{}, log, h.pool)
	return agent.NewWorker(agent.WorkerDeps{
		Pool: h.pool, Repo: h.repo, Queue: h.queue, Budget: h.budget,
		Assembler: h.assembler, Executor: exec, Verifier: agent.NewVerifier(model, persona.DefaultCharacter()),
		Config: cfg, WorkspaceRoot: h.workspaceRoot, Clock: clock.System{}, Log: log,
	})
}

// lockedBuffer is a bytes.Buffer the worker's goroutines may write while a test reads.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func requireNoDatabaseFailure(t *testing.T, logged string) {
	t.Helper()
	if strings.Contains(logged, "DATABASE_UNAVAILABLE") {
		t.Errorf("a stopped worker reported the database unavailable; it was only stopping:\n%s", logged)
	}
}

func (h *liveHarness) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := h.pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A worker stopped at any point in a task hands the task back and logs no database
// failure, and what it had already done before the stop (tokens spent, a tool call
// that ran) is recorded, while what the stop itself cut short is not.
func TestWorker_AWorkerStoppedAnywhereInATaskKeepsWhatItDidAndLogsNoDatabaseFailure(t *testing.T) {
	skipWithoutDatabase(t)

	t.Run("before its task starts", func(t *testing.T) {
		h, _, first, logged := stopMidTask(t, engine.RiskR1, false,
			func(stop context.CancelFunc) (llm.Client, *tools.Registry, io.Writer) {
				return &doneModel{}, tools.NewRegistry(), stopWhenLogged(logx.EventTaskCycleStarted, stop)
			})
		h.requireHandedBack(t, first.ID, first.AttemptCount)
		requireNoDatabaseFailure(t, logged)
	})

	t.Run("inside a model call", func(t *testing.T) {
		h, _, first, logged := stopMidTask(t, engine.RiskR1, false,
			func(stop context.CancelFunc) (llm.Client, *tools.Registry, io.Writer) {
				return &stageModel{answer: func(ctx context.Context, _ int, _ llm.Request) (*llm.Response, error) {
					stop()
					<-ctx.Done()
					return nil, ctx.Err()
				}}, tools.NewRegistry(), io.Discard
			})
		h.requireHandedBack(t, first.ID, first.AttemptCount)
		requireNoDatabaseFailure(t, logged)
	})

	for _, c := range []struct{ name, answer string }{
		{"as a model call answers that the task is done", completedAnswer},
		{"as a model call answers that the task is blocked", blockedAnswer},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, goal, first, logged := stopMidTask(t, engine.RiskR1, false,
				func(stop context.CancelFunc) (llm.Client, *tools.Registry, io.Writer) {
					return &stageModel{answer: func(context.Context, int, llm.Request) (*llm.Response, error) {
						stop()
						return answered(c.answer), nil
					}}, tools.NewRegistry(), io.Discard
				})
			h.requireHandedBack(t, first.ID, first.AttemptCount)
			requireNoDatabaseFailure(t, logged)
			if spent := h.count(t, `select tokens_spent::int from forge_goals where id = $1`, goal.ID); spent != 10 {
				t.Errorf("the goal has %d tokens spent; the call that answered as its worker stopped spent 10, "+
					"and a stop does not refund them", spent)
			}
		})
	}

	t.Run("as a tool call finishes", func(t *testing.T) {
		var second *stageTool
		h, _, first, logged := stopMidTask(t, engine.RiskR1, false,
			func(stop context.CancelFunc) (llm.Client, *tools.Registry, io.Writer) {
				finishing := &stageTool{name: "finishing", run: func(context.Context) (*tools.Result, error) {
					stop()
					return &tools.Result{Output: json.RawMessage(`{"made":"the part"}`), Raw: "made the part"}, nil
				}}
				second = &stageTool{name: "second", run: func(context.Context) (*tools.Result, error) {
					return &tools.Result{Output: json.RawMessage(`{}`)}, nil
				}}
				return &stageModel{answer: func(ctx context.Context, n int, _ llm.Request) (*llm.Response, error) {
					if n == 1 {
						return callTools("finishing", "second"), nil
					}
					return nil, ctx.Err()
				}}, registryOf(t, finishing, second), io.Discard
			})
		h.requireHandedBack(t, first.ID, first.AttemptCount)
		requireNoDatabaseFailure(t, logged)
		if n := h.count(t, `select count(*) from forge_tool_calls where task_id = $1 and tool_name = 'finishing' and status = 'succeeded'`,
			first.ID); n != 1 {
			t.Errorf("the ledger has %d succeeded records of the tool call that finished as its worker stopped, want 1; "+
				"without it the next attempt runs the call again", n)
		}
		if n := second.runs(); n != 0 {
			t.Errorf("a stopped worker went on to run the next tool call %d times; it should hand the task back instead", n)
		}
	})

	t.Run("inside a tool call", func(t *testing.T) {
		h, _, first, logged := stopMidTask(t, engine.RiskR1, false,
			func(stop context.CancelFunc) (llm.Client, *tools.Registry, io.Writer) {
				cut := &stageTool{name: "cut_short", run: func(ctx context.Context) (*tools.Result, error) {
					stop()
					<-ctx.Done()
					return nil, ctx.Err()
				}}
				return &stageModel{answer: func(ctx context.Context, n int, _ llm.Request) (*llm.Response, error) {
					if n == 1 {
						return callTools("cut_short"), nil
					}
					return nil, ctx.Err()
				}}, registryOf(t, cut), io.Discard
			})
		h.requireHandedBack(t, first.ID, first.AttemptCount)
		requireNoDatabaseFailure(t, logged)
		if n := h.count(t, `select count(*) from forge_tool_calls where task_id = $1`, first.ID); n != 0 {
			t.Errorf("the ledger holds %d records of a tool call the stop cut short; its idempotency key would then "+
				"refuse the record of the next attempt's call", n)
		}
		if n := h.count(t, `select count(*) from forge_checkpoints where task_id = $1`, first.ID); n != 0 {
			t.Errorf("%d checkpoints were saved from an iteration the stop cut short; the next attempt would resume "+
				"from a tool result that is only the stop", n)
		}
	})

	t.Run("during verification", func(t *testing.T) {
		h, _, first, logged := stopMidTask(t, engine.RiskR2, false,
			func(stop context.CancelFunc) (llm.Client, *tools.Registry, io.Writer) {
				return &stageModel{answer: func(ctx context.Context, _ int, r llm.Request) (*llm.Response, error) {
					if r.Role != llm.RoleVerifier {
						return answered(completedAnswer), nil
					}
					stop()
					<-ctx.Done()
					return nil, ctx.Err()
				}}, tools.NewRegistry(), io.Discard
			})
		h.requireHandedBack(t, first.ID, first.AttemptCount)
		requireNoDatabaseFailure(t, logged)
	})

	t.Run("at the approval gate", func(t *testing.T) {
		h, _, first, logged := stopMidTask(t, engine.RiskR2, true,
			func(stop context.CancelFunc) (llm.Client, *tools.Registry, io.Writer) {
				return &doneModel{}, tools.NewRegistry(), stopWhenLogged(logx.EventApprovalOpened, stop)
			})
		h.requireHandedBack(t, first.ID, first.AttemptCount)
		requireNoDatabaseFailure(t, logged)
	})
}

// A stop that lands after an approval request is written but before approval.requested
// is recorded leaves the approvals table and the timeline agreeing: each request the
// task has is on its timeline, once.
func TestWorker_AStopBetweenOpeningAnApprovalRequestAndRecordingItLeavesTheTimelineAndTheApprovalsAgreeing(t *testing.T) {
	skipWithoutDatabase(t)
	h := newGateHarness(t)
	goal := h.createGoal(t, "Stopped opening the gate", "the worker is stopped between the request and its event",
		engine.AutonomySandboxExecute, engine.RiskR2)
	h.seedChain(t, goal)
	if _, err := h.pool.Exec(context.Background(),
		`update forge_tasks set risk_tier = 'r2', requires_approval = true where goal_id = $1`, goal.ID); err != nil {
		t.Fatal(err)
	}
	first, _ := h.chainTasks(t, goal.ID)
	model := &doneModel{}

	var logs lockedBuffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := h.stoppableWorker(t, model, tools.NewRegistry(), &logs)
	w.OnApprovalRowWrittenForTest(cancel)
	awaitStop(t, startWorker(ctx, w))
	h.requireHandedBack(t, first.ID, first.AttemptCount)
	requireNoDatabaseFailure(t, logs.String())

	next, stopNext := context.WithCancel(context.Background())
	defer stopNext()
	nextDone := startWorker(next, h.stubWorker(model, time.Hour))
	deadline := time.Now().Add(time.Minute)
	for {
		if a, _ := h.chainTasks(t, goal.ID); a.Status == engine.StatusAwaitingApproval {
			break
		}
		if time.Now().After(deadline) {
			stopNext()
			<-nextDone
			t.Fatal("the next worker did not park the handed-back task at the gate within a minute")
		}
		time.Sleep(20 * time.Millisecond)
	}
	stopNext()
	awaitStop(t, nextDone)

	requests := h.count(t, `select count(*) from forge_approvals where task_id = $1`, first.ID)
	recorded := h.taskEvents(t, first.ID, engine.EventApprovalRequested)
	if requests != 1 || recorded != 1 {
		t.Errorf("the task has %d approval requests and its timeline records %d %s events; want one of each, "+
			"so that what is waiting for a human is on the record of what happened", requests, recorded,
			engine.EventApprovalRequested)
	}
}

// A worker stopped as its poll recovers a crashed worker's task still records the
// recovery on the timeline, and logs no database failure for the claim the stop then
// refused.
func TestWorker_AWorkerStoppedAsItRecoversACrashedWorkersTaskRecordsTheRecoveryAndLogsNoDatabaseFailure(t *testing.T) {
	skipWithoutDatabase(t)
	h := newGateHarness(t)
	goal := h.createGoal(t, "Recovered as the stop arrived", "the recovering worker is stopped as it recovers the task",
		engine.AutonomySandboxExecute, engine.RiskR1)
	first, _ := h.seedChain(t, goal)
	if _, err := h.pool.Exec(context.Background(), `
		update forge_tasks
		   set status = 'running', lease_owner = 'crashed-host/1/gone', lease_expires_at = now() - interval '1 minute',
		       attempt_count = 1, started_at = now() - interval '5 minutes'
		 where id = $1`, first.ID); err != nil {
		t.Fatal(err)
	}

	var logs lockedBuffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	trip := stopWhenLogged(logx.EventTaskLeaseExpired, cancel)
	awaitStop(t, startWorker(ctx, h.stoppableWorker(t, &doneModel{}, tools.NewRegistry(), io.MultiWriter(&logs, trip))))

	requireNoDatabaseFailure(t, logs.String())
	if n := h.taskEvents(t, first.ID, engine.EventTaskLeaseExpired); n != 1 {
		t.Errorf("the timeline has %d %s events for a task the stopped worker recovered, want 1; the recovery "+
			"happened before the stop, and a stop does not undo the record of it", n, engine.EventTaskLeaseExpired)
	}
}
