package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/text"
)

// A build as an engine goal (Phase 2, stage A1).
//
// # The problem this solves
//
// A build ran inside one HTTP request: planned, then built a pass at a time
// while a person watched, with everything it had made held in that request's
// memory until the last step. A car of hundreds of subsystems is an hour of
// passes, not a turn. A deploy, a crash or a closed tab lost all of it, and
// nothing charged its model calls to anything.
//
// The engine already has what that needs: goals with budgets, tasks with
// dependencies and leases, a reaper for a worker that dies. So a build can be a
// goal. Its plan is one task per step, each depending on the step before, and
// forge-worker runs a step exactly as the workbench does — the same
// buildOneStep, the same repairs and checks — then keeps what it built as a
// version of the design, inside the goal, before the task is marked done.
//
// # What resumes, and from where
//
// Nobody holds the model so far. Each step reads it from the version the step
// before it kept, so a worker that stops anywhere loses at most the step it was
// on, and whoever claims that step next starts from the last step kept. A step
// that kept its model and then stopped before it was marked done is not built a
// second time: keeping writes a checkpoint, and a step reads that first.
//
// # Why the steps run one after another
//
// Each step is shown what the steps before it built and positions against it.
// Two steps at once would each build on a model without the other, and the
// second to be kept would silently drop the first.

// TaskKindBuildStep marks a task that is one step of a build.
const TaskKindBuildStep = "geometry.build_step"

// stepLedgerLimit bounds a recorded input, as the workbench bounds a turn's.
const stepLedgerLimit = 2000

// checkpointBuildStepSaved is written once a step's model has been kept.
const checkpointBuildStepSaved = "build_step_saved"

// buildStepInputs is what a build step's task carries.
type buildStepInputs struct {
	Kind  string    `json:"kind"`
	Asked string    `json:"asked"`
	Step  buildTask `json:"step"`
	N     int       `json:"n"`
	Of    int       `json:"of"`
}

// buildStepResult is what a finished step records: the version the model now
// is, which the next step reads.
type buildStepResult struct {
	VersionID string `json:"version_id,omitempty"`
	Parts     int    `json:"parts"`
	Note      string `json:"note,omitempty"`
}

func buildStepOf(t *engine.Task) (buildStepInputs, bool) {
	var in buildStepInputs
	if t == nil || len(t.Inputs) == 0 || json.Unmarshal(t.Inputs, &in) != nil {
		return in, false
	}
	return in, in.Kind == TaskKindBuildStep
}

// buildPlan is the plan for a build: one task per step, each after the one before.
func buildPlan(asked string, steps []buildTask) *PlanResult {
	plan := &PlanResult{Rationale: fmt.Sprintf(
		"Build it in %d steps, each adding to the model the step before it kept.", len(steps))}
	prev := ""
	for i, s := range steps {
		inputs, _ := json.Marshal(buildStepInputs{Kind: TaskKindBuildStep, Asked: asked, Step: s, N: i + 1, Of: len(steps)})
		name := s.Name
		if name == "" {
			name = s.What
		}
		key := fmt.Sprintf("build-step-%02d", i+1)
		pt := PlannedTask{
			Key: key, Title: fmt.Sprintf("Step %d of %d — %s", i+1, len(steps), name),
			Instruction: s.What, Inputs: inputs, RiskTier: string(engine.RiskR1),
		}
		if prev != "" {
			pt.DependsOn = []string{prev}
		}
		plan.Tasks = append(plan.Tasks, pt)
		prev = key
	}
	return plan
}

// planBuildGoal asks for a build's steps and writes them into a draft goal as
// tasks. The planning call is charged to the goal like every call after it.
func planBuildGoal(ctx context.Context, pool *db.Pool, c *Conversation, applier *PlanApplier,
	goal *engine.Goal, log *logx.Logger) (*PlanOutcome, error) {
	const op = "agent.planBuildGoal"

	charged := chargeTo(c.client, applier.budget, pool, goal, applier.clock, log)
	steps, err := c.withClient(charged).planBuild(ctx, goal.Statement)
	if errors.Is(err, errNotWorthPlanning) {
		return nil, errs.New(op, errs.CodeValidationFailed).
			WithDetail("the model planned this as a single step, and one step is a turn rather than a build. " +
				"Ask for it in the workbench, or describe something with several subsystems")
	}
	if err != nil {
		return nil, err
	}
	plan := buildPlan(goal.Statement, steps)
	plan.Model = charged.ModelFor(llm.RoleConverse)
	created, tasks, err := applier.Apply(ctx, pool, goal, plan, "planner")
	if err != nil {
		return nil, err
	}
	return &PlanOutcome{Plan: created, Tasks: tasks, Rationale: plan.Rationale, Result: plan}, nil
}

