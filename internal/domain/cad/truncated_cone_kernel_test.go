package cad_test

import (
	"math"
	"testing"
)

// Which end of a truncated cone the kernel builds as the top.
// testdata/truncated_cone.py; docs/bugfix/2026-09-21-truncated-cone-built-upside-down.md.

type coneStation struct {
	T    float64 `json:"t"`
	Area float64 `json:"area"`
}

type conePart struct {
	ID       string        `json:"id"`
	Volume   float64       `json:"volume"`
	Centroid [3]float64    `json:"centroid"`
	Axis     [3]float64    `json:"axis"`
	Position [3]float64    `json:"position"`
	Stations []coneStation `json:"stations"`
}

type coneFixture struct {
	Error string     `json:"error"`
	Parts []conePart `json:"parts"`
}

// The frustum the fixture builds: 20 mm at the bottom, 5 mm at the top, 10 tall.
const (
	coneRadius = 20.0
	coneTop    = 5.0
	coneHeight = 10.0
)

// coneRadiusAt is the radius t mm from the frustum's centre along its own axis,
// read off the contract: radius at -height/2, radius_top at +height/2, straight
// between them (internal/domain/geometry/mesh.go, func cylinder).
func coneRadiusAt(t float64) float64 {
	return coneRadius + (t+coneHeight/2)/coneHeight*(coneTop-coneRadius)
}

// A truncated cone's radius_top is its TOP, and the kernel agrees with the
// renderer about which end that is.
//
// # Why every existing orientation fence was blind to this
//
// TestKernel_ACylinderPointsTheWayThisSystemDrawsIt builds a plain cylinder and
// reads its extent; TestKernel_TheKernelAndTheRendererAgreeAboutWhereAPartIs
// compares bounding boxes. A cylinder is the same at both ends, and a frustum's
// bounding box is the same end-for-end too (+-20 across, +-5 tall either way),
// so a solid built upside down produced identical numbers in both. So did its
// volume, its STEP file and its triangle count. The sidecar turned every
// cylinder and cone with Plane.XZ, whose normal is -Y, which landed radius_top
// at the BOTTOM for two years without a single test moving.
//
// # What can only be satisfied the right way up
//
// Two things, and neither of them is a length:
//
//   - the centre of volume. A frustum's centroid sits h(R^2+2Rr+3r^2)/(4(R^2+Rr+r^2))
//     from the LARGE base — 3.214 mm of 10 here, so 1.786 mm BELOW the middle when
//     the large base is down. Built end-for-end it comes out at +1.786.
//   - the area of the cross section cut near each end. Radius 8 at t=+3 is
//     201.1 mm^2 and radius 17 at t=-3 is 907.9; swapping them is the whole bug.
//
// Both are measured at stations taken along each copy's OWN axis, so the four
// copies — upright, turned a half turn about X, laid down a quarter turn about Z,
// and mirrored — all owe the same four numbers. A copy that came out end-for-end
// cannot borrow another copy's turn to look right.
func TestKernel_ATruncatedConesRadiusTopIsItsTop(t *testing.T) {
	var got coneFixture
	testdataJSON(t, "truncated_cone.py", &got)
	if got.Error != "" {
		t.Fatal(got.Error)
	}
	if len(got.Parts) != 4 {
		t.Fatalf("%d part(s), want 4: upright, turned, laid and mirrored", len(got.Parts))
	}

	// pi*h/3*(R^2+Rr+r^2), the frustum's volume: the same however it is turned,
	// which is exactly why it cannot be the fence on its own.
	wantVolume := math.Pi * coneHeight / 3 * (coneRadius*coneRadius + coneRadius*coneTop + coneTop*coneTop)
	// How far the centre of volume sits from the frustum's own middle, negative
	// because it leans towards the large base, which is DOWN.
	fromLarge := coneHeight * (coneRadius*coneRadius + 2*coneRadius*coneTop + 3*coneTop*coneTop) /
		(4 * (coneRadius*coneRadius + coneRadius*coneTop + coneTop*coneTop))
	wantOffset := fromLarge - coneHeight/2

	for _, p := range got.Parts {
		t.Logf("%s: volume %.4f mm³, centre %v, axis %v", p.ID, p.Volume, p.Centroid, p.Axis)
		if math.Abs(p.Volume-wantVolume) > 1e-6 {
			t.Errorf("%s: volume %g mm³, want %g — the fixture did not build the frustum it says it did",
				p.ID, p.Volume, wantVolume)
			continue
		}
		for a := 0; a < 3; a++ {
			want := p.Position[a] + wantOffset*p.Axis[a]
			if math.Abs(p.Centroid[a]-want) > 1e-6 {
				t.Errorf("%s: centre of volume %v, want %v on axis %d.\n"+
					"The frustum's centroid is %.4f mm from its LARGE base, so it leans %.4f mm "+
					"from the middle TOWARDS the base; the other sign means radius_top was built "+
					"at the bottom.", p.ID, p.Centroid, want, a, fromLarge, -wantOffset)
				break
			}
		}
		if len(p.Stations) < 2 {
			t.Errorf("%s: %d station(s); the fixture cuts four", p.ID, len(p.Stations))
			continue
		}
		for _, s := range p.Stations {
			r := coneRadiusAt(s.T)
			want := math.Pi * r * r
			if math.Abs(s.Area-want) > 1e-6*want {
				t.Errorf("%s: the section %+.1f mm along its own axis is %.3f mm², want %.3f "+
					"(radius %.3f). radius_top is the top: mesh.go draws radius_top at "+
					"+height/2 and radius at -height/2.", p.ID, s.T, s.Area, want, r)
			}
		}
	}

	// The fixture is only a fence while its two ends differ. A frustum that had
	// quietly become a cylinder would satisfy every line above with either end up.
	first, last := got.Parts[0].Stations[0], got.Parts[0].Stations[len(got.Parts[0].Stations)-1]
	if first.Area >= last.Area/2 {
		t.Fatalf("the fixture's ends measure %.3f and %.3f mm²: it is not tapered enough to "+
			"tell one end from the other", first.Area, last.Area)
	}
}
