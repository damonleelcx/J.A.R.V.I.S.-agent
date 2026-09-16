package httpapi

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/cad"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// The viewport draws every copy where the exporter places it. Phase 6, stages W1–W3.
//
// # What changed that needs holding
//
// Until W1 each placed part was its own draw call, placed by a matrix the renderer
// built as a uniform just before drawing it, and the tree fence could read that
// matrix's inputs off partsToDraw. Since W1 a part is an INSTANCE: its matrix, colour,
// opacity and highlight are written into a buffer per batch and read by the GPU at a
// stride and offset. A mistake there — a column read as a row, an offset one float
// out, a divisor left on — draws copies somewhere the document never put them, while
// partsToDraw and every earlier fence stay exactly right.
//
// # How it is read back
//
// scripts/webgl-stub.js is a WebGL context that records calls and reads each
// instance's attributes back out of the uploaded buffers by location, stride, offset
// and divisor, the way a GPU does. These fences drive the shipped Studio through it —
// WebGL2, WebGL1 with ANGLE_instanced_arrays, and WebGL1 without it — and hold what
// was SENT to Go's own expansion. The stub also refuses the state traps (a divisor on
// attribute 0, instance attributes left on for the grid), so a frame that would fail
// in a browser fails here.
//
// It cannot compile GLSL or time a GPU; docs/spikes/2026-09-15-instanced-viewport is
// the run in a real browser.

var glModes = []string{"webgl2", "webgl1", "webgl1-noext"}

type drawnInstance struct {
	Model     [16]float64
	Colour    [4]float64
	Highlight float64
}

type drawCall struct {
	Instanced bool
	Elements  int
	FrontFace string
	Instances []drawnInstance
}

type drawnFrame struct {
	Stats struct {
		Path                                                                string
		Batches, DrawCalls, Instances, Culled, Proxied, Hidden, Translucent int
	}
	Problems []string
	Draws    []drawCall
}

func (f drawnFrame) instances() []drawnInstance {
	var out []drawnInstance
	for _, d := range f.Draws {
		out = append(out, d.Instances...)
	}
	return out
}

// viewportRun is what the harness reports for one GL mode.
type viewportRun struct {
	Framed   drawnFrame
	Picks    []*string
	Near     []drawnFrame
	Far      *drawnFrame
	Selected []drawnFrame
	Isolated []drawnFrame
	Rows     [][]struct {
		Path  string
		Slots []string
	}
}

const viewportHarness = `
  const fs = require('fs');
  const stub = require(process.argv[2]);
  const F = stub.loadForge3D(process.argv[3]);
  const input = JSON.parse(fs.readFileSync(process.argv[4], 'utf8'));
  if (typeof F.drawBatches !== 'function' || typeof F.occurrenceMatcher !== 'function' ||
      typeof F.treeChildren !== 'function') {
    process.stdout.write(JSON.stringify({ missing: true }));
    return;
  }
  // ‼️ Taken, then reset: select, isolate and resetView draw too, and without the second
  // reset their calls land in the array the previous frame already returned.
  function frame(studio, record) {
    record.reset();
    studio.draw();
    const taken = { stats: studio.stats, problems: record.problems.slice(), draws: record.draws };
    record.reset();
    return taken;
  }
  const out = {};
  for (const mode of input.modes) {
    const made = stub.makeCanvas(mode, 640, 480), record = made.record;
    const studio = new F.Studio(made.canvas, { onError() {} });
    studio.load(input.spec, input.built || null);
    const run = { framed: frame(studio, record) };
    if (input.picks) {
      run.picks = [];
      run.near = [];
      for (const target of input.picks) {
        studio.camera.target = target;
        studio.camera.distance = input.pickDistance;
        run.near.push(frame(studio, record));
        const hit = studio.pick(320, 240);
        run.picks.push(hit ? hit.id : null);
      }
    }
    if (input.far) {
      studio.resetView();
      studio.camera.distance = input.far;
      run.far = frame(studio, record);
    }
    if (input.paths) {
      run.selected = [];
      run.isolated = [];
      for (const p of input.paths) {
        studio.resetView();
        studio.select(F.occurrenceMatcher(p));
        run.selected.push(frame(studio, record));
        studio.select(null);
        studio.isolate(F.occurrenceMatcher(p));
        run.isolated.push(frame(studio, record));
        studio.isolate(null);
      }
    }
    if (input.rows) run.rows = input.rows.map((q) => F.treeChildren(input.spec, q.ref, q.path));
    out[mode] = run;
  }
  process.stdout.write(JSON.stringify(out));
`

