package agent

import (
	"context"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
)

// What a person has already settled about a goal, and what the planner is told
// about it (PRD RSN-02, RSN-03).
//
// # Why these two live together
//
// A goal accumulates decisions that are not the planner's to make again: the
// answer to a question it asked, and the option somebody chose when it offered a
// choice. Both are written by a human, both are stored on the goal, and both
// mean the same thing to a plan — this part is closed, build on it.
//
// They were built a requirement apart and each nearly grew its own plumbing: a
// store, a wiring point in Intake.Plan, a field on Planner, a prompt section.
// Two reads of the same row, wired twice, is the shape that ends with one of
// them wired and the other not — which is precisely the state RSN-02 was found
// in. One store, one wiring point, one brief.
//
// # Why the write existing was not enough
//
// RSN-02 shipped with the answer stored, the gate released, and `goal answer`
// printing "replan so the plan is built on the answer" — and nothing on the
// planning path ever read the column. The advice was false: the replan asked
// the model the same ambiguous question again, and whether the second roll
// happened to ask again or to guess was luck.
//
// A write with no reader is the same defect as a check nobody calls. It is
// harder to see, because the feature demonstrably works right up to the last
// step: the question is asked, stored, answered, and the hold lifts. Everything
// is observable except the part that was the point.

// SettledStore reads what has already been decided about a goal.
//
// Nil is a legal value, like CharacterStore: a deployment that never asks a
// question and never offers a choice has nothing to read, and planning proceeds
// unchanged.
type SettledStore struct{ pool *db.Pool }

// NewSettledStore returns a store reading from forge_goals.
func NewSettledStore(pool *db.Pool) *SettledStore {
	if pool == nil {
		return nil
	}
	return &SettledStore{pool: pool}
}

// Settled is everything about a goal that a plan must not re-open.
type Settled struct {
	// Clarification is the question FORGE asked, and the answer if one was given.
	Clarification *clarificationHold
	// Choice is the option set and the option somebody picked from it.
	Choice *optionHold
}

// For reads both, through the readers that already own each one.
//
// Two queries rather than one hand-written join. The cost is a round trip
// against a call that is about to spend minutes in a model; the alternative is a
// third SELECT over the same columns, drifting from the two that the gates read
// — and a planner that disagrees with the gate about what was settled is worse
// than a planner that is slightly slower.
func (s *SettledStore) For(ctx context.Context, goalID string) (*Settled, error) {
	if s == nil {
		return nil, nil
	}
	clarification, err := clarificationFor(ctx, s.pool, goalID)
	if err != nil {
		return nil, err
	}
	choice, err := optionsFor(ctx, s.pool, goalID)
	if err != nil {
		return nil, err
	}
	return &Settled{Clarification: clarification, Choice: choice}, nil
}

// Brief is what the planner is told about all of it.
//
// unreadable reports that the goal names a chosen option its own set does not
// contain, so the caller can say so rather than let it pass as "nobody chose".
func (s *Settled) Brief() (brief string, unreadable bool) {
	if s == nil {
		return "", false
	}
	choice, unreadable := settledChoiceBrief(s.Choice)
	return settledAnswerBrief(s.Clarification) + choice, unreadable
}

// settledAnswerBrief is what a plan is told about a question that was answered.
//
// # Why only when it was answered
//
// An outstanding question is not settled — it is the opposite — and the planner
// is the thing that asked it. Telling it "you asked this and nobody replied"
// would either be ignored or, worse, read as permission to proceed on a guess.
// Left out, the planner sees the same ambiguity it saw the first time and asks
// again, which is the correct behaviour: the goal is held at r2 and above, and
// below it the question is already recorded as a labelled assumption.
//
// # Why it says the question as well as the answer
//
// An answer alone is a sentence with no subject. "Steel" means nothing to a
// model that cannot see it was the reply to "aluminium or steel?", and a plan
// built on a misread answer is worse than one built on none, because it carries
// no sign that it misread anything.
func settledAnswerBrief(hold *clarificationHold) string {
	if hold == nil || !hold.Answered || strings.TrimSpace(hold.Answer) == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## A question you asked has been answered\n\n")
	b.WriteString("You asked: " + hold.Question + "\n")
	b.WriteString("The answer: " + hold.Answer + "\n")
	b.WriteString("\nThis is settled. Plan on it, and do not ask it again — asking a second time " +
		"is how a person learns that answering changes nothing.\n")
	return b.String()
}
