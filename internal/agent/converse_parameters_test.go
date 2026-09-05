package agent

import (
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// Fences over Reply.validate for the parametric document (2026-09-05 phase).
//
// These live beside the unit and tolerance fences for the same reason those do:
// what they exercise is the BOUNDARY, the one choke point both the buffered and
// the streamed path go through. A parameter model that does not resolve changes
// no pixel of the render, so if the boundary does not say so, nothing does.

func withParams(params []geometry.Parameter, derived []geometry.Derived) *Reply {
	return &Reply{
		Speech: "here",
		Prototype: &Prototype{
			Name: "bracket", Units: "mm",
			Parts:       []geometry.Part{{ID: "p", Shape: "box", Size: map[string]float64{"width": 60}}},
			Parameters:  params,
			Derived:     derived,
			NotVerified: []string{"nothing checked"},
		},
	}
}

func notedAbout(r *Reply, substr string) bool {
	for _, n := range r.Prototype.NotVerified {
		if strings.Contains(n, substr) {
			return true
		}
	}
	return false
}

// A design whose parameters resolve must not collect notes. A banner that fires
// on correct input is a banner people stop reading — the reasoning standards.go
// already records for not scanning NotVerified.
func TestValidate_AResolvingParameterModelSaysNothing(t *testing.T) {
	r := withParams(
		[]geometry.Parameter{
			{Name: "plate_size", Value: 50, Unit: "mm", How: geometry.Chosen},
			{Name: "fillet_radius", Value: 3, Unit: "mm", How: geometry.Chosen},
		},
		[]geometry.Derived{{Name: "rib_length", Expression: "plate_size - 2 * fillet_radius"}},
	)
	if err := r.validate(); err != nil {
		t.Fatal(err)
	}
	if got := len(r.Prototype.NotVerified); got != 1 {
		t.Fatalf("a correct parametric document added %d notes: %v", got-1, r.Prototype.NotVerified)
	}
}

// A name that is never declared. The render looks identical; the number is not
// there.
func TestValidate_AnUnresolvableParameterModelIsSaidOutLoud(t *testing.T) {
	r := withParams(
		[]geometry.Parameter{{Name: "plate_size", Value: 50, Unit: "mm"}},
		[]geometry.Derived{{Name: "rib_length", Expression: "plate_size - 2 * edge_margin"}},
	)
	if err := r.validate(); err != nil {
		t.Fatal(err)
	}
	if !notedAbout(r, "could not resolve") {
		t.Fatalf("nothing told the reader the parameters do not resolve: %v", r.Prototype.NotVerified)
	}
	if !notedAbout(r, "edge_margin") {
		t.Errorf("the note does not name what is missing: %v", r.Prototype.NotVerified)
	}
}

// The spike's headline failure, surfaced. rib_length as a fixed 52 mm broke the
// build at plate_size=50 — and a reader looking at the render at plate_size=60
// cannot see that it is about to.
func TestValidate_AFixedNumberInADerivedFieldIsSaidOutLoud(t *testing.T) {
	r := withParams(
		[]geometry.Parameter{{Name: "plate_size", Value: 60, Unit: "mm"}},
		[]geometry.Derived{{Name: "rib_length", Expression: "52"}},
	)
	if err := r.validate(); err != nil {
		t.Fatal(err)
	}
	if !notedAbout(r, "with a caveat") {
		t.Fatalf("a caveat was not raised: %v", r.Prototype.NotVerified)
	}
	if !notedAbout(r, "it belongs in the parameters list") {
		t.Errorf("the note does not say what is actually wrong: %v", r.Prototype.NotVerified)
	}
}

// An error and a caveat must not read alike: one means a number is missing, the
// other means every number is present and one of them will not hold up.
func TestValidate_AnErrorAndACaveatDoNotReadAlike(t *testing.T) {
	broken := withParams(
		[]geometry.Parameter{{Name: "plate_size", Value: 50, Unit: "mm"}},
		[]geometry.Derived{{Name: "rib_length", Expression: "plate_size - edge_margin"}},
	)
	_ = broken.validate()
	caveat := withParams(
		[]geometry.Parameter{{Name: "plate_size", Value: 50, Unit: "mm"}},
		[]geometry.Derived{{Name: "rib_length", Expression: "52"}},
	)
	_ = caveat.validate()

	if notedAbout(broken, "with a caveat") {
		t.Error("a missing number was described as a caveat")
	}
	if notedAbout(caveat, "could not resolve") {
		t.Error("a resolving document was described as unresolvable")
	}
}

// Every stored variant and every non-parametric reply predates these fields.
func TestValidate_ANonParametricPrototypeIsUntouched(t *testing.T) {
	r := withParams(nil, nil)
	if err := r.validate(); err != nil {
		t.Fatal(err)
	}
	if got := len(r.Prototype.NotVerified); got != 1 {
		t.Fatalf("a non-parametric prototype collected notes: %v", r.Prototype.NotVerified)
	}
}

// Wave 11: the binding is APPLIED at the boundary, not merely described.
//
// This is the difference the whole wave is about. Before it, a part carrying
// size_from rendered at whatever number the model typed, and a reader looking at
// named parameters beside a shape would reasonably conclude that changing one
// would move it. Nothing downstream reads an expression, so if the boundary does
// not evaluate it, nothing ever does.
func TestValidate_ABoundDimensionIsResolvedIntoWhatTheRendererReads(t *testing.T) {
	r := &Reply{
		Speech: "here",
		Prototype: &Prototype{
			Name: "bracket", Units: "mm",
			Parameters: []geometry.Parameter{
				{Name: "plate_size", Value: 80, Unit: "mm", How: geometry.Chosen},
				{Name: "fillet_radius", Value: 3, Unit: "mm", How: geometry.Chosen},
			},
			Derived: []geometry.Derived{
				{Name: "rib_length", Expression: "plate_size - 2 * fillet_radius"},
			},
			Parts: []geometry.Part{{
				ID: "rib", Shape: "box",
				// The number the model typed is stale: it belongs to a 60 mm plate.
				Size:         map[string]float64{"width": 54, "height": 12, "depth": 6},
				SizeFrom:     map[string]string{"width": "rib_length"},
				Position:     []float64{0, 0, 0},
				PositionFrom: map[string]string{"z": "plate_size / 4"},
			}},
			NotVerified: []string{"nothing checked"},
		},
	}
	if err := r.validate(); err != nil {
		t.Fatal(err)
	}
	if got := r.Prototype.Parts[0].Size["width"]; got != 74 {
		t.Fatalf("width = %g, want 74 — the expression must reach Size, which is the only "+
			"thing the mesh, the comparison and the exporter ever read", got)
	}
	if got := r.Prototype.Parts[0].Position[2]; got != 20 {
		t.Errorf("position z = %g, want 20", got)
	}
	if !notedAbout(r, "with a caveat") {
		t.Errorf("the stale 54 contradicted the expression and nothing said so: %v",
			r.Prototype.NotVerified)
	}
}

// A part whose binding cannot be read must still render. The number the model
// typed is the best available answer, and a blanked dimension is a part that
// vanished from the viewport.
func TestValidate_ABrokenBindingStillRendersAndSaysSo(t *testing.T) {
	r := &Reply{
		Speech: "here",
		Prototype: &Prototype{
			Name: "bracket", Units: "mm",
			Parameters: []geometry.Parameter{{Name: "plate_size", Value: 80, Unit: "mm"}},
			Parts: []geometry.Part{{
				ID: "rib", Shape: "box",
				Size:     map[string]float64{"width": 54},
				SizeFrom: map[string]string{"width": "plaet_size"},
			}},
			NotVerified: []string{"nothing checked"},
		},
	}
	if err := r.validate(); err != nil {
		t.Fatal(err)
	}
	if got := r.Prototype.Parts[0].Size["width"]; got != 54 {
		t.Fatalf("width = %g; a failed binding must keep the authored number", got)
	}
	if !notedAbout(r, "could not resolve") {
		t.Errorf("the broken binding was not reported: %v", r.Prototype.NotVerified)
	}
}

// Wave 15: a feature that could not be applied, and the one place the picture
// and the file disagree, both reach the reader.
func TestValidate_AFeatureThatCannotBeAppliedIsSaidOutLoud(t *testing.T) {
	r := &Reply{
		Speech: "here",
		Prototype: &Prototype{
			Name: "bracket", Units: "mm",
			Parts: []geometry.Part{
				{ID: "plate", Name: "Plate", Shape: "box",
					Size: map[string]float64{"width": 60, "height": 6, "depth": 60}},
			},
			Features:    []geometry.Feature{{ID: "bore", Op: "cut", Of: "plate", With: []string{"ghost"}}},
			NotVerified: []string{"nothing checked"},
		},
	}
	if err := r.validate(); err != nil {
		t.Fatal(err)
	}
	if !notedAbout(r, "could not apply one of this design's features") {
		t.Fatalf("a dropped feature was not reported: %v", r.Prototype.NotVerified)
	}
	if !notedAbout(r, "ghost") {
		t.Errorf("the note does not name what was missing: %v", r.Prototype.NotVerified)
	}
}

// The render shows a cylinder standing in a plate; the exported file has a hole
// through it. Two things this product shows the same person, and it says so.
func TestValidate_TheViewportsDisagreementWithTheFileIsStated(t *testing.T) {
	r := &Reply{
		Speech: "here",
		Prototype: &Prototype{
			Name: "bracket", Units: "mm",
			Parts: []geometry.Part{
				{ID: "plate", Name: "Plate", Shape: "box",
					Size: map[string]float64{"width": 60, "height": 6, "depth": 60}},
				{ID: "hole", Name: "Hole", Shape: "cylinder",
					Size: map[string]float64{"radius": 2, "height": 20}},
			},
			Features:    []geometry.Feature{{ID: "bore", Op: "cut", Of: "plate", With: []string{"hole"}}},
			NotVerified: []string{"nothing checked"},
		},
	}
	if err := r.validate(); err != nil {
		t.Fatal(err)
	}
	if !notedAbout(r, "cannot cut a hole") {
		t.Fatalf("the divergence between the render and the file was not stated: %v",
			r.Prototype.NotVerified)
	}
}

// And a document with no features collects nothing.
func TestValidate_ANonFeaturedPrototypeSaysNothingAboutFeatures(t *testing.T) {
	r := withParams(nil, nil)
	if err := r.validate(); err != nil {
		t.Fatal(err)
	}
	if got := len(r.Prototype.NotVerified); got != 1 {
		t.Fatalf("a prototype with no features collected notes: %v", r.Prototype.NotVerified)
	}
}
