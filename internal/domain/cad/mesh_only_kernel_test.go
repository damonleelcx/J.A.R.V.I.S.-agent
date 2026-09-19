package cad_test

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// Mesh-only decorative parts (stage E1 of the "looks designed" work; damon's
// decision, 2026-09-18): a declared lattice is built as a mesh, never enters OCCT,
// and every exact reader leaves it out and says so. See geometry/lattice.go.

// gyroidInABox is a plate with a boss and a gyroid infill sitting over it.
func gyroidInABox(cell, wall float64) geometry.Document {
	doc := plate()
	doc.Parts = append(doc.Parts, geometry.Part{ID: "infill", Name: "Infill", Shape: "lattice", Lattice: "gyroid",
		Size:     map[string]float64{"width": 60, "height": 40, "depth": 30, "cell": cell, "thickness": wall},
		Position: []float64{0, 30, 0}, Rotation: []float64{0, 0, 0}})
	return doc
}

// A gyroid in a box builds as a mesh-only surface within the triangle budget, and
// the exact solids beside it are built exactly as they would be without it.
func TestKernel_AGyroidInABoxBuildsAsAMeshOnlyPartWithinBudget(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	doc := gyroidInABox(15, 2)
	built, err := k.BuildMesh(ctx, doc, geometry.Millimetre)
	if err != nil {
		t.Fatal(err)
	}
	var lattice *struct {
		tris     int
		meshOnly bool
	}
	for _, m := range built.Mesh {
		if m.ID == "infill" {
			lattice = &struct {
				tris     int
				meshOnly bool
			}{len(m.Triangles) / 3, m.MeshOnly}
		}
		if m.ID != "infill" && m.MeshOnly {
			t.Errorf("%s is marked mesh-only and is an exact solid", m.ID)
		}
	}
	if lattice == nil {
		t.Fatalf("the gyroid is not in the mesh reply; skipped %v", built.Skipped)
	}
	if !lattice.meshOnly {
		t.Error("the gyroid's surface is not marked mesh_only")
	}
	if lattice.tris == 0 || lattice.tris > geometry.MaxLatticeTriangles {
		t.Errorf("the gyroid is %d triangles; want 1..%d", lattice.tris, geometry.MaxLatticeTriangles)
	}
	if built.MeshOnlyTriangles != lattice.tris {
		t.Errorf("mesh_only_triangles %d, the part has %d", built.MeshOnlyTriangles, lattice.tris)
	}
	estimate, ok := geometry.LatticeTriangleEstimate(doc.Parts[2])
	if !ok || estimate < lattice.tris || estimate > 2*lattice.tris {
		t.Errorf("FORGE estimated %d triangles for a gyroid of %d: the estimate must err high, within 2x",
			estimate, lattice.tris)
	}
	if len(built.MeshOnly) != 1 || built.MeshOnly[0] != "Infill" {
		t.Errorf("the reply names %v as mesh-only; want [Infill]", built.MeshOnly)
	}
	if built.Parts != 2 {
		t.Errorf("the kernel built %d exact parts; the plate and the boss are 2", built.Parts)
	}

	// Nothing exact changed: the volume and the interference check are the plate's
	// alone, and the lattice (which sits over the boss) is never in the check.
	alone, err := k.BuildMesh(ctx, plate(), geometry.Millimetre)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(built.Volume-alone.Volume) > 1e-6*alone.Volume {
		t.Errorf("volume %v with the lattice, %v without: a mesh-only part has no volume in the solid",
			built.Volume, alone.Volume)
	}
	for _, f := range built.Interferences {
		if f.A == "infill" || f.B == "infill" || f.A == "Infill" || f.B == "Infill" {
			t.Errorf("the lattice was checked for interference: %+v", f)
		}
	}
	if built.InterferencePairs != alone.InterferencePairs {
		t.Errorf("%d pairs checked with the lattice, %d without", built.InterferencePairs, alone.InterferencePairs)
	}
}

