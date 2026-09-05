package tools

import (
	"encoding/json"
	"sort"
	"strings"
	"sync"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// Registry holds the tools available to a deployment.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
	// schemas are compiled once at registration. A tool is called in the hot
	// path of a worker loop, and re-parsing the same JSON on every call is work
	// done to reach a conclusion already known.
	schemas map[string]*Schema
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{tools: map[string]Tool{}, schemas: map[string]*Schema{}}
}

// Register adds a tool, validating its contract.
//
// Registration fails rather than warns on a bad contract. A tool with no timeout
// or no declared capability cannot be governed, and a registry that accepts it
// has a hole in exactly the place the registry exists to close.
func (r *Registry) Register(t Tool) error {
	const op = "tools.Registry.Register"

	c := t.Contract()
	if err := c.Validate(); err != nil {
		return err
	}
	// Compiled here as well as inside Validate, because this is the copy that
	// gets used. Validate proves the schema is enforceable; this keeps the
	// enforcement.
	schema, err := CompileSchema(c.Name, c.InputSchema)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[c.Name]; exists {
		return errs.New(op, errs.CodeConflict).
			WithDetail("a tool named %q is already registered; names appear in the "+
				"idempotency ledger, so two tools sharing one would deduplicate each other's calls", c.Name)
	}
	r.tools[c.Name] = t
	r.schemas[c.Name] = schema
	return nil
}

// ValidateInput checks a call's arguments against the tool's declared schema.
//
// # Why this lives on the registry
//
// Contract.InputSchema was documented as checked before a tool ran and nothing
// checked it, so an argument the contract forbade reached Run and encoding/json
// discarded it in silence. Putting the check here means every caller that can
// reach a tool can also reach its schema, and the executor cannot invoke one
// without having had the opportunity — the shape the authorisation call already
// uses (see httpapi/authorise.go).
//
// An unregistered tool is a NOT FOUND rather than a pass. A validator that
// waved through what it could not find would be a hole in the one place that
// exists to close it.
func (r *Registry) ValidateInput(name string, input json.RawMessage) error {
	r.mu.RLock()
	schema, ok := r.schemas[name]
	r.mu.RUnlock()

	if !ok {
		return errs.New("tools.Registry.ValidateInput", errs.CodeNotFound).
			WithDetail("no tool named %q is registered, so its arguments cannot be validated", name)
	}
	return schema.Validate(input)
}

// MustRegister panics on failure. For wiring at startup, where a bad contract is
// a programming error that must not reach a running system.
func (r *Registry) MustRegister(t Tool) {
	if err := r.Register(t); err != nil {
		panic("tools: " + err.Error())
	}
}

// Get returns a tool by name.
func (r *Registry) Get(name string) (Tool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	t, ok := r.tools[name]
	if !ok {
		return nil, errs.New("tools.Registry.Get", errs.CodeNotFound).
			WithDetail("no tool named %q is registered", name)
	}
	return t, nil
}

// Contracts returns every contract, sorted by name.
//
// Sorted deliberately: the tool list is part of a model request's prefix, and an
// unstable order invalidates the prompt cache on every call for no reason.
func (r *Registry) Contracts() []Contract {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]Contract, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t.Contract())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Grant is what a goal is permitted to use.
//
// Expressed as capabilities plus an explicit tier ceiling rather than a list of
// tool names, so adding a tool does not silently widen what every existing goal
// may do (PRD AGT-03, least privilege).
type Grant struct {
	// Capabilities the goal may exercise. A tool is available only if EVERY
	// capability it declares is granted — not merely one of them. A tool that
	// reads and deploys is a deploy tool.
	Capabilities []Capability
	// MaxRiskTier is the ceiling. A tool above it is not offered at all.
	MaxRiskTier engine.RiskTier
	// Autonomy is the goal's current level. Below sandbox_execute, no mutating
	// tool is offered regardless of capabilities.
	Autonomy engine.Autonomy
	// Production is the deployment context this goal runs in (PRD SAF-01).
	//
	// Held on the grant rather than read from configuration here, because a
	// grant is meant to be a complete statement of what a goal may do: a
	// permission check that reached for ambient state would classify differently
	// depending on where it was called from, which is the property a permission
	// check must not have.
	Production bool
	// CeilingSource explains where MaxRiskTier came from and what would raise
	// it, when the ceiling is the DOMAIN's rather than the goal's own.
	//
	// # Why the refusal needs this (2026-09-04)
	//
	// The reason returned below used to be "this tool is tier r2, above this
	// goal's ceiling of r1" — accurate, and a dead end. The person reading it
	// cannot tell whether they hit their own goal's setting, which they can
	// change, or the rule set of a licensed domain, which they cannot. The first
	// is a two-second fix and the second needs an authority this build has no
	// way to represent, and the message read identically for both.
	//
	// internal/domain/pack states this per domain and already shows it at
	// project creation and in `forgectl project industry`. This carries it to the
	// place work is actually stopped. Empty when the ceiling is the goal's own,
	// where there is no second authority to name.
	CeilingSource string
}

