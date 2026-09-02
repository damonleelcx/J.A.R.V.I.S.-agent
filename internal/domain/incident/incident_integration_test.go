package incident_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/auth"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/identity"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/incident"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Incident response against a real database.
//
// The property under test is SAF-07's one ordering rule — evidence before
// destruction — and the fact that "preserve evidence" actually preserves
// something rather than writing an empty marker.

type harness struct {
	pool    *db.Pool
	svc     *incident.Service
	clk     *clock.Fake
	userID  string
	project string
	goalID  string
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	url := os.Getenv("FORGE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("FORGE_TEST_DATABASE_URL is unset; skipping live-database tests.")
	}
	ctx := context.Background()
	schema := "forge_inc_" + strings.ToLower(strings.NewReplacer("/", "_", "-", "_").Replace(t.Name()))
	if len(schema) > 60 {
		schema = schema[:60]
	}
	cfg := func(u string) config.DBConfig {
		return config.DBConfig{URL: u, MaxConns: 8, MinConns: 1,
			MaxConnLifetime: time.Hour, MaxConnIdleTime: time.Minute, ConnectTimeout: 10 * time.Second}
	}
	admin, err := db.Connect(ctx, cfg(url), logx.Discard())
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
	pool, err := db.Connect(ctx, cfg(url+sep+"search_path="+schema), logx.Discard())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MigrateFS(ctx, pool, db.Files, db.MigrationsDir, logx.Discard()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		if c, err := db.Connect(context.Background(), cfg(url), logx.Discard()); err == nil {
			_, _ = c.Exec(context.Background(), "drop schema if exists "+schema+" cascade")
			c.Close()
		}
	})

	clk := clock.NewFake(time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC))
	h := &harness{pool: pool, clk: clk, svc: incident.NewService(pool, clk, logx.Discard())}
	h.seed(t)
	return h
}

func (h *harness) seed(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	now := h.clk.Now()

	hash, _ := auth.HashPassword("correct horse battery staple")
	u := &identity.User{ID: id.New(id.PrefixUser), Email: "responder@example.com",
		Status: identity.StatusActive, PasswordHash: hash, PasswordAlgo: auth.AlgoArgon2id,
		PasswordChangedAt: now, CreatedAt: now, UpdatedAt: now}
	if err := identity.NewRepository().CreateUser(ctx, h.pool, u); err != nil {
		t.Fatal(err)
	}
	h.userID = u.ID
	h.project = id.New(id.PrefixProject)
	if _, err := h.pool.Exec(ctx,
		`insert into forge_projects (id, owner_id, name, created_at, updated_at) values ($1,$2,'P',$3,$3)`,
		h.project, h.userID, now); err != nil {
		t.Fatal(err)
	}
	h.goalID = id.New(id.PrefixGoal)
	if _, err := h.pool.Exec(ctx,
		`insert into forge_goals (id, project_id, created_by, title, statement, status, started_at, created_at, updated_at)
		 values ($1,$2,$3,'G','S','active',$4,$4,$4)`, h.goalID, h.project, h.userID, now); err != nil {
		t.Fatal(err)
	}
	// Some timeline to preserve.
	repo := engine.NewRepository()
	for i := 0; i < 3; i++ {
		payload, _ := json.Marshal(map[string]any{"i": i})
		if err := repo.AppendEvent(ctx, h.pool, &engine.Event{
			GoalID: h.goalID, Kind: engine.EventTaskStarted, Actor: engine.ActorExecutor,
			Summary: "step", Payload: payload,
		}, now); err != nil {
			t.Fatal(err)
		}
	}
}

