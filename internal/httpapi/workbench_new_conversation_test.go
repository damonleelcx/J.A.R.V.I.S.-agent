package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// "New conversation" on the workbench, from the browser's side (2026-09-17).
//
// # Why
//
// There was no way to start a conversation from scratch at the workbench. The
// server already supported it — a turn with no conversation_id starts a new one
// and its history is read from that id alone — but once the page had an id it
// sent it on every turn for ever. The fix is a control that drops the id.
//
// ‼️ The trap this file exists for: a control that CLEARS THE PANE and keeps
// sending the old id. It looks exactly right, and every earlier turn still goes
// to the model. So the harness below runs the page's own state functions in node
// — loaded as a page loads workbench.js, the way ForgeGoalProgress is fenced —
// and reads the request BODY the page would send, not the screen.
//
// The endpoint's half (an empty id really does mean no history) is
// TestANewConversation_* in conversation_new_test.go, against Postgres.

const newConversationHarness = `
  const fs = require('fs'), vm = require('vm');
  const sandbox = { window: {}, document: { readyState: 'loading', addEventListener() {} }, console };
  vm.createContext(sandbox);
  vm.runInContext(fs.readFileSync(process.argv[2], 'utf8'), sandbox);
  const C = sandbox.window.ForgeConversation;
  if (!C || !C.begin || !C.adopt || !C.request || !C.clearWorkspace) { process.stdout.write(JSON.stringify({ missing: true })); return; }

  const store = (init) => {
    const m = Object.assign({}, init);
    return { m, getItem: (k) => (k in m ? m[k] : null), setItem: (k, v) => { m[k] = String(v); }, removeItem: (k) => { delete m[k]; } };
  };
  const onScreen = 'Bracket — 2 part(s): Plate [id: plate], Rib [id: rib] (units: mm)';
  const proto = { name: 'Bracket', units: 'mm', parts: [{ id: 'plate' }, { id: 'rib' }] };
  // A page that has been WORKED IN: a design drawn, variants in the rail, two of
  // them ticked, a part selected, a subtree isolated, a search running, a sketch
  // attached and a requirement ticked to build from. This is the state the
  // control has to leave behind, so the harness has to start from it.
  const page = (extra) => Object.assign({ conversationID: 'cnv_old', projectID: 'prj_1', industry: 'general',
    busy: false, goalPhase: 'none', proposal: null, goal: null, planTasks: null,
    prototype: proto, builtSolid: { triangles: [1] }, measured: [{ id: 'plate' }], states: ['thinking'],
    subtrees: { '/frame': {} }, selectedPart: 'plate', building: true,
    variants: [{ version_id: 'ver_1' }, { version_id: 'ver_2' }], picked: ['ver_1', 'ver_2'],
    recalled: [{ figure: 'M6' }], images: ['data:image/png;base64,AA'], fromNodes: ['nod_1'],
    requirements: [{ id: 'nod_1', title: 'holds 40kg' }],
    progress: { done: 1 }, progressError: 'x', clarification: 'which steel?',
    rationale: 'because', startMessage: 'Started.', planAsBuild: true, error: 'old failure',
    provenanceOpen: true, speak: true, formats: ['obj'] }, extra || {});
  const treeState = () => ({ open: { '/frame': true }, query: 'rib', isolated: '/frame' });
  const keys = (id) => ({ [C.key]: id, 'forge.workbench.project': 'prj_1' });
  const out = {};

  // An ordinary conversation, then "New conversation", then the first send, then
  // the server's conversation event, then the second send.
  let s = page(), st = store(keys('cnv_old')), t = treeState();
  out.before = C.request(s, 'the code name is Blue Heron', [], [], onScreen);
  out.begun = C.begin(s, st, t);
  // '' rather than onScreen: describeOnScreen returns '' with no prototype, and
  // begin has just cleared it. The page cannot describe a stage it has emptied,
  // and passing the old sentence here would be the harness asserting something
  // the page does not do.
  out.first = C.request(s, 'start over: a shelf bracket', [], [], '');
  out.firstStored = st.m[C.key] === undefined ? null : st.m[C.key];
  out.projectStored = st.m['forge.workbench.project'] || null;
  // What is left of the workspace the conversation was looking at.
  out.cleared = {
    prototype: s.prototype, builtSolid: s.builtSolid, measured: s.measured.length,
    states: s.states.length, subtrees: Object.keys(s.subtrees).length,
    selectedPart: s.selectedPart, building: s.building,
    variants: s.variants.length, picked: s.picked.length, recalled: s.recalled.length,
    images: s.images.length, fromNodes: s.fromNodes.length,
    phase: s.goalPhase, proposal: s.proposal, goal: s.goal, planTasks: s.planTasks,
    progress: s.progress, progressError: s.progressError, clarification: s.clarification,
    planAsBuild: s.planAsBuild, error: s.error,
    tree: { open: Object.keys(t.open).length, query: t.query, isolated: t.isolated },
    // Kept on purpose: the project, its industry, the requirement LIST (the
    // selection went), what this deployment can write, and how this person
    // likes to read a provenance banner.
    projectID: s.projectID, industry: s.industry, requirements: s.requirements.length,
    formats: s.formats.length, provenanceOpen: s.provenanceOpen
  };
  C.adopt(s, st, 'cnv_new');
  out.second = C.request(s, 'make it 4mm thick', [], [], onScreen);
  out.secondStored = st.m[C.key] || null;

  // Refused while something is in flight: nothing changes.
  out.refused = {};
  for (const [name, extra] of [['streaming', { busy: true }], ['planning', { goalPhase: 'planning' }], ['starting', { goalPhase: 'starting' }]]) {
    const p = page(extra), ps = store(keys('cnv_old')), pt = treeState();
    const r = C.begin(p, ps, pt);
    out.refused[name] = { reason: r.refused || '', id: p.conversationID, stored: ps.m[C.key] || null,
      request: C.request(p, 'x', [], [], '').conversation_id,
      // A refusal must not half-clear the page either.
      prototype: !!p.prototype, variants: p.variants.length, isolated: pt.isolated };
  }

  // A bare proposal and a card with a GOAL behind it both go: see the fence.
  const bare = page({ goalPhase: 'proposed', proposal: { title: 'Lamp' } });
  out.bareBegun = C.begin(bare, store(keys('cnv_old')), treeState());
  const running = page({ goalPhase: 'active', proposal: { title: 'Lamp' }, goal: { id: 'gol_1' }, planTasks: [{}] });
  out.runningBegun = C.begin(running, store(keys('cnv_old')), treeState());
  out.bare = { phase: bare.goalPhase, proposal: !!bare.proposal };
  out.running = { phase: running.goalPhase, proposal: !!running.proposal, goal: !!running.goal,
    planTasks: running.planTasks, progress: running.progress };

  // clearWorkspaceView on its own, with no tree passed: it must not throw, so
  // the control cannot be broken by a page whose assembly tree never existed.
  const alone = page();
  C.clearWorkspace(alone, null);
  out.alone = { prototype: alone.prototype, variants: alone.variants.length, projectID: alone.projectID };

  process.stdout.write(JSON.stringify(out));
`

