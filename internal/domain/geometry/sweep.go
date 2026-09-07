package geometry

import (
	"fmt"
	"math"
)

// Sweeps: an outline carried along a path.
//
// # What was missing
//
// An outline could be extruded — swept along a straight line perpendicular to
// itself — or revolved. Between them they cover the part that has one
// cross-section all the way through and the part that is turned. What neither
// covers is the part that BENDS: a hydraulic line, a handrail, a roll cage
// member, a cable tray, a wire form, a coolant pipe around an obstruction. Those
// are the same cross-section as an extrusion; what changes is that the line it
// follows is not straight.
//
// So "sweep" is "extrusion" with the line written down. A path of two points
// along local Z IS an extrusion, and produces the same solid.
//
// # The path
//
// A path is an OPEN polyline of at least two points in the part's own local
// frame — open because a closed one is a loop, and a loop needs a rule about
// where the section starts that nothing here would have. The profile's own
// origin rides the path, and the profile plane is perpendicular to the first
// segment.
//
// That is the same convention the extrusion already has, said in three
// dimensions: the part's position places the outline's ORIGIN (profile.go), so a
// hole placed against a drawn corner stays against it. It also means an outline
// drawn offset from its origin sweeps at that offset, which is how a groove or
// an eccentric section is described.
//
// What it does NOT do is centre anything. An extrusion centres its depth,
// because depth is a size like a box's height. A path is drawn, like an outline,
// and drawn things are used where they were drawn.
//
// # Corners are mitred
//
// At each interior point the section sits in the plane that bisects the two
// segments, which is what a mitred joint is and what every fabricated bend
// approximates. It has one property worth having: the wedge cut from the inside
// of the bend exactly equals the wedge added outside, so a swept solid whose
// outline is centred on its path has volume = area × total path length, exactly,
// however many times it bends. That identity is what the tests assert, and it
// holds for the kernel's B-Rep and this tessellation alike because they are the
// same polyhedron.
//
// Measured 2026-09-05 against build123d 0.11.1: it is also the ONLY transition
// mode that is right. OCCT's default (Transition.TRANSFORMED) returned 1600 mm³
// for an elbow whose correct volume is 5000, and Transition.ROUND rounds the
// corner into something the drawing did not say.
//
// # Twist
//
// Between segments the section is carried by the smallest rotation that takes
// one segment's direction to the next — a rotation-minimising frame. Any other
// choice twists the section as the path bends, which is invisible on a circular
// profile and wrong on every other one. It is also what OCCT does, which is why
// the exported solid and the drawn one agree about where the corners of a
// rectangular section end up.
//
// The frame at the FIRST segment is the smallest rotation taking +Z to it, so a
// path along +Z leaves the outline exactly as it was drawn and the sweep is the
// extrusion. It is continuous everywhere except straight down (-Z), where the
// rotation axis is genuinely undefined and a fixed one is chosen.

// minPathPoints is two, because one point is not a direction. There is no upper
// bound worth stating: a path somebody typed has a handful of points.
const minPathPoints = 2

// minClosedPathPoints is three: two points closed is a line drawn there and
// back, which sweeps the section through itself and encloses nothing.
const minClosedPathPoints = 3

