package workspace_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/auth"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/claim"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/identity"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/memory"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// The workspace model against a real database and the real migration chain.
//
// The unit tests hold the vocabularies. These hold what only exists once there
// is somewhere to write: that a node's kind cannot be changed, that anchors are
// real foreign keys rather than hopeful pointers, that an artifact version lands
// inside the audit chain, and that a machine's verdict never becomes a person's.

type harness struct {
	pool    *db.Pool
	svc     *workspace.Service
	clk     *clock.Fake
	userID  string
	project string
	goalID  string
	taskID  string
	toolID  string
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	url := os.Getenv("FORGE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("FORGE_TEST_DATABASE_URL is unset; skipping live-database tests. Run `make db-up` then `make test-integration`.")
	}
	ctx := context.Background()
	schema := "forge_ws_" + strings.ToLower(strings.NewReplacer("/", "_", "-", "_").Replace(t.Name()))
	if len(schema) > 60 {
		schema = schema[:60]
	}
	cfg := func(u string) config.DBConfig {
		return config.DBConfig{URL: u, MaxConns: 8, MinConns: 1,
			MaxConnLifetime: time.Hour, MaxConnIdleTime: time.Minute, ConnectTimeout: 10 * time.Second}
	}
	admin, err := db.Connect(ctx, cfg(url), logx.Discard())
	if err != nil {
		t.Fatalf("cannot reach the test database: %v", err)
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
		t.Fatalf("migrating the test schema: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		if c, err := db.Connect(context.Background(), cfg(url), logx.Discard()); err == nil {
			_, _ = c.Exec(context.Background(), "drop schema if exists "+schema+" cascade")
			c.Close()
		}
	})

	clk := clock.NewFake(time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC))
	h := &harness{pool: pool, clk: clk, svc: workspace.NewService(pool, clk, logx.Discard())}
	h.seed(t)
	return h
}

