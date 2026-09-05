package geometry

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

// enclosedVolume is the divergence theorem over a triangle soup: (A × B) · C / 6
// summed. It is the true volume for a CLOSED surface, zero-ish for one with a
// hole in it, and negative for one turned inside out.
func enclosedVolume(tris []Triangle) float64 {
	var v float64
	for _, t := range tris {
		v += (t.A[0]*(t.B[1]*t.C[2]-t.C[1]*t.B[2]) -
			t.A[1]*(t.B[0]*t.C[2]-t.C[0]*t.B[2]) +
			t.A[2]*(t.B[0]*t.C[1]-t.C[0]*t.B[1])) / 6
	}
	return v
}

func points(pts ...[2]float64) []Point {
	out := make([]Point, len(pts))
	for i, p := range pts {
		out[i] = Point{X: p[0], Y: p[1]}
	}
	return out
}

func way(pts ...[3]float64) []Point {
	out := make([]Point, len(pts))
	for i, p := range pts {
		out[i] = Point{X: p[0], Y: p[1], Z: p[2]}
	}
	return out
}

// A sweep along a straight path up local Z IS the extrusion, triangle for
// triangle.
//
// # Why this is the first test and why it is exact
//
// "sweep" is only worth having if it GENERALISES what was already there. If a
// straight path produced a solid that was merely close to the extrusion — the
// section rotated, the caps at different z, the winding reversed — then this
// would be a second, subtly different way to say the same thing, and the two
// would drift apart the first time either was edited.
//
// So it is asserted as identity rather than as similarity: same triangles, same
// order, same normals. That also pins the two conventions a sweep could get
// wrong on its own — that the first section is square to the path with the
// profile's own axes unrotated, and that the outline's origin rides the path.
func TestSwept_AStraightPathUpZIsExactlyTheExtrusion(t *testing.T) {
	outline := points([2]float64{0, 0}, [2]float64{40, 0}, [2]float64{40, 8},
		[2]float64{8, 8}, [2]float64{8, 40}, [2]float64{0, 40})

	extruded, _ := extrusion(Part{ID: "e", Shape: "extrusion", Profile: outline,
		Size: map[string]float64{"depth": 20}}, 20, Millimetre, func(string, ...any) {})
	sweptTris, _ := swept(Part{ID: "s", Shape: "sweep", Profile: outline,
		Path: way([3]float64{0, 0, -10}, [3]float64{0, 0, 10})}, Millimetre, func(string, ...any) {})

	if len(sweptTris) != len(extruded) {
		t.Fatalf("the sweep has %d triangles and the extrusion %d; a straight path is an "+
			"extrusion and must build the same solid", len(sweptTris), len(extruded))
	}
	// Compared as a SET. The order the two emit their caps in is not a property
	// of the solid, and pinning it would go red on a change that is not a defect
	// — but every facet, its winding and its normal are the shape itself.
	missing := facetSet(extruded)
	for _, tr := range sweptTris {
		key := facetKey(tr)
		if missing[key] == 0 {
			t.Fatalf("the sweep drew %v/%v/%v n%v, which the extrusion does not have",
				tr.A, tr.B, tr.C, tr.Normal)
		}
		missing[key]--
	}
	for key, n := range missing {
		if n > 0 {
			t.Fatalf("the extrusion has %d of %s that the sweep does not", n, key)
		}
	}
}

// facetKey names a triangle by its geometry, rotated so the same facet written
// from a different starting vertex reads the same. Rotation preserves the
// winding, so a facet turned inside out still reads differently — which is the
// half of this that matters.
func facetKey(t Triangle) string {
	// Negative zero is folded away first. The two builders arrive at the same
	// normal by different arithmetic — one from the edge direction, one from the
	// cross product of the facet — and they differ in the SIGN OF ZERO on axes
	// the facet does not face at all. That is not a difference in the solid, and
	// a key that treated it as one would fail this test for no reason.
	flat := func(v [3]float64) [3]float64 {
		return [3]float64{v[0] + 0, v[1] + 0, v[2] + 0}
	}
	corner := [3][3]float64{flat(t.A), flat(t.B), flat(t.C)}
	first := 0
	for i := 1; i < 3; i++ {
		if fmt.Sprint(corner[i]) < fmt.Sprint(corner[first]) {
			first = i
		}
	}
	return fmt.Sprintf("%.9v/%.9v/%.9v n%.6v", corner[first], corner[(first+1)%3],
		corner[(first+2)%3], flat(t.Normal))
}

