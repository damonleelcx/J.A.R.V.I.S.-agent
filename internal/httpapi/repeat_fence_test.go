package httpapi

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// The browser draws the copies the exporter builds, copy for copy.
//
// Until 2026-09-13 forge3d.js had no repeat expansion at all: it drew each
// authored part once, so a sixty-spoke wheel showed one spoke while the STEP file
// held sixty. Like the gear, the browser holds a copy of the rule (repeat.go), and
// this holds the copy to Go's answer: ids, positions, rotations, and which copies
// are tools being removed.
// docs/bugfix/2026-09-13-repeat-copies-were-invisible-to-most-readers.md
func TestRendererExpandsARepeatLikeTheExporter(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node on PATH; skipping the renderer/exporter repeat comparison")
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
	harness := filepath.Join(dir, "repeat.js")
	script := `
      const fs = require('fs'), vm = require('vm');
      const sandbox = { window: {}, console };
      vm.createContext(sandbox);
      vm.runInContext(fs.readFileSync(process.argv[2], 'utf8'), sandbox);
      const F = sandbox.window.Forge3D;
      const spec = JSON.parse(fs.readFileSync(process.argv[3], 'utf8'));
      if (typeof F.partsToDraw !== 'function') {
        process.stdout.write(JSON.stringify({ missing: true }));
      } else {
        // The list Studio.load draws, not a re-implementation of it.
        process.stdout.write(JSON.stringify(F.partsToDraw(spec).map(function (p) {
          return { id: p.spec.id, name: p.spec.name, position: p.spec.position,
                   rotation: p.spec.rotation, removed: p.removed };
        })));
      }
    `
	if err := os.WriteFile(harness, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}

	box := func(id string) geometry.Part {
		return geometry.Part{ID: id, Name: strings.Title(id), Shape: "box",
			Size:     map[string]float64{"width": 4, "height": 4, "depth": 60},
			Position: []float64{0, 30, 5}, Rotation: []float64{10, 0, 0}}
	}
	hub := geometry.Part{ID: "hub", Name: "Hub", Shape: "cylinder",
		Size: map[string]float64{"radius": 20, "height": 10}, Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}

	for _, tc := range []struct {
		name string
		doc  geometry.Document
	}{
		{"a straight row", func() geometry.Document {
			p := box("rail")
			p.Repeat = &geometry.Repeat{Count: 4, Offset: []float64{25, 0, -3}}
			return geometry.Document{Name: "d", Units: "mm", Parts: []geometry.Part{p}}
		}()},
		{"a full circle about y", func() geometry.Document {
			p := box("spoke")
			p.Repeat = &geometry.Repeat{Count: 7, About: "y"}
			return geometry.Document{Name: "d", Units: "mm", Parts: []geometry.Part{hub, p}}
		}()},
		{"five across 90 degrees about z", func() geometry.Document {
			p := box("bolt")
			p.Repeat = &geometry.Repeat{Count: 5, About: "z", Angle: 90}
			return geometry.Document{Name: "d", Units: "mm", Parts: []geometry.Part{p}}
		}()},
		{"a pattern used as a cutting tool", func() geometry.Document {
			p := box("hole")
			p.Repeat = &geometry.Repeat{Count: 3, About: "x"}
			return geometry.Document{Name: "d", Units: "mm", Parts: []geometry.Part{hub, p},
				Features: []geometry.Feature{{ID: "drill", Op: "cut", Of: "hub", With: []string{"hole"}}}}
		}()},
		{"a count below two is drawn once", func() geometry.Document {
			p := box("lonely")
			p.Repeat = &geometry.Repeat{Count: 1, Offset: []float64{10, 0, 0}}
			return geometry.Document{Name: "d", Units: "mm", Parts: []geometry.Part{hub, p}}
		}()},
		{"a count past the ceiling is dropped", func() geometry.Document {
			p := box("swarm")
			p.Repeat = &geometry.Repeat{Count: 100000, About: "z"}
			return geometry.Document{Name: "d", Units: "mm", Parts: []geometry.Part{hub, p}}
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
			if bytes.Contains(out, []byte(`"missing":true`)) {
				t.Fatal("forge3d.js exports no partsToDraw: the browser has no repeat expansion, so a " +
					"pattern is drawn as one part while the exporter builds every copy")
			}
			type jsPart struct {
				ID, Name           string
				Position, Rotation []float64
				Removed            bool
			}
			var got []jsPart
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("unreadable renderer output: %v\n%s", err, out)
			}

			want := tc.doc.Expanded()
			removed := map[string]bool{}
			for _, f := range want.Features {
				if op := strings.ToLower(f.Op); op == "cut" || op == "loft" {
					for _, id := range f.With {
						removed[id] = true
					}
				}
			}
			if len(got) != len(want.Parts) {
				t.Fatalf("the browser draws %d parts, the exporter builds %d", len(got), len(want.Parts))
			}
			for i, w := range want.Parts {
				g := got[i]
				if g.ID != w.ID || g.Name != w.Label() {
					t.Errorf("part %d: browser %q/%q, exporter %q/%q", i, g.ID, g.Name, w.ID, w.Label())
				}
				for axis := 0; axis < 3; axis++ {
					if math.Abs(at(g.Position, axis)-at(w.Position, axis)) > 1e-9 {
						t.Errorf("%s position[%d]: browser %v, exporter %v", w.ID, axis, g.Position, w.Position)
					}
					if math.Abs(at(g.Rotation, axis)-at(w.Rotation, axis)) > 1e-9 {
						t.Errorf("%s rotation[%d]: browser %v, exporter %v", w.ID, axis, g.Rotation, w.Rotation)
					}
				}
				if g.Removed != removed[w.ID] {
					t.Errorf("%s: browser removed=%v, exporter removed=%v", w.ID, g.Removed, removed[w.ID])
				}
			}
		})
	}
}

func at(v []float64, i int) float64 {
	if i < len(v) {
		return v[i]
	}
	return 0
}
