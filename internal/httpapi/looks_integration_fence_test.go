package httpapi

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// Looks integration (2026-09-19): the viewport reads what PRs 156 and 158 put on the
// mesh reply — mesh_only parts and the kernel's own normals — and the workbench says
// which rounds the kernel built smaller (feature_reductions).

const kernelNormalsHarness = `
  const fs = require('fs');
  const stub = require(process.argv[2]);
  const F = stub.loadForge3D(process.argv[3]);
  const input = JSON.parse(fs.readFileSync(process.argv[4], 'utf8'));
  const made = stub.makeCanvas('webgl2', 640, 480);
  const studio = new F.Studio(made.canvas, { onError() {} });
  studio.load(input.spec, input.built);
  studio.draw();
  const out = {};
  for (const b of studio.batches) {
    out[b.ids[0]] = { normals: Array.from(b.geo.normals), fromKernel: !!b.fromKernel };
  }
  process.stdout.write(JSON.stringify(out));
`

// A kernel mesh is shaded with the normals the kernel sent (PR 158: one per vertex from
// OCCT's surface, so a cylinder's side is smooth and its rims stay hard), renormalised
// because the wire rounds them; a reply without them, or with a count that does not
// match its vertices, is shaded from its triangles as before. The fixture's triangle
// lies in the XZ plane, so its facet normal is -Y and the kernel's (1, 1, 0) is told
// apart from it.
func TestRendererShadesAKernelMeshWithItsOwnNormals(t *testing.T) {
	tri := []float64{0, 0, 0, 1, 0, 0, 0, 0, 1}
	given := []float64{2, 2, 0, 2, 2, 0, 0, 0, 3}
	identity := []float64{1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1}
	turned := []float64{0, 1, 0, 0, -1, 0, 0, 0, 0, 0, 1, 0, 50, 0, 0, 1}
	built := map[string]any{
		"parts": []any{map[string]any{"id": "plate", "label": "Plate", "vertices": tri, "triangles": []int{0, 1, 2},
			"normals": given}},
		"definitions": []any{
			map[string]any{"vertices": tri, "triangles": []int{0, 1, 2}, "normals": given},
			map[string]any{"vertices": tri, "triangles": []int{0, 1, 2}},
			map[string]any{"vertices": tri, "triangles": []int{0, 1, 2}, "normals": []float64{0, 1, 0}},
		},
		"instances": []any{
			map[string]any{"id": "bolt-1", "label": "Bolt", "definition": 0, "matrix": turned},
			map[string]any{"id": "nut-1", "label": "Nut", "definition": 1, "matrix": identity},
			map[string]any{"id": "pin-1", "label": "Pin", "definition": 2, "matrix": identity},
		},
		"angular": 0.1,
	}
	box := func(id string, x float64) geometry.Part {
		return geometry.Part{ID: id, Shape: "box", Size: map[string]float64{"width": 5, "height": 5, "depth": 5},
			Position: []float64{x, 0, 0}, Color: "#888888", Opacity: 1}
	}
	doc := geometry.Document{Name: "normals", Units: "mm",
		Parts: []geometry.Part{box("plate", 0), box("bolt-1", 50), box("nut-1", 100), box("pin-1", 150)}}
	out := runNodeHarness(t, kernelNormalsHarness, map[string]any{"spec": doc, "built": built})
	var got map[string]struct {
		Normals    []float64
		FromKernel bool
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unreadable harness output: %s (%v)", out, err)
	}
	r := 1 / math.Sqrt2
	own := []float64{r, r, 0, r, r, 0, 0, 0, 1}
	facet := []float64{0, -1, 0, 0, -1, 0, 0, -1, 0}
	for id, want := range map[string][]float64{"plate": own, "bolt-1": own, "nut-1": facet, "pin-1": facet} {
		g, ok := got[id]
		if !ok || !g.FromKernel {
			t.Fatalf("%s is not drawn from the kernel's mesh: %s", id, out)
		}
		if len(g.Normals) != len(want) {
			t.Fatalf("%s: %d normals for 3 vertices", id, len(g.Normals))
		}
		for i := range want {
			if math.Abs(g.Normals[i]-want[i]) > 1e-6 {
				t.Errorf("%s is shaded with normals %v; want %v", id, g.Normals, want)
				break
			}
		}
	}
}

