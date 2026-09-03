package agent

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// A project's character reaches the model (PRD RSN-04).
//
// # What this is really checking
//
// Not that a column can be read — that a setting somebody makes changes what
// FORGE is told. The requirement's configurable half had a complete mechanism
// and no producer for its whole life: persona.SystemPrompt has always turned
// CritiqueIntensity into an instruction, and the "low" and "high" branches were
// unreachable because Character was only ever built by DefaultCharacter().
//
// So the assertion is on the SYSTEM PROMPT the client received. A test that
// checked the store returned "high" would have passed on every version of this
// code that never plumbed it through.

// promptSpy is an llm.Client that records the system prompt and answers nothing
// useful. What the model replies is irrelevant here; what it was told is the
// whole subject.
type promptSpy struct{ system string }

func (p *promptSpy) Complete(_ context.Context, req llm.Request) (*llm.Response, error) {
	for _, m := range req.Messages {
		if m.Role == llm.System {
			p.system = m.Content
		}
	}
	return &llm.Response{Content: `{"speech":"noted","detail":""}`, FinishReason: "stop"}, nil
}

func (p *promptSpy) ModelFor(llm.Role) string { return "spy" }

func characterHarness(t *testing.T) *db.Pool {
	t.Helper()
	url := os.Getenv("FORGE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("FORGE_TEST_DATABASE_URL is unset")
	}
	ctx := context.Background()
	schema := "forge_char_test"
	cfg := func(u string) config.DBConfig {
		return config.DBConfig{URL: u, MaxConns: 4, MinConns: 1,
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
	return pool
}

// project inserts one with the given character and returns its id.
func projectWith(t *testing.T, pool *db.Pool, critique, verbosity string) string {
	t.Helper()
	ctx := context.Background()
	owner := id.New(id.PrefixUser)
	if _, err := pool.Exec(ctx, `
		insert into forge_users (id, email, display_name, status, password_hash, password_algo,
			password_changed_at, created_at, updated_at)
		values ($1,$2,'Owner','active','x','argon2id',now(),now(),now())`,
		owner, owner+"@example.com"); err != nil {
		t.Fatal(err)
	}
	projectID := id.New(id.PrefixProject)
	if _, err := pool.Exec(ctx, `
		insert into forge_projects (id, owner_id, name, critique_intensity, verbosity,
			created_at, updated_at)
		values ($1,$2,'P',$3,$4,now(),now())`,
		projectID, owner, critique, verbosity); err != nil {
		t.Fatal(err)
	}
	return projectID
}

func TestAProjectsCharacterReachesTheModel(t *testing.T) {
	pool := characterHarness(t)
	store := NewCharacterStore(pool, logx.Discard())

	// The exact sentences persona.SystemPrompt emits for each setting. Quoted
	// rather than paraphrased: if somebody rewords the prompt, this test should
	// fail and be updated deliberately, because the reworded sentence is what
	// the model will actually be told.
	const argueHard = "Challenge assumptions actively"
	const argueLittle = "Raise only significant concerns"
	const explain = "Explain your reasoning as you go"

	t.Run("high critique reaches the prompt", func(t *testing.T) {
		spy := &promptSpy{}
		conv := NewConversation(spy, persona.DefaultCharacter()).WithCharacters(store)
		p := projectWith(t, pool, "high", "explanatory")

		if _, err := conv.Respond(context.Background(), p, nil, "is this bracket strong enough?", ""); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(spy.system, argueHard) {
			t.Errorf("the project is set to critique=high and the model was not told to argue.\n"+
				"Expected the prompt to contain %q.\nThe setting exists, is stored, and does not "+
				"reach the model — which is the exact state RSN-04 was in before this.", argueHard)
		}
		if !strings.Contains(spy.system, explain) {
			t.Errorf("verbosity=explanatory did not reach the prompt either")
		}
	})

	t.Run("low critique reaches the prompt", func(t *testing.T) {
		spy := &promptSpy{}
		conv := NewConversation(spy, persona.DefaultCharacter()).WithCharacters(store)
		p := projectWith(t, pool, "low", "terse")

		if _, err := conv.Respond(context.Background(), p, nil, "is this bracket strong enough?", ""); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(spy.system, argueLittle) {
			t.Errorf("the project is set to critique=low and the prompt does not say so")
		}
		// Whatever the setting, the safety carve-out travels with it. This is the
		// half of RSN-04 that must not be configurable.
		if !strings.Contains(spy.system, "safety") {
			t.Error("critique=low removed every mention of safety from the prompt; " +
				"the requirement says safety-critical dissent cannot be disabled")
		}
	})

	t.Run("no project falls back rather than failing", func(t *testing.T) {
		spy := &promptSpy{}
		conv := NewConversation(spy, persona.DefaultCharacter()).WithCharacters(store)

		if _, err := conv.Respond(context.Background(), "", nil, "hello", ""); err != nil {
			t.Fatalf("a conversation with no project failed: %v\nThe evaluation harness has "+
				"none, and a deployment that never sets a character must still work", err)
		}
		if strings.Contains(spy.system, argueHard) || strings.Contains(spy.system, argueLittle) {
			t.Error("with no project the prompt carried a non-default critique instruction")
		}
	})

	t.Run("an unreadable project falls back rather than failing", func(t *testing.T) {
		spy := &promptSpy{}
		conv := NewConversation(spy, persona.DefaultCharacter()).WithCharacters(store)

		// A project id that does not exist. A tone setting must never be able to
		// stop FORGE answering.
		if _, err := conv.Respond(context.Background(), "prj_MISSING", nil, "hello", ""); err != nil {
			t.Fatalf("an unknown project stopped the conversation: %v", err)
		}
		if !strings.Contains(spy.system, "How I speak") {
			t.Error("the fallback did not produce a usable system prompt")
		}
	})
}
