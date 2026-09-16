package geometry

import (
	"fmt"
	"strings"
	"testing"
)

// An id placed twice is refused at the storage door however the second part came
// to exist: authored, placed by the tree, or written out by a repeat — on its own
// or inside a pattern. Comparison, states, features and selection all address a
// part by id, so of two parts sharing one, one is unreachable.
// docs/bugfix/2026-09-14-a-repeat-copy-could-take-another-parts-id.md
func store(d Document) error {
	d.Name, d.Units = "ids", "mm"
	d.NotVerified = []string{"concept only"}
	return (&NewVariant{InitiatorID: "u", Agent: "converse", Generator: "g", Inputs: map[string]any{}, Document: d}).Validate()
}

func unitCube(id string) Part {
	return Part{ID: id, Shape: "box", Size: map[string]float64{"width": 1, "height": 1, "depth": 1}}
}

func repeatedCube(id string, count int) Part {
	p := unitCube(id)
	p.Repeat = &Repeat{Count: count, Offset: []float64{5, 0, 0}}
	return p
}

func TestNewVariant_AnIdPlacedTwiceIsRefusedHoweverItWasMade(t *testing.T) {
	for _, tc := range []struct {
		name string
		doc  Document
		dup  string
	}{
		{"a flat repeat's copy, then an authored part", Document{Parts: []Part{repeatedCube("rail", 3), unitCube("rail-2")}}, "rail-2"},
		{"an authored part, then a flat repeat's copy", Document{Parts: []Part{unitCube("rail-2"), repeatedCube("rail", 3)}}, "rail-2"},
		{"a top-level repeat's copy and a part the tree places", Document{
			Parts:       []Part{repeatedCube("rail", 3)},
			Definitions: []Part{unitCube("block")},
			Assemblies:  []Assembly{{ID: "root", Children: []Child{{ID: "rail-2", Ref: "block", Position: []float64{0, 9, 0}}}}},
			Root:        "root"}, "rail-2"},
		{"a repeat inside a pattern and a sibling's repeat", Document{
			Definitions: []Part{repeatedCube("block", 3)},
			Assemblies: []Assembly{{ID: "root", Children: []Child{
				{ID: "rail", Ref: "block", Pattern: &Pattern{Kind: "linear", Count: 3, Offset: []float64{0, 0, 20}}},
				{ID: "rail-2", Ref: "block", Position: []float64{0, 50, 0}},
			}}},
			Root: "root"}, "rail-2-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := store(tc.doc)
			want := fmt.Sprintf("part id %q appears twice", tc.dup)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("stored a document with %q placed twice: %v", tc.dup, err)
			}
		})
	}
}

// The other side of the same rule: copies whose ids nothing else holds are stored,
// and a state may still name the repeated part by its authored id.
func TestNewVariant_RepeatCopiesWithTheirOwnIdsAreStored(t *testing.T) {
	for _, tc := range []struct {
		name string
		doc  Document
	}{
		{"a flat repeat beside a part it does not name", Document{Parts: []Part{repeatedCube("rail", 3), unitCube("rail-4")}}},
		{"a repeat inside a pattern", Document{
			Definitions: []Part{repeatedCube("block", 3)},
			Assemblies: []Assembly{{ID: "root", Children: []Child{
				{ID: "rail", Ref: "block", Pattern: &Pattern{Kind: "linear", Count: 3, Offset: []float64{0, 0, 20}}},
			}}},
			Root: "root"}},
		{"a repeat of one beside a part named like a copy", Document{Parts: []Part{repeatedCube("rail", 1), unitCube("rail-1")}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := store(tc.doc); err != nil {
				t.Errorf("refused: %v", err)
			}
		})
	}
}
