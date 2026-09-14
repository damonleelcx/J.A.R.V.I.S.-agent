package cad_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// Patterned children reach the real kernel as the copies they place.
// Phase 1, stage D1c-2 of docs/plan-2026-09-13-millions-of-parts.md.
func TestKernel_BuildsAGridAndACircleOfCopies(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cube := geometry.Part{ID: "cube", Name: "Cube", Shape: "box",
		Size: map[string]float64{"width": 10, "height": 10, "depth": 10}}
	doc := geometry.Document{Name: "copies", Units: "mm", Definitions: []geometry.Part{cube},
		Assemblies: []geometry.Assembly{{ID: "root", Children: []geometry.Child{
			{ID: "cell", Ref: "cube", Pattern: &geometry.Pattern{Kind: "grid", Rows: 2, Columns: 3,
				RowOffset: []float64{0, 0, 20}, ColumnOffset: []float64{20, 0, 0}}},
			{ID: "ring", Ref: "cube", Position: []float64{0, 100, 60},
				Pattern: &geometry.Pattern{Kind: "polar", Count: 4, About: "y"}},
		}}}, Root: "root"}
	got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Parts != 10 || math.Abs(got.Volume-10000) > 1e-3 {
		t.Fatalf("built %d parts, %.3f mm³; want 10 cubes, 10000 mm³ (skipped %v)", got.Parts, got.Volume, got.Skipped)
	}
	if len(got.Interferences) != 0 {
		t.Errorf("copies placed apart report overlaps: %+v", got.Interferences)
	}
	// The ring of four about y reaches z = ±65 and x = ±65.
	if math.Abs(got.Bounds[5]-65) > 1e-6 || math.Abs(got.Bounds[2]-(-65)) > 1e-6 || math.Abs(got.Bounds[0]-(-65)) > 1e-6 {
		t.Errorf("bounds %v: the circle of copies did not turn about y", got.Bounds)
	}
}
