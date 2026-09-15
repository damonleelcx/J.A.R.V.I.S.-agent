package httpapi

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/cad"
)

// The browser moves each placed copy exactly as the build does. Phase 4, stage K4.
//
// The mesh reply sends a definition's triangles once and each copy as a 4×4
// column-major matrix. The renderer draws placed surfaces, so the workbench
// expands them with Forge3D.expandMeshInstances; the agent's pictures expand the
// same build with cad.Build.WorldMeshes. Two copies of one rule: this holds them
// together, on the payload the handler actually writes (meshPayload).
func TestRendererExpandsMeshInstancesLikeTheBuild(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node on PATH; skipping the mesh instance comparison")
	}
	src, err := assetFS.ReadFile("assets/forge3d.js")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	asset := filepath.Join(dir, "forge3d.js")
	if err := os.WriteFile(asset, src, 0o600); err != nil {
		t.Fatal(err)
	}

	// A turn of 30° about z with a move, and a quarter turn about x with another:
	// a matrix read by rows, or a lost translation, lands somewhere else.
	c, s := math.Cos(math.Pi/6), math.Sin(math.Pi/6)
	turnZ := [16]float64{c, s, 0, 0, -s, c, 0, 0, 0, 0, 1, 0, 100, -20, 5, 1}
	turnX := [16]float64{1, 0, 0, 0, 0, 0, 1, 0, 0, -1, 0, 0, -7, 40, 12.5, 1}
	built := &cad.Build{
		Mesh: []cad.MeshPart{{ID: "cut-plate", Label: "Plate",
			Vertices: []float64{0, 0, 0, 60, 0, 0, 0, 0, 60}, Triangles: []int32{0, 1, 2}}},
		MeshDefinitions: []cad.MeshDefinition{
			{Vertices: []float64{1, 2, 3, -4, 5, 6, 7, -8, 9}, Triangles: []int32{0, 1, 2}},
			{Vertices: []float64{0, 0, 0, 2, 0, 0, 0, 2, 0, 0, 0, 2}, Triangles: []int32{0, 1, 2, 0, 2, 3}},
		},
		MeshInstances: []cad.MeshInstance{
			{ID: "bolt-1", Label: "Bolt", Definition: 0, Matrix: turnZ},
			{ID: "bolt-2", Label: "Bolt", Definition: 0, Matrix: turnX},
			{ID: "nut-1", Label: "Nut", Definition: 1, Matrix: turnZ},
		},
	}
	parts, definitions, instances := meshPayload(built)
	body, _ := json.Marshal(map[string]any{"parts": parts, "definitions": definitions, "instances": instances})
	input := filepath.Join(dir, "reply.json")
	if err := os.WriteFile(input, body, 0o600); err != nil {
		t.Fatal(err)
	}

	harness := filepath.Join(dir, "instances.js")
	script := `
      const fs = require('fs'), vm = require('vm');
      const sandbox = { window: {}, console };
      vm.createContext(sandbox);
      vm.runInContext(fs.readFileSync(process.argv[2], 'utf8'), sandbox);
      const F = sandbox.window.Forge3D;
      if (typeof F.expandMeshInstances !== 'function') {
        process.stdout.write(JSON.stringify({ missing: true }));
        return;
      }
      const reply = JSON.parse(fs.readFileSync(process.argv[3], 'utf8'));
      process.stdout.write(JSON.stringify(F.expandMeshInstances(reply)));
    `
	if err := os.WriteFile(harness, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, harness, asset, input)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("the renderer could not be driven: %v %s", err, stderr.String())
	}
	if bytes.Contains(out, []byte(`"missing":true`)) {
		t.Fatal("forge3d.js exports no expandMeshInstances, so the workbench cannot draw placed copies")
	}
	var got []struct {
		ID        string    `json:"id"`
		Label     string    `json:"label"`
		Vertices  []float64 `json:"vertices"`
		Triangles []int32   `json:"triangles"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unreadable renderer output: %v\n%s", err, out)
	}

	want := built.WorldMeshes()
	if len(got) != len(want) {
		t.Fatalf("the browser expanded %d surfaces and the build %d", len(got), len(want))
	}
	for i, w := range want {
		g := got[i]
		if g.ID != w.ID || g.Label != w.Label || len(g.Triangles) != len(w.Triangles) || len(g.Vertices) != len(w.Vertices) {
			t.Fatalf("surface %d: browser %s/%s with %d vertices, build %s/%s with %d",
				i, g.ID, g.Label, len(g.Vertices), w.ID, w.Label, len(w.Vertices))
		}
		for j := range w.Triangles {
			if g.Triangles[j] != w.Triangles[j] {
				t.Fatalf("%s: triangles %v, build %v", w.ID, g.Triangles, w.Triangles)
			}
		}
		for j := range w.Vertices {
			if math.Abs(g.Vertices[j]-w.Vertices[j]) > 1e-9 {
				t.Fatalf("%s: vertex %d is %v in the browser and %v in the build", w.ID, j/3, g.Vertices, w.Vertices)
			}
		}
	}
}
