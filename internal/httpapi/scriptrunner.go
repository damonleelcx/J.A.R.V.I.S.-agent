package httpapi

import (
	"context"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/agent"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/cad"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// Giving the agent something that can run a script, without giving it the kernel.
//
// # Why an adapter and not the kernel itself
//
// agent.ScriptRunner is `RunScript(ctx, source) error`. The kernel's method
// returns the STEP it built as well, and the agent deliberately does not want
// it: the document is the single source of truth for what the model is, and the
// solid is built from it downstream. A cached shape here would be a second
// answer to the same question, and the two would eventually differ.
//
// It also keeps `internal/agent` free of a dependency on `internal/domain/cad`,
// which matters more than it looks: the agent is what a live test drives with a
// stub runner, and a test that had to construct a kernel to exercise a repair
// would need Python and OpenCASCADE to check a prompt.
type kernelScripts struct{ k *cad.Kernel }

func (r kernelScripts) RunScript(ctx context.Context, doc *geometry.Document, source string) error {
	var params map[string]float64
	if doc != nil {
		params = cad.ScriptParameters(*doc)
	}
	_, err := r.k.RunScript(ctx, source, params)
	return err
}

// scriptRunner returns the runner for a deployment, or nil when it does not run
// scripts.
//
// Returns an untyped nil so `c.runner != nil` means what it says. A typed nil in
// an interface is not nil, and the mistake would be silent in the worst
// direction — the contract would offer scripts and every verification would
// panic or, worse, be skipped.
func scriptRunner(k *cad.Kernel) agent.ScriptRunner {
	if k == nil || !k.ScriptsEnabled() || !k.Available() {
		return nil
	}
	return kernelScripts{k: k}
}

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

// kernelSolids builds the real surface of a document for the agent's checks.
//
// # Why the conversion lives here
//
// The kernel returns MeshPart — a flat []float64 of coordinates and an []int32
// of indices, which is what a mesh looks like on a wire. The agent wants
// geometry.Triangle, which is what a rasterizer looks at. Neither package should
// learn the other's shape to get from one to the other, so the translation sits
// where the two are already wired together.
type kernelSolids struct{ k *cad.Kernel }

func (r kernelSolids) BuildSurface(ctx context.Context, doc *geometry.Document) ([]geometry.RenderPart, error) {
	const op = "httpapi.kernelSolids.BuildSurface"
	if doc == nil {
		return nil, errs.New(op, errs.CodeInvariantViolated).WithDetail("no document to build")
	}
	// The unit must be one the kernel can convert: BuildDocument refuses an
	// unknown one outright, because a STEP file declares its own scale and
	// writing one would put a guess about scale inside the file. A picture has
	// no scale to get wrong — every view fits whatever it is given — so an
	// unstated unit falls back to millimetres HERE rather than losing the render.
	unit, known := geometry.ParseUnit(doc.Units)
	if !known {
		unit = geometry.Millimetre
	}
	built, err := r.k.BuildMesh(ctx, *doc, unit)
	if err != nil {
		return nil, err
	}
	out := make([]geometry.RenderPart, 0, len(built.Mesh))
	for _, m := range built.Mesh {
		tris := geometry.TrianglesFrom(m.Vertices, m.Triangles)
		if len(tris) == 0 {
			continue
		}
		out = append(out, geometry.RenderPart{ID: m.ID, Triangles: tris})
	}
	return out, nil
}

// solidBuilder returns the thing that builds a surface, or nil when this
// deployment has no kernel. Untyped nil, for the reason scriptRunner returns one.
func solidBuilder(k *cad.Kernel) agent.SolidBuilder {
	if k == nil || !k.Available() {
		return nil
	}
	return kernelSolids{k: k}
}
