package geometry

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"testing"
)

// Children written out one by one that could be one pattern. Phase 2, stage A3.

func plateOf(children ...Child) Document {
	return Document{Name: "plate", Units: "mm", Root: "plate",
		Definitions: []Part{{ID: "bolt", Shape: "cylinder", Size: map[string]float64{"radius": 3, "height": 20}},
			{ID: "nut", Shape: "box", Size: map[string]float64{"width": 10, "height": 5, "depth": 10}}},
		Assemblies: []Assembly{{ID: "plate", Children: children}}}
}

func placed(ref string, positions ...[]float64) []Child {
	out := make([]Child, len(positions))
	for i, p := range positions {
		out[i] = Child{ID: fmt.Sprintf("%s-%c", ref, 'a'+i), Ref: ref, Position: p}
	}
	return out
}

// placedSet is every placed part as a position and a rotation matrix, rounded and
// sorted, so two spellings of one design compare equal whatever their ids.
func placedSet(d Document) []string {
	var out []string
	for _, p := range d.PlacedParts() {
		at := placementOf(p.Position, p.Rotation, p.Mirrored)
		var b strings.Builder
		for _, v := range append(at.pos[:], at.m[:]...) {
			fmt.Fprintf(&b, "%.2f,", v+0)
		}
		out = append(out, strings.ReplaceAll(b.String(), "-0.00", "0.00"))
	}
	sort.Strings(out)
	return out
}

// asPattern replaces a run's children with the one patterned child the warning
// suggests, at the first one's place.
func asPattern(d Document, r Repetition) Document {
	var kept []Child
	for _, c := range d.Assemblies[0].Children {
		switch {
		case c.ID == r.Children[0]:
			p := r.Pattern
			c.Pattern = &p
			kept = append(kept, c)
		case strings.Contains(strings.Join(r.Children, "\x00")+"\x00", c.ID+"\x00"):
		default:
			kept = append(kept, c)
		}
	}
	d.Assemblies = []Assembly{{ID: d.Assemblies[0].ID, Children: kept}}
	return d
}

func onlyRepetition(t *testing.T, d Document) Repetition {
	t.Helper()
	got := d.EnumeratedRepetition()
	if len(got) != 1 {
		t.Fatalf("found %d runs; want 1: %+v", len(got), got)
	}
	return got[0]
}

func thePatternPlacesTheSameCopies(t *testing.T, d Document, r Repetition) {
	t.Helper()
	rewritten := asPattern(d, r)
	if a, b := placedSet(d), placedSet(rewritten); strings.Join(a, "\n") != strings.Join(b, "\n") {
		t.Errorf("the suggested pattern %+v does not place what the children did:\nwritten out:\n%s\nas the pattern:\n%s",
			r.Pattern, strings.Join(a, "\n"), strings.Join(b, "\n"))
	}
	if again := rewritten.EnumeratedRepetition(); len(again) != 0 {
		t.Errorf("the rewritten document is still flagged: %+v", again)
	}
}

// A straight row is found, and the warning names the assembly, the ref, the count
// and the pattern — which places exactly the copies the children did.
func TestRepetition_FindsAStraightRowAndTheExactPattern(t *testing.T) {
	d := plateOf(placed("bolt", []float64{5, 0, 0}, []float64{5, 0, 30}, []float64{5, 0, 60}, []float64{5, 0, 90}, []float64{5, 0, 120})...)
	r := onlyRepetition(t, d)
	if r.Assembly != "plate" || r.Ref != "bolt" || len(r.Children) != 5 || r.Pattern.Kind != "linear" ||
		r.Pattern.Count != 5 || fmt.Sprint(r.Pattern.Offset) != "[0 0 30]" {
		t.Errorf("found %+v", r)
	}
	for _, want := range []string{`Assembly "plate"`, `places "bolt" 5 times`, `"pattern": {"kind":"linear","count":5,"offset":[0,0,30]}`} {
		if !strings.Contains(r.Warning(), want) {
			t.Errorf("the warning does not say %s:\n%s", want, r.Warning())
		}
	}
	thePatternPlacesTheSameCopies(t, d, r)
}

