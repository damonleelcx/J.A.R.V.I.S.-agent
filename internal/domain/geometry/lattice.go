package geometry

import (
	"fmt"
	"math"
	"strings"
)

// Lattice: a decorative, MESH-ONLY part — a triply periodic sheet filling a box.
//
// # The decision this rests on
//
// damon, 2026-09-18: "looks designed" is now a FORGE goal, and a part DECLARED
// mesh-only may break the rule that every part is an exact solid. Only a declared
// one. Every part that is not mesh-only is still an exact solid the OCCT kernel
// builds and exports to STEP.
//
// # What mesh-only means, everywhere
//
// A lattice is DECORATIVE AND NEVER STRUCTURAL. Nothing FORGE says about it may
// imply it can be made or carries load (PRD VIS-06), so every reader treats it
// honestly and says so:
//   - the kernel builds it as a MESH with manifold3d and it never enters an OCCT
//     boolean (sidecar.py, _lattice_mesh); the mesh reply marks it mesh_only;
//   - a STEP file leaves it out, and says so in the export label, the download's
//     header and the file's own FILE_DESCRIPTION;
//   - mass and volume leave it out and name it (MassReport.MeshOnly);
//   - the interference check does not look at it, and the turn says so beside the
//     rest of what the check did not cover (agent/interference.go, coverageNote).
//     Skipped, not boxed: a conservative box would report every part the infill
//     sits around as "inside" it, and a repair driven by that would move correct
//     parts to make room for a decoration;
//   - no feature may cut, fuse, fillet or loft it, or use it as a tool: there is no
//     exact solid to operate on (Operations refuses it by name);
//   - the viewport labels it MeshOnlyLabel.
//
// # Why these three patterns and nothing else
//
// Gyroid, diamond (Schwarz D) and primitive (Schwarz P) are level sets of a few
// sines and cosines, so the kernel samples them and manifold3d's level set and
// boolean make a closed, manifold mesh from any numbers that pass the checks below.
// A strut lattice (octet truss) and a Voronoi foam are not here: each is a union of
// thousands of struts, whose cost and robustness were not measured, and they are
// said to be missing rather than approximated.

// latticeShape is the word, spelled once.
const latticeShape = "lattice"

// MeshOnlyLabel is what the viewport shows on every mesh-only part, and what the
// mesh reply hands the browser to show. One spelling, fenced against workbench.js.
const MeshOnlyLabel = "mesh-only - not manufacturable"

// latticePattern is one pattern FORGE can draw: its name, what the contract says it
// looks like, and how many triangles it makes per unit of volume — see
// latticeTriangleEstimate.
type latticePattern struct {
	Name string
	Says string
	// TrianglesCoefficient is C in triangles ≈ C · V / (cell · edge²), measured on a
	// 60 × 40 × 30 box at cells 7.5-30 (docs/spikes/2026-09-18-mesh-only-parts) and
	// set about 15% above the largest ratio seen, so the estimate errs high.
	TrianglesCoefficient float64
}

// latticePatterns is THE table: the contract is written from it (LatticeGuide), the
// checks read it, and the kernel's _LATTICE_PATTERNS carries the same names
// (fenced by building every one of them in the kernel).
var latticePatterns = []latticePattern{
	{Name: "gyroid", Says: "one continuous wavy sheet, no straight lines anywhere", TrianglesCoefficient: 45},
	{Name: "diamond", Says: "a sheet of saddle-shaped tunnels on a diamond grid", TrianglesCoefficient: 54},
	{Name: "primitive", Says: "rounded cubic cells joined by round necks", TrianglesCoefficient: 36},
}

const (
	// MaxLatticeTriangles is the most triangles one mesh-only part may be. The
	// kernel refuses past its own count too (_LATTICE_BUDGET, the same number).
	// Measured: a gyroid of this many builds in under a second on this laptop.
	MaxLatticeTriangles = 200000
	// maxMeshOnlyTriangles bounds all mesh-only parts in a document together,
	// every copy counted: they arrive in the mesh reply in assembly coordinates.
	// The same number as the kernel's whole-mesh budget (_MESH_BUDGET).
	maxMeshOnlyTriangles = 400000
	// A wall thinner than cell/20 is a cloud of slivers and needs a sampling grid
	// finer than the triangles are worth; thicker than cell/3 fills the cell and
	// is a block with dimples.
	minWallPerCell = 1.0 / 20
	maxWallPerCell = 1.0 / 3
	// The sampling step is cell/8, or finer where the wall needs it (thickness/1.5).
	latticeCellSamples = 8.0
	latticeWallSamples = 1.5
)

