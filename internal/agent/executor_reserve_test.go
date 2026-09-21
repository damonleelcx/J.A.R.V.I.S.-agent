package agent_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
)

// toolingModel asks for a tool on every call and charges 12,000 tokens a call: a task
// that would go on calling the model for as many iterations as it is allowed.
type toolingModel struct {
	mu sync.Mutex
	n  int
}

func (m *toolingModel) Complete(context.Context, llm.Request) (*llm.Response, error) {
	m.mu.Lock()
	m.n++
	n := m.n
	m.mu.Unlock()
	return &llm.Response{FinishReason: "tool_calls", Usage: llm.Usage{TotalTokens: 12_000},
		ToolCalls: []llm.ToolCall{{ID: fmt.Sprintf("call-%d", n), Type: "function",
			Function: llm.FunctionCall{Name: "read_file", Arguments: `{"path":"notes.txt"}`}}}}, nil
}

func (m *toolingModel) ModelFor(llm.Role) string { return "stub" }

func (m *toolingModel) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.n
}

// ‼️ An ordinary (non-build) goal's executor stops before a call its ceiling cannot pay
// for, as a build's calls do (2026-09-17, follow-up to the live re-check).
//
// The ceiling is 36,000 and every call costs 12,000. The executor called its model
// directly and the worker checked the ceiling only before the task started, so this task
// used to call until its iteration limit. Now the third call may cost 15,000 (the
// largest, 12,000, and a quarter) with 12,000 left, and is not placed.
//
// In thousands because a goal's first call reserves engine.FirstCallReserve rather than
// nothing (2026-09-20): a 300-token ceiling cannot pay for any call this system makes.
func TestExecutor_AnOrdinaryGoalStopsBeforeACallThatWouldPassItsCeiling(t *testing.T) {
	skipWithoutDatabase(t)
	h := newGateHarness(t)
	goal := h.createGoal(t, "Reserve", "an ordinary goal", engine.AutonomySandboxExecute, engine.RiskR1)
	h.seedChain(t, goal)
	if _, err := h.pool.Exec(context.Background(), `update forge_goals set max_tokens = 36000 where id = $1`, goal.ID); err != nil {
		t.Fatal(err)
	}
	model := &toolingModel{}

	status := h.runUntilSettled(t, h.stubWorker(model, time.Hour), goal.ID)

	if status != string(engine.GoalFailed) {
		t.Fatalf("a goal stopped by its budget ended %s", status)
	}
	var spent, largest int64
	if err := h.pool.QueryRow(context.Background(), `select tokens_spent, largest_call_tokens from forge_goals where id = $1`,
		goal.ID).Scan(&spent, &largest); err != nil {
		t.Fatal(err)
	}
	if spent > 36_000 || spent != 24_000 || largest != 12_000 || model.calls() != 2 {
		t.Fatalf("%d call(s), %d of 36000 spent, largest %d; want 2 calls and 24000 spent",
			model.calls(), spent, largest)
	}
	a, _ := h.chainTasks(t, goal.ID)
	why := "12000 tokens were left, and the next model call was not placed because it may cost 15000 " +
		"(the largest call this goal has made, 12000 tokens, and a quarter more), which would pass the ceiling. " +
		"The goal stops here rather than spend past it."
	if a.Status != engine.StatusFailed || !strings.Contains(a.ErrorDetail, "goal budget exhausted on tokens: used 24000 tokens of 36000. "+why) {
		t.Errorf("the task ended %s with %q", a.Status, a.ErrorDetail)
	}
	var said string
	if err := h.pool.QueryRow(context.Background(), `select summary from forge_events where goal_id = $1 and kind = $2`,
		goal.ID, engine.EventBudgetExceeded).Scan(&said); err != nil {
		t.Fatalf("the timeline does not say the budget stopped the task part-way: %v", err)
	}
	if said != "Budget exhausted on tokens: used 24000 tokens of 36000. "+why {
		t.Errorf("the timeline says %q", said)
	}
}