// Rows and columns written row by row are one grid, not two rows.
func TestRepetition_FindsAGrid(t *testing.T) {
	var at [][]float64
	for r := 0; r < 2; r++ {
		for c := 0; c < 3; c++ {
			at = append(at, []float64{float64(c) * 20, 0, float64(r) * 25})
		}
	}
	d := plateOf(placed("nut", at...)...)
	r := onlyRepetition(t, d)
	if p := r.Pattern; p.Kind != "grid" || p.Rows != 2 || p.Columns != 3 ||
		fmt.Sprint(p.RowOffset) != "[0 0 25]" || fmt.Sprint(p.ColumnOffset) != "[20 0 0]" {
		t.Errorf("found %+v", r.Pattern)
	}
	thePatternPlacesTheSameCopies(t, d, r)
}

// A ring about the origin, turned with the circle and rounded to three decimals
// the way a model writes it, is one polar pattern.
func TestRepetition_FindsAPolarRing(t *testing.T) {
	var children []Child
	for k := 0; k < 6; k++ {
		turn := RotationMatrix(axisRotation("y", float64(k)*math.Pi/3))
		p := mulMatVec(turn, [3]float64{40, 0, 0})
		e := EulerDegreesFromMatrix(mulMat3(turn, RotationMatrix(degreesToRadians3([]float64{90, 0, 0}))))
		r3 := func(v float64) float64 { return math.Round(v*1000) / 1000 }
		children = append(children, Child{ID: fmt.Sprintf("bolt-%d", k), Ref: "bolt",
			Position: []float64{r3(p[0]), r3(p[1]), r3(p[2])}, Rotation: []float64{r3(e[0]), r3(e[1]), r3(e[2])}})
	}
	d := plateOf(children...)
	r := onlyRepetition(t, d)
	if p := r.Pattern; p.Kind != "polar" || p.Count != 6 || p.About != "y" || p.Angle != 0 || r.Unturned {
		t.Errorf("found %+v (unturned %v)", r.Pattern, r.Unturned)
	}
	thePatternPlacesTheSameCopies(t, d, r)

	// A quarter of a ring, all facing the same way: still a ring, and said so.
	var arc []Child
	for k := 0; k < 4; k++ {
		p := mulMatVec(RotationMatrix(axisRotation("z", float64(k)*math.Pi/6)), [3]float64{0, 50, 0})
		arc = append(arc, Child{ID: fmt.Sprintf("pin-%d", k), Ref: "bolt", Position: []float64{p[0], p[1], p[2]}})
	}
	r = onlyRepetition(t, plateOf(arc...))
	if p := r.Pattern; p.Kind != "polar" || p.About != "z" || math.Abs(p.Angle-90) > 1e-6 || !r.Unturned ||
		!strings.Contains(r.Warning(), "round about that axis") {
		t.Errorf("found %+v (unturned %v): %s", r.Pattern, r.Unturned, r.Warning())
	}
}

