package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// The rest of stage W2: a hierarchy to cull through, a level between the box and the
// whole mesh, a search index, and geometry loaded a subtree at a time. Phase 6.
//
// # What each fence holds, and why against the thing it replaced
//
// Each of these makes the viewport do LESS work for the same picture — test fewer
// spheres, draw fewer triangles, read fewer names, upload fewer copies — and the way
// each goes wrong is by doing less of the picture too: a copy culled that was on
// screen, a search that misses a part, a subtree drawn with parts missing. So each is
// held to the slow path it replaces, which the studio keeps (hierarchy = false,
// lod = 0, scanOccurrences) or to Go's own answer (the subtree reply), and separately
// to doing less work, so a "faster" version that quietly does the old work fails too.

// runNodeHarness drives the shipped forge3d.js through scripts/webgl-stub.js with a
// harness of its own and returns what it printed.
func runNodeHarness(t *testing.T, script string, input any) []byte {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node on PATH; skipping the viewport comparison")
	}
	src, err := assetFS.ReadFile("assets/forge3d.js")
	if err != nil {
		t.Fatal(err)
	}
	stub, err := filepath.Abs(filepath.Join("..", "..", "scripts", "webgl-stub.js"))
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	asset, harness, spec := filepath.Join(dir, "forge3d.js"), filepath.Join(dir, "harness.js"), filepath.Join(dir, "input.json")
	for path, data := range map[string][]byte{asset: src, harness: []byte(script), spec: body} {
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
		t.Fatalf("forge3d.js lacks what this fence drives: %s", out)
	}
	return out
}

// yard is a few thousand copies of four shapes — a box, a sphere that simplifies, a
// mirrored cylinder and a translucent lamp — laid out on a grid wide enough that most
// cameras see part of it.
func yard() geometry.Document {
	return geometry.Document{Name: "yard", Units: "mm", Root: "yard",
		Definitions: []geometry.Part{
			{ID: "crate", Shape: "box", Size: map[string]float64{"width": 60, "height": 40, "depth": 80}, Color: "#aa7733", Opacity: 1},
			{ID: "buoy", Shape: "sphere", Size: map[string]float64{"radius": 25}, Color: "#cc3333", Opacity: 1},
			{ID: "post", Shape: "cylinder", Size: map[string]float64{"radius": 8, "height": 120}, Rotation: []float64{0, 0, 30},
				Color: "#3377aa", Opacity: 1},
			{ID: "lamp", Shape: "sphere", Size: map[string]float64{"radius": 10}, Color: "#ffee99", Opacity: 0.5},
		},
		Assemblies: []geometry.Assembly{
			{ID: "yard", Children: []geometry.Child{
				// ‼️ 20 × 20: a pattern places at most 512 copies, and a larger one places none.
				{ID: "bay", Ref: "bay", Pattern: &geometry.Pattern{Kind: "grid", Rows: 20, Columns: 20,
					RowOffset: []float64{0, 0, 300}, ColumnOffset: []float64{300, 20, 0}}},
			}},
			{ID: "bay", Children: []geometry.Child{
				{ID: "crate", Ref: "crate"},
				{ID: "buoy", Ref: "buoy", Position: []float64{0, 90, 0}},
				{ID: "post", Ref: "post", Position: []float64{100, 0, 40}, Mirror: "x"},
				{ID: "lamp", Ref: "lamp", Position: []float64{0, 160, 100}},
			}},
		}}
}

