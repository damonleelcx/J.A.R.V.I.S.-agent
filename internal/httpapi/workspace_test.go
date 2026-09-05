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
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/claim"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/identity"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// The workspace API (PRD RSN-01, WRK-03, WRK-04) against live Postgres.
//
// Two things are under test here that the domain fences cannot reach: the
// authorisation boundary, since every endpoint takes ids from the client; and
// what the wire shape does NOT allow — there is no way to change a node's kind
// and no way for a client to name who signed something off.

type wsHarness struct {
	h       *WorkspaceHandlers
	access  *access.Service
	pool    *db.Pool
	svc     *workspace.Service
	owner   *identity.User
	other   *identity.User
	project string
	goalID  string
	taskID  string
	toolID  string
}

func workspaceHarness(t *testing.T) *wsHarness {
	t.Helper()

	url := os.Getenv("FORGE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("FORGE_TEST_DATABASE_URL is unset")
	}
	ctx := context.Background()
	schema := "forge_http_workspace"

	cfg := func(u string) config.DBConfig {
		return config.DBConfig{URL: u, MaxConns: 6, MinConns: 1,
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
	t.Cleanup(pool.Close)

	d := testDeps()
	d.Pool = pool
	d.Clock = clock.System{}
	// The real router wires this; a harness that calls handlers directly must
	// too, or every check refuses and the tests pass for the wrong reason.
	d.Access = access.NewService(pool, d.Clock, logx.Discard())

	now := time.Now().UTC()
	mk := func(email string) *identity.User {
		u := &identity.User{ID: id.New(id.PrefixUser), Email: email}
		if _, err := pool.Exec(ctx, `
			insert into forge_users (id, email, display_name, status, password_hash, password_algo,
				password_changed_at, created_at, updated_at)
			values ($1,$2,'T','active','x','argon2id',$3,$3,$3)`, u.ID, u.Email, now); err != nil {
			t.Fatal(err)
		}
		return u
	}
	w := &wsHarness{h: NewWorkspaceHandlers(d), pool: pool, access: d.Access,
		svc: workspace.NewService(pool, d.Clock, logx.Discard())}
	w.owner = mk("owner@example.com")
	w.other = mk("intruder@example.com")

	w.project = newProject(t, pool, d.Access, w.owner.ID, "P", now)
	w.goalID = id.New(id.PrefixGoal)
	if _, err := pool.Exec(ctx,
		`insert into forge_goals (id, project_id, created_by, title, statement, status, started_at, created_at, updated_at)
		 values ($1,$2,$3,'G','S','active',$4,$4,$4)`, w.goalID, w.project, w.owner.ID, now); err != nil {
		t.Fatal(err)
	}
	planID := id.New(id.PrefixPlan)
	if _, err := pool.Exec(ctx,
		`insert into forge_plans (id, goal_id, version, created_at) values ($1,$2,1,$3)`,
		planID, w.goalID, now); err != nil {
		t.Fatal(err)
	}
	w.taskID = id.New(id.PrefixTask)
	if _, err := pool.Exec(ctx, `
		insert into forge_tasks (id, goal_id, plan_id, title, instruction, status, idempotency_key,
			max_attempts, not_before, priority, risk_tier, created_at, updated_at)
		values ($1,$2,$3,'t','do','pending','k',3,$4,100,'r1',$4,$4)`,
		w.taskID, w.goalID, planID, now); err != nil {
		t.Fatal(err)
	}
	w.toolID = id.New(id.PrefixToolCall)
	if _, err := pool.Exec(ctx, `
		insert into forge_tool_calls (id, task_id, idempotency_key, tool_name, status,
			started_at, ended_at, duration_ms, created_at)
		values ($1,$2,'tk','workspace_write','succeeded',$3,$3,0,$3)`,
		w.toolID, w.taskID, now); err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *wsHarness) node(t *testing.T, kind workspace.Kind, title string, how claim.Epistemic) *workspace.Node {
	t.Helper()
	n, err := w.svc.Add(context.Background(), workspace.NewNode{
		ProjectID: w.project, Kind: kind, Title: title, How: how, CreatedBy: w.owner.ID})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// The design in the wire shape: there is no field for a node's kind on the edit
// endpoint, so a client cannot turn an assumption into a requirement even by
// trying. The strict decoder is what makes "not in the struct" mean "refused"
// rather than "quietly ignored".
func TestAPI_TheEditEndpointHasNoWayToChangeAKind(t *testing.T) {
	w := workspaceHarness(t)
	n := w.node(t, workspace.KindAssumption, "the plate is 3 mm", claim.Assumed)

	rec := httptest.NewRecorder()
	r := req(w.owner, "PATCH", "/v1/workspace/nodes/"+n.ID, `{"kind":"requirement","title":"the plate is 3 mm"}`)
	r.SetPathValue("id", n.ID)
	w.h.EditNode(rec, r)
	if rec.Code == http.StatusOK {
		t.Fatalf("an edit naming a new kind was accepted: %s", rec.Body.String())
	}

	back, err := w.svc.Repo().FindNode(context.Background(), w.pool, n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if back.Kind != workspace.KindAssumption {
		t.Fatalf("the node is now a %s", back.Kind)
	}
}

// Promotion over the wire keeps both, and says so.
func TestAPI_PromotionKeepsTheOriginalAndSaysWhy(t *testing.T) {
	w := workspaceHarness(t)
	n := w.node(t, workspace.KindAssumption, "the motor is NEMA 17", claim.Assumed)

	rec := httptest.NewRecorder()
	r := req(w.owner, "POST", "/v1/workspace/nodes/"+n.ID+"/promote",
		`{"kind":"requirement","how":"retrieved","source":"the datasheet","retire_source":true}`)
	r.SetPathValue("id", n.ID)
	w.h.Promote(rec, r)
	if rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Node        NodeDTO `json:"node"`
		DerivesFrom string  `json:"derives_from"`
		Effect      string  `json:"effect"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Node.Kind != string(workspace.KindRequirement) {
		t.Fatalf("the promoted node is a %s", body.Node.Kind)
	}
	if body.DerivesFrom != n.ID {
		t.Fatalf("the new node derives from %q, not from the assumption %s", body.DerivesFrom, n.ID)
	}
	if !strings.Contains(body.Effect, "still readable") {
		t.Fatalf("the response does not say the original survives: %s", body.Effect)
	}
	if _, err := w.svc.Repo().FindNode(context.Background(), w.pool, n.ID); err != nil {
		t.Fatal("the assumption disappeared")
	}
}

// A node carries what it is known by and whether that may be acted on, computed
// server-side. A browser deciding for itself would eventually decide differently.
func TestAPI_NodesCarryTheirEpistemicVerdict(t *testing.T) {
	w := workspaceHarness(t)
	w.node(t, workspace.KindAssumption, "guessed", claim.Assumed)
	w.node(t, workspace.KindEvidence, "measured", claim.Observed)

	rec := httptest.NewRecorder()
	w.h.Graph(rec, req(w.owner, "GET", "/v1/workspace/graph?project_id="+w.project, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Nodes []NodeDTO `json:"nodes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Nodes) != 2 {
		t.Fatalf("%d nodes came back", len(body.Nodes))
	}
	for _, n := range body.Nodes {
		if n.HowMeans == "" {
			t.Fatalf("node %q carries a label with no meaning", n.Title)
		}
		switch n.Title {
		case "measured":
			if !n.Actionable {
				t.Fatal("an observed piece of evidence came back as not actionable")
			}
		case "guessed":
			if n.Actionable {
				t.Fatal("an assumption came back as something a reader may act on")
			}
		}
	}
}

// Defects and gaps stay separate on the wire. A client that merged them would
// show a permanent red badge on every project that has ever existed.
func TestAPI_ReviewKeepsContradictionsApartFromIncompleteness(t *testing.T) {
	w := workspaceHarness(t)
	w.node(t, workspace.KindRequirement, "mount the motor", claim.Retrieved)

	rec := httptest.NewRecorder()
	w.h.Review(rec, req(w.owner, "GET", "/v1/workspace/review?project_id="+w.project, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Consistent bool             `json:"consistent"`
		Defects    []map[string]any `json:"defects"`
		Gaps       []map[string]any `json:"gaps"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Consistent {
		t.Fatalf("an unverified requirement made the project inconsistent: %+v", body.Defects)
	}
	if len(body.Gaps) == 0 {
		t.Fatal("nothing verifies or owns the requirement and no gap was reported")
	}
	if len(body.Defects) != 0 {
		t.Fatalf("gaps leaked into defects: %+v", body.Defects)
	}
}

// Every endpoint takes ids from the client, so the row has to say who it
// belongs to.
func TestAPI_WorkspaceRefusesAnotherOwnersProject(t *testing.T) {
	w := workspaceHarness(t)
	n := w.node(t, workspace.KindRequirement, "mount the motor", claim.Retrieved)

	for _, tc := range []struct {
		name string
		call func(*httptest.ResponseRecorder)
	}{
		{"graph", func(rec *httptest.ResponseRecorder) {
			w.h.Graph(rec, req(w.other, "GET", "/v1/workspace/graph?project_id="+w.project, ""))
		}},
		{"review", func(rec *httptest.ResponseRecorder) {
			w.h.Review(rec, req(w.other, "GET", "/v1/workspace/review?project_id="+w.project, ""))
		}},
		{"add node", func(rec *httptest.ResponseRecorder) {
			w.h.AddNode(rec, req(w.other, "POST", "/v1/workspace/nodes",
				`{"project_id":"`+w.project+`","kind":"requirement","title":"mine now"}`))
		}},
		{"edit node", func(rec *httptest.ResponseRecorder) {
			r := req(w.other, "PATCH", "/v1/workspace/nodes/"+n.ID, `{"title":"mine now"}`)
			r.SetPathValue("id", n.ID)
			w.h.EditNode(rec, r)
		}},
		{"promote", func(rec *httptest.ResponseRecorder) {
			r := req(w.other, "POST", "/v1/workspace/nodes/"+n.ID+"/promote", `{"kind":"criterion"}`)
			r.SetPathValue("id", n.ID)
			w.h.Promote(rec, r)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.call(rec)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("%s returned %d for another owner's project; it must read exactly like one that does not exist: %s",
					tc.name, rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "mount the motor") {
				t.Fatal("the refusal leaked the contents it refused")
			}
		})
	}
}

// An edge must be authorised at BOTH ends. One end only would let a caller
// attach their own node to somebody else's and learn that it exists.
func TestAPI_AnEdgeIsAuthorisedAtBothEnds(t *testing.T) {
	w := workspaceHarness(t)
	ctx := context.Background()
	mine := w.node(t, workspace.KindTest, "my test", "")

	// A second project the intruder does own, with a node in it.
	theirs := newProject(t, w.pool, w.access, w.other.ID, "Q", time.Now().UTC())
	theirNode, err := w.svc.Add(ctx, workspace.NewNode{
		ProjectID: theirs, Kind: workspace.KindRequirement, Title: "their requirement",
		How: claim.Retrieved, CreatedBy: w.other.ID})
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	w.h.Relate(rec, req(w.other, "POST", "/v1/workspace/edges",
		`{"kind":"verifies","from_id":"`+mine.ID+`","to_id":"`+theirNode.ID+`"}`))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("an edge was drawn from another owner's node (status %d): %s", rec.Code, rec.Body.String())
	}
}

// WRK-04 over the wire: all seven facts, and "usable" computed here rather than
// left for a client to infer from two of them.
func TestAPI_ArtifactHistoryCarriesAllSevenFacts(t *testing.T) {
	w := workspaceHarness(t)
	ctx := context.Background()

	a, v, err := w.svc.RecordChange(ctx, workspace.Change{
		ProjectID: w.project, Path: "src/bracket.scad", InitiatorID: w.owner.ID,
		Agent: workspace.AgentExecutor, ToolCallID: &w.toolID,
		Inputs: map[string]any{"prompt": "make it"}, Diff: "+ module bracket() {}",
		GoalID: w.goalID, TaskID: &w.taskID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.svc.Verify(ctx, v.ID, workspace.Passed, "checks passed"); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	r := req(w.owner, "GET", "/v1/workspace/artifacts/"+a.ID, "")
	r.SetPathValue("id", a.ID)
	w.h.ArtifactHistory(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Versions []map[string]any `json:"versions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Versions) != 1 {
		t.Fatalf("%d versions", len(body.Versions))
	}
	got := body.Versions[0]
	for _, field := range []string{"initiator_id", "agent", "tool_call_id", "inputs", "diff",
		"verification_state", "human_disposition", "event_id"} {
		if _, ok := got[field]; !ok {
			t.Fatalf("WRK-04 names %s and the wire shape omits it: %v", field, got)
		}
	}
	// A machine passed it and no person has looked: not usable, and the reason
	// says so in words.
	if got["usable"] != false {
		t.Fatal("a version nobody had looked at was reported usable because the tests passed")
	}
	if !strings.Contains(got["usable_why"].(string), "not a sign-off") {
		t.Fatalf("the reason does not explain the distinction: %v", got["usable_why"])
	}
	if got["event_id"] == nil {
		t.Fatal("the version does not point at the timeline event that recorded it")
	}
}

// PRD SAF-05 on the wire: the deciding person is the caller, never a field.
func TestAPI_DispositionAuthorIsTheCallerNotTheBody(t *testing.T) {
	w := workspaceHarness(t)
	ctx := context.Background()

	_, v, err := w.svc.RecordChange(ctx, workspace.Change{
		ProjectID: w.project, Path: "src/bracket.scad", InitiatorID: w.owner.ID,
		Agent: workspace.AgentExecutor, ToolCallID: &w.toolID,
		Inputs: map[string]any{}, Diff: "", GoalID: w.goalID,
	})
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	r := req(w.owner, "POST", "/v1/workspace/versions/"+v.ID+"/disposition",
		`{"disposition":"accepted","dispositioned_by":"`+w.other.ID+`"}`)
	r.SetPathValue("id", v.ID)
	w.h.Dispose(rec, r)
	if rec.Code == http.StatusOK {
		t.Fatalf("a body naming its own approver was accepted: %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	r = req(w.owner, "POST", "/v1/workspace/versions/"+v.ID+"/disposition",
		`{"disposition":"accepted","reason":"looks right"}`)
	r.SetPathValue("id", v.ID)
	w.h.Dispose(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	back, err := w.svc.Repo().FindVersion(ctx, w.pool, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if back.DispositionedBy == nil || *back.DispositionedBy != w.owner.ID {
		t.Fatalf("the acceptance was attributed to %v; the caller was %s", back.DispositionedBy, w.owner.ID)
	}
}

// There is no endpoint for recording a change, and that is deliberate: WRK-04's
// seven include the tool call that made it, and a client naming one would be
// putting a fabricated row in the idempotency ledger.
func TestAPI_ThereIsNoWayToPostAnArtifactVersion(t *testing.T) {
	d := testDeps()
	router := NewRouter(d)

	for _, target := range []string{"/v1/workspace/versions", "/v1/workspace/artifacts"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest("POST", target, strings.NewReader("{}")))
		if rec.Code == http.StatusCreated || rec.Code == http.StatusOK {
			t.Fatalf("POST %s is routed; a client can assert which tool made a change", target)
		}
	}
}

// The reason a version may not be relied on is written for a person.
//
// # The defect this holds
//
// usable_why used to be err.Error(), which prefixes the operation and the error
// registry's generic cause. Every version in a project therefore carried
// "workspace.Version.Usable: VALIDATION_FAILED: One or more request fields
// failed validation" ahead of the sentence that says what is actually true —
// and that generic half is false here, since a version nobody has looked at is
// not a failed request field. It went unnoticed while nothing rendered the
// field; the workbench's Checks panel shows it against every version at once,
// which is where two lines of machinery repeated seven times became the panel.
//
// Fenced on both halves: the sentence must be there, and the machinery must not.
func TestTheReasonAVersionIsNotUsableIsWrittenForAPerson(t *testing.T) {
	for _, tc := range []struct {
		name string
		v    workspace.Version
		want string
	}{
		{"pending", workspace.Version{Version: 1, Verification: workspace.Unverified,
			Disposition: workspace.Pending}, "not a sign-off"},
		{"rejected", workspace.Version{Version: 2, Verification: workspace.Passed,
			Disposition: workspace.Rejected, DispositionReason: "wrong hole pattern"},
			"rejected by a person"},
		{"superseded", workspace.Version{Version: 3, Verification: workspace.Passed,
			Disposition: workspace.Superseded}, "read the current version instead"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := usableWhy(tc.v.Usable())
			if !strings.Contains(got, tc.want) {
				t.Errorf("the reason does not say what is true: %q does not contain %q", got, tc.want)
			}
			for _, machinery := range []string{"workspace.Version.Usable", "VALIDATION_FAILED",
				"CONFLICT", "FORBIDDEN", "request fields failed validation"} {
				if strings.Contains(got, machinery) {
					t.Errorf("the reason shown to a person carries %q, which is for a log: %q",
						machinery, got)
				}
			}
		})
	}
	if got := usableWhy(nil); strings.Contains(got, ":") {
		t.Errorf("the usable case should read as a sentence, not an error: %q", got)
	}
}

// The graph carries the rules in force on the project it belongs to.
//
// # Why this is on the graph response and why it is fenced
//
// The ceiling used to exist only in a terminal: a person in the browser met a
// refusal with no way to find out what it was about, which is the shape of the
// defect this whole area removed — a rule that never reaches the surface people
// use. The workbench already fetches this endpoint the first time a project
// exists, so the domain rides along rather than costing a second endpoint.
//
// Fenced because it is optional data on a response nobody would notice losing:
// the panel would simply stop rendering, and a silent panel looks exactly like a
// project with nothing to say.
func TestGraph_CarriesTheDomainInForce(t *testing.T) {
	h := workspaceHarness(t)
	ctx := context.Background()

	id, err := h.svc.EnsureProject(ctx, h.pool, "", h.owner.ID, "Bridge", "Civil engineering")
	if err != nil {
		t.Fatal(err)
	}
	body := graphBodyFor(t, h, id)

	domain, ok := body["domain"].(map[string]any)
	if !ok {
		t.Fatalf("the graph carries no domain, so the workbench cannot say which rules "+
			"apply or how far work may go:\n%v", body)
	}
	for _, want := range []string{"pack", "industry", "boundary", "requires", "ceiling"} {
		if s, _ := domain[want].(string); strings.TrimSpace(s) == "" {
			t.Errorf("the domain carries no %s", want)
		}
	}
	if domain["ceiling"] != "r1" {
		t.Errorf("ceiling = %v; expected the civil pack's r1", domain["ceiling"])
	}
	if _, raised := domain["authority"]; raised {
		t.Error("a project with nobody recorded reports a review authority")
	}
	if domain["can_record_authority"] != true {
		t.Errorf("the owner is not told they may record an authority (%v), so the panel "+
			"offers no control to the one person who can use it", domain["can_record_authority"])
	}
}

// A member who may NOT record one is told so, and the flag is only an affordance.
//
// # Why the flag exists and why it is not the control
//
// The panel has to decide whether to offer a button, and offering one that
// always 403s is worse than offering none. So the graph says whether THIS caller
// may write.
//
// It is not a permission. PUT and DELETE authorise server-side whatever the flag
// said, and this asserts BOTH halves: a maintainer is told no, and a maintainer
// who ignored that and called PUT anyway is still refused. A future reader must
// not mistake the affordance for the gate and stop checking there.
func TestGraph_TheRecordAffordanceIsNotThePermission(t *testing.T) {
	h := workspaceHarness(t)
	ctx := context.Background()

	id, err := h.svc.EnsureProject(ctx, h.pool, "", h.owner.ID, "Bridge", "Civil engineering")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.access.SetRole(ctx, access.Grant{
		ProjectID: id, UserID: h.other.ID, Role: access.RoleMaintainer, By: h.owner.ID,
	}); err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest("GET", "/v1/workspace/graph?project_id="+id, nil)
	r = r.WithContext(context.WithValue(ctx, ctxKeyUser, h.other))
	rec := httptest.NewRecorder()
	h.h.Graph(rec, r)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	domain, _ := body["domain"].(map[string]any)
	if domain == nil {
		t.Fatal("no domain on the response")
	}
	if domain["can_record_authority"] != false {
		t.Errorf("a maintainer is told they may record an authority (%v); the panel would "+
			"offer a control that always fails", domain["can_record_authority"])
	}
	// And the flag is not what stops them.
	put := reviewAuthReq(t, h, "PUT", id, `{"holder":"Someone Else"}`, h.other)
	if put.Code != 403 {
		t.Errorf("a maintainer who ignored the affordance recorded an authority (%d). "+
			"The flag is a hint; the gate is requirePermission on the write", put.Code)
	}
}

// A raised ceiling is reported WITH the caveat, never the holder alone.
//
// A client showing the name without it would present a claim as a credential.
// That is the failure docs/qualified-review.md exists to prevent, and the
// caveat travelling in the same object is what makes showing one without the
// other a deliberate act rather than an oversight.
func TestGraph_ARaisedCeilingTravelsWithItsCaveat(t *testing.T) {
	h := workspaceHarness(t)
	ctx := context.Background()

	id, err := h.svc.EnsureProject(ctx, h.pool, "", h.owner.ID, "Bridge", "Civil engineering")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.svc.RecordReviewAuthority(ctx, h.pool, id,
		"R. Okonkwo", "CEng MICE 481920", h.owner.ID); err != nil {
		t.Fatal(err)
	}

	domain, _ := graphBodyFor(t, h, id)["domain"].(map[string]any)
	if domain == nil {
		t.Fatal("no domain on the response")
	}
	if domain["ceiling"] != "r2" {
		t.Errorf("ceiling = %v; a recorded authority should have raised it to r2", domain["ceiling"])
	}
	a, ok := domain["authority"].(map[string]any)
	if !ok {
		t.Fatal("the ceiling rose and the response does not say what it rests on")
	}
	if a["holder"] != "R. Okonkwo" {
		t.Errorf("holder = %v", a["holder"])
	}
	if a["verified"] != false {
		t.Errorf("verified = %v; this build verifies nothing and must not imply it", a["verified"])
	}
	caveat, _ := a["caveat"].(string)
	if !strings.Contains(caveat, "RECORDED, NOT VERIFIED") {
		t.Errorf("the authority travels without its caveat, so a client can render a claim "+
			"as a credential without doing anything wrong: %q", caveat)
	}
}

func graphBodyFor(t *testing.T, h *wsHarness, projectID string) map[string]any {
	t.Helper()
	r := httptest.NewRequest("GET", "/v1/workspace/graph?project_id="+projectID, nil)
	r = r.WithContext(context.WithValue(context.Background(), ctxKeyUser, h.owner))
	rec := httptest.NewRecorder()
	h.h.Graph(rec, r)
	if rec.Code != 200 {
		t.Fatalf("graph returned %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	return body
}