// Irregular spacing, too few, different refs and mirrors, and children that already
// carry a pattern are not flagged.
func TestRepetition_IrregularSpacingIsNotFlagged(t *testing.T) {
	for name, d := range map[string]Document{
		"one step off by a millimetre": plateOf(placed("bolt", []float64{0, 0, 0}, []float64{10, 0, 0}, []float64{20, 0, 0}, []float64{31, 0, 0}, []float64{45, 0, 0})...),
		"three in a row":               plateOf(placed("bolt", []float64{0, 0, 0}, []float64{10, 0, 0}, []float64{20, 0, 0})...),
		"a grid with one moved":        plateOf(placed("nut", []float64{0, 0, 0}, []float64{20, 0, 0}, []float64{40, 0, 0}, []float64{0, 0, 25}, []float64{20, 0, 25}, []float64{41, 0, 25})...),
		"a ring off its radius":        plateOf(placed("bolt", []float64{40, 0, 0}, []float64{0, 0, -40}, []float64{-40, 0, 0}, []float64{0, 0, 42})...),
		"alternating refs": plateOf(
			Child{ID: "a", Ref: "bolt", Position: []float64{0, 0, 0}}, Child{ID: "b", Ref: "nut", Position: []float64{10, 0, 0}},
			Child{ID: "c", Ref: "bolt", Position: []float64{20, 0, 0}}, Child{ID: "d", Ref: "nut", Position: []float64{30, 0, 0}}),
		"one of them mirrored": func() Document {
			c := placed("bolt", []float64{0, 0, 0}, []float64{10, 0, 0}, []float64{20, 0, 0}, []float64{30, 0, 0})
			c[2].Mirror = "x"
			return plateOf(c...)
		}(),
		"one of them turned": func() Document {
			c := placed("bolt", []float64{0, 0, 0}, []float64{10, 0, 0}, []float64{20, 0, 0}, []float64{30, 0, 0})
			c[3].Rotation = []float64{0, 5, 0}
			return plateOf(c...)
		}(),
		"already patterned": func() Document {
			c := placed("bolt", []float64{0, 0, 0}, []float64{10, 0, 0}, []float64{20, 0, 0}, []float64{30, 0, 0})
			for i := range c {
				c[i].Pattern = &Pattern{Kind: "linear", Count: 2, Offset: []float64{0, 5, 0}}
			}
			return plateOf(c...)
		}(),
	} {
		if got := d.EnumeratedRepetition(); len(got) != 0 {
			t.Errorf("%s is flagged: %s", name, got[0].Warning())
		}
	}
}

// Floating point is not irregularity: steps of 0.1 summed, and a row a kilometre
// from the origin.
func TestRepetition_ToleratesFloatingPoint(t *testing.T) {
	var small, far [][]float64
	x := 0.0
	for k := 0; k < 10; k++ {
		small = append(small, []float64{x, 0, 0})
		x += 0.1
		far = append(far, []float64{1e6 + float64(k)*12.5, -3e5, 7})
	}
	for name, at := range map[string][][]float64{"tenths": small, "far away": far} {
		if got := plateOf(placed("bolt", at...)...).EnumeratedRepetition(); len(got) != 1 || got[0].Pattern.Count != 10 {
			t.Errorf("%s: found %+v; want one row of ten", name, got)
		}
	}
}

// A group that is not one line, one grid or one ring is searched for straight runs,
// each reported on its own, in written order.
func TestRepetition_FindsEachStraightRunInAGroup(t *testing.T) {
	d := plateOf(placed("bolt",
		[]float64{0, 0, 0}, []float64{10, 0, 0}, []float64{20, 0, 0}, []float64{30, 0, 0},
		[]float64{0, 50, 0}, []float64{0, 50, 7}, []float64{0, 50, 14}, []float64{0, 50, 21}, []float64{0, 50, 28})...)
	got := d.EnumeratedRepetition()
	if len(got) != 2 || got[0].Pattern.Count != 4 || got[1].Pattern.Count != 5 || got[1].Children[0] != "bolt-e" {
		t.Fatalf("found %+v", got)
	}
}

// The same document gives the same warnings in the same order: assemblies as they
// are written, groups by their first child, runs in written order.
func TestRepetition_IsDeterministic(t *testing.T) {
	row := func(ref string, y float64) []Child {
		c := placed(ref, []float64{0, y, 0}, []float64{10, y, 0}, []float64{20, y, 0}, []float64{30, y, 0})
		for i := range c {
			c[i].ID = fmt.Sprintf("%s-%v-%d", ref, y, i)
		}
		return c
	}
	d := plateOf()
	d.Assemblies = nil
	for _, id := range []string{"zeta", "alpha", "mid"} {
		var children []Child
		for _, ref := range []string{"nut", "bolt", "washer", "pin", "clip"} {
			children = append(children, row(ref, float64(len(id)*len(ref)))...)
		}
		d.Assemblies = append(d.Assemblies, Assembly{ID: id, Children: children})
	}
	first := d.EnumeratedRepetition()
	if len(first) != 15 || first[0].Assembly != "zeta" || first[0].Ref != "nut" || first[14].Assembly != "mid" || first[14].Ref != "clip" {
		t.Fatalf("order is not the document's: %d runs, first %+v", len(first), first)
	}
	want, _ := json.Marshal(first)
	for i := 0; i < 50; i++ {
		if got, _ := json.Marshal(d.EnumeratedRepetition()); string(got) != string(want) {
			t.Fatalf("run %d found a different answer:\n%s\n%s", i, got, want)
		}
	}
}