type ncRequest struct {
	Message        string `json:"message"`
	ProjectID      string `json:"project_id"`
	ConversationID string `json:"conversation_id"`
	OnScreen       string `json:"on_screen"`
}

type ncBegun struct {
	Previous string `json:"previous"`
	Refused  string `json:"refused"`
	// The id of a goal whose card went with the conversation, so the page can
	// say where that goal is still readable.
	Goal string `json:"goal"`
}

// ncCleared is the workspace AFTER the control ran: what must be empty, and what
// must still be there.
type ncCleared struct {
	Prototype     any    `json:"prototype"`
	BuiltSolid    any    `json:"builtSolid"`
	Measured      int    `json:"measured"`
	States        int    `json:"states"`
	Subtrees      int    `json:"subtrees"`
	SelectedPart  any    `json:"selectedPart"`
	Building      bool   `json:"building"`
	Variants      int    `json:"variants"`
	Picked        int    `json:"picked"`
	Recalled      int    `json:"recalled"`
	Images        int    `json:"images"`
	FromNodes     int    `json:"fromNodes"`
	Phase         string `json:"phase"`
	Proposal      any    `json:"proposal"`
	Goal          any    `json:"goal"`
	PlanTasks     any    `json:"planTasks"`
	Progress      any    `json:"progress"`
	ProgressError any    `json:"progressError"`
	Clarification any    `json:"clarification"`
	PlanAsBuild   bool   `json:"planAsBuild"`
	Error         any    `json:"error"`
	Tree          struct {
		Open     int    `json:"open"`
		Query    string `json:"query"`
		Isolated string `json:"isolated"`
	} `json:"tree"`
	ProjectID      string `json:"projectID"`
	Industry       string `json:"industry"`
	Requirements   int    `json:"requirements"`
	Formats        int    `json:"formats"`
	ProvenanceOpen bool   `json:"provenanceOpen"`
}