// sweptSections returns the outline's ring of points at every path vertex.
//
// frame is the section's own axes at the first vertex, row-major with the
// COLUMNS being where the profile's x, its y and the path's direction end up —
// the same convention RotationMatrix uses, so the kernel can place the face with
// it and does not have to hold a second opinion about how a sweep is framed.
//
// loops[0] is the outline and the rest are the holes in it, all carried by the
// SAME frames, so rings is indexed [loop][path vertex][point].
//
// rings may be returned ALONGSIDE an error. The two faults are not alike: a path
// that doubles back or repeats a point has no frame at all and nothing can be
// drawn, but a path that bends too tightly for its own section has perfectly
// good rings that happen to fold through each other, and drawing that with a
// note beside it beats a part that silently vanishes from the render.
func sweptSections(loops [][][2]float64, path [][3]float64, closed bool) (rings [][][][3]float64, frame [9]float64, err error) {
	if len(loops) == 0 || len(loops[0]) < minProfilePoints || len(path) < minPathPoints {
		return nil, frame, fmt.Errorf("needs an outline of at least %d points and a path of at least %d",
			minProfilePoints, minPathPoints)
	}

	tangent, axisX, axisY, err := sectionFrames(path, closed)
	if err != nil {
		return nil, frame, err
	}
	segments := len(tangent)
	n := len(path)

	// The bisector normal at each vertex: the plane the mitre joint sits in.
	//
	// A closed path has a bisector at EVERY vertex, the seam included: it is a
	// corner like any other, joining the last segment to the first. An open one
	// has ends instead, where the section sits square to the path.
	bisector := make([][3]float64, n)
	for j := 0; j < n; j++ {
		switch {
		case !closed && j == 0:
			bisector[j] = tangent[0]
		case !closed && j == n-1:
			bisector[j] = tangent[segments-1]
		default:
			bisector[j] = normalise(add3(tangent[(j-1+segments)%segments], tangent[j%segments]))
		}
	}

	frame = [9]float64{
		axisX[0][0], axisY[0][0], tangent[0][0],
		axisX[0][1], axisY[0][1], tangent[0][1],
		axisX[0][2], axisY[0][2], tangent[0][2],
	}

	// Each ring is the loop placed on the perpendicular section at the vertex,
	// then slid ALONG the segment until it meets the bisector plane. Sliding
	// rather than projecting is what makes it a mitre: every point stays on the
	// line the sweep actually carries it along, so the incoming face and the
	// outgoing face meet edge to edge.
	//
	// EVERY loop is carried by the same frames — the outline and the holes in it
	// — because they are one section. A bore carried by frames of its own would
	// drift out of the wall around it as the path bends, and the wall thickness
	// would vary for no reason anyone could see in the drawing.
	rings = make([][][][3]float64, len(loops))
	for l, loop := range loops {
		rings[l] = make([][][3]float64, n)
		for j := range path {
			// The segment ARRIVING at this vertex. An open path's first vertex
			// has none and uses its outgoing one; a closed path's first vertex
			// is arrived at by the last segment, which is why the frame has to
			// come back to itself for the seam to meet.
			in := j - 1
			if in < 0 {
				in = 0
				if closed {
					in = segments - 1
				}
			}
			t, m := tangent[in], bisector[j]
			denom := dot3(t, m) // > 0: a reversal was refused above
			ring := make([][3]float64, len(loop))
			for k, p := range loop {
				base := add3(path[j], add3(scale3(axisX[in], p[0]), scale3(axisY[in], p[1])))
				ring[k] = add3(base, scale3(t, dot3(sub3(path[j], base), m)/denom))
			}
			rings[l][j] = ring
		}
	}

	// A bend tighter than the section is wide folds the surface back through
	// itself. OCCT does NOT refuse this — measured 2026-09-05, it returned a
	// solid of 14546 mm³ for a shape that should have been about half that — so
	// unlike every other fault here there is no kernel error to fall back on,
	// and a silent wrong volume is the worst outcome available.
	//
	// The test is exact and needs no trigonometry: for every point of every loop,
	// the ring must ADVANCE along each segment. One that goes backwards is the
	// fold itself. Checked on the holes too: a bore near the inside of a bend
	// folds before the outline around it does.
	for l, loop := range loops {
		for i := 0; i < segments; i++ {
			for k := range loop {
				if dot3(sub3(rings[l][(i+1)%n][k], rings[l][i][k]), tangent[i]) <= 1e-9 {
					return rings, frame, fmt.Errorf("bends too tightly between path points %d "+
						"and %d for a section this wide: the surface folds back through itself "+
						"there, so what it sweeps is not a solid. Move those points further "+
						"apart, use a larger bend radius, or draw the section closer to the "+
						"path it follows", i+1, (i+1)%n+1)
				}
			}
		}
	}
	return rings, frame, nil
}

