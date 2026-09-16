package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// Fences for the interference check. The kernel's half is fenced against the
// real kernel in internal/domain/cad/interference_kernel_test.go; these are
// about what the TURN does with what it is told.

func twoBoxes() *Prototype {
	return &geometry.Document{Name: "assembly", Units: "mm", Parts: []geometry.Part{
		{ID: "block", Name: "Engine Block", Shape: "box",
			Size:     map[string]float64{"width": 100, "height": 100, "depth": 100},
			Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}},
		{ID: "cylinder-body", Name: "Master Cylinder", Shape: "box",
			Size:     map[string]float64{"width": 10, "height": 10, "depth": 10},
			Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}},
	}}
}

func clash(fraction float64) []geometry.Interference {
	return []geometry.Interference{{
		A: "cylinder-body", B: "block", ALabel: "Master Cylinder", BLabel: "Engine Block",
		Volume: 1000 * fraction, Fraction: fraction,
	}}
}

// ‼️ A deployment with no kernel must not be told its model is clear.
//
// The findings only exist when the picture came from the kernel. A described
// render finds nothing because nobody looked, and reporting that as "no parts
// overlap" is the silent downgrade the fifth promise refuses — it is also
// exactly how this check would rot: it would go on passing everywhere after the
// kernel was switched off.
func TestInterference_ADescribedRenderClaimsNothing(t *testing.T) {
	stub := &repairStub{}
	c := &Conversation{client: stub}
	reply := &Reply{Prototype: twoBoxes()}
	// Findings present, but the picture is the DESCRIBED one.
	sheet := builtSheet{Image: "data:,", FromKernel: false, Interferences: clash(1.0)}

	c.repairIfPartsOverlap(context.Background(), reply, &sheet)

	if stub.calls != 0 {
		t.Errorf("a described render drove %d repair call(s); it knows nothing about "+
			"interference and must not act as though it does", stub.calls)
	}
	if reply.Repaired != "" {
		t.Errorf("a described render produced a note about interference: %q", reply.Repaired)
	}
}

// A real overlap that is not BURIED is reported and nothing is rewritten.
//
// A tyre a millimetre into its rim and two rails meeting at a weld are real
// shared material and are not defects. Rewriting the model because of them is
// how a checker that fires on correct answers damages them — this repository has
// already deleted one rule for that.
func TestInterference_AGrazeIsReportedAndNotRepaired(t *testing.T) {
	stub := &repairStub{}
	c := &Conversation{client: stub}
	reply := &Reply{Prototype: twoBoxes()}
	sheet := builtSheet{Image: "data:,", FromKernel: true, Interferences: clash(0.02)}

	c.repairIfPartsOverlap(context.Background(), reply, &sheet)

	if stub.calls != 0 {
		t.Errorf("a 2%% overlap drove %d repair call(s); only buried parts may", stub.calls)
	}
	if !strings.Contains(reply.Repaired, "Master Cylinder") {
		t.Errorf("a real overlap was not reported to the reader at all: %q", reply.Repaired)
	}
	if !strings.Contains(reply.Repaired, "nothing was moved") {
		t.Errorf("the note does not say the model was left alone: %q", reply.Repaired)
	}
}

// A buried part drives a repair, and the repair is judged by REBUILDING.
func TestInterference_ABuriedPartIsRepairedAndReverified(t *testing.T) {
	moved := twoBoxes()
	moved.Parts[1].Position = []float64{500, 0, 0}
	stub := &repairStub{reply: mustJSON(t, moved)}
	// The sheet handed in already carries the clash; what the stub answers is the
	// REBUILD after the repair, and it is clean.
	solids := &solidsStub{parts: []geometry.RenderPart{cube("block")}}
	c := (&Conversation{client: stub}).WithSolids(solids)

	reply := &Reply{Prototype: twoBoxes()}
	sheet := builtSheet{Image: "data:,", FromKernel: true, Interferences: clash(1.0)}

	c.repairIfPartsOverlap(context.Background(), reply, &sheet)

	if stub.calls != 1 {
		t.Fatalf("a fully buried part drove %d repair call(s), want 1", stub.calls)
	}
	if !strings.Contains(stub.asked, "inside") || !strings.Contains(stub.asked, "Master Cylinder") {
		t.Errorf("the repair request does not name the defect or the part:\n%.300s", stub.asked)
	}
	if got := reply.Prototype.Parts[1].Position[0]; got != 500 {
		t.Errorf("the accepted repair was not installed: part is at x=%v", got)
	}
	if !strings.Contains(reply.Repaired, "re-checked with the kernel") {
		t.Errorf("a successful repair did not say it was re-verified: %q", reply.Repaired)
	}
	if len(sheet.Interferences) != 0 {
		t.Error("the sheet still carries the old findings, so a later check would read a " +
			"document that no longer exists")
	}
}

// ‼️ A repair that does not actually help is REFUSED, and the reader is told.
//
// This is the guard that makes the whole check safe to run: the worst a false
// positive can cost is one wasted call, because the model is only replaced when
// the kernel says the problem got smaller.
func TestInterference_ARepairThatDoesNotHelpIsRefused(t *testing.T) {
	moved := twoBoxes()
	moved.Parts[1].Position = []float64{1, 0, 0}
	stub := &repairStub{reply: mustJSON(t, moved)}
	// The rebuild after the repair still finds it buried.
	solids := &solidsStub{parts: []geometry.RenderPart{cube("block")}, clashes: clash(1.0)}
	c := (&Conversation{client: stub}).WithSolids(solids)

	reply := &Reply{Prototype: twoBoxes()}
	before := reply.Prototype.Parts[1].Position[0]
	sheet := builtSheet{Image: "data:,", FromKernel: true, Interferences: clash(1.0)}

	c.repairIfPartsOverlap(context.Background(), reply, &sheet)

	if got := reply.Prototype.Parts[1].Position[0]; got != before {
		t.Errorf("a repair that did not reduce the interference was installed anyway: x=%v", got)
	}
	if !strings.Contains(reply.Repaired, "could not correct") {
		t.Errorf("a failed repair did not say so: %q", reply.Repaired)
	}
	if !strings.Contains(reply.Repaired, "Master Cylinder") {
		t.Errorf("the reader is not told which parts are in the same place: %q", reply.Repaired)
	}
}

// Nothing found means nothing said. A note on every clean turn is noise, and
// noise is how a real finding gets skipped over.
func TestInterference_ACleanModelIsSilent(t *testing.T) {
	stub := &repairStub{}
	c := &Conversation{client: stub}
	reply := &Reply{Prototype: twoBoxes()}
	sheet := builtSheet{Image: "data:,", FromKernel: true}

	c.repairIfPartsOverlap(context.Background(), reply, &sheet)

	if stub.calls != 0 || reply.Repaired != "" {
		t.Errorf("a clean model produced calls=%d note=%q", stub.calls, reply.Repaired)
	}
}