// Classify returns the tier a call on this contract actually sits at, given the
// context this grant carries (PRD SAF-01).
//
// The contract's tier is the floor. Everything that can move it upward — the
// irreversibility of the effect, the permissions it exercises, whether this is
// production — is decided by engine.Classify so that the offer list and the
// executor cannot drift into two different opinions about the same call.
func (g Grant) Classify(c Contract) (engine.RiskTier, []string) {
	return engine.Classify(engine.Classification{
		Declared:     c.RiskTier,
		Irreversible: c.Reversibility == Irreversible,
		ManualUndo:   c.Reversibility == ReversibleManual,
		Mutating:     c.Mutating(),
		Deploys:      c.HasCapability(CapDeploy),
		Transacts:    c.HasCapability(CapTransact),
		Actuates:     c.HasCapability(CapControl),
		Production:   g.Production,
	})
}

// Permits reports whether the grant allows this contract, and why not when it
// does not.
//
// The reason is returned rather than a bare bool because it is shown to the
// model: "that tool is above this goal's risk ceiling" lets it choose another
// approach, where a tool silently missing from the list leads it to invent one.
func (g Grant) Permits(c Contract) (bool, string) {
	if g.Autonomy == engine.AutonomyProhibited {
		return false, "this goal's autonomy level is 'prohibited'; no tool may run"
	}
	if c.Mutating() && !g.Autonomy.AllowsExecution() {
		return false, "this goal is at autonomy '" + string(g.Autonomy) +
			"', which permits reasoning and drafting but not execution"
	}
	if !g.MaxRiskTier.Valid() {
		return false, "this goal has no valid risk ceiling"
	}
	// The tier this call sits at HERE, which is at least the declared one and may
	// be higher (PRD SAF-01). Classified before the ceiling is checked, so a tool
	// that is only consequential in production is refused there and offered
	// elsewhere rather than being judged by its declaration in both.
	tier, why := g.Classify(c)
	if tier.AtLeast(engine.RiskR5) {
		reason := "this tool is classified R5 (prohibited); no approval authorises it"
		if len(why) > 0 {
			reason += " — " + strings.Join(why, "; ")
		}
		return false, reason
	}
	if !g.MaxRiskTier.AtLeast(tier) {
		reason := "this tool is tier " + string(tier) +
			", above this goal's ceiling of " + string(g.MaxRiskTier)
		if tier != c.RiskTier {
			// Said explicitly, because "this tool is R3" is confusing to somebody
			// reading a contract that says R2. The tier moved, and why it moved is
			// the part that lets the model choose a different approach.
			reason += ". It is declared " + string(c.RiskTier) +
				" and classified higher here because " + strings.Join(why, ", and ")
		}
		// The authority that would raise it, when the ceiling is the domain's.
		// A limit with no path is a dead end — the same rule the pack table
		// follows at project creation, applied where work is actually stopped.
		if g.CeilingSource != "" {
			reason += ". " + g.CeilingSource
		}
		return false, reason
	}
	granted := map[Capability]bool{}
	for _, cap := range g.Capabilities {
		granted[cap] = true
	}
	for _, cap := range c.Capabilities {
		if !granted[cap] {
			return false, "this goal has not been granted the '" + string(cap) + "' capability"
		}
	}
	return true, ""
}

// Available returns the tools a grant permits AND that can actually run.
//
// The two are separate on purpose. A tool that is permitted but unavailable —
// a declared CAD or solver connector with no backend — is deliberately still
// OFFERED to the model, so that calling it produces an explicit
// CONNECTOR_UNAVAILABLE rather than the model quietly inventing the result it
// would have returned. Hiding it makes fabrication more likely, not less.
func (r *Registry) Available(g Grant) []Contract {
	var out []Contract
	for _, c := range r.Contracts() {
		if ok, _ := g.Permits(c); ok {
			out = append(out, c)
		}
	}
	return out
}

// Refused returns the tools a grant blocks, with the reason for each. Rendered
// into the model's context so it knows what it cannot do and why, instead of
// discovering the boundary by guessing at it.
func (r *Registry) Refused(g Grant) map[string]string {
	out := map[string]string{}
	for _, c := range r.Contracts() {
		if ok, why := g.Permits(c); !ok {
			out[c.Name] = why
		}
	}
	return out
}

// Definitions renders the permitted tools into the model's wire format.
func (r *Registry) Definitions(g Grant) []llm.ToolDefinition {
	contracts := r.Available(g)
	out := make([]llm.ToolDefinition, 0, len(contracts))
	for _, c := range contracts {
		desc := c.Description
		// An unavailable tool says so in its own description. The model then has
		// the information at the point of decision rather than after a failed
		// call, and — more importantly — it cannot mistake the absence of a
		// result for a result.
		if !c.Available {
			desc += "\n\nUNAVAILABLE in this deployment: " + c.UnavailableReason +
				" Calling it will fail explicitly. Do not guess what it would have returned."
		}
		out = append(out, llm.ToolDefinition{
			Type: "function",
			Function: llm.FunctionDefinition{
				Name:        c.Name,
				Description: desc,
				Parameters:  c.InputSchema,
			},
		})
	}
	return out
}
