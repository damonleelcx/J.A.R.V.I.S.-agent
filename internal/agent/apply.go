package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// PlanApplier writes a validated plan into the task DAG.
type PlanApplier struct {
	repo   *engine.Repository
	queue  *engine.Queue
	budget *engine.BudgetGuard
	clock  clock.Clock
	// workspace files the labelled assumption when low-risk work starts with an
	// unanswered question (PRD RSN-02). Optional: without it the work still
	// proceeds and the note is simply not filed, which is the right failure for
	// bookkeeping attached to exploration.
	workspace *workspace.Service
	log       *logx.Logger
}

// NewPlanApplier returns an applier.
func NewPlanApplier(repo *engine.Repository, queue *engine.Queue, budget *engine.BudgetGuard, clk clock.Clock) *PlanApplier {
	return &PlanApplier{repo: repo, queue: queue, budget: budget, clock: clk}
}

// WithWorkspace lets the applier label an assumption when a low-risk goal starts
// with a question outstanding (PRD RSN-02).
func (a *PlanApplier) WithWorkspace(ws *workspace.Service, log *logx.Logger) *PlanApplier {
	a.workspace = ws
	a.log = log
	return a
}

// actorID renders the optional actor for a node's created_by.
func actorID(byID *string) string {
	if byID == nil {
		return ""
	}
	return *byID
}

// logAssumptionFailure says the note was not filed, loudly, without stopping the
// work it describes.
func (a *PlanApplier) logAssumptionFailure(ctx context.Context, goal *engine.Goal, err error) {
	if a.log == nil {
		return
	}
	a.log.WarnWith(ctx, logx.EventAssumptionUnfiled, err, "goal_id", goal.ID,
		"detail", "this goal started with an unanswered question and the assumption could not "+
			"be written to the project graph; the work is proceeding on something nobody recorded")
}

