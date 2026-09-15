package cad

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

func block() geometry.Document {
	return geometry.Document{
		Name: "block", Units: "mm",
		Parts: []geometry.Part{{ID: "b", Shape: "box",
			Size:     map[string]float64{"width": 10, "height": 10, "depth": 10},
			Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}},
	}
}

// The retry path, exercised by an actual crash.
//
// # Why this test is inside the package
//
// A kernel whose process dies BETWEEN requests still believes it is started, so
// the next build writes into a closed pipe and fails. Recovering from that is
// what the single retry in BuildDocument is for, and the only way to reach the
// state is to kill the process without telling the kernel — which needs the
// unexported handle.
//
// The external test that looked like it covered this called Close, which resets
// the kernel's own state so the next build just starts a fresh process. A drill
// removed the retry outright and that test stayed green. This one goes red.
func TestRetryAfterTheProcessDies(t *testing.T) {
	python := envCADPython(t)
	k := New(python, logx.Discard())
	defer k.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	if _, err := k.BuildDocument(ctx, block(), geometry.Millimetre, ""); err != nil {
		t.Fatal(err)
	}

	// Kill it the way an OOM or a stray pkill would: the process goes, and the
	// kernel is not told. started stays true and the pipes stay open handles to
	// nothing.
	p := k.all[0].process()
	if p == nil {
		t.Fatal("the kernel has no process to kill; it was not kept warm")
	}
	_ = p.Kill()
	_, _ = p.Wait()
	time.Sleep(50 * time.Millisecond)

	got, err := k.BuildDocument(ctx, block(), geometry.Millimetre, "")
	if err != nil {
		t.Fatalf("the kernel did not recover from a killed process: %v", err)
	}
	if got.Parts != 1 {
		t.Errorf("built %d parts after the crash, want 1", got.Parts)
	}
}

// A killed process in a pool is replaced in ITS slot, whichever slot that is.
// Phase 4, stage K3's acceptance: "a killed sidecar is replaced".
//
// Both processes are started by hand and put back in slot order. The pool is a
// FIFO channel, so the two builds after the kill take slot 0 and then slot 1:
// each one meets a dead process and must replace it, rather than the first
// replacement being reused twice while slot 1 stays dead.
func TestRetryAfterEveryProcessInThePoolDies(t *testing.T) {
	python := envCADPython(t)
	k := New(python, logx.Discard()).WithPool(2)
	defer k.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var held []*sidecar
	for i := 0; i < 2; i++ {
		s, err := k.acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.start(ctx); err != nil {
			t.Fatal(err)
		}
		held = append(held, s)
	}
	dead := map[*sidecar]int{}
	for _, s := range held {
		p := s.process()
		dead[s] = p.Pid
		_ = p.Kill()
		_, _ = p.Wait()
	}
	for _, s := range held {
		k.release(s)
	}
	time.Sleep(50 * time.Millisecond)

	for i := 0; i < 2; i++ {
		got, err := k.BuildDocument(ctx, block(), geometry.Millimetre, "")
		if err != nil {
			t.Fatalf("build %d after every process died: %v", i+1, err)
		}
		if got.Parts != 1 {
			t.Errorf("build %d: %d parts, want 1", i+1, got.Parts)
		}
	}
	for _, s := range held {
		if p := s.process(); p == nil || p.Pid == dead[s] {
			t.Errorf("slot %d still holds the process that was killed", s.slot)
		}
	}
}

func envCADPython(t *testing.T) string {
	t.Helper()
	python := os.Getenv("FORGE_CAD_PYTHON")
	if python == "" {
		t.Skip("FORGE_CAD_PYTHON is unset; skipping the CAD kernel tests")
	}
	return python
}
