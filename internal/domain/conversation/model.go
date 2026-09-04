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
	return nil
}
