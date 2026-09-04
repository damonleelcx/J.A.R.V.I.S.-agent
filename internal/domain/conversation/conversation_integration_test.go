package conversation_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/auth"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/conversation"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/identity"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// The workbench record against a real database and the real migration chain.
//
// What only exists once there is somewhere to write: that a conversation comes
// back in order, that it belongs to exactly one person, that a client cannot
// name one into existence, and that deleting it deletes it.

type harness struct {
	pool *db.Pool
	svc  *conversation.Service
	clk  *clock.Fake
	a, b string // two people, because most of what matters here is a boundary
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	url := os.Getenv("FORGE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("FORGE_TEST_DATABASE_URL is unset; skipping live-database tests. Run `make db-up` then `make test-integration`.")
	}
	ctx := context.Background()
	schema := "forge_cnv_" + strings.ToLower(strings.NewReplacer("/", "_", "-", "_").Replace(t.Name()))
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

	clk := clock.NewFake(time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC))
	h := &harness{pool: pool, clk: clk,
		svc: conversation.NewService(pool, clk, logx.Discard())}
	h.a = h.person(t, "ada@example.com")
	h.b = h.person(t, "bo@example.com")
	return h
}

func (h *harness) person(t *testing.T, email string) string {
	t.Helper()
	now := h.clk.Now()
	hash, err := auth.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	u := &identity.User{ID: id.New(id.PrefixUser), Email: email,
		Status: identity.StatusActive, PasswordHash: hash, PasswordAlgo: auth.AlgoArgon2id,
		PasswordChangedAt: now, CreatedAt: now, UpdatedAt: now}
	if err := identity.NewRepository().CreateUser(context.Background(), h.pool, u); err != nil {
		t.Fatal(err)
	}
	return u.ID
}

func (h *harness) say(t *testing.T, conv, owner string, role conversation.Role, text string) *conversation.Turn {
	t.Helper()
	turn, err := h.svc.Record(context.Background(), conversation.Said{
		ConversationID: conv, OwnerID: owner, Role: role, Text: text,
	})
	if err != nil {
		t.Fatal(err)
	}
	return turn
}