// latticeSpec is a lattice's numbers, every one read and checked, in the
// document's own unit.
type latticeSpec struct {
	Pattern              latticePattern
	Width, Height, Depth float64
	Cell, Thickness      float64
}

// edge is the sampling step the kernel uses, in the document's unit.
func (l latticeSpec) edge() float64 {
	return math.Min(l.Cell/latticeCellSamples, l.Thickness/latticeWallSamples)
}

// latticeTriangleEstimate is how many triangles the kernel will make of it:
// C · V / (cell · edge²). The sheet's area per volume goes as 1/cell and each
// triangle covers about edge², so the ratio is unit-free.
func (l latticeSpec) triangles() int {
	e := l.edge()
	return int(math.Ceil(l.Pattern.TrianglesCoefficient * l.Width * l.Height * l.Depth / (l.Cell * e * e)))
}

// LatticeTriangleEstimate is the triangles FORGE expects the kernel to make of a
// lattice part, and false when its numbers are refused.
func LatticeTriangleEstimate(p Part) (int, bool) {
	l, problems := readLattice(p)
	if anyError(problems) {
		return 0, false
	}
	return l.triangles(), true
}

// IsMeshOnly reports whether a part is declared mesh-only: never an exact solid,
// never in STEP, never weighed or checked for interference.
func (p Part) IsMeshOnly() bool {
	return strings.EqualFold(strings.TrimSpace(p.Shape), latticeShape)
}

// MeshOnlyParts names every mesh-only part the document places, by label, in
// order, each placed copy counted once.
func (d Document) MeshOnlyParts() []string {
	var out []string
	for _, p := range d.Expanded().Parts {
		if p.IsMeshOnly() {
			out = append(out, p.Label())
		}
	}
	return out
}

// MeshOnlyNote is the one sentence every reader that leaves mesh-only parts out
// says, naming at most a few of them. "" when there are none.
func MeshOnlyNote(names []string, leftOutOf string) string {
	if len(names) == 0 {
		return ""
	}
	const most = 3
	shown, more := names, ""
	if len(names) > most {
		shown, more = names[:most], fmt.Sprintf(" and %d more", len(names)-most)
	}
	return fmt.Sprintf("%d mesh-only part(s) are left out of %s: %s%s. A mesh-only part is decorative, "+
		"not manufacturable and never structural (%s).", len(names), leftOutOf,
		strings.Join(shown, ", "), more, MeshOnlyLabel)
}

func latticePatternNamed(name string) (latticePattern, bool) {
	for _, p := range latticePatterns {
		if strings.EqualFold(strings.TrimSpace(name), p.Name) {
			return p, true
		}
	}
	return latticePattern{}, false
}

func latticePatternNames() []string {
	out := make([]string, len(latticePatterns))
	for i, p := range latticePatterns {
		out[i] = fmt.Sprintf("%q", p.Name)
	}
	return out
}

// readLattice reads a lattice's numbers and refuses, by name, every one that does
// not describe a lattice the kernel can build within its budget. Nothing is
// defaulted: a decoration somebody did not size is not one to invent.
func readLattice(p Part) (latticeSpec, []Problem) {
	var problems []Problem
	refuse := func(format string, args ...any) {
		problems = append(problems, Problem{Severity: Error, Name: p.Label(), Detail: fmt.Sprintf(format, args...)})
	}
	var l latticeSpec
	pattern, known := latticePatternNamed(p.Lattice)
	switch {
	case strings.TrimSpace(p.Lattice) == "":
		refuse("is a lattice with no \"lattice\" pattern; name one of %s", joinList(latticePatternNames()))
	case !known:
		refuse("asks for lattice pattern %q, which FORGE cannot draw; it draws %s", p.Lattice,
			joinList(latticePatternNames()))
	}
	l.Pattern = pattern
	read := func(key string) float64 {
		v, ok := p.Size[key]
		if !ok {
			refuse("is a lattice with no %q; a lattice needs width, height, depth, cell and thickness", key)
			return 0
		}
		if !(v > 0) || math.IsInf(v, 0) {
			refuse("has a lattice %s of %v; it must be a positive length", key, v)
			return 0
		}
		return v
	}
	l.Width, l.Height, l.Depth = read("width"), read("height"), read("depth")
	l.Cell, l.Thickness = read("cell"), read("thickness")
	if len(problems) > 0 {
		return l, problems
	}
	if l.Thickness < l.Cell*minWallPerCell || l.Thickness > l.Cell*maxWallPerCell {
		refuse("has a lattice wall %v thick in a %v cell; the wall must be between cell/20 (%v) and cell/3 (%v)",
			l.Thickness, l.Cell, round4(l.Cell*minWallPerCell), round4(l.Cell*maxWallPerCell))
		return l, problems
	}
	if n := l.triangles(); n > MaxLatticeTriangles {
		// The cell that would fit: triangles go as 1/cell³ when the wall scales with
		// the cell, which is how a person enlarges a lattice.
		fits := l.Cell * math.Cbrt(float64(n)/float64(MaxLatticeTriangles)) * 1.01
		refuse("is a %s lattice of about %d triangles, past the %d a mesh-only part may have; "+
			"a cell of %v or more (with the wall scaled with it), or a smaller box, fits",
			l.Pattern.Name, n, MaxLatticeTriangles, round4(fits))
	}
	return l, problems
}

