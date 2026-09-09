package agent_test

import (
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/agent"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// The agent is shown the NUMBERS, not just the names.
//
// # What this closes
//
// A revision used to be a full rewrite from a names-only summary: the agent was
// told "Sports Car Concept — 6 part(s): Main Body [id: chassis-body] … (units:
// mm)" and nothing more. To add one part it had to re-emit all six, and every
// dimension came from RECALL of its own earlier prose rather than from the
// document it was revising — so parts nobody asked about drifted, silently.
//
// The properties that matter are therefore not "a string is produced". They are
// that the dimensions, positions, parameters and features are all in it, because
// each one is a thing a revision would otherwise have to invent.
func TestCurrentModel_CarriesEverythingARevisionMustNotInvent(t *testing.T) {
	doc := &geometry.Document{
		Name: "Bracket", Units: "mm",
		Parameters: []geometry.Parameter{{Name: "plate_size", Value: 60, How: "chosen"}},
		Parts: []geometry.Part{
			{ID: "plate", Name: "Plate", Shape: "box",
				Size:     map[string]float64{"width": 60, "height": 6, "depth": 60},
				Position: []float64{0, 0, 0}, SizeFrom: map[string]string{"width": "plate_size"}},
			{ID: "bore", Name: "Bore", Shape: "cylinder",
				Size:     map[string]float64{"radius": 8, "height": 40},
				Position: []float64{0, 0, 0}},
		},
		Features: []geometry.Feature{{ID: "drill", Op: "cut", Of: "plate", With: []string{"bore"}}},
	}

	got := agent.CurrentModel(doc)
	if got == "" {
		t.Fatal("a document with parts produced no note, so the agent revises from recall")
	}
	for _, want := range []struct{ text, why string }{
		{`"plate"`, "the part id, which the agent is told to reuse"},
		{`"width":60`, "a DIMENSION — without it the agent retypes 60 from memory and may not"},
		{`"height":6`, "the plate thickness"},
		{`"radius":8`, "the bore radius, which decides whether the hole still fits"},
		{`"plate_size"`, "the parameter a dimension is bound to; a revision cannot keep a binding it cannot see"},
		{`"drill"`, "the feature id"},
		{`"cut"`, "the operation — a revision that dropped it would fill the hole back in"},
		{`"mm"`, "the unit, without which every number above is unitless"},
	} {
		if !strings.Contains(got, want.text) {
			t.Errorf("the note omits %s (%s).\ngot: %s", want.text, want.why, got)
		}
	}
}

// Nothing on screen is a normal answer, not an empty document.
//
// The first turn of a project has nothing to revise. Emitting an empty skeleton
// would tell the agent it is editing a model with no parts, which is a different
// and wrong instruction from "propose something".
func TestCurrentModel_IsEmptyWhenThereIsNothingToRevise(t *testing.T) {
	if got := agent.CurrentModel(nil); got != "" {
		t.Errorf("no model produced %q; the first turn has nothing to revise", got)
	}
	if got := agent.CurrentModel(&geometry.Document{Name: "empty", Units: "mm"}); got != "" {
		t.Errorf("a document with no parts produced %q; there is still nothing to revise", got)
	}
}
