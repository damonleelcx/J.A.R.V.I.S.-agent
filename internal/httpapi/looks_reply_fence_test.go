package httpapi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/agent"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/cad"
)

// Looks designed, stages A5 and B1, on the wire.

// A5: the mesh payload carries each vertex's normal, additively. A definition's
// normals are in its own frame and a part's in the assembly's, beside "vertices";
// a reader that does not know the field sees exactly the payload it saw before.
func TestMeshPayload_CarriesNormalsAdditively(t *testing.T) {
	built := &cad.Build{
		Mesh: []cad.MeshPart{{ID: "plate", Label: "Plate", Vertices: []float64{0, 0, 0, 1, 0, 0, 0, 0, 1},
			Triangles: []int32{0, 1, 2}, Normals: []float64{0, 1, 0, 0, 1, 0, 0, 1, 0}}},
		MeshDefinitions: []cad.MeshDefinition{{Vertices: []float64{0, 0, 0, 1, 0, 0, 0, 1, 0},
			Triangles: []int32{0, 1, 2}, Normals: []float64{0, 0, 1, 0, 0, 1, 0, 0, 1}}},
		MeshInstances: []cad.MeshInstance{{ID: "bolt-1", Definition: 0, Matrix: [16]float64{1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1}}},
	}
	parts, definitions, _ := meshPayload(built)
	body, err := json.Marshal(map[string]any{"parts": parts, "definitions": definitions})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Parts []struct {
			Vertices []float64 `json:"vertices"`
			Normals  []float64 `json:"normals"`
		} `json:"parts"`
		Definitions []struct {
			Vertices []float64 `json:"vertices"`
			Normals  []float64 `json:"normals"`
		} `json:"definitions"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Parts) != 1 || len(got.Parts[0].Normals) != 9 || got.Parts[0].Normals[1] != 1 {
		t.Errorf("a part's normals did not reach the wire: %s", body)
	}
	if len(got.Definitions) != 1 || len(got.Definitions[0].Normals) != 9 || got.Definitions[0].Normals[2] != 1 {
		t.Errorf("a definition's normals did not reach the wire: %s", body)
	}
	// And absent stays absent: a build without normals writes no "normals" key.
	built.Mesh[0].Normals, built.MeshDefinitions[0].Normals = nil, nil
	parts, definitions, _ = meshPayload(built)
	body, _ = json.Marshal(map[string]any{"parts": parts, "definitions": definitions})
	if strings.Contains(string(body), "normals") {
		t.Errorf("a mesh without normals writes the key anyway: %s", body)
	}
}

// B1: a round the kernel built smaller than asked is said in the STEP download's
// label, in-request and off-node alike, beside the features it could not apply.
func TestExportLabel_SaysWhichRoundsWereBuiltSmaller(t *testing.T) {
	reduced := "corners: the fillet of 45 mm did not build on all 4 edge group(s) — 1 edge near (30, 0, 30) at 22.5 mm"
	label := exportJobLabel(&agent.Export{VersionID: "v1", FeatureReductions: []string{reduced}})
	for _, want := range []string{"1 fillet(s) or chamfer(s) were built SMALLER", "22.5 mm"} {
		if !strings.Contains(label, want) {
			t.Errorf("the export job's label does not say %q: %s", want, label)
		}
	}
	if got := reducedLabel(nil); got != "" {
		t.Errorf("a file with every round as written carries a reduction clause: %q", got)
	}
	dto := toExportDTO(&agent.Export{ID: "e1", FeatureReductions: []string{reduced}})
	if len(dto.FeatureReductions) != 1 {
		t.Errorf("the export status does not carry the reduction: %+v", dto.FeatureReductions)
	}
	if empty := toExportDTO(&agent.Export{ID: "e2"}); empty.FeatureReductions == nil {
		t.Error("feature_reductions is null rather than an empty array")
	}
}
