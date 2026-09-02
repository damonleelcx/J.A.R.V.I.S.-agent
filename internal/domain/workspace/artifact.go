package workspace

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// The artifact lifecycle (PRD WRK-04).
//
// # The requirement, read literally
//
// "Every change identifies initiator, agent, tool, inputs, diff, verification
// state, human disposition." So a version missing any of the seven is REFUSED,
// not stored with a blank. A lifecycle record with holes in it is worse than no
// record: it looks like an audit trail and answers none of the questions one is
// kept for.
//
// # The distinction the whole thing turns on
//
// Verification state is what a MACHINE found. Human disposition is what a PERSON
// decided. They are separate columns and separate vocabularies, and nothing
// derives one from the other.
//
// This is PRD SAF-05 in storage form: "the AI approved it" is never acceptable
// authority. A single column would eventually be set to "accepted" by a passing
// test suite, and the row would then assert that somebody signed off on
// something nobody looked at.

// ArtifactKind is what sort of thing an artifact is.
type ArtifactKind string

const (
	ArtifactFile     ArtifactKind = "file"
	ArtifactDocument ArtifactKind = "document"
	ArtifactModel    ArtifactKind = "model"
	ArtifactDrawing  ArtifactKind = "drawing"
	ArtifactDataset  ArtifactKind = "dataset"
	ArtifactReport   ArtifactKind = "report"
)

var artifactKinds = []ArtifactKind{ArtifactFile, ArtifactDocument, ArtifactModel,
	ArtifactDrawing, ArtifactDataset, ArtifactReport}

// Valid reports whether k is a recognised artifact kind.
func (k ArtifactKind) Valid() bool {
	for _, a := range artifactKinds {
		if a == k {
			return true
		}
	}
	return false
}

// Agent is which part of FORGE produced a version.
//
// The same vocabulary as forge_events.actor, deliberately: a version and the
// event that recorded it must not be able to disagree about who acted.
type Agent string

const (
	AgentPlanner   Agent = "planner"
	AgentExecutor  Agent = "executor"
	AgentVerifier  Agent = "verifier"
	AgentHuman     Agent = "human"
	AgentScheduler Agent = "scheduler"
	AgentSystem    Agent = "system"
)

var agents = []Agent{AgentPlanner, AgentExecutor, AgentVerifier, AgentHuman, AgentScheduler, AgentSystem}

// Valid reports whether a is a recognised agent.
func (a Agent) Valid() bool {
	for _, x := range agents {
		if x == a {
			return true
		}
	}
	return false
}

// Verification is what a machine determined about a version.
type Verification string

const (
	// Unverified — nothing has checked it. The honest default, and the one a
	// version starts in.
	Unverified Verification = "unverified"
	Passed     Verification = "passed"
	Failed     Verification = "failed"
)

// Valid reports whether v is a recognised verification state.
func (v Verification) Valid() bool {
	return v == Unverified || v == Passed || v == Failed
}

// Disposition is what a person decided about a version.
type Disposition string

const (
	// Pending — no person has looked at it. Not "approved by default".
	Pending  Disposition = "pending"
	Accepted Disposition = "accepted"
	Rejected Disposition = "rejected"
	// Superseded — a later version replaced it before anybody ruled on it. A
	// real outcome rather than a lingering "pending" that implies somebody still
	// owes a decision on a version nothing uses.
	Superseded Disposition = "superseded"
)

// Valid reports whether d is a recognised disposition.
func (d Disposition) Valid() bool {
	return d == Pending || d == Accepted || d == Rejected || d == Superseded
}

