package geometry

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

func square(w, h float64) [][3]float64 {
	return [][3]float64{{0, 0, 0}, {w, 0, 0}, {w, h, 0}, {0, h, 0}}
}

// A rounded rectangle encloses the area arithmetic says it does.
//
// # Why this figure and not one observed once
//
// Rounding a right-angled corner with radius r removes the square of side r and
// puts back a quarter circle, so each corner loses r² − πr²/4. Four of them:
//
//	area = w·h − (4 − π)·r²
//
// which a reader can check without running anything. It also pins BOTH halves of
// the corner at once — where the arc starts and how far the centre is set back —
// because getting either wrong changes the area.
//
// The flattened outline is INSCRIBED, so it comes out slightly under. The bound
// is the deviation this code reports for itself: if the shortfall were larger
// than the deviation, the reported number would be a lie.
func TestFlattenCurve_ARoundedCornerRemovesWhatArithmeticSays(t *testing.T) {
	// The area a rounded corner removes, for ANY turn θ and radius r: the kite
	// between the vertex, the two tangent points and the centre has area r²tan(θ/2),
	// and the sector inside it has ½r²θ, so the corner loses the difference.
	//
	// A right angle reduces to the familiar (4−π)r²/4 per corner. It is also the
	// one angle where two different formulas for the CENTRE's setback coincide —
	// r/cos(θ/2) and r/sin(θ/2) are equal at 45° — so an obtuse case is here on
	// purpose. A drill on 2026-09-05 swapped one for the other and every test
	// stayed green, because every case in this file was a rectangle.
	removed := func(r, turn float64) float64 {
		return r*r*math.Tan(turn/2) - r*r*turn/2
	}
	rightAngle, obtuse := math.Pi/2, 3*math.Pi/4

	for _, tc := range []struct {
		name      string
		pts       [][3]float64
		radii     []float64
		wantExact float64
		wantArcs  int
		wantEdges int
	}{
		{"a plate with rounded corners", square(40, 40), []float64{10, 10, 10, 10},
			1600 - 4*removed(10, rightAngle), 4, 8},
		// A slot: the radius is half the width, so each end's two quarter-circles
		// meet and the straight between them vanishes. Also 20×20 + π·10², a
		// stadium arrived at two ways.
		{"a slot", square(40, 20), []float64{10, 10, 10, 10},
			714.1592653589793, 4, 6},
		// An ACUTE-turning corner: the 45° point of a right triangle, which the
		// path turns through 135° to get round. Nothing about a rectangle
		// exercises this.
		{"the sharp point of a gusset",
			[][3]float64{{0, 0, 0}, {40, 0, 0}, {0, 40, 0}}, []float64{0, 5, 0},
			800 - removed(5, obtuse), 1, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flat, radius, angle, segments, err := flattenCurve(tc.pts, tc.radii, true, "outline")
			if err != nil {
				t.Fatal(err)
			}
			got := math.Abs(signedArea(flat2D(flat)))
			dev := deviationOf(radius, angle, segments)
			// Every chord is inside its arc by at most `dev`, so the area lost is
			// bounded well inside dev × the rounded perimeter. A loose bound on
			// purpose: what is asserted is that the shortfall is EXPLAINED by the
			// deviation this code reports, not that it equals another formula.
			slack := dev*2*math.Pi*radius + 1e-9
			if got > tc.wantExact+1e-9 || got < tc.wantExact-slack {
				t.Errorf("the flattened outline encloses %.6f; the true rounded shape is "+
					"%.6f and the chords can only lose up to %.6f of it", got, tc.wantExact, slack)
			}

			exact, err := exactCurve(tc.pts, tc.radii, true, "outline")
			if err != nil {
				t.Fatal(err)
			}
			arcs := 0
			for _, e := range exact.Edges {
				if e.Via != nil {
					arcs++
				}
			}
			if arcs != tc.wantArcs || len(exact.Edges) != tc.wantEdges {
				t.Errorf("the kernel is sent %d edges of which %d are arcs; want %d and %d. "+
					"A slot has no straight left at its ends, and an edge that did not "+
					"vanish there is a zero-length one OCCT will refuse",
					len(exact.Edges), arcs, tc.wantEdges, tc.wantArcs)
			}
			if !exact.Closed {
				t.Error("the outline came back open")
			}
		})
	}
}

