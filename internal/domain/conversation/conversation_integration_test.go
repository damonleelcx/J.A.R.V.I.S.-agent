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

// ---------------------------------------------------------------------------
// The timings (wave 28, PRD NFR-05)
// ---------------------------------------------------------------------------

func ms(v int) *int        { return &v }
func count(v int64) *int64 { return &v }

// conversationFor mints a conversation for this person. A client may not name
// one into existence, so the tests below ask the service for one.
func (h *harness) conversationFor(t *testing.T, owner string) string {
	t.Helper()
	conv, err := h.svc.Resolve(context.Background(), "", owner)
	if err != nil {
		t.Fatal(err)
	}
	return conv
}

// timed says something and records what it cost.
func (h *harness) timed(t *testing.T, conv, owner, text string, tm *conversation.Timing) *conversation.Turn {
	t.Helper()
	turn, err := h.svc.Record(context.Background(), conversation.Said{
		ConversationID: conv, OwnerID: owner, Role: conversation.RoleForge,
		Text: text, Timing: tm,
	})
	if err != nil {
		t.Fatal(err)
	}
	return turn
}

// The measurement survives the tab that saw it happen.
//
// # What was wrong before this
//
// Every turn was already measured and every measurement went to the log and
// nowhere else. The Telemetry panel could only show what THIS browser tab had
// watched, so it emptied on reload and knew nothing about yesterday — the one
// PRD line (NFR-05) with a real measurement behind it and no way to look at it.
func TestATurnsTimingSurvivesTheTabThatSawIt(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	me := h.person(t, "timed@example.com")
	conv := h.timed(t, h.conversationFor(t, me), me, "here it is",
		&conversation.Timing{Model: "qwen3.7-plus", FirstTokenMS: ms(735),
			TotalMS: ms(4210), RoundTripMS: ms(4290), Tokens: count(1820)}).ConversationID

	got, err := h.svc.Measured(ctx, me)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("%d measured turns, want 1 — the timing was measured and then not stored", len(got))
	}
	tm := got[0].Timing
	if tm == nil {
		t.Fatal("the turn came back with no timing at all")
	}
	if tm.Model != "qwen3.7-plus" {
		t.Errorf("model = %q; NFR-05 names model selection and this is where it is kept", tm.Model)
	}
	if tm.FirstTokenMS == nil || *tm.FirstTokenMS != 735 {
		t.Errorf("first token = %v, want 735", tm.FirstTokenMS)
	}
	if tm.TotalMS == nil || *tm.TotalMS != 4210 {
		t.Errorf("total = %v, want 4210", tm.TotalMS)
	}
	if tm.Tokens == nil || *tm.Tokens != 1820 {
		t.Errorf("tokens = %v, want 1820", tm.Tokens)
	}
	// And it is on the turn it belongs to, not floating beside it.
	said, err := h.svc.History(ctx, conv, me)
	if err != nil || len(said) != 1 || said[0].Timing == nil {
		t.Fatalf("the timing did not come back with the turn it measured: %v %+v", err, said)
	}
}

// An unmeasured turn comes back UNMEASURED, not fast.
//
// This is the property the whole design rests on. A default of 0 in the column
// would make a turn nobody timed indistinguishable from an instantaneous one,
// and the panel — whose own rule is that a missing measurement renders as an em
// dash and never as a zero — would draw the best possible number for the worst
// possible reason.
func TestAnUnmeasuredTurnIsNotAFastOne(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	me := h.person(t, "untimed@example.com")

	// A human turn, and a FORGE turn with nothing measured. Neither is timed.
	conv := h.conversationFor(t, me)
	h.say(t, conv, me, conversation.RoleHuman, "design me a bracket")
	h.say(t, conv, me, conversation.RoleForge, "here you go")

	turns, err := h.svc.History(ctx, conv, me)
	if err != nil {
		t.Fatal(err)
	}
	for _, turn := range turns {
		if turn.Timing != nil {
			t.Errorf("turn %d came back with a timing nobody measured: %+v", turn.Seq, turn.Timing)
		}
	}
	// And it is not in the measured history at all, rather than being a row of
	// em dashes that reads as a failure.
	measured, err := h.svc.Measured(ctx, me)
	if err != nil {
		t.Fatal(err)
	}
	if len(measured) != 0 {
		t.Errorf("%d measured turns, want none: an unmeasured turn is not a measurement", len(measured))
	}
}

