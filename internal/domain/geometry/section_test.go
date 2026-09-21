package geometry

import (
	"strings"
	"testing"
)

// Named sections: what is validated away, and what the turn must always say about
// the numbers that survive. addresses issue 6.

func TestSections_ASectionThatCannotBeCutIsDroppedAndSaidSo(t *testing.T) {
	parts := []Part{{ID: "beam", Name: "beam", Shape: "box"}}
	kept, dropped := ValidateSections([]Section{
		{ID: "mid", Part: "beam", Axis: "x", At: 0},
		{ID: "ghost", Part: "nothing", Axis: "x", At: 0},
		{ID: "sideways", Part: "beam", Axis: "w", At: 0},
		{ID: "", Part: "beam", Axis: "y", At: 1},
		{ID: "mid", Part: "beam", Axis: "y", At: 2},
	}, parts)

	if len(kept) != 1 || kept[0].ID != "mid" || kept[0].Axis != "x" {
		t.Fatalf("the sections kept are %+v", kept)
	}
	if len(dropped) != 4 {
		t.Fatalf("four unusable sections produced %d note(s): %v", len(dropped), dropped)
	}
	joined := strings.Join(dropped, " ")
	for _, want := range []string{"ghost", "nothing", "sideways", "x, y or z", "no id"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the notes do not say %q: %v", want, dropped)
		}
	}
	// ‼️ A duplicate id is dropped rather than kept: two sections with one name
	// cannot be told apart in a report, and the second would quietly shadow the first.
	if !strings.Contains(joined, "A second section named") {
		t.Errorf("a duplicate section id was not refused: %v", dropped)
	}
}

// ‼️ The caveat is part of the note, not an option. A second moment of area beside
// a part is one step away from being read as "FORGE checked the strength", and a
// check that did not happen must never read as one that did.
func TestSections_TheNoteAlwaysSaysTheseAreNotAStress(t *testing.T) {
	note := SectionNote([]SectionProperties{{
		ID: "mid", Part: "beam", Axis: "x", At: 0, Area: 200,
		Axes: [2]string{"y", "z"}, SecondMoments: [2]float64{1666.67, 6666.67},
		Fibres: [2]float64{5, 10}, Moduli: [2]float64{333.33, 666.67},
	}})

	for _, want := range []string{"mid", "beam", "200", "1667", "6667", "GEOMETRY, not a stress",
		"applied no load", "no boundary conditions"} {
		if !strings.Contains(note, want) {
			t.Errorf("the section note does not say %q: %q", want, note)
		}
	}
	if SectionNote(nil) != "" {
		t.Error("a model with no sections produced a note about sections")
	}
}

// A section the kernel could not cut is reported as unmeasured, with the reason —
// never as a section of zero area, which reads like a part with no material in it.
func TestSections_AnUnmeasuredSectionIsNamedAndNeverPrintedAsZero(t *testing.T) {
	note := SectionNote([]SectionProperties{{
		ID: "mid", Part: "beam", Axis: "x", At: 900,
		Unmeasured: "the plane at 900 is outside the part, which runs from -50 to 50 on that axis",
	}})
	for _, want := range []string{"mid", "not measured", "outside the part"} {
		if !strings.Contains(note, want) {
			t.Errorf("the note does not say %q: %q", want, note)
		}
	}
	if strings.Contains(note, "area 0") {
		t.Errorf("an unmeasured section was printed as an area: %q", note)
	}
}
