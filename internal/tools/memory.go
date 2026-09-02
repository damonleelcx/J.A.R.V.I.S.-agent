package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/claim"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/memory"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// FORGE's own access to what it remembers (PRD MEM-01).
//
// # Why memory is a tool and not an invisible context injection
//
// The alternative was to have the assembler quietly prepend recalled items to
// every prompt. That is how most systems do it, and it has two properties this
// one cannot accept: nothing records that a particular item influenced a
// particular decision, and there is no moment where remembering is a governed
// act. As a tool it goes through the same registry, the same idempotency ledger
// and the same timeline as every other capability — so "why did FORGE think
// that?" is answerable from rows, and a deployment that does not want its agent
// writing memory simply does not register the tool.
//
// # Why the model is not asked HOW it knows something
//
// This is the rule from internal/domain/claim, applied at the one place it
// would be easiest to break. A tool input of "how": "observed" would be
// accepted, and the label would be exactly as reliable as the fabricated NEMA
// 17 bolt pattern that caused the vocabulary to exist — a component cannot be
// its own guard.
//
// So the label is derived from the ONLY thing that is structurally true here: a
// fact the model chose to write down is a conclusion it drew from context, and
// that is INFERRED. Not observed, because FORGE did not see it; not retrieved,
// because there is nothing to point at. If a later wave wants a stronger label
// it has to derive it from something checkable — a tool's actual output, a file
// actually read — and that will be a different tool, not a wider input schema.

// memoryWritableScopes are the layers FORGE may write to on its own initiative.
//
// Personal preferences are excluded because they are the user's to state, not
// the agent's to infer: an agent that decides what you prefer is worse than one
// that asks. Organisation memory is excluded because a conclusion drawn during
// one goal would become a fact for every project in the deployment, which is how
// a single wrong inference spreads everywhere. Both remain writable through the
// API, by a person.
var memoryWritableScopes = []memory.Scope{memory.ScopeTurn, memory.ScopeSession, memory.ScopeProject}

// MemoryRecallTool lets FORGE read what it remembers, with reasons.
type MemoryRecallTool struct {
	svc  *memory.Service
	pool *db.Pool
}

// NewMemoryRecallTool wires the recall tool.
func NewMemoryRecallTool(svc *memory.Service, pool *db.Pool) *MemoryRecallTool {
	return &MemoryRecallTool{svc: svc, pool: pool}
}

