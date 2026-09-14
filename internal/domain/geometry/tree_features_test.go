package geometry

import (
	"reflect"
	"strings"
	"testing"
)

// Features declared on an assembly are written out, per occurrence, as ordinary
// features naming the parts that occurrence placed. Phase 1, stage D1e of
// docs/plan-2026-09-13-millions-of-parts.md.

func featureLines(fs []Feature) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.ID+": "+f.Op+" "+f.Of+" with "+strings.Join(f.With, ","))
	}
	return out
}

func cube(id string) Part {
	return Part{ID: id, Shape: "box", Size: map[string]float64{"width": 2, "height": 2, "depth": 2}}
}

func weldedWheels(occurrences ...string) Document {
	hub := Part{ID: "hub", Shape: "cylinder", Size: map[string]float64{"radius": 20, "height": 10}}
	spoke := Part{ID: "spoke", Shape: "box", Size: map[string]float64{"width": 4, "height": 4, "depth": 60},
		Position: []float64{0, 0, 30}, Repeat: &Repeat{Count: 3, About: "y"}}
	car := Assembly{ID: "car"}
	for i, id := range occurrences {
		car.Children = append(car.Children, Child{ID: id, Ref: "wheel", Position: []float64{float64(i) * 200, 0, 0}})
	}
	return Document{Name: "car", Units: "mm", Definitions: []Part{hub, spoke}, Root: "car",
		Assemblies: []Assembly{car, {ID: "wheel",
			Children: []Child{{ID: "hub", Ref: "hub"}, {ID: "spoke", Ref: "spoke"}},
			Features: []Feature{{ID: "weld", Op: "fuse", Of: "hub", With: []string{"spoke"}}}}}}
}

func TestTreeFeature_AWeldDeclaredOnceIsAppliedInEveryOccurrence(t *testing.T) {
	d := weldedWheels("fl", "fr")
	for _, p := range d.TreeProblems() {
		if p.Severity == Error {
			t.Fatalf("problem: %s %s", p.Name, p.Detail)
		}
	}
	e := d.Expanded()
	want := []string{
		"fl/weld: fuse fl/hub with fl/spoke-1,fl/spoke-2,fl/spoke-3",
		"fr/weld: fuse fr/hub with fr/spoke-1,fr/spoke-2,fr/spoke-3",
	}
	if got := featureLines(e.Features); !reflect.DeepEqual(got, want) {
		t.Fatalf("features\n got %q\nwant %q", got, want)
	}
	if _, problems := e.Operations(); len(problems) != 0 {
		t.Fatalf("the written-out welds do not check: %+v", problems)
	}
}

