package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/cad"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/blob"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// The off-node STEP export job, against real Postgres.
//
// Every property here is a property of rows — a lease that lapses, a task the
// reaper hands back, a transaction that did or did not commit — so the database is
// real. The kernel and the store are not: what is under test is what the job does
// with their answers, and the real kernel and MinIO are fenced end to end in
// internal/httpapi (TestExports_RequestJobWorkerBlobStatusAndDownloadAgree).

// memObjects is the bucket two stores can share.
type memObjects struct {
	mu      sync.Mutex
	objects map[blob.Key][]byte
}

// memStore is a content-addressed store whose Put can be held at the door.
type memStore struct {
	bucket *memObjects
	// gate, when set, holds every Put until it is closed.
	gate chan struct{}
	// entered is closed when the first Put arrives, before the gate.
	entered chan struct{}
	once    sync.Once
}

func newMemStore(bucket *memObjects) *memStore { return &memStore{bucket: bucket} }

func (s *memStore) Available() bool { return true }

func (s *memStore) Put(ctx context.Context, r io.ReadSeeker) (blob.Key, error) {
	if s.entered != nil {
		s.once.Do(func() { close(s.entered) })
	}
	if s.gate != nil {
		select {
		case <-s.gate:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	k, _, _ := blob.KeyOf(bytes.NewReader(b))
	s.bucket.mu.Lock()
	defer s.bucket.mu.Unlock()
	s.bucket.objects[k] = b
	return k, nil
}

func (s *memStore) Get(_ context.Context, k blob.Key) (io.ReadCloser, error) {
	s.bucket.mu.Lock()
	defer s.bucket.mu.Unlock()
	b, ok := s.bucket.objects[k]
	if !ok {
		return nil, errs.New("memStore.Get", errs.CodeNotFound)
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (s *memStore) Has(_ context.Context, k blob.Key) (bool, error) {
	s.bucket.mu.Lock()
	defer s.bucket.mu.Unlock()
	_, ok := s.bucket.objects[k]
	return ok, nil
}

// signedKernel writes a "file" that says which worker wrote it.
type signedKernel struct {
	by    string
	mu    sync.Mutex
	calls int
}

func (k *signedKernel) Available() bool { return true }

func (k *signedKernel) ExportSTEPJob(_ context.Context, doc geometry.Document, _ geometry.Unit) (*cad.Build, error) {
	k.mu.Lock()
	k.calls++
	n := k.calls
	k.mu.Unlock()
	return &cad.Build{Parts: len(doc.Parts),
		STEP: []byte(fmt.Sprintf("ISO-10303-21;\n/* %s by worker %s, call %d */\nEND-ISO-10303-21;\n", doc.Name, k.by, n))}, nil
}

func exportEngineConfig() config.EngineConfig {
	return config.EngineConfig{
		WorkerConcurrency: 1, LeaseDuration: time.Minute,
		// An hour: a worker in these tests never heartbeats, so a "killed" worker
		// is one that simply stops, and its lease lapses on the clock the test
		// hands the reaper.
		LeaseHeartbeat: time.Hour, PollInterval: 20 * time.Millisecond, MaxAttemptsPerTask: 3,
		BackoffBase: 10 * time.Millisecond, BackoffMax: 50 * time.Millisecond,
		MaxIterationsPerTask: 12, MaxToolCallsPerIteration: 8,
		MaxTokensPerGoal: 1_000_000, MaxWallClockPerGoal: time.Hour, MaxTaskDepth: 3, MaxTasksPerGoal: 50,
	}
}

func exportWorker(t *testing.T, h *buildHarness, x *StepExporter) *Worker {
	t.Helper()
	cfg := exportEngineConfig()
	return NewWorker(WorkerDeps{Pool: h.pool, Repo: h.repo, Queue: h.queue, Budget: engine.NewBudgetGuard(cfg),
		Assembler: NewAssembler(h.repo, h.queue), Exports: x, Config: cfg,
		WorkspaceRoot: t.TempDir(), Clock: clock.System{}, Log: logx.Discard()})
}

func savePlate(t *testing.T, h *buildHarness, name string) *geometry.Variant {
	t.Helper()
	v, err := h.geo.Save(context.Background(), geometry.NewVariant{
		ProjectID: h.project, InitiatorID: h.userID, Agent: workspace.AgentConverse, Generator: "test",
		Inputs: map[string]any{"message": name},
		Document: geometry.Document{Name: name, Units: "mm",
			NotVerified: []string{"nothing about this plate has been checked"},
			Parts: []geometry.Part{{ID: "plate", Name: "Plate", Shape: "box",
				Size:     map[string]float64{"width": 60, "height": 5, "depth": 60},
				Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// claimAndRun claims the next task for w and runs it, returning when it has.
func claimAndRun(t *testing.T, h *buildHarness, w *Worker, wantTask string) {
	t.Helper()
	task, err := h.queue.Claim(context.Background(), h.pool, w.ID, time.Minute, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if task == nil || task.ID != wantTask {
		t.Fatalf("claimed %v, want task %s", task, wantTask)
	}
	w.runTask(context.Background(), task)
}

func exportOf(t *testing.T, h *buildHarness, exportID string) *Export {
	t.Helper()
	e, err := NewStepExports(h.pool, clock.System{}, logx.Discard()).Find(context.Background(), exportID)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func waitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(20 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// A worker that dies mid-export — after the kernel wrote the file, while it is
// being stored — leaves the export queued to run again, never succeeded. The worker
// that reclaims it finishes it. And when the dead worker turns out to be only
// slow, and finishes while the reclaiming worker is running the same task, it
// records nothing: the file on the export is the reclaiming worker's.
func TestStepExport_AWorkerKilledMidExportLeavesItRetryableAndNeverReportedSucceeded(t *testing.T) {
	h := newBuildHarness(t)
	ctx := context.Background()
	v := savePlate(t, h, "plate")
	e, created, err := NewStepExports(h.pool, clock.System{}, logx.Discard()).Request(ctx, v, "step", h.userID)
	if err != nil || !created {
		t.Fatalf("request: created=%v err=%v", created, err)
	}
	bucket := &memObjects{objects: map[blob.Key][]byte{}}

	// Worker A stops inside Put: the kernel is done, nothing is recorded. The last
	// moment a worker can die before the job has anything to show for it.
	storeA := &memStore{bucket: bucket, gate: make(chan struct{}), entered: make(chan struct{})}
	a := exportWorker(t, h, NewStepExporter(&signedKernel{by: "A"}, storeA, h.geo, h.repo, h.pool, clock.System{}, logx.Discard()))
	aDone := make(chan struct{})
	taskA, err := h.queue.Claim(ctx, h.pool, a.ID, time.Minute, time.Now())
	if err != nil || taskA == nil || taskA.ID != e.TaskID {
		t.Fatalf("worker A claimed %v (%v), want %s", taskA, err, e.TaskID)
	}
	go func() { defer close(aDone); a.runTask(ctx, taskA) }()
	waitClosed(t, storeA.entered, "worker A to reach the store")

	if got := exportOf(t, h, e.ID); got.Status != ExportRunning || got.BlobKey != "" {
		t.Fatalf("while worker A holds it the export is %s with key %q, want running with none", got.Status, got.BlobKey)
	}

	// A is gone: no release, no heartbeat. Its lease lapses and the reaper, which
	// is what notices a dead worker, hands the task back.
	reaped, err := h.queue.ReapExpiredLeases(ctx, h.pool, time.Now().Add(2*time.Minute), 10)
	if err != nil || len(reaped) != 1 {
		t.Fatalf("the reaper returned %d task(s) (%v), want A's one", len(reaped), err)
	}
	got := exportOf(t, h, e.ID)
	if got.Status != ExportQueued || got.BlobKey != "" {
		t.Fatalf("after A's lease lapsed the export is %s with key %q, want queued with none", got.Status, got.BlobKey)
	}
	if !strings.Contains(got.Reason, "attempt 1") {
		t.Errorf("a queued-again export does not say an attempt did not finish: %q", got.Reason)
	}

	// Worker B reclaims it, and is held in the store too, so that A can wake while
	// B is running the very same task.
	storeB := &memStore{bucket: bucket, gate: make(chan struct{}), entered: make(chan struct{})}
	b := exportWorker(t, h, NewStepExporter(&signedKernel{by: "B"}, storeB, h.geo, h.repo, h.pool, clock.System{}, logx.Discard()))
	bDone := make(chan struct{})
	taskB, err := h.queue.Claim(ctx, h.pool, b.ID, time.Minute, time.Now())
	if err != nil || taskB == nil || taskB.ID != e.TaskID {
		t.Fatalf("worker B claimed %v (%v), want %s", taskB, err, e.TaskID)
	}
	go func() { defer close(bDone); b.runTask(ctx, taskB) }()
	waitClosed(t, storeB.entered, "worker B to reach the store")

	close(storeA.gate)
	waitClosed(t, aDone, "worker A to finish")
	if got := exportOf(t, h, e.ID); got.Status != ExportRunning || got.BlobKey != "" {
		t.Fatalf("worker A, whose lease had lapsed, finished while B ran the task and the export became %s "+
			"with key %q; it must stay running with no file", got.Status, got.BlobKey)
	}

	close(storeB.gate)
	waitClosed(t, bDone, "worker B to finish")
	got = exportOf(t, h, e.ID)
	if got.Status != ExportSucceeded {
		t.Fatalf("after B finished the export is %s (%s), want succeeded", got.Status, got.Reason)
	}
	file, err := storeB.Get(ctx, got.BlobKey)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(file)
	if !bytes.Contains(body, []byte("by worker B")) {
		t.Errorf("the export's file is not the one worker B stored: %q", body)
	}
	if got.SizeBytes != int64(len(body)) || got.Attempts != 2 {
		t.Errorf("the export records %d bytes after %d attempt(s); want %d bytes after 2", got.SizeBytes, got.Attempts, len(body))
	}
}

// Asked for twice, one job. Asked for again once it has failed, a fresh one.
func TestStepExport_AskingTwiceReturnsTheExportAlreadyQueuedOrDoneAndAFailedOneIsAskedAfresh(t *testing.T) {
	h := newBuildHarness(t)
	ctx := context.Background()
	jobs := NewStepExports(h.pool, clock.System{}, logx.Discard())
	v := savePlate(t, h, "plate")

	first, created, err := jobs.Request(ctx, v, "step", h.userID)
	if err != nil || !created {
		t.Fatalf("first request: created=%v err=%v", created, err)
	}
	again, created, err := jobs.Request(ctx, v, "STEP", h.userID)
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("second request while queued: created=%v id=%s err=%v; want export %s back", created, again.ID, err, first.ID)
	}

	bucket := &memObjects{objects: map[blob.Key][]byte{}}
	claimAndRun(t, h, exportWorker(t, h, NewStepExporter(&signedKernel{by: "A"}, newMemStore(bucket), h.geo, h.repo,
		h.pool, clock.System{}, logx.Discard())), first.TaskID)
	done, created, err := jobs.Request(ctx, v, "step", h.userID)
	if err != nil || created || done.ID != first.ID || done.Status != ExportSucceeded {
		t.Fatalf("request once done: created=%v id=%s status=%s err=%v; want export %s, succeeded", created, done.ID,
			done.Status, err, first.ID)
	}
	var goals int
	if err := h.pool.QueryRow(ctx, `select count(*) from forge_goals where project_id = $1`, h.project).Scan(&goals); err != nil {
		t.Fatal(err)
	}
	if goals != 1 {
		t.Errorf("three requests for one version made %d goal(s), want 1", goals)
	}

	// A second version, whose job fails: asked again, it is queued afresh.
	w := savePlate(t, h, "other plate")
	failing, _, err := jobs.Request(ctx, w, "step", h.userID)
	if err != nil {
		t.Fatal(err)
	}
	claimAndRun(t, h, exportWorker(t, h, NewStepExporter(&signedKernel{by: "A"}, nil, h.geo, h.repo,
		h.pool, clock.System{}, logx.Discard())), failing.TaskID)
	if got := exportOf(t, h, failing.ID); got.Status != ExportFailed {
		t.Fatalf("an export with no store is %s, want failed", got.Status)
	}
	fresh, created, err := jobs.Request(ctx, w, "step", h.userID)
	if err != nil || !created || fresh.ID == failing.ID || fresh.Status != ExportQueued {
		t.Fatalf("asking again after a failure: created=%v id=%s status=%s err=%v; want a new queued export",
			created, fresh.ID, fresh.Status, err)
	}
	if got := exportOf(t, h, failing.ID); got.Status != ExportFailed {
		t.Errorf("the failed export became %s when it was superseded; it stays what it was", got.Status)
	}
}

// A worker with no bucket fails the job at once, naming the setting — before the
// kernel spends anything, and never as a success with nothing stored.
func TestStepExport_AWorkerWithNoBlobStoreFailsTheJobNamingTheSetting(t *testing.T) {
	h := newBuildHarness(t)
	ctx := context.Background()
	v := savePlate(t, h, "plate")
	e, _, err := NewStepExports(h.pool, clock.System{}, logx.Discard()).Request(ctx, v, "step", h.userID)
	if err != nil {
		t.Fatal(err)
	}
	k := &signedKernel{by: "A"}
	claimAndRun(t, h, exportWorker(t, h, NewStepExporter(k, blob.Disabled(), h.geo, h.repo, h.pool, clock.System{},
		logx.Discard())), e.TaskID)

	got := exportOf(t, h, e.ID)
	if got.Status != ExportFailed || !strings.Contains(got.Reason, "FORGE_BLOB_BUCKET") {
		t.Fatalf("the export is %s (%q); want failed, naming FORGE_BLOB_BUCKET", got.Status, got.Reason)
	}
	if got.Attempts != 1 {
		t.Errorf("a missing bucket was tried %d times; it will not appear on a retry", got.Attempts)
	}
	if k.calls != 0 {
		t.Errorf("the kernel was asked %d time(s) for a file with nowhere to go", k.calls)
	}
}

// Above the job's ceiling, and a million parts with it, refused before any goal
// exists, naming the ceiling.
func TestStepExport_ADesignAboveTheCeilingIsRefusedBeforeAnyJobExists(t *testing.T) {
	h := newBuildHarness(t)
	ctx := context.Background()
	jobs := NewStepExports(h.pool, clock.System{}, logx.Discard())

	for _, tc := range []struct {
		name          string
		parts, copies int
	}{{"one over the ceiling", 226, 400}, {"one million", 1954, 512}} {
		doc := geometry.Document{Name: tc.name, Units: "mm"}
		for i := 0; i < tc.parts; i++ {
			doc.Parts = append(doc.Parts, geometry.Part{ID: fmt.Sprintf("p%d", i), Name: "Rivet", Shape: "box",
				Size:   map[string]float64{"width": 4, "height": 4, "depth": 4},
				Repeat: &geometry.Repeat{Count: tc.copies, Offset: []float64{10, 0, 0}}})
		}
		// Not stored: the storage door refuses a million, and the job's refusal
		// must not depend on the door having been set higher.
		v := &geometry.Variant{VersionID: "ver_unstored", ProjectID: h.project, Name: tc.name,
			Units: geometry.Unit("mm"), Document: doc}
		_, _, err := jobs.Request(ctx, v, "step", h.userID)
		if errs.CodeOf(err) != errs.CodeValidationFailed || !strings.Contains(err.Error(), "90000") {
			t.Errorf("%s: answered %v; want VALIDATION_FAILED naming the 90000 ceiling", tc.name, err)
		}
	}
	var goals int
	if err := h.pool.QueryRow(ctx, `select count(*) from forge_goals where project_id = $1`, h.project).Scan(&goals); err != nil {
		t.Fatal(err)
	}
	if goals != 0 {
		t.Errorf("refused exports left %d goal(s) behind", goals)
	}
}
