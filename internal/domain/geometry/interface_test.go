package geometry

import (
	"math"
	"strings"
	"testing"
)

// Interfaces: a child attached `at` a named frame is measured in it. Every
// expectation below is composed by hand, in the order the decision states:
// (where the child is measured) ∘ (its pattern slot) ∘ (its own placement) ∘ (the
// definition's placement). Phase 1, stage D1d of
// docs/plan-2026-09-13-millions-of-parts.md.

func attachedPlacement(t *testing.T, d Document, id string) placement {
	t.Helper()
	for _, p := range d.TreeProblems() {
		if p.Severity == Error {
			t.Fatalf("problem: %s %s", p.Name, p.Detail)
		}
	}
	for _, p := range d.PlacedParts() {
		if p.ID == id {
			return placementOf(p.Position, p.Rotation, p.Mirrored)
		}
	}
	t.Fatalf("no part %q was placed", id)
	return placement{}
}

func sameAttachedPlacement(t *testing.T, label string, got, want placement) {
	t.Helper()
	for _, v := range [][3]float64{{0, 0, 0}, {2, -3, 4}, {-7, 1, 0.5}} {
		g, w := got.apply(v), want.apply(v)
		for k := 0; k < 3; k++ {
			if math.Abs(g[k]-w[k]) > 1e-7 {
				t.Fatalf("%s: point %v lands at %v, want %v", label, v, g, w)
			}
		}
	}
}

// childOwnPlacement is a child's own placement: position and rotation, then its mirror.
func childOwnPlacement(pos, rot []float64, mirror string) placement {
	p := placementOf(pos, rot, false)
	m, _ := reflectionAcross(mirror)
	p.m = mulMat3(p.m, m)
	return p
}

var blockOwnPlacement = placementOf([]float64{1, 2, 3}, []float64{10, 0, 0}, false) // block()'s own placement

func TestInterface_AChildSitsOnItsParentsInterface(t *testing.T) {
	d := Document{Name: "i", Units: "mm", Definitions: []Part{block("block")}, Root: "root",
		Assemblies: []Assembly{{ID: "root",
			Interfaces: []Interface{{ID: "mount", Position: []float64{10, 20, 30}, Rotation: []float64{0, 0, 90}}},
			Children: []Child{{ID: "c", Ref: "block", At: "mount",
				Position: []float64{5, 0, 0}, Rotation: []float64{0, 30, 0}, Mirror: "y"}}}}}
	want := placementOf([]float64{10, 20, 30}, []float64{0, 0, 90}, false).
		then(childOwnPlacement([]float64{5, 0, 0}, []float64{0, 30, 0}, "y")).then(blockOwnPlacement)
	sameAttachedPlacement(t, "c", attachedPlacement(t, d, "c"), want)
}

func TestInterface_APartMatesToASiblingsInterfaceThroughAMirror(t *testing.T) {
	d := Document{Name: "i", Units: "mm", Definitions: []Part{block("block")}, Root: "car",
		Assemblies: []Assembly{
			{ID: "car", Children: []Child{
				{ID: "fl", Ref: "corner", Position: []float64{100, 0, 50}, Rotation: []float64{0, 0, 30}, Mirror: "x"},
				{ID: "wheel", Ref: "block", At: "fl/hub", Position: []float64{0, 0, 2}},
			}},
			{ID: "corner", Interfaces: []Interface{{ID: "hub", Position: []float64{0, 40, 0}, Rotation: []float64{90, 0, 0}}},
				Children: []Child{{ID: "arm", Ref: "block"}}},
		}}
	fl := childOwnPlacement([]float64{100, 0, 50}, []float64{0, 0, 30}, "x")
	hub := placementOf([]float64{0, 40, 0}, []float64{90, 0, 0}, false)
	sameAttachedPlacement(t, "wheel", attachedPlacement(t, d, "wheel"), fl.then(hub).then(childOwnPlacement([]float64{0, 0, 2}, nil, "")).then(blockOwnPlacement))
	sameAttachedPlacement(t, "fl/arm", attachedPlacement(t, d, "fl/arm"), fl.then(blockOwnPlacement))
}

