package geometry

import (
	"math"
	"testing"
	"time"
)

// The test that matters: any CORRECT triangulation of an outline has the same
// total area as the outline. Asserting on the triangles themselves would pin an
// implementation detail and go red on a change that is not a defect.
func TestTriangulate_CoversExactlyTheOutlinesArea(t *testing.T) {
	for _, tc := range []struct {
		name string
		pts  [][2]float64
		area float64
	}{
		{"a square", [][2]float64{{0, 0}, {10, 0}, {10, 10}, {0, 10}}, 100},
		{"a triangle", [][2]float64{{0, 0}, {10, 0}, {0, 10}}, 50},
		// THE case a triangle fan gets wrong, and the first shape anybody draws.
		{"an L-bracket", [][2]float64{{0, 0}, {40, 0}, {40, 8}, {8, 8}, {8, 40}, {0, 40}}, 576},
		// 60x10 flange plus a 10-wide web standing 40 tall. The first version of
		// this line said 600+250 and the triangulation said 1000; the
		// triangulation was right.
		{"a T-section", [][2]float64{{0, 0}, {60, 0}, {60, 10}, {35, 10}, {35, 50}, {25, 50}, {25, 10}, {0, 10}}, 60*10 + 10*40},
		{"a channel", [][2]float64{{0, 0}, {40, 0}, {40, 30}, {32, 30}, {32, 8}, {8, 8}, {8, 30}, {0, 30}}, 40*8 + 2*8*22},
		{"a clockwise square", [][2]float64{{0, 10}, {10, 10}, {10, 0}, {0, 0}}, 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pts, tris, ok := triangulate(tc.pts)
			if !ok {
				t.Fatalf("could not triangulate: %v", tris)
			}
			// n-2 triangles for a simple polygon of n points. Fewer means an ear
			// was skipped and part of the outline is missing.
			if want := len(tc.pts) - 2; len(tris) != want {
				t.Errorf("%d triangles, want %d", len(tris), want)
			}
			if got := triangulatedArea(pts, tris); math.Abs(got-tc.area) > 1e-9 {
				t.Errorf("triangles cover %v, outline encloses %v — a fan across a concave "+
					"corner produces triangles that lie outside the part", got, tc.area)
			}
		})
	}
}

// Winding is normalised, so a caller can build normals from the triangle order
// without asking which way round the author drew it. An inside-out solid is a
// defect this repository has already shipped once.
func TestTriangulate_NormalisesWindingToCounterClockwise(t *testing.T) {
	cw := [][2]float64{{0, 10}, {10, 10}, {10, 0}, {0, 0}}
	pts, tris, ok := triangulate(cw)
	if !ok {
		t.Fatal("clockwise outline was refused")
	}
	for i, tr := range tris {
		if c := cross(pts[tr[0]], pts[tr[1]], pts[tr[2]]); c <= 0 {
			t.Errorf("triangle %d is wound clockwise (cross %v); every face built from it "+
				"would point into the solid", i, c)
		}
	}
}

// A self-intersecting outline cannot be triangulated, and must END rather than
// loop. What it produced so far is returned: a partial outline drawn with a note
// beats a part that vanishes, and beats a browser tab that hangs.
func TestTriangulate_StopsOnAnOutlineThatCrossesItself(t *testing.T) {
	bowtie := [][2]float64{{0, 0}, {10, 10}, {10, 0}, {0, 10}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, _, ok := triangulate(bowtie); ok {
			t.Error("a bow-tie reported success; its triangles cannot cover it")
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("triangulate did not terminate on a self-intersecting outline")
	}
}

func TestTriangulate_RefusesTooFewPoints(t *testing.T) {
	for _, pts := range [][][2]float64{nil, {{0, 0}}, {{0, 0}, {1, 1}}} {
		if _, _, ok := triangulate(pts); ok {
			t.Errorf("%d points were accepted as an outline", len(pts))
		}
	}
}

// The tessellated extrusion is a closed solid of the right volume.
//
// Volume from the divergence theorem: summing (A · n)/3 over every triangle
// gives the enclosed volume for a closed surface, and gives nonsense for one
// with a hole in it. So this asserts the caps, the walls and the winding all at
// once — which is what makes it worth more than counting triangles.
func TestExtrusion_IsAClosedSolidOfTheRightVolume(t *testing.T) {
	for _, tc := range []struct {
		name string
		pts  [][2]float64
		area float64
	}{
		{"a square", [][2]float64{{0, 0}, {10, 0}, {10, 10}, {0, 10}}, 100},
		{"an L-bracket", [][2]float64{{0, 0}, {40, 0}, {40, 8}, {8, 8}, {8, 40}, {0, 40}}, 576},
		{"a clockwise L", [][2]float64{{0, 40}, {8, 40}, {8, 8}, {40, 8}, {40, 0}, {0, 0}}, 576},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile := make([]Point, len(tc.pts))
			for i, p := range tc.pts {
				profile[i] = Point{X: p[0], Y: p[1]}
			}
			part := Part{ID: "e", Shape: "extrusion", Profile: profile,
				Size: map[string]float64{"depth": 20}}
			tris := extrusion(part, 20, func(string, ...any) {})

			var vol float64
			for _, tr := range tris {
				// (A × B) · C / 6 summed over triangles of a closed surface.
				vol += (tr.A[0]*(tr.B[1]*tr.C[2]-tr.C[1]*tr.B[2]) -
					tr.A[1]*(tr.B[0]*tr.C[2]-tr.C[0]*tr.B[2]) +
					tr.A[2]*(tr.B[0]*tr.C[1]-tr.C[0]*tr.B[1])) / 6
			}
			if want := tc.area * 20; math.Abs(vol-want) > 1e-6 {
				t.Errorf("the tessellated solid encloses %v, want %v — a negative or halved "+
					"figure means the winding is inside out, and a wrong one means the "+
					"surface is not closed", vol, want)
			}
		})
	}
}

