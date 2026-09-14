package geometry

import (
	"math"
	"math/rand"
	"testing"
)

// The inverse is held to the MATRIX: angles have more than one spelling, a
// rotation does not.
func TestEulerDegreesFromMatrix_IsTheInverseOfRotationMatrix(t *testing.T) {
	rng := rand.New(rand.NewSource(20260913))
	check := func(deg [3]float64) {
		t.Helper()
		want := RotationMatrix(degreesToRadians3(deg[:]))
		back := EulerDegreesFromMatrix(want)
		got := RotationMatrix(degreesToRadians3(back[:]))
		for i := range want {
			if math.Abs(got[i]-want[i]) > 1e-9 {
				t.Fatalf("rotation %v came back as %v: matrix entry %d is %v, want %v", deg, back, i, got[i], want[i])
			}
		}
	}
	for i := 0; i < 5000; i++ {
		check([3]float64{rng.Float64()*720 - 360, rng.Float64()*720 - 360, rng.Float64()*720 - 360})
	}
	// Gimbal lock, where x and z describe the same motion.
	for _, deg := range [][3]float64{{0, 90, 0}, {30, 90, 40}, {-70, -90, 15}, {180, 90, -180}, {0, 0, 0}, {90, 0, 0}, {0, 0, 180}} {
		check(deg)
	}
}

// Placing through the composed frame is placing through the child, then the parent.
func TestPlaceInFrame_ComposesChildThenParent(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	r := func() []float64 {
		return []float64{rng.Float64()*720 - 360, rng.Float64()*720 - 360, rng.Float64()*720 - 360}
	}
	p := func() []float64 {
		return []float64{rng.Float64()*400 - 200, rng.Float64()*400 - 200, rng.Float64()*400 - 200}
	}
	for i := 0; i < 5000; i++ {
		parentPos, parentRot, childPos, childRot := p(), r(), p(), r()
		v := [3]float64{rng.Float64()*50 - 25, rng.Float64()*50 - 25, rng.Float64()*50 - 25}

		// Child first, then parent.
		var cp, pp [3]float64
		copy(cp[:], childPos)
		copy(pp[:], parentPos)
		inFrame := translate(rotate(v, degreesToRadians3(childRot)), cp)
		want := translate(rotate(inFrame, degreesToRadians3(parentRot)), pp)

		pos, rot := placeInFrame(parentPos, parentRot, childPos, childRot)
		var wp [3]float64
		copy(wp[:], pos)
		got := translate(rotate(v, degreesToRadians3(rot)), wp)
		for k := 0; k < 3; k++ {
			if math.Abs(got[k]-want[k]) > 1e-7 {
				t.Fatalf("case %d: point %v lands at %v through the composed frame, %v through child then parent",
					i, v, got, want)
			}
		}
	}
}

// A frame with no position and no rotation changes nothing.
func TestPlaceInFrame_TheIdentityFrameChangesNothing(t *testing.T) {
	pos, rot := placeInFrame(nil, nil, []float64{12, -3, 7}, []float64{10, 20, 30})
	for i, w := range []float64{12, -3, 7} {
		if math.Abs(pos[i]-w) > 1e-12 {
			t.Errorf("position %v, want [12 -3 7]", pos)
		}
	}
	want := RotationMatrix(degreesToRadians3([]float64{10, 20, 30}))
	got := RotationMatrix(degreesToRadians3(rot))
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-12 {
			t.Errorf("rotation %v does not match [10 20 30]", rot)
			break
		}
	}
}
