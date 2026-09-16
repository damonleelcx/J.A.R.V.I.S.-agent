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
      process.stdout.write(JSON.stringify({ refusal: F.drawRefusal(spec), parts: F.partsToDraw(spec).map(function (p) {
        return { id: p.spec.id, label: p.spec.name || p.spec.id,
                 position: p.spec.position, rotation: p.spec.rotation, mirrored: !!p.spec.mirrored, removed: !!p.removed };
      }) }));
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
		{"children mirrored across x, y and z", func() geometry.Document {
			d := corner()
			d.Assemblies[0].Children[0].Mirror = "x"
			d.Assemblies[0].Children[1].Mirror = "y"
			d.Assemblies[1].Children[1].Mirror = "z"
			return d
		}()},
		{"a mirror inside a mirror cancels", func() geometry.Document {
			d := corner()
			d.Assemblies[0].Children[0].Mirror = "y"
			d.Assemblies[1].Children[0].Mirror = "x"
			return d
		}()},
		{"a mirrored definition placed by a mirrored child", func() geometry.Document {
			d := corner()
			d.Definitions[0].Mirrored = true
			d.Assemblies[0].Children[1].Mirror = "z"
			return d
		}()},
		{"a mirror across an unknown axis is left out", func() geometry.Document {
			d := corner()
			d.Assemblies[1].Children[1].Mirror = "sideways"
			return d
		}()},
		{"a sub-assembly patterned in a line, turned and mirrored", func() geometry.Document {
			d := corner()
			d.Assemblies[0].Children[0].Mirror = "y"
			d.Assemblies[0].Children[0].Pattern = &geometry.Pattern{Kind: "linear", Count: 3, Offset: []float64{0, 350, -40}}
			return d
		}()},
		{"a polar pattern over a repeated definition", func() geometry.Document {
			d := corner()
			d.Definitions[0].Repeat = &geometry.Repeat{Count: 3, Offset: []float64{0, 0, 12}}
			d.Assemblies[1].Children[1].Pattern = &geometry.Pattern{Kind: "polar", Count: 5, About: "x", Angle: 130}
			return d
		}()},
		// 2026-09-15 (bound child positions): the browser never evaluates a binding. It
		// reads the numbers Bind wrote, so a re-specified tree must draw where Go builds it.
		{"children and an interface bound to parameters, re-specified", func() geometry.Document {
			d := corner()
			d.Parameters = []geometry.Parameter{{Name: "half_track", Value: 1000, Unit: "mm", How: geometry.Chosen}}
			d.Assemblies[0].Children[0].PositionFrom = map[string]string{"x": "half_track", "z": "half_track / 2"}
			d.Assemblies[0].Children[1].PositionFrom = map[string]string{"x": "-half_track"}
			d.Assemblies[1].Interfaces = []geometry.Interface{{ID: "mount", Position: []float64{0, 50, 0},
				PositionFrom: map[string]string{"y": "half_track / 20"}}}
			d.Assemblies[1].Children[1].At = "mount"
			v, problems := d.WithParameters(map[string]float64{"half_track": 1234})
			if len(problems) != 0 || v.Assemblies[0].Children[0].Position[0] != 1234 || v.Assemblies[1].Interfaces[0].Position[1] != 61.7 {
				panic("the re-specified tree did not move its bound placements")
			}
			return *v
		}()},
		{"a named grid", func() geometry.Document {
			d := corner()
			d.Assemblies[1].Children[0].Name = "Bolt"
			d.Assemblies[1].Children[0].Pattern = &geometry.Pattern{Kind: "grid", Rows: 3, Columns: 2,
				RowOffset: []float64{0, 0, 25}, ColumnOffset: []float64{18, 4, 0}}
			return d
		}()},
		{"an aligned path in three dimensions, doubling back", func() geometry.Document {
			d := corner()
			d.Assemblies[1].Children[1].Pattern = &geometry.Pattern{Kind: "path", Count: 9, Align: true,
				Path: []geometry.Point{{X: 0, Y: 0, Z: 0}, {X: 30, Y: 40, Z: -20}, {X: 30, Y: 40, Z: -20}, {X: -50, Y: 40, Z: -20}, {X: -50, Y: 90, Z: 60}}}
			return d
		}()},
		{"refused patterns are left out, a pattern of one is drawn once", func() geometry.Document {
			d := corner()
			d.Assemblies[1].Children[0].Pattern = &geometry.Pattern{Kind: "spiral", Count: 4}
			d.Assemblies[1].Children[1].Pattern = &geometry.Pattern{Kind: "path", Count: 4,
				Path: []geometry.Point{{X: 0}, {X: 10, Radius: 2}, {X: 10, Y: 10}}}
			d.Assemblies[0].Children[1].Pattern = &geometry.Pattern{Kind: "polar", Count: 1, About: "z"}
			return d
		}()},
		{"a copy that shares a sibling's id is drawn as Go places it", func() geometry.Document {
			d := corner()
			d.Assemblies[1].Children[0].Pattern = &geometry.Pattern{Kind: "linear", Count: 3, Offset: []float64{5, 0, 0}}
			d.Assemblies[1].Children[1].ID = "damper-2"
			return d
		}()},
		{"a child on its parent's interface", func() geometry.Document {
			d := corner()
			d.Assemblies[1].Interfaces = []geometry.Interface{{ID: "hub", Position: []float64{0, 120, -30}, Rotation: []float64{20, 0, -35}}}
			d.Assemblies[1].Children[1].At = "hub"
			return d
		}()},
		{"a part on a sibling's interface, through a mirror", func() geometry.Document {
			d := corner()
			d.Assemblies[1].Interfaces = []geometry.Interface{{ID: "hub", Position: []float64{10, 60, 5}, Rotation: []float64{0, 70, 15}}}
			d.Assemblies[0].Children[1].Mirror = "y"
			d.Assemblies[0].Children = append(d.Assemblies[0].Children,
				geometry.Child{ID: "wheel", Ref: "damper", At: "front-right/hub", Position: []float64{0, 0, 15}, Rotation: []float64{0, 0, 45}})
			return d
		}()},
		{"a chain of attachments two levels down", func() geometry.Document {
			d := corner()
			d.Assemblies[1].Interfaces = []geometry.Interface{{ID: "strut", Position: []float64{0, 0, 70}, Rotation: []float64{0, 45, 0}}}
			d.Assemblies[1].Children = append(d.Assemblies[1].Children,
				geometry.Child{ID: "knuckle", Ref: "knuckle", At: "strut", Position: []float64{1, 2, 3}, Rotation: []float64{-10, 0, 5}})
			d.Assemblies = append(d.Assemblies, geometry.Assembly{ID: "knuckle",
				Interfaces: []geometry.Interface{{ID: "hub", Position: []float64{5, 6, 7}, Rotation: []float64{0, 30, 0}}},
				Children:   []geometry.Child{{ID: "pin", Ref: "damper"}}})
			d.Assemblies[0].Children = append(d.Assemblies[0].Children,
				geometry.Child{ID: "wheel", Ref: "damper", At: "front-left/knuckle/hub", Position: []float64{0, 0, 9}})
			return d
		}()},
		// The contract's cross-subsystem mount (2026-09-15, attach and bind): a wheel
		// assembly with a polar ring of nuts, placed FROM THE ROOT at a nested
		// interface of a sibling, one side mirrored and turned.
		{"a subsystem placed from the root at a mirrored sibling's nested interface", func() geometry.Document {
			d := corner()
			d.Assemblies[0].Children[1].Mirror = "x"
			d.Assemblies[1].Children = append(d.Assemblies[1].Children,
				geometry.Child{ID: "knuckle", Ref: "knuckle", Position: []float64{-50, 10, 0}, Rotation: []float64{0, 15, 0}})
			d.Assemblies = append(d.Assemblies,
				geometry.Assembly{ID: "knuckle",
					Interfaces: []geometry.Interface{{ID: "hub", Position: []float64{-20, 5, 0}, Rotation: []float64{0, 0, 90}}},
					Children:   []geometry.Child{{ID: "pin", Ref: "damper"}}},
				geometry.Assembly{ID: "wheel", Children: []geometry.Child{{ID: "tyre", Ref: "damper"},
					{ID: "nut", Ref: "damper", Position: []float64{60, 0, 0}, Pattern: &geometry.Pattern{Kind: "polar", Count: 5, About: "y"}}}})
			d.Assemblies[0].Children = append(d.Assemblies[0].Children,
				geometry.Child{ID: "left-wheel", Ref: "wheel", At: "front-left/knuckle/hub", Rotation: []float64{0, 0, 90}},
				geometry.Child{ID: "right-wheel", Ref: "wheel", At: "front-right/knuckle/hub", Rotation: []float64{0, 0, 90}})
			return d
		}()},
		// 2026-09-15 (root-id placement): a path a child of the root writes may begin
		// with the root's own id, and Go places that child — so the browser must place
		// it in the same frame rather than leaving it out.
		//
		// ‼️ No child of the root is named "car" here, on purpose. A first attempt at
		// this case added a second assembly with the root's own id: the duplicate was
		// ignored, the path resolved through the child on BOTH sides, and the case
		// agreed with Go whatever forge3d.js did about the leading id. The drill caught
		// it ("the browser refuses a path that names the root" stayed green) — which is
		// the whole reason the drills exist.
		{"a placement from the root whose path names the root", func() geometry.Document {
			d := corner()
			d.Assemblies[0].Interfaces = []geometry.Interface{{ID: "floor", Position: []float64{0, 10, -20}, Rotation: []float64{0, 25, 0}}}
			d.Assemblies[1].Interfaces = []geometry.Interface{{ID: "hub", Position: []float64{0, 50, 0}, Rotation: []float64{0, 0, 30}}}
			d.Assemblies[0].Children[1].Mirror = "x"
			d.Assemblies[0].Children = append(d.Assemblies[0].Children,
				geometry.Child{ID: "on-the-roots-own", Ref: "damper", At: "car/floor", Rotation: []float64{0, 0, 45}},
				geometry.Child{ID: "on-a-mirrored-sibling", Ref: "damper", At: "car/front-right/hub"})
			return d
		}()},
		// And a real child of the root named like the root keeps its meaning on both
		// sides: "car/hub" reaches THAT child's interface, and the root's own "floor" is
		// not reachable past it.
		{"a real child named like the root keeps its meaning", func() geometry.Document {
			d := corner()
			d.Assemblies[0].Interfaces = []geometry.Interface{{ID: "floor", Position: []float64{0, 10, -20}}}
			d.Assemblies[1].Interfaces = []geometry.Interface{{ID: "hub", Position: []float64{0, 50, 0}, Rotation: []float64{0, 0, 30}}}
			d.Assemblies[0].Children = append(d.Assemblies[0].Children,
				geometry.Child{ID: "car", Ref: "corner", Position: []float64{7, 0, -3}, Rotation: []float64{0, 0, 12}},
				geometry.Child{ID: "through-the-child", Ref: "damper", At: "car/hub"},
				geometry.Child{ID: "past-the-child", Ref: "damper", At: "car/floor"})
			return d
		}()},
		{"a pattern around an interface, and a part on one copy", func() geometry.Document {
			d := corner()
			d.Assemblies[0].Interfaces = []geometry.Interface{{ID: "axle", Position: []float64{0, 0, 300}, Rotation: []float64{90, 0, 0}}}
			d.Assemblies[1].Interfaces = []geometry.Interface{{ID: "hub", Position: []float64{0, 50, 0}, Rotation: []float64{0, 0, 30}}}
			d.Assemblies[0].Children = append(d.Assemblies[0].Children,
				geometry.Child{ID: "bolt", Ref: "corner", At: "axle", Position: []float64{40, 0, 0},
					Pattern: &geometry.Pattern{Kind: "polar", Count: 3, About: "z"}},
				geometry.Child{ID: "cap", Ref: "damper", At: "bolt-2/hub", Position: []float64{0, 4, 0}})
			return d
		}()},
		{"refused attachments are left out, their siblings are not", func() geometry.Document {
			d := corner()
			d.Assemblies[1].Interfaces = []geometry.Interface{{ID: "hub", Position: []float64{0, 50, 0}}}
			d.Assemblies[0].Children = append(d.Assemblies[0].Children,
				geometry.Child{ID: "lost", Ref: "damper", At: "front-left/nowhere"},
				geometry.Child{ID: "x", Ref: "corner", At: "y/hub"},
				geometry.Child{ID: "y", Ref: "corner", At: "x/hub"},
				geometry.Child{ID: "bolt", Ref: "corner", Pattern: &geometry.Pattern{Kind: "linear", Count: 2, Offset: []float64{0, 0, 90}}},
				geometry.Child{ID: "vague", Ref: "damper", At: "bolt/hub"},
				geometry.Child{ID: "kept", Ref: "damper", At: "bolt-1/hub"})
			return d
		}()},
		{"an assembly's cut, in every occurrence", func() geometry.Document {
			d := corner()
			d.Assemblies[1].Features = []geometry.Feature{{ID: "bore", Op: "cut", Of: "damper", With: []string{"spring"}}}
			return d
		}()},
		{"a cut whose tools are a whole pattern", func() geometry.Document {
			d := corner()
			d.Assemblies[1].Children[1].Pattern = &geometry.Pattern{Kind: "linear", Count: 3, Offset: []float64{0, 0, 12}}
			d.Assemblies[1].Features = []geometry.Feature{{ID: "holes", Op: "cut", Of: "damper", With: []string{"spring"}}}
			return d
		}()},
		{"a parent's cut reaching one copy inside a child", func() geometry.Document {
			d := corner()
			d.Assemblies[1].Children[1].Pattern = &geometry.Pattern{Kind: "linear", Count: 2, Offset: []float64{0, 0, 12}}
			d.Assemblies[0].Children = append(d.Assemblies[0].Children, geometry.Child{ID: "frame", Ref: "damper"})
			d.Assemblies[0].Features = []geometry.Feature{{ID: "notch", Op: "cut", Of: "frame", With: []string{"front-left/spring-2"}}}
			return d
		}()},
		{"a feature naming nothing leaves its tools solid", func() geometry.Document {
			d := corner()
			d.Assemblies[1].Features = []geometry.Feature{{ID: "bore", Op: "cut", Of: "damper", With: []string{"spring", "nowhere"}}}
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
		// Past the KERNEL's ceiling (4096) and well inside the viewport's: drawn, copy for
		// copy, since Phase 6, stage W1. A browser still holding 4096 draws nothing here.
		{"a design past the kernel's ceiling is drawn", func() geometry.Document {
			return geometry.Document{Name: "panel", Units: "mm", Root: "panel",
				Definitions: []geometry.Part{{ID: "rivet", Shape: "cylinder",
					Size:   map[string]float64{"radius": 1, "height": 2},
					Repeat: &geometry.Repeat{Count: 500, Offset: []float64{3, 0, 0}}}},
				Assemblies: []geometry.Assembly{{ID: "panel", Children: []geometry.Child{
					{ID: "row", Ref: "rivet", Pattern: &geometry.Pattern{Kind: "linear", Count: 10, Offset: []float64{0, 5, 0}}},
				}}}}
		}()},
		// Carried over the viewport's ceiling by a PATTERN: the next refusal case reaches it
		// through repeats alone, so the browser could forget to multiply by a pattern's
		// copies and still agree with Go there.
		{"a pattern that carries a design over the ceiling is refused whole", func() geometry.Document {
			return geometry.Document{Name: "panel", Units: "mm", Root: "panel",
				Definitions: []geometry.Part{{ID: "rivet", Shape: "cylinder",
					Size:   map[string]float64{"radius": 1, "height": 2},
					Repeat: &geometry.Repeat{Count: 500, Offset: []float64{3, 0, 0}}}},
				Assemblies: []geometry.Assembly{{ID: "panel", Children: []geometry.Child{
					{ID: "row", Ref: "rivet", Pattern: &geometry.Pattern{Kind: "linear", Count: 201, Offset: []float64{0, 5, 0}}},
				}}}}
		}()},
		{"a design too large to draw is refused whole, in Go's words", func() geometry.Document {
			d := geometry.Document{Name: "rivets", Units: "mm",
				Definitions: []geometry.Part{{ID: "rivet", Shape: "cylinder",
					Size:   map[string]float64{"radius": 1, "height": 2},
					Repeat: &geometry.Repeat{Count: 512, Offset: []float64{3, 0, 0}}}},
				Root: "panel"}
			panel := geometry.Assembly{ID: "panel"}
			for i := 0; i < 196; i++ {
				panel.Children = append(panel.Children, geometry.Child{ID: "row" + string(rune('a'+i%26)) + string(rune('a'+i/26)), Ref: "rivet",
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
			var drawn struct {
				Refusal string
				Parts   []struct {
					ID, Label          string
					Position, Rotation []float64
					Mirrored           bool
					Removed            bool
				}
			}
			if err := json.Unmarshal(out, &drawn); err != nil {
				t.Fatalf("unreadable renderer output: %v", err)
			}
			got := drawn.Parts
			// A design too large to draw draws nothing, and says why in Go's words (S0). The
			// ceiling is the VIEWPORT's since Phase 6, stage W1, and a refused design is not
			// expanded here either: that would cost the test what the refusal saves.
			var expanded geometry.Document
			if refusal := tc.doc.ViewportRefusal(); refusal != "" || drawn.Refusal != "" {
				if drawn.Refusal != refusal {
					t.Fatalf("the browser refuses with %q, the exporter with %q", drawn.Refusal, refusal)
				}
			} else {
				expanded = tc.doc.Expanded()
			}
			want := expanded.Parts
			// Material being removed is drawn ghosted: the tools of a cut or a loft,
			// including those an assembly's own features name (D1e).
			removed := map[string]bool{}
			for _, f := range expanded.Features {
				if op := strings.ToLower(f.Op); op == "cut" || op == "loft" {
					for _, id := range f.With {
						removed[id] = true
					}
				}
			}
			if len(got) != len(want) {
				t.Fatalf("the browser draws %d parts, the exporter builds %d", len(got), len(want))
			}
			for i, w := range want {
				g := got[i]
				if g.Mirrored != w.Mirrored {
					t.Fatalf("%s: browser mirrored=%v, exporter mirrored=%v", w.ID, g.Mirrored, w.Mirrored)
				}
				if g.Removed != removed[w.ID] {
					t.Fatalf("%s: the browser draws it removed=%v, the exporter removes it=%v", w.ID, g.Removed, removed[w.ID])
				}
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
