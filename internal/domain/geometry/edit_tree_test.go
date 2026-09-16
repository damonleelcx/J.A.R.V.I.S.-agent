package geometry

import (
	"strings"
	"testing"
)

// A prototype_edit changes a tree through its DESIGN: definitions and assemblies
// by id, one child of one assembly by "assembly-id/child-id". Decided 2026-09-14,
// stage D1f of docs/plan-2026-09-13-millions-of-parts.md.

func axleTree() Document {
	return Document{Name: "axle", Units: "mm", Root: "axle",
		Definitions: []Part{
			{ID: "hub", Shape: "cylinder", Size: map[string]float64{"radius": 20, "height": 10}},
			{ID: "spoke", Shape: "box", Size: map[string]float64{"width": 4, "height": 4, "depth": 60}, Position: []float64{0, 0, 30}},
			{ID: "axle-bar", Shape: "cylinder", Size: map[string]float64{"radius": 5, "height": 400}},
		},
		Assemblies: []Assembly{
			{ID: "wheel",
				Children: []Child{{ID: "hub", Ref: "hub"},
					{ID: "spoke", Ref: "spoke", Pattern: &Pattern{Kind: "polar", Count: 6, About: "y"}}},
				Features: []Feature{{ID: "weld", Op: "fuse", Of: "hub", With: []string{"spoke"}}}},
			{ID: "axle",
				Interfaces: []Interface{{ID: "left-end", Position: []float64{-200, 0, 0}}, {ID: "right-end", Position: []float64{200, 0, 0}}},
				Children: []Child{{ID: "bar", Ref: "axle-bar"},
					{ID: "left-wheel", Ref: "wheel", At: "left-end"},
					{ID: "right-wheel", Ref: "wheel", At: "right-end", Mirror: "x"}}},
		}}
}

func placedIDs(d Document) map[string]Part {
	out := map[string]Part{}
	for _, p := range d.PlacedParts() {
		out[p.ID] = p
	}
	return out
}

// Patching a definition changes every placement of it, and leaves the base alone.
func TestEdit_ADefinitionPatchedByIDChangesEveryPlacement(t *testing.T) {
	base := axleTree()
	hub := base.Definitions[0]
	hub.Size = map[string]float64{"radius": 25, "height": 10}
	out, problems := Edit{Patch: &Document{Definitions: []Part{hub}}}.Apply(base)
	if len(problems) != 0 {
		t.Fatalf("problems: %+v", problems)
	}
	placed := placedIDs(out)
	for _, id := range []string{"left-wheel/hub", "right-wheel/hub"} {
		if r := placed[id].Size["radius"]; r != 25 {
			t.Errorf("%s has radius %v after the definition was patched to 25", id, r)
		}
	}
	if base.Definitions[0].Size["radius"] != 20 {
		t.Error("the edit changed the base document's definition")
	}
}

// Patching an assembly replaces it whole, for every place it is used.
func TestEdit_AnAssemblyPatchedByIDIsReplacedWhole(t *testing.T) {
	base := axleTree()
	bare := Assembly{ID: "wheel", Children: []Child{{ID: "hub", Ref: "hub"}}}
	out, problems := Edit{Patch: &Document{Assemblies: []Assembly{bare}}}.Apply(base)
	if len(problems) != 0 {
		t.Fatalf("problems: %+v", problems)
	}
	if e := out.Expanded(); len(e.Features) != 0 || len(e.Parts) != 3 {
		t.Errorf("after replacing the wheel with a bare hub: %d parts, %d features; want 3 parts and no welds",
			len(e.Parts), len(e.Features))
	}
}

// One child goes, from one assembly, and the base keeps its children intact.
//
// The MIDDLE child, on purpose: filtering the base's children in place only shows
// when an element after the removed one is shifted into its slot. Removing the last
// child rewrites the earlier slots with their own values and proves nothing, which
// is how the first version of this fence stayed green under its drill.
func TestEdit_RemovesOneChildWithoutTouchingTheBase(t *testing.T) {
	base := axleTree()
	out, problems := Edit{Remove: Removals{Children: []string{"axle/left-wheel"}}}.Apply(base)
	if len(problems) != 0 {
		t.Fatalf("problems: %+v", problems)
	}
	right := false
	for id := range placedIDs(out) {
		if strings.HasPrefix(id, "left-wheel/") {
			t.Errorf("%s is still placed after removing axle/left-wheel", id)
		}
		right = right || strings.HasPrefix(id, "right-wheel/")
	}
	if !right {
		t.Error("removing the left wheel took the right wheel with it")
	}
	c := base.Assemblies[1].Children
	if len(c) != 3 || c[0].ID != "bar" || c[1].ID != "left-wheel" || c[2].ID != "right-wheel" {
		t.Errorf("removing a child rewrote the base's children in place: %+v", c)
	}
}

