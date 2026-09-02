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
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/memory"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// The memory API (PRD MEM-02), against live Postgres.
//
// What these are really for is the authorisation boundary. Every one of these
// endpoints takes an id from the client — an item id, a project id, an owner —
// and the only thing standing between a user and somebody else's personal
// preferences is that each of them is checked against the row rather than
// trusted. So most of what follows is one user trying to reach another's memory.

type memHarness struct {
	h       *MemoryHandlers
	pool    *db.Pool
	svc     *memory.Service
	owner   *identity.User
	other   *identity.User
	project string
	goalID  string
}

func memoryHarness(t *testing.T) *memHarness {
	t.Helper()

	url := os.Getenv("FORGE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("FORGE_TEST_DATABASE_URL is unset")
	}
	ctx := context.Background()
	schema := "forge_http_memory"

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
	d.Access = access.NewService(pool, d.Clock, logx.Discard())

	now := time.Now().UTC()
	mk := func(email string) *identity.User {
		u := &identity.User{ID: id.New(id.PrefixUser), Email: email}
		if _, err := pool.Exec(ctx, `
			insert into forge_users (id, email, display_name, status, password_hash, password_algo,
				password_changed_at, created_at, updated_at)
			values ($1,$2,'Tester','active','x','argon2id',$3,$3,$3)`, u.ID, u.Email, now); err != nil {
			t.Fatal(err)
		}
		return u
	}
	m := &memHarness{h: NewMemoryHandlers(d), pool: pool, svc: memory.NewService(pool, d.Clock, logx.Discard())}
	m.owner = mk("owner@example.com")
	m.other = mk("intruder@example.com")

	m.project = newProject(t, pool, d.Access, m.owner.ID, "P", now)
	m.goalID = id.New(id.PrefixGoal)
	if _, err := pool.Exec(ctx, `
		insert into forge_goals (id, project_id, created_by, title, statement, status, started_at, created_at, updated_at)
		values ($1,$2,$3,'G','S','active',$4,$4,$4)`, m.goalID, m.project, m.owner.ID, now); err != nil {
		t.Fatal(err)
	}
	return m
}

func req(user *identity.User, method, target, body string) *http.Request {
	if body == "" {
		body = "{}"
	}
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r.WithContext(context.WithValue(r.Context(), ctxKeyUser, user))
}

