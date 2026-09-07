// Package conversation keeps what was said at the workbench.
//
// # Why this exists
//
// PRD RSN-07 asks FORGE to resume from a structured checkpoint rather than a
// conversation summary. The agentic side has had exactly that since wave 1 —
// checkpoints, resume state, a recovery drill that kills a worker mid-task. The
// conversational side, which is the surface a person actually uses, had nothing:
// `history` was posted by the browser on every turn and no table held it, so a
// reload lost the thread that produced the work while the work itself survived.
//
// # Why a conversation has no row of its own
//
// It is its turns. It has no title, no members, no lifecycle and no state — a
// row holding an id and an owner would be a second place for those two facts to
// disagree with the turns themselves. So a conversation exists exactly when it
// has a turn, and deleting its turns deletes it. This is the same reasoning that
// kept a forge_variants table out of the geometry package.
package conversation

import (
	"strings"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// Role is who spoke.
//
// Two values, and deliberately not the LLM's role names. This records what was
// said in a PRODUCT; what was sent to a provider is a separate concern with its
// own vocabulary, and letting one leak into the other is how a record ends up
// describing an API call instead of a conversation.
type Role string

const (
	RoleHuman Role = "human"
	RoleForge Role = "forge"
)

var roles = []Role{RoleHuman, RoleForge}

// Roles returns the vocabulary, for the fence that holds it against the schema's
// check constraint.
func Roles() []Role { return append([]Role(nil), roles...) }

// Valid reports whether r is one of the two.
func (r Role) Valid() bool {
	for _, x := range roles {
		if x == r {
			return true
		}
	}
	return false
}

// Turn is one thing that was said.
type Turn struct {
	ID             string
	ConversationID string
	OwnerID        string
	// ProjectID is the project this turn belonged to at the time, and empty
	// before there was one. A project is created by the first thing worth
	// keeping rather than by the first sentence, so early turns genuinely belong
	// to no project.
	ProjectID string
	Seq       int
	Role      Role
	// Text is what was said. For FORGE this is the spoken half, kept short on
	// purpose (PRD §5.3).
	Text string
	// Detail is the long-form half the screen carries. Always empty for a human.
	Detail string
	// Images is how many pictures were attached. The bytes are not kept — an
	// image is an input to one turn (PRD VIS-01) and there is nowhere to put one.
	Images int
	SaidAt time.Time
	// Timing is what this turn cost, when it was measured. Nil means it was
	// NOT measured, which is a different fact from "it was instant" — see
	// Timing's own comment, and migration 0022.
	Timing *Timing
}

// Timing is the server's measurement of one FORGE turn (PRD NFR-05, AUD-02).
//
// # Why every field is a pointer
//
// Because "not measured" has to survive the trip. A turn that produced no speech
// has no time-to-first-token; a turn recorded before this existed has none of
// these at all. Zero is the wrong stand-in for any of them: the panel that reads
// these renders a missing measurement as an em dash and a reason precisely
// because zero is a number a reader takes as "instant", and a metric nobody
// collected looks best of all when drawn as one.
//
// # Why the two durations are both here
//
// FirstTokenMS and TotalMS are the model's part. RoundTripMS is the handler's,
// measured outside the model call, and the difference between it and TotalMS is
// this system's own overhead. Averaged into one "latency" they would answer
// neither question — the same reason the panel keeps the server's clock and the
// browser's clock in separate columns.
type Timing struct {
	// Model is which model answered. NFR-05 names model selection.
	Model string
	// FirstTokenMS is request-in to first speech token out, server clock.
	FirstTokenMS *int
	// TotalMS is the whole turn including the structured tail, server clock.
	TotalMS *int
	// RoundTripMS is what the handler took end to end.
	RoundTripMS *int
	// Tokens is what the provider reported for the turn.
	Tokens *int64
}

// Measured reports whether this timing carries anything at all.
//
// A Timing with every field nil is not a measurement of a fast turn; it is the
// absence of one, and it is not written.
func (t *Timing) Measured() bool {
	if t == nil {
		return false
	}
	return t.Model != "" || t.FirstTokenMS != nil || t.TotalMS != nil ||
		t.RoundTripMS != nil || t.Tokens != nil
}

// Validate refuses a turn the record could not honestly hold.
//
// Mirrors the schema's check constraints rather than trusting them, so a caller
// gets a sentence instead of a constraint name — and so the rules are readable
// in one place beside the type they describe.
func (t *Turn) Validate() error {
	const op = "conversation.Turn.Validate"

	if strings.TrimSpace(t.ConversationID) == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("a turn must name the conversation it belongs to")
	}
	if strings.TrimSpace(t.OwnerID) == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("a turn must name whose conversation it is; a record nobody owns is a " +
				"record nobody can delete, and PRD AUD-07 requires that deletion always be reachable")
	}
	if !t.Role.Valid() {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("%q is not a role; a turn was said by a human or by FORGE", t.Role)
	}
	if strings.TrimSpace(t.Text) == "" && strings.TrimSpace(t.Detail) == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("this turn says nothing at all. Either half may be empty; both may not, " +
				"because a record of somebody saying nothing is not a record of a turn")
	}
	if t.Role == RoleHuman && strings.TrimSpace(t.Detail) != "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("a human turn carries a detail, which is FORGE's half of the screen. " +
				"Somebody's message has been split by something that does not exist.")
	}
	if t.Images < 0 {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("a turn cannot have attached %d images", t.Images)
	}
	// A human turn is not timed: there is no model call behind it, and a figure
	// there would be a measurement of nothing attributed to a person. Mirrors
	// the schema's own check for the reason the rest of this function exists —
	// so the caller gets a sentence rather than a constraint name.
	if t.Role == RoleHuman && t.Timing.Measured() {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("a human turn carries a model timing. Nothing was asked of a model " +
				"to produce it, so there is nothing here that was measured")
	}
	for _, d := range []struct {
		name string
		val  *int
	}{{"time to first token", t.Timing.first()}, {"total time", t.Timing.total()},
		{"round trip", t.Timing.roundTrip()}} {
		if d.val != nil && *d.val < 0 {
			return errs.New(op, errs.CodeValidationFailed).
				WithDetail("this turn's %s is %d ms. A negative duration is a clock that went "+
					"backwards, and stored it would drag every median somewhere no measurement "+
					"can be", d.name, *d.val)
		}
	}
	if t.Timing != nil && t.Timing.Tokens != nil && *t.Timing.Tokens < 0 {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("this turn reports %d tokens", *t.Timing.Tokens)
	}
	return nil
}

// The three accessors below exist so Validate can loop rather than repeat
// itself, and so a nil Timing reads as "nothing measured" in one place.
func (t *Timing) first() *int {
	if t == nil {
		return nil
	}
	return t.FirstTokenMS
}

func (t *Timing) total() *int {
	if t == nil {
		return nil
	}
	return t.TotalMS
}

func (t *Timing) roundTrip() *int {
	if t == nil {
		return nil
	}
	return t.RoundTripMS
}

func (t *Timing) tokens() *int64 {
	if t == nil {
		return nil
	}
	return t.Tokens
}
