package geometry

import (
	"math"
	"strings"
	"testing"
)

func loop(pts ...[2]float64) []Point {
	out := make([]Point, len(pts))
	for i, p := range pts {
		out[i] = Point{X: p[0], Y: p[1]}
	}
	return out
}

func rect(x0, y0, x1, y1 float64) []Point {
	return loop([2]float64{x0, y0}, [2]float64{x1, y0}, [2]float64{x1, y1}, [2]float64{x0, y1})
}

// A hole in the outline takes material out of the solid, exactly.
//
// # Why the divergence theorem and not a triangle count
//
// A hole is not just a triangulation problem. It adds INNER WALLS, and those
// walls have to face into the void — out of the material — or the solid is
// partly inside out. Volume by the divergence theorem is zero for a surface with
// a gap in it, negative for one turned inside out, and wrong by exactly the
// hole's volume if the inner walls are missing or face the wrong way. One number
// holds all of it.
//
// The expectations are arithmetic: outer area less hole area, times the depth.
func TestExtrusion_AHoleTakesMaterialOut(t *testing.T) {
	for _, tc := range []struct {
		name  string
		outer []Point
		holes [][]Point
		want  float64
	}{
		{"one square hole", rect(0, 0, 40, 40),
			[][]Point{rect(10, 10, 30, 30)}, (1600 - 400) * 5},
		{"two holes", rect(0, 0, 60, 20),
			[][]Point{rect(5, 5, 15, 15), rect(45, 5, 55, 15)}, (1200 - 200) * 5},
		// A box section: the wall is what is left, and this is the case a sweep
		// needs, because a bent bore cannot be cut with a cylinder.
		{"a box section", rect(-10, -10, 10, 10),
			[][]Point{rect(-6, -6, 6, 6)}, (400 - 144) * 5},
		// The hole is concave, so bridging it is not the easy case.
		{"an L-shaped hole", rect(0, 0, 40, 40),
			[][]Point{loop([2]float64{10, 10}, [2]float64{30, 10}, [2]float64{30, 16},
				[2]float64{16, 16}, [2]float64{16, 30}, [2]float64{10, 30})},
			(1600 - (20*6 + 6*14)) * 5},
		// Wound the same way as the outline: the winding has to be normalised or
		// the "hole" is drawn as a second solid lump inside the part.
		{"a hole wound the wrong way", rect(0, 0, 40, 40),
			[][]Point{loop([2]float64{10, 10}, [2]float64{10, 30}, [2]float64{30, 30},
				[2]float64{30, 10})}, (1600 - 400) * 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			part := Part{ID: "e", Shape: "extrusion", Profile: tc.outer, Holes: tc.holes,
				Size: map[string]float64{"depth": 5}}
			tris, _ := extrusion(part, 5, Millimetre, func(string, ...any) {})
			if len(tris) == 0 {
				t.Fatal("nothing was drawn at all")
			}
			if vol := enclosedVolume(tris); math.Abs(vol-tc.want) > 1e-6 {
				t.Errorf("the solid encloses %.6f, want %.6f. Too much means the hole was "+
					"drawn as material or its walls are missing; a negative figure means "+
					"something is inside out", vol, tc.want)
			}

			// And the SURFACE, which volume cannot see.
			//
			// A hole is spliced into the outline by a bridge, and a wall built
			// over the merged ring would put a flat fin along that bridge,
			// standing inside the solid, drawn twice facing opposite ways. Its
			// contribution to the volume is exactly zero — the two copies cancel
			// — so every figure above stays correct. A drill on 2026-09-05 made
			// that substitution and nothing went red.
			//
			// The area is arithmetic the test does itself: two caps of the wall's
			// area, plus the depth times every loop's perimeter.
			want := 2 * tc.want / 5
			for _, l := range append([][]Point{tc.outer}, tc.holes...) {
				want += 5 * perimeterOf(l)
			}
			if got := surfaceArea(tris); math.Abs(got-want) > 1e-6 {
				t.Errorf("the surface measures %.6f, want %.6f. Too much means there are "+
					"facets inside the solid — a fin along the bridge that splices the hole "+
					"into the outline — and too little means a wall is missing", got, want)
			}
		})
	}
}