// Every point of the true arc is on the flattened one's outside, and the gap is
// the number this code reports.
//
// The deviation is the one thing a person is told about what flattening cost
// them, and it travels into the export label. A formula that under-reports it is
// worse than no number at all.
func TestFlattenCurve_ReportsWhatFlatteningCost(t *testing.T) {
	pts := square(40, 40)
	radii := []float64{10, 10, 10, 10}
	flat, radius, angle, segments, err := flattenCurve(pts, radii, true, "outline")
	if err != nil {
		t.Fatal(err)
	}
	if radius != 10 || segments < 1 {
		t.Fatalf("reported radius %v over %d segments", radius, segments)
	}
	if math.Abs(angle-math.Pi/2) > 1e-9 {
		t.Errorf("a right-angled corner turns through %v, want π/2", angle)
	}
	reported := deviationOf(radius, angle, segments)

	// The corner centred at (10, 10) has radius 10, and the sag is at the MIDDLE
	// OF EACH CHORD — the flattened points themselves sit exactly on the arc, so
	// measuring them gives zero and proves nothing. The first version of this
	// test did exactly that and reported a deviation of zero against a reported
	// 0.0308.
	centre := [3]float64{10, 10, 0}
	worst := 0.0
	for i := 0; i+1 < len(flat); i++ {
		a, b := flat[i], flat[i+1]
		if length3(sub3(a, centre)) > 10+1e-9 || length3(sub3(b, centre)) > 10+1e-9 {
			continue // a chord of another corner, or one of the straight sides
		}
		mid := scale3(add3(a, b), 0.5)
		if gap := 10 - length3(sub3(mid, centre)); gap > worst {
			worst = gap
		}
	}
	if worst > reported+1e-12 {
		t.Errorf("a flattened point sits %.9f inside the true arc, but the deviation this "+
			"code reports is %.9f — the number in the export label would understate what "+
			"the file actually is", worst, reported)
	}
	if worst < reported/2 {
		t.Errorf("the worst point is only %.9f inside a reported %.9f; the report is so "+
			"loose it says nothing", worst, reported)
	}
}

// The refusals, each naming what a person has to change.
//
// What is refused is a radius that cannot be READ as anything. A radius that is
// merely INERT — one that names no corner — is ignored instead, and the test
// below holds that line.
func TestRoundedCorners_RefusesWhatCannotBeRounded(t *testing.T) {
	for _, tc := range []struct {
		name, wants string
		pts         [][3]float64
		radii       []float64
		closed      bool
	}{
		{"radii that overlap each other", "would overlap",
			square(40, 20), []float64{15, 15, 15, 15}, true},
		{"a radius bigger than the whole edge", "would overlap",
			square(10, 10), []float64{8, 8, 8, 8}, true},
		{"a negative radius", "cannot be negative",
			square(40, 40), []float64{-5, 0, 0, 0}, true},
		{"a radius on a reversal", "180°",
			[][3]float64{{0, 0, 0}, {0, 0, 20}, {0, 0, 5}, {30, 0, 5}},
			[]float64{0, 5, 0, 0}, false},
		{"a radius on a point sitting on its neighbour", "sits on top of its neighbour",
			[][3]float64{{0, 0, 0}, {0, 0, 20}, {0, 0, 20}, {30, 0, 20}},
			[]float64{0, 0, 5, 0}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := roundedCorners(tc.pts, tc.radii, tc.closed, "outline")
			if err == nil {
				t.Fatal("this was rounded without complaint, and the result is not a shape")
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("said %q, which does not contain %q", err, tc.wants)
			}
		})
	}
}

// A radius that names NO CORNER is ignored, said out loud, and the part is still
// built.
//
// # Why these two are not refusals
//
// Neither is ambiguous: there is no corner at the end of an open run, and none
// where the edges either side are in line, so the number changes nothing. And
// refusing cost the whole part. Measured 2026-09-05: qwen-plus put a radius on
// every path point of a correct bent tube in two live runs of six, and on a
// point its path ran straight through in an eval run — all three buildable
// drawings that vanished from the file over an inert number.
//
// The corner must come back SHARP, and the note must name the point: a radius
// silently swallowed is the same failure one step quieter.
func TestRoundedCorners_IgnoresARadiusThatNamesNoCorner(t *testing.T) {
	for _, tc := range []struct {
		name, wants string
		pts         [][3]float64
		radii       []float64
		closed      bool
		at          int
	}{
		{"a radius on a straight run", "edges either side of it are in line",
			[][3]float64{{0, 0, 0}, {10, 0, 0}, {20, 0, 0}, {20, 10, 0}},
			[]float64{0, 5, 0, 0}, true, 1},
		{"a radius where an open path starts", "starts or ends",
			[][3]float64{{0, 0, 0}, {0, 0, 20}, {30, 0, 20}}, []float64{5, 0, 0}, false, 0},
		{"a radius where an open path ends", "starts or ends",
			[][3]float64{{0, 0, 0}, {0, 0, 20}, {30, 0, 20}}, []float64{0, 0, 5}, false, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			corners, ignored, err := roundedCorners(tc.pts, tc.radii, tc.closed, "path")
			if err != nil {
				t.Fatalf("an inert radius was refused, and the whole part with it: %v", err)
			}
			if !corners[tc.at].sharp {
				t.Errorf("point %d was rounded, and there is no corner there to round", tc.at+1)
			}
			if len(ignored) != 1 {
				t.Fatalf("%d notes for one ignored radius: %v", len(ignored), ignored)
			}
			if !strings.Contains(ignored[0], tc.wants) {
				t.Errorf("the note does not say why it means nothing: %q", ignored[0])
			}
			if !strings.Contains(ignored[0], fmt.Sprintf("point %d", tc.at+1)) {
				t.Errorf("the note does not name which point: %q", ignored[0])
			}
		})
	}
}

