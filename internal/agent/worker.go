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

	"github.com/jackc/pgx/v5"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	domainpack "github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/pack"
	domainworkspace "github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
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
	builds    *BuildSteps

	cfg        config.EngineConfig
	production bool
	workspace  string
	clock      clock.Clock
	log        *logx.Logger

	// approvalRowWrittenForTest runs between checkApproval writing a request row and
	// recording it on the timeline. Nil outside the fence that stops a worker there.
	approvalRowWrittenForTest func()
	// afterTransitionForTest runs after a transition has been written, and an error it
	// returns replaces the result transition actually got. Nil outside the fence for a
	// statement the stop cancelled after Postgres had committed it.
	afterTransitionForTest func(to engine.TaskStatus) error
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
	// Builds runs a build's steps (Phase 2, stage A1). Nil is a worker that
	// refuses them by name, which is what every worker was before builds ran as
	// goals.
	Builds *BuildSteps
	Config config.EngineConfig
	// Production is the deployment context, passed to every grant (PRD SAF-01).
	// False is the safe default to get wrong in only one direction: a
	// development deployment mislabelled as production refuses work, where the
	// reverse would run production changes at a development tier.
	Production bool
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
	// ‼️ The LAST eight characters of a fresh id, which are random. It took the
	// first eight, which are its millisecond timestamp's top bits and change about
	// once a second, so every worker one process started together — forge-worker
	// starts FORGE_WORKER_CONCURRENCY of them in one loop — had the same identity,
	// and no lease guard could tell a worker from its sibling.
	// docs/bugfix/2026-09-15-workers-started-together-shared-one-lease-identity.md
	run := id.New(id.PrefixRun)
	return &Worker{
		ID:         fmt.Sprintf("%s/%d/%s", host, os.Getpid(), run[len(run)-8:]),
		pool:       d.Pool,
		repo:       d.Repo,
		queue:      d.Queue,
		budget:     d.Budget,
		assembler:  d.Assembler,
		executor:   d.Executor,
		verifier:   d.Verifier,
		builds:     d.Builds,
		cfg:        d.Config,
		production: d.Production,
		workspace:  d.WorkspaceRoot,
		clock:      d.Clock,
		log:        d.Log,
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
			if ctx.Err() != nil {
				continue // stopped while claiming; the check above says so and returns
			}
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
			// What a finished task left waiting, released here too, for the reason
			// goals are settled here: a worker that died between a task's last
			// write and releasing its dependents leaves them with nothing to move
			// them. See releaseWaitingGoals.
			w.releaseWaitingGoals(ctx)

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
		if ctx.Err() != nil {
			w.handBack(ctx, task)
		}
		w.afterTask(ctx, goalID)
	}
}

// afterTaskTimeout bounds what a task's end sets moving when the worker is stopping,
// so a database that really has gone away cannot hold a stopping worker past the 30 s
// forge-worker gives its loops to return.
const afterTaskTimeout = 10 * time.Second

// outliving is the context for a write that records something that has already
// happened (an event, a spent token, a tool call that ran), bounded for afterTask's
// reason.
//
// # Why records and not decisions
//
// ‼️ A stop abandons the attempt, not the record of it. Written on the run context, a
// record that a stop overtook failed as DATABASE_UNAVAILABLE and was lost: tokens the
// budget never counted, a tool call the ledger never saw, so the next attempt ran it
// again. A DECISION (a transition, failing or retrying a task) is the opposite. It is
// left on the run context and skipped when stopping, because handBack gives the task
// and the decision to the next worker, which decides again from the row.
// docs/bugfix/2026-09-15-a-stopped-worker-lost-what-it-had-done-and-blamed-the-database.md
func outliving(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), afterTaskTimeout)
}

