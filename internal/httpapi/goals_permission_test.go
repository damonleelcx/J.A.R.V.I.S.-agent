package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/identity"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
)

// Who may plan work in a project, through the goal endpoints that call a model.
//
// Bugfix 2026-09-15: POST /v1/goals never checked the project_id it was given,
// so anyone signed in could draft a goal into a stranger's project and have the
// planner's model call run there. See
// docs/bugfix/2026-09-15-a-goal-could-be-drafted-into-a-project-its-caller-was-not-in.md.
//
// Every claim here is about rows and calls, not only the status: on the handler
// before the fix a stranger was ALSO answered 404 — after the goal had been
// written and the model asked, by the handler's own read of what it had just
// made. A fence on the status alone would have passed on the defect.
//
// ‼️ The helpers are named perm* on purpose. The stacked build branches carry
// the same fix with their own helpers (insertUser, goalsIn, ...), and a name
// shared with them would stop the package compiling the day the two meet.

// A valid one-task plan, in the shape the planner parses.
const permPlanReply = `{"rationale":"clear enough","clarification_needed":"","tasks":[
	{"key":"draw","title":"draw it","instruction":"draw the bracket","inputs":{},
	 "expected_output":{"description":"a drawing"},"depends_on":[],"risk_tier":"r1"}]}`

// permLLM answers every call with a plan and counts the calls.
type permLLM struct {
	mu    sync.Mutex
	asked int
}

func (s *permLLM) Complete(_ context.Context, _ llm.Request) (*llm.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asked++
	return &llm.Response{Content: permPlanReply, FinishReason: "stop", Model: "stub",
		Usage: llm.Usage{TotalTokens: 10}}, nil
}

func (s *permLLM) ModelFor(llm.Role) string { return "stub" }

func (s *permLLM) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.asked
}

// permHarness is startHarness with a model configured.
func permHarness(t *testing.T) (*GoalHandlers, *db.Pool, *identity.User, *permLLM) {
	t.Helper()
	h, pool, user := startHarness(t)
	stub := &permLLM{}
	d := h.deps
	d.LLM = stub
	return NewGoalHandlers(d), pool, user, stub
}

