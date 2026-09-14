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

// placeInFrame is where a child placed at childPos / childRot (degrees) inside a
// frame at parentPos / parentRot (degrees) ends up in the frame's parent.
//
// A point v of the child lands at R_child·v + p_child inside the frame, and the
// frame puts that at R_parent·(R_child·v + p_child) + p_parent. So the child's
// rotation there is R_parent·R_child, and its position is R_parent·p_child + p_parent.
func placeInFrame(parentPos, parentRot, childPos, childRot []float64) (pos, rot []float64) {
	rp := degreesToRadians3(parentRot)
	var cp, pp [3]float64
	copy(cp[:], padTo3(childPos))
	copy(pp[:], padTo3(parentPos))
	moved := translate(rotate(cp, rp), pp)
	turned := EulerDegreesFromMatrix(mulMat3(RotationMatrix(rp), RotationMatrix(degreesToRadians3(childRot))))
	return []float64{moved[0], moved[1], moved[2]}, []float64{turned[0], turned[1], turned[2]}
}
