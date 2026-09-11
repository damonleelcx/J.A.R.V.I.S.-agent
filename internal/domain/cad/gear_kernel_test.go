package cad_test

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// A gear is built by OpenCASCADE as the solid its numbers describe, and a
// feature can act on it like any other part.
//
// # Why the volume is compared with the DRAWING
//
// The sidecar has no gear case: a gear reaches it as the extrusion gear.go made.
// So the claim worth checking against the real kernel is that the solid is that
// section carried through the face width — not a second opinion about what a
// gear is. The involute itself is held against the gear standard in
// geometry/gear_test.go, where it can be measured without Python.
func TestKernel_BuildsAnInvoluteGear(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	size := map[string]float64{"module": 2, "teeth": 20, "depth": 6, "bore_radius": 4}
	gear := geometry.Part{ID: "spur", Name: "Spur Gear", Shape: "gear", Size: size,
		Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}
	doc := geometry.Document{Name: "gear", Units: "mm", Parts: []geometry.Part{gear}}

	got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "step")
	if err != nil {
		t.Fatal(err)
	}
	if got.Parts != 1 || len(got.Skipped) != 0 {
		t.Fatalf("the gear built %d part(s), skipped %v: %v", got.Parts, got.Skipped, got.Inferred)
	}

	profile, _, ok := geometry.GearOutlineForTest(size)
	if !ok {
		t.Fatal("gear.go refused the gear this test builds")
	}
	flat := geometry.FlattenOutlineForTest(profile)
	area := 0.0
	for i := range flat {
		a, b := flat[i], flat[(i+1)%len(flat)]
		area += a[0]*b[1] - b[0]*a[1]
	}
	// The flattened tip and root arcs sit just inside the true arcs the kernel
	// builds, which is well inside half a percent on this gear.
	want := (math.Abs(area)/2 - math.Pi*4*4) * 6
	if math.Abs(got.Volume-want)/want > 0.005 {
		t.Errorf("the gear is %.2f mm³; its section minus the bore, 6 thick, is %.2f", got.Volume, want)
	}
	// Tooth 0 points up +Y, and the face width is centred on the part.
	if math.Abs(got.Bounds[4]-22) > 1e-3 || math.Abs(got.Bounds[2]+3) > 1e-6 || math.Abs(got.Bounds[5]-3) > 1e-6 {
		t.Errorf("bounds %v; the tip circle is 22 and the gear is 6 thick about its centre", got.Bounds)
	}
	if !strings.HasPrefix(string(got.STEP), "ISO-10303-21;") {
		t.Error("no STEP file for a gear")
	}

	// A keyway: a box through the face, cut from the bore by the gear's own id.
	// A gear is an ordinary part to a feature, which is what lets a hub, a keyway
	// or spokes be said without any vocabulary of their own.
	keyed := doc
	keyed.Parts = append([]geometry.Part{gear}, geometry.Part{ID: "keyway", Name: "Keyway", Shape: "box",
		Size:     map[string]float64{"width": 2, "height": 3, "depth": 8},
		Position: []float64{0, 4.5, 0}, Rotation: []float64{0, 0, 0}})
	keyed.Features = []geometry.Feature{{ID: "key", Op: "cut", Of: "spur", With: []string{"keyway"}}}
	cut, err := k.BuildDocument(ctx, keyed, geometry.Millimetre, "step")
	if err != nil {
		t.Fatal(err)
	}
	if len(cut.FeatureFailures) != 0 || cut.Parts != 1 {
		t.Fatalf("the keyway was not cut: %d part(s), failures %v", cut.Parts, cut.FeatureFailures)
	}
	// The box is 2 × 3 × 6 inside the gear, less the sliver of it already in the
	// bore: about 24 mm³ of material.
	if removed := got.Volume - cut.Volume; removed < 20 || removed > 30 {
		t.Errorf("cutting the keyway removed %.2f mm³; about 24 was expected", removed)
	}
}
