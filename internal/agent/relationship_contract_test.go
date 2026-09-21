package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// Issue 7. The dimensions that collapse a solid are taught from the SAME table
// the refusal is written from, so the sentence in the prompt cannot drift from
// the rule in the code — and a model told which dimensions matter writes fewer
// documents that have to be refused at all.
func TestContract_TeachesTheCollapsingDimensionsFromTheValidatorsTable(t *testing.T) {
	guide := geometry.CollapsingDimensionGuide()
	for name, prompt := range map[string]string{"converse": converseFraming, "build": buildContract} {
		if !strings.Contains(prompt, guide) {
			t.Errorf("the %s contract does not print geometry.CollapsingDimensionGuide()", name)
		}
	}
	// And the guide is the table: every shape the validator refuses for, and
	// every dimension it refuses on, is in the sentence the model reads.
	for _, want := range []string{
		`"box" — "width", "height", "depth"`,
		`"cylinder" — "radius", "height"`,
		`"sphere" — "radius"`,
		`"cone" — "radius", "height"`,
		`"plane" — "width", "depth"`,
		`"extrusion" — "depth"`,
	} {
		if !strings.Contains(guide, want) {
			t.Errorf("the guide does not say %s:\n%s", want, guide)
		}
	}
	// And it does NOT teach a dimension the validator leaves alone: "radius_top"
	// is zero on purpose for a cone, and telling the model otherwise would make
	// it stop writing a shape FORGE builds.
	if strings.Contains(guide, "radius_top") {
		t.Errorf("the guide teaches radius_top, which is legitimately zero:\n%s", guide)
	}
}

// Issues 9, 10 and 11. What relationship checking covers is taught from the
// checker's own table, and so is what it does NOT: a model that knows an unnamed
// relationship comes back unchecked can name it instead.
func TestContract_TeachesRelationshipCheckingFromTheCheckersTable(t *testing.T) {
	guide := geometry.RelationshipGuide()
	for name, prompt := range map[string]string{"converse": converseFraming, "build": buildContract} {
		if !strings.Contains(prompt, guide) {
			t.Errorf("the %s contract does not print geometry.RelationshipGuide()", name)
		}
	}
	for _, k := range geometry.RelationshipKinds() {
		line := fmt.Sprintf("%s — from %s; FORGE reports %s.", k.Kind, k.Reads, k.Result)
		if !strings.Contains(guide, line) {
			t.Errorf("the guide does not carry the table's row for %q:\n%s", k.Kind, guide)
		}
	}
	if !strings.Contains(guide, "reported as NOT checked, with the reason") {
		t.Errorf("the guide does not say what happens to a relationship FORGE cannot name:\n%s", guide)
	}
}
