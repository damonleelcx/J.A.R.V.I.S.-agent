package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/tools"
)

// Worker claims tasks and drives them through the agent loop.
//
// # The loop
//
//	observe → plan → execute → verify → persist → continue
//
// Each pass is bounded and each pass ends on disk. A worker holds nothing
// between tasks, so stopping it at any point is safe and starting another one
// costs nothing but a claim. That is what makes horizontal scaling, crash
// recovery and pause/resume the same mechanism rather than three.
type Worker struct {
	ID string

	pool      *db.Pool
	repo      *engine.Repository
	queue     *engine.Queue
	budget    *engine.BudgetGuard
	assembler *Assembler
	executor  *Executor
	verifier  *Verifier

	cfg       config.EngineConfig
	workspace string
	clock     clock.Clock
	log       *logx.Logger
}

// WorkerDeps is what a worker needs.
type WorkerDeps struct {
	Pool      *db.Pool
	Repo      *engine.Repository
	Queue     *engine.Queue
	Budget    *engine.BudgetGuard
	Assembler *Assembler
	Executor  *Executor
	Verifier  *Verifier
	Config    config.EngineConfig
	// WorkspaceRoot holds one directory per goal. Goals never share one: a tool
	// scoped to "the workspace" would otherwise reach another goal's files, and
	// the sandbox would be per-process rather than per-goal.
	WorkspaceRoot string
	Clock         clock.Clock
	Log           *logx.Logger
}

// NewWorker returns a worker with a unique identity.
//
// The identity includes the hostname and the process id because it is written
// into lease rows: when a task is stuck, "which machine is holding it?" needs an
// answer, and a random uuid does not give one.
func NewWorker(d WorkerDeps) *Worker {
	host, _ := os.Hostname()
	if host == "" {
		host = "unknown-host"
	}
	return &Worker{
		ID:        fmt.Sprintf("%s/%d/%s", host, os.Getpid(), id.New(id.PrefixRun)[4:12]),
		pool:      d.Pool,
		repo:      d.Repo,
		queue:     d.Queue,
		budget:    d.Budget,
		assembler: d.Assembler,
		executor:  d.Executor,
		verifier:  d.Verifier,
		cfg:       d.Config,
		workspace: d.WorkspaceRoot,
		clock:     d.Clock,
		log:       d.Log,
	}
}

// Run drives the worker until ctx is cancelled.
//
// Cancellation is a graceful stop, not a kill: the current task is released back
// to the queue rather than abandoned to its lease timeout, so a deploy or a
// restart costs seconds rather than the full lease duration.
func (w *Worker) Run(ctx context.Context) error {
	w.log.Info(ctx, logx.EventWorkerReady, "worker_id", w.ID, "poll", w.cfg.PollInterval.String())

	for {
		if ctx.Err() != nil {
			w.log.Info(ctx, logx.EventWorkerStopped, "worker_id", w.ID)
			return nil
		}

		// Reaping runs on the polling path rather than in a separate goroutine
		// so that a single worker deployment still recovers crashed tasks.
		// Nothing else observes a crash: the process that would report it died.
		w.reapExpired(ctx)

		task, err := w.queue.Claim(ctx, w.pool, w.ID, w.cfg.LeaseDuration, w.clock.Now())
		if err != nil {
			w.log.ErrorWith(ctx, logx.EventWorkerIdle, err, "worker_id", w.ID)
			if !w.sleep(ctx, w.cfg.PollInterval) {
				return nil
			}
			continue
		}
		if task == nil {
			// Reconcile on the idle path. Settling only when a task finishes
			// makes goal state depend on event timing; this is the read that
			// converges it regardless of what was missed. See settleFinishedGoals.
			w.settleFinishedGoals(ctx)

			// An idle queue is the normal state of a long-running agent, not a
			// problem. Logged at debug so it does not drown the real events.
			w.log.Debug(ctx, logx.EventWorkerIdle, "worker_id", w.ID)
			if !w.sleep(ctx, w.cfg.PollInterval) {
				return nil
			}
			continue
		}

		goalID := task.GoalID
		w.runTask(ctx, task)
		// A task settling is the only moment a goal can become terminal, and the
		// worker is already here holding that fact. A separate sweeper would be a
		// second authority for goal status.
		w.settleGoal(ctx, goalID)
	}
}

