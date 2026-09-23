package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Defining a goal from the browser (2026-09-22).
//
// # What was missing
//
// POST /v1/goals has existed since the first wave. The browser reached it from
// exactly ONE place: the workbench's proposal card, after FORGE had offered a
// goal inside a conversation — which she does only when the conversation happens
// to turn that way. The operations console listed goals and could create none;
// its empty state told the reader to run `forgectl goal new`. So a person who
// arrived knowing what they wanted built either talked her into proposing it or
// left the product.
//
// damon's decision, 2026-09-22: a short New goal form in BOTH the console and
// the workbench — title, statement, risk tier, build-it-as-a-model — with the
// project taken from context; then plan it and show the same card the proposal
// path already shows.
//
// ‼️ Revised 2026-09-23, after he used it: "new goal is at operations page". The
// workbench's copy is gone. It sat at the bottom of the right-hand panel, below
// the assembly tree, the Variants rail, the industry picker and the member list
// — under the fold of any workbench that had built anything, which is every
// workbench somebody would want to define a goal from. The console's form is
// the one that stayed; the workbench keeps the proposal card, which still posts
// the body assets/newgoal.js builds.
//
// # What these fences hold, and the trap they exist for
//
// ‼️ The trap is a form that looks right and disagrees with the server. There are
// five rules on the far side (see assets/newgoal.js) and the browser now states
// four of them for the person filling the form in. A copy that drifts is worse
// than no copy: it disables a button for a value the server accepts, or — much
// worse — sends a body the server refuses and shows the error CODE's general
// words, "One or more request fields failed validation.", to somebody whose
// actual problem is that they are a viewer.
//
// So the harness below runs the shared module in node and reads the BODY it
// builds and the SENTENCE it would show, not the screen. The markup fences check
// that the two forms exist, carry the fields, and are wired to it. The server's
// half — that goal.create is checked against the named project, and which
// projects a person may plan work in — is TestCreateGoal_* and
// TestMyProjectsSaysWhereAGoalMayBeCreated.

const newGoalHarness = `
  const fs = require('fs'), vm = require('vm');
  const sandbox = { window: {}, console };
  vm.createContext(sandbox);
  vm.runInContext(fs.readFileSync(process.argv[2], 'utf8'), sandbox);
  const G = sandbox.window.ForgeNewGoal;
  if (!G || !G.check || !G.body || !G.refusal || !G.writable || !G.TIERS) {
    process.stdout.write(JSON.stringify({ missing: true })); return;
  }
  const out = { tiers: G.TIERS.map((t) => t.tier), glosses: G.TIERS.map((t) => t.gloss),
    maxTitle: G.MAX_TITLE, maxStatement: G.MAX_STATEMENT };

  // The console's form: a project chosen from the list, no industry.
  const consoleForm = { title: 'Shelf bracket', statement: 'A bracket that holds 40kg on a 200mm shelf.',
    risk_tier: 'r2', build: true, project_id: 'prj_1', industry: '' };
  out.consoleBody = G.body(consoleForm);
  out.consoleReady = G.ready(consoleForm);

  // The workbench's form: the project comes from the conversation.
  const inProject = { title: 'Lamp', statement: 'A desk lamp with a weighted base.',
    risk_tier: 'r1', build: false, project_id: 'prj_9', industry: 'civil' };
  out.workbenchBody = G.body(inProject);

  // The workbench's form with no project yet: one is created, in the industry
  // the conversation chose.
  const noProject = { title: 'Lamp', statement: 'A desk lamp.', risk_tier: 'r1', build: false,
    project_id: '', industry: 'civil' };
  out.newProjectBody = G.body(noProject);

  // Autonomy is never a field on either.
  out.fields = Object.keys(G.body(consoleForm)).sort();

  // What the button's state comes from.
  out.refusals = {};
  const cases = {
    empty: { title: '', statement: '', risk_tier: 'r1', project_id: 'prj_1' },
    noStatement: { title: 'Lamp', statement: '   ', risk_tier: 'r1', project_id: 'prj_1' },
    noTitle: { title: '  ', statement: 'A lamp.', risk_tier: 'r1', project_id: 'prj_1' },
    longTitle: { title: 'x'.repeat(G.MAX_TITLE + 1), statement: 'A lamp.', risk_tier: 'r1', project_id: 'prj_1' },
    longStatement: { title: 'Lamp', statement: 'x'.repeat(G.MAX_STATEMENT + 1), risk_tier: 'r1', project_id: 'prj_1' },
    atTheLimit: { title: 'x'.repeat(G.MAX_TITLE), statement: 'x'.repeat(G.MAX_STATEMENT), risk_tier: 'r1', project_id: 'prj_1' },
    badTier: { title: 'Lamp', statement: 'A lamp.', risk_tier: 'r9', project_id: 'prj_1' },
    prohibitedTier: { title: 'Lamp', statement: 'A lamp.', risk_tier: 'r5', project_id: 'prj_1' },
    industryWithProject: { title: 'Lamp', statement: 'A lamp.', risk_tier: 'r1', project_id: 'prj_1', industry: 'civil' },
    good: { title: 'Lamp', statement: 'A lamp.', risk_tier: 'r1', project_id: 'prj_1' },
    noTierGiven: { title: 'Lamp', statement: 'A lamp.', risk_tier: '', project_id: 'prj_1' }
  };
  for (const name of Object.keys(cases)) {
    out.refusals[name] = { why: G.check(cases[name]), ready: G.ready(cases[name]) };
  }

  // A refused request, read the way the forms read it.
  out.said = {
    detail: G.refusal({ code: 'FORBIDDEN', message: 'One or more request fields failed validation.',
      remedy: 'Correct the fields named in the details array and resubmit.',
      details: { detail: 'viewer cannot goal.create here - a viewer reads. Ask an owner to change your role.' } }, 403),
    notFound: G.refusal({ code: 'NOT_FOUND', message: 'The requested resource does not exist.',
      details: { detail: 'no project prj_stranger' } }, 404),
    noDetail: G.refusal({ message: 'The requested resource does not exist.', remedy: 'Check the id.' }, 404),
    nothing: G.refusal(null, 500)
  };

  // Which projects the form may offer, from the SERVER's answer.
  out.offered = G.writable([
    { id: 'prj_own', can_create_goal: true },
    { id: 'prj_view', can_create_goal: false },
    { id: 'prj_old' }
  ]).map((p) => p.id);

  process.stdout.write(JSON.stringify(out));
`

