package geometry

import "math"

// PartExtents is how big each part actually comes out, keyed by part id.
//
// # Why measured rather than read off "size"
//
// A box carries its three dimensions in "size" and an extrusion carries one:
// the other two are in its outline, and a sweep's are split between its outline
// and its path. Reading "size" would report an extrusion as "4500 deep and
// nothing else" — which is exactly the blindness that let a car body come back
// 4500mm wide with only the one correct number on screen.
//
// So this measures the same triangles the viewport draws and the exporter
// writes, after placement: the answer is the part's real bounding box in the
// assembly's frame, including its rotation. A part that does not build at all
// has no entry, which is a miss rather than a wrong number.
func PartExtents(d Document, unit Unit) map[string][3]float64 {
	m := Tessellate(d, unit)
	out := make(map[string][3]float64, len(m.Groups))
	for _, g := range m.Groups {
		if len(g.Triangles) == 0 {
			continue
		}
		lo := [3]float64{math.Inf(1), math.Inf(1), math.Inf(1)}
		hi := [3]float64{math.Inf(-1), math.Inf(-1), math.Inf(-1)}
		for _, t := range g.Triangles {
			for _, v := range [3][3]float64{t.A, t.B, t.C} {
				for i := 0; i < 3; i++ {
					lo[i] = math.Min(lo[i], v[i])
					hi[i] = math.Max(hi[i], v[i])
				}
			}
		}
		out[g.PartID] = [3]float64{hi[0] - lo[0], hi[1] - lo[1], hi[2] - lo[2]}
	}
	return out
}
