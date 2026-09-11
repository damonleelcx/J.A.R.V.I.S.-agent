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

// The browser draws the gear the exporter builds, point for point.
//
// # Why there are two copies
//
// gear.go turns a gear's numbers into an outline, and the browser cannot call it.
// Writing Go's outline onto the stored part instead would miss every document a
// repair replaced, because repairs do not bind (see gear.go). So forge3d.js holds
// a copy — and a copy that drifted would show a person teeth the STEP file does
// not have, which is the one thing this layer exists to prevent.
//
// # Why the drawing, not the facets
//
// For the reason compareBowedDrawing gives: the outline is what both decide, and
// two ear-clippings of one polygon are the same surface. The refusals are
// compared too, because a gear one side draws and the other refuses is a block on
// screen beside a note saying the part is missing.
func TestRendererDrawsTheSameGearAsTheExporter(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node on PATH; skipping the renderer/exporter gear comparison")
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
	harness := filepath.Join(dir, "gear.js")
	script := `
      const fs = require('fs'), vm = require('vm');
      const sandbox = { window: {}, console };
      vm.createContext(sandbox);
      vm.runInContext(fs.readFileSync(process.argv[2], 'utf8'), sandbox);
      const F = sandbox.window.Forge3D;
      const size = JSON.parse(fs.readFileSync(process.argv[3], 'utf8'));
      // Through the real dispatch as well as the function, so a gearOutline that
      // agreed perfectly while buildGeometry stopped calling it would still fail.
      const built = F.buildGeometry({ shape: 'gear', size: size });
      process.stdout.write(JSON.stringify({
        outline: F.gearOutline(size),
        approximated: built.approximated || '',
        supported: F.supportedShapes.indexOf('gear') >= 0,
        vertices: built.geo.positions.length
      }));
    `
	if err := os.WriteFile(harness, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		size map[string]float64
	}{
		{"module 2, 20 teeth, bored", map[string]float64{"module": 2, "teeth": 20, "depth": 6, "bore_radius": 4}},
		// Roots OUTSIDE the base circle, so there is no radial run at the root.
		{"roots outside the base circle", map[string]float64{"module": 1.5, "teeth": 48, "depth": 10}},
		{"a face width called thickness, at 25°", map[string]float64{"module": 3, "teeth": 12,
			"thickness": 8, "pressure_angle": 25}},
		{"too few teeth", map[string]float64{"module": 2, "teeth": 2, "depth": 6}},
		{"a bore through the roots", map[string]float64{"module": 2, "teeth": 20, "depth": 6, "bore_radius": 18}},
		{"a fractional tooth", map[string]float64{"module": 2, "teeth": 20.5, "depth": 6}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := filepath.Join(dir, "size.json")
			body, _ := json.Marshal(tc.size)
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
			type jsPoint struct {
				X, Y float64
				Via  *struct{ X, Y float64 }
			}
			var got struct {
				Outline *struct {
					Profile []jsPoint
					Holes   [][]jsPoint
				}
				Approximated string
				Supported    bool
				Vertices     int
			}
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatal(err)
			}
			if !got.Supported {
				t.Error("forge3d.js does not list gear among the shapes it supports")
			}

			profile, holes, ok := geometry.GearOutlineForTest(tc.size)
			if ok != (got.Outline != nil) {
				t.Fatalf("the exporter says this gear exists: %v; the renderer: %v. One side draws a "+
					"part the other reports missing.", ok, got.Outline != nil)
			}
			if !ok {
				if got.Approximated == "" {
					t.Error("the renderer drew a gear that cannot exist without saying so")
				}
				return
			}
			if got.Approximated != "" || got.Vertices == 0 {
				t.Errorf("the renderer did not draw a gear it agrees exists: %q, %d vertices",
					got.Approximated, got.Vertices)
			}

			compare := func(what string, drawn []jsPoint, built []geometry.Point) {
				t.Helper()
				if len(drawn) != len(built) {
					t.Fatalf("%s: the renderer drew %d points and the exporter %d", what, len(drawn), len(built))
				}
				for i := range drawn {
					d, b := drawn[i], built[i]
					if math.Abs(d.X-b.X) > 1e-9 || math.Abs(d.Y-b.Y) > 1e-9 {
						t.Fatalf("%s point %d: drawn (%v, %v), exported (%v, %v)", what, i, d.X, d.Y, b.X, b.Y)
					}
					if (d.Via == nil) != (b.Via == nil) {
						t.Fatalf("%s point %d: one side bows the edge arriving here and the other does not", what, i)
					}
					if d.Via != nil && (math.Abs(d.Via.X-b.Via.X) > 1e-9 || math.Abs(d.Via.Y-b.Via.Y) > 1e-9) {
						t.Fatalf("%s point %d: the arc passes through (%v, %v) drawn and (%v, %v) exported",
							what, i, d.Via.X, d.Via.Y, b.Via.X, b.Via.Y)
					}
				}
			}
			compare("outline", got.Outline.Profile, profile)
			if len(got.Outline.Holes) != len(holes) {
				t.Fatalf("the renderer drew %d bores and the exporter %d", len(got.Outline.Holes), len(holes))
			}
			for i := range holes {
				compare("bore", got.Outline.Holes[i], holes[i])
			}
		})
	}
}