// ---- Top-level parts of a flat document (added 2026-09-15) ----------------------

// boltPart is one bolt as a flat document writes it: its own name and note, which
// are not what makes two bolts different.
func boltPart(id string, position, rotation []float64) Part {
	return Part{ID: id, Name: "Bolt " + id, Note: "the bolt called " + id, Shape: "cylinder",
		Size: map[string]float64{"radius": 3, "height": 20}, Position: position, Rotation: rotation}
}

func flatRow(positions ...[]float64) Document {
	d := Document{Name: "rail", Units: "mm"}
	for i, p := range positions {
		d.Parts = append(d.Parts, boltPart(fmt.Sprintf("bolt-%c", 'a'+i), p, []float64{0, 0, 0}))
	}
	return d
}

// builtSet is every part a document builds — repeats written out — as a position
// and a rotation matrix, rounded and sorted.
func builtSet(d Document) []string {
	var out []string
	for _, p := range d.Expanded().Parts {
		at := placementOf(p.Position, p.Rotation, p.Mirrored)
		var b strings.Builder
		for _, v := range append(at.pos[:], at.m[:]...) {
			fmt.Fprintf(&b, "%.2f,", v+0)
		}
		out = append(out, strings.ReplaceAll(b.String(), "-0.00", "0.00"))
	}
	sort.Strings(out)
	return out
}

// asRepeat replaces a top-level run with the one repeated part the warning offers.
func asRepeat(d Document, r Repetition) Document {
	var kept []Part
	for _, p := range d.Parts {
		switch {
		case p.ID == r.Children[0]:
			rep := *r.Repeat
			p.Repeat = &rep
			kept = append(kept, p)
		case strings.Contains(strings.Join(r.Children, "\x00")+"\x00", p.ID+"\x00"):
		default:
			kept = append(kept, p)
		}
	}
	d.Parts = kept
	return d
}

func theRepeatWritesOutTheSameParts(t *testing.T, d Document, r Repetition) {
	t.Helper()
	if r.Repeat == nil {
		t.Fatalf("no repeat is offered for %+v", r)
	}
	rewritten := asRepeat(d, r)
	if a, b := builtSet(d), builtSet(rewritten); strings.Join(a, "\n") != strings.Join(b, "\n") {
		t.Errorf("the offered repeat %+v does not write out what the parts were:\nwritten out:\n%s\nas the repeat:\n%s",
			*r.Repeat, strings.Join(a, "\n"), strings.Join(b, "\n"))
	}
	if again := rewritten.EnumeratedRepetition(); len(again) != 0 {
		t.Errorf("the rewritten document is still flagged: %+v", again)
	}
}

// A flat document's parts written out in a row are found like children are, and the
// warning offers the "repeat" that writes out the same parts — checked by writing it
// out — and the assembly pattern as the other spelling.
func TestRepetition_FindsARowOfTopLevelPartsAndTheRepeatThatWritesThemOut(t *testing.T) {
	d := flatRow([]float64{0, 5, 0}, []float64{25, 5, 0}, []float64{50, 5, 0}, []float64{75, 5, 0}, []float64{100, 5, 0})
	r := onlyRepetition(t, d)
	if !r.TopLevel || r.Assembly != "" || r.Ref != "" || len(r.Children) != 5 || r.Pattern.Kind != "linear" ||
		r.Repeat == nil || r.Repeat.Count != 5 || fmt.Sprint(r.Repeat.Offset) != "[25 0 0]" {
		t.Fatalf("found %+v (repeat %+v)", r, r.Repeat)
	}
	for _, want := range []string{"Top-level parts bolt-a, bolt-b, bolt-c, bolt-d, bolt-e", "written out 5 times",
		`"repeat": {"count":5,"offset":[25,0,0]}`, `"definitions"`, `"pattern": {"kind":"linear","count":5,"offset":[25,0,0]}`} {
		if !strings.Contains(r.Warning(), want) {
			t.Errorf("the warning does not say %s:\n%s", want, r.Warning())
		}
	}
	theRepeatWritesOutTheSameParts(t, d, r)
}

