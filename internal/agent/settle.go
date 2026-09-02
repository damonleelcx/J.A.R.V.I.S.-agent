package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// settleGoal moves a goal to a terminal state once none of its work is
// outstanding.
//
// # Why this is needed at all
//
// Nothing else does it. Tasks reach terminal states on their own, and a goal
// whose tasks are all finished would otherwise sit "active" forever: it looks
// identical to one still working, it keeps consuming its wall-clock budget until
// that trips, and the person who asked for it never learns it is done. A
// long-running agent that cannot tell you it has finished is barely better than
// one that never finishes.
//
// # Why it runs on the worker rather than on a timer
//
// The moment a goal can become terminal is exactly the moment a task became
// terminal, and the worker is already there holding that fact. A separate
// sweeper would add a second thing that decides goal state, and two authorities
// for one field is how a goal ends up marked failed and succeeded on different
// rows of the timeline.
//
// # The outcome rule
//
// Succeeded requires that every task ended succeeded or skipped. One failure is
// enough to fail the goal — a partially completed goal reported as success is
// the same class of lie as an unverified task reported as verified.
func (w *Worker) settleGoal(ctx context.Context, goalID string) {
	depth, err := w.queue.Depth(ctx, w.pool, goalID)
	if err != nil {
		w.log.WarnWith(ctx, logx.EventGoalSettleFailed, err, "goal_id", goalID)
		return
	}

	outstanding, succeeded, failed, cancelled, skipped, total := 0, 0, 0, 0, 0, 0
	for status, n := range depth {
		total += n
		switch {
		case status.Active():
			outstanding += n
		case status == engine.StatusSucceeded:
			succeeded += n
		case status == engine.StatusFailed:
			failed += n
		case status == engine.StatusCancelled:
			cancelled += n
		case status == engine.StatusSkipped:
			skipped += n
		}
	}
	if total == 0 || outstanding > 0 {
		return // still working, or nothing planned yet
	}

	final := engine.GoalSucceeded
	summary := fmt.Sprintf("All %d task(s) finished: %d succeeded, %d skipped.", total, succeeded, skipped)
	failureCode := ""

	if failed > 0 || cancelled > 0 {
		final = engine.GoalFailed
		failureCode = "TASKS_FAILED"
		summary = fmt.Sprintf("%d of %d task(s) failed or were cancelled; %d succeeded, %d were skipped "+
			"as unreachable.", failed+cancelled, total, succeeded, skipped)
	}

	now := w.clock.Now()
	// Conditional on the goal still being active. Another worker settling the
	// same goal at the same instant must lose rather than overwrite, and a goal
	// a human paused or cancelled mid-flight must not be resurrected into a
	// terminal state it did not reach on its own.
	tag, err := w.pool.Exec(ctx, `
		update forge_goals
		   set status = $2, ended_at = $3, outcome_summary = $4,
		       failure_code = nullif($5, '')
		 where id = $1 and status = 'active'`,
		goalID, string(final), now, summary, failureCode)
	if err != nil {
		w.log.WarnWith(ctx, logx.EventGoalSettleFailed, err, "goal_id", goalID)
		return
	}
	if tag.RowsAffected() == 0 {
		return // already settled, paused, or cancelled by someone else
	}

	payload, _ := json.Marshal(map[string]any{
		"outcome": string(final), "tasks_total": total,
		"succeeded": succeeded, "failed": failed,
		"cancelled": cancelled, "skipped": skipped,
	})
	if err := w.repo.AppendEvent(ctx, w.pool, &engine.Event{
		GoalID: goalID, Kind: engine.EventGoalEnded, Actor: engine.ActorSystem,
		Summary: summary, Payload: payload,
	}, now); err != nil {
		w.log.WarnWith(ctx, logx.EventGoalSettleFailed, err, "goal_id", goalID,
			"detail", "the goal was settled but the timeline entry was lost")
	}

	w.log.Info(ctx, logx.EventGoalSettled,
		"goal_id", goalID, "outcome", string(final),
		"tasks_total", total, "succeeded", succeeded, "failed", failed, "skipped", skipped)
}

// settleFinishedGoals reconciles every active goal that has no outstanding work.
//
// # Why a sweep as well as the per-task call
//
// Settling only when a task finishes makes goal state depend on event timing,
// and event timing is exactly what a durable system cannot rely on. Three ways
// the event is missed, all of them ordinary:
//
//   - The worker dies between the task's last write and the settlement call.
//   - The settlement write loses a race and returns rows_affected = 0.
//   - The goal's tasks all finished under a build that had no settlement at all,
//     which is how this gap was found in the first place.
//
// In every case the goal sits "active" forever, indistinguishable from one still
// working. So the per-task call is the fast path and this is the guarantee: it
// runs on the idle poll, costs one indexed query when there is nothing to do,
// and converges regardless of what was missed.
//
// This is the general rule for any "A must be followed by B" invariant: the
// event-driven write needs an idempotent reconciliation read on a path that is
// necessarily travelled.
func (w *Worker) settleFinishedGoals(ctx context.Context) {
	rows, err := w.pool.Query(ctx, `
		select g.id
		  from forge_goals g
		 where g.status = 'active'
		   and exists (select 1 from forge_tasks t where t.goal_id = g.id)
		   and not exists (
		       select 1 from forge_tasks t
		        where t.goal_id = g.id
		          and t.status not in ('succeeded','failed','cancelled','skipped')
		   )
		 limit 25`)
	if err != nil {
		w.log.WarnWith(ctx, logx.EventGoalSettleFailed, err,
			"detail", "the goal reconciliation sweep could not run; finished goals may stay marked active")
		return
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var gid string
		if err := rows.Scan(&gid); err != nil {
			continue
		}
		ids = append(ids, gid)
	}
	if err := rows.Err(); err != nil {
		w.log.WarnWith(ctx, logx.EventGoalSettleFailed, err)
		return
	}
	// Settle outside the row iteration: settleGoal writes, and holding a cursor
	// open across writes on the same table invites lock ordering surprises.
	for _, gid := range ids {
		w.settleGoal(ctx, gid)
	}
}

// CompletionState summarises a goal's outcome for the API and the CLI.
type CompletionState struct {
	Outstanding int
	Succeeded   int
	Failed      int
	Skipped     int
	Total       int
}

// Settled reports whether no work remains.
func (c CompletionState) Settled() bool { return c.Total > 0 && c.Outstanding == 0 }

// GoalCompletion reads a goal's task counts.
func GoalCompletion(ctx context.Context, ex db.Querier, queue *engine.Queue, goalID string) (CompletionState, error) {
	depth, err := queue.Depth(ctx, ex, goalID)
	if err != nil {
		return CompletionState{}, errs.Wrap("agent.GoalCompletion", errs.CodeDatabaseUnavail, err)
	}
	var c CompletionState
	for status, n := range depth {
		c.Total += n
		switch {
		case status.Active():
			c.Outstanding += n
		case status == engine.StatusSucceeded:
			c.Succeeded += n
		case status == engine.StatusFailed, status == engine.StatusCancelled:
			c.Failed += n
		case status == engine.StatusSkipped:
			c.Skipped += n
		}
	}
	return c, nil
}

// SettleGoalForTest exposes settlement so tests can drive it deterministically
// instead of racing a running worker.
//
// Exported rather than duplicated in the test: a test that reimplements the
// logic it is checking proves only that two copies agree.
func (w *Worker) SettleGoalForTest(ctx context.Context, goalID string) { w.settleGoal(ctx, goalID) }
