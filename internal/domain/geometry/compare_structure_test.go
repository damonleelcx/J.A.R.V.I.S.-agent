package geometry

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// Comparing designs placed in assemblies (Phase 7, stage E2).
//
// Every fence here reads a car written the way the plan means a car to be written:
// one wheel definition placed at four corners, a bolt patterned five times round
// each corner's hub interface, and a spare. The part rows cannot see any of it —
// the document has no top-level parts — so each fence is about what the comparison
// says instead, and about what it must NOT do: report one edit once per place it
// shows up.

func carDoc() Document {
	return Document{
		Definitions: []Part{
			{ID: "wheel", Name: "Wheel", Shape: "cylinder", Size: map[string]float64{"radius": 300, "height": 30}},
			{ID: "bolt", Name: "Wheel bolt", Shape: "cylinder", Size: map[string]float64{"radius": 2, "height": 12}},
			{ID: "spare", Name: "Spare wheel", Shape: "cylinder", Size: map[string]float64{"radius": 300, "height": 30}},
		},
		Assemblies: []Assembly{
			{ID: "car", Name: "Car", Children: []Child{
				{ID: "front-left", Ref: "corner", Position: []float64{1200, 0, 800}},
				{ID: "front-right", Ref: "corner", Position: []float64{1200, 0, -800}, Mirror: "z"},
				{ID: "rear-left", Ref: "corner", Position: []float64{-1200, 0, 800}},
				{ID: "rear-right", Ref: "corner", Position: []float64{-1200, 0, -800}, Mirror: "z"},
				{ID: "spare", Ref: "spare", Position: []float64{-2000, 300, 0}},
			}},
			{ID: "corner", Name: "Corner",
				Interfaces: []Interface{{ID: "hub", Position: []float64{0, 0, 40}}},
				Features:   []Feature{{ID: "weld", Op: "fuse", Of: "wheel", With: []string{"bolt"}}},
				Children: []Child{
					{ID: "wheel", Ref: "wheel", At: "hub"},
					{ID: "bolt", Ref: "bolt", At: "hub", Position: []float64{50, 0, 0},
						Pattern: &Pattern{Kind: "polar", About: "z", Count: 5}},
				}},
		},
		Root: "car",
	}
}

func treeVariant(name string, units Unit, doc Document) Variant {
	v := variant(name, units)
	doc.Name, doc.Units, doc.NotVerified = name, string(units), []string{"nothing checked"}
	v.Document = doc
	return v
}

func definitionRow(t *testing.T, s *Structure, id string) DefinitionRow {
	t.Helper()
	for _, d := range s.Definitions {
		if d.ID == id {
			return d
		}
	}
	t.Fatalf("no definition row for %q in %+v", id, s.Definitions)
	return DefinitionRow{}
}

