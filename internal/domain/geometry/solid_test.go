package geometry_test

import (
	"math"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

func inchCube() geometry.Document {
	return geometry.Document{
		Name: "cube", Units: "in",
		Parts: []geometry.Part{{ID: "c", Name: "Cube", Shape: "box",
			Size:     map[string]float64{"width": 2, "height": 2, "depth": 2},
			Position: []float64{1, 0, 0}, Rotation: []float64{0, 0, 0}}},
	}
}

// Every length comes back in millimetres, because a STEP file states
// millimetres and the numbers in it have to mean that.
func TestSolids_ConvertsEveryLengthToMillimetres(t *testing.T) {
	got, _ := geometry.Solids(inchCube(), geometry.Inch)
	if len(got) != 1 {
		t.Fatalf("%d solids", len(got))
	}
	for _, key := range []string{"width", "height", "depth"} {
		if v := got[0].Dims[key]; math.Abs(v-50.8) > 1e-9 {
			t.Errorf("%s = %v mm, want 50.8 (2 in)", key, v)
		}
	}
	if v := got[0].Position[0]; math.Abs(v-25.4) > 1e-9 {
		t.Errorf("position x = %v mm, want 25.4 (1 in) — a correctly sized part in the "+
			"wrong place is still the wrong part", v)
	}
}

// The guard in Solids itself, reached directly. The kernel refuses an
// unconvertible unit before it ever gets here, so this is the only thing that
// holds the inner half — and a drill showed the outer guard alone made this
// look covered when it was not.
func TestSolids_BuildsNothingFromAnUnconvertibleUnit(t *testing.T) {
	doc := inchCube()
	doc.Units = "furlongs"

	got, notes := geometry.Solids(doc, geometry.UnitUnspecified)
	if len(got) != 0 {
		t.Fatalf("%d solids came back at a guessed scale: %+v", len(got), got)
	}
	if len(notes) == 0 || !strings.Contains(strings.Join(notes, " "), "no unit FORGE can convert") {
		t.Errorf("nothing said why there is no geometry: %v", notes)
	}
}

// A millimetre document is unchanged, so the conversion cannot be scaling the
// common case by some factor that happens to be 1 for the wrong reason.
func TestSolids_LeavesMillimetresAlone(t *testing.T) {
	doc := inchCube()
	doc.Units = "mm"
	got, _ := geometry.Solids(doc, geometry.Millimetre)
	if v := got[0].Dims["width"]; v != 2 {
		t.Errorf("width = %v, want 2", v)
	}
	if v := got[0].Position[0]; v != 1 {
		t.Errorf("position x = %v, want 1", v)
	}
}

// An EXTRUSION converts too. Its outline is a set of lengths like any other,
// and a profile left in inches while the depth is converted produces a part
// stretched by 25.4 in one direction and not the others.
//
// This needed its own case: the box fixture above has no profile at all, so a
// drill that stopped converting outlines left every unit test green.
func TestSolids_ConvertsAnOutlineToMillimetresToo(t *testing.T) {
	doc := geometry.Document{
		Name: "angle", Units: "in",
		Parts: []geometry.Part{{ID: "a", Name: "Angle", Shape: "extrusion",
			Profile: []geometry.Point{{X: 0, Y: 0}, {X: 2, Y: 0}, {X: 2, Y: 1}, {X: 0, Y: 1}},
			Size:    map[string]float64{"depth": 1}}},
	}
	got, notes := geometry.Solids(doc, geometry.Inch)
	if len(got) != 1 {
		t.Fatalf("%d solids: %v", len(got), notes)
	}
	corners := curveCorners(t, got[0].Outline)
	if n := len(corners); n != 4 {
		t.Fatalf("%d outline points survived: %v", n, corners)
	}
	// 2 in = 50.8 mm, 1 in = 25.4 mm.
	for i, want := range [][3]float64{{0, 0, 0}, {50.8, 0, 0}, {50.8, 25.4, 0}, {0, 25.4, 0}} {
		if p := corners[i]; math.Abs(p[0]-want[0]) > 1e-9 || math.Abs(p[1]-want[1]) > 1e-9 {
			t.Errorf("point %d = %v mm, want %v — the outline is still in inches while the "+
				"depth is in millimetres", i+1, p, want)
		}
	}
	if !got[0].Outline.Closed {
		t.Error("the outline was sent to the kernel as an open run of edges")
	}
	if d := got[0].Dims["depth"]; math.Abs(d-25.4) > 1e-9 {
		t.Errorf("depth = %v mm, want 25.4", d)
	}
}

// A SWEEP's path converts too, and it arrives with the frame its section starts
// in.
//
// # Why both halves are in one test
//
// A path left in inches while nothing else is puts the bends in the wrong place
// — the same 25.4× error the outline case above guards, in the field that says
// where the part GOES rather than how wide it is.
//
// The frame is the one thing about a sweep that reaches the CAD kernel as a
// CONVENTION rather than as geometry: which way up the section starts. Sending
// it means the kernel does not decide, and a sweep arriving without one would be
// built with its section oriented however OCCT felt — visible on any outline
// that is not rotationally symmetric, invisible on the round ones.
func TestSolids_ConvertsASweepsPathAndFramesItsSection(t *testing.T) {
	doc := geometry.Document{
		Name: "rail", Units: "in",
		Parts: []geometry.Part{{ID: "s", Name: "Rail", Shape: "sweep",
			Profile: []geometry.Point{{X: -1, Y: -1}, {X: 1, Y: -1}, {X: 1, Y: 1}, {X: -1, Y: 1}},
			Path:    []geometry.Point{{}, {Z: 2}, {X: 3, Z: 2}}}},
	}
	got, notes := geometry.Solids(doc, geometry.Inch)
	if len(got) != 1 {
		t.Fatalf("%d solids: %v", len(got), notes)
	}
	// 2 in = 50.8 mm, 3 in = 76.2 mm.
	corners := curveCorners(t, got[0].Path)
	for i, want := range [][3]float64{{0, 0, 0}, {0, 0, 50.8}, {76.2, 0, 50.8}} {
		if i >= len(corners) {
			t.Fatalf("the path came back with %d points: %v", len(corners), corners)
		}
		p := corners[i]
		for axis := 0; axis < 3; axis++ {
			if math.Abs(p[axis]-want[axis]) > 1e-9 {
				t.Errorf("path point %d = %v mm, want %v — the path is still in inches while "+
					"the outline is in millimetres, so every bend is in the wrong place",
					i+1, p, want)
			}
		}
	}
	if got[0].Path.Closed {
		t.Error("an open path was sent to the kernel as a closed one")
	}
	if got[0].SectionFrame == nil {
		t.Fatal("the sweep carries no section frame, so the kernel has to decide which way up " +
			"the outline starts — and it will not decide what the renderer decided")
	}
	// The path sets off along +Z, so the section starts exactly as it was drawn.
	// Compared with a tolerance because normalising a direction goes through a
	// square root, and 50.8/50.8 comes back one bit short of 1.
	for i, want := range [9]float64{1, 0, 0, 0, 1, 0, 0, 0, 1} {
		if math.Abs(got[0].SectionFrame[i]-want) > 1e-9 {
			t.Fatalf("a path along local Z framed the section as %v, want the identity — an "+
				"outline swept straight up is the outline as drawn", *got[0].SectionFrame)
		}
	}
	// And nothing else carries one, because nothing else has a section.
	plain, _ := geometry.Solids(inchCube(), geometry.Inch)
	if plain[0].SectionFrame != nil {
		t.Errorf("a box arrived carrying a section frame (%v); a zero or invented frame is a "+
			"convention nothing asked for", *plain[0].SectionFrame)
	}
}

// curveCorners is where a drawing goes: its start, then the end of every edge,
// with the closing edge back to the start dropped.
//
// Written out in the test rather than exported from the package because it is
// only ever useful for LOOKING at a curve, and a helper that walks the kernel's
// wire format would invite production code to walk it too.
func curveCorners(t *testing.T, c *geometry.Curve) [][3]float64 {
	t.Helper()
	if c == nil {
		t.Fatal("no curve at all: the shape was sent to the kernel with nothing to build from")
	}
	out := [][3]float64{c.Start}
	for _, e := range c.Edges {
		out = append(out, e.To)
	}
	if c.Closed && len(out) > 1 && out[len(out)-1] == c.Start {
		out = out[:len(out)-1]
	}
	return out
}

// A CORNER RADIUS converts to millimetres like every other length.
//
// # Why it needs a case of its own
//
// The conversion tests above have no radius in them, so a drill that stopped
// converting radii left every one of them green. The failure it hides is
// specific and bad: an outline correctly scaled to millimetres, with a corner
// rounded to a radius still in inches — a 0.25 in radius applied as 0.25 mm, so
// the corner is 25.4 times sharper than the drawing says. On a part meant to be
// bent, that is the difference between a radius the material tolerates and one
// that cracks it.
//
// Asserted through where the arc STARTS, because that is what a wrong radius
// moves: a right-angled corner's arc begins r back from the vertex, so a 0.25 in
// radius on a 2 in edge starts at 50.8 − 6.35 = 44.45 mm and an unconverted one
// starts at 50.55.
func TestSolids_ConvertsACornerRadiusToo(t *testing.T) {
	doc := geometry.Document{
		Name: "plate", Units: "in",
		Parts: []geometry.Part{{ID: "p", Name: "Plate", Shape: "extrusion",
			Profile: []geometry.Point{{X: 0, Y: 0}, {X: 2, Y: 0, Radius: 0.25},
				{X: 2, Y: 1}, {X: 0, Y: 1}},
			Size: map[string]float64{"depth": 1}}},
	}
	got, notes := geometry.Solids(doc, geometry.Inch)
	if len(got) != 1 {
		t.Fatalf("%d solids: %v", len(got), notes)
	}
	edges := got[0].Outline.Edges
	if len(edges) < 2 || edges[1].Via == nil {
		t.Fatalf("the rounded corner did not come through as an arc: %+v", edges)
	}
	// 2 in = 50.8 mm, 0.25 in = 6.35 mm.
	if x := edges[0].To[0]; math.Abs(x-44.45) > 1e-9 {
		t.Errorf("the arc starts at x = %v mm, want 44.45. At 50.55 the radius is still in "+
			"inches while the outline is in millimetres, and the corner is 25.4 times "+
			"sharper than it was drawn", x)
	}
	// And the point naming the arc is a radius from its centre, which for this
	// corner is (44.45, 6.35).
	centre := [3]float64{44.45, 6.35, 0}
	via := *edges[1].Via
	d := math.Sqrt(math.Pow(via[0]-centre[0], 2) + math.Pow(via[1]-centre[1], 2))
	if math.Abs(d-6.35) > 1e-9 {
		t.Errorf("the point sent to name the arc is %v from its centre, want the converted "+
			"radius 6.35", d)
	}
}
