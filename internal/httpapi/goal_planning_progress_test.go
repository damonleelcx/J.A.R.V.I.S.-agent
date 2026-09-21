package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/agent"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
)

// A goal waiting on the planner says so (PRD NFR-02: "long jobs report progress
// at least every 10 s").
//
// # The gap these close
//
// PR 145 covered every step a WORKER runs: runTask starts the heartbeat before
// it branches into the build loop, the export loop or the tool loop
// (internal/agent/worker.go), each beat writes the task row, and
// TaskDTO.last_seen_at is the stamp. Fenced in internal/agent/alive_test.go and
// internal/httpapi/task_last_seen_test.go.
//
// Planning is not a task. It runs synchronously inside CreateGoal and Replan,
// takes 128 s on a live model (GitHub issue 13), and until it finished the
// timeline gained nothing at all between goal.created and plan.created: a
// person watching over HTTP saw an open connection and no signal for over two
// minutes.
//
// # Why these run against live Postgres
//
// Every claim here is about rows in forge_events — that they exist, how many,
// how far apart they were written and what they say. A fake store would let the
// test agree with itself about all four, and the append path being exercised is
// the hash-chained one (SAF-06), which is the part that could refuse a write.
//
// The interval is scaled down through planProgressEvery, exactly as
// alive_test.go scales the worker's through SetAliveEveryForTest. The claim
// about the PRODUCTION interval is arithmetic and is fenced as arithmetic, in
// internal/agent/progress_test.go.

// A valid one-task plan, in the shape the planner parses.
const planprogReply = `{"rationale":"clear enough","clarification_needed":"","tasks":[
	{"key":"draw","title":"draw it","instruction":"draw the bracket","inputs":{},
	 "expected_output":{"description":"a drawing"},"depends_on":[],"risk_tier":"r1"}]}`

// planprogHeldLLM stands in for a planner that takes a while, which is the only
// interesting case: a model that answers instantly cannot show whether anything
// is reported WHILE it runs.
type planprogHeldLLM struct {
	hold time.Duration
	mu   sync.Mutex
	n    int
}

