package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
)

func testDefaults() config.EngineConfig {
	return config.EngineConfig{
		MaxTokensPerGoal:         1000,
		MaxCostCentsPerGoal:      500,
		MaxWallClockPerGoal:      24 * time.Hour,
		MaxTasksPerGoal:          50,
		MaxTaskDepth:             3,
		MaxIterationsPerTask:     10,
		MaxToolCallsPerIteration: 5,
	}
}

func activeGoal(started time.Time) *Goal {
	return &Goal{ID: "gol_test", Status: GoalActive, StartedAt: &started}
}

// TestEveryAxisIsEnforced is the fence behind "seven limits, not one".
//
// An agent runs away along independent axes: it can burn tokens without cost
// (a cached prefix), cost without wall clock (one expensive call), wall clock
// without tokens (waiting on a tool), and create thousands of tasks while
// spending almost nothing. A bound on one axis constrains none of the others,
// so this walks every axis and requires each to stop the agent on its own.
func TestEveryAxisIsEnforced(t *testing.T) {
	g := NewBudgetGuard(testDefaults())
	started := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	now := started.Add(time.Hour)

	tripped := map[LimitKind]bool{}

	// tokens
	goal := activeGoal(started)
	goal.Spend.Tokens = 1000
	if b := g.CheckGoal(goal, now); b == nil {
		t.Error("the token ceiling did not trip")
	} else {
		tripped[b.Kind] = true
	}

	// cost, with tokens well clear
	goal = activeGoal(started)
	goal.Spend.CostCents = 500
	if b := g.CheckGoal(goal, now); b == nil {
		t.Error("the cost ceiling did not trip")
	} else {
		tripped[b.Kind] = true
	}

	// wall clock, with nothing else spent
	goal = activeGoal(started)
	if b := g.CheckGoal(goal, started.Add(25*time.Hour)); b == nil {
		t.Error("the wall-clock ceiling did not trip")
	} else {
		tripped[b.Kind] = true
	}

	// task count
	goal = activeGoal(started)
	goal.Spend.TasksCreated = 50
	if b := g.CheckGoal(goal, now); b == nil {
		t.Error("the task-count ceiling did not trip")
	} else {
		tripped[b.Kind] = true
	}

	// depth, at zero task count — depth and breadth are different runaways
	goal = activeGoal(started)
	if b := g.CheckTaskCreation(goal, 3); b == nil {
		t.Error("the depth ceiling did not trip")
	} else {
		tripped[b.Kind] = true
	}

	// iterations
	if b := g.CheckIteration(10); b == nil {
		t.Error("the iteration ceiling did not trip")
	} else {
		tripped[b.Kind] = true
	}

	// tool calls per iteration
	if b := g.CheckToolCalls(5); b == nil {
		t.Error("the tool-call ceiling did not trip")
	} else {
		tripped[b.Kind] = true
	}

	// attempts
	if b := g.CheckAttempts(&Task{AttemptCount: 3, MaxAttempts: 3}); b == nil {
		t.Error("the attempt ceiling did not trip")
	} else {
		tripped[b.Kind] = true
	}

	for _, kind := range AllLimitKinds() {
		if !tripped[kind] {
			t.Errorf("axis %q is declared but nothing in this test made it trip; "+
				"either it is unenforced or the test does not cover it", kind)
		}
	}
}

// TestAxesAreIndependent is the point of having several. A goal at zero on every
// other axis must still be stopped by the one it has exhausted.
func TestAxesAreIndependent(t *testing.T) {
	g := NewBudgetGuard(testDefaults())
	started := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)

	// Enormous token spend, but the goal has created no tasks and used no time.
	goal := activeGoal(started)
	goal.Spend.Tokens = 1_000_000
	b := g.CheckGoal(goal, started.Add(time.Minute))
	if b == nil || b.Kind != LimitTokens {
		t.Fatalf("expected the token axis to trip alone, got %v", b)
	}

	// Thousands of tasks, but almost no tokens.
	goal = activeGoal(started)
	goal.Spend.TasksCreated = 5000
	goal.Spend.Tokens = 12
	b = g.CheckGoal(goal, started.Add(time.Minute))
	if b == nil || b.Kind != LimitTaskCount {
		t.Fatalf("expected the task-count axis to trip alone, got %v", b)
	}
}