// Apply writes a plan and its tasks, superseding any live plan for the goal.
//
// # Why the whole thing is one transaction
//
// A half-applied plan is worse than no plan: the goal has some of its tasks,
// none of the dependencies that were meant to order them, and no record of what
// was intended. It would run — that is the dangerous part — and produce work
// that nobody planned.
//
// # Why an existing task key is reused rather than recreated
//
// A replan usually changes part of a plan, not all of it. A task whose key
// already exists keeps its row, and therefore keeps its completed work, its
// checkpoints, and its place in the timeline. Recreating it would silently
// discard work that was already done and paid for.
func (a *PlanApplier) Apply(ctx context.Context, pool *db.Pool, goal *engine.Goal, plan *PlanResult, author string) (*engine.Plan, []*engine.Task, error) {
	const op = "agent.PlanApplier.Apply"

	if err := plan.Validate(); err != nil {
		return nil, nil, err
	}
	if plan.ClarificationNeeded != "" {
		return nil, nil, errs.New(op, errs.CodeValidationFailed).
			WithDetail("the planner asked a question rather than producing a plan: %s", plan.ClarificationNeeded)
	}

	now := a.clock.Now()
	var created *engine.Plan
	var tasks []*engine.Task

	err := db.InTx(ctx, pool, func(tx pgx.Tx) error {
		var version int
		if err := tx.QueryRow(ctx,
			`select coalesce(max(version), 0) + 1 from forge_plans where goal_id = $1`,
			goal.ID).Scan(&version); err != nil {
			return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
		}
		// Supersede the live plan first. The partial unique index permits only
		// one live plan per goal, so this ordering is not stylistic — inserting
		// first would violate it.
		if _, err := tx.Exec(ctx,
			`update forge_plans set superseded_at = $2 where goal_id = $1 and superseded_at is null`,
			goal.ID, now); err != nil {
			return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
		}

		created = &engine.Plan{
			ID: id.New(id.PrefixPlan), GoalID: goal.ID, Version: version,
			Rationale: plan.Rationale, Author: author, CreatedAt: now,
		}
		if _, err := tx.Exec(ctx,
			`insert into forge_plans (id, goal_id, version, rationale, author, created_at)
			 values ($1,$2,$3,$4,$5,$6)`,
			created.ID, created.GoalID, created.Version, created.Rationale, created.Author, created.CreatedAt); err != nil {
			return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
		}

		// Existing tasks by idempotency key, so a replan preserves work.
		existing := map[string]string{} // key -> task id
		rows, err := tx.Query(ctx,
			`select idempotency_key, id from forge_tasks where goal_id = $1`, goal.ID)
		if err != nil {
			return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
		}
		for rows.Next() {
			var key, tid string
			if err := rows.Scan(&key, &tid); err != nil {
				rows.Close()
				return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
			}
			existing[key] = tid
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
		}

		keyToID := map[string]string{}
		newCount := 0

		for _, pt := range plan.Tasks {
			if tid, reused := existing[pt.Key]; reused {
				keyToID[pt.Key] = tid
				continue
			}
			if breach := a.budget.CheckTaskCreation(goal, 0); breach != nil {
				return breach.Error()
			}

			tier := engine.RiskTier(pt.RiskTier)
			if !tier.Valid() {
				tier = engine.RiskR1
			}
			// A task may never exceed its goal's ceiling. A planner that tiers
			// something above it is clamped rather than trusted — and clamping
			// DOWN is safe, because the gate is then stricter, not looser.
			if !goal.RiskTier.AtLeast(tier) {
				tier = goal.RiskTier
			}

			t := &engine.Task{
				ID: id.New(id.PrefixTask), GoalID: goal.ID, PlanID: created.ID,
				Title: pt.Title, Instruction: pt.Instruction,
				Inputs: defaultObject(pt.Inputs), ExpectedOutput: defaultObject(pt.ExpectedOutput),
				// Every task starts pending. PromoteReadyTasks decides what is
				// runnable, from the edges — so readiness has one author.
				Status:           engine.StatusPending,
				IdempotencyKey:   pt.Key,
				MaxAttempts:      5,
				NotBefore:        now,
				Priority:         100,
				RiskTier:         tier,
				RequiresApproval: tier.RequiresApproval(),
				CreatedAt:        now, UpdatedAt: now,
			}
			if err := a.repo.CreateTask(ctx, tx, t, nil); err != nil {
				return err
			}
			keyToID[pt.Key] = t.ID
			tasks = append(tasks, t)
			newCount++
		}

		// Edges are written after every node exists, because a dependency may
		// point forward in the list.
		for _, pt := range plan.Tasks {
			for _, depKey := range pt.DependsOn {
				depID, ok := keyToID[depKey]
				if !ok {
					return errs.New(op, errs.CodeInvariantViolated).
						WithDetail("task %q depends on %q, which validation accepted but which has no row", pt.Key, depKey)
				}
				if _, err := tx.Exec(ctx,
					`insert into forge_task_deps (task_id, depends_on_id) values ($1,$2)
					 on conflict do nothing`, keyToID[pt.Key], depID); err != nil {
					return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
				}
			}
		}

		if err := a.budget.RecordTaskCreated(ctx, tx, goal.ID, newCount); err != nil {
			return err
		}
		if _, err := a.queue.PromoteReadyTasks(ctx, tx, goal.ID, now); err != nil {
			return err
		}

		ev := &engine.Event{
			GoalID: goal.ID, Kind: engine.EventPlanCreated, Actor: engine.ActorPlanner,
			Summary: fmt.Sprintf("Plan v%d: %d task(s), %d new. %s",
				version, len(plan.Tasks), newCount, plan.Rationale),
		}
		payload, _ := json.Marshal(map[string]any{
			"plan_id": created.ID, "version": version,
			"tasks_total": len(plan.Tasks), "tasks_new": newCount,
			"model": plan.Model,
		})
		ev.Payload = payload
		return a.repo.AppendEvent(ctx, tx, ev, now)
	})
	if err != nil {
		return nil, nil, err
	}
	return created, tasks, nil
}

