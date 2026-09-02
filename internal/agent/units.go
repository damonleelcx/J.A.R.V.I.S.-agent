package agent

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Unit and coordinate integrity (PRD WRK-05).
//
// # The failure this prevents
//
// A prototype declares its units once, for the whole assembly, and every
// dimension inside it is a bare number. That works until the declaration is
// missing, unrecognised, or ignored — and then a bare 60 is rendered next to a
// bare 42.3 with nothing saying which scale either is on. The Zoo spike made the
// contrast concrete: its output attaches a unit to every value (`plateSize =
// 60mm`, never `60`), and a dimension that travels without its unit is a
// dimension that will eventually be read in the wrong one.
//
// # Why the model contract did not change
//
// The obvious fix is to make the model emit {"value": 60, "unit": "mm"} per
// dimension. It was rejected: the contract is already the largest thing the model
// gets wrong, and every field added to it is another field to get wrong. Instead
// the boundary converts ONCE — the declared assembly unit is resolved into a real
// Unit here — and internally a dimension cannot be formatted without one, because
// Quantity is the only thing that formats.
//
// So the model keeps a simple contract, and the invariant is enforced on our side
// of it, which is the side that can be tested.

// Unit is a length unit FORGE understands.
type Unit string

const (
	// UnitUnspecified is what a missing or unrecognised declaration becomes. It
	// is a real value rather than an error: refusing the whole reply over a unit
	// would throw away geometry the person can still use, and rendering it
	// silently as millimetres would be a guess presented as a fact.
	UnitUnspecified Unit = ""
	Millimetre      Unit = "mm"
	Centimetre      Unit = "cm"
	Metre           Unit = "m"
	Inch            Unit = "in"
)

// unitTable is the closed set, with the factor to millimetres.
//
// A table rather than a switch, so adding a unit is one row and the conversions
// are readable side by side. Millimetres are the base because that is what the
// domain is: this is a workbench for parts, not for surveying.
var unitTable = []struct {
	Unit    Unit
	ToMM    float64
	Aliases []string
}{
	{Millimetre, 1, []string{"mm", "millimetre", "millimeter", "millimetres", "millimeters"}},
	{Centimetre, 10, []string{"cm", "centimetre", "centimeter", "centimetres", "centimeters"}},
	{Metre, 1000, []string{"m", "metre", "meter", "metres", "meters"}},
	{Inch, 25.4, []string{"in", "inch", "inches", `"`}},
}

// ParseUnit resolves a declared unit, and says whether it recognised it.
//
// An unrecognised unit is NOT silently treated as millimetres. That would be a
// guess about scale, and a wrong guess about scale is the difference between a
// bracket and a building.
func ParseUnit(s string) (Unit, bool) {
	norm := strings.ToLower(strings.TrimSpace(s))
	if norm == "" {
		return UnitUnspecified, false
	}
	for _, row := range unitTable {
		for _, a := range row.Aliases {
			if norm == a {
				return row.Unit, true
			}
		}
	}
	return UnitUnspecified, false
}

// Known reports whether the unit is one FORGE can convert.
func (u Unit) Known() bool { _, ok := ParseUnit(string(u)); return ok }

func (u Unit) toMM() (float64, bool) {
	for _, row := range unitTable {
		if row.Unit == u {
			return row.ToMM, true
		}
	}
	return 0, false
}

// Quantity is a number that cannot be rendered without its unit.
//
// That is the whole point of the type. There is no exported field holding a bare
// float that a caller could format by accident: String is the only way out, and
// String always carries the unit or says the unit is unknown.
type Quantity struct {
	value float64
	unit  Unit
	// decimals is how precisely the value was authored. PRD WRK-05 asks for
	// precision to travel with the value: rendering an authored 42.3 as 42.300
	// claims a precision nobody stated, and rendering it as 42 discards one that
	// was.
	decimals int
}

// NewQuantity builds a quantity from a value and a resolved unit.
func NewQuantity(value float64, unit Unit) Quantity {
	return Quantity{value: value, unit: unit, decimals: naturalDecimals(value)}
}

// Value returns the magnitude. Named so that a call site formatting it by hand
// is visible in review as exactly that.
func (q Quantity) Value() float64 { return q.value }

// Unit returns the unit, which may be UnitUnspecified.
func (q Quantity) Unit() Unit { return q.unit }

// Specified reports whether this quantity knows its own scale.
func (q Quantity) Specified() bool { return q.unit.Known() }

