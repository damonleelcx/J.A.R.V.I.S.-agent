package geometry

import (
	"math"
	"strings"
	"testing"
)

// spurSize is the gear the live measurement asks for: 20 teeth, module 2, 6 thick.
func spurSize() map[string]float64 {
	return map[string]float64{"module": 2, "teeth": 20, "depth": 6}
}

func gearDocument(size map[string]float64) Document {
	return Document{Name: "gear", Units: "mm", Parts: []Part{{
		ID: "spur", Name: "Spur Gear", Shape: "gear", Size: size,
		Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}}}
}

// The drawing is the gear a gear standard describes, not merely a toothed shape.
//
// # Why these three properties
//
// A toothed outline is easy to get plausibly wrong: petal lobes, straight flanks,
// teeth that are too fat or too thin all look like a gear at a glance — the
// generated reference images drew exactly that (sketch.go). So this measures the
// three things that make it THIS gear, each against the definition rather than
// against gear.go's own arithmetic:
//
//   - it reaches the tip circle m(z+2)/2 and the root circle m(z-2.5)/2;
//   - it has z teeth;
//   - a tooth at the PITCH circle is half the circular pitch thick, π·m/2 —
//     which is the property of an involute tooth on a standard gear that a wrong
//     curve does not keep.
func TestAGearHasInvoluteTeeth(t *testing.T) {
	profile, _, ok := GearOutlineForTest(spurSize())
	if !ok {
		t.Fatal("a module-2, 20-tooth gear was refused")
	}
	flat := FlattenOutlineForTest(profile)
	if len(flat) == 0 {
		t.Fatal("the gear's outline does not flatten, so nothing could build it")
	}
	const module, teeth = 2.0, 20
	tip, root, pitch := module*(teeth+2)/2, module*(teeth-2.5)/2, module*teeth/2

	lo, hi := math.Inf(1), 0.0
	for _, p := range flat {
		r := math.Hypot(p[0], p[1])
		lo, hi = math.Min(lo, r), math.Max(hi, r)
	}
	if math.Abs(hi-tip) > 1e-6 || math.Abs(lo-root) > 1e-6 {
		t.Errorf("the outline runs from radius %.6f to %.6f; a module-2, 20-tooth gear runs from "+
			"its root circle %.3f to its tip circle %.3f", lo, hi, root, tip)
	}

	// Teeth: runs of points on the tip circle. The loop starts at a root, so a
	// run cannot wrap round the start and be counted twice.
	tips, onTip := 0, false
	for _, p := range flat {
		at := math.Abs(math.Hypot(p[0], p[1])-tip) < 1e-6
		if at && !onTip {
			tips++
		}
		onTip = at
	}
	if tips != teeth {
		t.Errorf("the outline has %d teeth; it was asked for %d", tips, teeth)
	}

	// Where the outline crosses the pitch circle, in the order it is drawn. Tooth
	// 0 is drawn first, so its two flanks are the first two crossings.
	var crossings []float64
	for i := range flat {
		a, b := flat[i], flat[(i+1)%len(flat)]
		ra, rb := math.Hypot(a[0], a[1]), math.Hypot(b[0], b[1])
		if (ra-pitch)*(rb-pitch) >= 0 {
			continue
		}
		s := (pitch - ra) / (rb - ra)
		crossings = append(crossings, math.Atan2(a[1]+s*(b[1]-a[1]), a[0]+s*(b[0]-a[0])))
	}
	if len(crossings) != 2*teeth {
		t.Fatalf("the outline crosses the pitch circle %d times; %d teeth cross it %d times",
			len(crossings), teeth, 2*teeth)
	}
	thickness := pitch * (crossings[1] - crossings[0])
	if want := math.Pi * module / 2; math.Abs(thickness-want) > 0.02 {
		t.Errorf("a tooth is %.4f mm thick at the pitch circle; a standard involute tooth is half the "+
			"circular pitch, π·m/2 = %.4f. A flank that is not an involute gets this wrong.",
			thickness, want)
	}
	if centre := (crossings[0] + crossings[1]) / 2; math.Abs(centre-math.Pi/2) > 1e-3 {
		t.Errorf("tooth 0 is centred at %.4f rad; it points up +Y (π/2), so a gear facing you has "+
			"a tooth at the top", centre)
	}
}

