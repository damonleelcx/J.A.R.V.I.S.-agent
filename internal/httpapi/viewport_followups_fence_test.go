package httpapi

import (
	"context"
	"encoding/json"
	"math"
	"regexp"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// Three follow-ups from the W2 acceptance run (docs/spikes/2026-09-15-subtree-loading),
// taken with the kernel build ceiling measurement (docs/spikes/2026-09-15-kernel-build-ceiling):
//
//   - a design in inches or metres loaded a subtree at a time was never looked at, and
//     its mesh replies are in millimetres;
//   - a search over a car listed "Rivet 1" a hundred and twenty-six times, with nothing
//     to say which seam each was in;
//   - the provenance banner covered most of an 800-px stage.
//
// The workbench fences run the workbench's own functions in node, lifted out of
// workbench.js by name, against a stub element: the page has no test DOM, and a text
// check can be satisfied by code that renders nothing.

// unitRig is small, in the given unit, and has what a unit error would move: a top-level
// part, a turned definition, a mirrored copy of a sub-assembly and patterned copies.
func unitRig(units string) geometry.Document {
	box := func(id string, w, h, d float64, extra func(*geometry.Part)) geometry.Part {
		p := geometry.Part{ID: id, Shape: "box", Size: map[string]float64{"width": w, "height": h, "depth": d},
			Color: "#8899aa", Opacity: 1}
		if extra != nil {
			extra(&p)
		}
		return p
	}
	return geometry.Document{Name: "unit rig", Units: units, Root: "rig",
		NotVerified: []string{"a fence fixture"},
		Parts:       []geometry.Part{box("base", 40, 1, 20, func(p *geometry.Part) { p.Position = []float64{0, -2, 0} })},
		Definitions: []geometry.Part{
			box("plate", 10, 0.5, 6, func(p *geometry.Part) { p.Rotation = []float64{0, 30, 0} }),
			box("bolt", 0.4, 2, 0.4, nil),
		},
		Assemblies: []geometry.Assembly{
			{ID: "rig", Children: []geometry.Child{
				{ID: "left", Ref: "bracket", Position: []float64{-8, 1, 3}},
				{ID: "right", Ref: "bracket", Position: []float64{8, 1, 3}, Mirror: "z"},
				{ID: "row", Ref: "bolt", Position: []float64{-15, 0, -6},
					Pattern: &geometry.Pattern{Kind: "linear", Count: 6, Offset: []float64{6, 0, 0}}},
			}},
			{ID: "bracket", Children: []geometry.Child{
				{ID: "plate", Ref: "plate"},
				{ID: "bolt", Ref: "bolt", Position: []float64{2, 1, 1},
					Pattern: &geometry.Pattern{Kind: "grid", Rows: 2, Columns: 2, RowOffset: []float64{0, 0, 3}, ColumnOffset: []float64{3, 0, 0}}},
			}},
		}}
}

const unitHarness = `
  const fs = require('fs');
  const stub = require(process.argv[2]);
  const F = stub.loadForge3D(process.argv[3]);
  const input = JSON.parse(fs.readFileSync(process.argv[4], 'utf8'));
  const make = () => new F.Studio(stub.makeCanvas('webgl2', 640, 480).canvas, { onError() {} });
  // Every drawn instance's box in stage coordinates: its batch's box through its matrix.
  function boxes(studio) {
    const out = {};
    (studio.batches || []).forEach((b) => {
      const box = studio.lazy && b === studio.lazy.placeholder;
      const min = b.bounds.min, max = b.bounds.max;
      for (let i = 0; i < b.n; i++) {
        const m = b.model.subarray(i * 16, i * 16 + 16), lo = [Infinity, Infinity, Infinity], hi = [-Infinity, -Infinity, -Infinity];
        for (let c = 0; c < 8; c++) {
          const p = [c & 1 ? max[0] : min[0], c & 2 ? max[1] : min[1], c & 4 ? max[2] : min[2]];
          for (let k = 0; k < 3; k++) {
            const v = m[k] * p[0] + m[4 + k] * p[1] + m[8 + k] * p[2] + m[12 + k];
            lo[k] = Math.min(lo[k], v); hi[k] = Math.max(hi[k], v);
          }
        }
        out[(box ? 'box:' : '') + b.ids[i]] = { lo: lo, hi: hi, kernel: !!b.fromKernel };
      }
    });
    return out;
  }
  const out = {};
  for (const unit of Object.keys(input.cases)) {
    const c = input.cases[unit];
    const a = make(); a.load(c.spec);
    const w = make(); w.load(c.spec, c.whole);
    const l = make(); l.loadLazy(c.spec, () => {});
    const before = boxes(l);
    l.lazyState().requested.forEach((p) => l.addSubtree(p, c.subtrees[p] || null));
    out[unit] = { primitives: boxes(a), whole: boxes(w), boxes: before, lazy: boxes(l), state: l.lazyState() };
  }
  process.stdout.write(JSON.stringify(out));
`

type stageBox struct {
	Lo, Hi [3]float64
	Kernel bool
}

// A design that is not in millimetres is drawn from a mesh reply exactly where its own
// primitives are — loaded whole with the reply, and loaded a subtree at a time, where each
// subtree also lands inside the box that stood for it. Every reply is in millimetres (the
// kernel's and Go's alike), and the stage, the dimension overlays and those boxes are in
// the document's unit. Before this, an inch design's surface was drawn 25.4 times its boxes.
// docs/bugfix/2026-09-15-mesh-replies-were-drawn-in-millimetres-on-a-stage-in-the-documents-units.md
func TestMeshSubtree_ADesignInInchesOrMetresIsDrawnWhereItsPrimitivesAre(t *testing.T) {
	cases := map[string]any{}
	parts := map[string]int{}
	for _, unit := range []string{"in", "cm", "m", "mm"} {
		doc := unitRig(unit)
		u := geometry.Unit(unit)
		parts[unit] = len(doc.Expanded().Parts)
		whole, definitions, instances := meshPayload(goBuild(doc, u))
		subtrees := map[string]any{}
		for _, path := range []string{"base", "left", "right", "row"} {
			body, err := subtreeMesh(context.Background(), nil, doc, u, path)
			if err != nil {
				t.Fatalf("%s: Go refused the subtree %s: %v", unit, path, err)
			}
			subtrees[path] = body
		}
		cases[unit] = map[string]any{"spec": doc, "subtrees": subtrees,
			"whole": map[string]any{"parts": whole, "definitions": definitions, "instances": instances}}
	}
	out := runNodeHarness(t, unitHarness, map[string]any{"cases": cases})
	var got map[string]struct {
		Primitives, Whole, Boxes, Lazy map[string]stageBox
		State                          struct{ Loaded, Pending, Requested []string }
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unreadable renderer output: %v", err)
	}
	for _, unit := range []string{"in", "cm", "m", "mm"} {
		g := got[unit]
		if len(g.Primitives) != parts[unit] || parts[unit] < 15 {
			t.Fatalf("%s: the primitives draw %d parts; Go places %d", unit, len(g.Primitives), parts[unit])
		}
		if len(g.State.Pending) != 0 || len(g.State.Loaded) != 4 {
			t.Fatalf("%s: the lazy studio asked for %v and ends with %v loaded, %v pending; want base, left, right and row loaded",
				unit, g.State.Requested, g.State.Loaded, g.State.Pending)
		}
		span := 0.0
		for _, b := range g.Primitives {
			for k := 0; k < 3; k++ {
				span = math.Max(span, math.Max(math.Abs(b.Lo[k]), math.Abs(b.Hi[k])))
			}
		}
		tol := span * 1e-4
		for id, want := range g.Primitives {
			for name, drawn := range map[string]map[string]stageBox{"loaded whole": g.Whole, "loaded a subtree at a time": g.Lazy} {
				b, ok := drawn[id]
				if !ok || !b.Kernel {
					t.Fatalf("%s: %s, %s is not drawn from the mesh reply (present %v)", unit, name, id, ok)
				}
				for k := 0; k < 3; k++ {
					if math.Abs(b.Lo[k]-want.Lo[k]) > tol || math.Abs(b.Hi[k]-want.Hi[k]) > tol {
						t.Fatalf("%s: %s, %s is drawn over %v–%v; its primitive is at %v–%v in the document's unit",
							unit, name, id, b.Lo, b.Hi, want.Lo, want.Hi)
					}
				}
			}
			// A box stands for each top-level slot: a copy of a patterned row is its own.
			slot := strings.SplitN(id, "/", 2)[0]
			box, ok := g.Boxes["box:"+slot]
			if !ok {
				t.Fatalf("%s: no box stood for %s before its subtree arrived (boxes %d)", unit, slot, len(g.Boxes))
			}
			b := g.Lazy[id]
			for k := 0; k < 3; k++ {
				if b.Lo[k] < box.Lo[k]-tol || b.Hi[k] > box.Hi[k]+tol {
					t.Fatalf("%s: %s arrived at %v–%v, outside the box that stood for %s (%v–%v)", unit, id, b.Lo, b.Hi, slot, box.Lo, box.Hi)
				}
			}
		}
	}
}

// liftFunctions is the harness's copy of workbench.js's named functions, lifted by
// brace depth. Kept to functions whose strings hold no braces.
const liftFunctions = `
  function lift(src, name) {
    const at = src.indexOf('function ' + name + '(');
    if (at < 0) throw new Error('workbench.js has no function ' + name);
    for (let k = src.indexOf('{', at), depth = 0; k < src.length; k++) {
      if (src[k] === '{') depth++;
      else if (src[k] === '}' && --depth === 0) return src.slice(at, k + 1);
    }
    throw new Error('unbalanced function ' + name);
  }
  const visible = (html) => html.replace(/<[^>]*>/g, ' ').replace(/&amp;/g, '&').replace(/\s+/g, ' ').trim();
`

const searchRowsHarness = `
  const fs = require('fs');
  const stub = require(process.argv[2]);
  const F = stub.loadForge3D(process.argv[3]);
  const input = JSON.parse(fs.readFileSync(process.argv[4], 'utf8'));
` + liftFunctions + `
  const src = input.workbench;
  const made = new Function('state', 'tree', ['esc', 'treeRow', 'searchRows'].map((n) => lift(src, n)).join('\n') +
    '; return { searchRows: searchRows };')({ selectedPart: null }, { isolated: '' });
  const studio = new F.Studio(stub.makeCanvas('webgl2', 640, 480).canvas, { onError() {} });
  studio.load(input.spec);
  const hits = studio.findOccurrences(input.query, 50);
  const html = made.searchRows(hits);
  const rows = html.split('<div class="tnode"').slice(1).map((r) => ({
    path: (r.match(/data-path="([^"]*)"/) || [])[1],
    name: visible((r.match(/<span class="nm"[^>]*>([\s\S]*?)<\/span>/) || [])[1] || ''),
    text: visible('<x' + r) }));
  process.stdout.write(JSON.stringify({ found: hits.found, total: hits.total, rows: rows }));
`

// A search result says where the occurrence is, not only what it is called. The W2 run
// searched a car and read fifty rows of "Rivet 1", "Rivet 10", … — one per seam, and
// nothing on the row to say which seam. The row's visible text now carries the
// occurrence path, so no two rows read alike.
func TestWorkbenchSearchRowsSayWhereEachOccurrenceIs(t *testing.T) {
	js, err := assetFS.ReadFile("assets/workbench.js")
	if err != nil {
		t.Fatal(err)
	}
	rivet := geometry.Part{ID: "rivet", Shape: "cylinder", Size: map[string]float64{"radius": 2, "height": 6}, Color: "#cccccc", Opacity: 1}
	skin := geometry.Part{ID: "skin", Shape: "box", Size: map[string]float64{"width": 600, "height": 2, "depth": 100}, Color: "#1f5fa8", Opacity: 1}
	doc := geometry.Document{Name: "seams", Units: "mm", Root: "body", NotVerified: []string{"a fence fixture"},
		Definitions: []geometry.Part{rivet, skin},
		Assemblies: []geometry.Assembly{
			{ID: "body", Children: []geometry.Child{{ID: "seam", Ref: "seam", Name: "Seam",
				Pattern: &geometry.Pattern{Kind: "linear", Count: 12, Offset: []float64{0, 20, 0}}}}},
			{ID: "seam", Children: []geometry.Child{
				{ID: "skin", Ref: "skin"},
				{ID: "rivet", Ref: "rivet", Name: "Rivet", Position: []float64{-290, 3, 0},
					Pattern: &geometry.Pattern{Kind: "linear", Count: 25, Offset: []float64{20, 0, 0}}},
			}},
		}}
	out := runNodeHarness(t, searchRowsHarness, map[string]any{"spec": doc, "query": "Rivet 1", "workbench": string(js)})
	var got struct {
		Total int
		Found []struct{ ID, Label string }
		Rows  []struct{ Path, Name, Text string }
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unreadable harness output: %s (%v)", out, err)
	}
	// ‼️ Counted by PATH SEGMENT, not by the whole label.
	//
	// This fence was written when a tree copy was named "Rivet 1" outright, so several
	// occurrences really did read alike and `labels["Rivet 1"]` counted them. main's
	// PR #70 then named every tree part by its occurrence path — these are
	// "Seam 1 / Rivet 1 / rivet", "Seam 2 / Rivet 1 / rivet", …
	// (docs/bugfix/2026-09-14-tree-copies-shared-display-names.md) — so no two labels
	// are equal any more and that count was 0 after the merge.
	//
	// What the fence is FOR is unchanged and still checked below: the row says WHERE
	// the occurrence is, and no two rows read alike. The precondition it needs is that
	// the search reaches many occurrences that share a name somewhere in their path,
	// which is what the segment count holds. Under the old naming this counted the
	// same occurrences, so the fence has not been loosened — only re-expressed.
	segments := map[string]int{}
	for _, f := range got.Found {
		for _, seg := range strings.Split(f.Label, " / ") {
			segments[seg]++
		}
	}
	// Fifty rows reach about five seams (each has Rivet 1 and Rivet 10–19), so several
	// rows carry the same rivet name and only which seam it is tells them apart.
	if len(got.Rows) != len(got.Found) || len(got.Found) != 50 || got.Total < 12*11 || segments["Rivet 1"] < 4 {
		t.Fatalf("the search for Rivet 1 found %d of %d (rows %d, %d placed at a \"Rivet 1\"); the fixture no longer has seams of the same names",
			len(got.Found), got.Total, len(got.Rows), segments["Rivet 1"])
	}
	seen := map[string]string{}
	for i, r := range got.Rows {
		if r.Path != got.Found[i].ID {
			t.Fatalf("row %d is for %q; the search found %q", i, r.Path, got.Found[i].ID)
		}
		if !strings.Contains(r.Text, r.Path) {
			t.Errorf("the row for %s reads %q, which does not say where it is", r.Path, r.Text)
		}
		// The name column says WHAT the occurrence is; the path column says WHERE. The
		// row's whole text is unique either way once the path is in it, so these two
		// assertions — not the seen[r.Text] check below — are what hold the division.
		// geometry.Part.Name is the whole occurrence path since #70 ("Seam 1 / Rivet 1 /
		// rivet"); a row that took its name from it would say where twice and what never.
		if r.Name != got.Found[i].Label {
			t.Errorf("the row for %s is named %q in its name column; the search called it %q", r.Path, r.Name, got.Found[i].Label)
		}
		if strings.Contains(r.Name, "Seam") || strings.Contains(r.Name, geometry.NameSeparator) {
			t.Errorf("the row for %s is named %q, which carries the path above it; the path column says where it is", r.Path, r.Name)
		}
		if other, dup := seen[r.Text]; dup {
			t.Errorf("the rows for %s and %s both read %q", other, r.Path, r.Text)
		}
		seen[r.Text] = r.Path
	}
}

const provenanceHarness = `
  const fs = require('fs');
  const input = JSON.parse(fs.readFileSync(process.argv[4], 'utf8'));
` + liftFunctions + `
  const src = input.workbench;
  function element() {
    const cls = new Set(['provenance', 'hidden']);
    return { innerHTML: '', listeners: {}, addEventListener(type, f) { this.listeners[type] = f; },
      classList: { add: (c) => cls.add(c), remove: (c) => cls.delete(c), contains: (c) => cls.has(c),
                   toggle: (c, on) => (on === undefined ? !cls.has(c) : on) ? cls.add(c) : cls.delete(c) } };
  }
  function banner(subtrees) {
    const el = element(), state = JSON.parse(JSON.stringify(input.state));
    state.subtrees = {};
    for (let i = 0; i < subtrees; i++) state.subtrees['slot-' + i] = { source: 'kernel', note: 'built by the CAD kernel', occurrences: 30 + i, outside: [] };
    const fns = new Function('$', 'state', 'studio', 'standardsOf', 'figuresOf',
      ['esc', 'meshOnlyLabel', 'meshOnlyNames', 'renderProvenance', 'initProvenance'].map((n) => lift(src, n)).join('\n') +
      '; return { renderProvenance: renderProvenance, initProvenance: initProvenance };')(
      (id) => (id === 'provenance' ? el : null), state, { approximationNotes: () => ['Bracket: drawn as its box'] },
      (c) => c.standards || [], (c) => c.figures || []);
    // What a person sees of the banner: everything outside a folded container.
    const shown = () => visible(el.innerHTML.replace(/<div class="prov-details hidden"[\s\S]*$/, '</div>'));
    fns.initProvenance();
    fns.renderProvenance();
    const folded = { shown: shown(), html: el.innerHTML, hidden: el.classList.contains('hidden') };
    const click = el.listeners.click;
    if (click) click({ target: { closest: (sel) => (sel.indexOf('data-prov-toggle') >= 0 ? {} : null) } });
    const open = { shown: shown(), html: el.innerHTML, state: state.provenanceOpen };
    if (click) click({ target: { closest: (sel) => (sel.indexOf('data-prov-toggle') >= 0 ? {} : null) } });
    const again = { shown: shown() };
    if (click) click({ target: { closest: () => null } });
    const elsewhere = { shown: shown() };
    return { folded: folded, open: open, again: again, elsewhere: elsewhere, clickable: !!click };
  }
  process.stdout.write(JSON.stringify({ none: banner(0), many: banner(12) }));
`

// The provenance banner keeps its headline on the stage and folds its details until they
// are asked for, so a long banner no longer covers the model. The W2 run's banner — five
// not-verified items, the assumptions and a line for each of twelve loaded subtrees — took
// its whole 40 % of an 800-px stage and hid 70 of 73 copies of an isolated seam from the
// pointer. Folded, what shows is the same few words however much is behind them; open, it
// is everything it was; and the fold is a toggle, never a dismissal (PRD VIS-06).
func TestWorkbenchProvenanceBannerFoldsItsDetailsOffTheStage(t *testing.T) {
	js, err := assetFS.ReadFile("assets/workbench.js")
	if err != nil {
		t.Fatal(err)
	}
	css, err := assetFS.ReadFile("assets/workbench.css")
	if err != nil {
		t.Fatal(err)
	}
	state := map[string]any{
		"prototype": map[string]any{"model_note": "a model", "not_verified": []string{"not checked for strength",
			"not checked for fit", "not checked against a standard", "not a manufacturing drawing", "not costed"},
			"assumptions": []string{"steel", "rivets are 4 mm", "panels are 2 mm"}},
		"builtSolid": map[string]any{"notes": []string{"Clip 3 is not in the built solid"}},
		"recalled":   []any{},
	}
	out := runNodeHarness(t, provenanceHarness, map[string]any{"workbench": string(js), "state": state})
	type view struct {
		Shown, HTML string
		Hidden      bool
		State       bool
	}
	var got map[string]struct {
		Folded, Open, Again, Elsewhere view
		Clickable                      bool
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unreadable harness output: %s (%v)", out, err)
	}
	const headline = "This is a proposal, not a verified design."
	digits := regexp.MustCompile(`\d+`)
	many, none := got["many"], got["none"]
	if !many.Clickable {
		t.Fatal("initProvenance binds no click on the banner, so its details cannot be opened")
	}
	if many.Folded.Hidden || !strings.Contains(many.Folded.Shown, headline) {
		t.Fatalf("folded, the banner shows %q (hidden %v); the headline must always be on the stage", many.Folded.Shown, many.Folded.Hidden)
	}
	if digits.ReplaceAllString(many.Folded.Shown, "#") != digits.ReplaceAllString(none.Folded.Shown, "#") {
		t.Errorf("folded, the banner grows with what is behind it: %q with twelve subtrees, %q with none",
			many.Folded.Shown, none.Folded.Shown)
	}
	if len(many.Folded.Shown) > 160 {
		t.Errorf("folded, the banner still shows %d characters: %q", len(many.Folded.Shown), many.Folded.Shown)
	}
	for _, item := range []string{"not costed", "rivets are 4 mm", "slot-11 (41 parts)", "Clip 3 is not in the built solid", "Bracket: drawn as its box"} {
		if strings.Contains(many.Folded.Shown, item) {
			t.Errorf("folded, the banner still shows %q", item)
		}
		if !strings.Contains(many.Folded.HTML, item) || !strings.Contains(many.Open.Shown, item) {
			t.Errorf("%q is not in the banner's details, or not shown when they are opened", item)
		}
	}
	if !strings.Contains(many.Folded.HTML, `aria-expanded="false"`) || !strings.Contains(many.Open.HTML, `aria-expanded="true"`) || !many.Open.State {
		t.Error("the banner's toggle does not say whether its details are open (aria-expanded)")
	}
	if many.Again.Shown != many.Folded.Shown || many.Elsewhere.Shown != many.Folded.Shown {
		t.Errorf("a second click leaves %q and a click elsewhere %q; want the folded banner back, and unchanged", many.Again.Shown, many.Elsewhere.Shown)
	}
	if !strings.Contains(codeOnly(string(css)), ".provenance .prov-details.hidden { display: none; }") {
		t.Error("workbench.css does not hide a folded .prov-details itself, so shell.css's .hidden decides by load order")
	}
	if !strings.Contains(codeOnly(string(js)), "safely('provenance', initProvenance)") {
		t.Error("the workbench never calls initProvenance, so the toggle is bound to nothing")
	}
}
