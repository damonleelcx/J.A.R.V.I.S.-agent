package cad_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// An assembly tree reaches the real kernel as the parts it places.
// docs/plan-2026-09-13-millions-of-parts.md, stage D1b.
func TestKernel_BuildsAnAssemblyTree(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	block := geometry.Part{ID: "block", Name: "Block", Shape: "box",
		Size: map[string]float64{"width": 10, "height": 10, "depth": 10}}
	doc := geometry.Document{Name: "tree", Units: "mm",
		Definitions: []geometry.Part{block},
		Assemblies: []geometry.Assembly{
			{ID: "row", Children: []geometry.Child{
				{ID: "a", Ref: "block"},
				{ID: "b", Ref: "block", Position: []float64{30, 0, 0}, Rotation: []float64{0, 0, 45}},
				{ID: "pair", Ref: "pair", Position: []float64{0, 40, 0}},
			}},
			{ID: "pair", Children: []geometry.Child{
				{ID: "x", Ref: "block"},
				{ID: "y", Ref: "block", Position: []float64{20, 0, 0}},
			}},
		},
		Root: "row"}

	got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Parts != 4 || len(got.Skipped) != 0 {
		t.Fatalf("built %d of 4 placed blocks, skipped %v, notes %v", got.Parts, got.Skipped, got.Inferred)
	}
	if math.Abs(got.Volume-4000) > 1e-3 {
		t.Errorf("volume %.3f mm³, want 4000", got.Volume)
	}
	if len(got.Interferences) != 0 {
		t.Errorf("blocks placed apart report overlaps: %+v", got.Interferences)
	}
	// The nested pair sits at y = 40 ± 5; the rotated block reaches x = 30 + 5√2.
	if math.Abs(got.Bounds[4]-45) > 1e-6 || math.Abs(got.Bounds[3]-(30+5*math.Sqrt2)) > 1e-6 {
		t.Errorf("bounds %v: the placements did not compose", got.Bounds)
	}
}