// An open path keeps its two ends exactly where they were drawn.
//
// A rounded corner moves material away from the vertex it rounds. If that
// happened at an end, a sweep would stop short of where its path said it stops,
// and the part would be the wrong length — quietly, by the radius.
func TestExactCurve_AnOpenPathStartsAndEndsWhereItWasDrawn(t *testing.T) {
	pts := [][3]float64{{0, 0, 0}, {0, 0, 40}, {30, 0, 40}}
	curve, err := exactCurve(pts, []float64{0, 12, 0}, false, "path")
	if err != nil {
		t.Fatal(err)
	}
	if curve.Closed {
		t.Error("an open path came back closed")
	}
	if !same(curve.Start, pts[0]) {
		t.Errorf("the path starts at %v, not at %v", curve.Start, pts[0])
	}
	if end := curve.Edges[len(curve.Edges)-1].To; !same(end, pts[2]) {
		t.Errorf("the path ends at %v, not at %v", end, pts[2])
	}
	// straight, arc, straight — the bend eats into both legs and nothing else.
	if len(curve.Edges) != 3 || curve.Edges[0].Via != nil || curve.Edges[1].Via == nil ||
		curve.Edges[2].Via != nil {
		t.Fatalf("a single rounded bend produced %d edges: %+v", len(curve.Edges), curve.Edges)
	}
	// r·tan(θ/2) with θ = π/2 is r, so the arc starts 12 short of the corner.
	if want := ([3]float64{0, 0, 28}); !same(curve.Edges[0].To, want) {
		t.Errorf("the bend starts at %v, want %v — a right-angled bend of radius 12 leaves "+
			"the straight 12 before the corner", curve.Edges[0].To, want)
	}
	if want := ([3]float64{12, 0, 40}); !same(curve.Edges[1].To, want) {
		t.Errorf("the bend ends at %v, want %v", curve.Edges[1].To, want)
	}
	// And the point on the arc is at the radius from the centre, which for this
	// corner is (12, 0, 28).
	centre := [3]float64{12, 0, 28}
	if d := length3(sub3(*curve.Edges[1].Via, centre)); math.Abs(d-12) > 1e-9 {
		t.Errorf("the point sent to name the arc is %v from its centre, not the radius 12 — "+
			"the kernel would build a different arc from the one that was drawn", d)
	}
}

// A loop that closes itself by repeating its first point is READ, and the radius
// on the repeated point comes with it.
//
// # Why the radius has to move
//
// The model writes the corner twice: once as a destination, once as a corner
// carrying its radius. Dropping the point and leaving the radius behind would
// MITRE a corner somebody asked to be bent — a different part, made a different
// way, and silently. Observed exactly this way in the eval runs of 2026-09-05,
// where the repeated point carried R20 and the original carried nothing.
func TestWithoutClosingDuplicate_CarriesTheRadiusToThePointThatStays(t *testing.T) {
	loop := [][3]float64{{0, 0, 0}, {240, 0, 0}, {240, 90, 0}, {0, 90, 0}, {0, 0, 0}}

	pts, radii, dropped, conflict := withoutClosingDuplicate(loop, []float64{0, 20, 20, 20, 20})
	if !dropped || conflict {
		t.Fatalf("dropped=%v conflict=%v for a plain closing duplicate", dropped, conflict)
	}
	if len(pts) != 4 {
		t.Fatalf("%d points left, want 4", len(pts))
	}
	if radii[0] != 20 {
		t.Errorf("the first corner came back with radius %v; the repeated point carried 20 and "+
			"leaving it behind mitres a corner that was asked to be bent", radii[0])
	}

	// Nothing to drop: untouched, and reported as untouched.
	open := [][3]float64{{0, 0, 0}, {10, 0, 0}, {10, 10, 0}}
	if _, _, dropped, _ := withoutClosingDuplicate(open, []float64{0, 0, 0}); dropped {
		t.Error("a run that does not repeat its first point was altered")
	}

	// Both carrying a radius, and disagreeing: one corner, two answers.
	if _, _, _, conflict := withoutClosingDuplicate(loop, []float64{5, 0, 0, 0, 20}); !conflict {
		t.Error("two different radii on one corner were merged into one of them silently")
	}
	// Both carrying the SAME radius is not a conflict — it is the same corner
	// said twice, which is what the convention produces.
	if _, _, dropped, conflict := withoutClosingDuplicate(loop, []float64{20, 0, 0, 0, 20}); conflict || !dropped {
		t.Errorf("a corner written twice with the same radius was refused: dropped=%v conflict=%v",
			dropped, conflict)
	}
}