// Activate moves a goal from draft to active so its tasks become claimable.
//
// # Why the actor is a parameter
//
// Activation is the moment work becomes real: before it, the goal is a plan
// nobody has authorised; after it, workers may claim tasks and spend money. PRD
// AGT-07 requires consequential transitions to carry the named human authority,
// and AGT-08 requires proposed and running to be distinguishable states with a
// record of what moved between them.
//
// Until the workbench gained a "Start this" button this transition wrote nothing
// to the timeline at all, so a goal simply appeared to be running and no reader
// could tell who had decided that. byID is the account id where one is known
// (the web surface always knows it) and nil where it is genuinely not — a
// terminal operator holding the database credentials has no session to name.
func (a *PlanApplier) Activate(ctx context.Context, pool *db.Pool, goal *engine.Goal, by engine.Actor, byID *string) error {
	const op = "agent.PlanApplier.Activate"

	if err := engine.ValidateGoalTransition(goal.Status, engine.GoalActive); err != nil {
		return err
	}
	// A goal with no tasks cannot be started.
	//
	// This is the state a failed plan leaves behind: Draft commits the goal,
	// planning trips on its way to Apply, and what survives is a draft with
	// nothing in it. Activating that produced a goal that was "running" with
	// nothing to run — indistinguishable, from every surface, from one whose
	// work had not started yet.
	//
	// Refused with the thing to do instead. Recovering is a replan, and until
	// there was one this refusal would have been a dead end.
	var tasks int
	if err := pool.QueryRow(ctx,
		`select count(*) from forge_tasks where goal_id = $1`, goal.ID).Scan(&tasks); err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	if tasks == 0 {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("goal %s has no tasks, so starting it would produce a goal that is running with "+
				"nothing to run. Its plan never landed — planning is a model call and can time out. "+
				"Plan it again with `forgectl goal replan %s`.", goal.ID, goal.ID)
	}
	// PRD RSN-02: clarification before consequential work.
	//
	// Checked here rather than in either caller, because forgectl and the HTTP
	// API both pass through this function and a gate implemented twice is a gate
	// with one version of it out of date.
	hold, err := clarificationFor(ctx, pool, goal.ID)
	if err != nil {
		return err
	}
	assumption, err := gateOnClarification(hold, goal)
	if err != nil {
		return err
	}
	if assumption != "" {
		// Low-risk exploration proceeds, and what it rests on is written down.
		// A failure to file the note does not stop the work — see labelAssumption.
		if err := labelAssumption(ctx, pool, a.workspace, goal, assumption, actorID(byID)); err != nil {
			a.logAssumptionFailure(ctx, goal, err)
		}
	}
	// PRD RSN-03: an open choice holds consequential work.
	//
	// Here for the same reason the clarification gate is here — forgectl and the
	// HTTP API both pass through this function — and immediately after it,
	// because the two are the same shape: FORGE put something in front of a
	// person, and until they answer, starting the work would decide it for them.
	open, err := optionsFor(ctx, pool, goal.ID)
	if err != nil {
		return err
	}
	if err := gateOnOptions(open, goal); err != nil {
		return err
	}

	now := a.clock.Now()
	tag, err := pool.Exec(ctx, `
		update forge_goals
		   set status = 'active', started_at = coalesce(started_at, $2)
		 where id = $1 and status = $3`, goal.ID, now, string(goal.Status))
	if err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	if tag.RowsAffected() == 0 {
		return errs.New(op, errs.CodeConflict).
			WithDetail("goal %s was no longer %q when activation was written", goal.ID, goal.Status)
	}
	goal.Status = engine.GoalActive

	// Appended after the status write rather than inside it. The transition is
	// the fact that matters and it is already durable; a timeline that failed to
	// record it must not roll back a goal the operator has started. The failure
	// is logged by the caller's error path rather than swallowed silently.
	payload, _ := json.Marshal(map[string]any{"from": string(engine.GoalDraft), "to": string(engine.GoalActive)})
	if err := a.repo.AppendEvent(ctx, pool, &engine.Event{
		GoalID: goal.ID, Kind: engine.EventGoalActivated, Actor: by, ActorID: byID,
		Summary: "Goal activated. Its tasks are claimable by any running worker.",
		Payload: payload,
	}, now); err != nil {
		return err
	}
	return nil
}

func defaultObject(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(`{}`)
	}
	return raw
}
