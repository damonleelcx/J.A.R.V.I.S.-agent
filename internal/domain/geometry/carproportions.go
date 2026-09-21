package geometry

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// The proportions a car of a class has, read from published cars.
//
// Stage C2 of the looks-designed work. damon's decision, 2026-09-18: FORGE picks
// published automotive references, cites each source per number, and labels the
// whole table UNVALIDATED. The car template checks a design against it and WARNS
// outside a range; it never refuses a design for its proportions — a designer
// asking for a long-tail hypercar is not making a mistake a table of six
// production cars can see.
//
// # Why the ranges are computed and not written
//
// The file holds only what a source said: each car's length, wheelbase, tyre and
// so on, each with where it came from. A range typed beside them would be a number
// with no source, which is exactly what the file exists not to hold. So every
// range is the span of the cited cars, widened by the file's stated tolerance (the
// one number in it that is FORGE's own, and says so), and a rule no cited car can
// answer is not checked at all rather than guessed.

//go:embed templates/car_proportions.json
var carProportionsJSON []byte

// citedFigure is one number a source gave, in millimetres.
type citedFigure struct {
	MM        float64 `json:"mm"`
	AsWritten string  `json:"as_written"`
	Source    string  `json:"source"`
	Retrieved string  `json:"retrieved"`
}

// citedTyre is a tyre designation a source gave.
type citedTyre struct {
	Size      string `json:"size"`
	Source    string `json:"source"`
	Retrieved string `json:"retrieved"`
}

type referenceCar struct {
	Name            string       `json:"name"`
	Length          *citedFigure `json:"length"`
	Width           *citedFigure `json:"width"`
	Height          *citedFigure `json:"height"`
	Wheelbase       *citedFigure `json:"wheelbase"`
	TrackFront      *citedFigure `json:"track_front"`
	TrackRear       *citedFigure `json:"track_rear"`
	TyreFront       *citedTyre   `json:"tyre_front"`
	FrontOverhang   *citedFigure `json:"front_overhang"`
	RearOverhang    *citedFigure `json:"rear_overhang"`
	GroundClearance *citedFigure `json:"ground_clearance"`
}

type proportionTable struct {
	Status          string  `json:"status"`
	StatusNote      string  `json:"status_note"`
	Tolerance       float64 `json:"tolerance"`
	ToleranceSource string  `json:"tolerance_source"`
	Classes         map[string]struct {
		Cars []referenceCar `json:"cars"`
	} `json:"classes"`
}

// carProportions is the table, read once. A file that does not parse is a build
// that should not ship, and TestCarProportions_EveryFigureNamesItsSource reads it.
var carProportions = mustReadProportions(carProportionsJSON)

func mustReadProportions(raw []byte) proportionTable {
	var t proportionTable
	if err := json.Unmarshal(raw, &t); err != nil {
		panic("geometry: templates/car_proportions.json does not parse: " + err.Error())
	}
	return t
}

// tyrePattern reads "245/35 ZR19", "255/70R18", "285/30 R 20".
var tyrePattern = regexp.MustCompile(`^\s*(\d+)\s*/\s*(\d+)\s*Z?R\s*(\d+)\s*$`)

// tyreSize is a designation's section width, outside diameter and rim diameter in
// millimetres: the rim is inches, the sidewall is the aspect ratio's share of the
// section width, twice.
func tyreSize(designation string) (width, outside, rim float64, ok bool) {
	m := tyrePattern.FindStringSubmatch(strings.ToUpper(designation))
	if m == nil {
		return 0, 0, 0, false
	}
	w, _ := strconv.ParseFloat(m[1], 64)
	aspect, _ := strconv.ParseFloat(m[2], 64)
	r, _ := strconv.ParseFloat(m[3], 64)
	rim = r * 25.4
	return w, rim + 2*w*aspect/100, rim, true
}

// proportionRule is one thing the table can say about a car: a ratio, or (for ride
// height) a length in millimetres.
type proportionRule struct {
	Key  string // what it is called in a warning
	What string // the sentence fragment it reads as
	MM   bool   // an absolute length in millimetres, not a ratio
	of   func(c referenceCar) (float64, bool)
}

