// Package agent is FORGE's runtime: context assembly, the planner/executor/
// verifier split, and the worker loop that drives them.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/tools"
)

// Assembler rebuilds an execution context from durable state.
//
// # The rule this package exists to enforce
//
// The model's context window is not memory. It is a cache destroyed at the end
// of every request: not durable, not queryable, not auditable. So nothing is
// carried between cycles in a process — every cycle reconstructs what it needs
// from the database.
//
// The practical test is that killing a worker mid-task and restarting it must
// produce the same outcome as never having killed it. Anything held only in
// memory breaks that, which is why the assembler reads and the worker never
// caches.
//
// It also means crash recovery, pause/resume, and multi-day execution are the
// SAME code path rather than three special cases — the worker cannot tell
// whether it is resuming or starting, because there is no difference.
type Assembler struct {
	repo  *engine.Repository
	queue *engine.Queue
}

// NewAssembler returns an assembler.
func NewAssembler(repo *engine.Repository, queue *engine.Queue) *Assembler {
	return &Assembler{repo: repo, queue: queue}
}

// TaskContext is everything one execution cycle needs, and deliberately no more.
type TaskContext struct {
	Goal *engine.Goal
	Task *engine.Task
	Plan *engine.Plan

	// Dependencies are the tasks this one waited on, with their results — the
	// outputs this task's inputs refer to.
	Dependencies []*engine.Task

	// Checkpoint is the most recent resumable state, or nil on a first attempt.
	Checkpoint *engine.Checkpoint

	// RecentEvents is a bounded slice of this task's timeline. Bounded because
	// the full history of a multi-week goal does not fit, and because the older
	// it gets the less it bears on the next step.
	RecentEvents []*engine.Event

	// PriorAttempts summarises what already failed, so a retry does not repeat
	// the approach that failed. Without it, a retry is a re-run.
	PriorAttempts []AttemptSummary

	// Grant is what this goal is permitted to do right now.
	Grant tools.Grant

	// Budget is the remaining headroom, rendered so the model can pace itself
	// rather than being cut off mid-thought.
	BudgetNote string
}

// AttemptSummary is what one failed attempt is worth remembering.
type AttemptSummary struct {
	Attempt   int
	ErrorCode string
	Detail    string
	At        time.Time
}

// Assemble reconstructs the context for a task.
func (a *Assembler) Assemble(ctx context.Context, ex db.Querier, task *engine.Task, goal *engine.Goal, grant tools.Grant, budgetNote string) (*TaskContext, error) {
	const op = "agent.Assembler.Assemble"

	tc := &TaskContext{Goal: goal, Task: task, Grant: grant, BudgetNote: budgetNote}

	depIDs, err := a.repo.ListDependencies(ctx, ex, task.ID)
	if err != nil {
		return nil, err
	}
	for _, id := range depIDs {
		dep, err := a.repo.GetTask(ctx, ex, id)
		if err != nil {
			// A dangling dependency is corruption, not a missing optional. The
			// task's inputs refer to an output that no longer exists, and
			// proceeding would mean guessing at it.
			return nil, errs.Wrap(op, errs.CodeStateCorrupt, err).
				WithDetail("task %s depends on %s, which cannot be read", task.ID, id)
		}
		tc.Dependencies = append(tc.Dependencies, dep)
	}

	cp, err := a.repo.LatestCheckpoint(ctx, ex, task.ID)
	if err != nil {
		// A checkpoint that exists but cannot be decoded is worth reporting
		// loudly: resuming from nothing when a checkpoint was written means
		// silently redoing work, and possibly redoing side effects.
		return nil, err
	}
	tc.Checkpoint = cp

	events, err := a.repo.Timeline(ctx, ex, goal.ID, 40, 0)
	if err != nil {
		return nil, err
	}
	for _, e := range events {
		if e.TaskID != nil && *e.TaskID == task.ID {
			tc.RecentEvents = append(tc.RecentEvents, e)
		}
	}
	tc.PriorAttempts = extractAttempts(tc.RecentEvents)

	return tc, nil
}

// extractAttempts pulls failure summaries out of the timeline.
func extractAttempts(events []*engine.Event) []AttemptSummary {
	var out []AttemptSummary
	for _, e := range events {
		if e.Kind != engine.EventTaskFailed && e.Kind != engine.EventTaskRetrying {
			continue
		}
		var payload struct {
			Attempt   int    `json:"attempt"`
			ErrorCode string `json:"error_code"`
			Detail    string `json:"detail"`
		}
		_ = json.Unmarshal(e.Payload, &payload)
		out = append(out, AttemptSummary{
			Attempt:   payload.Attempt,
			ErrorCode: payload.ErrorCode,
			Detail:    payload.Detail,
			At:        e.CreatedAt,
		})
	}
	return out
}

