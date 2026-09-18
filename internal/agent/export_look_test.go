package agent

import (
	"context"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// LookedForTest runs the build's visual check on a finished model as a build step
// runs it (look over the render the conversation draws), for the live V4 check of a
// kept car: what the check saw, how many sub-assemblies it looked at closely of how
// many, and whether the picture was the kernel's. Only in test builds.
func LookedForTest(ctx context.Context, c *Conversation, doc *Prototype, asked string) (
	seen []geometry.Problem, looked, of int, fromKernel bool, err error) {
	sheet := c.render(ctx, doc)
	seen, cov, err := c.look(ctx, doc, asked, sheet)
	return seen, cov.Looked, cov.Of, sheet.FromKernel, err
}
