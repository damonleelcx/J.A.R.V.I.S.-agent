package cad_test

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// The car template builds through OpenCASCADE: every part, every feature, and no
// part buried in another, at the default proportions and at every extreme the
// fixtures name (geometry/car_fixtures.go). Stage C1 of the looks-designed work.
//
// The body is a smooth loft through seventeen stations with rounded corners, cut by
// four wheel arches; the wheels sit inside those arches and the splitter and fins
// touch the floor without entering it. Buried is the kernel's own measure
// (geometry.BuriedFraction), so a wheel left outside its arch fails here.
func TestKernel_BuildsTheCarTemplateWithNothingMissingOrBuried(t *testing.T) {
	// The kernel's own build limit, as production runs it: a car that needs longer is a
	// car no turn can build.
	k := kernel(t)
	names := make([]string, 0)
	for name := range geometry.CarFixturesForTest() {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
			defer cancel()
			doc := geometry.CarDocumentForTest(name)
			if f := doc.Faults(); len(f) != 0 {
				t.Fatalf("%d fault(s) before the kernel: %s", len(f), f[0].Detail)
			}
			got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "step")
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Skipped) != 0 || len(got.FeatureFailures) != 0 {
				t.Fatalf("skipped %v, feature failures %v", got.Skipped, got.FeatureFailures)
			}
			// One body, four tyres, four rims (each fused with its nuts), one splitter
			// and the fins: every solid the template writes, none lost.
			fins := 4.0
			if n, ok := geometry.CarFixturesForTest()[name]["diffuser_fins"]; ok {
				fins = n
			}
			if want := 10 + int(fins); got.Parts != want {
				t.Fatalf("%d solid(s) in the file; the car is %d", got.Parts, want)
			}
			buried := 0
			for _, i := range got.Interferences {
				if i.Buried() {
					buried++
					t.Errorf("buried: %s", i.Describe())
				}
			}
			if buried != 0 || got.InterferencesBuried != 0 {
				t.Fatalf("%d buried pair(s) (kernel count %d)", buried, got.InterferencesBuried)
			}
			// The car is the size it was asked to be. A smooth loft overshoots the
			// stations it passes through; eight stations at the cabin's corners put the
			// roof 13-61% above the stated height (docs/spikes/2026-09-18-car-template).
			size := geometry.CarFixturesForTest()[name]
			if h := got.Bounds[4]; h > size["height"]*1.02 || h < size["height"]*0.99 {
				t.Errorf("the car is %.0f high; it was asked to be %.0f", h, size["height"])
			}
			if l := got.Bounds[3] - got.Bounds[0]; l > size["length"]*1.001 || l < size["length"]*0.999 {
				t.Errorf("the car is %.0f long; it was asked to be %.0f", l, size["length"])
			}
			if !strings.HasPrefix(string(got.STEP), "ISO-10303-21;") {
				t.Fatal("no STEP file for the car")
			}
			t.Logf("%s: %d solids, %d interference(s) none buried, bounds %v phases %+v", name, got.Parts,
				len(got.Interferences), got.Bounds, got.Phases)
		})
	}
}