const hierarchyHarness = `
  const fs = require('fs');
  const stub = require(process.argv[2]);
  const F = stub.loadForge3D(process.argv[3]);
  const input = JSON.parse(fs.readFileSync(process.argv[4], 'utf8'));
  if (typeof F.Studio.prototype._visitTree !== 'function') { process.stdout.write('{"missing":true}'); return; }
  const made = stub.makeCanvas('webgl2', 640, 480), record = made.record;
  const studio = new F.Studio(made.canvas, { onError() {} });
  studio.load(input.spec);
  let seed = input.seed;
  const rnd = () => (seed = (seed * 1103515245 + 12345) % 2147483648) / 2147483648;
  const n = studio.batches.reduce((a, b) => a + b.n, 0);
  const nodes = studio.batches.reduce((a, b) => a + (b.tree ? b.tree.nodes : 0), 0);
  function snapshot(hierarchy) {
    studio.hierarchy = hierarchy;
    record.reset();
    studio.draw();
    const f = studio._frame, s = Object.assign({}, studio.stats);
    const seen = [];
    studio.batches.forEach((b, k) => { for (let i = 0; i < b.n; i++) if (b.seen[i] === f.number) seen.push(k + ':' + i); });
    const drawn = [];
    record.draws.forEach((d) => d.instances.forEach((x) => drawn.push(d.elements + '|' + d.frontFace + '|' +
      x.model.map((v) => v.toFixed(3)).join(',') + '|' + x.colour.map((v) => v.toFixed(4)).join(',') + '|' + x.highlight)));
    seen.sort(); drawn.sort();
    const problems = record.problems.slice();
    record.reset();
    return { s, seen: seen.join(' '), drawn: drawn.join(' '), problems };
  }
  const out = { n, nodes, cameras: 0, mismatches: 0, first: null, partial: 0, proxied: 0, simplified: 0,
                exploded: 0, isolated: 0, problems: [] };
  const span = studio.bounds.span, centre = studio.bounds.centre;
  for (let c = 0; c < input.cameras; c++) {
    studio.camera.yaw = rnd() * 2 * Math.PI;
    studio.camera.pitch = rnd() * 3 - 1.5;
    studio.camera.distance = span * (0.02 + rnd() * rnd() * 4);
    studio.camera.target = [0, 1, 2].map((k) => centre[k] + (rnd() - 0.5) * span);
    studio.explode = c % 5 === 4 ? rnd() * 0.5 : 0;
    const iso = c % 7 === 6 ? 'bay-' + (1 + Math.floor(rnd() * 400)) : '';
    studio.isolated = iso ? F.occurrenceMatcher(iso) : null;
    const a = snapshot(true), b = snapshot(false);
    out.cameras++;
    out.problems = out.problems.concat(a.problems, b.problems);
    const same = a.seen === b.seen && a.drawn === b.drawn && a.s.instances === b.s.instances &&
      a.s.proxied === b.s.proxied && a.s.simplified === b.s.simplified &&
      a.s.culled + a.s.hidden === b.s.culled + b.s.hidden;
    if (!same) {
      out.mismatches++;
      if (!out.first) out.first = { camera: c, hierarchy: a.s, flat: b.s, seenSame: a.seen === b.seen, drawnSame: a.drawn === b.drawn };
    }
    if (b.s.culled > 0 && b.s.instances > 0) out.partial++;
    if (b.s.proxied > 0) out.proxied++;
    if (b.s.simplified > 0) out.simplified++;
    if (studio.explode) out.exploded++;
    if (iso) out.isolated++;
  }
  studio.explode = 0; studio.isolated = null;
  studio.resetView();
  studio.camera.distance = span * 300;
  out.far = snapshot(true).s;
  studio.camera.target = [centre[0] + span * 100, centre[1], centre[2]];
  studio.camera.yaw = -Math.PI / 2; studio.camera.pitch = 0; studio.camera.distance = span;
  out.away = snapshot(true).s;
  process.stdout.write(JSON.stringify(out));
`

type cullStats struct {
	Instances, Culled, Hidden, Proxied, Simplified, Visited int
}

