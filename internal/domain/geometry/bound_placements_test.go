package geometry_test

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// Fences for a tree's placements bound to parameters (2026-09-15, bound child
// positions), and for binding never writing into the document a copy was made from.
// docs/bugfix/2026-09-15-binding-a-document-rewrote-the-document-it-was-made-from.md

// boundCar is a root placing one corner design twice at ±half_track, the right side
// mirrored. Each corner declares a hub hub_reach outboard of its arm, and a tyre is
// attached at it.
func boundCar() *geometry.Document {
	return &geometry.Document{Name: "car", Units: "mm", Root: "car",
		Parameters: []geometry.Parameter{
			{Name: "half_track", Value: 800, Unit: "mm", How: geometry.Chosen},
			{Name: "hub_reach", Value: 160, Unit: "mm", How: geometry.Chosen},
		},
		Definitions: []geometry.Part{
			{ID: "arm", Shape: "box", Size: map[string]float64{"width": 300, "height": 30, "depth": 60},
				SizeFrom: map[string]string{"width": "hub_reach + 140"}},
			{ID: "tyre", Shape: "cylinder", Size: map[string]float64{"radius": 330, "height": 240}},
		},
		Assemblies: []geometry.Assembly{
			{ID: "corner",
				Interfaces: []geometry.Interface{{ID: "hub", Position: []float64{-160, 0, 0},
					PositionFrom: map[string]string{"x": "-hub_reach"}}},
				Children: []geometry.Child{{ID: "arm", Ref: "arm"}, {ID: "wheel", Ref: "tyre", At: "hub"}}},
			{ID: "car", Children: []geometry.Child{
				{ID: "left", Ref: "corner", Position: []float64{-800, 330, 0},
					PositionFrom: map[string]string{"x": "-half_track"}},
				{ID: "right", Ref: "corner", Position: []float64{800, 330, 0},
					PositionFrom: map[string]string{"x": "half_track"}, Mirror: "x"},
			}},
		}}
}

func placedPositions(d *geometry.Document) map[string][]float64 {
	out := map[string][]float64{}
	for _, p := range d.PlacedParts() {
		out[p.ID] = p.Position
	}
	return out
}

func sameXYZ(got []float64, want ...float64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if math.Abs(got[i]-want[i]) > 1e-9 {
			return false
		}
	}
	return true
}

func childOf(d *geometry.Document, asm, id string) geometry.Child {
	for _, a := range d.Assemblies {
		if a.ID != asm {
			continue
		}
		for _, c := range a.Children {
			if c.ID == id {
				return c
			}
		}
	}
	return geometry.Child{}
}

// ‼️ A respec moves a child and an interface bound to the parameters it changes, and
// so every part placed beneath them. #97 and #111 stored such a position as its number,
// and a respec moved the definitions and left the wheels where they were.
func TestBind_AChildsAndAnInterfacesPositionFollowTheirParameters(t *testing.T) {
	d := boundCar()
	if problems := d.Bind(); len(problems) != 0 {
		t.Fatalf("a document whose placements agree with their bindings reported: %+v", problems)
	}
	got, problems := d.WithParameters(map[string]float64{"half_track": 900, "hub_reach": 200})
	if len(problems) != 0 {
		t.Fatalf("the respec reported: %+v", problems)
	}
	placed := placedPositions(got)
	for id, want := range map[string][3]float64{
		"left/arm": {-900, 330, 0}, "left/wheel": {-1100, 330, 0}, "right/wheel": {1100, 330, 0},
	} {
		if !sameXYZ(placed[id], want[0], want[1], want[2]) {
			t.Errorf("%s is at %v after half_track = 900 and hub_reach = 200; want %v", id, placed[id], want)
		}
	}
	if x := childOf(got, "car", "left").Position[0]; x != -900 {
		t.Errorf("the stored child position is %v; bound to -half_track it is -900", x)
	}
	if x := got.Assemblies[0].Interfaces[0].Position[0]; x != -200 {
		t.Errorf("the stored hub position is %v; bound to -hub_reach it is -200", x)
	}
	if again := got.Bind(); len(again) != 0 {
		t.Errorf("the re-specified document disagrees with itself: %+v", again)
	}
	if w := placedPositions(d)["left/wheel"]; !sameXYZ(w, -960, 330, 0) {
		t.Errorf("the respec moved the document it came from: left/wheel is at %v", w)
	}
}