// A ring of parts turned with the circle, rounded the way a model writes it, is one
// circular repeat about that axis.
func TestRepetition_FindsARingOfTopLevelParts(t *testing.T) {
	d := Document{Name: "hub", Units: "mm"}
	r3 := func(v float64) float64 { return math.Round(v*1000) / 1000 }
	for k := 0; k < 8; k++ {
		p := mulMatVec(RotationMatrix(axisRotation("y", float64(k)*math.Pi/4)), [3]float64{0, 0, 60})
		d.Parts = append(d.Parts, boltPart(fmt.Sprintf("spoke-%d", k), []float64{r3(p[0]), r3(p[1]), r3(p[2])}, []float64{0, float64(k) * 45, 0}))
	}
	r := onlyRepetition(t, d)
	if !r.TopLevel || r.Pattern.Kind != "polar" || r.Unturned || r.Repeat == nil || r.Repeat.About != "y" || r.Repeat.Count != 8 || r.Repeat.Angle != 0 {
		t.Fatalf("found %+v (repeat %+v)", r, r.Repeat)
	}
	theRepeatWritesOutTheSameParts(t, d, r)

	// The same ring with every spoke leaning 30° about x, turned with the circle as a
	// pattern turns a child. A repeat turns a copy by adding to its y angle, and a
	// turn about y applied before a lean about x is not the one applied after it — so
	// the repeat would lean each spoke a different way, no repeat is offered, and the
	// pattern is. (A lean about z commutes with that sum, and there the check finds
	// the repeat does place the same spokes and offers it: it is checked, not assumed.)
	tilted := Document{Name: "hub", Units: "mm"}
	lean := RotationMatrix(degreesToRadians3([]float64{30, 0, 0}))
	for k := 0; k < 8; k++ {
		turn := RotationMatrix(axisRotation("y", float64(k)*math.Pi/4))
		p := mulMatVec(turn, [3]float64{0, 0, 60})
		e := EulerDegreesFromMatrix(mulMat3(turn, lean))
		tilted.Parts = append(tilted.Parts, boltPart(fmt.Sprintf("spoke-%d", k), []float64{r3(p[0]), r3(p[1]), r3(p[2])},
			[]float64{r3(e[0]), r3(e[1]), r3(e[2])}))
	}
	r = onlyRepetition(t, tilted)
	if r.Pattern.Kind != "polar" || r.Unturned || r.Repeat != nil {
		t.Errorf("a leaning ring: found %+v, repeat %+v; want the polar pattern and no repeat", r.Pattern, r.Repeat)
	}
	if strings.Contains(r.Warning(), `"repeat": {`) || !strings.Contains(r.Warning(), `"pattern": {"kind":"polar"`) {
		t.Errorf("a leaning ring is offered a repeat that would lean its spokes the wrong way:\n%s", r.Warning())
	}
}

// A grid is found, and no repeat is offered for it, because a repeat is a line or a
// circle; the assembly pattern is.
func TestRepetition_ATopLevelGridIsOfferedThePatternNotARepeat(t *testing.T) {
	var at [][]float64
	for row := 0; row < 3; row++ {
		for col := 0; col < 4; col++ {
			at = append(at, []float64{float64(col) * 20, 0, float64(row) * 30})
		}
	}
	r := onlyRepetition(t, flatRow(at...))
	if !r.TopLevel || r.Pattern.Kind != "grid" || r.Pattern.Rows != 3 || r.Pattern.Columns != 4 || r.Repeat != nil {
		t.Fatalf("found %+v (repeat %+v)", r, r.Repeat)
	}
	for _, want := range []string{`not a grid`, `"pattern": {"kind":"grid","rows":3,"columns":4`} {
		if !strings.Contains(r.Warning(), want) {
			t.Errorf("the warning does not say %s:\n%s", want, r.Warning())
		}
	}
	if strings.Contains(r.Warning(), `"repeat": {`) {
		t.Errorf("a grid is offered a repeat, which cannot place one:\n%s", r.Warning())
	}
}

