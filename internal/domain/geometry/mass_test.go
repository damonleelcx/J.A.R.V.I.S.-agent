package geometry

import (
	"math"
	"strings"
	"testing"
)

// Mass, centre of gravity and envelope rolled up through the tree. Phase 5, stage
// V3. The kernel's measurements are given here; cad/mass_properties_kernel_test.go
// holds the kernel to them.

func near(a, b float64) bool { return math.Abs(a-b) <= 1e-9*math.Max(1, math.Abs(b)) }

func measured(id string, volume float64, centre [3]float64) SolidMeasure {
	return SolidMeasure{ID: id, Volume: volume, Centroid: centre, Measured: true,
		Bounds: [6]float64{centre[0] - 5, centre[1] - 5, centre[2] - 5, centre[0] + 5, centre[1] + 5, centre[2] + 5}}
}

func group(t *testing.T, r MassReport, path string) MassGroup {
	t.Helper()
	for _, g := range r.Groups {
		if g.Path == path {
			return g
		}
	}
	t.Fatalf("no group %q in %+v", path, r.Groups)
	return MassGroup{}
}

// With a density on every part, the centre is the centre of GRAVITY: the steel
// part pulls it toward itself.
func TestMassProperties_WeighsEachPartByItsDensity(t *testing.T) {
	doc := Document{Name: "pair", Units: "mm", Parts: []Part{
		{ID: "steel", Shape: "box", Material: &Material{Name: "steel", Density: 7850}},
		{ID: "alu", Shape: "box", Material: &Material{Name: "aluminium", Density: 2700}},
	}}
	r := MassProperties(doc, []SolidMeasure{
		measured("steel", 1000, [3]float64{0, 0, 0}),
		measured("alu", 1000, [3]float64{100, 0, 0}),
	})
	if r.Basis != MassByDensity || len(r.WithoutDensity) != 0 {
		t.Fatalf("basis %q, without density %v; every part had one", r.Basis, r.WithoutDensity)
	}
	whole := group(t, r, "")
	if want := 1000e-9*7850 + 1000e-9*2700; !near(whole.Mass, want) {
		t.Errorf("mass %g kg, want %g", whole.Mass, want)
	}
	if want := 100 * 2700.0 / (7850 + 2700); !near(whole.Centre[0], want) {
		t.Errorf("centre of gravity at x=%g, want %g (the steel part is heavier)", whole.Centre[0], want)
	}
	if whole.Bounds != [6]float64{-5, -5, -5, 105, 5, 5} {
		t.Errorf("envelope %v, want the union of both boxes", whole.Bounds)
	}
}

// ‼️ One part without a density and no mass is claimed: the centre is the centre
// of VOLUME, the report says so, and the part is named.
func TestMassProperties_WithoutEveryDensityIsWeighedByVolumeAndSaysSo(t *testing.T) {
	doc := Document{Name: "pair", Units: "mm", Parts: []Part{
		{ID: "steel", Shape: "box", Material: &Material{Name: "steel", Density: 7850}},
		{ID: "mystery", Shape: "box"},
	}}
	r := MassProperties(doc, []SolidMeasure{
		measured("steel", 1000, [3]float64{0, 0, 0}),
		measured("mystery", 1000, [3]float64{100, 0, 0}),
	})
	if r.Basis != MassByVolume {
		t.Errorf("basis %q with a part that has no density; want %q", r.Basis, MassByVolume)
	}
	if len(r.WithoutDensity) != 1 || r.WithoutDensity[0] != "mystery" {
		t.Errorf("without density %v, want [mystery]", r.WithoutDensity)
	}
	whole := group(t, r, "")
	if whole.Mass != 0 {
		t.Errorf("claimed a mass of %g kg without a density for every part", whole.Mass)
	}
	if !near(whole.Centre[0], 50) {
		t.Errorf("centre at x=%g; weighed by volume it is 50", whole.Centre[0])
	}
}

// Everything placed under an assembly occurrence adds up in that occurrence's
// group, and a part the kernel could not measure is named, never weighed.
func TestMassProperties_RollsUpThroughTheTree(t *testing.T) {
	steel := &Material{Name: "steel", Density: 8000}
	hub := Part{ID: "hub", Shape: "box", Material: steel}
	rim := Part{ID: "rim", Shape: "box", Material: steel}
	doc := Document{Name: "axle", Units: "mm", Root: "axle",
		Definitions: []Part{hub, rim},
		Assemblies: []Assembly{
			{ID: "wheel", Children: []Child{{ID: "hub", Ref: "hub"}, {ID: "rim", Ref: "rim"}}},
			{ID: "axle", Children: []Child{
				{ID: "left", Ref: "wheel", Position: []float64{-100, 0, 0}},
				{ID: "right", Ref: "wheel", Position: []float64{100, 0, 0}},
			}},
		}}
	r := MassProperties(doc, []SolidMeasure{
		measured("left/hub", 100, [3]float64{-100, 0, 0}),
		measured("left/rim", 300, [3]float64{-100, 0, 0}),
		measured("right/hub", 100, [3]float64{100, 0, 0}),
		measured("right/rim", 300, [3]float64{100, 0, 0}),
		{ID: "right/cover", Volume: 50},
	})
	if r.Basis != MassByDensity {
		t.Fatalf("basis %q; every measured part is steel (without density: %v)", r.Basis, r.WithoutDensity)
	}
	for _, path := range []string{"left", "right"} {
		g := group(t, r, path)
		if g.Parts != 2 || !near(g.Volume, 400) || !near(g.Mass, 400e-9*8000) {
			t.Errorf("%s: %d part(s), %g mm³, %g kg; want 2, 400, %g", path, g.Parts, g.Volume, g.Mass, 400e-9*8000)
		}
	}
	whole := group(t, r, "")
	if whole.Parts != 4 || !near(whole.Centre[0], 0) {
		t.Errorf("whole: %d part(s), centre x=%g; want 4 and 0", whole.Parts, whole.Centre[0])
	}
	if len(r.Unmeasured) != 1 || r.Unmeasured[0] != "right/cover" {
		t.Errorf("unmeasured %v, want [right/cover]", r.Unmeasured)
	}
}

// A density no material has is refused, not weighed.
func TestMaterial_RefusesADensityNoMaterialHas(t *testing.T) {
	for _, d := range []float64{-1, math.NaN(), math.Inf(1), 250000} {
		m := &Material{Name: "unobtainium", Density: d}
		if err := m.Validate(); err == nil || !strings.Contains(err.Error(), "kilograms per cubic metre") {
			t.Errorf("density %g was not refused with its unit named: %v", d, err)
		}
	}
	if err := (&Material{Name: "steel", Density: 7850}).Validate(); err != nil {
		t.Errorf("steel's density was refused: %v", err)
	}
	if err := (&Material{Name: "steel"}).Validate(); err != nil {
		t.Errorf("a material with no density was refused; density is optional: %v", err)
	}
}