// TestUnderBudgetIsAllowed guards against a guard that refuses everything, which
// would pass every "does it stop?" test and ship an agent that never starts.
func TestUnderBudgetIsAllowed(t *testing.T) {
	g := NewBudgetGuard(testDefaults())
	started := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)

	goal := activeGoal(started)
	goal.Spend = Spend{Tokens: 999, CostCents: 499, TasksCreated: 49}
	if b := g.CheckGoal(goal, started.Add(23*time.Hour)); b != nil {
		t.Errorf("a goal one unit under every ceiling was refused: %v", b.Kind)
	}
	if b := g.CheckTaskCreation(goal, 2); b != nil {
		t.Errorf("task creation one level under the depth ceiling was refused: %v", b.Kind)
	}
	if b := g.CheckIteration(9); b != nil {
		t.Error("iteration 9 of 10 was refused")
	}
	if b := g.CheckToolCalls(4); b != nil {
		t.Error("tool call 4 of 5 was refused")
	}
	if b := g.CheckAttempts(&Task{AttemptCount: 2, MaxAttempts: 3}); b != nil {
		t.Error("attempt 3 of 3 was refused before it was made")
	}
}

// TestGoalOverridesInheritWhenUnset — a nil override must mean "inherit", so a
// deployment-wide change reaches goals that never set their own ceiling.
func TestGoalOverridesInheritWhenUnset(t *testing.T) {
	g := NewBudgetGuard(testDefaults())
	started := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)

	goal := activeGoal(started)
	goal.Spend.Tokens = 1000 // exactly the process default
	if b := g.CheckGoal(goal, started.Add(time.Minute)); b == nil {
		t.Fatal("a goal with no override did not inherit the process ceiling")
	}

	// An explicit, larger override must be honoured.
	larger := int64(5000)
	goal.Budget.MaxTokens = &larger
	if b := g.CheckGoal(goal, started.Add(time.Minute)); b != nil {
		t.Errorf("an explicit larger ceiling was ignored: %v", b.Kind)
	}

	// And a smaller one.
	smaller := int64(100)
	goal.Budget.MaxTokens = &smaller
	if b := g.CheckGoal(goal, started.Add(time.Minute)); b == nil || b.Kind != LimitTokens {
		t.Error("an explicit smaller ceiling was ignored")
	}
}

// TestZeroCeilingMeansUnlimited documents the sentinel, because the alternative
// reading — "zero means nothing is allowed" — would deadlock every goal.
func TestZeroCeilingMeansUnlimited(t *testing.T) {
	g := NewBudgetGuard(config.EngineConfig{})
	started := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)

	goal := activeGoal(started)
	goal.Spend = Spend{Tokens: 1 << 40, CostCents: 1 << 40, TasksCreated: 1 << 20}
	if b := g.CheckGoal(goal, started.Add(1000*time.Hour)); b != nil {
		t.Errorf("a zero ceiling refused work; zero must mean unlimited, or an unset config deadlocks every goal (%v)", b.Kind)
	}
}

// TestWallClockNeedsAStart — a goal that has not started has no elapsed time,
// and must not be judged against a wall-clock ceiling.
func TestWallClockNeedsAStart(t *testing.T) {
	g := NewBudgetGuard(testDefaults())
	goal := &Goal{ID: "gol_x", Status: GoalDraft} // StartedAt nil
	if b := g.CheckGoal(goal, time.Now().Add(1000*time.Hour)); b != nil {
		t.Errorf("a goal that never started was judged against a wall-clock ceiling: %v", b.Kind)
	}
}

// TestBreachesAreActionable — a ceiling that stops the agent must say what to do,
// and the right action differs per axis. A generic "over budget" leaves an
// operator guessing which of seven things went wrong.
func TestBreachesAreActionable(t *testing.T) {
	g := NewBudgetGuard(testDefaults())
	started := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)

	breaches := []*LimitBreach{}
	goal := activeGoal(started)
	goal.Spend.Tokens = 1000
	breaches = append(breaches, g.CheckGoal(goal, started.Add(time.Minute)))
	breaches = append(breaches, g.CheckTaskCreation(activeGoal(started), 3))
	breaches = append(breaches, g.CheckIteration(10))
	breaches = append(breaches, g.CheckToolCalls(5))
	breaches = append(breaches, g.CheckAttempts(&Task{AttemptCount: 3, MaxAttempts: 3}))

	seen := map[string]bool{}
	for _, b := range breaches {
		if b == nil {
			t.Fatal("expected a breach")
		}
		if b.Remedy == "" {
			t.Errorf("%s: no remedy", b.Kind)
		}
		if b.Used == "" || b.Limit == "" {
			t.Errorf("%s: does not report how far over it went", b.Kind)
		}
		if seen[b.Remedy] {
			t.Errorf("%s reuses another axis's remedy; the right action differs per axis", b.Kind)
		}
		seen[b.Remedy] = true

		err := b.Error()
		if !strings.Contains(err.Error(), string(b.Kind)) {
			t.Errorf("%s: the error does not name the axis: %v", b.Kind, err)
		}
	}
}
