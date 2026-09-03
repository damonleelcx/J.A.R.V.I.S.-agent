package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/tools"
)

// A truthful recovery plan after partial failure (PRD AGT-05).
//
// # What makes this hard, and it is not the SQL
//
// After a goal stops partway the useful question is not "which tasks failed" —
// the task rows already say that. It is "what did it leave behind, which of that
// can be undone, and what is genuinely unknown". The third part is the one
// everything else depends on, and it is the one a summary is most tempted to
// drop.
//
// # Derived, never generated
//
// Every line of this comes from rows: task statuses, and the tool-call ledger
// with the risk tier and reversibility each call was recorded at. No model
// writes any of it.
//
// That is not a performance choice. This product's central promise is that it
// never claims a tool ran, a check passed, or a person approved something that
// did not happen — and the moment somebody most needs that promise is the moment
// a run has failed halfway and they are deciding what to touch. A recovery plan
// written by a model is a plausible account of a failure, which is precisely the
// artefact that gets somebody hurt.
//
// # The uncertainty this refuses to round off
//
// A tool call recorded as `failed` DID RUN. The tool was invoked, it started
// doing whatever it does, and it returned an error somewhere in the middle. Its
// effect may have applied fully, partly, or not at all, and the ledger cannot
// tell which — the row records that the call failed, not that nothing happened.
//
// Filing those under "did not happen" is the single most dangerous thing this
// type could do, because it is the answer somebody acts on: they re-run the
// task, and the half-applied effect happens twice. They are listed separately
// and described as uncertain, in those words.

// Effect is one thing a run did, or may have done.
type Effect struct {
	Tool   string
	TaskID string
	At     time.Time
	// Tier and Undo are as recorded WHEN THE CALL RAN, not as the tool declares
	// itself now. Empty means the call predates the column: not captured, which
	// the renderer says rather than treating as harmless.
	Tier engine.RiskTier
	Undo tools.Reversibility
	// Detail is the error for an uncertain effect, empty for one that succeeded.
	Detail string
}

// Undoable reports whether this effect can be reversed without a person.
func (e Effect) Undoable() bool { return e.Undo == tools.ReversibleAutomatic }

// Describe renders the reversibility in words somebody can act on.
func (e Effect) Describe() string {
	switch e.Undo {
	case tools.ReversibleNone:
		return "changed nothing"
	case tools.ReversibleAutomatic:
		return "can be undone automatically"
	case tools.ReversibleManual:
		return "needs a person to undo"
	case tools.Irreversible:
		return "CANNOT be undone"
	default:
		// The honest answer for a call recorded before reversibility was
		// captured. Deliberately not "reversible": the whole point of this type
		// is that it does not fill gaps with the comfortable option.
		return "reversibility was not recorded for this call"
	}
}

// RecoveryPlan is what a stopped goal left behind.
type RecoveryPlan struct {
	GoalID string
	Title  string
	Status engine.GoalStatus
	// TakenAt is when this was derived. A recovery plan without one is a
	// photograph somebody will treat as live — the same reason collab.Handoff
	// carries one.
	TakenAt time.Time

	// Completed and Unfinished are task titles with their status.
	Completed  []string
	Unfinished []string

	// Standing are effects that definitely happened: successful mutating calls.
	Standing []Effect
	// Uncertain are calls that FAILED after running. Their effects may have
	// applied in part. Separate from Standing because the correct action differs
	// and the difference is the point of this type.
	Uncertain []Effect
}

// Recover derives the plan for a goal.
func Recover(ctx context.Context, pool *db.Pool, goalID string) (*RecoveryPlan, error) {
	const op = "agent.Recover"

	p := &RecoveryPlan{GoalID: goalID, TakenAt: time.Now().UTC()}
	err := pool.QueryRow(ctx,
		`select title, status from forge_goals where id = $1`, goalID).Scan(&p.Title, &p.Status)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeNotFound, err).WithDetail("no goal %s", goalID)
	}

	rows, err := pool.Query(ctx,
		`select title, status from forge_tasks where goal_id = $1 order by created_at`, goalID)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	for rows.Next() {
		var title, status string
		if err := rows.Scan(&title, &status); err != nil {
			rows.Close()
			return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
		}
		if engine.TaskStatus(status) == engine.StatusSucceeded {
			p.Completed = append(p.Completed, title)
		} else {
			p.Unfinished = append(p.Unfinished, fmt.Sprintf("%s (%s)", title, status))
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}

	// Refused calls are excluded on purpose: the tool never ran, so there is no
	// effect to reverse and nothing uncertain about it. Succeeded and failed are
	// the two that touched the world, and they are kept apart.
	calls, err := pool.Query(ctx, `
		select tc.tool_name, tc.task_id, tc.status, tc.created_at,
		       coalesce(tc.risk_tier, ''), coalesce(tc.reversibility, ''),
		       coalesce(tc.error_detail, '')
		  from forge_tool_calls tc
		  join forge_tasks t on t.id = tc.task_id
		 where t.goal_id = $1 and tc.status in ('succeeded', 'failed')
		 order by tc.created_at`, goalID)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	defer calls.Close()
	for calls.Next() {
		var e Effect
		var status, tier, undo string
		if err := calls.Scan(&e.Tool, &e.TaskID, &status, &e.At, &tier, &undo, &e.Detail); err != nil {
			return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
		}
		e.Tier, e.Undo = engine.RiskTier(tier), tools.Reversibility(undo)
		if engine.ToolCallStatus(status) == engine.ToolSucceeded {
			// A call that changed nothing is not an effect to recover from.
			if e.Undo == tools.ReversibleNone {
				continue
			}
			e.Detail = ""
			p.Standing = append(p.Standing, e)
			continue
		}
		p.Uncertain = append(p.Uncertain, e)
	}
	return p, calls.Err()
}

