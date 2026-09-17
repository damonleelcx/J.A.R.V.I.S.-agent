package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// The literal-position note reads a coordinate to the digits it was written with
// (2026-09-17). Before, it matched a parameter's value exactly, so a model that rounded
// a derived value such as 2 * half_wheelbase / 7 = 385.714286 to 385.71 or 386 was
// never told it had typed a parameter.

// ‼️ Rounded values are named, with the value the parameter holds and each typed number,
// deterministically; a number off by more than its own last digit, a coarse number near
// a small value, and a rounded number a parameter holds exactly are not read as rounding.
func TestAssemble_AStepThatRoundsAParametersValueIsToldWhichParameter(t *testing.T) {
	withSeventh := func() *Prototype {
		d := carOnSuspension()
		d.Derived = []geometry.Derived{{Name: "seventh", Expression: "2 * half_wheelbase / 7", Why: "a seventh of the wheelbase"}}
		return d
	}
	step := func(d *Prototype, reply string) string {
		t.Helper()
		c := &Conversation{client: &scriptedStub{replies: []string{reply}}}
		next, note := c.buildOneStep(context.Background(), d, "a car", buildTask{Name: "Brakes", What: "brakes", Assembly: "brakes"}, 4, 5)
		if next == nil {
			t.Fatalf("the step was refused: %q", note)
		}
		return note
	}

	rounded := brakesStep(`{"id":"left-disc","ref":"disc","position":[-385.71,0,386]}`, `{"id":"caliper-mount","position":[0,0,385.7]}`)
	want := `This step typed 3 position(s) as the number a parameter already holds to the digits written: ` +
		`385.714286 is seventh (brakes/left-disc x = -385.71, brakes/left-disc z = 386, brakes interface caliper-mount z = 385.7). ` +
		`Write the parameter's name so the position follows it: "position_from": {"x": "-seventh"} on a part or a definition, ` +
		`and "-seventh" in a child's or an interface's "position", which FORGE keeps bound to the parameters.`
	first := step(withSeventh(), rounded)
	if !strings.Contains(first, want) {
		t.Errorf("the note does not name the rounded values:\n got %s\nwant %s", first, want)
	}
	for i := 0; i < 4; i++ {
		if again := step(withSeventh(), rounded); again != first {
			t.Errorf("the same step said two different things:\n%s\n%s", first, again)
		}
	}

	// An exact value is named before a rounded one, and the exact wording is kept.
	withSpacer := withSeventh()
	withSpacer.Parameters = append(withSpacer.Parameters, geometry.Parameter{Name: "spacer", Value: 386, Unit: "mm", How: "chosen"})
	exact := brakesStep(`{"id":"left-disc","ref":"disc","position":[386,386,386]}`, "")
	if note := step(withSpacer, exact); !strings.Contains(note, "as the number a parameter already holds: 386 is spacer (") {
		t.Errorf("a value a parameter holds exactly was read as another value rounded:\n%s", note)
	}

	withLip := func() *Prototype {
		d := carOnSuspension()
		d.Parameters = append(d.Parameters, geometry.Parameter{Name: "lip", Value: 12.34, Unit: "mm", How: "chosen"})
		return d
	}
	for _, tc := range []struct {
		name, reply string
		doc         *Prototype
	}{
		{"off by more than the last digit written", brakesStep(`{"id":"l","ref":"disc","position":[385.72,385.72,385.72]}`, ""), withSeventh()},
		{"an integer near a value more than 200 times its rounding", brakesStep(`{"id":"l","ref":"disc","position":[12,12,12]}`, ""), withLip()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if note := step(tc.doc, tc.reply); strings.Contains(note, "as the number a parameter already holds") {
				t.Errorf("the step was told it retyped a parameter:\n%s", note)
			}
		})
	}
	// The same lip written to its own digits is still read.
	if note := step(withLip(), brakesStep(`{"id":"l","ref":"disc","position":[12.3,12.3,12.3]}`, "")); !strings.Contains(note, "12.34 is lip (") {
		t.Errorf("12.3 is the 12.34 mm lip to the digits written, and was not named:\n%s", note)
	}
}
