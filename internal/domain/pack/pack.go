// Package pack owns the domain packs a project can be worked in, and which of
// them this build is validated for (PRD SEC-07).
package pack

import "strings"

// Domain packs and validated intended use (PRD SEC-07).
//
// # What a pack is
//
// The PRD is explicit that "a pack is NOT a prompt": it bundles schemas,
// terminology, standards access, tool adapters, artifact validators, 3D
// conventions, evaluation suites, safety policies, data-handling rules and
// qualified-review requirements. It is the rule set a project is worked under.
//
// # What it was
//
// A free-text column. `forge_projects.pack` was required to be non-empty and
// then never read by anything, and its two producers hard-code "software" and
// "general". So a typo, a pack this build has never heard of, and a regulated
// domain were all equally acceptable and equally inert — the column recorded a
// claim about which rules applied and no rule ever consulted it.
//
// That is worse than having no column. A deployment could hold projects marked
// `medical` and nothing anywhere would behave differently, while the record
// said a medical rule set was in force.
//
// # What SEC-07 asks for, and what this build can honestly do about it
//
// "Regulated deployments stay inside validated intended use." A regulated
// deployment is one where the intended use has been validated — by a process
// outside this repository, against a standard this repository does not
// implement, producing evidence this repository does not hold.
//
// This build has none of that. It also has no surface through which anybody
// could declare a regulated project: both producers pass a constant, and no API
// or CLI accepts a pack. So the honest position is not "regulated work is
// gated" — it is that regulated work is NOT AVAILABLE HERE, and the code says
// so at the point somebody tries.
//
// # Why this is not a configuration flag
//
// The obvious shape is FORGE_ENABLED_PACKS, and it is the wrong one. An
// environment variable that switches on patient-specific clinical use would
// make the boundary of a regulated deployment something an operator crosses by
// editing a file — no validation evidence, no qualified authority, no record of
// who decided. The PRD's own carve-out says patient-specific use "requires a
// separately validated deployment and is not enabled by this codebase", and a
// flag would quietly make that sentence false.
//
// Reaching regulated use therefore requires changing this table, in a commit,
// with whatever validation evidence that deployment has to stand on. That is a
// higher bar than a config knob on purpose, and it is the only bar this
// repository is in a position to enforce.

// Pack is the rule set a project is worked under.
type Pack string

// The packs PRD §"Domain packs" names. The set is closed: a project may not
// declare something outside it, because a pack nothing recognises selects no
// rules while looking like it selected some.
const (
	Software   Pack = "software"
	Mechanical Pack = "mechanical"
	Electrical Pack = "electrical"
	Civil      Pack = "civil"
	Robotics   Pack = "robotics"
	Aerospace  Pack = "aerospace"
	Medical    Pack = "medical"
	General    Pack = "general"
)

// Definition is one pack and what this build may do inside it.
type Definition struct {
	Pack Pack
	// Summary is the PRD's safety boundary for this pack, in one line.
	Summary string
	// Available is whether a project may be created in it HERE.
	//
	// False does not mean the pack is wrong or unsupported in principle. It
	// means this build has not been validated for the intended use the pack
	// requires, and the refusal says which authority would have to establish
	// that.
	Available bool
	// Requires is what a deployment would need before this pack could be
	// available: the validation, the qualified authority, the standard. Shown in
	// the refusal, because "not available" with no path is a dead end.
	Requires string
}

// definitions is the table. One row per pack the PRD names.
//
// A table rather than a switch, for riskRules' reason: this decides what gets
// refused, so it has to be readable by somebody who will not read the function
// that applies it.
var definitions = []Definition{
	{
		Pack:      Software,
		Summary:   "Sandbox by default; review before merge or deploy; secrets and production are stricter.",
		Available: true,
	},
	{
		Pack:      General,
		Summary:   "Unknown domain or missing standards: autonomy is lower and expert review is triggered.",
		Available: true,
	},
	{
		Pack:    Mechanical,
		Summary: "Render is not proof; drawing release, tooling, fabrication and certification need qualified review.",
		// Not a regulated pack in the SEC-07 sense, and still not available:
		// the qualified-review requirement it carries is a workflow this build
		// does not implement, and a project marked `mechanical` whose drawings
		// nothing gated would be the same lie the free-text column told.
		Available: false,
		Requires: "a qualified-review workflow for drawing release, tooling and certification, " +
			"which this build does not implement",
	},
	{
		Pack:      Electrical,
		Summary:   "High-voltage, RF, battery, bench actuation, procurement and compliance are gated.",
		Available: false,
		Requires:  "gating for the hazardous classes above, which this build does not implement",
	},
	{
		Pack:      Robotics,
		Summary:   "Simulation first; physical motion needs bounded mode, interlocks, clearance and human control.",
		Available: false,
		Requires: "bounded-mode motion control with interlocks. This build grants no actuation " +
			"capability at all, so there is nothing here to bound",
	},
	{
		Pack:      Civil,
		Summary:   "A licensed engineer owns calculations, issued drawings, compliance and field direction.",
		Available: false,
		Requires: "a named licensed engineer holding sign-off authority over calculations and issued " +
			"drawings — an authority this build cannot represent, let alone verify",
	},
	{
		Pack:      Aerospace,
		Summary:   "No unsupervised hazardous procedure, flight command, launch decision or release authority.",
		Available: false,
		Requires: "release authority and hazardous-procedure control under the operating certificate " +
			"of the organisation flying the vehicle",
	},
	{
		Pack:    Medical,
		Summary: "Regulated intended use only; a clinician approves patient-specific output; no autonomous diagnosis, treatment or instrument actuation.",
		// The PRD's carve-out, made enforceable rather than left as prose:
		// "Medical pack: educational and device-concept scope only. Patient-specific
		// use requires a separately validated deployment and is NOT enabled by this
		// codebase."
		Available: false,
		Requires: "a separately validated deployment under the intended use its regulator approved, " +
			"with a clinician holding approval authority over every patient-specific output. " +
			"This codebase is educational and device-concept scope only and does not enable " +
			"patient-specific use",
	},
}

// Lookup returns the definition of a pack, and whether it is one at all.
func Lookup(name string) (Definition, bool) {
	want := Pack(strings.ToLower(strings.TrimSpace(name)))
	for _, d := range definitions {
		if d.Pack == want {
			return d, true
		}
	}
	return Definition{}, false
}

// All returns every pack the PRD names, available or not.
//
// Including the unavailable ones is the point: a list of what a deployment may
// do is only readable next to what it may not.
func All() []Definition {
	out := make([]Definition, len(definitions))
	copy(out, definitions)
	return out
}

// AvailableNames lists the packs a project may be created in, for error text.
func AvailableNames() []string {
	var out []string
	for _, d := range definitions {
		if d.Available {
			out = append(out, string(d.Pack))
		}
	}
	return out
}
