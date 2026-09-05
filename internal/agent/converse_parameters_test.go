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
	if !notedAbout(r, "will not follow when anything else changes") {
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