const meshOnlyViewHarness = `
  const fs = require('fs');
  const stub = require(process.argv[2]);
  const F = stub.loadForge3D(process.argv[3]);
  const input = JSON.parse(fs.readFileSync(process.argv[4], 'utf8'));
  const out = {};
  for (const name of Object.keys(input.cases)) {
    const c = input.cases[name];
    const made = stub.makeCanvas('webgl2', 640, 480), record = made.record;
    const layer = { innerHTML: '', children: [] };
    const studio = new F.Studio(made.canvas, { onError() {}, labels: layer });
    if (typeof studio.meshOnlyTags !== 'function') { process.stdout.write('{"missing":true}'); return; }
    studio.load(c.spec, c.built || null);
    studio.showOverlays = false;
    record.reset();
    studio.draw();
    out[name] = {
      batches: studio.batches.map((b) => ({ ids: Array.from(b.ids), meshOnly: !!b.meshOnly, ghost: !!b.ghost,
        shading: Array.from(b.shading), opacity: Array.from(b.opacity), elements: b.count,
        fromKernel: !!b.fromKernel })),
      draws: record.draws,
      tags: studio.meshOnlyTags(),
      layer: layer.innerHTML
    };
  }
  process.stdout.write(JSON.stringify(out));
`

// A declared mesh-only part (PR 156) is drawn so it cannot be mistaken for a solid (PRD
// VIS-06): with the kernel's surface, in the mesh-only tint and material and
// translucent; with none, as a ghost of its box — never a solid box, which is what the
// renderer drew for an unknown shape before. Either way it carries an in-scene tag with
// the reply's mesh_only_label, anchored at its centre and shown with the overlays off.
// Solid parts beside it are untouched.
func TestRendererDrawsMeshOnlyPartsAsMeshOnly(t *testing.T) {
	block := geometry.Part{ID: "block", Name: "Block", Shape: "box", Size: map[string]float64{"width": 10, "height": 10, "depth": 10},
		Position: []float64{-100, 0, 0}, Color: "#aa3322", Opacity: 1, Material: &geometry.Material{Finish: "metal"}}
	infill := geometry.Part{ID: "infill", Name: "Infill", Shape: "lattice", Lattice: "gyroid",
		Size:     map[string]float64{"width": 60, "height": 40, "depth": 30, "cell": 15, "thickness": 2},
		Position: []float64{40, 20, 0}, Color: "#aa3322", Opacity: 1, Material: &geometry.Material{Finish: "metal"}}
	doc := geometry.Document{Name: "infill", Units: "mm", Parts: []geometry.Part{block, infill}}
	cube := []float64{-5, -5, -5, 5, -5, -5, 5, 5, -5, -5, 5, -5, -5, -5, 5, 5, -5, 5, 5, 5, 5, -5, 5, 5}
	faces := []int{0, 2, 1, 0, 3, 2, 4, 5, 6, 4, 6, 7, 0, 1, 5, 0, 5, 4, 3, 6, 2, 3, 7, 6, 0, 4, 7, 0, 7, 3, 1, 2, 6, 1, 6, 5}
	lattice := make([]float64, len(cube))
	for i := range cube {
		lattice[i] = cube[i]*3 + []float64{40, 20, 0}[i%3]
	}
	built := map[string]any{
		"parts": []any{
			map[string]any{"id": "block", "label": "Block", "vertices": cube, "triangles": faces},
			map[string]any{"id": "infill", "label": "Infill", "vertices": lattice, "triangles": faces, "mesh_only": true},
		},
		"mesh_only": []string{"Infill"}, "mesh_only_label": geometry.MeshOnlyLabel,
	}
	out := runNodeHarness(t, meshOnlyViewHarness, map[string]any{"cases": map[string]any{
		"kernel": map[string]any{"spec": doc, "built": built},
		"none":   map[string]any{"spec": doc},
	}})
	type batch struct {
		IDs              []string
		MeshOnly, Ghost  bool
		Shading, Opacity []float64
		Elements         int
		FromKernel       bool
	}
	var got map[string]struct {
		Batches []batch
		Draws   []drawCall
		Tags    []struct {
			ID, Name, Text string
			Ghost          bool
			At             []float64
		}
		Layer string
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unreadable harness output: %s (%v)", out, err)
	}
	tint := [3]float64{0x39 / 255.0, 0xc6 / 255.0, 0xc0 / 255.0}
	solid := [3]float64{0xaa / 255.0, 0x33 / 255.0, 0x22 / 255.0}
	for name, ghost := range map[string]bool{"kernel": false, "none": true} {
		run := got[name]
		var mo, blk *batch
		for i := range run.Batches {
			switch run.Batches[i].IDs[0] {
			case "infill":
				mo = &run.Batches[i]
			case "block":
				blk = &run.Batches[i]
			}
		}
		if mo == nil || blk == nil {
			t.Fatalf("%s: batches %+v", name, run.Batches)
		}
		if !mo.MeshOnly || mo.Ghost != ghost || mo.FromKernel == ghost {
			t.Errorf("%s: the lattice is drawn as %+v; want mesh-only, ghost=%v", name, *mo, ghost)
		}
		if blk.MeshOnly {
			t.Errorf("%s: the solid block is drawn as mesh-only", name)
		}
		if len(mo.Shading) == 3 && len(blk.Shading) == 3 && mo.Shading[0] == blk.Shading[0] &&
			mo.Shading[1] == blk.Shading[1] && mo.Shading[2] == blk.Shading[2] {
			t.Errorf("%s: the lattice shares the solid part's material %v", name, blk.Shading)
		}
		limit := 0.6
		if ghost {
			limit = 0.2
		}
		if len(mo.Opacity) != 1 || mo.Opacity[0] > limit+1e-6 || mo.Opacity[0] <= 0 {
			t.Errorf("%s: the lattice is drawn at opacity %v; want at most %g (never opaque like a solid)", name, mo.Opacity, limit)
		}
		if ghost && mo.Elements != 36 {
			t.Errorf("with no kernel the lattice is %d indices; its ghost is its box, 36", mo.Elements)
		}
		// What reached the GPU: the lattice's copy in the tint, the block in its own colour.
		var sawTint, sawSolid bool
		for _, d := range run.Draws {
			for _, in := range d.Instances {
				c := [3]float64{in.Colour[0], in.Colour[1], in.Colour[2]}
				if near3(c, tint) {
					sawTint = true
					if in.Colour[3] > limit+1e-6 {
						t.Errorf("%s: the lattice reached the GPU at alpha %g", name, in.Colour[3])
					}
				}
				if near3(c, solid) {
					sawSolid = true
				}
			}
		}
		if !sawTint || !sawSolid {
			t.Errorf("%s: drawn in the tint %v, the block's colour %v; draws %+v", name, sawTint, sawSolid, run.Draws)
		}
		if len(run.Tags) != 1 || run.Tags[0].ID != "infill" || run.Tags[0].Text != geometry.MeshOnlyLabel ||
			run.Tags[0].Ghost != ghost || len(run.Tags[0].At) != 3 ||
			math.Abs(run.Tags[0].At[0]-40) > 1e-6 || math.Abs(run.Tags[0].At[1]-20) > 1e-6 {
			t.Errorf("%s: tags %+v; want one reading %q at the lattice's centre (40, 20, 0)", name, run.Tags, geometry.MeshOnlyLabel)
		}
		if !strings.Contains(run.Layer, `class="mesh-only-tag"`) || !strings.Contains(run.Layer, geometry.MeshOnlyLabel) {
			t.Errorf("%s: with the overlays off the label layer reads %q; the tag is not an overlay", name, run.Layer)
		}
	}
}

