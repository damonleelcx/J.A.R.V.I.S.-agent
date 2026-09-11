package cad_test

import (
	"context"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/cad"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// A scripted part reaches the FILE and the kernel-built MESH, not only the
// script runner.
//
// # Why this exists
//
// Every script test in this package stopped at RunScript: the sandbox, the
// manifest, the refusals, the suggestions — all of it proved that a script RUNS.
// Nothing built a document containing one. And the sidecar could not: the STEP
// import that makes a script's solid a part had been written into _apply (the
// feature function) after a return, so _shape refused every scripted part as
// "unsupported shape 'step'". The turn told the person the part built — its own
// check is RunScript — while the export and the viewport's built mesh left it
// out. See docs/bugfix/2026-09-10-scripted-parts-never-exported.md.
//
// So this asserts the path a person downloads and looks at, beside an ordinary
// part (the case where the rest of the assembly builds and the scripted part is
// quietly absent) and alone (the case where nothing builds).
func TestKernel_AScriptedPartIsExportedAndMeshed(t *testing.T) {
	k := scriptedKernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// A 10 × 20 × 30 block the script builds, centred on its own origin like every
	// build123d primitive, placed at y = 50 by the part — the same placement rule
	// as a box.
	scripted := geometry.Part{ID: "block", Name: "Scripted Block", Shape: "script",
		Script:   "result = Box(10, 20, 30)\n",
		Position: []float64{0, 50, 0}, Rotation: []float64{0, 0, 0}}
	plate := geometry.Part{ID: "plate", Name: "Plate", Shape: "box",
		Size:     map[string]float64{"width": 60, "height": 6, "depth": 60},
		Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}

	for _, tc := range []struct {
		name   string
		parts  []geometry.Part
		volume float64
		bounds [6]float64
	}{
		{"beside an ordinary part", []geometry.Part{plate, scripted}, 60*6*60 + 10*20*30,
			[6]float64{-30, -3, -30, 30, 60, 30}},
		{"on its own", []geometry.Part{scripted}, 10 * 20 * 30,
			[6]float64{-5, 40, -15, 5, 60, 15}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := geometry.Document{Name: "scripted", Units: "mm", Parts: tc.parts}

			step, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "step")
			if err != nil {
				t.Fatalf("the STEP export refused a document with a scripted part: %v", err)
			}
			if len(step.Skipped) != 0 || step.Parts != len(tc.parts) {
				t.Fatalf("the STEP file holds %d of %d parts, skipped %v. A scripted part that the "+
					"turn said built is missing from the file.", step.Parts, len(tc.parts), step.Skipped)
			}
			if math.Abs(step.Volume-tc.volume) > 1e-3 {
				t.Errorf("volume %.3f mm³, want %.3f", step.Volume, tc.volume)
			}
			for i := range tc.bounds {
				if math.Abs(step.Bounds[i]-tc.bounds[i]) > 1e-3 {
					t.Fatalf("bounds %v, want %v — the scripted part is not where the document "+
						"places it", step.Bounds, tc.bounds)
				}
			}
			if !strings.HasPrefix(string(step.STEP), "ISO-10303-21;") {
				t.Error("no STEP file")
			}

			mesh, err := k.BuildMesh(ctx, doc, geometry.Millimetre)
			if err != nil {
				t.Fatalf("the viewport's built mesh refused a document with a scripted part: %v", err)
			}
			meshed := false
			for _, m := range mesh.Mesh {
				if m.ID == "block" && len(m.Triangles) > 0 {
					meshed = true
				}
			}
			if !meshed || len(mesh.Skipped) != 0 {
				t.Errorf("the built mesh has no surface for the scripted part (skipped %v). The "+
					"viewport would draw the assembly without it.", mesh.Skipped)
			}
		})
	}
}

// A refused assembly names the parts it refused and why.
//
// The sidecar has always sent those reasons ("Gear Body: unsupported shape
// 'step'"), and this error dropped them — so the one sentence that named the
// defect above reached nobody, and the export said only "no part could be
// built".
func TestKernel_ARefusedAssemblyNamesWhatItRefused(t *testing.T) {
	k := scriptedKernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// A box with no width: Go passes a stated 0 through, and OCCT refuses it.
	doc := geometry.Document{Name: "flat", Units: "mm", Parts: []geometry.Part{{
		ID: "sliver", Name: "Sliver", Shape: "box",
		Size:     map[string]float64{"width": 0, "height": 6, "depth": 60},
		Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}}}
	_, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "step")
	if err == nil {
		t.Fatal("a zero-width box built; this fixture no longer makes the kernel refuse a part")
	}
	if !strings.Contains(err.Error(), "Sliver") {
		t.Errorf("the refusal does not name the part the kernel refused, so a reader cannot tell "+
			"which one or why:\n%v", err)
	}
}

// scriptedKernel is the real kernel with scripts switched on, which a scripted
// part needs before BuildDocument will run its script.
func scriptedKernel(t *testing.T) *cad.Kernel {
	t.Helper()
	python := os.Getenv("FORGE_CAD_PYTHON")
	if python == "" {
		t.Skip("FORGE_CAD_PYTHON is unset; skipping the CAD kernel tests")
	}
	k := cad.New(python, logx.Discard()).WithScripts(true)
	t.Cleanup(k.Close)
	return k
}