// The kernel has no gear case and must never need one.
func TestTheKernelIsSentAGearAsAnExtrusion(t *testing.T) {
	solids, notes := Solids(gearDocument(spurSize()), Millimetre)
	if len(solids) != 1 {
		t.Fatalf("want one solid, got %d: %v", len(solids), notes)
	}
	s := solids[0]
	if s.Shape != "extrusion" {
		t.Errorf("the kernel would be sent shape %q; sidecar.py has no gear case and would refuse "+
			"the part — the involute is worked out once, in gear.go", s.Shape)
	}
	if s.Dims["depth"] != 6 || s.Outline == nil {
		t.Errorf("the extrusion is %v deep with outline %v; want 6 and the gear's section",
			s.Dims["depth"], s.Outline != nil)
	}
	facets := false
	for _, n := range notes {
		if strings.Contains(n, "not in this file") {
			t.Errorf("a correct gear was reported missing: %s", n)
		}
		if strings.Contains(n, "straight segments") {
			facets = true
		}
	}
	if !facets {
		t.Errorf("the export does not say its flanks are faceted. A STEP file is the one place a "+
			"machinist reads that, and it is true of every gear: %v", notes)
	}
}

// What the viewport, the contact sheet and the measurement path all read.
func TestAGearIsDrawnAndMeasuredAtItsOwnSize(t *testing.T) {
	doc := gearDocument(spurSize())
	m := Tessellate(doc, Millimetre)
	if len(m.Triangles()) == 0 {
		t.Fatalf("the gear drew nothing: %v", m.Inferences)
	}
	var maxY, maxZ, maxR float64
	for _, tr := range m.Triangles() {
		for _, v := range [][3]float64{tr.A, tr.B, tr.C} {
			maxY, maxZ = math.Max(maxY, v[1]), math.Max(maxZ, v[2])
			maxR = math.Max(maxR, math.Hypot(v[0], v[1]))
		}
	}
	// The tip is an ARC, stepped into chords at the viewport's fineness, so the
	// mesh touches the tip circle at its vertices and need not have one exactly
	// on +Y. Radius is the exact claim; y only has to be within the chord's sag.
	if math.Abs(maxR-22) > 1e-6 || maxY < 21.95 || math.Abs(maxZ-3) > 1e-9 {
		t.Errorf("the mesh reaches radius %.6f, y %.6f and z %.6f; the gear's tip circle is 22, "+
			"tooth 0 points up +Y, and it is 6 thick, centred", maxR, maxY, maxZ)
	}
	for _, n := range m.Inferences {
		if strings.Contains(n, "bounding box") {
			t.Errorf("the gear was drawn as a bounding box: %s", n)
		}
	}

	lo, hi := bounds(doc)
	if math.Abs(hi[1]-22) > 1e-9 || math.Abs(lo[2]+3) > 1e-9 {
		t.Errorf("a measured dimension would be drawn against %v..%v; the gear reaches 22 and ±3", lo, hi)
	}
	if got := Dimensions(doc.Parts[0], Millimetre); !strings.Contains(got, "20 teeth") ||
		!strings.Contains(got, "⌀44") {
		t.Errorf("the summary line reads %q; it should name the teeth and the 44 mm outside diameter", got)
	}
}

// A gear that cannot exist is a MISSING part, handed to the repair loop — never a
// block with "gear" on it.
func TestAGearThatCannotExistIsAFaultNotABox(t *testing.T) {
	for _, tc := range []struct {
		name string
		size map[string]float64
		says string
	}{
		{"no module", map[string]float64{"teeth": 20, "depth": 6}, `no "module"`},
		{"no teeth", map[string]float64{"module": 2, "depth": 6}, `no "teeth"`},
		{"a fractional tooth", map[string]float64{"module": 2, "teeth": 20.5, "depth": 6}, "whole number"},
		{"too few teeth", map[string]float64{"module": 2, "teeth": 2, "depth": 6}, "at least 3"},
		{"more teeth than a build draws", map[string]float64{"module": 1, "teeth": 600, "depth": 6},
			"the most this build will draw"},
		{"a bore through the roots", map[string]float64{"module": 2, "teeth": 20, "depth": 6,
			"bore_radius": 17.5}, "reaches the roots"},
		{"a pressure angle past 45", map[string]float64{"module": 2, "teeth": 20, "depth": 6,
			"pressure_angle": 50}, "between 0 and 45"},
		{"no face width at all", map[string]float64{"module": 2, "teeth": 20, "depth": -1},
			"positive length"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := gearDocument(tc.size)
			found := false
			for _, f := range doc.Faults() {
				if strings.Contains(f.Detail, tc.says) {
					found = true
				}
			}
			if !found {
				t.Errorf("Faults() does not say %q, so the repair loop is never told: %v", tc.says, doc.Faults())
			}
			said := false
			for _, p := range doc.ProfileProblems() {
				if p.Severity == Error && strings.Contains(p.Detail, tc.says) {
					said = true
				}
			}
			if !said {
				t.Errorf("the reader is not told either: %v", doc.ProfileProblems())
			}
			if tris := Tessellate(doc, Millimetre).Triangles(); len(tris) != 0 {
				t.Errorf("a gear that cannot exist drew %d triangles", len(tris))
			}
			if solids, _ := Solids(doc, Millimetre); len(solids) != 0 {
				t.Errorf("a gear that cannot exist was sent to the kernel as %q", solids[0].Shape)
			}
		})
	}
}

