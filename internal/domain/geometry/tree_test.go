package geometry

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

// Designs placed inside assemblies. docs/plan-2026-09-13-millions-of-parts.md, stage D1b.

func flatDoc() Document {
	return Document{Name: "flat", Units: "mm", Parts: []Part{{
		ID: "plate", Name: "Plate", Shape: "box",
		Size:     map[string]float64{"width": 60, "height": 6, "depth": 40},
		Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0},
	}}, Assumptions: []string{"a"}, NotVerified: []string{"b"}}
}

// A document with no tree is untouched, in memory and in what is stored.
func TestTree_AFlatDocumentIsUnchanged(t *testing.T) {
	d := flatDoc()
	before, _ := json.Marshal(d)
	e, problems := expandAssemblies(d)
	if len(problems) != 0 || !reflect.DeepEqual(e, d) {
		t.Fatalf("a flat document changed: problems=%v", problems)
	}
	after, _ := json.Marshal(d)
	if string(before) != string(after) || strings.Contains(string(after), "definitions") ||
		strings.Contains(string(after), "assemblies") || strings.Contains(string(after), "\"root\"") {
		t.Errorf("a flat document's JSON gained tree fields: %s", after)
	}
	if !d.HasGeometry() || len(d.PlacedParts()) != 1 {
		t.Error("a flat document reports no geometry")
	}
}

// damper placed in a corner placed in a car, every level moved and turned.
func carWithOneCorner() Document {
	return Document{Name: "car", Units: "mm",
		Definitions: []Part{{ID: "damper", Name: "Damper", Shape: "cylinder",
			Size:     map[string]float64{"radius": 20, "height": 300},
			Position: []float64{0, 50, 0}, Rotation: []float64{15, 0, 0}}},
		Assemblies: []Assembly{
			{ID: "car", Children: []Child{{ID: "front-left", Ref: "corner",
				Position: []float64{1000, 0, 500}, Rotation: []float64{0, 90, 0}}}},
			{ID: "corner", Children: []Child{{ID: "damper", Ref: "damper",
				Position: []float64{0, 200, 0}, Rotation: []float64{0, 0, 10}}}},
		},
		Root: "car"}
}

// transformed applies a part's placement to a point, the way place() does.
func transformed(v [3]float64, pos, rot []float64) [3]float64 {
	var p [3]float64
	copy(p[:], padTo3(pos))
	return translate(rotate(v, degreesToRadians3(rot)), p)
}

func TestTree_APartIsPlacedThroughEveryFrameAboveIt(t *testing.T) {
	d := carWithOneCorner()
	e, problems := expandAssemblies(d)
	if len(problems) != 0 {
		t.Fatalf("problems: %v", problems)
	}
	if len(e.Parts) != 1 || e.Parts[0].ID != "front-left/damper" {
		t.Fatalf("flattened to %+v", e.Parts)
	}
	got := e.Parts[0]
	for _, v := range [][3]float64{{0, 0, 0}, {20, 150, 0}, {-20, -150, 7}} {
		// Independently: definition frame, then the corner's child, then the car's child.
		inDef := transformed(v, []float64{0, 50, 0}, []float64{15, 0, 0})
		inCorner := transformed(inDef, []float64{0, 200, 0}, []float64{0, 0, 10})
		want := transformed(inCorner, []float64{1000, 0, 500}, []float64{0, 90, 0})
		have := transformed(v, got.Position, got.Rotation)
		for k := 0; k < 3; k++ {
			if math.Abs(have[k]-want[k]) > 1e-7 {
				t.Fatalf("point %v lands at %v, want %v", v, have, want)
			}
		}
	}
	if e.Root != "" || e.Assemblies != nil || e.Definitions != nil {
		t.Error("the flattened document still carries its tree, so expanding it again would place it twice")
	}
	again, _ := expandAssemblies(e)
	if !reflect.DeepEqual(again, e) {
		t.Error("expanding twice is not the same as expanding once")
	}
}

