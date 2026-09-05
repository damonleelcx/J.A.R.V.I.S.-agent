package geometry

import "math"

// Triangulating an outline, for everything that is not the CAD kernel.
//
// # Why this exists at all
//
// The kernel builds an extrusion as a real B-Rep and never needs triangles. The
// VIEWPORT and the mesh exporters do, and neither can call a kernel: one is
// JavaScript in a browser and the other must work in deployments that have no
// kernel configured at all.
//
// # Why ear clipping and not a fan
//
// A triangle fan from the first vertex is four lines of code and is WRONG for
// any concave outline — which is most of the interesting ones. An L-bracket is
// concave by definition, and a fan across its inner corner produces triangles
// that lie outside the part. The first shape anybody draws with this feature
// would have been drawn wrong.
//
// Ear clipping is O(n²) and this is fine: an outline somebody typed has a
// handful of points, not a thousand.
//
// # What it does NOT handle
//
// Holes in the outline (an outline inside another) and self-intersecting
// outlines. Neither can be expressed: a profile is ONE closed loop, and a hole
// is made by cutting a part rather than by drawing a second loop. A
// self-intersecting outline is refused by the kernel and produces a partial
// triangulation here, reported by the caller rather than silently drawn.

// triangulate returns the outline's triangles as index triples.
//
// Input is assumed closed — the last point joins the first — and is normalised
// to counter-clockwise so the caller can rely on the winding when it builds
// normals. An inside-out solid is a defect this repository has already shipped
// once (docs/bugfix/2026-09-02-exported-meshes-were-inside-out.md).
//
// It returns the points in the order the triangles index INTO, which may be the
// reverse of what came in. Returning them is not a convenience: the winding fix
// used to happen inside and the caller kept its own array, so the caps came out
// normalised and the side walls — built by walking the caller's points — did
// not. A clockwise L-bracket tessellated into a solid with its walls facing
// inward, and the only clue was a negative volume. A caller that uses the
// returned slice for everything cannot make that mistake.
//
// ok is false when the outline could not be fully triangulated, which means it
// crosses itself. The triangles produced so far are returned rather than
// discarded: a partial outline drawn with a note beats a part that vanishes.
func triangulate(in [][2]float64) (pts [][2]float64, tris [][3]int, ok bool) {
	n := len(in)
	if n < minProfilePoints {
		return in, nil, false
	}
	pts = in
	if signedArea(pts) < 0 {
		pts = make([][2]float64, n)
		for i := range in {
			pts[i] = in[n-1-i]
		}
	}

	// Work on an index list so the caller's points keep their identity, and so
	// the winding fix costs one reversed slice rather than a copy of the data.
	// Refused up front, because ear clipping cannot reliably detect it.
	//
	// A self-intersecting outline can have all of its vertices consumed and
	// report success: a bow-tie clips into two triangles that between them cover
	// twice the area the outline encloses, and the loop never notices. Measured
	// 2026-09-05 — the "it terminates" guard below caught the case where NO ear
	// is found and said nothing about this one.
	if selfIntersects(pts) {
		return pts, nil, false
	}

	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}

	// Every pass must remove at least one ear. guard counts passes that did
	// not, which is the only way a self-intersecting outline ends this loop.
	guard := 0
	for len(idx) > 3 {
		clipped := false
		for i := range idx {
			prev := idx[(i-1+len(idx))%len(idx)]
			cur := idx[i]
			next := idx[(i+1)%len(idx)]
			if !isEar(pts, idx, prev, cur, next) {
				continue
			}
			tris = append(tris, [3]int{prev, cur, next})
			idx = append(idx[:i], idx[i+1:]...)
			clipped = true
			break
		}
		if !clipped {
			guard++
			if guard > 1 {
				return pts, tris, false
			}
		}
	}
	if len(idx) == 3 {
		tris = append(tris, [3]int{idx[0], idx[1], idx[2]})
	}
	return pts, tris, true
}

