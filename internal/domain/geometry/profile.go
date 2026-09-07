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
	// Via bows the edge ARRIVING at this point into a circular arc that passes
	// through the via on the way (see curve.go). Absent — the ordinary case —
	// is a straight edge.
	//
	// # Why a through-point and not a radius and a direction
	//
	// Three points fix a circle completely: the plane, the centre, the size and
	// which way round. A radius plus two endpoints does not — it names one arc
	// per plane through the chord, and that ambiguity was measured producing a
	// visibly wrong solid on 2026-09-05. It is also already how the kernel is
	// told about an arc, so this adds a way for a person to say what OCCT could
	// always build rather than a new thing for it to learn.
	//
	// # Why a *Point rather than a type of its own
	//
	// A via IS a point: it has coordinates, and they follow the parameters like
	// every other coordinate here, so it carries the same x_from/y_from/z_from.
	// A separate struct would be the same four fields with a second set of rules
	// about how to read them. A via's OWN Radius and Via are meaningless and are
	// refused rather than ignored — a corner radius on a point that is not a
	// corner is a different mistake from the inert ones curve.go tolerates,
	// because there is no corner there even in principle.
	Via *Point `json:"via,omitempty"`
}

// loopBows reports whether any point of an authored loop bends the edge into it.
//
// Asked of the AUTHORED points rather than of the resolved polyline because the
// checks that consult it run before resolution — and because a via that turns
// out to name no arc still means "do not judge this drawing by its polygon",
// which is the question being asked.
func loopBows(loop []Point) bool {
	for _, pt := range loop {
		if pt.Via != nil {
			return true
		}
	}
	return false
}