// afterTask is what the end of a task sets moving, whether the task finished or
// the worker is stopping under it.
//
// # Why on a context of its own
//
// ‼️ A stop cancels ctx while a task runs, and both calls below used to run on that
// cancelled ctx: each failed at once and was logged as DATABASE_UNAVAILABLE, so every
// graceful stop in the middle of a task told whoever read the log that the database
// was down, and a task that finished in the instant the stop arrived left the tasks
// waiting on it pending until some worker's idle poll found them. Found stopping a
// live forge-worker with a console Ctrl-Break (#104).
// docs/bugfix/2026-09-15-a-stopping-worker-reported-its-own-stop-as-a-database-outage.md
//
// Bounded rather than unbounded: these are two short statements, and the bound is what
// keeps "outlives the stop" from becoming "outlives the process".
func (w *Worker) afterTask(ctx context.Context, goalID string) {
	book, cancel := context.WithTimeout(context.WithoutCancel(ctx), afterTaskTimeout)
	defer cancel()
	// ‼️ A finished task releases the tasks waiting on it, before the next
	// claim. Nothing did: a plan's first layer was made ready and every task
	// after it stayed pending forever. On the idle poll alone, a busy worker
	// would never get round to it.
	// docs/bugfix/2026-09-15-a-finished-task-never-released-the-tasks-waiting-on-it.md
	w.releaseWaiting(book, goalID)
	// A task settling is the only moment a goal can become terminal, and the
	// worker is already here holding that fact. A separate sweeper would be a
	// second authority for goal status.
	w.settleGoal(book, goalID)
}

// AfterTaskForTest exposes afterTask so a test can hand it the cancelled context a
// stop leaves Run holding, instead of racing a stop against a task's last write.
func (w *Worker) AfterTaskForTest(ctx context.Context, goalID string) { w.afterTask(ctx, goalID) }

// OnApprovalRowWrittenForTest makes hook run the moment checkApproval has written an
// approval request and before it records approval.requested, so a test can land a
// stop exactly there instead of racing one against two statements.
func (w *Worker) OnApprovalRowWrittenForTest(hook func()) { w.approvalRowWrittenForTest = hook }

// OnTransitionWrittenForTest makes hook run after every transition has been written, and
// lets it replace what the transition returned.
//
// # Why a seam and not a race
//
// ‼️ The case this reproduces is a statement the stop cancelled while it was on the wire,
// after Postgres had already committed it: the row moved and the caller was told only
// that its context was done. Which of the cancel request and the commit reaches the
// server first is not something a test can decide, so there is no way to land that
// instant by racing a real stop against a real statement. The seam produces exactly its
// shape — a committed transition whose caller sees a cancellation — and nothing else. It
// is nil outside the fence.
func (w *Worker) OnTransitionWrittenForTest(hook func(to engine.TaskStatus) error) {
	w.afterTransitionForTest = hook
}

// PollSweepsForTest runs the three reconciliations Run makes on its polling path — the
// lease reaper, and the two sweeps it runs when the queue is idle — so a test can hand
// them the cancelled context a stop leaves Run holding, instead of racing a stop against
// a sweep that takes one indexed query. AfterTaskForTest exists for the same reason.
func (w *Worker) PollSweepsForTest(ctx context.Context) {
	w.reapExpired(ctx)
	w.releaseWaitingGoals(ctx)
	w.settleFinishedGoals(ctx)
}