// The case a hole exists FOR: a bent tube, whose bore follows the path.
//
// # Why a cut cannot do this
//
// A bolt hole through a plate is a cylinder cut out of it, and that is still the
// right way to say it. A bore that turns a corner is not a cylinder and not any
// other shape this vocabulary can place in space — it is whatever the path is,
// offset inward. So it has to be part of the section, and the section has to be
// carried along the path with the SAME frames as the outline around it, or the
// wall thickness would vary round the bend for no reason a reader could see.
//
// The mitre identity does the checking: a section whose centroid rides the path
// sweeps area × path length, and the area here is the wall's, not the outline's.
func TestSwept_ABentTubeIsHollowAllTheWayRound(t *testing.T) {
	part := Part{ID: "s", Shape: "sweep",
		Profile: rect(-10, -10, 10, 10),
		Holes:   [][]Point{rect(-6, -6, 6, 6)},
		Path:    way([3]float64{0, 0, 0}, [3]float64{0, 0, 40}, [3]float64{30, 0, 40})}
	tris, _ := swept(part, Millimetre, func(string, ...any) {})
	if len(tris) == 0 {
		t.Fatal("nothing was drawn at all")
	}
	want := float64(400-144) * 70
	if vol := enclosedVolume(tris); math.Abs(vol-want) > 1e-6 {
		t.Errorf("the tube encloses %.6f, want %.6f (a 256 mm² wall carried 70 mm). The "+
			"outline's own 400 mm² would give %.0f, which is a solid bar", vol, want, 400.0*70)
	}
}

// A revolved section with a hole in it is a hollow ring.
//
// Pappus again, and it is worth having: the hole is swept round the axis at a
// different radius from the outline, so an implementation that turned the hole
// about the wrong centre — or forgot to turn it at all — is off by an amount no
// bounding box would show.
func TestRevolved_AHoleTurnsWithTheOutline(t *testing.T) {
	part := Part{ID: "r", Shape: "revolve", Axis: "y",
		Profile: rect(10, 0, 30, 20),
		Holes:   [][]Point{rect(15, 5, 25, 15)}}
	tris, _ := revolved(part, Millimetre, func(string, ...any) {})
	// Pappus: each rectangle sweeps 2π × (its centroid's radius) × its area.
	want := 2*math.Pi*20*(20*20) - 2*math.Pi*20*(10*10)
	vol := enclosedVolume(tris)
	// Tessellated round the turn, so inscribed and slightly under.
	if vol < want*0.99 || vol > want {
		t.Errorf("the ring encloses %.3f, want just under %.3f", vol, want)
	}
}

// What a document is told when its holes are not holes.
func TestProfileProblems_RefusesHolesThatAreNotHoles(t *testing.T) {
	for _, tc := range []struct {
		name, wants string
		part        Part
	}{
		{"a hole outside the outline", "not inside the outline",
			Part{ID: "e", Name: "Plate", Shape: "extrusion", Profile: rect(0, 0, 40, 40),
				Holes: [][]Point{rect(50, 50, 60, 60)}, Size: map[string]float64{"depth": 5}}},
		// Every corner of this hole is inside the material — one in each arm of
		// the U — and its edges cross the notch between them, which is not. A
		// convex outline cannot produce this case, and it is the one where
		// checking the corners alone would pass something unbuildable.
		{"a hole that spans a notch", "crosses the outline",
			Part{ID: "e", Name: "Channel", Shape: "extrusion",
				Profile: loop([2]float64{0, 0}, [2]float64{40, 0}, [2]float64{40, 40},
					[2]float64{30, 40}, [2]float64{30, 10}, [2]float64{10, 10},
					[2]float64{10, 40}, [2]float64{0, 40}),
				Holes: [][]Point{rect(5, 20, 35, 30)}, Size: map[string]float64{"depth": 5}}},
		{"two holes that overlap", "cross each other",
			Part{ID: "e", Name: "Plate", Shape: "extrusion", Profile: rect(0, 0, 40, 40),
				Holes: [][]Point{rect(10, 10, 25, 25), rect(20, 20, 35, 35)},
				Size:  map[string]float64{"depth": 5}}},
		{"an island inside a hole", "inside hole",
			Part{ID: "e", Name: "Plate", Shape: "extrusion", Profile: rect(0, 0, 40, 40),
				Holes: [][]Point{rect(15, 15, 25, 25), rect(5, 5, 35, 35)},
				Size:  map[string]float64{"depth": 5}}},
		{"a hole with two points", "at least 3",
			Part{ID: "e", Name: "Plate", Shape: "extrusion", Profile: rect(0, 0, 40, 40),
				Holes: [][]Point{loop([2]float64{10, 10}, [2]float64{20, 20})},
				Size:  map[string]float64{"depth": 5}}},
		{"a hole that crosses itself", "crosses itself",
			Part{ID: "e", Name: "Plate", Shape: "extrusion", Profile: rect(0, 0, 40, 40),
				// Deliberately LOPSIDED. A bow-tie drawn from the corners of a
				// rectangle has two lobes of equal and opposite area, so it is
				// caught by the "encloses nothing" check before the crossing one
				// and this case would test the wrong thing.
				Holes: [][]Point{loop([2]float64{10, 10}, [2]float64{35, 10},
					[2]float64{10, 25}, [2]float64{30, 30})},
				Size: map[string]float64{"depth": 5}}},
		{"a hole on a box", "carries holes but its shape is \"box\"",
			Part{ID: "b", Name: "Block", Shape: "box",
				Holes: [][]Point{rect(1, 1, 2, 2)}, Size: map[string]float64{"width": 5}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := Document{Name: "d", Units: "mm", Parts: []Part{tc.part}}
			problems := doc.ProfileProblems()
			if len(problems) == 0 {
				t.Fatalf("nothing was said about this part: %+v", tc.part)
			}
			var said []string
			for _, p := range problems {
				said = append(said, p.Name+" "+p.Detail)
			}
			if joined := strings.Join(said, " | "); !strings.Contains(joined, tc.wants) {
				t.Errorf("said %q, which does not contain %q", joined, tc.wants)
			}
		})
	}
}

