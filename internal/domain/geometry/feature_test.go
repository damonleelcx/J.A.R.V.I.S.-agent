package geometry_test

import (
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

func plated() *geometry.Document {
	return &geometry.Document{
		Name: "bracket", Units: "mm",
		Parameters: []geometry.Parameter{
			{Name: "fillet_radius", Value: 3, Unit: "mm", How: geometry.Chosen},
		},
		Parts: []geometry.Part{
			{ID: "plate", Name: "Plate", Shape: "box",
				Size: map[string]float64{"width": 60, "height": 6, "depth": 60}},
			{ID: "hole", Name: "Hole", Shape: "cylinder",
				Size: map[string]float64{"radius": 2, "height": 20}},
			{ID: "rib", Name: "Rib", Shape: "box",
				Size: map[string]float64{"width": 6, "height": 10, "depth": 40}},
		},
	}
}

func opProblem(problems []geometry.Problem, name, substr string) bool {
	for _, p := range problems {
		if p.Name == name && strings.Contains(p.Detail, substr) {
			return true
		}
	}
	return false
}

func TestOperations_ResolvesAFilletRadiusFromAnExpression(t *testing.T) {
	d := plated()
	d.Features = []geometry.Feature{
		{ID: "round", Op: "fillet", Of: "plate", RadiusFrom: "fillet_radius * 2", Edges: "vertical"},
	}
	ops, problems := d.Operations()
	if len(problems) != 0 {
		t.Fatalf("%+v", problems)
	}
	if len(ops) != 1 || ops[0].Radius != 6 {
		t.Fatalf("radius = %v, want 6 from the expression", ops)
	}
	if ops[0].Edges != "vertical" {
		t.Errorf("edges = %q", ops[0].Edges)
	}
}

// A tool is consumed. Using it twice means the second operation cuts with
// something that no longer exists.
func TestOperations_AToolIsUsedOnce(t *testing.T) {
	d := plated()
	d.Features = []geometry.Feature{
		{ID: "cut-plate", Op: "cut", Of: "plate", With: []string{"hole"}},
		{ID: "cut-rib", Op: "cut", Of: "rib", With: []string{"hole"}},
	}
	ops, problems := d.Operations()
	if len(ops) != 1 {
		t.Errorf("%d operations survived, want 1", len(ops))
	}
	if !opProblem(problems, "cut-rib", "already consumed") {
		t.Errorf("reusing a consumed tool was accepted: %+v", problems)
	}
}

// Cutting a part with itself removes the whole part, which is never what
// somebody meant.
func TestOperations_APartCannotBeItsOwnTool(t *testing.T) {
	d := plated()
	d.Features = []geometry.Feature{{ID: "self", Op: "cut", Of: "plate", With: []string{"plate"}}}
	_, problems := d.Operations()
	if !opProblem(problems, "self", "on itself") {
		t.Fatalf("a part cut with itself was accepted: %+v", problems)
	}
}

// THE SPIKE'S LESSON, held in place.
//
// "An index would silently select a different edge the moment a parameter
// changed, which is the failure mode that makes naive parametric scripts break
// on their second run." There is nowhere in this vocabulary to write one, and
// anything that is not a known rule is refused rather than guessed at.
func TestOperations_ThereIsNoWayToNameAnEdgeByNumber(t *testing.T) {
	for _, edges := range []string{"3", "edge-2", "0", "first", "outer"} {
		d := plated()
		d.Features = []geometry.Feature{
			{ID: "round", Op: "fillet", Of: "plate", Radius: 2, Edges: edges},
		}
		ops, problems := d.Operations()
		if len(ops) != 0 {
			t.Errorf("edges=%q was accepted", edges)
		}
		if !opProblem(problems, "round", "not a rule FORGE knows") {
			t.Errorf("edges=%q: %+v", edges, problems)
		}
	}
}

func TestOperations_RefusesWhatItCannotDo(t *testing.T) {
	for _, tc := range []struct {
		name  string
		f     geometry.Feature
		wants string
	}{
		{"an unknown operation", geometry.Feature{ID: "x", Op: "emboss", Of: "plate"}, "not an operation"},
		{"a part that does not exist", geometry.Feature{ID: "x", Op: "cut", Of: "ghost", With: []string{"hole"}}, "not a part"},
		{"a tool that does not exist", geometry.Feature{ID: "x", Op: "cut", Of: "plate", With: []string{"ghost"}}, "not a part"},
		{"a cut with nothing to cut with", geometry.Feature{ID: "x", Op: "cut", Of: "plate"}, "names nothing to cut with"},
		{"a fillet with tools", geometry.Feature{ID: "x", Op: "fillet", Of: "plate", Radius: 2, With: []string{"hole"}}, "does not take tools"},
		{"a fillet with no radius", geometry.Feature{ID: "x", Op: "fillet", Of: "plate"}, "needs a radius"},
		{"a negative radius", geometry.Feature{ID: "x", Op: "fillet", Of: "plate", Radius: -2}, "needs a radius"},
		{"a radius that does not resolve", geometry.Feature{ID: "x", Op: "fillet", Of: "plate", RadiusFrom: "no_such_thing"}, "does not evaluate"},
	} {
		d := plated()
		d.Features = []geometry.Feature{tc.f}
		ops, problems := d.Operations()
		if len(ops) != 0 {
			t.Errorf("%s was accepted", tc.name)
		}
		if !opProblem(problems, "x", tc.wants) {
			t.Errorf("%s: %+v", tc.name, problems)
		}
	}
}

// A part consumed as a tool must not also be listed as a part of the assembly.
func TestConsumedParts_NamesTheToolsAndNothingElse(t *testing.T) {
	d := plated()
	d.Features = []geometry.Feature{{ID: "bore", Op: "cut", Of: "plate", With: []string{"hole"}}}
	got := d.ConsumedParts()
	if !got["hole"] {
		t.Error("the cutting tool is not marked consumed, so the hole would be filled by it")
	}
	if got["plate"] || got["rib"] {
		t.Errorf("a part that survives was marked consumed: %v", got)
	}
}

// The viewport cannot draw a hole, and the reader is told rather than left to
// notice that the render and the file disagree.
func TestFeatureNotes_SayWhatTheScreenCannotShow(t *testing.T) {
	d := plated()
	d.Features = []geometry.Feature{
		{ID: "bore", Op: "cut", Of: "plate", With: []string{"hole"}},
		{ID: "round", Op: "fillet", Of: "plate", RadiusFrom: "fillet_radius", Edges: "vertical"},
	}
	notes := strings.Join(d.FeatureNotes(), " ")
	if !strings.Contains(notes, "cannot cut a hole") {
		t.Errorf("nothing says the viewport cannot draw the cut: %q", notes)
	}
	if !strings.Contains(notes, "Hole") || !strings.Contains(notes, "Plate") {
		t.Errorf("the note does not name the parts involved: %q", notes)
	}
	if !strings.Contains(notes, "drawn square on screen") {
		t.Errorf("nothing says the fillet is not drawn: %q", notes)
	}
	// And a document with no features says nothing at all.
	if n := plated().FeatureNotes(); len(n) != 0 {
		t.Errorf("a document with no features produced notes: %v", n)
	}
}