func TestEdit_RemovesADefinitionAndAnAssembly(t *testing.T) {
	base := axleTree()
	out, problems := Edit{Remove: Removals{Definitions: []string{"axle-bar"}, Assemblies: []string{"wheel"}}}.Apply(base)
	if len(problems) != 0 {
		t.Fatalf("problems: %+v", problems)
	}
	if len(out.Definitions) != 2 || len(out.Assemblies) != 1 || out.Assemblies[0].ID != "axle" {
		t.Errorf("definitions %d, assemblies %+v", len(out.Definitions), out.Assemblies)
	}
	if len(base.Definitions) != 3 || len(base.Assemblies) != 2 {
		t.Error("the removal changed the base")
	}
}

// Naming what is not there is an error, as it is for parts.
func TestEdit_RefusesToRemoveATreeElementThatIsNotThere(t *testing.T) {
	_, problems := Edit{Remove: Removals{
		Definitions: []string{"ghost"}, Assemblies: []string{"ghost"},
		Children: []string{"axle/ghost", "nowhere/bar", "no-separator"},
	}}.Apply(axleTree())
	joined := ""
	for _, p := range problems {
		joined += p.Detail + "\n"
	}
	for _, want := range []string{
		`cannot remove definition "ghost"`,
		`cannot remove assembly "ghost"`,
		`cannot remove child "ghost", which assembly "axle" does not place`,
		`there is no assembly "nowhere"`,
		`cannot remove child "no-separator": name it as "assembly-id/child-id"`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("want a problem containing %q, got:\n%s", want, joined)
		}
	}
}

func TestEdit_ATreeChangeIsNotAnEmptyEdit(t *testing.T) {
	for name, e := range map[string]Edit{
		"remove a definition": {Remove: Removals{Definitions: []string{"hub"}}},
		"remove an assembly":  {Remove: Removals{Assemblies: []string{"wheel"}}},
		"remove a child":      {Remove: Removals{Children: []string{"axle/bar"}}},
		"patch a definition":  {Patch: &Document{Definitions: []Part{{ID: "hub"}}}},
		"patch an assembly":   {Patch: &Document{Assemblies: []Assembly{{ID: "wheel"}}}},
		"patch the root":      {Patch: &Document{Root: "axle"}},
	} {
		if e.Empty() {
			t.Errorf("%s counts as changing nothing, so the turn would be refused as empty", name)
		}
	}
}

// Spans read definitions too, each in its own frame, and never group a definition
// with a top-level part.
func TestSpans_MeasuresDefinitionsInTheirOwnFrame(t *testing.T) {
	d := Document{Name: "motor", Units: "mm", Root: "mount",
		Parameters: []Parameter{{Name: "bolt_pitch", Value: 31, Unit: "mm", How: "chosen"}},
		Parts:      []Part{{ID: "marker", Shape: "box", PositionFrom: map[string]string{"x": "bolt_pitch / 2"}}},
		Definitions: []Part{
			{ID: "hole-a", Shape: "cylinder", PositionFrom: map[string]string{"x": "bolt_pitch / 2"}},
			{ID: "hole-b", Shape: "cylinder", PositionFrom: map[string]string{"x": "-bolt_pitch / 2"}},
		},
		Assemblies: []Assembly{{ID: "mount", Children: []Child{{ID: "a", Ref: "hole-a"}, {ID: "b", Ref: "hole-b"}}}},
	}
	spans := d.Spans()
	if len(spans) != 1 {
		t.Fatalf("want one span (the two holes), got %+v", spans)
	}
	s := spans[0]
	if s.Axis != "x" || s.Extent != 31 || strings.Join(s.Parts, ",") != "hole-a,hole-b" {
		t.Errorf("span %+v; want x, 31 mm, hole-a and hole-b", s)
	}
}