// sectionFrames carries the section along the path: one direction of travel per
// segment, and the section's own two axes alongside it.
//
// # Why the section is CARRIED rather than recomputed
//
// Each frame is the previous one turned by the smallest rotation between the two
// segment directions, which is a rotation-minimising frame: it never spins the
// section about the direction it is travelling in.
//
// The obvious alternative — work out each segment's frame from scratch, the same
// way the first one is worked out from +Z — is wrong in a way nothing measures.
// It agrees with this for a path that bends in ONE plane, and disagrees as soon
// as a second bend leaves that plane, because rotations do not commute. The
// section arrives at the far end rolled about its own axis by an angle nobody
// asked for. The volume is identical, the mitres still close, the extents of a
// symmetric section are unchanged — a drill on 2026-09-05 made exactly this
// substitution and every test in the package stayed green. What changes is where
// the flat face of a rail points, which is the entire question a rail answers.
//
// Held by TestSectionFrames_CarryTheSectionWithoutRollingIt, on a path whose two
// bends are in different planes, because a single bend cannot tell the two
// apart.
//
// It also refuses the two paths that have no frame at all, both of which are
// properties of the path alone: a segment of zero length, and a reversal.
func sectionFrames(path [][3]float64, closed bool) (tangent, axisX, axisY [][3]float64, err error) {
	n := len(path)
	segments := n - 1
	if closed {
		segments = n
	}
	for i := 0; i < segments; i++ {
		d := sub3(path[(i+1)%n], path[i])
		l := length3(d)
		if l < 1e-12 {
			// OCCT refuses this one with "BRep_API: command not done" (measured
			// 2026-09-05), which names nothing. Two identical points in a row is
			// almost always a copied line somebody forgot to edit, so it is
			// named the same way a repeated outline point is.
			return nil, nil, nil, fmt.Errorf("repeats path point %d; a path cannot have a "+
				"segment of zero length", i+1)
		}
		tangent = append(tangent, scale3(d, 1/l))
	}
	for i := 0; i < segments; i++ {
		if i == 0 && !closed {
			continue // nothing arrives at an open path's first vertex
		}
		// Checked before anything is carried. smallestRotation has an arbitrary
		// answer for a reversal, so a path that doubles back would be framed
		// with a straight face rather than refused — and OCCT fails on it with
		// an EMPTY message, which leaves this the only place a reader can be
		// told what is wrong.
		if length3(add3(tangent[(i-1+segments)%segments], tangent[i])) < 1e-9 {
			return nil, nil, nil, fmt.Errorf("doubles back on itself at path point %d; a sweep "+
				"cannot turn through 180°, because the section there would have no thickness "+
				"and the shape would pass back through what it has already swept", i+1)
		}
	}

	axisX = make([][3]float64, segments)
	axisY = make([][3]float64, segments)
	axisX[0], axisY[0] = initialSectionFrame(tangent[0])
	for i := 1; i < segments; i++ {
		turn := smallestRotation(tangent[i-1], tangent[i])
		axisX[i], axisY[i] = turn(axisX[i-1]), turn(axisY[i-1])
	}
	if closed {
		if err := framesClose(tangent, axisX, axisY); err != nil {
			return nil, nil, nil, err
		}
	}
	return tangent, axisX, axisY, nil
}

// framesClose reports whether a carried frame comes back to itself round a loop.
//
// # Why this has to be checked rather than assumed
//
// A closed path has no ends, so the section at the seam is whatever the frame
// says after going all the way round. Carrying a frame round a closed curve
// rotates it by the area its tangents enclose on the sphere — the holonomy — and
// that is zero only for special loops.
//
// Measured 2026-09-05, over 2000 random closed polylines: EVERY ONE of them came
// back rotated, by up to 179.8°. Planar loops close exactly, and so do loops
// symmetric enough for the rotation to cancel — the first three tried did, which
// is what made this look like a non-problem for an afternoon.
//
// # Why it is refused rather than fixed
//
// The residual twist could be spread along the path so the seam meets. That is
// what some CAD does, and it means the section is TWISTED the whole way round by
// an angle nobody asked for — invisible on a round tube, and a helical rail on
// anything else. Refusing says what is wrong and leaves the shape to the person
// who drew it.
func framesClose(tangent, axisX, axisY [][3]float64) error {
	last := len(tangent) - 1
	turn := smallestRotation(tangent[last], tangent[0])
	back := turn(axisX[last])
	angle := math.Atan2(dot3(back, axisY[0]), dot3(back, axisX[0]))
	if math.Abs(angle) < 1e-6 {
		return nil
	}
	return fmt.Errorf("is a closed path whose section does not come back to itself: carried "+
		"round the loop it returns rotated by %.1f°, so the two ends of the sweep would meet "+
		"at an angle and what it encloses is not a solid. A path that stays in one plane "+
		"always closes; one that does not, usually will not", math.Abs(angle)*180/math.Pi)
}

