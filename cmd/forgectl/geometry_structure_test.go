package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// `forgectl geometry compare` on two trees says which definition changed, how
// many times each variant places it, and which member of which assembly moved —
// and prints nothing new for flat documents (Phase 7, stage E2).
func TestPrintStructure_NamesTheChangedFieldTheCountsAndWhatMoved(t *testing.T) {
	car := func(radius, hubZ float64) geometry.Document {
		return geometry.Document{Units: "mm",
			Definitions: []geometry.Part{{ID: "wheel", Name: "Wheel", Shape: "cylinder",
				Size: map[string]float64{"radius": radius, "height": 30}}},
			Assemblies: []geometry.Assembly{
				{ID: "car", Children: []geometry.Child{
					{ID: "front", Ref: "axle", Position: []float64{1200, 0, 0}},
					{ID: "rear", Ref: "axle", Position: []float64{-1200, 0, 0}},
				}},
				{ID: "axle", Interfaces: []geometry.Interface{{ID: "hub", Position: []float64{0, 0, hubZ}}},
					Children: []geometry.Child{
						{ID: "wheel", Ref: "wheel", At: "hub", Pattern: &geometry.Pattern{Kind: "linear", Count: 2,
							Offset: []float64{0, 0, -1600}}},
					}},
			},
			Root: "car"}
	}
	variant := func(name string, d geometry.Document) geometry.Variant {
		return geometry.Variant{Name: name, Units: geometry.Millimetre, Document: d}
	}
	cmp := geometry.Compare([]geometry.Variant{variant("a", car(300, 800)), variant("b", car(320, 820))})

	var out bytes.Buffer
	if err := printStructure(&out, cmp.Structure); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"STRUCTURE", "≠ definition Wheel", "placed 4×", "changed: size",
		"interface hub: changed: position"} {
		if !strings.Contains(got, want) {
			t.Errorf("the structure section does not say %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "child wheel") {
		t.Errorf("the wheel child did not change and was listed:\n%s", got)
	}

	out.Reset()
	flat := geometry.Compare([]geometry.Variant{
		variant("a", geometry.Document{Parts: []geometry.Part{{ID: "p", Shape: "box"}}}),
		variant("b", geometry.Document{Parts: []geometry.Part{{ID: "p", Shape: "box"}}}),
	})
	if err := printStructure(&out, flat.Structure); err != nil || out.Len() != 0 {
		t.Errorf("flat documents printed a structure section (%v):\n%s", err, out.String())
	}
}