// sleep waits, returning false if the context was cancelled.
func (w *Worker) sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// reapExpired returns crashed workers' tasks to the queue.
func (w *Worker) reapExpired(ctx context.Context) {
	reaped, err := w.queue.ReapExpiredLeases(ctx, w.pool, w.clock.Now(), 20)
	if err != nil {
		w.log.WarnWith(ctx, logx.EventWorkerReaped, err, "worker_id", w.ID)
		return
	}
	for _, t := range reaped {
		w.log.Warn(ctx, logx.EventTaskLeaseExpired,
			"task_id", t.ID, "goal_id", t.GoalID, "status", string(t.Status),
			"attempt", t.AttemptCount,
			"detail", "the worker holding this task stopped responding; it was returned to the queue")
		w.appendEvent(ctx, t.GoalID, &t.ID, engine.EventTaskLeaseExpired, engine.ActorScheduler,
			"A worker holding this task stopped responding; the task was recovered.",
			map[string]any{"attempt": t.AttemptCount, "recovered_to": string(t.Status)})
	}
}

// runTask drives one task through the loop.
func (w *Worker) runTask(ctx context.Context, task *engine.Task) {
	goal, err := w.loadGoal(ctx, task.GoalID)
	if err != nil {
		w.failTask(ctx, task, errs.CodeStateCorrupt, "the task's goal could not be read: "+err.Error())
		return
	}

	// Budget is checked BEFORE any work, against persisted counters. A worker
	// restarting must not get a fresh allowance.
	if breach := w.budget.CheckGoal(goal, w.clock.Now()); breach != nil {
		w.log.Warn(ctx, logx.EventBudgetExceededLog,
			"goal_id", goal.ID, "task_id", task.ID, "limit", string(breach.Kind))
		w.appendEvent(ctx, goal.ID, &task.ID, engine.EventBudgetExceeded, engine.ActorSystem,
			fmt.Sprintf("Budget exhausted on %s: used %s of %s.", breach.Kind, breach.Used, breach.Limit),
			map[string]any{"limit_kind": string(breach.Kind), "used": breach.Used, "limit": breach.Limit})
		w.failTask(ctx, task, errs.CodeForbidden, breach.Error().Error())
		return
	}

	// Heartbeat while the task runs. Without it, any task longer than the lease
	// duration is reclaimed mid-flight and run a second time.
	hbCtx, stopHeartbeat := context.WithCancel(ctx)
	var hbWG sync.WaitGroup
	hbWG.Add(1)
	go func() {
		defer hbWG.Done()
		w.heartbeat(hbCtx, task.ID)
	}()
	defer func() {
		stopHeartbeat()
		hbWG.Wait()
	}()

	w.log.Info(ctx, logx.EventTaskCycleStarted,
		"worker_id", w.ID, "task_id", task.ID, "goal_id", goal.ID,
		"attempt", task.AttemptCount, "risk_tier", string(task.RiskTier))

	if err := w.repo.TransitionTask(ctx, w.pool, task, engine.StatusRunning, w.clock.Now(), engine.TaskMutation{}); err != nil {
		// Losing the race here means the reaper already took the task. Abandon
		// it quietly rather than continuing under a lease we no longer hold.
		w.log.Info(ctx, logx.EventTaskCycleEnded,
			"task_id", task.ID, "outcome", "abandoned", "reason", err.Error())
		return
	}
	w.appendEvent(ctx, goal.ID, &task.ID, engine.EventTaskStarted, engine.ActorExecutor,
		"Started work.", map[string]any{"attempt": task.AttemptCount, "worker": w.ID})

	// Approval gate, BEFORE any work at this tier. Asking afterwards would mean
	// the consequential thing already happened.
	if task.RequiresApproval {
		granted, err := w.checkApproval(ctx, goal, task)
		if err != nil {
			w.failTask(ctx, task, errs.CodeOf(err), err.Error())
			return
		}
		if !granted {
			return // parked in awaiting_approval; a human will move it
		}
	}

	workspace, err := w.goalWorkspace(goal.ID)
	if err != nil {
		w.failTask(ctx, task, errs.CodeInternal, err.Error())
		return
	}

	grant := grantFor(goal)
	tc, err := w.assembler.Assemble(ctx, w.pool, task, goal, grant, w.budgetNote(goal))
	if err != nil {
		w.failTask(ctx, task, errs.CodeOf(err), err.Error())
		return
	}

	outcome, err := w.executor.Execute(ctx, tc, workspace)
	if err != nil {
		w.retryOrFail(ctx, goal, task, err)
		return
	}

	switch {
	case outcome.Status == "blocked":
		// Blocked is a truthful terminal state, not a failure to retry. Retrying
		// a task that needs a human is how a queue spins.
		w.appendEvent(ctx, goal.ID, &task.ID, engine.EventTaskFailed, engine.ActorExecutor,
			"Blocked: "+outcome.BlockedReason, map[string]any{"status": "blocked"})
		w.failTask(ctx, task, errs.CodeForbidden, "blocked: "+outcome.BlockedReason)
		return

	case outcome.Status == "failed":
		w.retryOrFail(ctx, goal, task,
			errs.New("agent.Worker.runTask", errs.CodeInternal).WithDetail("%s", outcome.Summary))
		return
	}

	w.completeTask(ctx, goal, task, tc, outcome)
}

