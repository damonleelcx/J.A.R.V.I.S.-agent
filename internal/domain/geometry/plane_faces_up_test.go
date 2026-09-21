package geometry

import (
	"math"
	"testing"
)

// A plane is ONE-SIDED and FACES UP, in all three builders of one.
//
// The convention and the reasons for it are on `func plane` in mesh.go, which is
// where it lives. This is its fence in the exporter; the other two builders have
// theirs — TestTheRendererDrawsAPlaneFacingUp (internal/httpapi, over the shipped
// forge3d.js in node) and TestKernel_APlaneFacesUp (internal/domain/cad, over the
// real build123d kernel). All three are written against the same sentence: the
// declared normal is +Y and every triangle's (B-A)x(C-A) points the same way.
//
// # Why a plane needs its own fence at all
//
// Every other winding fence in this package is a VOLUME. TestExport_EverySolidIsWoundOutward
// computes the signed volume of each group and requires it positive, and it skips
// `plane` by name — "Not a solid: two triangles enclosing nothing. It has no inside
// to be turned out." A sheet encloses zero either way up, so the one fence that
// would have caught this could not be pointed at it. Nor could
// TestExport_EveryPrimitiveIsWoundOutwardInTheFile, which asks every facet to face
// AWAY FROM ITS PART'S CENTRE: a plane's centre is ON it. Neither test was wrong;
// a zero-thickness face simply has no interior to appeal to, so the side it faces
// has to be named rather than derived. That is what this does.
//
// # What this one does NOT prove
//
// mesh.go was already correct AT ITS OUTPUT before the 2026-09-21 fix and this
// test is green on both sides of it. Every facet leaves through
// appendNonDegenerate, which calls orient(), and orient swaps two corners of any
// facet wound against its stated normal — so the corner order written in `plane`
// was repaired on the way out and the exported STL always faced up. (That is the
// 2026-09-19 correction on
// docs/bugfix/2026-09-18-cylinders-cones-and-spheres-were-drawn-inside-out.md:
// orient() is the one place winding is decided here.) The corner order in `plane`
// was brought into agreement anyway so that nothing relies on that repair, but the
// literal order is not observable through this function and is not what this holds.
// What this holds is the DECLARED normal, which is what orient() obeys and
// therefore the real convention in this package: drill "the exporter's plane faces
// down" turns it over and takes the winding with it.
func TestAPlaneFacesUp(t *testing.T) {
	up := [3]float64{0, 1, 0}

	check := func(what string, tris []Triangle) {
		t.Helper()
		if len(tris) != 2 {
			t.Fatalf("%s: %d triangle(s), want the 2 a rectangle is cut into", what, len(tris))
		}
		for i, tr := range tris {
			if tr.Normal != up {
				t.Errorf("%s: triangle %d declares normal %v, want %v. A plane is one-sided "+
					"and faces UP (mesh.go, func plane): the kernel and forge3d.js both say +Y, "+
					"and the viewport culls a plane that faces the other way out of the very "+
					"view it is drawn for.", what, i, tr.Normal, up)
			}
			ab := [3]float64{tr.B[0] - tr.A[0], tr.B[1] - tr.A[1], tr.B[2] - tr.A[2]}
			ac := [3]float64{tr.C[0] - tr.A[0], tr.C[1] - tr.A[1], tr.C[2] - tr.A[2]}
			cross := [3]float64{
				ab[1]*ac[2] - ab[2]*ac[1],
				ab[2]*ac[0] - ab[0]*ac[2],
				ab[0]*ac[1] - ab[1]*ac[0],
			}
			if cross[1] <= 0 || math.Abs(cross[0]) > 1e-9 || math.Abs(cross[2]) > 1e-9 {
				t.Errorf("%s: triangle %d is wound %v, which does not point +Y. Its own corner "+
					"order, not the normal beside it: a consumer that decides which side is "+
					"outside from the winding (a slicer, a mesh repair) reads this and not the "+
					"normal.", what, i, cross)
			}
		}
		// The two triangles must cover the rectangle and not the same half twice —
		// a fence that only reads facings would be satisfied by a degenerate pair.
		var area float64
		for _, tr := range tris {
			ab := [3]float64{tr.B[0] - tr.A[0], tr.B[1] - tr.A[1], tr.B[2] - tr.A[2]}
			ac := [3]float64{tr.C[0] - tr.A[0], tr.C[1] - tr.A[1], tr.C[2] - tr.A[2]}
			area += math.Abs(ab[2]*ac[0]-ab[0]*ac[2]) / 2
		}
		if math.Abs(area-40) > 1e-9 {
			t.Errorf("%s: the two triangles cover %g mm², want 40 for a 10 x 4 plane", what, area)
		}
	}

	check("plane(10, 4)", plane(10, 4))

	// Through the dispatch as well, so the fence covers the shape a document names
	// and not only the builder underneath it. Unrotated: a turned plane faces
	// wherever it was turned to, and it is the part's OWN frame that has a side.
	doc := Document{Name: "sheet", Units: "mm", Parts: []Part{{
		ID: "sheet", Name: "Sheet", Shape: "plane",
		Size:     map[string]float64{"width": 10, "depth": 4},
		Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0},
	}}}
	mesh := Tessellate(doc, Millimetre)
	found := false
	for _, g := range mesh.Groups {
		if g.Shape != "plane" {
			continue
		}
		found = true
		check("Tessellate", g.Triangles)
	}
	if !found {
		t.Fatal("Tessellate produced no group for the plane part; this fence tested nothing")
	}
}
