package agent

import (
	"context"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/cad"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// A car's wheels, lug nuts on a polar pattern, attached across assemblies, built in
// the real kernel (2026-09-17). #118's live car never reached a pattern, so the
// contract's polar-pattern and cylinder-axis sentences, and whether lug nuts end up
// buried, were untested where it counts: in OCCT, on a wheel placed from the root at
// another assembly's interface (#111), one side mirrored.
//
// Offline: the wheels step is a scripted reply written as the contract teaches — rim
// and tyre upright on Y, ISO 4032 M12 nuts a ring "about": "y" at the rim's outer
// face, the wheel turned [0, 0, 90] where it is placed — and once as live run 2 wrote
// it, the ring at the wheel's centre. Nothing was found wrong (see the PR record); this
// fence keeps it that way.

// wheeledCarBase is carOnSuspension with a hub on each corner: a cylinder turned onto X
// whose outer face is at x = -190 in the suspension's frame (its inner face touching the
// arm's end at -150), and the "hub" interface a rim's half-width (100) beyond that, so a
// wheel centred on it has its inner face on the hub face.
func wheeledCarBase() *Prototype {
	d := carOnSuspension()
	d.Definitions = append(d.Definitions, geometry.Part{ID: "hub", Shape: "cylinder",
		Size: map[string]float64{"radius": 70, "height": 40}, Position: []float64{-170, 0, 0}, Rotation: []float64{0, 0, 90}})
	d.Assemblies[0].Children = append(d.Assemblies[0].Children, geometry.Child{ID: "hub", Ref: "hub"})
	d.Assemblies[0].Interfaces = []geometry.Interface{{ID: "hub", Position: []float64{-290, 0, 0}}}
	return d
}

// wheelsStep is the wheels step's reply with the nuts' ring at nutY on the wheel's axis.
func wheelsStep(nutY string) string {
	return `{"speech":"wheels","prototype_edit":{"patch":{
	  "definitions":[
	    {"id":"rim","shape":"cylinder","size":{"radius":230,"height":200}},
	    {"id":"tyre","shape":"revolve","axis":"y","profile":[{"x":230,"y":-110},{"x":330,"y":-110},{"x":330,"y":110},{"x":230,"y":110}]},
	    {"id":"lug-nut","shape":"standard","standard":"ISO 4032 M12"}],
	  "assemblies":[{"id":"wheel","children":[
	    {"id":"rim","ref":"rim"},{"id":"tyre","ref":"tyre"},
	    {"id":"lug-nut","ref":"lug-nut","position":[60,` + nutY + `,0],"pattern":{"kind":"polar","count":5,"about":"y"}}]}]},
	  "placements":[{"id":"left-wheel","at":"suspension-left/hub","rotation":[0,0,90]},
	                {"id":"right-wheel","at":"suspension-right/hub","rotation":[0,0,90]}]}}`
}

func carKernel(t *testing.T) *cad.Kernel {
	t.Helper()
	python := os.Getenv("FORGE_CAD_PYTHON")
	if python == "" {
		t.Skip("FORGE_CAD_PYTHON is unset; this fence builds the car in the real kernel")
	}
	k := cad.New(python, logx.Discard())
	t.Cleanup(k.Close)
	return k
}

// builtWheels runs the wheels step over wheeledCarBase and builds the car in the kernel.
func builtWheels(t *testing.T, k *cad.Kernel, nutY string) (*Prototype, *cad.Build) {
	t.Helper()
	c := &Conversation{client: &scriptedStub{replies: []string{wheelsStep(nutY)}}}
	car, note := c.buildOneStep(context.Background(), wheeledCarBase(), "a car",
		buildTask{Name: "Wheels", What: "wheels", Assembly: "wheel"}, 3, 5)
	if car == nil {
		t.Fatalf("the wheels step was refused: %q", note)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	built, err := k.BuildProperties(ctx, *car, geometry.Millimetre)
	if err != nil {
		t.Fatal(err)
	}
	// 2 corners × (arm, hub, rim, tyre and 5 nuts).
	if built.Parts != 18 || len(built.Skipped) != 0 || len(built.FeatureFailures) != 0 {
		t.Fatalf("built %d parts (skipped %v, features failed %v); two corners of an arm, a hub, a rim, a tyre "+
			"and five nuts are 18", built.Parts, built.Skipped, built.FeatureFailures)
	}
	return car, built
}

// ‼️ Written as the contract says, every nut is where Go places it, on a radius-60 ring
// round the wheel's own axis (X, after the turn), five copies 72° apart, its own axis
// along X, sitting on the rim's outer face on the outboard side of each wheel — the
// mirrored right side included — and nothing in the car shares material.
func TestKernelCar_LugNutsOnAPolarPatternAcrossAssembliesSitOnTheRimFace(t *testing.T) {
	k := carKernel(t)
	car, built := builtWheels(t, k, "105.4") // the ISO 4032 M12 nut is 10.8 mm high
	if built.InterferencesFound != 0 || !built.InterferencesBuriedCounted || built.InterferencesTruncated {
		t.Errorf("the car shares material in %d pair(s) (%+v, truncated %v); written as the contract says, nothing touches "+
			"more than a face", built.InterferencesFound, built.Interferences, built.InterferencesTruncated)
	}

	measured := map[string]geometry.SolidMeasure{}
	for _, m := range built.Properties {
		measured[m.ID] = m
	}
	// The kernel builds what Go places: every part's centre of volume is its placed
	// position (each is symmetric about its own origin).
	for _, p := range car.Expanded().Parts {
		m, ok := measured[p.ID]
		if !ok || !m.Measured {
			t.Errorf("%s is placed and the kernel did not measure it", p.ID)
			continue
		}
		for i := 0; i < 3; i++ {
			if math.Abs(m.Centroid[i]-p.Position[i]) > 1e-6 {
				t.Errorf("%s: the kernel's centre is %v and Go places it at %v", p.ID, m.Centroid, p.Position)
				break
			}
		}
	}

	extent := func(m geometry.SolidMeasure, axis int) float64 { return m.Bounds[axis+3] - m.Bounds[axis] }
	for _, side := range []struct {
		wheel   string
		outward float64
	}{{"left-wheel", -1}, {"right-wheel", 1}} {
		rim := measured[side.wheel+"/rim"]
		if math.Abs(extent(rim, 0)-200) > 1e-6 || math.Abs(extent(rim, 1)-460) > 1e-6 || math.Abs(extent(rim, 2)-460) > 1e-6 {
			t.Errorf("%s rim box %v: its height (200) runs along the axle, X, only if the cylinder stands on its Y "+
				"and the turn lays it on X", side.wheel, rim.Bounds)
		}
		outerFace := rim.Bounds[3]
		if side.outward < 0 {
			outerFace = rim.Bounds[0]
		}
		angles := map[int]bool{}
		for n := 1; n <= 5; n++ {
			id := side.wheel + "/lug-nut-" + string(rune('0'+n))
			nut, ok := measured[id]
			if !ok {
				t.Errorf("%s was not built; a polar pattern of 5 places five nuts", id)
				continue
			}
			dy, dz := nut.Centroid[1]-rim.Centroid[1], nut.Centroid[2]-rim.Centroid[2]
			if r := math.Hypot(dy, dz); math.Abs(r-60) > 1e-6 {
				t.Errorf("%s is %.6f from the wheel's axis; the ring is radius 60 round it", id, r)
			}
			angles[int(math.Round(math.Atan2(dz, dy)*180/math.Pi))] = true
			if math.Abs(extent(nut, 0)-10.8) > 1e-6 {
				t.Errorf("%s box %v: a nut's axis is the wheel's, X, so it is 10.8 mm along X", id, nut.Bounds)
			}
			inner := nut.Bounds[0]
			if side.outward < 0 {
				inner = nut.Bounds[3]
			}
			if math.Abs(inner-outerFace) > 1e-6 || (nut.Centroid[0]-rim.Centroid[0])*side.outward <= 0 {
				t.Errorf("%s spans x %.3f to %.3f; it sits on the rim's outboard face at x = %.3f",
					id, nut.Bounds[0], nut.Bounds[3], outerFace)
			}
		}
		if len(angles) != 5 {
			t.Errorf("%s's five nuts sit at %d distinct angles round the axle: %v", side.wheel, len(angles), angles)
		}
	}
}

// ‼️ Written as live run 2 wrote it, the ring at the wheel's centre, every nut is buried
// in its rim and the kernel says so, by count and by name — the finding a repair acts on.
func TestKernelCar_LugNutsRingedAtTheWheelsCentreAreReportedBuried(t *testing.T) {
	k := carKernel(t)
	_, built := builtWheels(t, k, "0")
	if built.InterferencesBuried != 10 || !built.InterferencesBuriedCounted {
		t.Errorf("the kernel counts %d buried clash(es) (counted %v); ten nuts inside two rims are 10",
			built.InterferencesBuried, built.InterferencesBuriedCounted)
	}
	buried := 0
	for _, i := range built.Interferences {
		if !i.Buried() {
			continue
		}
		buried++
		nut, rim := i.A, i.B
		if strings.Contains(rim, "lug-nut") {
			nut, rim = rim, nut
		}
		if !strings.Contains(nut, "/lug-nut-") || !strings.HasSuffix(rim, "/rim") || strings.Split(nut, "/")[0] != strings.Split(rim, "/")[0] {
			t.Errorf("buried pair %s x %s is not a nut in its own wheel's rim", i.A, i.B)
		}
	}
	if buried != 10 {
		t.Errorf("%d buried pair(s) listed; want all ten nuts named: %+v", buried, built.Interferences)
	}
}