// handBack returns the task a stopping worker still holds to the queue, at once and
// without counting the stopped attempt.
//
// # Why once, after runTask, rather than wherever runTask can be stopped
//
// ‼️ A stop can land anywhere in runTask: before the task starts, at the approval gate,
// inside a model call, during verification, or just after its last write. Each used to
// end the same way. The next write ran on the cancelled context and failed, and the
// task stayed claimed or running under its lease until the reaper recovered it minutes
// later, with the stop counted as an attempt. That is the opposite of what Run's own
// comment promised.
// docs/bugfix/2026-09-15-a-stopped-worker-left-its-task-to-run-out-its-lease.md
//
// A single call covers every one of those points because Release decides from the row,
// not from how far runTask got: it hands back only a task this worker still holds under
// a lease. A task that finished, failed or parked at the gate before the stop holds no
// lease, and one the reaper gave to another worker is not this worker's. Both come back
// as a CONFLICT, and nothing happens, which is right.
//
// On a context that outlives the stop, for afterTask's reason. A hand-back that cannot
// be written leaves the task to the reaper, which is where it was before this existed.
func (w *Worker) handBack(ctx context.Context, task *engine.Task) {
	book, cancel := context.WithTimeout(context.WithoutCancel(ctx), afterTaskTimeout)
	defer cancel()
	switch err := w.queue.Release(book, w.pool, task.ID, w.ID, w.clock.Now()); {
	case err == nil:
	case errs.CodeOf(err) == errs.CodeConflict:
		return
	default:
		w.log.WarnWith(book, logx.EventTaskHandBackFailed, err,
			"task_id", task.ID, "goal_id", task.GoalID, "worker_id", w.ID,
			"detail", "a stopping worker could not hand its task back; the lease reaper will return it when the lease expires")
		return
	}
	w.log.Info(book, logx.EventTaskHandedBack,
		"task_id", task.ID, "goal_id", task.GoalID, "worker_id", w.ID, "stopped_while", string(task.Status))
	w.appendEvent(book, task.GoalID, &task.ID, engine.EventTaskHandedBack, engine.ActorExecutor,
		"The worker running this task was stopped. The task was handed back to the queue, and the stopped attempt does not count against it.",
		map[string]any{"worker": w.ID, "stopped_while": string(task.Status)})
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
	// A stopping worker recovers nobody else's task on its way out. See the note on
	// settleFinishedGoals: a reconciliation the stop cancelled is the next worker's
	// poll, not something that failed.
	if ctx.Err() != nil {
		return
	}
	reaped, err := w.queue.ReapExpiredLeases(ctx, w.pool, w.clock.Now(), 20)
	if err != nil {
		if ctx.Err() == nil {
			w.log.WarnWith(ctx, logx.EventWorkerReaped, err, "worker_id", w.ID)
		}
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
		// ‼️ Through running, because the task is still only claimed and claimed
		// cannot move to failed. Failing it straight from claimed was refused, the
		// refusal was only logged, and the task sat claimed until its lease ran
		// out, to be claimed and refused again: the goal never stopped.
		// docs/bugfix/2026-09-15-a-budget-refusal-left-its-task-claimed.md
		if err := w.transition(ctx, task, engine.StatusRunning, engine.TaskMutation{}); err != nil {
			return
		}
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
		if ctx.Err() != nil {
			return // stopped before the task started; Run hands it back
		}
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

	// A step of a build (Phase 2, stage A1) is run by the build loop, not the
	// tool loop: it is a model to add to, not an instruction to carry out with
	// tools, and it is checked by the kernel rather than argued over. See
	// buildgoal.go.
	if in, ok := buildStepOf(task); ok {
		w.runBuildStep(ctx, goal, task, in)
		return
	}

	workspace, err := w.goalWorkspace(goal.ID)
	if err != nil {
		w.failTask(ctx, task, errs.CodeInternal, err.Error())
		return
	}

	// The rules in force on this project, read fresh rather than cached on the
	// goal: an industry corrected with `forgectl project industry` has to take
	// effect on the next task, not on the next restart. One query against a pool
	// already open, next to a model call that costs seconds.
	//
	// A failure here fails the TASK rather than defaulting to a permissive
	// ceiling. An unreadable rule set is not the same as an unrestricted one, and
	// PackFor's error says which project and how to fix it.
	domain, err := domainworkspace.NewService(w.pool, w.clock, w.log).PackFor(ctx, w.pool, goal.ProjectID)
	if err != nil {
		w.failTask(ctx, task, errs.CodeOf(err), err.Error())
		return
	}

	// Read next to the domain and from the same pool, so "which rules" and "who
	// is accountable for going past them" can never come from two reads that
	// disagree. An unreadable authority fails the task for PackFor's reason: a
	// permission that cannot be read is not an unrestricted one.
	authority, err := domainworkspace.NewService(w.pool, w.clock, w.log).
		ReviewAuthorityFor(ctx, w.pool, goal.ProjectID)
	if err != nil {
		w.failTask(ctx, task, errs.CodeOf(err), err.Error())
		return
	}

	grant := grantFor(goal, domain, authority, w.production)
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
		//
		// ‼️ Unless the stop arrived with the answer. The event below outlives the stop
		// and failTask does not, so without this the timeline would say the task failed
		// while Run handed it back. The stop wins, as it does over a failure.
		if ctx.Err() != nil {
			return
		}
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
		//
		// ‼️ Only a success that was written is recorded. The event outlives a stop and
		// the transition does not; recording one the stop refused would put a success on
		// the timeline of a task Run hands back.
		if err := w.transition(ctx, task, engine.StatusSucceeded, engine.TaskMutation{Result: resultJSON}); err != nil {
			return
		}
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
		//
		// ‼️ Unless the stop cancelled the call. Nothing was verified either way, but the
		// line this wrote — forge.verification.ran with error="context canceled" — is the
		// same line a verifier that really could produce no verdict writes, so every
		// graceful stop during verification read as a verifier failure. Not a database
		// error, so #109's fences never saw it. retryOrFail already returns on a stop;
		// this is the line beside it.
		if ctx.Err() == nil {
			w.log.WarnWith(ctx, logx.EventVerificationRan, err, "task_id", task.ID)
		}
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
		// Only a success that was written is recorded, for the reason above.
		if err := w.transition(ctx, task, engine.StatusSucceeded, engine.TaskMutation{
			Result: resultJSON, Verdict: verdictJSON, VerifiedAt: &now,
		}); err != nil {
			return
		}
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
	// ‼️ A stop is not a failed attempt. What brings a stopping worker here is almost
	// always the stop itself, as a cancelled model or verifier call, and every write
	// below would run on the cancelled context: it used to log a retry, lose its event
	// and fail its transition as DATABASE_UNAVAILABLE, and leave the task under its
	// lease. Run hands the task back instead (handBack).
	if ctx.Err() != nil {
		return
	}
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

	// # Why the request and its event are one transaction
	//
	// ‼️ They were two writes. A stop between them wrote the request and lost the event:
	// the next worker found the request pending and parked on it, and the approvals
	// table held a request that the timeline never mentioned. One transaction means a
	// stop, or a crash, leaves both or neither. With neither, the next worker opens the
	// gate afresh.
	// docs/bugfix/2026-09-15-a-stopped-worker-lost-what-it-had-done-and-blamed-the-database.md
	//
	// Not on a context that outlives the stop: opening the gate is a decision, and a
	// stopped worker leaves it to the next one. A reconciliation that writes the missing
	// event later would be a second place that records a request, and would still leave
	// the gap until some worker came back to the task.
	requested, _ := json.Marshal(map[string]any{"risk_tier": string(task.RiskTier)})
	if err := db.InTx(ctx, w.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			insert into forge_approvals (id, goal_id, task_id, risk_tier, summary, preview, requested_at)
			values ($1,$2,$3,$4,$5,$6,$7)
			on conflict do nothing`,
			id.New(id.PrefixApproval), goal.ID, task.ID, string(task.RiskTier),
			summary, preview, w.clock.Now()); err != nil {
			return errs.Wrap("agent.Worker.checkApproval", errs.CodeDatabaseUnavail, err)
		}
		if w.approvalRowWrittenForTest != nil {
			w.approvalRowWrittenForTest()
		}
		return w.repo.AppendEvent(ctx, tx, &engine.Event{
			GoalID: goal.ID, TaskID: &task.ID, Kind: engine.EventApprovalRequested, Actor: engine.ActorExecutor,
			Summary: "Waiting for a human to approve this action.", Payload: requested,
		}, w.clock.Now())
	}); err != nil {
		return false, err
	}

	w.log.Info(ctx, logx.EventApprovalOpened,
		"task_id", task.ID, "goal_id", goal.ID, "risk_tier", string(task.RiskTier))

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
				// A beat the task's end or a stop cancelled in flight has lost nothing:
				// the task is over, or Run is handing it back. It used to be logged as
				// the database being unavailable and the lease being lost.
				if ctx.Err() != nil {
					return
				}
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
	if w.afterTransitionForTest != nil {
		if forced := w.afterTransitionForTest(to); forced != nil {
			err = forced
		}
	}
	// A failure a stop is involved in may not be a failure at all: the statement can have
	// been on the wire when the stop cancelled it. Ask the row before deciding.
	if err != nil && ctx.Err() != nil && w.stopLanded(ctx, task, to) {
		return nil
	}
	// A transition is a decision, and a stopping worker's decisions are skipped, not
	// failed: the stop refused it, and handBack gives the task to a worker that decides
	// again. Logging it said the database was down. See outliving.
	if err != nil && ctx.Err() == nil {
		w.log.WarnWith(ctx, logx.EventTaskCycleEnded, err,
			"task_id", task.ID, "target", string(to))
	}
	return err
}

// stopLanded reports whether a write the stop appeared to cancel had in fact been
// committed, by reading the row back.
//
// # Why the row is asked rather than the error read
//
// ‼️ pgx refuses a statement on a context that is ALREADY cancelled, and cancels one
// that is already ON THE WIRE — and in the second case Postgres may have committed it
// before the cancel request arrived. The caller is told the same thing either way:
// context canceled. #109 made a stopping worker skip its failed transitions on the
// grounds that handBack passes the decision on, which is right for the first case and
// wrong for the second: a task that really did reach succeeded had no task.succeeded
// event, and nothing ever came back to notice, because nobody claims a succeeded task.
// docs/bugfix/2026-09-15-a-stop-still-guessed-at-a-committed-write-and-lost-a-security-record.md
//
// The row is the only thing that knows, so this asks it, on a context that outlives the
// stop for appendEvent's reason. A "no" — including a row some other worker has since
// moved on from, which this worker's lease makes remote — records nothing, which is the
// side to be wrong on: a decision that was not made is made again by the next worker.
// A read that cannot be made at all says the outcome is unknown, in those words, rather
// than claiming a success or a failure that nobody established.
func (w *Worker) stopLanded(ctx context.Context, task *engine.Task, to engine.TaskStatus) bool {
	rec, cancel := outliving(ctx)
	defer cancel()

	current, err := w.repo.GetTask(rec, w.pool, task.ID)
	if err != nil {
		w.log.WarnWith(rec, logx.EventTaskCycleEnded, err,
			"task_id", task.ID, "target", string(to),
			"detail", "the stop cancelled this transition while it was on the wire and the row could not be "+
				"read back, so whether Postgres applied it is UNKNOWN; nothing has been recorded either way")
		return false
	}
	if current.Status != to {
		return false
	}
	// It landed, so the decision was made and its record is owed. TransitionTask sets
	// this on the way out of its own success, and every caller reads it afterwards.
	task.Status = to
	return true
}

func (w *Worker) failTask(ctx context.Context, task *engine.Task, code errs.Code, detail string) {
	// ‼️ A failure reached while stopping is the stop's (a read the stop cancelled, the
	// approval transaction it rolled back) or loses to it, for retryOrFail's reason.
	// Both writes below would fail on the cancelled context and be logged as the
	// database being unavailable. Run hands the task back instead.
	if ctx.Err() != nil {
		return
	}
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
	// An event records what already happened, so a stop that arrives after it must not
	// lose it. See outliving.
	rec, cancel := outliving(ctx)
	defer cancel()
	if err := w.repo.AppendEvent(rec, w.pool, ev, w.clock.Now()); err != nil {
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
		// Not when the stop cancelled the read, for the reason above: no verdict is
		// being reached on thinner evidence, because the verifier call after this one
		// is cancelled too.
		if ctx.Err() == nil {
			w.log.WarnWith(ctx, logx.EventVerificationRan, err, "task_id", taskID,
				"detail", "raw tool output could not be read; the verifier is judging the executor's account of it instead")
		}
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
//
// # Why the domain pack is a second ceiling (2026-09-04)
//
// The ceiling used to be the goal's own tier alone. That meant the domain a
// project works in had no bearing on what could be done in it: a goal created at
// r2 reached r2 whether the project was software, where a merge is reviewed and
// reversible, or civil, where the equivalent act needs a licensed engineer this
// build cannot represent.
//
// The pack was the natural home for that limit and was doing nothing — the
// column was written by EnsureProject and read by no rule anywhere. This is the
// read. The LOWER of the two ceilings applies, because both are statements about
// what may happen and the stricter of two limits is the one that means anything.
//
// Deliberately not additive with the goal's tier: a project cannot RAISE what a
// goal may do. A pack with a high ceiling permits nothing the goal did not
// already permit, which keeps the property that lowering a goal's tier can only
// ever narrow it.
//
// See docs/bugfix/2026-09-04-the-pack-was-written-and-never-read.md.
func grantFor(goal *engine.Goal, domain domainpack.Definition, authority domainworkspace.ReviewAuthority,
	production bool) tools.Grant {
	caps := []tools.Capability{tools.CapRead}
	if goal.Autonomy.AtLeast(engine.AutonomyDraft) {
		caps = append(caps, tools.CapWrite)
	}
	if goal.Autonomy.AllowsExecution() {
		caps = append(caps, tools.CapExecute, tools.CapSimulate)
	}
	// The ONLY place a domain ceiling rises, and only for an attributed claim.
	//
	// What was established is that a named person accepted responsibility. What
	// was NOT established is a qualification: this build cannot check a licence,
	// and CeilingSource below says so in those words. Without that sentence this
	// mechanism would launder authority nothing verified.
	domainCeiling := domain.CeilingWith(authority.Recorded())
	ceiling := lowerTier(goal.RiskTier, domainCeiling)
	// Named only when the DOMAIN is the binding limit. When the goal's own tier
	// is what stops the work there is no second authority to point at, and
	// naming the pack anyway would send somebody to change an industry that was
	// not the thing in their way.
	var source string
	if domainCeiling.Valid() && ceiling == domainCeiling && ceiling != goal.RiskTier {
		source = fmt.Sprintf("That ceiling is the %s domain's, not this goal's: %s Work above %s "+
			"here would require %s.", domain.Pack, domain.Summary, domainCeiling, domain.Requires)
		if authority.Recorded() {
			// Said wherever a raised ceiling is in play, including when something
			// is refused ABOVE the raised one. A reader has to know the limit they
			// hit moved, and on what.
			source += fmt.Sprintf(" This project's ceiling was raised to %s because %s was "+
				"recorded as %s — RECORDED, NOT VERIFIED: this build cannot check a "+
				"qualification, and what it holds is a claim attributed to whoever made it.",
				domainCeiling, authority.Holder, domain.ReviewAuthority)
		}
	}
	return tools.Grant{
		Capabilities:  caps,
		MaxRiskTier:   ceiling,
		Autonomy:      goal.Autonomy,
		Production:    production,
		CeilingSource: source,
	}
}

// lowerTier returns the stricter of two ceilings.
//
// An invalid pack ceiling yields the goal's own, which is the pre-pack
// behaviour: this is reached only by callers holding a zero Definition, and
// silently widening to "no ceiling" would be the worst possible reading of an
// absent limit.
func lowerTier(goalTier, packTier engine.RiskTier) engine.RiskTier {
	if !packTier.Valid() {
		return goalTier
	}
	if packTier.AtLeast(goalTier) {
		return goalTier
	}
	return packTier
}
