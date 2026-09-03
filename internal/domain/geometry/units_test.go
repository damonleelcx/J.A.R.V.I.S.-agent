package geometry

import (
	"strings"
	"testing"
)

// An unrecognised unit must never be silently treated as millimetres. A wrong
// guess about scale is the difference between a bracket and a building.
func TestParseUnit_RefusesToGuess(t *testing.T) {
	for _, good := range []struct {
		in   string
		want Unit
	}{
		{"mm", Millimetre}, {"MM", Millimetre}, {" millimetres ", Millimetre},
		{"cm", Centimetre}, {"m", Metre}, {"metres", Metre},
		{"in", Inch}, {"inches", Inch}, {`"`, Inch},
	} {
		got, ok := ParseUnit(good.in)
		if !ok || got != good.want {
			t.Errorf("ParseUnit(%q) = %q,%v; want %q,true", good.in, got, ok, good.want)
		}
	}
	for _, bad := range []string{"", "  ", "furlongs", "millimetre-ish", "mils", "units", "mm2"} {
		if got, ok := ParseUnit(bad); ok {
			t.Errorf("ParseUnit(%q) resolved to %q — an unrecognised unit was guessed at", bad, got)
		}
	}
}

// The type exists so that a number cannot be rendered without its unit. If
// String ever emits a bare number for a known unit, the invariant is gone.
func TestQuantity_AlwaysCarriesItsUnit(t *testing.T) {
	q := NewQuantity(60, Millimetre)
	if got := q.String(); got != "60 mm" {
		t.Errorf("String() = %q, want %q", got, "60 mm")
	}
	if !strings.Contains(NewQuantity(60, UnitUnspecified).String(), "unit not stated") {
		t.Error("an unspecified quantity rendered without saying so — a reader would infer a scale")
	}
}

// Precision as authored travels with the value. Rendering 42.3 as 42 discards a
// precision that was stated; rendering it as 42.300 claims one that was not.
func TestQuantity_KeepsAuthoredPrecision(t *testing.T) {
	for _, tc := range []struct {
		in   float64
		want string
	}{
		{60, "60 mm"},
		{42.3, "42.3 mm"},
		{3.25, "3.25 mm"},
		{0.5, "0.5 mm"},
	} {
		if got := NewQuantity(tc.in, Millimetre).String(); got != tc.want {
			t.Errorf("NewQuantity(%v).String() = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestQuantity_ConvertsWithoutLosingOrInventingPrecision(t *testing.T) {
	mm := NewQuantity(60, Millimetre)
	m, ok := mm.In(Metre)
	if !ok {
		t.Fatal("mm to m failed")
	}
	// 60 mm authored to the millimetre is 0.060 m — three decimal places, and
	// the trailing zero is the precision that was already there. "0.06 m" would
	// quietly widen the tolerance tenfold, and "0 m" (the original bug, from an
	// inverted sign) loses the value entirely.
	if got := m.String(); got != "0.060 m" {
		t.Errorf("60 mm in metres = %q, want %q", got, "0.060 m")
	}
	// And the other direction must not invent decimals it does not have.
	back, ok := m.In(Millimetre)
	if !ok {
		t.Fatal("m to mm failed")
	}
	if got := back.String(); got != "60 mm" {
		t.Errorf("round trip gave %q, want %q", got, "60 mm")
	}
	if _, ok := NewQuantity(1, UnitUnspecified).In(Millimetre); ok {
		t.Error("a quantity with no unit was converted — there was nothing to convert from")
	}
}

// The renderer must not produce a bare number even when the assembly forgot to
// declare its unit.
func TestDimensions_NeverRendersABareNumber(t *testing.T) {
	box := Part{Shape: "box", Size: map[string]float64{"width": 50, "height": 5, "depth": 50}}
	if got := Dimensions(box, Millimetre); !strings.Contains(got, "mm") {
		t.Errorf("dimensions %q carry no unit", got)
	}
	unitless := Dimensions(box, UnitUnspecified)
	if !strings.Contains(unitless, "unit not stated") {
		t.Errorf("with no declared unit the dimensions rendered as %q — a reader would assume mm", unitless)
	}

	cyl := Part{Shape: "cylinder", Size: map[string]float64{"radius": 16, "height": 20}}
	got := Dimensions(cyl, Millimetre)
	if !strings.Contains(got, "⌀32 mm") {
		t.Errorf("cylinder rendered as %q; radius should be shown as a diameter with its unit", got)
	}
}