func permPost(user *identity.User, target, body string) *http.Request {
	r := httptest.NewRequest("POST", target, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r.WithContext(context.WithValue(r.Context(), ctxKeyUser, user))
}

func permUser(t *testing.T, pool *db.Pool, email string) *identity.User {
	t.Helper()
	u := &identity.User{ID: id.New(id.PrefixUser), Email: email}
	if _, err := pool.Exec(context.Background(), `
		insert into forge_users (id, email, display_name, status, password_hash, password_algo,
			password_changed_at, created_at, updated_at)
		values ($1,$2,'Other','active','x','argon2id',now(),now(),now())`, u.ID, u.Email); err != nil {
		t.Fatal(err)
	}
	return u
}

func permGrant(t *testing.T, pool *db.Pool, projectID, userID, grantedBy, role string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		insert into forge_project_members (project_id, user_id, role, granted_by, granted_at, updated_at)
		values ($1,$2,$3,$4,now(),now())`, projectID, userID, role, grantedBy); err != nil {
		t.Fatal(err)
	}
}

func permProjectOf(t *testing.T, pool *db.Pool, goalID string) string {
	t.Helper()
	var p string
	if err := pool.QueryRow(context.Background(),
		`select project_id from forge_goals where id = $1`, goalID).Scan(&p); err != nil {
		t.Fatal(err)
	}
	return p
}

func permGoalsIn(t *testing.T, pool *db.Pool, projectID string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`select count(*) from forge_goals where project_id = $1`, projectID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// createIn posts the request the endpoint has always taken, naming projectID.
func createIn(h *GoalHandlers, user *identity.User, projectID string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]any{
		"title": "Mine now", "statement": "draw a bracket", "project_id": projectID,
	})
	rec := httptest.NewRecorder()
	h.CreateGoal(rec, permPost(user, "/v1/goals", string(body)))
	return rec
}

// A stranger to a project cannot draft a goal into it: NOT FOUND, no goal
// written, no model asked.
func TestCreateGoal_RefusesAProjectTheCallerIsNotAMemberOf(t *testing.T) {
	h, pool, user, stub := permHarness(t)
	owner := permUser(t, pool, "owner-elsewhere@example.com")
	projectID := permProjectOf(t, pool, seedGoal(t, pool, owner.ID, engine.GoalDraft, 0))

	rec := createIn(h, user, projectID)

	if rec.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d: %s", rec.Code, rec.Body.String())
	}
	if n := permGoalsIn(t, pool, projectID); n != 1 {
		t.Errorf("the project now holds %d goals; a stranger wrote one into it", n)
	}
	if n := stub.calls(); n != 0 {
		t.Errorf("the model was asked %d time(s) on behalf of a stranger to the project", n)
	}
}

// A viewer reads a project and plans nothing in it: FORBIDDEN, because a member
// already knows the project exists.
func TestCreateGoal_RefusesAViewerOfTheProject(t *testing.T) {
	h, pool, user, stub := permHarness(t)
	owner := permUser(t, pool, "owner-viewed@example.com")
	projectID := permProjectOf(t, pool, seedGoal(t, pool, owner.ID, engine.GoalDraft, 0))
	permGrant(t, pool, projectID, user.ID, owner.ID, "viewer")

	rec := createIn(h, user, projectID)

	if rec.Code != http.StatusForbidden {
		t.Errorf("want 403, got %d: %s", rec.Code, rec.Body.String())
	}
	if n := permGoalsIn(t, pool, projectID); n != 1 {
		t.Errorf("the project now holds %d goals; a viewer wrote one into it", n)
	}
	if n := stub.calls(); n != 0 {
		t.Errorf("the model was asked %d time(s) for a viewer", n)
	}
}

// The check refuses the wrong people and nobody else: a contributor, who holds
// goal.create, drafts and plans into the project; and a request naming no
// project still gets a new project of the caller's own, as it always has.
func TestCreateGoal_AContributorPlansIntoTheProjectAndNoProjectStillMakesOne(t *testing.T) {
	h, pool, user, stub := permHarness(t)
	owner := permUser(t, pool, "owner-shared@example.com")
	projectID := permProjectOf(t, pool, seedGoal(t, pool, owner.ID, engine.GoalDraft, 0))
	permGrant(t, pool, projectID, user.ID, owner.ID, "contributor")

	rec := createIn(h, user, projectID)
	if rec.Code != http.StatusCreated {
		t.Fatalf("a contributor was refused: want 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if n := permGoalsIn(t, pool, projectID); n != 2 {
		t.Errorf("the project holds %d goals, want 2 (the seeded one and the contributor's)", n)
	}
	if n := stub.calls(); n != 1 {
		t.Errorf("the model was asked %d time(s), want 1", n)
	}

	rec = createIn(h, user, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("a goal with no project was refused: want 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Goal GoalDTO `json:"goal"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var role string
	if err := pool.QueryRow(context.Background(), `
		select m.role from forge_goals g
		  join forge_project_members m on m.project_id = g.project_id and m.user_id = $2
		 where g.id = $1 and g.project_id <> $3`, body.Goal.ID, user.ID, projectID).Scan(&role); err != nil {
		t.Fatalf("the goal with no project did not land in a new project of the caller's: %v", err)
	}
	if role != "owner" {
		t.Errorf("the caller is %q of the project made for them, want owner", role)
	}
}

// Replanning is the other goal route that calls a model, and it was already
// checked — through the goal's own project, with goal.create. Fenced here so it
// stays that way: a stranger is NOT FOUND, a viewer FORBIDDEN, and neither
// spends a model call.
func TestReplan_RefusesAStrangerAndAViewerOfTheGoalsProject(t *testing.T) {
	h, pool, user, stub := permHarness(t)
	owner := permUser(t, pool, "owner-replanned@example.com")
	goalID := seedGoal(t, pool, owner.ID, engine.GoalDraft, 0)

	replan := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		r := permPost(user, "/v1/goals/"+goalID+"/plan", "")
		r.SetPathValue("id", goalID)
		h.Replan(rec, r)
		return rec
	}

	if rec := replan(); rec.Code != http.StatusNotFound {
		t.Errorf("stranger: want 404, got %d: %s", rec.Code, rec.Body.String())
	}
	permGrant(t, pool, permProjectOf(t, pool, goalID), user.ID, owner.ID, "viewer")
	if rec := replan(); rec.Code != http.StatusForbidden {
		t.Errorf("viewer: want 403, got %d: %s", rec.Code, rec.Body.String())
	}
	if n := stub.calls(); n != 0 {
		t.Errorf("the model was asked %d time(s) to replan a goal the caller may not plan", n)
	}
	var tasks int
	if err := pool.QueryRow(context.Background(),
		`select count(*) from forge_tasks where goal_id = $1`, goalID).Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	if tasks != 0 {
		t.Errorf("the goal now has %d tasks planned by a caller who may not plan it", tasks)
	}
}
