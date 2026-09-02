package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/access"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/identity"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// These cover the workbench's "Start this" path — the one place a browser can
// turn a conversation into work. They run against live Postgres because every
// claim being made here is about rows: which goal exists, what status it holds,
// and what the timeline says happened. A fake store would let the test agree
// with itself about all three.
//
// No model is involved. Activation touches no LLM, and the one endpoint that
// does (CreateGoal) is asserted here only for its no-model behaviour.

func startHarness(t *testing.T) (*GoalHandlers, *db.Pool, *identity.User) {
	t.Helper()

	url := os.Getenv("FORGE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("FORGE_TEST_DATABASE_URL is unset")
	}
	ctx := context.Background()
	schema := "forge_http_start"

	admin, err := db.Connect(ctx, config.DBConfig{
		URL: url, MaxConns: 2, MinConns: 1,
		MaxConnLifetime: time.Hour, MaxConnIdleTime: time.Minute, ConnectTimeout: 10 * time.Second,
	}, logx.Discard())
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err := admin.Exec(ctx, "drop schema if exists "+schema+" cascade"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "create schema "+schema); err != nil {
		t.Fatal(err)
	}

	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	pool, err := db.Connect(ctx, config.DBConfig{
		URL: url + sep + "search_path=" + schema, MaxConns: 6, MinConns: 1,
		MaxConnLifetime: time.Hour, MaxConnIdleTime: time.Minute, ConnectTimeout: 10 * time.Second,
	}, logx.Discard())
	if err != nil {
		t.Fatal(err)
	}
	// The real migration chain, not a hand-written CREATE TABLE: a fixture that
	// invents its own schema tests the fixture.
	if _, err := db.MigrateFS(ctx, pool, db.Files, db.MigrationsDir, logx.Discard()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	d := testDeps()
	d.Pool = pool
	d.Clock = clock.System{}
	d.Access = access.NewService(pool, d.Clock, logx.Discard())
	d.Config.Engine = config.EngineConfig{
		MaxTasksPerGoal: 50, MaxTaskDepth: 3,
		MaxTokensPerGoal: 100000, MaxCostCentsPerGoal: 500,
		MaxWallClockPerGoal: time.Hour,
	}

	now := time.Now().UTC()
	user := &identity.User{ID: id.New(id.PrefixUser), Email: "start@example.com"}
	if _, err := pool.Exec(ctx, `
		insert into forge_users (id, email, display_name, status, password_hash, password_algo,
			password_changed_at, created_at, updated_at)
		values ($1,$2,'Start Tester','active','x','argon2id',$3,$3,$3)`,
		user.ID, user.Email, now); err != nil {
		t.Fatal(err)
	}
	return NewGoalHandlers(d), pool, user
}

// as builds a request that the handler will see as authenticated, without
// standing up the whole session stack: the thing under test is the goal
// transition, not the cookie.
func as(user *identity.User, method, target string) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader("{}"))
	r.Header.Set("Content-Type", "application/json")
	return r.WithContext(context.WithValue(r.Context(), ctxKeyUser, user))
}