// initialSectionFrame is where the profile's own x and y axes point when the
// path sets off in direction t: the smallest rotation that takes +Z to t.
//
// Smallest, so that a path along +Z is the identity and a sweep along it is
// exactly the extrusion — the property that makes "sweep" a generalisation
// rather than a second, subtly different thing.
func initialSectionFrame(t [3]float64) (x, y [3]float64) {
	up := [3]float64{0, 0, 1}
	turn := smallestRotation(up, t)
	return turn([3]float64{1, 0, 0}), turn([3]float64{0, 1, 0})
}

// smallestRotation returns the rotation taking unit vector a to unit vector b
// about the axis perpendicular to both — Rodrigues, with the angle in [0, π].
//
// The antiparallel case has no such axis: every rotation through π is equally
// "smallest". A fixed one is returned so the answer is at least deterministic,
// and the only path that reaches it is one setting off straight down, which
// nothing else about this system distinguishes.
func smallestRotation(a, b [3]float64) func([3]float64) [3]float64 {
	axis := cross3(a, b)
	sin := length3(axis)
	cos := dot3(a, b)
	if sin < 1e-12 {
		if cos > 0 {
			return func(v [3]float64) [3]float64 { return v }
		}
		perp := perpendicularTo(a)
		return func(v [3]float64) [3]float64 {
			// A half turn about any perpendicular axis: 2(v·k)k - v.
			return sub3(scale3(perp, 2*dot3(v, perp)), v)
		}
	}
	k := scale3(axis, 1/sin)
	return func(v [3]float64) [3]float64 {
		return add3(add3(scale3(v, cos), scale3(cross3(k, v), sin)),
			scale3(k, dot3(k, v)*(1-cos)))
	}
}

// perpendicularTo is any unit vector at right angles to v, chosen from whichever
// world axis v leans on least so the cross product cannot be degenerate.
func perpendicularTo(v [3]float64) [3]float64 {
	axis := [3]float64{1, 0, 0}
	if math.Abs(v[0]) > math.Abs(v[1]) {
		axis = [3]float64{0, 1, 0}
	}
	return normalise(cross3(v, axis))
}

func add3(a, b [3]float64) [3]float64 { return [3]float64{a[0] + b[0], a[1] + b[1], a[2] + b[2]} }
func sub3(a, b [3]float64) [3]float64 { return [3]float64{a[0] - b[0], a[1] - b[1], a[2] - b[2]} }
func scale3(a [3]float64, s float64) [3]float64 {
	return [3]float64{a[0] * s, a[1] * s, a[2] * s}
}
func dot3(a, b [3]float64) float64 { return a[0]*b[0] + a[1]*b[1] + a[2]*b[2] }
func cross3(a, b [3]float64) [3]float64 {
	return [3]float64{a[1]*b[2] - a[2]*b[1], a[2]*b[0] - a[0]*b[2], a[0]*b[1] - a[1]*b[0]}
}
func length3(a [3]float64) float64 { return math.Sqrt(dot3(a, a)) }

// pathPoints reads a part's path as plain coordinates, for the paths that run on
// a stored document and have no parameter context — the tessellator and the
// measurement path. Bind has already written the resolved numbers here.
func pathPoints(p Part) [][3]float64 {
	out := make([][3]float64, 0, len(p.Path))
	for _, pt := range p.Path {
		out = append(out, [3]float64{pt.X, pt.Y, pt.Z})
	}
	return out
}
