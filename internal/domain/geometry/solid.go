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
	// Outline is an extrusion's, a revolve's or a sweep's drawing in
	// millimetres, in the part's own XY plane (z is zero). Nil for every other
	// shape.
	//
	// A Curve rather than a list of points because a drawing can have ARCS in it
	// (curve.go), and points cannot say so. Sending points would make every
	// rounded corner a flat facet in the exported STEP.
	Outline *Curve `json:"outline,omitempty"`
	// Holes are the loops inside Outline, in millimetres. Empty when there are
	// none, which is the common case.
	Holes []Curve `json:"holes,omitempty"`
	// Path is a sweep's, in millimetres, in the part's own frame. Nil otherwise.
	Path *Curve `json:"path,omitempty"`
	// SectionFrame is where the outline's own x and y axes point when a sweep
	// sets off, row-major, with the COLUMNS being the profile's x, the profile's
	// y, and the path's first direction — the same convention as Matrix.
	//
	// # Why it travels rather than being derived
	//
	// A builder handed only a profile and a path has to decide which way up the
	// section starts and how it is carried round each bend, and there are
	// several defensible answers. Two builders deciding separately agree until
	// one is edited, and the day they diverge the exported solid has its section
	// rotated from the drawn one — visible on any outline that is not
	// rotationally symmetric, and invisible on the round ones people test with.
	//
	// So it is decided once, in sweptSections, and sent. A POINTER because there
	// is no harmless default: a zero matrix is a frame with no axes, and a shape
	// that reads one would build something degenerate rather than obviously
	// wrong. Nil says "this shape has no section", which is the truth for every
	// shape but a sweep.
	SectionFrame *[9]float64 `json:"section_frame,omitempty"`
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

	profiles, paths, profileProblems := d.resolvedProfiles()
	for _, problem := range profileProblems {
		inferred = append(inferred, fmt.Sprintf("%s %s, so it is not in this file.",
			problem.Name, problem.Detail))
	}

	out := make([]Solid, 0, len(d.Parts))
	for _, p := range d.Parts {
		dims := map[string]float64{}
		var section *outline
		var route *polyline
		switch strings.ToLower(p.Shape) {
		case "extrusion":
			pts, ok := profiles[p.ID]
			if !ok {
				// Reported above by resolvedProfiles. Skipped rather than
				// approximated: an outline nobody can read is not a shape to
				// guess at.
				continue
			}
			section = &pts
			dims["depth"] = sizeOr(p, "depth", 1, unit, infer)
		case "revolve":
			pts, ok := profiles[p.ID]
			if !ok {
				continue
			}
			section = &pts
			// No dimension of its own: a revolve's size is entirely its outline
			// and the axis it turns about. Asking for a depth as well would be
			// a second way to say something the outline already says.
		case "sweep":
			pts, ok := profiles[p.ID]
			way, hasPath := paths[p.ID]
			if !ok || !hasPath {
				continue
			}
			section, route = &pts, &way
			// No dimension either, for the same reason and more so: a sweep's
			// size is its outline and the path it follows, and a "depth" beside
			// them would be a third opinion about how far it goes.
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
		// length and every drawn coordinate is a length — including a corner
		// radius — so one factor covers them all; the rotation is an angle and
		// is untouched.
		for k, v := range dims {
			dims[k] = v * toMM
		}
		for i := range pos {
			pos[i] *= toMM
		}
		// The kernel is given the drawing with its ARCS INTACT, so that a
		// rounded corner comes out of OCCT as a real cylindrical surface. The
		// tessellators flatten instead, and say what that cost — see curve.go.
		var outlineCurve, pathCurve *Curve
		var holeCurves []Curve
		failed := ""
		if section != nil {
			c, err := section.Outer.scaled(toMM).exact("outline")
			if err != nil {
				failed = err.Error()
			}
			outlineCurve = &c
			for i, hole := range section.Holes {
				h, err := hole.scaled(toMM).exact(fmt.Sprintf("hole %d", i+1))
				if err != nil && failed == "" {
					failed = err.Error()
				}
				holeCurves = append(holeCurves, h)
			}
		}
		if route != nil && failed == "" {
			c, err := route.scaled(toMM).exact("path")
			if err != nil {
				failed = err.Error()
			}
			pathCurve = &c
		}

		// The section frame is computed from the CONVERTED and FLATTENED drawing,
		// so that the one thing the kernel is told about orientation was worked
		// out from the same numbers it is going to build with. It is a rotation
		// and unchanged by the conversion; flattening does not move the first
		// segment, because a rounded corner starts partway ALONG its edges and
		// leaves their directions alone.
		var frame *[9]float64
		if route != nil && failed == "" {
			flatOutline, _, oerr := section.Outer.scaled(toMM).flatten("outline", Millimetre)
			flatPath, _, perr := route.scaled(toMM).flatten("path", Millimetre)
			switch {
			case oerr != nil:
				failed = oerr.Error()
			case perr != nil:
				failed = perr.Error()
			default:
				if _, f, err := sweptSections([][][2]float64{flat2D(flatOutline)}, flatPath); err == nil {
					frame = &f
				} else {
					failed = err.Error()
				}
			}
		}
		if failed != "" {
			// resolvedProfiles refused every drawing that cannot be built, so
			// reaching this is the two of them disagreeing. Skipped rather than
			// sent half-resolved: a sweep with no frame is a section with no
			// orientation, and the kernel would build it somewhere arbitrary.
			inferred = append(inferred, fmt.Sprintf(
				"%s: this shape could not be resolved (%s), so it is not in this file.",
				p.Label(), failed))
			continue
		}

		out = append(out, Solid{
			ID: p.ID, Label: p.Label(), Shape: strings.ToLower(p.Shape), Dims: dims,
			Outline: outlineCurve, Holes: holeCurves, Path: pathCurve, SectionFrame: frame,
			Axis: axisOf(p), Matrix: RotationMatrix(rot), Position: pos,
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
