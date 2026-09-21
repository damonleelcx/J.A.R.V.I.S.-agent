package geometry

import (
	"strconv"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
)

// Looks integration, follow-up 4 (2026-09-19): does the Go exporter wind its curved
// primitives inside out, as forge3d.js did before PR 157?
//
// PR 157's bugfix doc (docs/bugfix/2026-09-18-cylinders-cones-and-spheres-were-drawn-
// inside-out.md) says, by reading, that mesh.go's cylinder() lists its side as
// (topA, botA, botB), which is clockwise seen from outside. It does — but every facet
// goes through appendNonDegenerate, which calls orient(), and orient swaps two corners
// whenever the winding disagrees with the facet's stated (outward) normal. So the file
// is wound outward. This fence reads that off the FILES a person downloads, not off
// cylinder(): every facet of every convex primitive, in STL and in OBJ, is wound so
// that (B-A)x(C-A) points away from the part's own centre and agrees with the normal
// the file states — turned, and mirrored, as well as plain.
// docs/bugfix/2026-09-18-cylinders-cones-and-spheres-were-drawn-inside-out.md, "Checked 2026-09-19".
func TestExport_EveryPrimitiveIsWoundOutwardInTheFile(t *testing.T) {
	parts := []Part{
		{ID: "wheel", Name: "wheel", Shape: "cylinder", Size: map[string]float64{"radius": 30, "height": 12},
			Position: []float64{0, 0, 0}, Rotation: []float64{90, 0, 0}},
		{ID: "taper", Name: "taper", Shape: "cylinder", Size: map[string]float64{"radius": 20, "radius_top": 8, "height": 25},
			Position: []float64{200, 0, 0}, Rotation: []float64{0, 0, 30}},
		{ID: "nose", Name: "nose", Shape: "cone", Size: map[string]float64{"radius": 15, "height": 40},
			Position: []float64{400, 0, 0}},
		{ID: "ball", Name: "ball", Shape: "sphere", Size: map[string]float64{"radius": 18},
			Position: []float64{600, 0, 0}},
		{ID: "block", Name: "block", Shape: "box", Size: map[string]float64{"width": 10, "height": 20, "depth": 30},
			Position: []float64{800, 0, 0}, Rotation: []float64{10, 20, 30}},
		{ID: "hub", Name: "hub", Shape: "cylinder", Size: map[string]float64{"radius": 25, "height": 10},
			Position: []float64{1000, 0, 0}, Mirrored: true},
	}
	v := &Variant{VersionID: "ver_w", ProjectID: "prj_w", Path: "geometry/winding.forge.json", Version: 1,
		Name: "winding", Units: Millimetre, Frame: FrameAssembly, Generator: "test",
		Verification: workspace.Unverified, Disposition: workspace.Pending,
		Document: Document{Name: "winding", Units: "mm", Parts: parts}}
	// Each part's centre in the file (a mirrored part is reflected in place), for "away
	// from the centre": the parts stand 200 mm apart and none is wider than 80, so the
	// nearest centre is the owner's.
	var centres [][3]float64
	for _, p := range parts {
		c := [3]float64{p.Position[0], p.Position[1], p.Position[2]}
		centres = append(centres, c)
	}
	owner := func(m [3]float64) [3]float64 {
		best, bd := centres[0], -1.0
		for _, c := range centres {
			d := (m[0]-c[0])*(m[0]-c[0]) + (m[1]-c[1])*(m[1]-c[1]) + (m[2]-c[2])*(m[2]-c[2])
			if bd < 0 || d < bd {
				best, bd = c, d
			}
		}
		return best
	}
	check := func(format string, facets []facet) {
		if len(facets) < 100 {
			t.Fatalf("%s: only %d facets read from the file", format, len(facets))
		}
		inward, disagree := 0, 0
		for _, f := range facets {
			ab := sub3(f.v[1], f.v[0])
			ac := sub3(f.v[2], f.v[0])
			n := [3]float64{ab[1]*ac[2] - ab[2]*ac[1], ab[2]*ac[0] - ab[0]*ac[2], ab[0]*ac[1] - ab[1]*ac[0]}
			m := [3]float64{(f.v[0][0] + f.v[1][0] + f.v[2][0]) / 3, (f.v[0][1] + f.v[1][1] + f.v[2][1]) / 3,
				(f.v[0][2] + f.v[1][2] + f.v[2][2]) / 3}
			if dot3(n, sub3(m, owner(m))) <= 0 {
				inward++
			}
			if dot3(n, f.n) <= 0 {
				disagree++
			}
		}
		if inward != 0 {
			t.Errorf("%s: %d of %d facets are wound toward their part's centre — a viewer that culls "+
				"back faces draws the inside of the far wall, as forge3d.js did before PR 157", format, inward, len(facets))
		}
		if disagree != 0 {
			t.Errorf("%s: %d of %d facets are wound against the normal the file states", format, disagree, len(facets))
		}
	}

	stl, err := Export(v, "stl")
	if err != nil {
		t.Fatal(err)
	}
	check("STL", readSTLFacets(t, string(stl.Content)))
	obj, err := Export(v, "obj")
	if err != nil {
		t.Fatal(err)
	}
	check("OBJ", readOBJFacets(t, string(obj.Content)))
}

type facet struct {
	v [3][3]float64
	n [3]float64
}

func floats3(t *testing.T, fields []string) [3]float64 {
	t.Helper()
	var out [3]float64
	for i := 0; i < 3; i++ {
		f, err := strconv.ParseFloat(fields[i], 64)
		if err != nil {
			t.Fatalf("not a number in the file: %q", fields[i])
		}
		out[i] = f
	}
	return out
}

func readSTLFacets(t *testing.T, s string) []facet {
	var out []facet
	var cur facet
	k := 0
	for _, line := range strings.Split(s, "\n") {
		f := strings.Fields(line)
		switch {
		case len(f) == 5 && f[0] == "facet" && f[1] == "normal":
			cur, k = facet{n: floats3(t, f[2:])}, 0
		case len(f) == 4 && f[0] == "vertex":
			if k < 3 {
				cur.v[k] = floats3(t, f[1:])
			}
			k++
		case len(f) == 1 && f[0] == "endfacet":
			out = append(out, cur)
		}
	}
	return out
}

func readOBJFacets(t *testing.T, s string) []facet {
	var verts, norms [][3]float64
	var out []facet
	for _, line := range strings.Split(s, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		switch f[0] {
		case "v":
			verts = append(verts, floats3(t, f[1:]))
		case "vn":
			norms = append(norms, floats3(t, f[1:]))
		case "f":
			if len(f) != 4 {
				t.Fatalf("a face that is not a triangle: %q", line)
			}
			var fc facet
			for i := 0; i < 3; i++ {
				ix := strings.Split(f[i+1], "/")
				vi, err1 := strconv.Atoi(ix[0])
				ni, err2 := strconv.Atoi(ix[len(ix)-1])
				if err1 != nil || err2 != nil || vi < 1 || vi > len(verts) || ni < 1 || ni > len(norms) {
					t.Fatalf("a face the file cannot resolve: %q", line)
				}
				fc.v[i], fc.n = verts[vi-1], norms[ni-1]
			}
			out = append(out, fc)
		}
	}
	return out
}
