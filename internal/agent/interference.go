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
	// ‼️ How much was checked is said whatever was found — including nothing.
	//
	// This used to return without a word when the list was empty, and a check the
	// pair budget stopped part-way returns an empty list too: a truncated check
	// read exactly like a clean one. Deferred, so it describes the sheet this
	// function ends with, which after a repair is the re-check's (Phase 5, V2).
	defer func() {
		if note := coverageNote(sheet); note != "" {
			reply.noteRepair(note)
		}
	}()
	found := sheet.Interferences
	if len(found) == 0 {
		return
	}
	problems := geometry.InterferenceProblems(found)
	if len(problems) == 0 {
		// Real overlaps, none of them buried. Said once, plainly, and nothing is
		// rewritten: these are the cases a concept model is allowed to have.
		reply.noteRepair("Some parts share material: " + list(found, sheet.Found) +
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
	reply.noteRepair("Parts are inside each other and FORGE could not correct it: " + list(found, sheet.Found))
}

// coverageNote says how much of the model the kernel's interference check looked
// at, or "" when it looked at all of it.
//
// Two ways a check covers less than the model, and both have to be said, because
// "no parts share material" is only true of the parts that were checked:
//   - the pair budget stopped it (Truncated): "checked X of Y";
//   - a part could not be built (Skipped): it was never in the check at all.
//
// Phase 5, stage V2 of docs/plan-2026-09-13-millions-of-parts.md: every check
// reports "checked X of Y", and a truncated check can never read as clean.
func coverageNote(sheet *builtSheet) string {
	var notes []string
	if sheet.Truncated {
		if sheet.Pairs > 0 {
			notes = append(notes, fmt.Sprintf("FORGE checked %d of %d pairs of parts that could touch "+
				"for shared material and stopped there, so the model is not known to be clear.",
				sheet.Checked, sheet.Pairs))
		} else {
			notes = append(notes, "FORGE stopped checking parts for shared material before it finished, "+
				"so the model is not known to be clear.")
		}
	}
	// ‼️ A list the kernel summarized says so. It is the worst of what was found,
	// not all of it, and "and N more" alone would count only the list (next scale
	// walls; cad.Build.InterferencesFound).
	if listed := len(sheet.Interferences); sheet.Found > listed {
		notes = append(notes, fmt.Sprintf("FORGE found %d pairs of parts sharing material and lists the %d "+
			"that share the most; the rest were counted, not listed.", sheet.Found, listed))
	}
	if n := len(sheet.Skipped); n > 0 {
		const most = 3
		named := sheet.Skipped
		more := ""
		if n > most {
			named, more = named[:most], fmt.Sprintf("; and %d more", n-most)
		}
		notes = append(notes, fmt.Sprintf("%d part(s) could not be built, so they were not checked for "+
			"shared material: %s%s.", n, strings.Join(named, "; "), more))
	}
	return strings.Join(notes, " ")
}

// list is the reader's sentence for a set of findings.
//
// Capped, because a broken assembly can produce dozens and a note nobody
// finishes reading is a note nobody reads. The worst are first — the kernel
// sorts by fraction — so a cap never hides the biggest one.
//
// total is how many were found, which is more than the list when the kernel
// summarized it; "and N more" counts from it, so a summarized list is never read
// as a short one.
func list(found []geometry.Interference, total int) string {
	const most = 3
	if total < len(found) {
		total = len(found)
	}
	parts := make([]string, 0, most+1)
	for i, f := range found {
		if i == most {
			break
		}
		parts = append(parts, f.Describe())
	}
	if more := total - len(parts); more > 0 {
		parts = append(parts, fmt.Sprintf("and %d more", more))
	}
	return strings.Join(parts, " ")
}
