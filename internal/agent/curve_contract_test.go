package agent

import (
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// The contract teaches bowed edges from the validator's own table (B3,
// 2026-09-18), and no longer tells the model it cannot say them.
//
// The old "radius" paragraph ended "a crescent, a lens, a bulged edge. There is
// no vocabulary for those here." — two paragraphs after the "via" paragraph that
// IS that vocabulary. Both prompts a model is given (conversation and build)
// must carry the guide geometry prints, and neither may carry the denial.
func TestContract_TeachesBowedEdgesFromTheValidatorsTable(t *testing.T) {
	guide := geometry.CurveGuide()
	for name, prompt := range map[string]string{"converse": converseFraming, "build": buildContract} {
		if !strings.Contains(prompt, guide) {
			t.Errorf("the %s contract does not print geometry.CurveGuide()", name)
		}
		if strings.Contains(prompt, "There is no vocabulary for those here") {
			t.Errorf("the %s contract still says there is no vocabulary for a crescent, lens or "+
				"bulged edge", name)
		}
		for _, shape := range []string{"A LENS", "A CRESCENT", "A BULGED FENDER STATION"} {
			if !strings.Contains(prompt, shape) {
				t.Errorf("the %s contract shows no example of %s", name, strings.ToLower(shape))
			}
		}
		// Printed once: the via paragraph moved into the guide, and a hand-typed
		// copy left behind would be a second set of rules to drift.
		if n := strings.Count(prompt, `- "via" on a point BENDS THE EDGE`); n != 1 {
			t.Errorf("the %s contract has %d via paragraphs, want 1", name, n)
		}
	}
}
