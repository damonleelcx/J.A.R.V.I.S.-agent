package workspace_test

import (
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/pack"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
)

// A pack's expected node kinds are real node kinds.
//
// # Why this fence lives HERE
//
// `pack.Definition.Schema` holds plain strings, because workspace imports pack
// and importing back would be a cycle. That is the right dependency direction
// and it costs a compiler check: nothing stops somebody writing "requirements"
// or "load_case" into the table, and a kind nothing recognises would make the
// validator that reads it silently expect a node that can never exist.
//
// This package can see both vocabularies. It is the only place the two can be
// held together, which is why the fence is here and not next to the table.
func TestPackSchemaKindsAreRealKinds(t *testing.T) {
	for _, d := range pack.All() {
		if len(d.Schema) == 0 {
			// Legitimate: medical and robotics are not workable, so nothing is
			// expected of a project in them.
			if d.Available() {
				t.Errorf("the %s pack expects no node kinds at all, so nothing about a "+
					"project in it can ever be reported as incomplete", d.Pack)
			}
			continue
		}
		for _, k := range d.Schema {
			if _, err := workspace.KindOf(workspace.Kind(k)); err != nil {
				t.Errorf("the %s pack expects a %q node and that is not a kind: %v.\n"+
					"A validator reading this would wait for a node that cannot be created",
					d.Pack, k, err)
			}
		}
	}
}

// Every workable pack expects the two kinds that make a project reviewable.
//
// Requirements and constraints are what the graph is FOR — PRD RSN-01 asks for a
// model of goals, requirements and constraints kept separate from the transcript.
// A domain that expected neither would have a schema that asked nothing.
func TestEveryWorkablePackExpectsRequirementsAndConstraints(t *testing.T) {
	for _, d := range pack.All() {
		if !d.Available() {
			continue
		}
		has := map[string]bool{}
		for _, k := range d.Schema {
			has[k] = true
		}
		for _, want := range []string{"requirement", "constraint"} {
			if !has[want] {
				t.Errorf("the %s pack does not expect a %s node", d.Pack, want)
			}
		}
	}
}
