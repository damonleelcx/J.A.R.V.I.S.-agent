package geometry

import (
	"fmt"
	"math"
)

// Curves: the drawing, once its corners have been worked out.
//
// # What was missing
//
// An outline and a path were both straight lines between points. Everything
// people actually make is full of radii: a plate has rounded corners, a slot
// ends in a semicircle, and a bent tube has a BEND RADIUS — which is not a
// decoration but the number the tube bender is set to, and the thing that
// decides whether the tube survives being bent at all.
//
// # Why a corner radius and not an arc segment
//
// The obvious contract is "this edge is an arc of radius r". It has two
// problems, and the second is fatal here.
//
// A radius and two endpoints do not determine an arc in three dimensions — they
// determine a whole family of them, one per plane through the chord. Measured
// 2026-09-05: build123d's RadiusArc, given two points in the XZ plane, returned
// an arc leaning out of that plane entirely (tangent (0.267, 0.535, 0.802) where
// (0, 0, 1) was wanted), and the sweep along it missed its own volume by 5% with
// the section visibly distorted. Nothing was wrong with OCCT. The request was
// ambiguous and it answered a different question.
//
// A CORNER RADIUS is not ambiguous. "Round this corner with r=25" names one arc:
// tangent to both edges, in the plane those two edges already define, on the
// inside of the turn. It is also the number a person actually has — a drawing
// says R5, a bender is set to a centreline radius — and it is the same idea in
// an outline and in a path, so there is one concept and one implementation
// rather than two that drift.
//
// What it cannot say is an arc that is NOT tangent to its neighbours: a crescent,
// a lens, an arc meeting a straight edge at an angle. Those need a vocabulary
// with a plane in it, and no model has been asked for one yet.
//
// # What a corner radius CAN say, which is more than it sounds
//
// A slot is a rectangle with the radius set to half its width on all four
// corners: the two arcs at each end meet exactly, and the straight between them
// vanishes. A washer face, a rounded gusset, a stadium, a D-section and a
// racetrack all fall out of the same one number.
//
// # It overlaps the fillet feature, and that is not an accident to be tidied up
//
// `fillet` already rounds edges of the built solid, and on an extrusion,
// `edges: "vertical"` rounds the outline's corners all the way through. So for
// that ONE case there are now two ways to say the same thing, and the difference
// is real: a corner radius is part of the DRAWING — it is what the section is,
// so it follows the section wherever the section goes, round every bend of a
// sweep and all the way round a revolve. A fillet is an operation performed on
// the finished solid, chosen by rule, and there is no rule that names one corner
// of an outline. Prefer the radius when the shape has it; prefer the fillet when
// the edge only exists once the solid does.
//
// # Where the arc actually gets built
//
// Twice, deliberately, and they are not the same fidelity:
//
//   - the CAD kernel gets the TRUE arc, as three points on it, which is what
//     makes an exported bend a real cylindrical surface rather than a
//     many-sided prism. Three points fix the plane, so the ambiguity above
//     cannot come back.
//   - the tessellators — the viewport and the mesh exporters — flatten it into
//     chords at the same fineness a cylinder gets, and REPORT the deviation, in
//     the units the assembly is drawn in. That is the bargain every curved shape
//     here already makes.

// arcTolerance is when two directions count as the same one. Below it there is
// no corner to round: the arc would have no angle, its centre would be at
// infinity, and every formula here divides by zero.
const arcTolerance = 1e-9

// Curve is a resolved outline or path: where it starts, and the edges that
// follow. Millimetres, in the part's own frame.
//
// # Why the kernel is given this rather than a list of points
//
// A list of points cannot say "and this corner is an arc", and the whole reason
// the kernel exists is that it builds real surfaces. Sending points would make
// every rounded corner a flat facet in the exported STEP — a mesh wearing a
// solid model's extension, which this repository refuses to write.
//
// One type for outlines AND paths, in three dimensions for both, because they
// are the same thing: an ordered run of edges. An outline's z is zero. Two types
// would mean two builders in the kernel and two chances to disagree.
type Curve struct {
	Start [3]float64  `json:"start"`
	Edges []CurveEdge `json:"edges"`
	// Closed says the last edge returns to Start. An outline is always closed; a
	// path is closed only when it was asked to be.
	Closed bool `json:"closed,omitempty"`
}