type ncRun struct {
	Missing       bool      `json:"missing"`
	Before        ncRequest `json:"before"`
	Begun         ncBegun   `json:"begun"`
	First         ncRequest `json:"first"`
	FirstStored   *string   `json:"firstStored"`
	ProjectStored *string   `json:"projectStored"`
	Cleared       ncCleared `json:"cleared"`
	Second        ncRequest `json:"second"`
	SecondStored  *string   `json:"secondStored"`
	Refused       map[string]struct {
		Reason    string  `json:"reason"`
		ID        string  `json:"id"`
		Stored    *string `json:"stored"`
		Request   string  `json:"request"`
		Prototype bool    `json:"prototype"`
		Variants  int     `json:"variants"`
		Isolated  string  `json:"isolated"`
	} `json:"refused"`
	Bare struct {
		Phase    string `json:"phase"`
		Proposal bool   `json:"proposal"`
	} `json:"bare"`
	BareBegun    ncBegun `json:"bareBegun"`
	RunningBegun ncBegun `json:"runningBegun"`
	Running      struct {
		Phase     string `json:"phase"`
		Proposal  bool   `json:"proposal"`
		Goal      bool   `json:"goal"`
		PlanTasks any    `json:"planTasks"`
		Progress  any    `json:"progress"`
	} `json:"running"`
	Alone struct {
		Prototype any    `json:"prototype"`
		Variants  int    `json:"variants"`
		ProjectID string `json:"projectID"`
	} `json:"alone"`
}

// orNil prints a stored key for a failure message: its value, or that there is none.
func orNil(p *string) string {
	if p == nil {
		return "nothing"
	}
	return fmt.Sprintf("%q", *p)
}

func runNewConversation(t *testing.T) ncRun {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node on PATH; skipping the workbench new-conversation fence")
	}
	src, err := assetFS.ReadFile("assets/workbench.js")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	asset, harness := filepath.Join(dir, "workbench.js"), filepath.Join(dir, "run.js")
	for path, data := range map[string][]byte{asset: src, harness: []byte(newConversationHarness)} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(node, harness, asset)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("the workbench could not be driven: %v %s", err, stderr.String())
	}
	var run ncRun
	if err := json.Unmarshal(out, &run); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if run.Missing {
		t.Fatal("workbench.js exports no ForgeConversation.begin/adopt/request/clearWorkspace: " +
			"there is nothing that starts a new conversation, or nothing that clears the workspace it was looking at")
	}
	return run
}

