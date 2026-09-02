// Package incident is FORGE's incident response record (PRD SAF-07).
//
// # The seven verbs, and the one ordering that matters
//
// SAF-07 names stop, revoke, quarantine, roll back, preserve evidence, notify
// and review. Six of them can happen in any order. One cannot: **evidence is
// preserved before anything destructive**. An incident response that stops the
// goal, revokes the credential and rolls the artifact back, and then gathers
// evidence, has gathered the evidence of its own response.
//
// So the record refuses a destructive action on an incident that has preserved
// nothing, and says why. It is a refusal rather than a warning because the
// moment it fires is the moment nobody is reading warnings.
//
// # Why the record is append-only
//
// An incident log that can be edited is a log that will be edited, and the edit
// will happen during the part of the incident nobody wants to explain later.
// Actions are appended with a sequence number; the incident itself moves through
// three states and carries a review that must be written before it closes.
package incident

import (
	"strings"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// Severity is how bad it is.
type Severity string

const (
	SeverityLow      Severity = "low"
	SeverityMedium   Severity = "medium"
	SeverityHigh     Severity = "high"
	SeverityCritical Severity = "critical"
)

// Valid reports whether s is recognised.
func (s Severity) Valid() bool {
	return s == SeverityLow || s == SeverityMedium || s == SeverityHigh || s == SeverityCritical
}

// Status is where an incident stands.
type Status string

const (
	// StatusOpen — it is happening.
	StatusOpen Status = "open"
	// StatusContained — the bleeding stopped. A real state rather than a note:
	// "we made it stop" and "we understand what happened" are different days,
	// and a two-state model forces one of them to be a lie.
	StatusContained Status = "contained"
	// StatusClosed — reviewed and finished.
	StatusClosed Status = "closed"
)

// Valid reports whether s is recognised.
func (s Status) Valid() bool {
	return s == StatusOpen || s == StatusContained || s == StatusClosed
}

// ActionKind is one of SAF-07's seven verbs.
type ActionKind string

const (
	// ActionStop halts work: a goal, a task, a worker.
	ActionStop ActionKind = "stop"
	// ActionRevoke withdraws access: a secret, a session, a grant.
	ActionRevoke ActionKind = "revoke"
	// ActionQuarantine marks something unusable without destroying it — the
	// difference between containment and deletion, and the reason a quarantined
	// thing can still be examined afterwards.
	ActionQuarantine ActionKind = "quarantine"
	// ActionRollBack restores an earlier state. In this system that means
	// appending a version that restores an earlier one, never deleting the one
	// in between.
	ActionRollBack ActionKind = "roll_back"
	// ActionPreserveEvidence captures what the system says right now, before
	// anything changes it.
	ActionPreserveEvidence ActionKind = "preserve_evidence"
	// ActionNotify tells somebody.
	ActionNotify ActionKind = "notify"
	// ActionReview is the account of what happened and what would prevent it.
	ActionReview ActionKind = "review"
)

// actionDefs is the closed set, with what each one does to the world.
//
// A table rather than a switch, for the same reason every other vocabulary in
// this codebase is one: the ordering rule reads off a column instead of being
// re-derived at each call site.
var actionDefs = []struct {
	Kind ActionKind
	// Destructive marks an action that changes or removes state a later
	// investigation would have wanted. These are the ones that must not run
	// before evidence is preserved.
	Destructive bool
	// NeedsTarget marks an action that is meaningless without naming what it
	// acted on. "We stopped something" is not an incident record.
	NeedsTarget bool
	Gloss       string
}{
	{ActionStop, true, true, "halt work: a goal, a task, a worker"},
	{ActionRevoke, true, true, "withdraw access: a secret, a session, a grant"},
	{ActionQuarantine, true, true, "mark unusable without destroying, so it can still be examined"},
	{ActionRollBack, true, true, "restore an earlier state by appending, never by deleting"},
	{ActionPreserveEvidence, false, false, "capture what the system says right now, before anything changes it"},
	{ActionNotify, false, true, "tell somebody"},
	{ActionReview, false, false, "what happened, what was done, and what would prevent it"},
}

// Actions returns the seven.
func Actions() []ActionKind {
	out := make([]ActionKind, 0, len(actionDefs))
	for _, d := range actionDefs {
		out = append(out, d.Kind)
	}
	return out
}

// ActionOf returns the definition for a verb.
func ActionOf(k ActionKind) (ActionKind, bool, bool, string, error) {
	for _, d := range actionDefs {
		if d.Kind == k {
			return d.Kind, d.Destructive, d.NeedsTarget, d.Gloss, nil
		}
	}
	return "", false, false, "", errs.New("incident.ActionOf", errs.CodeValidationFailed).
		WithDetail("%q is not one of SAF-07's seven actions; they are %s", k, strings.Join(actionNames(), ", "))
}

// Valid reports whether k is one of the seven.
func (k ActionKind) Valid() bool { _, _, _, _, err := ActionOf(k); return err == nil }

// Destructive reports whether this action changes state an investigation would
// have wanted.
func (k ActionKind) Destructive() bool { _, d, _, _, _ := ActionOf(k); return d }

// Gloss explains the verb.
func (k ActionKind) Gloss() string { _, _, _, g, _ := ActionOf(k); return g }

func actionNames() []string {
	out := make([]string, 0, len(actionDefs))
	for _, d := range actionDefs {
		out = append(out, string(d.Kind))
	}
	return out
}

// Outcome is what an action actually did.
//
// Present so that a dry run and a real one are distinguishable and a partial
// failure is not recorded as a success. An incident log that says "revoked" when
// two of three sessions were revoked is worse than one that says nothing.
type Outcome string

const (
	OutcomeDone    Outcome = "done"
	OutcomePartial Outcome = "partial"
	OutcomeFailed  Outcome = "failed"
	OutcomeDryRun  Outcome = "dry_run"
)

// Valid reports whether o is recognised.
func (o Outcome) Valid() bool {
	return o == OutcomeDone || o == OutcomePartial || o == OutcomeFailed || o == OutcomeDryRun
}

// Changed reports whether this outcome actually altered anything.
//
// A dry run and a failure did not, which is why neither of them satisfies the
// evidence-first rule and neither counts as containment.
func (o Outcome) Changed() bool { return o == OutcomeDone || o == OutcomePartial }

// Action is one thing done during an incident.
type Action struct {
	ID         string
	IncidentID string
	Seq        int
	Kind       ActionKind
	Target     string
	Detail     string
	Outcome    Outcome
	TakenBy    string
	TakenAt    time.Time
}

// Validate checks an action before it is appended.
func (a *Action) Validate() error {
	const op = "incident.Action.Validate"

	_, _, needsTarget, gloss, err := ActionOf(a.Kind)
	if err != nil {
		return err
	}
	if a.Outcome == "" {
		a.Outcome = OutcomeDone
	}
	if !a.Outcome.Valid() {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("outcome %q is not one of done, partial, failed, dry_run", a.Outcome)
	}
	if strings.TrimSpace(a.TakenBy) == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("an incident action must name the person who took it. " +
				"A response that cannot say who stopped the goal is not a response, it is an outage with notes.")
	}
	if needsTarget && strings.TrimSpace(a.Target) == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("a %s action must name what it acted on — %s. "+
				"\"We stopped something\" is not an incident record.", a.Kind, gloss)
	}
	if a.TakenAt.IsZero() {
		return errs.New(op, errs.CodeInvariantViolated).
			WithDetail("an incident action has no timestamp; the application clock owns every timestamp in this system")
	}
	return nil
}