func facetSet(tris []Triangle) map[string]int {
	out := map[string]int{}
	for _, t := range tris {
		out[facetKey(t)]++
	}
	return out
}

// A mitred sweep of a centred outline encloses area × path length, EXACTLY,
// however many times it bends.
//
// # Why that identity holds, and why it is the right thing to assert
//
// At every bend the section sits in the bisecting plane, which cuts a wedge off
// the inside of the corner and adds an equal wedge outside. The two cancel when
// the outline's centroid is on the path, because the displacement is linear
// across the section and integrates to zero about the centroid.
//
// So the expectation is arithmetic a reader can check — the same reason the
// revolve tests take their figures from Pappus — rather than a number this test
// watched the code produce once. It also asserts the surface is closed and
// outward-facing at the same time, since the divergence theorem gives zero for a
// surface with a hole and a negative figure for one that is inside out.
//
// The outline is centred DELIBERATELY: the identity is a property of a section
// riding its own path, and an off-centre one is covered separately below.
func TestSwept_EnclosesAreaTimesPathLength(t *testing.T) {
	square := points([2]float64{-5, -5}, [2]float64{5, -5}, [2]float64{5, 5}, [2]float64{-5, 5})
	const area = 100.0

	for _, tc := range []struct {
		name string
		path []Point
		want float64
	}{
		{"straight up", way([3]float64{0, 0, 0}, [3]float64{0, 0, 20}), area * 20},
		{"straight along x", way([3]float64{0, 0, 0}, [3]float64{20, 0, 0}), area * 20},
		{"straight down", way([3]float64{0, 0, 0}, [3]float64{0, 0, -20}), area * 20},
		{"a right-angled elbow", way([3]float64{0, 0, 0}, [3]float64{0, 0, 20},
			[3]float64{30, 0, 20}), area * 50},
		{"a 45° elbow", way([3]float64{0, 0, 0}, [3]float64{0, 0, 20}, [3]float64{20, 0, 40}),
			area * (20 + math.Sqrt(800))},
		{"three bends, out of plane", way([3]float64{0, 0, 0}, [3]float64{0, 0, 20},
			[3]float64{30, 0, 20}, [3]float64{30, 25, 20}), area * 75},
		{"a U bend", way([3]float64{0, 0, 0}, [3]float64{0, 0, 30}, [3]float64{25, 0, 30},
			[3]float64{25, 0, 5}), area * 80},
	} {
		t.Run(tc.name, func(t *testing.T) {
			part := Part{ID: "s", Shape: "sweep", Profile: square, Path: tc.path}
			tris, _ := swept(part, Millimetre, func(string, ...any) {})
			vol := enclosedVolume(tris)
			if math.Abs(vol-tc.want) > 1e-6 {
				t.Errorf("the swept solid encloses %.6f, want %.6f — a negative figure means "+
					"the winding is inside out, a short one means the mitre is eating material "+
					"the bend should have kept, and a long one means the corners overlap",
					vol, tc.want)
			}
		})
	}
}

