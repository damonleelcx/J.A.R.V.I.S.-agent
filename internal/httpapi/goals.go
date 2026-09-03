package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/agent"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/access"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// GoalHandlers serve the console's data.
type GoalHandlers struct {
	deps  Deps
	repo  *engine.Repository
	queue *engine.Queue
	// intake plans new goals. Nil when no model is configured, which is a legal
	// deployment: reading and starting goals needs no model, and only creation
	// says so rather than the whole surface failing to mount.
	intake *agent.Intake
}

// NewGoalHandlers wires the goal endpoints.
func NewGoalHandlers(d Deps) *GoalHandlers {
	h := &GoalHandlers{deps: d, repo: engine.NewRepository(), queue: engine.NewQueue()}
	if d.LLM != nil {
		h.intake = agent.NewIntake(d.LLM, persona.DefaultCharacter(), d.Config.Engine, d.Clock)
	}
	return h
}

// GoalDTO is a goal as the console sees it.
//
// AvatarState is computed server-side rather than derived in the browser. The
// rule that a pending approval outranks running work is a product decision, and
// a decision reimplemented in the client is a decision that will eventually
// disagree with itself.
type GoalDTO struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Statement   string  `json:"statement"`
	Status      string  `json:"status"`
	Autonomy    string  `json:"autonomy"`
	RiskTier    string  `json:"risk_tier"`
	AvatarState string  `json:"avatar_state"`
	StateLabel  string  `json:"state_label"`
	TokensSpent int64   `json:"tokens_spent"`
	TasksTotal  int     `json:"tasks_total"`
	TasksDone   int     `json:"tasks_done"`
	TasksFailed int     `json:"tasks_failed"`
	Outstanding int     `json:"outstanding"`
	Pending     int     `json:"pending_approvals"`
	CreatedAt   string  `json:"created_at"`
	StartedAt   *string `json:"started_at,omitempty"`
	EndedAt     *string `json:"ended_at,omitempty"`
	Outcome     string  `json:"outcome_summary,omitempty"`
}

// ListGoals handles GET /v1/goals.
func (h *GoalHandlers) ListGoals(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())

	// Every project the caller is a member of, not every project they created.
	visible, err := h.deps.visibleProjects(r, user.ID)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	rows, err := h.deps.Pool.Query(r.Context(), `
		select g.id, g.title, g.statement, g.status, g.autonomy, g.risk_tier,
		       g.tokens_spent, g.created_at, g.started_at, g.ended_at,
		       coalesce(g.outcome_summary, '')
		  from forge_goals g
		 where g.project_id = any($1)
		 order by g.created_at desc
		 limit 100`, visible)
	if err != nil {
		WriteError(w, r, h.deps.Log, errs.Wrap("httpapi.ListGoals", errs.CodeDatabaseUnavail, err))
		return
	}
	defer rows.Close()

	out := []GoalDTO{}
	for rows.Next() {
		dto, err := h.scanGoalRow(rows)
		if err != nil {
			WriteError(w, r, h.deps.Log, err)
			return
		}
		out = append(out, dto)
	}
	if err := rows.Err(); err != nil {
		WriteError(w, r, h.deps.Log, errs.Wrap("httpapi.ListGoals", errs.CodeDatabaseUnavail, err))
		return
	}
	// Counts are filled per goal after the cursor closes: holding a cursor open
	// while issuing more queries on the same pool is how a small pool deadlocks
	// under concurrency.
	for i := range out {
		h.fillCounts(r, &out[i])
	}
	WriteJSON(w, http.StatusOK, map[string]any{"goals": out})
}

// GetGoal handles GET /v1/goals/{id}.
func (h *GoalHandlers) GetGoal(w http.ResponseWriter, r *http.Request) {
	goalID := r.PathValue("id")
	user, _ := UserFrom(r.Context())

	dto, err := h.loadGoal(r, goalID, user.ID)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	tasks, err := h.repo.ListTasks(r.Context(), h.deps.Pool, goalID)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	deps := map[string][]string{}
	taskDTOs := make([]TaskDTO, 0, len(tasks))
	for _, t := range tasks {
		d, _ := h.repo.ListDependencies(r.Context(), h.deps.Pool, t.ID)
		deps[t.ID] = d
		taskDTOs = append(taskDTOs, toTaskDTO(t, d))
	}
	WriteJSON(w, http.StatusOK, map[string]any{"goal": dto, "tasks": taskDTOs})
}