func near3(a, b [3]float64) bool {
	return math.Abs(a[0]-b[0]) < 1e-3 && math.Abs(a[1]-b[1]) < 1e-3 && math.Abs(a[2]-b[2]) < 1e-3
}

// One spelling: the renderer's mesh-only label and shapes are geometry's.
func TestRendererSpellsMeshOnlyAsGeometryDoes(t *testing.T) {
	js, err := assetFS.ReadFile("assets/forge3d.js")
	if err != nil {
		t.Fatal(err)
	}
	code := codeOnly(string(js))
	if !strings.Contains(code, "var MESH_ONLY_LABEL = '"+geometry.MeshOnlyLabel+"';") {
		t.Errorf("forge3d.js does not spell the label %q", geometry.MeshOnlyLabel)
	}
	b, _ := json.Marshal(geometry.MeshOnlyShapes())
	if !strings.Contains(code, "var MESH_ONLY_SHAPES = "+strings.ReplaceAll(string(b), `"`, "'")+";") {
		t.Errorf("forge3d.js does not list the mesh-only shapes %s", b)
	}
}

const reducedHarness = `
  const fs = require('fs');
  const input = JSON.parse(fs.readFileSync(process.argv[4], 'utf8'));
` + liftFunctions + `
  const src = input.workbench;
  const cls = new Set(['provenance', 'hidden']);
  const el = { innerHTML: '', addEventListener() {},
    classList: { add: (c) => cls.add(c), remove: (c) => cls.delete(c), contains: (c) => cls.has(c) } };
  const proto = { parts: [{ id: 'plate', name: 'Plate', shape: 'box' }] };
  const state = { prototype: proto, recalled: [] };
  const studio = { approximationNotes: () => [], occurrenceIds: () => ['plate'], load: () => {}, setOverlays: () => {} };
  const fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve(input.reply) });
  const fns = new Function('$', 'state', 'studio', 'standardsOf', 'figuresOf', 'fetch',
    ['esc', 'meshOnlyLabel', 'meshOnlyNames', 'renderProvenance', 'refineWithBuiltSolid'].map((n) => lift(src, n)).join('\n') +
    '; return { renderProvenance: renderProvenance, refineWithBuiltSolid: refineWithBuiltSolid };')(
    (id) => (id === 'provenance' ? el : null), state, studio, () => [], () => [], fetch);
  (async () => {
    fns.refineWithBuiltSolid('v1', proto);
    for (let i = 0; i < 10; i++) await Promise.resolve();
    const head = visible(el.innerHTML.replace(/<div class="prov-details[\s\S]*$/, '</div>'));
    process.stdout.write(JSON.stringify({ head: head, all: visible(el.innerHTML) }));
  })();
`

