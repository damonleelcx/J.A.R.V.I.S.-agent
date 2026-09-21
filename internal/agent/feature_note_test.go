package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// Looks designed, stage B1: a fillet the kernel built smaller than asked, or on
// only some of its edges, is said in the turn — carried from the build onto the
// sheet, and from the sheet into the reply. So is a feature it could not apply.
func TestTurn_SaysWhichRoundsTheKernelBuiltSmaller(t *testing.T) {
	reduced := "corners: the fillet of 45 mm did not build on all 4 edge group(s), so FORGE rounded " +
		"what it could and says where — 1 edge near (30, 0, 30) at 22.5 mm (0.5× the 45 mm asked for)"
	failed := "fin-round: no fillet of 10, 5, 2.5 mm builds on any of these 1 edge group(s)"
	c := &Conversation{client: &repairStub{}, solids: coverageSolids{built: Built{
		Parts: []geometry.RenderPart{cube("plate")}, Checked: 1, Pairs: 1,
		FeatureReductions: []string{reduced}, FeatureFailures: []string{failed},
	}}}

	sheet := c.render(context.Background(), drilledPlate())
	if len(sheet.FeatureReductions) != 1 || len(sheet.FeatureFailures) != 1 {
		t.Fatalf("the render dropped what the kernel said about the features: %+v / %+v",
			sheet.FeatureReductions, sheet.FeatureFailures)
	}

	reply := &Reply{Prototype: twoBoxes()}
	c.repairIfPartsOverlap(context.Background(), reply, &sheet)
	for _, want := range []string{"1 fillet(s) or chamfer(s) did not fit as written", "22.5 mm (0.5×",
		"1 feature(s) could not be applied at all", "fin-round"} {
		if !strings.Contains(reply.Repaired, want) {
			t.Errorf("the turn does not say %q: %q", want, reply.Repaired)
		}
	}
}

// A build whose features all went in as written adds nothing.
func TestTurn_SaysNothingAboutRoundsThatFit(t *testing.T) {
	c := &Conversation{client: &repairStub{}}
	reply := &Reply{Prototype: twoBoxes()}
	sheet := builtSheet{Image: "data:,", FromKernel: true, Checked: 1, Pairs: 1}
	c.repairIfPartsOverlap(context.Background(), reply, &sheet)
	if reply.Repaired != "" {
		t.Errorf("a build with every feature applied produced a note: %q", reply.Repaired)
	}
}
