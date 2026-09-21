package geometry_test

import (
	"fmt"
	"math"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// Looks designed, stages B2 and B4 (damon's decision of 2026-09-18): more edge
// rules, and shells and thickened skins, validated here before the kernel sees
// them.

func withSurface() *geometry.Document {
	d := plated()
	d.Parts = append(d.Parts, geometry.Part{ID: "skin", Name: "Skin", Shape: "plane",
		Size: map[string]float64{"width": 50, "depth": 30}})
	return d
}

func TestOperations_TheNewEdgeRulesAreAccepted(t *testing.T) {
	for _, rule := range []string{"convex", "concave", "outer", "holes"} {
		d := plated()
		d.Features = []geometry.Feature{{ID: "round", Op: "fillet", Of: "plate", Radius: 1, Edges: rule}}
		ops, problems := d.Operations()
		if len(problems) != 0 || len(ops) != 1 || ops[0].Edges != rule {
			t.Errorf("edges=%q: ops=%+v problems=%+v", rule, ops, problems)
		}
	}
}

// "longer" measures against edge_length, which it needs and which nothing else takes.
func TestOperations_LongerNeedsAnEdgeLengthAndNothingElseTakesOne(t *testing.T) {
	d := plated()
	d.Features = []geometry.Feature{{ID: "round", Op: "fillet", Of: "plate", Radius: 1, Edges: "longer", EdgeLength: 20}}
	ops, problems := d.Operations()
	if len(problems) != 0 || len(ops) != 1 || ops[0].EdgeLength != 20 {
		t.Fatalf("ops=%+v problems=%+v", ops, problems)
	}
	for _, tc := range []struct {
		f     geometry.Feature
		wants string
	}{
		{geometry.Feature{ID: "round", Op: "fillet", Of: "plate", Radius: 1, Edges: "longer"}, "needs \"edge_length\""},
		{geometry.Feature{ID: "round", Op: "fillet", Of: "plate", Radius: 1, Edges: "longer", EdgeLength: -3}, "needs \"edge_length\""},
		{geometry.Feature{ID: "round", Op: "fillet", Of: "plate", Radius: 1, Edges: "top", EdgeLength: 20}, "does not measure one"},
	} {
		d := plated()
		d.Features = []geometry.Feature{tc.f}
		ops, problems := d.Operations()
		if len(ops) != 0 || !opProblem(problems, "round", tc.wants) {
			t.Errorf("%+v: ops=%+v problems=%+v", tc.f, ops, problems)
		}
	}
}

// "joins" rounds the seam a fuse left; on a part nothing was fused into it would
// select nothing, so it is refused by name instead.
func TestOperations_JoinsNeedsAnEarlierFuse(t *testing.T) {
	d := plated()
	d.Features = []geometry.Feature{{ID: "round", Op: "fillet", Of: "plate", Radius: 1, Edges: "joins"}}
	if ops, problems := d.Operations(); len(ops) != 0 || !opProblem(problems, "round", "no earlier \"fuse\"") {
		t.Fatalf("joins with nothing fused was accepted: ops=%+v problems=%+v", ops, problems)
	}
	d.Features = []geometry.Feature{
		{ID: "weld", Op: "fuse", Of: "plate", With: []string{"rib"}},
		{ID: "round", Op: "fillet", Of: "plate", Radius: 1, Edges: "joins"}}
	if ops, problems := d.Operations(); len(problems) != 0 || len(ops) != 2 {
		t.Fatalf("joins after a fuse was refused: ops=%+v problems=%+v", ops, problems)
	}
}

func TestOperations_AShellIsValidated(t *testing.T) {
	d := plated()
	d.Parameters = append(d.Parameters, geometry.Parameter{Name: "wall", Value: 1.5, Unit: "mm", How: geometry.Chosen})
	d.Features = []geometry.Feature{{ID: "hollow", Op: "shell", Of: "plate", ThicknessFrom: "wall * 2",
		Open: []string{"Top", "bottom", "top"}}}
	ops, problems := d.Operations()
	if len(problems) != 0 || len(ops) != 1 {
		t.Fatalf("ops=%+v problems=%+v", ops, problems)
	}
	if ops[0].Thickness != 3 || len(ops[0].Open) != 2 || ops[0].Open[0] != "top" || ops[0].Open[1] != "bottom" {
		t.Errorf("shell resolved to %+v; want thickness 3 from the expression, open [top bottom]", ops[0])
	}
	for _, tc := range []struct {
		f     geometry.Feature
		wants string
	}{
		{geometry.Feature{ID: "x", Op: "shell", Of: "plate", Open: []string{"top"}}, "needs a thickness"},
		{geometry.Feature{ID: "x", Op: "shell", Of: "plate", Thickness: 2}, "names no face"},
		{geometry.Feature{ID: "x", Op: "shell", Of: "plate", Thickness: 2, Open: []string{"lid"}}, "not a face FORGE can name"},
		{geometry.Feature{ID: "x", Op: "shell", Of: "skin", Thickness: 2, Open: []string{"top"}}, "no inside to hollow"},
		{geometry.Feature{ID: "x", Op: "shell", Of: "plate", Thickness: 2, Open: []string{"top"}, With: []string{"hole"}}, "does not take tools"},
		{geometry.Feature{ID: "x", Op: "thicken", Of: "plate", Thickness: 2}, "only a surface"},
		{geometry.Feature{ID: "x", Op: "thicken", Of: "skin", ThicknessFrom: "nope"}, "does not evaluate"},
	} {
		d := withSurface()
		d.Features = []geometry.Feature{tc.f}
		ops, problems := d.Operations()
		if len(ops) != 0 || !opProblem(problems, "x", tc.wants) {
			t.Errorf("%+v: want a refusal saying %q; ops=%+v problems=%+v", tc.f, tc.wants, ops, problems)
		}
	}
	d = withSurface()
	d.Features = []geometry.Feature{{ID: "x", Op: "thicken", Of: "skin", Thickness: 2}}
	if ops, problems := d.Operations(); len(problems) != 0 || len(ops) != 1 || ops[0].Thickness != 2 {
		t.Errorf("thickening a plane: ops=%+v problems=%+v", ops, problems)
	}
}

// Every length a feature carries reaches the kernel in millimetres, as a radius does.
func TestSolids_ConvertsEveryFeatureLengthToMillimetres(t *testing.T) {
	d := geometry.Document{Name: "cube", Units: "cm", Parts: []geometry.Part{{
		ID: "cube", Name: "Cube", Shape: "box",
		Size: map[string]float64{"width": 10, "height": 10, "depth": 10}}},
		Features: []geometry.Feature{
			{ID: "hollow", Op: "shell", Of: "cube", Thickness: 0.5, Open: []string{"top"}},
			{ID: "round", Op: "fillet", Of: "cube", Radius: 0.1, Edges: "longer", EdgeLength: 2}}}
	_, ops, problems, _ := geometry.SolidsAndOperations(d, geometry.Centimetre)
	if len(problems) > 0 || len(ops) != 2 {
		t.Fatalf("ops=%v problems=%v", ops, problems)
	}
	if math.Abs(ops[0].Thickness-5) > 1e-9 {
		t.Errorf("a 0.5 cm wall reaches the kernel as %g mm, want 5", ops[0].Thickness)
	}
	if math.Abs(ops[1].EdgeLength-20) > 1e-9 {
		t.Errorf("a 2 cm edge_length reaches the kernel as %g mm, want 20", ops[1].EdgeLength)
	}
}

// Stage B6: a perforation has a budget, refused by name past it and counted on the
// tools the cut actually consumes.
func TestOperations_APerforationPastTheBudgetIsRefused(t *testing.T) {
	for _, tc := range []struct {
		holes int
		ok    bool
	}{{geometry.MaxCutTools, true}, {geometry.MaxCutTools + 1, false}} {
		d := geometry.Document{Name: "panel", Units: "mm", Parts: []geometry.Part{{ID: "panel", Shape: "box",
			Size: map[string]float64{"width": 1000, "height": 5, "depth": 600}}}}
		var with []string
		for i := 0; i < tc.holes; i++ {
			id := fmt.Sprintf("hole-%d", i)
			with = append(with, id)
			d.Parts = append(d.Parts, geometry.Part{ID: id, Shape: "cylinder",
				Size: map[string]float64{"radius": 1, "height": 20}})
		}
		d.Features = []geometry.Feature{{ID: "perforate", Op: "cut", Of: "panel", With: with}}
		ops, problems := d.Operations()
		if tc.ok && (len(problems) != 0 || len(ops) != 1) {
			t.Errorf("%d holes, the budget exactly, was refused: %v", tc.holes, problems)
		}
		if !tc.ok && (len(ops) != 0 || !opProblem(problems, "perforate", "FORGE cuts at most 2000")) {
			t.Errorf("%d holes, one past the budget, was accepted: ops=%d problems=%v", tc.holes, len(ops), problems)
		}
	}
}
