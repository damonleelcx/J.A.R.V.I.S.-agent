package agent

import (
	"fmt"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// Placing the assembly a build step built, when the step did not.
//
// # The problem this solves
//
// A step on a tree builds the sub-assembly its plan names ("brakes") and places it
// from the root. Placing it means patching the root assembly, which an edit replaces
// whole, so the step has to restate every child the root already has. Measured on the
// 2026-09-15 live car (docs/spikes/2026-09-15-car-quality, run 1): of five steps that
// created a new assembly after the first, NONE placed it. Four patched only their own
// assembly; one wrote "root_children" into its patch, a field of what it was shown and
// not of an edit. The finished car held the suspension, brakes, steering and body as
// designs nothing placed. An unplaced assembly is not a fault, so nothing refused,
// repaired or noted it; the look said "the requested brake calipers are not present"
// and its repair could not say why.
//
// # Why FORGE places it rather than asking again
//
// The plan already said where it goes: the step builds that assembly, as a child of
// the root. There is exactly one reading of an assembly the step built and nothing
// places, and it costs no call. Asking the model again was measured too: the look's
// repair was sent the whole document on each of those steps and returned the root
// unchanged every time.
//
// # What it does not do
//
// It never moves a placement the step made, never places an assembly the plan did not
// name for this step, and places at the root's origin: the frame the step was shown
// its attachment points in. It says what it did.

// placeStepAssembly places the step's assembly from the root when the step built it
// and nothing places it, and returns the sentence saying so, or "".
func placeStepAssembly(d *Prototype, focus string) string {
	focus = strings.TrimSpace(focus)
	if d == nil || focus == "" || d.Root == focus {
		return ""
	}
	built := false
	for _, a := range d.Assemblies {
		if a.ID == focus {
			built = true
		}
		for _, c := range a.Children {
			if c.Ref == focus {
				return "" // placed already, wherever the step chose
			}
		}
	}
	if !built {
		return ""
	}
	if d.Root == "" {
		d.Root = focus
		return fmt.Sprintf("This step built the assembly %q and nothing placed it, so FORGE made it the model's root.", focus)
	}
	for i := range d.Assemblies {
		if d.Assemblies[i].ID != d.Root {
			continue
		}
		id := focus
		for childTaken(d.Assemblies[i].Children, id) {
			id += "-placed"
		}
		// New slices: the document's assemblies may still share their children with the
		// model before this step, which must not change.
		assemblies := append([]geometry.Assembly(nil), d.Assemblies...)
		assemblies[i].Children = append(append([]geometry.Child(nil), d.Assemblies[i].Children...),
			geometry.Child{ID: id, Ref: focus})
		d.Assemblies = assemblies
		return fmt.Sprintf("This step built the assembly %q and did not place it, so FORGE placed it from the root "+
			"%q at the root's origin.", focus, d.Root)
	}
	return ""
}

func childTaken(children []geometry.Child, id string) bool {
	for _, c := range children {
		if c.ID == id {
			return true
		}
	}
	return false
}