// TaskDTO is a task as the console sees it.
type TaskDTO struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Instruction string `json:"instruction"`
	Status      string `json:"status"`
	RiskTier    string `json:"risk_tier"`
	// Verified is deliberately separate from Status. PRD AGT-08: completion and
	// verification are different facts, and the console must be able to show a
	// succeeded-but-unverified task as exactly that.
	Verified         bool     `json:"verified"`
	VerificationReq  bool     `json:"verification_required"`
	RequiresApproval bool     `json:"requires_approval"`
	Attempts         int      `json:"attempts"`
	MaxAttempts      int      `json:"max_attempts"`
	DependsOn        []string `json:"depends_on"`
	ErrorCode        string   `json:"error_code,omitempty"`
	ErrorDetail      string   `json:"error_detail,omitempty"`
	StartedAt        *string  `json:"started_at,omitempty"`
	EndedAt          *string  `json:"ended_at,omitempty"`
}

func toTaskDTO(t *engine.Task, deps []string) TaskDTO {
	if deps == nil {
		deps = []string{}
	}
	d := TaskDTO{
		ID: t.ID, Title: t.Title, Instruction: t.Instruction,
		Status: string(t.Status), RiskTier: string(t.RiskTier),
		Verified:         t.Verified(),
		VerificationReq:  agent.VerificationRequired(t.RiskTier),
		RequiresApproval: t.RequiresApproval,
		Attempts:         t.AttemptCount, MaxAttempts: t.MaxAttempts,
		DependsOn: deps, ErrorCode: t.ErrorCode, ErrorDetail: t.ErrorDetail,
	}
	if t.StartedAt != nil {
		s := t.StartedAt.UTC().Format(time.RFC3339)
		d.StartedAt = &s
	}
	if t.EndedAt != nil {
		s := t.EndedAt.UTC().Format(time.RFC3339)
		d.EndedAt = &s
	}
	return d
}

// EventDTO is one timeline entry.
type EventDTO struct {
	Seq       int64  `json:"seq"`
	Kind      string `json:"kind"`
	Actor     string `json:"actor"`
	Summary   string `json:"summary"`
	TaskID    string `json:"task_id,omitempty"`
	CreatedAt string `json:"created_at"`
}

// Timeline handles GET /v1/goals/{id}/timeline.
//
// This is the "what happened, why, when, and what next" surface. Every entry
// carries its actor, so "FORGE proposed" and "a human approved" never collapse
// into one another.
func (h *GoalHandlers) Timeline(w http.ResponseWriter, r *http.Request) {
	goalID := r.PathValue("id")
	user, _ := UserFrom(r.Context())

	if _, err := h.loadGoal(r, goalID, user.ID); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	events, err := h.repo.Timeline(r.Context(), h.deps.Pool, goalID, 200, 0)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	out := make([]EventDTO, 0, len(events))
	for _, e := range events {
		d := EventDTO{
			Seq: e.Seq, Kind: e.Kind, Actor: string(e.Actor),
			Summary: e.Summary, CreatedAt: e.CreatedAt.UTC().Format(time.RFC3339),
		}
		if e.TaskID != nil {
			d.TaskID = *e.TaskID
		}
		out = append(out, d)
	}
	WriteJSON(w, http.StatusOK, map[string]any{"events": out})
}

// ApprovalDTO is a pending human gate.
type ApprovalDTO struct {
	ID          string          `json:"id"`
	GoalID      string          `json:"goal_id"`
	GoalTitle   string          `json:"goal_title"`
	TaskID      string          `json:"task_id"`
	RiskTier    string          `json:"risk_tier"`
	Summary     string          `json:"summary"`
	Preview     json.RawMessage `json:"preview"`
	RequestedAt string          `json:"requested_at"`
}

