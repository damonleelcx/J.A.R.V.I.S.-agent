package httpapi

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The proposal card follows the goal it started (2026-09-15).
//
// A live build started from the workbench card showed "Started." and nothing
// more: the steps, the versions they kept and how it ended were only in the
// operations console. The card now polls the goal and its timeline until the goal
// settles. This runs the workbench's own code in node — loaded exactly as a page
// loads it, the way forge3d.js is fenced — against stubbed endpoint replies, and
// counts the requests, because a poller that never stops looks the same on screen
// as one that does.

type progressPoll struct {
	Status int              `json:"status,omitempty"`
	Goal   map[string]any   `json:"goal,omitempty"`
	Tasks  []map[string]any `json:"tasks,omitempty"`
	Events []map[string]any `json:"events,omitempty"`
}

type progressSeen struct {
	Done    int  `json:"done"`
	Total   int  `json:"total"`
	Settled bool `json:"settled"`
	Current *struct {
		Title  string `json:"title"`
		Status string `json:"status"`
	} `json:"current"`
	Kept []string `json:"kept"`
	Stop *struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	} `json:"stop"`
	HTML string `json:"html"`
}

type progressRun struct {
	Missing     bool                   `json:"missing"`
	Seen        []progressSeen         `json:"seen"`
	Errors      []struct{ Status int } `json:"errors"`
	Requested   []string               `json:"requested"`
	Delays      []int                  `json:"delays"`
	PendingLeft bool                   `json:"pendingLeft"`
	Interval    int                    `json:"interval"`
}

const progressHarness = `
  const fs = require('fs'), vm = require('vm');
  const sandbox = { window: {}, document: { readyState: 'loading', addEventListener() {} }, console };
  vm.createContext(sandbox);
  vm.runInContext(fs.readFileSync(process.argv[2], 'utf8'), sandbox);
  const G = sandbox.window.ForgeGoalProgress;
  const polls = JSON.parse(fs.readFileSync(process.argv[3], 'utf8'));
  if (!G) { process.stdout.write(JSON.stringify({ missing: true })); return; }
  (async () => {
    let poll = 0, pending = null;
    const out = { seen: [], errors: [], requested: [], delays: [], interval: G.interval };
    const fetch = (url) => {
      out.requested.push(url);
      const r = polls[Math.min(poll, polls.length - 1)];
      const status = r.status || 200;
      const body = status !== 200 ? { error: { message: 'refused ' + status } }
        : url.endsWith('/timeline') ? { events: r.events || [] } : { goal: r.goal, tasks: r.tasks || [] };
      return Promise.resolve({ ok: status === 200, status, json: () => Promise.resolve(body) });
    };
    const flush = () => new Promise((res) => setImmediate(res));
    G.watch('gol_1', {
      fetch,
      setTimeout: (fn, ms) => { out.delays.push(ms); pending = fn; return out.delays.length; },
      clearTimeout: () => { pending = null; },
      onProgress: (p) => out.seen.push({ done: p.done, total: p.total, settled: p.settled,
        current: p.current, kept: p.kept, stop: p.stop, html: G.html(p, true) }),
      onError: (e) => out.errors.push({ status: e.status })
    });
    // Twenty ticks at most: a poller that never stops reads the last reply again
    // and again, and the request count says so.
    for (let i = 0; i < 20; i++) {
      await flush(); await flush();
      if (!pending) break;
      const fn = pending; pending = null; poll++; fn();
    }
    await flush(); await flush();
    out.pendingLeft = !!pending;
    process.stdout.write(JSON.stringify(out));
  })();
`

func runProgress(t *testing.T, polls []progressPoll) progressRun {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node on PATH; skipping the workbench goal-progress fence")
	}
	src, err := assetFS.ReadFile("assets/workbench.js")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	asset, harness, input := filepath.Join(dir, "workbench.js"), filepath.Join(dir, "run.js"), filepath.Join(dir, "polls.json")
	body, _ := json.Marshal(polls)
	for path, data := range map[string][]byte{asset: src, harness: []byte(progressHarness), input: body} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(node, harness, asset, input)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("the workbench could not be driven: %v %s", err, stderr.String())
	}
	var run progressRun
	if err := json.Unmarshal(out, &run); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if run.Missing {
		t.Fatal("workbench.js exports no ForgeGoalProgress: the card has nothing that follows a started goal")
	}
	return run
}

func step(id, title, status string) map[string]any {
	return map[string]any{"id": id, "title": title, "status": status}
}