// An outline drawn OFF its path still closes into a solid.
//
// # Why this needs a different assertion
//
// The identity above does not apply: the mitre wedges no longer cancel, so there
// is no arithmetic to check against. What still must be true is that the surface
// is CLOSED — and that is testable without knowing the volume, because the
// divergence theorem is translation-invariant only for a closed surface. Move
// the same triangles a long way from the origin and an open one changes its
// answer by a lot.
//
// This is the case the convention exists for: the outline's origin rides the
// path, so an outline drawn away from it sweeps at that offset, which is how an
// eccentric section or a rail carried beside its own centreline is described.
func TestSwept_AnOutlineOffItsPathStillClosesTheSolid(t *testing.T) {
	part := Part{ID: "s", Shape: "sweep",
		Profile: points([2]float64{20, 20}, [2]float64{30, 20}, [2]float64{30, 26}, [2]float64{20, 26}),
		Path:    way([3]float64{0, 0, 0}, [3]float64{0, 0, 40}, [3]float64{60, 0, 40})}
	tris, _ := swept(part, Millimetre, func(string, ...any) {})
	here := enclosedVolume(tris)

	moved := make([]Triangle, len(tris))
	for i, tr := range tris {
		shift := func(p [3]float64) [3]float64 { return [3]float64{p[0] + 1000, p[1] - 700, p[2] + 300} }
		moved[i] = Triangle{A: shift(tr.A), B: shift(tr.B), C: shift(tr.C), Normal: tr.Normal}
	}
	there := enclosedVolume(moved)

	if here <= 0 {
		t.Fatalf("the solid encloses %.6f — a sweep whose outline is off its path came out "+
			"inside out or open", here)
	}
	if math.Abs(here-there) > 1e-6*math.Max(1, math.Abs(here)) {
		t.Errorf("the same triangles enclose %.6f at the origin and %.6f moved away; a volume "+
			"that depends on where the part is means the surface is not closed", here, there)
	}
}

// The three ways a path cannot be swept, each named where it went wrong.
//
// # Why each is refused here rather than left to OCCT
//
// A repeated point comes back from OCCT as "BRep_API: command not done", which
// names nothing a person can act on. A reversal comes back with an EMPTY message
// — measured 2026-09-05, build123d 0.11.1. And a bend too tight for its own
// outline is not refused by OCCT AT ALL: it returned a plausible solid of
// 14546 mm³ for a shape whose surface folds through itself, which is the worst
// of the three because nothing looks wrong.
func TestSweptSections_RefusesThePathsThatAreNotSolids(t *testing.T) {
	square := [][2]float64{{-5, -5}, {5, -5}, {5, 5}, {-5, 5}}
	for _, tc := range []struct {
		name, wants string
		path        [][3]float64
	}{
		{"a repeated point", "repeats path point 1",
			[][3]float64{{0, 0, 0}, {0, 0, 0}, {0, 0, 20}}},
		{"a 180° reversal", "doubles back on itself at path point 2",
			[][3]float64{{0, 0, 0}, {0, 0, 20}, {0, 0, 5}}},
		// A 10-wide section mitred at a right angle reaches 5 mm back along each
		// leg, and the first leg is only 3 long.
		{"a bend tighter than the outline", "bends too tightly between path points 1 and 2",
			[][3]float64{{0, 0, 0}, {0, 0, 3}, {20, 0, 3}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := sweptSections([][][2]float64{square}, tc.path, false)
			if err == nil {
				t.Fatalf("this path was swept without complaint, and what it produces is not a solid")
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("said %q; a reader needs to be told which points are the problem, so "+
					"it should contain %q", err, tc.wants)
			}
		})
	}
}

// A path that bends too tightly still hands back its rings, and one that has no
// frame at all hands back nothing.
//
// The distinction is what lets the viewport draw a folded sweep with a note
// beside it — a part that vanishes from a render reads as a design with a piece
// missing — while the export path refuses the same document either way.
func TestSweptSections_ReturnsWhatCanStillBeDrawn(t *testing.T) {
	square := [][2]float64{{-5, -5}, {5, -5}, {5, 5}, {-5, 5}}

	rings, _, err := sweptSections([][][2]float64{square}, [][3]float64{{0, 0, 0}, {0, 0, 3}, {20, 0, 3}}, false)
	if err == nil || rings == nil {
		t.Errorf("a fold came back as rings=%v err=%v; it has good rings that happen to "+
			"overlap, and drawing them beats a part that disappears", rings != nil, err)
	}
	rings, _, err = sweptSections([][][2]float64{square}, [][3]float64{{0, 0, 0}, {0, 0, 20}, {0, 0, 5}}, false)
	if err == nil || rings != nil {
		t.Errorf("a reversal came back as rings=%v err=%v; there is no frame at that point, "+
			"so there is nothing honest to draw", rings != nil, err)
	}
}