// Culling through each batch's hierarchy keeps exactly the copies, at exactly the
// levels, that testing every copy keeps — on randomized cameras, exploded views and
// isolations — and a car seen from far away, or not seen at all, visits a handful of
// nodes rather than every copy. Phase 6, stage W2.
func TestRendererCullsAHierarchyExactlyAsItCullsEachCopy(t *testing.T) {
	doc := yard()
	out := runNodeHarness(t, hierarchyHarness, map[string]any{"spec": doc, "seed": 20260915, "cameras": 160})
	var got struct {
		N, Nodes, Cameras, Mismatches, Partial, Proxied, Simplified, Exploded, Isolated int
		First                                                                           any
		Problems                                                                        []string
		Far, Away                                                                       cullStats
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unreadable renderer output: %v", err)
	}
	if got.N != len(doc.Expanded().Parts) {
		t.Fatalf("the studio loaded %d copies of the %d the exporter places", got.N, len(doc.Expanded().Parts))
	}
	if len(got.Problems) > 0 {
		t.Fatalf("a frame did something a browser refuses: %v", got.Problems)
	}
	if got.Mismatches != 0 {
		t.Fatalf("on %d of %d cameras the hierarchy kept different copies from the per-copy test; first: %+v",
			got.Mismatches, got.Cameras, got.First)
	}
	if got.Partial < 20 || got.Proxied < 5 || got.Simplified < 5 || got.Exploded < 20 || got.Isolated < 10 {
		t.Fatalf("the cameras no longer exercise what they claim: %d partly culled, %d with boxes, %d simplified, "+
			"%d exploded, %d isolated", got.Partial, got.Proxied, got.Simplified, got.Exploded, got.Isolated)
	}
	// Every copy but the crates, which are twelve triangles already and never swapped for
	// their box.
	notBoxes := 0
	for _, p := range doc.Expanded().Parts {
		if p.Shape != "box" {
			notBoxes++
		}
	}
	if got.Far.Instances != got.N || got.Far.Proxied != notBoxes || got.Far.Visited > got.N/50 {
		t.Errorf("from far away %d of %d copies drawn, %d as boxes, visiting %d nodes; want all, the %d that are not boxes, and a handful",
			got.Far.Instances, got.N, got.Far.Proxied, got.Far.Visited, notBoxes)
	}
	if got.Away.Culled != got.N || got.Away.Visited > got.N/50 {
		t.Errorf("looking away, %d of %d copies culled visiting %d nodes; want all of them from a handful",
			got.Away.Culled, got.N, got.Away.Visited)
	}
}

const levelHarness = `
  const fs = require('fs');
  const stub = require(process.argv[2]);
  const F = stub.loadForge3D(process.argv[3]);
  const input = JSON.parse(fs.readFileSync(process.argv[4], 'utf8'));
  const out = {};
  for (const name of Object.keys(input.specs)) {
    const made = stub.makeCanvas('webgl2', 640, 480), record = made.record;
    const studio = new F.Studio(made.canvas, { onError() {} });
    studio.load(input.specs[name]);
    studio.draw();
    const b = studio.batches[0], pixels = studio._frame.pixels;
    if (!('simple' in b)) { process.stdout.write('{"missing":true}'); return; }
    const run = { full: b.count, simple: b.simple ? b.simple.count : 0, inside: true, frames: [] };
    if (b.simple) {
      const p = b.simple.geo.positions;
      for (let i = 0; i < p.length; i++) {
        const k = i % 3;
        if (p[i] < b.bounds.min[k] - 1e-9 || p[i] > b.bounds.max[k] + 1e-9) run.inside = false;
      }
    }
    for (const lod of [3, 0]) {
      studio.lod = lod;
      for (const px of input.pixels) {
        studio.camera.target = b.bounds.centre.slice();
        studio.camera.distance = b.radius[0] * pixels / px;
        record.reset();
        studio.draw();
        run.frames.push({ lod, px, elements: record.draws.map((d) => d.elements), stats: studio.stats, problems: record.problems });
        record.reset();
      }
    }
    out[name] = run;
  }
  process.stdout.write(JSON.stringify(out));
`