// Every kind of path names the parts beneath it, at any depth.
func TestTreeFeature_APathNamesAPlacementAtAnyDepth(t *testing.T) {
	rivet := cube("rivet")
	rivet.Repeat = &Repeat{Count: 2, Offset: []float64{5, 0, 0}}
	panel := func(f Feature) Document {
		return Document{Name: "p", Units: "mm", Root: "panel", Definitions: []Part{cube("plate"), rivet, cube("pin")},
			Assemblies: []Assembly{
				{ID: "panel", Children: []Child{
					{ID: "plate", Ref: "plate"},
					{ID: "row", Ref: "rivet", Pattern: &Pattern{Kind: "linear", Count: 2, Offset: []float64{0, 10, 0}}},
					{ID: "clip", Ref: "clip", Position: []float64{0, 0, 20}},
				}, Features: []Feature{f}},
				{ID: "clip", Children: []Child{{ID: "pin", Ref: "pin"}, {ID: "tab", Ref: "plate"}}},
			}}
	}
	for _, tc := range []struct {
		name, of string
		with     []string
		want     string
	}{
		{"a child", "plate", []string{"clip"}, "f: fuse plate with clip/pin,clip/tab"},
		{"a path into a sub-assembly", "plate", []string{"clip/pin"}, "f: fuse plate with clip/pin"},
		{"a whole pattern of repeated parts", "plate", []string{"row"}, "f: fuse plate with row-1-1,row-1-2,row-2-1,row-2-2"},
		{"one pattern copy", "plate", []string{"row-2"}, "f: fuse plate with row-2-1,row-2-2"},
		{"one repeat copy inside one pattern copy", "plate", []string{"row-2-1"}, "f: fuse plate with row-2-1"},
		{"`of` naming a group takes its first part", "row", []string{"plate"}, "f: fuse row-1-1 with plate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := panel(Feature{ID: "f", Op: "fuse", Of: tc.of, With: tc.with}).Expanded()
			if got := featureLines(e.Features); len(got) != 1 || got[0] != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// Top-level features come first and stay as written; an inner assembly's features
// are written out before the assembly that places it, so its welds exist first.
func TestTreeFeature_InnerFeaturesComeBeforeOuterOnes(t *testing.T) {
	d := Document{Name: "p", Units: "mm", Root: "panel",
		Parts:       []Part{cube("base")},
		Features:    []Feature{{ID: "top", Op: "fillet", Of: "base", Radius: 0.2}},
		Definitions: []Part{cube("plate"), cube("pin")},
		Assemblies: []Assembly{
			{ID: "panel", Children: []Child{{ID: "plate", Ref: "plate"}, {ID: "clip", Ref: "clip"}},
				Features: []Feature{{ID: "outer", Op: "fillet", Of: "plate", Radius: 0.2}}},
			{ID: "clip", Children: []Child{{ID: "pin", Ref: "pin"}},
				Features: []Feature{{ID: "inner", Op: "fillet", Of: "pin", Radius: 0.2}}},
		}}
	var ids []string
	for _, f := range d.Expanded().Features {
		ids = append(ids, f.ID)
	}
	if want := []string{"top", "clip/inner", "outer"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("feature order %q, want %q", ids, want)
	}
}

func TestTreeFeature_RefusesAPathThatPlacesNothing(t *testing.T) {
	d := weldedWheels("fl")
	d.Assemblies[1].Features = []Feature{
		{ID: "lost", Op: "fuse", Of: "nowhere", With: []string{"spoke"}},
		{ID: "stray", Op: "fuse", Of: "hub", With: []string{"spoke-1", "ghost"}},
		{ID: "kept", Op: "fillet", Of: "hub", Radius: 1},
	}
	var details []string
	for _, p := range d.TreeProblems() {
		if p.Severity == Error {
			details = append(details, p.Name+" "+p.Detail)
		}
	}
	joined := strings.Join(details, "\n")
	for _, want := range []string{
		`fl/lost applies to "nowhere", which names nothing assembly "wheel" places`,
		`fl/stray uses "ghost" as a tool, which names nothing assembly "wheel" places`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("want a problem %q, got:\n%s", want, joined)
		}
	}
	if got := featureLines(d.Expanded().Features); !reflect.DeepEqual(got, []string{"fl/kept: fillet fl/hub with "}) {
		t.Errorf("a refused feature was written out, or a good one was dropped with it: %q", got)
	}
}

// The checks every feature already gets reach tree features: a tool consumed by
// an inner assembly's cut cannot be used again by the outer one.
func TestTreeFeature_ATreeFeatureIsCheckedLikeAnyOther(t *testing.T) {
	d := Document{Name: "p", Units: "mm", Root: "panel", Definitions: []Part{cube("plate"), cube("pin"), cube("tab")},
		Assemblies: []Assembly{
			{ID: "panel", Children: []Child{{ID: "plate", Ref: "plate"}, {ID: "clip", Ref: "clip"}},
				Features: []Feature{{ID: "outer", Op: "cut", Of: "plate", With: []string{"clip/pin"}}}},
			{ID: "clip", Children: []Child{{ID: "pin", Ref: "pin"}, {ID: "tab", Ref: "tab"}},
				Features: []Feature{{ID: "inner", Op: "cut", Of: "tab", With: []string{"pin"}}}},
		}}
	found := false
	for _, p := range d.Faults() {
		found = found || (p.Name == "outer" && strings.Contains(p.Detail, "already consumed"))
	}
	if !found {
		t.Errorf("the outer cut reusing a consumed tool was not reported: %+v", d.Faults())
	}
}

// Expansion appends the tree's features to a COPY of the document's own.
func TestTreeFeature_ExpansionDoesNotWriteIntoTheCallersFeatures(t *testing.T) {
	d := weldedWheels("fl")
	d.Features = make([]Feature, 0, 4)
	d.Features = append(d.Features, Feature{ID: "top", Op: "fillet", Of: "fl/hub", Radius: 1})
	_ = d.Expanded()
	if spare := d.Features[:2][1]; spare.ID != "" {
		t.Errorf("expanding wrote %q into the spare capacity of the document's own features", spare.ID)
	}
}

func TestTreeFeature_ACloneDoesNotShareAnAssemblysFeatures(t *testing.T) {
	d := weldedWheels("fl")
	c := d.clone()
	c.Assemblies[1].Features[0].With[0] = "changed"
	if d.Assemblies[1].Features[0].With[0] != "spoke" {
		t.Error("changing a clone's assembly feature changed the original's")
	}
}