func (h *harness) open(t *testing.T) *incident.Incident {
	t.Helper()
	i, err := h.svc.Open(context.Background(), &incident.Incident{
		ProjectID: h.project, GoalID: &h.goalID, Title: "credential in a log",
		Statement: "A token appeared in an artifact diff.", Severity: incident.SeverityHigh,
		OpenedBy: h.userID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return i
}

// SAF-07's one ordering rule. An incident response that stops, revokes and rolls
// back before gathering evidence has gathered the evidence of its own response.
func TestIncident_DestructiveActionsAreRefusedBeforeEvidenceIsPreserved(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	inc := h.open(t)

	for _, kind := range []incident.ActionKind{
		incident.ActionStop, incident.ActionRevoke, incident.ActionQuarantine, incident.ActionRollBack,
	} {
		_, err := h.svc.Act(ctx, inc.ID, &incident.Action{
			Kind: kind, Target: h.goalID, Outcome: incident.OutcomeDone, TakenBy: h.userID})
		if err == nil {
			t.Fatalf("%s was allowed before any evidence was preserved", kind)
		}
		if !errs.Is(err, errs.CodeEvidenceNotPreserved) {
			t.Fatalf("%s failed with %s; the responder needs to be told to preserve evidence first",
				kind, errs.CodeOf(err))
		}
	}

	// Notify and review are not destructive and are allowed at any point.
	if _, err := h.svc.Act(ctx, inc.ID, &incident.Action{
		Kind: incident.ActionNotify, Target: "security@example.com", TakenBy: h.userID}); err != nil {
		t.Fatalf("notifying somebody was refused before evidence: %v", err)
	}

	// A dry run changes nothing, so rehearsing the response first is allowed.
	if _, err := h.svc.Act(ctx, inc.ID, &incident.Action{
		Kind: incident.ActionStop, Target: h.goalID,
		Outcome: incident.OutcomeDryRun, TakenBy: h.userID}); err != nil {
		t.Fatalf("a dry run was refused before evidence: %v", err)
	}
	// And a dry run does NOT satisfy the rule, because nothing was captured.
	if _, err := h.svc.Act(ctx, inc.ID, &incident.Action{
		Kind: incident.ActionPreserveEvidence, Outcome: incident.OutcomeDryRun, TakenBy: h.userID}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Act(ctx, inc.ID, &incident.Action{
		Kind: incident.ActionStop, Target: h.goalID, Outcome: incident.OutcomeDone, TakenBy: h.userID}); err == nil {
		t.Fatal("a dry-run evidence capture satisfied the evidence-first rule; nothing was actually captured")
	}
}

// Preserving evidence must preserve something. An empty marker would satisfy the
// ordering rule and record nothing, which is the worst possible outcome: the
// process looks followed and the evidence is gone.
func TestIncident_PreservedEvidenceActuallyContainsTheState(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	inc := h.open(t)

	action, ev, err := h.svc.PreserveEvidence(ctx, inc.ID, h.userID)
	if err != nil {
		t.Fatal(err)
	}
	if action.Kind != incident.ActionPreserveEvidence || !action.Outcome.Changed() {
		t.Fatalf("the capture was recorded as %s/%s", action.Kind, action.Outcome)
	}
	if len(ev.Incomplete) > 0 {
		t.Fatalf("the capture could not read part of the system: %v", ev.Incomplete)
	}
	if ev.GoalStatus != "active" {
		t.Fatalf("the goal's status was recorded as %q", ev.GoalStatus)
	}
	if ev.ChainIntact == nil || !*ev.ChainIntact {
		t.Fatalf("the audit chain's verdict at capture time was not recorded: %+v", ev.ChainIntact)
	}
	if !strings.Contains(ev.ChainSummary, "3 event") {
		t.Fatalf("the chain summary does not describe the timeline it saw: %q", ev.ChainSummary)
	}
	// The snapshot is in the action's detail, where nothing later in the
	// response can reach it.
	var stored incident.Evidence
	if err := json.Unmarshal([]byte(action.Detail), &stored); err != nil {
		t.Fatalf("the action's detail is not the snapshot: %v", err)
	}
	if stored.GoalStatus != "active" {
		t.Fatal("the stored snapshot does not match what was captured")
	}
}

// The point of capturing the chain's verdict: after the response has written its
// own events, "was it intact when this started?" is no longer answerable by
// running the verifier.
func TestIncident_EvidenceOutlivesTheStateItDescribes(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	inc := h.open(t)

	action, _, err := h.svc.PreserveEvidence(ctx, inc.ID, h.userID)
	if err != nil {
		t.Fatal(err)
	}
	// The response now does the destructive thing.
	if _, err := h.svc.Act(ctx, inc.ID, &incident.Action{
		Kind: incident.ActionStop, Target: h.goalID, Detail: "paused the goal",
		Outcome: incident.OutcomeDone, TakenBy: h.userID}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(ctx, `update forge_goals set status = 'paused' where id = $1`, h.goalID); err != nil {
		t.Fatal(err)
	}

	back, err := h.svc.Find(ctx, inc.ID)
	if err != nil {
		t.Fatal(err)
	}
	var captured *incident.Action
	for i := range back.Actions {
		if back.Actions[i].ID == action.ID {
			captured = &back.Actions[i]
		}
	}
	if captured == nil {
		t.Fatal("the evidence action is gone")
	}
	if !strings.Contains(captured.Detail, `"goal_status": "active"`) {
		t.Fatalf("the preserved state was overwritten by what happened afterwards: %s", captured.Detail)
	}
}

// Containment is a claim about the world, not about the record.
func TestIncident_ContainmentNeedsSomethingToHaveBeenDone(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	inc := h.open(t)

	if _, _, err := h.svc.PreserveEvidence(ctx, inc.ID, h.userID); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Contain(ctx, inc.ID, h.userID); err == nil {
		t.Fatal("an incident where nothing had been stopped was marked contained")
	}
	if _, err := h.svc.Act(ctx, inc.ID, &incident.Action{
		Kind: incident.ActionStop, Target: h.goalID, Outcome: incident.OutcomeDone, TakenBy: h.userID}); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Contain(ctx, inc.ID, h.userID); err != nil {
		t.Fatalf("containment was refused after the goal was stopped: %v", err)
	}
}

// Closing requires a review. It is the only part of an incident anybody reads a
// year later.
func TestIncident_ClosingRequiresAReview(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	inc := h.open(t)

	if err := h.svc.Close(ctx, inc.ID, h.userID, "   "); err == nil {
		t.Fatal("an incident was closed with no review")
	} else if !errs.Is(err, errs.CodeIncidentOpen) {
		t.Fatalf("got %s", errs.CodeOf(err))
	}

	const review = "A token was written into an artifact diff by a tool that had been granted it."
	if err := h.svc.Close(ctx, inc.ID, h.userID, review); err != nil {
		t.Fatal(err)
	}
	back, err := h.svc.Find(ctx, inc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if back.Status != incident.StatusClosed || back.Review != review {
		t.Fatalf("status %q, review %q", back.Status, back.Review)
	}
	if back.ClosedBy == nil || *back.ClosedBy != h.userID {
		t.Fatal("the closure was not attributed")
	}
	// The review is also the last action, so the response reads in one place.
	last := back.Actions[len(back.Actions)-1]
	if last.Kind != incident.ActionReview || last.Detail != review {
		t.Fatalf("the review is not the last action: %+v", last)
	}
	// And a closed record does not accept more actions.
	if _, err := h.svc.Act(ctx, inc.ID, &incident.Action{
		Kind: incident.ActionNotify, Target: "x", TakenBy: h.userID}); err == nil {
		t.Fatal("an action was appended to a closed incident")
	}
}

// The database enforces the closure rule too, so a write that bypasses the
// service cannot leave a closed incident with no review.
func TestIncident_TheDatabaseAlsoRefusesAnUnreviewedClosure(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	inc := h.open(t)

	_, err := h.pool.Exec(ctx, `update forge_incidents set status = 'closed' where id = $1`, inc.ID)
	if err == nil {
		t.Fatal("the database accepted a closed incident with no review, closer or time")
	}
	if !strings.Contains(err.Error(), "closure_complete") {
		t.Fatalf("the write failed for a different reason: %v", err)
	}
}

// Actions are appended in order and each names who took it.
func TestIncident_ActionsAreAppendOnlyAndAttributed(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	inc := h.open(t)

	if _, _, err := h.svc.PreserveEvidence(ctx, inc.ID, h.userID); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []incident.ActionKind{incident.ActionStop, incident.ActionRevoke, incident.ActionNotify} {
		if _, err := h.svc.Act(ctx, inc.ID, &incident.Action{
			Kind: kind, Target: "t", Outcome: incident.OutcomeDone, TakenBy: h.userID}); err != nil {
			t.Fatal(err)
		}
	}
	back, err := h.svc.Find(ctx, inc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Actions) != 4 {
		t.Fatalf("%d actions", len(back.Actions))
	}
	for i, a := range back.Actions {
		if a.Seq != i+1 {
			t.Fatalf("action %d has seq %d", i, a.Seq)
		}
		if a.TakenBy == "" {
			t.Fatalf("action %d names nobody", i)
		}
	}
	// An unattributed action is refused outright.
	if _, err := h.svc.Act(ctx, inc.ID, &incident.Action{
		Kind: incident.ActionNotify, Target: "x", TakenBy: ""}); err == nil {
		t.Fatal("an action nobody took was appended")
	}
	// So is one that does not say what it acted on.
	if _, err := h.svc.Act(ctx, inc.ID, &incident.Action{
		Kind: incident.ActionStop, TakenBy: h.userID}); err == nil {
		t.Fatal("a stop action that named nothing was appended")
	}
}

// Evidence must not record where the credentials are kept: an incident record is
// read by several people, and a snapshot naming the variables would be a map.
func TestIncident_EvidenceNamesSecretsWithoutSayingWhereTheyLive(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.pool.Exec(ctx, `
		insert into forge_secrets (id, project_id, name, source, env_var, description, created_by, created_at, updated_at)
		values ($1,$2,'github_token','env','FORGE_SECRET_GITHUB','for the API',$3,$4,$4)`,
		id.New(id.PrefixSecret), h.project, h.userID, h.clk.Now()); err != nil {
		t.Fatal(err)
	}
	inc := h.open(t)
	action, ev, err := h.svc.PreserveEvidence(ctx, inc.ID, h.userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ev.ActiveSecrets) != 1 || ev.ActiveSecrets[0] != "github_token" {
		t.Fatalf("the live handles were not recorded: %v", ev.ActiveSecrets)
	}
	if strings.Contains(action.Detail, "FORGE_SECRET_GITHUB") {
		t.Fatalf("the snapshot names the environment variable a credential lives in: %s", action.Detail)
	}
}