func round4(v float64) float64 { return math.Round(v*1e4) / 1e4 }

// latticeProblems checks every lattice in an EXPANDED document — each placed copy
// is a part here — and the budget they share. Keyed by part id, so a builder can
// leave out exactly the ones refused.
func latticeProblems(parts []Part) map[string][]Problem {
	out := map[string][]Problem{}
	total := 0
	for _, p := range parts {
		if !p.IsMeshOnly() {
			continue
		}
		l, problems := readLattice(p)
		if !anyError(problems) {
			if n := l.triangles(); total+n > maxMeshOnlyTriangles {
				problems = append(problems, Problem{Severity: Error, Name: p.Label(), Detail: fmt.Sprintf(
					"is a mesh-only part of about %d triangles, and the ones before it are already about %d: "+
						"past the %d all mesh-only parts in one design may have together", n, total, maxMeshOnlyTriangles)})
			} else {
				total += n
			}
		}
		if len(problems) > 0 {
			out[p.ID] = problems
		}
	}
	return out
}

// allLatticeProblems is latticeProblems as one list, in the parts' order.
func allLatticeProblems(parts []Part) []Problem {
	byID := latticeProblems(parts)
	var out []Problem
	for _, p := range parts {
		out = append(out, byID[p.ID]...)
	}
	return out
}

// latticeDims is what the kernel is sent for a lattice, converted by toMM: its box,
// cell, wall and the sampling step (decided here, once, so the estimate above and
// the kernel's grid are the same grid).
func latticeDims(l latticeSpec, toMM float64) map[string]float64 {
	return map[string]float64{
		"width": l.Width * toMM, "height": l.Height * toMM, "depth": l.Depth * toMM,
		"cell": l.Cell * toMM, "thickness": l.Thickness * toMM, "edge": l.edge() * toMM,
	}
}

// MeshOnlyShapes is every shape word that declares a part mesh-only.
func MeshOnlyShapes() []string { return []string{latticeShape} }

// LatticePatternNames is the pattern names, in the table's order.
func LatticePatternNames() []string {
	out := make([]string, len(latticePatterns))
	for i, p := range latticePatterns {
		out[i] = p.Name
	}
	return out
}

// LatticeGuide is the contract's paragraph on lattices, written from the table.
func LatticeGuide() string {
	var b strings.Builder
	b.WriteString(`- "lattice" is a DECORATIVE MESH-ONLY infill: a thin wavy sheet filling a box,
  for the organic look of a designed part. It is decorative and NEVER
  structural: it is not a solid, it is left out of the STEP file, of the mass
  and of the interference check, it is labelled "` + MeshOnlyLabel + `",
  and no feature may cut, fuse, fillet or loft it or use it as a tool. Never
  use one for anything that holds, carries or seals. Give its pattern and its
  numbers, all of them —
    {"id": "infill", "shape": "lattice", "lattice": "gyroid",
     "size": {"width": 60, "height": 40, "depth": 30, "cell": 15, "thickness": 1.5}}
  The box is centred on the position like a "box". "cell" is one repeat of the
  pattern and "thickness" the wall, between cell/20 and cell/3. The patterns:
`)
	for _, p := range latticePatterns {
		fmt.Fprintf(&b, "    %q — %s\n", p.Name, p.Says)
	}
	fmt.Fprintf(&b, "  A lattice may be at most %d triangles, about %v x width x height x depth /\n"+
		"  (cell x (cell/8)^2) for a %s: a small cell in a big box is refused, and\n"+
		"  the refusal names the cell that fits. A strut lattice and a Voronoi foam are\n"+
		"  not shapes FORGE has.\n", MaxLatticeTriangles, latticePatterns[0].TrianglesCoefficient,
		latticePatterns[0].Name)
	return strings.TrimRight(b.String(), "\n")
}
