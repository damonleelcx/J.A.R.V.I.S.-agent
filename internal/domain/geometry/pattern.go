package geometry

import (
	"fmt"
	"math"
	"strings"
)

// Patterns on a placed child.
//
// Phase 1, stage D1c-2 of docs/plan-2026-09-13-millions-of-parts.md. Decided
// 2026-09-14: a new pattern object with linear, polar, grid and path kinds, rather
// than reusing a part's repeat on children.
//
// # What a pattern means
//
// A pattern writes a child out as several copies. Each copy is the child's own
// placement — its position, rotation and mirror — carried by the pattern's
// transform in the PARENT assembly's frame. So the child's position is its offset
// from each slot, and a pattern of a sub-assembly patterns all of it. Copy n
// (counting from 1) is "<child id>-n", the naming a part's repeat already uses.
//
// # The four kinds
//
//	linear  copy n is moved by (n-1) × offset.
//	polar   copy n is turned about x, y or z through the parent's origin, spaced
//	        exactly as a repeat spaces a circle (sweepAngle): a full turn divided
//	        by count, a partial sweep by count-1 so both ends of the arc are used.
//	grid    rows × columns copies, numbered row by row; the copy in row r and
//	        column c (from 0) is moved by r × row_offset + c × column_offset.
//	path    count copies at equal arc length along an open polyline in the
//	        parent's frame, the first at its start and the last at its end. With
//	        align, each copy is turned by the SMALLEST rotation that takes its local
//	        +X onto the path's direction there: the outgoing segment at a corner, the
//	        last segment at the end. Exactly opposite to +X is a half turn about z.
//
// # What it refuses by name
//
// A path point with a corner radius, an arc through a point, or an expression. A
// pattern follows straight segments at literal coordinates, and silently ignoring a
// radius would put copies somewhere nobody drew them. (A pattern's offsets and a
// polar pattern's angle may be bound to parameters: pattern_binding.go. Its path's
// points may not.)
type Pattern struct {
	Kind string `json:"kind"`
	// Count is how many copies exist in total, the first included (linear, polar, path).
	Count int `json:"count,omitempty"`
	// Offset is the step between copies of a linear pattern. OffsetFrom binds its
	// axes ("x", "y", "z") to expressions, as position_from binds a position.
	Offset     []float64         `json:"offset,omitempty"`
	OffsetFrom map[string]string `json:"offset_from,omitempty"`
	// About and Angle turn a polar pattern: the axis through the parent's origin,
	// and the total sweep in DEGREES — zero means a full turn. AngleFrom binds the
	// sweep to an expression.
	About     string  `json:"about,omitempty"`
	Angle     float64 `json:"angle,omitempty"`
	AngleFrom string  `json:"angle_from,omitempty"`
	// Rows, Columns, RowOffset and ColumnOffset lay out a grid; the _from maps bind
	// the offsets' axes as OffsetFrom does.
	Rows             int               `json:"rows,omitempty"`
	Columns          int               `json:"columns,omitempty"`
	RowOffset        []float64         `json:"row_offset,omitempty"`
	RowOffsetFrom    map[string]string `json:"row_offset_from,omitempty"`
	ColumnOffset     []float64         `json:"column_offset,omitempty"`
	ColumnOffsetFrom map[string]string `json:"column_offset_from,omitempty"`
	// Path is the open polyline a path pattern follows, in the parent's frame, and
	// Align turns each copy to follow it.
	Path  []Point `json:"path,omitempty"`
	Align bool    `json:"align,omitempty"`
	Note  string  `json:"note,omitempty"`
}

// patternSlot is one copy's transform in the parent's frame.
type patternSlot struct {
	suffix string // "" for an unpatterned child, "-n" for copy n
	number string // "" or "n"
	at     placement
}

func slot(n int, at placement) patternSlot {
	return patternSlot{suffix: fmt.Sprintf("-%d", n), number: fmt.Sprint(n), at: at}
}

