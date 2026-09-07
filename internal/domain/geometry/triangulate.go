package geometry

import (
	"math"
	"sort"
)

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
// # Holes
//
// An outline may have loops inside it (document.go: Holes). Ear clipping cannot
// see them — it walks ONE ring of vertices — so each hole is spliced into the
// outer loop first, by a BRIDGE: a segment from a hole vertex to a visible outer
// vertex, traversed out and back, which turns a ring-with-holes into one ring
// that happens to touch itself along the bridge. That is the standard treatment
// and it is exact: no area is added or lost, because the bridge is traversed in
// both directions and encloses nothing.
//
// # What it does NOT handle
//
// Self-intersecting outlines, and holes that cross the outline or each other.
// Those are refused before they get here (profile.go), where the offending loop
// can be named.

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
	pts = counterClockwise(in)

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
	tris, ok = earClip(pts)
	return pts, tris, ok
}

// earClip is the clipping loop alone, with nothing checked.
//
// Separate from triangulate because a BRIDGED polygon — an outline with its
// holes spliced in — touches itself along every bridge and so fails
// selfIntersects by construction. The loops it was built from were each checked
// before the splice, which is where the check belongs and where a bad loop can
// be named.
func earClip(pts [][2]float64) (tris [][3]int, ok bool) {
	n := len(pts)
	if n < minProfilePoints {
		return nil, false
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
				return tris, false
			}
		}
	}
	if len(idx) == 3 {
		tris = append(tris, [3]int{idx[0], idx[1], idx[2]})
	}
	return tris, true
}

// triangulateLoops triangulates an outline with holes in it.
//
// Returns the merged point list the triangles index into — the outer loop with
// every hole spliced in — normalised so the outline is counter-clockwise and
// every hole runs the other way. That winding is not a convention chosen here
// for tidiness: it is what makes the SAME wall-normal formula produce an
// outward normal on the outline and an inward one on a bore, without either
// caller knowing which loop it is walking.
func triangulateLoops(outer [][2]float64, holes [][][2]float64) section {
	loops := sectionLoops(outer, holes)
	if len(holes) == 0 {
		pts, tris, ok := triangulate(outer)
		return section{Merged: pts, Tris: tris, Loops: [][][2]float64{pts}, OK: ok}
	}
	out := section{Loops: loops, Merged: loops[0]}
	for _, loop := range loops {
		if len(loop) < minProfilePoints || selfIntersects(loop) {
			return out
		}
	}

	// One region per SOLID area. Ordinarily there is exactly one — an outline
	// with its bores — and this reduces to what it always did. It is more than
	// one when a loop sits inside a hole: see nestLoops.
	regions := nestLoops(loops)
	out.OK = true
	for _, r := range regions {
		var merged [][2]float64
		var tris [][3]int
		var ok bool
		if len(r.holes) == 0 {
			merged, tris, ok = triangulate(r.outer)
		} else {
			merged, ok = mergeHoles(r.outer, r.holes)
			if ok {
				tris, ok = earClip(merged)
			}
		}
		if !ok {
			// One region that will not close does not lose the others: the same
			// bargain the callers make about a partial outline, one level down.
			out.OK = false
			if len(merged) == 0 {
				continue
			}
		}
		// Indices are offset into the CONCATENATED point list, so every caller
		// goes on seeing one list of points and one list of triangles. That is
		// what keeps this change out of the extrusion, the revolve and the
		// sweep, each of which is fenced facet-for-facet against the browser.
		base := len(out.Merged)
		if len(out.Merged) == 0 && len(regions) == 1 {
			out.Merged = merged
		} else {
			if base == 0 {
				out.Merged = nil
			}
			out.Merged = append(out.Merged, merged...)
		}
		for _, t := range tris {
			out.Tris = append(out.Tris, [3]int{t[0] + base, t[1] + base, t[2] + base})
		}
	}
	return out
}