// A STEP file leaves the lattice out and says so in its own FILE_DESCRIPTION; a
// file with no mesh-only part is unchanged.
func TestKernel_STEPLeavesOutAMeshOnlyPartAndSaysSoInTheFile(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	built, err := k.BuildDocument(ctx, gyroidInABox(15, 2), geometry.Millimetre, "step")
	if err != nil {
		t.Fatal(err)
	}
	header := string(built.STEP)
	if i := strings.Index(header, "ENDSEC;"); i > 0 {
		header = header[:i]
	}
	if !strings.Contains(header, "FILE_DESCRIPTION") ||
		!strings.Contains(header, "1 mesh-only part(s) are NOT in this file: Infill") {
		t.Errorf("the STEP header does not say the lattice is left out:\n%s", header)
	}
	if strings.Contains(string(built.STEP), "'Infill'") {
		t.Error("the STEP file holds a product named Infill: the lattice is in it")
	}
	if len(built.MeshOnly) != 1 {
		t.Errorf("the STEP build names %v as mesh-only", built.MeshOnly)
	}

	plain, err := k.BuildDocument(ctx, plate(), geometry.Millimetre, "step")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plain.STEP), "mesh-only") {
		t.Error("a file with no mesh-only part talks about one")
	}
}

// Mass properties never measure a lattice, and name it.
func TestKernel_PropertiesLeaveOutAMeshOnlyPart(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	doc := gyroidInABox(15, 2)
	built, err := k.BuildProperties(ctx, doc, geometry.Millimetre)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range built.Properties {
		if m.ID == "infill" {
			t.Errorf("the kernel measured the lattice: %+v", m)
		}
	}
	report := geometry.MassProperties(doc, built.Properties)
	if len(report.MeshOnly) != 1 || report.MeshOnly[0] != "Infill" {
		t.Errorf("the mass report names %v as mesh-only", report.MeshOnly)
	}
}

// Every pattern in Go's table builds in the kernel: the two lists are one list.
func TestKernel_EveryLatticePatternFORGETeachesBuilds(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	for _, pattern := range geometry.LatticePatternNames() {
		doc := geometry.Document{Name: pattern, Units: "mm", Parts: []geometry.Part{{
			ID: "l", Name: "L", Shape: "lattice", Lattice: pattern,
			Size:     map[string]float64{"width": 30, "height": 30, "depth": 30, "cell": 15, "thickness": 1.5},
			Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}}}
		built, err := k.BuildMesh(ctx, doc, geometry.Millimetre)
		if err != nil {
			t.Errorf("%s: %v", pattern, err)
			continue
		}
		if len(built.Mesh) != 1 || !built.Mesh[0].MeshOnly || len(built.Mesh[0].Triangles) == 0 {
			t.Errorf("%s: no mesh-only surface came back (skipped %v)", pattern, built.Skipped)
		}
	}
}

// The lattice is placed where its box is, and its wall comes out about as thick as
// asked: the pattern's gradient factor (sidecar.py, _LATTICE_PATTERNS) is what
// turns "thickness" into the level the sheet is cut at, measured in
// docs/spikes/2026-09-18-mesh-only-parts. Within 20%: the factor is exact near cell/10
// and the wall runs thicker as it approaches cell/7 (diamond +16% at cell/7.5).
func TestKernel_ALatticeSitsInItsBoxWithTheWallItAskedFor(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	for _, pattern := range geometry.LatticePatternNames() {
		doc := gyroidInABox(15, 2)
		doc.Parts = doc.Parts[2:]
		doc.Parts[0].Lattice = pattern
		built, err := k.BuildMesh(ctx, doc, geometry.Millimetre)
		if err != nil || len(built.Mesh) != 1 {
			t.Fatalf("%s: %v", pattern, err)
		}
		v, tris := built.Mesh[0].Vertices, built.Mesh[0].Triangles
		lo, hi := [3]float64{math.Inf(1), math.Inf(1), math.Inf(1)}, [3]float64{math.Inf(-1), math.Inf(-1), math.Inf(-1)}
		for i := 0; i+2 < len(v); i += 3 {
			for a := 0; a < 3; a++ {
				lo[a], hi[a] = math.Min(lo[a], v[i+a]), math.Max(hi[a], v[i+a])
			}
		}
		// The box is 60 x 40 x 30 centred on (0, 30, 0).
		want := [6]float64{-30, 10, -15, 30, 50, 15}
		for a := 0; a < 3; a++ {
			if math.Abs(lo[a]-want[a]) > 0.01 || math.Abs(hi[a]-want[a+3]) > 0.01 {
				t.Errorf("%s: the lattice spans %v..%v; its box is %v", pattern, lo, hi, want)
				break
			}
		}
		volume, area := 0.0, 0.0
		for i := 0; i+2 < len(tris); i += 3 {
			p := func(j int32) [3]float64 { return [3]float64{v[3*j], v[3*j+1], v[3*j+2]} }
			a, b, c := p(tris[i]), p(tris[i+1]), p(tris[i+2])
			volume += (a[0]*(b[1]*c[2]-b[2]*c[1]) - a[1]*(b[0]*c[2]-b[2]*c[0]) + a[2]*(b[0]*c[1]-b[1]*c[0])) / 6
			u := [3]float64{b[0] - a[0], b[1] - a[1], b[2] - a[2]}
			w := [3]float64{c[0] - a[0], c[1] - a[1], c[2] - a[2]}
			x, y, z := u[1]*w[2]-u[2]*w[1], u[2]*w[0]-u[0]*w[2], u[0]*w[1]-u[1]*w[0]
			area += math.Sqrt(x*x+y*y+z*z) / 2
		}
		boxVolume, boxArea := 60.0*40*30, 2*(60.0*40+40*30+60*30)
		wall := 2 * volume / (area - volume/boxVolume*boxArea)
		t.Logf("%s: wall %.3f for 2 asked", pattern, wall)
		if volume <= 0 || math.Abs(wall-2) > 0.4 {
			t.Errorf("%s: the wall is %.3f thick (volume %.0f, area %.0f); 2 was asked", pattern, wall, volume, area)
		}
	}
}

