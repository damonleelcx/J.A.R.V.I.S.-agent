package cad_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// Moving an interface moves what is attached to it — in the solid the real kernel
// builds, not only in the placement arithmetic. Phase 1, stage D1d of
// docs/plan-2026-09-13-millions-of-parts.md.
//
// A 10 mm cube `arm` sits at the corner's origin; a 10 mm cube `tyre` is attached
// at the corner's `hub`, 5 mm out along the hub's local x. The hub is turned 90°
// about y, which takes that local x to world -z, so the tyre's centre is
// (100, 0, hub_z - 5) and its top face is at exactly z = hub_z.
func TestKernel_MovingAnInterfaceMovesWhatIsAttachedToIt(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cube := func(id string) geometry.Part {
		return geometry.Part{ID: id, Shape: "box", Size: map[string]float64{"width": 10, "height": 10, "depth": 10}}
	}
	doc := func(hubZ float64) geometry.Document {
		return geometry.Document{Name: "corner", Units: "mm", Root: "car",
			Definitions: []geometry.Part{cube("block"), cube("wheel")},
			Assemblies: []geometry.Assembly{
				{ID: "car", Children: []geometry.Child{
					{ID: "fl", Ref: "corner", Position: []float64{100, 0, 0}},
					{ID: "tyre", Ref: "wheel", At: "fl/hub", Position: []float64{5, 0, 0}},
				}},
				{ID: "corner",
					Interfaces: []geometry.Interface{{ID: "hub", Position: []float64{0, 0, hubZ}, Rotation: []float64{0, 90, 0}}},
					Children:   []geometry.Child{{ID: "arm", Ref: "block"}}},
			}}
	}
	for _, hubZ := range []float64{50, 80} {
		got, err := k.BuildDocument(ctx, doc(hubZ), geometry.Millimetre, "")
		if err != nil {
			t.Fatal(err)
		}
		if got.Parts != 2 || math.Abs(got.Volume-2000) > 1e-3 {
			t.Fatalf("hub at z=%v: built %d parts, %.3f mm³; want 2 cubes, 2000 mm³ (skipped %v)", hubZ, got.Parts, got.Volume, got.Skipped)
		}
		want := []float64{95, -5, -5, 105, 5, hubZ}
		for i, w := range want {
			if math.Abs(got.Bounds[i]-w) > 1e-6 {
				t.Fatalf("hub at z=%v: bounds %v, want %v — the tyre did not follow the hub", hubZ, got.Bounds, want)
			}
		}
	}
}
