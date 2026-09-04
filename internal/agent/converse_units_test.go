package agent

import (
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/claim"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// These two fences live here rather than beside the unit vocabulary they check,
// because what they actually exercise is Reply.validate: the BOUNDARY where a
// model's free-text unit is resolved. The vocabulary moved to
// internal/domain/geometry at PRD VIS-04; the boundary did not.

// An unrecognised unit is caught at the boundary, recorded as unspecified, and
// the reader is told in the place they are already looking.
func TestValidate_UnknownUnitIsRecordedNotGuessed(t *testing.T) {
	for _, declared := range []string{"", "furlongs"} {
		r := &Reply{
			Speech: "here",
			Prototype: &Prototype{
				Name: "thing", Units: declared,
				Parts: []geometry.Part{{ID: "p", Shape: "box", Size: map[string]float64{"width": 60}}},
			},
		}
		if err := r.validate(); err != nil {
			t.Fatalf("%q: %v", declared, err)
		}
		if r.Prototype.Units != "" {
			t.Errorf("%q: units became %q; an unconvertible unit must not survive as if it were usable",
				declared, r.Prototype.Units)
		}
		var told bool
		for _, n := range r.Prototype.NotVerified {
			if strings.Contains(n, "unitless") {
				told = true
			}
		}
		if !told {
			t.Errorf("%q: the reader was not told the numbers have no unit: %v", declared, r.Prototype.NotVerified)
		}
	}
}

// A declared, convertible unit must be left alone — the guard must not fire on
// the normal case.
func TestValidate_KnownUnitSurvives(t *testing.T) {
	r := &Reply{
		Speech: "here",
		Prototype: &Prototype{
			Name: "thing", Units: "mm",
			Parts:       []geometry.Part{{ID: "p", Shape: "box", Size: map[string]float64{"width": 60}}},
			NotVerified: []string{"nothing checked"},
		},
	}
	if err := r.validate(); err != nil {
		t.Fatal(err)
	}
	if r.Prototype.Units != "mm" {
		t.Fatalf("a valid unit was altered to %q", r.Prototype.Units)
	}
	for _, n := range r.Prototype.NotVerified {
		if strings.Contains(n, "unitless") {
			t.Fatal("the unit warning fired on a prototype that declared a valid unit")
		}
	}
}

// The conversation door drops an invented tolerance and tells the reader
// (PRD VIS-03).
//
// # Why this exists on top of geometry's own fences
//
// Those call DrawableOverlays directly. This drives Reply.validate — the
// boundary a model's geometry actually crosses on its way to a browser — so a
// build that validates overlays only at the STORAGE door turns it red. That was
// the real state of this feature for its first hour: a fabricated tolerance
// could never be saved and would have been drawn on screen immediately.
func TestValidate_AnInventedToleranceIsDroppedNotDrawn(t *testing.T) {
	r := &Reply{
		Speech: "here it is",
		Prototype: &Prototype{
			Name: "bracket", Units: "mm",
			Parts: []geometry.Part{{ID: "p", Shape: "box", Size: map[string]float64{"width": 60}}},
			Overlays: []geometry.Overlay{
				{
					ID: "d1", Kind: geometry.Dimension, Label: "bore",
					From: []float64{0, 0, 0}, To: []float64{12, 0, 0},
					Value: 12, Unit: "mm",
					// The dangerous shape: FORGE's own arithmetic, wearing a
					// tolerance it cannot have derived from anything.
					How: claim.Calculated, Tolerance: "±0.02",
				},
				{
					ID: "d2", Kind: geometry.Dimension, Label: "plate width",
					From: []float64{0, 0, 0}, To: []float64{60, 0, 0},
					Value: 60, Unit: "mm", How: claim.Calculated,
					Source: "the part sizes in this document",
				},
			},
		},
	}
	if err := r.validate(); err != nil {
		t.Fatalf("the turn was refused outright: %v\n"+
			"Refusing throws away a shape somebody is waiting on; the bad overlay is what "+
			"should be dropped", err)
	}

	if len(r.Prototype.Overlays) != 1 || r.Prototype.Overlays[0].ID != "d2" {
		t.Fatalf("overlays after validation: %+v\n"+
			"A tolerance FORGE derived reached the browser, where a dimension line is the most "+
			"authoritative mark on the render", r.Prototype.Overlays)
	}

	var told bool
	for _, n := range r.Prototype.NotVerified {
		if strings.Contains(n, "±0.02") && strings.Contains(n, "manufacturing decision") {
			told = true
		}
	}
	if !told {
		t.Errorf("the reader was not told a tolerance had been removed: %v\n"+
			"Dropping it silently leaves them looking at a render missing something they "+
			"cannot see is missing", r.Prototype.NotVerified)
	}
}

// A well-formed overlay survives the boundary untouched — the guard must not
// fire on the normal case.
func TestValidate_AuthoredOverlaysSurvive(t *testing.T) {
	r := &Reply{
		Speech: "here",
		Prototype: &Prototype{
			Name: "bracket", Units: "mm",
			Parts:       []geometry.Part{{ID: "p", Shape: "box", Size: map[string]float64{"width": 60}}},
			NotVerified: []string{"nothing checked"},
			Overlays: []geometry.Overlay{{
				ID: "d1", Kind: geometry.Dimension, Label: "bore",
				From: []float64{0, 0, 0}, To: []float64{12, 0, 0},
				Value: 12, Unit: "mm",
				How: claim.Retrieved, Source: "drawing 41-A rev 3", Tolerance: "±0.05",
			}},
		},
	}
	if err := r.validate(); err != nil {
		t.Fatal(err)
	}
	if len(r.Prototype.Overlays) != 1 {
		t.Fatalf("a drawing's own tolerance was dropped: %+v", r.Prototype.Overlays)
	}
	for _, n := range r.Prototype.NotVerified {
		if strings.Contains(n, "manufacturing decision") {
			t.Fatal("the tolerance guard fired on an overlay that named its drawing")
		}
	}
}
