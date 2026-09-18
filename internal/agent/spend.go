package agent

import (
	"context"
	"sync"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// A build's model calls, charged to the goal they are made for (Phase 2, stage A1).
//
// # The problem this solves
//
// The executor records spend after each of its own calls, and nothing else in
// the agent did. A build step is not one call: it is the step, then a repair
// for a fault, a look at the picture, a repair for what the look saw, a script
// check — each of them a call to the model. Run as a goal, a build whose repairs
// were free would pass its budget without the budget ever having seen it.
//
// So a build's Conversation is handed this client, and every call it makes —
// planning, the step, every repair, every look — is recorded against the goal
// before its answer is used.
//
// # Why it also refuses
//
// The worker checks the budget before a task starts, and a step makes several
// calls after that. A client that only recorded would let the last step of a
// spent goal make all of them.
//
// ‼️ And it refuses BEFORE the ceiling, not at it (2026-09-17, decided by the
// coordinator under damon's delegation: a goal must not spend past its ceiling).
// It used to refuse once the spend REACHED the ceiling, so the last call placed
// under it — up to ~16,000 tokens for a build step — landed past it. A call is now
// placed only if what the goal has spent, plus the reserve for it and for every
// call still in flight (the largest call the goal has made, and a quarter more;
// engine.BudgetGuard.CheckCall), still fits. When it does not, the goal stops with
// the budget-stop text, saying how much was left and why the call was not placed.
// Fence: TestBuildGoal_AGoalStopsBeforeACallThatWouldPassItsCeiling.
//
// Tokens and cost only. The wall clock and the task count are the worker's to
// check between tasks: a step abandoned half-built because the clock ticked over
// during a repair is not a refusal anybody could act on.
type chargedClient struct {
	inner  llm.Client
	budget *engine.BudgetGuard
	pool   db.Querier
	goal   engine.Goal
	clock  clock.Clock
	log    *logx.Logger

	mu     sync.Mutex
	spent  int64
	failed error
	breach *engine.LimitBreach
	// largest is the costliest call of this goal so far, read from the goal and
	// raised by every call this client makes; inflight is the calls placed and not
	// yet answered. Together they are what the next call must leave room for.
	largest  int64
	inflight int
}

// chargeTo wraps a client so every call it makes is charged to goal.
func chargeTo(inner llm.Client, budget *engine.BudgetGuard, pool db.Querier, goal *engine.Goal,
	clk clock.Clock, log *logx.Logger) *chargedClient {
	if log == nil {
		log = logx.Discard()
	}
	return &chargedClient{inner: inner, budget: budget, pool: pool, goal: *goal, clock: clk, log: log,
		largest: goal.Spend.LargestCall}
}

func (c *chargedClient) Complete(ctx context.Context, req llm.Request) (*llm.Response, error) {
	c.mu.Lock()
	if c.breach == nil {
		g := c.goal
		g.Spend.Tokens += c.spent
		g.Spend.LargestCall = c.largest
		if b := c.budget.CheckCall(&g, c.clock.Now(), c.inflight); b != nil &&
			(b.Kind == engine.LimitTokens || b.Kind == engine.LimitCost) {
			c.breach = b
		}
	}
	breach := c.breach
	if breach == nil {
		c.inflight++
	}
	c.mu.Unlock()
	if breach != nil {
		return nil, breach.Error()
	}

	resp, err := c.inner.Complete(ctx, req)
	c.mu.Lock()
	c.inflight--
	c.mu.Unlock()
	if err != nil {
		c.mu.Lock()
		c.failed = err
		c.mu.Unlock()
		return nil, err
	}
	c.mu.Lock()
	c.spent += resp.Usage.TotalTokens
	c.largest = max(c.largest, resp.Usage.TotalTokens)
	c.mu.Unlock()
	// Recorded even when the caller is going away: the tokens were spent the
	// moment the answer came back, whether or not anybody reads it.
	if err := c.budget.RecordSpend(context.WithoutCancel(ctx), c.pool, c.goal.ID, resp.Usage.TotalTokens, 0); err != nil {
		c.log.WarnWith(ctx, logx.EventBudgetRecordFailed, err, "goal_id", c.goal.ID)
	}
	return resp, nil
}

func (c *chargedClient) ModelFor(role llm.Role) string { return c.inner.ModelFor(role) }

// Breach is the ceiling a call was refused at, or nil.
func (c *chargedClient) Breach() *engine.LimitBreach {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.breach
}

// Failed is the last error the model itself returned, or nil.
func (c *chargedClient) Failed() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.failed
}

// withClient is this conversation speaking through another client — the same
// kernel, runner and stores, with every call going where client sends it.
func (c *Conversation) withClient(client llm.Client) *Conversation {
	cc := *c
	cc.client = client
	return &cc
}
