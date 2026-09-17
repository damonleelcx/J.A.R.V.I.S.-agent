package agent_test

import (
	"context"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/agent"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/tools"
)

// Every model call a goal makes is charged to it: the planner's and the verifier's too.
//
// Found by running an r2 goal through the real forged and forge-worker against a
// stand-in model that logs what it served (docs/spikes/2026-09-17-unverified-paths):
// the stand-in served 450 tokens of planning, two executor calls of 600 and a verifier
// call of 500, and the goal recorded 1,200 — the executor's alone. A build goal charges
// its planning (planBuildGoal); an ordinary goal did not, and no goal charged its
// verifier, so a goal's ceiling (FORGE_MAX_TOKENS_PER_GOAL, or its own max_tokens)
// never saw either. docs/bugfix/2026-09-17-a-goals-planner-and-verifier-calls-were-never-charged.md

// roleModel answers by role, charging each role its own number of tokens so a total
// says which calls were counted.
type roleModel struct{}

const (
	planTokens     = 450
	executeTokens  = 10
	verifierTokens = 7
)

func (roleModel) Complete(_ context.Context, r llm.Request) (*llm.Response, error) {
	switch r.Role {
	case llm.RolePlanner:
		return &llm.Response{FinishReason: "stop", Usage: llm.Usage{TotalTokens: planTokens},
			Content: `{"rationale":"one task","clarification_needed":"","tasks":[{"key":"only","title":"Only",
				"instruction":"do it","inputs":{},"expected_output":{},"depends_on":[],"risk_tier":"r1"}]}`}, nil
	case llm.RoleVerifier:
		return &llm.Response{FinishReason: "stop", Usage: llm.Usage{TotalTokens: verifierTokens},
			Content: `{"verified":true,"confidence":"high","reasoning":"the evidence supports the summary",
				"unsupported_claims":[],"missing_checks":[],"recommendation":"accept"}`}, nil
	}
	return answered(completedAnswer), nil // executeTokens
}

func (roleModel) ModelFor(llm.Role) string { return "stub" }

func TestIntake_AnOrdinaryGoalsPlanningIsChargedToTheGoal(t *testing.T) {
	skipWithoutDatabase(t)
	h := newGateHarness(t)
	goal := h.createGoal(t, "Charged planning", "plan one task", engine.AutonomySandboxExecute, engine.RiskR1)

	out, err := agent.NewIntake(roleModel{}, persona.DefaultCharacter(), h.cfg, clock.System{}).
		Plan(context.Background(), h.pool, goal)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Tasks) != 1 {
		t.Fatalf("planned %d tasks, want the stub's 1", len(out.Tasks))
	}
	if spent := h.count(t, `select tokens_spent::int from forge_goals where id = $1`, goal.ID); spent != planTokens {
		t.Fatalf("the goal has %d tokens spent after planning; the planner's call spent %d, and a goal's "+
			"ceiling that never sees its planning is not the goal's spend", spent, planTokens)
	}
}

func TestWorker_TheVerifiersCallIsChargedToTheGoal(t *testing.T) {
	skipWithoutDatabase(t)
	h := newGateHarness(t)
	goal := h.createGoal(t, "Charged verification", "two verified tasks", engine.AutonomySandboxExecute, engine.RiskR2)
	h.seedChain(t, goal)
	// r2: verified before it may succeed. Not gated, so no approval is needed to reach the verifier.
	if _, err := h.pool.Exec(context.Background(),
		`update forge_tasks set risk_tier = 'r2', requires_approval = false where goal_id = $1`, goal.ID); err != nil {
		t.Fatal(err)
	}

	model := roleModel{}
	exec := agent.NewExecutor(model, tools.NewRegistry(), h.repo, h.budget, persona.DefaultCharacter(),
		clock.System{}, h.log, h.pool)
	w := agent.NewWorker(agent.WorkerDeps{
		Pool: h.pool, Repo: h.repo, Queue: h.queue, Budget: h.budget,
		Assembler: h.assembler, Executor: exec, Verifier: agent.NewVerifier(model, persona.DefaultCharacter()),
		Config: h.cfg, WorkspaceRoot: h.workspaceRoot, Clock: clock.System{}, Log: h.log,
	})
	if status := h.runUntilSettled(t, w, goal.ID); status != string(engine.GoalSucceeded) {
		t.Fatalf("the goal settled %s, want succeeded", status)
	}
	if verified := h.count(t, `select count(*)::int from forge_tasks where goal_id = $1 and verified_at is not null`,
		goal.ID); verified != 2 {
		t.Fatalf("%d of 2 tasks were verified; the fence needs the verifier to have been asked for both", verified)
	}
	want := 2 * (executeTokens + verifierTokens)
	if spent := h.count(t, `select tokens_spent::int from forge_goals where id = $1`, goal.ID); spent != want {
		t.Fatalf("the goal has %d tokens spent; two executor calls of %d and two verifier calls of %d are %d. "+
			"%d is the executor's alone: the verifier's calls were not charged", spent, executeTokens, verifierTokens,
			want, 2*executeTokens)
	}
}
