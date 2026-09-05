package geometry_test

import (
	"math"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

func inchCube() geometry.Document {
	return geometry.Document{
		Name: "cube", Units: "in",
		Parts: []geometry.Part{{ID: "c", Name: "Cube", Shape: "box",
			Size:     map[string]float64{"width": 2, "height": 2, "depth": 2},
			Position: []float64{1, 0, 0}, Rotation: []float64{0, 0, 0}}},
	}
}

// Every length comes back in millimetres, because a STEP file states
// millimetres and the numbers in it have to mean that.
func TestSolids_ConvertsEveryLengthToMillimetres(t *testing.T) {
	got, _ := geometry.Solids(inchCube(), geometry.Inch)
	if len(got) != 1 {
		t.Fatalf("%d solids", len(got))
	}
	for _, key := range []string{"width", "height", "depth"} {
		if v := got[0].Dims[key]; math.Abs(v-50.8) > 1e-9 {
			t.Errorf("%s = %v mm, want 50.8 (2 in)", key, v)
		}
	}
	if v := got[0].Position[0]; math.Abs(v-25.4) > 1e-9 {
		t.Errorf("position x = %v mm, want 25.4 (1 in) — a correctly sized part in the "+
			"wrong place is still the wrong part", v)
	}
}

// The guard in Solids itself, reached directly. The kernel refuses an
// unconvertible unit before it ever gets here, so this is the only thing that
// holds the inner half — and a drill showed the outer guard alone made this
// look covered when it was not.
func TestSolids_BuildsNothingFromAnUnconvertibleUnit(t *testing.T) {
	doc := inchCube()
	doc.Units = "furlongs"

	got, notes := geometry.Solids(doc, geometry.UnitUnspecified)
	if len(got) != 0 {
		t.Fatalf("%d solids came back at a guessed scale: %+v", len(got), got)
	}
	if len(notes) == 0 || !strings.Contains(strings.Join(notes, " "), "no unit FORGE can convert") {
		t.Errorf("nothing said why there is no geometry: %v", notes)
	}
}

// A millimetre document is unchanged, so the conversion cannot be scaling the
// common case by some factor that happens to be 1 for the wrong reason.
func TestSolids_LeavesMillimetresAlone(t *testing.T) {
	doc := inchCube()
	doc.Units = "mm"
	got, _ := geometry.Solids(doc, geometry.Millimetre)
	if v := got[0].Dims["width"]; v != 2 {
		t.Errorf("width = %v, want 2", v)
	}
	if v := got[0].Position[0]; v != 1 {
		t.Errorf("position x = %v, want 1", v)
	}
}
