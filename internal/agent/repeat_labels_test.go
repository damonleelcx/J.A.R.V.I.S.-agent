package agent

import (
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// The resize check names a copy it reports. Its extents come from the expanded
// document and its labels came from the authored one, so a copy was reported as
// " is now … mm" with no name at all.
// docs/bugfix/2026-09-13-repeat-copies-were-invisible-to-most-readers.md
func TestTurned_NamesACopyOfARepeatedPart(t *testing.T) {
	row := func(width float64) *Prototype {
		return &geometry.Document{Name: "row", Units: "mm", Parts: []geometry.Part{{
			ID: "block", Name: "Block", Shape: "box",
			Size:     map[string]float64{"width": width, "height": 10, "depth": 10},
			Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0},
			Repeat: &geometry.Repeat{Count: 3, Offset: []float64{100, 0, 0}},
		}}}
	}
	problems := turnedOnItsSide(row(10), row(40))
	if len(problems) == 0 {
		t.Fatal("quadrupling every copy's width was not reported as a resize")
	}
	for _, p := range problems {
		if !strings.HasPrefix(p.Detail, "Block ") {
			t.Errorf("a copy was reported without its name: %q", p.Detail)
		}
	}
}