type ngCase struct {
	Why   string `json:"why"`
	Ready bool   `json:"ready"`
}

type ngBody struct {
	Title     string `json:"title"`
	Statement string `json:"statement"`
	RiskTier  string `json:"risk_tier"`
	ProjectID string `json:"project_id"`
	Industry  string `json:"industry"`
	Build     bool   `json:"build"`
}

type ngRun struct {
	Missing        bool              `json:"missing"`
	Tiers          []string          `json:"tiers"`
	Glosses        []string          `json:"glosses"`
	MaxTitle       int               `json:"maxTitle"`
	MaxStatement   int               `json:"maxStatement"`
	ConsoleBody    ngBody            `json:"consoleBody"`
	ConsoleReady   bool              `json:"consoleReady"`
	WorkbenchBody  ngBody            `json:"workbenchBody"`
	NewProjectBody ngBody            `json:"newProjectBody"`
	Fields         []string          `json:"fields"`
	Refusals       map[string]ngCase `json:"refusals"`
	Said           map[string]string `json:"said"`
	Offered        []string          `json:"offered"`
}

func runNewGoal(t *testing.T) ngRun {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node on PATH; skipping the new goal form fence")
	}
	src, err := assetFS.ReadFile("assets/newgoal.js")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	asset, harness := filepath.Join(dir, "newgoal.js"), filepath.Join(dir, "run.js")
	for path, data := range map[string][]byte{asset: src, harness: []byte(newGoalHarness)} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(node, harness, asset)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("the goal form module could not be driven: %v %s", err, stderr.String())
	}
	var run ngRun
	if err := json.Unmarshal(out, &run); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if run.Missing {
		t.Fatal("assets/newgoal.js exports no ForgeNewGoal.check/body/refusal/writable/TIERS: there is " +
			"nothing for either form to validate or build a request with")
	}
	return run
}

