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

// The browser flattens an assembly tree exactly as the exporter does.
//
// Phase 1, stage D1b of docs/plan-2026-09-13-millions-of-parts.md. forge3d.js holds a
// copy of geometry/tree.go and frame.go (the browser cannot call Go), and this holds
// the copy to Go's answer: every placed part's id, label, position and rotation.
//
// Rotations are compared as MATRICES, not angles: a rotation has more than one Euler
// spelling, and Go's math and V8's may pick different ones at ±180° for the same turn.
func TestRendererFlattensATreeLikeTheExporter(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node on PATH; skipping the renderer/exporter tree comparison")
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
	harness := filepath.Join(dir, "tree.js")
	script := `
      const fs = require('fs'), vm = require('vm');
      const sandbox = { window: {}, console };
      vm.createContext(sandbox);
      vm.runInContext(fs.readFileSync(process.argv[2], 'utf8'), sandbox);
      const F = sandbox.window.Forge3D;
      const spec = JSON.parse(fs.readFileSync(process.argv[3], 'utf8'));
      process.stdout.write(JSON.stringify(F.partsToDraw(spec).map(function (p) {
        return { id: p.spec.id, label: p.spec.name || p.spec.id,
                 position: p.spec.position, rotation: p.spec.rotation };
      })));
    `
	if err := os.WriteFile(harness, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}

	box := func(id string) geometry.Part {
		return geometry.Part{ID: id, Name: "", Shape: "box",
			Size:     map[string]float64{"width": 10, "height": 20, "depth": 30},
			Position: []float64{3, -4, 5}, Rotation: []float64{15, -25, 35}}
	}
	corner := func() geometry.Document {
		return geometry.Document{Name: "car", Units: "mm",
			Definitions: []geometry.Part{box("damper")},
			Assemblies: []geometry.Assembly{
				{ID: "car", Children: []geometry.Child{
					{ID: "front-left", Ref: "corner", Position: []float64{1000, 0, 500}, Rotation: []float64{0, 90, 0}},
					{ID: "front-right", Ref: "corner", Name: "Right corner", Position: []float64{-1000, 0, 500}, Rotation: []float64{180, 0, -170}},
				}},
				{ID: "corner", Children: []geometry.Child{
					{ID: "damper", Ref: "damper", Position: []float64{0, 200, 0}, Rotation: []float64{0, 0, 10}},
					{ID: "spring", Ref: "damper", Name: "Spring", Position: []float64{40, 180, -5}, Rotation: []float64{-90, 45, 0}},
				}},
			},
			Root: "car"}
	}
	for _, tc := range []struct {
		name string
		doc  geometry.Document
	}{
		{"two corners, every level moved and turned", corner()},
		{"a repeat inside a placed definition", func() geometry.Document {
			d := corner()
			d.Definitions[0].Repeat = &geometry.Repeat{Count: 5, About: "y"}
			return d
		}()},
		{"top-level parts beside the tree", func() geometry.Document {
			d := corner()
			d.Parts = []geometry.Part{box("frame")}
			return d
		}()},
		{"an unknown ref is left out, its siblings are not", func() geometry.Document {
			d := corner()
			d.Assemblies[1].Children = append(d.Assemblies[1].Children, geometry.Child{ID: "ghost", Ref: "nowhere"})
			return d
		}()},
		{"a cycle is left out", func() geometry.Document {
			d := corner()
			d.Assemblies[1].Children = append(d.Assemblies[1].Children, geometry.Child{ID: "loop", Ref: "car"})
			return d
		}()},
		{"nesting past the limit stops", func() geometry.Document {
			var asms []geometry.Assembly
			id := func(i int) string { return "level-" + string(rune('a'+i)) }
			for i := 0; i < 20; i++ {
				asms = append(asms, geometry.Assembly{ID: id(i), Children: []geometry.Child{
					{ID: "part", Ref: "damper", Position: []float64{float64(i), 0, 0}},
					{ID: "deeper", Ref: id(i + 1), Rotation: []float64{0, 0, 7}},
				}})
			}
			return geometry.Document{Name: "deep", Units: "mm", Definitions: []geometry.Part{box("damper")},
				Assemblies: asms, Root: id(0)}
		}()},
		{"the part ceiling stops", func() geometry.Document {
			d := geometry.Document{Name: "rivets", Units: "mm",
				Definitions: []geometry.Part{{ID: "rivet", Shape: "cylinder",
					Size:   map[string]float64{"radius": 1, "height": 2},
					Repeat: &geometry.Repeat{Count: 512, Offset: []float64{3, 0, 0}}}},
				Root: "panel"}
			panel := geometry.Assembly{ID: "panel"}
			for i := 0; i < 9; i++ {
				panel.Children = append(panel.Children, geometry.Child{ID: "row" + string(rune('a'+i)), Ref: "rivet",
					Position: []float64{0, float64(i) * 5, 0}})
			}
			d.Assemblies = []geometry.Assembly{panel}
			return d
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := filepath.Join(dir, "spec.json")
			body, _ := json.Marshal(tc.doc)
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
			var got []struct {
				ID, Label          string
				Position, Rotation []float64
			}
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("unreadable renderer output: %v", err)
			}
			want := tc.doc.Expanded().Parts
			if len(got) != len(want) {
				t.Fatalf("the browser draws %d parts, the exporter builds %d", len(got), len(want))
			}
			for i, w := range want {
				g := got[i]
				if g.ID != w.ID || g.Label != w.Label() {
					t.Fatalf("part %d: browser %q/%q, exporter %q/%q", i, g.ID, g.Label, w.ID, w.Label())
				}
				for k := 0; k < 3; k++ {
					if math.Abs(at(g.Position, k)-at(w.Position, k)) > 1e-7 {
						t.Fatalf("%s position: browser %v, exporter %v", w.ID, g.Position, w.Position)
					}
				}
				gm := geometry.RotationMatrix(radians(g.Rotation))
				wm := geometry.RotationMatrix(radians(w.Rotation))
				for k := range gm {
					if math.Abs(gm[k]-wm[k]) > 1e-9 {
						t.Fatalf("%s rotation: browser %v, exporter %v (matrix entry %d differs)", w.ID, g.Rotation, w.Rotation, k)
					}
				}
			}
		})
	}
}

func radians(deg []float64) [3]float64 {
	var out [3]float64
	for i := 0; i < 3 && i < len(deg); i++ {
		out[i] = deg[i] * math.Pi / 180
	}
	return out
}