// loopParents reports, for each hole, which loop directly contains it: -1 for
// the outline, or the index of another hole.
//
// # Why the CAD kernel is given this rather than working it out
//
// The kernel holds the drawing as CURVES — arcs and lines — and containment is a
// question about polygons. It would have to flatten them again to answer it, at
// a fineness it would have to choose, and a kernel that decided nesting
// differently from the tessellator would export a solid that is not the one on
// screen. So the reading is made once, here, where nestLoops already makes it
// for the triangles, and travels with the solid.
//
// The same reasoning as the section frame, which is computed here and sent for
// exactly this reason: a builder handed only the parts and asked to decide is a
// second opinion waiting to diverge.
func loopParents(outer [][2]float64, holes [][][2]float64) []int {
	_, parent := nesting(append([][][2]float64{outer}, holes...))
	out := make([]int, len(holes))
	for i := range holes {
		// parent indexes into a list whose element 0 is the outline; the
		// caller's holes are numbered from 0, so shift. -1 already means "the
		// outline", and 0-1 = -1 says the same thing.
		out[i] = parent[i+1] - 1
	}
	return out
}

// region is one solid area of a section: its boundary, and the voids directly
// inside it.
type region struct {
	outer [][2]float64
	holes [][][2]float64
}

// nestLoops groups a section's loops into the solid areas they describe.
//
// # What this is for
//
// A loop inside a hole is an ISLAND: solid material standing in a void. The
// annular slot with a post in the middle, the letter O extruded, a lug in the
// bottom of a pocket, a spider in a casting's core. Until wave 30 it was refused
// with "an island in a hole is a second outline, and there is no vocabulary for
// one here" — and the vocabulary turns out to be one this document already has.
//
// # Why nesting is READ rather than declared
//
// The even-odd rule is how every format a model has seen represents this —
// TrueType glyphs, SVG paths, DXF, shapefiles — and it is unambiguous: a loop
// contained in an odd number of others is solid, in an even number it is a void.
// So a new field would be a second way to say something the drawing already
// says, and the two could then disagree.
//
// It is not a guess. The loops are already known not to cross (holesFit refuses
// that before this is reached), so containment is a fact about the drawing, and
// one point per loop settles it.
//
// What the reader is owed instead is to be TOLD, which profile.go does: a hole
// inside a hole is a real mistake as well as a real shape, and the note is what
// makes the difference visible.
//
// # Why arbitrary depth rather than one level
//
// Because the rule is the same at every depth and stopping at two would be an
// arbitrary limit that somebody hits. A hole in an island in a hole is a
// counterbore in a boss in a pocket, which is an ordinary machined part.
// The loops arrive ALREADY WOUND, from sectionLoops, and that winding is trusted
// rather than reapplied. One rule, in one place: a second copy here would be a
// second opinion about which loops are solid, and the day the two disagreed the
// caps would be cut from one reading and the walls faced by the other.
func nestLoops(loops [][][2]float64) []region {
	depth, parent := nesting(loops)
	var out []region
	for i := range loops {
		if depth[i]%2 != 0 {
			continue // a void, and it belongs to whatever contains it
		}
		r := region{outer: loops[i]}
		for j := range loops {
			if depth[j]%2 == 1 && parent[j] == i {
				r.holes = append(r.holes, loops[j])
			}
		}
		out = append(out, r)
	}
	return out
}

// section is an outline and its holes, ready to be built into a solid.
//
// # Why the merged list and the loops are BOTH kept
//
// The caps are triangles over the merged ring, bridges and all. The walls are
// not: a wall along a bridge would be a quad of zero width, drawn twice, facing
// both ways. So the walls walk the loops separately, and the two lists are
// different views of the same section rather than one standing in for the other.
type section struct {
	// Merged is the outline with every hole spliced in. Tris index into it.
	Merged [][2]float64
	Tris   [][3]int
	// Loops is the outline first, then each hole, each wound so that the same
	// wall-normal formula points out of the material on all of them.
	Loops [][][2]float64
	OK    bool
}

// sectionLoops winds every loop so that one wall-normal formula points out of
// the material on all of them.
//
// A solid boundary runs counter-clockwise and a void runs the other way. That
// opposition is what makes a bore's walls face the right way for free: the
// outward normal formula, applied to a loop running backwards, points INTO the
// hole, which is out of the material. Neither the extrusion, the revolve nor the
// sweep has to know which loop it is walking.
//
// Which loops are solid is decided by NESTING and not by position in the list
// (wave 30). "The first one is the outline and the rest are holes" was true
// while a hole could not contain anything; a loop inside a hole is an island,
// and winding it like a hole points its wall into the solid — invisible in a
// silhouette and wrong in every file.
func sectionLoops(outer [][2]float64, holes [][][2]float64) [][][2]float64 {
	loops := append([][][2]float64{outer}, holes...)
	depth, _ := nesting(loops)
	out := make([][][2]float64, len(loops))
	for i, loop := range loops {
		if depth[i]%2 == 0 {
			out[i] = counterClockwise(loop)
		} else {
			out[i] = clockwise(loop)
		}
	}
	return out
}