// ListApprovals handles GET /v1/approvals — everything waiting on this person.
func (h *GoalHandlers) ListApprovals(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())

	visible, err := h.deps.visibleProjects(r, user.ID)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}

	rows, err := h.deps.Pool.Query(r.Context(), `
		select a.id, a.goal_id, g.title, a.task_id, a.risk_tier, a.summary, a.preview, a.requested_at
		  from forge_approvals a
		  join forge_goals g on g.id = a.goal_id
		  join forge_projects p on p.id = g.project_id
		 where p.id = any($1) and a.decision = 'pending'
		 order by a.requested_at asc`, visible)
	if err != nil {
		WriteError(w, r, h.deps.Log, errs.Wrap("httpapi.ListApprovals", errs.CodeDatabaseUnavail, err))
		return
	}
	defer rows.Close()

	out := []ApprovalDTO{}
	for rows.Next() {
		var a ApprovalDTO
		var requested time.Time
		if err := rows.Scan(&a.ID, &a.GoalID, &a.GoalTitle, &a.TaskID,
			&a.RiskTier, &a.Summary, &a.Preview, &requested); err != nil {
			WriteError(w, r, h.deps.Log, errs.Wrap("httpapi.ListApprovals", errs.CodeDatabaseUnavail, err))
			return
		}
		a.RequestedAt = requested.UTC().Format(time.RFC3339)
		out = append(out, a)
	}
	WriteJSON(w, http.StatusOK, map[string]any{"approvals": out})
}

type decideRequest struct {
	Decision string `json:"decision"` // "approve" | "reject"
	Reason   string `json:"reason"`
}

// Decide handles POST /v1/approvals/{id}.
//
// The decision is attributed to the authenticated user and recorded on the
// timeline as an ActorHuman event with their account id. PRD SAF-05: an approval
// that cannot name the person who made it is not an approval.
func (h *GoalHandlers) Decide(w http.ResponseWriter, r *http.Request) {
	approvalID := r.PathValue("id")
	user, _ := UserFrom(r.Context())

	var req decideRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	var decision engine.ApprovalDecision
	switch req.Decision {
	case "approve":
		decision = engine.ApprovalApproved
	case "reject":
		decision = engine.ApprovalRejected
	default:
		WriteError(w, r, h.deps.Log, errs.New("httpapi.Decide", errs.CodeValidationFailed).
			WithDetail("decision must be \"approve\" or \"reject\", got %q", req.Decision))
		return
	}

	// Deciding a gate needs approval.decide, not merely read access. PRD SAF-05
	// names the accountable human, and this is where the system decides who is
	// allowed to be that human — a contributor may create the work and may not
	// sign it off.
	var goalOfApproval string
	if err := h.deps.Pool.QueryRow(r.Context(),
		`select goal_id from forge_approvals where id = $1`, approvalID).Scan(&goalOfApproval); err != nil {
		WriteError(w, r, h.deps.Log, errs.New("httpapi.Decide", errs.CodeNotFound).
			WithDetail("no approval %s", approvalID))
		return
	}
	if _, err := h.deps.requireGoalPermission(r, goalOfApproval, user.ID, access.PermApprovalDecide); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}

	now := h.deps.Clock.Now()
	var taskID, goalID string
	// The permission is checked above; `decision = 'pending'` in the UPDATE is
	// what makes the decision itself atomic. That guard is the one that matters
	// for concurrency: two people answering the same gate at once, where the
	// loser must be told rather than silently overwriting the winner. Moving the
	// permission out of the statement does not weaken it — a role revoked in the
	// microseconds between is not a threat anybody has.
	err := h.deps.Pool.QueryRow(r.Context(), `
		update forge_approvals
		   set decision = $2, decided_by = $3, decided_at = $4, decision_reason = $5
		 where id = $1 and decision = 'pending'
		returning task_id, goal_id`,
		approvalID, string(decision), user.ID, now, req.Reason).Scan(&taskID, &goalID)
	if err != nil {
		WriteError(w, r, h.deps.Log, errs.Wrap("httpapi.Decide", errs.CodeConflict, err).
			WithDetail("approval %s has already been decided", approvalID))
		return
	}

	// An approved task goes back to the QUEUE rather than continuing in place:
	// the worker that opened the gate is long gone, and a fresh one must pick it
	// up under a new lease.
	target := engine.StatusReady
	mut := engine.TaskMutation{}
	if decision == engine.ApprovalRejected {
		target = engine.StatusFailed
		mut.ErrorCode = string(errs.CodeForbidden)
		mut.ErrorDetail = "rejected by " + user.Email + ": " + req.Reason
	}
	task, err := h.repo.GetTask(r.Context(), h.deps.Pool, taskID)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	if err := h.repo.TransitionTask(r.Context(), h.deps.Pool, task, target, now, mut); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}

	kind := engine.EventApprovalGranted
	if decision == engine.ApprovalRejected {
		kind = engine.EventApprovalRejected
	}
	payload, _ := json.Marshal(map[string]any{"approval_id": approvalID, "reason": req.Reason})
	if err := h.repo.AppendEvent(r.Context(), h.deps.Pool, &engine.Event{
		ID: id.New(id.PrefixEvent), GoalID: goalID, TaskID: &taskID, Kind: kind,
		Actor: engine.ActorHuman, ActorID: &user.ID,
		Summary: string(decision) + " by " + user.Email + ". " + req.Reason,
		Payload: payload,
	}, now); err != nil {
		h.deps.Log.WarnWith(r.Context(), logx.EventApprovalOpened, err,
			"detail", "the decision was recorded but its timeline entry was lost")
	}

	h.deps.Log.Info(r.Context(), logx.EventApprovalOpened,
		"approval_id", approvalID, "decision", string(decision), "decided_by", user.ID)

	WriteJSON(w, http.StatusOK, map[string]any{
		"decision": string(decision),
		"message":  map[bool]string{true: "Approved. The task has been returned to the queue.", false: "Rejected."}[decision == engine.ApprovalApproved],
	})
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// loadGoal reads a goal the caller is permitted to see.
//
// Authorisation goes through the access service rather than into the query's
// WHERE clause. The difference matters: the permission is named at the call
// site, so a reader can tell that this is a READ and not a write, and a goal the
// caller cannot see is still reported exactly as one that does not exist.
func (h *GoalHandlers) loadGoal(r *http.Request, goalID, userID string) (*GoalDTO, error) {
	if _, err := h.deps.requireGoalPermission(r, goalID, userID, access.PermProjectRead); err != nil {
		return nil, err
	}
	row := h.deps.Pool.QueryRow(r.Context(), `
		select g.id, g.title, g.statement, g.status, g.autonomy, g.risk_tier,
		       g.tokens_spent, g.created_at, g.started_at, g.ended_at,
		       coalesce(g.outcome_summary, '')
		  from forge_goals g where g.id = $1`, goalID)

	dto, err := h.scanGoalRow(row)
	if err != nil {
		return nil, errs.New("httpapi.loadGoal", errs.CodeNotFound).
			WithDetail("no goal %s", goalID)
	}
	h.fillCounts(r, &dto)
	return &dto, nil
}

