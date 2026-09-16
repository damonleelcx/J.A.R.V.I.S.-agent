package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	domainpack "github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/pack"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
)

// The model of an ordinary turn is told what the repetition check found in the
// document it is revising. Phase 2, stage A3; closed 2026-09-15.
//
// # The problem this closes
//
// A3 told the PERSON (a notice) and a BUILD STEP's model (its prompt). The model of
// the next conversational turn — the one that actually revises the forty children —
// was shown the document and nothing about it, so it wrote them out again.

func TestBuildMessages_ATurnIsToldWhatCouldBeOnePattern(t *testing.T) {
	c := &Conversation{}
	const asked = "add a nut under each screw"
	turn := func(current *Prototype) string {
		built := c.buildMessages(persona.DefaultCharacter(), domainpack.Definition{}, nil, asked, "", current, nil)
		return built[len(built)-1].Content
	}

	// Five screws written out one by one: told, with the pattern, after the document
	// and before what the person asked.
	repetitive := turn(enumeratedPlate())
	note, model, message := strings.Index(repetitive, plateSuggestion), strings.Index(repetitive, `"assemblies"`), strings.Index(repetitive, asked)
	if note < 0 {
		t.Fatalf("a turn revising five screws written out one by one is not told they could be one pattern:\n%s", repetitive)
	}
	if model < 0 || model > note || message < note {
		t.Errorf("the note is not between the document it is about and the person's message (document %d, note %d, message %d):\n%s",
			model, note, message, repetitive)
	}

	// The same screws as one patterned child: told nothing.
	patterned := enumeratedPlate()
	patterned.Assemblies[0].Children = []geometry.Child{{ID: "screw", Ref: "screw",
		Pattern: &geometry.Pattern{Kind: "linear", Count: 5, Offset: []float64{30, 0, 0}}}}
	if clean := turn(patterned); strings.Contains(clean, "FORGE's check found") || strings.Contains(clean, "could be one pattern") {
		t.Errorf("a turn revising a document with nothing written out is told there is:\n%s", clean)
	}

	// A flat document's parts: told the repeat.
	flat := &Prototype{Name: "Rail", Units: "mm", NotVerified: []string{"a concept, not an analysis"}}
	for i := 0; i < 5; i++ {
		flat.Parts = append(flat.Parts, geometry.Part{ID: fmt.Sprintf("post-%d", i), Name: "Post", Shape: "box",
			Size: map[string]float64{"width": 4, "height": 40, "depth": 4}, Position: []float64{float64(i) * 50, 20, 0},
			Rotation: []float64{0, 0, 0}})
	}
	if got := turn(flat); !strings.Contains(got, `"repeat": {"count":5,"offset":[50,0,0]}`) {
		t.Errorf("a turn revising five posts written out one by one is not told the repeat that places them:\n%s", got)
	}

	// Bounded: nine runs are five sentences and a count of the rest.
	many := enumeratedPlate()
	for i := 0; i < 8; i++ {
		a := many.Assemblies[0]
		a.ID = fmt.Sprintf("plate-%d", i)
		many.Assemblies = append(many.Assemblies, a)
	}
	got := turn(many)
	if n := strings.Count(got, `places "screw" 5 times`); n != maxRepetitionNotes || !strings.Contains(got, "4 more run(s)") {
		t.Errorf("nine runs were told as %d sentences (want %d and a count of the other 4):\n%s", n, maxRepetitionNotes, got)
	}
}
