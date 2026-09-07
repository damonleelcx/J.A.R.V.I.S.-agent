package geometry

import (
	"strings"
	"testing"
)

// A mesh export must say what its features did NOT do (wave 30).
//
// # The defect
//
// Tessellate draws parts and has never performed a feature — that needs a
// kernel, and this is a triangle builder. What it also did was say nothing. So
// an OBJ or STL of a bracket with four bolt holes contained four solid POSTS
// standing on the plate, with no hint anywhere in the file that the four
// cylinders in it are the exact opposite of what they represent.
//
// The viewport has drawn a cut tool as a warning-gold ghost since wave 15 for
// precisely this reason. The exported mesh — which is the artefact somebody
// takes away and opens somewhere else, where FORGE cannot caption anything —
// said nothing at all.
func bracketWithHoles() Document {
	return Document{
		Name: "bracket", Units: "mm",
		Parts: []Part{
			{ID: "plate", Name: "Plate", Shape: "box",
				Size:     map[string]float64{"width": 60, "height": 6, "depth": 60},
				Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}},
			{ID: "hole-0", Name: "Hole 0", Shape: "cylinder",
				Size:     map[string]float64{"radius": 1.75, "height": 24},
				Position: []float64{15, 0, 15}, Rotation: []float64{0, 0, 0}},
		},
		Features: []Feature{
			{ID: "bolt-holes", Op: "cut", Of: "plate", With: []string{"hole-0"}},
			{ID: "corners", Op: "fillet", Of: "plate", Radius: 3, Edges: "vertical"},
		},
	}
}

func TestAMeshSaysWhichFeaturesItDidNotPerform(t *testing.T) {
	m := Tessellate(bracketWithHoles(), Millimetre)
	joined := strings.Join(m.Inferences, "\n")

	for _, want := range []struct{ needle, why string }{
		{"is NOT removed in this file",
			"the cut was not reported, so the exported mesh contains the tool as a solid and " +
				"says nothing about it"},
		{"Hole 0 is a cut TOOL",
			"the tool part was not named. The parts list and the group names in the file are " +
				"where somebody looks when they wonder what a cylinder is doing there."},
		{"a mesh has no edges to round",
			"the fillet was not reported, so the file is squarer than the design and silent"},
	} {
		if !strings.Contains(joined, want.needle) {
			t.Errorf("no inference contains %q: %s\nGot:\n%s", want.needle, want.why, joined)
		}
	}
}

// It names the parts by their human names, because the file's group names are
// what a reader has in front of them.
func TestTheFeatureNoteNamesTheParts(t *testing.T) {
	m := Tessellate(bracketWithHoles(), Millimetre)
	joined := strings.Join(m.Inferences, "\n")
	if !strings.Contains(joined, "Plate") || !strings.Contains(joined, "Hole 0") {
		t.Errorf("the notes do not name the parts they are about:\n%s", joined)
	}
}

// A document with no features says nothing about them. A note on every export
// would be noise, and noise is what makes a real warning invisible.
func TestADocumentWithNoFeaturesSaysNothingAboutThem(t *testing.T) {
	doc := bracketWithHoles()
	doc.Features = nil
	for _, note := range Tessellate(doc, Millimetre).Inferences {
		if strings.Contains(note, "TOOL") || strings.Contains(note, "NOT removed") {
			t.Errorf("a document with no features was warned about one: %q", note)
		}
	}
}
