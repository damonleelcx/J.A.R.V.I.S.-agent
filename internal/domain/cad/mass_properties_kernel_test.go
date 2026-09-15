package cad_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// Where each built part's volume is, for mass, centre of gravity and envelope.
// Phase 5, stage V3 of docs/plan-2026-09-13-millions-of-parts.md.

// A part's volume, centre and box are the solid's, where the build placed it; and
// a roll-up with a density on both parts weighs them by it.
func TestKernel_EachPartIsMeasuredWhereItWasBuilt(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	steel := &geometry.Material{Name: "steel", Density: 7850}
	doc := geometry.Document{Name: "pair", Units: "mm", Parts: []geometry.Part{
		{ID: "block", Name: "Block", Shape: "box", Material: steel,
			Size:     map[string]float64{"width": 10, "height": 20, "depth": 30},
			Position: []float64{100, 0, 0}, Rotation: []float64{0, 0, 0}},
		{ID: "pin", Name: "Pin", Shape: "cylinder", Material: steel,
			Size:     map[string]float64{"radius": 5, "height": 10},
			Position: []float64{0, 50, 0}, Rotation: []float64{0, 0, 0}},
	}}
	got, err := k.BuildProperties(ctx, doc, geometry.Millimetre)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]geometry.SolidMeasure{}
	for _, m := range got.Properties {
		byID[m.ID] = m
	}
	block, pin := byID["block"], byID["pin"]
	if !block.Measured || !pin.Measured {
		t.Fatalf("parts not measured: %+v", got.Properties)
	}
	if math.Abs(block.Volume-6000) > 1e-6 || math.Abs(pin.Volume-math.Pi*25*10) > 1e-6 {
		t.Errorf("volumes %g and %g mm³, want 6000 and %g", block.Volume, pin.Volume, math.Pi*250)
	}
	for _, c := range []struct {
		name       string
		have, want [3]float64
	}{{"block", block.Centroid, [3]float64{100, 0, 0}}, {"pin", pin.Centroid, [3]float64{0, 50, 0}}} {
		for a := 0; a < 3; a++ {
			if math.Abs(c.have[a]-c.want[a]) > 1e-6 {
				t.Errorf("%s's centre is %v, want %v: where the build placed it", c.name, c.have, c.want)
				break
			}
		}
	}
	if math.Abs(block.Bounds[0]-95) > 1e-6 || math.Abs(block.Bounds[3]-105) > 1e-6 {
		t.Errorf("block's box %v; x runs 95 to 105", block.Bounds)
	}

	r := geometry.MassProperties(doc, got.Properties)
	if r.Basis != geometry.MassByDensity {
		t.Fatalf("basis %q; both parts are steel (without density %v)", r.Basis, r.WithoutDensity)
	}
	if want := (6000 + math.Pi*250) * 1e-9 * 7850; math.Abs(r.Groups[0].Mass-want) > 1e-12 {
		t.Errorf("mass %g kg, want %g", r.Groups[0].Mass, want)
	}
}

// propertiesComparison is what testdata/part_properties.py reports.
type propertiesComparison struct {
	Parts      int      `json:"parts"`
	Compared   int      `json:"compared"`
	WorstMM    float64  `json:"worst_mm"`
	Mismatches []string `json:"mismatches"`
}

// A copy's volume and centre measured once per shape are what measuring the
// placed solid gives: turned, mirrored, and — for a part a feature changed —
// measured on its own.
func TestKernel_APartsCentreMeasuredPerShapeIsItsSolidsCentre(t *testing.T) {
	python := os.Getenv("FORGE_CAD_PYTHON")
	if python == "" {
		t.Skip("FORGE_CAD_PYTHON is unset; skipping the CAD kernel tests")
	}
	out, err := exec.Command(python, filepath.Join("testdata", "part_properties.py"), "sidecar.py").Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			t.Fatalf("comparing part properties: %v\n%s", err, exit.Stderr)
		}
		t.Fatalf("comparing part properties: %v", err)
	}
	var got propertiesComparison
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("reading the comparison: %v\n%s", err, out)
	}
	t.Logf("%d parts compared, centres at most %.3g mm apart", got.Compared, got.WorstMM)
	for _, m := range got.Mismatches {
		t.Error(m)
	}
	if got.Parts < 10 || got.Compared != got.Parts {
		t.Errorf("compared %d of %d parts; the fixture has 13", got.Compared, got.Parts)
	}
}
