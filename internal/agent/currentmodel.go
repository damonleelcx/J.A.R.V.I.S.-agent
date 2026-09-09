package agent

import (
	"encoding/json"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// What the model on screen actually is, for the turn that revises it.
//
// # The problem this solves
//
// A revision was a full rewrite from a names-only summary. The agent was told
// "Sports Car Concept — 6 part(s): Main Body [id: chassis-body], … (units: mm)"
// and nothing else: no dimension, no position, no profile, no feature. To add a
// spoiler it had to re-emit all six parts, and every number came from RECALL of
// its own earlier prose rather than from the document it was revising.
//
// So the parts nobody asked about drifted. Ask for a spoiler and the wheelbase
// could change, silently, because nothing compared the new numbers with the old
// ones. Removal had the mirror problem: a part left out by accident and a part
// removed on purpose produce the same document.
//
// # Why the document rather than a prettier summary
//
// This is the schema the agent already emits. Handing back exactly what it
// writes is the least confusing representation available, and it needs no
// second renderer that could describe the model differently from the way the
// model is stored — which is how the on-screen note and the document would drift
// apart the first time either changed.
//
// # Why the server builds it and the page does not
//
// The page used to send the conversation history and no longer does: the server
// builds it from the record it wrote itself, so a client cannot put words in
// FORGE's mouth. The same reasoning applies with more force here — a page that
// supplied the current geometry could tell FORGE it had built something it never
// built, and the reply would be a revision of a fiction. The page says nothing
// about this; the server reads its own record.
//
// # What is left out, and why
//
// Assumptions, not-verified notes and overlays are prose about the design rather
// than the design, and the agent restates them each turn anyway. Parameters and
// derived values ARE included: a dimension bound to `plate_size` cannot be
// revised by an agent that cannot see that `plate_size` exists.
type currentModel struct {
	Name       string               `json:"name"`
	Units      string               `json:"units"`
	Parameters []geometry.Parameter `json:"parameters,omitempty"`
	Derived    []geometry.Derived   `json:"derived,omitempty"`
	Parts      []geometry.Part      `json:"parts"`
	Features   []geometry.Feature   `json:"features,omitempty"`
}

// CurrentModel renders the model being revised, or "" when there is none.
//
// Empty for the first turn of a project, which is correct: there is nothing on
// screen to revise and the agent is being asked to propose rather than change.
func CurrentModel(d *Prototype) string {
	if d == nil || len(d.Parts) == 0 {
		return ""
	}
	body, err := json.Marshal(currentModel{
		Name: d.Name, Units: d.Units,
		Parameters: d.Parameters, Derived: d.Derived,
		Parts: d.Parts, Features: d.Features,
	})
	if err != nil {
		// Never fatal: a turn without this context is the turn that shipped
		// before it existed. The agent proposes from the conversation instead
		// of revising from the record, which is worse and still works.
		return ""
	}
	return string(body)
}