// Render writes the plan as something a person reads at the moment a run has
// stopped and they are deciding what to touch.
//
// Ordered by what has to be known first: what is uncertain, then what stands and
// cannot be undone, then the rest. A recovery plan that opens with a list of
// completed tasks buries the two facts that change what somebody does next.
func (p *RecoveryPlan) Render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Recovery plan for %s (%s)\n", p.Title, p.GoalID)
	fmt.Fprintf(&b, "Goal status: %s · derived %s\n", p.Status, p.TakenAt.Format(time.RFC3339))
	b.WriteString("\nEverything below is read from the record. None of it is written by a model.\n")

	if len(p.Uncertain) > 0 {
		b.WriteString("\n## Read this first: calls that ran and then failed\n\n")
		b.WriteString("These tools were invoked and returned an error. The record says the call " +
			"failed; it does NOT say the effect did not happen. Each may have applied fully, " +
			"partly, or not at all — check before re-running, because a half-applied effect " +
			"repeated is how one failure becomes two.\n\n")
		for _, e := range p.Uncertain {
			fmt.Fprintf(&b, "- %s on %s at %s — %s\n", e.Tool, e.TaskID,
				e.At.Format(time.RFC3339), e.Describe())
			if e.Detail != "" {
				fmt.Fprintf(&b, "    failed with: %s\n", truncate(e.Detail, 200))
			}
		}
	}

	irreversible, manual, automatic := p.partition()
	if len(irreversible) > 0 {
		b.WriteString("\n## Effects that stand and cannot be undone\n\n")
		for _, e := range irreversible {
			fmt.Fprintf(&b, "- %s on %s at %s\n", e.Tool, e.TaskID, e.At.Format(time.RFC3339))
		}
		b.WriteString("\nRecovery has to work forward from these. There is no rollback for them.\n")
	}
	if len(manual) > 0 {
		b.WriteString("\n## Effects a person must look at\n\n")
		for _, e := range manual {
			// Describe() rather than a fixed sentence, because this group holds two
			// different facts: an effect somebody has to undo by hand, and one whose
			// reversibility was never recorded. Printing one heading over both would
			// state the first about the second.
			fmt.Fprintf(&b, "- %s on %s at %s — %s\n", e.Tool, e.TaskID,
				e.At.Format(time.RFC3339), e.Describe())
		}
	}
	if len(automatic) > 0 {
		b.WriteString("\n## Effects that can be undone automatically\n\n")
		for _, e := range automatic {
			fmt.Fprintf(&b, "- %s on %s at %s — %s\n", e.Tool, e.TaskID,
				e.At.Format(time.RFC3339), e.Describe())
		}
		b.WriteString("\nFORGE does not perform this rollback. Undoing work is destructive in its " +
			"own right, and doing it automatically from a failure it has just misunderstood is " +
			"a second failure. The list is here so a person can.\n")
	}

	fmt.Fprintf(&b, "\n## Tasks\n\nCompleted: %d\n", len(p.Completed))
	for _, t := range p.Completed {
		fmt.Fprintf(&b, "- %s\n", t)
	}
	fmt.Fprintf(&b, "\nUnfinished: %d\n", len(p.Unfinished))
	for _, t := range p.Unfinished {
		fmt.Fprintf(&b, "- %s\n", t)
	}
	if len(p.Uncertain) == 0 && len(p.Standing) == 0 {
		b.WriteString("\nNo tool call in this goal changed anything, so there is nothing to " +
			"undo and nothing uncertain.\n")
	}
	return b.String()
}

// partition splits standing effects by what it would take to reverse them.
//
// Anything not recognised is grouped with manual rather than automatic: an
// unrecorded reversibility is a thing a person has to look at, and putting it in
// the automatic pile would tell somebody a machine can clean it up.
func (p *RecoveryPlan) partition() (irreversible, manual, automatic []Effect) {
	for _, e := range p.Standing {
		switch e.Undo {
		case tools.Irreversible:
			irreversible = append(irreversible, e)
		case tools.ReversibleAutomatic:
			automatic = append(automatic, e)
		default:
			manual = append(manual, e)
		}
	}
	return irreversible, manual, automatic
}