// A revolved outline is a closed solid of the right volume, about either axis.
//
// Volume by the divergence theorem again, which is what makes this worth more
// than counting facets: it is zero for a surface with a hole in it and negative
// for one turned inside out, so the caps, the winding and the sweep are all
// asserted at once.
//
// The figures come from Pappus, so the expectation is arithmetic somebody can
// check rather than a number this test observed once.
func TestRevolved_IsAClosedSolidOfTheRightVolume(t *testing.T) {
	for _, tc := range []struct {
		name string
		pts  [][2]float64
		axis string
		want float64
	}{
		// An annulus: a rectangle from r=10 to r=20, 5 tall.
		{"a ring about Y", [][2]float64{{10, 0}, {20, 0}, {20, 5}, {10, 5}}, "y",
			math.Pi * (400 - 100) * 5},
		{"a ring about X", [][2]float64{{0, 10}, {5, 10}, {5, 20}, {0, 20}}, "x",
			math.Pi * (400 - 100) * 5},
		// A cone, whose outline TOUCHES the axis — the common case for a dome or
		// a point, and the one where facets collapse to nothing.
		{"a cone about Y", [][2]float64{{0, 0}, {10, 0}, {0, 20}}, "y",
			math.Pi * 100 * 20 / 3},
		// Wound the other way: the winding must be normalised or the solid comes
		// out inside out and the volume negative.
		{"a clockwise ring", [][2]float64{{10, 5}, {20, 5}, {20, 0}, {10, 0}}, "y",
			math.Pi * (400 - 100) * 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile := make([]Point, len(tc.pts))
			for i, p := range tc.pts {
				profile[i] = Point{X: p[0], Y: p[1]}
			}
			part := Part{ID: "r", Shape: "revolve", Profile: profile, Axis: tc.axis}
			tris := revolved(part, func(string, ...any) {})

			var vol float64
			for _, tr := range tris {
				vol += (tr.A[0]*(tr.B[1]*tr.C[2]-tr.C[1]*tr.B[2]) -
					tr.A[1]*(tr.B[0]*tr.C[2]-tr.C[0]*tr.B[2]) +
					tr.A[2]*(tr.B[0]*tr.C[1]-tr.C[0]*tr.B[1])) / 6
			}
			// Tessellated, so it is inscribed in the true surface and slightly
			// under. At 40 segments the shortfall is about 0.4%.
			if vol < tc.want*0.99 || vol > tc.want {
				t.Errorf("the tessellated solid encloses %.3f, want just under %.3f — a "+
					"negative figure means the winding is inside out, and a wildly wrong "+
					"one means the surface is not closed", vol, tc.want)
			}
		})
	}
}

// The outline shapes are WIRED IN, reached through Tessellate the way the mesh
// exporters reach them.
//
// # Why this is separate from the two tests above
//
// Those call extrusion() and revolved() directly, so they prove the geometry is
// right and say nothing about whether partTriangles ever calls them. A drill
// removed the "revolve" case from the dispatch and both stayed green: the
// functions were still correct, and no document could reach them. An unknown
// shape falls back to a bounding box, so the failure is a part silently drawn
// and exported as a block.
func TestTessellate_ReachesTheOutlineShapes(t *testing.T) {
	square := []Point{{X: 0, Y: 0}, {X: 10, Y: 0}, {X: 10, Y: 10}, {X: 0, Y: 10}}
	ring := []Point{{X: 10, Y: 0}, {X: 20, Y: 0}, {X: 20, Y: 5}, {X: 10, Y: 5}}

	for _, tc := range []struct {
		name string
		part Part
		want float64
	}{
		{"an extrusion", Part{ID: "e", Shape: "extrusion", Profile: square,
			Size: map[string]float64{"depth": 20}}, 100 * 20},
		{"a revolve", Part{ID: "r", Shape: "revolve", Axis: "y", Profile: ring},
			math.Pi * (400 - 100) * 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mesh := Tessellate(Document{Name: "d", Units: "mm", Parts: []Part{tc.part}}, Millimetre)
			var vol float64
			for _, tr := range mesh.Triangles() {
				vol += (tr.A[0]*(tr.B[1]*tr.C[2]-tr.C[1]*tr.B[2]) -
					tr.A[1]*(tr.B[0]*tr.C[2]-tr.C[0]*tr.B[2]) +
					tr.A[2]*(tr.B[0]*tr.C[1]-tr.C[0]*tr.B[1])) / 6
			}
			// A bounding-box fallback would enclose a different volume entirely,
			// which is exactly what a missing dispatch case produces.
			if vol < tc.want*0.98 || vol > tc.want*1.001 {
				t.Errorf("Tessellate produced a solid of %.3f, want about %.3f — the shape "+
					"is not reaching its builder and is being drawn as a box", vol, tc.want)
			}
		})
	}
}