// The body both forms send is the one the server's rules are written about.
//
// ‼️ project_id is the assertion that matters. Omitting it is TWO bugs at once:
// Draft's EnsureProject makes a brand new project named after the goal's title
// (the 2026-09-15 filing bug), and the goal.create check in CreateGoal — which
// only runs when a project is named — never happens. industry alongside it is
// refused by the server rather than dropped, so the pair must never be sent.
func TestNewGoalForm_SendsTheProjectAndNeverAnIndustryWithIt(t *testing.T) {
	run := runNewGoal(t)

	if run.ConsoleBody.ProjectID != "prj_1" {
		t.Errorf("the console's form sends project_id %q. Without it the goal is drafted into a NEW project "+
			"named after its own title, and the goal.create permission check never runs",
			run.ConsoleBody.ProjectID)
	}
	if run.WorkbenchBody.ProjectID != "prj_9" {
		t.Errorf("the workbench's form sends project_id %q; it must be the conversation's project, or the goal "+
			"is filed away from the design it is about", run.WorkbenchBody.ProjectID)
	}
	if run.WorkbenchBody.Industry != "" {
		t.Errorf("the form sent industry %q together with a project id. agent.Intake.Draft REFUSES the pair — "+
			"the industry belongs to the project — so every goal added to an existing project would be an error",
			run.WorkbenchBody.Industry)
	}
	// And the one case where an industry is right: there is no project yet, so
	// one is created, and the industry is what it is created in.
	if run.NewProjectBody.ProjectID != "" || run.NewProjectBody.Industry != "civil" {
		t.Errorf("with no project yet the form sent project_id=%q industry=%q; want an empty project and the "+
			"chosen industry, or the project this goal creates is filed as `general` whatever was picked",
			run.NewProjectBody.ProjectID, run.NewProjectBody.Industry)
	}
	if run.ConsoleBody.Title != "Shelf bracket" || run.ConsoleBody.Statement == "" {
		t.Errorf("the title or statement did not reach the body: %+v", run.ConsoleBody)
	}
	if run.ConsoleBody.RiskTier != "r2" {
		t.Errorf("risk_tier = %q; the tier chosen on the form is the goal's ceiling and must be the one sent",
			run.ConsoleBody.RiskTier)
	}
	if !run.ConsoleBody.Build {
		t.Error("build was dropped, so \"build it as a model\" would plan ordinary tasks for the executor " +
			"instead of steps the CAD kernel builds")
	}

	// ‼️ Autonomy is not a field, and must not become one here. Migration
	// 0028_autonomy_is_write_once installs a trigger refusing every write to the
	// column (PRD AGT-04), so a form that offered it would offer a value that can
	// never be changed, beside four that can.
	for _, f := range run.Fields {
		if f == "autonomy" {
			t.Error("the form sends an autonomy field. Autonomy is fixed at creation — migration 0028 " +
				"installs a trigger that refuses any write to the column — so offering it on a form " +
				"among settings that CAN be revisited misrepresents what it is")
		}
	}
	want := []string{"build", "industry", "project_id", "risk_tier", "statement", "title"}
	if strings.Join(run.Fields, ",") != strings.Join(want, ",") {
		t.Errorf("the request body's fields are %v; want exactly %v. A field the server does not declare is "+
			"rejected by DecodeJSON as unknown, and the reply is about JSON rather than about the goal",
			run.Fields, want)
	}
}

