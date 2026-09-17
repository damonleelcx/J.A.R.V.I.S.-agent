package geometry

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// A synthetic yard of cylinders and extrusions placed by patterns, for
// docs/spikes/2026-09-17-one-million-after (item: #94 measured clash reuse on a box
// lattice only).
//
// A "drive" is cad's shaftAndGirder fixture (interference_prism_kernel_test.go): a
// 4 m cylinder shaft along x with 191 collars round it by a linear pattern and one
// collar half over each end, and a 4 m extruded L girder along z with 96 cleats by a
// linear pattern, a cleat half over each end and 95 pins leaning 35° through the
// flange, also by a linear pattern — 388 occurrences and 386 clashes. A "ring" is the
// drive stood 8 m off the y axis and turned round it by a polar pattern of 8, so the
// same drive sits at eight rotations. The yard is rings × rings of those rings by a
// grid pattern, 24 m apart. Nothing is listed: every repeated part is a pattern.
//
// Drives do not touch one another: a drive lies within 2,830 mm of its own origin in
// the xz plane, and neighbours in a ring are 6,123 mm apart; a ring lies within
// 10,830 mm of its centre, and rings are 24 m apart. (The first run spaced rings
// 20 m apart, and neighbouring rings clashed three times each: 36 extra clashes at
// 3 × 3, 180 at 6 × 6, 540 at 10 × 10 — recorded in the spike, not hidden.)
const (
	yardDrivesPerRing   = 8
	yardDriveOccurrence = 388
	yardDriveClashes    = 386
)

func prismYard(side int) Document {
	ell := [][2]float64{{0, 0}, {40, 0}, {40, 6}, {6, 6}, {6, 40}, {0, 40}}
	profile := make([]Point, len(ell))
	for i, p := range ell {
		profile[i] = Point{X: p[0], Y: p[1]}
	}
	defs := []Part{
		{ID: "shaft", Name: "Shaft", Shape: "cylinder", Size: map[string]float64{"radius": 10, "height": 4000}},
		{ID: "collar", Name: "Collar", Shape: "box", Size: map[string]float64{"width": 10, "height": 30, "depth": 30}},
		{ID: "girder", Name: "Girder", Shape: "extrusion", Profile: profile, Size: map[string]float64{"depth": 4000}},
		{ID: "cleat", Name: "Cleat", Shape: "box", Size: map[string]float64{"width": 20, "height": 20, "depth": 8}},
		{ID: "pin", Name: "Pin", Shape: "cylinder", Size: map[string]float64{"radius": 1.5, "height": 30}},
	}
	drive := Assembly{ID: "drive", Name: "Drive", Children: []Child{
		{ID: "shaft", Ref: "shaft", Rotation: []float64{0, 0, 90}},
		{ID: "collars", Ref: "collar", Position: []float64{-1900, 0, 0},
			Pattern: &Pattern{Kind: "linear", Count: 191, Offset: []float64{20, 0, 0}}},
		{ID: "left-collar", Ref: "collar", Position: []float64{-2000, 0, 0}},
		{ID: "right-collar", Ref: "collar", Position: []float64{2000, 0, 0}},
		{ID: "girder", Ref: "girder", Position: []float64{0, 1000, 0}},
		{ID: "cleats", Ref: "cleat", Position: []float64{20, 1003, -1900},
			Pattern: &Pattern{Kind: "linear", Count: 96, Offset: []float64{0, 0, 40}}},
		{ID: "bottom-cleat", Ref: "cleat", Position: []float64{20, 1003, -2000}},
		{ID: "top-cleat", Ref: "cleat", Position: []float64{20, 1003, 2000}},
		{ID: "leaning", Ref: "pin", Position: []float64{20, 1003, -1880}, Rotation: []float64{35, 0, 0},
			Pattern: &Pattern{Kind: "linear", Count: 95, Offset: []float64{0, 0, 40}}},
	}}
	ring := Assembly{ID: "ring", Name: "Ring of drives", Children: []Child{
		{ID: "drive", Ref: "drive", Position: []float64{8000, 0, 0},
			Pattern: &Pattern{Kind: "polar", Count: yardDrivesPerRing, About: "y"}},
	}}
	yardChild := Child{ID: "ring", Ref: "ring"}
	if side > 1 {
		yardChild.Pattern = &Pattern{Kind: "grid", Rows: side, Columns: side,
			RowOffset: []float64{0, 0, 24000}, ColumnOffset: []float64{24000, 0, 0}}
	}
	yard := Assembly{ID: "yard", Name: "Yard", Children: []Child{yardChild}}
	return Document{Name: "Prism yard", Units: "mm", Root: "yard",
		NotVerified: []string{"a synthetic scale fixture, not a design"},
		Definitions: defs, Assemblies: []Assembly{drive, ring, yard}}
}

