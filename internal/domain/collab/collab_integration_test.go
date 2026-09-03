package collab_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/auth"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/access"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/claim"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/collab"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/identity"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// The room and the handoff, against a real database.

type harness struct {
	pool    *db.Pool
	svc     *collab.Service
	clk     *clock.Fake
	alice   string
	bob     string
	project string
	goalID  string
	planID  string
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	url := os.Getenv("FORGE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("FORGE_TEST_DATABASE_URL is unset; skipping live-database tests.")
	}
	ctx := context.Background()
	schema := "forge_col_" + strings.ToLower(strings.NewReplacer("/", "_", "-", "_").Replace(t.Name()))
	if len(schema) > 60 {
		schema = schema[:60]
	}
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
	t.Cleanup(func() {
		pool.Close()
		if c, err := db.Connect(context.Background(), cfg(url), logx.Discard()); err == nil {
			_, _ = c.Exec(context.Background(), "drop schema if exists "+schema+" cascade")
			c.Close()
		}
	})

	clk := clock.NewFake(time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC))
	h := &harness{pool: pool, clk: clk, svc: collab.NewService(pool, clk, logx.Discard())}
	h.seed(t)
	return h
}

func (h *harness) seed(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	now := h.clk.Now()

	mk := func(email string) string {
		hash, _ := auth.HashPassword("correct horse battery staple")
		u := &identity.User{ID: id.New(id.PrefixUser), Email: email, Status: identity.StatusActive,
			PasswordHash: hash, PasswordAlgo: auth.AlgoArgon2id,
			PasswordChangedAt: now, CreatedAt: now, UpdatedAt: now}
		if err := identity.NewRepository().CreateUser(ctx, h.pool, u); err != nil {
			t.Fatal(err)
		}
		return u.ID
	}
	h.alice, h.bob = mk("alice@example.com"), mk("bob@example.com")

	h.project = id.New(id.PrefixProject)
	if _, err := h.pool.Exec(ctx,
		`insert into forge_projects (id, owner_id, name, created_at, updated_at) values ($1,$2,'P',$3,$3)`,
		h.project, h.alice, now); err != nil {
		t.Fatal(err)
	}
	acc := access.NewService(h.pool, h.clk, logx.Discard())
	if err := acc.EnsureOwner(ctx, h.pool, h.project, h.alice); err != nil {
		t.Fatal(err)
	}
	h.goalID = id.New(id.PrefixGoal)
	if _, err := h.pool.Exec(ctx, `
		insert into forge_goals (id, project_id, created_by, title, statement, status, started_at, created_at, updated_at)
		values ($1,$2,$3,'Mount the motor','Build and fit the bracket','active',$4,$4,$4)`,
		h.goalID, h.project, h.alice, now); err != nil {
		t.Fatal(err)
	}
	h.planID = id.New(id.PrefixPlan)
	if _, err := h.pool.Exec(ctx,
		`insert into forge_plans (id, goal_id, version, created_at) values ($1,$2,1,$3)`,
		h.planID, h.goalID, now); err != nil {
		t.Fatal(err)
	}
}