// PlanBuild plans a draft goal as a build: the statement is what to build, and
// each step becomes a task forge-worker runs with the CAD kernel.
func (in *Intake) PlanBuild(ctx context.Context, pool *db.Pool, goal *engine.Goal) (*PlanOutcome, error) {
	return planBuildGoal(ctx, pool, NewConversation(in.planner.client, in.planner.char), in.applier, goal, in.logger())
}

// BuildSteps runs the steps of a build for a worker.
type BuildSteps struct {
	conv   *Conversation
	geo    *geometry.Service
	repo   *engine.Repository
	budget *engine.BudgetGuard
	pool   *db.Pool
	clock  clock.Clock
	log    *logx.Logger
}

// NewBuildSteps returns the runner a worker hands build steps to. conv is the
// conversation a step is built by — with the kernel, when the deployment has one.
func NewBuildSteps(conv *Conversation, geo *geometry.Service, repo *engine.Repository,
	budget *engine.BudgetGuard, pool *db.Pool, clk clock.Clock, log *logx.Logger) *BuildSteps {
	if log == nil {
		log = logx.Discard()
	}
	return &BuildSteps{conv: conv, geo: geo, repo: repo, budget: budget, pool: pool, clock: clk, log: log}
}

func (b *BuildSteps) run(ctx context.Context, goal *engine.Goal, task *engine.Task, in buildStepInputs) (*Outcome, error) {
	// ‼️ A step whose model was kept is not built again. Its worker stopped
	// between keeping it and marking the task done; building it again would pay
	// for the step twice and keep a second version of the same step.
	cp, err := b.repo.LatestCheckpoint(ctx, b.pool, task.ID)
	if err != nil {
		return nil, err
	}
	if cp != nil && cp.Kind == checkpointBuildStepSaved {
		var kept buildStepResult
		if json.Unmarshal(cp.State, &kept) == nil && kept.VersionID != "" {
			return stepOutcome(in, kept, true), nil
		}
	}

	doc, from, err := b.modelSoFar(ctx, task, in)
	if err != nil {
		return nil, err
	}

	started := b.clock.Now()
	charged := chargeTo(b.conv.client, b.budget, b.pool, goal, b.clock, b.log)
	next, note := b.conv.withClient(charged).buildOneStep(ctx, doc, in.Asked, in.Step, in.N, in.Of)

	// Stopped is not finished. A repair cut off by a stopping worker leaves a
	// model that looks complete and was never checked; it is not kept.
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	// Refused by the budget part-way through: the goal stops here, and what the
	// steps before kept stays kept.
	if breach := charged.Breach(); breach != nil {
		return nil, breach.Error()
	}
	// The model could not be reached. That is a failure to retry with backoff,
	// not a step that built nothing — the error's own code says whether it is.
	if next == nil {
		if failed := charged.Failed(); failed != nil {
			return nil, failed
		}
	}

	kept := buildStepResult{VersionID: from, Parts: len(doc.Parts), Note: note}
	if next != nil && next.HasGeometry() {
		call, err := b.recordStep(ctx, task, in, started)
		if err != nil {
			return nil, err
		}
		v, err := b.geo.Save(ctx, geometry.NewVariant{
			ProjectID: goal.ProjectID, InitiatorID: goal.CreatedBy,
			Agent: workspace.AgentExecutor, Generator: charged.ModelFor(llm.RoleConverse),
			Document: *next,
			Inputs: map[string]any{
				"source": "build goal", "asked": text.Clip(in.Asked, stepLedgerLimit),
				"step": in.Step.Name, "what": text.Clip(in.Step.What, stepLedgerLimit), "n": in.N, "of": in.Of,
				"from_version": from,
			},
			// Phase 7, stage E3: a save inside a goal writes the chained
			// artifact.changed event, naming the step that made it.
			GoalID: goal.ID, TaskID: task.ID, ToolCallID: call,
		})
		if err != nil {
			return nil, err
		}
		kept.VersionID, kept.Parts = v.VersionID, len(v.Document.Parts)
		state, _ := json.Marshal(kept)
		if _, err := b.repo.SaveCheckpoint(ctx, b.pool, task.ID, checkpointBuildStepSaved, state, b.clock.Now()); err != nil {
			b.log.WarnWith(ctx, logx.EventCheckpointFailed, err, "task_id", task.ID,
				"detail", "the step's model was kept but no resume point says so; a worker that stops "+
					"before this task is marked done will build the step again")
		}
	}
	return stepOutcome(in, kept, false), nil
}

