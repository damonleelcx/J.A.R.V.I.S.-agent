package geometry

import (
	"strings"
	"testing"
)

// Every reader sees the copies of a repeated part, not just the part as written.
// docs/bugfix/2026-09-13-repeat-copies-were-invisible-to-most-readers.md

func fiveBoxesInARow() Document {
	return Document{Name: "row", Units: "mm", Parts: []Part{{
		ID: "block", Name: "Block", Shape: "box", Color: "#ff0000",
		Size:     map[string]float64{"width": 10, "height": 10, "depth": 10},
		Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0},
		Repeat: &Repeat{Count: 5, Offset: []float64{100, 0, 0}},
	}}}
}

// Five 10 mm boxes 100 mm apart reach from -5 to 405: 410 mm overall, not 10.
func TestMeasure_IncludesEveryCopyOfARepeatedPart(t *testing.T) {
	var width float64
	found := false
	for _, o := range Measure(fiveBoxesInARow(), Millimetre) {
		if strings.Contains(o.Label, "overall width") {
			width, found = o.Value, true
		}
	}
	if !found {
		t.Fatal("Measure reported no overall width")
	}
	if width < 409.999 || width > 410.001 {
		t.Errorf("five boxes 100 mm apart measure %.3f mm wide, want 410 — the copies are not in the extent", width)
	}
}

// A copy is drawn in its part's colour, not the grey kept for "no such part".
func TestContactSheet_ColoursACopyLikeItsPart(t *testing.T) {
	d := fiveBoxesInARow()
	got := partColour(d, "block-3")
	want, _ := parseHexColour("#ff0000")
	if got != want {
		t.Errorf("copy block-3 is drawn in %v, want its part's %v", got, want)
	}
}

// A state may name the whole pattern or one copy; a copy that does not exist is refused.
func TestValidateStates_NamesACopyOrThePattern(t *testing.T) {
	d := fiveBoxesInARow()
	for _, hidden := range []string{"block", "block-3"} {
		states := []AssemblyState{{Name: "exploded", Hidden: []string{hidden}}}
		if err := ValidateStates(states, d.Parts); err != nil {
			t.Errorf("a state hiding %q was refused: %v", hidden, err)
		}
	}
	states := []AssemblyState{{Name: "exploded", Hidden: []string{"block-9"}}}
	if err := ValidateStates(states, d.Parts); err == nil {
		t.Error("a state hiding block-9, which the pattern of five does not make, was accepted")
	}
}

func TestExpanded_WritesOutCopiesAndLeavesTheAuthoredDocumentAlone(t *testing.T) {
	d := fiveBoxesInARow()
	e := d.Expanded()
	if len(e.Parts) != 5 || e.Parts[2].ID != "block-3" {
		t.Fatalf("expanded to %d parts: %+v", len(e.Parts), e.Parts)
	}
	if len(d.Parts) != 1 || d.Parts[0].Repeat == nil {
		t.Error("expanding changed the authored document")
	}
}