func TestAGearIsReadFromTheWordsAModelWrites(t *testing.T) {
	t.Run("a correct gear is told nothing", func(t *testing.T) {
		// The pressure angle defaults to 20 because the contract says an absence
		// means 20. A note here would fire on every correctly written gear.
		doc := gearDocument(spurSize())
		if got := doc.ProfileProblems(); len(got) != 0 {
			t.Errorf("a correctly written 20-tooth gear raised %v", got)
		}
	})
	t.Run("thickness is the face width", func(t *testing.T) {
		doc := gearDocument(map[string]float64{"module": 2, "teeth": 20, "thickness": 6})
		solids, _ := Solids(doc, Millimetre)
		if len(solids) != 1 || solids[0].Dims["depth"] != 6 {
			t.Fatalf("a gear 6 \"thickness\" thick was not built 6 deep: %v", solids)
		}
		said := false
		for _, p := range doc.ProfileProblems() {
			if p.Severity == Warning && strings.Contains(p.Detail, `"thickness"`) {
				said = true
			}
		}
		if !said {
			t.Error("reading \"thickness\" as the face width is a reading, and the reader is not told")
		}
	})
	t.Run("few teeth say they are not undercut", func(t *testing.T) {
		doc := gearDocument(map[string]float64{"module": 2, "teeth": 12, "depth": 6})
		said := false
		for _, p := range doc.ProfileProblems() {
			if strings.Contains(p.Detail, "undercut") {
				said = true
			}
		}
		if !said {
			t.Error("a 12-tooth gear is undercut when it is cut and the drawing is not; nobody is told")
		}
	})
	t.Run("an outline on a gear is ignored and said", func(t *testing.T) {
		doc := gearDocument(spurSize())
		doc.Parts[0].Profile = []Point{{X: 0, Y: 0}, {X: 90, Y: 0}, {X: 90, Y: 90}}
		var maxX float64
		for _, tr := range Tessellate(doc, Millimetre).Triangles() {
			maxX = math.Max(maxX, math.Max(tr.A[0], math.Max(tr.B[0], tr.C[0])))
		}
		if maxX > 22+1e-6 {
			t.Errorf("the gear was drawn from the outline it carried (reaches x %.1f)", maxX)
		}
		said := false
		for _, p := range doc.ProfileProblems() {
			if strings.Contains(p.Detail, "was ignored") {
				said = true
			}
		}
		if !said {
			t.Error("an outline written onto a gear was dropped silently")
		}
	})
}

// A gear follows its parameters like any other bound dimension.
func TestAGearFollowsItsParameters(t *testing.T) {
	doc := gearDocument(spurSize())
	doc.Parameters = []Parameter{{Name: "tooth_count", Value: 20}}
	doc.Parts[0].SizeFrom = map[string]string{"teeth": "tooth_count"}
	next, problems := doc.WithParameters(map[string]float64{"tooth_count": 30})
	for _, p := range problems {
		if p.Severity == Error {
			t.Fatalf("respecifying the tooth count failed: %v", problems)
		}
	}
	_, hi := bounds(*next)
	if math.Abs(hi[1]-32) > 1e-9 {
		t.Errorf("with 30 teeth the gear reaches %.4f; module 2 × (30 + 2) / 2 is 32", hi[1])
	}
}

// Gears are expanded before patterns, so a repeated gear is repeated gears.
func TestARepeatedGearIsRepeatedGears(t *testing.T) {
	doc := gearDocument(spurSize())
	doc.Parts[0].Repeat = &Repeat{Count: 2, Offset: []float64{44, 0, 0}}
	m := Tessellate(doc, Millimetre)
	if len(m.Groups) != 2 {
		t.Fatalf("a gear repeated twice drew %d parts: %v", len(m.Groups), m.Inferences)
	}
	if solids, notes := Solids(doc, Millimetre); len(solids) != 2 || solids[1].Shape != "extrusion" {
		t.Errorf("a gear repeated twice was exported as %d solids: %v", len(solids), notes)
	}
}
