package geometry

import (
	"math"
	"strings"
	"testing"
)

// The fences over an arc that is NOT tangent to its neighbours (wave 29).
//
// What is held here is the thing the corner radius could never say: an edge that
// BOWS. A crescent, a lens, a cam lobe, the belly of a bracket that clears
// something. See the note at the foot of curve.go for why the vocabulary is a
// through-point and not a radius.

func at(x, y float64) Point { return Point{X: x, Y: y} }
func bowTo(x, y float64, vx, vy float64) Point {
	return Point{X: x, Y: y, Via: &Point{X: vx, Y: vy}}
}

// The maths, on a case where the answer is known exactly.
//
// A semicircle of radius 5 from (-5,0) through (0,5) to (5,0): the centre is the
// origin, the radius is 5, and the turn is exactly π.
func TestArcThrough_ResolvesACircleFromThreePoints(t *testing.T) {
	centre, axis, radius, angle, ok := arcThrough(
		[3]float64{-5, 0, 0}, [3]float64{0, 5, 0}, [3]float64{5, 0, 0})
	if !ok {
		t.Fatal("three points on a circle did not resolve into one")
	}
	if length3(centre) > 1e-9 {
		t.Errorf("centre = %v, want the origin", centre)
	}
	if math.Abs(radius-5) > 1e-9 {
		t.Errorf("radius = %v, want 5", radius)
	}
	if math.Abs(angle-math.Pi) > 1e-9 {
		t.Errorf("angle = %v, want π", angle)
	}
	// The travel goes from (-5,0) up over (0,5), which about +z is CLOCKWISE, so
	// the axis is -z. Getting this backwards draws the other half of the circle:
	// same endpoints, same radius, the wrong shape.
	if axis[2] > -0.5 {
		t.Errorf("axis = %v; the arc turns the wrong way and would bow away from its via", axis)
	}
	// And the via is genuinely on it.
	mid := add3(centre, rotateAbout(sub3([3]float64{-5, 0, 0}, centre), axis, angle/2))
	if length3(sub3(mid, [3]float64{0, 5, 0})) > 1e-9 {
		t.Errorf("the halfway point of the resolved arc is %v, not the via it was built through", mid)
	}
}

// Three points in line describe no circle, and are read as the straight edge
// they are rather than taking the part with them.
func TestArcThrough_RefusesWhatIsNotAnArc(t *testing.T) {
	for _, tc := range []struct {
		name          string
		from, via, to [3]float64
	}{
		{"in line", [3]float64{0, 0, 0}, [3]float64{5, 0, 0}, [3]float64{10, 0, 0}},
		{"via on an end", [3]float64{0, 0, 0}, [3]float64{0, 0, 0}, [3]float64{10, 0, 0}},
		{"ends coincide", [3]float64{0, 0, 0}, [3]float64{5, 5, 0}, [3]float64{0, 0, 0}},
	} {
		if _, _, _, _, ok := arcThrough(tc.from, tc.via, tc.to); ok {
			t.Errorf("%s: resolved into an arc", tc.name)
		}
	}
}

// A bowed edge leaves its chord, and the flattened drawing shows it.
func TestABowedEdgeLeavesItsChord(t *testing.T) {
	// A square whose top edge bows up to y=15 at its middle.
	loop := readLoop([]Point{
		at(0, 0), at(20, 0), at(20, 10), bowTo(0, 10, 10, 15)}, true)

	flat, dev, err := loop.flatten("outline", Millimetre)
	if err != nil {
		t.Fatal(err)
	}
	var top float64
	for _, p := range flat {
		if p[1] > top {
			top = p[1]
		}
	}
	if math.Abs(top-15) > 1e-6 {
		t.Errorf("the drawing reaches y=%.4f; the via says the edge passes through y=15. "+
			"A via that is not drawn is a straight edge wearing a curve's name.", top)
	}
	// It is an approximation, and it says so — the same bargain every curved
	// shape here makes.
	if dev == nil {
		t.Error("a bowed edge reported no deviation. It is chords standing in for an arc, " +
			"and the exported mesh has to say by how much.")
	}
}