// One design placed twice is two parts, sharing nothing mutable.
func TestTree_OneDefinitionPlacedTwiceIsTwoIndependentParts(t *testing.T) {
	d := carWithOneCorner()
	d.Assemblies[0].Children = append(d.Assemblies[0].Children,
		Child{ID: "front-right", Ref: "corner", Position: []float64{-1000, 0, 500}})
	e, problems := expandAssemblies(d)
	if len(problems) != 0 || len(e.Parts) != 2 {
		t.Fatalf("parts=%d problems=%v", len(e.Parts), problems)
	}
	if e.Parts[0].ID != "front-left/damper" || e.Parts[1].ID != "front-right/damper" {
		t.Errorf("ids %q, %q", e.Parts[0].ID, e.Parts[1].ID)
	}
	e.Parts[0].Size["radius"] = 999
	if e.Parts[1].Size["radius"] != 20 || d.Definitions[0].Size["radius"] != 20 {
		t.Error("placements share a size map, so changing one part changes the other and the definition")
	}
}

// A definition's repeat is written out in the definition's own frame.
func TestTree_ARepeatInsideADefinitionTurnsWithThePlacement(t *testing.T) {
	d := Document{Name: "wheel", Units: "mm",
		Definitions: []Part{{ID: "spoke", Name: "Spoke", Shape: "box",
			Size:     map[string]float64{"width": 4, "height": 4, "depth": 60},
			Position: []float64{0, 0, 30}, Repeat: &Repeat{Count: 4, About: "y"}}},
		Assemblies: []Assembly{{ID: "car", Children: []Child{{ID: "wheel", Ref: "spoke", Name: "Spoke",
			Position: []float64{500, 300, 0}, Rotation: []float64{0, 0, 90}}}}},
		Root: "car"}
	e, problems := expandAssemblies(d)
	if len(problems) != 0 || len(e.Parts) != 4 {
		t.Fatalf("parts=%d problems=%v", len(e.Parts), problems)
	}
	local, _ := expandRepeats(Document{Parts: []Part{d.Definitions[0]}})
	for i, p := range e.Parts {
		if want := "wheel-" + string(rune('1'+i)); p.ID != want {
			t.Errorf("copy %d id %q, want %q", i, p.ID, want)
		}
		// The child's name, then the definition copy's own (NameSeparator).
		if want := "Spoke / Spoke " + string(rune('1'+i)); p.Name != want {
			t.Errorf("copy %d name %q, want %q", i, p.Name, want)
		}
		v := [3]float64{2, -1, 30}
		want := transformed(transformed(v, local.Parts[i].Position, local.Parts[i].Rotation),
			[]float64{500, 300, 0}, []float64{0, 0, 90})
		have := transformed(v, p.Position, p.Rotation)
		for k := 0; k < 3; k++ {
			if math.Abs(have[k]-want[k]) > 1e-7 {
				t.Fatalf("copy %d: point lands at %v, want %v — the pattern did not turn with the wheel", i, have, want)
			}
		}
	}
}

// Top-level parts and the tree coexist, top-level first.
func TestTree_TopLevelPartsComeFirst(t *testing.T) {
	d := carWithOneCorner()
	d.Parts = flatDoc().Parts
	placed := d.PlacedParts()
	if len(placed) != 2 || placed[0].ID != "plate" || placed[1].ID != "front-left/damper" {
		t.Fatalf("placed %+v", placed)
	}
	if !d.HasGeometry() {
		t.Error("HasGeometry is false")
	}
}