// The browser refuses what the server would refuse, in the server's words.
//
// Each sentence below is agent.Intake.Draft's or CreateGoal's own. A person who
// fixes the form and submits anyway must not be told two different things by the
// two halves of the same rule.
func TestNewGoalForm_ChecksWhatTheServerChecks(t *testing.T) {
	run := runNewGoal(t)

	if run.MaxTitle != maxGoalTitle || run.MaxStatement != maxGoalStatement {
		t.Errorf("the form's limits are title %d / statement %d; the server's are %d / %d. The counter under "+
			"each box would then promise a length the server rejects",
			run.MaxTitle, run.MaxStatement, maxGoalTitle, maxGoalStatement)
	}

	for _, c := range []struct {
		name, contains, why string
	}{
		{"empty", "a goal needs both a title and a statement",
			"an empty form is submittable, so pressing the button spends a round trip to be told nothing"},
		{"noStatement", "a goal needs both a title and a statement",
			"a statement of only spaces passes; the server trims it and refuses"},
		{"noTitle", "a goal needs both a title and a statement", "a title of only spaces passes"},
		{"longTitle", "at most", "a title over the column's width is submittable"},
		{"longStatement", "at most", "a statement over the column's width is submittable"},
		{"badTier", "is not recognised", "an unrecognised tier is submittable"},
		{"prohibitedTier", "is not recognised",
			"r5 is offered or accepted. PRD §8.1 makes r5 PROHIBITED — refused, not gated — and " +
				"`forgectl goal new --risk` offers r0 to r4, so a form that takes r5 disagrees with both"},
		{"industryWithProject", "the industry belongs to the project",
			"an industry alongside a project id passes the browser and is refused by the server"},
	} {
		got := run.Refusals[c.name]
		if got.Why == "" || !strings.Contains(got.Why, c.contains) {
			t.Errorf("%s: the form says %q; want a reason containing %q — %s", c.name, got.Why, c.contains, c.why)
		}
		if got.Ready {
			t.Errorf("%s: the submit button is ENABLED for a body the server refuses — %s", c.name, c.why)
		}
	}

	// And the other side: a valid goal must not be blocked by a browser rule the
	// server does not have. A check stricter than the server is a feature nobody
	// can reach and nothing reports.
	for _, name := range []string{"good", "atTheLimit", "noTierGiven"} {
		if got := run.Refusals[name]; !got.Ready || got.Why != "" {
			t.Errorf("%s: the form refuses a goal the server would accept (%q). A browser check stricter than "+
				"the server's is a rule nobody can see and nothing enforces", name, got.Why)
		}
	}

	// The tiers offered are the CLI's, with what each one means beside it.
	if strings.Join(run.Tiers, ",") != "r0,r1,r2,r3,r4" {
		t.Errorf("the form offers tiers %v; `forgectl goal new --risk` offers r0 | r1 | r2 | r3 | r4, and the "+
			"two surfaces must not disagree about what a person may choose", run.Tiers)
	}
	for i, g := range run.Glosses {
		if strings.TrimSpace(g) == "" {
			t.Errorf("tier %s is offered with no gloss. A bare letter in a dropdown is a number somebody "+
				"guesses at; the ceiling they pick decides what the goal may do", run.Tiers[i])
		}
	}
}

// A refusal arrives in the words written for THAT refusal.
//
// # What this catches
//
// The envelope carries the error CODE's general words in `message` and the
// sentence written for this refusal in `details.detail`. Both forms' api()
// helpers threw `message` alone — so a viewer who pressed the button was told
// "One or more request fields failed validation." and nothing about their role,
// and somebody naming a project they are not in was told a field was wrong
// rather than that there is no such project. Neither is something a person can
// act on, and neither is what happened.
func TestNewGoalForm_ShowsTheServersOwnRefusal(t *testing.T) {
	run := runNewGoal(t)

	if !strings.Contains(run.Said["detail"], "viewer cannot goal.create here") {
		t.Errorf("a refused create reads as %q. The sentence written for the refusal is in details.detail; "+
			"showing the code's general words instead tells a viewer some field was wrong",
			run.Said["detail"])
	}
	if strings.Contains(run.Said["detail"], "One or more request fields failed validation") {
		t.Errorf("the refusal still carries the error code's general words: %q", run.Said["detail"])
	}
	if !strings.Contains(run.Said["notFound"], "no project prj_stranger") {
		t.Errorf("naming a project the caller is not in reads as %q; the server says which project it could "+
			"not find, which is the only actionable part", run.Said["notFound"])
	}
	// A refusal with no detail still has to say something, and a total absence of
	// an envelope must not render as "undefined".
	if run.Said["noDetail"] == "" || !strings.Contains(run.Said["noDetail"], "Check the id.") {
		t.Errorf("a refusal with no detail reads as %q; want the message and its remedy", run.Said["noDetail"])
	}
	if run.Said["nothing"] == "" || strings.Contains(run.Said["nothing"], "undefined") {
		t.Errorf("a refusal with no body at all reads as %q", run.Said["nothing"])
	}

	// Both api() helpers must actually use it, or the sentences above are read by
	// nothing.
	cb, err := assetFS.ReadFile("assets/console.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(codeOnly(string(cb)), "window.ForgeNewGoal.refusal(e, r.status)") {
		t.Error("console.js no longer reads the refusal through ForgeNewGoal.refusal, so a refused goal " +
			"shows the error code's general words and a viewer is told some field was wrong")
	}

	// ‼️ Read inside the workbench's api() and nowhere else. This exact line —
	// `new Error(refusalText(e, r.status, 'Request failed'))` — also appears in
	// watchExport's poll, so a file-wide search finds one when the other has been
	// taken out, and the fence stayed green while every POST on the page went back
	// to the error code's general words. Caught by its own drill, 2026-09-22.
	wb, err := assetFS.ReadFile("assets/workbench.js")
	if err != nil {
		t.Fatal(err)
	}
	wjs := codeOnly(string(wb))
	astart := strings.Index(wjs, "function api(path, body)")
	astop := strings.Index(wjs, "function goalRequest()")
	if astart < 0 || astop < astart {
		t.Fatal("workbench.js has no api(path, body) above goalRequest(); this fence reads between them")
	}
	if !strings.Contains(wjs[astart:astop], "refusalText(e, r.status, 'Request failed')") {
		t.Error("the workbench's api() no longer reads the refusal through refusalText, so every POST it " +
			"makes — /v1/goals among them — reports the error code's general words instead of the " +
			"sentence written for that refusal")
	}
	if !strings.Contains(wjs[astart:astop], "err.status = r.status;") {
		t.Error("the workbench's api() no longer carries the HTTP status on the error it throws")
	}
}

