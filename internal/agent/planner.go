package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// plannerFraming is the role instruction appended to FORGE's identity.
//
// It is explicit that the planner does not act. A single model asked both to
// decide what to do and to do it drifts toward whatever it can do next rather
// than whatever the goal needs — so the separation is stated to the model as
// well as enforced by which tools it is given (none).
const plannerFraming = `You are PLANNING. You do not execute anything in this role, and you have no tools.

Decompose the goal into tasks a worker can pick up independently. Each task will
be executed later, possibly days later, possibly by a different process that sees
only what you write here — so a task whose meaning depends on being read straight
after this conversation is a broken task.

Rules for a good decomposition:

- Each task is ONE unit of work with a clear finish. "Set up the project" is not
  a task; "create go.mod with module X and Go 1.26" is.
- State the inputs a task needs and what it must produce. A task whose expected
  output is unstated cannot be verified afterwards, only agreed with.
- Declare dependencies honestly. Two tasks that could run at once should not
  depend on each other just because you wrote them in order.
- Prefer fewer, larger tasks over many trivial ones. Every task costs a model
  call, a lease, and a row; decomposition is not free.
- If the goal is ambiguous in a way that changes what should be built, do not
  guess: return a single task that asks the human the specific question.
- Risk tiers: r0 discussion, r1 reversible sandbox work, r2 consequential digital
  change, r3 release preparation, r4 safety-critical. Anything at r2 or above
  will pause for a human before it runs. Tier honestly — under-tiering to avoid a
  gate is the worst thing you can do here.

Reply with JSON only, matching this shape exactly:

{
  "rationale": "one paragraph on the shape of this plan and why",
  "clarification_needed": "" or "the specific question that must be answered first",
  "tasks": [
    {
      "key": "stable-kebab-identifier",
      "title": "short imperative title",
      "instruction": "what the worker must do, written for someone with no other context",
      "inputs": {},
      "expected_output": {"description": "what a finished result looks like"},
      "depends_on": ["key-of-another-task"],
      "risk_tier": "r1"
    }
  ]
}`

// Planner turns a goal into a task DAG.
type Planner struct {
	client llm.Client
	char   persona.Character
}

// NewPlanner returns a planner.
func NewPlanner(client llm.Client, char persona.Character) *Planner {
	return &Planner{client: client, char: char}
}

// PlannedTask is one node the planner proposes.
type PlannedTask struct {
	Key            string          `json:"key"`
	Title          string          `json:"title"`
	Instruction    string          `json:"instruction"`
	Inputs         json.RawMessage `json:"inputs"`
	ExpectedOutput json.RawMessage `json:"expected_output"`
	DependsOn      []string        `json:"depends_on"`
	RiskTier       string          `json:"risk_tier"`
}

// PlanResult is a whole proposed plan.
type PlanResult struct {
	Rationale string `json:"rationale"`
	// ClarificationNeeded, when non-empty, means the planner refused to guess.
	// That is a success, not a failure: a plan built on a wrong assumption costs
	// far more than a question.
	ClarificationNeeded string        `json:"clarification_needed"`
	Tasks               []PlannedTask `json:"tasks"`
	Usage               llm.Usage     `json:"-"`
	Model               string        `json:"-"`
}

// Plan decomposes a goal.
func (p *Planner) Plan(ctx context.Context, goal *engine.Goal, priorPlan *PlanResult, replanReason string) (*PlanResult, error) {
	const op = "agent.Planner.Plan"

	var user strings.Builder
	fmt.Fprintf(&user, "## Goal\n\n%s\n\n%s\n", goal.Title, goal.Statement)

	if len(goal.CompletionCriteria) > 0 {
		user.WriteString("\nComplete when:\n")
		for _, c := range goal.CompletionCriteria {
			fmt.Fprintf(&user, "- %s\n", c.Statement)
		}
	}
	fmt.Fprintf(&user, "\nAutonomy ceiling: %s\nRisk ceiling: %s\n", goal.Autonomy, goal.RiskTier)

	if replanReason != "" {
		fmt.Fprintf(&user, "\n## This is a REPLAN\n\nWhy: %s\n", replanReason)
		if priorPlan != nil {
			user.WriteString("\nThe previous plan was:\n")
			for _, t := range priorPlan.Tasks {
				fmt.Fprintf(&user, "- %s: %s\n", t.Key, t.Title)
			}
			user.WriteString("\nKeep the task keys that are still correct — reusing a key means that " +
				"task is not recreated and its completed work is preserved. Only change what the " +
				"reason above actually requires.\n")
		}
	}

	resp, err := p.client.Complete(ctx, llm.Request{
		Role: llm.RolePlanner,
		Messages: []llm.Message{
			{Role: llm.System, Content: persona.SystemPrompt(p.char, plannerFraming)},
			{Role: llm.User, Content: user.String()},
		},
		JSONMode:  true,
		MaxTokens: 8192,
	})
	if err != nil {
		return nil, err
	}
	// A truncated plan is missing its tail, and its tail is where the later
	// tasks live. Accepting it would silently produce a half-plan that looks
	// complete.
	if resp.Truncated() {
		return nil, errs.New(op, errs.CodeExternalProtocol).
			WithDetail("the planner was cut off at the token limit; the plan is incomplete and was discarded " +
				"rather than executed as a partial one")
	}

	var out PlanResult
	if err := json.Unmarshal([]byte(extractJSON(resp.Content)), &out); err != nil {
		return nil, errs.Wrap(op, errs.CodeExternalProtocol, err).
			WithDetail("the planner did not return usable JSON: %s", truncate(resp.Content, 400))
	}
	out.Usage = resp.Usage
	out.Model = resp.Model

	if err := out.Validate(); err != nil {
		return nil, err
	}
	return &out, nil
}