// Starting a new conversation drops the id the page sends, keeps the project,
// and the id the server then mints is what the next send uses.
func TestWorkbench_ANewConversationSendsNoConversationIDAndThenUsesTheNewOne(t *testing.T) {
	run := runNewConversation(t)

	if run.Before.ConversationID != "cnv_old" {
		t.Fatalf("before starting a new conversation the page sends conversation_id %q; the harness expected cnv_old, "+
			"so it is not reading the real request", run.Before.ConversationID)
	}
	if run.Begun.Refused != "" || run.Begun.Previous != "cnv_old" {
		t.Errorf("starting a new conversation on an idle page returned %+v; want the previous id and no refusal", run.Begun)
	}
	// ‼️ THE assertion. The server reads history from the id it is sent, so this
	// field being non-empty is every old turn going to the model with a pane that
	// looks empty.
	if run.First.ConversationID != "" {
		t.Errorf("the first message after \"New conversation\" still sends conversation_id %q. The pane is clear "+
			"and the server replays the old conversation to the model.", run.First.ConversationID)
	}
	if run.FirstStored != nil {
		t.Errorf("the page still remembers conversation %q, so a reload reopens the conversation that was left",
			*run.FirstStored)
	}
	// A fresh conversation, not a fresh project.
	if run.First.ProjectID != "prj_1" || run.ProjectStored == nil || *run.ProjectStored != "prj_1" {
		t.Errorf("the new conversation left the project: sends project_id %q, stored %s. Its first variant would "+
			"land in a new project, away from the design on screen.", run.First.ProjectID, orNil(run.ProjectStored))
	}
	// ‼️ The on_screen note is now EMPTY on the first turn, and that is the point
	// rather than a regression: the stage is cleared, describeOnScreen returns ''
	// with no prototype, and a note describing a design that is no longer drawn
	// would be the page telling the model about something the person cannot see.
	// Until 2026-09-22 the design was kept and this asserted the opposite.
	if run.First.OnScreen != "" {
		t.Errorf("the first message after \"New conversation\" still describes %q as being on screen. "+
			"The stage was cleared; the model would be told about a design that is not there.", run.First.OnScreen)
	}
	if run.Second.ConversationID != "cnv_new" || run.SecondStored == nil || *run.SecondStored != "cnv_new" {
		t.Errorf("after the server names the new conversation the page sends %q and remembers %s; want cnv_new — "+
			"otherwise every turn starts yet another conversation", run.Second.ConversationID, orNil(run.SecondStored))
	}
}

// While a turn streams or a goal is being planned or started, "New conversation"
// says why not and changes nothing.
func TestWorkbench_ANewConversationIsRefusedWithAReasonWhileSomethingIsInFlight(t *testing.T) {
	run := runNewConversation(t)
	for _, name := range []string{"streaming", "planning", "starting"} {
		r, ok := run.Refused[name]
		if !ok {
			t.Fatalf("the harness did not run the %s case", name)
		}
		if r.Reason == "" {
			t.Errorf("%s: not refused. A reply still arriving is recorded into the old conversation and painted "+
				"into the new pane", name)
		}
		if r.ID != "cnv_old" || r.Stored == nil || *r.Stored != "cnv_old" || r.Request != "cnv_old" {
			t.Errorf("%s: refused, but the conversation was dropped anyway (id %q, stored %s, sends %q)",
				name, r.ID, orNil(r.Stored), r.Request)
		}
	}
	for _, r := range run.Refused {
		if !r.Prototype || r.Variants == 0 || r.Isolated == "" {
			t.Errorf("a refused new conversation cleared the workspace anyway (design %v, %d variants, isolated %q). "+
				"The refusal exists because the turn still arriving belongs to THIS page; half-clearing it is worse "+
				"than either answer", r.Prototype, r.Variants, r.Isolated)
		}
	}
}