func TestTree_RefusesWhatCannotBePlaced(t *testing.T) {
	def := Part{ID: "block", Shape: "box", Size: map[string]float64{"width": 1, "height": 1, "depth": 1}}
	for _, tc := range []struct {
		name string
		doc  Document
		want string
	}{
		{"no root", Document{Definitions: []Part{def}}, "names no root"},
		{"root is not an assembly", Document{Definitions: []Part{def}, Root: "block"}, "is not an assembly"},
		{"unknown ref", Document{Assemblies: []Assembly{{ID: "a", Children: []Child{{ID: "c", Ref: "ghost"}}}}, Root: "a"},
			"neither a definition nor an assembly"},
		{"cycle", Document{Assemblies: []Assembly{
			{ID: "a", Children: []Child{{ID: "b", Ref: "b"}}},
			{ID: "b", Children: []Child{{ID: "a", Ref: "a"}}}}, Root: "a"}, "inside itself"},
		{"slash in a child id", Document{Definitions: []Part{def},
			Assemblies: []Assembly{{ID: "a", Children: []Child{{ID: "x/y", Ref: "block"}}}}, Root: "a"}, "contain no"},
		{"duplicate child ids", Document{Definitions: []Part{def},
			Assemblies: []Assembly{{ID: "a", Children: []Child{{ID: "c", Ref: "block"}, {ID: "c", Ref: "block"}}}}, Root: "a"},
			"two children"},
		{"a definition and an assembly share an id", Document{Definitions: []Part{def},
			Assemblies: []Assembly{{ID: "block", Children: nil}}, Root: "block"}, "both a definition and an assembly"},
		{"a definition defined twice", Document{Definitions: []Part{def, def},
			Assemblies: []Assembly{{ID: "a", Children: []Child{{ID: "c", Ref: "block"}}}}, Root: "a"}, "defined twice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			problems := tc.doc.TreeProblems()
			found := false
			for _, p := range problems {
				if p.Severity == Error && strings.Contains(p.Detail, tc.want) {
					found = true
				}
			}
			if !found {
				t.Errorf("want an error containing %q, got %+v", tc.want, problems)
			}
		})
	}
}

func TestTree_RefusesNestingDeeperThanTheLimit(t *testing.T) {
	var asms []Assembly
	for i := 0; i <= maxTreeDepth+1; i++ {
		id := "a" + strings.Repeat("x", i)
		next := "a" + strings.Repeat("x", i+1)
		asms = append(asms, Assembly{ID: id, Children: []Child{{ID: "c", Ref: next}}})
	}
	d := Document{Assemblies: asms, Root: "a"}
	for _, p := range d.TreeProblems() {
		if strings.Contains(p.Detail, "nests more than") {
			return
		}
	}
	t.Fatalf("a tree %d deep was not refused: %+v", maxTreeDepth+2, d.TreeProblems())
}

// Every reader sees what the tree places.
func TestTree_EveryReaderSeesTheTree(t *testing.T) {
	d := carWithOneCorner()
	d.Definitions[0].Color = "#ff0000"
	const id = "front-left/damper"

	if e := d.Expanded(); len(e.Parts) != 1 || e.Parts[0].ID != id {
		t.Errorf("Expanded: %+v", e.Parts)
	}
	solids, _, _, notes := SolidsAndOperations(d, Millimetre)
	if len(solids) != 1 || solids[0].ID != id {
		t.Errorf("the kernel would be sent %+v (notes %v)", solids, notes)
	}
	m := Tessellate(d, Millimetre)
	if len(m.Groups) != 1 || m.Groups[0].PartID != id || len(m.Groups[0].Triangles) == 0 {
		t.Errorf("the mesh has %d group(s)", len(m.Groups))
	}
	if faults := d.Faults(); len(faults) != 0 {
		t.Errorf("a sound tree reports faults: %+v", faults)
	}
	if len(Measure(d, Millimetre)) == 0 {
		t.Error("Measure reports nothing for a document that is only a tree")
	}
	if got, want := partColour(d, id), [3]float64{1, 0, 0}; got != want {
		t.Errorf("the placed damper is drawn in %v, want its definition's %v", got, want)
	}
	if err := ValidateStates([]AssemblyState{{Name: "no damper", Hidden: []string{id}}}, d.PlacedParts()); err != nil {
		t.Errorf("a state hiding a placed part was refused: %v", err)
	}
}

