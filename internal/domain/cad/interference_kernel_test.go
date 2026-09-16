package cad_test

import (
	"context"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// The interference check, against the real kernel.
//
// Every case here is a property of OpenCASCADE and of the order _build applies
// features in, not of our arithmetic, so none of them can be faked. The two that
// matter most are the NEGATIVE ones: this check exists to drive repairs, and a
// checker that fires on a correct model drives repairs that damage it — this
// repository has already had to delete one rule for exactly that.
// See docs/spikes/2026-09-12-car-ceiling/README.md.

func box(id string, w, h, d float64, x, y, z float64) geometry.Part {
	return geometry.Part{ID: id, Name: id, Shape: "box",
		Size:     map[string]float64{"width": w, "height": h, "depth": d},
		Position: []float64{x, y, z}, Rotation: []float64{0, 0, 0}}
}

func TestKernel_TwoSolidsInTheSameSpaceAreReported(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// 10 x 10 x 10 each, offset 5 in x: they share 5 x 10 x 10 = 500 mm³, which
	// is half of either one.
	doc := geometry.Document{Name: "clash", Units: "mm", Parts: []geometry.Part{
		box("left", 10, 10, 10, 0, 0, 0),
		box("right", 10, 10, 10, 5, 0, 0),
	}}
	got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Interferences) != 1 {
		t.Fatalf("two boxes half inside each other reported %d interference(s): %+v",
			len(got.Interferences), got.Interferences)
	}
	c := got.Interferences[0]
	if c.Volume < 499 || c.Volume > 501 {
		t.Errorf("shared volume is %.1f mm³, want 500", c.Volume)
	}
	if c.Fraction < 0.49 || c.Fraction > 0.51 {
		t.Errorf("shared fraction is %.3f, want 0.5", c.Fraction)
	}
	if !c.Buried() {
		t.Errorf("half of a part inside another is the threshold case and did not count as buried")
	}
}

func TestKernel_PartsThatDoNotTouchAreNotReported(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	doc := geometry.Document{Name: "apart", Units: "mm", Parts: []geometry.Part{
		box("left", 10, 10, 10, 0, 0, 0),
		box("right", 10, 10, 10, 100, 0, 0),
	}}
	got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Interferences) != 0 {
		t.Fatalf("two parts 90mm apart were reported as interfering: %+v", got.Interferences)
	}
}

// ‼️ The false positive this whole design exists to avoid.
//
// A bolt hole is a cylinder CUT from a plate. Until the cut is applied the
// cylinder is a solid standing inside the plate, which is exactly what
// geometry.Tessellate draws and what look.go had to be taught to ignore
// (docs/plan-2026-09-09-complex-prototypes.md, Stage 6). A check written over
// the described document would report every hole in every mechanical part ever
// built. This one runs after the tools are consumed, so there is nothing to
// report — and if somebody ever moves it earlier, this goes red.
func TestKernel_ACutToolIsNotAnInterference(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	plate := box("plate", 50, 10, 50, 0, 0, 0)
	drill := geometry.Part{ID: "drill", Name: "Bolt Hole", Shape: "cylinder",
		Size:     map[string]float64{"radius": 3, "height": 30},
		Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}
	doc := geometry.Document{Name: "plate with a hole", Units: "mm",
		Parts: []geometry.Part{plate, drill},
		Features: []geometry.Feature{{ID: "hole", Op: "cut", Of: "plate",
			With: []string{"drill"}}}}

	got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Skipped) > 0 || len(got.FeatureFailures) > 0 {
		t.Fatalf("the fixture itself did not build: skipped=%v failed=%v", got.Skipped, got.FeatureFailures)
	}
	if len(got.Interferences) != 0 {
		t.Fatalf("a correctly cut bolt hole was reported as an interference: %+v — "+
			"this is the false positive the check is computed on kept solids to avoid",
			got.Interferences)
	}
}

// ‼️ The second false positive, and the reason a bounding-box test would not do.
//
// A part inside a HOLLOW enclosure shares no material with it. Bounding boxes
// say it is entirely buried; the solids say they never touch. Measured directly
// against build123d before this was written: common volume 0.0.
func TestKernel_APartInsideAHollowEnclosureIsNotAnInterference(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	doc := geometry.Document{Name: "boxed", Units: "mm", Parts: []geometry.Part{
		box("case", 40, 40, 40, 0, 0, 0),
		box("void", 30, 30, 30, 0, 0, 0),
		box("component", 10, 10, 10, 0, 0, 0),
	}, Features: []geometry.Feature{
		{ID: "hollow", Op: "cut", Of: "case", With: []string{"void"}},
	}}

	got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Skipped) > 0 || len(got.FeatureFailures) > 0 {
		t.Fatalf("the fixture itself did not build: skipped=%v failed=%v", got.Skipped, got.FeatureFailures)
	}
	if len(got.Interferences) != 0 {
		t.Fatalf("a component sitting inside a hollow case was reported as an interference: %+v",
			got.Interferences)
	}
}

// A part entirely swallowed by another is the case the live car produced —
// "master-cylinder is 100% inside engine-block" — and the only class that drives
// an automatic repair.
func TestKernel_ASwallowedPartIsReportedAsBuried(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	doc := geometry.Document{Name: "swallowed", Units: "mm", Parts: []geometry.Part{
		box("block", 100, 100, 100, 0, 0, 0),
		box("cylinder-body", 10, 10, 10, 0, 0, 0),
	}}
	got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Interferences) != 1 {
		t.Fatalf("a part inside a solid block reported %d interference(s): %+v",
			len(got.Interferences), got.Interferences)
	}
	c := got.Interferences[0]
	// The SMALLER part is named first, or the sentence built from this reads
	// "the block is 100% inside the small part".
	if c.A != "cylinder-body" || c.B != "block" {
		t.Errorf("the smaller part should be named first, got a=%s b=%s", c.A, c.B)
	}
	if c.Fraction < 0.99 {
		t.Errorf("a fully swallowed part is %.3f inside, want 1.0", c.Fraction)
	}
	if !c.Buried() {
		t.Error("a fully swallowed part did not count as buried")
	}
	if len(geometry.InterferenceProblems(got.Interferences)) != 1 {
		t.Error("a buried part produced no Problem for the repair path")
	}
}
