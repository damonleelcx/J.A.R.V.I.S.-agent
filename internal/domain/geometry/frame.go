package geometry

import "math"

// Frames: placing a part inside an assembly that is itself placed.
//
// # Why this exists
//
// Phase 1 of docs/plan-2026-09-13-millions-of-parts.md moves the document from a
// flat list of parts at absolute positions to designs placed inside assemblies.
// A part in a suspension corner that is mirrored onto the other side of a car has
// a position and rotation of its own, inside the corner, inside the car. Every
// reader of a document still reads parts at absolute positions, so flattening
// has to compose those frames into the one position and rotation a Part stores.
//
// The stored rotation is Euler XYZ in DEGREES, read by RotationMatrix. Composing
// two rotations is a matrix product, and turning the product back into the three
// numbers a Part stores needs the exact inverse of RotationMatrix. Nothing in
// this package had one. The browser has to reach the identical answer (it draws
// the flattened parts), so the math is kept here, small and proven, and mirrored
// term for term in forge3d.js.

// EulerDegreesFromMatrix returns the rotation, in degrees, that RotationMatrix
// turns into m. It is the inverse of RotationMatrix for every rotation matrix:
// RotationMatrix(radians(EulerDegreesFromMatrix(m))) == m.
//
// The angles are not unique — a rotation has more than one Euler spelling — so
// the answer is held to the MATRIX, not to the angles that made it. At gimbal lock
// (a quarter turn about y, where x and z describe the same motion) z is chosen as
// 0 and x carries the whole turn.
func EulerDegreesFromMatrix(m [9]float64) [3]float64 {
	// RotationMatrix is, row-major:
	//   cy*cz              -cy*sz               sy
	//   sx*sy*cz + cx*sz   -sx*sy*sz + cx*cz   -sx*cy
	//  -cx*sy*cz + sx*sz    cx*sy*sz + sx*cz    cx*cy
	sy := math.Max(-1, math.Min(1, m[2]))
	y := math.Asin(sy)
	cy := math.Cos(y)
	var x, z float64
	if cy > 1e-9 {
		x = math.Atan2(-m[5], m[8])
		z = math.Atan2(-m[1], m[0])
	} else {
		// Gimbal lock: cy = 0. Choose z = 0; then m[3] = sx*sy and m[4] = cx.
		z = 0
		x = math.Atan2(m[3]*sy, m[4])
	}
	const deg = 180 / math.Pi
	return [3]float64{x * deg, y * deg, z * deg}
}

// degreesToRadians3 is a stored rotation in the unit RotationMatrix reads.
func degreesToRadians3(r []float64) [3]float64 {
	var out [3]float64
	copy(out[:], padTo3(r))
	for i := range out {
		out[i] *= math.Pi / 180
	}
	return out
}

func mulMat3(a, b [9]float64) [9]float64 {
	var out [9]float64
	for r := 0; r < 3; r++ {
		for c := 0; c < 3; c++ {
			out[r*3+c] = a[r*3]*b[c] + a[r*3+1]*b[3+c] + a[r*3+2]*b[6+c]
		}
	}
	return out
}

// placement is where something is and how it is turned — and, since D1c, whether
// it is REFLECTED. A position plus a 3×3 matrix that is a rotation, or a rotation
// composed with one reflection.
//
// # Why a matrix and not three angles
//
// A reflection cannot be written as a rotation: a mirrored left control arm has
// the opposite handedness from the right one, and no Euler angles turn one into
// the other. Composing frames through a tree that mirrors a corner onto the other
// side of a car therefore has to carry the reflection through every level, and a
// matrix does that for free: two reflections multiply back into a rotation.
//
// A part STORES the result as a rotation plus one flag (Part.Mirror): "negate
// local x, then rotate". Every reflection-with-rotation can be written that way —
// a mirror across y is the flag plus a half turn about z — so there is exactly one
// stored spelling, and every reader honours one rule.
type placement struct {
	pos [3]float64
	m   [9]float64
}

// mirrorX negates local x: the one reflection a stored part carries.
var mirrorX = [9]float64{-1, 0, 0, 0, 1, 0, 0, 0, 1}

// reflectionAcross is the reflection through the plane normal to axis, or the
// identity for "". ok is false for anything else.
func reflectionAcross(axis string) (m [9]float64, ok bool) {
	switch axis {
	case "":
		return [9]float64{1, 0, 0, 0, 1, 0, 0, 0, 1}, true
	case "x":
		return mirrorX, true
	case "y":
		return [9]float64{1, 0, 0, 0, -1, 0, 0, 0, 1}, true
	case "z":
		return [9]float64{1, 0, 0, 0, 1, 0, 0, 0, -1}, true
	}
	return [9]float64{}, false
}

// placementOf is a stored position and rotation (degrees), with local x reflected
// first when mirrored — exactly how a Part is placed.
func placementOf(pos, rotDeg []float64, mirrored bool) placement {
	var p placement
	copy(p.pos[:], padTo3(pos))
	p.m = RotationMatrix(degreesToRadians3(rotDeg))
	if mirrored {
		p.m = mulMat3(p.m, mirrorX)
	}
	return p
}

func mulMatVec(m [9]float64, v [3]float64) [3]float64 {
	return [3]float64{
		m[0]*v[0] + m[1]*v[1] + m[2]*v[2],
		m[3]*v[0] + m[4]*v[1] + m[5]*v[2],
		m[6]*v[0] + m[7]*v[1] + m[8]*v[2],
	}
}

func det3(m [9]float64) float64 {
	return m[0]*(m[4]*m[8]-m[5]*m[7]) - m[1]*(m[3]*m[8]-m[5]*m[6]) + m[2]*(m[3]*m[7]-m[4]*m[6])
}

// apply places a point.
func (p placement) apply(v [3]float64) [3]float64 {
	return translate(mulMatVec(p.m, v), p.pos)
}

// then is this placement's frame holding child: a point placed by child and then
// by p lands where p.then(child) places it.
func (p placement) then(child placement) placement {
	return placement{pos: p.apply(child.pos), m: mulMat3(p.m, child.m)}
}

// stored is the placement as a Part stores it: position, rotation in degrees, and
// whether local x is reflected first. placementOf(stored()) is this placement.
func (p placement) stored() (pos, rot []float64, mirrored bool) {
	m := p.m
	if det3(m) < 0 {
		// m = R·Mx for a proper rotation R, so R = m·Mx (Mx is its own inverse).
		m = mulMat3(m, mirrorX)
		mirrored = true
	}
	e := EulerDegreesFromMatrix(m)
	return []float64{p.pos[0], p.pos[1], p.pos[2]}, []float64{e[0], e[1], e[2]}, mirrored
}