// The kernel gets the TRUE arc, so an exported bow is a real cylindrical surface
// rather than a many-sided prism.
func TestTheKernelIsSentTheArcAndNotItsChords(t *testing.T) {
	loop := readLoop([]Point{
		at(0, 0), at(20, 0), at(20, 10), bowTo(0, 10, 10, 15)}, true)

	curve, err := loop.exact("outline")
	if err != nil {
		t.Fatal(err)
	}
	var arcs int
	for _, e := range curve.Edges {
		if e.Via != nil {
			arcs++
		}
	}
	if arcs != 1 {
		t.Fatalf("%d arc edges reached the kernel, want 1. Sending chords makes every bow a "+
			"flat facet in the exported STEP — a mesh wearing a solid model's extension.",
			arcs)
	}
	// And it is a small curve, not a huge one: four points in, four edges out.
	if len(curve.Edges) != 4 {
		t.Errorf("%d edges, want 4 (three straights and one arc)", len(curve.Edges))
	}
}

// A crescent: two arcs between the same two points, bowing different amounts.
// This is the shape the whole wave exists for and a corner radius cannot say it.
func TestACrescentIsExpressible(t *testing.T) {
	doc := Document{Name: "crescent", Units: "mm", Parts: []Part{{
		ID: "moon", Name: "Moon", Shape: "extrusion",
		Size: map[string]float64{"depth": 3}, Position: []float64{0, 0, 0},
		Rotation: []float64{0, 0, 0},
		Profile: []Point{
			at(-20, 0),
			// Out along a deep bow…
			bowTo(20, 0, 0, 14),
			// …and back along a shallower one, which leaves a crescent.
			bowTo(-20, 0, 0, 6),
		},
	}}}
	solids, notes := Solids(doc, Millimetre)
	if len(solids) != 1 {
		t.Fatalf("the crescent was not built: %v", notes)
	}
	mesh := Tessellate(doc, Millimetre)
	if len(mesh.Triangles()) == 0 {
		t.Fatal("the crescent drew nothing")
	}
	// Three points, and it encloses area, which no polygon of three straight
	// edges through those points would: they are in line.
	if mesh.Deviations == nil {
		t.Error("no deviation was reported for a shape made entirely of arcs")
	}
}

// A radius where an arc meets the corner is IGNORED with a warning, not refused.
//
// Rounding an arc into an arc is a fillet between two curves, and the
// construction in curve.go — which walks back r·tan(θ/2) along a straight — has
// no answer for it. Refusing would follow the rule wave 24 removed: an inert
// radius used to cost two whole parts.
func TestARadiusBesideAnArcIsIgnoredRatherThanFatal(t *testing.T) {
	loop := polyline{
		Points: [][3]float64{{0, 0, 0}, {20, 0, 0}, {20, 10, 0}, {0, 10, 0}},
		Radii:  []float64{0, 0, 3, 0},
		Vias:   []*[3]float64{nil, nil, nil, {10, 15, 0}},
		Closed: true,
	}
	ignored, err := loop.validate("outline")
	if err != nil {
		t.Fatalf("a radius beside an arc was refused, and the whole part with it: %v", err)
	}
	var said bool
	for _, s := range ignored {
		if strings.Contains(s, "arc meets the corner") {
			said = true
		}
	}
	if !said {
		t.Errorf("the radius was dropped without saying so: %v", ignored)
	}
	// And the corner really is sharp, so the arc's own end is where it was drawn.
	corners, _, err := roundedCorners(loop.Points, loop.Radii, loop.Vias, true, "outline")
	if err != nil {
		t.Fatal(err)
	}
	if !corners[2].sharp {
		t.Error("the corner beside an arc was rounded. An arc edge must have sharp ends, or " +
			"its endpoints move and every place that assumes otherwise is wrong.")
	}
}

// A via that names no arc is ignored the same way, and the edge is drawn
// straight — which is what it is.
func TestAViaThatNamesNoArcIsIgnored(t *testing.T) {
	loop := readLoop([]Point{
		at(0, 0), at(20, 0), at(20, 10),
		// Collinear with its two ends: a circle of infinite radius.
		{X: 0, Y: 10, Via: &Point{X: 10, Y: 10}},
	}, true)

	ignored, err := loop.validate("outline")
	if err != nil {
		t.Fatalf("an inert via was refused: %v", err)
	}
	var said bool
	for _, s := range ignored {
		if strings.Contains(s, "names no arc") {
			said = true
		}
	}
	if !said {
		t.Errorf("an inert via was dropped silently: %v", ignored)
	}
	flat, _, err := loop.flatten("outline", Millimetre)
	if err != nil {
		t.Fatal(err)
	}
	if len(flat) != 4 {
		t.Errorf("%d points drawn for a four-point square whose via names nothing", len(flat))
	}
}

