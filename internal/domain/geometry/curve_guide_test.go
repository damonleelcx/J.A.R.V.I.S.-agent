package geometry

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// Fences for B3 (2026-09-18): bowed edges taught from the validator's own table,
// and measured exactly. See curve_guide.go.

func extrusionOf(profile []Point) Document {
	return Document{Name: "b3", Units: "mm", Parts: []Part{{
		ID: "p", Name: "P", Shape: "extrusion", Size: map[string]float64{"depth": 3},
		Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}, Profile: profile,
	}}}
}

// Every row of curveRules is enforced with the words in its refusal AND taught
// with the words in its teach. A rule the validator applies and the contract
// does not state is a refusal the model walks into; one the contract states and
// the validator does not apply is a promise nothing keeps.
func TestCurveGuide_TeachesEveryRuleTheValidatorEnforces(t *testing.T) {
	guide := CurveGuide()
	for _, tc := range []struct {
		name    string
		rule    curveRule
		profile []Point
	}{
		{"a via of a via", ruleViaOfVia,
			[]Point{at(0, 0), at(20, 0), {X: 0, Y: 10, Via: &Point{X: 10, Y: 15, Via: &Point{X: 1, Y: 1}}}}},
		{"a via with a radius", ruleViaRadius,
			[]Point{at(0, 0), at(20, 0), {X: 0, Y: 10, Via: &Point{X: 10, Y: 15, Radius: 2}}}},
		{"a via with a z", ruleViaZ,
			[]Point{at(0, 0), at(20, 0), {X: 0, Y: 10, Via: &Point{X: 10, Y: 15, Z: 4}}}},
		{"a via in line", ruleViaNoArc,
			[]Point{at(-20, 0), at(20, 0), at(20, 20), bowTo(-20, 20, 0, 20)}},
		{"a bulge across another edge", ruleBulgeCrosses,
			[]Point{at(0, 0), at(40, 0), at(40, 10), bowTo(0, 10, 20, -5)}},
		{"two arcs that coincide", ruleBulgeEmpty,
			[]Point{bowTo(-20, 0, 0, 8), bowTo(20, 0, 0, 8)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, notes := Solids(extrusionOf(tc.profile), Millimetre)
			if !strings.Contains(strings.Join(notes, " | "), tc.rule.refusal) {
				t.Errorf("the validator did not say %q; it said %v", tc.rule.refusal, notes)
			}
			if !strings.Contains(guide, tc.rule.teach) {
				t.Errorf("the contract's curve guide does not teach %q", tc.rule.teach)
			}
		})
	}
	if len(curveRules) != 6 {
		t.Errorf("curveRules has %d rows and this test triggers 6; a new row needs a case here",
			len(curveRules))
	}
}

// segmentArea is the area between a chord c and its arc bowed by s, as the
// flattened drawing encloses it: the arc stepped into arcSegments chords, whose
// polygon is r²/2·(n·sin(θ/n) − sin θ). Exact for that polygon, so the fence can
// be tight; as n grows it tends to the true segment r²/2·(θ − sin θ), which is
// what the kernel fences hold the solid to.
func segmentArea(c, s float64) float64 {
	r := (c*c/4 + s*s) / (2 * s)
	theta := 2 * math.Asin(c/2/r)
	if s > r { // more than a semicircle
		theta = 2*math.Pi - theta
	}
	n := float64(arcSegments(theta))
	return r * r / 2 * (n*math.Sin(theta/n) - math.Sin(theta))
}