// driveViewport runs the harness over input and returns each mode's run.
func driveViewport(t *testing.T, input map[string]any) map[string]viewportRun {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node on PATH; skipping the instanced viewport comparison")
	}
	src, err := assetFS.ReadFile("assets/forge3d.js")
	if err != nil {
		t.Fatal(err)
	}
	stub, err := filepath.Abs(filepath.Join("..", "..", "scripts", "webgl-stub.js"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	asset := filepath.Join(dir, "forge3d.js")
	harness := filepath.Join(dir, "viewport.js")
	spec := filepath.Join(dir, "input.json")
	if input["modes"] == nil {
		input["modes"] = glModes
	}
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string][]byte{asset: src, harness: []byte(viewportHarness), spec: body} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(node, harness, stub, asset, spec)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("the renderer could not be driven: %v %s", err, stderr.String())
	}
	if bytes.Contains(out, []byte(`"missing":true`)) {
		t.Fatal("forge3d.js exports no drawBatches, occurrenceMatcher or treeChildren")
	}
	runs := map[string]viewportRun{}
	if err := json.Unmarshal(out, &runs); err != nil {
		t.Fatalf("unreadable renderer output: %v", err)
	}
	for _, mode := range input["modes"].([]string) {
		if _, ok := runs[mode]; !ok {
			t.Fatalf("the harness reported nothing for %s", mode)
		}
	}
	return runs
}

// placement is where the exporter puts a part: T · R · (x negated when mirrored),
// column-major, from geometry.RotationMatrix — the rule Part.Mirrored states.
func placement(p geometry.Part) [16]float64 {
	r := geometry.RotationMatrix(radians(p.Rotation))
	sx := 1.0
	if p.Mirrored {
		sx = -1
	}
	return [16]float64{r[0] * sx, r[3] * sx, r[6] * sx, 0, r[1], r[4], r[7], 0, r[2], r[5], r[8], 0,
		at(p.Position, 0), at(p.Position, 1), at(p.Position, 2), 1}
}

// ‼️ Float32 by construction: the browser uploads matrices as a Float32Array, so a
// translation near 2 m carries ~1e-4 of rounding. A copy placed wrong is off by
// millimetres or turned by degrees, so neither tolerance hides one.
func sameMatrix(a, b [16]float64) bool {
	for k := range a {
		tol := 2e-5
		if k >= 12 {
			tol = 2e-3
		}
		if math.Abs(a[k]-b[k]) > tol {
			return false
		}
	}
	return true
}

// whose names each drawn instance by the part of want it is drawn as, matched by
// matrix. It fails the test for an instance that is no part's placement, and for a
// part placed by no instance when complete is set.
func whose(t *testing.T, label string, drawn []drawnInstance, want []geometry.Part, complete bool) []string {
	t.Helper()
	used := make([]bool, len(want))
	ids := make([]string, len(drawn))
	for i, d := range drawn {
		found := false
		for j, p := range want {
			if !used[j] && sameMatrix(d.Model, placement(p)) {
				used[j], ids[i], found = true, p.ID, true
				break
			}
		}
		if !found {
			t.Fatalf("%s: an instance is drawn with matrix %v, which places no part the exporter builds", label, d.Model)
		}
	}
	if complete {
		for j, u := range used {
			if !u {
				t.Fatalf("%s: the exporter builds %s at %v and no instance draws it", label, want[j].ID, placement(want[j]))
			}
		}
	}
	return ids
}

func rgb(hex string) [3]float64 {
	v, _ := strconv.ParseUint(strings.TrimPrefix(hex, "#"), 16, 32)
	return [3]float64{float64(v>>16&255) / 255, float64(v>>8&255) / 255, float64(v&255) / 255}
}