// A tree that cannot be placed is a fault, and the file says what is missing.
func TestTree_ABrokenTreeIsAFaultAndTheFileSaysSo(t *testing.T) {
	d := carWithOneCorner()
	d.Assemblies[1].Children[0].Ref = "ghost"
	found := false
	for _, f := range d.Faults() {
		if strings.Contains(f.Detail, "neither a definition nor an assembly") {
			found = true
		}
	}
	if !found {
		t.Errorf("an unknown ref is not a fault: %+v", d.Faults())
	}
	_, notes := Solids(d, Millimetre)
	if !strings.Contains(strings.Join(notes, " "), "not in this file") {
		t.Errorf("the export does not say a placement is missing: %v", notes)
	}
}

func TestTree_ACloneSharesNothingWithTheOriginal(t *testing.T) {
	d := carWithOneCorner()
	c := d.clone()
	c.Definitions[0].Size["radius"] = 999
	c.Assemblies[0].Children[0].Position[0] = -1
	if d.Definitions[0].Size["radius"] != 20 {
		t.Error("changing a clone's definition changed the original's")
	}
	if d.Assemblies[0].Children[0].Position[0] != 1000 {
		t.Error("changing a clone's placement changed the original's")
	}
}

// The storage door: a tree-only document is stored, a broken one is not, and ids
// must be unique across top-level parts and the tree together.
func TestTree_TheStorageDoorReadsThePlacedParts(t *testing.T) {
	base := func(doc Document) *NewVariant {
		doc.NotVerified = []string{"concept only"}
		return &NewVariant{InitiatorID: "u", Agent: "converse", Generator: "g",
			Inputs: map[string]any{}, Document: doc}
	}
	if err := base(carWithOneCorner()).Validate(); err != nil {
		t.Errorf("a document that is only a tree was refused: %v", err)
	}
	broken := carWithOneCorner()
	broken.Root = "nowhere"
	if err := base(broken).Validate(); err == nil {
		t.Error("a tree whose root does not exist was stored")
	}
	clash := carWithOneCorner()
	clash.Parts = []Part{{ID: "front-left/damper", Shape: "box"}}
	if err := base(clash).Validate(); err == nil || !strings.Contains(err.Error(), "appears twice") {
		t.Errorf("a top-level part sharing a placed part's id was stored: %v", err)
	}
}

// A definition's size follows its parameter, and every placement reads it.
func TestTree_BindEvaluatesADefinitionsSizesForEveryPlacement(t *testing.T) {
	d := carWithOneCorner()
	d.Assemblies[0].Children = append(d.Assemblies[0].Children,
		Child{ID: "front-right", Ref: "corner", Position: []float64{-1000, 0, 500}})
	d.Parameters = []Parameter{{Name: "damper_radius", Value: 25, Unit: "mm", How: Chosen}}
	d.Definitions[0].SizeFrom = map[string]string{"radius": "damper_radius"}
	for _, p := range d.Bind() {
		if p.Severity == Error {
			t.Fatalf("binding failed: %s %s", p.Name, p.Detail)
		}
	}
	if got := d.Definitions[0].Size["radius"]; got != 25 {
		t.Fatalf("the definition's radius is %v after binding, want 25", got)
	}
	for _, p := range d.Expanded().Parts {
		if p.Size["radius"] != 25 {
			t.Errorf("%s reads radius %v, want the bound 25", p.ID, p.Size["radius"])
		}
	}
}

// The door checks the parts a tree PLACES, not only the top-level ones: a
// definition with no shape is refused once a child places it. This is what keeps
// the "reads only top-level parts" drill honest now that the duplicate-id check
// reads every expanded part and would catch a tree clash on its own.
func TestTree_TheStorageDoorChecksThePartsATreePlaces(t *testing.T) {
	d := Document{Definitions: []Part{{ID: "blank", Size: map[string]float64{"width": 1}}},
		Assemblies: []Assembly{{ID: "root", Children: []Child{{ID: "slot", Ref: "blank"}}}}, Root: "root"}
	if err := store(d); err == nil || !strings.Contains(err.Error(), `part "slot / blank" has no shape`) {
		t.Errorf("a placed definition with no shape was stored: %v", err)
	}
}