// completeTask verifies where required, then records the outcome.
func (w *Worker) completeTask(ctx context.Context, goal *engine.Goal, task *engine.Task, tc *TaskContext, outcome *Outcome) {
	resultJSON, _ := json.Marshal(map[string]any{
		"summary":     outcome.Summary,
		"result":      outcome.Result,
		"evidence":    outcome.Evidence,
		"assumptions": outcome.Assumptions,
	})

	if !VerificationRequired(task.RiskTier) {
		// Recorded as succeeded, and deliberately NOT as verified. The two are
		// different facts, and a low-tier task that nobody checked must not
		// present as one that was checked.
		w.transition(ctx, task, engine.StatusSucceeded, engine.TaskMutation{Result: resultJSON})
		w.appendEvent(ctx, goal.ID, &task.ID, engine.EventTaskSucceeded, engine.ActorExecutor,
			outcome.Summary,
			map[string]any{"verified": false, "reason": "tier " + string(task.RiskTier) + " does not require verification"})
		w.log.Info(ctx, logx.EventTaskCycleEnded,
			"task_id", task.ID, "outcome", "succeeded", "verified", false,
			"iterations", outcome.Iterations, "tool_calls", outcome.ToolCallsMade)
		return
	}

	if err := w.transition(ctx, task, engine.StatusVerifying, engine.TaskMutation{Result: resultJSON}); err != nil {
		return
	}

	verdict, err := w.verifier.Verify(ctx, tc, outcome, w.rawToolOutput(ctx, task.ID))
	if err != nil {
		// A verifier that could not produce a verdict has verified nothing.
		// Treating that as a pass is exactly the failure the verifier exists to
		// prevent, so it becomes a retry.
		w.log.WarnWith(ctx, logx.EventVerificationRan, err, "task_id", task.ID)
		w.retryOrFail(ctx, goal, task, err)
		return
	}
	verdictJSON, _ := json.Marshal(verdict)

	w.log.Info(ctx, logx.EventVerificationRan,
		"task_id", task.ID, "verified", verdict.Passed(),
		"confidence", verdict.Confidence, "recommendation", verdict.Recommendation,
		"verifier_model", verdict.Model)

	if verdict.Passed() {
		now := w.clock.Now()
		w.transition(ctx, task, engine.StatusSucceeded, engine.TaskMutation{
			Result: resultJSON, Verdict: verdictJSON, VerifiedAt: &now,
		})
		w.appendEvent(ctx, goal.ID, &task.ID, engine.EventVerificationOK, engine.ActorVerifier,
			verdict.Reasoning,
			map[string]any{"confidence": verdict.Confidence, "verifier_model": verdict.Model})
		return
	}

	w.appendEvent(ctx, goal.ID, &task.ID, engine.EventVerificationFail, engine.ActorVerifier,
		verdict.Reasoning, map[string]any{
			"recommendation":     verdict.Recommendation,
			"unsupported_claims": verdict.UnsupportedClaims,
			"missing_checks":     verdict.MissingChecks,
			"verifier_model":     verdict.Model,
		})

	if verdict.RequiresRework() {
		w.retryOrFail(ctx, goal, task, errs.New("agent.Worker", errs.CodeInternal).
			WithDetail("verification did not pass: %s", verdict.Reasoning))
		return
	}
	w.failTask(ctx, task, errs.CodeInvariantViolated,
		"verification rejected the result: "+verdict.Reasoning)
}