// Incident is one response.
type Incident struct {
	ID        string
	ProjectID string
	GoalID    *string
	Title     string
	Statement string
	Severity  Severity
	Status    Status

	OpenedBy string
	OpenedAt time.Time

	Review   string
	ClosedBy *string
	ClosedAt *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time

	// Actions, oldest first. Loaded by the repository.
	Actions []Action
}

// Validate checks an incident before it is opened.
func (i *Incident) Validate() error {
	const op = "incident.Incident.Validate"

	if strings.TrimSpace(i.Title) == "" {
		return errs.New(op, errs.CodeValidationFailed).WithDetail("an incident needs a title")
	}
	if strings.TrimSpace(i.Statement) == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("incident %q records nothing about what happened. Write it in your own words: "+
				"every summary afterwards is derived, and this is the thing they can be checked against.", i.Title)
	}
	if strings.TrimSpace(i.ProjectID) == "" || strings.TrimSpace(i.OpenedBy) == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("an incident must name its project and who opened it")
	}
	if i.Severity == "" {
		i.Severity = SeverityMedium
	}
	if !i.Severity.Valid() {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("severity %q is not one of low, medium, high, critical", i.Severity)
	}
	if i.Status == "" {
		i.Status = StatusOpen
	}
	if !i.Status.Valid() {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("status %q is not one of open, contained, closed", i.Status)
	}
	return nil
}

// EvidencePreserved reports whether anything has been captured yet.
//
// A dry run does not count: nothing was captured, and the point of the rule is
// that something WAS.
func (i *Incident) EvidencePreserved() bool {
	for _, a := range i.Actions {
		if a.Kind == ActionPreserveEvidence && a.Outcome.Changed() {
			return true
		}
	}
	return false
}

// AllowsAction reports whether an action may be appended now, and says why not.
//
// The one ordering rule in SAF-07: evidence before destruction. A dry run is
// always allowed — it changes nothing, and rehearsing the response before
// gathering evidence is exactly what somebody should be able to do.
func (i *Incident) AllowsAction(kind ActionKind, outcome Outcome) error {
	const op = "incident.Incident.AllowsAction"

	if i.Status == StatusClosed {
		return errs.New(op, errs.CodeConflict).
			WithDetail("incident %s is closed; reopen it or open a new one rather than appending to a finished record", i.ID)
	}
	if !kind.Destructive() || !outcome.Changed() {
		return nil
	}
	if i.EvidencePreserved() {
		return nil
	}
	return errs.New(op, errs.CodeEvidenceNotPreserved).
		WithDetail("this incident has preserved no evidence, and %s would change state an investigation "+
			"needs. Record a preserve_evidence action first — or run this one as a dry_run, which changes nothing.",
			kind)
}

// Closeable reports whether the incident may be closed.
func (i *Incident) Closeable(review string) error {
	const op = "incident.Incident.Closeable"

	if i.Status == StatusClosed {
		return errs.New(op, errs.CodeConflict).WithDetail("incident %s is already closed", i.ID)
	}
	if strings.TrimSpace(review) == "" {
		return errs.New(op, errs.CodeIncidentOpen).
			WithDetail("closing incident %q needs a review: what happened, what was done, and what would "+
				"prevent it. SAF-07 names review as one of the seven steps, and it is the only part "+
				"anybody reads a year later.", i.Title)
	}
	return nil
}
