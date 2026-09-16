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

// A mirrored primitive is drawn where the exporter's mesh puts it.
//
// modelMatrix reflects a mirrored primitive's own x before turning and moving it.
// Placement is compared as BOUNDS: the browser and Go each triangulate an outline
// their own way, but both put its corners in the same places.
// Phase 1, stage D1c of docs/plan-2026-09-13-millions-of-parts.md.
func TestRendererPlacesAMirroredPartLikeTheExporter(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node on PATH; skipping the mirrored placement comparison")
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
	harness := filepath.Join(dir, "mirror.js")
	script := `
      const fs = require('fs'), vm = require('vm');
      const sandbox = { window: {}, console };
      vm.createContext(sandbox);
      vm.runInContext(fs.readFileSync(process.argv[2], 'utf8'), sandbox);
      const F = sandbox.window.Forge3D;
      const spec = JSON.parse(fs.readFileSync(process.argv[3], 'utf8'));
      const p = F.partsToDraw(spec)[0];
      const m = F.modelMatrix(p, [0, 0, 0]);
      const pos = F.buildGeometry(p.spec).geo.positions;
      const b = [Infinity, Infinity, Infinity, -Infinity, -Infinity, -Infinity];
      for (let i = 0; i < pos.length; i += 3) {
        const x = pos[i], y = pos[i + 1], z = pos[i + 2];
        const w = [m[0] * x + m[4] * y + m[8] * z + m[12], m[1] * x + m[5] * y + m[9] * z + m[13],
                   m[2] * x + m[6] * y + m[10] * z + m[14]];
        for (let k = 0; k < 3; k++) { b[k] = Math.min(b[k], w[k]); b[3 + k] = Math.max(b[3 + k], w[k]); }
      }
      process.stdout.write(JSON.stringify(b));
    `
	if err := os.WriteFile(harness, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}

	bounds := func(mirrored bool) ([6]float64, [6]float64) {
		arm := geometry.Part{ID: "arm", Name: "Arm", Shape: "extrusion",
			Size: map[string]float64{"depth": 10},
			Profile: []geometry.Point{{X: 0, Y: 0}, {X: 40, Y: 0}, {X: 40, Y: 10}, {X: 10, Y: 10},
				{X: 10, Y: 30}, {X: 0, Y: 30}},
			Position: []float64{5, -3, 2}, Rotation: []float64{10, 25, 20}, Mirrored: mirrored}
		doc := geometry.Document{Name: "arm", Units: "mm", Parts: []geometry.Part{arm}}

		var want [6]float64
		for k := 0; k < 3; k++ {
			want[k], want[3+k] = math.Inf(1), math.Inf(-1)
		}
		for _, g := range geometry.Tessellate(doc, geometry.Millimetre).Groups {
			for _, tri := range g.Triangles {
				for _, v := range [3][3]float64{tri.A, tri.B, tri.C} {
					for k := 0; k < 3; k++ {
						want[k] = math.Min(want[k], v[k])
						want[3+k] = math.Max(want[3+k], v[k])
					}
				}
			}
		}
		input := filepath.Join(dir, "spec.json")
		body, _ := json.Marshal(doc)
		if err := os.WriteFile(input, body, 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(node, harness, asset, input)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("the renderer could not be driven: %v %s", err, stderr.String())
		}
		var got [6]float64
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unreadable renderer output: %v %s", err, out)
		}
		return got, want
	}

	// ‼️ 1e-4 mm, not 1e-6. The browser builds every model matrix as a Float32Array
	// (mat4, multiply, translation, scaling in forge3d.js), because that is what
	// WebGL draws with, so a coordinate near 30 mm carries about 1e-6 of rounding by
	// construction — measured 1.09e-6 on this fixture. A wrong mirror misplaces the
	// arm by tens of millimetres, so this tolerance loses nothing.
	const float32Tolerance = 1e-4
	plainGot, plainWant := bounds(false)
	mirrorGot, mirrorWant := bounds(true)
	for i := range mirrorGot {
		if math.Abs(plainGot[i]-plainWant[i]) > float32Tolerance {
			t.Fatalf("plain arm: browser bounds %v, exporter %v", plainGot, plainWant)
		}
		if math.Abs(mirrorGot[i]-mirrorWant[i]) > float32Tolerance {
			t.Fatalf("mirrored arm: browser bounds %v, exporter %v", mirrorGot, mirrorWant)
		}
	}
	if plainWant == mirrorWant {
		t.Fatal("the fixture is symmetric, so this proves nothing about mirroring")
	}
}
