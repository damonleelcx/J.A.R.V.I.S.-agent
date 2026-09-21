package agent

import (
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// Issues 9 and 10 reaching the TURN, which is the half that matters: a check
// nobody is shown is a check that did not happen.
//
// The document below is the shape both issues describe at once — a plate with a
// parameter nothing reads, and four holes at typed coordinates that read as a
// pattern and behave as a snapshot. It resolves, it binds, it builds, and before
// this nothing anywhere said either thing.
func TestSettle_TheTurnIsToldWhatRelationshipCheckingCouldNotCheck(t *testing.T) {
	hole := func(id string, x float64) geometry.Part {
		return geometry.Part{ID: id, Shape: "cylinder",
			Size:     map[string]float64{"radius": 2, "height": 6},
			Position: []float64{x, 0, 0}}
	}
	doc := settleDocument(&Prototype{
		Name: "plate", Units: "mm",
		Parameters: []geometry.Parameter{
			{Name: "plate_size", Value: 120, Unit: "mm", How: geometry.Chosen},
			{Name: "bolt_pitch", Value: 30, Unit: "mm", How: geometry.Chosen},
		},
		Parts: []geometry.Part{
			{ID: "plate", Shape: "box",
				Size:     map[string]float64{"width": 120, "height": 6, "depth": 40},
				SizeFrom: map[string]string{"width": "plate_size"}},
			hole("bolt-1", -45), hole("bolt-2", -15), hole("bolt-3", 15), hole("bolt-4", 45),
		},
	})
	said := strings.Join(doc.NotVerified, "\n")
	for _, want := range []string{
		"FORGE checked this design's relationships",
		// A parameter nothing reads (issue 9).
		"bolt_pitch is a parameter nothing in this design reads",
		// The pattern that is not one (issue 10).
		"evenly spaced 30 apart along x",
	} {
		if !strings.Contains(said, want) {
			t.Errorf("the turn was never told %q:\n%s", want, said)
		}
	}
	// It is a note and not a refusal: the parts are all still there.
	if len(doc.Faults()) != 0 {
		t.Errorf("a decorative binding must be reported, not refused: %v", doc.Faults())
	}
}

// And a document whose relationships all check out says nothing, because a note
// that appears on correct input is a note people stop reading — the reason this
// codebase keeps giving for not adding one.
func TestSettle_ADesignWhoseRelationshipsCheckOutIsToldNothing(t *testing.T) {
	doc := settleDocument(&Prototype{
		Name: "mount", Units: "mm",
		Parameters: []geometry.Parameter{{Name: "pitch", Value: 31, Unit: "mm", How: geometry.Chosen}},
		Parts: []geometry.Part{
			{ID: "mount-hole-l", Shape: "cylinder", Size: map[string]float64{"radius": 1.6, "height": 6},
				Position: []float64{15.5, 0, 0}, PositionFrom: map[string]string{"x": "pitch / 2"}},
			{ID: "mount-hole-r", Shape: "cylinder", Size: map[string]float64{"radius": 1.6, "height": 6},
				Position: []float64{-15.5, 0, 0}, PositionFrom: map[string]string{"x": "0 - pitch / 2"}},
		},
	})
	for _, note := range doc.NotVerified {
		if strings.HasPrefix(note, "FORGE checked this design's relationships") {
			t.Errorf("a document whose relationships check out was given a caveat: %q", note)
		}
	}
}
