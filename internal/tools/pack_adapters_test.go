package tools

import (
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/pack"
)

// A pack's required adapters are adapters this build knows about.
//
// # Why this fence lives HERE
//
// `pack.Definition.Adapters` holds plain strings so the pack table stays a leaf.
// Nothing else checks them, and a name that matches no tool would produce a
// refusal naming a solver that does not exist even as a concept here — worse
// than the generic refusal it replaced, because it sends somebody looking for a
// dependency to install.
//
// Every adapter named is expected to be UNAVAILABLE. That is not a defect: the
// spike that examined integrating a real kernel recommended against it, and the
// PRD carve-out says a fabricated solver result is the most dangerous output
// this system could produce. The pack states what the domain needs; this build
// states that it has none. Both facts are true and neither one implies the other.
func TestPackAdaptersNameRealTools(t *testing.T) {
	known := map[string]Contract{}
	for _, tool := range StandardUnavailableConnectors() {
		known[tool.Contract().Name] = tool.Contract()
	}
	if len(known) == 0 {
		t.Fatal("no connectors are declared, so this fence checks nothing")
	}

	for _, d := range pack.All() {
		for _, name := range d.Adapters {
			if _, ok := known[name]; !ok {
				t.Errorf("the %s pack requires the adapter %q and no tool by that name exists.\n"+
					"A refusal naming it would send somebody looking for a dependency to "+
					"install, when the truth is that this build has no such connector at all",
					d.Pack, name)
			}
		}
	}
}

// Nothing a pack requires is available, and the packs that need most have most.
//
// The second half is the one that would rot quietly: a table where every domain
// required nothing would pass the fence above and say nothing about any of them.
func TestPackAdaptersAreUnavailableAndNotEmptyEverywhere(t *testing.T) {
	unavailable := map[string]bool{}
	for _, tool := range StandardUnavailableConnectors() {
		if _, isUnavailable := tool.(*Unavailable); isUnavailable {
			unavailable[tool.Contract().Name] = true
		}
	}
	declared := 0
	for _, d := range pack.All() {
		for _, name := range d.Adapters {
			declared++
			if !unavailable[name] {
				t.Errorf("the %s pack requires %q and this build claims to HAVE it. "+
					"If a real backend has landed, the pack's ceiling and its Requires "+
					"text both need revisiting", d.Pack, name)
			}
		}
	}
	if declared == 0 {
		t.Fatal("no pack requires any adapter, so no refusal can ever say which " +
			"solver a domain needs — the field exists and states nothing")
	}
	// The domains whose work is inherently analytical must name a solver.
	for _, name := range []string{"civil", "aerospace", "automotive", "mechanical"} {
		d, _ := pack.Lookup(name)
		if len(d.Adapters) == 0 {
			t.Errorf("%s requires no adapter at all, which is not true of that domain", name)
		}
	}
}