// copies returns where a pattern places a child's copies. The problem is an
// Error (no copies) or a Warning (drawn once), and carries no Name; the caller
// names the child. A nil pattern is one unnamed copy at the child's own place.
func (p *Pattern) copies() ([]patternSlot, *Problem) {
	identity := placementOf(nil, nil, false)
	if p == nil {
		return []patternSlot{{at: identity}}, nil
	}
	fail := func(format string, args ...any) ([]patternSlot, *Problem) {
		return nil, &Problem{Severity: Error, Detail: fmt.Sprintf(format, args...)}
	}
	once := func(format string, args ...any) ([]patternSlot, *Problem) {
		return []patternSlot{{at: identity}}, &Problem{Severity: Warning, Detail: fmt.Sprintf(format, args...)}
	}
	vector := func(v []float64) ([3]float64, bool) {
		var out [3]float64
		if len(v) == 0 {
			return out, false
		}
		copy(out[:], padTo3(v))
		for _, x := range out {
			if math.IsNaN(x) || math.IsInf(x, 0) {
				return out, false
			}
		}
		return out, true
	}
	moved := func(v [3]float64) placement {
		at := identity
		at.pos = v
		return at
	}

	switch kind := strings.ToLower(strings.TrimSpace(p.Kind)); kind {
	case "linear":
		step, ok := vector(p.Offset)
		if !ok {
			return fail("is a linear pattern with no usable offset, so every copy would sit on the first")
		}
		if p.Count > maxRepeat {
			return fail("is a linear pattern of %d copies; %d is the most one pattern places", p.Count, maxRepeat)
		}
		if p.Count < 2 {
			return once("is a linear pattern of %d copy, which is not a pattern, so it is placed once", p.Count)
		}
		out := make([]patternSlot, 0, p.Count)
		for n := 1; n <= p.Count; n++ {
			k := float64(n - 1)
			out = append(out, slot(n, moved([3]float64{step[0] * k, step[1] * k, step[2] * k})))
		}
		return out, nil

	case "polar":
		if p.About != "x" && p.About != "y" && p.About != "z" {
			return fail("is a polar pattern about %q; it turns about \"x\", \"y\" or \"z\"", p.About)
		}
		if p.Count > maxRepeat {
			return fail("is a polar pattern of %d copies; %d is the most one pattern places", p.Count, maxRepeat)
		}
		if p.Count < 2 {
			return once("is a polar pattern of %d copy, which is not a pattern, so it is placed once", p.Count)
		}
		// The same spacing rule a part's repeat uses, not a second one.
		between := sweepAngle(&Repeat{Count: p.Count, Angle: p.Angle})
		out := make([]patternSlot, 0, p.Count)
		for n := 1; n <= p.Count; n++ {
			at := identity
			at.m = RotationMatrix(axisRotation(p.About, between*float64(n-1)))
			out = append(out, slot(n, at))
		}
		return out, nil

	case "grid":
		if p.Rows < 1 || p.Columns < 1 {
			return fail("is a grid of %d row(s) and %d column(s); both must be at least 1", p.Rows, p.Columns)
		}
		if p.Rows*p.Columns > maxRepeat {
			return fail("is a grid of %d copies; %d is the most one pattern places", p.Rows*p.Columns, maxRepeat)
		}
		row, rowOK := vector(p.RowOffset)
		col, colOK := vector(p.ColumnOffset)
		if p.Rows > 1 && !rowOK {
			return fail("is a grid of %d rows with no usable row_offset, so its rows would overlap", p.Rows)
		}
		if p.Columns > 1 && !colOK {
			return fail("is a grid of %d columns with no usable column_offset, so its columns would overlap", p.Columns)
		}
		if p.Rows*p.Columns < 2 {
			return once("is a grid of one copy, which is not a pattern, so it is placed once")
		}
		out := make([]patternSlot, 0, p.Rows*p.Columns)
		for n := 0; n < p.Rows*p.Columns; n++ {
			r, c := float64(n/p.Columns), float64(n%p.Columns)
			out = append(out, slot(n+1, moved([3]float64{
				row[0]*r + col[0]*c, row[1]*r + col[1]*c, row[2]*r + col[2]*c})))
		}
		return out, nil

	case "path":
		if len(p.Path) < 2 {
			return fail("is a path pattern with %d point(s); a path needs at least 2", len(p.Path))
		}
		pts := make([][3]float64, len(p.Path))
		for i, q := range p.Path {
			switch {
			case q.Radius != 0 || q.RadiusFrom != "":
				return fail("is a path pattern whose point %d has a corner radius; a pattern follows straight "+
					"segments, so give the corner as points", i+1)
			case q.Via != nil:
				return fail("is a path pattern whose point %d is reached through an arc; a pattern follows "+
					"straight segments, so give the curve as points", i+1)
			case q.XFrom != "" || q.YFrom != "" || q.ZFrom != "":
				return fail("is a path pattern whose point %d is written as an expression; a pattern path is "+
					"read at the numbers it states", i+1)
			}
			pts[i] = [3]float64{q.X, q.Y, q.Z}
		}
		if p.Count > maxRepeat {
			return fail("is a path pattern of %d copies; %d is the most one pattern places", p.Count, maxRepeat)
		}
		stations, ok := pathStations(pts, p.Count)
		if !ok {
			return fail("is a path pattern whose path has no length, so its copies have nowhere to go")
		}
		if p.Count < 2 {
			return once("is a path pattern of %d copy, which is not a pattern, so it is placed once at "+
				"the path's start", p.Count)
		}
		out := make([]patternSlot, 0, len(stations))
		for n, st := range stations {
			at := moved(st.point)
			if p.Align {
				at.m = rotationTaking(st.direction)
			}
			out = append(out, slot(n+1, at))
		}
		return out, nil
	}
	return fail("is a pattern of kind %q; the kinds are linear, polar, grid and path", p.Kind)
}

