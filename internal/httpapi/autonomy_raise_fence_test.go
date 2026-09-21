package httpapi

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/access"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/identity"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Asking for a higher autonomy is refused, and the asking is recorded
// (PRD AGT-04; issue 21).
//
// # Why this endpoint
//
// POST /v1/goals/{id}/plan is where somebody would first try. The goal exists,
// its plan is being rewritten, and "while you are at it, let it do more" is the
// obvious next thought — it is the feature request this codebase will get.
//
// The source fence in internal/domain/engine holds that nothing WRITES the
// autonomy column after creation, and 0028 holds it from under the database.
// This holds the third part: that the surface answers in the requirement's own
// words and leaves a record, rather than rejecting an unknown JSON field and
// recording nothing at all.

// raiseHarness is startHarness with a model configured and a readable log.
//
// ‼️ Its own harness rather than permHarness: the audit event is half of what is
// being fenced, and permHarness keeps the discarding logger startHarness built.
func raiseHarness(t *testing.T) (*GoalHandlers, *db.Pool, *identity.User, *bytes.Buffer) {
	t.Helper()
	_, pool, user := startHarness(t)

	var buf bytes.Buffer
	d := testDeps()
	d.Pool = pool
	d.Clock = clock.System{}
	d.Log = logx.New(logx.Options{Output: &buf})
	d.Access = access.NewService(pool, d.Clock, logx.Discard())
	d.Config.Engine = config.EngineConfig{
		MaxTasksPerGoal: 50, MaxTaskDepth: 3,
		MaxTokensPerGoal: 100000, MaxCostCentsPerGoal: 500,
	}
	d.LLM = &permLLM{}
	return NewGoalHandlers(d), pool, user, &buf
}

func postPlan(t *testing.T, h *GoalHandlers, user *identity.User, goalID, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/goals/"+goalID+"/plan", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.SetPathValue("id", goalID)
	r = r.WithContext(context.WithValue(r.Context(), ctxKeyUser, user))
	rr := httptest.NewRecorder()
	h.Replan(rr, r)
	return rr
}

func autonomyOf(t *testing.T, pool *db.Pool, goalID string) string {
	t.Helper()
	var a string
	if err := pool.QueryRow(context.Background(),
		`select autonomy from forge_goals where id = $1`, goalID).Scan(&a); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestReplan_RefusesARaiseOfTheGoalsOwnAutonomy(t *testing.T) {
	h, pool, user, log := raiseHarness(t)
	goalID := seedGoal(t, pool, user.ID, engine.GoalDraft, 0)

	// The seeded goal is at sandbox_execute; approval_gated is one rung up.
	rr := postPlan(t, h, user, goalID, `{"autonomy":"approval_gated"}`)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("replanning with a raised autonomy answered %d, want 403.\n"+
			"PRD AGT-04 says the system never silently raises its own autonomy level; a "+
			"surface that accepts the field and plans anyway is that raise happening.\nbody: %s",
			rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "AGT-04") {
		t.Errorf("the refusal does not name the requirement it enforces, so the next person to "+
			"hit it learns nothing: %s", rr.Body.String())
	}
	if !strings.Contains(log.String(), string(logx.EventAutonomyRaiseRefused)) {
		t.Errorf("no %s audit event.\nAGT-04's word is SILENTLY: a refusal nothing records "+
			"leaves nobody able to answer whether anything ever asked.\nlog: %s",
			logx.EventAutonomyRaiseRefused, log.String())
	}
	if got := autonomyOf(t, pool, goalID); got != string(engine.AutonomySandboxExecute) {
		t.Errorf("the goal is now at %q; a refused request changed the row", got)
	}
}

// Naming the level it already has is not a request for anything.
//
// A client that reads a goal and echoes its fields back must not be refused: a
// rule that fires on an unchanged value teaches people to strip fields rather
// than to leave autonomy alone.
func TestReplan_EchoingTheGoalsOwnAutonomyIsNotARaise(t *testing.T) {
	h, pool, user, _ := raiseHarness(t)
	goalID := seedGoal(t, pool, user.ID, engine.GoalDraft, 0)

	rr := postPlan(t, h, user, goalID, `{"autonomy":"sandbox_execute"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("a client echoing back the level it read was refused: %d %s",
			rr.Code, rr.Body.String())
	}
}

// Lowering is refused here too, for a duller reason than a raise.
//
// Narrowing a goal is legitimate; doing it as a side effect of REPLANNING is
// not. This endpoint rewrites the plan, and a level that moved because somebody
// pressed "plan again" is a change nobody pressed a button for either.
func TestReplan_WillNotLowerAutonomyEither(t *testing.T) {
	h, pool, user, _ := raiseHarness(t)
	goalID := seedGoal(t, pool, user.ID, engine.GoalDraft, 0)

	rr := postPlan(t, h, user, goalID, `{"autonomy":"discuss"}`)
	if rr.Code == http.StatusOK {
		t.Fatal("replanning quietly lowered the goal's autonomy")
	}
	if got := autonomyOf(t, pool, goalID); got != string(engine.AutonomySandboxExecute) {
		t.Errorf("the goal is now at %q", got)
	}
}

// The database refuses a raise even when no Go code is in the way.
//
// The fence the other two cannot be: a repair script, a migration somebody wrote
// at 2am, a hand-edited row. 0028 is what makes autonomy write-once for writers
// who never found the rule.
func TestAutonomyRaiseIsRefusedByTheDatabase(t *testing.T) {
	_, pool, user := startHarness(t)
	goalID := seedGoal(t, pool, user.ID, engine.GoalDraft, 0)
	ctx := context.Background()

	_, err := pool.Exec(ctx,
		`update forge_goals set autonomy = 'approval_gated' where id = $1`, goalID)
	if err == nil {
		t.Fatal("a plain UPDATE raised a goal's autonomy.\n" +
			"PRD AGT-04's guarantee rested on the absence of a code path; 0028 is what holds " +
			"it against a writer that never went through one")
	}
	if !strings.Contains(err.Error(), "AGT-04") {
		t.Errorf("the database's refusal does not say what rule it is: %v", err)
	}

	// Lowering is still allowed: narrowing a goal mid-run is a safety act, and a
	// database that refused it would be the reason FORGE could not calm
	// something down.
	if _, err := pool.Exec(ctx,
		`update forge_goals set autonomy = 'draft' where id = $1`, goalID); err != nil {
		t.Fatalf("the database refused a LOWERING of autonomy: %v", err)
	}
	// And moving off 'prohibited' is the largest raise there is.
	if _, err := pool.Exec(ctx,
		`update forge_goals set autonomy = 'prohibited' where id = $1`, goalID); err != nil {
		t.Fatalf("the database refused a move to prohibited: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`update forge_goals set autonomy = 'discuss' where id = $1`, goalID); err == nil {
		t.Fatal("a goal was moved off 'prohibited'. Prohibited is a refusal, not a low setting: " +
			"turning it into a permission is the largest raise there is")
	}
}
