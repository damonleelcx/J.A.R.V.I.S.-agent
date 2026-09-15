package cad_test

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// A clash slid along a cylinder's length or an extrusion's depth (next scale walls;
// sidecar.py, _slabs and _INTERFERENCE_SLIDE). #94 slid clashes along boxes only.
// docs/spikes/2026-09-15-next-scale-walls.

type prismRuns struct {
	Error string `json:"error"`
	Runs  []struct {
		Seed     int      `json:"seed"`
		Parts    int      `json:"parts"`
		Uncached cacheRun `json:"uncached"`
		Cached   cacheRun `json:"cached"`
	} `json:"runs"`
}

// Every clash reused along a prism is the clash measured on its own.
//
// testdata/interference_prisms.py, four seeds: shafts along each axis and turned
// with collars inside, at, and just inside and outside their ends by less than and
// more than the containment margin, off the axis, turned and leaning; pins across
// and leaning along them; extruded L girders plain, mirrored and turned with cleats
// and pins along them, near and over their ends, and leaning pins; a cone with rings
// along it, which must not slide; and a short extrusion with bars through it.
func TestKernel_AClashSlidAlongAPrismIsTheClashMeasuredAgain(t *testing.T) {
	var got prismRuns
	testdataJSON(t, "interference_prisms.py", &got, "1", "2", "3", "4")
	if got.Error != "" {
		t.Fatal(got.Error)
	}
	if len(got.Runs) != 4 {
		t.Fatalf("%d run(s), want 4", len(got.Runs))
	}
	for _, run := range got.Runs {
		u, c := run.Uncached, run.Cached
		t.Logf("seed %d, %d parts: %d pairs, %d clashes; every pair %d booleans, reused %d booleans + %d reused",
			run.Seed, run.Parts, u.Pairs, len(u.Found), u.Booleans, c.Booleans, c.Reused)
		if u.Truncated || c.Truncated || u.Pairs != c.Pairs || c.Booleans+c.Reused != u.Booleans {
			t.Fatalf("seed %d: the runs are not comparable: pairs %d and %d, booleans %d and %d+%d, truncated %v and %v",
				run.Seed, u.Pairs, c.Pairs, u.Booleans, c.Booleans, c.Reused, u.Truncated, c.Truncated)
		}
		if len(u.Found) < 150 || c.Reused*2 < u.Booleans {
			t.Fatalf("seed %d: %d clashes and %d of %d pairs reused; the fixture needs many clashes and many slides",
				run.Seed, len(u.Found), c.Reused, u.Booleans)
		}
		if len(c.Found) != len(u.Found) {
			t.Fatalf("seed %d: reused found %d clashes, every pair %d", run.Seed, len(c.Found), len(u.Found))
		}
		cached := map[[2]string]geometry.Interference{}
		for _, f := range c.Found {
			cached[[2]string{f.A, f.B}] = f
		}
		for _, want := range u.Found {
			have, ok := cached[[2]string{want.A, want.B}]
			if !ok {
				t.Errorf("seed %d: every pair found %s in %s, and the reused run did not", run.Seed, want.A, want.B)
				continue
			}
			// ‼️ 1e-4 of the volume, not the 1e-6 the box fences use. OCCT's common
			// volume of a curved clash moves with where the PAIR sits in the world: the
			// same shaft and leaning pin, the same relative pose, gave 381.67564 and
			// 381.681559 mm³ when both were moved by a rigid motion (1.7e-5 at most over
			// five motions), while repeating the boolean gave the same bits and sliding
			// the pair along the shaft moved it 1e-10. So the reference every pair
			// measures differs from itself by this much, reuse or no reuse — and #94's
			// same-pose cache already reuses across world placements. A wrong slide is
			// orders larger: a chord of a different depth, a ring higher up a cone.
			// docs/spikes/2026-09-15-next-scale-walls/boolean_noise.py.
			if math.Abs(have.Volume-want.Volume) > 1e-4*math.Max(1, want.Volume) ||
				math.Abs(have.Fraction-want.Fraction) > 1e-4*math.Max(1e-3, want.Fraction) {
				t.Errorf("seed %d, %s in %s: reused %.9g mm³, measured %.9g", run.Seed, want.A, want.B, have.Volume, want.Volume)
			}
		}
	}
}