// COL-01's whole point. There is no anonymous speaker and no default.
func TestRoom_EveryTurnNamesItsSpeaker(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	room, err := h.svc.OpenRoom(ctx, h.project, h.goalID, "design review", h.alice)
	if err != nil {
		t.Fatal(err)
	}

	// A human turn with no speaker is refused.
	if _, err := h.svc.Say(ctx, room.ID, &collab.Turn{
		Speaker: collab.SpeakerHuman, Text: "the tolerance is fine"}); err == nil {
		t.Fatal("an unattributed human turn was recorded; six months later nobody knows who said it")
	}
	// A human turn with no label is refused: rendering the name later from the
	// account shows who they are now, not who said this.
	if _, err := h.svc.Say(ctx, room.ID, &collab.Turn{
		Speaker: collab.SpeakerHuman, SpeakerID: &h.alice, Text: "hello"}); err == nil {
		t.Fatal("a human turn with no recorded name was accepted")
	}
	// A label but no id. This case exists because a drill showed the two checks
	// were being held by one assertion: the "neither" case above is caught by
	// the label check alone, so disabling the id check left the fence green.
	// Each half of the rule now has a case that only it can refuse.
	if _, err := h.svc.Say(ctx, room.ID, &collab.Turn{
		Speaker: collab.SpeakerHuman, SpeakerLabel: "Alice", Text: "hello"}); err == nil {
		t.Fatal("a human turn with a name and no account was accepted; the id half of the rule is not enforced")
	}
	// The database is the second line: the same write straight past the service
	// must fail too.
	if _, err := h.pool.Exec(ctx, `
		insert into forge_room_turns (id, room_id, seq, speaker_kind, speaker_label, text, channel, said_at)
		values ($1,$2,999,'human','Alice','said past the service','text',$3)`,
		id.New(id.PrefixTurn), room.ID, h.clk.Now()); err == nil {
		t.Fatal("the database accepted an unattributed human turn; the service check is the only thing holding it")
	}
	// An unrecognised speaker kind is refused rather than defaulted.
	if _, err := h.svc.Say(ctx, room.ID, &collab.Turn{
		Speaker: "anonymous", Text: "hello"}); err == nil {
		t.Fatal("an unrecognised speaker kind was accepted")
	}
	// FORGE naming a user is refused: a transcript must not suggest a person
	// said what the system said.
	if _, err := h.svc.Say(ctx, room.ID, &collab.Turn{
		Speaker: collab.SpeakerForge, SpeakerID: &h.alice, Text: "I drew it"}); err == nil {
		t.Fatal("a FORGE turn was attributed to a person")
	}

	good, err := h.svc.Say(ctx, room.ID, &collab.Turn{
		Speaker: collab.SpeakerHuman, SpeakerID: &h.alice, SpeakerLabel: "Alice",
		Text: "the tolerance is fine", Channel: collab.ChannelVoice})
	if err != nil {
		t.Fatal(err)
	}
	if good.Seq != 1 {
		t.Fatalf("the first turn is seq %d", good.Seq)
	}
	forge, err := h.svc.Say(ctx, room.ID, &collab.Turn{
		Speaker: collab.SpeakerForge, SpeakerLabel: "FORGE", Text: "I have drawn it"})
	if err != nil {
		t.Fatal(err)
	}
	if forge.SpeakerID != nil {
		t.Fatal("a FORGE turn carries a user id")
	}
}

