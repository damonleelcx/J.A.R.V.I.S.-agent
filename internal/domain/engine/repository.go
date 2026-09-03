package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
)

// taskFields is the ordered column list for forge_tasks.
//
// One list, used to build both the bare and the prefixed form, because a SELECT
// list that drifts from its scan function produces a runtime column-count error
// with no compile-time warning — and it happens on the least-tested query.
var taskFields = []string{
	"id", "goal_id", "plan_id", "parent_task_id", "depth",
	"title", "instruction", "inputs", "expected_output",
	"status", "idempotency_key", "attempt_count", "max_attempts",
	"lease_owner", "lease_expires_at", "not_before", "priority",
	"risk_tier", "requires_approval",
	"result", "verified_at", "verdict",
	"error_code", "error_detail",
	"started_at", "ended_at", "created_at", "updated_at",
}

var taskColumns = strings.Join(taskFields, ", ")

// taskColumnsPrefixed renders the same list qualified by a table alias, for
// queries that join.
func taskColumnsPrefixed(alias string) string {
	out := make([]string, len(taskFields))
	for i, f := range taskFields {
		out[i] = alias + "." + f
	}
	return strings.Join(out, ", ")
}

func scanTask(row pgx.Row) (*Task, error) {
	var t Task
	var status, riskTier string
	var errCode, errDetail *string
	err := row.Scan(
		&t.ID, &t.GoalID, &t.PlanID, &t.ParentTaskID, &t.Depth,
		&t.Title, &t.Instruction, &t.Inputs, &t.ExpectedOutput,
		&status, &t.IdempotencyKey, &t.AttemptCount, &t.MaxAttempts,
		&t.LeaseOwner, &t.LeaseExpiresAt, &t.NotBefore, &t.Priority,
		&riskTier, &t.RequiresApproval,
		&t.Result, &t.VerifiedAt, &t.Verdict,
		&errCode, &errDetail,
		&t.StartedAt, &t.EndedAt, &t.CreatedAt, &t.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	t.Status = TaskStatus(status)
	t.RiskTier = RiskTier(riskTier)
	if errCode != nil {
		t.ErrorCode = *errCode
	}
	if errDetail != nil {
		t.ErrorDetail = *errDetail
	}
	// A status the code does not recognise is corruption, not a caller error.
	// Coercing it to something plausible would let the engine act on a task
	// whose state it does not actually understand.
	if !t.Status.Valid() {
		return nil, errs.New("engine.scanTask", errs.CodeStateCorrupt).
			WithDetail("task %s has status %q, unknown to this build", t.ID, status)
	}
	if !t.RiskTier.Valid() {
		return nil, errs.New("engine.scanTask", errs.CodeStateCorrupt).
			WithDetail("task %s has risk tier %q, unknown to this build", t.ID, riskTier)
	}
	return &t, nil
}

func wrapTaskScan(op string, err error) error {
	if errs.Is(err, errs.CodeStateCorrupt) {
		return err
	}
	return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
}

// Repository persists the engine's aggregates.
type Repository struct{}

// NewRepository returns the Postgres implementation.
func NewRepository() *Repository { return &Repository{} }

// ---------------------------------------------------------------------------
// tasks
// ---------------------------------------------------------------------------

// CreateTask inserts a task and its dependency edges in one statement set.
//
// Depth and the per-goal task ceiling are enforced by the caller before this
// point; the database's job here is referential integrity and the idempotency
// key's uniqueness.
func (r *Repository) CreateTask(ctx context.Context, ex db.Querier, t *Task, dependsOn []string) error {
	const op = "engine.Repository.CreateTask"

	if t.CreatedAt.IsZero() {
		return errs.New(op, errs.CodeInvariantViolated).
			WithDetail("task %s has no CreatedAt; the application clock owns every timestamp", t.ID)
	}
	if t.IdempotencyKey == "" {
		return errs.New(op, errs.CodeInvariantViolated).
			WithDetail("task %s has no idempotency key; a retry could not be deduplicated", t.ID)
	}
	if !t.Status.Valid() {
		return errs.New(op, errs.CodeInvariantViolated).
			WithDetail("task %s has unrecognised status %q", t.ID, t.Status)
	}

	_, err := ex.Exec(ctx, `
		insert into forge_tasks
			(id, goal_id, plan_id, parent_task_id, depth, title, instruction,
			 inputs, expected_output, status, idempotency_key, max_attempts,
			 not_before, priority, risk_tier, requires_approval, created_at, updated_at)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$17)`,
		t.ID, t.GoalID, t.PlanID, t.ParentTaskID, t.Depth, t.Title, t.Instruction,
		jsonOrEmpty(t.Inputs), jsonOrEmpty(t.ExpectedOutput), string(t.Status),
		t.IdempotencyKey, t.MaxAttempts, t.NotBefore, t.Priority,
		string(t.RiskTier), t.RequiresApproval, t.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return errs.Wrap(op, errs.CodeConflict, err).
				WithDetail("a task with idempotency key %q already exists in goal %s", t.IdempotencyKey, t.GoalID)
		}
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}

	for _, dep := range dependsOn {
		if dep == t.ID {
			return errs.New(op, errs.CodeInvariantViolated).
				WithDetail("task %s cannot depend on itself", t.ID)
		}
		if _, err := ex.Exec(ctx,
			`insert into forge_task_deps (task_id, depends_on_id) values ($1,$2)
			 on conflict do nothing`, t.ID, dep); err != nil {
			return errs.Wrap(op, errs.CodeDatabaseUnavail, err).
				WithDetail("adding dependency %s → %s", t.ID, dep)
		}
	}
	return nil
}

