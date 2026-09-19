package geometry

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// CarFixture is a car part with the given size keys, in millimetres. Exported to
// the cad package's kernel fences through CarDocumentForTest.
func carPart(size map[string]float64) Part {
	return Part{ID: "gt", Name: "GT", Shape: "car", Class: "sports", Size: size,
		Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}
}

func expandedCar(t *testing.T, size map[string]float64) (Document, []Problem) {
	t.Helper()
	d := Document{Name: "car", Units: "mm", Parts: []Part{carPart(size)}}
	problems := ExpandTemplates(&d)
	for _, p := range d.Bind() {
		if p.Severity == Error {
			t.Fatalf("the written-out car does not bind: %s: %s", p.Name, p.Detail)
		}
	}
	return d, problems
}

// A car written out is a tree of ordinary parts with nothing missing, at the
// default proportions and at every extreme the fixtures name.
func TestCar_IsWrittenOutAsATreeWithNoFaults(t *testing.T) {
	for name, size := range CarFixturesForTest() {
		t.Run(name, func(t *testing.T) {
			d, problems := expandedCar(t, size)
			for _, p := range problems {
				if p.Severity == Error {
					t.Fatalf("the car was refused: %s", p.Detail)
				}
			}
			for _, p := range d.Parts {
				if isCar(p) {
					t.Fatal("a car part survived the write-out")
				}
			}
			if f := d.Faults(); len(f) != 0 {
				t.Fatalf("%d fault(s), first: %s: %s", len(f), f[0].Name, f[0].Detail)
			}
			placed := d.Expanded().Parts
			shapes := map[string]int{}
			for _, p := range placed {
				shapes[p.Shape]++
			}
			// The stations, 4 arch cutters, 4 × (tyre, rim, lugs), a splitter and fins.
			if shapes["section"] != carStations || shapes["extrusion"] < 8 || shapes["box"] != 1 {
				t.Fatalf("placed shapes %v", shapes)
			}
			expanded := d.Expanded()
			ops, featureProblems := expanded.Operations()
			if len(featureProblems) != 0 {
				t.Fatalf("feature problems: %v", featureProblems)
			}
			kinds := map[string]int{}
			for _, op := range ops {
				kinds[op.Op]++
			}
			if kinds["loft"] != 1 || kinds["cut"] != 1 || kinds["fillet"] != 1 || kinds["fuse"] != 4 {
				t.Fatalf("operations %v; a car is one loft, one cut of four arches, one fillet and "+
					"a fuse of the nuts to each wheel's rim", kinds)
			}
		})
	}
}

// Every number is bound: changing one parameter moves the wheels, the arches and
// the body's stations together, so a respec cannot leave a wheel outside its arch.
func TestCar_ARespecMovesTheWheelsTheArchesAndTheBodyTogether(t *testing.T) {
	d, _ := expandedCar(t, CarFixturesForTest()["default"])
	respec, problems := d.WithParameters(map[string]float64{"gt_wheelbase": 2800})
	for _, p := range problems {
		if p.Severity == Error {
			t.Fatalf("respec: %s", p.Detail)
		}
	}
	at := func(doc *Document, id string) []float64 {
		for _, p := range doc.Expanded().Parts {
			if p.ID == id {
				return p.Position
			}
		}
		t.Fatalf("no part %s", id)
		return nil
	}
	for _, id := range []string{"gt/front-left/tyre", "gt/body/arch-front-left", "gt/body/station-0"} {
		before, after := at(&d, id)[0], at(respec, id)[0]
		if math.Abs(after-before-100) > 1e-6 {
			t.Errorf("%s moved %g along the car when the wheelbase grew by 200; it should move 100", id, after-before)
		}
	}
	if f := respec.Faults(); len(f) != 0 {
		t.Fatalf("the respec car has %d fault(s): %s", len(f), f[0].Detail)
	}
}

// A car outside its class's published proportions is WARNED about and still built.
func TestCar_ProportionsOutsideTheClassAreWarnedNeverRefused(t *testing.T) {
	size := CarFixturesForTest()["default"]
	_, inside := expandedCar(t, size)
	for _, p := range inside {
		if strings.Contains(p.Detail, "outside") {
			t.Fatalf("a car inside the sports range was warned: %s", p.Detail)
		}
	}
	stretched := map[string]float64{}
	for k, v := range size {
		stretched[k] = v
	}
	stretched["wheelbase"] = 3400 // 0.74 of the length; the sports cars span about 0.54-0.59
	stretched["front_overhang"] = 560
	d, problems := expandedCar(t, stretched)
	warned := false
	for _, p := range problems {
		if p.Severity == Error {
			t.Fatalf("a proportion refused the car: %s", p.Detail)
		}
		if strings.Contains(p.Detail, "wheelbase as a share of length") && strings.Contains(p.Detail, "UNVALIDATED") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("no warning for a wheelbase 0.74 of the length: %v", problems)
	}
	if hasCar(d.Parts) || d.Root == "" {
		t.Fatal("the warned car was not built")
	}
}