// The question a room record is kept for: who was present when that happened.
func TestRoom_PresenceIsAnswerableAtAnInstant(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	room, _ := h.svc.OpenRoom(ctx, h.project, h.goalID, "design review", h.alice)
	joinedAt := h.clk.Now()

	h.clk.Advance(5 * time.Minute)
	bobJoined := h.clk.Now()
	if err := h.svc.Join(ctx, room.ID, h.bob); err != nil {
		t.Fatal(err)
	}
	h.clk.Advance(5 * time.Minute)
	bobLeft := h.clk.Now()
	if err := h.svc.Leave(ctx, room.ID, h.bob); err != nil {
		t.Fatal(err)
	}

	back, err := h.svc.Find(ctx, room.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := back.PresentAt(joinedAt); len(got) != 1 || got[0] != h.alice {
		t.Fatalf("at the start the room held %v", got)
	}
	if got := back.PresentAt(bobJoined.Add(time.Minute)); len(got) != 2 {
		t.Fatalf("while both were in it the room held %v", got)
	}
	if got := back.PresentAt(bobLeft.Add(time.Minute)); len(got) != 1 {
		t.Fatalf("after Bob left the room held %v", got)
	}
	// Leaving is recorded, not deleted: a departure that removed the row would
	// answer "was Bob here" wrongly for every instant he was.
	var found bool
	for _, p := range back.Participants {
		if p.UserID == h.bob {
			found = true
		}
	}
	if !found {
		t.Fatal("Bob is gone from the record entirely, so nothing says he was ever there")
	}
}

// A closed transcript does not gain turns.
func TestRoom_ClosingEndsTheRecord(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	room, _ := h.svc.OpenRoom(ctx, h.project, h.goalID, "design review", h.alice)
	if err := h.svc.Join(ctx, room.ID, h.bob); err != nil {
		t.Fatal(err)
	}
	h.clk.Advance(time.Minute)
	if err := h.svc.Close(ctx, room.ID, h.alice); err != nil {
		t.Fatal(err)
	}

	if _, err := h.svc.Say(ctx, room.ID, &collab.Turn{
		Speaker: collab.SpeakerHuman, SpeakerID: &h.alice, SpeakerLabel: "Alice",
		Text: "one more thing"}); err == nil {
		t.Fatal("a turn was appended to a closed transcript")
	}
	if err := h.svc.Join(ctx, room.ID, h.bob); err == nil {
		t.Fatal("somebody joined a closed room")
	}
	back, _ := h.svc.Find(ctx, room.ID)
	// Everybody still in it left when it closed; otherwise the record says they
	// are present forever.
	for _, p := range back.Participants {
		if p.LeftAt == nil {
			t.Fatalf("%s is still recorded as present in a closed room", p.UserID)
		}
	}
}

// "A record of who approved what" — the second half of COL-01.
func TestRoom_LinksTheApprovalsMadeInIt(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	room, _ := h.svc.OpenRoom(ctx, h.project, h.goalID, "design review", h.alice)
	taskID := h.addTask(t)
	approvalID := h.addApproval(t, taskID)

	if err := h.svc.LinkApproval(ctx, room.ID, approvalID); err != nil {
		t.Fatal(err)
	}
	// Idempotent: linking twice is not two approvals.
	if err := h.svc.LinkApproval(ctx, room.ID, approvalID); err != nil {
		t.Fatal(err)
	}
	back, _ := h.svc.Find(ctx, room.ID)
	if len(back.ApprovalIDs) != 1 || back.ApprovalIDs[0] != approvalID {
		t.Fatalf("the room records %v", back.ApprovalIDs)
	}
}

func (h *harness) addTask(t *testing.T) string {
	t.Helper()
	task := &engine.Task{
		ID: id.New(id.PrefixTask), GoalID: h.goalID, PlanID: h.planID,
		Title: "fit it", Instruction: "fit the bracket", Status: engine.StatusPending,
		IdempotencyKey: id.New(id.PrefixTask), MaxAttempts: 3, NotBefore: h.clk.Now(),
		Priority: 100, RiskTier: engine.RiskR2, CreatedAt: h.clk.Now(), UpdatedAt: h.clk.Now(),
	}
	if err := engine.NewRepository().CreateTask(context.Background(), h.pool, task, nil); err != nil {
		t.Fatal(err)
	}
	return task.ID
}

func (h *harness) addApproval(t *testing.T, taskID string) string {
	t.Helper()
	approvalID := id.New(id.PrefixApproval)
	if _, err := h.pool.Exec(context.Background(), `
		insert into forge_approvals (id, goal_id, task_id, risk_tier, summary, requested_at)
		values ($1,$2,$3,'r2','fit the bracket to the plate',$4)`,
		approvalID, h.goalID, taskID, h.clk.Now()); err != nil {
		t.Fatal(err)
	}
	return approvalID
}

// ---------------------------------------------------------------------------
// COL-02 — the handoff
// ---------------------------------------------------------------------------

// A handoff of live work must never read as a conclusion. It is handed to
// somebody who was not there, which is exactly the reader least able to tell.
func TestHandoff_NeverImpliesCompletionWhileWorkIsOpen(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	taskID := h.addTask(t)
	h.addApproval(t, taskID)

	doc, err := h.svc.TakeHandoff(ctx, h.goalID)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Complete() {
		t.Fatal("a goal with a pending task and an undecided approval handed off as complete")
	}
	if len(doc.Unresolved) == 0 {
		t.Fatal("nothing was listed as outstanding")
	}
	if doc.ApprovalsPending != 1 {
		t.Fatalf("%d approvals reported pending", doc.ApprovalsPending)
	}
	if !strings.Contains(doc.Summary(), "not a result") {
		t.Fatalf("the summary reads as a conclusion: %s", doc.Summary())
	}
	// The rendered document leads with what is open, because a reader skims.
	rendered := doc.Render()
	openIdx := strings.Index(rendered, "STILL OPEN")
	stateIdx := strings.Index(rendered, "STATE")
	if openIdx < 0 || stateIdx < 0 || openIdx > stateIdx {
		t.Fatalf("the document does not lead with what is unresolved:\n%s", rendered)
	}
}

// All seven of COL-02's parts.
func TestHandoff_CarriesEverythingCOL02Names(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	taskID := h.addTask(t)
	h.addApproval(t, taskID)

	// Some timeline, an artifact version, evidence and a risk — the inputs
	// waves 1 to 5 built.
	repo := engine.NewRepository()
	if err := repo.AppendEvent(ctx, h.pool, &engine.Event{
		GoalID: h.goalID, Kind: engine.EventGoalActivated, Actor: engine.ActorHuman,
		Summary: "started the goal"}, h.clk.Now()); err != nil {
		t.Fatal(err)
	}
	toolID := id.New(id.PrefixToolCall)
	if _, err := h.pool.Exec(ctx, `
		insert into forge_tool_calls (id, task_id, idempotency_key, tool_name, status,
			started_at, ended_at, duration_ms, created_at)
		values ($1,$2,$3,'workspace_write','succeeded',$4,$4,0,$4)`,
		toolID, taskID, id.New(id.PrefixToolCall), h.clk.Now()); err != nil {
		t.Fatal(err)
	}
	ws := workspace.NewService(h.pool, h.clk, logx.Discard())
	if _, _, err := ws.RecordChange(ctx, workspace.Change{
		ProjectID: h.project, Path: "src/bracket.scad", InitiatorID: h.alice,
		Agent: workspace.AgentExecutor, ToolCallID: &toolID,
		Inputs: map[string]any{}, Diff: "+ bracket", GoalID: h.goalID}); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.Add(ctx, workspace.NewNode{ProjectID: h.project, Kind: workspace.KindRisk,
		Title: "the plate may deform", CreatedBy: h.alice}); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.Add(ctx, workspace.NewNode{ProjectID: h.project, Kind: workspace.KindEvidence,
		Title: "load test at 12 kg passed", How: claim.Observed, CreatedBy: h.alice}); err != nil {
		t.Fatal(err)
	}

	doc, err := h.svc.TakeHandoff(ctx, h.goalID)
	if err != nil {
		t.Fatal(err)
	}
	// 1 state, 2 actions, 3 versions, 4 approvals, 5 evidence, 6 risks, 7 next.
	if doc.Status == "" || doc.TasksTotal == 0 {
		t.Fatal("no state")
	}
	if len(doc.RecentActions) == 0 {
		t.Fatal("no actions")
	}
	if len(doc.Versions) == 0 {
		t.Fatal("no artifact versions")
	}
	if doc.ApprovalsPending == 0 {
		t.Fatal("no approvals")
	}
	if len(doc.Evidence) == 0 {
		t.Fatal("no evidence")
	}
	if len(doc.OpenRisks) == 0 {
		t.Fatal("no open risks")
	}
	if len(doc.Recommended) == 0 {
		t.Fatal("nothing recommended")
	}
	// The artifact's standing is computed, not left to the reader.
	if doc.Versions[0].Usable {
		t.Fatal("a version nobody has looked at was reported usable")
	}
	if !strings.Contains(doc.Render(), "not from a model's opinion") {
		t.Fatal("the document does not say where its recommendations came from")
	}
}

// A handoff is a photograph. It must say when it was taken.
func TestHandoff_IsStampedAndDerivedFresh(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.addTask(t)

	first, err := h.svc.TakeHandoff(ctx, h.goalID)
	if err != nil {
		t.Fatal(err)
	}
	if first.TakenAt.IsZero() {
		t.Fatal("the handoff has no timestamp, so a reader cannot tell how stale it is")
	}
	h.clk.Advance(time.Hour)
	second, err := h.svc.TakeHandoff(ctx, h.goalID)
	if err != nil {
		t.Fatal(err)
	}
	if !second.TakenAt.After(first.TakenAt) {
		t.Fatal("a second handoff carries the first one's instant, so it is stored rather than derived")
	}
}

// Searching the transcript (PRD AUD-06).
//
// # Why this is a database test and not a unit test
//
// Everything worth checking here is Postgres behaviour: that "brackets" and
// "bracket" stem to the same lexeme, that websearch_to_tsquery survives what
// people type, and that a redacted turn's generated search_vector is empty. A
// test with a fake store would assert my beliefs about Postgres rather than
// Postgres.
func TestRoom_TranscriptIsSearchable(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	room, err := h.svc.OpenRoom(ctx, h.project, h.goalID, "design review", h.alice)
	if err != nil {
		t.Fatal(err)
	}
	say := func(text string, ch collab.Channel) *collab.Turn {
		t.Helper()
		turn, err := h.svc.Say(ctx, room.ID, &collab.Turn{
			Speaker: collab.SpeakerHuman, SpeakerID: &h.alice, SpeakerLabel: "Alice",
			Text: text, Channel: ch})
		if err != nil {
			t.Fatal(err)
		}
		return turn
	}
	say("the bracket is too thin at the root", collab.ChannelText)
	say("we should widen both brackets by 2mm", collab.ChannelText)
	say("the tolerance is fine", collab.ChannelText)
	secret := say("the fixture bolt is undersized", collab.ChannelVoice)

	texts := func(turns []collab.Turn) []string {
		out := []string{}
		for _, turn := range turns {
			out = append(out, turn.Text)
		}
		return out
	}
	search := func(q string) []collab.Turn {
		t.Helper()
		got, err := h.svc.SearchTurns(ctx, room.ID, q)
		if err != nil {
			t.Fatalf("searching for %q: %v", q, err)
		}
		return got
	}

	// The case the whole change exists for. A substring filter finds "brackets"
	// when you search "bracket" and finds NOTHING when you search "brackets",
	// because containment only runs one way. Stemming makes the two agree.
	for _, q := range []string{"bracket", "brackets"} {
		got := search(q)
		if len(got) != 2 {
			t.Errorf("searching %q returned %d turns (%v); both the singular and the "+
				"plural turn should match, which is the asymmetry full text is here to remove",
				q, len(got), texts(got))
		}
	}

	// Order is the order things were said. A transcript ranked by relevance
	// hands back a conversation in an order nobody spoke it in.
	got := search("bracket")
	if len(got) == 2 && got[0].Seq > got[1].Seq {
		t.Errorf("matches came back seq %d then %d; a transcript search must stay chronological",
			got[0].Seq, got[1].Seq)
	}

	// Scoped to the room. A second room's turns must not leak into the first's
	// results, which is the failure that turns a search box into a disclosure.
	other, err := h.svc.OpenRoom(ctx, h.project, h.goalID, "other room", h.alice)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Say(ctx, other.ID, &collab.Turn{
		Speaker: collab.SpeakerHuman, SpeakerID: &h.alice, SpeakerLabel: "Alice",
		Text: "a bracket in another room", Channel: collab.ChannelText}); err != nil {
		t.Fatal(err)
	}
	if got := search("bracket"); len(got) != 2 {
		t.Errorf("searching one room returned %d turns (%v); another room's transcript is visible",
			len(got), texts(got))
	}

	// What people type. to_tsquery raises a syntax error on every one of these;
	// a search box that 500s because somebody typed "and" is not a search box.
	for _, q := range []string{"bracket and", "bracket &", `"too thin"`, "bracket -tolerance", "!!!"} {
		if _, err := h.svc.SearchTurns(ctx, room.ID, q); err != nil {
			t.Errorf("searching %q failed: %v\nwebsearch_to_tsquery is used precisely so that "+
				"ordinary typing cannot produce an error", q, err)
		}
	}
	// A quoted phrase means the phrase, not the words in any order.
	if got := search(`"too thin"`); len(got) != 1 {
		t.Errorf(`searching "too thin" as a phrase returned %d turns (%v)`, len(got), texts(got))
	}

	// An empty search is refused rather than answered with the whole transcript.
	// Returning everything would be indistinguishable from a search that matched
	// everything, and the caller could not tell which had happened.
	if _, err := h.svc.SearchTurns(ctx, room.ID, "   "); err == nil {
		t.Error("an empty search was answered; a search with no query is a mistake, not a match-all")
	}

	// SEC-06: a redacted turn cannot be found by searching for what it said.
	if _, err := h.svc.RedactVoice(ctx, room.ID, &h.alice, h.alice); err != nil {
		t.Fatal(err)
	}
	if got := search("undersized"); len(got) != 0 {
		t.Errorf("a redacted turn was returned by searching for its content: %v\n"+
			"SEC-06 deletion means the words are gone, including from the index", texts(got))
	}
	// And the row is still there, unfindable rather than absent — the record
	// still says somebody spoke at that moment.
	room, err = h.svc.Find(ctx, room.ID)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, turn := range room.Turns {
		if turn.ID == secret.ID {
			found = true
			if turn.RedactedAt == nil {
				t.Error("the redacted turn is not marked redacted")
			}
		}
	}
	if !found {
		t.Error("the redacted turn vanished from the transcript; deletion is redaction, not removal")
	}
}

// The generated column is what makes redaction hold, so it is checked directly
// rather than only through the query that reads it.
//
// If search_vector were ever maintained by a trigger or by application code, this
// is the assertion that would fail first: a copy can be missed by an UPDATE path,
// and a stale index would still hold the words of a turn somebody deleted.
func TestRoom_RedactionEmptiesTheSearchIndexItself(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	room, err := h.svc.OpenRoom(ctx, h.project, h.goalID, "design review", h.alice)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := h.svc.Say(ctx, room.ID, &collab.Turn{
		Speaker: collab.SpeakerHuman, SpeakerID: &h.alice, SpeakerLabel: "Alice",
		Text: "the fixture bolt is undersized", Channel: collab.ChannelVoice})
	if err != nil {
		t.Fatal(err)
	}

	vector := func() string {
		t.Helper()
		var v string
		if err := h.pool.QueryRow(ctx,
			`select search_vector::text from forge_room_turns where id = $1`, turn.ID).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if vector() == "" {
		t.Fatal("a turn was indexed as empty; the generated column is not computing and " +
			"every assertion about search below it would pass vacuously")
	}
	if _, err := h.svc.RedactVoice(ctx, room.ID, &h.alice, h.alice); err != nil {
		t.Fatal(err)
	}
	if v := vector(); v != "" {
		t.Errorf("after redaction the search index still holds %q; the words of a deleted "+
			"turn are still in the database and findable", v)
	}
}

// The read path refuses a redacted turn even when its text is still there.
//
// # Why this case has to be constructed by hand
//
// SearchTurns has two independent reasons a deleted turn cannot be found: the
// generated search_vector empties when redaction blanks the text, and the query
// filters redacted_at. Through the ordinary path the first one alone is enough,
// which means deleting the second changes nothing anybody can observe — a guard
// no test can fail is decoration, and this repository has been bitten by exactly
// that shape before.
//
// So the row is put into the state a future bug would produce: redaction stamped
// and the text left behind, which is what a new deletion path that forgets to
// blank would write. The constraints permit it, so the database would too.
func TestRoom_SearchRefusesARedactedTurnEvenWithItsTextIntact(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	room, err := h.svc.OpenRoom(ctx, h.project, h.goalID, "design review", h.alice)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := h.svc.Say(ctx, room.ID, &collab.Turn{
		Speaker: collab.SpeakerHuman, SpeakerID: &h.alice, SpeakerLabel: "Alice",
		Text: "the fixture bolt is undersized", Channel: collab.ChannelVoice})
	if err != nil {
		t.Fatal(err)
	}
	// Stamped as redacted with the words still in the row.
	if _, err := h.pool.Exec(ctx,
		`update forge_room_turns set redacted_at = $2, redacted_by = $3 where id = $1`,
		turn.ID, h.clk.Now(), h.alice); err != nil {
		t.Fatal(err)
	}

	got, err := h.svc.SearchTurns(ctx, room.ID, "undersized")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("a turn marked redacted was returned by searching its text (%d hits); "+
			"the query's own redacted_at filter is the only thing standing between a "+
			"half-applied deletion and the words it was meant to remove", len(got))
	}
}
