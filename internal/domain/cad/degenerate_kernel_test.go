package cad_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// Issue 7, through the kernel that accepted it.
//
// The whole complaint is that OCCT builds a zero-radius cylinder without
// complaint and returns a solid of volume 0, and the mesh path agrees with OCCT,
// so the meaningless result is internally consistent all the way to the export.
// Only a real build can show that the refusal happens BEFORE that: the part is
// not in the kernel's answer, the volume is the rest of the design's, and the
// reason names the dimension.
func TestKernel_AZeroRadiusPartNeverReachesOCCT(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	doc := geometry.Document{
		Name: "bracket", Units: "mm",
		Parts: []geometry.Part{
			{ID: "plate", Name: "Plate", Shape: "box",
				Size:     map[string]float64{"width": 60, "height": 6, "depth": 60},
				Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}},
			{ID: "boss", Name: "Boss", Shape: "cylinder",
				Size:     map[string]float64{"radius": 0, "height": 8},
				Position: []float64{0, 7, 0}, Rotation: []float64{0, 0, 0}},
		},
	}
	got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Parts != 1 {
		t.Errorf("the kernel built %d parts, want 1 — the collapsed one must not be sent", got.Parts)
	}
	if want := 21600.0; math.Abs(got.Volume-want) > 1 {
		t.Errorf("volume = %.4f mm³, want %.0f — the plate alone", got.Volume, want)
	}
}

// And the wall thickness FORGE reports is the wall OCCT actually builds.
//
// The measurement is taken on the loops the section is drawn from, which is
// arithmetic in this package and not a query of the kernel — so the thing worth
// proving is that the two agree about where the material is. A 40 mm square tube
// with a 20 mm square bore has a 10 mm wall, and π is not involved: the built
// volume is (40² - 20²) × 50 exactly, which is only true if the bore is where
// the wall measurement says it is.
func TestKernel_TheWallFORGEMeasuresIsTheWallOCCTBuilds(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	doc := geometry.Document{
		Name: "tube", Units: "mm",
		Parts: []geometry.Part{{ID: "tube", Name: "Tube", Shape: "extrusion",
			Size:     map[string]float64{"depth": 50},
			Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0},
			Profile: []geometry.Point{{X: -20, Y: -20}, {X: 20, Y: -20}, {X: 20, Y: 20}, {X: -20, Y: 20}},
			Holes: [][]geometry.Point{{
				{X: -10, Y: -10}, {X: 10, Y: -10}, {X: 10, Y: 10}, {X: -10, Y: 10}}},
		}},
	}

	var wall float64
	for _, r := range doc.Relationships() {
		if r.Kind == "wall thickness" && r.Checked {
			wall = r.Value
		}
	}
	if math.Abs(wall-10) > 1e-9 {
		t.Fatalf("FORGE measured a wall of %v mm, want 10", wall)
	}

	got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}
	// Outer 40 x 40 less an inner (40 - 2*wall) square, carried 50 deep.
	inner := 40 - 2*wall
	want := (40*40 - inner*inner) * 50
	if math.Abs(got.Volume-want) > 0.01 {
		t.Errorf("volume = %.4f mm³, want %.4f — the solid OCCT built does not have the wall "+
			"FORGE reported", got.Volume, want)
	}
}