// A car the template cannot build is refused by the number that makes it
// impossible, left as a "car" part, and reported by Faults so the repair loop sees it.
func TestCar_AnImpossibleCarIsNamedAndLeftForTheRepairLoop(t *testing.T) {
	size := map[string]float64{}
	for k, v := range CarFixturesForTest()["default"] {
		size[k] = v
	}
	size["front_overhang"] = 300 // inside the wheel arch
	d := Document{Name: "car", Units: "mm", Parts: []Part{carPart(size)}}
	problems := ExpandTemplates(&d)
	if !anyError(problems) || !strings.Contains(problems[0].Detail, "front overhang") {
		t.Fatalf("problems %v", problems)
	}
	if !hasCar(d.Parts) {
		t.Fatal("the refused car was dropped instead of left for the repair loop")
	}
	faults := d.Faults()
	if len(faults) == 0 || !strings.Contains(faults[0].Detail, "front overhang") {
		t.Fatalf("Faults did not report the car: %v", faults)
	}
	missing := Document{Units: "mm", Parts: []Part{carPart(map[string]float64{"length": 4500})}}
	if f := missing.Faults(); len(f) == 0 || !strings.Contains(f[0].Detail, "no \"width\"") {
		t.Fatalf("a car with no width: %v", f)
	}
}

// Settling twice writes the car out once.
func TestCar_WritingOutIsIdempotent(t *testing.T) {
	d, _ := expandedCar(t, CarFixturesForTest()["default"])
	before, _ := json.Marshal(d)
	ExpandTemplates(&d)
	after, _ := json.Marshal(d)
	if string(before) != string(after) {
		t.Fatal("expanding a written-out car changed it")
	}
}

// A car a tree places is written out where the tree places it.
func TestCar_ACarDefinitionPlacedByATreeIsWrittenOutInPlace(t *testing.T) {
	def := carPart(CarFixturesForTest()["default"])
	d := Document{Name: "two cars", Units: "mm", Definitions: []Part{def},
		Assemblies: []Assembly{{ID: "yard", Children: []Child{
			{ID: "a", Ref: "gt"}, {ID: "b", Ref: "gt", Position: []float64{0, 0, 4000}}}}},
		Root: "yard"}
	if p := ExpandTemplates(&d); anyError(p) {
		t.Fatalf("%v", p)
	}
	d.Bind()
	if f := d.Faults(); len(f) != 0 {
		t.Fatalf("%v", f)
	}
	n := 0
	for _, p := range d.Expanded().Parts {
		if strings.HasSuffix(p.ID, "/body/station-0") {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("%d bodies placed; the yard places two cars", n)
	}
}

// Every figure in the proportion table names where it came from, and the table
// says it is UNVALIDATED (damon's decision, 2026-09-18).
func TestCarProportions_EveryFigureNamesItsSourceAndTheTableIsUnvalidated(t *testing.T) {
	if carProportions.Status != "UNVALIDATED" || !strings.HasPrefix(carProportions.StatusNote, "UNVALIDATED") {
		t.Fatalf("the table's status is %q", carProportions.Status)
	}
	if carProportions.ToleranceSource == "" {
		t.Fatal("the tolerance does not say whose number it is")
	}
	for class, cl := range carProportions.Classes {
		if len(cl.Cars) < 2 {
			t.Errorf("%s has %d car(s); a range needs at least two", class, len(cl.Cars))
		}
		for _, c := range cl.Cars {
			for name, f := range map[string]*citedFigure{"length": c.Length, "width": c.Width, "height": c.Height,
				"wheelbase": c.Wheelbase, "track_front": c.TrackFront, "track_rear": c.TrackRear,
				"front_overhang": c.FrontOverhang, "rear_overhang": c.RearOverhang, "ground_clearance": c.GroundClearance} {
				if f == nil {
					continue
				}
				if f.MM <= 0 || strings.TrimSpace(f.Source) == "" || strings.TrimSpace(f.AsWritten) == "" ||
					(f.Retrieved != "fetched" && f.Retrieved != "search") {
					t.Errorf("%s %s: %+v has no source, no figure as written, or no retrieval", c.Name, name, *f)
				}
			}
			if c.TyreFront != nil {
				if _, _, _, ok := tyreSize(c.TyreFront.Size); !ok || c.TyreFront.Source == "" {
					t.Errorf("%s tyre %+v", c.Name, *c.TyreFront)
				}
			}
		}
	}
}

// A tyre designation reads as the diameter it is: 235/40 ZR19 is 19 inches of rim
// and two 94 mm sidewalls.
func TestCarProportions_ATyreDesignationReadsAsItsDiameter(t *testing.T) {
	w, d, rim, ok := tyreSize("235/40 ZR19")
	if !ok || w != 235 || math.Abs(rim-482.6) > 1e-9 || math.Abs(d-(482.6+188)) > 1e-9 {
		t.Fatalf("235/40 ZR19 read as %g wide, %g outside, %g rim", w, d, rim)
	}
}

// A corner radius the body's sections cannot carry is refused by the number that
// controls it, before a body with no outline is written.
func TestCar_AnEdgeRadiusTheSectionsCannotCarryIsRefusedByName(t *testing.T) {
	size := map[string]float64{}
	for k, v := range CarFixturesForTest()["short-tall-suv"] {
		size[k] = v
	}
	size["edge_radius"] = 0.08
	size["nose_height"] = 0.2
	size["ride_height"] = 150
	d := Document{Name: "car", Units: "mm", Parts: []Part{carPart(size)}}
	problems := ExpandTemplates(&d)
	if !anyError(problems) || !strings.Contains(problems[len(problems)-1].Detail, "edge_radius") {
		t.Fatalf("problems %v", problems)
	}
	if !hasCar(d.Parts) || len(d.Definitions) != 0 {
		t.Fatal("a car whose sections cannot be drawn was written out anyway")
	}
}