// The card goes too — including one with a goal behind it (2026-09-22).
//
// # What changed, and why the old rule was wrong
//
// Until today clearWorkspaceView kept a proposal that had a GOAL behind it,
// planned or running, on the reasoning that it is work in the project and its
// progress must stay visible. That was right while the design, the rail and the
// stage were all kept around it. It is wrong now that they are cleared: a lone
// progress card for a goal started in a conversation that no longer exists, on
// an otherwise empty workspace, is the most confusing thing that could be left
// on the page.
//
// ‼️ Nothing is deleted. The goal is still a goal, still running, still in the
// console's Goals panel — and begin() returns its id so the message names it and
// says where to follow it, which is the assertion below that matters most.
func TestWorkbench_ANewConversationClearsTheProposalCardAndNamesTheGoal(t *testing.T) {
	run := runNewConversation(t)

	if run.Bare.Phase != "none" || run.Bare.Proposal {
		t.Errorf("a proposal nothing was created from stayed on the card (%+v); it belonged to the old conversation", run.Bare)
	}
	if run.BareBegun.Goal != "" {
		t.Errorf("a bare proposal reported goal %q; there was no goal to name", run.BareBegun.Goal)
	}
	if run.Running.Phase != "none" || run.Running.Proposal || run.Running.Goal ||
		run.Running.PlanTasks != nil || run.Running.Progress != nil {
		t.Errorf("a started goal's card survived a new conversation (%+v). The workspace around it is cleared, "+
			"so what is left is a progress card belonging to a conversation that is gone", run.Running)
	}
	if run.RunningBegun.Goal != "gol_1" {
		t.Errorf("starting a new conversation over a started goal returned goal %q; want gol_1. Without the id "+
			"the page cannot say which goal is still running or where to follow it, and a cleared card then "+
			"reads as a cancelled goal", run.RunningBegun.Goal)
	}
}