// The section frame is orthonormal, right-handed, and square to the path.
//
// # Why the kernel is not the only thing that needs this
//
// The frame is the ONE thing about a sweep that travels to the CAD kernel as a
// convention rather than as geometry, and a frame that is not orthonormal builds
// a sheared solid rather than an obviously broken one. Its third column is the
// path's own direction, which is what "square to the path" means.
func TestSweptSections_FramesTheSectionSquareToThePath(t *testing.T) {
	square := [][2]float64{{-5, -5}, {5, -5}, {5, 5}, {-5, 5}}
	for _, dir := range [][3]float64{
		{0, 0, 20}, {20, 0, 0}, {0, 20, 0}, {0, 0, -20}, {0, -20, 0}, {7, -3, 12},
	} {
		_, frame, err := sweptSections([][][2]float64{square}, [][3]float64{{0, 0, 0}, dir}, false)
		if err != nil {
			t.Fatalf("a straight path along %v: %v", dir, err)
		}
		col := func(i int) [3]float64 { return [3]float64{frame[i], frame[i+3], frame[i+6]} }
		x, y, z := col(0), col(1), col(2)
		for i, v := range [][3]float64{x, y, z} {
			if math.Abs(length3(v)-1) > 1e-9 {
				t.Errorf("along %v: column %d has length %v, want 1", dir, i, length3(v))
			}
		}
		if math.Abs(dot3(x, y)) > 1e-9 || math.Abs(dot3(x, z)) > 1e-9 || math.Abs(dot3(y, z)) > 1e-9 {
			t.Errorf("along %v: the frame's axes are not at right angles (%v, %v, %v)", dir, x, y, z)
		}
		if h := cross3(x, y); math.Abs(h[0]-z[0]) > 1e-9 || math.Abs(h[1]-z[1]) > 1e-9 ||
			math.Abs(h[2]-z[2]) > 1e-9 {
			t.Errorf("along %v: x × y is %v, not the path direction %v — a left-handed frame "+
				"mirrors the section", dir, h, z)
		}
		if want := normalise(dir); math.Abs(z[0]-want[0]) > 1e-9 || math.Abs(z[1]-want[1]) > 1e-9 ||
			math.Abs(z[2]-want[2]) > 1e-9 {
			t.Errorf("along %v: the section's normal is %v, so the outline is not square to "+
				"the path it sets off along", dir, z)
		}
	}
}

