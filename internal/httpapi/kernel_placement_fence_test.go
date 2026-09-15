package httpapi

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// A kernel-built part is drawn where the exporter puts it, not placed a second time.
//
// The CAD kernel tessellates each solid AFTER placing it, so its mesh is already in
// assembly coordinates. Until 2026-09-13 the browser applied the part's position and
// rotation to that mesh again, and every kernel-built part away from the origin was
// drawn somewhere it is not — a box at x=100 turned 30 degrees landed near (187, 50).
// docs/bugfix/2026-09-13-kernel-built-parts-were-placed-twice.md
//
// # Why the fixture mesh is Go's tessellation and not the kernel's
//
// CI has no build123d. What matters is not how the mesh was made but that it is
// already PLACED, and Go's tessellator produces exactly that: the same part's
// triangles in assembly coordinates. The real kernel's placement was confirmed
// separately (a box at x=100 arrives spanning x 95 to 105).
func TestRendererDoesNotPlaceAKernelMeshTwice(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node on PATH; skipping the kernel-mesh placement comparison")
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

	part := geometry.Part{ID: "block", Name: "Block", Shape: "box",
		Size:     map[string]float64{"width": 20, "height": 20, "depth": 20},
		Position: []float64{100, 0, 0}, Rotation: []float64{0, 0, 30}}
	doc := geometry.Document{Name: "d", Units: "mm", Parts: []geometry.Part{part}}

	// The placed surface, and where its centre is.
	m := geometry.Tessellate(doc, geometry.Millimetre)
	if len(m.Groups) != 1 {
		t.Fatalf("fixture tessellated to %d groups", len(m.Groups))
	}
	var vertices []float64
	var triangles []int
	var want [3]float64
	for _, tri := range m.Groups[0].Triangles {
		for _, v := range [3][3]float64{tri.A, tri.B, tri.C} {
			triangles = append(triangles, len(vertices)/3)
			vertices = append(vertices, v[0], v[1], v[2])
			for i := 0; i < 3; i++ {
				want[i] += v[i]
			}
		}
	}
	for i := range want {
		want[i] /= float64(len(vertices) / 3)
	}

	var spec map[string]any
	raw, _ := json.Marshal(doc)
	_ = json.Unmarshal(raw, &spec)
	parts := spec["parts"].([]any)
	withMesh := map[string]any{}
	for k, v := range parts[0].(map[string]any) {
		withMesh[k] = v
	}
	withMesh["meshes"] = map[string]any{"block": map[string]any{"vertices": vertices, "triangles": triangles}}
	spec["parts"] = []any{withMesh}
	body, _ := json.Marshal(map[string]any{"kernel": spec, "primitive": doc})
	input := filepath.Join(dir, "spec.json")
	if err := os.WriteFile(input, body, 0o600); err != nil {
		t.Fatal(err)
	}

	harness := filepath.Join(dir, "placement.js")
	script := `
      const fs = require('fs'), vm = require('vm');
      const sandbox = { window: {}, console };
      vm.createContext(sandbox);
      vm.runInContext(fs.readFileSync(process.argv[2], 'utf8'), sandbox);
      const F = sandbox.window.Forge3D;
      const input = JSON.parse(fs.readFileSync(process.argv[3], 'utf8'));
      if (typeof F.modelMatrix !== 'function' || typeof F.drawBatches !== 'function') {
        process.stdout.write(JSON.stringify({ missing: true }));
        return;
      }
      function centre(positions, m) {
        let c = [0, 0, 0], n = positions.length / 3;
        for (let i = 0; i < positions.length; i += 3) {
          const x = positions[i], y = positions[i + 1], z = positions[i + 2];
          c[0] += m[0] * x + m[4] * y + m[8] * z + m[12];
          c[1] += m[1] * x + m[5] * y + m[9] * z + m[13];
          c[2] += m[2] * x + m[6] * y + m[10] * z + m[14];
        }
        return c.map(v => v / n);
      }
      const k = F.partsToDraw(input.kernel)[0];
      const p = F.partsToDraw(input.primitive)[0];
      const kernelVerts = k.mesh.vertices;
      const primitiveVerts = F.buildGeometry(p.spec).geo.positions;
      process.stdout.write(JSON.stringify({
        fromKernel: k.fromKernel,
        primitiveFromKernel: p.fromKernel,
        kernel: centre(kernelVerts, F.modelMatrix(k, [0, 0, 0])),
        displaced: centre(kernelVerts, F.modelMatrix(k, [5, -2, 3])),
        primitive: centre(primitiveVerts, F.modelMatrix(p, [0, 0, 0])),
        // What the instanced draw places each with (Phase 6, stage W1).
        batchKernel: centre(kernelVerts, F.drawBatches([k]).batches[0].instances[0].matrix),
        batchPrimitive: centre(primitiveVerts, F.drawBatches([p]).batches[0].instances[0].matrix)
      }));
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
		t.Fatal("forge3d.js exports no modelMatrix or drawBatches, so nothing decides placement in one testable place")
	}
	var got struct {
		FromKernel, PrimitiveFromKernel bool
		Kernel, Displaced, Primitive    [3]float64
		BatchKernel, BatchPrimitive     [3]float64
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unreadable renderer output: %v\n%s", err, out)
	}
	near := func(a, b [3]float64) bool {
		for i := range a {
			if math.Abs(a[i]-b[i]) > 1e-6 {
				return false
			}
		}
		return true
	}
	if !got.FromKernel || got.PrimitiveFromKernel {
		t.Fatalf("fromKernel: part with a mesh %v, part without %v", got.FromKernel, got.PrimitiveFromKernel)
	}
	if !near(got.Kernel, want) {
		t.Errorf("the kernel-built part is drawn centred at %v; the exporter puts it at %v — it was placed twice",
			got.Kernel, want)
	}
	// The draw itself places through drawBatches since Phase 6, stage W1, not through
	// modelMatrix, so the draw is held to the same answer.
	if !near(got.BatchKernel, want) {
		t.Errorf("the instanced draw puts the kernel-built part at %v; the exporter puts it at %v — it was placed twice",
			got.BatchKernel, want)
	}
	if !near(got.BatchPrimitive, want) {
		t.Errorf("the instanced draw puts the primitive at %v, want %v", got.BatchPrimitive, want)
	}
	if !near(got.Primitive, want) {
		t.Errorf("the primitive is drawn centred at %v, want %v", got.Primitive, want)
	}
	if moved := [3]float64{want[0] + 5, want[1] - 2, want[2] + 3}; !near(got.Displaced, moved) {
		t.Errorf("an exploded or offset kernel-built part is drawn at %v, want %v — a view's displacement must still apply",
			got.Displaced, moved)
	}
}