// Everything the previous conversation left on the workspace, gone — and
// everything that belongs to the PROJECT, still there (2026-09-22).
//
// # Why this fence exists
//
// "New conversation" used to clear the transcript alone. What a person saw was
// an empty transcript in front of a fully dressed workspace: the previous design
// still drawn, its variants still ticked for comparison, a part still selected,
// a subtree still isolated, a search still filtering the assembly, a sketch
// still attached ready to ride along with the next message, and a proposal card
// still offering work from a conversation that no longer existed. It read as a
// page that had LOST its transcript rather than as a new start.
//
// damon, 2026-09-22: "no need delete, no need start fresh. just make it work
// with new conversation."
//
// ‼️ The trap on the other side is a control that "starts fresh" by dropping the
// project too — which would send the next variant into a brand new project, away
// from everything this person has been building. So this fence asserts BOTH
// halves: the view is empty, and the project, its industry, its requirement list
// and this deployment's formats are untouched. Nothing here is a request; no
// stored design and no goal is deleted by any of it.
func TestWorkbench_ANewConversationClearsTheWorkspaceItWasLookingAt(t *testing.T) {
	run := runNewConversation(t)
	c := run.Cleared

	for _, gone := range []struct {
		name  string
		empty bool
		why   string
	}{
		{"the design on the stage", c.Prototype == nil, "the previous design is still drawn behind an empty transcript"},
		{"the kernel's solid for it", c.BuiltSolid == nil, "the built surface of the previous design is still held"},
		{"its measured parts", c.Measured == 0, "measurements of a design that is no longer drawn are still on the page"},
		{"the avatar states of the last turn", c.States == 0, "states from the conversation that was left are still held"},
		{"the subtrees fetched for it", c.Subtrees == 0, "meshes of the previous design are still held"},
		{"the selected part", c.SelectedPart == nil, "a part of the previous design is still selected"},
		{"the building flag", !c.Building, "the voice surface stays docked over an empty stage instead of returning to the middle"},
		{"the variants rail", c.Variants == 0, "the previous conversation's variants are still in the rail"},
		{"what was ticked to compare", c.Picked == 0, "version ids from the previous conversation are still ticked"},
		{"the figures recalled from memory", c.Recalled == 0, "standards figures quoted in the old conversation are still shown"},
		{"an attached sketch", c.Images == 0, "a sketch attached in the old conversation would ride along on the first new message"},
		{"the requirements ticked to build from", c.FromNodes == 0, "requirements chosen for the old conversation's next turn are still chosen"},
		{"the proposal card", c.Phase == "none" && c.Proposal == nil && c.Goal == nil && c.PlanTasks == nil,
			"work proposed in the conversation that was left is still on the card"},
		{"the goal progress being read", c.Progress == nil && c.ProgressError == nil, "progress for a goal from the old conversation is still shown"},
		{"a clarifying question", c.Clarification == nil, "a question about the old conversation's goal is still waiting"},
		{"the build-it-as-a-model choice", !c.PlanAsBuild, "a choice made about the old proposal is applied to the next one"},
		{"the last error", c.Error == nil, "a failure from the conversation that was left is still on screen"},
		{"the assembly tree's open rows", c.Tree.Open == 0, "rows of the previous design's tree are still open"},
		{"the assembly search", c.Tree.Query == "", "the tree is still filtered by a search typed in the old conversation"},
		{"the isolated subtree", c.Tree.Isolated == "", "one subtree of the previous design is still the only thing drawn"},
	} {
		if !gone.empty {
			t.Errorf("a new conversation did not clear %s: %s", gone.name, gone.why)
		}
	}

	// And the half that must NOT be cleared. A fresh conversation, not a fresh
	// project — and nothing deleted on the server.
	if c.ProjectID != "prj_1" || c.Industry != "general" {
		t.Errorf("the new conversation left the project (project %q, industry %q). Its first variant would land "+
			"in a new project, away from everything already built here", c.ProjectID, c.Industry)
	}
	if c.Requirements == 0 {
		t.Error("the requirements LIST was cleared as well as the selection. The list is the project's — read " +
			"from its graph — and a new conversation in the same project still has them to build from")
	}
	if c.Formats == 0 {
		t.Error("the formats this deployment can write were cleared. They are a property of the DEPLOYMENT, " +
			"read once from GET /v1/geometry/formats, and clearing them silently removes export buttons")
	}
	if !c.ProvenanceOpen {
		t.Error("the provenance banner's folded/open preference was cleared. It is how this person reads a " +
			"workspace, not something the last conversation left behind")
	}

	// clearWorkspaceView with no tree must not throw: the fence would otherwise
	// pass for a page that has an assembly tree and break the control on one
	// that does not.
	if run.Alone.Prototype != nil || run.Alone.Variants != 0 || run.Alone.ProjectID != "prj_1" {
		t.Errorf("clearing the workspace without a tree did not behave: %+v", run.Alone)
	}
}

