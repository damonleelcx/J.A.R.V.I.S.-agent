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
