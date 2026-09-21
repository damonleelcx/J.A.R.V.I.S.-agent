package cad_test

import (
	"math"
	"testing"
)

// Which way the kernel builds a plane.
// testdata/plane_up.py; docs/bugfix/2026-09-21-a-plane-faced-down-and-was-culled-from-above.md.

type planeDefinition struct {
	UsedBy   []string     `json:"used_by"`
	Normals  [][3]float64 `json:"normals"`
	Windings [][3]float64 `json:"windings"`
}

type planeCopy struct {
	ID         string     `json:"id"`
	Up         [3]float64 `json:"up"`
	FaceNormal [3]float64 `json:"face_normal"`
}

type planeFixture struct {
	Error       string            `json:"error"`
	Definitions []planeDefinition `json:"definitions"`
	Bounds      []float64         `json:"bounds"`
	Parts       []planeCopy       `json:"parts"`
}

// A plane is one-sided and FACES UP, and the kernel agrees with the two renderers
// about which way that is.
//
// The convention and the reasons for it live on `func plane` in
// internal/domain/geometry/mesh.go. The other two fences over the same sentence
// are TestAPlaneFacesUp (geometry, the exporter) and
// TestTheRendererDrawsAPlaneFacingUp (internal/httpapi, the shipped forge3d.js in
// node).
//
// # What was wrong
//
// _shape built a plane as `Plane.XZ * Rectangle(width, depth)`. Plane.XZ's normal
// is -Y — the same fact that built every truncated cone end-for-end one day
// earlier (PR 167) — so the kernel's plane faced DOWN: declared -Y and wound -Y,
// perfectly coherent with itself and against both renderers, the exported STL and
// the OBJ, all of which say +Y. On the stage it was worse than a disagreement:
// forge3d.js shades a kernel mesh with the kernel's own normals and culls back
// faces, so the same plane was lit from underneath AND thrown away whenever the
// camera was above it.
//
// # Why nothing caught it
//
// Every winding fence in this system is a VOLUME or a distance from a centre —
// TestExport_EverySolidIsWoundOutward, TestExport_EveryPrimitiveIsWoundOutwardInTheFile,
// TestRendererPrimitivesFaceOutward — and a plane encloses no volume and has its
// own centre ON it. The first of those excludes `plane` by name for exactly that
// reason; the other two simply never list it. A zero-thickness face has no
// interior to appeal to, so the side it faces cannot be derived and has to be
// named. TestKernel_AMeshCarriesSmoothNormalsAndKeepsHardEdges asks a cylinder's
// normals to be radial or along the axis, which a flat sheet is not, and does not
// build one.
//
// # Why four copies, and why the bounds
//
// Read in the DEFINITION's own frame, which all the unturned copies share, so a
// kernel that faced the right way only before anything was turned is caught by
// the per-copy B-rep face normal instead — measured in world coordinates against
// that copy's own up. The mirrored copy is the one that would catch a reflection
// re-wound without its normals carried round with it.
//
// The bounds are here because the frame that turns +Z to +Y is not free to spin
// the local x with it the way a cylinder's is. `Plane.ZX * Rectangle(width, depth)`
// — the cone's fix — would face the right way and lay WIDTH along Z, which is a
// different rectangle. A plane is not round about its axis, so the correction has
// to leave x alone.
func TestKernel_APlaneFacesUp(t *testing.T) {
	var got planeFixture
	testdataJSON(t, "plane_up.py", &got)
	if got.Error != "" {
		t.Fatal(got.Error)
	}
	up := [3]float64{0, 1, 0}

	if len(got.Definitions) == 0 {
		t.Fatal("the kernel meshed no plane; this fence tested nothing")
	}
	for _, d := range got.Definitions {
		if len(d.Normals) == 0 || len(d.Windings) != 2 {
			t.Fatalf("the definition used by %v has %d normal(s) and %d triangle(s); "+
				"want a normal per vertex and the 2 a rectangle is cut into",
				d.UsedBy, len(d.Normals), len(d.Windings))
		}
		for i, n := range d.Normals {
			if !sameDirection(n, up) {
				t.Errorf("the definition used by %v: vertex %d has normal %v, want %v. A plane "+
					"is one-sided and faces UP (internal/domain/geometry/mesh.go, func plane); "+
					"Plane.XZ's normal is -Y and builds it face-down.", d.UsedBy, i, n, up)
			}
		}
		for i, w := range d.Windings {
			if !sameDirection(w, up) {
				t.Errorf("the definition used by %v: triangle %d is wound %v, want %v. The "+
					"corner order, not the normal beside it: forge3d.js culls back faces, so a "+
					"plane wound the other way is not drawn at all from above.", d.UsedBy, i, w, up)
			}
		}
	}

	for _, p := range got.Parts {
		t.Logf("%s: up %v, face normal %v", p.ID, p.Up, p.FaceNormal)
		if !sameDirection(p.FaceNormal, p.Up) {
			t.Errorf("%s: the solid's face points %v; its own up is %v. This is the B-rep face, "+
				"which is what STEP carries and what a thicken grows from, so a plane that faces "+
				"the wrong way here is wrong in the downloaded file as well as on the stage.",
				p.ID, p.FaceNormal, p.Up)
		}
	}

	// width is X and depth is Z, or the plane faces the right way and is the wrong
	// rectangle.
	if len(got.Bounds) != 6 {
		t.Fatalf("the upright plane reported %d bound(s), want 6", len(got.Bounds))
	}
	for i, want := range []float64{-5, 0, -2, 5, 0, 2} {
		if math.Abs(got.Bounds[i]-want) > 1e-9 {
			t.Fatalf("the upright 10 x 4 plane spans %v, want [-5 0 -2 5 0 2]: width along X, "+
				"depth along Z, nothing along Y. A frame that turned +Z to +Y by spinning the "+
				"local x with it (Plane.ZX, which is right for a cone) swaps them.", got.Bounds)
		}
	}
}

// sameDirection is true when two unit vectors point the same way, to the slack a
// quarter turn's cos(90°) leaves behind (1.2e-16).
func sameDirection(a, b [3]float64) bool {
	for i := 0; i < 3; i++ {
		if math.Abs(a[i]-b[i]) > 1e-9 {
			return false
		}
	}
	return true
}
