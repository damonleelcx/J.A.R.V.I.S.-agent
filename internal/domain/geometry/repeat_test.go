package geometry_test

import (
	"math"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// A wire wheel: one spoke, written sixty times.
//
// # What this closes
//
// One JSON object means one part, so sixty spokes meant sixty hand-written
// objects — and this model starts losing parts at thirteen. That ceiling is what
// Stage 3 of the plan is for. The obvious answer was to let the model write
// build123d Python, where sixty spokes is a for-loop; the reasoning for not
// doing that is in repeat.go, and the short version is that the loop's only real
// gift was repetition and repetition can be declared.
func spokedWheel(count int) geometry.Document {
	return geometry.Document{Name: "wheel", Units: "mm", Parts: []geometry.Part{
		{ID: "hub", Name: "Hub", Shape: "cylinder",
			Size: map[string]float64{"radius": 40, "height": 30}, Rotation: []float64{0, 0, 90}},
		{ID: "spoke", Name: "Spoke", Shape: "box",
			Size:     map[string]float64{"width": 4, "height": 4, "depth": 300},
			Position: []float64{0, 150, 0},
			Repeat:   &geometry.Repeat{Count: count, About: "x"}},
	}}
}

func TestRepeat_SixtySpokesFromOnePart(t *testing.T) {
	m := geometry.Tessellate(spokedWheel(60), geometry.Millimetre)
	if got := len(m.Groups); got != 61 {
		t.Fatalf("the wheel drew %d parts; one hub and sixty spokes is 61. A pattern that "+
			"does not reach the mesh is a pattern the viewport cannot show", got)
	}
	// They must be in DIFFERENT PLACES, measured by where each copy's material
	// actually sits. A first vertex is not enough: copies stacked at one
	// position but each spun about its own centre have different vertices and
	// the same location, which is exactly the bug — and a drill proved that
	// version of this test could not see it.
	seen := map[[2]int]bool{}
	for _, g := range m.Groups {
		if len(g.Triangles) == 0 {
			continue
		}
		var sum [3]float64
		n := 0
		for _, tr := range g.Triangles {
			for _, v := range [3][3]float64{tr.A, tr.B, tr.C} {
				sum[1] += v[1]
				sum[2] += v[2]
				n++
			}
		}
		seen[[2]int{int(math.Round(sum[1] / float64(n))), int(math.Round(sum[2] / float64(n)))}] = true
	}
	if len(seen) < 50 {
		t.Errorf("the sixty spokes sit at %d distinct centres. They are stacked on top of "+
			"each other, so the pattern spins each copy but never places it", len(seen))
	}
}

// The solid builder and the viewport must agree, or the exported file is a
// different wheel from the one on screen.
func TestRepeat_TheFileAndThePictureAgree(t *testing.T) {
	solids, _ := geometry.Solids(spokedWheel(12), geometry.Millimetre)
	mesh := geometry.Tessellate(spokedWheel(12), geometry.Millimetre)
	if len(solids) != len(mesh.Groups) {
		t.Errorf("the file has %d parts and the picture has %d. A downloaded file that is a "+
			"different shape from the render is the one failure a label cannot cover",
			len(solids), len(mesh.Groups))
	}
}

// A feature naming the pattern acts on every copy, and does not read as naming
// something that does not exist.
func TestRepeat_AFeatureNamingThePatternIsNotAFault(t *testing.T) {
	d := spokedWheel(8)
	d.Features = []geometry.Feature{{ID: "weld", Op: "fuse", Of: "hub", With: []string{"spoke"}}}
	if faults := d.Faults(); len(faults) > 0 {
		t.Errorf("fusing the spokes to the hub was reported as a fault, so a document that "+
			"builds is refused: %+v", faults)
	}
	ops, problems := (&d).Operations()
	_ = ops
	for _, p := range problems {
		if p.Severity == geometry.Error {
			t.Errorf("operation problem: %s — %s", p.Name, p.Detail)
		}
	}
}

// A count nobody could look at is refused rather than drawn.
func TestRepeat_RefusesACountThatWouldStopTheViewport(t *testing.T) {
	d := spokedWheel(100000)
	m := geometry.Tessellate(d, geometry.Millimetre)
	if len(m.Groups) > 600 {
		t.Fatalf("it drew %d parts. A pattern with no ceiling is a way for one part to "+
			"spend the whole tessellation budget", len(m.Groups))
	}
	var told bool
	for _, s := range m.Inferences {
		if len(s) > 0 {
			told = true
		}
	}
	if !told {
		t.Error("the pattern was refused and nothing said so, so the reader sees a wheel " +
			"with no spokes and no reason")
	}
}

// A part with no Repeat is untouched — the ordinary case must cost nothing.
func TestRepeat_LeavesOrdinaryPartsAlone(t *testing.T) {
	plain := geometry.Document{Name: "p", Units: "mm", Parts: []geometry.Part{
		{ID: "a", Name: "A", Shape: "box", Size: map[string]float64{"width": 10, "height": 10, "depth": 10}}}}
	m := geometry.Tessellate(plain, geometry.Millimetre)
	if len(m.Groups) != 1 || m.Groups[0].PartID != "a" {
		t.Errorf("a part with no repeat came out as %d group(s) with id %q", len(m.Groups), m.Groups[0].PartID)
	}
}

// A loft says whether it is smooth or faceted, and the kernel is told.
//
// # What this closes
//
// A loft's blend was always smooth and there was no way to ask for anything
// else. Smooth is the right default — it is what makes a loft the only way to
// say "sculpted", and a car body is a loft through six sections — but a hopper
// or a transition duct really is faceted, and rounding its corners silently is
// the kind of wrong nobody notices until it is machined.
//
// The value must reach the OPERATION. A flag on the document that stops at the
// document is a flag that does nothing, which is worse than not having it: the
// reply says the shape is faceted and the file is smooth.
func TestLoft_SaysWhetherItIsFaceted(t *testing.T) {
	d := geometry.Document{Name: "duct", Units: "mm",
		Parts: []geometry.Part{
			{ID: "a", Name: "A", Shape: "section",
				Profile: []geometry.Point{{X: -50, Y: -50}, {X: 50, Y: -50}, {X: 50, Y: 50}, {X: -50, Y: 50}}},
			{ID: "b", Name: "B", Shape: "section", Position: []float64{0, 0, 200},
				Profile: []geometry.Point{{X: -20, Y: -20}, {X: 20, Y: -20}, {X: 20, Y: 20}, {X: -20, Y: 20}}},
		},
		Features: []geometry.Feature{{ID: "blend", Op: "loft", Of: "a", With: []string{"b"}, Ruled: true}},
	}
	ops, problems := d.Operations()
	for _, p := range problems {
		if p.Severity == geometry.Error {
			t.Fatalf("the fixture does not resolve: %s — %s", p.Name, p.Detail)
		}
	}
	if len(ops) != 1 {
		t.Fatalf("wanted one operation, got %d", len(ops))
	}
	if !ops[0].Ruled {
		t.Error("the loft was asked to be faceted and the kernel is not told, so the reply " +
			"describes a shape the file does not contain")
	}

	// And the default is smooth, because that is what a sculpted body needs.
	d.Features[0].Ruled = false
	ops, _ = d.Operations()
	if ops[0].Ruled {
		t.Error("a loft defaulted to faceted; smooth is the reason to loft at all")
	}
}
