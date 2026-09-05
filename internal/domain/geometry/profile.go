package geometry

import (
	"fmt"
	"math"
	"strings"
)

// Profiles: the shape that is not a primitive.
//
// # What was missing
//
// Every solid this system could describe started from a box, a cylinder, a cone,
// a sphere or a plane. That is enough for a plate with holes in it and nothing
// else: an L-bracket, a T-section, a channel, a gusset — the ordinary
// cross-sections most fabricated parts actually are — could not be said at all.
//
// An extrusion is a closed 2D outline swept along an axis. It is the operation
// almost every real part begins with, and it is what makes the difference
// between "primitives with material removed" and a vocabulary somebody can
// design in.
//
// # Where the outline lives, and why it is NOT re-centred
//
// The points are in the part's own XY plane, and the part's position places that
// plane's ORIGIN — not the outline's centre, which is how every other shape here
// behaves.
//
// The inconsistency is deliberate. A profile's coordinates are written by hand:
// somebody says the corner is at (0, 0) and the flange runs to (40, 0), and then
// positions a bolt hole against those numbers. Re-centring the outline on its own
// bounding box would move every one of them by an amount that depends on the
// outline's shape — so adding a point to the far end of a flange would silently
// shift the holes. Centring is right for a box, whose dimensions are symmetric
// by construction. It is wrong for a drawing.
//
// The extrude direction IS centred, from -depth/2 to +depth/2, because depth is
// a size like any other and behaves like a box's height.

// Point is one vertex of a profile, in the part's local XY plane.
//
// XFrom and YFrom are the same coordinates as EXPRESSIONS and win when both are
// given, for the reason size_from wins over size: one is the relationship and
// the other is a snapshot of it. A profile whose points do not follow the
// parameters is a drawing that stops being true the first time somebody changes
// one.
type Point struct {
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	XFrom string  `json:"x_from,omitempty"`
	YFrom string  `json:"y_from,omitempty"`
}

// minProfilePoints is three, because two points enclose no area and one is not
// an outline. A "profile" with fewer is not a degenerate shape to be drawn
// thinly — it is a document that means nothing, and it is refused.
const minProfilePoints = 3

// resolvedProfiles evaluates every extrusion's outline, and says what is wrong.
//
// Returned in DOCUMENT units. Solids converts to millimetres afterwards, in the
// one place that owns that conversion.
func (d *Document) resolvedProfiles() (map[string][][2]float64, []Problem) {
	if d == nil {
		return nil, nil
	}
	res := d.Resolve()
	lookup := func(n string) (float64, bool) {
		v, ok := res.Values[n]
		return v.Number, ok
	}

	out := map[string][][2]float64{}
	var problems []Problem
	add := func(name, format string, args ...any) {
		problems = append(problems, Problem{Severity: Error, Name: name,
			Detail: fmt.Sprintf(format, args...)})
	}

	for _, p := range d.Parts {
		isExtrusion := strings.EqualFold(p.Shape, "extrusion")
		if !isExtrusion && len(p.Profile) == 0 {
			continue
		}
		label := p.Label()
		if !isExtrusion {
			// A profile on a box is not a box with a profile: it is somebody
			// meaning one thing and writing another, and guessing which would
			// put a shape in the file that nobody asked for.
			add(label, "carries a profile but its shape is %q; an outline is only extruded "+
				"when the shape is \"extrusion\"", p.Shape)
			continue
		}
		if len(p.Profile) < minProfilePoints {
			add(label, "is an extrusion with %d point(s); an outline needs at least %d to "+
				"enclose anything", len(p.Profile), minProfilePoints)
			continue
		}

		pts := make([][2]float64, 0, len(p.Profile))
		bad := false
		for i, pt := range p.Profile {
			x, err := coordinate(pt.X, pt.XFrom, lookup)
			if err != nil {
				add(label, "point %d: x %v", i+1, err)
				bad = true
				break
			}
			y, err := coordinate(pt.Y, pt.YFrom, lookup)
			if err != nil {
				add(label, "point %d: y %v", i+1, err)
				bad = true
				break
			}
			pts = append(pts, [2]float64{x, y})
		}
		if bad {
			continue
		}

		// A repeated point is a zero-length edge, which OCCT refuses and which
		// is almost always a copied line somebody forgot to edit. Named, with
		// the index, because in a list of eight coordinate pairs "one of these
		// is duplicated" is not something a person can act on.
		if i, j, dup := duplicatePoint(pts); dup {
			add(label, "points %d and %d are the same (%g, %g); an outline cannot have an "+
				"edge of zero length", i+1, j+1, pts[i][0], pts[i][1])
			continue
		}
		if area := math.Abs(signedArea(pts)); area < 1e-9 {
			add(label, "encloses no area; the points are all on one line")
			continue
		}
		// An outline that crosses itself is not a shape. OCCT refuses it too,
		// but saying so here names the part and reaches a reader who has no
		// kernel configured at all.
		if selfIntersects(pts) {
			add(label, "crosses itself, so it does not enclose a single region; check the "+
				"order of the points")
			continue
		}
		out[p.ID] = pts
	}
	sortProblems(problems)
	return out, problems
}

// ProfileProblems is everything wrong with this document's outlines.
//
// Exported so the conversation boundary can tell a reader, for the same reason
// the feature and parameter problems are: an extrusion that does not resolve is
// a part that is simply NOT THERE, and the render looks like a design with a
// piece missing rather than like an error.
func (d *Document) ProfileProblems() []Problem {
	_, problems := d.resolvedProfiles()
	return problems
}

func coordinate(literal float64, expr string, lookup func(string) (float64, bool)) (float64, error) {
	if strings.TrimSpace(expr) == "" {
		if math.IsNaN(literal) || math.IsInf(literal, 0) {
			return 0, fmt.Errorf("is not a finite number")
		}
		return literal, nil
	}
	node, err := parseExpression(expr)
	if err != nil {
		return 0, fmt.Errorf("%q cannot be read: %v", expr, err)
	}
	value, err := node.Eval(lookup)
	if err != nil {
		return 0, fmt.Errorf("%q does not evaluate: %v", expr, err)
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, fmt.Errorf("%q does not produce a finite number", expr)
	}
	return value, nil
}

func duplicatePoint(pts [][2]float64) (int, int, bool) {
	for i := 0; i < len(pts); i++ {
		j := (i + 1) % len(pts)
		if math.Abs(pts[i][0]-pts[j][0]) < 1e-12 && math.Abs(pts[i][1]-pts[j][1]) < 1e-12 {
			return i, j, true
		}
	}
	return 0, 0, false
}

// signedArea is the shoelace formula. Positive is counter-clockwise.
//
// Used for two different questions and worth keeping one of: whether the outline
// encloses anything at all, and which way round it is wound — which decides
// which way the extruded faces point, and an inside-out solid is a defect this
// repository has already had once (docs/bugfix/2026-09-02-exported-meshes-were-inside-out.md).
func signedArea(pts [][2]float64) float64 {
	var a float64
	for i := range pts {
		j := (i + 1) % len(pts)
		a += pts[i][0]*pts[j][1] - pts[j][0]*pts[i][1]
	}
	return a / 2
}