// CurveEdge is one edge: a straight line to To, or — when Via is present — a
// circular arc from where the last edge ended, through Via, to To.
//
// Three points and not a centre-and-angle, because three points on an arc fix it
// completely and unambiguously in three dimensions, and a radius does not.
type CurveEdge struct {
	To  [3]float64  `json:"to"`
	Via *[3]float64 `json:"via,omitempty"`
}

// polyline is a resolved drawing before its corners become arcs: the points, the
// corner radius at each, and whether it comes back to where it started.
//
// One type for an outline and for a path, because the corner arithmetic does not
// care which it is looking at. What differs is only Closed — and that an
// outline's Closed is always true, while a path is told.
type polyline struct {
	Points [][3]float64
	Radii  []float64
	Closed bool
}

// flatten is the polyline the tessellators draw, and what it cost to draw it
// that way. The deviation is nil when nothing is rounded, which is not a
// rounding of zero but the absence of any curve to approximate.
func (p polyline) flatten(what string, unit Unit) ([][3]float64, *Deviation, error) {
	flat, radius, angle, segments, err := flattenCurve(p.Points, p.Radii, p.Closed, what)
	if err != nil {
		return nil, nil, err
	}
	return flat, arcDeviation(radius, angle, segments, unit), nil
}

// validate is the corner arithmetic run for what it has to SAY: what is wrong,
// and what was given and could not mean anything.
func (p polyline) validate(what string) ([]string, error) {
	_, ignored, err := roundedCorners(p.Points, p.Radii, p.Closed, what)
	return ignored, err
}

// exact is the drawing the CAD kernel builds, arcs and all.
func (p polyline) exact(what string) (Curve, error) {
	return exactCurve(p.Points, p.Radii, p.Closed, what)
}

// rounded reports whether any corner has a radius, which is the difference
// between "this is a polyhedron and every builder agrees to the last bit" and
// "this has a curve in it, approximated on the way to the screen".
func (p polyline) rounded() bool {
	for _, r := range p.Radii {
		if r != 0 {
			return true
		}
	}
	return false
}

// scaled converts every length to millimetres. A radius is a length like any
// other, and one left in inches while the points are converted rounds a corner
// 25.4 times too hard.
func (p polyline) scaled(toMM float64) polyline {
	out := polyline{Points: make([][3]float64, len(p.Points)),
		Radii: make([]float64, len(p.Radii)), Closed: p.Closed}
	for i, pt := range p.Points {
		out.Points[i] = scale3(pt, toMM)
	}
	for i, r := range p.Radii {
		out.Radii[i] = r * toMM
	}
	return out
}

// withoutClosingDuplicate drops a loop's redundant closing point.
//
// # Why a repeated first point is read rather than refused
//
// Every polygon format a model has read — GeoJSON, WKT, shapefiles — closes a
// ring by repeating its first point. This contract asks the opposite ("the
// outline is closed for you; do not repeat the first point at the end") and a
// model reaches for what it knows anyway: measured 2026-09-05 over 24 eval runs,
// SIX of the seven drawings FORGE refused outright were this and nothing else.
//
// It has exactly one reading. A closed loop that returns to its own first point
// has a final edge of zero length, which is never a shape — so the repeated
// point is redundant, and dropping it is the only way to read the drawing as
// anything at all. Refusing cost the whole part.
//
// # Why the RADIUS moves with it
//
// The repeated point often carries the corner radius while the original does
// not: the model writes the corner once as a destination and once as a corner.
// Dropping the point and leaving the radius behind would mitre a corner somebody
// asked to be bent — a different part, made a different way, and silently. So
// the radius is carried to the point that stays.
//
// conflict reports the one case with no single reading: both points carrying a
// radius, and the two disagreeing. One corner cannot have two radii.
func withoutClosingDuplicate(pts [][3]float64, radii []float64) (
	outPts [][3]float64, outRadii []float64, dropped, conflict bool) {

	n := len(pts)
	if n < 2 || !same(pts[0], pts[n-1]) {
		return pts, radii, false, false
	}
	outPts = pts[:n-1]
	outRadii = append([]float64{}, radii[:n-1]...)
	closing := 0.0
	if n-1 < len(radii) {
		closing = radii[n-1]
	}
	switch {
	case closing == 0:
	case len(outRadii) == 0:
	case outRadii[0] == 0:
		outRadii[0] = closing
	case outRadii[0] != closing:
		return pts, radii, false, true
	}
	return outPts, outRadii, true, false
}