// nesting reports how many loops contain each one, and which contains it most
// directly.
//
// Containment is a fact rather than a guess: the loops are already known not to
// cross — holesFit refuses that before any of this is reached — so one point per
// loop settles it.
func nesting(loops [][][2]float64) (depth []int, parent []int) {
	depth = make([]int, len(loops))
	parent = make([]int, len(loops))
	for i := range loops {
		if len(loops[i]) == 0 {
			continue
		}
		for j := range loops {
			if i != j && len(loops[j]) > 0 && insideLoop(loops[i][0], loops[j]) {
				depth[i]++
			}
		}
	}
	for i := range loops {
		best := -1
		for j := range loops {
			if i == j || len(loops[j]) == 0 || len(loops[i]) == 0 {
				continue
			}
			if !insideLoop(loops[i][0], loops[j]) {
				continue
			}
			if best < 0 || depth[j] > depth[best] {
				best = j
			}
		}
		parent[i] = best
	}
	return depth, parent
}

// counterClockwise and clockwise return the loop wound the stated way, reversing
// a copy when it is not.
func counterClockwise(loop [][2]float64) [][2]float64 {
	if signedArea(loop) >= 0 {
		return loop
	}
	return reversed(loop)
}

func clockwise(loop [][2]float64) [][2]float64 {
	if signedArea(loop) <= 0 {
		return loop
	}
	return reversed(loop)
}

func reversed(loop [][2]float64) [][2]float64 {
	out := make([][2]float64, len(loop))
	for i := range loop {
		out[i] = loop[len(loop)-1-i]
	}
	return out
}

// mergeHoles splices every hole into the outer loop, one bridge at a time.
//
// # How a bridge is chosen
//
// Every (hole vertex, outer vertex) pair is a candidate, tried shortest first.
// The one that is taken is the first that stays inside the material: it may not
// properly cross any edge of what has been merged so far or of any hole still to
// come, and its midpoint must be inside the outline and outside every hole.
//
// Shortest first is not an optimisation. A long bridge is far more likely to
// graze another loop, and the shortest valid one is also the least visible in
// the triangulation it produces.
//
// # Why every remaining hole is checked and not just the merged polygon
//
// A bridge to a hole that has not been spliced yet would cut straight across it,
// and the polygon would only stop being simple three splices later — where the
// failure is a triangulation that quietly covers the wrong region rather than an
// error anyone can trace back.
func mergeHoles(outer [][2]float64, holes [][][2]float64) ([][2]float64, bool) {
	merged := append([][2]float64{}, outer...)
	remaining := append([][][2]float64{}, holes...)

	for len(remaining) > 0 {
		// Rightmost hole first: the classical order, and the one that keeps a
		// bridge from having to reach across a hole that is still in the way.
		best := 0
		for i, h := range remaining {
			if rightmostX(h) > rightmostX(remaining[best]) {
				best = i
			}
		}
		hole := remaining[best]
		remaining = append(remaining[:best:best], remaining[best+1:]...)

		spliced, ok := bridgeInto(merged, hole, remaining)
		if !ok {
			return nil, false
		}
		merged = spliced
	}
	return merged, true
}

func rightmostX(loop [][2]float64) float64 {
	x := math.Inf(-1)
	for _, p := range loop {
		x = math.Max(x, p[0])
	}
	return x
}

type bridgeCandidate struct {
	outer, hole int
	length      float64
}