// A mirrored lattice is its reflection in its own x, facing out: a gyroid is
// chiral, so the mirrored one is a different surface, and every reader of a
// mirrored part reflects it (Part.Mirrored).
func TestKernel_AMirroredLatticeIsItsReflection(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	surface := func(mirrored bool) ([]float64, []int32) {
		doc := gyroidInABox(15, 2)
		doc.Parts = doc.Parts[2:]
		doc.Parts[0].Position = []float64{0, 0, 0}
		doc.Parts[0].Mirrored = mirrored
		built, err := k.BuildMesh(ctx, doc, geometry.Millimetre)
		if err != nil || len(built.Mesh) != 1 {
			t.Fatalf("mirrored %v: %v", mirrored, err)
		}
		return built.Mesh[0].Vertices, built.Mesh[0].Triangles
	}
	plainV, plainT := surface(false)
	mirrorV, mirrorT := surface(true)
	key := func(x, y, z float64) [3]int64 {
		return [3]int64{int64(math.Round(x * 1e4)), int64(math.Round(y * 1e4)), int64(math.Round(z * 1e4))}
	}
	plain := map[[3]int64]bool{}
	for i := 0; i+2 < len(plainV); i += 3 {
		plain[key(plainV[i], plainV[i+1], plainV[i+2])] = true
	}
	moved := 0
	for i := 0; i+2 < len(mirrorV); i += 3 {
		if !plain[key(-mirrorV[i], mirrorV[i+1], mirrorV[i+2])] {
			moved++
		}
	}
	if len(mirrorV) != len(plainV) || moved > 0 {
		t.Errorf("%d of %d mirrored vertices are not the original's reflected in x", moved, len(mirrorV)/3)
	}
	same := 0
	for i := 0; i+2 < len(mirrorV); i += 3 {
		if plain[key(mirrorV[i], mirrorV[i+1], mirrorV[i+2])] {
			same++
		}
	}
	if same == len(mirrorV)/3 {
		t.Error("the mirrored lattice is the unmirrored one: it was not reflected")
	}
	signed := func(v []float64, tris []int32) float64 {
		total := 0.0
		for i := 0; i+2 < len(tris); i += 3 {
			a, b, c := 3*tris[i], 3*tris[i+1], 3*tris[i+2]
			total += (v[a]*(v[b+1]*v[c+2]-v[b+2]*v[c+1]) - v[a+1]*(v[b]*v[c+2]-v[b+2]*v[c]) +
				v[a+2]*(v[b]*v[c+1]-v[b+1]*v[c])) / 6
		}
		return total
	}
	if p, m := signed(plainV, plainT), signed(mirrorV, mirrorT); p <= 0 || math.Abs(m-p) > 1e-6*p {
		t.Errorf("enclosed volume %v unmirrored, %v mirrored: a reflection must keep its faces facing out", p, m)
	}
}

// A design of nothing but mesh-only parts draws, and refuses a STEP file by name.
func TestKernel_OnlyMeshOnlyPartsDrawButHaveNoSTEP(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	doc := gyroidInABox(15, 2)
	doc.Parts = doc.Parts[2:]
	if built, err := k.BuildMesh(ctx, doc, geometry.Millimetre); err != nil || len(built.Mesh) != 1 {
		t.Fatalf("a lone lattice did not draw: %v", err)
	}
	_, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "step")
	if err == nil || !strings.Contains(err.Error(), "every part is mesh-only") {
		t.Errorf("a STEP of a lone lattice: %v", err)
	}
}