// partOutline and partPath read a part's drawing out of a stored document.
//
// Literal coordinates, because Bind has already written every expression's value
// here and nothing downstream evaluates anything — see binding.go. A caller that
// evaluated them again would be a second opinion about what the document says.
func partOutline(p Part) polyline {
	return readLoop(p.Profile, true)
}

// readLoop turns a document's points into a polyline, dropping a redundant
// closing point on the way in.
//
// Done HERE and not only at the document boundary, because every reader has to
// agree about what the drawing is: the tessellator, the measurement path and the
// kernel each build from this, and one of them keeping a zero-length edge the
// others dropped is the renderer and the exported file disagreeing about the
// shape.
func readLoop(pts []Point, closed bool) polyline {
	out := polyline{Closed: closed}
	for _, pt := range pts {
		out.Points = append(out.Points, [3]float64{pt.X, pt.Y, pt.Z})
		out.Radii = append(out.Radii, pt.Radius)
	}
	if closed {
		out.Points, out.Radii, _, _ = withoutClosingDuplicate(out.Points, out.Radii)
	}
	return out
}

// partHoles reads the loops inside a part's outline.
func partHoles(p Part) []polyline {
	out := make([]polyline, 0, len(p.Holes))
	for _, hole := range p.Holes {
		out = append(out, readLoop(hole, true))
	}
	return out
}

// flattenSection flattens an outline and its holes together, and reports the
// worst approximation among them.
func flattenSection(outer polyline, holes []polyline, unit Unit) (
	flatOuter [][2]float64, flatHoles [][][2]float64, dev *Deviation, err error) {

	pts, dev, err := outer.flatten("outline", unit)
	if err != nil {
		return nil, nil, nil, err
	}
	flatOuter = flat2D(pts)
	for i, hole := range holes {
		flat, hdev, herr := hole.flatten(fmt.Sprintf("hole %d", i+1), unit)
		if herr != nil {
			return nil, nil, nil, herr
		}
		flatHoles = append(flatHoles, flat2D(flat))
		dev = worseDeviation(dev, hdev)
	}
	return flatOuter, flatHoles, dev, nil
}

func partPath(p Part) polyline {
	// Closed travels with the path, because it changes what the path IS: the
	// first and last points of a closed one are corners like any other, and may
	// carry a bend radius, while an open path's are ends and may not. It also
	// decides whether a repeated final point is a closing convention or a
	// zero-length segment somebody meant.
	return readLoop(p.Path, p.PathClosed)
}

// worseDeviation is whichever of two approximations departs further from the
// shape it stands in for.
//
// A shape can be approximated twice over — a revolve with rounded corners is
// faceted round its turn AND round each corner — and reporting the finer of the
// two would understate the file everywhere the coarser one dominates. The same
// reasoning as sphereDeviation, which has had this problem since spheres.
func worseDeviation(a, b *Deviation) *Deviation {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case b.Max.Value() > a.Max.Value():
		return b
	default:
		return a
	}
}

// corner is what happens at one vertex once its radius is applied.
type corner struct {
	// index is the vertex this came from, for naming it in a refusal.
	index int
	// sharp is a corner with no radius: the polyline simply turns.
	sharp bool
	// cut is how far back along each edge the arc starts, and is what decides
	// whether two corners have room to coexist.
	cut                   float64
	from, via, to, centre [3]float64
	radius, angle         float64
	axis                  [3]float64
}