// A copy is drawn whole when it is large on screen, as its batch's simplified mesh when
// it is small, and as its box when it is smaller still — decided by its projected radius
// in pixels — and a simplified mesh is less than half the triangles and inside the
// shape's box. A shape too simple to halve goes from whole to box. Phase 6, stage W2.
func TestRendererDrawsACopyAtTheLevelItsProjectedSizeCalls(t *testing.T) {
	one := func(p geometry.Part) geometry.Document {
		p.ID, p.Color, p.Opacity = "it", "#888888", 1
		return geometry.Document{Name: p.Shape, Units: "mm", Parts: []geometry.Part{p}}
	}
	specs := map[string]geometry.Document{
		"sphere": one(geometry.Part{Shape: "sphere", Size: map[string]float64{"radius": 50}}),
		"box":    one(geometry.Part{Shape: "box", Size: map[string]float64{"width": 50, "height": 50, "depth": 50}}),
	}
	pixels := []float64{1, 2.9, 3.1, 10, 23.9, 24.1, 100}
	out := runNodeHarness(t, levelHarness, map[string]any{"specs": specs, "pixels": pixels})
	var got map[string]struct {
		Full, Simple int
		Inside       bool
		Frames       []struct {
			Lod      int
			Px       float64
			Elements []int
			Stats    cullStats
			Problems []string
		}
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unreadable renderer output: %v", err)
	}
	sphere, box := got["sphere"], got["box"]
	if sphere.Simple == 0 || sphere.Simple*2 > sphere.Full || !sphere.Inside {
		t.Fatalf("the sphere's simplified mesh is %d indices of %d, inside its box: %v", sphere.Simple, sphere.Full, sphere.Inside)
	}
	if box.Simple != 0 {
		t.Errorf("a box of twelve triangles was given a simplified level of %d indices", box.Simple)
	}
	for name, run := range got {
		for _, f := range run.Frames {
			if len(f.Problems) > 0 || len(f.Elements) != 1 {
				t.Fatalf("%s at %v px: %d draws, problems %v", name, f.Px, len(f.Elements), f.Problems)
			}
			want, level := run.Full, "whole"
			switch {
			case f.Lod == 0:
			case f.Px < 3 && run.Full > 36:
				want, level = 36, "its box"
			case f.Px < 24 && run.Simple > 0:
				want, level = run.Simple, "simplified"
			}
			if f.Elements[0] != want {
				t.Errorf("%s with lod %d at %v px of radius is drawn with %d indices; want %s (%d)", name, f.Lod, f.Px, f.Elements[0], level, want)
			}
			if (level == "simplified") != (f.Stats.Simplified == 1) || (level == "its box") != (f.Stats.Proxied == 1) {
				t.Errorf("%s at %v px: stats say %d simplified and %d boxes, drawn %s", name, f.Px, f.Stats.Simplified, f.Stats.Proxied, level)
			}
		}
	}
}

const searchHarness = `
  const fs = require('fs');
  const stub = require(process.argv[2]);
  const F = stub.loadForge3D(process.argv[3]);
  const input = JSON.parse(fs.readFileSync(process.argv[4], 'utf8'));
  const made = stub.makeCanvas('webgl2', 640, 480);
  const studio = new F.Studio(made.canvas, { onError() {} });
  if (typeof studio.scanOccurrences !== 'function') { process.stdout.write('{"missing":true}'); return; }
  studio.load(input.spec);
  let seed = 7;
  const rnd = () => (seed = (seed * 1103515245 + 12345) % 2147483648) / 2147483648;
  const ids = studio.occurrenceIds(), queries = input.queries.slice();
  for (let i = 0; i < 400; i++) {
    const p = studio.parts[Math.floor(rnd() * ids.length)];
    const text = rnd() < 0.8 ? p.id : String(p.spec.name || p.id);
    const at = Math.floor(rnd() * text.length), len = 1 + Math.floor(rnd() * 12);
    let q = text.substr(at, len);
    if (rnd() < 0.3) q = q.toUpperCase();
    if (rnd() < 0.2) q = q + ids[Math.floor(rnd() * ids.length)].substr(0, 3);
    queries.push(q);
  }
  const out = { rows: ids.length, queries: queries.length, mismatches: [], found: 0, selective: null };
  for (const q of queries) {
    const a = JSON.stringify(studio.findOccurrences(q, 50)), b = JSON.stringify(studio.scanOccurrences(q, 50));
    if (a !== b) out.mismatches.push({ q, index: a.slice(0, 200), scan: b.slice(0, 200) });
    if (JSON.parse(b).total > 0) out.found++;
  }
  const hit = studio.findOccurrences(input.selective, 50);
  out.selective = { total: hit.total, stats: studio.searchStats, scan: studio.scanOccurrences(input.selective, 50).total };
  process.stdout.write(JSON.stringify(out));
`

