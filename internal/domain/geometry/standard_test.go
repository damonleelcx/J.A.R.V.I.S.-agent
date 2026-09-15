package geometry

import (
	"math"
	"reflect"
	"strings"
	"testing"
)

// Standard parts, named by designation. Phase 2, stage A3.

func expandedStandard(t *testing.T, designation, units string, size map[string]float64) Part {
	t.Helper()
	q, ok := StandardPartForTest(Part{ID: "p", Shape: "standard", Standard: designation, Size: size,
		Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}, units)
	if !ok {
		t.Fatalf("%q in %q was refused", designation, units)
	}
	return q
}

func loopBounds(pts []Point) (minX, maxX, minY, maxY float64) {
	minX, minY = math.Inf(1), math.Inf(1)
	maxX, maxY = math.Inf(-1), math.Inf(-1)
	for _, p := range pts {
		minX, maxX = math.Min(minX, p.X), math.Max(maxX, p.X)
		minY, maxY = math.Min(minY, p.Y), math.Max(maxY, p.Y)
	}
	return
}

func closeTo(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// The figures are the published ones. A few checked against the standards by hand,
// so a row typed wrong — or a drawing that reads the wrong column — fails here
// rather than in somebody's counterbore.
func TestStandard_TheCatalogueCarriesThePublishedFigures(t *testing.T) {
	// ISO 4762 M8: head diameter 13, head height 8, thread 8; l = 30 below the head.
	screw := expandedStandard(t, "ISO 4762 M8x30", "mm", nil)
	minX, maxX, minY, maxY := loopBounds(screw.Profile)
	if screw.Shape != "revolve" || screw.Axis != "y" || minX != 0 || !closeTo(maxX, 6.5) || !closeTo(maxY, 8) || !closeTo(minY, -30) {
		t.Errorf("M8x30 is a %s about %q, radius %v, from y %v to %v; want a revolve about y of radius 6.5 "+
			"from -30 to 8", screw.Shape, screw.Axis, maxX, minY, maxY)
	}
	if !closeTo(screw.Profile[1].X, 4) {
		t.Errorf("the M8 shank has radius %v; want 4", screw.Profile[1].X)
	}

	// ISO 15 608: 8 bore, 22 outside, 7 wide.
	bearing := expandedStandard(t, "ISO 15 608", "mm", nil)
	minX, maxX, minY, maxY = loopBounds(bearing.Profile)
	if !closeTo(minX*2, 8) || !closeTo(maxX*2, 22) || !closeTo(maxY-minY, 7) || !closeTo(maxY, 3.5) {
		t.Errorf("608 is %v × %v × %v (centred at %v); want 8 × 22 × 7 centred", minX*2, maxX*2, maxY-minY, (maxY+minY)/2)
	}

	// ISO 4032 M8: 13 across flats, 6.8 high, on an 8 mm thread.
	nut := expandedStandard(t, "ISO 4032 M8", "mm", nil)
	_, maxX, _, maxY = loopBounds(nut.Profile)
	if nut.Shape != "extrusion" || !closeTo(nut.Size["depth"], 6.8) || !closeTo(maxY*2, 13) || !closeTo(maxX*2, 26/math.Sqrt(3)) {
		t.Errorf("M8 nut: %s, depth %v, across flats %v, across corners %v", nut.Shape, nut.Size["depth"], maxY*2, maxX*2)
	}
	if len(nut.Holes) != 1 || !closeTo(nut.Holes[0][0].X, 4) {
		t.Errorf("the M8 nut's hole is %+v; want one of radius 4", nut.Holes)
	}

	// ISO 7089 M8: 8.4 hole, 16 outside, 1.6 thick.
	washer := expandedStandard(t, "ISO 7089 M8", "mm", nil)
	minX, maxX, minY, maxY = loopBounds(washer.Profile)
	if !closeTo(minX*2, 8.4) || !closeTo(maxX*2, 16) || !closeTo(maxY-minY, 1.6) {
		t.Errorf("M8 washer is %v / %v × %v; want 8.4 / 16 × 1.6", minX*2, maxX*2, maxY-minY)
	}

	// EN 10219-2 SHS 40×40×3: 40 outside, 34 inside, corners 6 and 3, cut to 600.
	tube := expandedStandard(t, "EN 10219 SHS 40x40x3", "mm", map[string]float64{"length": 600})
	_, maxX, _, _ = loopBounds(tube.Profile)
	_, holeX, _, _ := loopBounds(tube.Holes[0])
	if !closeTo(maxX*2, 40) || !closeTo(holeX*2, 34) || tube.Profile[0].Radius != 6 || tube.Holes[0][0].Radius != 3 || tube.Size["depth"] != 600 {
		t.Errorf("SHS 40x40x3: %v outside, %v inside, corners %v/%v, depth %v", maxX*2, holeX*2,
			tube.Profile[0].Radius, tube.Holes[0][0].Radius, tube.Size["depth"])
	}

	// EN 10056-1 L40×40×4: legs 40, thickness 4, root radius 6, toe radius 3.
	angle := expandedStandard(t, "EN 10056 L40x40x4", "mm", map[string]float64{"length": 300})
	if p := angle.Profile; !closeTo(p[1].X, 40) || !closeTo(p[2].Y, 4) || p[3].Radius != 6 || p[2].Radius != 3 {
		t.Errorf("L40x40x4 is drawn %+v", p)
	}

	// The lengths ISO 4762 tabulates for M8, and no other.
	var m8 []string
	for _, d := range StandardDesignations() {
		if strings.HasPrefix(d, "ISO 4762 M8x") {
			m8 = append(m8, strings.TrimPrefix(d, "ISO 4762 M8x"))
		}
	}
	if got := strings.Join(m8, ","); got != "12,16,20,25,30,35,40,45,50,55,60,65,70,80" {
		t.Errorf("M8 cap screws come in %s", got)
	}
}

// Every designation in the catalogue is a part FORGE can build: no outline a
// corner radius does not fit, no loop that crosses its axis.
func TestStandard_EveryDesignationBuilds(t *testing.T) {
	for _, designation := range StandardDesignations() {
		d := Document{Name: designation, Units: "mm", Parts: []Part{{ID: "p", Shape: "standard", Standard: designation,
			Size: map[string]float64{"length": 500}, Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}}}
		if faults := d.Faults(); len(faults) != 0 {
			t.Errorf("%s has faults: %+v", designation, faults)
		}
		solids, _, problems, notes := SolidsAndOperations(d, Millimetre)
		if len(solids) != 1 || len(problems) != 0 {
			t.Errorf("%s: %d solid(s), problems %+v, notes %q", designation, len(solids), problems, notes)
		}
	}
}

// The kernel is sent revolves and extrusions, never the word, and the measurement
// reads the part it is sent — a nut stood up along Y, a screw from its shank's end
// to the top of its head.
func TestStandard_TheKernelAndTheMeasurementSeeTheDrawnPart(t *testing.T) {
	d := Document{Name: "fixing", Units: "mm", Parts: []Part{
		{ID: "screw", Shape: "standard", Standard: "ISO 4762 M8x30", Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}},
		{ID: "nut", Shape: "standard", Standard: "iso 4032  m8", Position: []float64{0, -20, 0}, Rotation: []float64{0, 0, 0}},
	}}
	solids, _, problems, notes := SolidsAndOperations(d, Millimetre)
	if len(solids) != 2 || len(problems) != 0 {
		t.Fatalf("%d solid(s), problems %+v, notes %q", len(solids), problems, notes)
	}
	for _, n := range notes {
		if strings.Contains(n, "not a shape") {
			t.Errorf("the kernel request calls a standard part unknown: %s", n)
		}
	}
	// The nut through the mesh, which applies each part's rotation — Measure's box
	// does not, by design (overlay.go) — so this is where "stood up along Y" shows.
	lo := [3]float64{math.Inf(1), math.Inf(1), math.Inf(1)}
	hi := [3]float64{math.Inf(-1), math.Inf(-1), math.Inf(-1)}
	for _, g := range Tessellate(Document{Name: "nut", Units: "mm", Parts: d.Parts[1:]}, Millimetre).Groups {
		for _, tri := range g.Triangles {
			for _, v := range [][3]float64{tri.A, tri.B, tri.C} {
				for k := 0; k < 3; k++ {
					lo[k], hi[k] = math.Min(lo[k], v[k]), math.Max(hi[k], v[k])
				}
			}
		}
	}
	if y, z := hi[1]-lo[1], hi[2]-lo[2]; math.Abs(y-6.8) > 1e-6 || math.Abs(z-13) > 1e-6 || math.Abs((hi[1]+lo[1])/2+20) > 1e-6 {
		t.Errorf("the M8 nut is drawn %v tall along Y and %v along Z, centred at y %v; want 6.8 along its axis, "+
			"13 across the flats, centred on its position", y, z, (hi[1]+lo[1])/2)
	}
	// The screw through Measure: unrotated, so its box is exact, and it reads the
	// expansion rather than a unit box.
	for _, o := range Measure(Document{Name: "screw", Units: "mm", Parts: d.Parts[:1]}, Millimetre) {
		if o.Label == "overall height" && math.Abs(o.Value-38) > 1e-6 {
			t.Errorf("the M8x30 screw is %v tall; want 38, the shank and the head", o.Value)
		}
	}
}

