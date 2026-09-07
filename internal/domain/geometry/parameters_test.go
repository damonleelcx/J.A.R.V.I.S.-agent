package geometry_test

import (
	"math"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// The bracket from docs/spikes/2026-09-05-parametric-cad-kernel/, in the shape
// that HELD across a 3.4x size sweep. rib_length follows the plate; that is the
// entire finding.
func parametricBracket(plateSize float64) *geometry.Document {
	return &geometry.Document{
		Name: "NEMA 17 bracket", Units: "mm",
		Parameters: []geometry.Parameter{
			{Name: "plate_size", Value: plateSize, Unit: "mm", How: geometry.Chosen},
			{Name: "fillet_radius", Value: 3, Unit: "mm", How: geometry.Chosen},
		},
		Derived: []geometry.Derived{
			{Name: "rib_length", Expression: "plate_size - 2 * fillet_radius",
				Why: "a rib that does not follow the plate overhangs it when the plate shrinks"},
		},
	}
}

func TestResolve_TheDerivedRibFollowsThePlateAcrossTheSweep(t *testing.T) {
	// The same six sizes the spike swept. Premise A: all six built a valid solid
	// once rib_length was derived; the same model with rib_length fixed at 52 mm
	// failed at plate_size=50.
	for _, plate := range []float64{35, 42.3, 50, 60, 80, 120} {
		res := parametricBracket(plate).Resolve()
		if !res.OK() {
			t.Fatalf("plate_size=%g: %+v", plate, res.Problems)
		}
		got, ok := res.Values["rib_length"]
		if !ok {
			t.Fatalf("plate_size=%g: rib_length did not resolve", plate)
		}
		if want := plate - 6; math.Abs(got.Number-want) > 1e-9 {
			t.Errorf("plate_size=%g: rib_length = %g, want %g", plate, got.Number, want)
		}
		if got.Number > plate {
			t.Errorf("plate_size=%g: rib overhangs the plate at %g — the sweep failure", plate, got.Number)
		}
		if got.Unit != "mm" {
			t.Errorf("plate_size=%g: rib_length inherited unit %q, want mm", plate, got.Unit)
		}
	}
}

// The failure the spike found, in the form it would now take: a "derived" value
// that is really a fixed number. It still resolves — it is a usable number — and
// it is called out, because nothing about the number itself reveals that it will
// not follow anything.
func TestResolve_ADerivedValueThatReadsNothingIsAFixedNumber(t *testing.T) {
	d := parametricBracket(50)
	d.Derived = append(d.Derived, geometry.Derived{Name: "rib_length_fixed", Expression: "52"})

	res := d.Resolve()
	if !res.OK() {
		t.Fatalf("a fixed number is usable and must not break the document: %+v", res.Problems)
	}
	if v := res.Values["rib_length_fixed"]; v.Number != 52 {
		t.Fatalf("rib_length_fixed = %g, want 52", v.Number)
	}
	if !hasProblem(res, geometry.Warning, "rib_length_fixed", "names no parameter") {
		t.Errorf("a derived value reading no parameter was not called out: %+v", res.Problems)
	}
}

// Premise B's actual defect, resolved. The model quoted 42.3 mm as a NEMA 17
// bolt "diagonal" and divided it — landing on 29.91 mm where the published
// square pitch is 31 mm. What matters HERE is the dependency edge: the derived
// figure must carry the parameter it rests on, or nothing downstream can tell
// that 29.91 is a NEMA 17 claim at all.
func TestResolve_ADerivedFigureCarriesTheParameterItRestsOn(t *testing.T) {
	d := &geometry.Document{
		Units: "mm",
		Parameters: []geometry.Parameter{
			{Name: "bolt_diagonal", Value: 42.3, Unit: "mm",
				How: geometry.FromStandard, Source: "NEMA 17 motor datasheet"},
			{Name: "plate_thickness", Value: 6, Unit: "mm", How: geometry.Chosen},
		},
		Derived: []geometry.Derived{
			{Name: "bolt_pitch", Expression: "(bolt_diagonal / sqrt(2))"},
			{Name: "hole_offset", Expression: "bolt_pitch / 2"},
		},
	}

	res := d.Resolve()
	if !res.OK() {
		t.Fatalf("%+v", res.Problems)
	}
	pitch := res.Values["bolt_pitch"]
	if math.Abs(pitch.Number-29.9105) > 0.001 {
		t.Fatalf("bolt_pitch = %g, want the 29.91 the spike observed", pitch.Number)
	}
	if !contains(pitch.Depends, "bolt_diagonal") {
		t.Errorf("bolt_pitch does not name the parameter it rests on: %v", pitch.Depends)
	}
	// Two hops. Provenance has to survive the whole chain, not just one edge.
	offset := res.Values["hole_offset"]
	if !contains(offset.Depends, "bolt_diagonal") {
		t.Errorf("provenance did not survive two hops: hole_offset depends %v", offset.Depends)
	}
	if contains(offset.Depends, "bolt_pitch") {
		t.Errorf("Depends must collapse to PARAMETERS; got the intermediate: %v", offset.Depends)
	}
}

func TestResolve_AMissingParameterLeavesTheValueAbsentNotZero(t *testing.T) {
	d := &geometry.Document{
		Units:      "mm",
		Parameters: []geometry.Parameter{{Name: "plate_size", Value: 50, Unit: "mm"}},
		Derived: []geometry.Derived{
			{Name: "rib_length", Expression: "plate_size - 2 * edge_margin"},
		},
	}
	res := d.Resolve()
	if res.OK() {
		t.Fatal("a derived value reading an undeclared parameter must be an error")
	}
	if v, ok := res.Values["rib_length"]; ok {
		t.Fatalf("rib_length resolved to %g; a value that could not be computed must be "+
			"ABSENT, because a silent 0 turns a broken document into a plausible one", v.Number)
	}
	if !hasProblem(res, geometry.Error, "rib_length", "edge_margin") {
		t.Errorf("the problem did not name what was missing: %+v", res.Problems)
	}
}

func TestResolve_ACycleIsNamedRatherThanHung(t *testing.T) {
	d := &geometry.Document{
		Units:      "mm",
		Parameters: []geometry.Parameter{{Name: "plate_size", Value: 50, Unit: "mm"}},
		Derived: []geometry.Derived{
			{Name: "a", Expression: "b + 1"},
			{Name: "b", Expression: "a + 1"},
		},
	}
	res := d.Resolve()
	if res.OK() {
		t.Fatal("a cycle must be an error")
	}
	if _, ok := res.Values["a"]; ok {
		t.Error("a value in a cycle must not resolve")
	}
	if !hasProblem(res, geometry.Error, "a", "cycle") {
		t.Errorf("the cycle was not named: %+v", res.Problems)
	}
}

// Millimetres mixed with inches is the one unit error that silently destroys a
// part, so it is an error and the result carries NO unit rather than a guess.
func TestResolve_MixedUnitsAreRefusedRatherThanPicked(t *testing.T) {
	d := &geometry.Document{
		Units: "mm",
		Parameters: []geometry.Parameter{
			{Name: "plate_size", Value: 50, Unit: "mm"},
			{Name: "bolt_gap", Value: 1, Unit: "in"},
		},
		Derived: []geometry.Derived{{Name: "span", Expression: "plate_size - bolt_gap"}},
	}
	res := d.Resolve()
	if !hasProblem(res, geometry.Error, "span", "mixes units") {
		t.Fatalf("mixed units were not reported: %+v", res.Problems)
	}
	if u := res.Values["span"].Unit; u != "" {
		t.Errorf("span claimed unit %q; a mixed-unit result must not claim one", u)
	}
}

// A bare multiplier must not stop a length inheriting its unit — otherwise the
// commonest expression in the domain resolves to a number with no scale.
func TestResolve_DimensionlessTermsDoNotVoteOnTheUnit(t *testing.T) {
	res := parametricBracket(50).Resolve()
	if got := res.Values["rib_length"].Unit; got != "mm" {
		t.Fatalf("rib_length unit = %q, want mm (the literal 2 must not clear it)", got)
	}
}

func TestResolve_OneNameCannotHaveTwoSourcesOfTruth(t *testing.T) {
	d := parametricBracket(50)
	d.Derived = append(d.Derived, geometry.Derived{Name: "plate_size", Expression: "60"})
	res := d.Resolve()
	if !hasProblem(res, geometry.Error, "plate_size", "two sources of truth") {
		t.Fatalf("a name that is both a parameter and derived was accepted: %+v", res.Problems)
	}
	if v := res.Values["plate_size"]; v.Number != 50 {
		t.Errorf("the parameter must stand; got %g", v.Number)
	}
}

func TestResolve_AParameterCannotShadowABuiltInConstant(t *testing.T) {
	d := &geometry.Document{
		Units:      "mm",
		Parameters: []geometry.Parameter{{Name: "pi", Value: 3, Unit: ""}},
	}
	if res := d.Resolve(); !hasProblem(res, geometry.Error, "pi", "built-in constant") {
		t.Fatalf("shadowing pi was accepted silently: %+v", res.Problems)
	}
}

// "Quoted from a standard" with nothing naming the standard is the 2026-09-02
// fabrication wearing provenance it does not have.
func TestResolve_AStandardClaimWithNoSourceIsCalledOut(t *testing.T) {
	d := &geometry.Document{
		Units: "mm",
		Parameters: []geometry.Parameter{
			{Name: "bolt_spacing", Value: 31, Unit: "mm", How: geometry.FromStandard},
		},
	}
	res := d.Resolve()
	if !res.OK() {
		t.Fatalf("a sourceless standard claim is still a usable number: %+v", res.Problems)
	}
	if !hasProblem(res, geometry.Warning, "bolt_spacing", "names no source") {
		t.Errorf("not called out: %+v", res.Problems)
	}
}

func TestResolve_AnEmptyDocumentIsNotAnError(t *testing.T) {
	// Every stored variant predates these fields. Absence must keep meaning
	// "not parametric", never "parametric and broken".
	res := (&geometry.Document{Name: "plain", Units: "mm"}).Resolve()
	if !res.OK() || len(res.Problems) != 0 || len(res.Values) != 0 {
		t.Fatalf("a non-parametric document produced %+v", res)
	}
}

func TestResolve_IsDeterministic(t *testing.T) {
	d := parametricBracket(50)
	d.Derived = append(d.Derived,
		geometry.Derived{Name: "zzz", Expression: "rib_length / 2"},
		geometry.Derived{Name: "aaa", Expression: "zzz + 1"},
	)
	first := d.Resolve()
	for i := 0; i < 20; i++ {
		got := d.Resolve()
		if strings.Join(got.Order, ",") != strings.Join(first.Order, ",") {
			t.Fatalf("evaluation order is not deterministic: %v vs %v", got.Order, first.Order)
		}
	}
}

func hasProblem(r geometry.Resolution, sev geometry.Severity, name, substr string) bool {
	for _, p := range r.Problems {
		if p.Severity == sev && p.Name == name && strings.Contains(p.Detail, substr) {
			return true
		}
	}
	return false
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
