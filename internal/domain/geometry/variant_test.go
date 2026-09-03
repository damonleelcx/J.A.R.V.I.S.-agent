package geometry

import (
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
)

func newVariant() NewVariant {
	return NewVariant{
		ProjectID: "prj_1", InitiatorID: "usr_1",
		Agent: workspace.AgentConverse, Generator: "claude-opus-5",
		Inputs: map[string]any{"message": "design me a bracket"},
		Document: Document{
			Name: "bracket", Units: "mm",
			Parts:       []Part{{ID: "plate", Name: "plate", Shape: "box", Size: map[string]float64{"width": 60}}},
			NotVerified: []string{"nothing checked"},
		},
	}
}

// PRD VIS-04 names six things every render must link to. A variant that cannot
// state one of them is refused rather than stored with a blank, because a
// provenance panel with a hole in it looks like provenance and answers none of
// the questions one is shown for.
func TestNewVariant_RefusesAnythingItCannotFullyAttribute(t *testing.T) {
	cases := []struct {
		fact   string
		break_ func(*NewVariant)
		expect string
	}{
		{"initiator", func(n *NewVariant) { n.InitiatorID = "" }, "whose proposal"},
		{"generator", func(n *NewVariant) { n.Generator = "  " }, "generator"},
		{"agent", func(n *NewVariant) { n.Agent = "nobody" }, "recognised agent"},
		{"inputs", func(n *NewVariant) { n.Inputs = nil }, "made from"},
		{"name", func(n *NewVariant) { n.Document.Name = "" }, "named"},
		{"parts", func(n *NewVariant) { n.Document.Parts = nil }, "no parts"},
	}
	for _, c := range cases {
		n := newVariant()
		c.break_(&n)
		err := n.Validate()
		if err == nil {
			t.Errorf("a variant with no %s was accepted", c.fact)
			continue
		}
		if !strings.Contains(err.Error(), c.expect) {
			t.Errorf("%s: refusal does not explain itself: %v", c.fact, err)
		}
	}
}

// VIS-06 at the storage boundary as well as the conversation one. The
// conversation guarantees it for the path that exists today; this guarantees it
// for every path, including the ones written by somebody who has not read
// converse.go.
func TestNewVariant_RefusesGeometryThatClaimsNothingIsUnestablished(t *testing.T) {
	n := newVariant()
	n.Document.NotVerified = nil
	err := n.Validate()
	if err == nil {
		t.Fatal("geometry was stored without stating what it does not establish")
	}
	if !strings.Contains(err.Error(), "VIS-06") {
		t.Errorf("the refusal does not name the requirement it is enforcing: %v", err)
	}
}

// Comparison matches parts across variants by id. A duplicate inside one variant
// makes that matching ambiguous, and the side-by-side view would then report a
// difference that is an artefact of the duplicate rather than of the design.
func TestNewVariant_RefusesADuplicatePartID(t *testing.T) {
	n := newVariant()
	n.Document.Parts = append(n.Document.Parts, n.Document.Parts[0])
	err := n.Validate()
	if err == nil {
		t.Fatal("two parts sharing an id were accepted")
	}
	if !strings.Contains(err.Error(), "compare") {
		t.Errorf("the refusal does not say what breaks: %v", err)
	}
}

// The happy path must still pass — a validator that refuses everything is not a
// fence, it is an outage.
func TestNewVariant_AcceptsAFullyAttributedProposal(t *testing.T) {
	n := newVariant()
	if err := n.Validate(); err != nil {
		t.Fatal(err)
	}
}

// The workbench conversation records no tool call, and that is legal for exactly
// two agents. Widening it further would let any agent write an untraceable
// change.
func TestArtifactVersion_OnlyTwoAgentsWorkWithoutATool(t *testing.T) {
	for _, a := range workspace.Agents() {
		v := workspace.Version{
			ArtifactID: "art_1", InitiatorID: "usr_1", Agent: a,
			Inputs: []byte(`{}`), Verification: workspace.Unverified, Disposition: workspace.Pending,
		}
		err := v.Validate()
		toolless := a == workspace.AgentHuman || a == workspace.AgentConverse
		if toolless && err != nil {
			t.Errorf("%s must be able to record a change with no tool call: %v", a, err)
		}
		if !toolless && err == nil {
			t.Errorf("%s recorded a change with no tool call, which nobody can trace to an action", a)
		}
	}
}

// A path is derived from the name so that successive proposals of the same thing
// accumulate as versions of one artifact — which is what makes "v1 against v3"
// a history rather than two unrelated rows.
func TestArtifactPath_TheSameNameLandsOnTheSameArtifact(t *testing.T) {
	if a, b := artifactPath("NEMA 17 bracket"), artifactPath("nema 17 bracket"); a != b {
		t.Errorf("the same assembly took two paths: %q and %q", a, b)
	}
	if got := artifactPath("NEMA 17 bracket"); got != "geometry/nema-17-bracket.forge.json" {
		t.Errorf("path is %q", got)
	}
	// A name of nothing but punctuation still needs a path: the schema refuses a
	// blank one, and a person must not meet an error over a shape that is fine.
	if got := artifactPath("!!!"); got != "geometry/unnamed.forge.json" {
		t.Errorf("an unnameable assembly produced %q", got)
	}
}
