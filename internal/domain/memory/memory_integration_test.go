package memory_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/auth"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/claim"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/identity"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/memory"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Memory against a real database and the real migration chain.
//
// The unit tests hold the layer table's shape. These hold the properties that
// only exist once there is somewhere to write to: that expiry is enforced by the
// read rather than by a sweep, that a user's deletion survives the agent trying
// to learn the same thing again, and that a superseded decision stays readable.

type harness struct {
	pool    *db.Pool
	svc     *memory.Service
	clk     *clock.Fake
	userID  string
	otherID string
	project string
	goalID  string
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	url := envOr(t)
	ctx := context.Background()

	schema := "forge_mem_" + strings.ToLower(strings.NewReplacer("/", "_", "-", "_").Replace(t.Name()))
	if len(schema) > 60 {
		schema = schema[:60]
	}
	admin, err := db.Connect(ctx, dbConfig(url), logx.Discard())
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
	pool, err := db.Connect(ctx, dbConfig(url+sep+"search_path="+schema), logx.Discard())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MigrateFS(ctx, pool, db.Files, db.MigrationsDir, logx.Discard()); err != nil {
		t.Fatalf("migrating the test schema: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		if cleanup, err := db.Connect(context.Background(), dbConfig(url), logx.Discard()); err == nil {
			_, _ = cleanup.Exec(context.Background(), "drop schema if exists "+schema+" cascade")
			cleanup.Close()
		}
	})

	clk := clock.NewFake(time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC))
	h := &harness{pool: pool, clk: clk, svc: memory.NewService(pool, clk, logx.Discard())}
	h.seed(t)
	return h
}

func envOr(t *testing.T) string {
	t.Helper()
	url := os.Getenv("FORGE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("FORGE_TEST_DATABASE_URL is unset; skipping live-database tests. Run `make db-up` then `make test-integration`.")
	}
	return url
}

func dbConfig(url string) config.DBConfig {
	return config.DBConfig{
		URL: url, MaxConns: 8, MinConns: 1,
		MaxConnLifetime: time.Hour, MaxConnIdleTime: time.Minute, ConnectTimeout: 10 * time.Second,
	}
}

// seed builds the owner, a second user, a project and a goal through the real
// repositories, so the fixture meets the same constraints production does.
func (h *harness) seed(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	now := h.clk.Now()

	mk := func(email string) string {
		hash, err := auth.HashPassword("correct horse battery staple")
		if err != nil {
			t.Fatal(err)
		}
		u := &identity.User{
			ID: id.New(id.PrefixUser), Email: email, Status: identity.StatusActive,
			PasswordHash: hash, PasswordAlgo: auth.AlgoArgon2id,
			PasswordChangedAt: now, CreatedAt: now, UpdatedAt: now,
		}
		if err := identity.NewRepository().CreateUser(ctx, h.pool, u); err != nil {
			t.Fatal(err)
		}
		return u.ID
	}
	h.userID = mk("owner@example.com")
	h.otherID = mk("someone.else@example.com")

	h.project = id.New(id.PrefixProject)
	if _, err := h.pool.Exec(ctx,
		`insert into forge_projects (id, owner_id, name, created_at, updated_at) values ($1,$2,$3,$4,$4)`,
		h.project, h.userID, "test project", now); err != nil {
		t.Fatal(err)
	}
	h.goalID = id.New(id.PrefixGoal)
	if _, err := h.pool.Exec(ctx,
		`insert into forge_goals (id, project_id, created_by, title, statement, status, started_at, created_at, updated_at)
		 values ($1,$2,$3,$4,$5,'active',$6,$6,$6)`,
		h.goalID, h.project, h.userID, "test goal", "do the thing", now); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) remember(t *testing.T, w memory.Write) *memory.Item {
	t.Helper()
	item, err := h.svc.Remember(context.Background(), w)
	if err != nil {
		t.Fatalf("remembering %q: %v", w.Key, err)
	}
	return item
}

func (h *harness) projectWrite(key string, value any) memory.Write {
	return memory.Write{Scope: memory.ScopeProject, Owner: h.project, Key: key,
		Value: value, How: claim.Observed, Source: "test"}
}

// ---------------------------------------------------------------------------
// MEM-01 — layers, retention
// ---------------------------------------------------------------------------