// A hole that a rounded corner has swallowed is refused, and a hole that still
// fits is not.
//
// # Why the check runs on the flattened outline
//
// A corner radius moves the boundary INWARD. A hole tucked into the corner of a
// plate can sit comfortably inside the drawn rectangle and poke straight out
// through the rounded one — and the rounded one is the part. Checking the drawn
// corners would pass a document whose kernel build then fails with a message
// that names no loop and no point.
func TestProfileProblems_AHoleMustFitTheROUNDEDOutline(t *testing.T) {
	// A hole hard against the corner of a 40×40 plate. With square corners it
	// fits; with R15 corners the boundary has moved past it.
	hole := rect(1, 1, 9, 9)
	square := Part{ID: "e", Name: "Plate", Shape: "extrusion", Profile: rect(0, 0, 40, 40),
		Holes: [][]Point{hole}, Size: map[string]float64{"depth": 5}}
	squareDoc := Document{Name: "d", Units: "mm", Parts: []Part{square}}
	if p := squareDoc.ProfileProblems(); len(p) != 0 {
		t.Fatalf("a hole well inside a square-cornered plate was refused: %+v", p)
	}

	rounded := square
	rounded.Profile = []Point{{X: 0, Y: 0, Radius: 15}, {X: 40, Y: 0, Radius: 15},
		{X: 40, Y: 40, Radius: 15}, {X: 0, Y: 40, Radius: 15}}
	roundedDoc := Document{Name: "d", Units: "mm", Parts: []Part{rounded}}
	problems := roundedDoc.ProfileProblems()
	if len(problems) == 0 {
		t.Fatal("the same hole was accepted on a plate whose corner had been rounded away " +
			"from underneath it; the kernel would refuse this and name nothing")
	}
	if !strings.Contains(problems[0].Detail, "not inside the outline") {
		t.Errorf("the refusal does not say what is wrong: %q", problems[0].Detail)
	}
}

// surfaceArea sums the triangles' areas — every facet, including any that should
// not be there.
func surfaceArea(tris []Triangle) float64 {
	var total float64
	for _, t := range tris {
		u := sub3(t.B, t.A)
		v := sub3(t.C, t.A)
		total += length3(cross3(u, v)) / 2
	}
	return total
}

func perimeterOf(loop []Point) float64 {
	var total float64
	for i := range loop {
		j := (i + 1) % len(loop)
		dx, dy := loop[j].X-loop[i].X, loop[j].Y-loop[i].Y
		total += math.Sqrt(dx*dx + dy*dy)
	}
	return total
}
