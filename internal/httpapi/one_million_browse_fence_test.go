package httpapi

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// The workbench's slow paths on a 1,020,782-part design that PR 147 found and left
// (docs/spikes/2026-09-17-one-million-browse): search walked the whole tree on every
// keystroke, opening a car's row loaded all 30,023 of its parts, and the build card did
// not show when the worker was last seen. Each is held here, by counting and by
// comparing with what it replaced, not by timing.

// mixedSite is blockSite (125,000 studs) with ids and names in mixed case, a named
// child, a definition written out by its own repeat (so a label carries " / "), and a
// top-level part that repeats: every kind of line the search index writes.
func mixedSite() geometry.Document {
	d := blockSite()
	d.Parts = []geometry.Part{{ID: "Sign-X", Name: "Site Sign", Shape: "box",
		Size: map[string]float64{"width": 1, "height": 1, "depth": 1}, Color: "#cccccc", Opacity: 1,
		Repeat: &geometry.Repeat{Count: 2, Offset: []float64{5, 0, 0}}}}
	d.Definitions = append(d.Definitions, geometry.Part{ID: "Post", Name: "Post", Shape: "box",
		Size: map[string]float64{"width": 1, "height": 4, "depth": 1}, Color: "#cccccc", Opacity: 1,
		Repeat: &geometry.Repeat{Count: 2, Offset: []float64{0, 5, 0}}})
	d.Assemblies[0].Children = append(d.Assemblies[0].Children,
		geometry.Child{ID: "Gate-A", Ref: "gate", Name: "Main Gate", Position: []float64{-50, 0, 0}})
	d.Assemblies = append(d.Assemblies, geometry.Assembly{ID: "gate", Children: []geometry.Child{{ID: "Post", Ref: "Post",
		Pattern: &geometry.Pattern{Kind: "linear", Count: 3, Offset: []float64{2, 0, 0}}}}})
	return d
}

const treeIndexHarness = `
  const fs = require('fs');
  const stub = require(process.argv[2]);
  const F = stub.loadForge3D(process.argv[3]);
  const input = JSON.parse(fs.readFileSync(process.argv[4], 'utf8'));
  const studio = new F.Studio(stub.makeCanvas('webgl2', 640, 480).canvas, { onError() {}, onNotice() {} });
  const out = { searches: [], sameAsWalk: [], builtOnce: true, indexed: true };
  studio.loadLazy(input.spec, () => {});
  out.browse = studio.lazyState().browse;
  let first = null;
  input.queries.forEach((q) => {
    const got = studio.findOccurrences(q, 50), walked = F.searchTree(input.spec, q, 50);
    if (first === null) first = studio._treeIndex;
    if (studio._treeIndex !== first) out.builtOnce = false;
    if (!studio.searchStats || !studio.searchStats.indexed) out.indexed = false;
    out.searches.push(got);
    out.sameAsWalk.push(JSON.stringify(got) === JSON.stringify(walked));
  });
  out.hadIndex = !!first;
  // Another design loaded: its own index, not the last one's.
  studio.loadLazy(input.other, () => {});
  out.other = studio.findOccurrences(input.otherQuery, 50);
  out.otherWalked = F.searchTree(input.other, input.otherQuery, 50);
  process.stdout.write(JSON.stringify(out));
`

