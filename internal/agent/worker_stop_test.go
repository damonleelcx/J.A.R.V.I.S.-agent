package agent_test

import (
	"bytes"
	"context"
	"io"
	"strings"
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

	// ‼️ Only the bookkeeping's own events are read. On this branch runTask's writes
	// after the stop (the retry it records for the cancelled model call) still run on
	// the cancelled context; that is a separate path, named in the bugfix doc.
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
