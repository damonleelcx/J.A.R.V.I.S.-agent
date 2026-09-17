package agent_test

import (
	"context"
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

// A running task says it is still at work at least every agent.AliveEvery (PRD NFR-02:
// long jobs report progress at least every 10 s).
//
// Found running a 10-step build through the real forged and forge-worker against a
// stand-in model that took 12 s a step: nothing a client could read changed for
// 11.7-13.1 s at a time, because the only writes during a step were its start and its
// end, and the lease heartbeat that also writes the row defaults to every 20 s.
// docs/bugfix/2026-09-17-a-long-build-step-showed-no-progress-for-its-whole-length.md

func TestAliveEvery_LeavesAClientPollingAtItAFreshStampInsideTenSeconds(t *testing.T) {
	// A stamp at most AliveEvery old, read by a client polling every AliveEvery, is at
	// most twice that old when it is seen.
	if 2*agent.AliveEvery > 10*time.Second {
		t.Fatalf("agent.AliveEvery is %s: a client polling at that rate can see a stamp %s old, "+
			"past NFR-02's 10 s", agent.AliveEvery, 2*agent.AliveEvery)
	}
}

// heldModel blocks every call until released, and says when the first one arrived.
type heldModel struct {
	once    sync.Once
	arrived chan struct{}
	release chan struct{}
}

func (m *heldModel) Complete(ctx context.Context, _ llm.Request) (*llm.Response, error) {
	m.once.Do(func() { close(m.arrived) })
	select {
	case <-m.release:
		return answered(completedAnswer), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (m *heldModel) ModelFor(llm.Role) string { return "stub" }

func TestWorker_ARunningTaskIsStampedAliveWhileItsModelCallRunsWhateverTheLeaseHeartbeat(t *testing.T) {
	skipWithoutDatabase(t)
	h := newGateHarness(t)
	goal := h.createGoal(t, "Seen alive", "a task whose model call takes a while",
		engine.AutonomySandboxExecute, engine.RiskR1)
	first, _ := h.seedChain(t, goal)

	cfg := h.cfg
	cfg.LeaseHeartbeat = time.Hour // tuned for leases; must not decide how often a person hears
	cfg.PollInterval = 10 * time.Millisecond
	model := &heldModel{arrived: make(chan struct{}), release: make(chan struct{})}
	exec := agent.NewExecutor(model, tools.NewRegistry(), h.repo, h.budget, persona.DefaultCharacter(),
		clock.System{}, h.log, h.pool)
	w := agent.NewWorker(agent.WorkerDeps{
		Pool: h.pool, Repo: h.repo, Queue: h.queue, Budget: h.budget,
		Assembler: h.assembler, Executor: exec, Verifier: agent.NewVerifier(model, persona.DefaultCharacter()),
		Config: cfg, WorkspaceRoot: h.workspaceRoot, Clock: clock.System{}, Log: h.log,
	})
	w.SetAliveEveryForTest(40 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = w.Run(ctx) }()
	defer func() { cancel(); <-done }()

	select {
	case <-model.arrived:
	case <-time.After(30 * time.Second):
		t.Fatal("the worker never reached the model call")
	}
	// Inside the call now, and nothing else writes the task until it returns.
	stamps := map[time.Time]bool{}
	for end := time.Now().Add(600 * time.Millisecond); time.Now().Before(end); {
		var at time.Time
		if err := h.pool.QueryRow(context.Background(),
			`select updated_at from forge_tasks where id = $1 and status = 'running'`, first.ID).Scan(&at); err != nil {
			t.Fatalf("the task is not running inside its model call: %v", err)
		}
		stamps[at] = true
		time.Sleep(10 * time.Millisecond)
	}
	close(model.release)

	// 600 ms at one stamp every 40 ms is ~15; 4 leaves room for a loaded machine and is
	// still impossible for a row written only when the step starts and ends.
	if len(stamps) < 4 {
		t.Fatalf("the running task's row was stamped %d time(s) in 600 ms of its model call. "+
			"A client polling it sees nothing change for the whole call, however long: NFR-02 "+
			"asks for progress at least every 10 s", len(stamps))
	}
}