// Every example the guide prints builds exactly as printed, and the two whose
// area has a closed form enclose it. An example the validator refused would
// teach a model to be refused.
func TestCurveGuide_EveryExampleBuildsAsPrinted(t *testing.T) {
	guide := CurveGuide()
	want := map[string]float64{
		"A LENS":     2 * segmentArea(40, 8),
		"A CRESCENT": segmentArea(40, 14) - segmentArea(40, 6),
	}
	if len(curveExamples) < 3 {
		t.Fatalf("the guide shows %d examples; a lens, a crescent and a fender station were asked for",
			len(curveExamples))
	}
	for _, ex := range curveExamples {
		t.Run(ex.name, func(t *testing.T) {
			raw, _ := json.Marshal(ex.profile)
			if !strings.Contains(guide, string(raw)) {
				t.Errorf("the guide does not print the example's profile %s", raw)
			}
			// Reparsed from the printed JSON, which is what a model copies.
			var printed []Point
			if err := json.Unmarshal(raw, &printed); err != nil {
				t.Fatal(err)
			}
			solids, notes := Solids(extrusionOf(printed), Millimetre)
			if len(solids) != 1 || len(notes) != 0 {
				t.Fatalf("the example did not build cleanly: solids=%d notes=%v", len(solids), notes)
			}
			area, ok := want[ex.name]
			if !ok {
				return
			}
			flat := FlattenOutlineForTest(printed)
			pts := make([][2]float64, len(flat))
			for i, p := range flat {
				pts[i] = [2]float64{p[0], p[1]}
			}
			got := math.Abs(signedArea(pts))
			if math.Abs(got-area) > 1e-9*area {
				t.Errorf("%s encloses %.3f mm², want %.3f", ex.name, got, area)
			}
		})
	}
}

// The Go measurement of a bowed outline reaches the ARC, not its chords.
//
// The bow here is tilted so its furthest point falls between two chord ends;
// the test checks that first, because on a case where the chords happen to hit
// the extreme both answers agree and the fence would assert nothing.
func TestMeasure_ABowedEdgeReachesItsArcNotItsChords(t *testing.T) {
	profile := []Point{at(0, 0), at(40, 0), bowTo(0, 30, 32, 22)}
	doc := extrusionOf(profile)

	// The circle through (40,0), (32,22), (0,30), worked out independently of
	// arcThrough: the circumcentre from the perpendicular-bisector equations.
	ax, ay, bx, by, cx, cy := 40.0, 0.0, 32.0, 22.0, 0.0, 30.0
	d := 2 * (ax*(by-cy) + bx*(cy-ay) + cx*(ay-by))
	ux := ((ax*ax+ay*ay)*(by-cy) + (bx*bx+by*by)*(cy-ay) + (cx*cx+cy*cy)*(ay-by)) / d
	uy := ((ax*ax+ay*ay)*(cx-bx) + (bx*bx+by*by)*(ax-cx) + (cx*cx+cy*cy)*(bx-ax)) / d
	r := math.Hypot(ax-ux, ay-uy)
	// The arc runs anticlockwise from (40,0) through (32,22) to (0,30); its
	// highest point is at angle π/2 if that lies on it, and its rightmost at 0.
	start, end := math.Atan2(ay-uy, ax-ux), math.Atan2(cy-uy, cx-ux)
	on := func(a float64) bool { return a > start && a < end }
	wantMaxY, wantMaxX := 30.0, 40.0
	if on(math.Pi / 2) {
		wantMaxY = uy + r
	}
	if on(0) {
		wantMaxX = ux + r
	}
	if wantMaxX <= 40+1e-6 {
		t.Fatalf("the case is wrong: the arc does not pass its rightmost point (centre %.3f,%.3f r %.3f)",
			ux, uy, r)
	}

	chordMaxX, chordMaxY := math.Inf(-1), math.Inf(-1)
	for _, p := range FlattenOutlineForTest(profile) {
		chordMaxX = math.Max(chordMaxX, p[0])
		chordMaxY = math.Max(chordMaxY, p[1])
	}
	t.Logf("arc reaches (%.6f, %.6f); its chords, which this measured before B3, (%.6f, %.6f)",
		wantMaxX, wantMaxY, chordMaxX, chordMaxY)
	if chordMaxX > wantMaxX-1e-4 {
		t.Fatalf("the case is wrong: the chords reach x=%.6f, the arc %.6f — they must differ",
			chordMaxX, wantMaxX)
	}

	e := ExtentOf(doc)
	if e == nil {
		t.Fatal("no extent")
	}
	if math.Abs(e.Max[0]-wantMaxX) > 1e-9 || math.Abs(e.Max[1]-wantMaxY) > 1e-9 {
		t.Errorf("measured max (%.6f, %.6f), the arc reaches (%.6f, %.6f); the chords reach x=%.6f",
			e.Max[0], e.Max[1], wantMaxX, wantMaxY, chordMaxX)
	}
	if e.Min[0] != 0 || e.Min[1] != 0 {
		t.Errorf("measured min (%g, %g), want (0, 0)", e.Min[0], e.Min[1])
	}
}
