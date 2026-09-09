package agent

import (
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	domainpack "github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/pack"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
)

// The model on screen reaches the prompt, and beats the page's summary of it.
//
// # Why this is a separate fence from the renderer's
//
// CurrentModel can be perfect and never be called. That is exactly the shape of
// the bug this closes: the note the agent received was produced correctly, by
// the PAGE, and carried only names — so a revision retyped every dimension from
// recall. A renderer with no producer changes nothing.
//
// # Why the page's note must lose
//
// The page used to send the conversation history and no longer does: the server
// builds it from its own record so a client cannot put words in FORGE's mouth.
// A page that could still supply the geometry would be the same hole one layer
// down — it could describe a model that was never built, and the reply would be
// a revision of a fiction that then gets saved as the next version.
func TestBuildMessages_PrefersTheRecordOverThePagesSummary(t *testing.T) {
	c := &Conversation{}
	current := &geometry.Document{
		Name: "Bracket", Units: "mm",
		Parts: []geometry.Part{{ID: "plate", Name: "Plate", Shape: "box",
			Size: map[string]float64{"width": 60, "height": 6, "depth": 60}}},
	}

	built := c.buildMessages(persona.DefaultCharacter(), domainpack.Definition{}, nil,
		"add a rib", "A PAGE-SUPPLIED CLAIM ABOUT WHAT IS ON SCREEN", current, nil)
	last := built[len(built)-1].Content

	if !strings.Contains(last, `"width":60`) {
		t.Errorf("the prompt carries no dimensions, so a revision restates them from recall.\ngot: %s", last)
	}
	if !strings.Contains(last, "copy every other dimension EXACTLY") {
		t.Error("the prompt does not tell the agent to copy the dimensions it was given. " +
			"Showing it the numbers and not saying what to do with them leaves the drift in place.")
	}
	if strings.Contains(last, "A PAGE-SUPPLIED CLAIM ABOUT WHAT IS ON SCREEN") {
		t.Error("the page's own claim about the model reached the prompt alongside the record. " +
			"A client that can describe the geometry can describe one that was never built, " +
			"and the reply would be a revision of a fiction.")
	}
	if !strings.Contains(last, "add a rib") {
		t.Error("the person's actual message is missing from their own turn")
	}
}

// With no record, the page's summary is still better than nothing.
//
// A deployment with no database keeps geometry only for the life of a turn, so
// there is no record to read and the page's note is the only thing that knows
// what is on screen. Dropping it there would make revision worse than it was.
func TestBuildMessages_FallsBackToThePageWhenThereIsNoRecord(t *testing.T) {
	c := &Conversation{}
	built := c.buildMessages(persona.DefaultCharacter(), domainpack.Definition{}, nil,
		"add a rib", "a bracket is on screen", nil, nil)
	last := built[len(built)-1].Content

	if !strings.Contains(last, "a bracket is on screen") {
		t.Error("with no stored model the page's note was dropped too, so the agent is told " +
			"nothing at all about what it is revising")
	}
}