// retryOrFail applies backoff and returns the task to the queue, or fails it.
func (w *Worker) retryOrFail(ctx context.Context, goal *engine.Goal, task *engine.Task, cause error) {
	if breach := w.budget.CheckAttempts(task); breach != nil || !errs.IsRetryable(cause) {
		reason := "no attempts remain"
		if breach == nil {
			reason = "the failure is not retryable"
		}
		w.log.Info(ctx, logx.EventTaskCycleEnded,
			"task_id", task.ID, "outcome", "failed", "reason", reason, "cause", cause.Error())
		w.appendEvent(ctx, goal.ID, &task.ID, engine.EventTaskFailed, engine.ActorExecutor,
			cause.Error(), map[string]any{
				"attempt":    task.AttemptCount,
				"error_code": string(errs.CodeOf(cause)),
				"detail":     cause.Error(),
				"reason":     reason,
			})
		w.failTask(ctx, task, errs.CodeOf(cause), cause.Error())
		return
	}

	delay := w.backoff(task.AttemptCount)
	notBefore := w.clock.Now().Add(delay)

	w.log.Info(ctx, logx.EventTaskRetryingLog,
		"task_id", task.ID, "attempt", task.AttemptCount, "of", task.MaxAttempts,
		"retry_in", delay.String(), "cause", cause.Error())
	w.appendEvent(ctx, goal.ID, &task.ID, engine.EventTaskRetrying, engine.ActorExecutor,
		fmt.Sprintf("Attempt %d failed; retrying in %s.", task.AttemptCount, delay.Round(time.Second)),
		map[string]any{
			"attempt":    task.AttemptCount,
			"error_code": string(errs.CodeOf(cause)),
			"detail":     cause.Error(),
		})

	w.transition(ctx, task, engine.StatusReady, engine.TaskMutation{
		NotBefore:   &notBefore,
		ErrorCode:   string(errs.CodeOf(cause)),
		ErrorDetail: cause.Error(),
	})
}

// backoff returns an exponential delay with jitter.
//
// Jitter matters more than the exponent: without it, several tasks that failed
// on the same upstream outage retry in lockstep and reproduce the outage.
func (w *Worker) backoff(attempt int) time.Duration {
	base := time.Duration(float64(w.cfg.BackoffBase) * math.Pow(2, float64(attempt-1)))
	if base > w.cfg.BackoffMax {
		base = w.cfg.BackoffMax
	}
	if base <= 0 {
		base = w.cfg.BackoffBase
	}
	return base/2 + time.Duration(rand.Int63n(int64(base/2)+1))
}

// checkApproval opens or reads the human gate for a task.
func (w *Worker) checkApproval(ctx context.Context, goal *engine.Goal, task *engine.Task) (bool, error) {
	var decision string
	err := w.pool.QueryRow(ctx,
		`select decision from forge_approvals where task_id = $1 order by requested_at desc limit 1`,
		task.ID).Scan(&decision)

	switch {
	case err == nil && decision == string(engine.ApprovalApproved):
		return true, nil
	case err == nil && decision == string(engine.ApprovalRejected):
		w.failTask(ctx, task, errs.CodeForbidden, "a human rejected this action")
		return false, nil
	case err == nil && decision == string(engine.ApprovalPending):
		// Already waiting. Park without a lease so the gate does not pin a
		// worker or expire as if the worker had crashed.
		w.transition(ctx, task, engine.StatusAwaitingApproval, engine.TaskMutation{})
		return false, nil
	}

	summary := fmt.Sprintf("%s\n\nThis is a %s action on goal %q. It will not run until you approve it.",
		task.Instruction, task.RiskTier, goal.Title)
	preview, _ := json.Marshal(map[string]any{
		"task":            task.Title,
		"instruction":     task.Instruction,
		"risk_tier":       string(task.RiskTier),
		"inputs":          task.Inputs,
		"expected_output": task.ExpectedOutput,
	})

	if _, err := w.pool.Exec(ctx, `
		insert into forge_approvals (id, goal_id, task_id, risk_tier, summary, preview, requested_at)
		values ($1,$2,$3,$4,$5,$6,$7)
		on conflict do nothing`,
		id.New(id.PrefixApproval), goal.ID, task.ID, string(task.RiskTier),
		summary, preview, w.clock.Now()); err != nil {
		return false, errs.Wrap("agent.Worker.checkApproval", errs.CodeDatabaseUnavail, err)
	}

	w.log.Info(ctx, logx.EventApprovalOpened,
		"task_id", task.ID, "goal_id", goal.ID, "risk_tier", string(task.RiskTier))
	w.appendEvent(ctx, goal.ID, &task.ID, engine.EventApprovalRequested, engine.ActorExecutor,
		"Waiting for a human to approve this action.",
		map[string]any{"risk_tier": string(task.RiskTier)})

	w.transition(ctx, task, engine.StatusAwaitingApproval, engine.TaskMutation{})
	return false, nil
}

