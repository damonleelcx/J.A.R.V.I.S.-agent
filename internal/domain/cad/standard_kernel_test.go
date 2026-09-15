package cad_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// Standard parts built by the real kernel. Phase 2, stage A3; the gap its PR left
// open ("no real-kernel test builds one"), closed 2026-09-15.
//
// # The problem this solves
//
// A3's fences prove the kernel is SENT revolves and extrusions with no problems,
// and that the Go mesh of a nut is the right size. Neither is OCCT. A revolve whose
// profile the sidecar reads about the wrong axis, an extrusion it runs from zero
// where the mesh centres it, a hole loop it drops, a document unit applied twice —
// each would send the same request, pass every one of those fences, and export a
// STEP file with the wrong fastener in it.
//
// # Why the expectations are typed here and not read from the catalogue
//
// An expectation computed from standard.go's own rows agrees with a wrong row. So
// each solid below is worked out from the published figures (the tables standard.go
// names), by the formula for that solid: a screw is a head cylinder on a shank
// cylinder (the socket, like the thread, is not drawn), a nut a hexagonal prism
// with its bore taken out, a washer and a bearing a tube, a hollow section a rounded
// rectangle less a rounded rectangle, an angle the EN 10056-1 area formula.
//
// # Tolerance
//
// Volume within one part in 10,000 and every extent of the bounding box, and every
// centre the formula gives, within 0.01 mm. OCCT's properties on these solids —
// lines, circles and exact arcs — agree with the formulas far more closely than
// that. The smallest defect this is here for, an L20x20x3 toe radius of 2 instead
// of 1.75 (docs/bugfix/2026-09-15-an-angles-toe-radius-was-not-half-its-root-radius.md),
// moves the volume by 36 parts in 10,000.

const (
	standardVolumeTolerance = 1e-4 // relative
	standardBoxTolerance    = 0.01 // mm
)