// The console's form offers only the projects the SERVER says work may be
// planned in.
//
// A permission matrix copied into the browser is the copy that goes stale: the
// day a role's permissions change it either offers a project whose create will
// be refused, or hides one that would have worked, and nothing says so. The
// answer comes from GET /v1/projects, computed where access.Role.Allows lives.
func TestNewGoalForm_OffersOnlyProjectsTheServerSaysAreWritable(t *testing.T) {
	run := runNewGoal(t)

	if strings.Join(run.Offered, ",") != "prj_own" {
		t.Errorf("the form offers %v. It must offer exactly the projects whose can_create_goal is true: a "+
			"project offered here and refused on submit is the form promising something the server will "+
			"not do, and a row with no field at all is one this build cannot vouch for", run.Offered)
	}

	// And the filter must be the server's field, not a role comparison here.
	b, err := assetFS.ReadFile("assets/newgoal.js")
	if err != nil {
		t.Fatal(err)
	}
	js := codeOnly(string(b))
	if !strings.Contains(js, "p.can_create_goal === true") {
		t.Error("the writable filter no longer reads can_create_goal from the server's answer")
	}
	for _, role := range []string{"'contributor'", "'maintainer'", "'viewer'", "'owner'"} {
		if strings.Contains(js, role) {
			t.Errorf("newgoal.js compares a role (%s) to decide what may be offered. The role/permission "+
				"matrix lives in internal/domain/access/model.go; a second copy here is the copy that "+
				"goes stale", role)
		}
	}
}

