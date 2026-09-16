package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// Parts occupying the same material.
//
// # The gap this closes, and the measurement that found it
//
// A live car build on 2026-09-12 came back with 28 parts, document faults ZERO,
// a clean kernel build and a passing visual check — and the master cylinder
// entirely inside the engine block, the uprights inside the brake rotors. No
// check in FORGE could see any of it. geometry/assembly.go had said so in plain
// words since it was written: no interference test, no clearance, no kinematics.
// docs/spikes/2026-09-12-car-ceiling/README.md
//
// This is the first check here that is GEOMETRIC rather than visual, and it is
// the only kind that scales. A contact sheet at four hundred parts is a grey
// smudge, and material shared inside a solid was never visible in a picture at
// any part count — the vision model passed this car.
//
// # Why it costs no model call
//
// The numbers arrive from the kernel build that the render already runs (see
// render.go, Built). So the check itself is free, and only a repair costs a
// call. That is the opposite of every other check in this turn and is the reason
// it sits so early in what should be built next.
//
// # Why only the buried ones drive a repair
//
// A concept assembly has legitimate shared material: a tyre modelled a
// millimetre into its rim, two rails meeting at a weld. Reporting those is
// right; rewriting the model because of them is not. geometry.BuriedFraction is
// the line, and the reasoning for it is in interference.go beside the constant.
// Everything under it is reported to the reader and changes nothing.

// repairIfPartsOverlap asks for a fix when parts share material, and says so
// either way.
//
// ‼️ It reads the sheet's findings, which are meaningful ONLY when the picture
// came from the kernel. A deployment with no kernel finds nothing here and must
// not be told its model is clear — that is the silent downgrade the fifth
// promise refuses, so this returns without a word rather than with a reassuring
// one.
func (c *Conversation) repairIfPartsOverlap(ctx context.Context, reply *Reply, sheet *builtSheet) {
	if reply == nil || reply.Prototype == nil || sheet == nil || !sheet.FromKernel {
		return
	}
	found := sheet.Interferences
	if len(found) == 0 {
		return
	}
	problems := geometry.InterferenceProblems(found)
	if len(problems) == 0 {
		// Real overlaps, none of them buried. Said once, plainly, and nothing is
		// rewritten: these are the cases a concept model is allowed to have.
		reply.noteRepair("Some parts share material: " + list(found) +
			" That can be deliberate at this stage, so nothing was moved.")
		return
	}

	if fixed := c.repairGeometry(ctx, reply.Prototype, problems); fixed != nil {
		// Re-built, not re-read: the question "is it still inside" can only be
		// answered by the kernel, and a repair that claimed success against the
		// OLD numbers would be a check that cannot fail. Same reason look.go
		// re-renders after a repair it accepts.
		after := c.render(ctx, fixed)
		if after.FromKernel &&
			len(fixed.Faults()) <= len(reply.Prototype.Faults()) &&
			len(geometry.InterferenceProblems(after.Interferences)) < len(problems) {
			reply.Prototype = fixed
			*sheet = after
			remaining := geometry.InterferenceProblems(after.Interferences)
			if len(remaining) == 0 {
				reply.noteRepair("Parts were sitting inside each other. FORGE moved them apart " +
					"and re-checked with the kernel.")
				return
			}
			problems = remaining
			found = after.Interferences
		}
	}

	// Still wrong: say it, with the parts named. A reader looking at a model that
	// builds, reports no faults and passes the visual check has no other way to
	// learn that two of its parts are in the same place.
	reply.noteRepair("Parts are inside each other and FORGE could not correct it: " + list(found))
}

// list is the reader's sentence for a set of findings.
//
// Capped, because a broken assembly can produce dozens and a note nobody
// finishes reading is a note nobody reads. The worst are first — the kernel
// sorts by fraction — so a cap never hides the biggest one.
func list(found []geometry.Interference) string {
	const most = 3
	parts := make([]string, 0, most)
	for i, f := range found {
		if i == most {
			parts = append(parts, fmt.Sprintf("and %d more", len(found)-most))
			break
		}
		parts = append(parts, f.Describe())
	}
	return strings.Join(parts, " ")
}