// Prompt renders the context as the user-role message for an execution cycle.
//
// # Why this is written by hand rather than serialised
//
// A JSON dump of the aggregate would be complete and unreadable — to the model
// and to the person debugging why the model did the wrong thing. This is
// composed as a briefing: the goal, then the specific task, then what is already
// known, then what is left. Sections are omitted entirely when empty rather than
// rendered as "none", because an empty section still costs tokens and still
// reads as something to consider.
func (tc *TaskContext) Prompt() string {
	var b strings.Builder

	b.WriteString("## Goal\n\n")
	fmt.Fprintf(&b, "%s\n\n%s\n", tc.Goal.Title, tc.Goal.Statement)

	if len(tc.Goal.CompletionCriteria) > 0 {
		b.WriteString("\nThis goal is complete when:\n")
		for _, c := range tc.Goal.CompletionCriteria {
			mark := " "
			if c.Satisfied {
				mark = "x"
			}
			fmt.Fprintf(&b, "- [%s] %s", mark, c.Statement)
			if !c.Verifiable() {
				b.WriteString("  (no automatic check — a human must confirm this)")
			}
			b.WriteString("\n")
		}
	}

	fmt.Fprintf(&b, "\n## Your task\n\n%s\n\n%s\n", tc.Task.Title, tc.Task.Instruction)

	if len(tc.Task.Inputs) > 0 && string(tc.Task.Inputs) != "{}" {
		fmt.Fprintf(&b, "\nInputs:\n```json\n%s\n```\n", indentJSON(tc.Task.Inputs))
	}
	if len(tc.Task.ExpectedOutput) > 0 && string(tc.Task.ExpectedOutput) != "{}" {
		fmt.Fprintf(&b, "\nWhat this task must produce:\n```json\n%s\n```\n", indentJSON(tc.Task.ExpectedOutput))
	}

	if len(tc.Dependencies) > 0 {
		b.WriteString("\n## Results this task builds on\n\n")
		for _, dep := range tc.Dependencies {
			fmt.Fprintf(&b, "### %s (%s)\n", dep.Title, dep.Status)
			if len(dep.Result) > 0 && string(dep.Result) != "null" {
				fmt.Fprintf(&b, "```json\n%s\n```\n", indentJSON(dep.Result))
			} else {
				b.WriteString("Produced no recorded result.\n")
			}
			// State whether a dependency was verified, because "it succeeded"
			// and "it was checked" are different facts and this task may be
			// about to build on an unchecked one.
			if dep.Verified() {
				b.WriteString("_Verified._\n")
			} else if dep.Status == engine.StatusSucceeded {
				b.WriteString("_Completed but NOT verified — treat its output as unconfirmed._\n")
			}
			b.WriteString("\n")
		}
	}

	if tc.Checkpoint != nil {
		b.WriteString("\n## Where you left off\n\n")
		fmt.Fprintf(&b, "You were interrupted. This is the state you saved at step %d (%s):\n\n```json\n%s\n```\n",
			tc.Checkpoint.Seq, tc.Checkpoint.Kind, indentJSON(tc.Checkpoint.State))
		b.WriteString("\nContinue from here. Do not repeat work this state shows as already done, " +
			"and be careful about repeating anything with an external effect.\n")
	}

	if len(tc.PriorAttempts) > 0 {
		b.WriteString("\n## Attempts that already failed\n\n")
		for _, at := range tc.PriorAttempts {
			fmt.Fprintf(&b, "- Attempt %d (%s): %s — %s\n",
				at.Attempt, at.At.UTC().Format(time.RFC3339), at.ErrorCode, at.Detail)
		}
		b.WriteString("\nDo not repeat an approach that already failed for the same reason. " +
			"If you cannot see a different approach, say so rather than trying the same thing again.\n")
	}

	if refusals := tc.refusalNotes(); refusals != "" {
		b.WriteString("\n## What you may not do on this goal\n\n")
		b.WriteString(refusals)
	}

	if tc.BudgetNote != "" {
		fmt.Fprintf(&b, "\n## Budget\n\n%s\n", tc.BudgetNote)
	}
	return b.String()
}

// refusalNotes renders the permission boundary in prose.
//
// Stating what is forbidden, rather than only omitting it, is deliberate: a
// model that finds no tool for a job tends to improvise one or answer from its
// own weights. Told "you may not deploy on this goal", it can say so instead.
func (tc *TaskContext) refusalNotes() string {
	var b strings.Builder
	if !tc.Grant.Autonomy.AllowsExecution() {
		fmt.Fprintf(&b, "- This goal is at autonomy '%s'. You may reason and draft, but you may not "+
			"execute anything. If the task needs execution, say that and stop.\n", tc.Grant.Autonomy)
	}
	fmt.Fprintf(&b, "- Actions above risk tier %s are not available on this goal.\n", tc.Grant.MaxRiskTier)
	b.WriteString("- If a tool you need is unavailable, say the check could not be run. " +
		"Do not estimate the result it would have returned.\n")
	return b.String()
}

// Messages renders the full request for a role.
func (tc *TaskContext) Messages(systemPrompt string) []llm.Message {
	return []llm.Message{
		{Role: llm.System, Content: systemPrompt},
		{Role: llm.User, Content: tc.Prompt()},
	}
}

func indentJSON(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		// Not valid JSON. Returned verbatim rather than replaced by an error
		// string: the model can still read it, and hiding it would remove the
		// only clue to how it got malformed.
		return string(raw)
	}
	return buf.String()
}
