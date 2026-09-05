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
// An outline is a closed 2D shape, and there are three things worth doing with
// one. EXTRUDING it sweeps it along an axis, which is where most fabricated
// parts begin. REVOLVING it turns it about an axis, which is where every turned
// one does: a shaft, a boss, a flange, a pulley, a dome, a nozzle. SWEEPING it
// carries it along a path, which is where everything that BENDS comes from — a
// pipe run, a handrail, a cable tray, a wire form (see sweep.go).
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
	X float64 `json:"x"`
	Y float64 `json:"y"`
	// Z is read ONLY on a sweep's path, which is the one list of points here
	// that is not confined to a plane. An outline carrying one is refused rather
	// than flattened: a z somebody typed is a z they meant, and silently
	// dropping it draws a different shape from the one they described.
	Z float64 `json:"z,omitempty"`
	// Radius rounds this corner: an arc of this radius, tangent to both edges
	// meeting here (see curve.go). Zero, and absent, mean a sharp corner.
	//
	// The same field on an outline point and on a path point, because it is the
	// same idea in both — and on a path it is the BEND RADIUS, which is the
	// number a tube bender is set to rather than a decoration.
	Radius     float64 `json:"radius,omitempty"`
	XFrom      string  `json:"x_from,omitempty"`
	YFrom      string  `json:"y_from,omitempty"`
	ZFrom      string  `json:"z_from,omitempty"`
	RadiusFrom string  `json:"radius_from,omitempty"`
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

// outlineShapes is the closed set of shapes that are drawn from an outline
// rather than from dimensions.
//
// One table, because "does this shape read a profile" is asked in four places
// and a shape that is in three of them is a part that resolves, draws, and then
// exports as a bounding box.
var outlineShapes = map[string]bool{"extrusion": true, "revolve": true, "sweep": true}

// resolvedProfiles evaluates every outline and every sweep path, and says what
// is wrong with them.
//
// Returned in DOCUMENT units. Solids converts to millimetres afterwards, in the
// one place that owns that conversion.
//
// Profiles and paths come back from ONE pass because they are one question: a
// sweep with an unreadable path is exactly as absent from the file as one with
// an unreadable outline, and a caller that had to ask twice would eventually ask
// once.
// outline is a resolved section: the loop around it, and the loops inside it.
type outline struct {
	Outer polyline
	Holes []polyline
}