func det3(m [16]float64) float64 {
	return m[0]*(m[5]*m[10]-m[6]*m[9]) - m[4]*(m[1]*m[10]-m[2]*m[9]) + m[8]*(m[1]*m[6]-m[2]*m[5])
}

func noProblems(t *testing.T, label string, f drawnFrame) {
	t.Helper()
	if len(f.Problems) > 0 {
		t.Fatalf("%s: the frame did something a browser refuses: %v", label, f.Problems)
	}
}

// instancedCar places every kind of pattern, mirrors at three levels, a repeat inside
// a definition, a cut whose tools are ghosted, a translucent part and a top-level
// part beside the tree.
func instancedCar() geometry.Document {
	size := func(kv ...float64) map[string]float64 {
		names := []string{"width", "height", "depth"}
		out := map[string]float64{}
		for i, v := range kv {
			out[names[i]] = v
		}
		return out
	}
	return geometry.Document{Name: "car", Units: "mm", Root: "car",
		Parts: []geometry.Part{{ID: "frame", Name: "Frame", Shape: "box", Size: size(2400, 40, 1200),
			Position: []float64{0, -300, 0}, Rotation: []float64{0, 5, 0}, Color: "#555555", Opacity: 1}},
		Definitions: []geometry.Part{
			{ID: "damper", Shape: "box", Size: size(10, 20, 30), Position: []float64{3, -4, 5},
				Rotation: []float64{15, -25, 35}, Color: "#ff8800", Opacity: 1},
			{ID: "pin", Shape: "cylinder", Size: map[string]float64{"radius": 4, "height": 30}, Color: "#3366cc",
				Opacity: 1, Repeat: &geometry.Repeat{Count: 3, Offset: []float64{0, 0, 12}}},
			{ID: "lens", Shape: "sphere", Size: map[string]float64{"radius": 9}, Color: "#aaccee", Opacity: 0.5,
				Mirrored: true, Rotation: []float64{0, 0, 20}},
		},
		Assemblies: []geometry.Assembly{
			{ID: "car", Children: []geometry.Child{
				{ID: "front-left", Ref: "corner", Position: []float64{1000, 0, 500}, Rotation: []float64{0, 90, 0},
					Mirror: "y", Pattern: &geometry.Pattern{Kind: "linear", Count: 3, Offset: []float64{0, 350, -40}}},
				{ID: "front-right", Ref: "corner", Position: []float64{-1000, 0, 500}, Rotation: []float64{180, 0, -170}},
				{ID: "lamp", Ref: "lens", Position: []float64{0, 300, 900}, Pattern: &geometry.Pattern{Kind: "grid",
					Rows: 2, Columns: 3, RowOffset: []float64{0, 0, 60}, ColumnOffset: []float64{80, 0, 0}}},
				{ID: "trim", Ref: "damper", Mirror: "x", Pattern: &geometry.Pattern{Kind: "path", Count: 5, Align: true,
					Path: []geometry.Point{{X: 0, Y: 0, Z: 0}, {X: 300, Y: 400, Z: -200}, {X: -500, Y: 400, Z: -200}}}},
			}},
			{ID: "corner", Children: []geometry.Child{
				{ID: "damper", Ref: "damper", Position: []float64{0, 200, 0}, Rotation: []float64{0, 0, 10}},
				{ID: "pin", Ref: "pin", Position: []float64{40, 180, -5}, Mirror: "z",
					Pattern: &geometry.Pattern{Kind: "polar", Count: 5, About: "x", Angle: 130}},
			}, Features: []geometry.Feature{{ID: "bore", Op: "cut", Of: "damper", With: []string{"pin"}}}},
		}}
}