func event(kind, summary string) map[string]any {
	return map[string]any{"kind": kind, "summary": summary}
}

// A build being built, then finished: each poll says where it is, and the card
// stops reading once the goal has settled.
func TestWorkbench_TheCardShowsABuildsProgressAndStopsWhenItSettles(t *testing.T) {
	run := runProgress(t, []progressPoll{
		{Goal: map[string]any{"id": "gol_1", "status": "active", "tasks_total": 3, "tasks_done": 1,
			"tokens_spent": 11158, "max_tokens": 100000},
			Tasks: []map[string]any{step("t1", "Step 1 of 3 — Base", "succeeded"),
				step("t2", "Step 2 of 3 — Arm", "running"), step("t3", "Step 3 of 3 — Shade", "pending")},
			Events: []map[string]any{event("task.started", "Started."),
				event("task.succeeded", "Step 1 of 3 (Base): 1 part(s), kept as version ver_01A."),
				event("plan.created", "Plan v1")}},
		{Goal: map[string]any{"id": "gol_1", "status": "succeeded", "tasks_total": 3, "tasks_done": 3,
			"tokens_spent": 33000, "outcome_summary": "All 3 task(s) finished: 3 succeeded, 0 skipped."},
			Tasks: []map[string]any{step("t1", "Step 1 of 3 — Base", "succeeded"),
				step("t2", "Step 2 of 3 — Arm", "succeeded"), step("t3", "Step 3 of 3 — Shade", "succeeded")},
			Events: []map[string]any{event("goal.ended", "All 3 task(s) finished: 3 succeeded, 0 skipped."),
				event("task.succeeded", "Step 3 of 3 (Shade): 3 part(s), kept as version ver_03C."),
				event("task.succeeded", "Step 2 of 3 (Arm): 2 part(s), kept as version ver_02B."),
				event("task.succeeded", "Step 1 of 3 (Base): 1 part(s), kept as version ver_01A.")}},
	})

	if len(run.Seen) != 2 {
		t.Fatalf("the card was told about %d poll(s) of 2: %+v", len(run.Seen), run)
	}
	first := run.Seen[0]
	if first.Done != 1 || first.Total != 3 || first.Settled || first.Current == nil ||
		!strings.HasPrefix(first.Current.Title, "Step 2 of 3") || strings.Join(first.Kept, ",") != "ver_01A" {
		t.Errorf("mid-build, the card reads %+v", first)
	}
	for _, want := range []string{"1 of 3 steps done", "11,158 of 100,000 tokens", "Now: Step 2 of 3", "1 version kept", "ver_01A"} {
		if !strings.Contains(first.HTML, want) {
			t.Errorf("mid-build, the card does not show %q:\n%s", want, first.HTML)
		}
	}
	last := run.Seen[1]
	if !last.Settled || last.Current != nil || strings.Join(last.Kept, ",") != "ver_01A,ver_02B,ver_03C" {
		t.Errorf("finished, the card reads %+v", last)
	}
	if !strings.Contains(last.HTML, "3 of 3 steps done") || !strings.Contains(last.HTML, "All 3 task(s) finished") {
		t.Errorf("finished, the card does not say so:\n%s", last.HTML)
	}
	if len(run.Requested) != 4 || run.PendingLeft {
		t.Errorf("%d request(s) and a poll still scheduled=%v after the goal settled; want 4 and none",
			len(run.Requested), run.PendingLeft)
	}
	if run.Interval < 2000 || run.Interval > 15000 || len(run.Delays) != 1 || run.Delays[0] != run.Interval {
		t.Errorf("polled every %v ms (interval %d); a live step takes 5–15 s", run.Delays, run.Interval)
	}
}