// The binding is stored: a document read back from its JSON carries it, stores the same
// bytes again, and a respec of what was read moves the child.
func TestBind_ABoundPlacementSurvivesStorageAndMovesWhenReadBack(t *testing.T) {
	d := boundCar()
	d.Bind()
	body, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"position_from":{"x":"-half_track"}`) {
		t.Fatalf("the child's binding is not in what is stored: %s", body)
	}
	var read geometry.Document
	if err := json.Unmarshal(body, &read); err != nil {
		t.Fatal(err)
	}
	if again, _ := json.Marshal(&read); string(again) != string(body) {
		t.Fatalf("the document read back stores different bytes.\n want %s\n  got %s", body, again)
	}
	got, problems := read.WithParameters(map[string]float64{"half_track": 700})
	if len(problems) != 0 {
		t.Fatalf("the respec reported: %+v", problems)
	}
	if w := placedPositions(got)["right/wheel"]; !sameXYZ(w, 860, 330, 0) {
		t.Errorf("right/wheel is at %v after half_track = 700 on the stored copy; want (860, 330, 0)", w)
	}
}

// A tree with no bound placement is the same bytes after Bind, and a respec changes only
// the parameter it was asked to: no "position_from" appears, and nothing else moves.
func TestBind_ATreeWithoutBoundPlacementsIsTheSameBytes(t *testing.T) {
	d := boundCar()
	d.Definitions[0].SizeFrom = nil
	d.Assemblies[0].Interfaces[0].PositionFrom = nil
	for i := range d.Assemblies[1].Children {
		d.Assemblies[1].Children[i].PositionFrom = nil
	}
	before, _ := json.Marshal(d)
	if problems := d.Bind(); len(problems) != 0 {
		t.Fatalf("binding reported: %+v", problems)
	}
	after, _ := json.Marshal(d)
	if string(after) != string(before) {
		t.Fatalf("binding changed a document with nothing bound.\n want %s\n  got %s", before, after)
	}
	if strings.Contains(string(after), "position_from") {
		t.Errorf("a document with nothing bound stores a position_from: %s", after)
	}
	v, problems := d.WithParameters(map[string]float64{"half_track": 900})
	if len(problems) != 0 {
		t.Fatalf("the respec reported: %+v", problems)
	}
	respecified, _ := json.Marshal(v)
	if want := strings.Replace(string(before), `"value":800`, `"value":900`, 1); string(respecified) != want {
		t.Errorf("a respec of a document with nothing bound changed more than the parameter.\n want %s\n  got %s",
			want, respecified)
	}
}

// A placement bound to something that does not evaluate keeps its number and is named
// the way an edit names it, in a part's words.
func TestBind_APlacementThatCannotBeBoundKeepsItsNumberAndSaysWhich(t *testing.T) {
	d := boundCar()
	d.Assemblies[1].Children[0].PositionFrom = map[string]string{"x": "-missing_thing"}
	d.Assemblies[0].Interfaces[0].PositionFrom = map[string]string{"w": "hub_reach"}
	problems := d.Bind()
	var child, iface bool
	for _, p := range problems {
		if p.Severity == geometry.Error && p.Name == "car/left" &&
			strings.Contains(p.Detail, "missing_thing") && strings.Contains(p.Detail, "on the placement was kept") {
			child = true
		}
		if p.Severity == geometry.Error && p.Name == "corner interface hub" && strings.Contains(p.Detail, "not an axis") {
			iface = true
		}
	}
	if !child || !iface {
		t.Errorf("the broken bindings were not named (child %v, interface %v): %+v", child, iface, problems)
	}
	if x := childOf(d, "car", "left").Position[0]; x != -800 {
		t.Errorf("a child whose binding does not evaluate moved to %v; its number is -800", x)
	}
}

// ‼️ Binding the document an edit produced leaves the model the edit was applied to
// alone. Edit.Apply copies lists and shares the size maps, positions and children inside
// them, so binding an edit that changed a parameter rewrote the base.
func TestBind_BindingAnEditedModelLeavesTheModelItWasMadeFromAlone(t *testing.T) {
	base := *boundCar()
	base.Parts = []geometry.Part{{ID: "badge", Shape: "box",
		Size: map[string]float64{"width": 10, "height": 10, "depth": 10}, SizeFrom: map[string]string{"width": "hub_reach / 16"},
		Position: []float64{0, 0, 800}, PositionFrom: map[string]string{"z": "half_track"}},
		{ID: "plate", Shape: "extrusion", Size: map[string]float64{"depth": 5}, Position: []float64{0, 0, 0},
			Profile: []geometry.Point{{X: 0, Y: 0}, {X: 160, XFrom: "hub_reach", Y: 0},
				{X: 160, XFrom: "hub_reach", Y: 20}, {X: 0, Y: 20}}}}
	baseBody, _ := json.Marshal(base)

	out, problems := geometry.Edit{Patch: &geometry.Document{Parameters: []geometry.Parameter{
		{Name: "half_track", Value: 900, Unit: "mm", How: geometry.Chosen},
		{Name: "hub_reach", Value: 200, Unit: "mm", How: geometry.Chosen},
	}}}.Apply(base)
	if len(problems) != 0 {
		t.Fatalf("the edit reported: %+v", problems)
	}
	// Warnings are expected: the edited model's numbers are the old parameters' until it
	// is bound, and Bind says so. Only an error would mean something was not bound.
	for _, p := range out.Bind() {
		if p.Severity == geometry.Error {
			t.Fatalf("binding the edited model refused %s: %s", p.Name, p.Detail)
		}
	}
	if got, _ := json.Marshal(base); string(got) != string(baseBody) {
		t.Errorf("binding the edited model rewrote the model it was applied to.\n want %s\n  got %s", baseBody, got)
	}
	if w := out.Definitions[0].Size["width"]; w != 340 {
		t.Errorf("the edited arm is %v wide; bound to hub_reach + 140 = 340", w)
	}
	if x := childOf(&out, "car", "left").Position[0]; x != -900 {
		t.Errorf("the edited left corner is at x = %v; want -900", x)
	}
	if z := out.Parts[0].Position[2]; z != 900 {
		t.Errorf("the edited badge is at z = %v; want 900", z)
	}
}

// ‼️ A respec leaves its source's outline alone. clone copied sizes and positions but
// shared a part's outline and holes, which Bind writes an expression's coordinates into.
func TestWithParameters_LeavesTheSourcesOutlineAlone(t *testing.T) {
	d := &geometry.Document{Name: "plate", Units: "mm",
		Parameters: []geometry.Parameter{{Name: "plate", Value: 60, Unit: "mm", How: geometry.Chosen}},
		Parts: []geometry.Part{{ID: "p", Shape: "extrusion", Size: map[string]float64{"depth": 5},
			Position: []float64{0, 0, 0},
			Profile: []geometry.Point{{X: 0, Y: 0}, {X: 60, XFrom: "plate", Y: 0},
				{X: 60, XFrom: "plate", Y: 20}, {X: 0, Y: 20}},
			Holes: [][]geometry.Point{{{X: 52, XFrom: "plate - 8", Y: 5}, {X: 56, XFrom: "plate - 4", Y: 5},
				{X: 56, XFrom: "plate - 4", Y: 9}, {X: 52, XFrom: "plate - 8", Y: 9}}},
		}}}
	if problems := d.Bind(); len(problems) != 0 {
		t.Fatalf("binding reported: %+v", problems)
	}
	got, problems := d.WithParameters(map[string]float64{"plate": 90})
	if len(problems) != 0 {
		t.Fatalf("the respec reported: %+v", problems)
	}
	if x := got.Parts[0].Profile[1].X; x != 90 {
		t.Errorf("the variant's outline corner is at x = %v; want 90", x)
	}
	if x := d.Parts[0].Profile[1].X; x != 60 {
		t.Errorf("the respec moved its SOURCE's outline corner to x = %v; it is 60", x)
	}
	if x := d.Parts[0].Holes[0][0].X; x != 52 {
		t.Errorf("the respec moved its SOURCE's hole to x = %v; it is 52", x)
	}
}

// A bound child is not offered as a pattern: a pattern would keep the first copy's place
// and drop the relationship for every other, as for a top-level part.
func TestRepetition_ABoundChildIsNotOfferedAsAPattern(t *testing.T) {
	rack := func(bound bool) geometry.Document {
		d := geometry.Document{Name: "rack", Units: "mm", Root: "rack",
			Parameters:  []geometry.Parameter{{Name: "pitch", Value: 100, Unit: "mm", How: geometry.Chosen}},
			Definitions: []geometry.Part{{ID: "slot", Shape: "box", Size: map[string]float64{"width": 20, "height": 20, "depth": 20}}},
			Assemblies: []geometry.Assembly{{ID: "rack", Children: []geometry.Child{
				{ID: "a", Ref: "slot", Position: []float64{0, 0, 0}},
				{ID: "b", Ref: "slot", Position: []float64{100, 0, 0}},
				{ID: "c", Ref: "slot", Position: []float64{200, 0, 0}},
				{ID: "d", Ref: "slot", Position: []float64{300, 0, 0}},
			}}}}
		if bound {
			for i, expr := range []string{"0 * pitch", "pitch", "2 * pitch", "3 * pitch"} {
				d.Assemblies[0].Children[i].PositionFrom = map[string]string{"x": expr}
			}
		}
		return d
	}
	offered := func(d geometry.Document) bool {
		for _, r := range d.EnumeratedRepetition() {
			if r.Assembly == "rack" {
				return true
			}
		}
		return false
	}
	if !offered(rack(false)) {
		t.Fatal("four evenly spaced unbound slots are not offered as a pattern; the fence below proves nothing")
	}
	if offered(rack(true)) {
		t.Error("four slots bound to the pitch are offered as a pattern that would drop the binding")
	}
}
