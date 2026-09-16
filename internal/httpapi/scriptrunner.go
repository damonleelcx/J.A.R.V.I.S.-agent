package httpapi

import (
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/agent"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/agent/cadbridge"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/cad"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
)

// The kernel's adapters for the agent live in internal/agent/cadbridge, shared
// with forge-worker since it builds too (Phase 2, stage A1).
func scriptRunner(k *cad.Kernel) agent.ScriptRunner { return cadbridge.Scripts(k) }

func solidBuilder(k *cad.Kernel) agent.SolidBuilder { return cadbridge.Solids(k) }

// illustrator returns the thing that draws a reference, or nil when this
// deployment has no image model.
//
// Untyped nil on purpose, for the reason scriptRunner returns one: a typed nil
// inside an interface is not nil, and the mistake would be silent in the worst
// direction — every turn would pay for a drawing call that cannot succeed.
//
// It takes the model client rather than a config value because the client is
// what knows whether a model is configured for the role, and asking it keeps
// "the deployment draws" from becoming a second fact that can disagree with the
// first.
func illustrator(c llm.Client) llm.Illustrator {
	i, ok := c.(llm.Illustrator)
	if !ok || i == nil || i.IllustratorModel() == "" {
		return nil
	}
	return i
}
