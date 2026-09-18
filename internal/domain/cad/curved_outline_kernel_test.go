package cad_test

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// Kernel fences for bowed outlines in extrusions and lofts (B3, 2026-09-18).
//
// damon, 2026-09-18: FORGE's output should look designed — a body that swells
// over its wheels rather than a box. A "via" (wave 29) already bowed an edge; what
// no test held was that a lens builds as two TRUE arcs end to end (volume and
// STEP), that a loft blends into a bowed station exactly, and that Go's own
// measurement of a bowed outline agrees with OCCT's.

func via(x, y, vx, vy float64) geometry.Point {
	return geometry.Point{X: x, Y: y, Via: &geometry.Point{X: vx, Y: vy}}
}

// circularSegment is the area between a chord c and its arc bowed by s, and
// the arc's radius and turn.
func circularSegment(c, s float64) (area, r, theta float64) {
	r = (c*c/4 + s*s) / (2 * s)
	theta = 2 * math.Asin(c/2/r)
	return r * r / 2 * (theta - math.Sin(theta)), r, theta
}

// A lens extruded is two arcs, exactly: its volume is two circular segments
// times the depth, and its STEP file carries cylinders where a chorded lens
// would carry flat facets.
func TestKernel_ALensExtrusionIsTwoExactArcs(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	const chord, sag, depth = 40.0, 8.0, 10.0
	doc := geometry.Document{Name: "lens", Units: "mm", Parts: []geometry.Part{{
		ID: "lens", Name: "Lens", Shape: "extrusion",
		Size: map[string]float64{"depth": depth}, Position: []float64{0, 0, 0},
		Rotation: []float64{0, 0, 0},
		// The guide's own example (geometry.CurveGuide).
		Profile: []geometry.Point{via(-chord/2, 0, 0, -sag), via(chord/2, 0, 0, sag)},
	}}}
	got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "step")
	if err != nil {
		t.Fatalf("a lens was not built: %v", err)
	}
	seg, _, _ := circularSegment(chord, sag)
	want := 2 * seg * depth
	// Measured 2026-09-18: 4400.234526 against 4400.234526 — OCCT's arc is the
	// circle. Chords at the renderer's fineness (8 per arc) enclose 1.1% less.
	if math.Abs(got.Volume-want) > want*1e-6 {
		t.Errorf("lens volume = %.4f mm³, want %.4f (two segments of chord %g bowed %g, x %g)",
			got.Volume, want, chord, sag, depth)
	}
	// Two cylindrical faces, one per arc. A lens sent as chords would have none.
	step := string(got.STEP)
	if n := strings.Count(step, "CYLINDRICAL_SURFACE"); n < 2 {
		t.Errorf("the STEP file has %d CYLINDRICAL_SURFACE entities; a lens has two arcs", n)
	}
	if strings.Contains(step, "TRIANGULATED") || strings.Contains(step, "POLY_LOOP") {
		t.Error("the STEP file is tessellated")
	}
}

// A loft from a plain rectangle into a station whose top bows up blends to the
// volume the ruled correspondence gives, and its STEP keeps the arc.
//
// # The formula
//
// Two stations h apart, a 40 x 20 rectangle and the same with its top edge bowed
// through (0, 25). OCCT pairs the top line with the top arc and joins points at
// the same fraction of each edge's parameter: along the line that is x, along
// the arc it is the angle φ. At a fraction t of the way up, the section is the
// rectangle plus the area between y=20 and a curve that is (1−t)·line + t·arc,
// which is
//
//	A(t) = A0 + t(1−t)·P + t²·S
//
// with S the circular segment and P = c · mean over φ of (arc height above the
// chord) = c·(r·sin(θ/2)/(θ/2) − r + s). Integrating over t:
//
//	V = h·(A0 + P/6 + S/3)
//
// Measured 2026-09-18 against OCCT: 26013.8305 both, agreeing to 2e-10. The
// naive "mean of the two end areas" h·(A0 + S/2) is 4.2e-4 high, so the
// tolerance below tells them apart.
func TestKernel_ALoftIntoABulgedStationBlendsExactly(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	const half, top, bulge, h = 20.0, 20.0, 25.0, 30.0
	station := func(id string, z float64, profile []geometry.Point) geometry.Part {
		return geometry.Part{ID: id, Name: id, Shape: "section", Profile: profile,
			Position: []float64{0, 0, z}, Rotation: []float64{0, 0, 0}}
	}
	doc := geometry.Document{
		Name: "swell", Units: "mm",
		Parts: []geometry.Part{
			station("plain", 0, []geometry.Point{
				{X: -half, Y: 0}, {X: half, Y: 0}, {X: half, Y: top}, {X: -half, Y: top}}),
			station("bulged", h, []geometry.Point{
				{X: -half, Y: 0}, {X: half, Y: 0}, {X: half, Y: top}, via(-half, top, 0, bulge)}),
		},
		Features: []geometry.Feature{{ID: "blend", Op: "loft", Of: "plain", With: []string{"bulged"}}},
	}
	got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "step")
	if err != nil {
		t.Fatalf("lofting into a bowed station: %v", err)
	}
	c, s := 2*half, bulge-top
	seg, r, theta := circularSegment(c, s)
	p := c * (r*math.Sin(theta/2)/(theta/2) - r + s)
	a0 := 2 * half * top
	want := h * (a0 + p/6 + seg/3)
	if math.Abs(got.Volume-want) > want*1e-6 {
		t.Errorf("loft volume = %.4f mm³, want %.4f = h(A0 + P/6 + S/3) with A0=%g P=%.4f S=%.4f.\n"+
			"  The mean of the end areas would be %.4f; a station whose arc was dropped, %.4f.",
			got.Volume, want, a0, p, seg, h*(a0+seg/2), h*a0)
	}
	if got.Bounds[4] < bulge-1e-3 {
		t.Errorf("the loft reaches y=%.4f; the bowed station passes through %g", got.Bounds[4], bulge)
	}
	step := string(got.STEP)
	if !strings.Contains(step, "CIRCLE") {
		t.Error("the loft's STEP has no CIRCLE; the bowed station's edge was not kept as an arc")
	}
	if strings.Contains(step, "TRIANGULATED") || strings.Contains(step, "POLY_LOOP") {
		t.Error("the STEP file is tessellated")
	}
}

// Go measures a bowed outline where OCCT does.
//
// The bow is tilted so the arc's rightmost point falls between two chord ends:
// Go used to measure the chords there and came up short by up to the chord
// deviation. geometry.ExtentOf is what the stored extent and every dimension
// overlay read.
func TestKernel_GoMeasuresABowedOutlineWhereTheKernelDoes(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	doc := geometry.Document{Name: "tilted", Units: "mm", Parts: []geometry.Part{{
		ID: "p", Name: "P", Shape: "extrusion", Size: map[string]float64{"depth": 3},
		Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0},
		Profile: []geometry.Point{{X: 0, Y: 0}, {X: 40, Y: 0}, via(0, 30, 32, 22)},
	}}}
	got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}
	e := geometry.ExtentOf(doc)
	if e == nil {
		t.Fatal("Go measured nothing")
	}
	goBox := [6]float64{e.Min[0], e.Min[1], e.Min[2], e.Max[0], e.Max[1], e.Max[2]}
	for i := 0; i < 6; i++ {
		if math.Abs(goBox[i]-got.Bounds[i]) > 1e-3 {
			t.Errorf("bound %d: Go measures %.6f, OCCT %.6f\n  Go  %v\n  OCCT %v",
				i, goBox[i], got.Bounds[i], goBox, got.Bounds)
		}
	}
}