func (d *Document) resolvedProfiles() (map[string]outline, map[string]polyline, []Problem) {
	if d == nil {
		return nil, nil, nil
	}
	res := d.Resolve()
	lookup := func(n string) (float64, bool) {
		v, ok := res.Values[n]
		return v.Number, ok
	}

	out := map[string]outline{}
	paths := map[string]polyline{}
	var problems []Problem
	add := func(name, format string, args ...any) {
		problems = append(problems, Problem{Severity: Error, Name: name,
			Detail: fmt.Sprintf(format, args...)})
	}

	for _, p := range d.Parts {
		shape := strings.ToLower(strings.TrimSpace(p.Shape))
		usesOutline := outlineShapes[shape]
		if !usesOutline && len(p.Profile) == 0 && len(p.Path) == 0 && len(p.Holes) == 0 && !p.PathClosed {
			continue
		}
		label := p.Label()
		if !usesOutline {
			// A profile on a box is not a box with a profile: it is somebody
			// meaning one thing and writing another, and guessing which would
			// put a shape in the file that nobody asked for.
			//
			// Holes are named here too. A box carrying holes and nothing else
			// used to fall through this check and be built as a plain box —
			// found by a test on 2026-09-05, and the failure is a part exported
			// solid with its voids silently dropped.
			carries := "an outline"
			switch {
			case len(p.Profile) > 0:
			case len(p.Holes) > 0:
				carries = "holes"
			default:
				carries = "a path"
			}
			add(label, "carries %s but its shape is %q; an outline and the holes in it are "+
				"only used when the shape is \"extrusion\", \"revolve\" or \"sweep\", and a "+
				"path only when it is \"sweep\"", carries, p.Shape)
			continue
		}
		if shape != "sweep" && (len(p.Path) > 0 || p.PathClosed) {
			// Same reasoning one line up, from the other side: a path on an
			// extrusion is somebody who meant a sweep, and building the
			// extrusion would quietly throw the path away.
			add(label, "is an %s but carries a path; an outline is only carried along a path "+
				"when the shape is \"sweep\"", shape)
			continue
		}
		if len(p.Profile) < minProfilePoints {
			add(label, "is an %s with %d point(s); an outline needs at least %d to "+
				"enclose anything", shape, len(p.Profile), minProfilePoints)
			continue
		}
		if shape == "sweep" && len(p.Path) < minPathPoints {
			add(label, "is a sweep with a path of %d point(s); a path needs at least %d, "+
				"because one point is a place and not a direction", len(p.Path), minPathPoints)
			continue
		}
		if shape == "sweep" && p.PathClosed && len(p.Path) < minClosedPathPoints {
			// Two points closed is a line there and a line back, which sweeps
			// the section through itself and encloses nothing.
			add(label, "is a sweep round a closed path of %d points; a loop needs at least "+
				"%d, because two points closed is a line drawn twice",
				len(p.Path), minClosedPathPoints)
			continue
		}
		if _, known := revolveAxes[strings.ToLower(strings.TrimSpace(p.Axis))]; !known && shape == "revolve" {
			add(label, "turns about %q, which is not an axis of its own outline; a revolve "+
				"turns about \"y\" (the default) or \"x\"", p.Axis)
			continue
		}

		pts := make([][2]float64, 0, len(p.Profile))
		radii := make([]float64, 0, len(p.Profile))
		bad := false
		for i, pt := range p.Profile {
			if pt.Z != 0 || strings.TrimSpace(pt.ZFrom) != "" {
				// Dropping it would draw a flat outline where somebody described
				// a shape that leaves its plane, and say nothing. An outline
				// that wants a z is a sweep whose path has not been written.
				add(label, "point %d carries a z; an outline lies in the part's own XY plane, "+
					"and z is only read on a sweep's path", i+1)
				bad = true
				break
			}
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
			r, err := coordinate(pt.Radius, pt.RadiusFrom, lookup)
			if err != nil {
				add(label, "point %d: corner radius %v", i+1, err)
				bad = true
				break
			}
			pts = append(pts, [2]float64{x, y})
			radii = append(radii, r)
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
		outer := polyline{Points: lift3D(pts), Radii: radii, Closed: true}
		// The corner radii are checked against the outline they are drawn on: a
		// radius is only wrong in relation to the edges either side of it, so
		// this cannot be done a point at a time.
		if err := outer.validate("outline"); err != nil {
			add(label, "%v", err)
			continue
		}

		holes, holeProblem := d.resolvedHoles(p, lookup)
		if holeProblem != "" {
			add(label, "%s", holeProblem)
			continue
		}
		flatOuter, flatHoles, _, ferr := flattenSection(outer, holes, Millimetre)
		if ferr != nil {
			add(label, "%v", ferr)
			continue
		}
		if problem := holesFit(flatOuter, flatHoles); problem != "" {
			add(label, "%s", problem)
			continue
		}

		if shape == "sweep" {
			way := make([][3]float64, 0, len(p.Path))
			bends := make([]float64, 0, len(p.Path))
			for i, pt := range p.Path {
				x, xerr := coordinate(pt.X, pt.XFrom, lookup)
				y, yerr := coordinate(pt.Y, pt.YFrom, lookup)
				z, zerr := coordinate(pt.Z, pt.ZFrom, lookup)
				r, rerr := coordinate(pt.Radius, pt.RadiusFrom, lookup)
				if err := firstOf(xerr, yerr, zerr, rerr); err != nil {
					add(label, "path point %d: %v", i+1, err)
					bad = true
					break
				}
				way = append(way, [3]float64{x, y, z})
				bends = append(bends, r)
			}
			if bad {
				continue
			}
			// A repeated point, checked on what was WRITTEN rather than on what
			// flattening produced. Flattening drops consecutive duplicates on
			// purpose — two arcs that meet exactly, at the end of a slot, each
			// produce the point where they touch — so a duplicate somebody typed
			// would be swallowed by that and never reported. It is almost always
			// a copied line they forgot to edit, and it is named the same way a
			// repeated outline point is.
			for i := 0; i+1 < len(way); i++ {
				if same(way[i], way[i+1]) {
					add(label, "path points %d and %d are the same (%g, %g, %g); a path "+
						"cannot have a segment of zero length",
						i+1, i+2, way[i][0], way[i][1], way[i][2])
					bad = true
					break
				}
			}
			if bad {
				continue
			}
			route := polyline{Points: way, Radii: bends, Closed: p.PathClosed}
			if err := route.validate("path"); err != nil {
				add(label, "%v", err)
				continue
			}
			// Built here, and the result thrown away, because every fault a path
			// can have is a fault of the path AND the outline together: whether
			// a bend is too tight depends on how wide the outline is. Refused
			// rather than drawn partially, because this is the path the kernel
			// reads — and the one fault OCCT does not catch, a fold, comes back
			// from it as a plausible solid with the wrong volume.
			//
			// Checked on the FLATTENED forms, which is where the fault would
			// actually show: a bend radius smaller than the outline is wide
			// folds the inside of the bend through itself, and on the flattened
			// path that is exactly a ring that fails to advance.
			flatPath, _, perr := route.flatten("path", Millimetre)
			if perr != nil {
				add(label, "%v", perr)
				continue
			}
			if _, _, err := sweptSections(append([][][2]float64{flatOuter}, flatHoles...), flatPath, p.PathClosed); err != nil {
				add(label, "%v", err)
				continue
			}
			paths[p.ID] = route
		}
		out[p.ID] = outline{Outer: outer, Holes: holes}
	}
	sortProblems(problems)
	return out, paths, problems
}

// firstOf is the first error of several, so three coordinates can be evaluated
// and reported as one point rather than as three lines about the same point.
func firstOf(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// ProfileProblems is everything wrong with this document's outlines — and with
// the paths the swept ones follow, which are the same claim: a part that is not
// in the shape.
//
// Exported so the conversation boundary can tell a reader, for the same reason
// the feature and parameter problems are: an extrusion that does not resolve is
// a part that is simply NOT THERE, and the render looks like a design with a
// piece missing rather than like an error.
func (d *Document) ProfileProblems() []Problem {
	_, _, problems := d.resolvedProfiles()
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

// resolvedHoles evaluates the loops inside an outline, and says what is wrong
// with them.
//
// Returns a problem STRING rather than appending, because a hole is only ever
// reported against the part that carries it — there is nothing else a reader
// could act on — and a hole that cannot be read takes the whole part out of the
// file rather than leaving a solid one with its bore missing.
func (d *Document) resolvedHoles(p Part, lookup func(string) (float64, bool)) ([]polyline, string) {
	out := make([]polyline, 0, len(p.Holes))
	for n, hole := range p.Holes {
		if len(hole) < minProfilePoints {
			return nil, fmt.Sprintf("hole %d has %d point(s); a hole needs at least %d to "+
				"enclose anything", n+1, len(hole), minProfilePoints)
		}
		pts := make([][2]float64, 0, len(hole))
		radii := make([]float64, 0, len(hole))
		for i, pt := range hole {
			if pt.Z != 0 || strings.TrimSpace(pt.ZFrom) != "" {
				return nil, fmt.Sprintf("hole %d point %d carries a z; a hole lies in the "+
					"outline's own plane", n+1, i+1)
			}
			x, xerr := coordinate(pt.X, pt.XFrom, lookup)
			y, yerr := coordinate(pt.Y, pt.YFrom, lookup)
			r, rerr := coordinate(pt.Radius, pt.RadiusFrom, lookup)
			if err := firstOf(xerr, yerr, rerr); err != nil {
				return nil, fmt.Sprintf("hole %d point %d: %v", n+1, i+1, err)
			}
			pts = append(pts, [2]float64{x, y})
			radii = append(radii, r)
		}
		if i, j, dup := duplicatePoint(pts); dup {
			return nil, fmt.Sprintf("hole %d has points %d and %d the same (%g, %g); a loop "+
				"cannot have an edge of zero length", n+1, i+1, j+1, pts[i][0], pts[i][1])
		}
		if math.Abs(signedArea(pts)) < 1e-9 {
			return nil, fmt.Sprintf("hole %d encloses no area; its points are all on one line", n+1)
		}
		if selfIntersects(pts) {
			return nil, fmt.Sprintf("hole %d crosses itself, so it does not enclose a single "+
				"region; check the order of its points", n+1)
		}
		loop := polyline{Points: lift3D(pts), Radii: radii, Closed: true}
		if err := loop.validate(fmt.Sprintf("hole %d", n+1)); err != nil {
			return nil, err.Error()
		}
		out = append(out, loop)
	}
	return out, ""
}

// holesFit checks that every hole is really a hole: inside the outline, and not
// running into another one.
//
// # Why this is checked here and not left to OCCT
//
// A face whose inner wire pokes out of its outer wire is not a face. OCCT will
// either refuse it — with a message that names no loop and no point — or, worse,
// build something: the tessellator's bridge algorithm cannot find a valid bridge
// and gives up, so the viewport draws a partial shape while the kernel exports a
// whole one, and nothing says they differ.
//
// Checked on the FLATTENED loops, because a corner radius moves the boundary. A
// hole that fits inside the drawn corners of an outline may not fit inside the
// rounded ones, and it is the rounded ones that are the part.
func holesFit(outer [][2]float64, holes [][][2]float64) string {
	for i, hole := range holes {
		for k, pt := range hole {
			if !insideLoop(pt, outer) {
				return fmt.Sprintf("hole %d is not inside the outline — its point %d is at "+
					"(%g, %g), which is outside. A hole is a loop WITHIN the outline; a "+
					"shape cut from the edge is part of the outline itself",
					i+1, k+1, pt[0], pt[1])
			}
		}
		if loopsCross(hole, outer) {
			return fmt.Sprintf("hole %d crosses the outline, so what is left is not a single "+
				"region", i+1)
		}
		for j := i + 1; j < len(holes); j++ {
			if loopsCross(hole, holes[j]) {
				return fmt.Sprintf("holes %d and %d cross each other; two holes that overlap "+
					"are one hole, and it has to be drawn as one loop", i+1, j+1)
			}
			if insideLoop(hole[0], holes[j]) || insideLoop(holes[j][0], hole) {
				return fmt.Sprintf("hole %d is inside hole %d. An island in a hole is a "+
					"second outline, and there is no vocabulary for one here", i+1, j+1)
			}
		}
	}
	return ""
}

func loopsCross(a, b [][2]float64) bool {
	for i := range a {
		p, q := a[i], a[(i+1)%len(a)]
		for j := range b {
			if segmentsCross(p, q, b[j], b[(j+1)%len(b)]) {
				return true
			}
		}
	}
	return false
}
