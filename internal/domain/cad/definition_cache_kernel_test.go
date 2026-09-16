package cad_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// Each distinct shape is built once per request, and every occurrence of it is a
// located copy of that one build. Phase 4, stage K1 of
// docs/plan-2026-09-13-millions-of-parts.md.

// The acceptance criterion, at S0's build ceiling: 4096 occurrences of one
// definition build with ONE shape build, and the volume is exact per occurrence.
func TestKernel_OneDefinitionPlacedManyTimesIsBuiltOnce(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	stud := geometry.Part{ID: "stud", Shape: "box", Size: map[string]float64{"width": 4, "height": 6, "depth": 8},
		Repeat: &geometry.Repeat{Count: 512, Offset: []float64{10, 0, 0}}}
	doc := geometry.Document{Name: "panel", Units: "mm", Root: "panel", Definitions: []geometry.Part{stud},
		Assemblies: []geometry.Assembly{{ID: "panel", Children: []geometry.Child{
			{ID: "row", Ref: "stud", Pattern: &geometry.Pattern{Kind: "linear", Count: 8, Offset: []float64{0, 0, 20}}},
		}}}}
	start := time.Now()
	got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("4096 occurrences of one definition: %d shape build(s), %.1f s", got.ShapeBuilds, time.Since(start).Seconds())
	if got.Parts != 4096 || got.ShapeBuilds != 1 {
		t.Fatalf("built %d parts from %d shape build(s); want 4096 parts from 1", got.Parts, got.ShapeBuilds)
	}
	if want := 4096.0 * 4 * 6 * 8; math.Abs(got.Volume-want) > 1e-6*want {
		t.Errorf("volume %.6f mm³, want %.0f (exact per occurrence)", got.Volume, want)
	}
}

// A mirrored occurrence is a different solid, and so is a different shape: each
// distinct shape is built once, not one shape per document.
func TestKernel_DistinctShapesAreBuiltOnceEach(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	ell := geometry.Part{ID: "ell", Shape: "extrusion", Size: map[string]float64{"depth": 5},
		Profile: []geometry.Point{{X: 0, Y: 0}, {X: 20, Y: 0}, {X: 20, Y: 5}, {X: 5, Y: 5}, {X: 5, Y: 15}, {X: 0, Y: 15}},
		Repeat:  &geometry.Repeat{Count: 3, Offset: []float64{0, 0, 30}}}
	pin := geometry.Part{ID: "pin", Shape: "cylinder", Size: map[string]float64{"radius": 2, "height": 10},
		Repeat: &geometry.Repeat{Count: 4, Offset: []float64{0, 0, 30}}}
	doc := geometry.Document{Name: "rack", Units: "mm", Root: "rack", Definitions: []geometry.Part{ell, pin},
		Assemblies: []geometry.Assembly{{ID: "rack", Children: []geometry.Child{
			{ID: "left", Ref: "ell", Position: []float64{-100, 0, 0}},
			{ID: "right", Ref: "ell", Position: []float64{100, 0, 0}, Mirror: "x"},
			{ID: "pins", Ref: "pin", Position: []float64{0, 50, 0}},
		}}}}
	got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}
	// Three ells on each side and four pins; the right-hand ells are reflected.
	if got.Parts != 10 || got.ShapeBuilds != 3 {
		t.Fatalf("built %d parts from %d shape build(s); want 10 parts from 3 (ell, mirrored ell, pin)", got.Parts, got.ShapeBuilds)
	}
	ellArea := 20.0*5 + 5*10
	want := 6*ellArea*5 + 4*math.Pi*2*2*10
	if math.Abs(got.Volume-want) > 1e-6*want {
		t.Errorf("volume %.6f mm³, want %.6f", got.Volume, want)
	}
}

// A cut on ONE occurrence changes that occurrence only. If the shared build were
// modified in place, every copy would lose the hole's volume.
func TestKernel_AFeatureOnOneOccurrenceLeavesItsCopiesWhole(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	plate := geometry.Part{ID: "plate", Shape: "box", Size: map[string]float64{"width": 60, "height": 10, "depth": 60},
		Repeat: &geometry.Repeat{Count: 3, Offset: []float64{100, 0, 0}}}
	bore := geometry.Part{ID: "bore", Shape: "cylinder", Size: map[string]float64{"radius": 5, "height": 40},
		Position: []float64{100, 0, 0}}
	doc := geometry.Document{Name: "plates", Units: "mm", Parts: []geometry.Part{plate, bore},
		Features: []geometry.Feature{{ID: "drill", Op: "cut", Of: "plate-2", With: []string{"bore"}}}}
	got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.FeatureFailures) != 0 || got.Parts != 3 || got.ShapeBuilds != 2 {
		t.Fatalf("%d parts from %d shape build(s), failures %v; want 3 plates from 2 builds (plate, bore)",
			got.Parts, got.ShapeBuilds, got.FeatureFailures)
	}
	want := 3*60.0*10*60 - math.Pi*5*5*10
	if math.Abs(got.Volume-want) > 1e-3 {
		t.Errorf("volume %.3f mm³, want %.3f: the hole was cut from more than the one plate it names", got.Volume, want)
	}
}

// A repeated scripted part runs its script once, however many copies it places.
func TestKernel_ARepeatedScriptedPartRunsItsScriptOnce(t *testing.T) {
	k := scriptedKernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	block := geometry.Part{ID: "block", Name: "Scripted Block", Shape: "script",
		Script: "result = Box(4, 4, 4)\n",
		Repeat: &geometry.Repeat{Count: 5, Offset: []float64{20, 0, 0}}}
	doc := geometry.Document{Name: "blocks", Units: "mm", Parts: []geometry.Part{block}}
	got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Parts != 5 || got.ScriptRuns != 1 || got.ShapeBuilds != 1 {
		t.Fatalf("%d parts, %d script run(s), %d shape build(s); want 5 parts from one run and one build",
			got.Parts, got.ScriptRuns, got.ShapeBuilds)
	}
	if math.Abs(got.Volume-5*64) > 1e-3 {
		t.Errorf("volume %.3f mm³, want %d", got.Volume, 5*64)
	}
}
