package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/agent"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/identity"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// A build started from the API (Phase 2, stage A1 follow-ups).
//
// Through the real handlers against live Postgres, with a stub model: every
// claim here is about rows — which tasks a plan wrote, which project a goal
// landed in, which versions a step kept and what the timeline says about them.

const buildThreeSteps = `{"steps":[{"name":"chassis","what":"the chassis"},
	{"name":"wheels","what":"four wheels"},{"name":"body","what":"the body"}]}`

func buildWholeDoc(id, name string) string {
	return `{"speech":"built ` + name + `","prototype":{"name":"m","units":"mm","parts":[
	  {"id":"` + id + `","name":"` + name + `","shape":"box","size":{"width":100,"height":100,"depth":100}}]}}`
}

func buildAddPart(id, name string) string {
	return `{"speech":"added ` + name + `","prototype_edit":{"patch":{"parts":[
	  {"id":"` + id + `","name":"` + name + `","shape":"box","size":{"width":100,"height":100,"depth":100}}]}}}`
}

// buildLLM answers in order and charges the same tokens for every answer.
type buildLLM struct {
	mu      sync.Mutex
	replies []string
	asked   []string
	tokens  int64
}

func (s *buildLLM) Complete(_ context.Context, req llm.Request) (*llm.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.asked)
	last := ""
	for _, m := range req.Messages {
		if m.Role == llm.User {
			last = m.Content
		}
	}
	s.asked = append(s.asked, last)
	r := `{"speech":"done"}`
	if n < len(s.replies) {
		r = s.replies[n]
	}
	return &llm.Response{Content: r, FinishReason: "stop", Model: "stub",
		Usage: llm.Usage{TotalTokens: s.tokens}}, nil
}

// No vision model: a step is built and checked without a look, as on a
// deployment that has none.
func (s *buildLLM) ModelFor(r llm.Role) string {
	if r == llm.RoleVision {
		return ""
	}
	return "stub"
}

func (s *buildLLM) calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.asked...)
}

// buildHandlers is startHarness with a model configured.
func buildHandlers(t *testing.T, stub llm.Client) (*GoalHandlers, *db.Pool, *identity.User) {
	t.Helper()
	h, pool, user := startHarness(t)
	d := h.deps
	d.LLM = stub
	return NewGoalHandlers(d), pool, user
}

