package geometry_test

import (
	"reflect"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// A part a tree places is named by its occurrence path: every child above it (its
// name, or its id when it has none, with its pattern number), then its own name.
// Before, only a named child renamed its parts, and only one level down, so the
// first four cases below repeated names while their ids were distinct.
// docs/bugfix/2026-09-14-tree-copies-shared-display-names.md
func TestTree_EveryPartItPlacesIsNamedByItsOccurrence(t *testing.T) {
	box := func(id, name string) geometry.Part {
		return geometry.Part{ID: id, Name: name, Shape: "box", Size: map[string]float64{"width": 1, "height": 1, "depth": 1}}
	}
	ell := box("ell", "Ell")
	ell.Repeat = &geometry.Repeat{Count: 3, Offset: []float64{0, 0, 30}}
	pin, hub := box("pin", "Pin"), box("hub", "Hub")
	one := func(defs []geometry.Part, children ...geometry.Child) geometry.Document {
		return geometry.Document{Root: "r", Definitions: defs, Assemblies: []geometry.Assembly{{ID: "r", Children: children}}}
	}
	row := &geometry.Pattern{Kind: "linear", Count: 3, Offset: []float64{5, 0, 0}}

	for _, c := range []struct {
		name string
		doc  geometry.Document
		want []string
	}{
		{"unnamed children of a repeated definition",
			one([]geometry.Part{ell}, geometry.Child{ID: "left", Ref: "ell"}, geometry.Child{ID: "right", Ref: "ell", Mirror: "x"}),
			[]string{"left / Ell 1", "left / Ell 2", "left / Ell 3", "right / Ell 1", "right / Ell 2", "right / Ell 3"}},
		{"unnamed children of a plain definition",
			one([]geometry.Part{pin}, geometry.Child{ID: "a", Ref: "pin"}, geometry.Child{ID: "b", Ref: "pin", Position: []float64{5, 0, 0}}),
			[]string{"a / Pin", "b / Pin"}},
		{"an unnamed patterned child",
			one([]geometry.Part{pin}, geometry.Child{ID: "row", Ref: "pin", Pattern: row}),
			[]string{"row 1 / Pin", "row 2 / Pin", "row 3 / Pin"}},
		{"two occurrences of a sub-assembly",
			geometry.Document{Root: "car", Definitions: []geometry.Part{hub}, Assemblies: []geometry.Assembly{
				{ID: "car", Children: []geometry.Child{
					{ID: "front", Name: "Front wheel", Ref: "wheel"},
					{ID: "rear", Name: "Rear wheel", Ref: "wheel", Position: []float64{0, 0, 50}}}},
				{ID: "wheel", Children: []geometry.Child{{ID: "hub", Ref: "hub"}}}}},
			[]string{"Front wheel / hub / Hub", "Rear wheel / hub / Hub"}},
		{"a named patterned child",
			one([]geometry.Part{pin}, geometry.Child{ID: "row", Name: "Row", Ref: "pin", Pattern: row}),
			[]string{"Row 1 / Pin", "Row 2 / Pin", "Row 3 / Pin"}},
		{"a definition with no name is named by its id",
			one([]geometry.Part{box("bolt", "")}, geometry.Child{ID: "left", Name: "Left", Ref: "bolt"}),
			[]string{"Left / bolt"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			seen := map[string]bool{}
			for _, p := range c.doc.Expanded().Parts {
				if seen[p.Label()] {
					t.Errorf("two parts are named %q", p.Label())
				}
				seen[p.Label()] = true
				got = append(got, p.Label())
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("names %q, want %q", got, c.want)
			}
		})
	}
}