// shaftAndGirder is a 4 m shaft along x with 191 collars round it 20 mm apart and a
// collar on each end, half over; and, 1 m away, a 4 m extruded L girder along z with
// 96 cleats on its flange 40 mm apart, a cleat on each end, half over, and 95 pins
// leaning 35° through the flange between the cleats.
//
// A collar is 10 mm along the shaft and 30 across it, so the shaft lies inside the
// collar's width and depth, and the collar inside the shaft's length: only the
// shaft's slab frees the collar along it, carried into the collar's frame. A cleat
// and a leaning pin are inside the girder's depth and nothing frees them from their
// own frames: only the girder's slab slides them.
func shaftAndGirder() geometry.Document {
	shaft := geometry.Part{ID: "shaft", Name: "Shaft", Shape: "cylinder", Size: map[string]float64{"radius": 10, "height": 4000}}
	collar := geometry.Part{ID: "collar", Name: "Collar", Shape: "box", Size: map[string]float64{"width": 10, "height": 30, "depth": 30}}
	ell := [][2]float64{{0, 0}, {40, 0}, {40, 6}, {6, 6}, {6, 40}, {0, 40}}
	profile := make([]geometry.Point, len(ell))
	for i, p := range ell {
		profile[i] = geometry.Point{X: p[0], Y: p[1]}
	}
	girder := geometry.Part{ID: "girder", Name: "Girder", Shape: "extrusion", Profile: profile,
		Size: map[string]float64{"depth": 4000}}
	cleat := geometry.Part{ID: "cleat", Name: "Cleat", Shape: "box", Size: map[string]float64{"width": 20, "height": 20, "depth": 8}}
	pin := geometry.Part{ID: "pin", Name: "Pin", Shape: "cylinder", Size: map[string]float64{"radius": 1.5, "height": 30}}
	return geometry.Document{Name: "drive", Units: "mm", Root: "drive",
		Definitions: []geometry.Part{shaft, collar, girder, cleat, pin},
		Assemblies: []geometry.Assembly{{ID: "drive", Children: []geometry.Child{
			{ID: "shaft", Ref: "shaft", Rotation: []float64{0, 0, 90}},
			{ID: "collars", Ref: "collar", Position: []float64{-1900, 0, 0},
				Pattern: &geometry.Pattern{Kind: "linear", Count: 191, Offset: []float64{20, 0, 0}}},
			{ID: "left-collar", Ref: "collar", Position: []float64{-2000, 0, 0}},
			{ID: "right-collar", Ref: "collar", Position: []float64{2000, 0, 0}},
			{ID: "girder", Ref: "girder", Position: []float64{0, 1000, 0}},
			{ID: "cleats", Ref: "cleat", Position: []float64{20, 1003, -1900},
				Pattern: &geometry.Pattern{Kind: "linear", Count: 96, Offset: []float64{0, 0, 40}}},
			{ID: "bottom-cleat", Ref: "cleat", Position: []float64{20, 1003, -2000}},
			{ID: "top-cleat", Ref: "cleat", Position: []float64{20, 1003, 2000}},
			{ID: "leaning", Ref: "pin", Position: []float64{20, 1003, -1880}, Rotation: []float64{35, 0, 0},
				Pattern: &geometry.Pattern{Kind: "linear", Count: 95, Offset: []float64{0, 0, 40}}},
		}}}}
}

// Collars along a shaft and cleats and leaning pins along an extruded girder are
// each one clash wherever they sit, and the ones over an end are measured. Before
// this, each was a boolean: 191 collars, 96 cleats and 95 pins.
func TestKernel_CollarsAlongAShaftAndCleatsAlongAGirderPayForOneBooleanEach(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	got, err := k.BuildDocument(ctx, shaftAndGirder(), geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d parts: %d pair(s), %d boolean(s) paid for, %d reused, %d interference(s), truncated=%v",
		got.Parts, got.InterferencePairs, got.InterferenceBooleans, got.InterferenceReused,
		len(got.Interferences), got.InterferencesTruncated)
	const clashes = 193 + 98 + 95
	if got.InterferencesTruncated || len(got.Interferences) != clashes || got.InterferencePairs != clashes {
		t.Fatalf("%d interference(s) from %d pair(s), truncated=%v; want every collar, cleat and pin, %d",
			len(got.Interferences), got.InterferencePairs, got.InterferencesTruncated, clashes)
	}
	// One for the collars inside the shaft's length and one for each end collar; the
	// same for the cleats; one for the leaning pins.
	if got.InterferenceBooleans > 7 || got.InterferenceBooleans+got.InterferenceReused != clashes {
		t.Errorf("%d boolean(s) paid for and %d reused; sliding along the shaft and the girder is at most 7",
			got.InterferenceBooleans, got.InterferenceReused)
	}
	collarInside, cleatInside := math.Pi*100*10, 20.0*6*8
	var lean float64
	counts := map[string]int{}
	for _, c := range got.Interferences {
		switch {
		case c.A == "left-collar" || c.A == "right-collar":
			counts["end collar"]++
			near(t, c, collarInside/2, 0.01)
		case strings.HasPrefix(c.A, "collars"):
			counts["collar"]++
			near(t, c, collarInside, 0.01)
		case c.A == "bottom-cleat" || c.A == "top-cleat":
			counts["end cleat"]++
			near(t, c, cleatInside/2, 0.01)
		case strings.HasPrefix(c.A, "cleats"):
			counts["cleat"]++
			near(t, c, cleatInside, 0.01)
		case strings.HasPrefix(c.A, "leaning"):
			counts["leaning pin"]++
			if lean == 0 {
				lean = c.Volume
			}
			near(t, c, lean, 1e-6)
		default:
			t.Errorf("an unexpected clash: %+v", c)
		}
	}
	want := map[string]int{"end collar": 2, "collar": 191, "end cleat": 2, "cleat": 96, "leaning pin": 95}
	for name, n := range want {
		if counts[name] != n {
			t.Errorf("found %d %s clash(es), want %d", counts[name], name, n)
		}
	}
}

func near(t *testing.T, c geometry.Interference, want, rel float64) {
	t.Helper()
	if math.Abs(c.Volume-want) > rel*want {
		t.Errorf("%s in %s shares %.4f mm³, want %.4f", c.A, c.B, c.Volume, want)
	}
}
