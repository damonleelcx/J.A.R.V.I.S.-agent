package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/agent"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/access"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
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
	// Industry is the domain a NEW project is created in — the label the
	// selector shows ("Civil engineering") or the pack id ("civil"). Ignored
	// nowhere: sent alongside project_id it is REFUSED, because the industry
	// belongs to the project and a caller passing both believes they are
	// setting something. See agent.Intake.Draft.
	//
	// Omitted means unstated, which is the `general` pack rather than a guess.
	Industry string `json:"industry"`
	// Build plans the statement as a BUILD of a model rather than as work
	// (Phase 2, stage A1): one task per step, each waiting for the step before,
	// run by forge-worker with the CAD kernel and kept as a version of the design.
	// The same agent.Intake.PlanBuild `forgectl goal new --build` calls.
	//
	// A field on this endpoint rather than a sibling route, because everything
	// around the plan is identical — the draft, the permission, the refusal
	// that leaves the draft named, and above all that nothing runs until
	// POST /v1/goals/{id}/start. A second route would be a second copy of those,
	// and the copy is where one of them would be forgotten.
	Build bool `json:"build"`
	// MaxTokens is the goal's own token ceiling, build or not. Omitted, the goal
	// inherits the engine's (FORGE_MAX_TOKENS_PER_GOAL); given, it must be positive
	// and not above the engine's, or the request is refused before anything is
	// written or any model is asked — agent.Intake.Draft holds the rule, so
	// `forgectl goal new --max-tokens` refuses the same values.
	//
	// A pointer, so an explicit 0 is refused rather than read as "not given".
	MaxTokens *int64 `json:"max_tokens"`
}

// replanRequest is POST /v1/goals/{id}/plan's optional body.
type replanRequest struct {
	// Build replans the draft as a build. ‼️ Not remembered from the first
	// attempt: nothing on a goal row says it was meant as a build, so a build
	// whose planning tripped must be replanned with build:true, or it comes back
	// as ordinary tasks the executor runs instead of steps the kernel builds.
	Build bool `json:"build"`
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

	// ‼️ A named project must be one the caller may plan work in, checked BEFORE
	// anything is written or any model is asked.
	//
	// Nothing checked it. Draft hands the id to EnsureProject, which returns early
	// on any id it is given, so anyone signed in could write a draft goal into a
	// stranger's project and have the planner's call run on their behalf — and
	// was then told 404, because the goal it had just written was one it could
	// not read. The same project-scoped check every other goal endpoint makes
	// (goal.create, as Replan asks for), and the same answer: a project the
	// caller is not in is NOT FOUND, never FORBIDDEN.
	// docs/bugfix/2026-09-15-a-goal-could-be-drafted-into-a-project-its-caller-was-not-in.md
	if req.ProjectID != "" {
		if err := h.deps.requirePermission(r, req.ProjectID, user.ID, access.PermGoalCreate); err != nil {
			WriteError(w, r, h.deps.Log, err)
			return
		}
	}

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
		Industry:  strings.TrimSpace(req.Industry),
		Title:     req.Title,
		Statement: req.Statement,
		Autonomy:  autonomy,
		RiskTier:  risk,
		MaxTokens: req.MaxTokens,
	})
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}

	plan := h.intake.Plan
	if req.Build {
		plan = h.intake.PlanBuild
	}
	outcome, err := plan(ctx, h.deps.Pool, goal)
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
		// Whether the tasks are the steps of a build. Echoed, so a client that
		// renders the plan says what starting it will do.
		"build": req.Build,
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

// Replan handles POST /v1/goals/{id}/plan.
//
// # Why the workbench needs this and not only the terminal
//
// Planning is a model call of one to three minutes, and the workbench is where
// most goals are drafted. When it trips, what the person is left with is a draft
// with no tasks and a Start button that now refuses — correctly, because
// starting it would produce a goal running with nothing to run. Without a way to
// plan it again, the only recovery from a browser is to describe the whole thing
// over.
//
// It needs goal.create rather than goal.start: replanning DRAFTS work, it does
// not authorise any. Starting stays the separate act (PRD AGT-02).
func (h *GoalHandlers) Replan(w http.ResponseWriter, r *http.Request) {
	const op = "httpapi.Replan"

	if h.intake == nil {
		WriteError(w, r, h.deps.Log, errs.New(op, errs.CodeConfigInvalid).
			WithDetail("no model is configured, so FORGE cannot plan. Set FORGE_LLM_API_KEY and restart."))
		return
	}
	user, _ := UserFrom(r.Context())
	goalID := r.PathValue("id")

	// The body is optional: this endpoint took none before build goals existed,
	// and a caller that still sends none gets the ordinary plan it always got.
	var req replanRequest
	if r.ContentLength != 0 {
		if err := DecodeJSON(w, r, &req); err != nil && !errors.Is(err, io.EOF) {
			WriteError(w, r, h.deps.Log, err)
			return
		}
	}

	goal, err := h.loadGoalFor(r, goalID, user.ID, access.PermGoalCreate)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}

	// The same budget hierarchy as CreateGoal: longer than the model client's
	// own timeout, never shorter, or the handler kills the call mid-retry and
	// reports a deadline that points at the model rather than at the timeout
	// that caused it.
	ctx, cancel := context.WithTimeout(r.Context(), h.deps.Config.LLM.RequestTimeout+15*time.Second)
	defer cancel()

	replan := h.intake.Replan
	if req.Build {
		replan = h.intake.ReplanBuild
	}
	outcome, err := replan(ctx, h.deps.Pool, goal)
	if err != nil {
		h.deps.Log.WarnWith(r.Context(), logx.EventGoalPlanFailed, err,
			"goal_id", goalID, "user_id", user.ID)
		WriteError(w, r, h.deps.Log, err)
		return
	}
	if outcome.ClarificationNeeded != "" {
		// A question is the planner working correctly (PRD AGT-02), and the goal
		// stays a draft. 200 with the question rather than an error, for the
		// same reason CreateGoal does it.
		WriteJSON(w, http.StatusOK, map[string]any{
			"goal_id":              goal.ID,
			"clarification_needed": outcome.ClarificationNeeded,
			"tasks":                []any{},
			"note": "The planner needs an answer before it can plan this. The goal is still a draft; " +
				"answer the question in its statement and plan it again.",
		})
		return
	}
	h.deps.Log.Info(r.Context(), logx.EventGoalDrafted,
		"goal_id", goal.ID, "user_id", user.ID, "tasks", len(outcome.Tasks), "replanned", true)

	tasks := []TaskDTO{}
	for _, t := range outcome.Tasks {
		deps, _ := h.repo.ListDependencies(r.Context(), h.deps.Pool, t.ID)
		tasks = append(tasks, toTaskDTO(t, deps))
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"goal_id":   goal.ID,
		"tasks":     tasks,
		"rationale": outcome.Rationale,
		"note": "Planned. Nothing runs until it is started — that is the separate act, and it is " +
			"deliberately separate (PRD AGT-02).",
	})
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
		engine.NewBudgetGuard(h.deps.Config.Engine), h.deps.Clock).
		WithWorkspace(workspace.NewService(h.deps.Pool, h.deps.Clock, h.deps.Log), h.deps.Log)
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