// The figures are millimetres, drawn in the document's unit.
func TestStandard_IsSizedInTheDocumentsUnit(t *testing.T) {
	for units, per := range map[string]float64{"cm": 10, "m": 1000, "inches": 25.4} {
		screw := expandedStandard(t, "ISO 4762 M8x30", units, nil)
		_, maxX, minY, _ := loopBounds(screw.Profile)
		if math.Abs(maxX-6.5/per) > 1e-12 || math.Abs(minY+30/per) > 1e-12 {
			t.Errorf("in %s the M8x30 head radius is %v and its shank ends at %v; want %v and %v",
				units, maxX, minY, 6.5/per, -30/per)
		}
	}
	tube := expandedStandard(t, "EN 10219 SHS 40x40x3", "m", map[string]float64{"length": 0.6})
	if tube.Size["depth"] != 0.6 {
		t.Errorf("a section's length is already in the document's units; got depth %v", tube.Size["depth"])
	}
}

// A designation FORGE does not have is refused by name, with the nearest it does
// have — and the refusal is a fault, so the turn's repair loop hands it back.
func TestStandard_AnUnknownDesignationIsRefusedWithTheNearestNamed(t *testing.T) {
	for _, tc := range []struct {
		name, standard, units string
		size                  map[string]float64
		want                  []string
	}{
		{"a length ISO 4762 does not list", "ISO 4762 M8x33", "mm", nil,
			[]string{`"ISO 4762 M8x33"`, `the nearest it has are "ISO 4762 M8x30", "ISO 4762 M8x35"`}},
		{"the same screw under DIN's old number", "DIN 912 M8x30", "mm", nil, []string{`nearest it has are "ISO 4762 M8x30"`}},
		{"a sealed bearing's suffix", "608-2RS", "mm", nil, []string{`nearest it has are "ISO 15 608"`}},
		{"no designation", "", "mm", nil, []string{"names no designation"}},
		{"no unit to size it in", "ISO 15 608", "furlongs", nil, []string{"published in millimetres"}},
		{"a section with no length", "EN 10219 SHS 40x40x3", "mm", nil, []string{`"size": {"length": ...}`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := Document{Name: "x", Units: tc.units, Parts: []Part{{ID: "bolt", Shape: "standard", Standard: tc.standard, Size: tc.size}}}
			faults := d.Faults()
			if len(faults) != 1 {
				t.Fatalf("faults %+v; want exactly the refused part", faults)
			}
			for _, w := range tc.want {
				if !strings.Contains(faults[0].Detail, w) {
					t.Errorf("the refusal does not say %s:\n%s", w, faults[0].Detail)
				}
			}
			if got := d.Expanded().Parts; len(got) != 0 {
				t.Errorf("a refused standard part is drawn anyway: %+v", got)
			}
			if _, ok := StandardPartForTest(d.Parts[0], tc.units); ok {
				t.Error("StandardPartForTest accepts what the document refuses")
			}
		})
	}
}

// Expanding what was expanded changes nothing, which is what lets every reader call it.
func TestStandard_ExpandingTwiceIsExpandingOnce(t *testing.T) {
	d := Document{Name: "x", Units: "mm", Parts: []Part{
		{ID: "nut", Shape: "standard", Standard: "ISO 4032 M6", Position: []float64{1, 2, 3}, Rotation: []float64{0, 45, 0}, Mirrored: true},
		{ID: "row", Shape: "standard", Standard: "ISO 7089 M6", Repeat: &Repeat{Count: 4, Offset: []float64{20, 0, 0}}},
	}}
	once := d.Expanded()
	if twice := once.Expanded(); !reflect.DeepEqual(once, twice) {
		t.Errorf("expanding twice changed the document:\n%+v\n%+v", once.Parts, twice.Parts)
	}
	if len(once.Parts) != 5 || once.Parts[1].Shape != "revolve" || once.Parts[4].ID != "row-4" {
		t.Errorf("a repeated washer is not four revolves: %+v", once.Parts)
	}
}
