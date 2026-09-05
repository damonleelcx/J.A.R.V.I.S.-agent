package geometry

import (
	"fmt"
	"sort"
	"strings"
)

// Solids: one part, reduced to what any builder needs.
//
// # Why this exists
//
// Two things now build geometry from a Document — the tessellator in mesh.go and
// the CAD kernel in internal/domain/cad — and they have to agree about three
// awkward details:
//
//   - which dimension a shape reads, and what it uses when the model gave none;
//   - what a rotation MEANS (the Euler order, and that it is radians);
//   - that a part is centred on its own position, rotated then translated.
//
// Each of those is a paragraph of convention. Implemented twice they agree until
// somebody edits one, and the day they diverge the exported file shows a part
// somewhere other than where it was drawn — which is the one failure a download
// cannot be labelled out of, because there is no label attached to a file.
//
// So the convention is applied ONCE, here, and both builders read the result.
// The kernel in particular never sees a Part: it receives numbers and a matrix,
// which is all a kernel should need and nothing it could misinterpret.

// Solid is a part with every dimension resolved, converted, and its placement
// precomputed.
//
// EVERY LENGTH IS IN MILLIMETRES, whatever the document declared. See Solids.
type Solid struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Shape string `json:"shape"`
	// Dims are the dimensions this shape reads, defaults applied and converted
	// to millimetres. Which keys are present depends on the shape and is the
	// builder's contract.
	Dims map[string]float64 `json:"dims"`
	// Profile is an extrusion's or a revolve's outline in millimetres, in the
	// part's own XY plane. Empty for every other shape.
	Profile [][2]float64 `json:"profile,omitempty"`
	// Axis is which way a revolve turns, "y" or "x". Empty otherwise.
	Axis string `json:"axis,omitempty"`
	// Matrix is the rotation, row-major, from RotationMatrix.
	Matrix [9]float64 `json:"matrix"`
	// Position is the centre, in millimetres.
	Position [3]float64 `json:"position"`
}

// Solids reduces every part, converts it to millimetres, and reports every
// dimension it had to invent.
//
// # Why it converts, and why that is not optional
//
// A STEP file DECLARES its unit, and build123d writes SI_UNIT(.MILLI.,.METRE.)
// unconditionally. So a document in inches, sent through unconverted, produces a
// file that says a 2 inch cube is 2 MILLIMETRES — confidently, in a format
// everything downstream treats as exact. That is a factor of 25.4 in a
// manufacturable artefact, and it is silent.
//
// Measured 2026-09-05: a 2 in cube came back with volume 8 and bounds ±1, in a
// file declaring millimetres. It was wrong from the first build of the kernel
// and no test asked, because every fixture was already in mm.
//
// Millimetres because that is this domain's base (units.go) and what the format
// writes. An unconvertible unit returns NOTHING rather than guessing: a wrong
// guess about scale is the difference between a bracket and a building, and the
// callers refuse on the empty result.
//
// The inferences are the same sentences the mesh exporter records, and for the
// same reason: a defaulted 1 is indistinguishable from a stated 1 once it is in
// a file, and there is no provenance banner attached to a download.
func Solids(d Document, unit Unit) ([]Solid, []string) {
	toMM, convertible := unit.toMM()
	if !convertible {
		return nil, []string{"This assembly declares no unit FORGE can convert, so nothing " +
			"could be built: a file that states a scale must state the right one."}
	}
	var inferred []string
	infer := func(format string, args ...any) {
		inferred = append(inferred, fmt.Sprintf(format, args...))
	}

	profiles, profileProblems := d.resolvedProfiles()
	for _, problem := range profileProblems {
		inferred = append(inferred, fmt.Sprintf("%s %s, so it is not in this file.",
			problem.Name, problem.Detail))
	}

	out := make([]Solid, 0, len(d.Parts))
	for _, p := range d.Parts {
		dims := map[string]float64{}
		var profile [][2]float64
		switch strings.ToLower(p.Shape) {
		case "extrusion":
			pts, ok := profiles[p.ID]
			if !ok {
				// Reported above by resolvedProfiles. Skipped rather than
				// approximated: an outline nobody can read is not a shape to
				// guess at.
				continue
			}
			profile = pts
			dims["depth"] = sizeOr(p, "depth", 1, unit, infer)
		case "revolve":
			pts, ok := profiles[p.ID]
			if !ok {
				continue
			}
			profile = pts
			// No dimension of its own: a revolve's size is entirely its outline
			// and the axis it turns about. Asking for a depth as well would be
			// a second way to say something the outline already says.
		case "box":
			dims["width"] = sizeOr(p, "width", 1, unit, infer)
			dims["height"] = sizeOr(p, "height", 1, unit, infer)
			dims["depth"] = sizeOr(p, "depth", 1, unit, infer)
		case "plane":
			dims["width"] = sizeOr(p, "width", 1, unit, infer)
			dims["depth"] = sizeOr(p, "depth", 1, unit, infer)
		case "sphere":
			dims["radius"] = sizeOr(p, "radius", 0.5, unit, infer)
		case "cylinder", "tube":
			dims["radius"] = sizeOr(p, "radius", 0.5, unit, infer)
			dims["height"] = sizeOr(p, "height", 1, unit, infer)
			// radius_top is what makes a cylinder a truncated cone. Absent means
			// straight-sided, which is not an inference and is not reported.
			if v, ok := p.Size["radius_top"]; ok {
				dims["radius_top"] = v
			} else {
				dims["radius_top"] = dims["radius"]
			}
		case "cone":
			dims["radius"] = sizeOr(p, "radius", 0.5, unit, infer)
			dims["height"] = sizeOr(p, "height", 1, unit, infer)
			dims["radius_top"] = 0
		default:
			// An unknown shape is skipped rather than guessed at. The mesh path
			// makes the same choice; a builder inventing a box for a word it did
			// not recognise would put a solid in a file that nothing asked for.
			inferred = append(inferred,
				fmt.Sprintf("%s: %q is not a shape FORGE can build, so it is not in this file.",
					p.Label(), p.Shape))
			continue
		}

		pos := [3]float64{}
		copy(pos[:], padTo3(p.Position))
		rot := [3]float64{}
		copy(rot[:], padTo3(p.Rotation))

		// To millimetres. Every dimension here is a length, every position is a
		// length and every profile coordinate is a length, so one factor covers
		// all three; the rotation is an angle and is untouched.
		for k, v := range dims {
			dims[k] = v * toMM
		}
		for i := range pos {
			pos[i] *= toMM
		}
		scaled := make([][2]float64, len(profile))
		for i, pt := range profile {
			scaled[i] = [2]float64{pt[0] * toMM, pt[1] * toMM}
		}

		out = append(out, Solid{
			ID: p.ID, Label: p.Label(), Shape: strings.ToLower(p.Shape), Dims: dims,
			Profile: scaled, Axis: axisOf(p), Matrix: RotationMatrix(rot), Position: pos,
		})
	}
	sort.SliceStable(inferred, func(i, j int) bool { return inferred[i] < inferred[j] })
	return out, inferred
}

// axisOf names a revolve's axis and nothing else's, so a box does not arrive at
// the kernel carrying an axis it has no use for.
func axisOf(p Part) string {
	if strings.EqualFold(strings.TrimSpace(p.Shape), "revolve") {
		return RevolveAxis(p)
	}
	return ""
}
