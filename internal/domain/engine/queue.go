package engine

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// SkipReasonDependencyFailed marks a task skipped because something it depended
// on failed, as opposed to one removed by a replan.
//
// # Why the distinction is load-bearing
//
// Both outcomes land in status 'skipped', and they mean opposite things to
// everything downstream:
//
//   - Skipped by a REPLAN: the work is no longer needed. Tasks waiting on it
//     should proceed, or removing one task would deadlock everything after it.
//   - Skipped because a DEPENDENCY FAILED: the work is unreachable. Tasks
//     waiting on it must also be skipped, or the failure stops propagating one
//     level down and leaves a tail of permanently pending tasks.
//
// A first version of this engine had only the status and so could satisfy one
// rule or the other, never both: propagation stopped after a single level.
// The reason is now recorded in error_code, which already exists to say why a
// task ended the way it did, and both queries branch on it.
const SkipReasonDependencyFailed = "DEPENDENCY_FAILED"

// Queue is the durable job queue.
//
// Bullet B9: execute work through workers and a queue rather than keeping one
// LLM request or process alive indefinitely. The queue is a table, not a broker,
// because the work items already have to be durable, transactional, and joinable
// against the rest of the domain — and a separate broker would mean two sources
// of truth about what work exists, kept in sync by hope.
type Queue struct{}

// NewQueue returns the queue.
func NewQueue() *Queue { return &Queue{} }

// Claim leases the next runnable task for a worker, or returns nil when there
// is nothing to do.
//
// # Why this shape
//
// `FOR UPDATE SKIP LOCKED` is the whole design. Without it, N workers polling
// the same "oldest ready task" all select the same row, one wins the update, and
// the other N-1 do a wasted round trip — at which point throughput stops scaling
// with workers. SKIP LOCKED makes each worker take the next row nobody else has
// locked, so concurrent claims are contention-free rather than merely correct.
//
// The lease is a timestamp rather than a boolean flag. A crashed worker leaves a
// lease that simply lapses; nothing has to notice the crash, and no janitor is
// required for the common case. That is the difference between a queue that
// recovers on its own and one that needs an operator at 3am.
//
// Returns (nil, nil) when the queue is empty — not an error. An idle queue is
// the normal state of a long-running agent, and treating it as an error would
// fill the log with noise that hides the real ones.
func (q *Queue) Claim(ctx context.Context, ex db.Querier, workerID string, leaseFor time.Duration, now time.Time) (*Task, error) {
	const op = "engine.Queue.Claim"

	if workerID == "" {
		return nil, errs.New(op, errs.CodeInvariantViolated).
			WithDetail("a lease must name its owner; an anonymous lease cannot be reclaimed or attributed")
	}
	if leaseFor <= 0 {
		return nil, errs.New(op, errs.CodeInvariantViolated).
			WithDetail("lease duration must be positive, got %s", leaseFor)
	}

	expires := now.Add(leaseFor)

	row := ex.QueryRow(ctx, `
		update forge_tasks
		   set status           = 'claimed',
		       lease_owner      = $1,
		       lease_expires_at = $2,
		       attempt_count    = attempt_count + 1
		 where id = (
		     select t.id
		       from forge_tasks t
		       join forge_goals g on g.id = t.goal_id
		      where t.status      = 'ready'
		        and t.lease_owner is null
		        and t.not_before  <= $3
		        -- A paused goal must stop producing work immediately (bullet
		        -- B10). Filtering here rather than in the worker means a pause
		        -- takes effect on the next claim, with no in-flight window.
		        and g.status      = 'active'
		      order by t.priority asc, t.created_at asc
		      -- SKIP LOCKED is what makes concurrent workers contention-free
		      -- rather than merely correct: each takes the next row nobody else
		      -- has locked, instead of all colliding on the oldest one.
		      for update of t skip locked
		      limit 1
		 )
		returning `+taskColumns,
		workerID, expires, now)

	task, err := scanTask(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Nothing to do. The normal state of a long-running agent.
			return nil, nil
		}
		return nil, wrapTaskScan(op, err)
	}
	return task, nil
}

