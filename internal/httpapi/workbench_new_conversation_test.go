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
  if (!C || !C.begin || !C.adopt || !C.request) { process.stdout.write(JSON.stringify({ missing: true })); return; }

  const store = (init) => {
    const m = Object.assign({}, init);
    return { m, getItem: (k) => (k in m ? m[k] : null), setItem: (k, v) => { m[k] = String(v); }, removeItem: (k) => { delete m[k]; } };
  };
  const onScreen = 'Bracket — 2 part(s): Plate [id: plate], Rib [id: rib] (units: mm)';
  const page = (extra) => Object.assign({ conversationID: 'cnv_old', projectID: 'prj_1', industry: 'general',
    busy: false, goalPhase: 'none', proposal: null, goal: null, planTasks: null }, extra || {});
  const keys = (id) => ({ [C.key]: id, 'forge.workbench.project': 'prj_1' });
  const out = {};

  // An ordinary conversation, then "New conversation", then the first send, then
  // the server's conversation event, then the second send.
  let s = page(), st = store(keys('cnv_old'));
  out.before = C.request(s, 'the code name is Blue Heron', [], [], onScreen);
  out.begun = C.begin(s, st);
  out.first = C.request(s, 'start over: a shelf bracket', [], [], onScreen);
  out.firstStored = st.m[C.key] === undefined ? null : st.m[C.key];
  out.projectStored = st.m['forge.workbench.project'] || null;
  C.adopt(s, st, 'cnv_new');
  out.second = C.request(s, 'make it 4mm thick', [], [], onScreen);
  out.secondStored = st.m[C.key] || null;

  // Refused while something is in flight: nothing changes.
  out.refused = {};
  for (const [name, extra] of [['streaming', { busy: true }], ['planning', { goalPhase: 'planning' }], ['starting', { goalPhase: 'starting' }]]) {
    const p = page(extra), ps = store(keys('cnv_old'));
    const r = C.begin(p, ps);
    out.refused[name] = { reason: r.refused || '', id: p.conversationID, stored: ps.m[C.key] || null,
      request: C.request(p, 'x', [], [], '').conversation_id };
  }

  // A bare proposal goes with the old conversation; a card with a goal behind it stays.
  const bare = page({ goalPhase: 'proposed', proposal: { title: 'Lamp' } });
  C.begin(bare, store(keys('cnv_old')));
  const running = page({ goalPhase: 'active', proposal: { title: 'Lamp' }, goal: { id: 'gol_1' }, planTasks: [{}] });
  C.begin(running, store(keys('cnv_old')));
  out.bare = { phase: bare.goalPhase, proposal: !!bare.proposal };
  out.running = { phase: running.goalPhase, proposal: !!running.proposal, goal: !!running.goal };

  process.stdout.write(JSON.stringify(out));
`

type ncRequest struct {
	Message        string `json:"message"`
	ProjectID      string `json:"project_id"`
	ConversationID string `json:"conversation_id"`
	OnScreen       string `json:"on_screen"`
}

type ncRun struct {
	Missing bool      `json:"missing"`
	Before  ncRequest `json:"before"`
	Begun   struct {
		Previous string `json:"previous"`
		Refused  string `json:"refused"`
	} `json:"begun"`
	First         ncRequest `json:"first"`
	FirstStored   *string   `json:"firstStored"`
	ProjectStored *string   `json:"projectStored"`
	Second        ncRequest `json:"second"`
	SecondStored  *string   `json:"secondStored"`
	Refused       map[string]struct {
		Reason  string  `json:"reason"`
		ID      string  `json:"id"`
		Stored  *string `json:"stored"`
		Request string  `json:"request"`
	} `json:"refused"`
	Bare struct {
		Phase    string `json:"phase"`
		Proposal bool   `json:"proposal"`
	} `json:"bare"`
	Running struct {
		Phase    string `json:"phase"`
		Proposal bool   `json:"proposal"`
		Goal     bool   `json:"goal"`
	} `json:"running"`
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
		t.Fatal("workbench.js exports no ForgeConversation.begin/adopt/request: there is nothing that starts a new conversation")
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
	if run.First.OnScreen == "" {
		t.Error("the new conversation's first turn no longer describes the design on screen, so \"make that " +
			"taller\" has nothing to refer to")
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
	if run.Bare.Phase != "none" || run.Bare.Proposal {
		t.Errorf("a proposal nothing was created from stayed on the card (%+v); it belonged to the old conversation", run.Bare)
	}
	if run.Running.Phase != "active" || !run.Running.Proposal || !run.Running.Goal {
		t.Errorf("a running goal's card was cleared (%+v); it is work in the project and its progress must stay visible",
			run.Running)
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
	} {
		if !strings.Contains(control, want.code) {
			t.Errorf("startNewConversation no longer has %q: %s", want.code, want.why)
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
