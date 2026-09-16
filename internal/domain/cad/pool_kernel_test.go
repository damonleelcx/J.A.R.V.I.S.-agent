package cad_test

import (
	"context"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/cad"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// A pool of kernel processes builds side by side. Phase 4, stage K3 of
// docs/plan-2026-09-13-millions-of-parts.md: "concurrent builds do not serialise
// (timed test)".
//
// # Why a ratio and not a number
//
// Two builds at once against one alone, on the same machine in the same minute:
// the machine's speed cancels out. One process serialises them, so together takes
// about twice as long as alone; a pool of two runs them side by side, so together
// takes about as long. 1.6× sits between the two with room for a busy runner.
func TestKernel_ConcurrentBuildsDoNotSerialise(t *testing.T) {
	python := os.Getenv("FORGE_CAD_PYTHON")
	if python == "" {
		t.Skip("FORGE_CAD_PYTHON is unset; skipping the CAD kernel tests")
	}
	if runtime.NumCPU() < 2 {
		t.Skip("one CPU cannot run two builds side by side, so this would measure the machine, not the pool")
	}
	k := cad.New(python, logx.Discard()).WithPool(2)
	t.Cleanup(k.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// 2,048 studs: about a second of shapes and interference check, long enough
	// that a process start or a scheduler hiccup is noise beside it.
	doc := studGrid(256, "x")
	build := func() {
		got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
		if err != nil {
			t.Error(err)
			return
		}
		if got.Parts != 2048 {
			t.Errorf("%d parts, want 2048", got.Parts)
		}
	}
	both := func() time.Duration {
		start := time.Now()
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); build() }()
		go func() { defer wg.Done(); build() }()
		wg.Wait()
		return time.Since(start)
	}

	// Warm: two builds at once start both processes, so neither timing below
	// includes build123d's import.
	both()
	start := time.Now()
	build()
	alone := time.Since(start)
	together := both()
	if t.Failed() {
		return
	}
	ratio := float64(together) / float64(alone)
	t.Logf("one build alone %v; two at once %v (%.2f×)", alone, together, ratio)
	if ratio > 1.6 {
		t.Errorf("two builds at once took %.2f× as long as one alone (%v against %v): "+
			"a pool of two runs them side by side (~1×), one process serialises them (~2×)",
			ratio, together, alone)
	}
}