// Every instance the browser uploads is a part the exporter builds, at the exporter's
// placement, in its colour and opacity, wound the way its matrix turns it — and every
// part the exporter builds is drawn by exactly one instance. Through WebGL2, WebGL1
// with ANGLE_instanced_arrays and WebGL1 without it.
func TestRendererUploadsTheInstancesTheExporterPlaces(t *testing.T) {
	doc := instancedCar()
	expanded := doc.Expanded()
	want := expanded.Parts
	removed := map[string]bool{}
	for _, f := range expanded.Features {
		if strings.EqualFold(f.Op, "cut") {
			for _, id := range f.With {
				removed[id] = true
			}
		}
	}
	byID := map[string]geometry.Part{}
	geometries := map[string]bool{}
	mirrored := 0
	for _, p := range want {
		byID[p.ID] = p
		key, _ := json.Marshal([]any{p.Shape, p.Size})
		geometries[string(key)] = true
		if p.Mirrored {
			mirrored++
		}
	}
	if len(removed) == 0 || mirrored == 0 || len(want) < 60 {
		t.Fatalf("the fixture no longer exercises what it claims: %d removed, %d mirrored, %d parts",
			len(removed), mirrored, len(want))
	}

	runs := driveViewport(t, map[string]any{"spec": doc})
	for _, mode := range glModes {
		f := runs[mode].Framed
		noProblems(t, mode, f)
		drawn := f.instances()
		if len(drawn) != len(want) || f.Stats.Culled != 0 {
			t.Fatalf("%s: %d instances drawn and %d culled with the whole model framed; the exporter builds %d",
				mode, len(drawn), f.Stats.Culled, len(want))
		}
		ids := whose(t, mode, drawn, want, true)
		i := 0
		for _, d := range f.Draws {
			for _, in := range d.Instances {
				p := byID[ids[i]]
				i++
				wantRGB, wantAlpha := rgb(p.Color), p.Opacity
				if removed[p.ID] {
					wantRGB, wantAlpha = rgb("#e6cd8f"), 0.22
				}
				for k := 0; k < 3; k++ {
					if math.Abs(in.Colour[k]-wantRGB[k]) > 1e-6 {
						t.Fatalf("%s: %s is drawn in %v, want %v", mode, p.ID, in.Colour, wantRGB)
					}
				}
				if math.Abs(in.Colour[3]-wantAlpha) > 1e-6 {
					t.Fatalf("%s: %s is drawn at opacity %v, want %v (removed=%v)", mode, p.ID, in.Colour[3], wantAlpha, removed[p.ID])
				}
				if cw := det3(in.Model) < 0; cw != (d.FrontFace == "cw") {
					t.Fatalf("%s: %s is drawn with front face %s and a matrix of determinant %v — inside out",
						mode, p.ID, d.FrontFace, det3(in.Model))
				}
				if in.Highlight != 0 {
					t.Fatalf("%s: %s is highlighted with nothing selected", mode, p.ID)
				}
			}
		}
		// One call per batch per winding and level of detail (three since Phase 6, stage W2:
		// whole, simplified, box), for what is opaque; a batch per distinct shape. What is
		// translucent is sorted by copy and may take more.
		if f.Stats.Batches != len(geometries) {
			t.Errorf("%s: %d batches for %d distinct shapes", mode, f.Stats.Batches, len(geometries))
		}
		opaqueCalls := 0
		for _, d := range f.Draws {
			if len(d.Instances) > 0 && d.Instances[0].Colour[3] >= 1 {
				opaqueCalls++
			}
		}
		switch mode {
		case "webgl1-noext":
			if f.Stats.DrawCalls != len(want) {
				t.Errorf("%s: %d draw calls for %d copies without instancing", mode, f.Stats.DrawCalls, len(want))
			}
		default:
			if opaqueCalls > 6*f.Stats.Batches {
				t.Errorf("%s: %d opaque draw calls for %d batches; instancing draws each batch in at most six",
					mode, opaqueCalls, f.Stats.Batches)
			}
		}
	}
}

