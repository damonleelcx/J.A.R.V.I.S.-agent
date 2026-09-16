package agent

import (
	"context"
	"strings"
	"testing"
)

// How much of the model the interference check covered reaches the turn. Phase 5,
// stage V2 of docs/plan-2026-09-13-millions-of-parts.md: "a truncated check can
// never read as clean".

// ‼️ The case this stage exists for: the budget stopped the check and it found
// nothing. repairIfPartsOverlap used to return without a word, so this turn read
// exactly like one whose model had been checked in full and was clear.
func TestInterference_ATruncatedCheckSaysSoInTheTurn(t *testing.T) {
	for _, tc := range []struct {
		name  string
		sheet builtSheet
		also  string
	}{
		{"nothing found", builtSheet{Image: "data:,", FromKernel: true, Truncated: true, Checked: 2000, Pairs: 5000}, ""},
		{"a graze found", builtSheet{Image: "data:,", FromKernel: true, Truncated: true, Checked: 2000, Pairs: 5000,
			Interferences: clash(0.02)}, "share material"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &repairStub{}
			c := &Conversation{client: stub}
			reply := &Reply{Prototype: twoBoxes()}
			sheet := tc.sheet

			c.repairIfPartsOverlap(context.Background(), reply, &sheet)

			if !strings.Contains(reply.Repaired, "checked 2000 of 5000") {
				t.Errorf("a check stopped after 2000 of 5000 pairs said: %q", reply.Repaired)
			}
			if tc.also != "" && !strings.Contains(reply.Repaired, tc.also) {
				t.Errorf("the finding itself was lost beside the coverage note: %q", reply.Repaired)
			}
			if stub.calls != 0 {
				t.Errorf("truncation alone drove %d repair call(s); an unchecked pair is not a defect", stub.calls)
			}
		})
	}
}

// A part the kernel could not build was never in the check, and is named.
func TestInterference_APartThatWasNotBuiltIsNamedAsUnchecked(t *testing.T) {
	c := &Conversation{client: &repairStub{}}
	reply := &Reply{Prototype: twoBoxes()}
	sheet := builtSheet{Image: "data:,", FromKernel: true, Checked: 1, Pairs: 1,
		Skipped: []string{"Bracket: Standard_Failure", "Gusset: no volume", "Rib: BRep_API", "Web: bad outline"}}

	c.repairIfPartsOverlap(context.Background(), reply, &sheet)

	for _, want := range []string{"4 part(s) could not be built", "Bracket", "Rib", "and 1 more"} {
		if !strings.Contains(reply.Repaired, want) {
			t.Errorf("the note does not say %q: %q", want, reply.Repaired)
		}
	}
}

// A check that covered the whole model adds nothing: a note on every clean turn is
// a note nobody reads.
func TestInterference_ACompleteCheckAddsNoCoverageNote(t *testing.T) {
	c := &Conversation{client: &repairStub{}}
	reply := &Reply{Prototype: twoBoxes()}
	sheet := builtSheet{Image: "data:,", FromKernel: true, Checked: 12, Pairs: 12}

	c.repairIfPartsOverlap(context.Background(), reply, &sheet)

	if reply.Repaired != "" {
		t.Errorf("a complete, clean check produced a note: %q", reply.Repaired)
	}
}

// A described render knows nothing about coverage either, and says nothing.
func TestInterference_ADescribedRenderSaysNothingAboutCoverage(t *testing.T) {
	c := &Conversation{client: &repairStub{}}
	reply := &Reply{Prototype: twoBoxes()}
	sheet := builtSheet{Image: "data:,", FromKernel: false, Truncated: true, Checked: 1, Pairs: 9,
		Skipped: []string{"Bracket: nope"}}

	c.repairIfPartsOverlap(context.Background(), reply, &sheet)

	if reply.Repaired != "" {
		t.Errorf("a described render produced a coverage note: %q", reply.Repaired)
	}
}

// ‼️ A list the kernel cut to its bound says how many were found (next scale walls).
//
// The kernel lists at most the worst 10,000 clashes and counts all of them. Read by
// its length, a 1M airframe's 1,760,000 clashes would read as 10,000 — and a sheet
// listing one would say "and 0 more". Every pair was checked, so it must not say the
// model is not known to be clear either.
func TestInterference_ASummarizedListSaysHowManyWereFound(t *testing.T) {
	stub := &repairStub{}
	c := &Conversation{client: stub}
	reply := &Reply{Prototype: twoBoxes()}
	sheet := builtSheet{Image: "data:,", FromKernel: true, Checked: 11175, Pairs: 11175, Found: 11175,
		Interferences: clash(0.02)}

	c.repairIfPartsOverlap(context.Background(), reply, &sheet)

	for _, want := range []string{"found 11175 pairs", "lists the 1 that share the most", "and 11174 more"} {
		if !strings.Contains(reply.Repaired, want) {
			t.Errorf("a list of 1 of 11,175 clashes did not say %q: %q", want, reply.Repaired)
		}
	}
	if strings.Contains(reply.Repaired, "not known to be clear") {
		t.Errorf("a whole check whose list was summarized read as a truncated one: %q", reply.Repaired)
	}
	if stub.calls != 0 {
		t.Errorf("a graze drove %d repair call(s)", stub.calls)
	}

	whole := &Reply{Prototype: twoBoxes()}
	all := builtSheet{Image: "data:,", FromKernel: true, Checked: 1, Pairs: 1, Found: 1, Interferences: clash(0.02)}
	c.repairIfPartsOverlap(context.Background(), whole, &all)
	if strings.Contains(whole.Repaired, "lists the") || strings.Contains(whole.Repaired, "more") {
		t.Errorf("a whole list was described as a summary: %q", whole.Repaired)
	}
}
