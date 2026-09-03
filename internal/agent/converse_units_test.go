package agent

import (
	"strings"
	"testing"

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