func both(a, b *citedFigure) (float64, float64, bool) {
	if a == nil || b == nil || a.MM <= 0 || b.MM <= 0 {
		return 0, 0, false
	}
	return a.MM, b.MM, true
}

// proportionRules is every rule the template checks, in the order it reports them.
var proportionRules = []proportionRule{
	{Key: "wheelbase/length", What: "wheelbase as a share of length", of: func(c referenceCar) (float64, bool) {
		wb, l, ok := both(c.Wheelbase, c.Length)
		return wb / l, ok
	}},
	{Key: "height/length", What: "height as a share of length", of: func(c referenceCar) (float64, bool) {
		h, l, ok := both(c.Height, c.Length)
		return h / l, ok
	}},
	{Key: "wheel diameter/height", What: "wheel (tyre outside) diameter as a share of height", of: func(c referenceCar) (float64, bool) {
		if c.TyreFront == nil || c.Height == nil || c.Height.MM <= 0 {
			return 0, false
		}
		_, d, _, ok := tyreSize(c.TyreFront.Size)
		return d / c.Height.MM, ok
	}},
	{Key: "track/width", What: "mean track as a share of width", of: func(c referenceCar) (float64, bool) {
		f, r, ok := both(c.TrackFront, c.TrackRear)
		if !ok || c.Width == nil || c.Width.MM <= 0 {
			return 0, false
		}
		return (f + r) / 2 / c.Width.MM, true
	}},
	{Key: "front overhang/length", What: "front overhang as a share of length", of: func(c referenceCar) (float64, bool) {
		fo, l, ok := both(c.FrontOverhang, c.Length)
		return fo / l, ok
	}},
	{Key: "rear overhang/length", What: "rear overhang as a share of length", of: func(c referenceCar) (float64, bool) {
		ro, l, ok := both(c.RearOverhang, c.Length)
		return ro / l, ok
	}},
	{Key: "ride height", What: "ride height (the ground clearance the source states)", MM: true, of: func(c referenceCar) (float64, bool) {
		if c.GroundClearance == nil || c.GroundClearance.MM <= 0 {
			return 0, false
		}
		return c.GroundClearance.MM, true
	}},
}

// proportionRange is one rule's range for one class, and the cars it came from.
type proportionRange struct {
	Low, High float64
	From      []string
}

func (r proportionRange) mid() float64 { return (r.Low + r.High) / 2 }