// Every layer must actually be writable. A layer the schema rejects is a layer
// that exists only in the Go table, and the two would have drifted silently.
func TestMemory_EveryLayerRoundTrips(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	owners := map[memory.Owner]string{
		memory.OwnerGoal: h.goalID, memory.OwnerProject: h.project,
		memory.OwnerUser: h.userID, memory.OwnerNone: "",
	}
	for _, layer := range memory.Layers() {
		w := memory.Write{Scope: layer.Scope, Owner: owners[layer.Owner],
			Key: "probe." + string(layer.Scope), Value: map[string]any{"n": 1},
			How: claim.Observed, Source: "test"}
		item, err := h.svc.Remember(ctx, w)
		if err != nil {
			t.Fatalf("%s memory could not be written: %v", layer.Scope, err)
		}
		if layer.DefaultTTL > 0 && item.ExpiresAt == nil {
			t.Fatalf("%s memory was written with no expiry despite a %s lifetime", layer.Scope, layer.DefaultTTL)
		}
		if layer.DefaultTTL == 0 && item.ExpiresAt != nil {
			t.Fatalf("%s memory was given an expiry it does not have", layer.Scope)
		}
	}
}

// The property the whole retention design rests on: expiry is enforced by the
// READ. A deployment whose sweep never runs must not serve week-old turn
// context as though it were current.
func TestMemory_ExpiryIsEnforcedWithoutASweep(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	h.remember(t, memory.Write{Scope: memory.ScopeTurn, Owner: h.goalID,
		Key: "scratch", Value: "passing detail", How: claim.Observed})

	// Past the turn layer's lifetime, with nothing swept.
	h.clk.Advance(time.Hour)

	got, err := h.svc.Recall(ctx, memory.Recall{GoalID: h.goalID, Scopes: []memory.Scope{memory.ScopeTurn}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expired turn memory was recalled with no sweep having run: %v", got[0])
	}

	// And the row is still there — this is a read-time guarantee, not a delete.
	var n int
	if err := h.pool.QueryRow(ctx, `select count(*) from forge_memory where key = 'scratch'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("the expired row was %d rows in the table; the read filtered it, nothing should have removed it", n)
	}

	// The sweep then reclaims it, and says how many.
	removed, err := h.svc.Sweep(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("the sweep reclaimed %d rows; one was expired", removed)
	}
}

// An exact key must not be a way around expiry. If it were, every caller who
// knew a key would silently opt out of retention.
func TestMemory_ExactKeyDoesNotBypassExpiry(t *testing.T) {
	h := newHarness(t)
	h.remember(t, memory.Write{Scope: memory.ScopeTurn, Owner: h.goalID,
		Key: "scratch", Value: 1, How: claim.Observed})
	h.clk.Advance(time.Hour)

	got, err := h.svc.Recall(context.Background(), memory.Recall{
		GoalID: h.goalID, Keys: []string{"scratch"}, Scopes: []memory.Scope{memory.ScopeTurn}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatal("asking for an expired item by name returned it; exact-key lookup is an expiry bypass")
	}
}

func TestMemory_PinningOutlivesTheLayer(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	item := h.remember(t, memory.Write{Scope: memory.ScopeTurn, Owner: h.goalID,
		Key: "keep.this", Value: "matters", How: claim.Observed})
	if err := h.svc.Pin(ctx, item.ID, true); err != nil {
		t.Fatal(err)
	}
	h.clk.Advance(24 * time.Hour)

	got, err := h.svc.Recall(ctx, memory.Recall{GoalID: h.goalID, Scopes: []memory.Scope{memory.ScopeTurn}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("a pinned item was dropped at its layer's expiry (%d returned)", len(got))
	}
	if got[0].Why != memory.ReasonPinned {
		t.Fatalf("a pinned item came back for reason %q; the reader should be told it was pinned", got[0].Why)
	}
	if n, err := h.svc.Sweep(ctx); err != nil || n != 0 {
		t.Fatalf("the sweep removed %d pinned rows (err %v); pinning must survive it", n, err)
	}
}

// Personal preference must not leak into somebody else's recall. This is the
// visibility rule that is actually enforced today, so it is the one asserted.
func TestMemory_PersonalMemoryDoesNotLeakBetweenUsers(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	h.remember(t, memory.Write{Scope: memory.ScopeUser, Owner: h.userID,
		Key: "units.preference", Value: "metric", How: claim.Observed})

	got, err := h.svc.Recall(ctx, memory.Recall{UserID: h.otherID, Scopes: []memory.Scope{memory.ScopeUser}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("another user's personal memory was returned: %v", got[0])
	}
}

// Reading an owned layer without naming its owner must fail loudly. Returning
// nothing would read as "there is nothing there" rather than "you did not say
// whose", and the caller would ship the bug.
func TestMemory_RecallingAnOwnedLayerWithoutAnOwnerIsRefused(t *testing.T) {
	h := newHarness(t)
	_, err := h.svc.Recall(context.Background(), memory.Recall{Scopes: []memory.Scope{memory.ScopeUser}})
	if err == nil {
		t.Fatal("personal memory was recalled with nobody named")
	}
	if !errs.Is(err, errs.CodeValidationFailed) {
		t.Fatalf("got %s; the caller needs to be told what is missing", errs.CodeOf(err))
	}
}

// ---------------------------------------------------------------------------
// MEM-02 — user control
// ---------------------------------------------------------------------------

// The requirement in one test: nothing comes back without a reason THIS query
// returned it.
func TestMemory_EveryRecalledItemSaysWhyItWasRetrieved(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	h.remember(t, h.projectWrite("bolt.size", "M3"))
	h.remember(t, h.projectWrite("bolt.pitch", "0.5 mm"))
	h.remember(t, h.projectWrite("finish", "anodised"))
	pinned := h.remember(t, h.projectWrite("always.say", "this project is metric"))
	if err := h.svc.Pin(ctx, pinned.ID, true); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		rc   memory.Recall
		want memory.Reason
		key  string
	}{
		{"exact key", memory.Recall{ProjectID: h.project, Keys: []string{"finish"},
			Scopes: []memory.Scope{memory.ScopeProject}}, memory.ReasonExactKey, "finish"},
		{"prefix", memory.Recall{ProjectID: h.project, Prefix: "bolt.",
			Scopes: []memory.Scope{memory.ScopeProject}}, memory.ReasonPrefix, "bolt.size"},
		{"pinned", memory.Recall{ProjectID: h.project,
			Scopes: []memory.Scope{memory.ScopeProject}}, memory.ReasonPinned, "always.say"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := h.svc.Recall(ctx, tc.rc)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) == 0 {
				t.Fatal("nothing came back")
			}
			var found bool
			for _, r := range got {
				if strings.TrimSpace(string(r.Why)) == "" || strings.TrimSpace(r.Detail) == "" {
					t.Fatalf("%q came back with no reason; MEM-02 asks FORGE to show why", r.Item.Key)
				}
				if strings.Contains(r.Detail, "defect") {
					t.Fatalf("%q came back with an unrecognised reason: %s", r.Item.Key, r.Detail)
				}
				if r.Item.Key == tc.key {
					found = true
					if r.Why != tc.want {
						t.Fatalf("%q was returned as %q; it matched by %q", r.Item.Key, r.Why, tc.want)
					}
				}
			}
			if !found {
				t.Fatalf("%q did not come back at all", tc.key)
			}
		})
	}
}

// The one that makes deletion mean something. FORGE writes memory unprompted,
// so a plain delete would be undone on the next turn that observed the same
// thing — and nothing would report that it had been.
func TestMemory_ForgettingSurvivesTheAgentLearningItAgain(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	item := h.remember(t, h.projectWrite("supplier", "Acme Fasteners"))
	if err := h.svc.Forget(ctx, item.ID, h.userID, "wrong supplier"); err != nil {
		t.Fatal(err)
	}

	// The agent observes the same thing again and tries to write it.
	_, err := h.svc.Remember(ctx, h.projectWrite("supplier", "Acme Fasteners"))
	if err == nil {
		t.Fatal("a forgotten memory was silently re-learned; the user's deletion undid itself")
	}
	if !errs.Is(err, errs.CodeMemoryForgotten) {
		t.Fatalf("re-learning a forgotten key reported %s; it must say the key was forgotten", errs.CodeOf(err))
	}

	// And it stays out of recall.
	got, err := h.svc.Recall(ctx, memory.Recall{ProjectID: h.project,
		Keys: []string{"supplier"}, Scopes: []memory.Scope{memory.ScopeProject}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatal("a forgotten item was recalled")
	}
}

// Forgetting removes the content, not the record of the deletion. Keeping the
// value would not be forgetting; losing the row would let it be re-learned.
func TestMemory_ForgettingClearsTheValueAndKeepsTheAccount(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	item := h.remember(t, h.projectWrite("home.address", "12 Example Street"))
	if err := h.svc.Forget(ctx, item.ID, h.userID, "personal data"); err != nil {
		t.Fatal(err)
	}

	back, err := h.svc.Repo().FindByID(ctx, h.pool, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(back.Value), "Example Street") {
		t.Fatalf("the forgotten value is still in the row: %s", back.Value)
	}
	if !back.Forgotten() || back.ForgottenBy == nil || *back.ForgottenBy != h.userID {
		t.Fatal("the deletion was not attributed to the person who asked for it")
	}
	if back.ForgottenReason != "personal data" {
		t.Fatalf("the reason was recorded as %q", back.ForgottenReason)
	}
}

// Forgetting twice must not move the date: "when did this stop being used?" has
// one answer.
func TestMemory_ForgettingIsIdempotent(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	item := h.remember(t, h.projectWrite("k", "v"))
	if err := h.svc.Forget(ctx, item.ID, h.userID, "first"); err != nil {
		t.Fatal(err)
	}
	first, err := h.svc.Repo().FindByID(ctx, h.pool, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	h.clk.Advance(time.Hour)
	if err := h.svc.Forget(ctx, item.ID, h.userID, "second"); err != nil {
		t.Fatalf("forgetting an already-forgotten item failed: %v", err)
	}
	again, err := h.svc.Repo().FindByID(ctx, h.pool, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !again.ForgottenAt.Equal(*first.ForgottenAt) {
		t.Fatalf("the deletion date moved from %s to %s on a second forget", first.ForgottenAt, again.ForgottenAt)
	}
	if again.ForgottenReason != "first" {
		t.Fatalf("the recorded reason was overwritten with %q", again.ForgottenReason)
	}
}

// Purge is the deliberate act that re-opens a key, and it refuses to stand in
// for a deletion that was never recorded.
func TestMemory_PurgeReopensTheKeyAndRefusesLiveItems(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	live := h.remember(t, h.projectWrite("still.here", "v"))
	if err := h.svc.Purge(ctx, live.ID); err == nil {
		t.Fatal("a live item was purged without the deletion ever being recorded")
	}

	item := h.remember(t, h.projectWrite("supplier", "Acme"))
	if err := h.svc.Forget(ctx, item.ID, h.userID, "wrong"); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Purge(ctx, item.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Remember(ctx, h.projectWrite("supplier", "Beta Fixings")); err != nil {
		t.Fatalf("the key was still blocked after a purge: %v", err)
	}
}

// A correction is not a new memory: the age and the pin are facts a user reads
// when deciding whether to trust the item.
func TestMemory_CorrectionKeepsIdentityAgeAndPin(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	item := h.remember(t, h.projectWrite("wall.thickness", "3 mm"))
	if err := h.svc.Pin(ctx, item.ID, true); err != nil {
		t.Fatal(err)
	}
	h.clk.Advance(2 * time.Hour)

	fixed, err := h.svc.Correct(ctx, item.ID, "4 mm", claim.Observed, "measured on the print")
	if err != nil {
		t.Fatal(err)
	}
	if fixed.ID != item.ID {
		t.Fatal("correcting an item replaced it with a new one; its age and pin would have reset")
	}
	if !fixed.CreatedAt.Equal(item.CreatedAt) {
		t.Fatalf("created_at moved from %s to %s on a correction", item.CreatedAt, fixed.CreatedAt)
	}
	back, err := h.svc.Repo().FindByID(ctx, h.pool, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !back.Pinned {
		t.Fatal("a correction silently unpinned the item")
	}
	if !strings.Contains(string(back.Value), "4 mm") {
		t.Fatalf("the correction did not take: %s", back.Value)
	}
}

// Correcting a forgotten item would bring back what a user deleted.
func TestMemory_CorrectingAForgottenItemIsRefused(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	item := h.remember(t, h.projectWrite("k", "v"))
	if err := h.svc.Forget(ctx, item.ID, h.userID, "no"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Correct(ctx, item.ID, "v2", claim.Observed, "s"); err == nil {
		t.Fatal("a forgotten item was corrected back into existence")
	}
}

// Export must show the deletion. Omitting forgotten rows would tell the user
// their deletion left nothing behind, which is not true — the key is still
// claimed, and that is what stops it being re-learned.
func TestMemory_ExportShowsForgottenItemsAsForgotten(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	h.remember(t, h.projectWrite("kept", "value"))
	gone := h.remember(t, h.projectWrite("removed", "value"))
	if err := h.svc.Forget(ctx, gone.ID, h.userID, "asked to"); err != nil {
		t.Fatal(err)
	}

	export, err := h.svc.ExportLayer(ctx, memory.ScopeProject, h.project)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := json.Marshal(export)
	if err != nil {
		t.Fatal(err)
	}
	if len(export.Items) != 2 {
		t.Fatalf("the export holds %d items; the forgotten one must appear, saying so", len(export.Items))
	}
	var sawForgotten bool
	for _, it := range export.Items {
		if it.Key == "removed" {
			sawForgotten = it.ForgottenAt != "" && it.ForgottenReason == "asked to"
			if strings.Contains(string(it.Value), "value") {
				t.Fatal("the export leaked the content of a forgotten item")
			}
		}
		if it.HowMeans == "" {
			t.Fatalf("export item %q does not say what its epistemic label means", it.Key)
		}
	}
	if !sawForgotten {
		t.Fatalf("the export did not mark the forgotten item: %s", blob)
	}
}

// ---------------------------------------------------------------------------
// MEM-03 — decision log
// ---------------------------------------------------------------------------

func (h *harness) decision(t *testing.T, title, what string, supersedes *string) *memory.Decision {
	t.Helper()
	d, err := h.svc.RecordDecision(context.Background(), &memory.Decision{
		ProjectID: h.project, GoalID: &h.goalID, AuthorID: h.userID,
		Title: title, Decision: what, Rationale: "because " + what,
		Alternatives: []memory.Alternative{{Option: "do nothing", WhyNot: "the bracket still needs mounting"}},
		Evidence:     []claim.Claim{{Statement: "the plate is 3 mm", How: claim.Observed, Source: "the print"}},
		Affected:     []string{"art_bracket"},
		SupersedesID: supersedes,
	})
	if err != nil {
		t.Fatalf("recording %q: %v", title, err)
	}
	return d
}

// The requirement in one test: a decision can supersede another, and both stay
// readable.
func TestDecisions_SupersessionKeepsBothReadable(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	first := h.decision(t, "Mount with M3", "use M3 bolts", nil)
	h.clk.Advance(time.Hour)
	second := h.decision(t, "Mount with M4", "use M4 bolts", &first.ID)

	chain, err := h.svc.DecisionChain(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 2 {
		t.Fatalf("the chain holds %d decisions; both the old answer and the new one must be readable", len(chain))
	}
	if chain[0].ID != first.ID || chain[1].ID != second.ID {
		t.Fatal("the chain is not in the order the decisions were made")
	}
	if chain[0].Current() {
		t.Fatal("the superseded decision still reports itself as the current answer")
	}
	if !chain[1].Current() {
		t.Fatal("the decision that superseded it does not report itself as current")
	}
	// The old one keeps everything, including why the option that lost lost.
	if chain[0].Decision != "use M3 bolts" || len(chain[0].Alternatives) != 1 {
		t.Fatal("superseding edited the decision it replaced")
	}
	// And the chain is reachable from either end.
	fromEnd, err := h.svc.DecisionChain(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(fromEnd) != 2 || fromEnd[0].ID != first.ID {
		t.Fatal("the chain could not be walked backwards from the current decision")
	}
}

// "What do we currently believe?" must have exactly one answer.
func TestDecisions_ADecisionCanOnlyBeSupersededOnce(t *testing.T) {
	h := newHarness(t)

	first := h.decision(t, "Mount with M3", "use M3 bolts", nil)
	h.decision(t, "Mount with M4", "use M4 bolts", &first.ID)

	_, err := h.svc.RecordDecision(context.Background(), &memory.Decision{
		ProjectID: h.project, AuthorID: h.userID, Title: "Mount with M5",
		Decision: "use M5 bolts", SupersedesID: &first.ID,
	})
	if err == nil {
		t.Fatal("the same decision was superseded twice; the log now holds two contradictory currents")
	}
	if !errs.Is(err, errs.CodeDecisionSuperseded) {
		t.Fatalf("got %s; the author needs to be told to supersede the current one instead", errs.CodeOf(err))
	}
}

// Superseding across projects would let one project's history rewrite another's.
func TestDecisions_CannotSupersedeAcrossProjects(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	first := h.decision(t, "Mount with M3", "use M3 bolts", nil)

	other := id.New(id.PrefixProject)
	if _, err := h.pool.Exec(ctx,
		`insert into forge_projects (id, owner_id, name, created_at, updated_at) values ($1,$2,$3,$4,$4)`,
		other, h.userID, "another project", h.clk.Now()); err != nil {
		t.Fatal(err)
	}
	_, err := h.svc.RecordDecision(ctx, &memory.Decision{
		ProjectID: other, AuthorID: h.userID, Title: "elsewhere",
		Decision: "use M4", SupersedesID: &first.ID,
	})
	if err == nil {
		t.Fatal("a decision in one project superseded a decision in another")
	}
}

// The evidence survives the round trip with its labels intact — the whole
// reason MEM-03 waited for the claim vocabulary.
func TestDecisions_EvidenceKeepsItsEpistemicLabels(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	d, err := h.svc.RecordDecision(ctx, &memory.Decision{
		ProjectID: h.project, AuthorID: h.userID, Title: "Bore diameter",
		Decision: "5 mm bore",
		Evidence: []claim.Claim{
			{Statement: "the shaft measured 4.98 mm", How: claim.Observed, Source: "calipers"},
			{Statement: "NEMA 17 shafts are 5 mm", How: claim.Retrieved},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	back, err := h.svc.FindDecision(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Evidence) != 2 {
		t.Fatalf("%d evidence items survived the round trip", len(back.Evidence))
	}
	if back.Evidence[0].How != claim.Observed || !back.Evidence[0].Actionableish() {
		t.Fatal("a measurement came back unusable")
	}
	if back.Evidence[1].How != claim.Retrieved || back.Evidence[1].Actionableish() {
		t.Fatal("a figure recalled from model weights came back as something a reader may act on")
	}
	if !strings.Contains(back.Evidence[1].Source, "FORGE's own memory") {
		t.Fatalf("the recalled figure's source reads %q; it must name where it really came from", back.Evidence[1].Source)
	}
}

func TestDecisions_ListingSeparatesCurrentFromSuperseded(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	first := h.decision(t, "Mount with M3", "use M3 bolts", nil)
	h.clk.Advance(time.Hour)
	h.decision(t, "Mount with M4", "use M4 bolts", &first.ID)
	h.clk.Advance(time.Hour)
	h.decision(t, "Finish", "anodise it", nil)

	all, err := h.svc.ListDecisions(ctx, memory.DecisionFilter{ProjectID: h.project})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("%d decisions listed; three were made", len(all))
	}
	current, err := h.svc.ListDecisions(ctx, memory.DecisionFilter{ProjectID: h.project, CurrentOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(current) != 2 {
		t.Fatalf("%d decisions are current; the superseded one must be excluded and the other two kept", len(current))
	}
	for _, d := range current {
		if !d.Current() {
			t.Fatalf("decision %q was listed as current and does not think it is", d.Title)
		}
	}
}

// The database is the second enforcement point, so that two concurrent
// supersessions cannot both win by passing the service check together.
func TestDecisions_TheDatabaseAlsoRefusesASecondSuccessor(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	first := h.decision(t, "Mount with M3", "use M3 bolts", nil)
	h.decision(t, "Mount with M4", "use M4 bolts", &first.ID)

	// Straight past the service, the way a race would arrive.
	_, err := h.pool.Exec(ctx, `
		insert into forge_decisions (id, project_id, author_id, title, decision, supersedes_id, decided_at, created_at, updated_at)
		values ($1,$2,$3,'race','use M5',$4,$5,$5,$5)`,
		id.New(id.PrefixDecision), h.project, h.userID, first.ID, h.clk.Now())
	if err == nil {
		t.Fatal("the database accepted a second successor; the service check is the only thing holding the invariant")
	}
	if !strings.Contains(err.Error(), "forge_decisions_supersedes_once") {
		t.Fatalf("the write failed for a different reason: %v", err)
	}
}

func TestDecisions_CannotSupersedeItself(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	_, err := h.pool.Exec(ctx, `
		insert into forge_decisions (id, project_id, author_id, title, decision, supersedes_id, decided_at, created_at, updated_at)
		values ('dec_self',$1,$2,'self','x','dec_self',$3,$3,$3)`,
		h.project, h.userID, h.clk.Now())
	if err == nil {
		t.Fatal("a decision superseded itself; the chain would never terminate")
	}
}