// A search through the index answers exactly what reading every occurrence answers —
// the same parts, in the same order, with the same count — and a selective query reads
// a small share of the occurrences. Phase 6, stage W2.
func TestRendererFindsOccurrencesThroughAnIndexLikeTheScan(t *testing.T) {
	doc := instancedCar()
	doc.Definitions[1].Repeat = &geometry.Repeat{Count: 3, Offset: []float64{0, 0, 12}}
	big := yard()
	doc.Definitions = append(doc.Definitions, big.Definitions...)
	doc.Assemblies = append(doc.Assemblies, big.Assemblies[1])
	doc.Assemblies[0].Children = append(doc.Assemblies[0].Children, geometry.Child{ID: "bay", Ref: "bay", Name: "Bay",
		Pattern: &geometry.Pattern{Kind: "grid", Rows: 20, Columns: 20, RowOffset: []float64{0, 0, 300}, ColumnOffset: []float64{300, 0, 0}}})
	// "pin-5-3-1" has every three-letter run a real id has ("pin-5-3", "pin-3-1") and is
	// no id: an index that answers from its runs without checking the whole query finds it.
	queries := []string{"pin-5-3-1", "front-left-2/pin-3", "PIN", "bay-17/", "Bay 3", "nothing-like-this", "-", "p", "e/l", "bay-400/lamp"}
	out := runNodeHarness(t, searchHarness, map[string]any{"spec": doc, "queries": queries, "selective": "front-left-2/pin-3"})
	var got struct {
		Rows, Queries, Found int
		Mismatches           []map[string]string
		Selective            struct {
			Total, Scan int
			Stats       struct {
				Rows, Examined int
				Indexed        bool
			}
		}
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unreadable renderer output: %v", err)
	}
	if len(got.Mismatches) > 0 {
		t.Fatalf("%d of %d searches answer differently through the index; first: %v", len(got.Mismatches), got.Queries, got.Mismatches[0])
	}
	if got.Rows < 1600 || got.Found < got.Queries/2 {
		t.Fatalf("the searches no longer exercise what they claim: %d rows, %d of %d queries found something", got.Rows, got.Found, got.Queries)
	}
	s := got.Selective
	if s.Total == 0 || s.Total != s.Scan || !s.Stats.Indexed || s.Stats.Examined*20 > s.Stats.Rows {
		t.Errorf("a search for front-left-2/pin-3 found %d (scan %d), read %d of %d occurrences, indexed %v; want a twentieth at most",
			s.Total, s.Scan, s.Stats.Examined, s.Stats.Rows, s.Stats.Indexed)
	}
}

// lazyCar is past the kernel's ceiling because of one patterned row of riveted panels;
// every other row is small.
func lazyCar() geometry.Document {
	cyl := func(id string, r, h float64) geometry.Part {
		return geometry.Part{ID: id, Shape: "cylinder", Size: map[string]float64{"radius": r, "height": h}, Color: "#cccccc", Opacity: 1}
	}
	return geometry.Document{Name: "lazy car", Units: "mm", Root: "car",
		Parts: []geometry.Part{{ID: "frame", Shape: "box", Size: map[string]float64{"width": 3000, "height": 40, "depth": 1400},
			Position: []float64{0, -200, 0}, Color: "#555555", Opacity: 1}},
		Definitions: []geometry.Part{
			cyl("rivet", 2, 6),
			{ID: "skin", Shape: "box", Size: map[string]float64{"width": 1000, "height": 2, "depth": 180}, Color: "#1f5fa8", Opacity: 1},
			{ID: "damper", Shape: "box", Size: map[string]float64{"width": 20, "height": 300, "depth": 20}, Rotation: []float64{0, 0, 10},
				Color: "#e0b040", Opacity: 1},
			{ID: "lens", Shape: "sphere", Size: map[string]float64{"radius": 40}, Color: "#fff3b0", Opacity: 1},
			cyl("pin", 5, 40),
		},
		Assemblies: []geometry.Assembly{
			{ID: "car", Children: []geometry.Child{
				{ID: "front-left", Ref: "corner", Position: []float64{1200, 0, 600}, Mirror: "z",
					Pattern: &geometry.Pattern{Kind: "linear", Count: 2, Offset: []float64{-2400, 0, 0}}},
				{ID: "panel", Ref: "seam", Position: []float64{-500, 400, -600},
					Pattern: &geometry.Pattern{Kind: "linear", Count: 10, Offset: []float64{0, 20, 130}}},
				{ID: "lamp", Ref: "lens", Position: []float64{1500, 300, 400},
					Pattern: &geometry.Pattern{Kind: "grid", Rows: 2, Columns: 3, RowOffset: []float64{0, 0, -800}, ColumnOffset: []float64{0, 100, 0}}},
				{ID: "seat", Ref: "corner", Position: []float64{0, 200, 0}, Rotation: []float64{0, 90, 0}},
			}},
			{ID: "corner", Children: []geometry.Child{
				{ID: "damper", Ref: "damper"},
				{ID: "pin", Ref: "pin", Position: []float64{60, 0, 0}, Pattern: &geometry.Pattern{Kind: "polar", Count: 5, About: "y"}},
			}},
			{ID: "seam", Children: []geometry.Child{
				{ID: "skin", Ref: "skin"},
				{ID: "rivet", Ref: "rivet", Position: []float64{-490, 3, 0}, Pattern: &geometry.Pattern{Kind: "linear", Count: 480, Offset: []float64{2, 0, 0}}},
			}},
		}}
}