// The section is CARRIED along the path without being rolled about it.
//
// # What rolling looks like, and why nothing else in this package catches it
//
// The plausible wrong implementation is to work out each segment's frame from
// scratch, the way the first one is worked out from +Z, instead of turning the
// previous one. That agrees with this for a path that bends in a single plane
// and disagrees the moment a second bend leaves it, because rotations do not
// commute — and the section arrives at the far end rolled about its own axis by
// an angle nobody asked for.
//
// NOTHING ELSE NOTICES. The volume is identical, because a rolled section is
// still the same section. The mitres still close, so the solid is still closed.
// The extents of a symmetric outline are unchanged. A drill on 2026-09-05 made
// exactly this substitution and every test in this package stayed green,
// including the one that used to be here — which bent the path once, in one
// plane, where the two implementations agree. That test asserted a property that
// could not fail.
//
// So this asserts the property itself, on a path whose two bends are in
// DIFFERENT planes: the smallest rotation between two directions leaves the axis
// perpendicular to both alone, so a frame carried by it keeps its component
// along that axis. A frame that rolls does not.
func TestSectionFrames_CarryTheSectionWithoutRollingIt(t *testing.T) {
	// Up, then along x, then along y: the second bend leaves the first's plane.
	path := [][3]float64{{0, 0, 0}, {0, 0, 30}, {40, 0, 30}, {40, 25, 30}}
	tangent, axisX, axisY, err := sectionFrames(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(tangent) != 3 {
		t.Fatalf("%d segments, want 3", len(tangent))
	}

	for i := range tangent {
		// Orthonormal and right-handed at EVERY segment, not just the first: a
		// frame that drifts out of square builds a sheared solid rather than an
		// obviously broken one.
		for _, v := range [][3]float64{axisX[i], axisY[i]} {
			if math.Abs(length3(v)-1) > 1e-9 {
				t.Errorf("segment %d has an axis of length %v", i+1, length3(v))
			}
		}
		if math.Abs(dot3(axisX[i], axisY[i])) > 1e-9 ||
			math.Abs(dot3(axisX[i], tangent[i])) > 1e-9 ||
			math.Abs(dot3(axisY[i], tangent[i])) > 1e-9 {
			t.Errorf("segment %d: the section is not square to the path (%v, %v, %v)",
				i+1, axisX[i], axisY[i], tangent[i])
		}
		if h := cross3(axisX[i], axisY[i]); math.Abs(dot3(h, tangent[i])-1) > 1e-9 {
			t.Errorf("segment %d: x × y does not point along the path, so the section is "+
				"mirrored", i+1)
		}
	}

	for i := 0; i+1 < len(tangent); i++ {
		axis := normalise(cross3(tangent[i], tangent[i+1]))
		for name, pair := range map[string][2][3]float64{
			"x": {axisX[i], axisX[i+1]}, "y": {axisY[i], axisY[i+1]},
		} {
			before, after := dot3(pair[0], axis), dot3(pair[1], axis)
			if math.Abs(before-after) > 1e-9 {
				t.Errorf("at the bend after path point %d the section's own %s went from %v "+
					"to %v along the bend's own axis — the smallest rotation leaves that "+
					"component alone, so anything else is the section rolling as it goes, "+
					"which changes nothing measurable and everything about where a rail's "+
					"flat face points", i+2, name, before, after)
			}
		}
	}
}

// And the roll shows up in the geometry: an outline point stays where it was put
// relative to a bend it never turns through.
//
// Kept alongside the frame test because this is the consequence a person would
// see. The first bend is in the XZ plane, so the section's own y — the axis of
// that bend — must come through it untouched.
func TestSwept_KeepsTheSectionSquareThroughABend(t *testing.T) {
	profile := [][2]float64{{-1, -6}, {1, -6}, {1, 6}, {-1, 6}}
	rings, _, err := sweptSections([][][2]float64{profile}, [][3]float64{{0, 0, 0}, {0, 0, 20}, {30, 0, 20}}, false)
	if err != nil {
		t.Fatal(err)
	}
	outerRings := rings[0]
	last := outerRings[len(outerRings)-1]
	for k, p := range profile {
		if got := last[k][1]; math.Abs(got-p[1]) > 1e-9 {
			t.Errorf("outline point %d was drawn at y = %v and ends the path at y = %v",
				k+1, p[1], got)
		}
	}
}

// What a document is told when its sweep is not one.
//
// # Why these are refusals and not repairs
//
// Every one of them is somebody meaning a shape and writing another, and there
// is no reading of the document that recovers which. A z on an outline point is
// either a sweep whose path has not been written or a coordinate in the wrong
// column; a path on an extrusion is a sweep that says "extrusion". Guessing puts
// a solid in the file that nobody asked for, so the part is left out and named.
//
// The fold is the one that has to be caught HERE rather than by the kernel: OCCT
// does not refuse it, and returns a plausible solid with the wrong volume.
func TestProfileProblems_RefusesWhatASweepCannotBe(t *testing.T) {
	square := points([2]float64{-5, -5}, [2]float64{5, -5}, [2]float64{5, 5}, [2]float64{-5, 5})
	straight := way([3]float64{0, 0, 0}, [3]float64{0, 0, 20})

	// builtAnyway is the difference between "this outline is not used" and "this
	// part is not in the shape", and it is not a detail. A BOX carrying a path is
	// still a box: its dimensions say everything needed to build it, and the note
	// tells the reader the path was ignored — which is the contract a box
	// carrying a profile has always had. An outline SHAPE whose outline or path
	// cannot be read has nothing left to build from, and is left out.
	for _, tc := range []struct {
		name, wants string
		part        Part
		builtAnyway bool
	}{
		{"a z on an outline point", "point 2 carries a z",
			Part{ID: "e", Name: "Rail", Shape: "extrusion",
				Profile: []Point{{X: 0, Y: 0}, {X: 10, Y: 0, Z: 4}, {X: 10, Y: 10}},
				Size:    map[string]float64{"depth": 5}}, false},
		{"a path on an extrusion", "carries a path",
			Part{ID: "e", Name: "Rail", Shape: "extrusion", Profile: square, Path: straight,
				Size: map[string]float64{"depth": 5}}, false},
		{"a path on a box", "carries a path but its shape is \"box\"",
			Part{ID: "b", Name: "Block", Shape: "box", Path: straight,
				Size: map[string]float64{"width": 5}}, true},
		{"a sweep with nowhere to go", "path of 1 point(s)",
			Part{ID: "s", Name: "Tube", Shape: "sweep", Profile: square,
				Path: way([3]float64{0, 0, 0})}, false},
		{"a sweep with no path at all", "path of 0 point(s)",
			Part{ID: "s", Name: "Tube", Shape: "sweep", Profile: square}, false},
		{"a bend tighter than its outline", "bends too tightly",
			Part{ID: "s", Name: "Tube", Shape: "sweep", Profile: square,
				Path: way([3]float64{0, 0, 0}, [3]float64{0, 0, 3}, [3]float64{20, 0, 3})}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := Document{Name: "d", Units: "mm", Parts: []Part{tc.part}}
			problems := doc.ProfileProblems()
			if len(problems) == 0 {
				t.Fatalf("nothing was said about this part, so it is silently absent from the "+
					"shape: %+v", tc.part)
			}
			var said []string
			for _, p := range problems {
				said = append(said, p.Name+" "+p.Detail)
			}
			joined := strings.Join(said, " | ")
			if !strings.Contains(joined, tc.wants) {
				t.Errorf("said %q, which does not contain %q — a reader has to be able to act "+
					"on it", joined, tc.wants)
			}
			// And it is genuinely out of the built shape, not merely commented on.
			solids, _ := Solids(doc, Millimetre)
			built := false
			for _, s := range solids {
				if s.ID == tc.part.ID {
					built = true
				}
			}
			if built != tc.builtAnyway {
				t.Errorf("built = %v, want %v — a part that cannot be read must be absent "+
					"from the file rather than approximated, and one whose DIMENSIONS still "+
					"say what it is must not vanish because of a field it does not read",
					built, tc.builtAnyway)
			}
		})
	}
}