// recordStep writes the ledger row a kept step's version names (PRD WRK-04).
//
// A build step is not a tool the model chose. It is the worker's own action on
// the model's answer, and it is recorded as a call anyway, because WRK-04 asks
// every change the executor makes to name the action that made it, and a version
// without one is a change nobody can trace to anything. One row per attempt,
// keyed by it: a step retried after its worker hung is two actions, and the
// ledger says so.
func (b *BuildSteps) recordStep(ctx context.Context, task *engine.Task, in buildStepInputs, started time.Time) (string, error) {
	const op = "agent.BuildSteps.recordStep"
	now := b.clock.Now()
	input, _ := json.Marshal(in)
	attempt := max(task.AttemptCount, 1)
	callID := id.New(id.PrefixToolCall)
	err := b.pool.QueryRow(ctx, `
		insert into forge_tool_calls (id, task_id, idempotency_key, tool_name, input, status, attempt,
			started_at, ended_at, duration_ms, created_at, risk_tier)
		values ($1,$2,$3,$4,$5,'succeeded',$6,$7,$8,$9,$8,nullif($10,''))
		on conflict (idempotency_key) do update set idempotency_key = excluded.idempotency_key
		returning id`,
		callID, task.ID, idempotencyKey(task.ID, TaskKindBuildStep, fmt.Sprintf("attempt %d", attempt)),
		TaskKindBuildStep, input, attempt, started, now, now.Sub(started).Milliseconds(), string(task.RiskTier)).
		Scan(&callID)
	if err != nil {
		return "", errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	return callID, nil
}

// modelSoFar is the model the step before this one kept, or an empty one for
// the first step.
func (b *BuildSteps) modelSoFar(ctx context.Context, task *engine.Task, in buildStepInputs) (*geometry.Document, string, error) {
	deps, err := b.repo.ListDependencies(ctx, b.pool, task.ID)
	if err != nil {
		return nil, "", err
	}
	for _, depID := range deps {
		dep, err := b.repo.GetTask(ctx, b.pool, depID)
		if err != nil {
			return nil, "", err
		}
		var done struct {
			Result buildStepResult `json:"result"`
		}
		if json.Unmarshal(dep.Result, &done) != nil || done.Result.VersionID == "" {
			continue
		}
		v, err := b.geo.Find(ctx, done.Result.VersionID)
		if err != nil {
			return nil, "", err
		}
		doc := v.Document
		return &doc, v.VersionID, nil
	}
	return &geometry.Document{Name: in.Asked, Units: "mm"}, "", nil
}

func stepOutcome(in buildStepInputs, kept buildStepResult, resumed bool) *Outcome {
	summary := fmt.Sprintf("Step %d of %d (%s): %d part(s), kept as version %s.",
		in.N, in.Of, in.Step.Name, kept.Parts, kept.VersionID)
	if kept.VersionID == "" {
		summary = fmt.Sprintf("Step %d of %d (%s): nothing kept yet.", in.N, in.Of, in.Step.Name)
	}
	if kept.Note != "" {
		summary += " " + kept.Note
	}
	if resumed {
		summary += " Kept before its worker stopped, so not built again."
	}
	result, _ := json.Marshal(kept)
	return &Outcome{Status: "completed", Summary: summary, Result: result}
}

// runBuildStep runs one step of a build and records how it ended.
func (w *Worker) runBuildStep(ctx context.Context, goal *engine.Goal, task *engine.Task, in buildStepInputs) {
	if w.builds == nil {
		w.failTask(ctx, task, errs.CodeConfigInvalid,
			"this worker cannot run a build step: it was started without a build runner (agent.WorkerDeps.Builds)")
		return
	}
	outcome, err := w.builds.run(ctx, goal, task, in)
	if err != nil {
		if ctx.Err() != nil {
			// Stopping, not failing. The attempt is abandoned and must not be
			// counted against the step.
			//
			// ‼️ The hand-back itself is Run's, not this function's — see handBack,
			// which releases every stopped task and records it on the timeline.
			// Releasing here as well made that release a no-op conflict, which
			// handBack swallows by design, so a stopped build step was handed back
			// without the event that says so.
			return
		}
		w.retryOrFail(ctx, goal, task, err)
		return
	}
	resultJSON, _ := json.Marshal(map[string]any{"summary": outcome.Summary, "result": outcome.Result})
	if err := w.transition(ctx, task, engine.StatusSucceeded, engine.TaskMutation{Result: resultJSON}); err != nil {
		return
	}
	// Succeeded and NOT verified, said plainly. A build step is checked inside
	// the step — the fault repair, the look, interference, the kernel — and the
	// verifier, which judges an executor's claims against its evidence, has no
	// claims here to judge.
	w.appendEvent(ctx, goal.ID, &task.ID, engine.EventTaskSucceeded, engine.ActorExecutor, outcome.Summary,
		map[string]any{"verified": false, "reason": "a build step is checked by the repairs and the kernel inside the step"})
}
