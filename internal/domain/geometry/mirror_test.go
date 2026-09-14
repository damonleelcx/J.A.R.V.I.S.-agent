package geometry

import (
	"math"
	"strings"
	"testing"
)

// Mirror — a reflection, not a turn. Phase 1, stage D1c of
// docs/plan-2026-09-13-millions-of-parts.md.

// An L drawn entirely on the +x side of its own origin: a wrong reflection shows.
func lBracket() Part {
	return Part{ID: "arm", Name: "Arm", Shape: "extrusion",
		Size: map[string]float64{"depth": 10},
		Profile: []Point{{X: 0, Y: 0}, {X: 40, Y: 0}, {X: 40, Y: 10}, {X: 10, Y: 10},
			{X: 10, Y: 30}, {X: 0, Y: 30}},
		Position: []float64{5, 0, 0}, Rotation: []float64{0, 0, 20}}
}

func TestMirror_AChildMirroredAcrossYIsReflectedNotTurned(t *testing.T) {
	d := Document{Name: "car", Units: "mm", Definitions: []Part{lBracket()},
		Assemblies: []Assembly{{ID: "car", Children: []Child{
			{ID: "left", Ref: "arm", Position: []float64{-100, 0, 0}, Rotation: []float64{0, 30, 0}},
			{ID: "right", Ref: "arm", Position: []float64{100, 0, 0}, Rotation: []float64{0, 30, 0}, Mirror: "y"},
		}}}, Root: "car"}
	e, problems := expandAssemblies(d)
	if len(problems) != 0 || len(e.Parts) != 2 {
		t.Fatalf("parts=%d problems=%v", len(e.Parts), problems)
	}
	if e.Parts[0].Mirrored || !e.Parts[1].Mirrored {
		t.Fatalf("mirrored flags: left %v, right %v", e.Parts[0].Mirrored, e.Parts[1].Mirrored)
	}
	right := e.Parts[1]
	for _, v := range [][3]float64{{0, 0, 0}, {40, 30, 5}, {10, 10, -5}} {
		// Independently: the definition's placement, then a reflection of y, then the child's turn and move.
		inDef := placementOf([]float64{5, 0, 0}, []float64{0, 0, 20}, false).apply(v)
		reflected := [3]float64{inDef[0], -inDef[1], inDef[2]}
		want := placementOf([]float64{100, 0, 0}, []float64{0, 30, 0}, false).apply(reflected)
		got := placementOf(right.Position, right.Rotation, right.Mirrored).apply(v)
		for k := 0; k < 3; k++ {
			if math.Abs(got[k]-want[k]) > 1e-7 {
				t.Fatalf("point %v lands at %v, want %v", v, got, want)
			}
		}
	}
}

func TestMirror_AMirrorInsideAMirrorCancels(t *testing.T) {
	d := Document{Name: "d", Units: "mm", Definitions: []Part{lBracket()},
		Assemblies: []Assembly{
			{ID: "top", Children: []Child{{ID: "side", Ref: "side", Mirror: "x"}}},
			{ID: "side", Children: []Child{{ID: "arm", Ref: "arm", Mirror: "z"}}},
		}, Root: "top"}
	e, _ := expandAssemblies(d)
	if len(e.Parts) != 1 || e.Parts[0].Mirrored {
		t.Errorf("two mirrors did not cancel: %+v", e.Parts)
	}
}

func TestTree_RefusesAMirrorAcrossAnUnknownAxis(t *testing.T) {
	d := Document{Definitions: []Part{lBracket()},
		Assemblies: []Assembly{{ID: "a", Children: []Child{{ID: "c", Ref: "arm", Mirror: "diagonal"}}}}, Root: "a"}
	for _, p := range d.TreeProblems() {
		if strings.Contains(p.Detail, "mirrors across") {
			return
		}
	}
	t.Fatalf("a mirror across %q was not refused: %+v", "diagonal", d.TreeProblems())
}

func signedVolume(ts []Triangle) float64 {
	var v float64
	for _, t := range ts {
		v += t.A[0]*(t.B[1]*t.C[2]-t.B[2]*t.C[1]) - t.A[1]*(t.B[0]*t.C[2]-t.B[2]*t.C[0]) + t.A[2]*(t.B[0]*t.C[1]-t.B[1]*t.C[0])
	}
	return v / 6
}

// The mesh of a mirrored part is the reflection of the part, and its faces still point out.
func TestMirror_TheMeshOfAMirroredPartIsItsReflectionFacingOut(t *testing.T) {
	plain := lBracket()
	mirrored := lBracket()
	mirrored.Mirrored = true
	a := Tessellate(Document{Name: "a", Units: "mm", Parts: []Part{plain}}, Millimetre).Groups[0].Triangles
	b := Tessellate(Document{Name: "b", Units: "mm", Parts: []Part{mirrored}}, Millimetre).Groups[0].Triangles
	if len(a) == 0 || len(a) != len(b) {
		t.Fatalf("triangles: plain %d, mirrored %d", len(a), len(b))
	}
	if va, vb := signedVolume(a), signedVolume(b); va <= 0 || math.Abs(va-vb) > 1e-6*math.Abs(va) {
		t.Fatalf("signed volume: plain %v, mirrored %v — a mirrored mesh must enclose the same volume, facing out", va, vb)
	}
	// Every corner of the mirrored mesh is a corner of the plain part's local
	// shape, reflected, turned and moved.
	local := Tessellate(Document{Name: "l", Units: "mm", Parts: []Part{{ID: "arm", Shape: "extrusion",
		Size: plain.Size, Profile: plain.Profile, Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}}},
		Millimetre).Groups[0].Triangles
	place := placementOf(plain.Position, plain.Rotation, true)
	for i := range local {
		for k, corner := range [3][3]float64{local[i].A, local[i].B, local[i].C} {
			want := place.apply(corner)
			got := [3][3]float64{b[i].A, b[i].C, b[i].B}[k] // corners B and C are swapped
			for axis := 0; axis < 3; axis++ {
				if math.Abs(got[axis]-want[axis]) > 1e-9 {
					t.Fatalf("triangle %d corner %d: %v, want %v", i, k, got, want)
				}
			}
		}
	}
}

func TestMirror_MeasurementFlipsAnAsymmetricExtent(t *testing.T) {
	p := lBracket()
	p.Position = []float64{0, 0, 0}
	p.Rotation = []float64{0, 0, 0}
	p.Mirrored = true
	min, max := bounds(Document{Name: "m", Units: "mm", Parts: []Part{p}})
	if min[0] != -40 || max[0] != 0 {
		t.Errorf("a mirrored L drawn from x=0 to 40 measures x from %v to %v, want -40 to 0", min[0], max[0])
	}
}

func TestMirror_TheKernelIsToldToMirror(t *testing.T) {
	p := lBracket()
	p.Mirrored = true
	solids, _ := Solids(Document{Name: "k", Units: "mm", Parts: []Part{p}}, Millimetre)
	if len(solids) != 1 || !solids[0].Mirrored {
		t.Fatalf("the kernel is sent %+v", solids)
	}
}
