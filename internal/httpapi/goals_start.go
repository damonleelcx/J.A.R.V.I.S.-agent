package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/agent"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/access"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// The two endpoints in this file are what turns a conversation into work.
//
// # Why there are two of them rather than one
//
// The obvious shape is one call — "create this goal and run it". It was
// rejected. PRD AGT-02 requires a scoped plan and preview before material
// action, and AGT-04 forbids autonomy being raised without the person seeing it
// happen. A single call would mean the plan came into existence and started
// running inside one button press, and the person who pressed it would have
// authorised a list of tasks they never saw.
//
// So POST /v1/goals writes a DRAFT and plans it — nothing runs, nothing is
// claimable, no money is spent beyond the planner's own call — and
// POST /v1/goals/{id}/start is the separate, deliberate act. This is exactly
// how `forgectl goal new` and `forgectl goal start` already work, and both
// surfaces run the same agent.Intake underneath so they cannot drift apart.
//
// # Why the conversation still cannot start work by itself
//
// It cannot reach these endpoints. /v1/converse only ever emits a PROPOSAL; the
// browser renders it as a card with a button, and a person presses the button.
// That the two things now sit on one screen does not make them one act.

// createGoalRequest is work a person has decided to have done.
type createGoalRequest struct {
	Title     string `json:"title"`
	Statement string `json:"statement"`
	// RiskTier and Autonomy default to the same values `forgectl goal new`
	// defaults to. Stated in one place — see defaultsFor — so the terminal and
	// the browser cannot disagree about what an unspecified goal means.
	RiskTier  string `json:"risk_tier"`
	Autonomy  string `json:"autonomy"`
	ProjectID string `json:"project_id"`
}

// Field ceilings. These are not security controls — BodyLimit already bounds the
// request — they exist so a mis-wired client gets a named validation failure
// instead of a database error about a column width.
const (
	maxGoalTitle     = 200
	maxGoalStatement = 8000
)

