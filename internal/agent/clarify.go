package agent

import (
	"context"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/claim"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// Clarification before consequential work (PRD RSN-02).
//
// # The two halves of the requirement, and why the second one matters
//
// "Socratic clarification before consequential work; labeled assumptions
// permitted for low-risk exploration."
//
// The first half is a gate. The second half is the reason the gate is bearable:
// a system that stopped dead every time it was unsure would teach people to
// phrase goals so it never asks, which produces the same unexamined work with
// the questions suppressed. So the answer depends on what the work is about to
// do — and the tier already says.
//
// Below r2 the goal proceeds and the unanswered question is written into the
// project graph as an ASSUMPTION: a node of kind assumption, epistemically
// `assumed`, saying what was not resolved. That is what "labeled" means here.
// The exploration happens, and what it rests on is visible rather than implied.
//
// At r2 and above the goal is held. r2 is "consequential digital change" and the
// requirement's own words are "before consequential work", so the tier boundary
// is not a judgement call — it is the definition, already written down.

// clarificationHold is what the gate found.
type clarificationHold struct {
	Question string
	Answered bool
}

// clarificationFor reads the outstanding question for a goal.
func clarificationFor(ctx context.Context, q db.Querier, goalID string) (*clarificationHold, error) {
	var question, answer *string
	err := q.QueryRow(ctx,
		`select clarification_question, clarification_answer from forge_goals where id = $1`,
		goalID).Scan(&question, &answer)
	if err != nil {
		return nil, errs.Wrap("agent.clarificationFor", errs.CodeDatabaseUnavail, err)
	}
	if question == nil {
		return nil, nil
	}
	return &clarificationHold{Question: *question, Answered: answer != nil}, nil
}

// recordQuestion stores what the planner refused to guess past.
//
// Writing NULL when there is no question is as important as writing one when
// there is: a goal that asked, was answered, and was replanned into a plan with
// no question must not keep a stale one that holds it forever.
func recordQuestion(ctx context.Context, q db.Querier, goalID, question string) error {
	_, err := q.Exec(ctx, `
		update forge_goals
		   set clarification_question = nullif($2, ''),
		       clarification_answer = case when $2 = '' then null else clarification_answer end,
		       updated_at = now()
		 where id = $1`, goalID, question)
	if err != nil {
		return errs.Wrap("agent.recordQuestion", errs.CodeDatabaseUnavail, err)
	}
	return nil
}

// AnswerClarification records the answer to the question a goal is held on.
//
// Refusing to answer a question nobody asked is not pedantry: an answer stored
// against no question would satisfy the gate for the NEXT question, which is the
// one failure mode a clarification gate cannot have.
func AnswerClarification(ctx context.Context, pool *db.Pool, goalID, answer string) error {
	const op = "agent.AnswerClarification"

	hold, err := clarificationFor(ctx, pool, goalID)
	if err != nil {
		return err
	}
	if hold == nil {
		return errs.New(op, errs.CodeConflict).
			WithDetail("goal %s is not waiting on a question, so there is nothing to answer", goalID)
	}
	if _, err := pool.Exec(ctx,
		`update forge_goals set clarification_answer = $2, updated_at = now() where id = $1`,
		goalID, answer); err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	return nil
}

// gateOnClarification decides whether this goal may start.
//
// Returns the assumption to record when low-risk work is allowed to proceed
// without an answer, and an error when consequential work is held.
func gateOnClarification(hold *clarificationHold, goal *engine.Goal) (assumption string, err error) {
	if hold == nil || hold.Answered {
		return "", nil
	}
	if goal.RiskTier.AtLeast(engine.RiskR2) {
		return "", errs.New("agent.PlanApplier.Activate", errs.CodeValidationFailed).
			WithDetail("this goal is %s — consequential work — and FORGE asked a question that has "+
				"not been answered:\n\n  %s\n\nAnswer it with `forgectl goal answer %s \"...\"`, "+
				"then start it. Below r2 the same question would be recorded as a labelled "+
				"assumption and the work would proceed; at r2 and above it is held, because "+
				"RSN-02 puts clarification before consequential work.",
				goal.RiskTier, hold.Question, goal.ID)
	}
	return hold.Question, nil
}

// labelAssumption writes the unanswered question into the project graph.
//
// PRD RSN-02's second half. The work proceeds and what it rests on is a node
// somebody can find, argue with, and later promote into a requirement — rather
// than a decision that happened in a prompt and left no trace.
//
// A failure to write it does NOT block the goal, and that is deliberate in the
// other direction from the gate above: the gate protects consequential work, and
// this is low-risk exploration, where refusing to start because a note could not
// be filed would be the system stopping over its own bookkeeping.
func labelAssumption(ctx context.Context, q db.Querier, ws *workspace.Service, goal *engine.Goal,
	question, byUserID string) error {
	if ws == nil || question == "" {
		return nil
	}
	// Whoever started it, or failing that whoever created the goal.
	//
	// byUserID is empty on the forgectl path — `Activate(..., nil)` — and a node
	// with no author is refused by the workspace service, so without this the
	// assumption is silently never filed for every CLI start. Found by the test
	// that asserts the node exists rather than that the call was made.
	author := byUserID
	if author == "" {
		author = goal.CreatedBy
	}
	if author == "" {
		if err := q.QueryRow(ctx,
			`select created_by from forge_goals where id = $1`, goal.ID).Scan(&author); err != nil {
			return errs.Wrap("agent.labelAssumption", errs.CodeDatabaseUnavail, err)
		}
	}
	_, err := ws.Add(ctx, workspace.NewNode{
		ProjectID: goal.ProjectID,
		Kind:      workspace.KindAssumption,
		Title:     "Unanswered before starting: " + truncate(question, 120),
		Body: "FORGE asked this before planning and it was not answered. The goal is " +
			string(goal.RiskTier) + ", which RSN-02 treats as low-risk exploration, so the work " +
			"proceeded on the assumption that the answer does not change what should be built.\n\n" +
			"Question: " + question,
		How:       claim.Assumed,
		Source:    "goal " + goal.ID,
		CreatedBy: author,
	})
	return err
}
