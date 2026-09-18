-- 0025_goal_largest_call: the largest single model call a goal has paid for.
--
-- docs/spikes/2026-09-17-live-verification, follow-up 4; decided 2026-09-17 by the
-- coordinator under damon's delegation: a goal must not spend past its ceiling.
--
-- ===========================================================================
-- 1. What was wrong
-- ===========================================================================
-- A build's model calls are refused once what the goal has spent REACHES its
-- ceiling (agent/spend.go). One call placed just under it — a build step's call
-- is up to ~16,000 tokens — lands past it, so a goal with a 70,000 ceiling can
-- end at 85,000. The live harness had the same rule and spent 301,142 of a
-- 300,000 cap; PR 148 closed it there by reserving, before each call, the
-- largest call seen so far plus a quarter, per call in flight.
--
-- ===========================================================================
-- 2. Why the number is stored
-- ===========================================================================
-- A goal's calls are made by more than one process and more than one task: the
-- plan in forged, each build step in whichever forge-worker claims it, and a
-- worker may restart between steps. The budget guard reads PERSISTED counters for
-- exactly that reason (engine/budget.go), and the reservation must too: a
-- largest-call figure held in memory would start every step, and every restarted
-- worker, at nothing, which is the overshoot this closes. Written in the same
-- statement that adds a call's tokens to tokens_spent (BudgetGuard.RecordSpend),
-- so the two cannot disagree.

alter table forge_goals
    add column if not exists largest_call_tokens bigint not null default 0;