// An arc that bulges across another edge is refused — and only the FLATTENED
// drawing can see it, because the points themselves do not cross.
func TestAnArcThatCrossesAnotherEdgeIsRefused(t *testing.T) {
	doc := Document{Name: "crossing", Units: "mm", Parts: []Part{{
		ID: "bad", Name: "Bad", Shape: "extrusion",
		Size: map[string]float64{"depth": 3}, Position: []float64{0, 0, 0},
		Rotation: []float64{0, 0, 0},
		Profile: []Point{
			at(0, 0), at(40, 0), at(40, 10),
			// Bows back down THROUGH the bottom edge. The circle through
			// (40,10), (20,-5) and (0,10) is centred at (20, 15.83) with r
			// 20.83, so it meets y=0 at x=6.46 and x=33.54 — both squarely on
			// the bottom edge, which runs from x=0 to x=40.
			//
			// A bulge that goes far enough the OTHER way is not this: an arc
			// dipping to y=-30 swings out past x=40 and x=0 and passes under the
			// bottom edge without ever touching it, which is an odd shape but a
			// valid one. Getting that wrong is how this test would assert a
			// refusal the code is right not to make.
			bowTo(0, 10, 20, -5),
		},
	}}}
	solids, notes := Solids(doc, Millimetre)
	if len(solids) != 0 {
		t.Fatal("an outline whose arc crosses another edge was built. Its POINTS do not cross, " +
			"so the check on the drawn points cannot see this — only the flattened one can.")
	}
	var said bool
	for _, n := range notes {
		if strings.Contains(n, "once its arcs are drawn") {
			said = true
		}
	}
	if !said {
		t.Errorf("it was refused without naming the cause: %v", notes)
	}
}

// A via converts with everything else. Left in inches while its endpoints became
// millimetres it would describe an arc bulging 25 times too far — and unlike a
// wrong radius, which OCCT eventually refuses, a wrong via still builds.
func TestAViaIsConvertedWithItsDrawing(t *testing.T) {
	loop := readLoop([]Point{at(0, 0), at(2, 0), bowTo(0, 2, 1, 3)}, true)
	mm := loop.scaled(25.4)
	if mm.Vias[2] == nil {
		t.Fatal("the via was lost in conversion")
	}
	if math.Abs(mm.Vias[2][1]-3*25.4) > 1e-9 {
		t.Errorf("the via converted to %v; the points became millimetres and it did not",
			mm.Vias[2])
	}
}

// A loop that closes by repeating its first point carries the via with it, for
// the same reason the radius is carried — and with a cleaner argument: it is the
// same edge, renumbered.
func TestAClosingDuplicateCarriesItsVia(t *testing.T) {
	loop := readLoop([]Point{
		at(0, 0), at(20, 0), at(20, 10), at(0, 10),
		{X: 0, Y: 0, Via: &Point{X: -8, Y: 5}},
	}, true)

	if len(loop.Points) != 4 {
		t.Fatalf("%d points after dropping the closing duplicate, want 4", len(loop.Points))
	}
	if loop.Vias[0] == nil {
		t.Fatal("the via on the repeated point was dropped. It describes the CLOSING edge, " +
			"which is entry 0 once the duplicate is gone — so losing it draws a bowed edge " +
			"straight: a different outline of the same overall size.")
	}
	if loop.Vias[0][0] != -8 {
		t.Errorf("the carried via is %v, want the one that was written", loop.Vias[0])
	}
}

// A via is a point, not a corner: its own radius and its own via are refused
// rather than ignored, because there is nothing there even in principle.
func TestAViaCannotCarryACornerOrAViaOfItsOwn(t *testing.T) {
	for _, tc := range []struct {
		name string
		via  *Point
		want string
	}{
		{"a radius", &Point{X: 10, Y: 15, Radius: 2}, "corner radius"},
		{"a via", &Point{X: 10, Y: 15, Via: &Point{X: 1, Y: 1}}, "via of its own"},
		{"a z", &Point{X: 10, Y: 15, Z: 4}, "carries a z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := Document{Name: "n", Units: "mm", Parts: []Part{{
				ID: "p", Shape: "extrusion", Size: map[string]float64{"depth": 3},
				Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0},
				Profile: []Point{at(0, 0), at(20, 0), {X: 0, Y: 10, Via: tc.via}},
			}}}
			solids, notes := Solids(doc, Millimetre)
			if len(solids) != 0 {
				t.Fatal("it was built anyway")
			}
			joined := strings.Join(notes, " ")
			if !strings.Contains(joined, tc.want) {
				t.Errorf("the refusal does not say why: %v", notes)
			}
		})
	}
}