// Stopped by its budget: said as a budget stop, not an ordinary failure.
func TestWorkbench_TheCardSaysABuildWasStoppedByItsBudget(t *testing.T) {
	run := runProgress(t, []progressPoll{{
		Goal: map[string]any{"id": "gol_1", "status": "failed", "tasks_total": 3, "tasks_done": 1, "tasks_failed": 1},
		Tasks: []map[string]any{step("t1", "Step 1 of 3 — Base", "succeeded"),
			{"id": "t2", "title": "Step 2 of 3 — Arm", "status": "failed", "error_code": "FORBIDDEN",
				"error_detail": "engine.Budget: goal budget exhausted on tokens: used 150 tokens of 150."},
			step("t3", "Step 3 of 3 — Shade", "skipped")},
		Events: []map[string]any{event("goal.ended", "1 of 3 task(s) failed or were cancelled"),
			event("budget.exceeded", "Budget exhausted on tokens: used 150 tokens of 150."),
			event("task.succeeded", "Step 1 of 3 (Base): 1 part(s), kept as version ver_01A.")},
	}})
	if len(run.Seen) != 1 || run.Seen[0].Stop == nil || run.Seen[0].Stop.Kind != "budget" {
		t.Fatalf("a budget stop reads %+v", run.Seen)
	}
	if !strings.Contains(run.Seen[0].HTML, "Stopped by its budget") || !strings.Contains(run.Seen[0].HTML, "used 150 tokens of 150") {
		t.Errorf("the card does not say the budget stopped it:\n%s", run.Seen[0].HTML)
	}
	if len(run.Requested) != 2 || run.PendingLeft {
		t.Errorf("a settled goal was read %d time(s), still scheduled=%v", len(run.Requested)/2, run.PendingLeft)
	}
}

// A step that failed is named.
func TestWorkbench_TheCardNamesTheStepThatFailed(t *testing.T) {
	run := runProgress(t, []progressPoll{{
		Goal: map[string]any{"id": "gol_1", "status": "failed", "tasks_total": 2, "tasks_done": 1, "tasks_failed": 1},
		Tasks: []map[string]any{step("t1", "Step 1 of 2 — Base", "succeeded"),
			{"id": "t2", "title": "Step 2 of 2 — Arm", "status": "failed", "error_detail": "the model could not be reached"}},
	}})
	if len(run.Seen) != 1 || run.Seen[0].Stop == nil || run.Seen[0].Stop.Kind != "failed" ||
		!strings.Contains(run.Seen[0].HTML, "Step 2 of 2 — Arm failed: the model could not be reached") {
		t.Errorf("a failed step reads %+v", run.Seen)
	}
}

// A dropped connection is retried; a goal that is gone is not asked for again.
func TestWorkbench_TheCardRetriesADroppedReadAndStopsForAGoalThatIsGone(t *testing.T) {
	settled := progressPoll{Goal: map[string]any{"id": "gol_1", "status": "succeeded", "tasks_total": 1, "tasks_done": 1}}

	run := runProgress(t, []progressPoll{{Status: 503}, settled})
	if len(run.Errors) != 1 || len(run.Seen) != 1 || !run.Seen[0].Settled || run.PendingLeft {
		t.Errorf("after a 503 the card read %+v; want one error, then the settled goal, then nothing", run)
	}

	run = runProgress(t, []progressPoll{{Status: 404}})
	if len(run.Errors) != 1 || len(run.Requested) != 2 || run.PendingLeft || len(run.Delays) != 0 {
		t.Errorf("a goal that is gone was asked for again: %d request(s), scheduled=%v", len(run.Requested), run.PendingLeft)
	}
}

// The card starts following when "Start it" succeeds, stops for a new proposal,
// and renders what it read. The functions above could be correct and unwired.
func TestWorkbench_TheProposalCardFollowsTheGoalItStarted(t *testing.T) {
	b, err := assetFS.ReadFile("assets/workbench.js")
	if err != nil {
		t.Fatal(err)
	}
	js := codeOnly(string(b))
	start, end := strings.Index(js, "function startIt()"), strings.Index(js, "function renderProposal()")
	if start < 0 || end < start {
		t.Fatal("startIt or renderProposal is gone; this fence reads between them")
	}
	if !strings.Contains(js[start:end], "followStartedGoal()") {
		t.Error("\"Start it\" no longer follows the goal it started, so the card says \"Started.\" and nothing more")
	}
	propose := strings.Index(js, "function proposeGoal(goal)")
	if propose < 0 || !strings.Contains(js[propose:start], "stopFollowingGoal()") {
		t.Error("a new proposal does not stop following the last goal, so two goals are read into one card")
	}
	if !strings.Contains(js[end:], "goalProgressHTML(state.progress") {
		t.Error("renderProposal does not render the progress it read")
	}
}