func seedGoal(t *testing.T, pool *db.Pool, ownerID string, status engine.GoalStatus, tasks int) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()

	projectID := id.New(id.PrefixProject)
	if _, err := pool.Exec(ctx, `
		insert into forge_projects (id, owner_id, name, pack, created_at, updated_at)
		values ($1,$2,'Test project','software',$3,$3)`, projectID, ownerID, now); err != nil {
		t.Fatal(err)
	}
	// Membership, not owner_id, is what authorisation reads (PRD SEC-02). A
	// fixture that inserted the project and stopped would leave its own owner
	// unable to see it — which is production behaviour, and the reason this
	// line is here rather than the check being relaxed.
	if err := access.NewService(pool, clock.System{}, logx.Discard()).
		EnsureOwner(ctx, pool, projectID, ownerID); err != nil {
		t.Fatal(err)
	}
	goalID := id.New(id.PrefixGoal)
	if _, err := pool.Exec(ctx, `
		insert into forge_goals (id, project_id, created_by, title, statement, status,
			autonomy, risk_tier, completion_criteria, created_at, updated_at, started_at)
		values ($1,$2,$3,'Seeded','Seeded statement',$4,'sandbox_execute','r1','[]'::jsonb,$5,$5,$6)`,
		goalID, projectID, ownerID, string(status), now,
		map[bool]any{true: now, false: nil}[status == engine.GoalActive]); err != nil {
		t.Fatal(err)
	}
	repo := engine.NewRepository()
	planID := id.New(id.PrefixPlan)
	if tasks > 0 {
		if _, err := pool.Exec(ctx, `
			insert into forge_plans (id, goal_id, version, rationale, author, created_at)
			values ($1,$2,1,'seeded','planner',$3)`, planID, goalID, now); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < tasks; i++ {
		task := &engine.Task{
			ID: id.New(id.PrefixTask), GoalID: goalID, PlanID: planID,
			IdempotencyKey: "seeded-" + string(rune('a'+i)), Title: "Seeded task",
			Instruction: "do nothing", Status: engine.StatusPending,
			RiskTier: engine.RiskR1, MaxAttempts: 3,
			Inputs: json.RawMessage(`{}`), ExpectedOutput: json.RawMessage(`{}`),
			CreatedAt: now, UpdatedAt: now,
		}
		if err := repo.CreateTask(ctx, pool, task, nil); err != nil {
			t.Fatal(err)
		}
	}
	return goalID
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response was not JSON: %v\n%s", err, rec.Body.String())
	}
	return body
}

// A draft goal starts, and the timeline names the human who started it.
//
// PRD AGT-07: a consequential transition carries the named human authority.
// Before the workbench had a Start button this transition wrote nothing at all,
// so a reader could see a goal running and not who decided that.
func TestStartGoal_ActivatesAndAttributes(t *testing.T) {
	h, pool, user := startHarness(t)
	goalID := seedGoal(t, pool, user.ID, engine.GoalDraft, 2)

	rec := httptest.NewRecorder()
	r := as(user, "POST", "/v1/goals/"+goalID+"/start")
	r.SetPathValue("id", goalID)
	h.StartGoal(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var status string
	var startedAt *time.Time
	if err := pool.QueryRow(context.Background(),
		`select status, started_at from forge_goals where id = $1`, goalID).
		Scan(&status, &startedAt); err != nil {
		t.Fatal(err)
	}
	if status != string(engine.GoalActive) {
		t.Fatalf("goal status is %q, want active", status)
	}
	if startedAt == nil {
		t.Fatal("started_at was not stamped, so the console cannot say when work began")
	}

	var kind, actor string
	var actorID *string
	if err := pool.QueryRow(context.Background(), `
		select kind, actor, actor_id from forge_events
		 where goal_id = $1 and kind = $2`, goalID, engine.EventGoalActivated).
		Scan(&kind, &actor, &actorID); err != nil {
		t.Fatalf("no %s event on the timeline: %v", engine.EventGoalActivated, err)
	}
	if actor != string(engine.ActorHuman) {
		t.Fatalf("activation was attributed to %q, want human", actor)
	}
	if actorID == nil || *actorID != user.ID {
		t.Fatalf("activation names %v, want the account that pressed the button (%s)", actorID, user.ID)
	}
}

// Pressing Start twice is a double click, a second tab, or a retry. It is not a
// defect report.
//
// Regression: the state machine refused active→active with INVARIANT_VIOLATED,
// which the error registry renders as HTTP 500 reading "it indicates a logic
// defect, not a user error" — shown to someone who clicked twice. Found by
// clicking twice.
func TestStartGoal_AlreadyActiveIsNotAnError(t *testing.T) {
	h, pool, user := startHarness(t)
	goalID := seedGoal(t, pool, user.ID, engine.GoalDraft, 1)

	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		r := as(user, "POST", "/v1/goals/"+goalID+"/start")
		r.SetPathValue("id", goalID)
		h.StartGoal(rec, r)
		if rec.Code != http.StatusOK {
			t.Fatalf("press %d returned %d, want 200: %s", i+1, rec.Code, rec.Body.String())
		}
	}

	// And exactly one activation, so the timeline does not report the goal
	// being started twice when it was started once.
	var events int
	if err := pool.QueryRow(context.Background(),
		`select count(*) from forge_events where goal_id = $1 and kind = $2`,
		goalID, engine.EventGoalActivated).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("timeline has %d activation events, want exactly 1", events)
	}
}