// isEar reports whether the corner at cur can be cut off.
//
// Two conditions, and both are needed: the corner must turn the same way as the
// outline (a reflex corner is not an ear), and no other vertex may lie inside
// the triangle it would cut (cutting one off would remove material that is part
// of the shape).
func isEar(pts [][2]float64, idx []int, prev, cur, next int) bool {
	a, b, c := pts[prev], pts[cur], pts[next]
	if cross(a, b, c) <= 0 {
		return false // reflex or collinear, given counter-clockwise winding
	}
	for _, other := range idx {
		if other == prev || other == cur || other == next {
			continue
		}
		if pointInTriangle(pts[other], a, b, c) {
			return false
		}
	}
	return true
}

// cross is the z of (b-a) × (c-b). Positive means a left turn.
func cross(a, b, c [2]float64) float64 {
	return (b[0]-a[0])*(c[1]-b[1]) - (b[1]-a[1])*(c[0]-b[0])
}

// pointInTriangle uses the same sign test on all three edges.
//
// The boundary counts as inside. A vertex sitting exactly on an edge of a
// candidate ear makes it not an ear: clipping it would produce a zero-area
// sliver and leave the vertex stranded, which is how ear clipping loops forever
// on an outline that touches itself.
func pointInTriangle(p, a, b, c [2]float64) bool {
	d1 := cross(a, b, p)
	d2 := cross(b, c, p)
	d3 := cross(c, a, p)
	hasNeg := d1 < 0 || d2 < 0 || d3 < 0
	hasPos := d1 > 0 || d2 > 0 || d3 > 0
	return !(hasNeg && hasPos)
}

// triangulatedArea sums the triangles' areas.
//
// Used by the tests as the check that matters: any correct triangulation of an
// outline has the same total area as the outline, whatever order it cut the ears
// in. Asserting on the triangles themselves would pin an implementation detail
// and go red on a change that is not a defect.
func triangulatedArea(pts [][2]float64, tris [][3]int) float64 {
	var total float64
	for _, t := range tris {
		total += math.Abs(cross(pts[t[0]], pts[t[1]], pts[t[2]])) / 2
	}
	return total
}

// selfIntersects reports whether any two non-adjacent edges of the closed
// outline cross.
//
// # Why this is a separate question from "can it be triangulated"
//
// Ear clipping consumes vertices; it does not verify the shape. A bow-tie has
// four vertices, two of which are ears by the local turn test, so clipping
// "succeeds" and produces triangles covering twice the enclosed area. The
// failure is not that the algorithm gets stuck — it is that the outline was
// never a shape.
//
// O(n²) over a handful of hand-written points, which is nothing, and it is the
// property itself rather than a proxy for it.
func selfIntersects(pts [][2]float64) bool {
	n := len(pts)
	for i := 0; i < n; i++ {
		a1, a2 := pts[i], pts[(i+1)%n]
		for j := i + 1; j < n; j++ {
			// Adjacent edges share an endpoint and always "touch". The pair
			// (0, n-1) is adjacent too, because the outline is closed.
			if j == i || (j+1)%n == i || (i+1)%n == j {
				continue
			}
			if segmentsCross(a1, a2, pts[j], pts[(j+1)%n]) {
				return true
			}
		}
	}
	return false
}

func segmentsCross(p1, p2, p3, p4 [2]float64) bool {
	d1 := cross(p3, p4, p1)
	d2 := cross(p3, p4, p2)
	d3 := cross(p1, p2, p3)
	d4 := cross(p1, p2, p4)
	if ((d1 > 0 && d2 < 0) || (d1 < 0 && d2 > 0)) &&
		((d3 > 0 && d4 < 0) || (d3 < 0 && d4 > 0)) {
		return true
	}
	// Collinear touching: an endpoint lying ON another edge is still the outline
	// meeting itself, and it is the case that makes ear clipping loop.
	return (d1 == 0 && onSegment(p3, p4, p1)) || (d2 == 0 && onSegment(p3, p4, p2)) ||
		(d3 == 0 && onSegment(p1, p2, p3)) || (d4 == 0 && onSegment(p1, p2, p4))
}

func onSegment(a, b, p [2]float64) bool {
	return math.Min(a[0], b[0]) <= p[0] && p[0] <= math.Max(a[0], b[0]) &&
		math.Min(a[1], b[1]) <= p[1] && p[1] <= math.Max(a[1], b[1])
}