func (s *planprogHeldLLM) Complete(ctx context.Context, _ llm.Request) (*llm.Response, error) {
	s.mu.Lock()
	s.n++
	s.mu.Unlock()
	select {
	case <-time.After(s.hold):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &llm.Response{Content: planprogReply, FinishReason: "stop", Model: "stub",
		Usage: llm.Usage{TotalTokens: 10}}, nil
}

func (s *planprogHeldLLM) ModelFor(llm.Role) string { return "stub" }

// planprogEvery scales the reporting interval for the length of one fence and
// puts it back, so a failure cannot leave the package reporting at 150 ms.
func planprogEvery(t *testing.T, d time.Duration) {
	t.Helper()
	was := planProgressEvery
	planProgressEvery = d
	t.Cleanup(func() { planProgressEvery = was })
}

// planprogEvent is one timeline row, read straight from the table.
type planprogEvent struct {
	seq       int64
	kind      string
	actor     string
	summary   string
	createdAt time.Time
	elapsedMS int64
}

func planprogTimeline(t *testing.T, h *GoalHandlers, goalID string) []planprogEvent {
	t.Helper()
	rows, err := h.deps.Pool.Query(context.Background(), `
		select seq, kind, actor, summary, created_at, payload
		  from forge_events where goal_id = $1 order by seq`, goalID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var out []planprogEvent
	for rows.Next() {
		var e planprogEvent
		var payload []byte
		if err := rows.Scan(&e.seq, &e.kind, &e.actor, &e.summary, &e.createdAt, &payload); err != nil {
			t.Fatal(err)
		}
		if e.kind == engine.EventGoalProgress {
			var p struct {
				ElapsedMS    int64  `json:"elapsed_ms"`
				ElapsedHuman string `json:"elapsed_human"`
			}
			if err := json.Unmarshal(payload, &p); err != nil {
				t.Fatalf("progress event %d carries unreadable payload %q: %v", e.seq, payload, err)
			}
			e.elapsedMS = p.ElapsedMS
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCreateGoal_AGoalWaitingOnThePlannerReportsProgressInsideNFR02sTenSeconds(t *testing.T) {
	// 150 ms stands in for the production 5 s; the plan is held for ten of them.
	const every = 150 * time.Millisecond
	const held = 10 * every

	h, _, user := buildHandlers(t, &planprogHeldLLM{hold: held})
	planprogEvery(t, every)

	start := time.Now()
	w := httptest.NewRecorder()
	h.CreateGoal(w, postAs(user, "/v1/goals",
		`{"title":"A bracket","statement":"draw a bracket for the mount"}`))
	if w.Code != 201 {
		t.Fatalf("POST /v1/goals answered %d: %s", w.Code, w.Body.String())
	}
	took := time.Since(start)

	var created struct {
		Goal struct {
			ID string `json:"id"`
		} `json:"goal"`
		Tasks []map[string]any `json:"tasks"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if len(created.Tasks) != 1 {
		t.Fatalf("the stub plan produced %d task(s); the fence needs the ordinary planning path to "+
			"have actually run", len(created.Tasks))
	}

	events := planprogTimeline(t, h, created.Goal.ID)
	var progress []planprogEvent
	for _, e := range events {
		if e.kind == engine.EventGoalProgress {
			progress = append(progress, e)
		}
	}

	// Ten intervals inside the planner call is ten reports; 4 leaves room for a
	// loaded machine and is still impossible for a handler that writes nothing
	// between the draft and the plan.
	if len(progress) < 4 {
		var kinds []string
		for _, e := range events {
			kinds = append(kinds, e.kind)
		}
		t.Fatalf("a plan held for %s (the request took %s) left %d %s event(s) on the timeline; the "+
			"whole timeline is [%s]. A client polling GET /v1/goals/{id}/timeline sees nothing "+
			"change for the entire plan — 128 s on a live model — and NFR-02 asks a long job to "+
			"report progress at least every 10 s",
			held, took, len(progress), engine.EventGoalProgress, strings.Join(kinds, ", "))
	}

	// Each one says how long, and only ever more. This is the payload half;
	// the summary half below is what an HTTP client actually receives.
	for i, e := range progress {
		if e.elapsedMS <= 0 {
			t.Fatalf("progress event %d carries elapsed_ms=%d. A progress report that does not say "+
				"how long the plan has been running is the fake progress bar this deliberately is "+
				"not", e.seq, e.elapsedMS)
		}
		if i > 0 && e.elapsedMS <= progress[i-1].elapsedMS {
			t.Fatalf("progress event %d says %d ms elapsed and %d says %d ms: the elapsed time is "+
				"not moving, so it is not being measured",
				progress[i-1].seq, progress[i-1].elapsedMS, e.seq, e.elapsedMS)
		}
		if !strings.Contains(e.summary, "still planning") || !strings.Contains(e.summary, "elapsed") {
			t.Fatalf("progress event %d reads %q. httpapi.EventDTO gives a client the summary and "+
				"not the payload, so the summary is the only place elapsed time reaches anyone "+
				"watching over HTTP", e.seq, e.summary)
		}
		if e.actor != string(engine.ActorPlanner) {
			t.Fatalf("progress event %d is attributed to %q. The planner is what is holding the "+
				"goal, and PRD requires who caused an event to be recorded rather than inferred",
				e.seq, e.actor)
		}
	}

	// ‼️ The cadence, measured from the moment the REQUEST ARRIVED and across the
	// whole timeline, not only between progress events. The number that matters
	// is the longest a watcher saw nothing at all, and a draft goal writes no
	// timeline event of its own — nothing was on this goal's timeline until
	// plan.created — so the first silence starts when the caller pressed the
	// button, not when some earlier row was written.
	worst := events[0].createdAt.Sub(start)
	worstBetween := "the request arriving → " + events[0].kind
	for i := 1; i < len(events); i++ {
		if gap := events[i].createdAt.Sub(events[i-1].createdAt); gap > worst {
			worst = gap
			worstBetween = events[i-1].kind + " → " + events[i].kind
		}
	}
	t.Logf("%d events (%d progress) over a %s plan at every=%s; longest silence %s (%s)",
		len(events), len(progress), took, every, worst, worstBetween)

	// The production interval is chosen so a client polling at the same rate
	// sees a report at most twice the interval old (agent.ProgressEvery). 4x
	// leaves room for a shared laptop and is still far inside what a handler
	// that reported nothing would show, which is the whole plan.
	if worst > 4*every {
		t.Fatalf("the longest a watcher saw nothing was %s (%s), at a reporting interval of %s. "+
			"Scaled to production that is %s of silence against NFR-02's 10 s ceiling",
			worst, worstBetween, every, worst*agent.ProgressEvery/every)
	}

	// And the same events reach a client through the real endpoint, because a
	// row nobody can read is not a progress signal.
	tr := getAs(user, "/v1/goals/"+created.Goal.ID+"/timeline")
	tr.SetPathValue("id", created.Goal.ID)
	tw := httptest.NewRecorder()
	h.Timeline(tw, tr)
	if tw.Code != 200 {
		t.Fatalf("GET /v1/goals/{id}/timeline answered %d: %s", tw.Code, tw.Body.String())
	}
	var timeline struct {
		Events []EventDTO `json:"events"`
	}
	if err := json.Unmarshal(tw.Body.Bytes(), &timeline); err != nil {
		t.Fatal(err)
	}
	served := 0
	for _, e := range timeline.Events {
		if e.Kind == engine.EventGoalProgress && strings.Contains(e.Summary, "elapsed") {
			served++
		}
	}
	if served != len(progress) {
		t.Fatalf("the timeline endpoint served %d progress entr(ies) carrying an elapsed time and "+
			"the table holds %d. The signal exists only if the polling surface hands it over",
			served, len(progress))
	}
}

func TestCreateGoal_APlanThatAnswersAtOnceDoesNotSpamTheTimelineWithProgress(t *testing.T) {
	// The other half of the requirement, and the one that stops it being met by
	// reporting constantly. Most plans are short; a goal whose plan came back
	// before the first interval elapsed has nothing to report, and a "still
	// planning … 0s elapsed" line in front of plan.created would grow the
	// permanent record of every goal for nothing.
	//
	// It also fences the first report waiting a whole interval rather than
	// firing the moment the ticker starts.
	const every = 500 * time.Millisecond

	h, _, user := buildHandlers(t, &planprogHeldLLM{hold: 0})
	planprogEvery(t, every)

	start := time.Now()
	w := httptest.NewRecorder()
	h.CreateGoal(w, postAs(user, "/v1/goals",
		`{"title":"A bracket","statement":"draw a bracket for the mount"}`))
	if w.Code != 201 {
		t.Fatalf("POST /v1/goals answered %d: %s", w.Code, w.Body.String())
	}
	took := time.Since(start)
	if took >= every {
		t.Skipf("the unheld plan took %s, which is not shorter than the %s reporting interval this "+
			"fence needs it to beat; the machine is too loaded to judge this", took, every)
	}

	var created struct {
		Goal struct {
			ID string `json:"id"`
		} `json:"goal"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	for _, e := range planprogTimeline(t, h, created.Goal.ID) {
		if e.kind == engine.EventGoalProgress {
			t.Fatalf("a plan that answered in %s, inside one %s reporting interval, still wrote "+
				"%q to the timeline. Every short plan would grow the permanent record with a line "+
				"saying nothing happened yet", took, every, e.summary)
		}
	}
}
