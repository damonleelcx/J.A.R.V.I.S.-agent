package geometry

import "testing"

// wheelCar is a car root placing two wheels, each a rim, a ring of five nuts and a
// definition with its own repeat.
func wheelCar() Document {
	return Document{Name: "car", Units: "mm", Root: "car",
		Definitions: []Part{
			{ID: "rim", Shape: "cylinder", Size: map[string]float64{"radius": 200, "height": 180}},
			{ID: "nut", Shape: "cylinder", Size: map[string]float64{"radius": 10, "height": 20}},
			{ID: "spoke", Shape: "box", Size: map[string]float64{"width": 5, "height": 5, "depth": 150},
				Repeat: &Repeat{Count: 3, About: "y"}},
		},
		Assemblies: []Assembly{
			{ID: "wheel", Children: []Child{
				{ID: "rim", Ref: "rim"},
				{ID: "nut", Ref: "nut", Position: []float64{57, 100, 0}, Pattern: &Pattern{Kind: "polar", Count: 5, About: "y"}},
				{ID: "spoke", Ref: "spoke"},
			}},
			{ID: "car", Children: []Child{
				{ID: "left-wheel", Ref: "wheel", Position: []float64{-800, 0, 0}},
				{ID: "right-wheel", Ref: "wheel", Position: []float64{800, 0, 0}, Mirror: "x"},
			}},
		}}
}

// A flattened part is traced back to the child that places it, and the copy it is.
func TestPlacedBy_NamesTheChildAndThePatternCopyThatPlaceAPart(t *testing.T) {
	d := wheelCar()
	at, ok := d.PlacedBy("left-wheel/nut-3")
	if !ok || at.Assembly != "wheel" || at.Child != "nut" || at.Copy != 3 || at.Ref != "nut" ||
		at.Pattern == nil || at.Pattern.About != "y" {
		t.Errorf("left-wheel/nut-3 = %+v, %v; want copy 3 of child nut in wheel, polar about y", at, ok)
	}
	if at, ok := d.PlacedBy("right-wheel/spoke-2"); !ok || at.Child != "spoke" || at.Copy != 0 {
		t.Errorf("a definition's repeat copy = %+v, %v; want child spoke, not a pattern copy", at, ok)
	}
	if at, ok := d.PlacedBy("left-wheel/rim"); !ok || at.Child != "rim" || at.Copy != 0 {
		t.Errorf("left-wheel/rim = %+v, %v", at, ok)
	}
	for _, id := range []string{"rim", "left-wheel/nut-9x", "front-wheel/rim", "left-wheel/rim/deeper"} {
		if at, ok := d.PlacedBy(id); ok {
			t.Errorf("%q is not placed by the tree, but PlacedBy said %+v", id, at)
		}
	}
}

// Every part the tree places can be traced back, whatever the expansion named it.
func TestPlacedBy_EveryPlacedPartIsTracedToAChild(t *testing.T) {
	d := wheelCar()
	placed := d.Expanded().Parts
	if len(placed) != 2*(1+5+3) {
		t.Fatalf("the car places %d parts; want 18", len(placed))
	}
	for _, p := range placed {
		if _, ok := d.PlacedBy(p.ID); !ok {
			t.Errorf("%q was placed by the tree and cannot be traced to a child", p.ID)
		}
	}
}