// CarClasses names the classes the table has, sorted.
func CarClasses() []string {
	out := make([]string, 0, len(carProportions.Classes))
	for c := range carProportions.Classes {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// proportionRangeFor is a rule's range over a class's cited cars, widened by the
// table's tolerance; ok is false when no car in the class answers the rule. An
// empty class reads every class's cars (the pooled range), which is what a car
// with no class is given defaults from.
func proportionRangeFor(class string, rule proportionRule) (proportionRange, bool) {
	var cars []referenceCar
	if class == "" {
		for _, name := range CarClasses() {
			cars = append(cars, carProportions.Classes[name].Cars...)
		}
	} else {
		cars = carProportions.Classes[class].Cars
	}
	r := proportionRange{Low: math.Inf(1), High: math.Inf(-1)}
	for _, c := range cars {
		v, ok := rule.of(c)
		if !ok || math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		r.Low, r.High = math.Min(r.Low, v), math.Max(r.High, v)
		r.From = append(r.From, c.Name)
	}
	if len(r.From) == 0 {
		return proportionRange{}, false
	}
	tol := carProportions.Tolerance
	r.Low, r.High = r.Low*(1-tol), r.High*(1+tol)
	return r, true
}

// defaultFrom is a rule's midpoint for the class, or over every class when the
// class's own cars do not answer it; pooled says which.
func defaultFrom(class string, rule proportionRule) (value float64, pooled bool) {
	if r, ok := proportionRangeFor(class, rule); ok {
		return r.mid(), false
	}
	r, _ := proportionRangeFor("", rule)
	return r.mid(), true
}

func ruleNamed(key string) proportionRule {
	for _, r := range proportionRules {
		if r.Key == key {
			return r
		}
	}
	panic("geometry: no proportion rule " + key)
}

// tyreShares are the section width and the rim diameter as shares of the outside
// diameter, from the class's cited tyres (pooled when it has none): what a car
// given only a wheel diameter is fitted with.
func tyreShares(class string) (widthShare, rimShare float64) {
	collect := func(cars []referenceCar) (w, r []float64) {
		for _, c := range cars {
			if c.TyreFront == nil {
				continue
			}
			tw, d, rim, ok := tyreSize(c.TyreFront.Size)
			if ok && d > 0 {
				w, r = append(w, tw/d), append(r, rim/d)
			}
		}
		return
	}
	w, r := collect(carProportions.Classes[class].Cars)
	if len(w) == 0 {
		var all []referenceCar
		for _, name := range CarClasses() {
			all = append(all, carProportions.Classes[name].Cars...)
		}
		w, r = collect(all)
	}
	mean := func(xs []float64) float64 {
		s := 0.0
		for _, x := range xs {
			s += x
		}
		return s / float64(len(xs))
	}
	return mean(w), mean(r)
}

// overhangShare is the front overhang's share of both overhangs over the class's
// cars that state both (pooled when none does).
func overhangShare(class string) float64 {
	share := func(cars []referenceCar) (float64, bool) {
		s, n := 0.0, 0
		for _, c := range cars {
			if f, r, ok := both(c.FrontOverhang, c.RearOverhang); ok {
				s, n = s+f/(f+r), n+1
			}
		}
		if n == 0 {
			return 0, false
		}
		return s / float64(n), true
	}
	if v, ok := share(carProportions.Classes[class].Cars); ok {
		return v
	}
	var all []referenceCar
	for _, name := range CarClasses() {
		all = append(all, carProportions.Classes[name].Cars...)
	}
	v, _ := share(all)
	return v
}

// proportionWarnings checks a car against its class's ranges. Only warnings: a
// design outside a range is told so and built anyway (decision, 2026-09-18).
// mmPerUnit converts the document's lengths to millimetres; zero means the unit is
// not known, and the one absolute rule (ride height) is then not checked.
func proportionWarnings(label, class string, c carSpec, mmPerUnit float64) []Problem {
	var out []Problem
	warn := func(format string, args ...any) {
		out = append(out, Problem{Severity: Warning, Name: label, Detail: fmt.Sprintf(format, args...)})
	}
	if class == "" {
		warn("names no \"class\", so its proportions were not checked against any class (%s); its "+
			"unstated dimensions were taken from every class's cars together", strings.Join(CarClasses(), ", "))
		return out
	}
	if _, known := carProportions.Classes[class]; !known {
		warn("names the class %q, which the proportion table does not have (%s), so its proportions "+
			"were not checked", class, strings.Join(CarClasses(), ", "))
		return out
	}
	values := map[string]float64{
		"wheelbase/length":      c.Wheelbase / c.Length,
		"height/length":         c.Height / c.Length,
		"wheel diameter/height": c.WheelDiameter / c.Height,
		"track/width":           (c.TrackFront + c.TrackRear) / 2 / c.Width,
		"front overhang/length": c.FrontOverhang / c.Length,
		"rear overhang/length":  c.rearOverhang() / c.Length,
	}
	if mmPerUnit > 0 {
		values["ride height"] = c.RideHeight * mmPerUnit
	}
	for _, rule := range proportionRules {
		v, has := values[rule.Key]
		if !has {
			continue
		}
		r, ok := proportionRangeFor(class, rule)
		if !ok {
			continue
		}
		if v >= r.Low && v <= r.High {
			continue
		}
		format := func(x float64) string {
			if rule.MM {
				return fmt.Sprintf("%.0f mm", x)
			}
			return fmt.Sprintf("%.3f", x)
		}
		warn("has a %s of %s, outside %s to %s for a %s — the span of %s, widened by %.0f%%. "+
			"The table is UNVALIDATED, so this is a prompt to look, not a fault; it was built as given",
			rule.What, format(v), format(r.Low), format(r.High), class,
			strings.Join(r.From, " and "), carProportions.Tolerance*100)
	}
	return out
}