// GetTask reads one task.
func (r *Repository) GetTask(ctx context.Context, ex db.Querier, taskID string) (*Task, error) {
	const op = "engine.Repository.GetTask"

	t, err := scanTask(ex.QueryRow(ctx, `select `+taskColumns+` from forge_tasks where id = $1`, taskID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errs.Wrap(op, errs.CodeNotFound, err).WithDetail("no task %s", taskID)
		}
		return nil, wrapTaskScan(op, err)
	}
	return t, nil
}

// TransitionTask moves a task to a new status, enforcing the state machine and
// the lease.
//
// # Two guards, both load-bearing
//
// The transition is validated in Go *and* the UPDATE re-checks the current
// status in its WHERE clause. That is not redundancy for its own sake: between
// reading the task and writing it, another worker's reaper may have reclaimed
// the lease and moved the row. The WHERE clause turns that race into a CONFLICT
// the caller must handle, instead of a silent overwrite of someone else's work.
func (r *Repository) TransitionTask(ctx context.Context, ex db.Querier, t *Task, to TaskStatus, now time.Time, mut TaskMutation) error {
	const op = "engine.Repository.TransitionTask"

	if err := ValidateTransition(t.Status, to); err != nil {
		return err
	}

	// A task leaving a lease-holding state must give the lease up. Leaving it
	// set would hide the task from the queue permanently — and the database
	// constraint would reject the row anyway, but with a message about a check
	// constraint rather than about what actually went wrong.
	clearLease := !to.HoldsLease()

	var endedAt *time.Time
	if to.Terminal() {
		endedAt = &now
	}
	var startedAt *time.Time
	if to == StatusRunning && t.StartedAt == nil {
		startedAt = &now
	}

	tag, err := ex.Exec(ctx, `
		update forge_tasks
		   set status           = $2,
		       lease_owner      = case when $3 then null else lease_owner end,
		       lease_expires_at = case when $3 then null else lease_expires_at end,
		       started_at       = coalesce(started_at, $4),
		       ended_at         = coalesce($5, ended_at),
		       not_before       = coalesce($6, not_before),
		       result           = coalesce($7, result),
		       verdict          = coalesce($8, verdict),
		       verified_at      = coalesce($9, verified_at),
		       error_code       = case when $10 = '' then error_code else $10 end,
		       error_detail     = case when $11 = '' then error_detail else $11 end
		 where id = $1
		   -- The row must still be where we last saw it. Between our read and
		   -- this write, a reaper may have reclaimed it.
		   and status = $12`,
		t.ID, string(to), clearLease, startedAt, endedAt, mut.NotBefore,
		nullableJSON(mut.Result), nullableJSON(mut.Verdict), mut.VerifiedAt,
		mut.ErrorCode, mut.ErrorDetail, string(t.Status))
	if err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err).
			WithDetail("transitioning task %s from %s to %s", t.ID, t.Status, to)
	}
	if tag.RowsAffected() == 0 {
		return errs.New(op, errs.CodeConflict).
			WithDetail("task %s was no longer in status %q when the transition was written; "+
				"another worker or the lease reaper moved it first", t.ID, t.Status)
	}
	t.Status = to
	return nil
}