// A goal with no tasks must not become "active".
//
// PRD AGT-08 makes running and proposed distinct states. An activated goal with
// an empty DAG looks exactly like work and never does any: status active,
// nothing claimable, nothing ever finishing.
func TestStartGoal_RefusesAGoalWithNoTasks(t *testing.T) {
	h, pool, user := startHarness(t)
	goalID := seedGoal(t, pool, user.ID, engine.GoalDraft, 0)

	rec := httptest.NewRecorder()
	r := as(user, "POST", "/v1/goals/"+goalID+"/start")
	r.SetPathValue("id", goalID)
	h.StartGoal(rec, r)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", rec.Code, rec.Body.String())
	}
	var status string
	if err := pool.QueryRow(context.Background(),
		`select status from forge_goals where id = $1`, goalID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != string(engine.GoalDraft) {
		t.Fatalf("goal moved to %q despite the refusal", status)
	}
}

// Another account's goal is reported as absent, not as forbidden: the endpoint
// must not be a membership oracle.
func TestStartGoal_OtherOwnersGoalIsNotFound(t *testing.T) {
	h, pool, user := startHarness(t)
	other := &identity.User{ID: id.New(id.PrefixUser), Email: "other@example.com"}
	now := time.Now().UTC()
	if _, err := pool.Exec(context.Background(), `
		insert into forge_users (id, email, display_name, status, password_hash, password_algo,
			password_changed_at, created_at, updated_at)
		values ($1,$2,'Other','active','x','argon2id',$3,$3,$3)`, other.ID, other.Email, now); err != nil {
		t.Fatal(err)
	}
	goalID := seedGoal(t, pool, other.ID, engine.GoalDraft, 1)

	rec := httptest.NewRecorder()
	r := as(user, "POST", "/v1/goals/"+goalID+"/start")
	r.SetPathValue("id", goalID)
	h.StartGoal(rec, r)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

// A deployment with no model must SAY it cannot plan, rather than failing in a
// way that reads as a bug. Reading and starting goals need no model, so the
// endpoint mounts either way.
func TestCreateGoal_WithoutAModelExplainsItself(t *testing.T) {
	d := testDeps()
	d.Clock = clock.System{}
	h := NewGoalHandlers(d) // Deps.LLM is nil

	rec := httptest.NewRecorder()
	user := &identity.User{ID: id.New(id.PrefixUser), Email: "nomodel@example.com"}
	r := as(user, "POST", "/v1/goals")
	h.CreateGoal(rec, r)

	if rec.Code != http.StatusInternalServerError && rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unexpected status %d: %s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	errObj, _ := body["error"].(map[string]any)
	if errObj["code"] != string(errs.CodeConfigInvalid) {
		t.Fatalf("want CONFIG_INVALID, got %v: %s", errObj["code"], rec.Body.String())
	}
	// The specific text ("set FORGE_LLM_API_KEY") is withheld from the BODY on
	// purpose: respond.go suppresses `detail` on 5xx so a server-side error
	// cannot leak internal structure to a caller. It reaches the operator
	// through the error log instead. What the caller must still get is a code
	// and a remedy, so those are what is asserted.
	if errObj["remedy"] == "" || errObj["remedy"] == nil {
		t.Fatalf("no remedy offered: %s", rec.Body.String())
	}
}

// The defaults a browser gets with no risk or autonomy stated must be the ones
// the terminal already uses. Two surfaces disagreeing about what an unspecified
// goal means is how a low-risk default quietly becomes a high-risk one.
func TestDefaultsMatchTheCLI(t *testing.T) {
	autonomy, risk := defaultsFor("", "")
	if autonomy != engine.AutonomySandboxExecute {
		t.Fatalf("default autonomy is %q, want %q (forgectl goal new --autonomy default)",
			autonomy, engine.AutonomySandboxExecute)
	}
	if risk != engine.RiskR1 {
		t.Fatalf("default risk is %q, want %q (forgectl goal new --risk default)", risk, engine.RiskR1)
	}
	// An unrecognised value is passed through so validation can name it, never
	// silently replaced — a typo must not become a quiet downgrade.
	if a, r := defaultsFor("wide_open", "r9"); a.Valid() || r.Valid() {
		t.Fatalf("unrecognised values were accepted: %q %q", a, r)
	}
}
