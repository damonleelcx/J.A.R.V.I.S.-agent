package engine

import (
	"encoding/json"
	"time"
)

// Goal is a long-running objective.
type Goal struct {
	ID        string
	ProjectID string
	CreatedBy string
	Title     string
	// Statement is the objective in the user's own words, preserved verbatim.
	// Every summary downstream is derived from it; this is the thing they can
	// be checked against.
	Statement string
	Status    GoalStatus
	Autonomy  Autonomy
	RiskTier  RiskTier

	// CompletionCriteria are machine-checkable, so the verifier has something to
	// evaluate rather than interpret.
	CompletionCriteria []CompletionCriterion
	TargetCompletionAt *time.Time

	Budget Budget
	Spend  Spend

	OutcomeSummary string
	FailureCode    string

	StartedAt *time.Time
	EndedAt   *time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

// CompletionCriterion is one testable condition for a goal being done.
//
// Bullet B1 asks for "measurable completion criteria". Prose criteria cannot be
// checked, only agreed with — so each carries a Check the verifier can act on
// and a Rationale explaining why it belongs.
type CompletionCriterion struct {
	ID string `json:"id"`
	// Statement is the criterion in human terms.
	Statement string `json:"statement"`
	// Check names how it is confirmed: "test", "artifact_exists", "human_review",
	// "tool_output_matches". An empty Check is a criterion nothing can verify,
	// and is reported as such rather than treated as satisfied.
	Check string `json:"check"`
	// Args parameterises the check.
	Args map[string]any `json:"args,omitempty"`
	// Satisfied and SatisfiedAt are set only by the verifier, never by the
	// executor. PRD AGT-08: completing work and confirming it are distinct facts.
	Satisfied   bool       `json:"satisfied"`
	SatisfiedAt *time.Time `json:"satisfied_at,omitempty"`
	// Evidence points at what confirmed it — a tool call id, an artifact, an
	// approval. A criterion marked satisfied with no evidence is a claim.
	Evidence string `json:"evidence,omitempty"`
}

// Verifiable reports whether this criterion names a check that can actually run.
func (c CompletionCriterion) Verifiable() bool { return c.Check != "" }

// Budget bounds one goal along every axis it can run away on.
//
// A nil field means "inherit the process default", so a deployment-wide change
// reaches goals that never overrode it.
type Budget struct {
	MaxTokens    *int64
	MaxCostCents *int64
	MaxWallClock *time.Duration
	MaxTasks     *int
}

// Spend is the running total against Budget.
type Spend struct {
	Tokens       int64
	CostCents    int64
	TasksCreated int
}

// Milestone is a checkpoint on the way to a goal.
type Milestone struct {
	ID         string
	GoalID     string
	Ordinal    int
	Title      string
	Criterion  string
	Status     string
	AchievedAt *time.Time
	CreatedAt  time.Time
}

// Plan is a versioned decomposition of a goal into tasks.
type Plan struct {
	ID           string
	GoalID       string
	Version      int
	Rationale    string
	Author       string
	SupersededAt *time.Time
	CreatedAt    time.Time
}

// Live reports whether this is the plan currently in force.
func (p *Plan) Live() bool { return p.SupersededAt == nil }

// Task is one durable unit of work — a node in the goal's DAG.
type Task struct {
	ID           string
	GoalID       string
	PlanID       string
	ParentTaskID *string
	Depth        int

	Title       string
	Instruction string
	// Inputs and ExpectedOutput give the task "clear inputs and outputs"
	// (bullet B2). Without a declared expectation the verifier has nothing to
	// check against and degenerates into asking the model whether it is happy.
	Inputs         json.RawMessage
	ExpectedOutput json.RawMessage

	Status TaskStatus

	// IdempotencyKey deduplicates work within a goal (bullet B8).
	IdempotencyKey string

	AttemptCount int
	MaxAttempts  int

	LeaseOwner     *string
	LeaseExpiresAt *time.Time

	// NotBefore hides the task from the queue until this instant. Retry backoff,
	// a timer, and an external wait all reduce to setting it (bullet B11).
	NotBefore time.Time
	Priority  int

	RiskTier         RiskTier
	RequiresApproval bool

	Result json.RawMessage
	// VerifiedAt is a separate fact from Status == Succeeded. PRD AGT-06 forbids
	// treating completion as verification.
	VerifiedAt *time.Time
	Verdict    json.RawMessage

	ErrorCode   string
	ErrorDetail string

	StartedAt *time.Time
	EndedAt   *time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Verified reports whether evidence has confirmed this task's result.
//
// Deliberately not the same as Status == StatusSucceeded. A task can succeed and
// fail verification; reporting the two as one is how a system claims something
// was checked when nothing checked it.
func (t *Task) Verified() bool { return t.VerifiedAt != nil }

// LeaseHeldBy reports whether owner currently holds this task's lease at now.
func (t *Task) LeaseHeldBy(owner string, now time.Time) bool {
	return t.LeaseOwner != nil && *t.LeaseOwner == owner &&
		t.LeaseExpiresAt != nil && now.Before(*t.LeaseExpiresAt)
}

// LeaseExpired reports whether a lease exists but has lapsed — the signal that
// a worker died mid-task.
func (t *Task) LeaseExpired(now time.Time) bool {
	return t.LeaseOwner != nil && t.LeaseExpiresAt != nil && !now.Before(*t.LeaseExpiresAt)
}

// AttemptsRemaining reports how many tries are left.
func (t *Task) AttemptsRemaining() int {
	if t.AttemptCount >= t.MaxAttempts {
		return 0
	}
	return t.MaxAttempts - t.AttemptCount
}

// Checkpoint is durable resumable state for a task.
type Checkpoint struct {
	ID        string
	TaskID    string
	Seq       int
	Kind      string
	State     json.RawMessage
	CreatedAt time.Time
}

// Checkpoint kinds. Enumerated so the timeline can be filtered by a stable
// token rather than by a sentence somebody typed.
const (
	CheckpointIterationEnd    = "iteration_end"
	CheckpointToolResult      = "tool_result"
	CheckpointApprovalGranted = "approval_granted"
	CheckpointVerification    = "verification_done"
	CheckpointResumeState     = "resume_state"
)

// Event is one entry in the append-only execution timeline.
type Event struct {
	ID        string
	GoalID    string
	TaskID    *string
	Seq       int64
	Kind      string
	Actor     Actor
	ActorID   *string
	Summary   string
	Payload   json.RawMessage
	CreatedAt time.Time
}

// Actor names who caused an event.
//
// PRD requires "Forge proposed", "human approved" and "Forge executed" to be
// distinguishable, which is impossible if authorship is inferred rather than
// recorded.
type Actor string

const (
	ActorPlanner   Actor = "planner"
	ActorExecutor  Actor = "executor"
	ActorVerifier  Actor = "verifier"
	ActorHuman     Actor = "human"
	ActorScheduler Actor = "scheduler"
	ActorSystem    Actor = "system"
	// ActorConverse is the workbench conversation (PRD VIS-04). It proposes
	// geometry outside any goal, and it is none of the five above: 'human'
	// would credit a person with a shape FORGE drew, and 'system' would
	// attribute a proposal to infrastructure. Added in migration 0011.
	ActorConverse Actor = "converse"
)

// Valid reports whether a is a recognised actor.
func (a Actor) Valid() bool {
	switch a {
	case ActorPlanner, ActorExecutor, ActorVerifier, ActorHuman, ActorScheduler, ActorSystem, ActorConverse:
		return true
	}
	return false
}

// AllActors returns every actor, for the schema-coherence fence.
func AllActors() []Actor {
	return []Actor{ActorPlanner, ActorExecutor, ActorVerifier, ActorHuman, ActorScheduler, ActorSystem, ActorConverse}
}

// Event kinds, enumerated for the same reason as log event names.
const (
	EventGoalCreated       = "goal.created"
	EventGoalActivated     = "goal.activated"
	EventGoalPaused        = "goal.paused"
	EventGoalResumed       = "goal.resumed"
	EventGoalEnded         = "goal.ended"
	EventPlanCreated       = "plan.created"
	EventPlanSuperseded    = "plan.superseded"
	EventTaskCreated       = "task.created"
	EventTaskReady         = "task.ready"
	EventTaskClaimed       = "task.claimed"
	EventTaskStarted       = "task.started"
	EventTaskCheckpointed  = "task.checkpointed"
	EventTaskSucceeded     = "task.succeeded"
	EventTaskFailed        = "task.failed"
	EventTaskRetrying      = "task.retrying"
	EventTaskCancelled     = "task.cancelled"
	EventTaskSkipped       = "task.skipped"
	EventTaskLeaseExpired  = "task.lease_expired"
	EventToolCalled        = "tool.called"
	EventToolSucceeded     = "tool.succeeded"
	EventToolFailed        = "tool.failed"
	EventToolRefused       = "tool.refused"
	EventToolDeduplicated  = "tool.deduplicated"
	EventApprovalRequested = "approval.requested"
	EventApprovalGranted   = "approval.granted"
	EventApprovalRejected  = "approval.rejected"
	EventApprovalExpired   = "approval.expired"
	EventVerificationOK    = "verification.passed"
	EventVerificationFail  = "verification.failed"
	EventBudgetExceeded    = "budget.exceeded"
	EventReplanTriggered   = "replan.triggered"
	// EventArtifactChanged is the timeline's record of a change to an artifact
	// (PRD WRK-04). The artifact version points back at the event, which is what
	// puts every change inside the tamper-evident chain rather than beside it.
	EventArtifactChanged = "artifact.changed"
)

// ToolCall is one invocation in the idempotency ledger.
type ToolCall struct {
	ID     string
	TaskID string
	// IdempotencyKey is globally unique, not per task: the same logical action
	// must deduplicate even if a replan moved it to a different task.
	IdempotencyKey string
	ToolName       string
	Input          json.RawMessage

	Status ToolCallStatus

	Output json.RawMessage
	// RawOutput is the unedited result. PRD AGT-06: preserve raw outputs and
	// distinguish tool evidence from model inference.
	RawOutput   string
	ErrorCode   string
	ErrorDetail string

	Attempt    int
	StartedAt  *time.Time
	EndedAt    *time.Time
	DurationMS int64
	CreatedAt  time.Time
}

// ToolCallStatus is a tool call's lifecycle position.
type ToolCallStatus string

const (
	ToolPending   ToolCallStatus = "pending"
	ToolRunning   ToolCallStatus = "running"
	ToolSucceeded ToolCallStatus = "succeeded"
	ToolFailed    ToolCallStatus = "failed"
	// ToolRefused means the policy plane declined to run it (PRD SAF-04). This
	// is a correct outcome, not an error to retry — which is exactly why it is
	// not folded into ToolFailed.
	ToolRefused ToolCallStatus = "refused"
)

// Terminal reports whether the call has resolved.
func (s ToolCallStatus) Terminal() bool {
	return s == ToolSucceeded || s == ToolFailed || s == ToolRefused
}

// AllToolCallStatuses returns every status, for the coherence fence.
func AllToolCallStatuses() []ToolCallStatus {
	return []ToolCallStatus{ToolPending, ToolRunning, ToolSucceeded, ToolFailed, ToolRefused}
}

// Approval is a durable human gate.
type Approval struct {
	ID       string
	GoalID   string
	TaskID   string
	RiskTier RiskTier
	// Summary and Preview are what a reviewer decides on. PRD AGT-02 requires
	// the intent, affected artifacts, expected outputs, risks, and what cannot
	// be undone — at a level appropriate to the consequence.
	Summary string
	Preview json.RawMessage

	RequestedAt time.Time
	ExpiresAt   *time.Time

	Decision       ApprovalDecision
	DecidedBy      *string
	DecidedAt      *time.Time
	DecisionReason string
}

// ApprovalDecision is the outcome of a gate.
type ApprovalDecision string

const (
	ApprovalPending   ApprovalDecision = "pending"
	ApprovalApproved  ApprovalDecision = "approved"
	ApprovalRejected  ApprovalDecision = "rejected"
	ApprovalExpired   ApprovalDecision = "expired"
	ApprovalWithdrawn ApprovalDecision = "withdrawn"
)

// Decided reports whether the gate has resolved.
func (d ApprovalDecision) Decided() bool { return d != ApprovalPending }

// ByHuman reports whether this outcome came from a person. Expired and
// withdrawn are system outcomes and carry no decider, which is why the database
// constraint excludes them from the attribution requirement.
func (d ApprovalDecision) ByHuman() bool {
	return d == ApprovalApproved || d == ApprovalRejected
}

// AllApprovalDecisions returns every decision, for the coherence fence.
func AllApprovalDecisions() []ApprovalDecision {
	return []ApprovalDecision{ApprovalPending, ApprovalApproved, ApprovalRejected, ApprovalExpired, ApprovalWithdrawn}
}