const lazyHarness = `
  const fs = require('fs');
  const stub = require(process.argv[2]);
  const F = stub.loadForge3D(process.argv[3]);
  const input = JSON.parse(fs.readFileSync(process.argv[4], 'utf8'));
  const made = stub.makeCanvas('webgl2', 640, 480), record = made.record;
  const studio = new F.Studio(made.canvas, { onError() {} });
  if (typeof studio.loadLazy !== 'function' || typeof F.firstViewPaths !== 'function') {
    process.stdout.write('{"missing":true}'); return;
  }
  studio.lod = 0;
  function frame() {
    record.reset();
    studio.draw();
    const taken = { stats: Object.assign({}, studio.stats), problems: record.problems.slice(),
      draws: record.draws.map((d) => ({ elements: d.elements, instances: d.instances.map((x) => ({ model: x.model, colour: x.colour })) })) };
    record.reset();
    return taken;
  }
  const asked = [];
  const out = { lazily: F.loadsLazily(input.spec), firstView: F.firstViewPaths(input.spec) };
  out.total = studio.loadLazy(input.spec, (p) => asked.push(p));
  out.first = asked.slice();
  out.before = frame();
  out.first.forEach((p) => studio.addSubtree(p, input.replies[p]));
  out.answered = frame();
  asked.length = 0;
  out.open = [studio.requestSubtree('panel'), studio.requestSubtree('panel'), studio.requestSubtree('panel-3'),
              studio.requestSubtree('front-left-2/pin')];
  out.askedOpen = asked.slice();
  out.pending = frame();
  studio.addSubtree('panel', input.replies.panel);
  out.all = frame();
  out.state = studio.lazyState();
  out.ids = studio.occurrenceIds().length;
  studio.isolate(F.occurrenceMatcher('panel-3'));
  out.isolated = frame();
  process.stdout.write(JSON.stringify(out));
`

type lazyFrame struct {
	Stats struct {
		Instances, Placeholders, Culled int
	}
	Problems []string
	Draws    []struct {
		Elements  int
		Instances []struct {
			Model  [16]float64
			Colour [4]float64
		}
	}
}

// placed is every (triangle count, matrix) a frame drew for a part rather than for a
// placeholder box.
func (f lazyFrame) placed() []placedCopy {
	var out []placedCopy
	for _, d := range f.Draws {
		for _, in := range d.Instances {
			if in.Colour[3] < 1 {
				continue
			}
			out = append(out, placedCopy{d.Elements, in.Model})
		}
	}
	return out
}

type placedCopy struct {
	elements int
	matrix   [16]float64
}

