package agent

import (
	"context"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/access"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
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
	projectID := req.ProjectID
	if projectID == "" {
		projectID = id.New(id.PrefixProject)
		if _, err := pool.Exec(ctx,
			`insert into forge_projects (id, owner_id, name, pack, created_at, updated_at)
			 values ($1,$2,$3,'software',$4,$4)`,
			projectID, req.OwnerID, req.Title, now); err != nil {
			return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
		}
		// The creator becomes the project's owner in the membership table, which
		// is what authorisation reads (PRD SEC-02). owner_id above records who
		// made it; without this row they could not see what they just created.
		if err := access.NewService(pool, in.clock, nil).EnsureOwner(ctx, pool, projectID, req.OwnerID); err != nil {
			return nil, err
		}
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
	result, err := in.planner.Plan(ctx, goal, nil, "")
	if err != nil {
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
