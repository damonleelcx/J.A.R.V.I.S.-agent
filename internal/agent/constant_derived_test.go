package agent_test

import (
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/agent"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// Issue 8, and the exact shape the live run produced: 2 of 3 derived figures
// written as bare constants.
//
// The provenance check travels along dependency edges, and a bare constant has
// none — it depends on nothing, so nothing propagates to it and the check said
// nothing about it. The banner then read clean, and a reader reasonably
// concluded the figures had been traced when two of the three had not. That is
// worse than not having the check: it converts an unknown into a false
// assurance.
func TestStandardsClaims_ABareConstantInDerivedIsReportedAsHavingNoSource(t *testing.T) {
	doc := &agent.Prototype{
		Name: "NEMA 17 mount", Units: "mm",
		Parameters: []geometry.Parameter{
			{Name: "plate_width", Value: 60, Unit: "mm", How: geometry.Chosen},
		},
		Derived: []geometry.Derived{
			// Recalled, and written where nothing can check it.
			{Name: "bolt_pitch", Expression: "31.0"},
			{Name: "face_size", Expression: "42.3"},
			// Ordinary arithmetic over a parameter: traced, and silent.
			{Name: "rib_position_x", Expression: "plate_width / 2"},
		},
	}
	claims := claimsFor(doc)

	var unsourced []string
	for _, c := range claims {
		for _, s := range c.Standards {
			if strings.Contains(s, "no source") {
				unsourced = append(unsourced, c.Text)
			}
		}
	}
	if len(unsourced) != 2 {
		t.Fatalf("reported %d figures with no source, want 2 of the 3 derived: %v", len(unsourced), unsourced)
	}
	for _, want := range []string{"bolt_pitch = 31", "face_size = 42.3"} {
		found := false
		for _, got := range unsourced {
			if strings.HasPrefix(got, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("%q was not reported as having no source; got %v", want, unsourced)
		}
	}
	// And ordinary arithmetic is not a false alarm. rib_position_x rests on a
	// parameter, so where it came from is already answerable.
	for _, got := range unsourced {
		if strings.HasPrefix(got, "rib_position_x") {
			t.Errorf("a value derived from a parameter was reported as sourceless: %q", got)
		}
	}
	// The reason travels in Via, where provenance belongs — never in Text, which
	// the eval scorer reads positionally.
	c, ok := findClaim(claims, "bolt_pitch = 31")
	if !ok {
		t.Fatal("no claim for bolt_pitch")
	}
	if !strings.Contains(c.Via, "names no parameter") || !strings.Contains(c.Via, `"31.0"`) {
		t.Errorf("Via does not say why there is no source: %q", c.Via)
	}
	if c.Where != "derived value" {
		t.Errorf("where = %q, want \"derived value\"", c.Where)
	}
}

// A zero is not a recalled figure; it is an origin. parameters.go already
// records what happened when the adjacent warning did not make that
// distinction — it fired twice on `motor_centre_x = 0`, a centre that is CORRECT
// not to follow anything — and this banner is the one place a real finding has
// to stay visible.
func TestStandardsClaims_AConstantZeroInDerivedIsNotCalledARecalledFigure(t *testing.T) {
	doc := &agent.Prototype{
		Name: "mount", Units: "mm",
		Parameters: []geometry.Parameter{{Name: "plate_width", Value: 60, Unit: "mm"}},
		Derived:    []geometry.Derived{{Name: "motor_centre_x", Expression: "0"}},
	}
	for _, c := range claimsFor(doc) {
		for _, s := range c.Standards {
			if strings.Contains(s, "no source") {
				t.Errorf("a zero was reported as a figure with no source: %q", c.Text)
			}
		}
	}
}