// resolveVia evaluates a point's through-point, if it has one.
//
// One function for the outline, the holes and the path, because a via means the
// same thing in all three and three copies of these rules would be three places
// for them to differ. planar refuses a z, which is what an outline and a hole
// need and a path does not.
//
// Returns nil and no error when there is no via, which is almost every point.
func resolveVia(pt Point, planar bool, lookup func(string) (float64, bool)) (*[3]float64, error) {
	v := pt.Via
	if v == nil {
		return nil, nil
	}
	if v.Via != nil {
		return nil, fmt.Errorf("its via carries a via of its own; an arc passes through one " +
			"point, and a via is that point rather than another edge")
	}
	if v.Radius != 0 || strings.TrimSpace(v.RadiusFrom) != "" {
		return nil, fmt.Errorf("its via carries a corner radius; a via is a point the edge " +
			"passes THROUGH, not a corner, so there is nothing there to round")
	}
	if planar && (v.Z != 0 || strings.TrimSpace(v.ZFrom) != "") {
		return nil, fmt.Errorf("its via carries a z; the arc lies in the same plane as the " +
			"drawing it bends")
	}
	x, xerr := coordinate(v.X, v.XFrom, lookup)
	y, yerr := coordinate(v.Y, v.YFrom, lookup)
	z, zerr := coordinate(v.Z, v.ZFrom, lookup)
	if err := firstOf(xerr, yerr, zerr); err != nil {
		return nil, fmt.Errorf("its via: %v", err)
	}
	out := [3]float64{x, y, z}
	return &out, nil
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

// minLoopPoints is how many points a closed drawing needs, given what its edges
// are allowed to be.
//
// Three was right while every edge was a straight line, and it stopped being
// right the moment an edge could BOW (curve.go, wave 29). A CRESCENT is two arcs
// between two points; a circular segment is one arc and one straight between the
// same two. Both enclose area, and neither can be drawn with three points
// without inventing one that is not part of the shape.
//
// So the floor is a property of the drawing rather than a constant: two when
// something bends, three when nothing does. Note that this is about the points a
// PERSON writes — every flattened list downstream still gets many more, and the
// checks over those keep using minProfilePoints.
func minLoopPoints(loop []Point) int {
	for _, pt := range loop {
		if pt.Via != nil {
			return 2
		}
	}
	return minProfilePoints
}

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
	// A WARNING is something in the drawing that could not mean anything and was
	// ignored. The part is still built from what still says what it is — the same
	// bargain a box carrying a profile has always had — and Solids says so
	// without claiming the part is missing.
	note := func(name string, details ...string) {
		for _, d := range details {
			problems = append(problems, Problem{Severity: Warning, Name: name, Detail: d})
		}
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
		if len(p.Profile) < minLoopPoints(p.Profile) {
			add(label, "is an %s with %d point(s); an outline needs at least %d to "+
				"enclose anything", shape, len(p.Profile), minLoopPoints(p.Profile))
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
		vias := make([]*[3]float64, 0, len(p.Profile))
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
			via, verr := resolveVia(pt, true, lookup)
			if verr != nil {
				add(label, "point %d %v", i+1, verr)
				bad = true
				break
			}
			pts = append(pts, [2]float64{x, y})
			radii = append(radii, r)
			vias = append(vias, via)
		}
		if bad {
			continue
		}

		// A loop that closes itself by repeating its first point is READ, not
		// refused: see withoutClosingDuplicate. Done before the duplicate check
		// below, which is the one that would otherwise refuse it.
		lifted, radii, vias, dropped, conflict := withoutClosingDuplicate(lift3D(pts), radii, vias)
		if conflict {
			add(label, "closes by repeating its first point, and the two copies carry "+
				"different corner radii; one corner cannot have two")
			continue
		}
		if dropped {
			note(label, "closes its outline by repeating its first point. The outline is "+
				"closed already, so the repeated point was dropped")
			pts = flat2D(lifted)
		}
		if len(pts) < minLoopPoints(p.Profile) {
			add(label, "is an %s with %d point(s) once its repeated closing point is dropped; "+
				"an outline needs at least %d to enclose anything",
				shape, len(pts), minLoopPoints(p.Profile))
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
		// A drawing that bows encloses area its POLYGON does not — a crescent's
		// three points are in line, and a two-point lens has no polygon at all.
		// So this check waits for the flattened form below, where the area is
		// the area of the shape rather than of the chords standing in for it.
		bowsHere := loopBows(p.Profile)
		if area := math.Abs(signedArea(pts)); !bowsHere && area < 1e-9 {
			add(label, "encloses no area; the points are all on one line")
			continue
		}
		// An outline that crosses itself is not a shape. OCCT refuses it too,
		// but saying so here names the part and reaches a reader who has no
		// kernel configured at all.
		if !bowsHere && selfIntersects(pts) {
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
		outer := polyline{Points: lift3D(pts), Radii: radii, Vias: vias, Closed: true}
		// The corner radii are checked against the outline they are drawn on: a
		// radius is only wrong in relation to the edges either side of it, so
		// this cannot be done a point at a time.
		ignored, err := outer.validate("outline")
		note(label, ignored...)
		if err != nil {
			add(label, "%v", err)
			continue
		}

		holes, holeNotes, holeProblem := d.resolvedHoles(p, lookup)
		note(label, holeNotes...)
		if holeProblem != "" {
			add(label, "%s", holeProblem)
			continue
		}
		flatOuter, flatHoles, _, ferr := flattenSection(outer, holes, Millimetre)
		if ferr != nil {
			add(label, "%v", ferr)
			continue
		}
		// Checked AGAIN, on the flattened drawing, when an edge bows.
		//
		// # Why the check above is not enough once arcs exist
		//
		// selfIntersects runs on the points as drawn, which is the right place
		// for it: it names the two points that cross and it catches the common
		// mistake, which is points in the wrong order. A bowed edge is invisible
		// to it — the arc leaves the chord, and an arc that bulges far enough
		// crosses an edge the straight version cleared by a mile. The polygon
		// is fine; the shape is not.
		//
		// So the flattened form is checked too, and only when something bows,
		// because on a drawing of straight edges this would be the same question
		// asked twice with a worse error message: the flattened outline has no
		// point numbers a person would recognise.
		if outer.bows() {
			if math.Abs(signedArea(flatOuter)) < 1e-9 {
				add(label, "encloses no area once its arcs are drawn")
				continue
			}
			if selfIntersects(flatOuter) {
				add(label, "crosses itself once its arcs are drawn. The points do not cross, so "+
					"this is a via bulging its edge across another one — move the via closer to "+
					"the line between its two ends")
				continue
			}
		}
		if problem := holesFit(flatOuter, flatHoles); problem != "" {
			add(label, "%s", problem)
			continue
		}
		// An island reads as a shape AND as a mistake with the same spelling, so
		// which reading was taken is said out loud (wave 30).
		note(label, islandNotes(flatHoles)...)

		if shape == "sweep" {
			way := make([][3]float64, 0, len(p.Path))
			bends := make([]float64, 0, len(p.Path))
			// A path is the one drawing here that is NOT confined to a plane, so
			// its vias may carry a z like its points do.
			wayVias := make([]*[3]float64, 0, len(p.Path))
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
				via, verr := resolveVia(pt, false, lookup)
				if verr != nil {
					add(label, "path point %d %v", i+1, verr)
					bad = true
					break
				}
				way = append(way, [3]float64{x, y, z})
				bends = append(bends, r)
				wayVias = append(wayVias, via)
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
			// A loop closed by repeating its first point is READ, and its radius
			// comes with it: the repeated point often carries the corner radius
			// while the original does not, and leaving it behind would mitre a
			// corner somebody asked to be bent. See withoutClosingDuplicate.
			if p.PathClosed {
				cleaned, radii, cleanedVias, dropped, conflict :=
					withoutClosingDuplicate(way, bends, wayVias)
				if conflict {
					add(label, "closes its path by repeating its first point, and the two "+
						"copies carry different bend radii; one corner cannot have two")
					continue
				}
				if dropped {
					note(label, "closes its path by repeating its first point. A closed path "+
						"joins its last point to its first already, so the repeated point "+
						"was dropped")
					way, bends, wayVias = cleaned, radii, cleanedVias
				}
				if len(way) < minClosedPathPoints {
					add(label, "is a sweep round a closed path of %d points once its repeated "+
						"closing point is dropped; a loop needs at least %d",
						len(way), minClosedPathPoints)
					continue
				}
			}
			route := polyline{Points: way, Radii: bends, Vias: wayVias, Closed: p.PathClosed}
			ignoredBends, berr := route.validate("path")
			note(label, ignoredBends...)
			if berr != nil {
				add(label, "%v", berr)
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
func (d *Document) resolvedHoles(p Part, lookup func(string) (float64, bool)) ([]polyline, []string, string) {
	out := make([]polyline, 0, len(p.Holes))
	var notes []string
	for n, hole := range p.Holes {
		if len(hole) < minLoopPoints(hole) {
			return nil, notes, fmt.Sprintf("hole %d has %d point(s); a hole needs at least %d to "+
				"enclose anything", n+1, len(hole), minLoopPoints(hole))
		}
		pts := make([][2]float64, 0, len(hole))
		radii := make([]float64, 0, len(hole))
		vias := make([]*[3]float64, 0, len(hole))
		for i, pt := range hole {
			if pt.Z != 0 || strings.TrimSpace(pt.ZFrom) != "" {
				return nil, notes, fmt.Sprintf("hole %d point %d carries a z; a hole lies in the "+
					"outline's own plane", n+1, i+1)
			}
			x, xerr := coordinate(pt.X, pt.XFrom, lookup)
			y, yerr := coordinate(pt.Y, pt.YFrom, lookup)
			r, rerr := coordinate(pt.Radius, pt.RadiusFrom, lookup)
			if err := firstOf(xerr, yerr, rerr); err != nil {
				return nil, notes, fmt.Sprintf("hole %d point %d: %v", n+1, i+1, err)
			}
			via, verr := resolveVia(pt, true, lookup)
			if verr != nil {
				return nil, notes, fmt.Sprintf("hole %d point %d %v", n+1, i+1, verr)
			}
			pts = append(pts, [2]float64{x, y})
			radii = append(radii, r)
			vias = append(vias, via)
		}
		// A hole closed by repeating its first point is read the same way an
		// outline's is, and for the same reason.
		lifted, cleaned, cleanedVias, dropped, conflict :=
			withoutClosingDuplicate(lift3D(pts), radii, vias)
		if conflict {
			return nil, notes, fmt.Sprintf("hole %d closes by repeating its first point, and "+
				"the two copies carry different corner radii; one corner cannot have two", n+1)
		}
		if dropped {
			notes = append(notes, fmt.Sprintf("closes hole %d by repeating its first point. A "+
				"hole is a closed loop already, so the repeated point was dropped", n+1))
			pts, radii, vias = flat2D(lifted), cleaned, cleanedVias
		}
		if len(pts) < minLoopPoints(hole) {
			return nil, notes, fmt.Sprintf("hole %d has %d point(s) once its repeated closing "+
				"point is dropped; a hole needs at least %d to enclose anything",
				n+1, len(pts), minLoopPoints(hole))
		}
		if i, j, dup := duplicatePoint(pts); dup {
			return nil, notes, fmt.Sprintf("hole %d has points %d and %d the same (%g, %g); a loop "+
				"cannot have an edge of zero length", n+1, i+1, j+1, pts[i][0], pts[i][1])
		}
		if !loopBows(hole) && math.Abs(signedArea(pts)) < 1e-9 {
			return nil, notes, fmt.Sprintf("hole %d encloses no area; its points are all on one line", n+1)
		}
		if !loopBows(hole) && selfIntersects(pts) {
			return nil, notes, fmt.Sprintf("hole %d crosses itself, so it does not enclose a single "+
				"region; check the order of its points", n+1)
		}
		loop := polyline{Points: lift3D(pts), Radii: radii, Vias: vias, Closed: true}
		ignored, err := loop.validate(fmt.Sprintf("hole %d", n+1))
		notes = append(notes, ignored...)
		if err != nil {
			return nil, notes, err.Error()
		}
		out = append(out, loop)
	}
	return out, notes, ""
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
			// A loop inside another loop is an ISLAND — solid material standing
			// in a void — and it is read that way rather than refused (wave 30).
			// See nestLoops for the rule and for why it is not a new field.
			//
			// Left as a NOTE and not silent, because it is a real shape and a
			// real mistake with the same spelling: an annular slot with a post
			// in it, and a bolt hole somebody accidentally drew inside a pocket,
			// arrive here looking identical. The reader is told which reading
			// was taken.
			_ = j
		}
	}
	return ""
}

// islandNotes says which holes were read as islands.
//
// # Why this is reported rather than assumed understood
//
// A hole drawn inside another hole is two things at once: an annular slot with a
// post in the middle — a real part, and the reason islands are now read — and a
// bolt hole somebody put inside a pocket by mistake. They are spelled
// identically, and this build cannot tell them apart, so it takes the reading
// the drawing supports and NAMES it. Somebody who meant the second one sees the
// sentence and moves the hole.
func islandNotes(holes [][][2]float64) []string {
	var out []string
	for i, hole := range holes {
		if len(hole) == 0 {
			continue
		}
		for j, other := range holes {
			if i == j || len(other) == 0 || !insideLoop(hole[0], other) {
				continue
			}
			out = append(out, fmt.Sprintf("draws hole %d inside hole %d, so it is read as an "+
				"ISLAND — solid material standing in the void, the way a post stands in an "+
				"annular slot. If it was meant as a second bore, move it outside hole %d",
				i+1, j+1, j+1))
			break
		}
	}
	return out
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