// CreateGoal handles POST /v1/goals: draft the goal, plan it, run nothing.
//
// The response is deliberately the whole plan. A caller that only learned "goal
// created" would have to make a second round trip to show the person what they
// are about to authorise, and a UI that can render the Start button before the
// plan arrives is a UI that will eventually render it without one.
func (h *GoalHandlers) CreateGoal(w http.ResponseWriter, r *http.Request) {
	const op = "httpapi.CreateGoal"

	if h.intake == nil {
		WriteError(w, r, h.deps.Log, errs.New(op, errs.CodeConfigInvalid).
			WithDetail("no model is configured, so FORGE cannot plan a goal. "+
				"Set FORGE_LLM_API_KEY and restart the server, or create the goal from a "+
				"terminal with `forgectl goal new`."))
		return
	}

	var req createGoalRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	req.Statement = strings.TrimSpace(req.Statement)

	if req.Title == "" || req.Statement == "" {
		WriteError(w, r, h.deps.Log, errs.New(op, errs.CodeValidationFailed).
			WithDetail("a goal needs both a title and a statement of what to do"))
		return
	}
	if len(req.Title) > maxGoalTitle || len(req.Statement) > maxGoalStatement {
		WriteError(w, r, h.deps.Log, errs.New(op, errs.CodeValidationFailed).
			WithDetail("title must be at most %d characters and the statement at most %d",
				maxGoalTitle, maxGoalStatement))
		return
	}

	autonomy, risk := defaultsFor(req.Autonomy, req.RiskTier)
	user, _ := UserFrom(r.Context())

	// Planning is a model call and takes tens of seconds to minutes. The
	// deadline is derived from the model client's own timeout and set LONGER
	// than it, not shorter: a handler that dies first kills the call mid-retry
	// and reports a context deadline, which points the reader at the model
	// rather than at the timeout hierarchy that actually caused it. This is the
	// same bug that was fixed on /v1/converse.
	ctx, cancel := context.WithTimeout(r.Context(), h.deps.Config.LLM.RequestTimeout+15*time.Second)
	defer cancel()

	goal, err := h.intake.Draft(ctx, h.deps.Pool, agent.DraftRequest{
		OwnerID:   user.ID,
		ProjectID: req.ProjectID,
		Title:     req.Title,
		Statement: req.Statement,
		Autonomy:  autonomy,
		RiskTier:  risk,
	})
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}

	outcome, err := h.intake.Plan(ctx, h.deps.Pool, goal)
	if err != nil {
		// The draft survives, and the reader is told so by id. Rolling it back
		// would be tidier and less truthful: the goal exists, it is visible in
		// the console and to `forgectl goal show`, and pretending otherwise
		// would leave a row nobody was told about.
		h.deps.Log.WarnWith(r.Context(), logx.EventGoalPlanFailed, err,
			"goal_id", goal.ID, "user_id", user.ID)
		WriteError(w, r, h.deps.Log, errs.Wrap(op, errs.CodeOf(err), err).
			WithDetail("planning failed. Goal %s is saved as a draft with no tasks and "+
				"nothing is running. Inspect it with `forgectl goal show %s`.", goal.ID, goal.ID))
		return
	}

	dto, err := h.loadGoal(r, goal.ID, user.ID)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}

	body := map[string]any{
		"goal":  dto,
		"tasks": []TaskDTO{},
		// Stated rather than implied. A client must not have to infer from an
		// empty task list that nothing is running (PRD AGT-08).
		"running": false,
	}
	if outcome.ClarificationNeeded != "" {
		// The planner refused to guess. That is the planner working, so this is
		// a 201 with a question attached rather than an error: the draft is
		// real, it simply has no plan yet.
		body["clarification_needed"] = outcome.ClarificationNeeded
		h.deps.Log.Info(r.Context(), logx.EventGoalDrafted,
			"goal_id", goal.ID, "user_id", user.ID, "clarification", true)
		WriteJSON(w, http.StatusCreated, body)
		return
	}

	tasks := make([]TaskDTO, 0, len(outcome.Tasks))
	for _, t := range outcome.Tasks {
		deps, _ := h.repo.ListDependencies(r.Context(), h.deps.Pool, t.ID)
		tasks = append(tasks, toTaskDTO(t, deps))
	}
	body["tasks"] = tasks
	body["rationale"] = outcome.Rationale
	if outcome.Plan != nil {
		body["plan_version"] = outcome.Plan.Version
	}

	h.deps.Log.Info(r.Context(), logx.EventGoalDrafted,
		"goal_id", goal.ID, "user_id", user.ID,
		"tasks", len(tasks), "risk_tier", string(risk), "autonomy", string(autonomy))
	WriteJSON(w, http.StatusCreated, body)
}

// StartGoal handles POST /v1/goals/{id}/start — the material act.
func (h *GoalHandlers) StartGoal(w http.ResponseWriter, r *http.Request) {
	const op = "httpapi.StartGoal"

	goalID := r.PathValue("id")
	user, _ := UserFrom(r.Context())

	// Starting is a separate permission from creating: PRD AGT-02 makes planning
	// and running two deliberate acts, and a contributor who may draft a plan is
	// not necessarily the person who may set workers loose on it.
	goal, err := h.loadGoalFor(r, goalID, user.ID, access.PermGoalStart)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}

	// A goal with no tasks would activate into a state that looks like work and
	// is not: status "active", nothing claimable, nothing ever finishing. PRD
	// AGT-08 makes running and proposed distinct states, so refusing here is
	// the requirement rather than defensiveness.
	tasks, err := h.repo.ListTasks(r.Context(), h.deps.Pool, goal.ID)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	if len(tasks) == 0 {
		WriteError(w, r, h.deps.Log, errs.New(op, errs.CodeValidationFailed).
			WithDetail("goal %s has no tasks, so activating it would produce a goal that "+
				"looks like it is running and never does anything. Plan it first.", goal.ID))
		return
	}

	// # Why an already-active goal is a success and not an error
	//
	// Pressing Start twice is an ordinary thing a person does: a double click, a
	// tab left open in another window, a retry after a slow response. The
	// caller's intent — "have this goal be running" — is already satisfied, so
	// answering with a failure would be answering a question nobody asked.
	//
	// This was not the first behaviour. The state machine refused draft→active
	// on an already-active goal with INVARIANT_VIOLATED, which the error
	// registry renders as a 500 reading "it indicates a logic defect, not a user
	// error" — shown to somebody who had simply clicked twice. The same
	// reasoning already governs POST /v1/auth/sign-out in this package: a
	// request that arrives with nothing left to do has achieved its purpose.
	//
	// Any OTHER status is a real conflict and says so, naming the state, because
	// "start a goal that has already ended" is not the same request at all.
	message := "Started. Its tasks are now claimable — they execute when a FORGE " +
		"worker is running (`make work`, or the forge-worker binary)."

	switch goal.Status {
	case engine.GoalActive:
		message = "Already running — this goal was started earlier. Nothing changed."
	case engine.GoalDraft:
		if err := h.intakeStart(r, goal, user.ID); err != nil {
			WriteError(w, r, h.deps.Log, err)
			return
		}
		h.deps.Log.Info(r.Context(), logx.EventGoalStarted,
			"goal_id", goal.ID, "started_by", user.ID, "tasks", len(tasks))
	default:
		WriteError(w, r, h.deps.Log, errs.New(op, errs.CodeConflict).
			WithDetail("goal %s is %s, not a draft, so it cannot be started. "+
				"Only a drafted goal can be activated.", goal.ID, goal.Status))
		return
	}

	dto, err := h.loadGoal(r, goal.ID, user.ID)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"goal": dto,
		// Said plainly because it is the one thing that surprises people: an
		// active goal executes only while a worker process is running, and a
		// deployment with no worker will sit at "active" forever looking healthy.
		"message": message,
	})
}

