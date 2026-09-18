package agent

import (
	"context"
	"testing"
)

// ‼️ A step reports the parts the model PLACES. Every step of the 2026-09-17 live car
// reported "parts=0", because a tree has no top-level parts and the count was
// len(doc.Parts); a build goal's outcome read "Step 3 of 8 (…): 0 part(s)" the same way.
// docs/spikes/2026-09-17-live-verification
func TestAssemble_AStepOfATreeReportsThePartsItPlaces(t *testing.T) {
	base := carOnSuspension() // two arms placed, no top-level parts
	step := brakesStep(`{"id":"left-disc","ref":"disc","position":[-800,0,1350]},`+
		`{"id":"right-disc","ref":"disc","position":[800,0,1350]}`, ``)
	stub := &scriptedStub{replies: []string{
		`{"steps":[{"name":"Brakes","what":"two brake discs","assembly":"brakes"},` +
			`{"name":"Wheels","what":"four wheels","assembly":"wheel"}]}`,
		step, `{"speech":"wheels","prototype_edit":{` + wheelPatch + `}}`,
	}}
	c := &Conversation{client: stub}
	var seen []BuildStep
	doc, notes, err := c.assemble(context.Background(), "a car", base,
		func(s BuildStep) error { seen = append(seen, s); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Parts) != 0 {
		t.Fatalf("the fence needs a model written as a tree; this one has %d top-level parts", len(doc.Parts))
	}
	want := doc.Occurrences()
	if want <= 2 {
		t.Fatalf("the brakes step placed nothing (the model places %d); the fence proves nothing. notes: %v", want, notes)
	}
	if len(seen) != 2 || seen[1].Parts != want || seen[0].Parts <= 2 {
		t.Fatalf("the step reported %+v; the model it kept places %d parts", seen, want)
	}
}
