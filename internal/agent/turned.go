package agent

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// resizeTolerance is how much a part may change size in a revision before it
// stops being a restyle. Deliberately generous: a rounder body really is a bit
// smaller, a taller cabin really is taller, and a check that fires on those
// would be turned off. What it catches is a part that came back a DIFFERENT
// SIZE OF THING — measured, the failures are 2.4x.
const resizeTolerance = 1.5

// turnedOnItsSide reports parts a revision resized when it was asked to restyle.
//
// # The failure this answers
//
// An outline is drawn facing the viewer: its own x is the assembly's X, its own
// y is Y, and the shape travels along Z. A car's SIDE ELEVATION — the roofline,
// the taper, everything "less boxy" means — is 4500mm across the page, so
// drawing it as an outline puts the car's LENGTH on the X axis and the model
// comes back 4500 wide and 1900 long. Length and width swapped. The reply says
// the body was reshaped and what is on screen is a slab.
//
// # Why this is a check and not another paragraph of contract
//
// It was three paragraphs of contract first, and they were measured. Against
// qwen3.7-plus on the live car, 10 runs each:
//
//	no guidance                          2/10 turned the body on its side
//	"the outline is the cross-section"   3/10
//	"an outline is drawn facing you",
//	  with the fix for both shapes       2/10
//
// Not one of them beat saying nothing. And the two survivors of the last
// attempt had FOLLOWED it: one used the rotation it was told to use and left
// the depth at the length; the other sent the path along X as instructed and
// put the 4500 along it. The model obeys the letter and lands somewhere else,
// which is what an axis convention does to prose. So it is checked instead, and
// the model is told the numbers rather than the rule — which is the same reason
// georepair.go hands back the builder's own words rather than a lecture.
func turnedOnItsSide(before, after *Prototype) []geometry.Problem {
	if before == nil || after == nil {
		return nil
	}
	was := geometry.PartExtents(*before, geometry.Millimetre)
	now := geometry.PartExtents(*after, geometry.Millimetre)

	label := map[string]string{}
	for _, p := range after.Parts {
		label[p.ID] = p.Label()
	}

	var out []geometry.Problem
	ids := make([]string, 0, len(was))
	for id := range was {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		b, a := was[id], now[id]
		if _, ok := now[id]; !ok {
			continue // gone, or unbuildable: vanished.go and Faults() have those
		}
		if !resized(b, a) {
			continue
		}
		detail := fmt.Sprintf("%s is now %.0f x %.0f x %.0f mm and was %.0f x %.0f x %.0f mm.",
			label[id], a[0], a[1], a[2], b[0], b[1], b[2])
		// Naming the mechanism when the numbers show it: a permutation is the
		// drawing facing the wrong way, and saying so is what lets the model fix
		// it rather than rescale it.
		if isPermutation(b, a) {
			detail += " Those are the same three dimensions in a different order, so the part " +
				"has been turned on its side: the outline is facing across the assembly " +
				"instead of along it. Keep the part the size it was."
		} else {
			detail += " A restyle must not resize the part. Keep the dimensions it had."
		}
		out = append(out, geometry.Problem{
			Severity: geometry.Error,
			Name:     label[id],
			Detail:   detail,
		})
	}
	return out
}

func resized(b, a [3]float64) bool {
	for i := 0; i < 3; i++ {
		if b[i] <= 0 || a[i] <= 0 {
			continue
		}
		if r := a[i] / b[i]; r > resizeTolerance || r < 1/resizeTolerance {
			return true
		}
	}
	return false
}

// isPermutation is true when the same three lengths came back on different axes.
// Tolerance is proportional: a reshaped part is a few percent off its old
// envelope even when it is exactly the same size of thing.
func isPermutation(b, a [3]float64) bool {
	bs, as := b, a
	sort.Float64s(bs[:])
	sort.Float64s(as[:])
	for i := 0; i < 3; i++ {
		if bs[i] <= 0 {
			return false
		}
		if math.Abs(as[i]-bs[i])/bs[i] > 0.25 {
			return false
		}
	}
	return true
}

// repairIfTurned gives a revision that resized a part one chance to put it back,
// and says so either way.
//
// Runs AFTER repairIfFaulty: a document that will not build has a worse problem
// than one that builds the wrong size, and repairing the fault may change the
// dimensions anyway.
func (c *Conversation) repairIfTurned(ctx context.Context, reply *Reply, before *Prototype) {
	if reply == nil || reply.Prototype == nil || before == nil {
		return
	}
	problems := turnedOnItsSide(before, reply.Prototype)
	if len(problems) == 0 {
		return
	}
	if fixed := c.repairGeometry(ctx, reply.Prototype, problems); fixed != nil {
		// Only if it actually helped AND did not break the build getting there.
		if len(fixed.Faults()) <= len(reply.Prototype.Faults()) &&
			len(turnedOnItsSide(before, fixed)) < len(problems) {
			reply.Prototype = fixed
			problems = turnedOnItsSide(before, fixed)
			if len(problems) == 0 {
				reply.noteRepair("A part came back a different size from the one it replaced. " +
					"FORGE put it back to the size it was and re-checked.")
				return
			}
		}
	}
	// Still wrong: say it plainly. The person asked for a restyle and is looking
	// at the part they asked about, so a body that is now a different size of
	// thing is the last thing they will measure.
	names := make([]string, 0, len(problems))
	for _, p := range problems {
		names = append(names, p.Detail)
	}
	reply.noteRepair("This revision changed the size of something it was not asked to resize. " +
		strings.Join(names, " "))
}