// The form is on the operations console, and on no other surface (2026-09-23).
//
// # Why the workbench's copy came out
//
// It shipped on both surfaces on 2026-09-22. On the workbench it landed at the
// bottom of the right-hand panel — under the assembly tree, the Variants rail,
// the industry picker and the member list — which is below the fold of any
// workbench that has built something, and every workbench somebody would want to
// define a goal from has built something. damon, after using it: "new goal is at
// operations page". So the console's copy is the only one, where the Goals card
// is on the page as served and the project is chosen out of a picker rather than
// inherited from a conversation.
//
// # Why the disabled state is fenced in the markup
//
// The button is enabled by the module's own check, on input. If the markup ships
// it enabled, the very first press — before anything has been typed and before
// any handler has run — posts an empty goal and the person meets the server's
// refusal instead of the form's own sentence. It has to be disabled AS SERVED.
//
// ‼️ The workbench half of this fence is an ABSENCE, and an absence passes
// vacuously against an empty render. Both halves read a real rendered page and
// both refuse to run against a short one.
func TestNewGoalForm_IsOnTheOperationsPageAndNotTheWorkbench(t *testing.T) {
	pages := NewPageHandlers(testDeps())

	rr := httptest.NewRecorder()
	pages.Console(rr, httptest.NewRequest(http.MethodGet, "/console", nil))
	page := rr.Body.String()
	if len(page) < 200 {
		t.Fatalf("the console rendered only %d bytes; this fence would pass vacuously", len(page))
	}

	if !strings.Contains(page, `id="newgoal-form"`) {
		t.Fatal("the console has no #newgoal-form: there is now no way at all to define a goal from the " +
			"browser except by talking FORGE into proposing one, which is the whole of what was missing")
	}
	for _, id := range []string{"newgoal-open", "newgoal-project", "newgoal-title", "newgoal-statement",
		"newgoal-risk", "newgoal-build", "newgoal-go", "newgoal-cancel"} {
		if !strings.Contains(page, `id="`+id+`"`) {
			t.Errorf("the console is missing #%s from the new goal form", id)
		}
	}
	// Autonomy is not a field.
	if strings.Contains(page, `id="newgoal-autonomy"`) {
		t.Error("the console offers an autonomy control. It is fixed at creation and migration 0028 " +
			"refuses every write to the column")
	}
	// The submit button must be served disabled.
	at := strings.Index(page, `id="newgoal-go"`)
	open := strings.LastIndex(page[:at], "<")
	end := strings.Index(page[at:], ">")
	if at < 0 || open < 0 || end < 0 || !strings.Contains(page[at:at+end], "disabled") {
		t.Errorf("the console serves #newgoal-go enabled. The first press, before anything is typed, "+
			"would post an empty goal: %.160s", page[open:])
	}
	// And the form must be served closed, so it is not a wall of fields on a
	// page somebody opened to read a timeline.
	formAt := strings.Index(page, `id="newgoal-form"`)
	formOpen := strings.LastIndex(page[:formAt], "<")
	formEnd := strings.Index(page[formAt:], ">")
	if !strings.Contains(page[formOpen:formAt+formEnd], "hidden") {
		t.Errorf("the console serves the new goal form open: %.200s", page[formOpen:])
	}
	// The shared module has to be loaded BEFORE the page script that calls it.
	mod := strings.Index(page, "newgoal.js")
	script := strings.Index(page, "console.js")
	if mod < 0 {
		t.Error("the console does not load newgoal.js, so every rule the form checks is undefined")
	} else if script >= 0 && mod > script {
		t.Error("the console loads newgoal.js AFTER console.js; ForgeNewGoal would be undefined when the " +
			"form is wired")
	}

	// ‼️ And the workbench carries none of it. Not a hidden copy, not a copy
	// behind a breakpoint: the markup is gone, so there is nothing to scroll to
	// under the member list and nothing for a fence on the console's copy to
	// match by accident.
	wrr := httptest.NewRecorder()
	pages.Workbench(wrr, httptest.NewRequest(http.MethodGet, "/workbench", nil))
	wb := wrr.Body.String()
	if len(wb) < 200 {
		t.Fatalf("the workbench rendered only %d bytes; the absence below would pass vacuously", len(wb))
	}
	for _, id := range []string{"newgoal-head", "newgoal", "newgoal-form", "newgoal-open", "newgoal-where",
		"newgoal-title", "newgoal-statement", "newgoal-risk", "newgoal-build", "newgoal-go",
		"newgoal-cancel", "newgoal-why"} {
		if strings.Contains(wb, `id="`+id+`"`) {
			t.Errorf("the workbench still serves #%s. The goal form was moved to the operations page "+
				"because on this surface it sits under the assembly tree, the variants, the industry "+
				"and the people — where nobody finds it", id)
		}
	}
	// The proposal card is what this surface keeps, and it has to still be here:
	// removing the form must not have taken the path that was always there.
	for _, id := range []string{"proposal-head", "proposal"} {
		if !strings.Contains(wb, `id="`+id+`"`) {
			t.Errorf("the workbench no longer serves #%s: taking the form out took the proposal card "+
				"with it, and now nothing on this surface can accept work FORGE offers", id)
		}
	}

	// The console's script must reach the module rather than re-deriving any of
	// it, and must post the body it builds.
	for _, f := range []struct{ asset, want, why string }{
		{"assets/console.js", "JSON.stringify(G.body(f))",
			"the console builds its own request body, so the five rules the server holds have a second copy"},
		{"assets/console.js", "api('/v1/goals', {",
			"the console no longer posts to /v1/goals, so its form creates nothing"},
		{"assets/console.js", "G.writable(state.projects)",
			"the console's project list is no longer filtered by what the server says is writable"},
		// The workbench still POSTs /v1/goals from the proposal card, through the
		// SAME module — which is why newgoal.js is still loaded there.
		{"assets/workbench.js", "window.ForgeNewGoal.body({",
			"the workbench builds the goal request itself again, which is how project_id went missing in September"},
	} {
		b, err := assetFS.ReadFile(f.asset)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(codeOnly(string(b)), f.want) {
			t.Errorf("%s no longer contains %q: %s", f.asset, f.want, f.why)
		}
	}

	// ‼️ And the workbench's wiring for a form it no longer serves is gone too. A
	// page script that still binds #newgoal-* is one template edit away from the
	// form coming back by accident, and renderNewGoal() left on the paths that
	// adopt a project is dead work run on every conversation.
	b, err := assetFS.ReadFile("assets/workbench.js")
	if err != nil {
		t.Fatal(err)
	}
	wjs := codeOnly(string(b))
	for _, gone := range []struct{ code, why string }{
		{"safely('new-goal', initNewGoal)", "boot still wires a new goal form the workbench does not serve"},
		{"function initNewGoal()", "the workbench still binds the form's open, cancel, submit and input handlers"},
		{"function renderNewGoal()", "the workbench still renders a form it does not serve"},
		{"function newGoalFields()", "the workbench still reads fields that are not on the page"},
		{"function newGoalWhere()", "the workbench still composes the \"where this goes\" line for a form " +
			"that is gone; the console's picker is what names the project now"},
		{"renderNewGoal();", "a call to renderNewGoal survives on one of the paths that adopt a project, " +
			"so every new conversation runs dead work"},
	} {
		if strings.Contains(wjs, gone.code) {
			t.Errorf("workbench.js still contains %q: %s", gone.code, gone.why)
		}
	}
}