// The sibling's own attachment is part of where its interface is.
func TestInterface_AChainOfAttachmentsTwoLevelsDown(t *testing.T) {
	d := Document{Name: "i", Units: "mm", Definitions: []Part{block("block")}, Root: "car",
		Assemblies: []Assembly{
			{ID: "car", Children: []Child{
				{ID: "fl", Ref: "corner", Position: []float64{100, 0, 0}, Rotation: []float64{0, 20, 0}},
				{ID: "wheel", Ref: "block", At: "fl/knuckle/hub", Position: []float64{0, 0, 9}},
			}},
			{ID: "corner", Interfaces: []Interface{{ID: "strut", Position: []float64{0, 0, 70}, Rotation: []float64{0, 45, 0}}},
				Children: []Child{{ID: "knuckle", Ref: "knuckle", At: "strut", Position: []float64{3, 0, 0}, Rotation: []float64{-10, 0, 5}}}},
			{ID: "knuckle", Interfaces: []Interface{{ID: "hub", Position: []float64{0, 15, 0}, Rotation: []float64{0, 0, -20}}},
				Children: []Child{{ID: "pin", Ref: "block"}}},
		}}
	fl := childOwnPlacement([]float64{100, 0, 0}, []float64{0, 20, 0}, "")
	strut := placementOf([]float64{0, 0, 70}, []float64{0, 45, 0}, false)
	knuckle := childOwnPlacement([]float64{3, 0, 0}, []float64{-10, 0, 5}, "")
	hub := placementOf([]float64{0, 15, 0}, []float64{0, 0, -20}, false)
	sameAttachedPlacement(t, "wheel", attachedPlacement(t, d, "wheel"),
		fl.then(strut).then(knuckle).then(hub).then(childOwnPlacement([]float64{0, 0, 9}, nil, "")).then(blockOwnPlacement))
	sameAttachedPlacement(t, "fl/knuckle/pin", attachedPlacement(t, d, "fl/knuckle/pin"), fl.then(strut).then(knuckle).then(blockOwnPlacement))
}

// A pattern at an interface turns about the interface's own axis.
func TestInterface_APatternIsMeasuredInTheInterfacesFrame(t *testing.T) {
	d := Document{Name: "i", Units: "mm", Definitions: []Part{block("block")}, Root: "root",
		Assemblies: []Assembly{{ID: "root",
			Interfaces: []Interface{{ID: "hub", Position: []float64{0, 0, 100}, Rotation: []float64{90, 0, 0}}},
			Children: []Child{{ID: "bolt", Ref: "block", At: "hub", Position: []float64{20, 0, 0},
				Pattern: &Pattern{Kind: "polar", Count: 4, About: "z"}}}}}}
	hub := placementOf([]float64{0, 0, 100}, []float64{90, 0, 0}, false)
	checkCopies(t, d, []string{"bolt-1", "bolt-2", "bolt-3", "bolt-4"}, func(n int) placement {
		return hub.then(placementOf(nil, []float64{0, 0, 90 * float64(n-1)}, false)).then(childOwnPlacement([]float64{20, 0, 0}, nil, ""))
	})
}

func TestInterface_APartMatesToOneCopyOfAPattern(t *testing.T) {
	d := Document{Name: "i", Units: "mm", Definitions: []Part{block("block")}, Root: "root",
		Assemblies: []Assembly{
			{ID: "root", Children: []Child{
				{ID: "bolts", Ref: "bolt", Pattern: &Pattern{Kind: "linear", Count: 3, Offset: []float64{0, 30, 0}}},
				{ID: "nut", Ref: "block", At: "bolts-2/seat", Rotation: []float64{0, 0, 15}},
			}},
			{ID: "bolt", Interfaces: []Interface{{ID: "seat", Position: []float64{0, 0, 5}}},
				Children: []Child{{ID: "shank", Ref: "block"}}},
		}}
	slot2 := placementOf([]float64{0, 30, 0}, nil, false)
	seat := placementOf([]float64{0, 0, 5}, nil, false)
	sameAttachedPlacement(t, "nut", attachedPlacement(t, d, "nut"), slot2.then(seat).then(childOwnPlacement(nil, []float64{0, 0, 15}, "")).then(blockOwnPlacement))
}

// The acceptance criterion of D1d: moving an interface moves every child attached
// to it, in every occurrence of the assembly that declares it — by exactly that
// occurrence's turn (and mirror) of the move — and nothing else.
func TestInterface_MovingAnInterfaceMovesEveryChildAttachedToIt(t *testing.T) {
	doc := func(hub []float64) Document {
		return Document{Name: "i", Units: "mm", Definitions: []Part{block("block")}, Root: "car",
			Assemblies: []Assembly{
				{ID: "car", Children: []Child{
					{ID: "fl", Ref: "corner", Position: []float64{100, 0, 0}, Rotation: []float64{0, 0, 90}},
					{ID: "fr", Ref: "corner", Position: []float64{-100, 0, 0}, Mirror: "x"},
					{ID: "wheel-l", Ref: "block", At: "fl/hub", Rotation: []float64{0, 90, 0}},
					{ID: "wheel-r", Ref: "block", At: "fr/hub", Rotation: []float64{0, 90, 0}},
				}},
				{ID: "corner", Interfaces: []Interface{{ID: "hub", Position: hub, Rotation: []float64{0, 0, 10}}},
					Children: []Child{{ID: "arm", Ref: "block"}, {ID: "tie", Ref: "block", At: "hub"}}},
			}}
	}
	delta := [3]float64{7, -3, 11}
	before := doc([]float64{0, 40, 0})
	after := doc([]float64{delta[0], 40 + delta[1], delta[2]})
	turned := map[string][9]float64{
		"wheel-l": childOwnPlacement(nil, []float64{0, 0, 90}, "").m, "fl/tie": childOwnPlacement(nil, []float64{0, 0, 90}, "").m,
		"wheel-r": childOwnPlacement(nil, nil, "x").m, "fr/tie": childOwnPlacement(nil, nil, "x").m,
	}
	for _, id := range []string{"wheel-l", "wheel-r", "fl/tie", "fr/tie", "fl/arm", "fr/arm"} {
		b, a := attachedPlacement(t, before, id), attachedPlacement(t, after, id)
		move := [3]float64{}
		if m, attached := turned[id]; attached {
			move = mulMatVec(m, delta)
		}
		for _, v := range [][3]float64{{0, 0, 0}, {2, -3, 4}} {
			pb, pa := b.apply(v), a.apply(v)
			for k := 0; k < 3; k++ {
				if math.Abs(pa[k]-pb[k]-move[k]) > 1e-7 {
					t.Fatalf("%s: point %v moved by %v, want %v", id, v,
						[3]float64{pa[0] - pb[0], pa[1] - pb[1], pa[2] - pb[2]}, move)
				}
			}
		}
	}
}