// roundedCorners resolves every vertex into a sharp turn or an arc.
//
// closed says whether the run comes back to its first point, which decides
// whether the first and last vertices are CORNERS at all — an open path ends at
// them, and an end is not a corner.
//
// # Why an INERT radius is ignored and a bad one is refused
//
// ignored names every radius that was given and describes no corner: one on the
// end of an open path, and one on a point its neighbours run straight through.
// Neither is ambiguous — there is no corner there, so the number changes nothing
// — and neither is silent: the caller turns each into a warning the reader sees.
//
// Refusing them cost the whole part, twice, for a number that means nothing.
// Measured 2026-09-05: qwen-plus put a radius on every path point of a correct
// bent tube in two live runs of six, and on a point its path ran straight
// through in an eval run. All three were buildable drawings that vanished.
//
// What is still refused is a radius that cannot be READ as anything: a negative
// one, one on a point sitting on top of its neighbour, one on a reversal, and
// two that need more edge than there is between them.
func roundedCorners(pts [][3]float64, radii []float64, closed bool, what string) (
	[]corner, []string, error) {

	n := len(pts)
	out := make([]corner, n)
	var ignored []string
	inert := func(i int, why string) {
		ignored = append(ignored, fmt.Sprintf("has a corner radius on %s point %d, %s, so it "+
			"was ignored", what, i+1, why))
	}
	for i := range pts {
		out[i] = corner{index: i, sharp: true}
		r := 0.0
		if i < len(radii) {
			r = radii[i]
		}
		interior := closed || (i > 0 && i < n-1)
		if r == 0 {
			continue
		}
		if r < 0 {
			return nil, ignored, fmt.Errorf("%s point %d has a corner radius of %g; a radius is a "+
				"distance and cannot be negative", what, i+1, r)
		}
		if !interior {
			inert(i, "which is where the run starts or ends rather than a corner")
			continue
		}

		prev, next := pts[(i-1+n)%n], pts[(i+1)%n]
		in, out2 := sub3(pts[i], prev), sub3(next, pts[i])
		if length3(in) < arcTolerance || length3(out2) < arcTolerance {
			return nil, ignored, fmt.Errorf("%s point %d has a corner radius but sits on top of its "+
				"neighbour, so there is no corner to round", what, i+1)
		}
		dIn, dOut := normalise(in), normalise(out2)

		axis := cross3(dIn, dOut)
		sin := length3(axis)
		cos := dot3(dIn, dOut)
		if sin < arcTolerance {
			if cos > 0 {
				inert(i, "where the edges either side of it are in line")
				continue
			}
			return nil, ignored, fmt.Errorf("%s point %d turns back through 180°, which no radius can "+
				"round: the arc would have to close on itself", what, i+1)
		}
		angle := math.Atan2(sin, cos) // the turn, in (0, π)

		// How far back along each edge the arc has to start. r·tan(θ/2), which
		// is r for a right angle and grows without bound as the turn tightens —
		// which is exactly why a tight corner needs long edges either side, and
		// why the check below exists.
		cut := r * math.Tan(angle/2)
		from := add3(pts[i], scale3(dIn, -cut))
		to := add3(pts[i], scale3(dOut, cut))
		// The centre is along the inward bisector at r/sin(α/2), where α is the
		// INTERIOR angle π−θ. sin(α/2) = cos(θ/2).
		bisector := normalise(sub3(dOut, dIn))
		centre := add3(pts[i], scale3(bisector, r/math.Cos(angle/2)))
		// A point on the arc, halfway round it: out from the centre along the
		// bisector of the two tangent radii.
		mid := add3(centre, scale3(normalise(add3(sub3(from, centre), sub3(to, centre))), r))

		out[i] = corner{index: i, sharp: false, cut: cut, from: from, via: mid, to: to,
			centre: centre, radius: r, angle: angle, axis: scale3(axis, 1/sin)}
	}

	// Two corners on one edge must leave room for each other. This is the same
	// shape of check as a sweep's fold: what is refused is not one radius being
	// wrong but two of them together asking for more edge than there is.
	last := n - 1
	if !closed {
		last = n - 2
	}
	for i := 0; i <= last; i++ {
		j := (i + 1) % n
		span := length3(sub3(pts[j], pts[i]))
		need := out[i].cut + out[j].cut
		if need > span+arcTolerance {
			return nil, ignored, fmt.Errorf("the corner radii at %s points %d and %d need %.4g of the "+
				"%.4g between them, so the two arcs would overlap; use smaller radii, or move "+
				"the points further apart", what, i+1, j+1, need, span)
		}
	}
	return out, ignored, nil
}