// intakeStart activates, whether or not a model is configured.
//
// Activation touches no model, so a deployment with no FORGE_LLM_API_KEY must
// still be able to start a goal that was planned elsewhere. Building the applier
// directly here rather than requiring the Intake keeps that true.
func (h *GoalHandlers) intakeStart(r *http.Request, goal *engine.Goal, userID string) error {
	applier := agent.NewPlanApplier(h.repo, h.queue,
		engine.NewBudgetGuard(h.deps.Config.Engine), h.deps.Clock)
	return applier.Activate(r.Context(), h.deps.Pool, goal, engine.ActorHuman, &userID)
}

// loadGoalFor reads the engine's own view of a goal, checking a permission.
//
// Separate from loadGoal, which returns the console's DTO: activation needs the
// domain object, because the transition is validated against the status that was
// actually read rather than against a string that travelled through JSON.
//
// The permission is a parameter rather than fixed, because reading a goal and
// starting one are different acts (PRD AGT-02) and this is used for both. It was
// previously scoped by `p.owner_id = $caller`; membership decides now.
func (h *GoalHandlers) loadGoalFor(r *http.Request, goalID, userID string, p access.Permission) (*engine.Goal, error) {
	if _, err := h.deps.requireGoalPermission(r, goalID, userID, p); err != nil {
		return nil, err
	}
	var g engine.Goal
	var status, autonomy, risk string
	err := h.deps.Pool.QueryRow(r.Context(), `
		select g.id, g.project_id, g.created_by, g.title, g.statement, g.status,
		       g.autonomy, g.risk_tier, g.created_at
		  from forge_goals g where g.id = $1`, goalID).
		Scan(&g.ID, &g.ProjectID, &g.CreatedBy, &g.Title, &g.Statement, &status,
			&autonomy, &risk, &g.CreatedAt)
	if err != nil {
		return nil, errs.New("httpapi.loadGoalFor", errs.CodeNotFound).
			WithDetail("no goal %s", goalID)
	}
	g.Status = engine.GoalStatus(status)
	g.Autonomy = engine.Autonomy(autonomy)
	g.RiskTier = engine.RiskTier(risk)
	return &g, nil
}

// defaultsFor resolves autonomy and risk, rejecting nothing and inventing
// nothing: an unrecognised value is left as-is so agent.Intake's own validation
// names it, rather than being quietly replaced by a default the caller did not
// ask for. Silently downgrading a risk tier would be the worst possible way to
// handle a typo.
func defaultsFor(autonomy, risk string) (engine.Autonomy, engine.RiskTier) {
	if strings.TrimSpace(autonomy) == "" {
		autonomy = string(engine.AutonomySandboxExecute)
	}
	if strings.TrimSpace(risk) == "" {
		risk = string(engine.RiskR1)
	}
	return engine.Autonomy(autonomy), engine.RiskTier(risk)
}
