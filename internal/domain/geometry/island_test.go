package geometry

import (
	"math"
	"strings"
	"testing"
)

// An island inside a hole (wave 30).
//
// Until now: "hole %d is inside hole %d. An island in a hole is a second
// outline, and there is no vocabulary for one here." The vocabulary turned out
// to be one the drawing already had — a loop contained in an odd number of
// others is solid — which is how TrueType glyphs, SVG paths, DXF and shapefiles
// all represent it.

func squareLoop(half float64) []Point {
	return []Point{{X: -half, Y: -half}, {X: half, Y: -half},
		{X: half, Y: half}, {X: -half, Y: half}}
}

// islandPlate is a 60x60 plate with a 40x40 pocket and a 20x20 post standing in
// the middle of it. 3600 − 1600 + 400 = 2400 mm² of material, 5 thick.
func islandPlate() Document {
	return Document{Name: "island", Units: "mm", Parts: []Part{{
		ID: "plate", Name: "Plate", Shape: "extrusion",
		Size: map[string]float64{"depth": 5}, Position: []float64{0, 0, 0},
		Rotation: []float64{0, 0, 0},
		Profile:  squareLoop(30),
		Holes:    [][]Point{squareLoop(20), squareLoop(10)},
	}}}
}

func TestAnIslandIsSolidInTheMesh(t *testing.T) {
	m := Tessellate(islandPlate(), Millimetre)
	tris := m.Triangles()
	if len(tris) == 0 {
		t.Fatal("a section with an island drew nothing")
	}
	const want = (60*60 - 40*40 + 20*20) * 5
	got := enclosedVolume(tris)
	if math.Abs(got-want) > 0.01 {
		t.Errorf("the drawn solid encloses %.4f mm³, want %.1f.\n"+
			"10000 means the island was cut away with the pocket. A NEGATIVE figure means "+
			"the island's wall faces into the material — the winding, which is what the "+
			"nesting parity decides.", got, float64(want))
	}
}

// The parity is what makes the island's wall face the right way. A wall that
// faces inward is invisible in a silhouette and wrong in every file.
func TestAnIslandsWallFacesOutOfTheMaterial(t *testing.T) {
	// sectionLoops IS the winding rule now — nestLoops trusts what it is given —
	// so this asks the one place that decides.
	wound := sectionLoops(pts2(squareLoop(30)),
		[][][2]float64{pts2(squareLoop(20)), pts2(squareLoop(10))})
	if len(wound) != 3 {
		t.Fatalf("%d loops, want 3", len(wound))
	}
	// Outline counter-clockwise, pocket clockwise, island counter-clockwise.
	for i, want := range []bool{true, false, true} {
		ccw := signedArea(wound[i]) > 0
		if ccw != want {
			t.Errorf("loop %d is wound %v, want counter-clockwise=%v. The same wall-normal "+
				"formula is applied to all three, so the winding IS the direction the wall "+
				"faces.", i, ccw, want)
		}
	}
}

func pts2(loop []Point) [][2]float64 {
	out := make([][2]float64, len(loop))
	for i, p := range loop {
		out[i] = [2]float64{p.X, p.Y}
	}
	return out
}

// It is READ as an island and the reading is said out loud, because an annular
// slot with a post in it and a bolt hole somebody put inside a pocket are
// spelled identically and this build cannot tell them apart.
func TestAnIslandIsReportedAsOne(t *testing.T) {
	solids, notes := Solids(islandPlate(), Millimetre)
	if len(solids) != 1 {
		t.Fatalf("the part was not built: %v", notes)
	}
	joined := strings.Join(notes, "\n")
	if !strings.Contains(joined, "read as an ISLAND") {
		t.Errorf("nothing said that hole 2 was read as an island:\n%s", joined)
	}
}

// The nesting travels to the kernel, which holds the drawing as curves and
// cannot answer a question about polygons at the same fineness the tessellator
// used.
func TestTheKernelIsToldWhichLoopIsAnIsland(t *testing.T) {
	solids, _ := Solids(islandPlate(), Millimetre)
	if len(solids) != 1 {
		t.Fatal("no solid")
	}
	parents := solids[0].HoleParents
	if len(parents) != 2 {
		t.Fatalf("hole_parents = %v, want one entry per hole", parents)
	}
	if parents[0] != -1 {
		t.Errorf("the pocket's parent is %d; it is directly inside the outline (-1)", parents[0])
	}
	if parents[1] != 0 {
		t.Errorf("the island's parent is %d; it is inside hole 0. Without this the kernel "+
			"treats it as a second hole and cuts the post away.", parents[1])
	}
}

// A section with no nesting sends nothing, which is the wire every document
// written before this produced — and absence has to keep meaning "all directly
// in the outline".
func TestAnOrdinarySectionSendsNoNesting(t *testing.T) {
	doc := islandPlate()
	doc.Parts[0].Holes = [][]Point{squareLoop(20)}
	solids, _ := Solids(doc, Millimetre)
	if len(solids) != 1 {
		t.Fatal("no solid")
	}
	if solids[0].HoleParents != nil {
		t.Errorf("hole_parents = %v for a section with no island; absence and all -1 are the "+
			"same statement and the shorter one is what every older document sends",
			solids[0].HoleParents)
	}
}

// Depth keeps going. A hole in an island is a counterbore in a boss in a pocket,
// which is an ordinary machined part rather than an edge case.
func TestNestingKeepsGoing(t *testing.T) {
	doc := islandPlate()
	doc.Parts[0].Holes = append(doc.Parts[0].Holes, squareLoop(4))
	m := Tessellate(doc, Millimetre)
	const want = (60*60 - 40*40 + 20*20 - 8*8) * 5
	got := enclosedVolume(m.Triangles())
	if math.Abs(got-want) > 0.01 {
		t.Errorf("the drawn solid encloses %.4f mm³, want %.1f — a bore through the post",
			got, float64(want))
	}
}