// The functions above could be correct and unwired. The control exists, is a
// real labelled button, and reaches them; the page's send builds its body from
// the same function the harness read; and a restore still on its way cannot
// paint the old conversation over the new one.
func TestWorkbench_TheNewConversationControlIsAButtonWiredToTheRequest(t *testing.T) {
	pages := NewPageHandlers(testDeps())
	rr := httptest.NewRecorder()
	pages.Workbench(rr, httptest.NewRequest(http.MethodGet, "/workbench", nil))
	page := rr.Body.String()
	if len(page) < 200 {
		t.Fatalf("the workbench rendered only %d bytes; this fence would pass vacuously", len(page))
	}
	at := strings.Index(page, `id="new-conversation"`)
	if at < 0 {
		t.Fatal("the workbench has no #new-conversation control: there is no way to start a conversation from scratch")
	}
	open := strings.LastIndex(page[:at], "<")
	end := strings.Index(page[at:], "</button>")
	if open < 0 || end < 0 || !strings.HasPrefix(page[open:], `<button type="button"`) ||
		!strings.Contains(page[at:at+end], ">New conversation") {
		t.Errorf("#new-conversation is not a <button type=\"button\"> labelled \"New conversation\": %.200s", page[open:])
	}
	if talk := strings.Index(page, `id="wb-talk"`); talk < 0 || talk > at || strings.Index(page, `id="transcript"`) < at {
		t.Error("the control is not in the conversation column's header, above the transcript it clears")
	}

	b, err := assetFS.ReadFile("assets/workbench.js")
	if err != nil {
		t.Fatal(err)
	}
	js := codeOnly(string(b))
	// Read inside startNewConversation itself: the transcript is also cleared by
	// Delete, so a file-wide match would pass with this function emptied.
	start, stop := strings.Index(js, "function startNewConversation()"), strings.Index(js, "function initNewConversation()")
	if start < 0 || stop < start {
		t.Fatal("startNewConversation or initNewConversation is gone; this fence reads between them")
	}
	control := js[start:stop]
	for _, want := range []struct{ code, why string }{
		{"var r = beginNewConversation(state, storage());", "the control clears the screen without dropping the conversation id"},
		{"if (r.refused) {", "the control ignores a refusal and clears the screen while a turn is still arriving"},
		{"$('transcript').innerHTML = '';", "the old turns stay on screen in the new conversation"},
		{"clearWorkspaceDOM();", "the transcript is cleared and the previous design, its variants, its " +
			"selection, its section cut and its proposal card are all still on screen"},
	} {
		if !strings.Contains(control, want.code) {
			t.Errorf("startNewConversation no longer has %q: %s", want.code, want.why)
		}
	}
	// And inside clearWorkspaceDOM: the half that empties the SCREEN. The state
	// half is fenced in node above and would be invisible here; this is the
	// canvas, the viewport's own controls and the panels painted from that state.
	dstart := strings.Index(js, "function clearWorkspaceDOM()")
	dstop := strings.Index(js, "function startNewConversation()")
	if dstart < 0 || dstop < dstart {
		t.Fatal("clearWorkspaceDOM is gone, or no longer sits above startNewConversation; this fence reads between them")
	}
	wipe := js[dstart:dstop]
	for _, want := range []struct{ code, why string }{
		{"studio.load(null);", "the previous design is still drawn on the stage, behind an empty transcript"},
		{"studio.isolate(null);", "one subtree of the previous design is still the only thing drawn"},
		{"studio.select(null);", "a part of the previous design is still lit on the canvas"},
		{"studio.setSection('none', 0.5);", "a section cut taken through the previous design is still cutting the " +
			"empty stage, which is what makes a cleared stage look broken rather than empty"},
		{"if (el) el.value = pair[1];", "the viewport's own controls still show the previous design's section, " +
			"explode and transparency, and the assembly search box still holds what was typed into it"},
		{"stopFollowingGoal();", "the page keeps polling a goal whose card it has just cleared"},
		{"if (job && job.watch) job.watch.stop();", "the STEP export jobs whose rail rows have gone are still " +
			"being polled, with nothing on the page to paint them into"},
		{"closeCompare();", "the side-by-side panel of two variants that are no longer in the rail stays open"},
		{"renderVariants();", "the rail still shows the previous conversation's variants"},
		{"renderProposal();", "the proposal card is cleared in state and still painted on screen"},
		{"renderAttachments('');", "a sketch attached in the old conversation is still in the strip above the input"},
		{"setPlace(false);", "the voice surface stays docked in the corner over an empty stage instead of " +
			"returning to the middle, which is where it belongs when there is nothing to look at"},
	} {
		if !strings.Contains(wipe, want.code) {
			t.Errorf("clearWorkspaceDOM no longer has %q: %s", want.code, want.why)
		}
	}
	// ‼️ And what it must NOT touch. These are how this person reads a workspace,
	// not leftovers from a conversation; clearing them would make "new
	// conversation" also mean "forget my viewing preferences".
	for _, keep := range []struct{ code, why string }{
		{"$('grid')", "the grid toggle is reset by a new conversation"},
		{"$('dims')", "the dimensions toggle is reset by a new conversation"},
		{"provenanceOpen", "the provenance banner's folded/open preference is reset by a new conversation"},
	} {
		if strings.Contains(wipe, keep.code) {
			t.Errorf("clearWorkspaceDOM touches %s: %s", keep.code, keep.why)
		}
	}

	for _, want := range []struct{ code, why string }{
		{"safely('new-conversation', initNewConversation)", "boot never binds the control, so the button does nothing"},
		{"btn.addEventListener('click', function () { startNewConversation(); })", "the button does not start a new conversation"},
		{"body: JSON.stringify(converseRequest(state, text, images, fromNodes, describeOnScreen()))",
			"the turn request is no longer built by converseRequest, so the fence above reads a body nothing sends"},
		{"if (state.conversationID !== id) return false;", "a restore still loading paints the old turns into the new conversation"},
		{"if (state.conversationID !== deleting) return;", "a delete finishing late forgets the NEW conversation's id"},
		{"adoptConversation(state, storage(), id);", "the id the server mints is no longer adopted, so every turn starts another conversation"},
	} {
		if !strings.Contains(js, want.code) {
			t.Errorf("workbench.js no longer has %q: %s", want.code, want.why)
		}
	}
}