// Heartbeat extends a lease the worker still holds.
//
// The `lease_owner = $2` guard is the point. A worker whose lease already
// expired and was reclaimed by someone else must NOT be able to extend it —
// otherwise two workers would believe they own the same task, which is the exact
// failure leases exist to prevent. A heartbeat that finds it no longer owns the
// lease returns CONFLICT, and the caller must abandon its work rather than
// finish it.
func (q *Queue) Heartbeat(ctx context.Context, ex db.Querier, taskID, workerID string, leaseFor time.Duration, now time.Time) error {
	const op = "engine.Queue.Heartbeat"

	tag, err := ex.Exec(ctx, `
		update forge_tasks
		   set lease_expires_at = $3
		 where id = $1
		   and lease_owner = $2
		   and lease_expires_at > $4
		   and status in ('claimed','running','verifying')`,
		taskID, workerID, now.Add(leaseFor), now)
	if err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	if tag.RowsAffected() == 0 {
		return errs.New(op, errs.CodeConflict).
			WithDetail("worker %s no longer holds the lease on task %s; another worker may have reclaimed it. "+
				"Abandon this attempt rather than completing it — finishing work under a lost lease is how the same task runs twice",
				workerID, taskID)
	}
	return nil
}

// Release hands a task back without completing it.
//
// Used when a worker shuts down cleanly mid-task (bullet B24: cancellation must
// be safe). Returning it to 'ready' immediately is far better than letting the
// lease lapse, because the task resumes in seconds rather than after the full
// lease duration.
func (q *Queue) Release(ctx context.Context, ex db.Querier, taskID, workerID string, notBefore time.Time) error {
	const op = "engine.Queue.Release"

	tag, err := ex.Exec(ctx, `
		update forge_tasks
		   set status = 'ready', lease_owner = null, lease_expires_at = null,
		       not_before = $3
		 where id = $1 and lease_owner = $2 and status in ('claimed','running')`,
		taskID, workerID, notBefore)
	if err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	if tag.RowsAffected() == 0 {
		return errs.New(op, errs.CodeConflict).
			WithDetail("task %s is not held by worker %s in a releasable state", taskID, workerID)
	}
	return nil
}

