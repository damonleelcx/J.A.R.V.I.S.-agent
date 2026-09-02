package tools_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/claim"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/memory"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/tools"
)

// The memory tools (PRD MEM-01), against live Postgres.
//
// The property under test is the one that would be easiest to lose: FORGE does
// not get to say how it knows something. Everything else here follows from that.

type toolHarness struct {
	pool     *db.Pool
	svc      *memory.Service
	recall   *tools.MemoryRecallTool
	remember *tools.MemoryRememberTool
	goalID   string
	project  string
	userID   string
}

func toolsHarness(t *testing.T) *toolHarness {
	t.Helper()

	url := os.Getenv("FORGE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("FORGE_TEST_DATABASE_URL is unset")
	}
	ctx := context.Background()
	schema := "forge_tools_memory"

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

	clk := clock.System{}
	svc := memory.NewService(pool, clk, logx.Discard())
	h := &toolHarness{pool: pool, svc: svc,
		recall:   tools.NewMemoryRecallTool(svc, pool),
		remember: tools.NewMemoryRememberTool(svc, pool),
	}

	now := time.Now().UTC()
	h.userID = id.New(id.PrefixUser)
	if _, err := pool.Exec(ctx, `
		insert into forge_users (id, email, display_name, status, password_hash, password_algo,
			password_changed_at, created_at, updated_at)
		values ($1,'tool@example.com','T','active','x','argon2id',$2,$2,$2)`, h.userID, now); err != nil {
		t.Fatal(err)
	}
	h.project = id.New(id.PrefixProject)
	if _, err := pool.Exec(ctx, `
		insert into forge_projects (id, owner_id, name, created_at, updated_at) values ($1,$2,'P',$3,$3)`,
		h.project, h.userID, now); err != nil {
		t.Fatal(err)
	}
	h.goalID = id.New(id.PrefixGoal)
	if _, err := pool.Exec(ctx, `
		insert into forge_goals (id, project_id, created_by, title, statement, status, started_at, created_at, updated_at)
		values ($1,$2,$3,'G','S','active',$4,$4,$4)`, h.goalID, h.project, h.userID, now); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *toolHarness) call(t *testing.T, tool interface {
	Run(context.Context, tools.Invocation) (*tools.Result, error)
}, input string) (*tools.Result, error) {
	t.Helper()
	return tool.Run(context.Background(), tools.Invocation{
		GoalID: h.goalID, Input: json.RawMessage(input), IdempotencyKey: "k"})
}

// The rule from internal/domain/claim, at the place it would be easiest to
// break. There is no input for it, and what lands in the row is inferred.
func TestTool_ForgeCannotDeclareHowItKnowsSomething(t *testing.T) {
	h := toolsHarness(t)

	// The schema forbids it, so a model that tries is refused at the boundary
	// rather than believed.
	schema := string(h.remember.Contract().InputSchema)
	if strings.Contains(schema, `"how"`) {
		t.Fatalf("memory_remember accepts an epistemic label from the model:\n%s", schema)
	}
	if !strings.Contains(schema, `"additionalProperties": false`) {
		t.Fatal("memory_remember accepts arbitrary extra fields, so a 'how' would slip through the schema check")
	}

	if _, err := h.call(t, h.remember,
		`{"scope":"project","key":"wall.thickness","value":"3 mm"}`); err != nil {
		t.Fatal(err)
	}
	item, err := h.svc.Repo().FindByKey(context.Background(), h.pool, memory.ScopeProject, h.project, "wall.thickness")
	if err != nil {
		t.Fatal(err)
	}
	if item.How != claim.Inferred {
		t.Fatalf("a fact the model chose to write down was recorded as %q; it is a conclusion drawn from context", item.How)
	}

	// And a model that sends one anyway is REFUSED, not silently corrected.
	//
	// This half of the test exists because the schema half did not catch the
	// real behaviour: Contract.InputSchema is documented as validated before a
	// tool runs and in this build nothing validates it, so "how" reached Run and
	// encoding/json dropped it without a word. The label that got stored was the
	// safe one, which is exactly why nobody would have noticed — the model would
	// have gone on believing it had recorded a measurement.
	_, err = h.call(t, h.remember,
		`{"scope":"project","key":"claimed","value":"3 mm","how":"observed"}`)
	if err == nil {
		t.Fatal("a model stated how it knew something and the tool accepted the call; the label was dropped in silence")
	}
	if !errs.Is(err, errs.CodeValidationFailed) {
		t.Fatalf("got %s; the model must be told its argument was not accepted", errs.CodeOf(err))
	}
	if _, findErr := h.svc.Repo().FindByKey(context.Background(), h.pool, memory.ScopeProject, h.project, "claimed"); findErr == nil {
		t.Fatal("the refused call still wrote a row")
	}
	if (claim.Claim{How: item.How, Source: item.Source}).Actionableish() {
		t.Fatal("model-written memory came back as something a later cycle may act on without checking")
	}
}