// Timings are somebody's, like the turns they are attached to.
func TestOneAccountCannotReadAnothersTimings(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	mine := h.person(t, "mine@example.com")
	theirs := h.person(t, "theirs@example.com")

	h.timed(t, h.conversationFor(t, mine), mine, "mine",
		&conversation.Timing{Model: "m", FirstTokenMS: ms(100)})

	got, err := h.svc.Measured(ctx, theirs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("a second account read %d of somebody else's measured turns. The scope is in "+
			"the query rather than applied afterwards for exactly this reason.", len(got))
	}
}

// Deleting a conversation deletes its measurements with it.
//
// PRD AUD-07 requires deletion to always be reachable, and this is the reason
// the timings are columns on the turn rather than a table beside it: there is
// one delete path, it already exists, and there is no second sweeper to forget.
func TestForgettingAConversationTakesItsTimings(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	me := h.person(t, "forgetful@example.com")
	conv := h.timed(t, h.conversationFor(t, me), me, "here",
		&conversation.Timing{Model: "m", FirstTokenMS: ms(500)}).ConversationID

	if _, err := h.svc.Forget(ctx, conv, me); err != nil {
		t.Fatal(err)
	}
	got, err := h.svc.Measured(ctx, me)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("%d measured turns survived the deletion of the conversation they belonged to. "+
			"A record of when somebody had a conversation, after they deleted it, is the "+
			"failure AUD-07 exists to prevent.", len(got))
	}
}

// The schema refuses what the domain refuses, and the other way round.
//
// Both halves matter: a check the code enforces and the schema does not is one
// a second writer bypasses, and a check the schema enforces and the code does
// not is a constraint name where a sentence should be.
func TestTheSchemaAndTheCodeAgreeAboutTimings(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	me := h.person(t, "constrained@example.com")

	t.Run("a human turn cannot be timed", func(t *testing.T) {
		_, err := h.svc.Record(ctx, conversation.Said{
			ConversationID: h.conversationFor(t, me),
			OwnerID:        me, Role: conversation.RoleHuman, Text: "hello",
			Timing: &conversation.Timing{Model: "m", FirstTokenMS: ms(10)},
		})
		if err == nil {
			t.Fatal("a human turn was recorded with a model timing on it")
		}
		if errs.CodeOf(err) != errs.CodeValidationFailed {
			t.Errorf("code = %v; want a validation failure with a sentence in it", errs.CodeOf(err))
		}
	})

	t.Run("a negative duration is refused", func(t *testing.T) {
		_, err := h.svc.Record(ctx, conversation.Said{
			ConversationID: h.conversationFor(t, me),
			OwnerID:        me, Role: conversation.RoleForge, Text: "hi",
			Timing: &conversation.Timing{Model: "m", FirstTokenMS: ms(-1)},
		})
		if err == nil {
			t.Fatal("a negative time to first token was stored; it would drag every median " +
				"somewhere no measurement can be")
		}
	})

	// The two subtests above stop at Validate, which is where a person gets a
	// sentence — and which means neither of them has judged the SCHEMA. A check
	// only the code enforces is one the next writer bypasses, so both
	// constraints are asked directly, past the domain.
	t.Run("and the schema enforces them without the code", func(t *testing.T) {
		conv := h.conversationFor(t, me)
		for _, tc := range []struct {
			name, role string
			first      int
		}{
			{"a human turn carrying a timing", "human", 10},
			{"a negative duration", "forge", -1},
		} {
			_, err := h.pool.Exec(ctx, `
				insert into forge_conversation_turns
					(id, conversation_id, owner_id, seq, role, text, images, said_at,
					 model, first_token_ms)
				values ($1, $2, $3, 99, $4, 'x', 0, now(), 'm', $5)`,
				id.New(id.PrefixTurn), conv, me, tc.role, tc.first)
			if err == nil {
				t.Errorf("the schema accepted %s. The domain refuses it, which protects this "+
					"writer and no other one.", tc.name)
			}
		}
	})
}