// A round the kernel built smaller than asked (PR 158's feature_reductions) is on the
// banner's headline as a count — outside the fold, where it cannot be missed — and by
// name in the details, and on the STEP export panel beside the features it could not
// apply.
func TestWorkbenchSaysWhichRoundsTheKernelBuiltSmaller(t *testing.T) {
	js, err := assetFS.ReadFile("assets/workbench.js")
	if err != nil {
		t.Fatal(err)
	}
	reduced := "fillet corners on Plate: 1 edge group built at 22.5 (asked 45)"
	reply := map[string]any{"parts": []any{map[string]any{"id": "plate", "label": "Plate",
		"vertices": []float64{0, 0, 0}, "triangles": []int{}}}, "feature_reductions": []string{reduced}}
	out := runNodeHarness(t, reducedHarness, map[string]any{"workbench": string(js), "reply": reply})
	var got struct{ Head, All string }
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unreadable harness output: %s (%v)", out, err)
	}
	if !strings.Contains(got.Head, "1 round(s) built smaller than asked") {
		t.Errorf("the folded banner reads %q; a round built smaller is not on it", got.Head)
	}
	if !strings.Contains(got.All, reduced) {
		t.Errorf("the banner's details do not name the round: %q", got.All)
	}
	code := codeOnly(string(js))
	if !strings.Contains(code, "var reductions = exp.feature_reductions || [];") ||
		!strings.Contains(code, "html += section(reductions.length + ' fillet(s) or chamfer(s) were built SMALLER") {
		t.Error("the STEP export panel does not say which rounds were built smaller")
	}
}
