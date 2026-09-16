package geometry

import "fmt"

// Two parts occupying the same material.
//
// # The gap this closes
//
// assembly.go states it plainly and has since it was written: "no interference
// test, no clearance, no kinematics". On 2026-09-12 a live car build measured
// what that costs — 28 parts, document faults ZERO, a clean kernel build, a
// passing visual check, and the master cylinder entirely inside the engine
// block. Nothing lied. Nothing asked.
// docs/spikes/2026-09-12-car-ceiling/README.md
//
// This is the first check in FORGE that is geometric rather than visual, and it
// is the only kind that scales: a contact sheet at four hundred parts is a grey
// smudge, and a defect INSIDE a solid was never visible in a picture anyway.
//
// # Why the numbers arrive from the kernel and are not computed here
//
// A bounding-box test in Go would be free and wrong in both directions, because
// Tessellate performs no boolean — every bolt hole would report as interference
// (the cut tool is still a solid cylinder standing in the plate), and a part
// inside a hollow enclosure would report too. Both were measured; see
// cad/sidecar.py, where this is computed on the solids that survive the
// features. That is Stage 7's rule once more: look at the solid that was built,
// not the one that was described.
//
// So a deployment with no CAD kernel gets no interference findings rather than
// approximate ones, exactly as it gets no STEP rather than an approximate STEP.
type Interference struct {
	// A and B are the two part ids, and the labels the person sees.
	A      string `json:"a"`
	B      string `json:"b"`
	ALabel string `json:"a_label,omitempty"`
	BLabel string `json:"b_label,omitempty"`
	// Volume is the shared material in CUBIC MILLIMETRES, like every other
	// number the kernel returns.
	Volume float64 `json:"volume"`
	// Fraction is that volume as a share of the SMALLER part — the number that
	// says whether this is a graze or a part that has been swallowed. A quarter
	// of a small bracket buried in a chassis rail matters; the same cubic
	// millimetres shared between two chassis rails is a seam.
	Fraction float64 `json:"fraction"`
}

// Buried reports whether this is the unambiguous case: most of the smaller part
// is inside the larger one.
//
// # Why there is a threshold at all, and why repairs use only this side of it
//
// A concept assembly has legitimate shared material: a tyre modelled a
// millimetre into its rim, two rails meeting at a weld, a press fit. Those are
// real overlaps and reporting them is right, but they must not drive an
// automatic repair — this repository has already had to delete one rule that
// fired on correct models, because a checker that complains about a correct
// answer drives repairs that damage it.
//
// A part MOSTLY inside another is different in kind. There is no assembly in
// which the master cylinder belongs inside the engine block, and no tolerance
// story that produces it.
//
// ‼️ The number is NOT yet calibrated against live data — the 2026-09-12 run
// measured bounding-box fractions, which are not these. Every interference is
// reported whatever its fraction, so the next live build produces the
// distribution this constant should be chosen from.
func (i Interference) Buried() bool { return i.Fraction >= BuriedFraction }

// BuriedFraction is the share of the smaller part that has to be inside the
// other one before it is treated as certainly wrong. See Buried.
const BuriedFraction = 0.5

// Describe is the sentence a person — or a model being asked to fix it — reads.
//
// Phrased as what is WRONG rather than as a measurement, for the same reason
// georepair hands back the builder's own words: "shares 412 mm³" invites a
// discussion about tolerance, and "is 78% inside" names the defect.
func (i Interference) Describe() string {
	a, b := i.ALabel, i.BLabel
	if a == "" {
		a = i.A
	}
	if b == "" {
		b = i.B
	}
	// A is the part the fraction is ABOUT — the kernel reports the share of the
	// smaller solid, and names it first — so the sentence reads in that order.
	return fmt.Sprintf("%s is %.0f%% inside %s: they occupy the same material (%.0f mm³). "+
		"Move one of them, or make the part that contains the other hollow.",
		a, i.Fraction*100, b, i.Volume)
}

// InterferenceProblems turns findings into the Problem list every repair path
// already takes.
//
// Only the buried ones. The rest are reported to the reader by the caller and
// deliberately do not drive a rewrite — see Buried.
func InterferenceProblems(found []Interference) []Problem {
	var out []Problem
	for _, i := range found {
		if !i.Buried() {
			continue
		}
		out = append(out, Problem{
			Severity: Error,
			Name:     i.A,
			Detail:   i.Describe(),
		})
	}
	return out
}