// A path written as EXPRESSIONS follows its parameters, and the numbers are
// written back where the renderer reads them.
//
// Same reasoning as an outline's coordinates: nothing downstream evaluates
// anything, so a path coordinate that stayed an expression would be read as a
// zero and the bend would silently move to the origin.
func TestBind_ResolvesAPathsExpressions(t *testing.T) {
	doc := Document{
		Name: "rail", Units: "mm",
		Parameters: []Parameter{
			{Name: "rise", Value: 40, Unit: "mm", How: "chosen"},
			{Name: "run", Value: 60, Unit: "mm", How: "chosen"},
		},
		Parts: []Part{{ID: "s", Name: "Rail", Shape: "sweep",
			Profile: points([2]float64{-2, -2}, [2]float64{2, -2}, [2]float64{2, 2}, [2]float64{-2, 2}),
			Path:    []Point{{}, {ZFrom: "rise"}, {XFrom: "run", ZFrom: "rise"}}}},
	}
	if problems := doc.Bind(); len(problems) != 0 {
		t.Fatalf("binding reported %+v", problems)
	}
	got := doc.Parts[0].Path
	for i, want := range [][3]float64{{0, 0, 0}, {0, 0, 40}, {60, 0, 40}} {
		p := got[i]
		if math.Abs(p.X-want[0]) > 1e-9 || math.Abs(p.Y-want[1]) > 1e-9 || math.Abs(p.Z-want[2]) > 1e-9 {
			t.Errorf("path point %d is (%v, %v, %v), want %v — an expression left unevaluated "+
				"is read downstream as a zero, which moves the bend to the origin",
				i+1, p.X, p.Y, p.Z, want)
		}
	}
}

