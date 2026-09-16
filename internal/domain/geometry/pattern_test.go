package geometry

import (
	"math"
	"strings"
	"testing"
)

// Patterns on a placed child: every copy lands where an independent calculation
// puts it. Phase 1, stage D1c-2 of docs/plan-2026-09-13-millions-of-parts.md.

func block(id string) Part {
	return Part{ID: id, Name: "Block", Shape: "box",
		Size:     map[string]float64{"width": 4, "height": 6, "depth": 8},
		Position: []float64{1, 2, 3}, Rotation: []float64{10, 0, 0}}
}

func patterned(child Child, defs ...Part) Document {
	if len(defs) == 0 {
		defs = []Part{block("block")}
	}
	return Document{Name: "p", Units: "mm", Definitions: defs,
		Assemblies: []Assembly{{ID: "root", Children: []Child{child}}}, Root: "root"}
}

// checkCopies holds every placed copy to the independent world transform for copy n.
func checkCopies(t *testing.T, d Document, wantIDs []string, world func(n int) placement) {
	t.Helper()
	e, problems := expandAssemblies(d)
	for _, p := range problems {
		if p.Severity == Error {
			t.Fatalf("problem: %s %s", p.Name, p.Detail)
		}
	}
	if len(e.Parts) != len(wantIDs) {
		t.Fatalf("placed %d copies, want %d: %+v", len(e.Parts), len(wantIDs), e.Parts)
	}
	def := d.Definitions[0]
	for i, p := range e.Parts {
		if p.ID != wantIDs[i] {
			t.Errorf("copy %d id %q, want %q", i+1, p.ID, wantIDs[i])
		}
		got := placementOf(p.Position, p.Rotation, p.Mirrored)
		want := world(i + 1).then(placementOf(def.Position, def.Rotation, def.Mirrored))
		for _, v := range [][3]float64{{0, 0, 0}, {2, -3, 4}} {
			g, w := got.apply(v), want.apply(v)
			for k := 0; k < 3; k++ {
				if math.Abs(g[k]-w[k]) > 1e-7 {
					t.Fatalf("copy %d: point %v lands at %v, want %v", i+1, v, g, w)
				}
			}
		}
	}
}

func TestPattern_Linear(t *testing.T) {
	child := Child{ID: "rail", Ref: "block", Position: []float64{0, 10, 0}, Rotation: []float64{0, 0, 15},
		Pattern: &Pattern{Kind: "linear", Count: 4, Offset: []float64{25, 0, -3}}}
	checkCopies(t, patterned(child), []string{"rail-1", "rail-2", "rail-3", "rail-4"}, func(n int) placement {
		shift := placementOf([]float64{25 * float64(n-1), 0, -3 * float64(n-1)}, nil, false)
		return shift.then(placementOf([]float64{0, 10, 0}, []float64{0, 0, 15}, false))
	})
}

func TestPattern_PolarFullAndPartial(t *testing.T) {
	full := Child{ID: "bolt", Ref: "block", Position: []float64{50, 0, 0},
		Pattern: &Pattern{Kind: "polar", Count: 6, About: "y"}}
	checkCopies(t, patterned(full), []string{"bolt-1", "bolt-2", "bolt-3", "bolt-4", "bolt-5", "bolt-6"},
		func(n int) placement {
			turn := placementOf(nil, []float64{0, 60 * float64(n-1), 0}, false)
			return turn.then(placementOf([]float64{50, 0, 0}, nil, false))
		})
	partial := Child{ID: "vane", Ref: "block", Position: []float64{0, 40, 0},
		Pattern: &Pattern{Kind: "polar", Count: 5, About: "z", Angle: 90}}
	checkCopies(t, patterned(partial), []string{"vane-1", "vane-2", "vane-3", "vane-4", "vane-5"},
		func(n int) placement {
			turn := placementOf(nil, []float64{0, 0, 22.5 * float64(n-1)}, false)
			return turn.then(placementOf([]float64{0, 40, 0}, nil, false))
		})
}

func TestPattern_GridIsNumberedRowByRow(t *testing.T) {
	child := Child{ID: "cell", Ref: "block",
		Pattern: &Pattern{Kind: "grid", Rows: 2, Columns: 3, RowOffset: []float64{0, 0, 20}, ColumnOffset: []float64{15, 0, 0}}}
	checkCopies(t, patterned(child), []string{"cell-1", "cell-2", "cell-3", "cell-4", "cell-5", "cell-6"},
		func(n int) placement {
			r, c := float64((n-1)/3), float64((n-1)%3)
			return placementOf([]float64{15 * c, 0, 20 * r}, nil, false)
		})
}

