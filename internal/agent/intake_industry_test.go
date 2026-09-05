package agent_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/agent"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// The industry reaches the project (PRD §"Domain packs").
//
// # Why these are behavioural rather than a scan for hardcoded strings
//
// Draft used to pass the CONSTANT "software" to EnsureProject, so every goal
// created anywhere — the CLI, the workbench's "Start this" — produced a project
// filed under a pack whose rules are about merging code, whatever the work
// actually was. The pack column recorded a domain nobody chose.
//
// A fence that grepped for the literal would pass the moment somebody wrote
// `const defaultPack = "software"`, which is the same defect one line further
// away. These assert what a caller can observe: the industry asked for is the
// industry in force, and an unstated one is `general` — the pack that MEANS
// unknown — rather than a guess that reads exactly like a decision.
//
// See docs/bugfix/2026-09-04-the-pack-was-written-and-never-read.md.

func industryHarness(t *testing.T) (*db.Pool, string) {
	t.Helper()
	url := os.Getenv("FORGE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("FORGE_TEST_DATABASE_URL is unset; run `make db-up` then `make test-integration`.")
	}
	ctx := context.Background()
	schema := "forge_intake_industry"
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
	owner := id.New(id.PrefixUser)
	if _, err := pool.Exec(ctx, `
		insert into forge_users (id, email, display_name, status, password_hash, password_algo,
			password_changed_at, created_at, updated_at)
		values ($1,$2,'Owner','active','x','argon2id',now(),now(),now())`,
		owner, owner+"@example.com"); err != nil {
		t.Fatal(err)
	}
	return pool, owner
}

// newIntake builds one with no model. Draft calls none — that is the property
// the workbench depends on — so a nil client is a legitimate caller here and
// this test would fail loudly if that stopped being true.
func newIntake() *agent.Intake {
	return agent.NewIntake(nil, persona.DefaultCharacter(), config.EngineConfig{}, clock.System{})
}

func packOf(t *testing.T, pool *db.Pool, projectID string) string {
	t.Helper()
	var got string
	if err := pool.QueryRow(context.Background(),
		`select pack from forge_projects where id = $1`, projectID).Scan(&got); err != nil {
		t.Fatalf("reading the project's pack: %v", err)
	}
	return got
}

// The industry a person asked for is the one the project is created in.
func TestDraft_CreatesTheProjectInTheIndustryAsked(t *testing.T) {
	pool, owner := industryHarness(t)
	ctx := context.Background()
	in := newIntake()

	for _, tc := range []struct{ given, want string }{
		{"Mechanical engineering", "mechanical"},
		{"Civil engineering", "civil"},
		{"Architecture", "architecture"},
		{"Product design", "product-design"},
		{"automotive", "automotive"},
	} {
		goal, err := in.Draft(ctx, pool, agent.DraftRequest{
			OwnerID: owner, Industry: tc.given,
			Title: "Work in " + tc.given, Statement: "Do the thing.",
			Autonomy: engine.AutonomyDraft, RiskTier: engine.RiskR1,
		})
		if err != nil {
			t.Errorf("drafting a goal in %q failed: %v", tc.given, err)
			continue
		}
		if got := packOf(t, pool, goal.ProjectID); got != tc.want {
			t.Errorf("a goal drafted in %q produced a project in pack %q, expected %q",
				tc.given, got, tc.want)
		}
	}
}

// An unstated industry is `general`, which MEANS unknown — never "software".
func TestDraft_UnstatedIndustryIsGeneralNotAGuess(t *testing.T) {
	pool, owner := industryHarness(t)
	ctx := context.Background()

	goal, err := newIntake().Draft(ctx, pool, agent.DraftRequest{
		OwnerID: owner,
		// The statement is deliberately about a bracket. Under the old constant
		// this produced a project filed as `software`, and anything that starts
		// inferring a domain here must not quietly reintroduce that: a guess is
		// indistinguishable from a decision once it is in the column.
		Title: "Motor bracket", Statement: "Design a bracket to hold a small stepper motor.",
		Autonomy: engine.AutonomyDraft, RiskTier: engine.RiskR1,
	})
	if err != nil {
		t.Fatalf("drafting a goal with no industry failed: %v", err)
	}
	got := packOf(t, pool, goal.ProjectID)
	if got == "software" {
		t.Fatal("a goal about a bracket was filed under the `software` pack.\n" +
			"That is the defect this fence exists for: the pack recorded a domain " +
			"nobody chose, and its rules are about merging code")
	}
	if got != "general" {
		t.Errorf("an unstated industry produced pack %q; expected `general`, the pack whose "+
			"definition IS an unknown domain. Anything else is a guess wearing a decision's "+
			"clothes", got)
	}
}

// Asking for an industry on an EXISTING project is refused, not dropped.
//
// EnsureProject returns early on a known project id, so the value would
// otherwise vanish silently and the caller would believe they had set it.
func TestDraft_RefusesAnIndustryOnAnExistingProject(t *testing.T) {
	pool, owner := industryHarness(t)
	ctx := context.Background()
	in := newIntake()

	first, err := in.Draft(ctx, pool, agent.DraftRequest{
		OwnerID: owner, Industry: "Civil engineering",
		Title: "Bridge study", Statement: "Look at the span options.",
		Autonomy: engine.AutonomyDraft, RiskTier: engine.RiskR1,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = in.Draft(ctx, pool, agent.DraftRequest{
		OwnerID: owner, ProjectID: first.ProjectID, Industry: "Architecture",
		Title: "Second goal", Statement: "Something else.",
		Autonomy: engine.AutonomyDraft, RiskTier: engine.RiskR1,
	})
	if err == nil {
		t.Fatal("an industry was accepted alongside an existing project id and silently ignored.\n" +
			"The caller believes they set the domain the work is done under; they did not")
	}
	if errs.CodeOf(err) != errs.CodeValidationFailed {
		t.Errorf("refused with %s; a meaningless combination is bad input", errs.CodeOf(err))
	}
	// The project it was already in must be untouched by the refusal.
	if got := packOf(t, pool, first.ProjectID); got != "civil" {
		t.Errorf("the refused call changed the project's pack to %q", got)
	}
	// And adding a goal to it WITHOUT an industry still works, or the refusal
	// above would have made existing projects unusable.
	if _, err := in.Draft(ctx, pool, agent.DraftRequest{
		OwnerID: owner, ProjectID: first.ProjectID,
		Title: "Third goal", Statement: "Something else again.",
		Autonomy: engine.AutonomyDraft, RiskTier: engine.RiskR1,
	}); err != nil {
		t.Errorf("adding a goal to an existing project without an industry failed: %v", err)
	}
}

// An industry this build does not know is refused, and the refusal lists them.
func TestDraft_RefusesAnUnknownIndustry(t *testing.T) {
	pool, owner := industryHarness(t)

	_, err := newIntake().Draft(context.Background(), pool, agent.DraftRequest{
		OwnerID: owner, Industry: "Cryptocurrency",
		Title: "Whatever", Statement: "Do the thing.",
		Autonomy: engine.AutonomyDraft, RiskTier: engine.RiskR1,
	})
	if err == nil {
		t.Fatal("a project was created in an industry this build has never heard of")
	}
	if !strings.Contains(err.Error(), "mechanical") {
		t.Errorf("the refusal does not say what IS available: %v", err)
	}
}