func bridgeInto(merged, hole [][2]float64, pending [][][2]float64) ([][2]float64, bool) {
	candidates := make([]bridgeCandidate, 0, len(merged)*len(hole))
	for i := range merged {
		for j := range hole {
			d := [2]float64{hole[j][0] - merged[i][0], hole[j][1] - merged[i][1]}
			candidates = append(candidates, bridgeCandidate{i, j, d[0]*d[0] + d[1]*d[1]})
		}
	}
	sort.Slice(candidates, func(a, b int) bool {
		return candidates[a].length < candidates[b].length
	})

	for _, c := range candidates {
		a, b := merged[c.outer], hole[c.hole]
		if a == b {
			continue
		}
		if blocksBridge(a, b, merged) || blocksBridge(a, b, hole) {
			continue
		}
		blocked := false
		for _, other := range pending {
			if blocksBridge(a, b, other) {
				blocked = true
				break
			}
		}
		if blocked {
			continue
		}
		mid := [2]float64{(a[0] + b[0]) / 2, (a[1] + b[1]) / 2}
		if !insideLoop(mid, merged) || insideLoop(mid, hole) {
			continue
		}
		inPending := false
		for _, other := range pending {
			if insideLoop(mid, other) {
				inPending = true
				break
			}
		}
		if inPending {
			continue
		}

		// Out along the bridge, all the way round the hole, and back. The two
		// bridge vertices appear twice each, which is the whole trick: the ring
		// touches itself along a segment of zero width and encloses exactly what
		// it did before.
		out := make([][2]float64, 0, len(merged)+len(hole)+2)
		out = append(out, merged[:c.outer+1]...)
		out = append(out, hole[c.hole:]...)
		out = append(out, hole[:c.hole+1]...)
		out = append(out, merged[c.outer:]...)
		return out, true
	}
	return nil, false
}

// blocksBridge reports whether any edge of loop properly crosses the segment.
//
// Sharing an ENDPOINT does not count: a bridge runs from a vertex of one loop to
// a vertex of another, so the edges either side of both ends touch it by
// construction. What counts is a crossing anywhere else, including a vertex
// lying part-way along the bridge — that is a bridge running through a corner,
// which produces a polygon that is not simple.
func blocksBridge(a, b [2]float64, loop [][2]float64) bool {
	for i := range loop {
		p, q := loop[i], loop[(i+1)%len(loop)]
		if p == a || p == b || q == a || q == b {
			// Still refused if the shared endpoint means the edge lies ALONG the
			// bridge rather than merely touching it at a point.
			if cross(a, b, p) == 0 && cross(a, b, q) == 0 {
				return true
			}
			continue
		}
		if segmentsProperlyCross(a, b, p, q) {
			return true
		}
	}
	return false
}

func segmentsProperlyCross(p1, p2, p3, p4 [2]float64) bool {
	d1, d2 := cross(p3, p4, p1), cross(p3, p4, p2)
	d3, d4 := cross(p1, p2, p3), cross(p1, p2, p4)
	if ((d1 > 0 && d2 < 0) || (d1 < 0 && d2 > 0)) &&
		((d3 > 0 && d4 < 0) || (d3 < 0 && d4 > 0)) {
		return true
	}
	// An endpoint of one lying part-way along the other. Touching AT an endpoint
	// is allowed; touching in the middle is a crossing.
	return (d1 == 0 && strictlyBetween(p3, p4, p1)) || (d2 == 0 && strictlyBetween(p3, p4, p2)) ||
		(d3 == 0 && strictlyBetween(p1, p2, p3)) || (d4 == 0 && strictlyBetween(p1, p2, p4))
}

func strictlyBetween(a, b, p [2]float64) bool {
	if p == a || p == b {
		return false
	}
	return onSegment(a, b, p)
}

// insideLoop is the even-odd ray test: how many times a ray from p crosses the
// loop. Odd means inside.
func insideLoop(p [2]float64, loop [][2]float64) bool {
	in := false
	for i := range loop {
		a, b := loop[i], loop[(i+1)%len(loop)]
		if (a[1] > p[1]) != (b[1] > p[1]) {
			x := a[0] + (p[1]-a[1])/(b[1]-a[1])*(b[0]-a[0])
			if x > p[0] {
				in = !in
			}
		}
	}
	return in
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
		// A bridge puts TWO vertices at the same coordinates, and the boundary
		// counts as inside — so without this, the duplicate of a bridge endpoint
		// blocks every ear that touches it and clipping stalls on any outline
		// with a hole in it. Skipped by POSITION rather than by index, because
		// which index is the duplicate is not knowable from here.
		if pts[other] == a || pts[other] == b || pts[other] == c {
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
