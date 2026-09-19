package httpapi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/cad"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// Mesh-only decorative parts on the wire and on screen (stage E1 of the "looks
// designed" work; damon's decision, 2026-09-18). See geometry/lattice.go.

const meshOnlyHarness = `
  const fs = require('fs');
  const input = JSON.parse(fs.readFileSync(process.argv[4], 'utf8'));
` + liftFunctions + `
  const src = input.workbench;
  function element() {
    const cls = new Set(['provenance', 'hidden']);
    return { innerHTML: '', listeners: {}, addEventListener(type, f) { this.listeners[type] = f; },
      classList: { add: (c) => cls.add(c), remove: (c) => cls.delete(c), contains: (c) => cls.has(c) } };
  }
  async function run(proto, reply) {
    const el = element(), state = { prototype: proto, recalled: [] };
    let loaded = null;
    const studio = { approximationNotes: () => [], occurrenceIds: () => (proto.parts || []).map((p) => p.id),
      load: (p, b) => { loaded = b; }, setOverlays: () => {} };
    const fetch = () => Promise.resolve({ ok: !!reply, json: () => Promise.resolve(reply) });
    const fns = new Function('$', 'state', 'studio', 'standardsOf', 'figuresOf', 'fetch',
      ['esc', 'meshOnlyLabel', 'meshOnlyNames', 'renderProvenance', 'refineWithBuiltSolid'].map((n) => lift(src, n)).join('\n') +
      '; return { renderProvenance: renderProvenance, refineWithBuiltSolid: refineWithBuiltSolid };')(
      (id) => (id === 'provenance' ? el : null), state, studio, () => [], () => [], fetch);
    fns.renderProvenance();
    const before = visible(el.innerHTML.replace(/<div class="prov-details[\s\S]*$/, '</div>'));
    fns.refineWithBuiltSolid('v1', proto);
    for (let i = 0; i < 10; i++) await Promise.resolve();
    const after = visible(el.innerHTML.replace(/<div class="prov-details[\s\S]*$/, '</div>'));
    return { before: before, after: after, flagged: loaded ? (loaded.parts || []).filter((p) => p.mesh_only).map((p) => p.id) : null,
             kept: state.builtSolid ? state.builtSolid.meshOnly : null };
  }
  (async () => {
    const out = {};
    for (const k of Object.keys(input.cases)) out[k] = await run(input.cases[k].proto, input.cases[k].reply);
    process.stdout.write(JSON.stringify(out));
  })();
`

// The workbench labels every mesh-only part "mesh-only - not manufacturable" on the
// banner's HEADLINE, outside the fold (PRD VIS-06): from the document before any
// kernel answers, and from the kernel's reply, whose mesh_only flag reaches the
// renderer with each part's surface.
func TestWorkbenchLabelsMeshOnlyPartsOutsideTheFold(t *testing.T) {
	js, err := assetFS.ReadFile("assets/workbench.js")
	if err != nil {
		t.Fatal(err)
	}
	lattice := map[string]any{"id": "infill", "name": "Infill", "shape": "lattice", "lattice": "gyroid"}
	block := map[string]any{"id": "block", "name": "Block", "shape": "box"}
	reply := map[string]any{
		"parts": []any{
			map[string]any{"id": "block", "label": "Block", "vertices": []float64{0, 0, 0}, "triangles": []int{}},
			map[string]any{"id": "infill", "label": "Infill", "vertices": []float64{0, 0, 0}, "triangles": []int{}, "mesh_only": true},
		},
		"mesh_only": []string{"Infill"}, "mesh_only_label": geometry.MeshOnlyLabel,
	}
	cases := map[string]any{
		"lattice": map[string]any{"proto": map[string]any{"parts": []any{block, lattice}}, "reply": reply},
		"plain": map[string]any{"proto": map[string]any{"parts": []any{block}}, "reply": map[string]any{
			"parts": []any{map[string]any{"id": "block", "label": "Block", "vertices": []float64{}, "triangles": []int{}}}}},
	}
	out := runNodeHarness(t, meshOnlyHarness, map[string]any{"workbench": string(js), "cases": cases})
	var got map[string]struct {
		Before, After string
		Flagged, Kept []string
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unreadable harness output: %s (%v)", out, err)
	}
	want := geometry.MeshOnlyLabel + ": Infill"
	l := got["lattice"]
	if !strings.Contains(l.Before, want) {
		t.Errorf("before the kernel answers, the folded banner reads %q; want %q on it", l.Before, want)
	}
	if !strings.Contains(l.After, want) {
		t.Errorf("after the kernel answers, the folded banner reads %q; want %q on it", l.After, want)
	}
	if len(l.Flagged) != 1 || l.Flagged[0] != "infill" {
		t.Errorf("the renderer was handed mesh_only on %v; want [infill]", l.Flagged)
	}
	if len(l.Kept) != 1 || l.Kept[0] != "Infill" {
		t.Errorf("the workbench kept %v as the kernel's mesh-only parts", l.Kept)
	}
	p := got["plain"]
	if strings.Contains(p.Before+p.After, "mesh-only") {
		t.Errorf("a design with no mesh-only part is labelled: %q / %q", p.Before, p.After)
	}
}

// One spelling: the workbench's label and shape list are geometry's.
func TestTheWorkbenchSpellsMeshOnlyAsGeometryDoes(t *testing.T) {
	js, err := assetFS.ReadFile("assets/workbench.js")
	if err != nil {
		t.Fatal(err)
	}
	code := codeOnly(string(js))
	if !strings.Contains(code, "function meshOnlyLabel() { return '"+geometry.MeshOnlyLabel+"'; }") {
		t.Errorf("workbench.js does not spell the label %q", geometry.MeshOnlyLabel)
	}
	b, _ := json.Marshal(geometry.MeshOnlyShapes())
	if !strings.Contains(code, "var MESH_ONLY_SHAPES = "+strings.ReplaceAll(string(b), `"`, "'")+";") {
		t.Errorf("workbench.js does not list the mesh-only shapes %s", b)
	}
}

// The mesh reply marks a mesh-only surface, and only that one.
func TestTheMeshReplyMarksAMeshOnlyPart(t *testing.T) {
	parts, _, _ := meshPayload(&cad.Build{Mesh: []cad.MeshPart{
		{ID: "block", Label: "Block"}, {ID: "infill", Label: "Infill", MeshOnly: true}}})
	b, _ := json.Marshal(parts)
	if strings.Count(string(b), `"mesh_only":true`) != 1 || !strings.Contains(string(b), `"id":"infill","label":"Infill","vertices":null,"triangles":null,"mesh_only":true`) {
		t.Errorf("mesh parts on the wire: %s", b)
	}
}

// The STEP download's header and the mass reply say what they left out.
func TestSTEPAndMassRepliesSayMeshOnlyPartsAreLeftOut(t *testing.T) {
	label := stepExportLabel("v1", &cad.Build{MeshOnly: []string{"Infill"}})
	if !strings.HasPrefix(label, "1 mesh-only part(s) are NOT in this file ("+geometry.MeshOnlyLabel+"); ") {
		t.Errorf("STEP header label %q", label)
	}
	if strings.Contains(stepExportLabel("v1", &cad.Build{}), "mesh-only") {
		t.Error("a STEP file with no mesh-only part is labelled as leaving one out")
	}
	body := massBody("v1", geometry.MassReport{Basis: geometry.MassByDensity, MeshOnly: []string{"Infill"}}, nil)
	if note, _ := body["note"].(string); !strings.Contains(note, "1 mesh-only part(s) are left out of the mass, volume and centre: Infill") {
		t.Errorf("mass note %q", note)
	}
}
