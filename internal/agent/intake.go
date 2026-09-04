package agent

import (
	"context"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Intake turns described work into a planned goal.
//
// # Why this exists
//
// Two surfaces now start work: `forgectl goal new` and the workbench's "Start
// this" button. Both must do the SAME thing — create a project if needed, write
// a draft goal, plan it, write the task DAG — and two implementations of that
// sequence would drift. The one that drifted would be the one nobody was
// watching, and it would drift in the direction of whichever surface got the
// next bug report.
//
// # Why three steps rather than one call
//
// The split is a product rule, not a convenience:
//
//	Draft    writes the goal. No model, no cost, nothing runs.
//	Plan     asks the planner and writes the DAG. Still nothing runs.
//	Start    activates the goal. THIS is the material act.
//
// PRD AGT-02 requires a scoped plan and preview before material action, and
// AGT-04 forbids autonomy being raised silently. A single create-plan-and-run
// call would collapse all three into one press of one button, and the person who
// pressed it would never have seen the plan they authorised. The CLI already
// works this way (`--start` is an explicit opt-in); the web surface must not be
// laxer than the terminal.
type Intake struct {
	planner *Planner
	applier *PlanApplier
	clock   clock.Clock
	// log is optional. Nil is a legal caller — forgectl's tests build an intake
	// without one — and the hazard load then happens silently rather than not at
	// all.
	log *logx.Logger
}

// NewIntake wires the planner and the applier the same way for every caller.
func NewIntake(client llm.Client, char persona.Character, engineCfg config.EngineConfig, clk clock.Clock) *Intake {
	return &Intake{
		planner: NewPlanner(client, char),
		applier: NewPlanApplier(engine.NewRepository(), engine.NewQueue(),
			engine.NewBudgetGuard(engineCfg), clk),
		clock: clk,
	}
}

// WithLog gives the intake somewhere to record what planning did, notably the
// hazard load that PRD SAF-02 requires at r3 and above.
func (i *Intake) WithLog(log *logx.Logger) *Intake { i.log = log; return i }

// WithCharacters makes planning honour the project's critique intensity
// (PRD RSN-04). Forwarded to the planner: Intake owns one, and a caller that had
// to reach through to configure it would be a caller that can forget to.
func (i *Intake) WithCharacters(s *CharacterStore) *Intake {
	i.planner = i.planner.WithCharacters(s)
	return i
}

// DraftRequest is the work as described by a person.
type DraftRequest struct {
	// OwnerID is the account the goal belongs to. Required: PRD SAF-05 — work
	// that cannot name whose it is has no authority behind it.
	OwnerID string
	// ProjectID is an existing project, or empty to create one named after the
	// goal.
	ProjectID string
	Title     string
	Statement string
	Autonomy  engine.Autonomy
	RiskTier  engine.RiskTier
}

// PlanOutcome is what planning produced.
//
// ClarificationNeeded being set is a SUCCESS, not a failure: the planner refused
// to guess. The goal stays a draft with no tasks, and the caller is expected to
// put the question to the person rather than to bury it.
type PlanOutcome struct {
	Plan                *engine.Plan
	Tasks               []*engine.Task
	Rationale           string
	ClarificationNeeded string
	Result              *PlanResult
}

// Draft writes a goal, and a project to hold it if none was named.
//
// No model is called here, so this is fast and free. That matters for the
// workbench: the goal id exists before the minutes of planning start, so a
// person who closes the tab mid-plan still has something to come back to rather
// than nothing at all.
func (in *Intake) Draft(ctx context.Context, pool *db.Pool, req DraftRequest) (*engine.Goal, error) {
	const op = "agent.Intake.Draft"

	if req.OwnerID == "" || req.Title == "" || req.Statement == "" {
		return nil, errs.New(op, errs.CodeValidationFailed).
			WithDetail("a goal needs an owner, a title and a statement")
	}
	if !req.Autonomy.Valid() {
		return nil, errs.New(op, errs.CodeValidationFailed).
			WithDetail("autonomy %q is not recognised", req.Autonomy)
	}
	if !req.RiskTier.Valid() {
		return nil, errs.New(op, errs.CodeValidationFailed).
			WithDetail("risk tier %q is not recognised", req.RiskTier)
	}

	now := in.clock.Now()
	// One producer of projects, not two. The workbench also needs "a project to
	// put this in, making one if there is not one" when it keeps a geometry
	// variant (PRD VIS-04), and a second INSERT here would eventually drift from
	// that one on the part that is easiest to forget — the membership row,
	// without which the person who just created a project cannot see it.
	projectID, err := workspace.NewService(pool, in.clock, nil).
		EnsureProject(ctx, pool, req.ProjectID, req.OwnerID, req.Title, "software")
	if err != nil {
		return nil, err
	}

	goal := &engine.Goal{
		ID: id.New(id.PrefixGoal), ProjectID: projectID, CreatedBy: req.OwnerID,
		Title: req.Title, Statement: req.Statement, Status: engine.GoalDraft,
		Autonomy: req.Autonomy, RiskTier: req.RiskTier,
		CreatedAt: now, UpdatedAt: now,
	}
	if _, err := pool.Exec(ctx, `
		insert into forge_goals (id, project_id, created_by, title, statement, status,
			autonomy, risk_tier, completion_criteria, created_at, updated_at)
		values ($1,$2,$3,$4,$5,'draft',$6,$7,'[]'::jsonb,$8,$8)`,
		goal.ID, goal.ProjectID, goal.CreatedBy, goal.Title, goal.Statement,
		string(goal.Autonomy), string(goal.RiskTier), now); err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	return goal, nil
}

// Plan decomposes a drafted goal and writes the resulting task DAG.
//
// The goal remains a draft afterwards. Planning is not starting.
func (in *Intake) Plan(ctx context.Context, pool *db.Pool, goal *engine.Goal) (*PlanOutcome, error) {
	// PRD SAF-02. The hazard source is attached here rather than in NewIntake
	// because the pool arrives with the call — the same reason the project is
	// created against this pool a few lines up rather than one held on the
	// struct. Below r3 the planner never asks it anything.
	in.planner = in.planner.
		WithHazards(workspace.NewService(pool, in.clock, in.log), in.log).
		// PRD RSN-03, attached here for the same reason the hazard source is: the
		// pool arrives with the call.
		WithChoices(NewChoiceStore(pool))

	result, err := in.planner.Plan(ctx, goal, nil, "")
	if err != nil {
		return nil, err
	}
	// PRD RSN-02. Recorded before it is returned, because a question that is only
	// printed holds nothing: `goal replan` would ask the model again, and a
	// second roll that happened not to ask would produce a plan built on the
	// ambiguity nobody resolved.
	if err := recordQuestion(ctx, pool, goal.ID, result.ClarificationNeeded); err != nil {
		return nil, err
	}
	if result.ClarificationNeeded != "" {
		// Returned rather than raised. A question is the planner working
		// correctly, and an error would push the caller into treating it as a
		// failure to retry.
		return &PlanOutcome{ClarificationNeeded: result.ClarificationNeeded, Result: result}, nil
	}
	plan, tasks, err := in.applier.Apply(ctx, pool, goal, result, "planner")
	if err != nil {
		return nil, err
	}
	return &PlanOutcome{
		Plan: plan, Tasks: tasks, Rationale: result.Rationale, Result: result,
	}, nil
}

// Replan plans a draft goal that has none, so a planning failure is recoverable.
//
// # The failure this exists for
//
// Planning is a model call taking one to three minutes against a handler budget
// a little longer than that. When it trips — a slow provider, a retry storm, a
// tab closed mid-plan — Draft has already committed the goal and Plan has
// written nothing, so what survives is a draft with no tasks. Before this there
// was no way forward from there: activating it would start a goal with nothing
// to do, and the only recovery was to write the goal again under a new id and
// abandon the first.
//
// # What it refuses, and why
//
// Only a DRAFT with no tasks. Planning a goal that already has them is a
// different operation with different hazards — the existing tasks may have
// dependencies, may have been claimed, may have produced artifacts — and
// replacing a live plan needs a way to retire what is already there. This build
// has no such thing, and a command that silently added a second plan beside the
// first would leave two sets of tasks racing for the same goal.
//
// So the boundary is the recoverable state and nothing wider, and the refusal
// says which of the two conditions failed rather than a single unhelpful "no".
func (in *Intake) Replan(ctx context.Context, pool *db.Pool, goal *engine.Goal) (*PlanOutcome, error) {
	const op = "agent.Intake.Replan"

	if goal.Status != engine.GoalDraft {
		return nil, errs.New(op, errs.CodeConflict).
			WithDetail("goal %s is %s, not a draft. Replanning exists to recover a plan that never "+
				"landed; changing the plan of a goal that is already running would leave two sets of "+
				"tasks racing for it, and this build has no way to retire the first.", goal.ID, goal.Status)
	}
	var tasks int
	if err := pool.QueryRow(ctx,
		`select count(*) from forge_tasks where goal_id = $1`, goal.ID).Scan(&tasks); err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	if tasks > 0 {
		return nil, errs.New(op, errs.CodeConflict).
			WithDetail("goal %s already has %d task(s), so its plan did land. Replanning would add a "+
				"second plan beside the first rather than replacing it. Start the goal, or create a "+
				"new one if the plan is wrong.", goal.ID, tasks)
	}
	return in.Plan(ctx, pool, goal)
}

// Start activates a planned goal so its tasks become claimable.
//
// byID names the account that decided this, where the surface knows it. The web
// surface always does; a terminal operator has no session to name and passes
// nil. Either way the transition is recorded as a HUMAN act, because it is one.
func (in *Intake) Start(ctx context.Context, pool *db.Pool, goal *engine.Goal, byID *string) error {
	return in.applier.Activate(ctx, pool, goal, engine.ActorHuman, byID)
}

// PlannerModel names the model that will do the planning, so a caller can say
// which one it is waiting on rather than showing an anonymous spinner.
func (in *Intake) PlannerModel() string { return in.planner.client.ModelFor(llm.RolePlanner) }