// Eight copies along a 70 mm path with a corner at 30 mm: stations every 10 mm.
func TestPattern_PathSpacingAndAlignment(t *testing.T) {
	path := []Point{{X: 0, Y: 0}, {X: 30, Y: 0}, {X: 30, Y: 40}}
	stations := [][3]float64{{0, 0, 0}, {10, 0, 0}, {20, 0, 0}, {30, 0, 0}, {30, 10, 0}, {30, 20, 0}, {30, 30, 0}, {30, 40, 0}}
	ids := []string{"rivet-1", "rivet-2", "rivet-3", "rivet-4", "rivet-5", "rivet-6", "rivet-7", "rivet-8"}

	plain := Child{ID: "rivet", Ref: "block", Pattern: &Pattern{Kind: "path", Count: 8, Path: path}}
	checkCopies(t, patterned(plain), ids, func(n int) placement {
		return placementOf(stations[n-1][:], nil, false)
	})

	aligned := Child{ID: "rivet", Ref: "block", Pattern: &Pattern{Kind: "path", Count: 8, Path: path, Align: true}}
	checkCopies(t, patterned(aligned), ids, func(n int) placement {
		// On the first segment the path runs along +X; from the corner on, along +Y:
		// a quarter turn about z. The corner station takes the OUTGOING segment.
		turn := 0.0
		if n >= 4 {
			turn = 90
		}
		return placementOf(stations[n-1][:], []float64{0, 0, turn}, false)
	})
}

func TestPattern_ASubAssemblyIsPatternedWhole(t *testing.T) {
	d := Document{Name: "p", Units: "mm", Definitions: []Part{block("a"), block("b")},
		Assemblies: []Assembly{
			{ID: "root", Children: []Child{{ID: "cell", Ref: "pair",
				Pattern: &Pattern{Kind: "linear", Count: 3, Offset: []float64{0, 0, 40}}}}},
			{ID: "pair", Children: []Child{{ID: "a", Ref: "a"}, {ID: "b", Ref: "b", Position: []float64{12, 0, 0}}}},
		}, Root: "root"}
	e, _ := expandAssemblies(d)
	want := []string{"cell-1/a", "cell-1/b", "cell-2/a", "cell-2/b", "cell-3/a", "cell-3/b"}
	if len(e.Parts) != len(want) {
		t.Fatalf("placed %d parts", len(e.Parts))
	}
	for i, p := range e.Parts {
		if p.ID != want[i] {
			t.Errorf("part %d id %q, want %q", i, p.ID, want[i])
		}
	}
	if z := e.Parts[5].Position[2] - e.Parts[1].Position[2]; math.Abs(z-80) > 1e-9 {
		t.Errorf("the third copy of the pair sits %v above the first, want 80", z)
	}
}

func TestPattern_EveryCopyOfAMirroredChildIsMirrored(t *testing.T) {
	child := Child{ID: "arm", Ref: "block", Mirror: "y", Position: []float64{5, 0, 0},
		Pattern: &Pattern{Kind: "linear", Count: 3, Offset: []float64{0, 0, 30}}}
	e, _ := expandAssemblies(patterned(child))
	for _, p := range e.Parts {
		if !p.Mirrored {
			t.Errorf("%s is not mirrored", p.ID)
		}
	}
}

func TestPattern_CopiesOfANamedChildAreNumbered(t *testing.T) {
	child := Child{ID: "rail", Ref: "block", Name: "Rail",
		Pattern: &Pattern{Kind: "linear", Count: 2, Offset: []float64{10, 0, 0}}}
	e, _ := expandAssemblies(patterned(child))
	// Named by the occurrence path: the child's name and copy number, then the part's
	// own (docs/bugfix/2026-09-14-tree-copies-shared-display-names.md).
	if e.Parts[0].Name != "Rail 1 / Block" || e.Parts[1].Name != "Rail 2 / Block" {
		t.Errorf("names %q, %q", e.Parts[0].Name, e.Parts[1].Name)
	}
}

func TestPattern_RefusesWhatItCannotPlace(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pattern Pattern
		want    string
	}{
		{"unknown kind", Pattern{Kind: "spiral", Count: 3}, "the kinds are"},
		{"linear with no offset", Pattern{Kind: "linear", Count: 3}, "no usable offset"},
		{"polar about nothing", Pattern{Kind: "polar", Count: 3, About: "w"}, "turns about"},
		{"grid rows with no row offset", Pattern{Kind: "grid", Rows: 2, Columns: 1}, "row_offset"},
		{"grid of zero columns", Pattern{Kind: "grid", Rows: 2, Columns: 0}, "at least 1"},
		{"path of one point", Pattern{Kind: "path", Count: 3, Path: []Point{{X: 1}}}, "at least 2"},
		{"path with no length", Pattern{Kind: "path", Count: 3, Path: []Point{{X: 1}, {X: 1}}}, "no length"},
		{"path with a corner radius", Pattern{Kind: "path", Count: 3, Path: []Point{{X: 0}, {X: 10, Radius: 2}, {X: 10, Y: 5}}}, "corner radius"},
		{"path through an arc", Pattern{Kind: "path", Count: 3, Path: []Point{{X: 0}, {X: 10, Via: &Point{X: 5, Y: 3}}}}, "arc"},
		{"path written as an expression", Pattern{Kind: "path", Count: 3, Path: []Point{{X: 0}, {X: 10, YFrom: "gap"}}}, "expression"},
		{"more copies than one pattern places", Pattern{Kind: "linear", Count: maxRepeat + 1, Offset: []float64{1, 0, 0}}, "the most one pattern places"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pat := tc.pattern
			d := patterned(Child{ID: "c", Ref: "block", Pattern: &pat})
			found := false
			for _, p := range d.TreeProblems() {
				if p.Severity == Error && strings.Contains(p.Detail, tc.want) {
					found = true
				}
			}
			if !found {
				t.Errorf("want an error containing %q, got %+v", tc.want, d.TreeProblems())
			}
			if len(d.PlacedParts()) != 0 {
				t.Errorf("a refused pattern still placed %d part(s)", len(d.PlacedParts()))
			}
		})
	}
}