// Personal preference is the user's to state; org knowledge must not be set by
// one goal's conclusion.
func TestTool_ForgeCannotWritePersonalOrOrganisationMemory(t *testing.T) {
	h := toolsHarness(t)

	for _, scope := range []string{"user", "organisation"} {
		_, err := h.call(t, h.remember, `{"scope":"`+scope+`","key":"k","value":1}`)
		if err == nil {
			t.Fatalf("FORGE wrote %s memory on its own initiative", scope)
		}
		if !errs.Is(err, errs.CodeForbidden) {
			t.Fatalf("writing %s memory failed with %s; it must be a refusal the model can read", scope, errs.CodeOf(err))
		}
	}
}

// Recall hands the model the reason and the "may I act on this" verdict, rather
// than a label it has to interpret for itself.
func TestTool_RecallTellsTheModelWhatItMayActOn(t *testing.T) {
	h := toolsHarness(t)
	ctx := context.Background()

	if _, err := h.svc.Remember(ctx, memory.Write{Scope: memory.ScopeProject, Owner: h.project,
		Key: "plate.thickness", Value: "3 mm", How: claim.Observed, Source: "the print"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.call(t, h.remember, `{"scope":"project","key":"guessed.finish","value":"anodised"}`); err != nil {
		t.Fatal(err)
	}

	res, err := h.call(t, h.recall, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Recalled []struct {
			Key        string `json:"key"`
			How        string `json:"how"`
			HowMeans   string `json:"how_means"`
			Actionable bool   `json:"may_be_acted_on"`
			Why        string `json:"why_returned"`
		} `json:"recalled"`
	}
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Recalled) != 2 {
		t.Fatalf("%d items recalled; two were written", len(out.Recalled))
	}
	seen := map[string]bool{}
	for _, r := range out.Recalled {
		seen[r.Key] = true
		if r.Why == "" || r.HowMeans == "" {
			t.Fatalf("%q reached the model with no reason or no gloss", r.Key)
		}
		switch r.Key {
		case "plate.thickness":
			if !r.Actionable {
				t.Fatal("an observed measurement was marked as something the model may not act on")
			}
		case "guessed.finish":
			if r.Actionable {
				t.Fatal("a conclusion FORGE drew for itself was handed back as an established fact")
			}
		}
	}
	if !seen["plate.thickness"] || !seen["guessed.finish"] {
		t.Fatalf("recall did not return both items: %v", seen)
	}
	if res.Evidence == "" {
		t.Fatal("the recall result claims no evidence for what it returned")
	}
}

// A key the user deleted must stay deleted even when the agent is the one
// writing. This is the same guarantee as the API's, through the tool path.
func TestTool_ForgeCannotRelearnWhatAUserForgot(t *testing.T) {
	h := toolsHarness(t)
	ctx := context.Background()

	if _, err := h.call(t, h.remember, `{"scope":"project","key":"supplier","value":"Acme"}`); err != nil {
		t.Fatal(err)
	}
	item, err := h.svc.Repo().FindByKey(ctx, h.pool, memory.ScopeProject, h.project, "supplier")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Forget(ctx, item.ID, h.userID, "wrong"); err != nil {
		t.Fatal(err)
	}

	_, err = h.call(t, h.remember, `{"scope":"project","key":"supplier","value":"Acme"}`)
	if err == nil {
		t.Fatal("the agent re-learned a key the user had deleted")
	}
	if !errs.Is(err, errs.CodeMemoryForgotten) {
		t.Fatalf("got %s; the model must be told the key was forgotten", errs.CodeOf(err))
	}
}

// Writing the same key twice is a correction, not a duplicate call — so the
// ledger must not short-circuit the second one.
func TestTool_RememberIsNotIdempotentBecauseARewriteIsACorrection(t *testing.T) {
	h := toolsHarness(t)

	if h.remember.Contract().Idempotent {
		t.Fatal("memory_remember declares itself idempotent; the ledger would drop a correction as a repeat")
	}
	if !h.recall.Contract().Idempotent {
		t.Fatal("memory_recall declares itself non-idempotent; reading changes nothing")
	}
}

// The tool cannot be pointed at a project the goal does not belong to: the
// project is resolved from the row, and there is no input for it.
func TestTool_ProjectIsResolvedFromTheGoalNotTheInput(t *testing.T) {
	h := toolsHarness(t)

	for _, schema := range []string{
		string(h.remember.Contract().InputSchema),
		string(h.recall.Contract().InputSchema),
	} {
		if strings.Contains(schema, "project_id") || strings.Contains(schema, "goal_id") {
			t.Fatalf("a memory tool accepts an owner id from the model:\n%s", schema)
		}
	}
}