// A conversation outlives the page that had it (PRD RSN-07).
//
// # What was wrong before this
//
// `history` was posted BY THE BROWSER on every turn and no table held it, so a
// reload lost the thread that produced the work while the work itself survived.
// RSN-07 asks to resume from a structured checkpoint rather than a summary; the
// turns are that structure, and they were never written down.
func TestAConversationSurvivesTheSessionThatHadIt(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	conv, err := h.svc.Resolve(ctx, "", h.a)
	if err != nil {
		t.Fatal(err)
	}
	h.say(t, conv, h.a, conversation.RoleHuman, "a 24mm washer, 3mm thick")
	h.say(t, conv, h.a, conversation.RoleForge, "Proposing a washer.")
	h.say(t, conv, h.a, conversation.RoleHuman, "make it 4mm")

	got, err := h.svc.History(ctx, conv, h.a)
	if err != nil {
		t.Fatalf("the conversation did not come back: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("3 turns were said and %d came back", len(got))
	}
	// In order, and numbered. A record that returns the right sentences in the
	// wrong order is a different conversation.
	for i, want := range []string{"a 24mm washer, 3mm thick", "Proposing a washer.", "make it 4mm"} {
		if got[i].Text != want {
			t.Errorf("turn %d is %q, wanted %q", i+1, got[i].Text, want)
		}
		if got[i].Seq != i+1 {
			t.Errorf("turn %d is numbered %d; the order is the record", i+1, got[i].Seq)
		}
	}
	if got[0].Role != conversation.RoleHuman || got[1].Role != conversation.RoleForge {
		t.Errorf("the speakers came back wrong: %s then %s — a record that misattributes a "+
			"sentence says somebody said something they did not", got[0].Role, got[1].Role)
	}
}

// A conversation belongs to exactly one person.
//
// The id is a bare string in a URL and in localStorage. Every path that touches
// one — continue it, read it, delete it — is scoped to the caller, and each is
// checked separately here because they are three different queries and a filter
// is only applied where somebody remembered to apply it.
func TestAConversationIsOnlyItsOwners(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	conv, err := h.svc.Resolve(ctx, "", h.a)
	if err != nil {
		t.Fatal(err)
	}
	h.say(t, conv, h.a, conversation.RoleHuman, "the bracket needs two M5 holes")

	t.Run("cannot be continued", func(t *testing.T) {
		got, err := h.svc.Resolve(ctx, conv, h.b)
		if err == nil {
			t.Fatalf("Bo was allowed to continue Ada's conversation as %q — the next turn would "+
				"have been appended to somebody else's record", got)
		}
		if errs.CodeOf(err) != errs.CodeNotFound {
			t.Errorf("refused with %s; it must be NOT_FOUND, because telling a stranger that a "+
				"conversation exists but is not theirs is the fact they were probing for",
				errs.CodeOf(err))
		}
	})

	t.Run("cannot be read", func(t *testing.T) {
		if _, err := h.svc.History(ctx, conv, h.b); err == nil {
			t.Fatal("Bo read Ada's conversation")
		}
	})

	t.Run("cannot be deleted", func(t *testing.T) {
		if _, err := h.svc.Forget(ctx, conv, h.b); err == nil {
			t.Fatal("Bo deleted Ada's conversation")
		}
		if got, err := h.svc.History(ctx, conv, h.a); err != nil || len(got) != 1 {
			t.Fatalf("Ada's conversation did not survive Bo's attempt: %v, %d turns", err, len(got))
		}
	})
}

// A client cannot name a conversation into existence.
//
// If it could, it would be choosing its own ids — and an id a client chooses is
// one it can choose twice, guess, or aim at somebody else. The rule has no
// exceptions: an id that exists must be the caller's, and an id that does not
// exist is refused rather than created.
func TestAClientCannotNameAConversationIntoExistence(t *testing.T) {
	h := newHarness(t)

	invented := id.New(id.PrefixConversation)
	if got, err := h.svc.Resolve(context.Background(), invented, h.a); err == nil {
		t.Fatalf("an id the server never minted was accepted (%q). Ids would then be chosen by "+
			"whoever asks, which is the same as not having them.", got)
	}
}

// Deleting it deletes it, and says how much (PRD AUD-07, MEM-01).
//
// This layer's stated retention is "until the person says otherwise". That is
// only true if saying otherwise works, and only useful if the person can tell
// the difference between "deleted" and "there was nothing there".
func TestForgettingRemovesTheRecordAndSaysHowMuch(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	conv, err := h.svc.Resolve(ctx, "", h.a)
	if err != nil {
		t.Fatal(err)
	}
	h.say(t, conv, h.a, conversation.RoleHuman, "one")
	h.say(t, conv, h.a, conversation.RoleForge, "two")

	n, err := h.svc.Forget(ctx, conv, h.a)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("deleted %d turns, said 2 — the count is what tells somebody it worked", n)
	}
	if _, err := h.svc.History(ctx, conv, h.a); err == nil {
		t.Fatal("the conversation is still readable after being deleted")
	}
	if _, err := h.svc.Forget(ctx, conv, h.a); err == nil {
		t.Fatal("deleting it twice reported success the second time, which claims to have " +
			"removed something that was already gone")
	}
}

// Every role the code can write, the schema accepts — and nothing else.
//
// # Why this is a fence and not an assumption
//
// The role is a Go constant AND a check constraint, in two files that are edited
// at different times. A value added in Go alone fails at the INSERT, which in
// this codebase means a turn that vanishes with an error nobody reads; a value
// removed from Go alone leaves rows the scanner then refuses to load. Held
// against conversation.Roles() so adding one there is what turns this red.
func TestTheSchemaAcceptsEveryRoleTheCodeCanWrite(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	roles := conversation.Roles()
	if len(roles) < 2 {
		t.Fatalf("the package reports %d role(s); this fence is looking at nothing", len(roles))
	}
	for _, role := range roles {
		conv, err := h.svc.Resolve(ctx, "", h.a)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.svc.Record(ctx, conversation.Said{
			ConversationID: conv, OwnerID: h.a, Role: role, Text: "said by a " + string(role),
		}); err != nil {
			t.Errorf("the code can write role %q and the schema will not take it: %v", role, err)
		}
	}

	// And the constraint is real, not decorative: a role this build does not
	// know must be refused by the database even if something got past Go.
	conv, err := h.svc.Resolve(ctx, "", h.a)
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.pool.Exec(ctx, `
		insert into forge_conversation_turns
			(id, conversation_id, owner_id, seq, role, text, said_at)
		values ($1,$2,$3,1,'assistant','hello',$4)`,
		id.New(id.PrefixTurn), conv, h.a, h.clk.Now())
	if err == nil {
		t.Error("the database accepted role 'assistant'. The check constraint is not holding the " +
			"vocabulary, so a provider's word for a speaker could end up in a product's record.")
	}
}
