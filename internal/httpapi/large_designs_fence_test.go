package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/cad"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Defects the workbench check of 2026-09-17 found on main while loading large designs
// (docs/spikes/2026-09-17-workbench-viewport §3, §4, §5), each held here.

// errorDetail is a refusal's code and the sentence written for it.
func errorDetail(t *testing.T, rec *httptest.ResponseRecorder) (code, detail string) {
	t.Helper()
	var body struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unreadable refusal %d %q: %v", rec.Code, rec.Body.String(), err)
	}
	d, _ := body.Error.Details["detail"].(string)
	return body.Error.Code, d
}

// A STEP export of a design past the 4,096 parts a request builds is refused for THAT
// reason — with the count, the limit and the export job that writes it — and with the
// status every size refusal answers, where a CAD kernel is configured. It answered 501
// "This deployment has no CAD kernel configured" from the label, with a kernel present:
// the label read the build-time format table, where STEP is always unavailable. A
// design within the ceiling gets a STEP label, not a refusal; without a kernel the
// refusal is still the missing kernel.
func TestExportLabel_STEPWithAKernelSaysTheCeilingNotAMissingKernel(t *testing.T) {
	x := newExportsHarness(t, nil)
	big := x.save(t, exportRows(21, 200)) // 4,200 occurrences
	huge := x.save(t, exportRows(200, 460))
	small := x.save(t, exportPlate())

	withKernel := x.d
	// Never started: nothing here builds. Available() is what a configured deployment says.
	withKernel.CAD = cad.New("a-python-that-is-never-run", logx.Discard())
	h := NewGeometryHandlers(withKernel)
	call := func(handler http.HandlerFunc, route string, v *geometry.Variant) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		r := getAs(x.viewer, "/v1/geometry/"+v.VersionID+route)
		r.SetPathValue("id", v.VersionID)
		handler(rec, r)
		return rec
	}

	for _, route := range []struct {
		name    string
		handler http.HandlerFunc
		path    string
	}{{"label", h.ExportLabel, "/export/label?format=step"}, {"export", h.Export, "/export?format=step"}} {
		rec := call(route.handler, route.path, big)
		code, detail := errorDetail(t, rec)
		if rec.Code != http.StatusBadRequest || code != "VALIDATION_FAILED" {
			t.Errorf("the STEP %s of 4,200 parts with a kernel answered %d %s, want 400 VALIDATION_FAILED: %s",
				route.name, rec.Code, code, rec.Body.String())
		}
		for _, want := range []string{"4200 parts", "at most 4096", "STEP via worker", "/exports", "90000"} {
			if !strings.Contains(detail, want) {
				t.Errorf("the STEP %s refusal of 4,200 parts does not say %q: %q", route.name, want, detail)
			}
		}
		if strings.Contains(strings.ToLower(detail), "no cad kernel") {
			t.Errorf("the STEP %s refusal blames a missing kernel that is configured: %q", route.name, detail)
		}
	}

	rec := call(h.ExportLabel, "/export/label?format=step", huge)
	if _, detail := errorDetail(t, rec); rec.Code != http.StatusBadRequest || !strings.Contains(detail, "92000 parts") ||
		!strings.Contains(detail, "does not reach it") {
		t.Errorf("the STEP label of 92,000 parts answered %d %q; want the job's ceiling named as well", rec.Code, detail)
	}

	rec = call(h.ExportLabel, "/export/label?format=step", small)
	var got struct {
		Label struct {
			Format     string `json:"format"`
			FormatKind string `json:"format_kind"`
			Headline   string `json:"headline"`
		} `json:"label"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != http.StatusOK || got.Label.Format != "step" || got.Label.FormatKind != "parametric" ||
		strings.Contains(got.Label.Headline, "tessellated preview") {
		t.Errorf("the STEP label of a one-part plate with a kernel answered %d %+v: %s", rec.Code, got, rec.Body.String())
	}

	rec = call(NewGeometryHandlers(x.d).ExportLabel, "/export/label?format=step", big)
	if _, detail := errorDetail(t, rec); rec.Code != http.StatusNotImplemented || !strings.Contains(detail, "no CAD kernel configured") {
		t.Errorf("without a kernel the STEP label answered %d %q; want 501 naming the missing kernel", rec.Code, detail)
	}
}

// Listing a project costs the same whatever size its designs are, and says how many
// parts each places. It took 3.4-4.1 s with a 1,020,782-part design in the project,
// all of it measuring (which places every part). Counted, not timed: the allocations
// of listing 100,000 placed parts stay within those of listing 400, where the old
// listing allocated per part.
func TestVariantsListCountsWhatADesignPlacesWithoutExpandingIt(t *testing.T) {
	x := newExportsHarness(t, nil)
	smallProject := x.project
	x.save(t, exportRows(200, 2))
	x.project = newProject(t, x.pool, x.d.Access, x.owner.ID, "Q", time.Now().UTC())
	bigProject := x.project
	big := x.save(t, exportRows(200, 500))

	list := func(project string) (*httptest.ResponseRecorder, uint64) {
		rec := httptest.NewRecorder()
		r := getAs(x.owner, "/v1/geometry?project_id="+project)
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		x.h.List(rec, r)
		runtime.ReadMemStats(&after)
		return rec, after.Mallocs - before.Mallocs
	}
	list(smallProject) // warm the pool and the handler
	list(bigProject)
	var smallAllocs, bigAllocs uint64 = 1 << 62, 1 << 62
	var smallRec, bigRec *httptest.ResponseRecorder
	for i := 0; i < 3; i++ {
		rec, n := list(smallProject)
		smallRec, smallAllocs = rec, min(smallAllocs, n)
		rec, n = list(bigProject)
		bigRec, bigAllocs = rec, min(bigAllocs, n)
	}
	if bigAllocs > smallAllocs*2+2000 {
		t.Errorf("listing a design of 100,000 placed parts allocated %d times, listing one of 400 %d: the listing "+
			"grows with the size of the design", bigAllocs, smallAllocs)
	}

	rows := func(rec *httptest.ResponseRecorder) []map[string]json.RawMessage {
		var body struct {
			Variants []map[string]json.RawMessage `json:"variants"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil || len(body.Variants) != 1 {
			t.Fatalf("the listing answered %d %.300s", rec.Code, rec.Body.String())
		}
		return body.Variants
	}
	for _, c := range []struct {
		rec  *httptest.ResponseRecorder
		want string
	}{{smallRec, "400"}, {bigRec, "100000"}} {
		row := rows(c.rec)[0]
		if string(row["occurrences"]) != c.want {
			t.Errorf("a listed design placing %s parts says occurrences %s", c.want, row["occurrences"])
		}
		if _, measured := row["measured"]; measured || len(row["measured_note"]) < 10 {
			t.Errorf("a listed row carries measured %s and measured_note %s; want no measurement and the note saying where it is",
				row["measured"], row["measured_note"])
		}
	}

	// The live turn's event counts the same way: a tree kept from a conversation is not
	// "0 part(s)" in the rail either.
	tree := lazyCar()
	tree.NotVerified = []string{"a fence fixture"}
	saved := (&ConverseHandlers{deps: x.d, geo: x.svc}).keepGeometry(getAs(x.owner, "/v1/converse"),
		converseRequest{Message: "a car", ProjectID: bigProject}, &tree, "test-model", 0)
	if saved == nil || saved.NotKept != "" || saved.Parts != len(tree.Expanded().Parts) {
		t.Errorf("a kept tree design's event says %+v; it places %d parts", saved, len(tree.Expanded().Parts))
	}

	// The single read still measures, and counts the same way.
	rec := httptest.NewRecorder()
	r := getAs(x.owner, "/v1/geometry/"+big.VersionID)
	r.SetPathValue("id", big.VersionID)
	x.h.Get(rec, r)
	var one struct {
		Variant struct {
			Occurrences int               `json:"occurrences"`
			Measured    []json.RawMessage `json:"measured"`
		} `json:"variant"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &one); err != nil || one.Variant.Occurrences != 100000 || len(one.Variant.Measured) == 0 {
		t.Errorf("GET /v1/geometry/{id} answered %d with occurrences %d and %d measured dimensions",
			rec.Code, one.Variant.Occurrences, len(one.Variant.Measured))
	}
}

const railRowHarness = `
  const fs = require('fs');
  const input = JSON.parse(fs.readFileSync(process.argv[4], 'utf8'));
` + liftFunctions + `
  const railRow = new Function(lift(input.workbench, 'railRow') + '; return railRow;')();
  process.stdout.write(JSON.stringify({ listed: railRow(input.listed).parts, event: railRow(input.event).parts }));
`

// The variants rail says how many parts a design places. It read "0 part(s)" for every
// design written as a tree, counting top-level parts; the count now comes with the
// listing (occurrences) and with the live event (parts), both counted by Go.
func TestWorkbenchRailCountsWhatADesignPlaces(t *testing.T) {
	src, err := assetFS.ReadFile("assets/workbench.js")
	if err != nil {
		t.Fatal(err)
	}
	doc := lazyCar()
	placed := len(doc.Expanded().Parts)
	listed, err := json.Marshal(listedVariant(geometry.Variant{VersionID: "ver_1", Units: "mm", Document: doc}))
	if err != nil {
		t.Fatal(err)
	}
	out := runNodeHarness(t, railRowHarness, map[string]any{"workbench": string(src),
		"listed": json.RawMessage(listed), "event": map[string]any{"version_id": "ver_1", "parts": placed}})
	var got struct{ Listed, Event int }
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unreadable harness output %q: %v", out, err)
	}
	if got.Listed != placed || got.Event != placed {
		t.Errorf("the rail says %d part(s) for a listed tree design and %d for the live event; it places %d (top-level parts: %d)",
			got.Listed, got.Event, placed, len(doc.Parts))
	}
}

const wholeMeshHarness = `
  const fs = require('fs');
  const stub = require(process.argv[2]);
  const F = stub.loadForge3D(process.argv[3]);
  const input = JSON.parse(fs.readFileSync(process.argv[4], 'utf8'));
` + liftFunctions + `
  const flush = () => new Promise((res) => setImmediate(res));
  (async () => {
    const results = {};
    for (const name of Object.keys(input.scenarios)) {
      const sc = input.scenarios[name], requests = [];
      const window = { Forge3D: F, localStorage: { getItem: () => (sc.eager ? '1' : null) } };
      const fetch = (url) => {
        requests.push(url);
        return Promise.resolve({ ok: false, status: 400, json: () => Promise.resolve({ error: {} }) });
      };
      const studio = new F.Studio(stub.makeCanvas('webgl2', 640, 480).canvas, { onError() {}, onNotice() {} });
      const state = {}, tree = {}, none = () => {};
      const loadPrototype = new Function('state', 'studio', 'tree', '$', 'window', 'fetch', 'renderStates', 'setPlace',
        'renderParts', 'renderTree', 'renderProvenance',
        ['viewportEager', 'fetchSubtree', 'refineWithBuiltSolid', 'loadPrototype'].map((n) => lift(input.workbench, n)).join('\n') +
        '; return loadPrototype;')(state, studio, tree, () => null, window, fetch, none, none, none, none, none);
      loadPrototype(sc.spec, [], 'ver_1');
      await flush(); await flush();
      results[name] = requests;
    }
    process.stdout.write(JSON.stringify(results));
  })();
`

// The workbench asks for no whole-design mesh of a design its viewport has refused to
// draw: past the limit that request is refused (400) and there is nothing to receive.
// It was sent for a stored million-part design. A design within the limit still asks.
func TestWorkbenchAsksNoWholeMeshForADesignItRefused(t *testing.T) {
	src, err := assetFS.ReadFile("assets/workbench.js")
	if err != nil {
		t.Fatal(err)
	}
	over, small := yardOfRows(200, 501), yardOfRows(2, 3)
	if over.ViewportRefusal() == "" || small.ViewportRefusal() != "" {
		t.Fatal("the fixtures no longer straddle the viewport's limit")
	}
	out := runNodeHarness(t, wholeMeshHarness, map[string]any{"workbench": string(src), "scenarios": map[string]any{
		"browsed": map[string]any{"spec": over},
		"eager":   map[string]any{"spec": over, "eager": true},
		"small":   map[string]any{"spec": small},
	}})
	var got map[string][]string
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unreadable harness output %q: %v", out, err)
	}
	whole := func(requests []string) int {
		n := 0
		for _, u := range requests {
			if strings.HasSuffix(u, "/mesh") {
				n++
			}
		}
		return n
	}
	for _, name := range []string{"browsed", "eager"} {
		if n := whole(got[name]); n != 0 {
			t.Errorf("loading a design of %d parts (%s) asked for its whole mesh %d time(s): %v", 100200, name, n, got[name])
		}
	}
	if whole(got["small"]) != 1 {
		t.Errorf("a six-part design asked for its whole mesh %d times, want once: %v", whole(got["small"]), got["small"])
	}
	subtrees := 0
	for _, u := range got["browsed"] {
		if strings.Contains(u, "/mesh?subtree=") {
			subtrees++
		}
	}
	if subtrees == 0 {
		t.Errorf("a design past the limit was not browsed: its first view asked for no subtree (%v)", got["browsed"])
	}
}

// blockSite places 125,000 studs: 5 blocks × 50 rows × 500 studs, each block 25,000.
func blockSite() geometry.Document {
	stud := geometry.Part{ID: "stud", Name: "Stud", Shape: "box", Size: map[string]float64{"width": 1, "height": 1, "depth": 1},
		Color: "#cccccc", Opacity: 1}
	return geometry.Document{Name: "site", Units: "mm", Root: "site", Parts: []geometry.Part{},
		NotVerified: []string{"a fence fixture"}, Definitions: []geometry.Part{stud},
		Assemblies: []geometry.Assembly{
			{ID: "site", Children: []geometry.Child{{ID: "block", Ref: "block",
				Pattern: &geometry.Pattern{Kind: "linear", Count: 5, Offset: []float64{0, 0, 200}}}}},
			{ID: "block", Children: []geometry.Child{{ID: "row", Ref: "row",
				Pattern: &geometry.Pattern{Kind: "linear", Count: 50, Offset: []float64{0, 3, 0}}}}},
			{ID: "row", Children: []geometry.Child{{ID: "stud", Ref: "stud",
				Pattern: &geometry.Pattern{Kind: "linear", Count: 500, Offset: []float64{2, 0, 0}}}}},
		}}
}

const browseHarness = `
  const fs = require('fs');
  const stub = require(process.argv[2]);
  const F = stub.loadForge3D(process.argv[3]);
  const input = JSON.parse(fs.readFileSync(process.argv[4], 'utf8'));
  const made = stub.makeCanvas('webgl2', 640, 480), notices = [];
  const studio = new F.Studio(made.canvas, { onError() {}, onNotice(m) { notices.push(m); } });
  const asked = [], out = {};
  function same(ids, paths) {
    const want = new Set();
    paths.forEach((p) => input.expected[p].forEach((id) => want.add(id)));
    return ids.length === want.size && ids.every((id) => want.has(id));
  }
  out.lazily = F.loadsLazily(input.spec);
  studio.loadLazy(input.spec, (p) => asked.push(p));
  out.firstAsked = asked.slice();
  out.notice = notices[notices.length - 1];
  out.searches = input.queries.map((q) => studio.findOccurrences(q, 50));
  out.whole = studio.requestSubtree('block');
  out.wholeNotice = notices[notices.length - 1];
  out.asked = [];
  ['block-1', 'block-2', 'block-3', 'block-4'].forEach((p) => { out.asked.push(studio.requestSubtree(p)); studio.addSubtree(p, null); });
  out.cleared = notices[notices.length - 1];
  out.four = studio.lazyState();
  out.fourSame = same(studio.occurrenceIds(), ['block-1', 'block-2', 'block-3', 'block-4']);
  out.fifth = studio.requestSubtree('block-5');
  studio.addSubtree('block-5', null);
  out.five = studio.lazyState();
  out.fiveSame = same(studio.occurrenceIds(), ['block-2', 'block-3', 'block-4', 'block-5']);
  out.coveredAgain = studio.requestSubtree('block-5/row-3');
  studio.lod = 0;
  made.record.reset();
  studio.draw();
  out.instances = studio.stats.instances;
  out.problems = made.record.problems.slice();
  out.requested = asked;
  process.stdout.write(JSON.stringify(out));
`

// A design past the viewport's limit is BROWSED, by decision (2026-09-17: the limit bounds
// what is drawn at once, not what may be browsed). Refused whole in Go's words; a row
// loads when asked while what is drawn stays within the limit, the rows drawn longest ago
// put back to make room; a row that alone is past the limit is refused by name with its
// count, in Go's words; and search reads every occurrence the tree lists, as Go places
// them, drawn or not. A stored 1,020,782-part design listed its rows, found nothing and
// loaded nothing.
func TestRendererBrowsesADesignPastTheViewportLimit(t *testing.T) {
	prev := geometry.CurrentLimits()
	geometry.SetLimits(geometry.Limits{MaxOccurrences: 200_000})
	t.Cleanup(func() { geometry.SetLimits(prev) })

	doc := blockSite()
	placed := doc.Expanded().Parts
	if len(placed) != 125_000 || doc.ViewportRefusal() == "" {
		t.Fatalf("the fixture places %d parts; it must be past the viewport's limit", len(placed))
	}
	whole, err := doc.Subtree("block")
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string][]string{}
	for _, path := range []string{"block-1", "block-2", "block-3", "block-4", "block-5"} {
		under := geometry.OccurrenceUnder(path)
		for _, p := range placed {
			if under(p.ID) {
				expected[path] = append(expected[path], p.ID)
			}
		}
	}
	queries := []string{"block-3/row-7/stud-1", "Row 49 / Stud 50", "stud-500"}
	out := runNodeHarness(t, browseHarness, map[string]any{"spec": doc, "expected": expected, "queries": queries})
	type hits struct {
		Found []struct{ ID, Label string }
		Total int
	}
	var got struct {
		Lazily, Whole, FourSame, Fifth, FiveSame, CoveredAgain bool
		FirstAsked, Asked                                      json.RawMessage
		Notice, WholeNotice, Cleared                           string
		Searches                                               []hits
		Four, Five                                             struct {
			Loaded  []string
			Drawn   int
			Browse  bool
			Pending []string
		}
		Instances int
		Problems  []string
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unreadable renderer output %.300q: %v", out, err)
	}
	if !got.Lazily || !got.Four.Browse {
		t.Fatalf("a design of 125,000 parts is not browsed (lazily %v, browse %v)", got.Lazily, got.Four.Browse)
	}
	if !strings.HasPrefix(got.Notice, doc.ViewportRefusal()) {
		t.Errorf("the stage says %q; want Go's refusal first: %q", got.Notice, doc.ViewportRefusal())
	}
	for i, q := range queries {
		lq := strings.ToLower(q)
		var ids []string
		for _, p := range placed {
			if strings.Contains(strings.ToLower(p.ID), lq) || strings.Contains(strings.ToLower(p.Name), lq) {
				ids = append(ids, p.ID)
			}
		}
		s := got.Searches[i]
		var gotIDs []string
		for _, f := range s.Found {
			gotIDs = append(gotIDs, f.ID)
		}
		if s.Total != len(ids) || len(ids) == 0 || strings.Join(gotIDs, ",") != strings.Join(ids[:min(50, len(ids))], ",") {
			t.Errorf("searching %q found %d, first %v; Go places %d matching, first %v", q, s.Total, gotIDs[:min(3, len(gotIDs))],
				len(ids), ids[:min(3, len(ids))])
		}
	}
	if got.Whole || got.WholeNotice != whole.Refusal() {
		t.Errorf("asking for the row \"block\" (125,000 parts) answered %v and said %q; want refused in Go's words %q",
			got.Whole, got.WholeNotice, whole.Refusal())
	}
	if string(got.Asked) != "[true,true,true,true]" || got.Cleared != "" || !got.FourSame || got.Four.Drawn != 100_000 ||
		len(got.Four.Pending) != 0 {
		t.Errorf("four blocks of 25,000 asked %s, drew %d (the ids Go places: %v), notice %q, pending %v",
			got.Asked, got.Four.Drawn, got.FourSame, got.Cleared, got.Four.Pending)
	}
	sort.Strings(got.Five.Loaded)
	if !got.Fifth || !got.FiveSame || got.Five.Drawn != 100_000 ||
		strings.Join(got.Five.Loaded, ",") != "block-2,block-3,block-4,block-5" {
		t.Errorf("a fifth block answered %v and left %v loaded, %d drawn (ids Go places: %v); want block-1 put back and 100,000 drawn",
			got.Fifth, got.Five.Loaded, got.Five.Drawn, got.FiveSame)
	}
	if got.CoveredAgain || string(got.FirstAsked) != "[]" {
		t.Errorf("a row inside a loaded block was asked for again (%v), or the first view asked %s", got.CoveredAgain, got.FirstAsked)
	}
	if got.Instances > geometry.MaxViewportParts() || got.Instances != 100_000 || len(got.Problems) != 0 {
		t.Errorf("a frame drew %d instances (limit %d) with problems %v", got.Instances, geometry.MaxViewportParts(), got.Problems)
	}
}

const visibilityHarness = `
  const fs = require('fs'), vm = require('vm');
  const stub = require(process.argv[2]);
  const listeners = { window: {}, document: {} };
  const on = (where) => (type, fn) => { (listeners[where][type] = listeners[where][type] || []).push(fn); };
  const document = { visibilityState: 'visible', addEventListener: on('document') };
  const sandbox = { window: { addEventListener: on('window'), devicePixelRatio: 1 }, document, console };
  vm.createContext(sandbox);
  vm.runInContext(fs.readFileSync(process.argv[3], 'utf8'), sandbox);
  const F = sandbox.window.Forge3D;
  const made = stub.makeCanvas('webgl2', 800, 600), canvas = made.canvas;
  const studio = new F.Studio(canvas, { onError() {} });
  let draws = 0;
  const draw = studio.draw.bind(studio);
  studio.draw = () => { draws++; draw(); };
  const fire = (where, type) => (listeners[where][type] || []).forEach((fn) => fn());
  const size = () => [canvas.width, canvas.height];
  const out = { shown: size() };
  canvas.clientWidth = 0; canvas.clientHeight = 0; document.visibilityState = 'hidden';
  fire('document', 'visibilitychange');
  fire('window', 'resize');
  out.hidden = size();
  canvas.clientWidth = 1000; canvas.clientHeight = 700;
  draws = 0; document.visibilityState = 'visible';
  fire('document', 'visibilitychange');
  out.again = size(); out.drawsOnShow = draws;
  process.stdout.write(JSON.stringify(out));
`

// A viewport hidden and shown again is sized and redrawn when it is shown. It is
// draw-on-demand: nothing redrew a pane that came back without a window resize, and a
// resize while hidden (a canvas of no size) set its buffer to 640×480. PR 143 could not
// show the desktop app's Browser pane to see this; this is the renderer's half of it.
func TestRendererRedrawsWhenItsPaneIsShownAgain(t *testing.T) {
	var got struct {
		Shown, Hidden, Again [2]int
		DrawsOnShow          int
	}
	out := runNodeHarness(t, visibilityHarness, map[string]any{})
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unreadable renderer output %q: %v", out, err)
	}
	if got.Shown != [2]int{800, 600} {
		t.Fatalf("the canvas starts %v, want 800×600", got.Shown)
	}
	if got.Hidden != got.Shown {
		t.Errorf("a resize while hidden set the buffer to %v; a canvas of no size is hidden, and keeps %v", got.Hidden, got.Shown)
	}
	if got.Again != [2]int{1000, 700} || got.DrawsOnShow == 0 {
		t.Errorf("shown again at 1000×700 the buffer is %v after %d draw(s); want it sized and redrawn", got.Again, got.DrawsOnShow)
	}
}

const stepLabelHarness = `
  const fs = require('fs');
  const input = JSON.parse(fs.readFileSync(process.argv[4], 'utf8'));
` + liftFunctions + `
  const flush = () => new Promise((res) => setImmediate(res));
  (async () => {
    const results = {};
    for (const name of Object.keys(input.replies)) {
      const box = { innerHTML: '', textContent: '', classList: { remove() {} } };
      const document = { querySelector: () => box };
      const reply = input.replies[name];
      const fetch = () => Promise.resolve({ ok: reply.status < 300, status: reply.status, json: () => Promise.resolve(reply.body) });
      const show = new Function('document', 'fetch', ['esc', 'section', 'refusalText', 'showExportLabel'].map((n) => lift(input.workbench, n)).join('\n') +
        '; return showExportLabel;')(document, fetch);
      show('ver_1', name === 'obj' ? 'obj' : 'step');
      await flush(); await flush(); await flush();
      results[name] = visible(box.innerHTML);
    }
    process.stdout.write(JSON.stringify(results));
  })();
`

// The workbench shows a STEP label as what it is, a B-Rep, not "0 triangles" (the label
// PR 145 answers with a kernel sends triangles 0), and a refusal in the ceiling's own words.
func TestWorkbenchSTEPLabelSaysBRepNotTriangles(t *testing.T) {
	src, err := assetFS.ReadFile("assets/workbench.js")
	if err != nil {
		t.Fatal(err)
	}
	label := func(format, kind string) map[string]any {
		return map[string]any{"format": format, "format_kind": kind, "headline": "unverified", "units": "mm"}
	}
	out := runNodeHarness(t, stepLabelHarness, map[string]any{"workbench": string(src), "replies": map[string]any{
		"step": map[string]any{"status": 200, "body": map[string]any{"label": label("step", "parametric"), "triangles": 0}},
		"obj":  map[string]any{"status": 200, "body": map[string]any{"label": label("obj", "mesh"), "triangles": 12}},
		"refused": map[string]any{"status": 400, "body": map[string]any{"error": map[string]any{
			"message": "One or more request fields failed validation.",
			"details": map[string]any{"detail": "This design places 30023 parts, and a STEP file written during a request is built from at most 4096"}}}},
	}})
	var got map[string]string
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unreadable harness output %q: %v", out, err)
	}
	if !strings.Contains(got["step"], "B-Rep, not tessellated") || strings.Contains(got["step"], "triangles") {
		t.Errorf("a STEP label reads %q; want it called a B-Rep and no triangles counted", got["step"])
	}
	if !strings.Contains(got["obj"], "12 triangles") {
		t.Errorf("an OBJ label reads %q; want its triangles", got["obj"])
	}
	if !strings.Contains(got["refused"], "30023 parts") {
		t.Errorf("a refused STEP label reads %q; want the ceiling's own sentence", got["refused"])
	}
}

// siteOf is blocks × 10 rows × 100 studs, each block its own slot of the root.
func siteOf(blocks int) geometry.Document {
	d := blockSite()
	d.Assemblies[0].Children[0].Pattern.Count = blocks
	d.Assemblies[1].Children[0].Pattern.Count = 10
	d.Assemblies[2].Children[0].Pattern.Count = 100
	return d
}

// A subtree places only what leads to its path, and places it exactly where the whole
// design's expansion does. Asking a stored 1,020,782-part design for one rivet took 5.9 s,
// every request expanding the whole design (the 2026-09-17 workbench check). Counted, not
// timed: resolving one row of a 400-block site allocates within three times what one of a
// 2-block site does, where placing the whole site would be 400,000 parts.
// Where an assembly has features of its own, nothing is pruned, so a feature straddling
// the path is still found and named.
func TestSubtree_PlacesOnlyWhatLeadsToItsPathAndTheSameParts(t *testing.T) {
	prev := geometry.CurrentLimits()
	geometry.SetLimits(geometry.Limits{MaxOccurrences: 500_000})
	t.Cleanup(func() { geometry.SetLimits(prev) })

	featured := lazyCar()
	featured.Assemblies[2].Features = []geometry.Feature{{ID: "hole", Op: "cut", Of: "skin", With: []string{"rivet-1"}}}
	for name, c := range map[string]struct {
		doc   geometry.Document
		paths []string
	}{
		"lazy car":   {lazyCar(), []string{"panel", "panel-3", "front-left-2/pin", "front-left/pin-3", "seat", "frame", "lamp"}},
		"site":       {siteOf(7), []string{"block-3", "block-3/row-2", "block-7/row-10/stud-100", "block/row-1"}},
		"with feats": {featured, []string{"panel-2", "panel-2/rivet-4"}},
	} {
		all := c.doc.Expanded().Parts
		for _, path := range c.paths {
			under := geometry.OccurrenceUnder(path)
			var want []geometry.Part
			for _, p := range all {
				if under(p.ID) {
					want = append(want, p)
				}
			}
			sub, err := c.doc.Subtree(path)
			if err != nil {
				t.Errorf("%s: %s refused: %v", name, path, err)
				continue
			}
			got, _ := json.Marshal(sub.Parts)
			exp, _ := json.Marshal(want)
			if string(got) != string(exp) {
				t.Errorf("%s: the subtree %s places %d parts, not the %d the whole expansion places there (or not where)",
					name, path, len(sub.Parts), len(want))
			}
		}
	}
	if sub, err := featured.Subtree("panel-2/rivet-1"); err != nil || len(sub.Outside) == 0 {
		t.Errorf("a feature straddling the path is not named (err %v, outside %v)", err, sub)
	}

	small, big := siteOf(2), siteOf(400)
	allocs := func(d geometry.Document) float64 {
		return testing.AllocsPerRun(2, func() {
			if _, err := d.Subtree("block-2/row-3"); err != nil {
				t.Fatal(err)
			}
		})
	}
	// The pruned walk still reads each of the root's 400 slots to refuse it, so the big site
	// costs a few allocations a slot more; placing it whole is 400,000 parts.
	if a, b := allocs(small), allocs(big); b > a*3 {
		t.Errorf("one row of a 400-block site (400,000 parts) allocated %.0f times, of a 2-block site %.0f: a subtree "+
			"expands the whole design", b, a)
	}
}

// Opening one design costs the same whatever size it is, and measures it exactly as
// measuring it again would. GET /v1/geometry/{id} placed every part to find the
// design's overall dimensions: 2.1-3.1 s for the 1,020,782-part fleet, on every open.
// The corners are now kept when a version is stored (migration 0025). Counted, not
// timed: reading a design of 100,000 placed parts allocates within about twice what
// reading one of 400 does, where measuring allocated per part. A row stored before
// the column existed is measured the old way ONCE, answers the same, and is kept.
func TestGetMeasuresALargeDesignWithoutPlacingIt(t *testing.T) {
	x := newExportsHarness(t, nil)
	tree := lazyCar()
	tree.Assumptions = []string{"the wheelbase"}
	tree.NotVerified = []string{"a fence fixture"}
	small, big, car := x.save(t, exportRows(200, 2)), x.save(t, exportRows(200, 500)), x.save(t, tree)

	get := func(v *geometry.Variant) (json.RawMessage, uint64) {
		rec := httptest.NewRecorder()
		r := getAs(x.owner, "/v1/geometry/"+v.VersionID)
		r.SetPathValue("id", v.VersionID)
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		x.h.Get(rec, r)
		runtime.ReadMemStats(&after)
		var body struct {
			Variant struct {
				Measured json.RawMessage `json:"measured"`
			} `json:"variant"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil {
			t.Fatalf("GET /v1/geometry/%s answered %d %.300s", v.VersionID, rec.Code, rec.Body.String())
		}
		return body.Variant.Measured, after.Mallocs - before.Mallocs
	}
	fewest := func(v *geometry.Variant) uint64 {
		n := uint64(1 << 62)
		for i := 0; i < 3; i++ {
			_, a := get(v)
			n = min(n, a)
		}
		return n
	}
	same := func(name string, v *geometry.Variant) {
		t.Helper()
		got, _ := get(v)
		want, _ := json.Marshal(geometry.Measure(v.Document, v.Units))
		if string(got) != string(want) {
			t.Errorf("%s: GET measured %s\nmeasuring it again finds %s", name, got, want)
		}
	}
	// Kept when stored, before anything reads it.
	ctx := context.Background()
	kept := func() (n int) {
		t.Helper()
		if err := x.pool.QueryRow(ctx, `select count(*) from forge_geometry where version_id = any($1) and extent is not null`,
			[]string{small.VersionID, big.VersionID, car.VersionID}).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := kept(); n != 3 {
		t.Errorf("%d of 3 designs keep their extent when stored: the first open of the others places every part", n)
	}
	get(small) // warm the pool and the handler
	get(big)
	if s, b := fewest(small), fewest(big); b > s*2+2000 {
		t.Errorf("reading a design of 100,000 placed parts allocated %d times, one of 400 %d: opening a design "+
			"grows with the parts it places", b, s)
	}
	for name, v := range map[string]*geometry.Variant{"400 boxes": small, "100,000 boxes": big, "a car tree": car} {
		same(name, v)
	}

	// Stored before migration 0025: no extent kept.
	if _, err := x.pool.Exec(ctx, `update forge_geometry set extent = null where version_id = any($1)`,
		[]string{big.VersionID, car.VersionID}); err != nil {
		t.Fatal(err)
	}
	same("100,000 boxes stored before 0025", big)
	same("a car tree stored before 0025", car)
	if n := kept(); n != 3 {
		t.Errorf("after one read, %d of 3 rows keep an extent (2 were stored before 0025): every open places them again", n)
	}
	if s, b := fewest(small), fewest(big); b > s*2+2000 {
		t.Errorf("a design stored before 0025, read once, still allocates %d times against %d", b, s)
	}
}