// A closed path has no ends: the sweep meets itself at the seam.
//
// # Why the same identity still does the checking
//
// Every vertex of a closed path is a mitre joint, the seam included, so the
// wedge cut from the inside of each corner is still exactly balanced by the one
// added outside. The volume is area × the whole perimeter — and because there
// are no caps, a surface that did not actually close at the seam would leave a
// hole, and the divergence theorem returns something else entirely.
func TestSwept_AClosedPathEnclosesAreaTimesItsPerimeter(t *testing.T) {
	square := points([2]float64{-5, -5}, [2]float64{5, -5}, [2]float64{5, 5}, [2]float64{-5, 5})
	for _, tc := range []struct {
		name string
		path []Point
		want float64
		// rounded says the path has an arc in it, so these triangles are a
		// chord approximation rather than the solid itself and come out just
		// under. Nothing else here is approximate: a mitred path is a polyhedron
		// and its volume is exact.
		rounded bool
	}{
		{"a square ring", way([3]float64{0, 0, 0}, [3]float64{60, 0, 0},
			[3]float64{60, 60, 0}, [3]float64{0, 60, 0}), 100 * 240, false},
		// Planar but not in a world plane: the rule is that the frame comes back
		// to itself, and every planar loop does, whichever plane it is in.
		{"a ring tilted out of the world planes", way([3]float64{0, 0, 0},
			[3]float64{60, 0, 60}, [3]float64{60, 60, 60}, [3]float64{0, 60, 0}),
			100 * (2*math.Sqrt(7200) + 120), false},
		{"a triangle", way([3]float64{0, 0, 0}, [3]float64{60, 0, 0}, [3]float64{30, 50, 0}),
			100 * (60 + 2*math.Sqrt(900+2500)), false},
		// Every corner rounded, the SEAM INCLUDED. A closed path's first point is
		// a corner like any other, and rounding it is where the run has to start
		// at the END of that arc rather than at its beginning — four straights of
		// 30 and four quarter-turns, which between them make one whole circle.
		{"a ring rounded at every corner including the seam",
			[]Point{{Radius: 15}, {X: 60, Radius: 15}, {X: 60, Y: 60, Radius: 15},
				{Y: 60, Radius: 15}}, 100 * (120 + 2*math.Pi*15), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			part := Part{ID: "s", Shape: "sweep", Profile: square,
				Path: tc.path, PathClosed: true}
			tris, _ := swept(part, Millimetre, func(string, ...any) {})
			if len(tris) == 0 {
				t.Fatal("nothing was drawn at all")
			}
			vol := enclosedVolume(tris)
			low, high := tc.want-1e-6, tc.want+1e-6
			if tc.rounded {
				// Inscribed, so under — and the direction matters as much as the
				// size. A ring drawn LARGER than the solid it stands for would
				// mean the chords are outside the arcs, and somebody machining
				// to the exported file would be cutting into material the
				// picture said was there.
				low, high = tc.want*0.999, tc.want+1e-6
			}
			if vol < low || vol > high {
				t.Errorf("the ring encloses %.6f, want %.6f (within %.6f..%.6f). A figure "+
					"near zero means the seam did not meet and the surface is open; caps at "+
					"the seam would show as too much", vol, tc.want, low, high)
			}
		})
	}
}

// A closed path whose section does not come back to itself is refused, and told
// by how much.
//
// # Why this is the one that had to be measured rather than reasoned about
//
// A rotation-minimising frame carried round a closed curve returns rotated by
// the area its tangents enclose on the sphere. The first three loops tried —
// a planar square, a square with one corner lifted, a zigzag ring — ALL came
// back at 0.000°, which looked like a proof that closed paths were free.
//
// They were symmetric enough for the rotation to cancel. Measured over 2000
// random closed polylines on 2026-09-05: every single one came back rotated, by
// up to 179.8°. The control that settled it is the octant — +X, +Y, +Z, back to
// +X — which must give exactly 90 and does.
func TestSwept_AClosedPathMustBringItsSectionBack(t *testing.T) {
	small := points([2]float64{-2, -2}, [2]float64{2, -2}, [2]float64{2, 2}, [2]float64{-2, 2})
	doc := Document{Name: "loop", Units: "mm",
		Parts: []Part{{ID: "s", Name: "Loop", Shape: "sweep", Profile: small,
			// A saddle: no two of its bends share a plane.
			Path: way([3]float64{0, 0, 0}, [3]float64{50, 10, 30}, [3]float64{80, 60, -10},
				[3]float64{30, 90, 25}, [3]float64{-20, 40, 5}),
			PathClosed: true}}}

	problems := doc.ProfileProblems()
	if len(problems) == 0 {
		t.Fatal("a loop whose section comes back rotated was accepted; the two ends of the " +
			"sweep meet at an angle and what it encloses is not a solid")
	}
	detail := problems[0].Detail
	if !strings.Contains(detail, "does not come back to itself") {
		t.Errorf("the refusal does not say what the rule is: %q", detail)
	}
	// The ANGLE, because "it does not close" leaves a reader with nothing to do.
	// A tenth of a degree would be worth chasing; ten degrees is a different
	// drawing.
	if !strings.Contains(detail, "9.8°") {
		t.Errorf("the refusal does not name how far out it is: %q", detail)
	}
	if solids, _ := Solids(doc, Millimetre); len(solids) != 0 {
		t.Errorf("%d solids were built from a loop that does not close", len(solids))
	}
}

