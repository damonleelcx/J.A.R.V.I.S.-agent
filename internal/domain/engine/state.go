// Package engine is the durable workflow bounded context: goals, plans, the
// task DAG, the job queue, checkpoints, the execution timeline, the tool-call
// ledger, and human approval gates.
//
// These aggregates live in one package because they change together. A task
// transition writes a task row, appends a timeline event, and sometimes releases
// a lease or opens an approval — one atomic unit. Splitting them across packages
// would either produce a cycle or an anaemic model with the real rules living in
// a service that reaches into everything.
package engine

import (
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// TaskStatus is a task's position in its lifecycle.
//
// PRD AGT-08 requires that proposed, approved, running, failed, completed,
// verified, accepted and released stay distinct and are never implied falsely.
// The most important distinction encoded here is that **Succeeded does not mean
// verified**: the executor finishing is one fact, evidence confirming the result
// is another, and a human accepting it is a third. Collapsing them is how a
// system ends up reporting that something was checked when nothing checked it.
type TaskStatus string

const (
	// StatusPending means created, dependencies not yet satisfied.
	StatusPending TaskStatus = "pending"
	// StatusReady means every dependency succeeded; the task is queueable.
	StatusReady TaskStatus = "ready"
	// StatusClaimed means a worker holds a lease but has not begun.
	StatusClaimed TaskStatus = "claimed"
	// StatusRunning means the executor is working.
	StatusRunning TaskStatus = "running"
	// StatusAwaitingApproval means work is blocked on a human gate. The task
	// holds no lease here: a gate can stay open for days, and a lease held that
	// long would either expire noisily or pin a worker for nothing.
	StatusAwaitingApproval TaskStatus = "awaiting_approval"
	// StatusVerifying means the executor finished and evidence is being checked.
	StatusVerifying TaskStatus = "verifying"
	// StatusSucceeded means the work completed. It does NOT mean verified.
	StatusSucceeded TaskStatus = "succeeded"
	// StatusFailed means the work will not complete; attempts are exhausted or
	// the failure is not retryable.
	StatusFailed TaskStatus = "failed"
	// StatusCancelled means a human or the engine stopped it deliberately.
	StatusCancelled TaskStatus = "cancelled"
	// StatusSkipped means the task became unnecessary — usually because a
	// replan superseded it, or a dependency failed terminally.
	StatusSkipped TaskStatus = "skipped"
)

// Terminal reports whether no further transition is possible.
func (s TaskStatus) Terminal() bool {
	switch s {
	case StatusSucceeded, StatusFailed, StatusCancelled, StatusSkipped:
		return true
	}
	return false
}

// HoldsLease reports whether a task in this state may hold a worker lease.
//
// AwaitingApproval is deliberately absent. A gate may stay open for days; a
// lease held across it would either expire and look like a crashed worker, or
// occupy a worker slot doing nothing.
func (s TaskStatus) HoldsLease() bool {
	switch s {
	case StatusClaimed, StatusRunning, StatusVerifying:
		return true
	}
	return false
}

// Active reports whether the task still counts as outstanding work.
func (s TaskStatus) Active() bool { return !s.Terminal() }

// transitions is the complete legal state graph.
//
// It is a table rather than a switch for one reason: a table can be *read* as a
// specification, printed, and diffed. Every transition this engine may perform
// is visible here in twenty lines, which is the only way a reviewer can answer
// "can a task go from awaiting_approval straight to succeeded?" without tracing
// call sites. (It cannot.)
var transitions = map[TaskStatus][]TaskStatus{
	StatusPending: {
		StatusReady,     // dependencies satisfied
		StatusSkipped,   // superseded by a replan, or a dependency failed
		StatusCancelled, // goal cancelled
	},
	StatusReady: {
		StatusClaimed,
		StatusSkipped,
		StatusCancelled,
	},
	StatusClaimed: {
		StatusRunning,
		StatusReady, // lease expired or released without progress
		StatusCancelled,
	},
	StatusRunning: {
		StatusAwaitingApproval, // hit a gate mid-execution
		StatusVerifying,        // work done, evidence pending
		StatusSucceeded,        // work done, no verification required (r0/r1)
		StatusFailed,
		StatusReady, // attempt failed retryably; back to the queue
		StatusCancelled,
	},
	StatusAwaitingApproval: {
		StatusReady,     // approved — requeued so a fresh worker resumes it
		StatusFailed,    // rejected
		StatusCancelled, // gate expired or withdrawn
	},
	StatusVerifying: {
		StatusSucceeded,
		StatusFailed,
		StatusReady, // verification inconclusive and retryable
		StatusCancelled,
	},
	// Terminal states have no outgoing transitions. Reopening a finished task
	// would make the timeline unreadable; a replan creates a new task instead.
	StatusSucceeded: {},
	StatusFailed:    {},
	StatusCancelled: {},
	StatusSkipped:   {},
}

// CanTransition reports whether from → to is legal.
func CanTransition(from, to TaskStatus) bool {
	for _, allowed := range transitions[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

// ValidateTransition returns a typed error explaining an illegal transition.
//
// Called before every status write. An engine that only checks transitions in
// tests will eventually perform an illegal one in production and leave a row no
// code path knows how to advance — a task stuck forever with no error anywhere.
func ValidateTransition(from, to TaskStatus) error {
	const op = "engine.ValidateTransition"

	if !from.Valid() {
		return errs.New(op, errs.CodeStateCorrupt).
			WithDetail("current status %q is not a recognised task state", from)
	}
	if !to.Valid() {
		return errs.New(op, errs.CodeInvariantViolated).
			WithDetail("target status %q is not a recognised task state", to)
	}
	if from == to {
		return errs.New(op, errs.CodeInvariantViolated).
			WithDetail("task is already %q; a self-transition would append a timeline event describing no change", from)
	}
	if from.Terminal() {
		return errs.New(op, errs.CodeInvariantViolated).
			WithDetail("task is terminal (%q) and cannot move to %q; a replan must create a new task rather than reopen a finished one", from, to)
	}
	if !CanTransition(from, to) {
		return errs.New(op, errs.CodeInvariantViolated).
			WithDetail("%q → %q is not a legal transition; legal targets are %v", from, to, transitions[from])
	}
	return nil
}

// Valid reports whether s is a recognised status.
func (s TaskStatus) Valid() bool {
	_, ok := transitions[s]
	return ok
}

// AllTaskStatuses returns every status. Used by the fence that keeps this graph
// in step with the database CHECK constraint.
func AllTaskStatuses() []TaskStatus {
	return []TaskStatus{
		StatusPending, StatusReady, StatusClaimed, StatusRunning,
		StatusAwaitingApproval, StatusVerifying,
		StatusSucceeded, StatusFailed, StatusCancelled, StatusSkipped,
	}
}

// ---------------------------------------------------------------------------
// goal status
// ---------------------------------------------------------------------------

// GoalStatus is a goal's lifecycle position.
type GoalStatus string

const (
	GoalDraft     GoalStatus = "draft"
	GoalActive    GoalStatus = "active"
	GoalPaused    GoalStatus = "paused"
	GoalSucceeded GoalStatus = "succeeded"
	GoalFailed    GoalStatus = "failed"
	GoalCancelled GoalStatus = "cancelled"
)

// Terminal reports whether the goal has finished.
func (s GoalStatus) Terminal() bool {
	return s == GoalSucceeded || s == GoalFailed || s == GoalCancelled
}

// goalTransitions is the goal-level state graph.
//
// Pause and resume (bullet B10) are first-class rather than a flag, because a
// paused goal must be distinguishable from a stalled one. A flag beside "active"
// would let both look the same to the scheduler.
var goalTransitions = map[GoalStatus][]GoalStatus{
	GoalDraft:     {GoalActive, GoalCancelled},
	GoalActive:    {GoalPaused, GoalSucceeded, GoalFailed, GoalCancelled},
	GoalPaused:    {GoalActive, GoalCancelled, GoalFailed},
	GoalSucceeded: {},
	GoalFailed:    {},
	GoalCancelled: {},
}

// Valid reports whether s is a recognised goal status.
func (s GoalStatus) Valid() bool {
	_, ok := goalTransitions[s]
	return ok
}

// ValidateGoalTransition returns a typed error explaining an illegal move.
func ValidateGoalTransition(from, to GoalStatus) error {
	const op = "engine.ValidateGoalTransition"

	if !from.Valid() {
		return errs.New(op, errs.CodeStateCorrupt).
			WithDetail("current goal status %q is not recognised", from)
	}
	if !to.Valid() {
		return errs.New(op, errs.CodeInvariantViolated).
			WithDetail("target goal status %q is not recognised", to)
	}
	if from == to {
		return errs.New(op, errs.CodeInvariantViolated).
			WithDetail("goal is already %q", from)
	}
	if from.Terminal() {
		return errs.New(op, errs.CodeInvariantViolated).
			WithDetail("goal is terminal (%q) and cannot move to %q", from, to)
	}
	for _, allowed := range goalTransitions[from] {
		if allowed == to {
			return nil
		}
	}
	return errs.New(op, errs.CodeInvariantViolated).
		WithDetail("%q → %q is not a legal goal transition; legal targets are %v", from, to, goalTransitions[from])
}

// AllGoalStatuses returns every goal status.
func AllGoalStatuses() []GoalStatus {
	return []GoalStatus{GoalDraft, GoalActive, GoalPaused, GoalSucceeded, GoalFailed, GoalCancelled}
}

// ---------------------------------------------------------------------------
// autonomy and risk
// ---------------------------------------------------------------------------

// Autonomy is the ladder from PRD AGT-04.
//
// Ordered, and the order is enforced: PRD AGT-04 says the system "shall never
// silently raise its autonomy level", which is only checkable if levels compare.
type Autonomy string

const (
	// AutonomyDiscuss may reason and answer. It may not draft or act.
	AutonomyDiscuss Autonomy = "discuss"
	// AutonomyDraft may produce artifacts for review. It may not execute.
	AutonomyDraft Autonomy = "draft"
	// AutonomySandboxExecute may run reversible work inside the project boundary.
	AutonomySandboxExecute Autonomy = "sandbox_execute"
	// AutonomyApprovalGated may perform consequential digital work, each such
	// action gated by an explicit human approval.
	AutonomyApprovalGated Autonomy = "approval_gated"
	// AutonomyProhibited blocks all agentic action. Not the bottom of the
	// ladder — it is off it, a deliberate stop rather than a low setting.
	AutonomyProhibited Autonomy = "prohibited"
)

// rank orders the ladder for comparison. Prohibited is -1 rather than 0: it is
// not "less autonomy than discuss", it is a refusal, and giving it a rank inside
// the ladder would let an off-by-one comparison quietly enable it.
var autonomyRank = map[Autonomy]int{
	AutonomyProhibited:     -1,
	AutonomyDiscuss:        0,
	AutonomyDraft:          1,
	AutonomySandboxExecute: 2,
	AutonomyApprovalGated:  3,
}

// Valid reports whether a is a recognised level.
func (a Autonomy) Valid() bool {
	_, ok := autonomyRank[a]
	return ok
}

// AtLeast reports whether a permits everything min permits.
//
// Prohibited never satisfies anything, including itself, so a caller cannot
// accidentally treat "prohibited" as sufficient for a prohibited-level action.
func (a Autonomy) AtLeast(min Autonomy) bool {
	if a == AutonomyProhibited || min == AutonomyProhibited {
		return false
	}
	ra, okA := autonomyRank[a]
	rm, okM := autonomyRank[min]
	return okA && okM && ra >= rm
}

// AllowsExecution reports whether this level may run tools at all.
func (a Autonomy) AllowsExecution() bool { return a.AtLeast(AutonomySandboxExecute) }

// RiskTier is PRD §8.1.
type RiskTier string

const (
	// RiskR0 — general discussion, no project context.
	RiskR0 RiskTier = "r0"
	// RiskR1 — reversible draft inside a sandbox.
	RiskR1 RiskTier = "r1"
	// RiskR2 — consequential digital action: merge, baseline change, costly run.
	RiskR2 RiskTier = "r2"
	// RiskR3 — release or operational preparation.
	RiskR3 RiskTier = "r3"
	// RiskR4 — safety-critical support.
	RiskR4 RiskTier = "r4"
	// RiskR5 — prohibited. Refuse, enter a safe state, preserve evidence.
	RiskR5 RiskTier = "r5"
)

var riskRank = map[RiskTier]int{
	RiskR0: 0, RiskR1: 1, RiskR2: 2, RiskR3: 3, RiskR4: 4, RiskR5: 5,
}

// Valid reports whether t is a recognised tier.
func (t RiskTier) Valid() bool {
	_, ok := riskRank[t]
	return ok
}

// AtLeast reports whether t is as severe as min.
func (t RiskTier) AtLeast(min RiskTier) bool {
	rt, okT := riskRank[t]
	rm, okM := riskRank[min]
	return okT && okM && rt >= rm
}

// Prohibited reports whether this tier may never be performed, with or without
// approval. PRD §8.1: R5 is refused, not gated.
func (t RiskTier) Prohibited() bool { return t == RiskR5 }

// RequiresApproval reports whether an action at this tier needs a human gate
// before it may proceed.
//
// R2 and above. R0 and R1 are reversible and sandboxed; gating them would train
// reviewers to click through, which is how a gate stops being a control.
func (t RiskTier) RequiresApproval() bool { return t.AtLeast(RiskR2) && !t.Prohibited() }

// AllRiskTiers returns every tier.
func AllRiskTiers() []RiskTier { return []RiskTier{RiskR0, RiskR1, RiskR2, RiskR3, RiskR4, RiskR5} }

// AllAutonomyLevels returns every level.
func AllAutonomyLevels() []Autonomy {
	return []Autonomy{AutonomyDiscuss, AutonomyDraft, AutonomySandboxExecute, AutonomyApprovalGated, AutonomyProhibited}
}
