package geometry

import (
	"fmt"
	"sort"
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

// Solid is a part with every dimension resolved and its placement precomputed.
type Solid struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Shape string `json:"shape"`
	// Dims are the dimensions this shape reads, defaults already applied. Which
	// keys are present depends on the shape and is the builder's contract.
	Dims map[string]float64 `json:"dims"`
	// Matrix is the rotation, row-major, from RotationMatrix.
	Matrix [9]float64 `json:"matrix"`
	// Position is the centre, in the document's declared unit.
	Position [3]float64 `json:"position"`
}

// Solids reduces every part, and reports every dimension it had to invent.
//
// The inferences are the same sentences the mesh exporter records, and for the
// same reason: a defaulted 1 is indistinguishable from a stated 1 once it is in
// a file, and there is no provenance banner attached to a download.
func Solids(d Document, unit Unit) ([]Solid, []string) {
	var inferred []string
	infer := func(format string, args ...any) {
		inferred = append(inferred, fmt.Sprintf(format, args...))
	}

	out := make([]Solid, 0, len(d.Parts))
	for _, p := range d.Parts {
		dims := map[string]float64{}
		switch p.Shape {
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

		out = append(out, Solid{
			ID: p.ID, Label: p.Label(), Shape: p.Shape, Dims: dims,
			Matrix: RotationMatrix(rot), Position: pos,
		})
	}
	sort.SliceStable(inferred, func(i, j int) bool { return inferred[i] < inferred[j] })
	return out, inferred
}