// The console's form says which project it will write into, and says why when it
// cannot write into any.
//
// The 2026-09-15 bug — a goal drafted into a brand new project named after its
// own title, with its artifacts filed away from the conversation that made them
// — was invisible precisely because nothing on screen answered "where is this
// going". This form has no conversation to take a project from, so the answer is
// a picker: it is named, it is chosen, and it is sent. A submit with no project
// is refused here rather than left to EnsureProject to invent one.
func TestNewGoalForm_TheConsoleSaysWhichProjectItWritesInto(t *testing.T) {
	rr := httptest.NewRecorder()
	NewPageHandlers(testDeps()).Console(rr, httptest.NewRequest(http.MethodGet, "/console", nil))
	page := rr.Body.String()
	if !strings.Contains(page, `id="newgoal-project"`) {
		t.Fatal("the console's new goal form has no #newgoal-project control, so nothing on screen says " +
			"which project the goal is filed under")
	}
	if !strings.Contains(page, `<label for="newgoal-project">Project</label>`) {
		t.Error("the project control has no label reading \"Project\", so the select is an unnamed list of " +
			"names — and which of the four fields it is has to be guessed")
	}
	if !strings.Contains(page, `id="newgoal-none"`) {
		t.Error("there is nowhere to say WHY a goal cannot be created, so somebody in no project, or " +
			"holding a role that does not plan work, meets a disabled button and no sentence")
	}

	b, err := assetFS.ReadFile("assets/console.js")
	if err != nil {
		t.Fatal(err)
	}
	js := codeOnly(string(b))
	for _, want := range []struct{ code, why string }{
		{"project_id: $('newgoal-project') ? $('newgoal-project').value : '',",
			"the chosen project is not read out of the picker, so what is sent is not what was named"},
		{"var mine = G.writable(state.projects);",
			"the picker is not filled from the projects the server says work may be planned in"},
		{"pick.innerHTML = mine.map(function (p) {",
			"the picker is never filled, so it names no project and the form writes into none"},
		{"esc(p.name)", "the picker lists ids rather than the names the rest of the page uses"},
		{"if (!G || G.check(f) || !f.project_id || state.creating) { renderNewGoal(); return; }",
			"a submit with no project chosen is sent, and Draft's EnsureProject invents a new project " +
				"named after the goal's title — the 2026-09-15 filing bug, exactly"},
		{"none.textContent = state.projects.length",
			"the two reasons a goal cannot be created here — no project at all, or a role that does not " +
				"plan work — are no longer told apart, and neither is stated"},
	} {
		if !strings.Contains(js, want.code) {
			t.Errorf("console.js no longer contains %q: %s", want.code, want.why)
		}
	}
}
