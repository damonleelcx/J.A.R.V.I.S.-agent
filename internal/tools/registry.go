package tools

import (
	"sort"
	"sync"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// Registry holds the tools available to a deployment.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{tools: map[string]Tool{}} }

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
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[c.Name]; exists {
		return errs.New(op, errs.CodeConflict).
			WithDetail("a tool named %q is already registered; names appear in the "+
				"idempotency ledger, so two tools sharing one would deduplicate each other's calls", c.Name)
	}
	r.tools[c.Name] = t
	return nil
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
	if c.RiskTier.AtLeast(engine.RiskR5) {
		return false, "this tool is classified R5 (prohibited); no approval authorises it"
	}
	if !g.MaxRiskTier.AtLeast(c.RiskTier) {
		return false, "this tool is tier " + string(c.RiskTier) +
			", above this goal's ceiling of " + string(g.MaxRiskTier)
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
