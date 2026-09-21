package geometry

import (
	"fmt"
	"math"
	"strings"
)

// What a named cut through a part is worth, and what it is not.
//
// # Why this exists, and why it is not an FEA (issue 6, strength)
//
// Issue 6 asks for three checks. Interference is built (interference.go), and
// manufacturability is beside this (manufacturability.go). The third is strength,
// and the honest answer to it is: FORGE does not have one and cannot get one
// cheaply. A stress needs a LOAD, a load path, boundary conditions, a mesh, a
// solver and a material's yield strength. FORGE has none of those, and a number
// produced without them would be the most dangerous thing in this repository —
// exactly the failure the tolerance rule already names.
//
// So this measures the part of a strength calculation that is pure geometry and
// is exact: the area, the centroid, and the second moments of area of a plane cut
// through one part, with the section moduli that follow. Those are the numbers an
// engineer needs BEFORE a load is known, they are checkable against b*h^3/12 on a
// rectangle, and they make no claim whatsoever about whether the part holds.
//
// The PR that added this says what a real stress check would take. Nothing here
// pretends to be it.

// Section is a plane cut somebody named, in the document.
type Section struct {
	ID string `json:"id"`
	// Part is the id of the part to cut.
	Part string `json:"part"`
	// Axis is the cutting plane's NORMAL: "x", "y" or "z".
	Axis string `json:"axis"`
	// At is where along that axis the plane sits, in the document's own units.
	At float64 `json:"at"`
}

// SectionAxes is the closed set Axis is checked against, and the set the contract
// teaches.
var SectionAxes = []string{"x", "y", "z"}

// ValidSectionAxis reports whether a name is one of the three.
func ValidSectionAxis(axis string) bool {
	for _, a := range SectionAxes {
		if strings.EqualFold(strings.TrimSpace(axis), a) {
			return true
		}
	}
	return false
}

// SectionProperties is one cut as the kernel measured it, in MILLIMETRES.
type SectionProperties struct {
	ID   string  `json:"id"`
	Part string  `json:"part"`
	Axis string  `json:"axis"`
	At   float64 `json:"at"`
	// Area is in square millimetres, Centroid in millimetres.
	Area     float64    `json:"area_mm2"`
	Centroid [3]float64 `json:"centroid_mm"`
	// Axes names the two in-plane axes, in the order the moments are given:
	// "y","z" for a cut normal to x.
	Axes [2]string `json:"axes"`
	// SecondMoments are the second moments of AREA about each of those two axes
	// through the centroid, in mm^4. For a b x h rectangle the moment about the
	// axis h is measured along is b*h^3/12 — which is how the fence checks it.
	SecondMoments [2]float64 `json:"second_moments_mm4"`
	// Fibres are the distances from the centroid to the furthest material for
	// each of those bendings, in mm, and Moduli the section moduli (I/c) in mm^3.
	Fibres [2]float64 `json:"extreme_fibres_mm"`
	Moduli [2]float64 `json:"section_moduli_mm3"`
	// Unmeasured is why there is nothing here, and empty when there is.
	Unmeasured string `json:"unmeasured,omitempty"`
}

// Describe is the sentence the turn prints for one section.
func (s SectionProperties) Describe() string {
	if s.Unmeasured != "" {
		return fmt.Sprintf("%s (%s at %s = %g): not measured — %s",
			s.ID, s.Part, s.Axis, s.At, s.Unmeasured)
	}
	return fmt.Sprintf("%s (%s, the plane %s = %g): area %s mm², second moment of area "+
		"%s mm⁴ about %s and %s mm⁴ about %s, section moduli %s and %s mm³",
		s.ID, s.Part, s.Axis, s.At, trim(s.Area),
		trim(s.SecondMoments[0]), s.Axes[0], trim(s.SecondMoments[1]), s.Axes[1],
		trim(s.Moduli[0]), trim(s.Moduli[1]))
}

func trim(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "?"
	}
	return fmt.Sprintf("%.4g", v)
}

// SectionNote is what reaches the turn, and the caveat that must travel with it.
//
// ‼️ The caveat is not decoration. A second moment of area beside a part is one
// step from being read as "FORGE checked the strength", and this product's fifth
// promise is that nothing reads as a check that did not happen. So the sentence
// that says what these numbers are NOT is part of the note, not an option.
func SectionNote(sections []SectionProperties) string {
	if len(sections) == 0 {
		return ""
	}
	lines := make([]string, 0, len(sections))
	for _, s := range sections {
		lines = append(lines, s.Describe())
	}
	return fmt.Sprintf("Section properties, measured on the solid the kernel built: %s. "+
		"These are GEOMETRY, not a stress: FORGE ran no analysis, applied no load and knows "+
		"no boundary conditions, so nothing here says whether the part holds.",
		strings.Join(lines, "; "))
}

// ValidateSections drops the sections that cannot be measured and says why.
//
// A section naming a part that is not in the document, or an axis that is not one
// of three, would come back from the kernel as a refusal per section — which is
// correct but late. Dropped here, the reader is told once, in the document's own
// words, like a state that names a missing part (ValidateStates).
func ValidateSections(sections []Section, parts []Part) (kept []Section, dropped []string) {
	have := make(map[string]bool, len(parts))
	for _, p := range parts {
		have[p.ID] = true
	}
	seen := map[string]bool{}
	for _, s := range sections {
		switch {
		case strings.TrimSpace(s.ID) == "":
			dropped = append(dropped, "A section with no id was dropped: a section is reported by name.")
		case seen[s.ID]:
			dropped = append(dropped, fmt.Sprintf("A second section named %q was dropped: "+
				"two sections with one name cannot be told apart in the report.", s.ID))
		case !have[s.Part]:
			dropped = append(dropped, fmt.Sprintf("The section %q cuts %q, which is not a part of this "+
				"assembly, so it was dropped.", s.ID, s.Part))
		case !ValidSectionAxis(s.Axis):
			dropped = append(dropped, fmt.Sprintf("The section %q is cut on axis %q; a section plane's "+
				"normal is x, y or z, so it was dropped.", s.ID, s.Axis))
		default:
			seen[s.ID] = true
			s.Axis = strings.ToLower(strings.TrimSpace(s.Axis))
			kept = append(kept, s)
		}
	}
	return kept, dropped
}