// A browsed design's search reads an index built once, on its first search, and answers
// exactly what walking the tree answers: the same occurrences, in the same order, with
// the same labels and the same total, for mixed-case ids, named children, a repeating
// definition and a repeating top-level part. It walked the whole tree on every keystroke:
// 0.4-1.0 s per character over the 1,020,782-part fleet. A second design gets its own.
func TestRendererSearchesABrowsedDesignThroughAnIndexLikeTheWalk(t *testing.T) {
	prev := geometry.CurrentLimits()
	geometry.SetLimits(geometry.Limits{MaxOccurrences: 200_000})
	t.Cleanup(func() { geometry.SetLimits(prev) })

	doc := mixedSite()
	placed := doc.Expanded().Parts
	if doc.ViewportRefusal() == "" {
		t.Fatalf("the fixture places %d parts; it must be past the viewport's limit", len(placed))
	}
	other := mixedSite()
	other.Assemblies[0].Children[1].Name = "Back Gate"
	queries := []string{"gate-a/post-2", "GATE", "main gate / post 3", "post", "Post 2 / Post", "sign", "Site Sign",
		"sign-x-2", "stud-500", "block-5/row-50/stud-500", "row 49 / stud 50", "zz", "s", "-1", " / "}
	out := runNodeHarness(t, treeIndexHarness, map[string]any{"spec": doc, "queries": queries,
		"other": other, "otherQuery": "back gate"})
	type hits struct {
		Found []struct{ ID, Label string }
		Total int
	}
	var got struct {
		Browse, BuiltOnce, Indexed, HadIndex bool
		Searches                             []hits
		SameAsWalk                           []bool
		Other, OtherWalked                   hits
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unreadable renderer output %.300q: %v", out, err)
	}
	if !got.Browse {
		t.Fatal("the fixture is not browsed")
	}
	if !got.HadIndex || !got.Indexed || !got.BuiltOnce {
		t.Errorf("searching a browsed design %d times: index built %v, answered from it %v, built once %v; "+
			"want one index, built on the first search and read by every one after", len(queries), got.HadIndex,
			got.Indexed, got.BuiltOnce)
	}
	for i, q := range queries {
		if !got.SameAsWalk[i] {
			t.Errorf("searching %q through the index answered differently from walking the tree: %d found, first %+v",
				q, got.Searches[i].Total, got.Searches[i].Found[:min(3, len(got.Searches[i].Found))])
		}
		// And the walk is still what Go places (TestRendererBrowsesADesignPastTheViewportLimit holds the rest).
		lq := strings.ToLower(q)
		var ids []string
		for _, p := range placed {
			if strings.Contains(strings.ToLower(p.ID), lq) || strings.Contains(strings.ToLower(p.Name), lq) {
				ids = append(ids, p.ID)
			}
		}
		var gotIDs []string
		for _, f := range got.Searches[i].Found {
			gotIDs = append(gotIDs, f.ID)
		}
		if got.Searches[i].Total != len(ids) || strings.Join(gotIDs, ",") != strings.Join(ids[:min(50, len(ids))], ",") {
			t.Errorf("searching %q found %d, first %v; Go places %d matching, first %v", q, got.Searches[i].Total,
				gotIDs[:min(3, len(gotIDs))], len(ids), ids[:min(3, len(ids))])
		}
	}
	if got.Other.Total == 0 || !jsonEqual(got.Other, got.OtherWalked) {
		t.Errorf("a second design searched for %q found %+v; walking it finds %+v: the first design's index answered",
			"back gate", got.Other, got.OtherWalked)
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// treeWiring runs the workbench's own initTree and tree rows against a fake page: the
// search box, a click on a row's toggle and its "Draw all" button.
const treeWiringHarness = `
  const fs = require('fs');
  const input = JSON.parse(fs.readFileSync(process.argv[4], 'utf8'));
` + liftFunctions + `
  const src = input.workbench;
  const wait = Number((src.match(/var TREE_SEARCH_WAIT_MS = (\d+);/) || [])[1]);
  const el = (name) => ({ name, value: '', on: {}, addEventListener(type, fn) { this.on[type] = fn; } });
  const page = { tree: el('tree'), 'tree-search': el('tree-search'), 'tree-showall': el('tree-showall') };
  const timers = [], calls = [];
  const out = { wait, rendersWhileTyping: 0, renders: 0, pending: 0, query: null };
  const tree = { open: {}, query: '', isolated: '' };
  let renders = 0;
  const studio = {
    openRow(p) { calls.push('openRow ' + p); },
    requestSubtree(p) { calls.push('requestSubtree ' + p); },
    openedPartly(p) { return p === 'car-7' ? { drawn: 7109, total: 30023 } : null; },
    select() {}, isolate() {}
  };
  const made = new Function('$', 'tree', 'studio', 'state', 'renderTree', 'renderParts', 'window', 'setTimeout',
    'clearTimeout', 'TREE_SEARCH_WAIT_MS',
    ['initTree', 'treeRow', 'drawAllButton', 'esc'].map((n) => lift(src, n)).join('\n') +
    '; return { initTree: initTree, treeRow: treeRow };')(
    (id) => page[id], tree, studio, { selectedPart: null }, () => { renders++; }, () => {},
    { Forge3D: { occurrenceMatcher: () => () => true } },
    (fn, ms) => { timers.push({ fn, ms }); return timers.length; },
    (id) => { if (timers[id - 1]) timers[id - 1].fn = null; }, wait);
  made.initTree();
  const search = page['tree-search'];
  ['r', 'ri', 'riv', 'rive', 'rivet'].forEach((v) => { search.value = v; search.on.input(); });
  out.rendersWhileTyping = renders;
  const live = timers.filter((t) => t.fn);
  out.pending = live.length;
  out.delay = live.length ? live[0].ms : 0;
  live.forEach((t) => t.fn());
  out.renders = renders;
  out.query = tree.query;
  // A click on a row's toggle opens it; its "Draw all" asks for the whole row.
  const click = (attr, path) => page.tree.on.click({ target: { closest: () => ({ getAttribute: (a) => a === attr ? path : null }) } });
  click('data-toggle', 'car-7');
  click('data-drawall', 'car-7');
  out.calls = calls;
  out.openRow = made.treeRow('car-7', 'Car 7', 1, true, true, null);
  out.closedRow = made.treeRow('car-7', 'Car 7', 1, true, false, null);
  out.wholeRow = made.treeRow('car-8', 'Car 8', 1, true, true, null);
  process.stdout.write(JSON.stringify(out));
`

type treeWiringRun struct {
	Wait, RendersWhileTyping, Renders, Pending, Delay int
	Query                                             string
	Calls                                             []string
	OpenRow, ClosedRow, WholeRow                      string
}

func runTreeWiring(t *testing.T) treeWiringRun {
	t.Helper()
	src, err := assetFS.ReadFile("assets/workbench.js")
	if err != nil {
		t.Fatal(err)
	}
	var got treeWiringRun
	if err := json.Unmarshal(runNodeHarness(t, treeWiringHarness, map[string]any{"workbench": string(src)}), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

// The tree's search waits for typing to pause: five characters typed in a row search
// once, for what was typed last. Each character searched a million occurrences.
func TestWorkbenchTreeSearchWaitsForTypingToPause(t *testing.T) {
	got := runTreeWiring(t)
	if got.Wait < 50 || got.Wait > 500 {
		t.Errorf("the search waits %d ms for typing to pause; want long enough to span a keystroke and short enough "+
			"not to feel slow (50-500)", got.Wait)
	}
	if got.RendersWhileTyping != 0 || got.Pending != 1 || got.Delay != got.Wait || got.Renders != 1 || got.Query != "rivet" {
		t.Errorf("typing five characters rendered the tree %d times while typing and %d after, with %d search(es) "+
			"waiting (%d ms) for %q; want none while typing, then one, for \"rivet\"",
			got.RendersWhileTyping, got.Renders, got.Pending, got.Delay, got.Query)
	}
}

const openRowHarness = `
  const fs = require('fs');
  const stub = require(process.argv[2]);
  const F = stub.loadForge3D(process.argv[3]);
  const input = JSON.parse(fs.readFileSync(process.argv[4], 'utf8'));
  const notices = [];
  const studio = new F.Studio(stub.makeCanvas('webgl2', 640, 480).canvas, { onError() {}, onNotice(m) { notices.push(m); } });
  const asked = [];
  studio.loadLazy(input.spec, (p) => asked.push(p));
  const out = {};
  const arrive = (paths) => paths.forEach((p) => studio.addSubtree(p, null));
  out.plan = F.openPaths(input.spec, 'block-2');
  out.open = studio.openRow('block-2');
  out.opened = asked.slice();
  arrive(out.opened);
  out.afterOpen = { drawn: studio.lazyState().drawn, ids: studio.occurrenceIds(), partly: studio.openedPartly('block-2') };
  asked.length = 0;
  out.rest = studio.requestSubtree('block-2');
  arrive(asked.slice());
  out.afterRest = { drawn: studio.lazyState().drawn, ids: studio.occurrenceIds().length, partly: studio.openedPartly('block-2') };
  // Opened, then "Draw all" before its first rows arrive: they arrive under the whole row.
  asked.length = 0;
  studio.openRow('block-3');
  const first = asked.slice();
  studio.requestSubtree('block-3');
  arrive(['block-3']);
  arrive(first);
  out.raced = { drawn: studio.lazyState().drawn, ids: studio.occurrenceIds().length };
  // Near the limit: 40 rows of block-4 drawn (70,002 in all), block-5 opened (8,001 on
  // their way) and then drawn whole before those arrive. The rows on their way are
  // replaced by it, so it fits (95,003) and nothing drawn is put back.
  asked.length = 0;
  for (let r = 1; r <= 40; r++) studio.requestSubtree('block-4/row-' + r);
  arrive(asked.slice());
  asked.length = 0;
  studio.openRow('block-5');
  const fifth = asked.slice();
  studio.requestSubtree('block-5');
  arrive(['block-5']);
  arrive(fifth);
  out.full = { loaded: studio.lazyState().loaded.sort(), drawn: studio.lazyState().drawn, ids: studio.occurrenceIds().length };
  // Small enough: asked for whole.
  asked.length = 0;
  studio.openRow('block-1/row-3');
  out.small = { asked: asked.slice(), partly: studio.openedPartly('block-1/row-3') };
  // Nothing beneath fits: the row is asked for whole, and refused by name.
  asked.length = 0;
  studio.openRow('block');
  out.tooBig = { asked: asked.slice(), notice: notices[notices.length - 1] };
  process.stdout.write(JSON.stringify(out));
`

// openSite is blockSite with one small part in each block after its rows: a block
// places 25,001 parts, as rows of 500 and a sign.
func openSite() geometry.Document {
	d := blockSite()
	d.Assemblies[1].Children = append(d.Assemblies[1].Children, geometry.Child{ID: "sign", Ref: "stud", Position: []float64{-5, 0, 0}})
	return d
}

// Opening a row draws what it lists first, not the whole row: every child row that
// fits whole, then a pattern's copies in order, up to the first view's budget of
// parts and a bounded number of requests; the row then offers the rest ("Draw all"),
// which draws exactly Go's parts under it and replaces what the open drew, even when it
// is asked for before the open's rows arrive. A row within the budget is asked for
// whole, and a row nothing beneath which fits is asked for whole and refused by name.
// Opening a car of the fleet loaded all 30,023 of its parts.
func TestRendererOpensARowItsFirstRowsFirstAndTheRestWhenAsked(t *testing.T) {
	prev := geometry.CurrentLimits()
	geometry.SetLimits(geometry.Limits{MaxOccurrences: 200_000})
	t.Cleanup(func() { geometry.SetLimits(prev) })

	doc := openSite()
	placed := doc.Expanded().Parts
	under := func(paths ...string) []string {
		var ids []string
		for _, p := range placed {
			for _, path := range paths {
				if geometry.OccurrenceUnder(path)(p.ID) {
					ids = append(ids, p.ID)
					break
				}
			}
		}
		sort.Strings(ids)
		return ids
	}
	out := runNodeHarness(t, openRowHarness, map[string]any{"spec": doc})
	type plan struct {
		Paths        []string
		Drawn, Total int
	}
	type partly struct{ Drawn, Total int }
	var got struct {
		Plan, Open plan
		Opened     []string
		AfterOpen  struct {
			Drawn  int
			IDs    []string
			Partly *partly
		}
		Rest      bool
		AfterRest struct {
			Drawn, IDs int
			Partly     *partly
		}
		Raced struct{ Drawn, IDs int }
		Full  struct {
			Loaded     []string
			Drawn, IDs int
		}
		Small struct {
			Asked  []string
			Partly *partly
		}
		TooBig struct {
			Asked  []string
			Notice string
		}
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unreadable renderer output %.300q: %v", out, err)
	}

	want := []string{"block-2/sign"}
	for i := 1; i <= 16; i++ {
		want = append(want, fmt.Sprintf("block-2/row-%d", i))
	}
	block := len(under("block-2"))
	if block != 25_001 {
		t.Fatalf("the fixture's block places %d parts", block)
	}
	if strings.Join(got.Opened, ",") != strings.Join(want, ",") || got.Plan.Drawn != 8_001 || got.Plan.Total != block {
		t.Errorf("opening block-2 (25,001 parts) asked for %v, planning %d of %d; want the sign whole, then rows 1-16 "+
			"(8,001 parts, within the first view's 8,192)", got.Opened, got.Plan.Drawn, got.Plan.Total)
	}
	if len(got.Opened) > 24 {
		t.Errorf("one open sent %d requests", len(got.Opened))
	}
	sort.Strings(got.AfterOpen.IDs)
	if strings.Join(got.AfterOpen.IDs, ",") != strings.Join(under(want...), ",") || got.AfterOpen.Drawn != 8_001 {
		t.Errorf("after the open's rows arrived %d parts are drawn; want exactly Go's 8,001 under them", got.AfterOpen.Drawn)
	}
	if got.AfterOpen.Partly == nil || *got.AfterOpen.Partly != (partly{8_001, block}) {
		t.Errorf("the opened block says %+v of itself is drawn; want 8,001 of %d, so the tree can offer the rest", got.AfterOpen.Partly, block)
	}
	if !got.Rest || got.AfterRest.Drawn != block || got.AfterRest.IDs != block || got.AfterRest.Partly != nil {
		t.Errorf("drawing the rest of block-2 answered %v and left %d counted, %d drawn, partly %+v; want the whole block, %d, once",
			got.Rest, got.AfterRest.Drawn, got.AfterRest.IDs, got.AfterRest.Partly, block)
	}
	if got.Raced.Drawn != got.Raced.IDs || got.Raced.IDs != 2*block {
		t.Errorf("block-3 drawn whole before its opened rows arrived: %d counted against the limit, %d drawn; want both %d",
			got.Raced.Drawn, got.Raced.IDs, 2*block)
	}
	nearLimit := 3*block + 40*500
	if !strings.Contains(strings.Join(got.Full.Loaded, ","), "block-2,block-3") || len(got.Full.Loaded) != 43 ||
		got.Full.Drawn != nearLimit || got.Full.IDs != nearLimit {
		t.Errorf("drawing block-5 whole while its opened rows were on their way left %d rows loaded (%v…), %d counted, "+
			"%d drawn; want nothing put back (the rows on their way are replaced, not added): 43 rows and %d parts",
			len(got.Full.Loaded), got.Full.Loaded[:min(3, len(got.Full.Loaded))], got.Full.Drawn, got.Full.IDs, nearLimit)
	}
	if strings.Join(got.Small.Asked, ",") != "block-1/row-3" || got.Small.Partly != nil {
		t.Errorf("opening a row of 500 asked %v (partly %+v); want the row whole", got.Small.Asked, got.Small.Partly)
	}
	whole, err := doc.Subtree("block")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.TooBig.Asked) != 0 || got.TooBig.Notice != whole.Refusal() {
		t.Errorf("opening the ×5 row asked %v and said %q; want it refused in Go's words %q", got.TooBig.Asked,
			got.TooBig.Notice, whole.Refusal())
	}
}

// The workbench opens a row through the studio's openRow, and an opened row that drew
// only its first rows offers the rest with its count; its "Draw all" asks for the whole
// row. A closed row, and an opened row drawn whole, offer nothing.
func TestWorkbenchOpensARowItsFirstRowsFirstAndOffersTheRest(t *testing.T) {
	got := runTreeWiring(t)
	if strings.Join(got.Calls, ",") != "openRow car-7,requestSubtree car-7" {
		t.Errorf("opening car-7 and pressing its Draw all called %v; want openRow, then the whole row", got.Calls)
	}
	button := regexp.MustCompile(`<button[^>]*data-drawall="car-7"[^>]*>Draw all 30023</button>`)
	if !button.MatchString(got.OpenRow) {
		t.Errorf("an opened row that drew 7,109 of its 30,023 parts offers no \"Draw all 30023\": %s", got.OpenRow)
	}
	if strings.Contains(got.ClosedRow, "data-drawall") || strings.Contains(got.WholeRow, "data-drawall") {
		t.Errorf("a closed row or one drawn whole offers the rest: %s | %s", got.ClosedRow, got.WholeRow)
	}
}

const lastSeenHarness = `
  const fs = require('fs'), vm = require('vm');
  const sandbox = { window: {}, document: { readyState: 'loading', addEventListener() {} }, console };
  vm.createContext(sandbox);
  vm.runInContext(fs.readFileSync(process.argv[4], 'utf8'), sandbox);
  const G = sandbox.window.ForgeGoalProgress;
  const input = JSON.parse(fs.readFileSync(process.argv[5], 'utf8'));
  const now = Date.parse(input.now);
  const out = {};
  for (const [name, c] of Object.entries(input.cases)) {
    const p = G.summarise(c.goal, c.tasks, [], now);
    out[name] = { current: p.current, html: G.html(p, true) };
  }
  // Polled: "ago" is measured against the reply's Date, not the browser's clock.
  const fetch = (url) => Promise.resolve({ ok: true, status: 200,
    headers: { get: (h) => h.toLowerCase() === 'date' ? new Date(now).toUTCString() : null },
    json: () => Promise.resolve(url.endsWith('/timeline') ? { events: [] } : input.cases.fresh) });
  G.watch('gol_1', { fetch, setTimeout: () => 0, clearTimeout() {},
    onProgress: (p) => { out.polled = { current: p.current, html: G.html(p, true) }; } });
  setTimeout(() => process.stdout.write(JSON.stringify(out)), 20);
`

// A running build step says when its worker was last seen (PR 145's last_seen_at), in
// seconds, measured against the server's clock, and says the worker may have stopped
// once that is more than 30 s: the worker stamps it at least every 5 s. A step no
// worker holds, and a goal that has finished, say nothing about a worker.
func TestGoalCardSaysWhenTheWorkerWasLastSeen(t *testing.T) {
	src, err := assetFS.ReadFile("assets/workbench.js")
	if err != nil {
		t.Fatal(err)
	}
	const now = "2020-01-02T03:04:50Z"
	running := func(status, seen string) map[string]any {
		m := map[string]any{"id": "tsk_2", "title": "Add the hub", "status": status}
		if seen != "" {
			m["last_seen_at"] = seen
		}
		return map[string]any{"goal": map[string]any{"status": "running", "tasks_total": 3, "tasks_done": 1},
			"tasks": []map[string]any{step("tsk_1", "Draw the frame", "succeeded"), m, step("tsk_3", "Fit the wheels", "ready")}}
	}
	cases := map[string]any{
		"fresh":   running("running", "2020-01-02T03:04:46Z"),
		"stale":   running("running", "2020-01-02T03:04:05Z"),
		"claimed": running("claimed", "2020-01-02T03:04:48Z"),
		"ready":   running("ready", ""),
		"settled": map[string]any{"goal": map[string]any{"status": "succeeded", "tasks_total": 1, "tasks_done": 1},
			"tasks": []map[string]any{step("tsk_1", "Draw the frame", "succeeded")}},
	}
	out := runNodeHarness(t, `
  // runNodeHarness hands over one input file: the workbench travels in it.
  const args = process.argv.slice();
  require('fs').writeFileSync(args[4] + '.wb.js', JSON.parse(require('fs').readFileSync(args[4], 'utf8')).workbench);
  process.argv = args.slice(0, 4).concat([args[4] + '.wb.js', args[4]]);
`+lastSeenHarness, map[string]any{"workbench": string(src), "now": now, "cases": cases})
	type seen struct {
		Current *struct {
			Title   string
			SeenAgo *int
			Stale   bool
		}
		HTML string
	}
	var got map[string]seen
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unreadable harness output %.300q: %v", out, err)
	}
	for _, name := range []string{"fresh", "polled"} {
		c := got[name]
		if c.Current == nil || c.Current.SeenAgo == nil || *c.Current.SeenAgo != 4 || c.Current.Stale ||
			!strings.Contains(c.HTML, "worker last seen 4 s ago") || strings.Contains(c.HTML, "may have stopped") {
			t.Errorf("%s: a step whose worker was seen 4 s ago shows %q (%+v); want \"worker last seen 4 s ago\", not stale",
				name, c.HTML, c.Current)
		}
	}
	if c := got["claimed"]; !strings.Contains(c.HTML, "worker last seen 2 s ago") {
		t.Errorf("a claimed step seen 2 s ago shows %q", c.HTML)
	}
	if c := got["stale"]; c.Current == nil || !c.Current.Stale ||
		!regexp.MustCompile(`class="note bad">Worker last seen 45 s ago: it may have stopped`).MatchString(c.HTML) {
		t.Errorf("a step whose worker was last seen 45 s ago shows %q; want it flagged as possibly stopped", c.HTML)
	}
	for _, name := range []string{"ready", "settled"} {
		if strings.Contains(strings.ToLower(got[name].HTML), "last seen") {
			t.Errorf("%s: says when a worker was last seen: %q", name, got[name].HTML)
		}
	}
}