type scannable interface {
	Scan(dest ...any) error
}

func (h *GoalHandlers) scanGoalRow(row scannable) (GoalDTO, error) {
	var d GoalDTO
	var created time.Time
	var started, ended *time.Time
	if err := row.Scan(&d.ID, &d.Title, &d.Statement, &d.Status, &d.Autonomy, &d.RiskTier,
		&d.TokensSpent, &created, &started, &ended, &d.Outcome); err != nil {
		return d, err
	}
	d.CreatedAt = created.UTC().Format(time.RFC3339)
	if started != nil {
		s := started.UTC().Format(time.RFC3339)
		d.StartedAt = &s
	}
	if ended != nil {
		s := ended.UTC().Format(time.RFC3339)
		d.EndedAt = &s
	}
	return d, nil
}

// fillCounts adds task counts, pending approvals, and the avatar state.
func (h *GoalHandlers) fillCounts(r *http.Request, d *GoalDTO) {
	depth, err := h.queue.Depth(r.Context(), h.deps.Pool, d.ID)
	if err != nil {
		h.deps.Log.WarnWith(r.Context(), logx.EventHTTPRequest, err,
			"goal_id", d.ID, "detail", "task counts unavailable; the goal renders without them")
		return
	}
	var running bool
	for status, n := range depth {
		d.TasksTotal += n
		switch {
		case status.Active():
			d.Outstanding += n
			if status == engine.StatusRunning || status == engine.StatusClaimed {
				running = true
			}
		case status == engine.StatusSucceeded:
			d.TasksDone += n
		case status == engine.StatusFailed, status == engine.StatusCancelled:
			d.TasksFailed += n
		}
	}
	_ = h.deps.Pool.QueryRow(r.Context(),
		`select count(*) from forge_approvals where goal_id = $1 and decision = 'pending'`,
		d.ID).Scan(&d.Pending)

	state := persona.AvatarStateForGoal(d.Status, d.Pending > 0, running, false)
	d.AvatarState = string(state)
	d.StateLabel = state.Label()
}

var _ = db.ErrNoRows
