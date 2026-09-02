package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// LimitKind names which ceiling was reached.
//
// An agent can run away along seven independent axes, and a bound on one is not
// a bound. They are enumerated rather than collapsed into "over budget" because
// the operator's response differs: a token ceiling means raise the budget or
// narrow the goal; a depth ceiling means the planner is decomposing in circles;
// a wall-clock ceiling means something is waiting on a thing that will never
// arrive.
type LimitKind string

const (
	LimitTokens     LimitKind = "tokens"
	LimitCost       LimitKind = "cost"
	LimitWallClock  LimitKind = "wall_clock"
	LimitTaskCount  LimitKind = "task_count"
	LimitTaskDepth  LimitKind = "task_depth"
	LimitIterations LimitKind = "iterations_per_task"
	LimitToolCalls  LimitKind = "tool_calls_per_iteration"
	LimitAttempts   LimitKind = "attempts_per_task"
)

// AllLimitKinds returns every axis, for the fence that keeps them enumerated.
func AllLimitKinds() []LimitKind {
	return []LimitKind{
		LimitTokens, LimitCost, LimitWallClock, LimitTaskCount,
		LimitTaskDepth, LimitIterations, LimitToolCalls, LimitAttempts,
	}
}

// LimitBreach describes a ceiling that has been reached.
type LimitBreach struct {
	Kind LimitKind
	// Used and Limit are rendered into the message, so an operator does not have
	// to query the database to find out how far over the goal went.
	Used  string
	Limit string
	// Remedy is specific to the axis rather than generic, because the right
	// action differs per axis.
	Remedy string
}

// Error renders a breach as a typed error.
func (b LimitBreach) Error() *errs.Error {
	return errs.New("engine.Budget", errs.CodeForbidden).
		WithDetail("goal budget exhausted on %s: used %s of %s. %s", b.Kind, b.Used, b.Limit, b.Remedy).
		WithField("limit_kind", string(b.Kind))
}

// BudgetGuard decides whether a goal may keep spending.
//
// # Why this is checked rather than merely configured
//
// Configuring a ceiling and never consulting it is the most common way a
// "bounded" agent turns out to be unbounded. The guard is called before every
// model call and every task creation, and it reads the goal's *persisted*
// counters rather than an in-process tally — so a restart cannot reset the
// budget, and two workers cannot each spend the full allowance.
type BudgetGuard struct {
	defaults config.EngineConfig
}

// NewBudgetGuard returns a guard applying process defaults where a goal has not
// overridden them.
func NewBudgetGuard(defaults config.EngineConfig) *BudgetGuard {
	return &BudgetGuard{defaults: defaults}
}

// effective resolves a goal's ceiling against the process default.
//
// A nil override means "inherit", so a deployment-wide change reaches goals that
// never set their own. Note the asymmetry: a goal may lower a ceiling but the
// resolution deliberately does not stop it from raising one — that is a policy
// decision for whoever creates the goal, and clamping silently here would make
// a configured value untrue.
func effectiveInt64(override *int64, def int64) int64 {
	if override != nil {
		return *override
	}
	return def
}

func effectiveInt(override *int, def int) int {
	if override != nil {
		return *override
	}
	return def
}

func effectiveDuration(override *time.Duration, def time.Duration) time.Duration {
	if override != nil {
		return *override
	}
	return def
}

// CheckGoal reports the first ceiling a goal has reached, or nil.
//
// Returns the FIRST breach rather than all of them: the goal stops either way,
// and reporting one specific cause is more actionable than a list.
func (g *BudgetGuard) CheckGoal(goal *Goal, now time.Time) *LimitBreach {
	maxTokens := effectiveInt64(goal.Budget.MaxTokens, g.defaults.MaxTokensPerGoal)
	if maxTokens > 0 && goal.Spend.Tokens >= maxTokens {
		return &LimitBreach{
			Kind:   LimitTokens,
			Used:   fmt.Sprintf("%d tokens", goal.Spend.Tokens),
			Limit:  fmt.Sprintf("%d", maxTokens),
			Remedy: "Raise FORGE_MAX_TOKENS_PER_GOAL or the goal's own ceiling, or narrow the goal so it needs less context.",
		}
	}

	maxCost := effectiveInt64(goal.Budget.MaxCostCents, g.defaults.MaxCostCentsPerGoal)
	if maxCost > 0 && goal.Spend.CostCents >= maxCost {
		return &LimitBreach{
			Kind:   LimitCost,
			Used:   fmt.Sprintf("%d cents", goal.Spend.CostCents),
			Limit:  fmt.Sprintf("%d cents", maxCost),
			Remedy: "Raise the cost ceiling, or route cheaper roles to a smaller model.",
		}
	}

	maxWall := effectiveDuration(goal.Budget.MaxWallClock, g.defaults.MaxWallClockPerGoal)
	if maxWall > 0 && goal.StartedAt != nil {
		elapsed := now.Sub(*goal.StartedAt)
		if elapsed >= maxWall {
			return &LimitBreach{
				Kind:   LimitWallClock,
				Used:   elapsed.Round(time.Minute).String(),
				Limit:  maxWall.String(),
				Remedy: "A goal that has run this long is usually waiting on something that will not arrive. Check the timeline for the last state change before raising the ceiling.",
			}
		}
	}

	maxTasks := effectiveInt(goal.Budget.MaxTasks, g.defaults.MaxTasksPerGoal)
	if maxTasks > 0 && goal.Spend.TasksCreated >= maxTasks {
		return &LimitBreach{
			Kind:   LimitTaskCount,
			Used:   fmt.Sprintf("%d tasks", goal.Spend.TasksCreated),
			Limit:  fmt.Sprintf("%d", maxTasks),
			Remedy: "Breadth-first decomposition has exploded. Inspect the plan before raising this: a goal needing hundreds of tasks is usually several goals.",
		}
	}
	return nil
}

