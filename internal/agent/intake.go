package agent

import (
	"context"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/pack"
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

// logger returns somewhere to write, never nil.
//
// The log field is optional by design, and several collaborators call their
// logger without checking. Handing them a discard logger keeps "nil is legal"
// true rather than true-until-a-collaborator-logs.
func (in *Intake) logger() *logx.Logger {
	if in.log != nil {
		return in.log
	}
	return logx.Discard()
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
	// Industry is the domain the new project works in — either the label the
	// product's selector shows ("Civil engineering") or the pack id ("civil").
	//
	// Empty means UNSTATED, which resolves to the `general` pack rather than to a
	// guess. See Draft for why nothing here tries to infer it.
	//
	// Meaningless together with ProjectID, and refused rather than ignored: the
	// industry belongs to the project, so passing one alongside an existing
	// project is a caller who believes they are setting something.
	Industry  string
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

	// The industry is a property of the PROJECT, so asking for one while naming an
	// existing project is a caller who thinks they are setting something. Refused
	// rather than dropped: EnsureProject returns early on a known project id, so
	// the value would otherwise vanish without a word.
	if req.ProjectID != "" && strings.TrimSpace(req.Industry) != "" {
		return nil, errs.New(op, errs.CodeValidationFailed).
			WithDetail("an industry cannot be set while adding a goal to the existing project %s — "+
				"the industry belongs to the project, and changing it would change the rules "+
				"under which its earlier work was done.\n"+
				"Change it deliberately with `forgectl project industry --project %s --set <industry>`, "+
				"or omit --industry to add this goal to the project as it is.",
				req.ProjectID, req.ProjectID)
	}

	// # Why an unstated industry is `general` and NOT a guess (2026-09-04)
	//
	// This used to pass the constant "software", which was wrong for most of what
	// this product is used for: a bracket goal was filed under a pack whose rules
	// are about merging code. The obvious replacement is to infer the domain from
	// the title and statement — and it is not done here, for two reasons.
	//
	// Draft calls no model. That is a stated property a few lines up and the
	// workbench depends on it: the goal id has to exist before the minutes of
	// planning start, so somebody who closes the tab still has something to
	// return to. An inference step would put a model call in front of the one
	// operation that is currently free.
	//
	// And `general` is not a fallback invented to fill the hole — it is the pack
	// whose entire definition is this situation: "unknown domain or missing
	// standards: autonomy is lower and expert review is triggered". A guessed
	// industry that lands wrong files work under rules nobody chose while looking
	// exactly like a stated one. Saying "unknown" is the honest answer and it is
	// already a first-class one.
	industry := strings.TrimSpace(req.Industry)
	if industry == "" && req.ProjectID == "" {
		industry = string(pack.General)
	}

	now := in.clock.Now()
	// One producer of projects, not two. The workbench also needs "a project to
	// put this in, making one if there is not one" when it keeps a geometry
	// variant (PRD VIS-04), and a second INSERT here would eventually drift from
	// that one on the part that is easiest to forget — the membership row,
	// without which the person who just created a project cannot see it.
	projectID, err := workspace.NewService(pool, in.clock, nil).
		EnsureProject(ctx, pool, req.ProjectID, req.OwnerID, req.Title, industry)
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
		// PRD RSN-02 and RSN-03, attached here for the same reason the hazard
		// source is: the pool arrives with the call. One store for both, so a
		// deployment cannot end up with the choice wired and the answer not.
		WithSettled(NewSettledStore(pool))

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
	// What the planner made of the domain, recorded where a person will see it.
	// Never applied — see recordIndustryReading.
	in.recordIndustryReading(ctx, pool, goal, result.SuggestedIndustry)
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

// recordIndustryReading writes what the planner made of the goal's domain into
// the project graph, when the project has not been given one.
//
// # Why this proposes and never applies
//
// The industry selects the rule set a project is worked under. It is STATED —
// in the workbench selector, or `goal new --industry` — and the whole point of
// reading the pack was to stop a constant deciding it for everybody.
//
// A guessed industry that became the rules would read in the record exactly like
// a chosen one, which is the defect this area removed wearing a different hat.
// So this writes a node and changes nothing: the project keeps whatever domain
// it was created in, and a person can act on the reading or ignore it. PRD
// RSN-02 permits a labelled assumption for low-risk exploration; this is one.
//
// # Why it costs nothing
//
// The suggestion rides on the planner's existing reply. There is no second model
// call, so a deployment that never looks at these nodes pays nothing for them.
//
// # Why the label is `assumed` and not `inferred`
//
// The node's KIND is assumption, and workspace.KindOf permits assumptions
// exactly one epistemic label: assumed. That is deliberate — "an assumption
// filed as observed is not an assumption" — and the rule is right even though
// `inferred` describes how the planner arrived at it. What is being assumed is
// the PACK: nobody stated a domain, so the work proceeds under `general`. How
// FORGE read the goal is the node's body, not its epistemic status.
//
// # Why a failure here is swallowed
//
// A note about the domain is not worth failing a plan the person waited minutes
// for. Logged at WARN so a deployment where every one of these vanishes does not
// look like one where the planner never suggested anything.
func (in *Intake) recordIndustryReading(ctx context.Context, pool *db.Pool, goal *engine.Goal, suggested string) {
	suggested = strings.TrimSpace(suggested)
	if suggested == "" {
		return
	}
	def, ok := pack.Lookup(suggested)
	// A suggestion outside the closed set is dropped rather than recorded. The
	// model is asked for one of nine names; anything else is it inventing a
	// domain, and a node naming a pack that does not exist would send somebody
	// looking for rules there are none of.
	if !ok || def.Industry == "" {
		if in.log != nil {
			in.log.Warn(ctx, logx.EventNodeAdded, "goal_id", goal.ID,
				"suggested", suggested,
				"detail", "the planner suggested a domain this build does not offer; not recorded")
		}
		return
	}
	// A non-nil logger, because Intake's own contract says nil is a legal value
	// and workspace.Service.Add calls s.log.Info unguarded. Passing in.log
	// straight through panics for any caller that took the documented option —
	// forgectl's tests build an intake without a logger, and so does anything
	// else that follows the comment on the field.
	ws := workspace.NewService(pool, in.clock, in.logger())
	current, err := ws.PackFor(ctx, pool, goal.ProjectID)
	if err != nil {
		return // the project's rules are unreadable; a note about them helps nobody
	}
	// Only when nobody has said. A project already working in a domain does not
	// need the planner's opinion about it, and writing one every time somebody
	// planned a goal would fill the graph with noise.
	if current.Pack != pack.General {
		return
	}
	if _, err := ws.Add(ctx, workspace.NewNode{
		ProjectID: goal.ProjectID,
		Kind:      workspace.KindAssumption,
		Title:     "Working as " + current.Industry + "; this looks like " + def.Industry + " work",
		Body: "No industry was stated for this project, so it is worked under the " +
			string(current.Pack) + " pack: " + current.Summary + "\n\n" +
			"Planning this goal, FORGE read the work as " + def.Industry + ". " +
			"Nothing has been changed by that reading — the domain decides which rules " +
			"apply and it is yours to state, not FORGE's to assume.\n\n" +
			"If it is right: forgectl project industry --project " + goal.ProjectID +
			" --set " + string(def.Pack) + "\n" +
			def.Industry + " would mean: " + def.Summary,
		Source:    "planner, while planning goal " + goal.ID,
		CreatedBy: goal.CreatedBy,
	}); err != nil && in.log != nil {
		in.log.WarnWith(ctx, logx.EventNodeAdded, err, "goal_id", goal.ID,
			"detail", "the planner's reading of the domain could not be recorded")
	}
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