// Contract declares the recall tool.
func (t *MemoryRecallTool) Contract() Contract {
	return Contract{
		Name: "memory_recall",
		Description: "Recall what FORGE already knows about this goal and project. " +
			"Returns each item with how it is known (observed, retrieved, calculated, " +
			"simulated, inferred, assumed, proposed) and why it was returned. " +
			"Items marked as inferred or assumed are NOT established facts — treat them " +
			"as context, and say so if you rely on one.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"prefix": {"type": "string", "description": "only keys starting with this"},
				"key": {"type": "string", "description": "one exact key"},
				"limit": {"type": "integer", "minimum": 1, "maximum": 200}
			},
			"additionalProperties": false
		}`),
		Capabilities:  []Capability{CapRead},
		RiskTier:      engine.RiskR0,
		Reversibility: ReversibleNone,
		Timeout:       10 * time.Second,
		Idempotent:    true,
		Available:     true,
	}
}

type memoryRecallInput struct {
	Prefix string `json:"prefix"`
	Key    string `json:"key"`
	Limit  int    `json:"limit"`
}

// Run recalls memory for the invocation's goal and its project.
func (t *MemoryRecallTool) Run(ctx context.Context, inv Invocation) (*Result, error) {
	const op = "tools.MemoryRecallTool.Run"

	var in memoryRecallInput
	if len(inv.Input) > 0 {
		if err := decodeStrict(inv.Input, &in); err != nil {
			return nil, errs.Wrap(op, errs.CodeValidationFailed, err).
				WithDetail("memory_recall was called with arguments it does not accept")
		}
	}
	projectID, err := t.projectOf(ctx, inv.GoalID)
	if err != nil {
		return nil, err
	}

	rc := memory.Recall{GoalID: inv.GoalID, ProjectID: projectID, Prefix: in.Prefix, Limit: in.Limit}
	if in.Key != "" {
		rc.Keys = []string{in.Key}
	}
	got, err := t.svc.Recall(ctx, rc)
	if err != nil {
		return nil, err
	}

	type recalledOut struct {
		Key        string          `json:"key"`
		Value      json.RawMessage `json:"value"`
		How        string          `json:"how"`
		HowMeans   string          `json:"how_means"`
		Actionable bool            `json:"may_be_acted_on"`
		Layer      string          `json:"layer"`
		Why        string          `json:"why_returned"`
	}
	out := make([]recalledOut, 0, len(got))
	var raw strings.Builder
	for _, g := range got {
		c := claim.Claim{How: g.Item.How, Source: g.Item.Source}
		out = append(out, recalledOut{
			Key: g.Item.Key, Value: json.RawMessage(g.Item.Value),
			How: string(g.Item.How), HowMeans: g.Item.How.Gloss(),
			// Handed to the model explicitly rather than left for it to work out
			// from the label. "May I act on this?" is a rule, and a rule the
			// model re-derives is a rule it will eventually derive differently.
			Actionable: c.Actionableish(),
			Layer:      string(g.Item.Scope), Why: g.Detail,
		})
		fmt.Fprintf(&raw, "%s [%s] %s — %s\n", g.Item.Key, g.Item.How, g.Item.Value, g.Detail)
	}
	encoded, err := json.Marshal(map[string]any{"recalled": out})
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeSerializationFail, err)
	}
	return &Result{
		Output: encoded,
		Raw:    raw.String(),
		Evidence: fmt.Sprintf("%d memory item(s) read from this deployment's own store; each carries how it is known",
			len(out)),
	}, nil
}

func (t *MemoryRecallTool) projectOf(ctx context.Context, goalID string) (string, error) {
	return projectOfGoal(ctx, t.pool, goalID)
}

// MemoryRememberTool lets FORGE write down something it concluded.
type MemoryRememberTool struct {
	svc  *memory.Service
	pool *db.Pool
}

// NewMemoryRememberTool wires the remember tool.
func NewMemoryRememberTool(svc *memory.Service, pool *db.Pool) *MemoryRememberTool {
	return &MemoryRememberTool{svc: svc, pool: pool}
}

// Contract declares the remember tool.
//
// Note what the input schema does NOT have: a field for how the thing is known.
// See the package comment above — that is the whole point.
func (t *MemoryRememberTool) Contract() Contract {
	return Contract{
		Name: "memory_remember",
		Description: "Write down something concluded during this work so a later cycle can use it. " +
			"It is recorded as INFERRED — a conclusion drawn from context, not a measurement — " +
			"because that is what it is. Choose the layer by how long it should last: " +
			"'turn' for this exchange, 'session' for this stretch of work, 'project' for " +
			"something true of the project until somebody changes it. " +
			"A key a user has asked FORGE to forget will be refused.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"scope": {"type": "string", "enum": ["turn", "session", "project"]},
				"key": {"type": "string", "description": "a stable dotted key, e.g. bracket.wall_thickness"},
				"value": {"description": "any JSON value"}
			},
			"required": ["scope", "key", "value"],
			"additionalProperties": false
		}`),
		Capabilities: []Capability{CapWrite},
		RiskTier:     engine.RiskR1,
		// A memory write is undoable by FORGE itself: the user forgets it, or a
		// later write corrects it. Neither leaves the workspace.
		Reversibility: ReversibleAutomatic,
		Timeout:       10 * time.Second,
		// Not idempotent in the ledger's sense: writing the same key twice is a
		// correction, and short-circuiting the second one would silently keep a
		// value the agent had already decided to replace.
		Idempotent: false,
		Available:  true,
	}
}