func TestInterface_RefusesWhatItCannotAttach(t *testing.T) {
	base := func() Document {
		return Document{Name: "i", Units: "mm", Definitions: []Part{block("block")}, Root: "car",
			Assemblies: []Assembly{
				{ID: "car", Interfaces: []Interface{{ID: "mount"}}, Children: []Child{
					{ID: "fl", Ref: "corner"},
					{ID: "plain", Ref: "block"},
					{ID: "bolts", Ref: "corner", Pattern: &Pattern{Kind: "linear", Count: 3, Offset: []float64{0, 0, 50}}},
				}},
				{ID: "corner", Interfaces: []Interface{{ID: "hub"}}, Children: []Child{{ID: "arm", Ref: "block"}}},
			}}
	}
	add := func(children ...Child) Document {
		d := base()
		d.Assemblies[0].Children = append(d.Assemblies[0].Children, children...)
		return d
	}
	for _, tc := range []struct {
		name    string
		doc     Document
		refused []string
		want    string
	}{
		{"an interface the parent does not declare", add(Child{ID: "c", Ref: "block", At: "nowhere"}), []string{"c"},
			`declares no interface "nowhere" (it declares mount)`},
		{"an interface the sibling's assembly does not declare", add(Child{ID: "c", Ref: "block", At: "fl/nowhere"}), []string{"c"},
			`assembly "corner" declares no interface "nowhere" (it declares hub)`},
		{"a sibling that does not exist", add(Child{ID: "c", Ref: "block", At: "ghost/hub"}), []string{"c"},
			`has no child "ghost"`},
		{"a sibling that places a part", add(Child{ID: "c", Ref: "block", At: "plain/hub"}), []string{"c"},
			"interfaces are declared on assemblies only"},
		{"a patterned sibling named without a copy", add(Child{ID: "c", Ref: "block", At: "bolts/hub"}), []string{"c"},
			`is a pattern of 3 copies; attach to one of them, "bolts-1" to "bolts-3"`},
		{"two children attached to each other", add(Child{ID: "x", Ref: "corner", At: "y/hub"}, Child{ID: "y", Ref: "corner", At: "x/hub"}),
			[]string{"x", "y"}, "leads back to"},
		{"a child attached to itself", add(Child{ID: "self", Ref: "corner", At: "self/hub"}), []string{"self"}, "leads back to"},
		{"an empty segment", add(Child{ID: "c", Ref: "block", At: "fl//hub"}), []string{"c"}, "has an empty segment"},
		{"an interface id with a separator", func() Document {
			d := base()
			d.Assemblies[1].Interfaces = append(d.Assemblies[1].Interfaces, Interface{ID: "a/b"})
			return d
		}(), nil, "must be non-empty and contain no"},
		{"two interfaces with one id", func() Document {
			d := base()
			d.Assemblies[1].Interfaces = append(d.Assemblies[1].Interfaces, Interface{ID: "hub"})
			return d
		}(), nil, `two interfaces with the id "hub"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			found := false
			for _, p := range tc.doc.TreeProblems() {
				found = found || (p.Severity == Error && strings.Contains(p.Detail, tc.want))
			}
			if !found {
				t.Errorf("want an error containing %q, got %+v", tc.want, tc.doc.TreeProblems())
			}
			placed := map[string]bool{}
			for _, p := range tc.doc.PlacedParts() {
				placed[strings.SplitN(p.ID, PathSeparator, 2)[0]] = true
			}
			for _, id := range tc.refused {
				if placed[id] {
					t.Errorf("the refused child %q still placed a part", id)
				}
			}
			if !placed["fl"] || !placed["plain"] {
				t.Errorf("a refusal took its siblings with it: placed %v", placed)
			}
		})
	}
}

func TestInterface_ACloneDoesNotShareAnInterface(t *testing.T) {
	d := Document{Name: "i", Units: "mm", Definitions: []Part{block("block")}, Root: "root",
		Assemblies: []Assembly{{ID: "root", Interfaces: []Interface{{ID: "mount", Position: []float64{1, 2, 3}}},
			Children: []Child{{ID: "c", Ref: "block", At: "mount"}}}}}
	c := d.clone()
	c.Assemblies[0].Interfaces[0].Position[0] = 99
	if d.Assemblies[0].Interfaces[0].Position[0] != 1 {
		t.Error("changing a clone's interface moved the original's")
	}
}
