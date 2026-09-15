package cad_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// A mesh is tessellated once per definition, and every placed copy is its matrix.
// Phase 4, stage K4 of docs/plan-2026-09-13-millions-of-parts.md: "triangles
// counted once per definition".

// The acceptance at a size the Go path can build: 512 copies of one stud are one
// definition, 512 instances, no per-part meshes, and a triangle count of ONE stud.
func TestKernel_AMeshIsTessellatedOncePerDefinition(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	got, err := k.BuildMesh(ctx, studGrid(64, "x"), geometry.Millimetre)
	if err != nil {
		t.Fatal(err)
	}
	if got.MeshError != "" {
		t.Fatalf("mesh error: %s", got.MeshError)
	}
	if len(got.MeshDefinitions) != 1 || len(got.MeshInstances) != 512 || len(got.Mesh) != 0 {
		t.Fatalf("512 copies of one stud: %d definition(s), %d instance(s), %d separate mesh(es); want 1, 512, 0",
			len(got.MeshDefinitions), len(got.MeshInstances), len(got.Mesh))
	}
	if one := len(got.MeshDefinitions[0].Triangles) / 3; got.Triangles != one {
		t.Errorf("reported %d triangles; one stud is %d, and a definition is counted once", got.Triangles, one)
	}
	// Placed where the build put them: the expanded copies span the build's bounds.
	lo := [3]float64{math.Inf(1), math.Inf(1), math.Inf(1)}
	hi := [3]float64{math.Inf(-1), math.Inf(-1), math.Inf(-1)}
	for _, m := range got.WorldMeshes() {
		for i := 0; i+2 < len(m.Vertices); i += 3 {
			for a := 0; a < 3; a++ {
				lo[a] = math.Min(lo[a], m.Vertices[i+a])
				hi[a] = math.Max(hi[a], m.Vertices[i+a])
			}
		}
	}
	for a := 0; a < 3; a++ {
		if math.Abs(lo[a]-got.Bounds[a]) > 1e-6 || math.Abs(hi[a]-got.Bounds[a+3]) > 1e-6 {
			t.Fatalf("expanded copies span %v..%v; the build's bounds are %v", lo, hi, got.Bounds)
		}
	}
}

// meshComparison is what testdata/mesh_per_definition.py reports.
type meshComparison struct {
	Parts       int      `json:"parts"`
	Instanced   int      `json:"instanced"`
	Separate    int      `json:"separate"`
	Definitions int      `json:"definitions"`
	Compared    int      `json:"compared"`
	Mismatches  []string `json:"mismatches"`
}

// The instanced mesh is the mesh every part used to get: the same nodes, the same
// number of triangles, and the same closed surface.
//
// The fixture is built through the sidecar twice: as shipped, and with
// _MESH_PER_DEFINITION off, which is the per-solid tessellation K4 replaced. Each
// instance is moved by its matrix and compared with that part's own old mesh.
// Turned copies, an L extruded and mirrored (a mirrored box is itself, so it could
// not show a lost reflection), and a plate cut by a drill — a changed part that
// must NOT be drawn as its definition — are all in it.
//
// # Why the triangle lists are not compared index for index
//
// The first version did, and failed on the turned pins and the mirrored parts with
// identical node arrays (to 1e-14 mm), identical triangle counts, and identical
// area and signed volume. OCCT triangulates a shape placed and unplaced to the
// same nodes but may join them by different diagonals where two are equally good.
// That is the same surface, so area and SIGNED volume are compared instead: the
// sign is what a copy wound inside out would lose.
func TestKernel_AnInstancedMeshIsTheMeshEveryPartUsedToGet(t *testing.T) {
	python := os.Getenv("FORGE_CAD_PYTHON")
	if python == "" {
		t.Skip("FORGE_CAD_PYTHON is unset; skipping the CAD kernel tests")
	}
	out, err := exec.Command(python, filepath.Join("testdata", "mesh_per_definition.py"), "sidecar.py").Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			t.Fatalf("comparing meshes: %v\n%s", err, exit.Stderr)
		}
		t.Fatalf("comparing meshes: %v", err)
	}
	var got meshComparison
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("reading the comparison: %v\n%s", err, out)
	}
	t.Logf("%d parts: %d instanced from %d definition(s), %d meshed on their own, %d compared",
		got.Parts, got.Instanced, got.Definitions, got.Separate, got.Compared)
	for _, m := range got.Mismatches {
		t.Error(m)
	}
	if got.Compared != got.Parts {
		t.Errorf("compared %d of %d parts", got.Compared, got.Parts)
	}
	// Something shared and something not, or the comparison proves nothing.
	if got.Instanced < 6 || got.Definitions >= got.Instanced || got.Separate < 1 {
		t.Errorf("the fixture exercised %d instance(s) of %d definition(s) and %d separate mesh(es); "+
			"it needs shared definitions and a changed part", got.Instanced, got.Definitions, got.Separate)
	}
}
