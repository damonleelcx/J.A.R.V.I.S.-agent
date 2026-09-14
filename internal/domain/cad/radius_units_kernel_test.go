package cad_test

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// A fillet is the same physical size whatever unit the model is written in.
//
// Before 2026-09-13 a feature radius was sent in the document's units while every
// other length was converted to millimetres, so the same cube came back with
// three different fillets. Measured then: 975,587 mm³ in mm, 999,744 mm³ in cm.
// docs/bugfix/2026-09-13-feature-radii-were-sent-in-the-documents-units.md
func TestKernel_AFilletIsTheSameSizeInEveryUnit(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	build := func(units string, side, radius float64) float64 {
		t.Helper()
		unit, ok := geometry.ParseUnit(units)
		if !ok {
			t.Fatalf("unit %q", units)
		}
		doc := geometry.Document{Name: "cube", Units: units, Parts: []geometry.Part{{
			ID: "cube", Name: "Cube", Shape: "box",
			Size:     map[string]float64{"width": side, "height": side, "depth": side},
			Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}},
			Features: []geometry.Feature{{ID: "round", Op: "fillet", Of: "cube", Radius: radius, Edges: "all"}}}
		got, err := k.BuildDocument(ctx, doc, unit, "")
		if err != nil {
			t.Fatalf("%s: %v", units, err)
		}
		if len(got.FeatureFailures) > 0 {
			t.Fatalf("%s: the fillet was not applied: %v", units, got.FeatureFailures)
		}
		return got.Volume
	}

	// A 100 mm cube with a 10 mm fillet, written three ways.
	mm := build("mm", 100, 10)
	cm := build("cm", 10, 1)
	m := build("m", 0.1, 0.01)
	for name, v := range map[string]float64{"cm": cm, "m": m} {
		if math.Abs(v-mm) > 1 {
			t.Errorf("the same filleted cube is %.1f mm³ written in mm and %.1f mm³ written in %s", mm, v, name)
		}
	}
}

// When a fillet does not fit, the kernel's suggestion names its unit. The numbers
// are the kernel's millimetres; a reader who wrote the model in cm must not be
// offered a bare "10" for a radius they typed as "1".
func TestKernel_ARadiusThatDoesNotFitIsReportedInMillimetres(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	doc := geometry.Document{Name: "cube", Units: "cm", Parts: []geometry.Part{{
		ID: "cube", Name: "Cube", Shape: "box",
		Size:     map[string]float64{"width": 10, "height": 10, "depth": 10},
		Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}},
		Features: []geometry.Feature{{ID: "round", Op: "fillet", Of: "cube", Radius: 20, Edges: "all"}}}
	got, err := k.BuildDocument(ctx, doc, geometry.Centimetre, "")
	if err != nil {
		t.Fatalf("a document with one impossible fillet refused to build at all: %v", err)
	}
	if len(got.FeatureFailures) != 1 {
		t.Fatalf("a 20 cm fillet on a 10 cm cube was applied: %v", got.FeatureFailures)
	}
	if !strings.Contains(got.FeatureFailures[0], " mm") {
		t.Errorf("the failure quotes kernel numbers without their unit: %s", got.FeatureFailures[0])
	}
}
