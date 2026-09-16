package agent

import (
	"context"
	"strings"
	"testing"
)

// ‼️ The live car measurement counts the visual check's sub-assembly looks (stage V4)
// by how each one's prompt opens, read from the calls it records
// (car_ceiling_live_test.go: subAssemblyLook). The product has no hook for it, on
// purpose, so this holds the wording the measurement reads: reworded here, every
// sub-assembly look would count as a look at the whole model and V4 would measure
// as never having run.
func TestLook_ASubAssemblyLookOpensTheWayTheMeasurementReadsIt(t *testing.T) {
	stub := &lookStub{}
	c := &Conversation{client: stub}
	doc := car()
	if _, _, err := c.look(context.Background(), doc, "a car", c.render(context.Background(), doc)); err != nil {
		t.Fatal(err)
	}
	subs := 0
	for _, p := range stub.prompts {
		rest, ok := strings.CutPrefix(p, "This is one sub-assembly of the model, ")
		if !ok {
			continue
		}
		subs++
		if path, _, found := strings.Cut(rest, " (an occurrence of"); !found || path == "" {
			t.Errorf("a sub-assembly look does not name its path before %q: %q", " (an occurrence of", p)
		}
	}
	if subs == 0 {
		t.Errorf("no look opened the way the measurement reads a sub-assembly look: %q", stub.prompts)
	}
}
