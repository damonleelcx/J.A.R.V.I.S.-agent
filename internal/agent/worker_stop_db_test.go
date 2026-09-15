package agent

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// What a stopping worker does after the task it was running, against real Postgres.
//
// # Why these exist
//
// A graceful stop cancels the worker's context while a task runs. The task is handed
// back on a context of its own, and TestBuildGoal_AStoppedWorkerHandsItsStepBack holds
// that. What came AFTER the hand-back — releasing the tasks the finished one left
// waiting, settling the goal — ran on the cancelled context, failed at once, and was
// logged as DATABASE_UNAVAILABLE. Nothing read the log, so the fence stayed green while
// every live graceful stop reported a database outage.
// docs/bugfix/2026-09-15-a-stopping-worker-reported-its-own-stop-as-a-database-outage.md

// A worker told to stop in the instant a task finished still releases the tasks that
// task left waiting, and does not report the database unavailable while doing it.
func TestWorker_AStoppingWorkerStillReleasesWhatItsLastTaskLeftWaiting(t *testing.T) {
	h := newBuildHarness(t)
	goal := h.goal(t, nil)
	stub := &goalStub{tokens: 1, replies: []string{threeSteps}}
	h.plan(t, goal, stub)

	// Step 1 finished, exactly as runTask leaves it.
	first := h.tasks(t, goal.ID)[0]
	for _, next := range []engine.TaskStatus{engine.StatusClaimed, engine.StatusRunning, engine.StatusSucceeded} {
		if err := h.repo.TransitionTask(context.Background(), h.pool, first, next, time.Now().UTC(), engine.TaskMutation{}); err != nil {
			t.Fatal(err)
		}
	}

	var logs lockedBuffer
	w := h.workerLogging(t, stub, &logs)
	// The stop arrived as the task ended: Run reaches afterTask holding a cancelled ctx.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w.afterTask(ctx, goal.ID)

	if second := h.tasks(t, goal.ID)[1]; second.Status != engine.StatusReady {
		t.Errorf("step 2 is %s after the worker that finished step 1 stopped; it should be ready for the next "+
			"worker now, not when some worker's idle poll finds it", second.Status)
	}
	if out := logs.String(); strings.Contains(out, "DATABASE_UNAVAILABLE") {
		t.Errorf("a stopping worker reported the database unavailable; it was only stopping:\n%s", out)
	}
}

// A worker stopped in the middle of a step says nothing about the database: the only
// thing that happened is that it was asked to stop.
func TestBuildGoal_AStoppedWorkerDoesNotReportTheDatabaseUnavailable(t *testing.T) {
	h := newBuildHarness(t)
	goal := h.goal(t, nil)
	stub := &goalStub{tokens: 1, blockAt: 3, reached: make(chan struct{}),
		replies: []string{threeSteps, wholeDoc("chassis", "Chassis")}}
	h.plan(t, goal, stub)

	var logs lockedBuffer
	stop := start(h.workerLogging(t, stub, &logs))
	select {
	case <-stub.reached:
	case <-time.After(60 * time.Second):
		stop()
		t.Fatal("the worker never reached step 2")
	}
	stop()

	out := logs.String()
	for _, said := range []string{"forge.task.release_failed", "forge.goal.settle_failed", "DATABASE_UNAVAILABLE"} {
		if strings.Contains(out, said) {
			t.Errorf("a worker stopped mid-step logged %s; nothing failed, it was asked to stop:\n%s", said, out)
		}
	}
	if step2 := h.tasks(t, goal.ID)[1]; step2.Status != engine.StatusReady {
		t.Errorf("step 2 is %s after its worker stopped; it should have been handed back", step2.Status)
	}
}

// workerLogging is h.worker with its warnings written to out, so a test can read what
// an operator would.
func (h *buildHarness) workerLogging(t *testing.T, stub llm.Client, out io.Writer) *Worker {
	log := logx.New(logx.Options{Output: io.MultiWriter(out, testLog{t}), Format: "text",
		Service: "build-goal", Level: slog.LevelWarn})
	return NewWorker(WorkerDeps{Pool: h.pool, Repo: h.repo, Queue: h.queue, Budget: h.budget,
		Assembler: NewAssembler(h.repo, h.queue), Builds: h.steps(stub),
		Config: h.cfg, WorkspaceRoot: t.TempDir(), Clock: clock.System{}, Log: log})
}

// lockedBuffer is a bytes.Buffer the worker's goroutines can write while a test waits.
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