func TestKernel_EveryStandardFamilyBuildsTheSolidItsFiguresDescribe(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	pi, skip := math.Pi, math.NaN()
	// hexagon is a regular hexagon's area from its width across flats.
	hexagon := func(s float64) float64 { return math.Sqrt(3) / 2 * s * s }
	// rounded is a B × H rectangle with its four corners rounded to r.
	rounded := func(b, h, r float64) float64 { return b*h - (4-pi)*r*r }
	// angleArea is EN 10056-1 Table 1 Note 1's formula, in mm².
	angleArea := func(a, t, root, toe float64) float64 { return t*(2*a-t) + (1-pi/4)*(root*root-2*toe*toe) }

	const sectionLength = 250.0
	shs, rhs := rounded(40, 40, 6)-rounded(34, 34, 3), rounded(60, 40, 6)-rounded(54, 34, 3)
	angle := angleArea(20, 3, 3.5, 1.75)
	// The formulas, held to the areas the standards print (cm², three figures), so a
	// formula typed wrong here cannot agree with a drawing that is wrong the same way.
	for _, c := range []struct {
		name          string
		area, printed float64
	}{{"SHS 40x40x3 (EN 10219-2 Table C.2)", shs, 4.21}, {"RHS 60x40x3 (Table C.3)", rhs, 5.41}, {"L20x20x3 (EN 10056-1 Table 1)", angle, 1.12}} {
		if math.Abs(c.area/100-c.printed) > 0.005 {
			t.Fatalf("%s: the formula gives %.4f cm² and the standard prints %v", c.name, c.area/100, c.printed)
		}
	}

	type want struct {
		id, designation string
		length          float64    // a section's, in mm
		volume          float64    // mm³
		extent          [3]float64 // mm along x, y, z
		centre          [3]float64 // centre of volume from the part's position; NaN where not checked
	}
	// The screw: shank ⌀8 × 30 below the origin, head ⌀13 × 8 above it (ISO 4762 M8).
	shank, head := pi*4*4*30, pi*6.5*6.5*8
	wants := []want{
		{"screw", "ISO 4762 M8x30", 0, shank + head, [3]float64{13, 38, 13},
			[3]float64{0, (shank*-15 + head*4) / (shank + head), 0}},
		// Stood up along Y: 6.8 high, 13 across the flats along Z, corners along X (ISO 4032 M8).
		{"nut", "ISO 4032 M8", 0, (hexagon(13) - pi*4*4) * 6.8, [3]float64{26 / math.Sqrt(3), 6.8, 13}, [3]float64{0, 0, 0}},
		// ISO 7089 M8: 8.4 / 16 × 1.6, centred on its axis.
		{"washer", "ISO 7089 M8", 0, pi * (8*8 - 4.2*4.2) * 1.6, [3]float64{16, 1.6, 16}, [3]float64{0, 0, 0}},
		// ISO 15 608: 8 × 22 × 7.
		{"bearing", "ISO 15 608", 0, pi * (11*11 - 4*4) * 7, [3]float64{22, 7, 22}, [3]float64{0, 0, 0}},
		// Sections run along Z, centred on their own axis across it.
		{"shs", "EN 10219 SHS 40x40x3", sectionLength, shs * sectionLength, [3]float64{40, 40, sectionLength}, [3]float64{0, 0, skip}},
		{"rhs", "EN 10219 RHS 60x40x3", sectionLength, rhs * sectionLength, [3]float64{60, 40, sectionLength}, [3]float64{0, 0, skip}},
		// The heel at the origin, legs along +x and +y.
		{"angle", "EN 10056 L20x20x3", sectionLength, angle * sectionLength, [3]float64{20, 20, sectionLength}, [3]float64{skip, skip, skip}},
	}

	// The same parts in millimetres and in centimetres, each built in its own unit the
	// way the mass endpoint builds a variant (httpapi Mass passes v.Units). The kernel
	// reports millimetres either way, so a catalogue figure scaled twice, or not at
	// all, is a volume off by a thousand.
	//
	// ‼️ The unit argument is the DOCUMENT's unit, not the unit wanted back. This test
	// first passed Millimetre for the centimetre document and every length came back
	// ten times small — the kernel had read centimetres as millimetres, exactly as told.
	for _, units := range []struct {
		name  string
		unit  geometry.Unit
		perMM float64
	}{{"mm", geometry.Millimetre, 1}, {"cm", geometry.Centimetre, 0.1}} {
		doc := geometry.Document{Name: "fixings", Units: units.name}
		for i, w := range wants {
			p := geometry.Part{ID: w.id, Shape: "standard", Standard: w.designation,
				Position: []float64{float64(i) * 100 * units.perMM, 0, 0}, Rotation: []float64{0, 0, 0}}
			if w.length > 0 {
				p.Size = map[string]float64{"length": w.length * units.perMM}
			}
			doc.Parts = append(doc.Parts, p)
		}
		got, err := k.BuildProperties(ctx, doc, units.unit)
		if err != nil {
			t.Fatalf("%s: %v", units.name, err)
		}
		byID := map[string]geometry.SolidMeasure{}
		for _, m := range got.Properties {
			byID[m.ID] = m
		}
		for i, w := range wants {
			m, ok := byID[w.id]
			if !ok || !m.Measured {
				t.Errorf("%s in %s was not built and measured (measured: %+v)", w.designation, units.name, got.Properties)
				continue
			}
			if rel := math.Abs(m.Volume-w.volume) / w.volume; rel > standardVolumeTolerance {
				t.Errorf("%s in %s: OCCT built %.3f mm³ and the published figures give %.3f (%.2g off)",
					w.designation, units.name, m.Volume, w.volume, rel)
			}
			at := [3]float64{float64(i) * 100, 0, 0}
			for a := 0; a < 3; a++ {
				if extent := m.Bounds[a+3] - m.Bounds[a]; math.Abs(extent-w.extent[a]) > standardBoxTolerance {
					t.Errorf("%s in %s is %.4f mm along %c; the published figures give %.4f (box %v)",
						w.designation, units.name, extent, "xyz"[a], w.extent[a], m.Bounds)
				}
				if !math.IsNaN(w.centre[a]) && math.Abs(m.Centroid[a]-(at[a]+w.centre[a])) > standardBoxTolerance {
					t.Errorf("%s in %s has its centre at %.4f along %c; want %.4f",
						w.designation, units.name, m.Centroid[a], "xyz"[a], at[a]+w.centre[a])
				}
			}
		}
	}
}