// heartbeat extends the lease while a task runs.
func (w *Worker) heartbeat(ctx context.Context, taskID string) {
	ticker := time.NewTicker(w.cfg.LeaseHeartbeat)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.queue.Heartbeat(ctx, w.pool, taskID, w.ID, w.cfg.LeaseDuration, w.clock.Now()); err != nil {
				// Losing the lease means another worker now owns this task.
				// Logged loudly and the heartbeat stops; the executor will fail
				// its next write against the compare-and-set guard.
				w.log.WarnWith(ctx, logx.EventTaskLeaseExpired, err,
					"task_id", taskID, "worker_id", w.ID,
					"detail", "this worker no longer holds the lease; another worker may be running the same task")
				return
			}
		}
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func (w *Worker) transition(ctx context.Context, task *engine.Task, to engine.TaskStatus, mut engine.TaskMutation) error {
	err := w.repo.TransitionTask(ctx, w.pool, task, to, w.clock.Now(), mut)
	if err != nil {
		w.log.WarnWith(ctx, logx.EventTaskCycleEnded, err,
			"task_id", task.ID, "target", string(to))
	}
	return err
}

func (w *Worker) failTask(ctx context.Context, task *engine.Task, code errs.Code, detail string) {
	_ = w.transition(ctx, task, engine.StatusFailed, engine.TaskMutation{
		ErrorCode: string(code), ErrorDetail: detail,
	})
	// A failed task blocks everything downstream. Propagating immediately means
	// the goal reaches a truthful terminal state rather than leaving a tail of
	// tasks that will never run but still look pending.
	if _, err := w.queue.SkipTasksBlockedByFailure(ctx, w.pool, task.GoalID, w.clock.Now()); err != nil {
		w.log.WarnWith(ctx, logx.EventTaskSkippedLog, err, "goal_id", task.GoalID)
	}
}

func (w *Worker) appendEvent(ctx context.Context, goalID string, taskID *string, kind string, actor engine.Actor, summary string, payload map[string]any) {
	raw, _ := json.Marshal(payload)
	ev := &engine.Event{
		GoalID: goalID, TaskID: taskID, Kind: kind, Actor: actor,
		Summary: summary, Payload: raw,
	}
	if err := w.repo.AppendEvent(ctx, w.pool, ev, w.clock.Now()); err != nil {
		// The timeline is how anyone reconstructs what happened. A gap in it is
		// worth shouting about, but not worth failing work over.
		w.log.WarnWith(ctx, logx.EventTaskCycleEnded, err,
			"goal_id", goalID, "kind", kind,
			"detail", "a timeline event was lost; the execution history has a gap here")
	}
}

func (w *Worker) loadGoal(ctx context.Context, goalID string) (*engine.Goal, error) {
	var g engine.Goal
	var status, autonomy, risk string
	var criteria []byte
	err := w.pool.QueryRow(ctx, `
		select id, project_id, created_by, title, statement, status, autonomy, risk_tier,
		       completion_criteria, max_tokens, max_cost_cents, max_wallclock_ms, max_tasks,
		       tokens_spent, cost_cents_spent, tasks_created, started_at, created_at
		  from forge_goals where id = $1`, goalID).
		Scan(&g.ID, &g.ProjectID, &g.CreatedBy, &g.Title, &g.Statement, &status, &autonomy, &risk,
			&criteria, &g.Budget.MaxTokens, &g.Budget.MaxCostCents, &wallMillis{&g.Budget.MaxWallClock},
			&g.Budget.MaxTasks, &g.Spend.Tokens, &g.Spend.CostCents, &g.Spend.TasksCreated,
			&g.StartedAt, &g.CreatedAt)
	if err != nil {
		return nil, errs.Wrap("agent.Worker.loadGoal", errs.CodeNotFound, err)
	}
	g.Status = engine.GoalStatus(status)
	g.Autonomy = engine.Autonomy(autonomy)
	g.RiskTier = engine.RiskTier(risk)
	_ = json.Unmarshal(criteria, &g.CompletionCriteria)
	return &g, nil
}