func prismYardOccurrences(side int) int { return side * side * yardDrivesPerRing * yardDriveOccurrence }

// The yard places every repeated part by a pattern, and its kernel request is the
// real expansion's (solidsAndOperations, the path every build takes below the
// ceiling), not a hand-written one.
func TestPrismYard_PlacesItsPrismsByPatternsNotByListing(t *testing.T) {
	d := prismYard(2)
	if n := len(d.EnumeratedRepetition()); n != 0 {
		t.Errorf("EnumeratedRepetition reports %d group(s); every repeated part should be a pattern", n)
	}
	SetLimits(Limits{MaxOccurrences: 1 << 20})
	defer SetLimits(Limits{})
	solids, ops, _, notes := solidsAndOperations(d, Millimetre)
	if len(solids) != prismYardOccurrences(2) || len(ops) != 0 || len(notes) != 0 {
		t.Fatalf("%d solids (want %d), %d operations, notes %q", len(solids), prismYardOccurrences(2), len(ops), notes)
	}
	shapes := map[string]int{}
	for _, s := range solids {
		shapes[s.Shape]++
	}
	if shapes["extrusion"] != 32 || shapes["cylinder"] != 32*96 || shapes["box"] != 32*291 {
		t.Errorf("shapes %v; want 32 extrusions, %d cylinders, %d boxes", shapes, 32*96, 32*291)
	}
}

// TestScaleUp_MeasurePrismYard writes yard-<side>.json kernel requests for
// docs/spikes/2026-09-17-one-million-after. Skips unless FORGE_SCALE_MEASURE_OUT is set;
// FORGE_SCALE_YARD_SIDES lists the grid sides (default 1,3,6).
func TestScaleUp_MeasurePrismYard(t *testing.T) {
	out := os.Getenv("FORGE_SCALE_MEASURE_OUT")
	if out == "" {
		t.Skip("set FORGE_SCALE_MEASURE_OUT to a directory to write the prism yard's requests")
	}
	sides := []int{1, 3, 6}
	if s := os.Getenv("FORGE_SCALE_YARD_SIDES"); s != "" {
		sides = nil
		for _, f := range strings.Split(s, ",") {
			n, err := strconv.Atoi(strings.TrimSpace(f))
			if err != nil {
				t.Fatal(err)
			}
			sides = append(sides, n)
		}
	}
	SetLimits(Limits{MaxOccurrences: 1 << 22})
	defer SetLimits(Limits{})
	for _, side := range sides {
		solids, ops, _, notes := solidsAndOperations(prismYard(side), Millimetre)
		if len(ops) != 0 || len(notes) != 0 || len(solids) != prismYardOccurrences(side) {
			t.Fatalf("side %d: %d solids, %d operations, notes %q", side, len(solids), len(ops), notes)
		}
		n, err := writeRequest(filepath.Join(out, fmt.Sprintf("yard-%d.json", side)), solids)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Printf("YARD side %d: %d occurrences, %d expected clashes, %d bytes\n", side, len(solids),
			side*side*yardDrivesPerRing*yardDriveClashes, n)
	}
}