// ReapExpiredLeases returns tasks whose worker died back to the queue.
//
// This is the recovery path for a crashed or partitioned worker (bullet B28).
// It runs periodically rather than being triggered by a crash, because nothing
// reliably observes a crash — the process that would report it is the one that
// died.
//
// Tasks that have exhausted their attempts are failed rather than requeued.
// Otherwise a task that crashes its worker every time — an out-of-memory input,
// a panic-inducing payload — would cycle forever, taking a worker down with it
// on each pass.
func (q *Queue) ReapExpiredLeases(ctx context.Context, ex db.Querier, now time.Time, limit int) ([]*Task, error) {
	const op = "engine.Queue.ReapExpiredLeases"

	rows, err := ex.Query(ctx, `
		with expired as (
		    select id from forge_tasks
		     where lease_owner is not null
		       and lease_expires_at <= $1
		       and status in ('claimed','running','verifying')
		     order by lease_expires_at asc
		     limit $2
		     for update skip locked
		)
		update forge_tasks t
		   set status = case
		           when t.attempt_count >= t.max_attempts then 'failed'
		           else 'ready'
		       end,
		       lease_owner      = null,
		       lease_expires_at = null,
		       ended_at = case
		           when t.attempt_count >= t.max_attempts then $1
		           else t.ended_at
		       end,
		       error_code = case
		           when t.attempt_count >= t.max_attempts then 'LEASE_EXPIRED_ATTEMPTS_EXHAUSTED'
		           else t.error_code
		       end,
		       error_detail = case
		           when t.attempt_count >= t.max_attempts
		           then 'the worker holding this task stopped responding, and no attempts remain'
		           else t.error_detail
		       end
		  from expired
		 where t.id = expired.id
		returning `+taskColumnsPrefixed("t"),
		now, limit)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	defer rows.Close()

	var out []*Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, wrapTaskScan(op, err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	return out, nil
}

// PromoteReadyTasks moves pending tasks whose dependencies are all satisfied to
// 'ready', and returns how many moved.
//
// # Why NOT EXISTS rather than a counter
//
// A denormalised `pending_deps` counter on each task would be O(1) to check,
// and it would be one more thing that can drift from the edges it summarises —
// after a replan, a skip, or a partially applied transaction. This query is the
// authority; if it is ever too slow, the counter can be added as a cache in
// front of it, with this query as the reconciliation that proves the cache right.
//
// A dependency skipped by a REPLAN counts as satisfied: removing work must not
// deadlock everything downstream of it. A dependency skipped because its own
// dependency failed does not — see SkipReasonDependencyFailed.
func (q *Queue) PromoteReadyTasks(ctx context.Context, ex db.Querier, goalID string, now time.Time) (int64, error) {
	const op = "engine.Queue.PromoteReadyTasks"

	tag, err := ex.Exec(ctx, `
		update forge_tasks t
		   set status = 'ready'
		 where t.goal_id = $1
		   and t.status  = 'pending'
		   and not exists (
		       select 1
		         from forge_task_deps d
		         join forge_tasks dep on dep.id = d.depends_on_id
		        where d.task_id = t.id
		          and not (
		              dep.status = 'succeeded'
		              -- Skipped by a replan satisfies the dependency; skipped
		              -- because something upstream failed does not.
		              or (dep.status = 'skipped'
				  and coalesce(dep.error_code, '') <> $2)
		          )
		   )`, goalID, SkipReasonDependencyFailed)
	if err != nil {
		return 0, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	return tag.RowsAffected(), nil
}

// SkipTasksBlockedByFailure marks tasks unreachable because a dependency failed.
//
// Bullet B17: one task failing must not corrupt the workflow or lose completed
// work. The downstream tasks are marked 'skipped' with a reason rather than left
// 'pending' forever — a goal full of permanently pending tasks looks identical
// to one still working, and is the shape a stalled agent takes.
//
// Runs repeatedly until it stops changing anything, because skipping a task can
// block the tasks that depended on *it*.
func (q *Queue) SkipTasksBlockedByFailure(ctx context.Context, ex db.Querier, goalID string, now time.Time) (int64, error) {
	const op = "engine.Queue.SkipTasksBlockedByFailure"

	var total int64
	// Bounded rather than `for {}`: a cycle that slipped past plan validation
	// would otherwise spin here forever. The DAG's depth is the natural bound,
	// and exceeding it is a defect worth reporting rather than tolerating.
	const maxPasses = 64
	for pass := 0; pass < maxPasses; pass++ {
		tag, err := ex.Exec(ctx, `
			update forge_tasks t
			   set status = 'skipped',
			       ended_at = $2,
			       error_code = $3,
			       error_detail = 'a task this one depended on failed, was cancelled, or was itself unreachable'
			 where t.goal_id = $1
			   and t.status in ('pending','ready')
			   and exists (
			       select 1
			         from forge_task_deps d
			         join forge_tasks dep on dep.id = d.depends_on_id
			        where d.task_id = t.id
			          and (
			              dep.status in ('failed','cancelled')
			              -- Transitive: a dependency that was itself skipped as
			              -- unreachable makes this one unreachable too. Without
			              -- this arm the failure stops one level down and leaves
			              -- a tail of permanently pending tasks, which looks
			              -- exactly like a goal still working.
			              or (dep.status = 'skipped' and dep.error_code = $3)
			          )
			   )`, goalID, now, SkipReasonDependencyFailed)
		if err != nil {
			return total, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
		}
		n := tag.RowsAffected()
		total += n
		if n == 0 {
			return total, nil
		}
	}
	return total, errs.New(op, errs.CodeInvariantViolated).
		WithDetail("dependency propagation did not settle after %d passes on goal %s; the task graph probably contains a cycle that plan validation missed",
			maxPasses, goalID)
}

// Depth returns how deep in the queue a goal's outstanding work is, by status.
// Used by the observability surface and by the scheduler to decide whether a
// goal still needs attention.
func (q *Queue) Depth(ctx context.Context, ex db.Querier, goalID string) (map[TaskStatus]int, error) {
	const op = "engine.Queue.Depth"

	rows, err := ex.Query(ctx,
		`select status, count(*) from forge_tasks where goal_id = $1 group by status`, goalID)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	defer rows.Close()

	out := map[TaskStatus]int{}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
		}
		out[TaskStatus(s)] = n
	}
	if err := rows.Err(); err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	return out, nil
}
