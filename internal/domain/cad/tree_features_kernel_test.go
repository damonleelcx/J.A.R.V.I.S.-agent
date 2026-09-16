package cad_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// The acceptance criterion of D1e (docs/plan-2026-09-13-millions-of-parts.md): the
// B0 wheel — a hub fused with its repeated spokes — expressed as a sub-assembly
// that declares its own weld builds identically to the flat document, and placing
// it twice gives two welded wheels.
func TestKernel_AWheelWeldedAsASubAssemblyBuildsLikeTheFlatOne(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	flat := spokedHub(6)
	hub, spoke := flat.Parts[0], flat.Parts[1]
	tree := func(places ...[]float64) geometry.Document {
		car := geometry.Assembly{ID: "car"}
		for i, at := range places {
			car.Children = append(car.Children, geometry.Child{ID: "w" + string(rune('1'+i)), Ref: "wheel", Position: at})
		}
		return geometry.Document{Name: "wheel", Units: "mm", Root: "car",
			Definitions: []geometry.Part{hub, spoke},
			Assemblies: []geometry.Assembly{car, {ID: "wheel",
				Children: []geometry.Child{{ID: "hub", Ref: "hub"}, {ID: "spoke", Ref: "spoke"}},
				Features: []geometry.Feature{{ID: "weld", Op: "fuse", Of: "hub", With: []string{"spoke"}}}}}}
	}

	want, err := k.BuildDocument(ctx, flat, geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}
	one, err := k.BuildDocument(ctx, tree([]float64{0, 0, 0}), geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(one.FeatureFailures) != 0 || one.Parts != want.Parts || len(one.Interferences) != 0 {
		t.Fatalf("the sub-assembly built %d solid(s), failures %v, interferences %d; the flat wheel is %d solid(s)",
			one.Parts, one.FeatureFailures, len(one.Interferences), want.Parts)
	}
	if math.Abs(one.Volume-want.Volume) > 1e-6*want.Volume {
		t.Errorf("volume %.6f mm³, the flat wheel's is %.6f", one.Volume, want.Volume)
	}
	for i := range want.Bounds {
		if math.Abs(one.Bounds[i]-want.Bounds[i]) > 1e-6 {
			t.Fatalf("bounds %v, the flat wheel's are %v", one.Bounds, want.Bounds)
		}
	}

	two, err := k.BuildDocument(ctx, tree([]float64{0, 0, 0}, []float64{300, 0, 0}), geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(two.FeatureFailures) != 0 || two.Parts != 2*want.Parts {
		t.Fatalf("two placed wheels built %d solid(s), failures %v; want %d welded wheels", two.Parts, two.FeatureFailures, 2*want.Parts)
	}
	if math.Abs(two.Volume-2*want.Volume) > 1e-6*want.Volume || math.Abs(two.Bounds[3]-(want.Bounds[3]+300)) > 1e-6 {
		t.Errorf("two wheels: %.6f mm³ reaching x=%.6f; want %.6f reaching x=%.6f",
			two.Volume, two.Bounds[3], 2*want.Volume, want.Bounds[3]+300)
	}
}