// A real budget breach: the card says what the stop MEANT, not how it was reported.
//
// A failed task's detail is errs.Error()'s rendering — "<op>: <CODE>: <what that
// code means in general> (<the detail written for this failure>)" — and the card
// showed all of it, so somebody watching their own build hit a ceiling they had
// set and was told they were not permitted to do this.
//
// ‼️ The error_detail below is the EXACT string a breach put on screen
// (docs/spikes/2026-09-15-card-checked). The older budget fence above supplies a
// tidier one by hand, which is precisely why it never caught this.
// docs/bugfix/2026-09-15-a-budget-stop-was-shown-as-a-permissions-error.md
func TestWorkbench_TheCardSaysWhatABudgetStopMeantWithoutTheErrorsPlumbing(t *testing.T) {
	const real = "engine.Budget: FORBIDDEN: The authenticated principal is not permitted to perform " +
		"this action on this resource. (goal budget exhausted on tokens: used 1500 tokens of 800. " +
		"Raise FORGE_MAX_TOKENS_PER_GOAL or the goal's own ceiling, or narrow the goal so it needs " +
		"less context.)"

	run := runProgress(t, []progressPoll{{
		Goal: map[string]any{"id": "gol_1", "status": "failed", "tasks_total": 3, "tasks_done": 0,
			"tasks_failed": 1, "tokens_spent": 1500, "max_tokens": 800},
		Tasks: []map[string]any{
			{"id": "t1", "title": "Step 1 of 3 — Base", "status": "failed", "error_code": "FORBIDDEN",
				"error_detail": real},
			step("t2", "Step 2 of 3 — Arm", "skipped"), step("t3", "Step 3 of 3 — Shade", "skipped")},
		// ‼️ No budget.exceeded event. A breach INSIDE a step does not write one —
		// only the guard that refuses a task before it starts does — so the task's
		// own detail is all the card has for the commonest budget stop there is.
		Events: []map[string]any{event("goal.ended", "1 of 3 task(s) failed or were cancelled"),
			event("task.failed", real)},
	}})

	if len(run.Seen) != 1 || run.Seen[0].Stop == nil || run.Seen[0].Stop.Kind != "budget" {
		t.Fatalf("a real budget breach reads %+v", run.Seen)
	}
	html := run.Seen[0].HTML
	for _, want := range []string{"Stopped by its budget", "used 1500 tokens of 800",
		"Raise FORGE_MAX_TOKENS_PER_GOAL", "1,500 of 800 tokens"} {
		if !strings.Contains(html, want) {
			t.Errorf("the card does not say %q:\n%s", want, html)
		}
	}
	// The error's plumbing, and the sentence that belongs to the CODE rather than
	// to this stop. "not permitted" is the one thing a person must never be told
	// about a spending limit they set themselves.
	for _, unwanted := range []string{"FORBIDDEN", "engine.Budget", "authenticated principal", "not permitted"} {
		if strings.Contains(html, unwanted) {
			t.Errorf("the card still shows %q, which is how the stop was reported and not what it meant:\n%s",
				unwanted, html)
		}
	}
}

// "Start this" names the conversation's project, and the industry stays paired
// with it.
//
// With no project id the server has nothing to put the goal in, so Intake.Draft
// makes a NEW project for every goal started from the card — and the chosen
// industry goes with it, because the request blanks the industry whenever a
// project exists. The build then lands in a project the workbench's own panels
// do not read: its Files panel said "This project has no files yet." while the
// versions it had just watched being kept sat in a project of their own.
// docs/bugfix/2026-09-15-a-card-started-goal-landed-in-a-new-project.md
func TestWorkbench_TheCardStartsAGoalInTheConversationsProject(t *testing.T) {
	b, err := assetFS.ReadFile("assets/workbench.js")
	if err != nil {
		t.Fatal(err)
	}
	js := codeOnly(string(b))
	start, end := strings.Index(js, "function startThis()"), strings.Index(js, "function startIt()")
	if start < 0 || end < start {
		t.Fatal("startThis or startIt is gone; this fence reads between them")
	}
	body := js[start:end]
	if !strings.Contains(body, "project_id: state.projectID") {
		t.Error("\"Start this\" does not send the conversation's project, so a goal started from the card " +
			"creates a project of its own and its versions land outside the conversation that asked for them")
	}
	// Fenced as a PAIR: the server refuses an industry sent together with a
	// project id, so the industry must stay conditional on the very field above.
	// The defect was this guard outliving the field it was guarding.
	if !strings.Contains(body, "industry: state.projectID ? '' :") {
		t.Error("the industry is no longer conditional on the project id; sent together, the server refuses both")
	}
}

