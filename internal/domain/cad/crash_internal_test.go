package cad

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

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

	doc := geometry.Document{
		Name: "block", Units: "mm",
		Parts: []geometry.Part{{ID: "b", Shape: "box",
			Size:     map[string]float64{"width": 10, "height": 10, "depth": 10},
			Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}},
	}
	if _, err := k.BuildDocument(ctx, doc, geometry.Millimetre, ""); err != nil {
		t.Fatal(err)
	}

	// Kill it the way an OOM or a stray pkill would: the process goes, and the
	// kernel is not told. started stays true and the pipes stay open handles to
	// nothing.
	k.mu.Lock()
	if k.cmd == nil || k.cmd.Process == nil {
		k.mu.Unlock()
		t.Fatal("the kernel has no process to kill; it was not kept warm")
	}
	_ = k.cmd.Process.Kill()
	_, _ = k.cmd.Process.Wait()
	k.mu.Unlock()
	time.Sleep(50 * time.Millisecond)

	got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
	if err != nil {
		t.Fatalf("the kernel did not recover from a killed process: %v", err)
	}
	if got.Parts != 1 {
		t.Errorf("built %d parts after the crash, want 1", got.Parts)
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
