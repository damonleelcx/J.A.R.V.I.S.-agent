package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// What a build step on a tree is shown. Phase 2, stage A2 of
// docs/plan-2026-09-13-millions-of-parts.md.

// subsystems is a model of n two-part subsystems, sub-0 … sub-(n-1), each placed
// once by the root.
func subsystems(n int) *Prototype {
	doc := &geometry.Document{Name: "plant", Units: "mm", Root: "plant"}
	root := geometry.Assembly{ID: "plant"}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("sub-%d", i)
		a := geometry.Part{ID: id + "-a", Name: "Frame of " + id, Shape: "box", Size: map[string]float64{"width": 10, "height": 10, "depth": 10}}
		b := geometry.Part{ID: id + "-b", Name: "Cover of " + id, Shape: "box", Size: map[string]float64{"width": 10, "height": 10, "depth": 10}}
		doc.Definitions = append(doc.Definitions, a, b)
		doc.Assemblies = append(doc.Assemblies, geometry.Assembly{ID: id, Children: []geometry.Child{
			{ID: "a", Ref: a.ID}, {ID: "b", Ref: b.ID, Position: []float64{20, 0, 0}},
		}})
		root.Children = append(root.Children, geometry.Child{ID: "at-" + id, Ref: id, Position: []float64{float64(100 * i), 0, 0}})
	}
	doc.Assemblies = append(doc.Assemblies, root)
	return doc
}

// ‼️ The acceptance: the prompt a step is given stays the same size as the model
// grows. Ten subsystems, a hundred, a thousand — the step that builds sub-3 reads
// sub-3, while the whole model it used to be given grows a hundredfold.
func TestSubtreeModel_StaysTheSameSizeAsTheModelGrows(t *testing.T) {
	var views, wholes []int
	for _, n := range []int{10, 100, 1000} {
		doc := subsystems(n)
		view := SubtreeModel(doc, "sub-3")
		if view == "" {
			t.Fatalf("%d subsystems: no view of sub-3", n)
		}
		if strings.Contains(view, "sub-4-a") {
			t.Fatalf("%d subsystems: the view of sub-3 carries sub-4's parts", n)
		}
		views = append(views, len(view))
		wholes = append(wholes, len(CurrentModel(doc)))
	}
	t.Logf("view of one subsystem: %v bytes; whole model: %v bytes", views, wholes)
	// Only the count of other placements changes: 9, 99, 999.
	if spread := views[2] - views[0]; spread > 8 {
		t.Errorf("the view grew by %d bytes from 10 to 1000 subsystems (%v); it must stay the same size", spread, views)
	}
	if views[2] > maxStepContextBytes/16 {
		t.Errorf("the view of a two-part subsystem is %d bytes", views[2])
	}
	if wholes[2] < 50*wholes[0] {
		t.Errorf("the fixture does not grow (%v), so this proves nothing", wholes)
	}
}

// The focus comes with what it places and what it is attached at — a sibling's
// interfaces, but none of the sibling's parts.
func TestSubtreeModel_CarriesWhatTheFocusPlacesAndAttachesTo(t *testing.T) {
	box := func(id, name string) geometry.Part {
		return geometry.Part{ID: id, Name: name, Shape: "box", Size: map[string]float64{"width": 10, "height": 10, "depth": 10}}
	}
	doc := &geometry.Document{Name: "car", Units: "mm", Root: "car",
		Definitions: []geometry.Part{box("tyre", "Tyre"), box("hub-disc", "Hub disc"), box("rail", "Rail")},
		Assemblies: []geometry.Assembly{
			{ID: "hub", Children: []geometry.Child{{ID: "disc", Ref: "hub-disc"}}},
			{ID: "wheel", Children: []geometry.Child{{ID: "tyre", Ref: "tyre"}, {ID: "hub", Ref: "hub"}}},
			{ID: "frame", Interfaces: []geometry.Interface{{ID: "axle-left", Position: []float64{-500, 0, 0}}},
				Children: []geometry.Child{{ID: "rail", Ref: "rail"}}},
			{ID: "car", Interfaces: []geometry.Interface{{ID: "ground"}}, Children: []geometry.Child{
				{ID: "frame", Ref: "frame"},
				{ID: "fl", Ref: "wheel", At: "frame/axle-left"},
			}},
		}}

	view := SubtreeModel(doc, "wheel")
	for _, want := range []string{`"focus":"wheel"`, `"id":"tyre"`, `"id":"hub-disc"`, `"id":"hub"`, `"id":"fl"`, `"axle-left"`} {
		if !strings.Contains(view, want) {
			t.Errorf("the view of the wheel does not carry %s:\n%s", want, view)
		}
	}
	for _, unwanted := range []string{`"id":"rail"`, `"ground"`} {
		if strings.Contains(view, unwanted) {
			t.Errorf("the view of the wheel carries %s, which it neither contains nor attaches to:\n%s", unwanted, view)
		}
	}
}

// A step that creates an assembly is shown where it can attach and nothing else.
func TestSubtreeModel_ANewAssemblyIsShownWhereItCanAttach(t *testing.T) {
	doc := subsystems(20)
	for i := range doc.Assemblies {
		if doc.Assemblies[i].ID == "plant" {
			doc.Assemblies[i].Interfaces = []geometry.Interface{{ID: "roof-mount"}}
		}
	}
	view := SubtreeModel(doc, "roof")
	if !strings.Contains(view, `"new":true`) || !strings.Contains(view, "roof-mount") {
		t.Errorf("a new assembly's view does not say it is new or where it can attach:\n%s", view)
	}
	if strings.Contains(view, "sub-0-a") {
		t.Errorf("a new assembly's view carries another subsystem's parts:\n%s", view)
	}
}

// A flat document, or a step that names no assembly, has no view: it is shown
// whole, as before.
func TestSubtreeModel_AFlatDocumentIsShownWhole(t *testing.T) {
	if v := SubtreeModel(twoBoxes(), "anything"); v != "" {
		t.Errorf("a flat document produced a view: %s", v)
	}
	if v := SubtreeModel(subsystems(3), ""); v != "" {
		t.Errorf("a step that names no assembly produced a view: %s", v)
	}
}
