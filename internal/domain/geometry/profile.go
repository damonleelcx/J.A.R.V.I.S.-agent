package geometry

import (
	"fmt"
	"math"
	"strings"
)

// Profiles: the shapes that are not primitives.
//
// # What was missing
//
// Every solid this system could describe started from a box, a cylinder, a cone,
// a sphere or a plane. That is enough for a plate with holes in it and nothing
// else: an L-bracket, a T-section, a channel, a gusset — the ordinary
// cross-sections most fabricated parts actually are — could not be said at all.
//
// An outline is a closed 2D shape, and there are two things worth doing with
// one. EXTRUDING it sweeps it along an axis, which is where most fabricated
// parts begin. REVOLVING it turns it about an axis, which is where every turned
// one does: a shaft, a boss, a flange, a pulley, a dome, a nozzle.
//
// Between them they are the difference between "primitives with material
// removed" and a vocabulary somebody can design in.
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

// revolveAxes is the closed set of axes an outline may be turned about.
//
// Only X and Y, because the outline lies in the XY plane and those are the two
// axes IN it. Turning it about Z would sweep it out of its own plane, which
// produces a shape nobody means by "revolve".
//
// Y is the default because Y is up here, and a turned part standing on its axis
// is what somebody pictures.
var revolveAxes = map[string]string{"": "y", "y": "y", "x": "x"}

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
		shape := strings.ToLower(strings.TrimSpace(p.Shape))
		usesOutline := shape == "extrusion" || shape == "revolve"
		if !usesOutline && len(p.Profile) == 0 {
			continue
		}
		label := p.Label()
		if !usesOutline {
			// A profile on a box is not a box with a profile: it is somebody
			// meaning one thing and writing another, and guessing which would
			// put a shape in the file that nobody asked for.
			add(label, "carries a profile but its shape is %q; an outline is only used "+
				"when the shape is \"extrusion\" or \"revolve\"", p.Shape)
			continue
		}
		if len(p.Profile) < minProfilePoints {
			add(label, "is an %s with %d point(s); an outline needs at least %d to "+
				"enclose anything", shape, len(p.Profile), minProfilePoints)
			continue
		}
		if _, known := revolveAxes[strings.ToLower(strings.TrimSpace(p.Axis))]; !known && shape == "revolve" {
			add(label, "turns about %q, which is not an axis of its own outline; a revolve "+
				"turns about \"y\" (the default) or \"x\"", p.Axis)
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
		if shape == "revolve" {
			// An outline that crosses its own axis sweeps through itself, and
			// what comes out is not a solid. OCCT refuses it with "BRep_API:
			// command not done", which names nothing a person can act on — so
			// it is caught here, where the axis and the offending coordinate can
			// both be named.
			axis := revolveAxes[strings.ToLower(strings.TrimSpace(p.Axis))]
			if first, second, crosses := crossesAxis(pts, axis); crosses {
				other := map[string]string{"y": "x", "x": "y"}[axis]
				add(label, "is revolved about %s, so every point must be on one side of that "+
					"axis — point %d has %s = %g and point %d has %s = %g. An outline with "+
					"points on both sides sweeps through itself",
					axis, first.index+1, other, first.value, second.index+1, other, second.value)
				continue
			}
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

// crossesAxis reports the first point on the wrong side of a revolve's axis.
//
// Touching is allowed and common: a dome's outline meets the axis at its apex,
// and a cone's at its point. What is refused is an outline with points on BOTH
// sides, which sweeps through itself.
//
// The sign is taken from the first point that is off the axis, so an outline
// drawn entirely in negative x is legal — it is the same shape, mirrored.
type axisPoint struct {
	index int
	value float64
}

func crossesAxis(pts [][2]float64, axis string) (first, second axisPoint, crosses bool) {
	coord := func(p [2]float64) float64 {
		if axis == "x" {
			return p[1] // turning about X: the radius is y
		}
		return p[0] // turning about Y: the radius is x
	}
	// BOTH offending points are returned, because naming one is arbitrary: the
	// fault is not that a particular coordinate is negative, it is that two of
	// them disagree. "Point 2 has x = 10" reads as an accusation against a point
	// that may be perfectly correct — it is the pair that is wrong.
	for i, p := range pts {
		v := coord(p)
		if math.Abs(v) < 1e-12 {
			continue // on the axis, which is allowed and usual
		}
		if first == (axisPoint{}) && i >= 0 {
			first = axisPoint{index: i, value: v}
			continue
		}
		if (v > 0) != (first.value > 0) {
			return first, axisPoint{index: i, value: v}, true
		}
	}
	return axisPoint{}, axisPoint{}, false
}

// RevolveAxis resolves a part's revolve axis to "y" or "x".
//
// One reader for the table, so the tessellator, the kernel bridge and the
// measurement path cannot each have their own opinion about what an empty axis
// means.
func RevolveAxis(p Part) string {
	if a, ok := revolveAxes[strings.ToLower(strings.TrimSpace(p.Axis))]; ok {
		return a
	}
	return "y"
}
