package cad_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// A mirrored part is built as the reflection the Go mesh and the browser draw.
// Phase 1, stage D1c of docs/plan-2026-09-13-millions-of-parts.md.

func lArm() geometry.Part {
	return geometry.Part{ID: "arm", Name: "Arm", Shape: "extrusion",
		Size: map[string]float64{"depth": 10},
		Profile: []geometry.Point{{X: 0, Y: 0}, {X: 40, Y: 0}, {X: 40, Y: 10}, {X: 10, Y: 10},
			{X: 10, Y: 30}, {X: 0, Y: 30}},
		Position: []float64{5, 0, 0}, Rotation: []float64{0, 0, 20}}
}

func meshBounds(t *testing.T, doc geometry.Document) [6]float64 {
	t.Helper()
	b := [6]float64{math.Inf(1), math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1), math.Inf(-1)}
	for _, g := range geometry.Tessellate(doc, geometry.Millimetre).Groups {
		for _, tri := range g.Triangles {
			for _, v := range [3][3]float64{tri.A, tri.B, tri.C} {
				for k := 0; k < 3; k++ {
					b[k] = math.Min(b[k], v[k])
					b[3+k] = math.Max(b[3+k], v[k])
				}
			}
		}
	}
	return b
}

func sameBounds(a, b [6]float64) bool {
	for i := range a {
		if math.Abs(a[i]-b[i]) > 1e-6 {
			return false
		}
	}
	return true
}

func TestKernel_MirrorsAPartBeforePlacingIt(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var bounds [2][6]float64
	for i, mirrored := range []bool{false, true} {
		arm := lArm()
		arm.Mirrored = mirrored
		doc := geometry.Document{Name: "arm", Units: "mm", Parts: []geometry.Part{arm}}
		got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
		if err != nil {
			t.Fatal(err)
		}
		if math.Abs(got.Volume-6000) > 1e-3 {
			t.Errorf("mirrored=%v: volume %.3f mm³, want 6000 — a reflection keeps the volume", mirrored, got.Volume)
		}
		if want := meshBounds(t, doc); !sameBounds(got.Bounds, want) {
			t.Errorf("mirrored=%v: the kernel built bounds %v, the mesh draws %v", mirrored, got.Bounds, want)
		}
		bounds[i] = got.Bounds
	}
	if sameBounds(bounds[0], bounds[1]) {
		t.Error("the mirrored arm has the same bounds as the plain one, so the kernel did not reflect it")
	}
}

func TestKernel_BuildsATreeChildMirroredAcrossY(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	doc := geometry.Document{Name: "pair", Units: "mm", Definitions: []geometry.Part{lArm()},
		Assemblies: []geometry.Assembly{{ID: "pair", Children: []geometry.Child{
			{ID: "left", Ref: "arm", Position: []float64{-100, 0, 0}, Rotation: []float64{0, 30, 0}},
			{ID: "right", Ref: "arm", Position: []float64{100, 0, 0}, Rotation: []float64{0, 30, 0}, Mirror: "y"},
		}}}, Root: "pair"}
	got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Parts != 2 || math.Abs(got.Volume-12000) > 1e-3 {
		t.Fatalf("parts %d volume %.3f, want 2 and 12000", got.Parts, got.Volume)
	}
	if want := meshBounds(t, doc); !sameBounds(got.Bounds, want) {
		t.Errorf("the kernel built bounds %v, the mesh draws %v", got.Bounds, want)
	}
}