// String renders the quantity for a reader.
//
// When the unit is unknown the number is still shown — it is the only figure
// there is — but it is marked, so nobody reads a scale into it that was never
// declared.
func (q Quantity) String() string {
	n := strconv.FormatFloat(q.value, 'f', q.decimals, 64)
	if !q.Specified() {
		return n + " (unit not stated)"
	}
	return n + " " + string(q.unit)
}

// In converts to another unit, reporting whether the conversion was possible.
//
// A quantity with no declared unit cannot be converted, because there is nothing
// to convert from. Returning a plausible number here would be inventing the
// premise of the whole calculation.
func (q Quantity) In(target Unit) (Quantity, bool) {
	from, okFrom := q.unit.toMM()
	to, okTo := target.toMM()
	if !okFrom || !okTo {
		return Quantity{}, false
	}
	converted := q.value * from / to
	return Quantity{value: converted, unit: target, decimals: convertedDecimals(q.decimals, from/to)}, true
}

// naturalDecimals reports how many decimal places a value was authored with,
// bounded so a float artefact does not become a precision claim.
func naturalDecimals(v float64) int {
	if v == math.Trunc(v) {
		return 0
	}
	s := strconv.FormatFloat(v, 'f', -1, 64)
	if i := strings.IndexByte(s, '.'); i >= 0 {
		if d := len(s) - i - 1; d <= 4 {
			return d
		}
	}
	return 4
}

// convertedDecimals carries precision through a conversion instead of losing or
// inventing it.
//
// The ratio is source-to-target. Converting to a LARGER unit shrinks the number,
// so more decimal places are needed to say the same thing: 60 mm authored to the
// millimetre is 0.060 m, not 0.06 m and certainly not "0 m". The trailing zero is
// not noise, it is the precision that was already there — dropping it would
// quietly widen the tolerance by a factor of ten.
//
// An earlier version had this sign backwards and rendered 60 mm as "0 m".
func convertedDecimals(source int, ratio float64) int {
	extra := 0
	if ratio > 0 && ratio != 1 {
		// ratio < 1 means the target unit is larger, which needs more decimals.
		// Negative on purpose when converting to a SMALLER unit: 0.060 m is
		// 60 mm, not 60.000 mm — the extra places would claim a precision the
		// value never had. Clamping happens on the result, not here.
		extra = int(math.Round(-math.Log10(ratio)))
	}
	d := source + extra
	if d < 0 {
		d = 0
	}
	if d > 6 {
		d = 6
	}
	return d
}

// Frame is the coordinate frame a position is expressed in.
//
// One value today, named rather than implied. PRD WRK-05 requires the frame to
// travel with the coordinate, and "assembly origin, Y up" written in a prompt is
// not the frame travelling — it is the frame being hoped for.
type Frame string

const (
	// FrameAssembly is the assembly's own origin, Y up, millimetres unless the
	// prototype says otherwise. It is what converse.go's contract describes.
	FrameAssembly Frame = "assembly-origin-y-up"
)

// Dimensions renders a part's size with every number carrying its unit.
//
// This replaces the previous arrangement, where the assembly's unit string was
// appended to a joined list of bare numbers — correct when the unit was declared
// and silently wrong when it was not.
func Dimensions(p PrototypePart, unit Unit) string {
	q := func(v float64) string { return NewQuantity(v, unit).String() }
	s := p.Size
	if s == nil {
		return ""
	}
	get := func(k string) (float64, bool) { v, ok := s[k]; return v, ok }

	switch p.Shape {
	case "cylinder", "cone", "tube":
		var parts []string
		if r, ok := get("radius"); ok {
			parts = append(parts, "⌀"+q(r*2))
		}
		if rt, ok := get("radius_top"); ok {
			if r, had := get("radius"); !had || rt != r {
				parts = append(parts, "top ⌀"+q(rt*2))
			}
		}
		if h, ok := get("height"); ok {
			parts = append(parts, "h "+q(h))
		}
		return strings.Join(parts, " · ")
	case "sphere":
		if r, ok := get("radius"); ok {
			return "⌀" + q(r*2)
		}
		return ""
	default:
		w, hasW := get("width")
		h, hasH := get("height")
		d, hasD := get("depth")
		if !hasW && !hasH && !hasD {
			return ""
		}
		part := func(v float64, ok bool) string {
			if !ok {
				return "?"
			}
			return q(v)
		}
		return fmt.Sprintf("%s × %s × %s", part(w, hasW), part(h, hasH), part(d, hasD))
	}
}