type memoryRememberInput struct {
	Scope string          `json:"scope"`
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

// Run records a conclusion as inferred memory.
func (t *MemoryRememberTool) Run(ctx context.Context, inv Invocation) (*Result, error) {
	const op = "tools.MemoryRememberTool.Run"

	var in memoryRememberInput
	if err := decodeStrict(inv.Input, &in); err != nil {
		return nil, errs.Wrap(op, errs.CodeValidationFailed, err).
			WithDetail("memory_remember was called with arguments it does not accept. " +
				"In particular there is no way to state how something is known: a fact FORGE " +
				"chose to write down is recorded as inferred, because that is what it is.")
	}
	scope := memory.Scope(in.Scope)
	if !writableScope(scope) {
		return nil, errs.New(op, errs.CodeForbidden).
			WithDetail("FORGE may write to %s memory only. Personal preferences are the user's to state, "+
				"and organisation knowledge is not something one goal's conclusion may set for every project.",
				scopeList())
	}

	owner := inv.GoalID
	if scope == memory.ScopeProject {
		projectID, err := projectOfGoal(ctx, t.pool, inv.GoalID)
		if err != nil {
			return nil, err
		}
		owner = projectID
	}

	var value any
	if err := json.Unmarshal(in.Value, &value); err != nil {
		return nil, errs.Wrap(op, errs.CodeValidationFailed, err).
			WithDetail("the value is not valid JSON")
	}
	item, err := t.svc.Remember(ctx, memory.Write{
		Scope: scope, Owner: owner, Key: in.Key, Value: value,
		// Derived, not accepted. See the package comment.
		How: claim.Inferred,
		// Provenance points at the work that produced it, so a reader can find
		// the timeline where the conclusion was drawn.
		Source: "concluded during goal " + inv.GoalID,
	})
	if err != nil {
		return nil, err
	}

	encoded, _ := json.Marshal(map[string]any{
		"key": item.Key, "layer": string(item.Scope), "how": string(item.How),
		"how_means": item.How.Gloss(),
		"expires_at": func() string {
			if item.ExpiresAt == nil {
				return "never"
			}
			return item.ExpiresAt.UTC().Format(time.RFC3339)
		}(),
	})
	return &Result{
		Output: encoded,
		Raw:    fmt.Sprintf("remembered %s in %s memory as inferred", item.Key, item.Scope),
		Evidence: "a conclusion was recorded, labelled inferred; it is not evidence of anything " +
			"and must not be cited as a measurement",
	}, nil
}

func writableScope(s memory.Scope) bool {
	for _, w := range memoryWritableScopes {
		if w == s {
			return true
		}
	}
	return false
}

func scopeList() string {
	out := make([]string, 0, len(memoryWritableScopes))
	for _, s := range memoryWritableScopes {
		out = append(out, string(s))
	}
	return strings.Join(out, ", ")
}

// decodeStrict rejects fields the input struct does not declare.
//
// # Why this is here and not left to the schema
//
// The contract carries a JSON Schema with "additionalProperties": false, and
// Contract.InputSchema is documented as being validated before a tool runs. In
// this build it is not: registry.go hands the schema to the model provider and
// nothing else reads it, so an unexpected field reaches Run and encoding/json
// discards it in silence.
//
// For these two tools that silence is the exact failure they exist to prevent.
// A model that sends "how": "observed" would have its label dropped, the item
// would be stored as inferred, and the model would carry on believing it had
// recorded a measurement. The safe value would be written and the caller would
// be misled about what happened — which is worse than an error, because nothing
// reports it.
//
// So the refusal is made here, where it is certainly enforced, rather than
// relying on a validation step that is described but does not exist.
//
// See docs/bugfix/2026-09-02-tool-input-schemas-are-never-validated.md. The
// general defect is still open and affects every tool; if the executor ever
// gains real schema validation, this stays anyway — belt and braces on the one
// tool whose correctness depends on a field being absent.
// Fence: TestTool_ForgeCannotDeclareHowItKnowsSomething.
func decodeStrict(raw json.RawMessage, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	return nil
}

// projectOfGoal resolves a goal's project.
//
// The Invocation carries only a goal id, and project memory needs a project.
// Resolved from the row rather than passed in, so a tool call cannot name a
// project the goal does not belong to.
func projectOfGoal(ctx context.Context, pool *db.Pool, goalID string) (string, error) {
	const op = "tools.projectOfGoal"

	var projectID string
	err := pool.QueryRow(ctx, `select project_id from forge_goals where id = $1`, goalID).Scan(&projectID)
	if err != nil {
		return "", errs.Wrap(op, errs.CodeNotFound, err).
			WithDetail("goal %s has no project, so there is no project memory to reach", goalID)
	}
	return projectID, nil
}
