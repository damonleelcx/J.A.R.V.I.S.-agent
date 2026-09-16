package cad

import (
	"math"
	"testing"
)

// A placed copy is its definition's triangles moved by a COLUMN-major matrix.
// Phase 4, stage K4. Pure arithmetic, so no kernel: the kernel's side is held by
// TestKernel_AnInstancedMeshIsTheMeshEveryPartUsedToGet.
func TestWorldMeshesPlacesEachCopyByItsMatrix(t *testing.T) {
	// A quarter turn about z, then (10, 20, 30). Column-major: the first column is
	// where x goes (onto +y), the second where y goes (onto -x), the last the move.
	quarter := [16]float64{0, 1, 0, 0, -1, 0, 0, 0, 0, 0, 1, 0, 10, 20, 30, 1}
	b := &Build{
		Mesh: []MeshPart{{ID: "cut-plate", Vertices: []float64{0, 0, 0}, Triangles: []int32{0, 0, 0}}},
		MeshDefinitions: []MeshDefinition{{
			Vertices:  []float64{1, 0, 0, 0, 1, 0, 0, 0, 1},
			Triangles: []int32{0, 1, 2},
		}},
		MeshInstances: []MeshInstance{
			{ID: "bolt-1", Label: "Bolt", Definition: 0, Matrix: quarter},
			{ID: "stray", Definition: 3, Matrix: quarter},
		},
	}
	got := b.WorldMeshes()
	if len(got) != 2 || got[0].ID != "cut-plate" || got[1].ID != "bolt-1" {
		t.Fatalf("world meshes %+v; want the changed part, then bolt-1 (an instance of no definition is dropped)", got)
	}
	want := []float64{10, 21, 30, 9, 20, 30, 10, 20, 31}
	for i, w := range want {
		if math.Abs(got[1].Vertices[i]-w) > 1e-12 {
			t.Fatalf("bolt-1 vertices %v, want %v", got[1].Vertices, want)
		}
	}
	if got[1].Label != "Bolt" || len(got[1].Triangles) != 3 {
		t.Errorf("bolt-1 lost its label or triangles: %+v", got[1])
	}
	// The definition itself is untouched: every copy is moved from the same source.
	if b.MeshDefinitions[0].Vertices[0] != 1 {
		t.Errorf("expanding a copy moved the definition's own vertices: %v", b.MeshDefinitions[0].Vertices)
	}
}