// CheckTaskCreation reports whether a new task may be created at the given depth.
//
// Depth and count are checked separately because they catch different runaways:
// recursive decomposition goes deep, and breadth-first explosion goes wide. A
// limit on one does not constrain the other at all.
func (g *BudgetGuard) CheckTaskCreation(goal *Goal, depth int) *LimitBreach {
	maxDepth := g.defaults.MaxTaskDepth
	if maxDepth > 0 && depth >= maxDepth {
		return &LimitBreach{
			Kind:   LimitTaskDepth,
			Used:   fmt.Sprintf("depth %d", depth),
			Limit:  fmt.Sprintf("%d", maxDepth),
			Remedy: "The planner is decomposing in circles. Read the task chain at this depth before raising the limit — it usually repeats itself.",
		}
	}
	maxTasks := effectiveInt(goal.Budget.MaxTasks, g.defaults.MaxTasksPerGoal)
	if maxTasks > 0 && goal.Spend.TasksCreated >= maxTasks {
		return &LimitBreach{
			Kind:   LimitTaskCount,
			Used:   fmt.Sprintf("%d tasks", goal.Spend.TasksCreated),
			Limit:  fmt.Sprintf("%d", maxTasks),
			Remedy: "Breadth-first decomposition has exhausted the task ceiling for this goal.",
		}
	}
	return nil
}

// CheckIteration reports whether a task may run another observe/plan/execute
// cycle.
func (g *BudgetGuard) CheckIteration(iteration int) *LimitBreach {
	max := g.defaults.MaxIterationsPerTask
	if max > 0 && iteration >= max {
		return &LimitBreach{
			Kind:   LimitIterations,
			Used:   fmt.Sprintf("%d iterations", iteration),
			Limit:  fmt.Sprintf("%d", max),
			Remedy: "A task looping this many times is not converging. Read its checkpoints: it is usually repeating one failing tool call.",
		}
	}
	return nil
}

// CheckToolCalls reports whether another tool call may be made this iteration.
func (g *BudgetGuard) CheckToolCalls(callsThisIteration int) *LimitBreach {
	max := g.defaults.MaxToolCallsPerIteration
	if max > 0 && callsThisIteration >= max {
		return &LimitBreach{
			Kind:   LimitToolCalls,
			Used:   fmt.Sprintf("%d calls", callsThisIteration),
			Limit:  fmt.Sprintf("%d", max),
			Remedy: "One iteration fanned out further than expected. Check whether the task should have been decomposed instead.",
		}
	}
	return nil
}

// CheckAttempts reports whether a task has retries left.
func (g *BudgetGuard) CheckAttempts(task *Task) *LimitBreach {
	if task.AttemptCount >= task.MaxAttempts {
		return &LimitBreach{
			Kind:   LimitAttempts,
			Used:   fmt.Sprintf("%d attempts", task.AttemptCount),
			Limit:  fmt.Sprintf("%d", task.MaxAttempts),
			Remedy: "The task failed every attempt. Read the last error before retrying it by hand — retrying an unretryable failure just spends the budget.",
		}
	}
	return nil
}

// RecordSpend adds usage to a goal's persisted counters.
//
// # Why this is an atomic increment rather than a read-modify-write
//
// Several workers spend against one goal concurrently. Reading the total,
// adding, and writing it back would lose every update that interleaved — and
// lost spend means the ceiling is silently higher than configured, which is the
// exact failure a budget exists to prevent. `set x = x + $n` is resolved by the
// database under a row lock.
func (g *BudgetGuard) RecordSpend(ctx context.Context, ex db.Querier, goalID string, tokens, costCents int64) error {
	const op = "engine.BudgetGuard.RecordSpend"

	if tokens < 0 || costCents < 0 {
		return errs.New(op, errs.CodeInvariantViolated).
			WithDetail("spend must not be negative (tokens=%d cost=%d); a refund path would let a goal spend forever", tokens, costCents)
	}
	if tokens == 0 && costCents == 0 {
		return nil
	}
	tag, err := ex.Exec(ctx, `
		update forge_goals
		   set tokens_spent = tokens_spent + $2,
		       cost_cents_spent = cost_cents_spent + $3
		 where id = $1`, goalID, tokens, costCents)
	if err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	if tag.RowsAffected() == 0 {
		return errs.New(op, errs.CodeNotFound).WithDetail("no goal %s", goalID)
	}
	return nil
}

// RecordTaskCreated increments the task counter atomically, for the same reason.
func (g *BudgetGuard) RecordTaskCreated(ctx context.Context, ex db.Querier, goalID string, n int) error {
	const op = "engine.BudgetGuard.RecordTaskCreated"

	if n <= 0 {
		return nil
	}
	if _, err := ex.Exec(ctx,
		`update forge_goals set tasks_created = tasks_created + $2 where id = $1`, goalID, n); err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	return nil
}