// TaskMutation carries the optional fields a transition may also write.
type TaskMutation struct {
	Result      json.RawMessage
	Verdict     json.RawMessage
	VerifiedAt  *time.Time
	ErrorCode   string
	ErrorDetail string
	// NotBefore reschedules the task — retry backoff, or a wake-up timer.
	NotBefore *time.Time
}

// ListTasks returns a goal's tasks, oldest first.
func (r *Repository) ListTasks(ctx context.Context, ex db.Querier, goalID string) ([]*Task, error) {
	const op = "engine.Repository.ListTasks"

	rows, err := ex.Query(ctx,
		`select `+taskColumns+` from forge_tasks where goal_id = $1 order by created_at asc`, goalID)
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

// ListDependencies returns the ids a task waits on.
func (r *Repository) ListDependencies(ctx context.Context, ex db.Querier, taskID string) ([]string, error) {
	const op = "engine.Repository.ListDependencies"

	rows, err := ex.Query(ctx,
		`select depends_on_id from forge_task_deps where task_id = $1 order by depends_on_id`, taskID)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// checkpoints
// ---------------------------------------------------------------------------

// SaveCheckpoint appends resumable state.
//
// The sequence number is allocated inside the INSERT rather than read and
// incremented by the caller, so two concurrent writers cannot both compute the
// same next value. The unique constraint on (task_id, seq) is the backstop.
func (r *Repository) SaveCheckpoint(ctx context.Context, ex db.Querier, taskID, kind string, state json.RawMessage, now time.Time) (*Checkpoint, error) {
	const op = "engine.Repository.SaveCheckpoint"

	if len(state) == 0 {
		return nil, errs.New(op, errs.CodeInvariantViolated).
			WithDetail("a checkpoint with no state cannot be resumed from and should not be written")
	}

	cp := &Checkpoint{
		ID:     id.New(id.PrefixCheckpoint),
		TaskID: taskID,
		Kind:   kind,
		State:  state,
	}
	err := ex.QueryRow(ctx, `
		insert into forge_checkpoints (id, task_id, seq, kind, state, created_at)
		values ($1, $2,
		        (select coalesce(max(seq), 0) + 1 from forge_checkpoints where task_id = $2),
		        $3, $4, $5)
		returning seq, created_at`,
		cp.ID, taskID, kind, state, now).Scan(&cp.Seq, &cp.CreatedAt)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	return cp, nil
}

// LatestCheckpoint returns the most recent checkpoint for a task, or nil.
//
// This is what "reconstruct the agent's working context from persisted state on
// every execution cycle" reads (bullet B6). Nothing else is trusted to survive.
func (r *Repository) LatestCheckpoint(ctx context.Context, ex db.Querier, taskID string) (*Checkpoint, error) {
	const op = "engine.Repository.LatestCheckpoint"

	var cp Checkpoint
	err := ex.QueryRow(ctx, `
		select id, task_id, seq, kind, state, created_at
		  from forge_checkpoints where task_id = $1
		 order by seq desc limit 1`, taskID).
		Scan(&cp.ID, &cp.TaskID, &cp.Seq, &cp.Kind, &cp.State, &cp.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// No checkpoint yet is normal for a task on its first attempt.
			return nil, nil
		}
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	if len(cp.State) == 0 {
		return nil, errs.New(op, errs.CodeCheckpointUnreadabl).
			WithDetail("checkpoint %s for task %s has empty state", cp.ID, taskID)
	}
	return &cp, nil
}

// ---------------------------------------------------------------------------
// events
// ---------------------------------------------------------------------------

// AppendEvent adds to the execution timeline.
//
// Sequence allocation happens inside the INSERT, as for checkpoints. The unique
// constraint on (goal_id, seq) means a race produces a retryable conflict rather
// than two events silently sharing a position — which would make the timeline
// unorderable exactly when someone is trying to reconstruct what happened.
func (r *Repository) AppendEvent(ctx context.Context, ex db.Querier, e *Event, now time.Time) error {
	const op = "engine.Repository.AppendEvent"

	if !e.Actor.Valid() {
		return errs.New(op, errs.CodeInvariantViolated).
			WithDetail("event actor %q is not recognised; an unattributed event cannot answer 'who did this?'", e.Actor)
	}
	if e.Kind == "" {
		return errs.New(op, errs.CodeInvariantViolated).WithDetail("event has no kind")
	}
	if e.ID == "" {
		e.ID = id.New(id.PrefixEvent)
	}

	/* The audit chain (PRD SAF-06).
	 *
	 * The previous event's hash is read, this event's hash is computed from it in
	 * Go, and both are written with the row. Read-then-write is a race — two
	 * appends to one goal can read the same predecessor — and it is closed the
	 * same way the sequence number already was: unique (goal_id, seq) rejects the
	 * loser and the caller retries. No new failure mode, and the alternative
	 * (hashing inside SQL) would make the chain verifiable only by asking the
	 * same database to agree with itself.
	 *
	 * This is the ONLY place events are written, which is why the chain can be
	 * claimed to cover all of them. */
	var prevSeq int64
	var prevHash *string
	if err := ex.QueryRow(ctx, `
		select seq, hash from forge_events
		 where goal_id = $1 order by seq desc limit 1`, e.GoalID).Scan(&prevSeq, &prevHash); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
		}
	}
	link := chainGenesis
	if prevHash != nil && *prevHash != "" {
		link = *prevHash
	}

	digest, err := PayloadDigest(e.Payload)
	if err != nil {
		return err
	}
	e.Seq = prevSeq + 1
	e.CreatedAt = now
	hash := EventHash(link, e, digest)

	if _, err := ex.Exec(ctx, `
		insert into forge_events (id, goal_id, task_id, seq, kind, actor, actor_id, summary,
		                          payload, created_at, prev_hash, hash, payload_digest)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		e.ID, e.GoalID, e.TaskID, e.Seq, e.Kind, string(e.Actor), e.ActorID,
		e.Summary, jsonOrEmpty(e.Payload), now, link, hash, digest); err != nil {
		if isUniqueViolation(err) {
			return errs.Wrap(op, errs.CodeConflict, err).
				WithDetail("timeline sequence collision on goal %s; retry the enclosing transaction", e.GoalID)
		}
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	return nil
}

// Timeline returns a goal's events, newest first.
func (r *Repository) Timeline(ctx context.Context, ex db.Querier, goalID string, limit int, beforeSeq int64) ([]*Event, error) {
	const op = "engine.Repository.Timeline"

	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if beforeSeq <= 0 {
		beforeSeq = 1 << 62
	}
	rows, err := ex.Query(ctx, `
		select id, goal_id, task_id, seq, kind, actor, actor_id, summary, payload, created_at
		  from forge_events
		 where goal_id = $1 and seq < $2
		 order by seq desc limit $3`, goalID, beforeSeq, limit)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	defer rows.Close()

	var out []*Event
	for rows.Next() {
		var e Event
		var actor string
		if err := rows.Scan(&e.ID, &e.GoalID, &e.TaskID, &e.Seq, &e.Kind,
			&actor, &e.ActorID, &e.Summary, &e.Payload, &e.CreatedAt); err != nil {
			return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
		}
		e.Actor = Actor(actor)
		out = append(out, &e)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func jsonOrEmpty(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return []byte("{}")
	}
	return raw
}

// nullableJSON returns nil for absent JSON, so a coalesce() in SQL leaves the
// existing value alone rather than overwriting it with an empty object.
func nullableJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return []byte(raw)
}

func isUniqueViolation(err error) bool {
	var pgErr interface{ SQLState() string }
	return errors.As(err, &pgErr) && pgErr.SQLState() == "23505"
}
