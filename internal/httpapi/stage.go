package httpapi

import (
	"fmt"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/tools"
)

// The stage's panels — PRD WRK-01's "shared canvas" (code, CAD/EDA previews,
// diagrams, telemetry, requirements, diffs, simulations, test results).
//
// # Why this list lives in Go rather than in the page's script
//
// Two of these panels are empty in this build and will stay empty until somebody
// links a solver or an EDA kernel. That is a fact about the BUILD, and the build
// knows it in exactly one place: internal/tools, where the connectors that would
// do the work are declared and refused.
//
// A panel that explained its own emptiness in hand-written browser text would be
// a second copy of that fact, and the two would part company the first time one
// of them was edited — most likely in the direction where the page keeps saying
// "no solver here" after somebody attached one. So each unavailable panel NAMES
// the connectors whose absence empties it, and the words shown to the reader are
// the connectors' own (Contract.UnavailableReason), not a paraphrase.
// stageConnector resolves the names, and TestAnEmptyPanelNamesConnectorsThatAre
// ActuallyRefused fails if a named connector stops being unavailable.
//
// # Why it is rendered into the page rather than served from an endpoint
//
// Same reason persona.Soul is (see pageData.Soul): it is static text that
// changes when the build changes. An endpoint would be a public contract to
// maintain for something the page already has at render time.
//
// # Why the empty panels exist at all, rather than simply not being there
//
// This is tools.Unavailable's argument, one layer up. A panel that is absent
// reads as a feature nobody got to, and the reader fills the gap with a guess —
// usually a wrong one, usually generous. A panel that is present and says a
// solver was never linked cannot be misread, and it says so at the moment
// somebody went looking for a simulation, which is the moment it matters.

// StagePanel is one view of the shared canvas.
type StagePanel struct {
	// ID is the panel's handle in the DOM and in the script: tab-<id> and
	// panel-<id>.
	ID string
	// Label is the tab's text. Short: five of these sit on one row.
	Label string
	// Gloss is one line saying what the panel shows, under the tab strip.
	Gloss string
	// Available is whether this build can fill the panel at all.
	Available bool
	// Reason is why it cannot, in the panel's own terms — what is missing and
	// what a person can do instead. Empty when Available.
	Reason string
	// Refused are the declared-and-unavailable connectors behind this panel,
	// each with the reason it gives for itself.
	Refused []RefusedConnector
}

// RefusedConnector is a declared capability with no backend here.
type RefusedConnector struct {
	Name   string
	Reason string
}

// stagePanels is what the workbench offers, in tab order.
//
// Model is first because it is the product's subject (PRD §1.2), and it is the
// panel the workbench opens on.
func stagePanels() []StagePanel {
	return []StagePanel{
		{
			ID: "model", Label: "Model", Available: true,
			Gloss: "The prototype and what qualifies it.",
		},
		{
			ID: "files", Label: "Files", Available: true,
			Gloss: "Every file, every version, every diff.",
		},
		{
			ID: "checks", Label: "Checks", Available: true,
			Gloss: "What a machine found. What a person decided.",
		},
		{
			ID: "eda", Label: "EDA", Available: false,
			Gloss: "Schematics, netlists, board layouts.",
			Reason: "There is no electrical design here to preview. Nothing in this build " +
				"produces a schematic, a netlist or a board, and nothing can read one: the " +
				"workspace holds files, documents, models, drawings, datasets and reports, and " +
				"none of those is a circuit.\n\n" +
				"Draw it in the native EDA tool. FORGE can hold an exported document and reason " +
				"about what you tell it is in there — and it will say that is what it is doing, " +
				"because reading your description of a circuit is not the same as reading the " +
				"circuit.",
			Refused: stageConnectors("spice_simulate"),
		},
		{
			ID: "sim", Label: "Simulation", Available: false,
			Gloss: "Stresses, displacements, node voltages, timing.",
			Reason: "No solver of any kind is linked into this build, so there are no results to " +
				"show — and there is deliberately no path by which an estimate could appear here " +
				"looking like one. A number produced without a solver is not an analysis, and this " +
				"panel showing one would be the single most dangerous thing this system could do " +
				"(PRD RSN-06).\n\n" +
				"Run the analysis in a qualified tool. The result can be recorded against a " +
				"version as evidence, where it will carry the method that produced it.",
			Refused: stageConnectors("fea_solve", "spice_simulate"),
		},
	}
}

// stageConnectors resolves connector names to what they say about themselves.
//
// Panics on an unknown name, at process start rather than in a request: a panel
// citing a connector that does not exist is a panel whose explanation is empty,
// and discovering that from a blank space on a page is discovering it too late.
func stageConnectors(names ...string) []RefusedConnector {
	declared := map[string]tools.Contract{}
	for _, t := range tools.StandardUnavailableConnectors() {
		c := t.Contract()
		declared[c.Name] = c
	}
	out := make([]RefusedConnector, 0, len(names))
	for _, n := range names {
		c, ok := declared[n]
		if !ok {
			panic(fmt.Sprintf("stage panel names connector %q, which is not declared in "+
				"tools.StandardUnavailableConnectors()", n))
		}
		out = append(out, RefusedConnector{Name: c.Name, Reason: c.UnavailableReason})
	}
	return out
}