// Irregular spacing, too few, parts that differ in anything but place and name, and
// parts something else names by id are not flagged.
func TestRepetition_TopLevelPartsThatAreNotOneRunAreNotFlagged(t *testing.T) {
	row := func() Document {
		return flatRow([]float64{0, 0, 0}, []float64{10, 0, 0}, []float64{20, 0, 0}, []float64{30, 0, 0})
	}
	for name, d := range map[string]Document{
		"one step off by a millimetre": flatRow([]float64{0, 0, 0}, []float64{10, 0, 0}, []float64{20, 0, 0}, []float64{31, 0, 0}, []float64{45, 0, 0}),
		"three in a row":               flatRow([]float64{0, 0, 0}, []float64{10, 0, 0}, []float64{20, 0, 0}),
		"a ring off its radius": flatRow([]float64{40, 0, 0}, []float64{0, 0, -40}, []float64{-40, 0, 0},
			[]float64{0, 0, 42}),
		"one of them a different size": func() Document {
			d := row()
			d.Parts[2].Size = map[string]float64{"radius": 4, "height": 20}
			return d
		}(),
		"one of them another material": func() Document {
			d := row()
			d.Parts[1].Material = &Material{Name: "brass"}
			return d
		}(),
		"one of them mirrored": func() Document {
			d := row()
			d.Parts[3].Mirrored = true
			return d
		}(),
		"one of them turned": func() Document {
			d := row()
			d.Parts[3].Rotation = []float64{0, 5, 0}
			return d
		}(),
		"one of them is a feature's tool": func() Document {
			d := row()
			d.Features = []Feature{{ID: "hole", Op: "cut", Of: "plate", With: []string{"bolt-c"}}}
			return d
		}(),
		"one of them hidden in a state": func() Document {
			d := row()
			d.States = []AssemblyState{{ID: "open", Hidden: []string{"bolt-b"}}}
			return d
		}(),
		"one of them placed by an expression": func() Document {
			d := row()
			d.Parts[0].PositionFrom = map[string]string{"x": "0"}
			return d
		}(),
		"already repeats": func() Document {
			d := row()
			for i := range d.Parts {
				d.Parts[i].Repeat = &Repeat{Count: 2, Offset: []float64{0, 5, 0}}
			}
			return d
		}(),
	} {
		if got := d.EnumeratedRepetition(); len(got) != 0 {
			t.Errorf("%s is flagged: %s", name, got[0].Warning())
		}
	}
}

// Top-level parts are reported before any assembly — the order every reader sees
// parts in — and their groups in the order each group's first part is written.
func TestRepetition_TopLevelPartsComeFirstInWrittenOrder(t *testing.T) {
	d := plateOf(placed("bolt", []float64{0, 0, 0}, []float64{10, 0, 0}, []float64{20, 0, 0}, []float64{30, 0, 0})...)
	for i := 0; i < 4; i++ {
		d.Parts = append(d.Parts,
			Part{ID: fmt.Sprintf("nut-%d", i), Shape: "box", Size: map[string]float64{"width": 10, "height": 5, "depth": 10},
				Position: []float64{float64(i) * 15, 40, 0}},
			boltPart(fmt.Sprintf("pin-%d", i), []float64{float64(i) * 15, 80, 0}, nil))
	}
	got := d.EnumeratedRepetition()
	if len(got) != 3 || !got[0].TopLevel || got[0].Children[0] != "nut-0" || !got[1].TopLevel || got[1].Children[0] != "pin-0" ||
		got[2].TopLevel || got[2].Assembly != "plate" {
		t.Fatalf("found, in order: %+v", got)
	}
	want, _ := json.Marshal(got)
	for i := 0; i < 50; i++ {
		if again, _ := json.Marshal(d.EnumeratedRepetition()); string(again) != string(want) {
			t.Fatalf("run %d found a different answer:\n%s\n%s", i, again, want)
		}
	}
}