// Validate checks a proposed plan is executable before any of it is written.
//
// Validation happens here rather than at insert time so a bad plan is rejected
// whole. Half-inserting a plan and failing on task seven leaves a goal with six
// tasks and no way to reach its end.
func (p *PlanResult) Validate() error {
	const op = "agent.PlanResult.Validate"

	if p.ClarificationNeeded != "" {
		// A plan that asks a question is allowed to have no tasks.
		return nil
	}
	if len(p.Tasks) == 0 {
		return errs.New(op, errs.CodeExternalProtocol).
			WithDetail("the planner returned no tasks and asked no question")
	}

	keys := map[string]bool{}
	for i, t := range p.Tasks {
		if t.Key == "" {
			return errs.New(op, errs.CodeExternalProtocol).
				WithDetail("task %d has no key; dependencies could not refer to it", i)
		}
		if keys[t.Key] {
			return errs.New(op, errs.CodeExternalProtocol).
				WithDetail("two tasks share the key %q; a dependency on it would be ambiguous", t.Key)
		}
		keys[t.Key] = true

		if strings.TrimSpace(t.Title) == "" || strings.TrimSpace(t.Instruction) == "" {
			return errs.New(op, errs.CodeExternalProtocol).
				WithDetail("task %q has no title or no instruction", t.Key)
		}
		if t.RiskTier != "" && !engine.RiskTier(t.RiskTier).Valid() {
			return errs.New(op, errs.CodeExternalProtocol).
				WithDetail("task %q declares unknown risk tier %q", t.Key, t.RiskTier)
		}
	}

	// Dependencies must resolve.
	for _, t := range p.Tasks {
		for _, dep := range t.DependsOn {
			if dep == t.Key {
				return errs.New(op, errs.CodeExternalProtocol).
					WithDetail("task %q depends on itself", t.Key)
			}
			if !keys[dep] {
				return errs.New(op, errs.CodeExternalProtocol).
					WithDetail("task %q depends on %q, which is not in the plan", t.Key, dep)
			}
		}
	}
	// And the graph must be acyclic. A cycle inserts cleanly and then deadlocks:
	// every task in it waits forever, the goal never finishes, and nothing
	// reports an error — the exact shape of a silently stalled agent.
	if cycle := findCycle(p.Tasks); cycle != nil {
		return errs.New(op, errs.CodeExternalProtocol).
			WithDetail("the plan contains a dependency cycle: %s. Every task in it would wait forever",
				strings.Join(cycle, " → "))
	}
	return nil
}

// findCycle returns a cycle in the dependency graph, or nil.
func findCycle(tasks []PlannedTask) []string {
	deps := map[string][]string{}
	for _, t := range tasks {
		deps[t.Key] = t.DependsOn
	}

	const (
		white = 0 // unvisited
		grey  = 1 // on the current path
		black = 2 // fully explored
	)
	colour := map[string]int{}
	var path []string

	var visit func(string) []string
	visit = func(k string) []string {
		colour[k] = grey
		path = append(path, k)
		for _, d := range deps[k] {
			switch colour[d] {
			case grey:
				// Found it. Return the path from where the cycle starts.
				for i, p := range path {
					if p == d {
						return append(append([]string{}, path[i:]...), d)
					}
				}
				return append([]string{}, path...)
			case white:
				if c := visit(d); c != nil {
					return c
				}
			}
		}
		path = path[:len(path)-1]
		colour[k] = black
		return nil
	}

	for _, t := range tasks {
		if colour[t.Key] == white {
			path = nil
			if c := visit(t.Key); c != nil {
				return c
			}
		}
	}
	return nil
}

// extractJSON pulls a JSON object out of a response that may be wrapped.
//
// JSON mode is not universally honoured, and even when it is, some models fence
// the output in markdown. Recovering the object is worth doing because the
// alternative is discarding a correct plan over formatting.
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	if fence := strings.Index(s, "```"); fence >= 0 {
		rest := s[fence+3:]
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			rest = rest[nl+1:]
		}
		if end := strings.Index(rest, "```"); end >= 0 {
			s = strings.TrimSpace(rest[:end])
		}
	}
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start >= 0 && end > start {
		return s[start : end+1]
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
