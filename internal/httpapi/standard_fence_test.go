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

// The browser draws every standard part the exporter builds, figure for figure.
//
// Phase 2, stage A3. standard.go writes a designation out as a revolve or an
// extrusion, and the browser cannot call it, so forge3d.js holds a copy of the
// catalogue and the drawing — the gear's arrangement, and for the gear's reason: a
// copy that drifted would put a screw on screen that the STEP file does not hold.
//
// Every designation in Go's catalogue is driven through the copy, so a row typed
// differently, a row missing and a row only one side has all fail here. Refusals
// are compared too, and the real dispatch is driven: a part Go refuses must reach
// the screen as a labelled box, and one Go builds must not.
func TestRendererDrawsTheSameStandardPartAsTheExporter(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node on PATH; skipping the renderer/exporter standard part comparison")
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
	harness := filepath.Join(dir, "standard.js")
	script := `
      const fs = require('fs'), vm = require('vm');
      const sandbox = { window: {}, console };
      vm.createContext(sandbox);
      vm.runInContext(fs.readFileSync(process.argv[2], 'utf8'), sandbox);
      const F = sandbox.window.Forge3D;
      const cases = JSON.parse(fs.readFileSync(process.argv[3], 'utf8'));
      process.stdout.write(JSON.stringify({
        designations: F.standardDesignations(),
        supported: F.supportedShapes.indexOf('standard') >= 0,
        results: cases.map(function (c) {
          const drawn = F.partsToDraw({ units: c.units, parts: [c.part] });
          const built = drawn.length ? F.buildGeometry(drawn[0].spec) : null;
          return { part: F.standardPart(c.part, c.units),
                   drawnShape: drawn.length ? drawn[0].spec.shape : '',
                   approximated: built ? (built.approximated || '') : 'nothing drawn' };
        })
      }));
    `
	if err := os.WriteFile(harness, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}

	type tc struct {
		Part  geometry.Part `json:"part"`
		Units string        `json:"units"`
	}
	var cases []tc
	for i, d := range geometry.StandardDesignations() {
		cases = append(cases, tc{Part: geometry.Part{ID: "p", Shape: "standard", Standard: d,
			Size:     map[string]float64{"length": 600},
			Position: []float64{1, -2, 3}, Rotation: []float64{10, -20, 30 + float64(i)}, Mirrored: i%2 == 1}, Units: "mm"})
	}
	extra := []struct {
		standard, units string
		size            map[string]float64
	}{
		{"ISO 4032 M6", "cm", nil}, {"ISO 4762 M12x120", "inches", nil}, {"ISO 15 6005", " Metres ", nil},
		{"iso 4762  m8×30", "mm", nil}, {"EN 10056 L50x50x5", "m", map[string]float64{"length": 1.2}},
		// Refused on both sides.
		{"ISO 4762 M8x33", "mm", nil}, {"608-2RS", "mm", nil}, {"", "mm", nil}, {"ISO 15 608", "furlongs", nil},
		{"ISO 15 608", "", nil}, {"EN 10219 SHS 40x40x3", "mm", nil}, {"EN 10219 SHS 40x40x3", "mm", map[string]float64{"length": -5}},
	}
	for _, e := range extra {
		cases = append(cases, tc{Part: geometry.Part{ID: "q", Name: "Named", Shape: "standard", Standard: e.standard, Size: e.size,
			Position: []float64{0, 0, 0}, Rotation: []float64{0, 90, 0}}, Units: e.units})
	}
	input := filepath.Join(dir, "cases.json")
	body, _ := json.Marshal(cases)
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
	var got struct {
		Designations []string
		Supported    bool
		Results      []struct {
			Part         *geometry.Part `json:"part"`
			DrawnShape   string         `json:"drawnShape"`
			Approximated string         `json:"approximated"`
		}
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Supported {
		t.Error("forge3d.js does not list standard among the shapes it supports")
	}
	if want := geometry.StandardDesignations(); len(got.Designations) != len(want) {
		t.Errorf("the renderer has %d designations and the exporter %d", len(got.Designations), len(want))
	} else {
		for i := range want {
			if got.Designations[i] != want[i] {
				t.Errorf("designation %d: the renderer has %q, the exporter %q", i, got.Designations[i], want[i])
			}
		}
	}

	close := func(a, b float64) bool { return math.Abs(a-b) <= 1e-9 }
	points := func(what string, drawn, built []geometry.Point) string {
		if len(drawn) != len(built) {
			return what + ": point counts differ"
		}
		for i := range drawn {
			d, b := drawn[i], built[i]
			if !close(d.X, b.X) || !close(d.Y, b.Y) || !close(d.Radius, b.Radius) || (d.Via == nil) != (b.Via == nil) ||
				(d.Via != nil && (!close(d.Via.X, b.Via.X) || !close(d.Via.Y, b.Via.Y))) {
				return what + ": point differs"
			}
		}
		return ""
	}
	matrix := func(p geometry.Part) [9]float64 {
		r := make([]float64, 3)
		copy(r, p.Rotation)
		return geometry.RotationMatrix([3]float64{r[0] * math.Pi / 180, r[1] * math.Pi / 180, r[2] * math.Pi / 180})
	}
	for i, c := range cases {
		res := got.Results[i]
		built, ok := geometry.StandardPartForTest(c.Part, c.Units)
		name := c.Part.Standard + " in " + c.Units
		if ok != (res.Part != nil) {
			t.Errorf("%s: the exporter builds it: %v; the renderer: %v", name, ok, res.Part != nil)
			continue
		}
		if !ok {
			if res.DrawnShape != "standard" || res.Approximated == "" {
				t.Errorf("%s is refused, and the renderer drew a %q without saying so (%q)", name, res.DrawnShape, res.Approximated)
			}
			continue
		}
		d := *res.Part
		if res.DrawnShape != built.Shape || res.Approximated != "" {
			t.Errorf("%s: the renderer's dispatch drew %q (%q); the exporter builds a %s", name, res.DrawnShape, res.Approximated, built.Shape)
		}
		problems := []string{
			points("profile", d.Profile, built.Profile),
		}
		if len(d.Holes) != len(built.Holes) {
			problems = append(problems, "hole counts differ")
		} else {
			for h := range built.Holes {
				problems = append(problems, points("hole", d.Holes[h], built.Holes[h]))
			}
		}
		if d.Shape != built.Shape || d.Standard != built.Standard || d.Name != built.Name || d.Axis != built.Axis || d.Mirrored != built.Mirrored {
			problems = append(problems, "shape, designation, name, axis or mirror differ")
		}
		if len(d.Size) != len(built.Size) || !close(d.Size["depth"], built.Size["depth"]) {
			problems = append(problems, "size differs")
		}
		for k := 0; k < 3; k++ {
			if !close(d.Position[k], built.Position[k]) {
				problems = append(problems, "position differs")
			}
		}
		dm, bm := matrix(d), matrix(built)
		for k := range dm {
			if !close(dm[k], bm[k]) {
				problems = append(problems, "rotation differs")
				break
			}
		}
		for _, p := range problems {
			if p != "" {
				t.Errorf("%s: %s\n renderer: %+v\n exporter: %+v", name, p, d, built)
			}
		}
	}
}