// Artifact is a thing produced inside the project's authorised boundary.
type Artifact struct {
	ID        string
	ProjectID string
	// Path is relative to the project workspace — WRK-04's "authorized boundary".
	Path      string
	Kind      ArtifactKind
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Version is one change to an artifact, carrying WRK-04's seven facts.
type Version struct {
	ID         string
	ArtifactID string
	Version    int

	// 1. Initiator: the person whose intent this serves. Never empty, and never
	//    a service account: a scheduled run traces to whoever configured it.
	InitiatorID string
	// 2. Agent: which part of FORGE produced it.
	Agent Agent
	// 3. Tool: the call that made it. Nil only when Agent is human — inventing a
	//    tool call to satisfy the field would put a fabricated row in the ledger.
	ToolCallID *string
	// 4. Inputs: what it was made from.
	Inputs json.RawMessage
	// 5. Diff: what changed. "" is legal and means "nothing textual to show";
	//    it is stored as an empty string rather than NULL so that "no diff
	//    recorded" cannot be confused with "nothing changed".
	Diff string

	// 6. What a machine found.
	Verification     Verification
	VerificationNote string

	// 7. What a person decided.
	Disposition       Disposition
	DispositionedBy   *string
	DispositionedAt   *time.Time
	DispositionReason string

	// EventID points at the timeline row that recorded this change — the first
	// thing in the system to point at the audit chain (PRD SAF-06). Nil when the
	// change happened outside a goal, such as a person uploading a spec.
	EventID *string

	CreatedAt time.Time
}

// Settled reports whether a person has ruled on this version.
//
// Deliberately not "approved": a rejection is settled too. The question this
// answers is "does somebody still owe a decision", which is the one an operator
// asks about a queue.
func (v *Version) Settled() bool {
	return v.Disposition != Pending
}

// Usable reports whether this version may be relied on, and says why not when it
// may not.
//
// Both facts are required, and in this order: a machine's pass does not make a
// change acceptable, and a person's acceptance does not make it correct. A
// caller that wanted only one of them should ask for that one by name.
func (v *Version) Usable() error {
	const op = "workspace.Version.Usable"

	switch v.Disposition {
	case Rejected:
		return errs.New(op, errs.CodeForbidden).
			WithDetail("version %d was rejected by a person%s", v.Version, reasonSuffix(v.DispositionReason))
	case Superseded:
		return errs.New(op, errs.CodeConflict).
			WithDetail("version %d was replaced before anybody ruled on it; read the current version instead", v.Version)
	case Pending:
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("version %d has not been looked at by a person yet. "+
				"Verification state is %q, which is what a machine found; it is not a sign-off.", v.Version, v.Verification)
	}
	if v.Verification != Passed {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("version %d was accepted by a person but its verification state is %q%s",
				v.Version, v.Verification, reasonSuffix(v.VerificationNote))
	}
	return nil
}

// Validate enforces WRK-04's seven before a version is written.
//
// Every branch here exists because the requirement names the field. This is the
// one place in the codebase where a long list of required-field checks is the
// design rather than a smell: the requirement IS the list.
func (v *Version) Validate() error {
	const op = "workspace.Version.Validate"

	if strings.TrimSpace(v.ArtifactID) == "" {
		return errs.New(op, errs.CodeValidationFailed).WithDetail("a version must name its artifact")
	}
	// 1. initiator
	if strings.TrimSpace(v.InitiatorID) == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("WRK-04: a change must identify its initiator. Every change traces to somebody who wanted it — " +
				"a scheduled run traces to whoever configured the schedule.")
	}
	// 2. agent
	if !v.Agent.Valid() {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("WRK-04: a change must identify the agent that made it; %q is not one of planner, executor, verifier, human, scheduler, system", v.Agent)
	}
	// 3. tool
	if v.Agent != AgentHuman && (v.ToolCallID == nil || strings.TrimSpace(*v.ToolCallID) == "") {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("WRK-04: a change made by the %s must identify the tool call that made it. "+
				"Only a human works without a tool; anything else with no tool call is a change nobody can trace to an action.", v.Agent)
	}
	if v.Agent == AgentHuman && v.ToolCallID != nil && strings.TrimSpace(*v.ToolCallID) != "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("this version says a human made it and also names a tool call; one of the two is wrong, and guessing which would misattribute the change")
	}
	// 4. inputs — an explicit empty object is fine; absent is not, because
	//    "made from nothing" and "nobody recorded what it was made from" are
	//    different and only one of them is true.
	if len(v.Inputs) == 0 {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("WRK-04: a change must identify its inputs. Write {} if it genuinely had none — " +
				"that is a statement, whereas an absent field is a gap.")
	}
	if !json.Valid(v.Inputs) {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("the inputs recorded for this change are not valid JSON")
	}
	// 5. diff — see the field comment; "" is a legal value, so nothing to check
	//    beyond the column being NOT NULL, which the schema does.
	// 6. verification state
	if !v.Verification.Valid() {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("WRK-04: verification state %q is not one of unverified, passed, failed", v.Verification)
	}
	// 7. human disposition
	if !v.Disposition.Valid() {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("WRK-04: human disposition %q is not one of pending, accepted, rejected, superseded", v.Disposition)
	}
	if v.Disposition == Accepted || v.Disposition == Rejected {
		if v.DispositionedBy == nil || strings.TrimSpace(*v.DispositionedBy) == "" || v.DispositionedAt == nil {
			return errs.New(op, errs.CodeValidationFailed).
				WithDetail("a version %s by a person must name that person and when. "+
					"There is deliberately no way to record a human decision without one (PRD SAF-05).", v.Disposition)
		}
	}
	if v.Disposition == Pending && v.DispositionedBy != nil {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("this version is pending and also names who decided it; a pending version is one nobody has ruled on")
	}
	return nil
}

func reasonSuffix(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return ""
	}
	return ": " + reason
}
