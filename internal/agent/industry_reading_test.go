package agent_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/agent"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
)

// The planner's reading of the domain PROPOSES; it never applies.
//
// # What these hold
//
// The industry selects the rule set a project is worked under, and reading the
// pack existed to stop a constant deciding it for everybody. A guessed industry
// that BECAME the rules would read in the record exactly like a stated one — the
// same defect wearing a different hat.
//
// So the suggestion is written into the project graph and changes nothing. The
// load-bearing fence is the pack assertion: if a later change makes the
// suggestion authoritative "because it is usually right", it goes red.
//
// # Why a stub model rather than calling the writer directly
//
// The writer is unexported and the path that matters is Plan() — prompt, reply,
// parse, record. A test that called the writer would prove the writer works
// while the planner quietly stopped asking for a suggestion at all.

// suggestingPlanner returns a planner reply carrying an industry suggestion and
// a clarification, so no task DAG is needed to reach the recording step.
type suggestingPlanner struct{ industry string }

func (s *suggestingPlanner) Complete(_ context.Context, _ llm.Request) (*llm.Response, error) {
	body, _ := json.Marshal(map[string]any{
		"rationale":            "not planned; this run exists to exercise the domain reading",
		"clarification_needed": "What load case should this be sized for?",
		"suggested_industry":   s.industry,
		"tasks":                []any{},
	})
	return &llm.Response{Content: string(body), FinishReason: "stop"}, nil
}

func (s *suggestingPlanner) ModelFor(llm.Role) string { return "stub-planner" }

func planWithSuggestion(t *testing.T, pool *db.Pool, owner, industry, suggests string) *engine.Goal {
	t.Helper()
	in := agent.NewIntake(&suggestingPlanner{industry: suggests},
		persona.DefaultCharacter(), config.EngineConfig{}, clock.System{})
	goal, err := in.Draft(context.Background(), pool, agent.DraftRequest{
		OwnerID: owner, Industry: industry,
		Title:     "Beam sizing",
		Statement: "Give me a starting size for a simply supported steel beam spanning 6 m.",
		Autonomy:  engine.AutonomyDraft, RiskTier: engine.RiskR1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := in.Plan(context.Background(), pool, goal); err != nil {
		t.Fatalf("planning: %v", err)
	}
	return goal
}

// assumptionsIn returns the assumption nodes on a project, title and body joined.
func assumptionsIn(t *testing.T, pool *db.Pool, projectID string) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`select title, coalesce(body,'') from forge_nodes
		  where project_id = $1 and kind = 'assumption'`, projectID)
	if err != nil {
		t.Fatalf("reading assumptions: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var title, body string
		if err := rows.Scan(&title, &body); err != nil {
			t.Fatal(err)
		}
		out = append(out, title+"\n"+body)
	}
	return out
}

// The reading is recorded where a person will see it, and the pack is untouched.
func TestIndustryReading_IsRecordedAndChangesNothing(t *testing.T) {
	pool, owner := industryHarness(t)

	goal := planWithSuggestion(t, pool, owner, "", "civil")

	if after := packOf(t, pool, goal.ProjectID); after != "general" {
		t.Errorf("the planner's reading CHANGED the project's pack to %q.\n"+
			"A guessed domain that becomes the rule set reads in the record exactly like "+
			"a stated one, which is the defect this whole area removed", after)
	}
	notes := assumptionsIn(t, pool, goal.ProjectID)
	if len(notes) == 0 {
		t.Fatal("nothing was recorded, so the reading exists only in a log nobody reads")
	}
	joined := strings.Join(notes, "\n")
	for _, want := range []string{"Civil engineering", "project industry", "--set civil"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the note does not mention %q, so a person cannot act on it:\n%s", want, joined)
		}
	}
}

// A project that already states its domain gets no note.
func TestIndustryReading_IsSilentWhenTheDomainIsStated(t *testing.T) {
	pool, owner := industryHarness(t)

	goal := planWithSuggestion(t, pool, owner, "Civil engineering", "architecture")

	if n := assumptionsIn(t, pool, goal.ProjectID); len(n) != 0 {
		t.Errorf("a project already working in civil was told what the planner thought:\n%v", n)
	}
	if got := packOf(t, pool, goal.ProjectID); got != "civil" {
		t.Errorf("the stated domain was changed to %q", got)
	}
}

// A suggestion outside the industries the selector offers is dropped.
//
// `software`, `medical` and `robotics` are real packs and are NOT industries the
// product offers; a note recommending one would point at a domain no selector
// can reach, and for two of them at a domain no project may be created in.
func TestIndustryReading_RefusesADomainTheSelectorDoesNotOffer(t *testing.T) {
	pool, owner := industryHarness(t)

	for _, notOffered := range []string{"biotech", "medical", "robotics", "software", ""} {
		goal := planWithSuggestion(t, pool, owner, "", notOffered)
		if n := assumptionsIn(t, pool, goal.ProjectID); len(n) != 0 {
			t.Errorf("%q was recorded as a suggestion:\n%v", notOffered, n)
		}
		if got := packOf(t, pool, goal.ProjectID); got != "general" {
			t.Errorf("%q changed the pack to %q", notOffered, got)
		}
	}
}