func postAs(user *identity.User, target, body string) *http.Request {
	r := httptest.NewRequest("POST", target, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r.WithContext(context.WithValue(r.Context(), ctxKeyUser, user))
}

func getAs(user *identity.User, target string) *http.Request {
	r := httptest.NewRequest("GET", target, nil)
	return r.WithContext(context.WithValue(r.Context(), ctxKeyUser, user))
}

func insertUser(t *testing.T, pool *db.Pool, email string) *identity.User {
	t.Helper()
	u := &identity.User{ID: id.New(id.PrefixUser), Email: email}
	now := time.Now().UTC()
	if _, err := pool.Exec(context.Background(), `
		insert into forge_users (id, email, display_name, status, password_hash, password_algo,
			password_changed_at, created_at, updated_at)
		values ($1,$2,'Other','active','x','argon2id',$3,$3,$3)`, u.ID, u.Email, now); err != nil {
		t.Fatal(err)
	}
	return u
}

func goalsIn(t *testing.T, pool *db.Pool, projectID string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`select count(*) from forge_goals where project_id = $1`, projectID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// POST /v1/goals with build:true plans the statement as a build: one task per
// step, each waiting for the step before, and nothing running.
func TestCreateGoal_ABuildIsPlannedAsOneTaskPerStepEachWaitingForTheOneBefore(t *testing.T) {
	stub := &buildLLM{tokens: 70, replies: []string{buildThreeSteps}}
	h, pool, user := buildHandlers(t, stub)

	rec := httptest.NewRecorder()
	h.CreateGoal(rec, postAs(user, "/v1/goals",
		`{"title":"A car","statement":"a sports car","build":true}`))

	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Goal    GoalDTO   `json:"goal"`
		Tasks   []TaskDTO `json:"tasks"`
		Running bool      `json:"running"`
		Build   bool      `json:"build"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Build || body.Running || body.Goal.Status != string(engine.GoalDraft) {
		t.Errorf("build=%v running=%v status=%s; want a build, not running, a draft",
			body.Build, body.Running, body.Goal.Status)
	}
	if len(body.Tasks) != 3 {
		t.Fatalf("a three-step build planned %d task(s): %s", len(body.Tasks), rec.Body.String())
	}
	for i, task := range body.Tasks {
		if i == 0 && len(task.DependsOn) != 0 {
			t.Errorf("step 1 waits for %v", task.DependsOn)
		}
		if i > 0 && (len(task.DependsOn) != 1 || task.DependsOn[0] != body.Tasks[i-1].ID) {
			t.Errorf("step %d waits for %v, not step %d (%s)", i+1, task.DependsOn, i, body.Tasks[i-1].ID)
		}
	}
	asked := stub.calls()
	if len(asked) != 1 || !strings.HasPrefix(asked[0], "Plan the build of: a sports car") {
		t.Errorf("the model was not asked to plan a build: %q", asked)
	}
	tasks, err := engine.NewRepository().ListTasks(context.Background(), pool, body.Goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		if !strings.Contains(string(task.Inputs), agent.TaskKindBuildStep) {
			t.Errorf("task %q is not a build step: %s", task.Title, task.Inputs)
		}
	}
	// The planning call is charged to the goal, like every call after it.
	if body.Goal.TokensSpent != 70 {
		t.Errorf("the goal shows %d tokens spent; the planning call cost 70", body.Goal.TokensSpent)
	}
}

// A build the model planned as a single step is refused, and the draft is
// named so the reader can find it.
func TestCreateGoal_ABuildOfOneStepIsRefusedAndTheDraftSurvives(t *testing.T) {
	stub := &buildLLM{replies: []string{`{"steps":[{"name":"bracket","what":"a bracket"}]}`}}
	h, pool, user := buildHandlers(t, stub)

	rec := httptest.NewRecorder()
	h.CreateGoal(rec, postAs(user, "/v1/goals", `{"title":"A bracket","statement":"a bracket","build":true}`))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", rec.Code, rec.Body.String())
	}
	var goalID string
	if err := pool.QueryRow(context.Background(),
		`select id from forge_goals where created_by = $1`, user.ID).Scan(&goalID); err != nil {
		t.Fatalf("no draft survived the refused plan: %v", err)
	}
	if !strings.Contains(rec.Body.String(), goalID) {
		t.Errorf("the refusal does not name the draft it left (%s): %s", goalID, rec.Body.String())
	}
}

// The refusal on a named project holds when the request asks for a BUILD, not
// only for ordinary work.
//
// The check itself is shared — it runs before Plan or PlanBuild is chosen — and
// its reviewed fences are TestCreateGoal_RefusesAProjectTheCallerIsNotAMemberOf
// and TestCreateGoal_RefusesAViewerOfTheProject in goals_permission_test.go,
// which send the plain request. These two are the build stack's own coverage of
// the same refusal on the request shape those do not send. Go's -run matches by
// substring, so the drill that removes the check runs these as well.
//
// Worth fencing separately because the build path is where the defect was found,
// and it did not look the same there: with build:true the stub's reply is not a
// build plan, so an unchecked create answered 502 rather than the 404 main
// reproduced. The status code alone named a different culprit on each branch.
// docs/bugfix/2026-09-15-a-goal-could-be-drafted-into-a-project-its-caller-was-not-in.md
func TestCreateGoal_RefusesAProjectTheCallerIsNotAMemberOfWhenAskedForABuild(t *testing.T) {
	stub := &buildLLM{replies: []string{buildThreeSteps}}
	h, pool, user := buildHandlers(t, stub)
	other := insertUser(t, pool, "owner-elsewhere-build@example.com")
	projectID := projectOf(t, pool, seedGoal(t, pool, other.ID, engine.GoalDraft, 0))

	rec := httptest.NewRecorder()
	h.CreateGoal(rec, postAs(user, "/v1/goals",
		`{"title":"Mine now","statement":"a car","build":true,"project_id":"`+projectID+`"}`))

	if rec.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d: %s", rec.Code, rec.Body.String())
	}
	if n := goalsIn(t, pool, projectID); n != 1 {
		t.Errorf("the project now holds %d goals; a stranger wrote a build into it", n)
	}
	if asked := stub.calls(); len(asked) != 0 {
		t.Errorf("the model was asked %d time(s) on behalf of a stranger to the project", len(asked))
	}
}

// A viewer reads a project and plans no build in it.
func TestCreateGoal_RefusesAViewerOfTheProjectWhenAskedForABuild(t *testing.T) {
	stub := &buildLLM{replies: []string{buildThreeSteps}}
	h, pool, user := buildHandlers(t, stub)
	other := insertUser(t, pool, "owner-viewed-build@example.com")
	projectID := projectOf(t, pool, seedGoal(t, pool, other.ID, engine.GoalDraft, 0))
	if _, err := pool.Exec(context.Background(), `
		insert into forge_project_members (project_id, user_id, role, granted_by, granted_at, updated_at)
		values ($1,$2,'viewer',$3,now(),now())`, projectID, user.ID, other.ID); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.CreateGoal(rec, postAs(user, "/v1/goals",
		`{"title":"Viewer's","statement":"a car","build":true,"project_id":"`+projectID+`"}`))

	if rec.Code != http.StatusForbidden {
		t.Errorf("want 403, got %d: %s", rec.Code, rec.Body.String())
	}
	if n := goalsIn(t, pool, projectID); n != 1 {
		t.Errorf("the project now holds %d goals; a viewer wrote one into it", n)
	}
	if asked := stub.calls(); len(asked) != 0 {
		t.Errorf("the model was asked %d time(s) for a viewer", len(asked))
	}
}

// POST /v1/goals/{id}/plan with build:true replans a draft as a build. Without
// it, a build whose planning tripped would be recovered as ordinary tasks that
// forge-worker hands to the executor rather than the kernel.
func TestReplan_ADraftIsReplannedAsABuildWhenAsked(t *testing.T) {
	stub := &buildLLM{replies: []string{buildThreeSteps}}
	h, pool, user := buildHandlers(t, stub)
	goalID := seedGoal(t, pool, user.ID, engine.GoalDraft, 0)

	rec := httptest.NewRecorder()
	r := postAs(user, "/v1/goals/"+goalID+"/plan", `{"build":true}`)
	r.SetPathValue("id", goalID)
	h.Replan(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	tasks, err := engine.NewRepository().ListTasks(context.Background(), pool, goalID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 3 {
		t.Fatalf("replanned into %d task(s), want 3 steps", len(tasks))
	}
	for _, task := range tasks {
		if !strings.Contains(string(task.Inputs), agent.TaskKindBuildStep) {
			t.Errorf("task %q is not a build step: %s", task.Title, task.Inputs)
		}
	}
}

// A replan with no body still means an ordinary plan: the endpoint took none
// before, and a caller that sends none must not be refused for it.
func TestReplan_AnEmptyBodyIsStillAccepted(t *testing.T) {
	h, pool, user := buildHandlers(t, &buildLLM{})
	goalID := seedGoal(t, pool, user.ID, engine.GoalDraft, 0)

	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/v1/goals/"+goalID+"/plan", nil)
	r = r.WithContext(context.WithValue(r.Context(), ctxKeyUser, user))
	r.SetPathValue("id", goalID)
	h.Replan(rec, r)

	if rec.Code == http.StatusBadRequest && strings.Contains(rec.Body.String(), "JSON") {
		t.Fatalf("an empty body was refused as bad JSON: %s", rec.Body.String())
	}
}

var keptVersion = regexp.MustCompile(`kept as version (\S+)\.`)

// A build started over HTTP runs on a real worker, and its progress is read
// through the endpoints that already exist: each step on the goal, each kept
// version on the timeline naming the step that kept it.
func TestBuildGoal_StepsAndKeptVersionsShowOnTheGoalAndItsTimeline(t *testing.T) {
	stub := &buildLLM{tokens: 10, replies: []string{buildThreeSteps,
		buildWholeDoc("chassis", "Chassis"), buildAddPart("wheels", "Wheels"), buildAddPart("body", "Body")}}
	h, pool, user := buildHandlers(t, stub)
	ctx := context.Background()

	rec := httptest.NewRecorder()
	h.CreateGoal(rec, postAs(user, "/v1/goals", `{"title":"A car","statement":"a car","build":true}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	goalID := decode(t, rec)["goal"].(map[string]any)["id"].(string)

	rec = httptest.NewRecorder()
	r := postAs(user, "/v1/goals/"+goalID+"/start", `{}`)
	r.SetPathValue("id", goalID)
	h.StartGoal(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}

	cfg := config.EngineConfig{
		WorkerConcurrency: 1, LeaseDuration: time.Minute, LeaseHeartbeat: time.Hour,
		PollInterval: 20 * time.Millisecond, MaxAttemptsPerTask: 3,
		BackoffBase: 10 * time.Millisecond, BackoffMax: 50 * time.Millisecond,
		MaxIterationsPerTask: 12, MaxToolCallsPerIteration: 8,
		MaxTokensPerGoal: 1_000_000, MaxWallClockPerGoal: time.Hour, MaxTaskDepth: 3, MaxTasksPerGoal: 50,
	}
	clk := clock.System{}
	repo, queue, budget := engine.NewRepository(), engine.NewQueue(), engine.NewBudgetGuard(cfg)
	geo := geometry.NewService(pool, clk, logx.Discard())
	w := agent.NewWorker(agent.WorkerDeps{Pool: pool, Repo: repo, Queue: queue, Budget: budget,
		Assembler: agent.NewAssembler(repo, queue),
		Builds: agent.NewBuildSteps(agent.NewConversation(stub, persona.DefaultCharacter()),
			geo, repo, budget, pool, clk, logx.Discard()),
		Config: cfg, WorkspaceRoot: t.TempDir(), Clock: clk, Log: logx.Discard()})
	wctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); _ = w.Run(wctx) }()
	deadline := time.Now().Add(60 * time.Second)
	status := string(engine.GoalActive)
	for time.Now().Before(deadline) && status == string(engine.GoalActive) {
		if err := pool.QueryRow(ctx, `select status from forge_goals where id = $1`, goalID).Scan(&status); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	if status != string(engine.GoalSucceeded) {
		t.Fatalf("the build ended %s", status)
	}

	// The goal: every step, done, and what the build spent.
	rec = httptest.NewRecorder()
	r = getAs(user, "/v1/goals/"+goalID)
	r.SetPathValue("id", goalID)
	h.GetGoal(rec, r)
	var got struct {
		Goal  GoalDTO   `json:"goal"`
		Tasks []TaskDTO `json:"tasks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("%v: %s", err, rec.Body.String())
	}
	if got.Goal.TasksDone != 3 || len(got.Tasks) != 3 {
		t.Errorf("the goal shows %d of %d task(s) done; want 3 of 3", got.Goal.TasksDone, len(got.Tasks))
	}
	if got.Goal.TokensSpent != int64(len(stub.calls()))*10 {
		t.Errorf("the goal shows %d tokens spent for %d calls of 10", got.Goal.TokensSpent, len(stub.calls()))
	}

	// The timeline: each step says which version it kept, and each kept version
	// is on the timeline naming that step.
	rec = httptest.NewRecorder()
	r = getAs(user, "/v1/goals/"+goalID+"/timeline")
	r.SetPathValue("id", goalID)
	h.Timeline(rec, r)
	var tl struct {
		Events []EventDTO `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &tl); err != nil {
		t.Fatalf("%v: %s", err, rec.Body.String())
	}
	kept := map[string]string{} // task → version its summary names
	changed := map[string]int{} // task → artifact.changed events naming it
	for _, e := range tl.Events {
		switch e.Kind {
		case engine.EventTaskSucceeded:
			if m := keptVersion.FindStringSubmatch(e.Summary); m != nil {
				kept[e.TaskID] = m[1]
			}
		case engine.EventArtifactChanged:
			changed[e.TaskID]++
		}
	}
	variants, err := geo.List(ctx, projectOf(t, pool, goalID), 50)
	if err != nil {
		t.Fatal(err)
	}
	versions := map[string]int{}
	for _, v := range variants {
		versions[v.VersionID] = len(v.Document.Parts)
	}
	for i, task := range got.Tasks {
		v, ok := kept[task.ID]
		if !ok {
			t.Errorf("step %d (%s): the timeline does not say which version it kept", i+1, task.Title)
			continue
		}
		if _, exists := versions[v]; !exists {
			t.Errorf("step %d names version %s, which is not a version of the design", i+1, v)
		}
		if changed[task.ID] != 1 {
			t.Errorf("step %d: %d artifact.changed event(s) name it on the timeline, want 1", i+1, changed[task.ID])
		}
	}
	if len(versions) != 3 {
		t.Errorf("%d version(s) kept for three steps", len(versions))
	}
}

func projectOf(t *testing.T, pool *db.Pool, goalID string) string {
	t.Helper()
	var p string
	if err := pool.QueryRow(context.Background(), `select project_id from forge_goals where id = $1`, goalID).Scan(&p); err != nil {
		t.Fatal(err)
	}
	return p
}

// The workbench's "Start this" can ask for a build. Without the flag in the
// request, the control would be a box somebody ticks and the server never hears.
func TestWorkbench_StartThisSendsWhetherToPlanABuild(t *testing.T) {
	b, err := assetFS.ReadFile("assets/workbench.js")
	if err != nil {
		t.Fatal(err)
	}
	js := codeOnly(string(b))
	start := strings.Index(js, "function startThis()")
	end := strings.Index(js, "function startIt()")
	if start < 0 || end < start {
		t.Fatal("startThis or startIt is gone; this fence reads the request between them")
	}
	if !regexp.MustCompile(`build:\s*!!state\.planAsBuild`).MatchString(js[start:end]) {
		t.Error("startThis no longer sends build: !!state.planAsBuild to POST /v1/goals, so " +
			"ticking \"build it\" plans ordinary tasks instead of steps")
	}
	if !strings.Contains(js, "state.planAsBuild = ") {
		t.Error("nothing sets state.planAsBuild, so the control cannot change what is sent")
	}
}