func (h *harness) seed(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	now := h.clk.Now()

	hash, err := auth.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	u := &identity.User{ID: id.New(id.PrefixUser), Email: "owner@example.com",
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
	planID := id.New(id.PrefixPlan)
	if _, err := h.pool.Exec(ctx,
		`insert into forge_plans (id, goal_id, version, created_at) values ($1,$2,1,$3)`,
		planID, h.goalID, now); err != nil {
		t.Fatal(err)
	}
	h.taskID = id.New(id.PrefixTask)
	task := &engine.Task{ID: h.taskID, GoalID: h.goalID, PlanID: planID,
		Title: "t", Instruction: "do it", Status: engine.StatusPending,
		IdempotencyKey: "seed-key", MaxAttempts: 3, NotBefore: now, Priority: 100,
		RiskTier: engine.RiskR1, CreatedAt: now, UpdatedAt: now}
	if err := engine.NewRepository().CreateTask(ctx, h.pool, task, nil); err != nil {
		t.Fatal(err)
	}
	h.toolID = id.New(id.PrefixToolCall)
	// A terminal tool call records when it ended — forge_tool_calls_terminal_has_end.
	// The fixture goes through the same constraints production does, so a change
	// to them shows up here rather than in a deployment.
	if _, err := h.pool.Exec(ctx,
		`insert into forge_tool_calls (id, task_id, idempotency_key, tool_name, status,
			started_at, ended_at, duration_ms, created_at)
		 values ($1,$2,'tool-key','workspace_write','succeeded',$3,$3,0,$3)`,
		h.toolID, h.taskID, now); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) add(t *testing.T, kind workspace.Kind, title string, how claim.Epistemic) *workspace.Node {
	t.Helper()
	n, err := h.svc.Add(context.Background(), workspace.NewNode{
		ProjectID: h.project, Kind: kind, Title: title, How: how, CreatedBy: h.userID})
	if err != nil {
		t.Fatalf("adding %s %q: %v", kind, title, err)
	}
	return n
}

func (h *harness) relate(t *testing.T, k workspace.EdgeKind, from, to *workspace.Node) *workspace.Edge {
	t.Helper()
	e, err := h.svc.Relate(context.Background(), k, from.ID, to.ID, "", h.userID)
	if err != nil {
		t.Fatalf("%s %s -> %s: %v", k, from.Kind, to.Kind, err)
	}
	return e
}

// ---------------------------------------------------------------------------
// RSN-01 — a structure separate from the transcript
// ---------------------------------------------------------------------------

// Every owned kind must actually be writable. A kind the schema rejects exists
// only in the Go table, and the two would have drifted in silence.
func TestGraph_EveryOwnedKindRoundTrips(t *testing.T) {
	h := newHarness(t)
	for _, d := range workspace.Kinds() {
		if d.Anchor != workspace.AnchorNone {
			continue
		}
		n := h.add(t, d.Kind, "probe "+string(d.Kind), "")
		if n.How == "" {
			t.Fatalf("%s was written with no epistemic label", d.Kind)
		}
		if n.How != d.Default {
			t.Fatalf("%s defaulted to %q; its table says %q", d.Kind, n.How, d.Default)
		}
	}
}

// The rule promotion exists to protect. An assumption that turns out to be true
// does not become a requirement — because then nobody could ever ask what was
// built on top of a guess.
func TestGraph_ANodesKindCannotBeChanged(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// Two shapes, because a live run found they took different paths. Changing
	// only the kind hit the guard; changing the kind AND the label — which is
	// what a caller actually does — hit the epistemic check first and was told
	// "a requirement cannot be assumed", a true statement about the wrong
	// problem. The first thing wrong with the call is that it changes a kind.
	for _, tc := range []struct {
		name string
		how  claim.Epistemic
	}{
		{"kind alone", claim.Assumed},
		{"kind and label together", claim.Retrieved},
	} {
		a := h.add(t, workspace.KindAssumption, "the plate is 3 mm ("+tc.name+")", claim.Assumed)
		a.Kind = workspace.KindRequirement
		a.How = tc.how
		a.UpdatedAt = h.clk.Now()

		err := h.svc.Repo().UpdateNode(ctx, h.pool, a)
		if err == nil {
			t.Fatalf("%s: an assumption was edited into a requirement; its history as a guess is gone", tc.name)
		}
		if !errs.Is(err, errs.CodeInvariantViolated) {
			t.Fatalf("%s: got %s (%v); changing a kind must be reported as changing a kind, "+
				"whatever else the call got wrong", tc.name, errs.CodeOf(err), err)
		}
		if !strings.Contains(err.Error(), "derives_from") {
			t.Fatalf("%s: the refusal did not say what to do instead: %v", tc.name, err)
		}
		back, err := h.svc.Repo().FindNode(ctx, h.pool, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		if back.Kind != workspace.KindAssumption {
			t.Fatalf("%s: the node is now a %s", tc.name, back.Kind)
		}
	}

	a := h.add(t, workspace.KindAssumption, "the plate is 3 mm", claim.Assumed)
	a.Kind = workspace.KindRequirement
	a.How = claim.Retrieved
	a.UpdatedAt = h.clk.Now()
	if err := h.svc.Repo().UpdateNode(ctx, h.pool, a); err == nil {
		t.Fatal("an assumption was edited into a requirement")
	}

	back, err := h.svc.Repo().FindNode(ctx, h.pool, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if back.Kind != workspace.KindAssumption {
		t.Fatalf("the node is now a %s", back.Kind)
	}
}

// Promotion keeps both, and the link between them.
func TestGraph_PromotionKeepsTheGuessItCameFrom(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	guess := h.add(t, workspace.KindAssumption, "the plate is 3 mm", claim.Assumed)
	req, edge, err := h.svc.Promote(ctx, guess.ID, workspace.NewNode{
		Kind: workspace.KindRequirement, Title: "the plate is 3 mm",
		How: claim.Observed, Source: "measured on the print", CreatedBy: h.userID,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if req.Kind != workspace.KindRequirement || req.How != claim.Observed {
		t.Fatalf("the promoted node is a %s known by %s", req.Kind, req.How)
	}
	if edge.Kind != workspace.EdgeDerivesFrom || edge.FromID != req.ID || edge.ToID != guess.ID {
		t.Fatalf("the provenance edge does not run from the requirement to the assumption: %+v", edge)
	}

	old, err := h.svc.Repo().FindNode(ctx, h.pool, guess.ID)
	if err != nil {
		t.Fatal("the assumption disappeared when it was promoted")
	}
	if old.Kind != workspace.KindAssumption {
		t.Fatalf("the assumption became a %s", old.Kind)
	}
	if old.Status != workspace.StatusRetired {
		t.Fatalf("the superseded assumption is %s; it should be retired — no longer in force, still readable", old.Status)
	}
}

// Promotion is one act. A half-done promotion would leave a requirement with no
// record of where it came from, which is indistinguishable from one that was
// always a requirement.
func TestGraph_PromotionIsAllOrNothing(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	guess := h.add(t, workspace.KindAssumption, "the plate is 3 mm", claim.Assumed)
	// An evidence node cannot be `assumed`, so this promotion fails at the new
	// node. The retirement of the source must not have happened either.
	_, _, err := h.svc.Promote(ctx, guess.ID, workspace.NewNode{
		Kind: workspace.KindEvidence, Title: "measured", How: claim.Assumed, CreatedBy: h.userID,
	}, true)
	if err == nil {
		t.Fatal("evidence was created as assumed")
	}
	back, err := h.svc.Repo().FindNode(ctx, h.pool, guess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if back.Status == workspace.StatusRetired {
		t.Fatal("a failed promotion retired the node it was promoting from")
	}
}

// ---------------------------------------------------------------------------
// WRK-03 — the graph
// ---------------------------------------------------------------------------

// The pairing rule, enforced against what the nodes actually are rather than
// what the caller says they are.
func TestGraph_EdgePairingIsCheckedAgainstTheRealNodes(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	req := h.add(t, workspace.KindRequirement, "mount the motor", claim.Retrieved)
	test := h.add(t, workspace.KindTest, "bracket load test", "")

	h.relate(t, workspace.EdgeVerifies, test, req)

	if _, err := h.svc.Relate(ctx, workspace.EdgeVerifies, req.ID, test.ID, "", h.userID); err == nil {
		t.Fatal("a requirement was allowed to verify a test")
	}
	if _, err := h.svc.Relate(ctx, workspace.EdgeVerifies, test.ID, req.ID, "", h.userID); err == nil {
		t.Fatal("the same edge was drawn twice; every count over the graph is now wrong")
	} else if !errs.Is(err, errs.CodeConflict) {
		t.Fatalf("a duplicate edge reported %s", errs.CodeOf(err))
	}
	if _, err := h.svc.Relate(ctx, workspace.EdgeVerifies, test.ID, test.ID, "", h.userID); err == nil {
		t.Fatal("a node was allowed to verify itself")
	}
}

// Anchors are real foreign keys. This is the whole reason the graph anchors
// external rows instead of holding polymorphic (kind, id) endpoints: a deleted
// goal must not leave the graph full of pointers to nothing.
func TestGraph_DeletingAGoalTakesItsAnchorAndEdges(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	anchor, err := h.svc.Anchor(ctx, h.project, workspace.KindGoal, h.goalID, h.userID)
	if err != nil {
		t.Fatal(err)
	}
	component := h.add(t, workspace.KindComponent, "bracket", "")
	h.relate(t, workspace.EdgeDependsOn, component, anchor)

	if _, err := h.pool.Exec(ctx, `delete from forge_goals where id = $1`, h.goalID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Repo().FindNode(ctx, h.pool, anchor.ID); err == nil {
		t.Fatal("the goal was deleted and its anchor survived, pointing at nothing")
	}
	edges, err := h.svc.Repo().ListEdges(ctx, h.pool, h.project)
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 0 {
		t.Fatalf("%d edge(s) survived a deleted endpoint: %+v", len(edges), edges)
	}
}

// Two anchors for one goal would make every traversal return its edges twice,
// and neither anchor would be the wrong one to delete.
func TestGraph_AnchoringIsIdempotent(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	first, err := h.svc.Anchor(ctx, h.project, workspace.KindGoal, h.goalID, h.userID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.svc.Anchor(ctx, h.project, workspace.KindGoal, h.goalID, h.userID)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("anchoring the same goal twice made two nodes: %s and %s", first.ID, second.ID)
	}
	// And the database refuses a second one written straight past the service.
	_, err = h.pool.Exec(ctx, `
		insert into forge_nodes (id, project_id, kind, how, status, goal_id, created_by, created_at, updated_at)
		values ($1,$2,'goal','proposed','accepted',$3,$4,$5,$5)`,
		id.New(id.PrefixNode), h.project, h.goalID, h.userID, h.clk.Now())
	if err == nil {
		t.Fatal("the database accepted a second goal anchor; the service check is the only thing holding it")
	}
}

// A decision (PRD MEM-03) reaches the graph through an anchor rather than a
// copy, which is the reason wave 3's table and this one do not disagree.
func TestGraph_DecisionsJoinTheGraphWithoutBeingCopied(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	mem := memory.NewService(h.pool, h.clk, logx.Discard())
	d, err := mem.RecordDecision(ctx, &memory.Decision{
		ProjectID: h.project, AuthorID: h.userID, Title: "M4 bolts", Decision: "use M4"})
	if err != nil {
		t.Fatal(err)
	}
	anchor, err := h.svc.Anchor(ctx, h.project, workspace.KindDecision, d.ID, h.userID)
	if err != nil {
		t.Fatal(err)
	}
	if anchor.Title != "" || anchor.Body != "" {
		t.Fatalf("the decision anchor holds its own copy of the content: %q / %q", anchor.Title, anchor.Body)
	}
	ref, ok := anchor.AnchorRef()
	if !ok || ref != d.ID {
		t.Fatalf("the anchor points at %q, not at decision %s", ref, d.ID)
	}

	risk := h.add(t, workspace.KindRisk, "the plate may deform", "")
	h.relate(t, workspace.EdgeMitigates, anchor, risk)
}

// ---------------------------------------------------------------------------
// The review
// ---------------------------------------------------------------------------

// A defect is a contradiction; a gap is an absence. Mixing them makes a report
// that is always red, and a check that is always red is a check nobody reads.
func TestReview_SeparatesContradictionsFromIncompleteness(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	req := h.add(t, workspace.KindRequirement, "mount the motor", claim.Retrieved)

	rev, err := h.svc.Review(ctx, h.project)
	if err != nil {
		t.Fatal(err)
	}
	if !rev.Sound() {
		t.Fatalf("a graph with one unverified requirement reported a defect: %+v", rev.Defects)
	}
	if len(rev.Gaps) == 0 {
		t.Fatal("nothing verifies or owns the requirement and no gap was reported")
	}
	var sawUnverified bool
	for _, g := range rev.Gaps {
		if g.Problem == "unverified" && len(g.NodeIDs) == 1 && g.NodeIDs[0] == req.ID {
			sawUnverified = true
		}
	}
	if !sawUnverified {
		t.Fatalf("the unverified requirement was not named: %+v", rev.Gaps)
	}
	if !strings.Contains(rev.Summary(), "gap") {
		t.Fatalf("the summary hides the gaps: %s", rev.Summary())
	}
}

func TestReview_ADependencyCycleIsADefect(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	a := h.add(t, workspace.KindComponent, "bracket", "")
	b := h.add(t, workspace.KindComponent, "mount", "")
	c := h.add(t, workspace.KindComponent, "plate", "")
	h.relate(t, workspace.EdgeDependsOn, a, b)
	h.relate(t, workspace.EdgeDependsOn, b, c)
	h.relate(t, workspace.EdgeDependsOn, c, a)

	rev, err := h.svc.Review(ctx, h.project)
	if err != nil {
		t.Fatal(err)
	}
	if rev.Sound() {
		t.Fatal("three components depending on each other in a loop reported no defect")
	}
	var found *workspace.Finding
	for i := range rev.Defects {
		if rev.Defects[i].Problem == "dependency-cycle" {
			found = &rev.Defects[i]
		}
	}
	if found == nil {
		t.Fatalf("the cycle was not named as one: %+v", rev.Defects)
	}
	if !strings.Contains(found.Detail, "bracket") {
		t.Fatalf("the defect does not name the nodes in the loop: %s", found.Detail)
	}
}

// The epistemic vocabulary earning its keep on the graph: something AGREED TO
// whose every input is a guess reads as settled and is not.
func TestReview_AcceptedOnNothingButGuessesIsADefect(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	guess := h.add(t, workspace.KindAssumption, "the motor is NEMA 17", claim.Assumed)
	req, err := h.svc.Add(ctx, workspace.NewNode{
		ProjectID: h.project, Kind: workspace.KindRequirement, Title: "42.3 mm bolt circle",
		How: claim.Retrieved, Status: workspace.StatusAccepted, CreatedBy: h.userID})
	if err != nil {
		t.Fatal(err)
	}
	h.relate(t, workspace.EdgeDerivesFrom, req, guess)

	rev, err := h.svc.Review(ctx, h.project)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, d := range rev.Defects {
		if d.Problem == "accepted-on-assumption" {
			found = true
		}
	}
	if !found {
		t.Fatalf("a requirement accepted on nothing but an assumption was not reported: %+v", rev.Defects)
	}

	// Give it a measurement to stand on and the defect goes away — otherwise the
	// check would fire on every real project and be turned off.
	measured := h.add(t, workspace.KindEvidence, "measured 42.3 mm on the part", claim.Observed)
	h.relate(t, workspace.EdgeDerivesFrom, req, measured)

	rev, err = h.svc.Review(ctx, h.project)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range rev.Defects {
		if d.Problem == "accepted-on-assumption" {
			t.Fatalf("still reported after a measurement was recorded behind it: %s", d.Detail)
		}
	}
}

// ---------------------------------------------------------------------------
// WRK-04 — the artifact lifecycle
// ---------------------------------------------------------------------------

func (h *harness) change(t *testing.T, path, diff string) (*workspace.Artifact, *workspace.Version) {
	t.Helper()
	a, v, err := h.svc.RecordChange(context.Background(), workspace.Change{
		ProjectID: h.project, Path: path, InitiatorID: h.userID,
		Agent: workspace.AgentExecutor, ToolCallID: &h.toolID,
		Inputs: map[string]any{"prompt": "make the bracket"}, Diff: diff,
		GoalID: h.goalID, TaskID: &h.taskID, Summary: "wrote " + path,
	})
	if err != nil {
		t.Fatalf("recording a change to %q: %v", path, err)
	}
	return a, v
}

// The plan said WRK-04 is where the audit chain pays off. This is that: a change
// lands in the timeline, the version points at the event, and the chain still
// verifies over it.
func TestArtifacts_AChangeLandsInsideTheAuditChain(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	_, v := h.change(t, "src/bracket.scad", "+ module bracket() {}")
	if v.EventID == nil {
		t.Fatal("a change made inside a goal wrote no timeline event, so nothing traces it")
	}

	var kind, summary string
	if err := h.pool.QueryRow(ctx,
		`select kind, summary from forge_events where id = $1`, *v.EventID).Scan(&kind, &summary); err != nil {
		t.Fatalf("the event the version points at does not exist: %v", err)
	}
	if kind != engine.EventArtifactChanged {
		t.Fatalf("the event is a %q", kind)
	}

	report, err := engine.NewRepository().VerifyChain(ctx, h.pool, h.goalID)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Intact() {
		t.Fatalf("recording an artifact change broke the goal's hash chain: %s", report.Summary())
	}
	if report.Chained == 0 {
		t.Fatal("the event was written outside the chain")
	}
}

// The event and the version are one fact. A crash between them would leave
// either a version pointing at nothing or an event describing a change with no
// record, and both look like ordinary rows afterwards.
func TestArtifacts_AFailedChangeWritesNeitherEventNorVersion(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	before := h.eventCount(t)
	// No tool call and a non-human agent: refused by Version.Validate, AFTER the
	// event has been appended inside the same transaction.
	_, _, err := h.svc.RecordChange(ctx, workspace.Change{
		ProjectID: h.project, Path: "src/x.scad", InitiatorID: h.userID,
		Agent: workspace.AgentExecutor, ToolCallID: nil,
		Inputs: map[string]any{}, Diff: "", GoalID: h.goalID,
	})
	if err == nil {
		t.Fatal("a change with no tool call was recorded")
	}
	if after := h.eventCount(t); after != before {
		t.Fatalf("the event survived a refused change: %d events before, %d after", before, after)
	}
	if _, err := h.svc.Repo().FindArtifactByPath(ctx, h.pool, h.project, "src/x.scad"); err == nil {
		t.Fatal("the artifact was created by a change that was refused")
	}
}

func (h *harness) eventCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := h.pool.QueryRow(context.Background(),
		`select count(*) from forge_events where goal_id = $1`, h.goalID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Versions append and the current one is derived, so there is nothing to
// disagree with the version numbers.
func TestArtifacts_VersionsAppendAndCurrentIsDerived(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	a, v1 := h.change(t, "src/bracket.scad", "+ first")
	_, v2 := h.change(t, "src/bracket.scad", "+ second")
	if v1.Version != 1 || v2.Version != 2 {
		t.Fatalf("versions came out as %d and %d", v1.Version, v2.Version)
	}
	cur, err := h.svc.Repo().CurrentVersion(ctx, h.pool, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cur.ID != v2.ID {
		t.Fatalf("the current version is %d; the latest is %d", cur.Version, v2.Version)
	}
	hist, err := h.svc.History(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist.Versions) != 2 {
		t.Fatalf("%d versions in the history", len(hist.Versions))
	}
	if hist.Versions[0].Diff != "+ second" {
		t.Fatal("the history is not newest first")
	}
}

// A version nobody ruled on becomes superseded when a newer one lands. A
// version somebody DID rule on is left alone: overwriting that would erase a
// human decision to tidy a queue.
func TestArtifacts_SupersedingSpareTheDecisionsPeopleMade(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	a, v1 := h.change(t, "src/bracket.scad", "+ first")
	if err := h.svc.Dispose(ctx, v1.ID, workspace.Rejected, h.userID, "wrong hole spacing"); err != nil {
		t.Fatal(err)
	}
	_, v2 := h.change(t, "src/bracket.scad", "+ second")
	h.change(t, "src/bracket.scad", "+ third")

	hist, err := h.svc.History(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	byVersion := map[int]workspace.Version{}
	for _, v := range hist.Versions {
		byVersion[v.Version] = v
	}
	if got := byVersion[1].Disposition; got != workspace.Rejected {
		t.Fatalf("the version a person rejected is now %q", got)
	}
	if got := byVersion[v2.Version].Disposition; got != workspace.Superseded {
		t.Fatalf("a pending version that was replaced is %q, not superseded", got)
	}
	if got := byVersion[3].Disposition; got != workspace.Pending {
		t.Fatalf("the newest version is %q rather than awaiting a person", got)
	}
}

// The distinction the whole design turns on, through the real write path.
func TestArtifacts_VerifyingDoesNotAcceptAndAcceptingDoesNotVerify(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	_, v := h.change(t, "src/bracket.scad", "+ first")
	if err := h.svc.Verify(ctx, v.ID, workspace.Passed, "12 checks passed"); err != nil {
		t.Fatal(err)
	}
	back, err := h.svc.Repo().FindVersion(ctx, h.pool, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if back.Verification != workspace.Passed {
		t.Fatalf("verification did not record: %q", back.Verification)
	}
	if back.Disposition != workspace.Pending {
		t.Fatalf("a passing test set the human disposition to %q", back.Disposition)
	}
	if err := back.Usable(); err == nil {
		t.Fatal("a version nobody had looked at was usable because the tests passed")
	}

	if err := h.svc.Dispose(ctx, v.ID, workspace.Accepted, h.userID, "looks right"); err != nil {
		t.Fatal(err)
	}
	back, err = h.svc.Repo().FindVersion(ctx, h.pool, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if back.DispositionedBy == nil || *back.DispositionedBy != h.userID {
		t.Fatal("the acceptance was not attributed to the person who made it")
	}
	if err := back.Usable(); err != nil {
		t.Fatalf("a verified and accepted version was not usable: %v", err)
	}
}

// A decision is not revised in place: "we rejected that" must not quietly
// become "we accepted it" with nothing recording the change of mind.
func TestArtifacts_ADispositionIsNotRevisedInPlace(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	_, v := h.change(t, "src/bracket.scad", "+ first")
	if err := h.svc.Dispose(ctx, v.ID, workspace.Rejected, h.userID, "no"); err != nil {
		t.Fatal(err)
	}
	err := h.svc.Dispose(ctx, v.ID, workspace.Accepted, h.userID, "actually yes")
	if err == nil {
		t.Fatal("a rejection was turned into an acceptance in place")
	}
	if !errs.Is(err, errs.CodeConflict) {
		t.Fatalf("got %s", errs.CodeOf(err))
	}
	if !strings.Contains(err.Error(), "new version") {
		t.Fatalf("the refusal did not say what to do instead: %v", err)
	}
}

// The database enforces attribution too, so a write that bypasses the service
// cannot record an acceptance by nobody.
func TestArtifacts_TheDatabaseAlsoRefusesAnUnattributedAcceptance(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	_, v := h.change(t, "src/bracket.scad", "+ first")
	_, err := h.pool.Exec(ctx,
		`update forge_artifact_versions set human_disposition = 'accepted' where id = $1`, v.ID)
	if err == nil {
		t.Fatal("the database accepted a human disposition with no human named")
	}
	if !strings.Contains(err.Error(), "disposition_attributed") {
		t.Fatalf("the write failed for a different reason: %v", err)
	}
}

// An artifact joins the graph through an anchor, which is what makes WRK-03's
// "files" and WRK-04's artifacts one thing rather than two.
func TestArtifacts_JoinTheGraphAsAnchors(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	a, _ := h.change(t, "src/bracket.scad", "+ first")
	anchor, err := h.svc.Anchor(ctx, h.project, workspace.KindArtifact, a.ID, h.userID)
	if err != nil {
		t.Fatal(err)
	}
	req := h.add(t, workspace.KindRequirement, "mount the motor", claim.Retrieved)
	h.relate(t, workspace.EdgeSatisfies, anchor, req)

	if _, err := h.pool.Exec(ctx, `delete from forge_artifacts where id = $1`, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Repo().FindNode(ctx, h.pool, anchor.ID); err == nil {
		t.Fatal("deleting the artifact left its graph anchor pointing at nothing")
	}
}

// ---------------------------------------------------------------------------
// The actor vocabulary, against the schema that has to accept it
// ---------------------------------------------------------------------------

// Every agent Go recognises must be one the database will store, and every actor
// too.
//
// # Why this did not exist until now
//
// engine.AllActors carried the comment "for the schema-coherence fence" and had
// no caller — a fence that was declared and never built. It went unnoticed
// because the vocabulary had not changed since it was written. Migration 0011
// changed it (adding 'converse' for the workbench conversation), which is
// exactly the moment a drift between the Go list and the two check constraints
// would have shipped silently: the Go side would accept the value and the INSERT
// would fail in production on a path no test happened to take.
//
// Both halves are checked here because forge_artifact_versions.agent and
// forge_events.actor are documented as agreeing about who acted, and a migration
// that widened one and not the other would make that comment false.
func TestVocabulary_EveryAgentAndActorIsAcceptedByTheSchema(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	for _, a := range workspace.Agents() {
		toolCall := &h.toolID
		if a == workspace.AgentHuman || a == workspace.AgentConverse {
			// The two that legitimately work without one; naming a tool call
			// here would be refused by Validate for misattributing the change.
			toolCall = nil
		}
		_, _, err := h.svc.RecordChange(ctx, workspace.Change{
			ProjectID: h.project, Path: "agents/" + string(a) + ".txt",
			InitiatorID: h.userID, Agent: a, ToolCallID: toolCall,
			Inputs: map[string]any{}, Diff: "",
		})
		if err != nil {
			t.Errorf("agent %q is recognised by Go and refused by the database: %v", a, err)
		}
	}

	for _, actor := range engine.AllActors() {
		ev := &engine.Event{
			GoalID: h.goalID, Kind: engine.EventArtifactChanged, Actor: actor,
			ActorID: &h.userID, Summary: "vocabulary check", Payload: []byte(`{}`),
		}
		if err := engine.NewRepository().AppendEvent(ctx, h.pool, ev, h.clk.Now()); err != nil {
			t.Errorf("actor %q is recognised by Go and refused by the database: %v", actor, err)
		}
	}

	// And the two lists must be the same set, since one is cast to the other in
	// RecordChange. A value in only one of them is a value that becomes invalid
	// the moment a geometry variant is saved inside a goal.
	agents := map[string]bool{}
	for _, a := range workspace.Agents() {
		agents[string(a)] = true
	}
	for _, actor := range engine.AllActors() {
		if !agents[string(actor)] {
			t.Errorf("engine.Actor %q has no matching workspace.Agent; RecordChange casts one to the other", actor)
		}
		delete(agents, string(actor))
	}
	for a := range agents {
		t.Errorf("workspace.Agent %q has no matching engine.Actor; a version made by it could not "+
			"write the timeline event it points at", a)
	}
}