// flattenCurve turns a drawing into the polyline the tessellators draw.
//
// worstRadius and worstAngle describe the arc that departs furthest from its
// chords, so the caller can report the deviation with a number rather than an
// adjective. Zero when nothing is rounded, which is the common case and is not a
// deviation at all.
func flattenCurve(pts [][3]float64, radii []float64, closed bool, what string) (
	flat [][3]float64, worstRadius, worstAngle float64, segments int, err error) {

	corners, _, err := roundedCorners(pts, radii, closed, what)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	// Consecutive duplicates are dropped as they are emitted. Two arcs that meet
	// exactly — a slot's end — each produce the point where they touch, and a
	// repeated point is a zero-length edge: the outline checks refuse one, ear
	// clipping can loop on one, and OCCT will not build one.
	push := func(p [3]float64) {
		if len(flat) > 0 && same(flat[len(flat)-1], p) {
			return
		}
		flat = append(flat, p)
	}
	seam := 0 // how many points the first corner contributed
	for _, c := range corners {
		if c.sharp {
			push(pts[c.index])
			if c.index == 0 {
				seam = len(flat)
			}
			continue
		}
		n := arcSegments(c.angle)
		// The tangent points are ON the arc, so they are emitted as its ends and
		// the interior points are stepped between them. An arc drawn this way is
		// inscribed: every drawn point lies on the true surface, and the error is
		// in the middle of each chord.
		for k := 0; k <= n; k++ {
			t := c.angle * float64(k) / float64(n)
			push(add3(c.centre, rotateAbout(sub3(c.from, c.centre), c.axis, t)))
		}
		if c.index == 0 {
			seam = len(flat)
		}
		if deviationOf(c.radius, c.angle, n) > deviationOf(worstRadius, worstAngle, segments) {
			worstRadius, worstAngle, segments = c.radius, c.angle, n
		}
	}
	// A closed drawing can also meet itself at the seam, for the same reason.
	if closed && len(flat) > 1 && same(flat[0], flat[len(flat)-1]) {
		flat = flat[:len(flat)-1]
	}

	// A CLOSED drawing starts where its first corner ENDS, not where it begins.
	//
	// # Why, and what goes wrong otherwise
	//
	// A closed path's first point is a corner like any other, so it may be
	// rounded — and then the flattened run begins part-way along an arc, where
	// the direction of travel is a CHORD of that arc rather than the straight the
	// arc is tangent to. The exact curve the kernel builds has the true tangent
	// there. The two disagree by half a chord's turn, which for a right angle at
	// this fineness is 4.5°, and the section frame computed from one and applied
	// to the other tilts the whole solid.
	//
	// Measured 2026-09-05: a 60×60 ring with R15 corners came back from the
	// kernel at 21358.7 mm³ against an arithmetic 21424.8, spanning −5.36..65.36
	// where a 10-wide section can only reach −5..65.
	//
	// Rotating the ring to start at the END of that arc costs nothing — a closed
	// run has no first point, only a place we chose to start writing it down —
	// and puts both representations on the straight that follows, where they
	// agree exactly.
	if closed && seam > 1 && len(flat) > 0 {
		k := (seam - 1) % len(flat)
		flat = append(append([][3]float64{}, flat[k:]...), flat[:k]...)
	}
	return flat, worstRadius, worstAngle, segments, nil
}