// A kernel definition from the mesh reply is uploaded once and every copy of it
// drawn by the reply's own matrix, in one call; a part a feature changed is drawn
// where the kernel left it. Phase 6, stage W1, over stage K4's payload.
func TestRendererDrawsTheMeshReplysInstancesByTheirMatrices(t *testing.T) {
	c, s := math.Cos(math.Pi/6), math.Sin(math.Pi/6)
	turnZ := [16]float64{c, s, 0, 0, -s, c, 0, 0, 0, 0, 1, 0, 100, -20, 5, 1}
	turnX := [16]float64{1, 0, 0, 0, 0, 0, 1, 0, 0, -1, 0, 0, -7, 40, 12.5, 1}
	identity := [16]float64{1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1}
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
	box := func(id string, x float64) geometry.Part {
		return geometry.Part{ID: id, Shape: "box", Size: map[string]float64{"width": 5, "height": 5, "depth": 5},
			Position: []float64{x, 300, 0}, Color: "#888888", Opacity: 1}
	}
	doc := geometry.Document{Name: "bolts", Units: "mm",
		Parts: []geometry.Part{box("bolt-1", 0), box("bolt-2", 50), box("nut-1", 100), box("cut-plate", 150)}}

	runs := driveViewport(t, map[string]any{"spec": doc,
		"built": map[string]any{"parts": parts, "definitions": definitions, "instances": instances}})
	type placed struct {
		elements int
		matrix   [16]float64
	}
	want := []placed{{3, turnZ}, {3, turnX}, {6, turnZ}, {3, identity}}
	for _, mode := range glModes {
		f := runs[mode].Framed
		noProblems(t, mode, f)
		var got []placed
		for _, d := range f.Draws {
			for _, in := range d.Instances {
				got = append(got, placed{d.Elements, in.Model})
			}
		}
		if len(got) != len(want) {
			t.Fatalf("%s: %d instances drawn from a reply of 3 copies and 1 placed part", mode, len(got))
		}
		used := make([]bool, len(got))
		for _, w := range want {
			found := false
			for i, g := range got {
				if !used[i] && g.elements == w.elements && sameMatrix(g.matrix, w.matrix) {
					used[i], found = true, true
					break
				}
			}
			if !found {
				t.Fatalf("%s: nothing is drawn with %d indices at %v; drawn: %v", mode, w.elements, w.matrix, got)
			}
		}
		if f.Stats.Batches != 3 {
			t.Errorf("%s: %d batches; two definitions and one placed part are three", mode, f.Stats.Batches)
		}
		if mode != "webgl1-noext" && f.Stats.DrawCalls != 3 {
			t.Errorf("%s: %d draw calls; one per definition and one for the placed part is three", mode, f.Stats.DrawCalls)
		}
	}
}

// pickable is a design whose parts stand far enough apart that a ray from 60 mm away
// through one part's centre can reach no other: patterned, mirrored, nested.
func pickable() geometry.Document {
	return geometry.Document{Name: "frame", Units: "mm", Root: "frame",
		Definitions: []geometry.Part{
			{ID: "bolt", Shape: "box", Size: map[string]float64{"width": 10, "height": 20, "depth": 30},
				Rotation: []float64{0, 30, 0}, Color: "#aa7733", Opacity: 1},
			{ID: "nut", Shape: "cylinder", Size: map[string]float64{"radius": 8, "height": 12},
				Color: "#3377aa", Opacity: 1},
		},
		Assemblies: []geometry.Assembly{
			{ID: "frame", Children: []geometry.Child{
				{ID: "row", Ref: "bay", Mirror: "x", Pattern: &geometry.Pattern{Kind: "linear", Count: 3, Offset: []float64{400, 0, 0}}},
				{ID: "ring", Ref: "nut", Position: []float64{0, 600, 0}, Pattern: &geometry.Pattern{Kind: "polar", Count: 4, About: "z"}},
				{ID: "lamp", Ref: "bolt", Position: []float64{0, -400, -400}, Pattern: &geometry.Pattern{Kind: "grid",
					Rows: 2, Columns: 2, RowOffset: []float64{0, 0, -300}, ColumnOffset: []float64{0, -300, 0}}},
			}},
			{ID: "bay", Children: []geometry.Child{
				{ID: "bolt", Ref: "bolt"},
				{ID: "nut", Ref: "nut", Position: []float64{0, 0, 250}, Mirror: "y"},
			}},
		}}
}

