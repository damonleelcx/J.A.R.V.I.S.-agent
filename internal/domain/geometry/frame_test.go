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

func randomPlacement(rng *rand.Rand) placement {
	deg := []float64{rng.Float64()*720 - 360, rng.Float64()*720 - 360, rng.Float64()*720 - 360}
	pos := []float64{rng.Float64()*400 - 200, rng.Float64()*400 - 200, rng.Float64()*400 - 200}
	p := placementOf(pos, deg, false)
	// Any of the three reflections, or none, applied in the local frame.
	if axis := []string{"", "x", "y", "z"}[rng.Intn(4)]; axis != "" {
		r, _ := reflectionAcross(axis)
		p.m = mulMat3(p.m, r)
	}
	return p
}

// Composing placements, reflections included, is placing through one then the other.
func TestPlacement_ComposingIsApplyingInTurn(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 5000; i++ {
		parent, child := randomPlacement(rng), randomPlacement(rng)
		v := [3]float64{rng.Float64()*50 - 25, rng.Float64()*50 - 25, rng.Float64()*50 - 25}
		want := parent.apply(child.apply(v))
		got := parent.then(child).apply(v)
		for k := 0; k < 3; k++ {
			if math.Abs(got[k]-want[k]) > 1e-7 {
				t.Fatalf("case %d: %v lands at %v composed, %v in turn", i, v, got, want)
			}
		}
	}
}

// What a part stores rebuilds the same placement — the reflection included.
func TestPlacement_TheStoredFormRebuildsTheSamePlacement(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	mirrored := 0
	for i := 0; i < 5000; i++ {
		p := randomPlacement(rng)
		pos, rot, m := p.stored()
		if m {
			mirrored++
		}
		if (det3(p.m) < 0) != m {
			t.Fatalf("case %d: determinant %v but mirrored=%v", i, det3(p.m), m)
		}
		back := placementOf(pos, rot, m)
		for k := range p.m {
			if math.Abs(back.m[k]-p.m[k]) > 1e-9 {
				t.Fatalf("case %d: stored as rot %v mirror %v, rebuilt entry %d %v, want %v", i, rot, m, k, back.m[k], p.m[k])
			}
		}
		for k := range p.pos {
			if back.pos[k] != p.pos[k] {
				t.Fatalf("case %d: position changed", i)
			}
		}
	}
	if mirrored < 3000 || mirrored > 4500 {
		t.Errorf("only %d of 5000 random placements were reflections; the generator is not exercising mirror", mirrored)
	}
}

// Two reflections cancel: mirroring a mirrored child stores no mirror.
func TestPlacement_TwoMirrorsCancel(t *testing.T) {
	ry, _ := reflectionAcross("y")
	rz, _ := reflectionAcross("z")
	a := placementOf([]float64{10, 0, 0}, []float64{0, 30, 0}, false)
	a.m = mulMat3(a.m, ry)
	b := placementOf([]float64{0, 5, 0}, []float64{0, 0, 45}, false)
	b.m = mulMat3(b.m, rz)
	if _, _, m := a.then(b).stored(); m {
		t.Error("a reflection inside a reflection was stored as mirrored")
	}
	if _, _, m := a.stored(); !m {
		t.Error("a single reflection was stored as not mirrored")
	}
}
