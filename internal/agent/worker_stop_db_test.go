package agent

import (
	"bytes"
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

// What a stopping worker logs when the task it stops in is a BUILD step, against real
// Postgres.
//
// # Why this exists beside worker_stop_test.go
//
// A graceful stop cancels the worker's context while a task runs. What came after the
// task — releasing the tasks it left waiting, settling the goal — ran on the cancelled
// context, failed at once, and was logged as DATABASE_UNAVAILABLE. main's fences for
// that (TestWorker_AWorkerStoppedMidTaskDoesNotReportItsBookkeepingAsADatabaseFailure,
// TestWorker_ATaskFinishedAsTheStopArrivesStillReleasesItsDependentsAndSettlesItsGoal)
// stop a worker on the executor path. This one stops it inside runBuildStep, where the
// defect was found live, and reads the whole log for DATABASE_UNAVAILABLE rather than
// only the two bookkeeping events.
// docs/bugfix/2026-09-15-a-stopping-worker-reported-its-own-stop-as-a-database-outage.md

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