// A click on a drawn copy names its occurrence path — the id the exporter gives that
// part — through the matrix the copy was drawn with. And only what is in view is
// sent: from 60 mm away most of the design is culled, from far away every cylinder is
// drawn as its box, and neither changes what a click finds. Phase 6, stages W2 and W3.
func TestRendererPicksAnInstanceBackToItsOccurrencePath(t *testing.T) {
	doc := pickable()
	want := doc.Expanded().Parts
	var targets [][]float64
	nuts := 0
	for i, p := range want {
		for _, q := range want[i+1:] {
			d := math.Hypot(math.Hypot(at(p.Position, 0)-at(q.Position, 0), at(p.Position, 1)-at(q.Position, 1)),
				at(p.Position, 2)-at(q.Position, 2))
			if d < 150 {
				t.Fatalf("the fixture puts %s and %s %.0f mm apart, too close to pick one unambiguously", p.ID, q.ID, d)
			}
		}
		targets = append(targets, []float64{at(p.Position, 0), at(p.Position, 1), at(p.Position, 2)})
		if p.Shape == "cylinder" {
			nuts++
		}
	}
	runs := driveViewport(t, map[string]any{"spec": doc, "picks": targets, "pickDistance": 60, "far": 1e5})
	for _, mode := range glModes {
		run := runs[mode]
		for i, p := range want {
			near := run.Near[i]
			noProblems(t, mode+" near "+p.ID, near)
			if run.Picks[i] == nil || *run.Picks[i] != p.ID {
				got := "nothing"
				if run.Picks[i] != nil {
					got = *run.Picks[i]
				}
				t.Errorf("%s: a click on %s's centre picks %s", mode, p.ID, got)
			}
			if near.Stats.Culled == 0 || near.Stats.Instances+near.Stats.Culled != len(want) || near.Stats.Proxied != 0 {
				t.Errorf("%s: looking at %s from 60 mm, %d drawn, %d culled, %d as boxes; want some culled, none as boxes",
					mode, p.ID, near.Stats.Instances, near.Stats.Culled, near.Stats.Proxied)
			}
		}
		far := *run.Far
		noProblems(t, mode+" far", far)
		// Every draw from 100 m is twelve triangles: the bolts because they are boxes, the
		// nuts because they are drawn as their boxes. Near, a nut is its forty-sided self.
		boxes := 0
		for _, d := range far.Draws {
			if d.Elements == 36 {
				boxes += len(d.Instances)
			}
		}
		if far.Stats.Proxied != nuts || boxes != len(want) || far.Stats.Culled != 0 {
			t.Errorf("%s: from 100 m, %d copies drawn as boxes, %d of %d drawn with twelve triangles and %d culled; want the %d cylinders as boxes",
				mode, far.Stats.Proxied, boxes, len(want), far.Stats.Culled, nuts)
		}
		whose(t, mode+" far", far.instances(), want, true)
	}
}

// without is doc with one child taken out of one assembly.
func without(doc geometry.Document, assembly, child string) geometry.Document {
	out := doc
	out.Assemblies = make([]geometry.Assembly, len(doc.Assemblies))
	for i, a := range doc.Assemblies {
		out.Assemblies[i] = a
		if a.ID != assembly {
			continue
		}
		out.Assemblies[i].Children = nil
		for _, c := range a.Children {
			if c.ID != child {
				out.Assemblies[i].Children = append(out.Assemblies[i].Children, c)
			}
		}
	}
	return out
}

// placedBy is every occurrence doc places that it would not place without that child:
// what a tree row reaches, worked out by Go without reading a path.
func placedBy(doc geometry.Document, assembly, child string) map[string]bool {
	kept := map[string]bool{}
	for _, p := range without(doc, assembly, child).Expanded().Parts {
		kept[p.ID] = true
	}
	out := map[string]bool{}
	for _, p := range doc.Expanded().Parts {
		if !kept[p.ID] {
			out[p.ID] = true
		}
	}
	return out
}