// sameCopies reports whether two lists hold the same copies, by triangle count and
// matrix, in any order.
func sameCopies(a, b []placedCopy) bool {
	if len(a) != len(b) {
		return false
	}
	key := func(c placedCopy) float64 {
		return float64(c.elements)*1e7 + c.matrix[12] + c.matrix[13]*3.1 + c.matrix[14]*7.3
	}
	sort.Slice(a, func(i, j int) bool { return key(a[i]) < key(a[j]) })
	sort.Slice(b, func(i, j int) bool { return key(b[i]) < key(b[j]) })
	used := make([]bool, len(b))
	for _, x := range a {
		found := false
		for j, y := range b {
			if !used[j] && x.elements == y.elements && sameMatrix(x.matrix, y.matrix) {
				used[j], found = true, true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// A design past the kernel's ceiling is listed whole and uploaded a subtree at a time:
// its first view asks for exactly the small top-level rows and draws everything else as
// one box per slot; opening a row asks for exactly that row, once, and nothing already
// covered is asked for again; and what a subtree draws is, copy for copy, what Go's
// subtree reply places there. Phase 6, stage W2.
func TestRendererLoadsASubtreeWhenItIsAskedForAndDrawsWhatGoPlacesThere(t *testing.T) {
	doc := lazyCar()
	expanded := doc.Expanded().Parts
	if doc.DrawRefusal() == "" {
		t.Fatal("the fixture is no longer past the kernel's ceiling, so it would not load lazily")
	}
	// The first view, worked out by Go: the top-level part, then each child the root
	// places whose parts number no more than the kernel builds at once.
	wantFirst := []string{"frame"}
	for _, c := range doc.Assemblies[0].Children {
		if n := len(placedBy(doc, "car", c.ID)); n <= geometry.MaxBuiltParts() {
			wantFirst = append(wantFirst, c.ID)
		}
	}
	replies := map[string]map[string]any{}
	copiesOf := map[string][]placedCopy{}
	for _, path := range append(append([]string(nil), wantFirst...), "panel") {
		body, err := subtreeMesh(context.Background(), nil, doc, "mm", path)
		if err != nil {
			t.Fatalf("Go refused the subtree %s: %v", path, err)
		}
		replies[path] = body
		defs, insts := payloadOf(t, body)
		for _, in := range insts {
			copiesOf[path] = append(copiesOf[path], placedCopy{len(defs[in.Definition].Triangles), in.Matrix})
		}
	}

	out := runNodeHarness(t, lazyHarness, map[string]any{"spec": doc, "replies": replies})
	var got struct {
		Lazily                                   bool
		FirstView, First, AskedOpen              []string
		Total, Ids                               int
		Open                                     []bool
		Before, Answered, Pending, All, Isolated lazyFrame
		State                                    struct {
			Loaded, Pending, Requested []string
			Placeholders               int
		}
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unreadable renderer output: %v", err)
	}
	for name, f := range map[string]lazyFrame{"before": got.Before, "answered": got.Answered, "pending": got.Pending, "all": got.All} {
		noProblems(t, name, drawnFrame{Problems: f.Problems})
	}
	if !got.Lazily || got.Total != len(expanded) || got.Ids != len(expanded) {
		t.Fatalf("loaded lazily %v, listing %d and %d of the %d occurrences Go places", got.Lazily, got.Total, got.Ids, len(expanded))
	}
	if strings.Join(got.First, ",") != strings.Join(wantFirst, ",") || strings.Join(got.FirstView, ",") != strings.Join(wantFirst, ",") {
		t.Fatalf("the first view asks for %v (policy %v); the small rows are %v", got.First, got.FirstView, wantFirst)
	}
	// Before any reply: nothing but one box per top-level slot is uploaded.
	slots := 1 + 2 + 10 + 6 + 1
	if b := got.Before.Stats; b.Instances != b.Placeholders || b.Placeholders != slots || len(got.Before.placed()) != 0 {
		t.Fatalf("before any subtree arrived, %d instances were drawn, %d of them boxes; want only the %d slot boxes",
			b.Instances, b.Placeholders, slots)
	}
	var first []placedCopy
	for _, p := range wantFirst {
		first = append(first, copiesOf[p]...)
	}
	if a := got.Answered; !sameCopies(a.placed(), first) || a.Stats.Placeholders != 10 || a.Stats.Culled != 0 {
		t.Fatalf("after the first view, %d copies drawn and %d boxes; Go's replies place %d, and the ten panels stay boxes",
			len(a.placed()), a.Stats.Placeholders, len(first))
	}
	if len(first) >= len(expanded)/2 {
		t.Fatalf("the first view uploads %d of %d copies, which is not loading lazily", len(first), len(expanded))
	}
	if len(got.Open) != 4 || !got.Open[0] || got.Open[1] || got.Open[2] || got.Open[3] || strings.Join(got.AskedOpen, ",") != "panel" {
		t.Fatalf("opening panel, panel again, panel-3 and front-left-2/pin asked %v (%v); want panel, once", got.AskedOpen, got.Open)
	}
	if len(got.Pending.placed()) != len(first) {
		t.Fatalf("asking for a subtree drew %d copies before it arrived", len(got.Pending.placed())-len(first))
	}
	all := append(append([]placedCopy(nil), first...), copiesOf["panel"]...)
	if a := got.All; !sameCopies(a.placed(), all) || a.Stats.Placeholders != 0 || len(all) != len(expanded) {
		t.Fatalf("with every subtree loaded, %d copies and %d boxes are drawn; Go places %d", len(a.placed()), a.Stats.Placeholders, len(expanded))
	}
	if len(got.State.Pending) != 0 || len(got.State.Loaded) != len(wantFirst)+1 || got.State.Placeholders != 0 {
		t.Errorf("the studio ends with %+v", got.State)
	}
	var panel3 []placedCopy
	for _, c := range copiesOf["panel"] {
		// panel-3 is the third copy along the row: 20 up and 130 across per copy.
		if math.Abs(c.matrix[13]-(400+2*20+3)) < 1 || math.Abs(c.matrix[13]-(400+2*20)) < 1 {
			panel3 = append(panel3, c)
		}
	}
	if len(panel3) != 481 || !sameCopies(got.Isolated.placed(), panel3) {
		t.Errorf("isolating panel-3 draws %d copies; its subtree places %d of 481", len(got.Isolated.placed()), len(panel3))
	}
}

// A stored design written as a tree reaches a reader with "parts": [] rather than null,
// so a client that lists top-level parts, or decides whether there is anything to
// draw, does not stop at it. Found by the W2 acceptance run: the workbench put a
// stored 30,000-part car on nothing and never drew its tree.
func TestVariantDTO_ATreeWithNoTopLevelPartsSendsAnEmptyList(t *testing.T) {
	doc := lazyCar()
	doc.Parts = nil
	body, err := json.Marshal(toVariantDTO(geometry.Variant{Document: doc}))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Document map[string]json.RawMessage `json:"document"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if string(got.Document["parts"]) != "[]" || string(got.Document["root"]) != `"car"` {
		t.Errorf("a tree with no top-level parts is sent with parts %s and root %s; want [] and \"car\"",
			got.Document["parts"], got.Document["root"])
	}
}

// The workbench loads a large stored design lazily, fetches a subtree's mesh when the
// studio asks for one, and asks for one whenever a row is opened, selected or isolated.
// A text check, like TestWorkbenchDrawsTheMeshReplyInstanced: the wiring is a few
// calls, and the realistic regression is one of them going missing. Phase 6, stage W2.
func TestWorkbenchLoadsALargeTreeASubtreeAtATime(t *testing.T) {
	b, err := assetFS.ReadFile("assets/workbench.js")
	if err != nil {
		t.Fatalf("reading workbench.js: %v", err)
	}
	js := codeOnly(string(b))
	for _, want := range []string{"studio.loadLazy(proto,", "'/mesh?subtree='", "studio.addSubtree(path, b)", "studio.failSubtree(path"} {
		if !strings.Contains(js, want) {
			t.Errorf("workbench.js has no %s, so a large design is not loaded a subtree at a time", want)
		}
	}
	// A stored tree has no top-level parts; restoring only documents with parts left
	// every stored tree off the stage (found by the W2 acceptance run).
	if restore := strings.Index(js, "function drawRestoredVariant"); restore < 0 ||
		!strings.Contains(js[restore:restore+strings.Index(js[restore:], "loadPrototype(")], "pick.document.root") {
		t.Error("the workbench restores a stored variant only when it has top-level parts, so a design written as a tree is never drawn on reload")
	}
	start := strings.Index(js, "function initTree")
	if start < 0 {
		t.Fatal("workbench.js has no initTree")
	}
	body := js[start:]
	if end := strings.Index(body[1:], "\n  function "); end > 0 {
		body = body[:end+1]
	}
	for _, branch := range []struct{ from, to, what string }{
		{"getAttribute('data-toggle')", "getAttribute('data-select')", "opening"},
		{"getAttribute('data-select')", "getAttribute('data-isolate')", "selecting"},
		{"getAttribute('data-isolate')", "renderTree();", "isolating"},
	} {
		i := strings.Index(body, branch.from)
		j := strings.Index(body[i+1:], branch.to)
		if i < 0 || j < 0 || !strings.Contains(body[i:i+1+j], "studio.requestSubtree(") {
			t.Errorf("%s a tree row does not ask the studio for that row's subtree", branch.what)
		}
	}
}
