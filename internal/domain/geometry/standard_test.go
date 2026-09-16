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

// Several figures per family, each as its source prints it; the sources are named
// on the tables in standard.go (read 2026-09-15). Typed here from those tables and
// not from the Go rows, so a row that drifts from its standard fails whatever the
// drawing does with it — and every row of every family is here, so a row added
// without a figure read for it fails too.
func TestStandard_EveryFamilyCarriesTheFiguresItsSourcePublishes(t *testing.T) {
	has := map[string]bool{}
	count := map[string]int{}
	for _, d := range StandardDesignations() {
		has[d] = true
		count[strings.Join(strings.Fields(d)[:2], " ")]++
	}
	rows := func(family string, want int) {
		t.Helper()
		if count[family] != want {
			t.Errorf("the catalogue has %d %s designations and %d were read against the standard", count[family], family, want)
		}
	}

	// ISO 4762:2004 Table 1: d, dk max, k max, and the commercial lengths between its
	// stepped lines — with the series length on either side of that range absent.
	for _, s := range []struct {
		size                                      string
		d, dk, k, below, shortest, longest, above float64
	}{
		{"M3", 3, 5.5, 3, 4, 5, 30, 35},
		{"M4", 4, 7, 4, 5, 6, 40, 45},
		{"M5", 5, 8.5, 5, 6, 8, 50, 55},
		{"M6", 6, 10, 6, 8, 10, 60, 65},
		{"M8", 8, 13, 8, 10, 12, 80, 90},
		{"M10", 10, 16, 10, 12, 16, 100, 110},
		{"M12", 12, 18, 12, 16, 20, 120, 130},
	} {
		name := func(l float64) string { return "ISO 4762 " + s.size + "x" + mm(l) }
		screw := expandedStandard(t, name(s.shortest), "mm", nil)
		_, maxX, minY, maxY := loopBounds(screw.Profile)
		if !closeTo(maxX*2, s.dk) || !closeTo(maxY, s.k) || !closeTo(screw.Profile[1].X*2, s.d) || !closeTo(minY, -s.shortest) {
			t.Errorf("%s is a head ⌀%v × %v on a ⌀%v shank %v long; ISO 4762 has ⌀%v × %v on ⌀%v",
				name(s.shortest), maxX*2, maxY, screw.Profile[1].X*2, -minY, s.dk, s.k, s.d)
		}
		if !has[name(s.longest)] || has[name(s.below)] || has[name(s.above)] {
			t.Errorf("ISO 4762 tabulates %s from %v to %v; the catalogue has %v: %v, %v: %v, %v: %v", s.size,
				s.shortest, s.longest, s.below, has[name(s.below)], s.longest, has[name(s.longest)], s.above, has[name(s.above)])
		}
	}

	// ISO 4032:2012 Table 1: s (nom. = max.) and m max.
	for _, n := range []struct {
		size    string
		d, s, m float64
	}{{"M3", 3, 5.5, 2.4}, {"M4", 4, 7, 3.2}, {"M5", 5, 8, 4.7}, {"M6", 6, 10, 5.2}, {"M8", 8, 13, 6.8}, {"M10", 10, 16, 8.4}, {"M12", 12, 18, 10.8}} {
		nut := expandedStandard(t, "ISO 4032 "+n.size, "mm", nil)
		_, _, _, maxY := loopBounds(nut.Profile)
		if !closeTo(maxY*2, n.s) || !closeTo(nut.Size["depth"], n.m) || len(nut.Holes) != 1 || !closeTo(nut.Holes[0][0].X*2, n.d) {
			t.Errorf("ISO 4032 %s is %v across flats and %v high on %+v; the standard has %v and %v on ⌀%v",
				n.size, maxY*2, nut.Size["depth"], nut.Holes, n.s, n.m, n.d)
		}
	}
	rows("ISO 4032", 7)

	// ISO 7089:2000 Table 1: d1 nom. (min.), d2 nom. (max.), h nom.
	for _, w := range []struct {
		size      string
		d1, d2, h float64
	}{{"M3", 3.2, 7, 0.5}, {"M4", 4.3, 9, 0.8}, {"M5", 5.3, 10, 1}, {"M6", 6.4, 12, 1.6}, {"M8", 8.4, 16, 1.6}, {"M10", 10.5, 20, 2}, {"M12", 13, 24, 2.5}} {
		minX, maxX, minY, maxY := loopBounds(expandedStandard(t, "ISO 7089 "+w.size, "mm", nil).Profile)
		if !closeTo(minX*2, w.d1) || !closeTo(maxX*2, w.d2) || !closeTo(maxY-minY, w.h) {
			t.Errorf("ISO 7089 %s is %v / %v × %v; the standard has %v / %v × %v", w.size, minX*2, maxX*2, maxY-minY, w.d1, w.d2, w.h)
		}
	}
	rows("ISO 7089", 7)

	// ISO/R 15/1-1968 Table 3, diameter series 0, dimension series 10: d, D and B.
	// ISO 15:2017's own table was not reachable; standard.go says what carries these
	// figures forward to it.
	for _, b := range []struct {
		series  string
		d, D, B float64
	}{{"608", 8, 22, 7}, {"6000", 10, 26, 8}, {"6001", 12, 28, 8}, {"6002", 15, 32, 9}, {"6003", 17, 35, 10}, {"6004", 20, 42, 12}, {"6005", 25, 47, 12}} {
		minX, maxX, minY, maxY := loopBounds(expandedStandard(t, "ISO 15 "+b.series, "mm", nil).Profile)
		if !closeTo(minX*2, b.d) || !closeTo(maxX*2, b.D) || !closeTo(maxY-minY, b.B) {
			t.Errorf("%s is %v × %v × %v; ISO/R 15/1 Table 3 has %v × %v × %v", b.series, minX*2, maxX*2, maxY-minY, b.d, b.D, b.B)
		}
	}
	rows("ISO 15", 7)

	// EN 10219-2:2006: every section is in Table C.2 or C.3, with the corners B.3 gives
	// for calculation (2T outside, T inside for T ≤ 6), enclosing the area the table
	// prints (cm², three figures).
	for _, h := range []struct {
		designation   string
		b, h, t, area float64
	}{
		{"EN 10219 SHS 20x20x2", 20, 20, 2, 1.34}, {"EN 10219 SHS 30x30x3", 30, 30, 3, 3.01},
		{"EN 10219 SHS 40x40x3", 40, 40, 3, 4.21}, {"EN 10219 SHS 40x40x4", 40, 40, 4, 5.35},
		{"EN 10219 SHS 50x50x5", 50, 50, 5, 8.36}, {"EN 10219 RHS 40x20x2", 40, 20, 2, 2.14},
		{"EN 10219 RHS 60x40x3", 60, 40, 3, 5.41},
	} {
		tube := expandedStandard(t, h.designation, "mm", map[string]float64{"length": 100})
		minX, maxX, minY, maxY := loopBounds(tube.Profile)
		inMinX, inMaxX, inMinY, inMaxY := loopBounds(tube.Holes[0])
		ro, ri := tube.Profile[0].Radius, tube.Holes[0][0].Radius
		b, hh, ib, ih := maxX-minX, maxY-minY, inMaxX-inMinX, inMaxY-inMinY
		area := (b*hh - (4-math.Pi)*ro*ro) - (ib*ih - (4-math.Pi)*ri*ri)
		if !closeTo(b, h.b) || !closeTo(hh, h.h) || !closeTo((b-ib)/2, h.t) || !closeTo((hh-ih)/2, h.t) ||
			!closeTo(ro, 2*h.t) || !closeTo(ri, h.t) || math.Abs(area/100-h.area) > 0.005 {
			t.Errorf("%s is %v × %v with walls %v/%v, corners %v and %v, enclosing %.4f cm²; EN 10219-2 has %v × %v × %v, "+
				"corners %v and %v, %v cm²", h.designation, b, hh, (b-ib)/2, (hh-ih)/2, ro, ri, area/100, h.b, h.h, h.t, 2*h.t, h.t, h.area)
		}
	}
	rows("EN 10219", 7)

	// EN 10056-1:1998 Table 1 and EN 10056-1:2017 Table 1, which print the same a, t, root
	// radius and area for these rows; the 1998 Note 1's toe radius of half the root radius
	// (what the 2017 edition says of it was not reachable); and the sectional area by that
	// note's formula is the printed one.
	for _, a := range []struct {
		size             string
		a, t, root, area float64
	}{{"L20x20x3", 20, 3, 3.5, 1.12}, {"L30x30x3", 30, 3, 5, 1.74}, {"L40x40x4", 40, 4, 6, 3.08}, {"L50x50x5", 50, 5, 7, 4.80}} {
		p := expandedStandard(t, "EN 10056 "+a.size, "mm", map[string]float64{"length": 100}).Profile
		toe, root := p[2].Radius, p[3].Radius
		area := a.t*(2*a.a-a.t) + (1-math.Pi/4)*(root*root-2*toe*toe)
		if !closeTo(p[1].X, a.a) || !closeTo(p[2].Y, a.t) || !closeTo(root, a.root) || !closeTo(toe, a.root/2) ||
			p[4].Radius != toe || math.Abs(area/100-a.area) > 0.005 {
			t.Errorf("EN 10056 %s has legs %v, thickness %v, root radius %v and toe radii %v/%v (%.4f cm²); the standard has "+
				"%v, %v, %v and a toe radius of half the root radius, %v (%v cm²)",
				a.size, p[1].X, p[2].Y, root, toe, p[4].Radius, area/100, a.a, a.t, a.root, a.root/2, a.area)
		}
	}
	rows("EN 10056", 4)
}