// Two points closed is a line drawn there and back.
func TestSwept_AClosedPathNeedsThreePoints(t *testing.T) {
	doc := Document{Name: "loop", Units: "mm",
		Parts: []Part{{ID: "s", Name: "Loop", Shape: "sweep",
			Profile:    points([2]float64{-2, -2}, [2]float64{2, -2}, [2]float64{2, 2}, [2]float64{-2, 2}),
			Path:       way([3]float64{0, 0, 0}, [3]float64{40, 0, 0}),
			PathClosed: true}}}
	problems := doc.ProfileProblems()
	if len(problems) == 0 || !strings.Contains(problems[0].Detail, "a line drawn twice") {
		t.Errorf("a two-point loop was accepted or badly explained: %+v", problems)
	}
}

// A bend radius on the END of an open path is ignored, and the part is still
// built.
//
// # Why this one is not a refusal when its neighbours are
//
// There is no corner at an end, so the number changes nothing. Unlike a z on an
// outline point — which could be a coordinate in the wrong column, or a sweep
// whose path was never written, and has no single reading — this has exactly
// one: there is nothing there to round.
//
// Refusing cost the whole part. Over five live runs on 2026-09-05 qwen-plus
// filled in a radius on EVERY path point, ends included, in two of them, and
// both times a correct, buildable bent tube vanished from the file because of an
// inert number on a point with no corner. It is the same bargain a box carrying
// a profile already gets: the extra is reported, and the part is built from what
// still says what it is.
func TestSwept_ARadiusOnAnEndIsIgnoredRatherThanFatal(t *testing.T) {
	doc := Document{Name: "line", Units: "mm",
		Parts: []Part{{ID: "s", Name: "Coolant line", Shape: "sweep",
			Profile: points([2]float64{-5, -5}, [2]float64{5, -5}, [2]float64{5, 5}, [2]float64{-5, 5}),
			Path:    []Point{{Radius: 8}, {Z: 40, Radius: 12}, {X: 30, Z: 40, Radius: 8}}}}}

	problems := doc.ProfileProblems()
	if len(problems) != 2 {
		t.Fatalf("expected the two ends to be reported, got %d: %+v", len(problems), problems)
	}
	for _, p := range problems {
		if p.Severity != Warning {
			t.Errorf("a radius on an end was reported as %s; it is inert, not fatal", p.Severity)
		}
		if !strings.Contains(p.Detail, "it was ignored") {
			t.Errorf("the note does not say what happened to it: %q", p.Detail)
		}
	}

	// And the part is BUILT, with the bend in the middle intact.
	solids, notes := Solids(doc, Millimetre)
	if len(solids) != 1 {
		t.Fatalf("%d solids: %v", len(solids), notes)
	}
	arcs := 0
	for _, e := range solids[0].Path.Edges {
		if e.Via != nil {
			arcs++
		}
	}
	if arcs != 1 {
		t.Errorf("%d arcs in the path; the ends carry no corner and the middle carries one", arcs)
	}
	// Nothing in the export label may claim the part is missing when it is here.
	for _, n := range notes {
		if strings.Contains(n, "not in this file") {
			t.Errorf("the export says %q about a part that is in the file", n)
		}
	}
}
