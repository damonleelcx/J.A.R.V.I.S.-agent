package geometry

import (
	"encoding/json"
	"reflect"
	"testing"
)

// A design's dimensions, measured from the extent kept when it was stored, are exactly
// the dimensions measuring it again finds: every design shape this package measures —
// flat, repeated, a gear, an outline, a tree turned and mirrored and patterned at every
// level, a tree that places nothing, nothing at all — through the JSON the column holds.
// And an extent from another revision of the arithmetic is not trusted.
func TestExtent_MeasuresExactlyAsMeasureDoes(t *testing.T) {
	mirrored := carWithOneCorner()
	mirrored.Assemblies[0].Children = append(mirrored.Assemblies[0].Children,
		Child{ID: "front-right", Ref: "corner", Position: []float64{-1000, 0, 500},
			Rotation: []float64{0, -90, 30}, Mirror: "x"},
		Child{ID: "rear", Ref: "corner", Position: []float64{0, 0, -900}, Rotation: []float64{20, 0, 0},
			Pattern: &Pattern{Kind: "linear", Count: 3, Offset: []float64{0, 0, -250}}})
	mirrored.Assumptions = []string{"the damper's length"}
	nothingPlaced := Document{Name: "empty", Units: "mm", Root: "top",
		Assemblies: []Assembly{{ID: "top", Children: []Child{}}}}

	docs := map[string]Document{
		"flat": flatDoc(), "one corner": carWithOneCorner(), "turned, mirrored, patterned": mirrored,
		"wheel car": wheelCar(), "car": carDoc(), "axle": axleTree(), "island": islandPlate(),
		"bracket": bracketWithHoles(), "tube": tubeDocument(), "five boxes": fiveBoxesInARow(),
		"gear": gearDocument(spurSize()), "places nothing": nothingPlaced, "no geometry": {},
	}
	for name, doc := range docs {
		want := Measure(doc, Millimetre)
		kept := ExtentOf(doc)
		// Through the column: what is read back is what was written.
		if kept != nil {
			b, err := json.Marshal(kept)
			if err != nil {
				t.Fatalf("%s: the extent cannot be stored: %v", name, err)
			}
			kept = &Extent{}
			if err := json.Unmarshal(b, kept); err != nil {
				t.Fatal(err)
			}
		}
		if got := MeasureFrom(doc, Millimetre, kept); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: measured from its kept extent %+v\nmeasured again %+v", name, got, want)
		}
		if doc.HasGeometry() && !kept.Current() {
			t.Errorf("%s: a design with geometry keeps no extent (%+v), so every read places it again", name, kept)
		}
	}
	if e := ExtentOf(nothingPlaced); e == nil || !e.Empty {
		t.Errorf("a tree that places nothing keeps %+v; want an empty extent, not a missing one", e)
	}

	// Another revision's corners are not measured from.
	doc := flatDoc()
	old := &Extent{Rev: ExtentRev - 1, Min: [3]float64{-1e6, -1e6, -1e6}, Max: [3]float64{1e6, 1e6, 1e6}}
	if got := MeasureFrom(doc, Millimetre, old); !reflect.DeepEqual(got, Measure(doc, Millimetre)) {
		t.Errorf("an extent from revision %d was measured from: %+v", old.Rev, got)
	}
}