type pathStation struct {
	point, direction [3]float64
}

// pathStations are count points at equal arc length along an open polyline, with
// the unit direction of the segment each lies on (the outgoing one at a corner,
// the last one at the end). ok is false when the path has no length. A count
// below 2 is one station at the start.
func pathStations(pts [][3]float64, count int) ([]pathStation, bool) {
	type seg struct {
		from, dir     [3]float64
		start, length float64
	}
	var segs []seg
	total := 0.0
	for i := 0; i+1 < len(pts); i++ {
		d := [3]float64{pts[i+1][0] - pts[i][0], pts[i+1][1] - pts[i][1], pts[i+1][2] - pts[i][2]}
		l := math.Sqrt(d[0]*d[0] + d[1]*d[1] + d[2]*d[2])
		if l == 0 {
			continue // a repeated point is not a segment and has no direction
		}
		segs = append(segs, seg{from: pts[i], dir: [3]float64{d[0] / l, d[1] / l, d[2] / l}, start: total, length: l})
		total += l
	}
	if len(segs) == 0 {
		return nil, false
	}
	if count < 2 {
		return []pathStation{{point: segs[0].from, direction: segs[0].dir}}, true
	}
	out := make([]pathStation, 0, count)
	for n := 0; n < count; n++ {
		s := total * float64(n) / float64(count-1)
		i := len(segs) - 1
		for j, sg := range segs {
			if s < sg.start+sg.length {
				i = j
				break
			}
		}
		sg := segs[i]
		t := math.Min(s-sg.start, sg.length)
		out = append(out, pathStation{
			point:     [3]float64{sg.from[0] + sg.dir[0]*t, sg.from[1] + sg.dir[1]*t, sg.from[2] + sg.dir[2]*t},
			direction: sg.dir,
		})
	}
	return out, true
}

// rotationTaking is the smallest rotation that turns +X onto the unit vector d:
// about the axis +X × d by the angle between them. Exactly opposite is a half
// turn about z, so the answer never depends on floating-point noise in an axis.
func rotationTaking(d [3]float64) [9]float64 {
	axis := [3]float64{0, -d[2], d[1]} // +X × d
	s := math.Sqrt(axis[1]*axis[1] + axis[2]*axis[2])
	c := d[0]
	if s < 1e-12 {
		if c > 0 {
			return [9]float64{1, 0, 0, 0, 1, 0, 0, 0, 1}
		}
		return [9]float64{-1, 0, 0, 0, -1, 0, 0, 0, 1}
	}
	kx, ky, kz := 0.0, axis[1]/s, axis[2]/s
	v := 1 - c
	// Rodrigues, row-major.
	return [9]float64{
		c + kx*kx*v, kx*ky*v - kz*s, kx*kz*v + ky*s,
		ky*kx*v + kz*s, c + ky*ky*v, ky*kz*v - kx*s,
		kz*kx*v - ky*s, kz*ky*v + kx*s, c + kz*kz*v,
	}
}
