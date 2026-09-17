package agent

import (
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// E1's open item, settled 2026-09-17: the turn says what a parameter or feature edit
// reached. A parameter change is never silent, least of all when nothing follows it;
// a feature follows the rule a part does, silent when it acts on exactly what it names.

func parametricPlate() *Prototype {
	return &geometry.Document{Name: "Bracket", Units: "mm",
		Parameters: []geometry.Parameter{
			{Name: "plate_w", Value: 60, Unit: "mm", How: geometry.Chosen},
			{Name: "hole_pitch", Value: 20, Unit: "mm", How: geometry.Chosen},
		},
		Parts: []geometry.Part{
			{ID: "plate", Name: "Plate", Shape: "box", Size: map[string]float64{"width": 60, "height": 6, "depth": 60},
				SizeFrom: map[string]string{"width": "plate_w"}},
			{ID: "hole", Name: "Hole", Shape: "cylinder", Size: map[string]float64{"radius": 3, "height": 20},
				Repeat: &geometry.Repeat{Count: 4, Offset: []float64{12, 0, 0}}},
			{ID: "boss", Name: "Boss", Shape: "cylinder", Size: map[string]float64{"radius": 8, "height": 10}},
		}}
}

func TestResolveEdit_AParameterOrFeatureEditSaysWhatItReached(t *testing.T) {
	for _, tc := range []struct {
		name  string
		patch geometry.Document
		want  string // "" means no note at all
	}{
		{"a parameter something follows", geometry.Document{Parameters: []geometry.Parameter{{Name: "plate_w", Value: 80, Unit: "mm", How: geometry.Chosen}}},
			"changed parameter plate_w: 1 occurrence (plate)"},
		{"a parameter nothing follows", geometry.Document{Parameters: []geometry.Parameter{{Name: "hole_pitch", Value: 25, Unit: "mm", How: geometry.Chosen}}},
			"changed parameter hole_pitch: no placed part follows it, so nothing moved or resized; a size or position typed as its number stays as typed"},
		{"a feature whose tool is repeated", geometry.Document{Features: []geometry.Feature{{ID: "holes", Op: "cut", Of: "plate", With: []string{"hole"}}}},
			"changed feature holes: 5 occurrences (plate, hole-1, hole-2, hole-3, hole-4)"},
		{"a feature on exactly the parts it names", geometry.Document{Features: []geometry.Feature{{ID: "boss-on", Op: "fuse", Of: "plate", With: []string{"boss"}}}},
			""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			patch := tc.patch
			r := &Reply{PrototypeEdit: &geometry.Edit{Patch: &patch}}
			if err := r.resolveEdit(parametricPlate()); err != nil {
				t.Fatalf("refused: %v", err)
			}
			if tc.want == "" {
				if r.Repaired != "" {
					t.Errorf("an edit that reached exactly what it named left a note: %q", r.Repaired)
				}
				return
			}
			if !strings.Contains(r.Repaired, tc.want) {
				t.Errorf("want the note to contain %q, got %q", tc.want, r.Repaired)
			}
		})
	}
}
