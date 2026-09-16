package cad_test

import (
	"context"
	"runtime"
	"strings"
	"testing"
)

// OpenCASCADE's own thread pool runs on one thread inside a script, however many
// processors the machine has.
//
// build123d runs every boolean in parallel, and OpenCASCADE sizes the pool it runs
// them on to the processors ONLINE. The four thread variables script.go sets do
// not reach that pool, so on a 16-CPU machine a script still started 16 threads and
// was stopped by the address-space cap or the CPU limit.
// docs/bugfix/2026-09-15-scripts-still-failed-on-machines-with-many-cores.md
func TestScript_RunsOpenCascadeOnOneThread(t *testing.T) {
	k := scriptKernel(t)
	if runtime.NumCPU() < 2 {
		t.Skip("one CPU sizes the pool to one thread whatever the sandbox does, so this could not fail here")
	}
	res, err := k.RunScript(context.Background(), "result = Box(20, 20, 20) - Cylinder(4, 30)", nil)
	if err != nil && strings.Contains(err.Error(), "No module named 'build123d'") {
		t.Skip("build123d is not installed, so a script cannot build anything")
	}
	if err != nil {
		t.Fatalf("a boolean in a script did not run: %v", err)
	}
	if res.KernelThreads != 1 {
		t.Errorf("OpenCASCADE's pool had %d thread(s) while the script ran; it must be 1, "+
			"or a machine with many cores exhausts the script's memory and CPU limits", res.KernelThreads)
	}
}