// wallMillis adapts a nullable bigint of milliseconds to a *time.Duration.
type wallMillis struct{ d **time.Duration }

// Scan implements sql.Scanner.
func (w wallMillis) Scan(src any) error {
	if src == nil {
		*w.d = nil
		return nil
	}
	ms, ok := src.(int64)
	if !ok {
		return fmt.Errorf("wallMillis: expected int64, got %T", src)
	}
	d := time.Duration(ms) * time.Millisecond
	*w.d = &d
	return nil
}

// goalWorkspace returns the directory a goal's tools may touch, creating it.
//
// One directory per GOAL, not per worker. A workspace shared between goals means
// a tool scoped to "the workspace" can read another goal's files, which makes
// the sandbox per-process rather than per-goal — and the boundary users think
// they have is the per-goal one.
func (w *Worker) goalWorkspace(goalID string) (string, error) {
	if !id.Valid(goalID, id.PrefixGoal) {
		return "", errs.New("agent.Worker.goalWorkspace", errs.CodeInvariantViolated).
			WithDetail("goal id %q is malformed; refusing to derive a filesystem path from it", goalID)
	}
	dir := filepath.Join(w.workspace, goalID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", errs.Wrap("agent.Worker.goalWorkspace", errs.CodeInternal, err).
			WithDetail("cannot create the workspace for goal %s", goalID)
	}
	return dir, nil
}

// rawToolOutput returns this task's unedited tool output for the verifier.
//
// Raw, not the executor's account of it. This is what lets the verifier catch
// the case the executor cannot catch on itself: a summary that does not match
// what the tools actually returned.
func (w *Worker) rawToolOutput(ctx context.Context, taskID string) []string {
	rows, err := w.pool.Query(ctx, `
		select tool_name, coalesce(raw_output, ''), status
		  from forge_tool_calls where task_id = $1 order by created_at asc limit 20`, taskID)
	if err != nil {
		w.log.WarnWith(ctx, logx.EventVerificationRan, err, "task_id", taskID,
			"detail", "raw tool output could not be read; the verifier is judging the executor's account of it instead")
		return nil
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var name, raw, status string
		if err := rows.Scan(&name, &raw, &status); err != nil {
			continue
		}
		out = append(out, fmt.Sprintf("[%s → %s]\n%s", name, status, raw))
	}
	return out
}

// budgetNote renders remaining headroom for the model, so it can pace itself
// rather than being cut off mid-thought.
func (w *Worker) budgetNote(goal *engine.Goal) string {
	max := w.cfg.MaxTokensPerGoal
	if goal.Budget.MaxTokens != nil {
		max = *goal.Budget.MaxTokens
	}
	if max <= 0 {
		return "No token ceiling is set for this goal."
	}
	remaining := max - goal.Spend.Tokens
	pct := float64(goal.Spend.Tokens) / float64(max) * 100
	note := fmt.Sprintf("This goal has used %d of %d tokens (%.0f%%); about %d remain.",
		goal.Spend.Tokens, max, pct, remaining)
	if pct > 75 {
		note += " You are near the ceiling — prefer finishing what you have over exploring further."
	}
	return note
}

// grantFor derives the permission set for a goal.
//
// Capabilities are tied to the autonomy level rather than configured separately,
// so there is one place to reason about "what may this goal do" instead of two
// that can disagree. Deploy, transact and control are never granted by this
// build — they are the capabilities whose tools do not exist here, and granting
// a capability with no tool behind it only creates a false sense of scope.
func grantFor(goal *engine.Goal) tools.Grant {
	caps := []tools.Capability{tools.CapRead}
	if goal.Autonomy.AtLeast(engine.AutonomyDraft) {
		caps = append(caps, tools.CapWrite)
	}
	if goal.Autonomy.AllowsExecution() {
		caps = append(caps, tools.CapExecute, tools.CapSimulate)
	}
	return tools.Grant{
		Capabilities: caps,
		MaxRiskTier:  goal.RiskTier,
		Autonomy:     goal.Autonomy,
	}
}