func assemblyRow(t *testing.T, s *Structure, id string) AssemblyRow {
	t.Helper()
	for _, a := range s.Assemblies {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("no assembly row for %q in %+v", id, s.Assemblies)
	return AssemblyRow{}
}

func memberRow(t *testing.T, rows []MemberRow, id string) MemberRow {
	t.Helper()
	for _, m := range rows {
		if m.ID == id {
			return m
		}
	}
	t.Fatalf("no row for %q in %+v", id, rows)
	return MemberRow{}
}

// The fixture is a car FORGE would build, not one it would refuse: a comparison of
// two broken trees would prove nothing about trees.
func TestCompare_TheCarFixtureIsATreeFORGEPlaces(t *testing.T) {
	d := carDoc()
	if p := d.TreeProblems(); len(p) != 0 {
		t.Fatalf("the fixture has tree problems: %+v", p)
	}
	if n := len(d.PlacedParts()); n != 25 {
		t.Fatalf("the fixture places %d parts; 4 wheels, 20 bolts and a spare are 25", n)
	}
}

// A comparison of flat documents is what it was before trees could be compared.
// Structure is nil, not empty — the wire leaves the section out on that, and
// httpapi's fence holds the response byte for byte.
func TestCompare_AFlatComparisonHasNoStructure(t *testing.T) {
	c := Compare([]Variant{
		variant("a", Millimetre, box60mm("plate", 60)),
		variant("b", Centimetre, box60mm("plate", 7.2)),
	})
	if c.Structure != nil {
		t.Fatalf("two flat documents grew a structure section: %+v", c.Structure)
	}
	if len(c.Parts) != 1 || !c.Parts[0].Differs() {
		t.Fatalf("the part rows changed: %+v", c.Parts)
	}
}

// The part rows are left exactly as they are when a variant is a tree: they read
// the top-level parts, and the tree's occurrences are never written into them.
func TestCompare_ATreeLeavesThePartRowsAsTheyWere(t *testing.T) {
	withTree := func(name string, width float64) Variant {
		d := carDoc()
		d.Parts = []Part{box60mm("plate", width)}
		return treeVariant(name, Millimetre, d)
	}
	flat := func(v Variant) Variant {
		v.Document.Definitions, v.Document.Assemblies, v.Document.Root = nil, nil, ""
		return v
	}
	a, b := withTree("a", 60), withTree("b", 72)
	trees := Compare([]Variant{a, b})
	plain := Compare([]Variant{flat(a), flat(b)})

	if !reflect.DeepEqual(trees.Parts, plain.Parts) {
		t.Fatalf("a tree changed the part rows.\n with tree: %+v\n    flat: %+v", trees.Parts, plain.Parts)
	}
	if !reflect.DeepEqual(trees.MatchNotes, plain.MatchNotes) || !reflect.DeepEqual(trees.NotComparable, plain.NotComparable) {
		t.Errorf("a tree changed the notes: %v / %v against %v / %v",
			trees.MatchNotes, trees.NotComparable, plain.MatchNotes, plain.NotComparable)
	}
	if trees.Structure == nil {
		t.Error("two trees were compared with no structure section")
	}
}

// The reason this stage exists. A wheel placed four times is ONE design, and
// changing it is one change, reported on the definition with the number of times
// each variant places it — not four part rows to be recognised as one edit.
func TestCompare_ChangingADefinitionPlacedFourTimesIsOneChangedDefinition(t *testing.T) {
	bigger := carDoc()
	bigger.Definitions[0].Size = map[string]float64{"radius": 320, "height": 30}

	c := Compare([]Variant{treeVariant("a", Millimetre, carDoc()), treeVariant("b", Millimetre, bigger)})
	if c.Structure == nil {
		t.Fatal("two trees were compared with no structure section")
	}
	var differing []string
	for _, d := range c.Structure.Definitions {
		if d.Differs() {
			differing = append(differing, d.ID)
		}
	}
	if !slices.Equal(differing, []string{"wheel"}) {
		t.Fatalf("changing the wheel made %v differ; it is one definition", differing)
	}
	wheel := definitionRow(t, c.Structure, "wheel")
	if !slices.Equal(wheel.Changed, []string{"size"}) {
		t.Errorf("the wheel's radius changed and the row names %v", wheel.Changed)
	}
	if !slices.Equal(wheel.Occurrences, []int{4, 4}) {
		t.Errorf("the wheel is placed 4 times on each side and the row counts %v", wheel.Occurrences)
	}
	for _, a := range c.Structure.Assemblies {
		if a.Differs() {
			t.Errorf("assembly %q is reported as changed; only a definition was edited: %+v", a.ID, a)
		}
	}
	if len(c.Parts) != 0 {
		t.Errorf("the tree's occurrences were written into %d part rows", len(c.Parts))
	}
}

// One more copy in a pattern is a change to the CHILD that patterns it, and it
// changes how many times the definition is placed — everywhere the corner is.
func TestCompare_APatternCopyChangesTheCountsAndTheChildsPattern(t *testing.T) {
	more := carDoc()
	more.Assemblies[1].Children[1].Pattern = &Pattern{Kind: "polar", About: "z", Count: 6}

	c := Compare([]Variant{treeVariant("a", Millimetre, carDoc()), treeVariant("b", Millimetre, more)})
	bolt := definitionRow(t, c.Structure, "bolt")
	if !slices.Equal(bolt.Occurrences, []int{20, 24}) {
		t.Errorf("5 then 6 bolts at each of 4 corners is 20 then 24; the row counts %v", bolt.Occurrences)
	}
	if len(bolt.Changed) != 0 || !bolt.Differs() {
		t.Errorf("the bolt itself is unchanged and placed more often: changed %v, differs %v", bolt.Changed, bolt.Differs())
	}
	child := memberRow(t, assemblyRow(t, c.Structure, "corner").Children, "bolt")
	if !slices.Equal(child.Changed, []string{"pattern"}) {
		t.Errorf("the bolt child's pattern changed and the row names %v", child.Changed)
	}
	if w := definitionRow(t, c.Structure, "wheel"); w.Differs() {
		t.Errorf("the wheel is reported as changed by the bolts' pattern: %+v", w)
	}
}

// An interface that moved is reported on the assembly that declares it. The
// children attached at it have not changed — their `at` still names it — and
// saying they had would send the reader to the wrong place.
func TestCompare_AMovedInterfaceIsReportedOnItsAssembly(t *testing.T) {
	moved := carDoc()
	moved.Assemblies[1].Interfaces = []Interface{{ID: "hub", Position: []float64{0, 0, 55}}}

	c := Compare([]Variant{treeVariant("a", Millimetre, carDoc()), treeVariant("b", Millimetre, moved)})
	corner := assemblyRow(t, c.Structure, "corner")
	hub := memberRow(t, corner.Interfaces, "hub")
	if !slices.Equal(hub.Changed, []string{"position"}) {
		t.Fatalf("the hub moved 15 mm and its row names %v", hub.Changed)
	}
	if !corner.Differs() {
		t.Error("the corner holds a moved interface and is reported as unchanged")
	}
	for _, ch := range corner.Children {
		if ch.Differs() {
			t.Errorf("child %q is attached at the hub and did not change, but is reported as %+v", ch.ID, ch)
		}
	}
	for _, d := range c.Structure.Definitions {
		if d.Differs() {
			t.Errorf("definition %q is reported as changed by a moved interface: %+v", d.ID, d)
		}
	}
}

// A definition or child in one variant only is the biggest difference there is.
func TestCompare_AddedAndRemovedDefinitionsAndChildrenAreReported(t *testing.T) {
	next := carDoc()
	next.Definitions = append(next.Definitions[:2:2],
		Part{ID: "mudguard", Name: "Mudguard", Shape: "box", Size: map[string]float64{"width": 400}})
	next.Assemblies[0].Children = next.Assemblies[0].Children[:4:4]
	next.Assemblies[1].Children = append(next.Assemblies[1].Children, Child{ID: "mudguard", Ref: "mudguard"})

	c := Compare([]Variant{treeVariant("a", Millimetre, carDoc()), treeVariant("b", Millimetre, next)})

	spare := definitionRow(t, c.Structure, "spare")
	if !slices.Equal(spare.MissingFrom, []int{2}) || !slices.Equal(spare.Occurrences, []int{1, 0}) {
		t.Errorf("the spare was removed: missing from %v, placed %v", spare.MissingFrom, spare.Occurrences)
	}
	mudguard := definitionRow(t, c.Structure, "mudguard")
	if !slices.Equal(mudguard.MissingFrom, []int{1}) || !slices.Equal(mudguard.Occurrences, []int{0, 4}) {
		t.Errorf("the mudguard was added at every corner: missing from %v, placed %v",
			mudguard.MissingFrom, mudguard.Occurrences)
	}
	if spareChild := memberRow(t, assemblyRow(t, c.Structure, "car").Children, "spare"); !slices.Equal(spareChild.MissingFrom, []int{2}) {
		t.Errorf("the spare's child was removed and its row says missing from %v", spareChild.MissingFrom)
	}
	if guardChild := memberRow(t, assemblyRow(t, c.Structure, "corner").Children, "mudguard"); !slices.Equal(guardChild.MissingFrom, []int{1}) {
		t.Errorf("the mudguard's child was added and its row says missing from %v", guardChild.MissingFrom)
	}
}

// A pattern on a sub-assembly multiplies everything beneath it, and an assembly
// placed from two paths is counted from both. Held to the expansion's own answer:
// the counts are worked out without placing anything, and a count that disagrees
// with what is placed is a count of something else.
func TestCompare_APatternedAssemblyMultipliesWhatItPlaces(t *testing.T) {
	doc := Document{
		Definitions: []Part{{ID: "pin", Shape: "cylinder", Size: map[string]float64{"radius": 1, "height": 5}}},
		Assemblies: []Assembly{
			{ID: "rack", Children: []Child{
				{ID: "row", Ref: "row", Pattern: &Pattern{Kind: "linear", Count: 3, Offset: []float64{0, 0, 40}}},
				{ID: "loose", Ref: "cell", Position: []float64{-100, 0, 0}},
			}},
			{ID: "row", Children: []Child{
				{ID: "cell", Ref: "cell", Pattern: &Pattern{Kind: "grid", Rows: 2, Columns: 5,
					RowOffset: []float64{0, 10, 0}, ColumnOffset: []float64{10, 0, 0}}},
			}},
			{ID: "cell", Children: []Child{
				{ID: "a", Ref: "pin"},
				{ID: "b", Ref: "pin", Position: []float64{3, 0, 0}},
			}},
		},
		Root: "rack",
	}
	c := Compare([]Variant{treeVariant("a", Millimetre, doc), treeVariant("b", Millimetre, doc)})
	pin := definitionRow(t, c.Structure, "pin")
	placed := len(doc.PlacedParts())
	if placed != 62 {
		t.Fatalf("the fixture places %d pins; 3 rows of 10 cells of 2, and one loose cell of 2, are 62", placed)
	}
	if !slices.Equal(pin.Occurrences, []int{placed, placed}) {
		t.Errorf("the tree places %d pins and the row counts %v", placed, pin.Occurrences)
	}
}

// The part rows' trap, in the tree: 60 mm and 6 cm are one width, and a width in
// no convertible unit is neither changed nor unchanged but said to be uncompared.
func TestCompare_ADefinitionIsComparedInMillimetres(t *testing.T) {
	plate := func(width float64) Document {
		return Document{
			Definitions: []Part{{ID: "plate", Shape: "box", Size: map[string]float64{"width": width}}},
			Assemblies:  []Assembly{{ID: "top", Children: []Child{{ID: "plate", Ref: "plate"}}}},
			Root:        "top",
		}
	}
	c := Compare([]Variant{
		treeVariant("a", Millimetre, plate(60)),
		treeVariant("b", Centimetre, plate(6)),
		treeVariant("c", UnitUnspecified, plate(60)),
	})
	row := definitionRow(t, c.Structure, "plate")
	if len(row.Changed) != 0 {
		t.Errorf("60 mm, 6 cm and an uncomparable 60 were reported as a change to %v", row.Changed)
	}
	joined := strings.Join(c.NotComparable, "\n")
	if !strings.Contains(joined, "definition plate") || !strings.Contains(joined, "columns 1 and 3") {
		t.Errorf("the width in no unit was not said to be uncompared: %v", c.NotComparable)
	}
	if strings.Contains(joined, "columns 1 and 2") {
		t.Errorf("millimetres and centimetres were said to be uncomparable: %v", c.NotComparable)
	}
}

// A definition's own repeat multiplies how many times it is placed, exactly as the
// tree places it, and a change to how many copies it makes is that count and not a
// "changed: repeat" beside an unchanged count (settled 2026-09-17; E2 left it for
// review). "repeat" still names a change to how the copies are laid out, and a
// change in copies nobody places, which no count can show.
func TestCompare_ADefinitionsOwnRepeatIsMultipliedIntoItsCount(t *testing.T) {
	boltCount := func(d Document) int {
		n := 0
		for _, p := range d.Expanded().Parts {
			if strings.Contains(p.ID, "/bolt") {
				n++
			}
		}
		return n
	}
	doubled := carDoc()
	doubled.Definitions[1].Repeat = &Repeat{Count: 2, Offset: []float64{0, 0, 20}}
	if placed := boltCount(doubled); placed != 40 {
		t.Fatalf("the fixture places %d bolts; 5 at each of 4 corners, each repeated twice, is 40", placed)
	}
	c := Compare([]Variant{treeVariant("a", Millimetre, carDoc()), treeVariant("b", Millimetre, doubled)})
	bolt := definitionRow(t, c.Structure, "bolt")
	if !slices.Equal(bolt.Occurrences, []int{20, 40}) {
		t.Errorf("20 bolts, then each repeated twice, are 20 then 40 as placed; the row counts %v", bolt.Occurrences)
	}
	if len(bolt.Changed) != 0 || !bolt.Differs() {
		t.Errorf("how many copies is the count, said once: changed %v, differs %v", bolt.Changed, bolt.Differs())
	}

	// The same number of copies laid out differently is a changed field with the same count.
	spread := carDoc()
	spread.Definitions[1].Repeat = &Repeat{Count: 2, Offset: []float64{0, 0, 35}}
	c = Compare([]Variant{treeVariant("a", Millimetre, doubled), treeVariant("b", Millimetre, spread)})
	bolt = definitionRow(t, c.Structure, "bolt")
	if !slices.Equal(bolt.Occurrences, []int{40, 40}) || !slices.Equal(bolt.Changed, []string{"repeat"}) {
		t.Errorf("two copies 20 mm apart then 35 mm apart: placed %v, changed %v; want 40, 40 and repeat",
			bolt.Occurrences, bolt.Changed)
	}

	// A definition nobody places: no count can show its copies, so the field says it,
	// in the table's order.
	unplaced := func(repeat *Repeat, radius float64) Document {
		d := carDoc()
		d.Definitions = append(d.Definitions, Part{ID: "washer", Shape: "cylinder",
			Size: map[string]float64{"radius": radius, "height": 2}, Repeat: repeat})
		return d
	}
	c = Compare([]Variant{
		treeVariant("a", Millimetre, unplaced(nil, 4)),
		treeVariant("b", Millimetre, unplaced(&Repeat{Count: 3, Offset: []float64{5, 0, 0}}, 5)),
	})
	washer := definitionRow(t, c.Structure, "washer")
	if !slices.Equal(washer.Occurrences, []int{0, 0}) || !slices.Equal(washer.Changed, []string{"size", "repeat"}) {
		t.Errorf("an unplaced washer made 3 times and wider: placed %v, changed %v; want 0, 0 and size, repeat",
			washer.Occurrences, washer.Changed)
	}
}