// ‼️ A step refused by a gate succeeds and keeps nothing; the card says so, by step, and
// does not count the version it left behind as one it kept. Live run 3 of 2026-09-17
// read "3 succeeded" with step 3 refused (docs/spikes/2026-09-17-live-verification).
func TestWorkbench_TheCardSaysAStepWasRefusedAndKeptNothing(t *testing.T) {
	outcome := "All 3 task(s) finished: 3 succeeded, 0 skipped. 1 build step(s) were refused, kept nothing: Step 3 of 3 — Shade."
	run := runProgress(t, []progressPoll{{
		Goal: map[string]any{"id": "gol_1", "status": "succeeded", "tasks_total": 3, "tasks_done": 3,
			"outcome_summary": outcome},
		Tasks: []map[string]any{step("t1", "Step 1 of 3 — Base", "succeeded"),
			step("t2", "Step 2 of 3 — Arm", "succeeded"), step("t3", "Step 3 of 3 — Shade", "succeeded")},
		Events: []map[string]any{event("goal.ended", outcome),
			event("task.succeeded", "Step 3 of 3 (Shade): refused, kept nothing; the model stays at version ver_02B (2 part(s)). "+
				"Step 3 (Shade) was refused: it named a root that is not the model's."),
			event("task.succeeded", "Step 2 of 3 (Arm): 2 part(s), kept as version ver_02B."),
			event("task.succeeded", "Step 1 of 3 (Base): 1 part(s), kept as version ver_01A.")},
	}})
	if len(run.Seen) != 1 || !run.Seen[0].Settled {
		t.Fatalf("the card read %+v", run.Seen)
	}
	seen := run.Seen[0]
	if strings.Join(seen.Kept, ",") != "ver_01A,ver_02B" {
		t.Errorf("the card counts versions %v; the refused step kept none", seen.Kept)
	}
	if !strings.Contains(seen.HTML, `<div class="note bad">1 step was refused and kept nothing: Step 3 of 3 (Shade)</div>`) {
		t.Errorf("the card does not say step 3 was refused and kept nothing:\n%s", seen.HTML)
	}
	if !strings.Contains(seen.HTML, "Finished.") {
		t.Errorf("a goal whose kept model is valid still finished; the card must say that too:\n%s", seen.HTML)
	}
}

// ‼️ A goal stopped with tokens left, because its next call could not fit (engine
// BudgetGuard.CheckCall, 2026-09-17), reads as a budget stop that says how much was
// left and why — from the step's own error when no budget event is on the timeline,
// through plainStopText, which keeps the whole reason and drops the error's plumbing.
func TestWorkbench_TheCardSaysWhyABuildStoppedWithTokensLeft(t *testing.T) {
	why := "50 tokens were left, and the next model call was not placed because it may cost 125 " +
		"(the largest call this goal has made, 100 tokens, and a quarter more), which would pass the ceiling. " +
		"The goal stops here rather than spend past it."
	detail := "engine.Budget: FORBIDDEN: the action is not permitted (goal budget exhausted on tokens: used 300 tokens of 350. " +
		why + " Raise FORGE_MAX_TOKENS_PER_GOAL or the goal's own ceiling, or narrow the goal so it needs less context.)"
	run := runProgress(t, []progressPoll{{
		Goal: map[string]any{"id": "gol_1", "status": "failed", "tasks_total": 3, "tasks_done": 2, "tasks_failed": 1,
			"tokens_spent": 300, "max_tokens": 350},
		Tasks: []map[string]any{step("t1", "Step 1 of 3 — Base", "succeeded"), step("t2", "Step 2 of 3 — Arm", "succeeded"),
			{"id": "t3", "title": "Step 3 of 3 — Shade", "status": "failed", "error_code": "FORBIDDEN", "error_detail": detail}},
	}})
	if len(run.Seen) != 1 || run.Seen[0].Stop == nil || run.Seen[0].Stop.Kind != "budget" {
		t.Fatalf("a stop with tokens left reads %+v", run.Seen)
	}
	if !strings.Contains(run.Seen[0].Stop.Text, "used 300 tokens of 350. "+why) || strings.Contains(run.Seen[0].Stop.Text, "FORBIDDEN") {
		t.Errorf("the card's stop reads %q", run.Seen[0].Stop.Text)
	}
	if !strings.Contains(run.Seen[0].HTML, "Stopped by its budget") || !strings.Contains(run.Seen[0].HTML, "300 of 350 tokens") {
		t.Errorf("the card does not show the budget stop with the goal under its ceiling:\n%s", run.Seen[0].HTML)
	}
}