func both(a, b map[string]bool) map[string]bool {
	out := map[string]bool{}
	for k := range a {
		if b[k] {
			out[k] = true
		}
	}
	return out
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Selecting a tree row lights exactly the occurrences under it, and isolating it draws
// exactly those — the set Go places because that child exists, worked out by taking
// the child away rather than by reading paths. A patterned row's copies divide it with
// nothing left over. And the rows are the children the exporter keeps. Phase 6,
// stages W2 and W3.
func TestRendererSelectsAndIsolatesTheOccurrencesUnderATreeNode(t *testing.T) {
	doc := instancedCar()
	doc.Definitions[0].Repeat = &geometry.Repeat{Count: 3, Offset: []float64{0, 0, 40}}
	want := doc.Expanded().Parts
	cases := []struct {
		path string
		want map[string]bool
	}{
		{"front-left", placedBy(doc, "car", "front-left")},
		{"front-right", placedBy(doc, "car", "front-right")},
		{"lamp", placedBy(doc, "car", "lamp")},
		{"front-right/damper", both(placedBy(doc, "car", "front-right"), placedBy(doc, "corner", "damper"))},
		{"front-left/pin", both(placedBy(doc, "car", "front-left"), placedBy(doc, "corner", "pin"))},
		{"front-left-2", nil},
		{"front-left-1", nil},
		{"front-left-3", nil},
	}
	var paths []string
	for _, tc := range cases {
		paths = append(paths, tc.path)
	}
	runs := driveViewport(t, map[string]any{"spec": doc, "paths": paths, "rows": []map[string]string{
		{"ref": "car", "path": ""}, {"ref": "corner", "path": "front-left-2"}}})

	for _, mode := range glModes {
		run := runs[mode]
		slots := map[string]bool{}
		for i, tc := range cases {
			sel, iso := run.Selected[i], run.Isolated[i]
			noProblems(t, mode+" select "+tc.path, sel)
			noProblems(t, mode+" isolate "+tc.path, iso)
			lit := map[string]bool{}
			all := sel.instances()
			for j, id := range whose(t, mode+" "+tc.path, all, want, true) {
				if all[j].Highlight == 1 {
					lit[id] = true
				}
			}
			shown := map[string]bool{}
			for _, id := range whose(t, mode+" isolate "+tc.path, iso.instances(), want, false) {
				shown[id] = true
			}
			if len(lit) == 0 || strings.Join(keys(lit), ",") != strings.Join(keys(shown), ",") {
				t.Fatalf("%s: %s lights %v and isolates %v", mode, tc.path, keys(lit), keys(shown))
			}
			if iso.Stats.Hidden+iso.Stats.Instances+iso.Stats.Culled != len(want) || iso.Stats.Instances != len(shown) {
				t.Errorf("%s: isolating %s drew %d and hid %d of %d", mode, tc.path, iso.Stats.Instances, iso.Stats.Hidden, len(want))
			}
			if tc.want == nil {
				for id := range lit {
					if slots[id] {
						t.Fatalf("%s: %s is under two copies of front-left", mode, id)
					}
					slots[id] = true
				}
				continue
			}
			if strings.Join(keys(lit), ",") != strings.Join(keys(tc.want), ",") {
				t.Fatalf("%s: selecting %s lights\n  %v\nGo places under it\n  %v", mode, tc.path, keys(lit), keys(tc.want))
			}
		}
		if strings.Join(keys(slots), ",") != strings.Join(keys(cases[0].want), ",") {
			t.Errorf("%s: the three copies of front-left reach %v together, and front-left reaches %v",
				mode, keys(slots), keys(cases[0].want))
		}

		top, inside := run.Rows[0], run.Rows[1]
		if len(top) != 4 || top[0].Path != "front-left" || strings.Join(top[0].Slots, ",") != "front-left-1,front-left-2,front-left-3" ||
			top[1].Path != "front-right" || top[1].Slots != nil || top[3].Path != "trim" || len(top[3].Slots) != 5 {
			t.Errorf("%s: the rows under the car are %+v", mode, top)
		}
		if len(inside) != 2 || inside[0].Path != "front-left-2/damper" || inside[1].Path != "front-left-2/pin" ||
			len(inside[1].Slots) != 5 {
			t.Errorf("%s: the rows under front-left-2 are %+v", mode, inside)
		}
	}
}