// exactCurve turns a drawing into the edges the CAD kernel builds: the true
// arcs, not chords.
//
// Every vertex contributes an ENTRY point and an EXIT point — the same point
// when it is sharp, the two ends of its arc when it is rounded — and the curve
// is those, joined by straights. Written that way because the alternative is a
// special case per combination of (first/last, sharp/rounded, open/closed), and
// the version of this function that had them got one wrong.
func exactCurve(pts [][3]float64, radii []float64, closed bool, what string) (Curve, error) {
	corners, _, err := roundedCorners(pts, radii, closed, what)
	if err != nil {
		return Curve{}, err
	}
	entry := func(i int) [3]float64 {
		if corners[i].sharp {
			return pts[i]
		}
		return corners[i].from
	}
	exit := func(i int) [3]float64 {
		if corners[i].sharp {
			return pts[i]
		}
		return corners[i].to
	}
	arc := func(i int) CurveEdge {
		via := corners[i].via
		return CurveEdge{To: corners[i].to, Via: &via}
	}

	// A closed run starts where its first corner ENDS — see flattenCurve for
	// why, and for the 0.3% of volume it costs to start anywhere else. An open
	// run's first vertex is always sharp (a radius there is refused), so its
	// entry and its exit are the same point and this reads the same either way.
	curve := Curve{Start: exit(0), Closed: closed}
	for i := 0; i+1 < len(pts); i++ {
		// The straight between two corners. It VANISHES when the two arcs meet
		// exactly, which is not a degenerate case to guard against but the whole
		// point of a slot: a rectangle whose radius is half its width ends in a
		// semicircle, and there is no straight left between the two quarters.
		if !same(exit(i), entry(i+1)) {
			curve.Edges = append(curve.Edges, CurveEdge{To: entry(i + 1)})
		}
		if !corners[i+1].sharp {
			curve.Edges = append(curve.Edges, arc(i+1))
		}
	}
	if closed {
		if !same(exit(len(pts)-1), entry(0)) {
			curve.Edges = append(curve.Edges, CurveEdge{To: entry(0)})
		}
		// The seam's own arc closes the run, ending back at Start.
		if !corners[0].sharp {
			curve.Edges = append(curve.Edges, arc(0))
		}
	}
	return curve, nil
}

// arcSegments is how many chords replace an arc.
//
// Proportional to the angle, from the same count a full circle gets, so a
// rounded corner and a cylinder beside it are drawn to the same fineness — and
// the exported mesh is the surface that was on screen, which is what the
// tessellation fence exists to keep true.
func arcSegments(angle float64) int {
	n := int(math.Ceil(float64(radialSegments) * angle / (2 * math.Pi)))
	if n < 1 {
		return 1
	}
	return n
}

// deviationOf is how far an arc's chords sit inside it: the sagitta of one
// chord, r − r·cos(half the angle each chord spans). Exact, not an estimate —
// the same formula chordDeviation uses for a cylinder, which is the same
// question asked about a full turn.
func deviationOf(radius, angle float64, segments int) float64 {
	if radius <= 0 || segments <= 0 {
		return 0
	}
	return radius * (1 - math.Cos(angle/float64(2*segments)))
}

// arcDeviation reports the flattening of the worst arc in a drawing.
func arcDeviation(radius, angle float64, segments int, unit Unit) *Deviation {
	d := deviationOf(radius, angle, segments)
	if d <= 0 {
		return nil
	}
	return &Deviation{Segments: segments, Max: NewQuantity(round(d, 6), unit)}
}

// rotateAbout turns v about a unit axis by angle, right-handed. Rodrigues.
func rotateAbout(v, axis [3]float64, angle float64) [3]float64 {
	c, s := math.Cos(angle), math.Sin(angle)
	return add3(add3(scale3(v, c), scale3(cross3(axis, v), s)),
		scale3(axis, dot3(axis, v)*(1-c)))
}

// flat2D drops the z of a drawing that lies in a plane, for the callers that
// work in the outline's own two dimensions.
func flat2D(pts [][3]float64) [][2]float64 {
	out := make([][2]float64, len(pts))
	for i, p := range pts {
		out[i] = [2]float64{p[0], p[1]}
	}
	return out
}

// lift3D is the other direction: an outline's points into the XY plane.
func lift3D(pts [][2]float64) [][3]float64 {
	out := make([][3]float64, len(pts))
	for i, p := range pts {
		out[i] = [3]float64{p[0], p[1], 0}
	}
	return out
}