// What the control SAYS has to be what it did (2026-09-22).
//
// # Why a fence on a sentence
//
// The sentence this replaced was "New conversation. Nothing said before this is
// sent to me. The design and its project are still here." Every word of it was
// true while the workspace was kept. The moment the stage is cleared it becomes
// the interface asserting something the system did not do — and of all the
// things to be wrong about, "your design is still here" is the one that would
// make somebody stop looking for it.
//
// Three facts have to be in it, because each answers a question a person has at
// that exact moment: nothing earlier reaches the model, NOTHING IS DELETED, and
// where the things that left the screen can still be found. The fence reads the
// sentence out of startNewConversation rather than from a rendered page, because
// there is no server-side render of a message the browser composes.
func TestWorkbench_ANewConversationSaysWhatItActuallyDid(t *testing.T) {
	b, err := assetFS.ReadFile("assets/workbench.js")
	if err != nil {
		t.Fatal(err)
	}
	js := codeOnly(string(b))
	start := strings.Index(js, "function startNewConversation()")
	stop := strings.Index(js, "function initNewConversation()")
	if start < 0 || stop < start {
		t.Fatal("startNewConversation or initNewConversation is gone; this fence reads between them")
	}
	said := js[start:stop]

	if strings.Contains(said, "The design and its project are still here") {
		t.Error("the control still says \"The design and its project are still here\". The stage is cleared " +
			"now, so that sentence tells somebody their design is on screen when it is not — and the " +
			"reasonable next thought is that it was lost")
	}
	for _, want := range []struct{ phrase, why string }{
		{"Nothing said before this is sent to me",
			"the one guarantee this control exists for — an empty conversation_id means no earlier turn " +
				"reaches the model — is no longer stated"},
		{"the workspace is", "the message does not mention the workspace being cleared, so an emptied " +
			"stage reads as a page that has lost the design rather than as a new start"},
		{"Nothing is deleted", "the message does not say that nothing was deleted. A cleared stage beside " +
			"silence on that point is indistinguishable from a destructive action"},
		{"Files", "the message does not say where the designs that left the screen still are"},
		{"Conversations in the console", "the message does not say where the conversation just left can be reopened"},
		{"Goals in the console", "the message does not say where a goal whose card was cleared is still followed, " +
			"so a cleared card reads as a cancelled goal"},
	} {
		if !strings.Contains(said, want.phrase) {
			t.Errorf("the new-conversation message no longer contains %q: %s", want.phrase, want.why)
		}
	}
}