func (m *memHarness) write(t *testing.T, scope memory.Scope, owner, key string, value any) *memory.Item {
	t.Helper()
	item, err := m.svc.Remember(context.Background(), memory.Write{
		Scope: scope, Owner: owner, Key: key, Value: value, How: claim.Observed, Source: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return item
}

// MEM-02's "show why an item was retrieved", on the wire. A client must never
// have to guess, and a blank reason must never render as one.
func TestAPI_RecallCarriesTheReasonForEveryItem(t *testing.T) {
	m := memoryHarness(t)
	m.write(t, memory.ScopeProject, m.project, "bolt.size", "M3")
	m.write(t, memory.ScopeUser, m.owner.ID, "units", "metric")

	rec := httptest.NewRecorder()
	m.h.Recall(rec, req(m.owner, "GET", "/v1/memory/recall?project_id="+m.project, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("recall returned %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Recalled []RecalledDTO `json:"recalled"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Recalled) < 2 {
		t.Fatalf("%d items recalled; the project note and the personal one were both live", len(body.Recalled))
	}
	for _, r := range body.Recalled {
		if strings.TrimSpace(r.Why) == "" || strings.TrimSpace(r.WhyDetail) == "" {
			t.Fatalf("item %q came back over the wire with no reason", r.Item.Key)
		}
		if r.Item.HowMeans == "" {
			t.Fatalf("item %q carries a label with no meaning; the browser would have to invent one", r.Item.Key)
		}
	}
}

// Personal memory is the caller's own, whatever the query string says.
func TestAPI_RecallCannotReadAnotherUsersPersonalMemory(t *testing.T) {
	m := memoryHarness(t)
	m.write(t, memory.ScopeUser, m.owner.ID, "units", "metric")

	rec := httptest.NewRecorder()
	// The intruder names the owner every way the endpoint accepts an id.
	m.h.Recall(rec, req(m.other, "GET", "/v1/memory/recall?user_id="+m.owner.ID+"&owner="+m.owner.ID, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("recall returned %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "metric") {
		t.Fatalf("another user's personal memory came back: %s", rec.Body.String())
	}
}

func TestAPI_ListRefusesAProjectTheCallerDoesNotOwn(t *testing.T) {
	m := memoryHarness(t)
	m.write(t, memory.ScopeProject, m.project, "supplier", "Acme")

	rec := httptest.NewRecorder()
	m.h.List(rec, req(m.other, "GET", "/v1/memory?scope=project&owner="+m.project, ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("reading another owner's project memory returned %d; it must look exactly like a project that does not exist", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "Acme") {
		t.Fatal("the refusal leaked the contents it refused")
	}
}

// The sharpest one: item ids travel in the URL, so the row has to say who it
// belongs to. Otherwise anybody with an id can delete anybody's memory.
func TestAPI_CannotForgetAnotherUsersItem(t *testing.T) {
	m := memoryHarness(t)
	item := m.write(t, memory.ScopeUser, m.owner.ID, "units", "metric")

	rec := httptest.NewRecorder()
	r := req(m.other, "DELETE", "/v1/memory/"+item.ID, "")
	r.SetPathValue("id", item.ID)
	m.h.ForgetItem(rec, r)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("one user deleted another's memory (status %d)", rec.Code)
	}
	back, err := m.svc.Repo().FindByID(context.Background(), m.pool, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if back.Forgotten() {
		t.Fatal("the item was forgotten despite the request being refused")
	}
}

func TestAPI_CannotPatchAnotherUsersItem(t *testing.T) {
	m := memoryHarness(t)
	item := m.write(t, memory.ScopeUser, m.owner.ID, "units", "metric")

	rec := httptest.NewRecorder()
	r := req(m.other, "PATCH", "/v1/memory/"+item.ID, `{"pinned":true}`)
	r.SetPathValue("id", item.ID)
	m.h.UpdateItem(rec, r)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("one user edited another's memory (status %d): %s", rec.Code, rec.Body.String())
	}
}

// The owner's own path works, and the response tells them what forgetting
// actually did — the key stays claimed, which is why FORGE will not re-learn it.
func TestAPI_ForgetSaysThatTheKeyStaysClaimed(t *testing.T) {
	m := memoryHarness(t)
	item := m.write(t, memory.ScopeProject, m.project, "supplier", "Acme")

	rec := httptest.NewRecorder()
	r := req(m.owner, "DELETE", "/v1/memory/"+item.ID+"?reason=wrong", "")
	r.SetPathValue("id", item.ID)
	m.h.ForgetItem(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("the owner could not delete their own memory: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "will not learn it again") {
		t.Fatalf("the response did not say the deletion holds: %s", rec.Body.String())
	}
}

// A corrected value must restate how it is now known. Carrying the old label
// forward would make a fresh guess look like a checked figure.
func TestAPI_CorrectionMustRestateHowItIsKnown(t *testing.T) {
	m := memoryHarness(t)
	item := m.write(t, memory.ScopeProject, m.project, "thickness", "3 mm")

	for _, body := range []string{`{"value":"4 mm"}`, `{"value":"4 mm","how":"measured-ish"}`} {
		rec := httptest.NewRecorder()
		r := req(m.owner, "PATCH", "/v1/memory/"+item.ID, body)
		r.SetPathValue("id", item.ID)
		m.h.UpdateItem(rec, r)
		if rec.Code == http.StatusOK {
			t.Fatalf("a correction was accepted without a valid epistemic label: %s", body)
		}
	}

	rec := httptest.NewRecorder()
	r := req(m.owner, "PATCH", "/v1/memory/"+item.ID, `{"value":"4 mm","how":"observed","pinned":true}`)
	r.SetPathValue("id", item.ID)
	m.h.UpdateItem(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("a properly labelled correction was refused: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "4 mm") {
		t.Fatalf("the correction did not take: %s", rec.Body.String())
	}
}

// The decision log's author is the caller, never a field in the body.
func TestAPI_DecisionAuthorIsTheCallerNotTheBody(t *testing.T) {
	m := memoryHarness(t)

	rec := httptest.NewRecorder()
	m.h.RecordDecision(rec, req(m.owner, "POST", "/v1/decisions", `{
		"project_id":"`+m.project+`","title":"Bore","decision":"5 mm bore",
		"author_id":"`+m.other.ID+`"}`))
	// author_id is not a field on the request, so a body that carries one is
	// rejected outright by the strict decoder rather than quietly ignored.
	if rec.Code == http.StatusCreated {
		t.Fatalf("a body naming its own author was accepted: %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	m.h.RecordDecision(rec, req(m.owner, "POST", "/v1/decisions",
		`{"project_id":"`+m.project+`","title":"Bore","decision":"5 mm bore"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("recording a decision failed: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Decision DecisionDTO `json:"decision"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Decision.AuthorID != m.owner.ID {
		t.Fatalf("the decision was attributed to %s; the caller was %s", body.Decision.AuthorID, m.owner.ID)
	}
	if !body.Decision.Current {
		t.Fatal("a brand-new decision does not report itself as current")
	}
}

// A client cannot talk a recalled figure into being actionable by choosing a
// stronger word for it.
func TestAPI_EvidenceLabelsAreValidatedServerSide(t *testing.T) {
	m := memoryHarness(t)

	rec := httptest.NewRecorder()
	m.h.RecordDecision(rec, req(m.owner, "POST", "/v1/decisions", `{
		"project_id":"`+m.project+`","title":"Bore","decision":"5 mm bore",
		"evidence":[{"statement":"NEMA 17 shafts are 5 mm","how":"retrieved"},
		            {"statement":"anything at all","how":"simulated"}]}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Decision DecisionDTO `json:"decision"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Decision.Evidence) != 2 {
		t.Fatalf("%d evidence items came back", len(body.Decision.Evidence))
	}
	if body.Decision.Evidence[0].Actionable {
		t.Fatal("a figure with no source came back as evidence a reader may act on")
	}
	if !strings.Contains(body.Decision.Evidence[0].Source, "FORGE's own memory") {
		t.Fatalf("the sourceless retrieval was not named as recalled: %+v", body.Decision.Evidence[0])
	}
	if body.Decision.Evidence[1].How != string(claim.Inferred) {
		t.Fatalf("a claim of simulation survived in a deployment with no solver: %q", body.Decision.Evidence[1].How)
	}
}

// Both answers readable, over the wire, from one request.
func TestAPI_DecisionChainShowsWhatWasBelievedAndWhatReplacedIt(t *testing.T) {
	m := memoryHarness(t)
	ctx := context.Background()

	first, err := m.svc.RecordDecision(ctx, &memory.Decision{
		ProjectID: m.project, AuthorID: m.owner.ID, Title: "M3", Decision: "use M3"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.svc.RecordDecision(ctx, &memory.Decision{
		ProjectID: m.project, AuthorID: m.owner.ID, Title: "M4", Decision: "use M4",
		SupersedesID: &first.ID})
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	r := req(m.owner, "GET", "/v1/decisions/"+first.ID, "")
	r.SetPathValue("id", first.ID)
	m.h.GetDecision(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Decision DecisionDTO   `json:"decision"`
		Chain    []DecisionDTO `json:"chain"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Decision.Current {
		t.Fatal("a superseded decision reported itself current on the wire")
	}
	if body.Decision.SupersededByID != second.ID {
		t.Fatalf("the superseded decision points at %q; %s replaced it", body.Decision.SupersededByID, second.ID)
	}
	if len(body.Chain) != 2 || body.Chain[0].ID != first.ID || body.Chain[1].ID != second.ID {
		t.Fatalf("the chain did not carry both answers in order: %+v", body.Chain)
	}
}

func TestAPI_CannotReadDecisionsInAnotherOwnersProject(t *testing.T) {
	m := memoryHarness(t)
	d, err := m.svc.RecordDecision(context.Background(), &memory.Decision{
		ProjectID: m.project, AuthorID: m.owner.ID, Title: "M3", Decision: "use M3"})
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	r := req(m.other, "GET", "/v1/decisions/"+d.ID, "")
	r.SetPathValue("id", d.ID)
	m.h.GetDecision(rec, r)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("a decision in another owner's project returned %d: %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	m.h.ListDecisions(rec, req(m.other, "GET", "/v1/decisions?project_id="+m.project, ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("listing another owner's decisions returned %d", rec.Code)
	}
}

// The layer table is public because it describes the build, not anybody's
// memory — and it must say plainly which visibility rules are actually
// enforced, so nobody designs against a boundary that does not exist yet.
func TestAPI_LayersSayWhichVisibilityIsEnforced(t *testing.T) {
	d := testDeps()
	h := NewMemoryHandlers(d)

	rec := httptest.NewRecorder()
	h.Layers(rec, httptest.NewRequest("GET", "/v1/memory/layers", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d", rec.Code)
	}
	var body struct {
		Layers []struct {
			Scope    string `json:"scope"`
			Enforced bool   `json:"visibility_enforced"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Layers) != 5 {
		t.Fatalf("%d layers reported", len(body.Layers))
	}
	for _, l := range body.Layers {
		if l.Scope == string(memory.ScopeOrganisation) && l.Enforced {
			t.Fatal("organisation visibility is reported as enforced; there is no membership model to enforce it with")
		}
		if l.Scope == string(memory.ScopeUser) && !l.Enforced {
			t.Fatal("personal visibility is reported as unenforced; it is the one that is")
		}
	}
}