// A pattern of one is drawn once with a warning, as a repeat of one is.
func TestPattern_AnyPatternOfOneIsPlacedOnceWithAWarning(t *testing.T) {
	d := patterned(Child{ID: "c", Ref: "block", Pattern: &Pattern{Kind: "linear", Count: 1, Offset: []float64{1, 0, 0}}})
	placed := d.PlacedParts()
	if len(placed) != 1 || placed[0].ID != "c" {
		t.Fatalf("placed %+v", placed)
	}
	warned := false
	for _, p := range d.TreeProblems() {
		if p.Severity == Error {
			t.Fatalf("a pattern of one is an error: %s", p.Detail)
		}
		warned = warned || p.Severity == Warning
	}
	if !warned {
		t.Error("a pattern of one said nothing")
	}
}

func TestPattern_ACloneDoesNotShareAPattern(t *testing.T) {
	d := patterned(Child{ID: "c", Ref: "block", Pattern: &Pattern{Kind: "path", Count: 2,
		Path: []Point{{X: 0}, {X: 10}}, Offset: []float64{1, 2, 3}}})
	c := d.clone()
	c.Assemblies[0].Children[0].Pattern.Path[1].X = 99
	c.Assemblies[0].Children[0].Pattern.Offset[0] = 99
	if d.Assemblies[0].Children[0].Pattern.Path[1].X != 10 || d.Assemblies[0].Children[0].Pattern.Offset[0] != 1 {
		t.Error("changing a clone's pattern changed the original's")
	}
}

// A pattern's copies are named "<child>-n", which a sibling may already be called.
// The walk places both, as it places any two children; the storage door is the one
// place that refuses an id placed twice (variant.go), and it reads the PLACED parts,
// so a collision a pattern makes is refused there like any other.
func TestPattern_ACopyThatTakesASiblingsIdIsRefusedAtTheStorageDoor(t *testing.T) {
	d := patterned(Child{ID: "rail", Ref: "block", Pattern: &Pattern{Kind: "linear", Count: 3, Offset: []float64{10, 0, 0}}})
	d.Assemblies[0].Children = append(d.Assemblies[0].Children, Child{ID: "rail-2", Ref: "block", Position: []float64{0, 50, 0}})
	d.NotVerified = []string{"concept only"}
	err := (&NewVariant{InitiatorID: "u", Agent: "converse", Generator: "g", Inputs: map[string]any{}, Document: d}).Validate()
	if err == nil || !strings.Contains(err.Error(), `"rail-2" appears twice`) {
		t.Errorf("a pattern copy sharing a sibling's id was stored: %v", err)
	}
}

// The alignment turn is the smallest one: +X lands on the direction, and the axis
// of the turn (perpendicular to both) is left where it was.
func TestPattern_AlignmentIsTheSmallestTurn(t *testing.T) {
	for _, d := range [][3]float64{{1, 0, 0}, {-1, 0, 0}, {0, 0, 1}, {0, -1, 0}, {1.0 / 3, 2.0 / 3, -2.0 / 3}, {-0.6, 0, 0.8}} {
		m := rotationTaking(d)
		if det := det3(m); math.Abs(det-1) > 1e-12 {
			t.Fatalf("%v: determinant %v, not a rotation", d, det)
		}
		x := mulMatVec(m, [3]float64{1, 0, 0})
		for k := 0; k < 3; k++ {
			if math.Abs(x[k]-d[k]) > 1e-12 {
				t.Fatalf("%v: +X lands on %v", d, x)
			}
		}
		axis := [3]float64{0, -d[2], d[1]}
		if n := math.Hypot(axis[1], axis[2]); n > 1e-9 {
			turned := mulMatVec(m, axis)
			for k := 0; k < 3; k++ {
				if math.Abs(turned[k]-axis[k]) > 1e-12 {
					t.Fatalf("%v: the turn moved its own axis %v to %v, so it is not the smallest turn", d, axis, turned)
				}
			}
		}
	}
}
