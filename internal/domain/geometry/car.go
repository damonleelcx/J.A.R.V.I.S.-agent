package geometry

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// Car: a whole road car, said in the numbers a car is specified by.
//
// Stage C1 of the looks-designed work (damon, 2026-09-18: "looks designed" is a
// FORGE goal). Built the way the gear is (gear.go): the model gives a part whose
// shape is "car" and whose size holds the car's numbers; FORGE builds the car from
// them, deterministically. The 2026-09-12 live car used zero lofts, sweeps,
// fillets or cuts although the prompt asked for them; the gear went ten in ten the
// moment the model stopped deriving geometry and only gave numbers.
//
// # Why it is written out at the door and not at read time
//
// A gear is written out as the extrusion it is by every reader, and the browser
// holds a copy of that drawing (gearOutline in forge3d.js). A car is a tree of
// dozens of parts with a loft, cuts, fuses and a fillet; a second copy of that in
// JavaScript would be a second car. So the car is written out ONCE, where every
// document a turn produces is settled (settleDocument in internal/agent, the door
// every producer — a reply, an edit, a repair, a build pass — goes through), into
// the vocabulary everything already reads: parameters, definitions, assemblies,
// patterns and assembly features. The kernel, interference, mass, the viewport and
// STEP export then see an ordinary tree and change not at all.
//
// # Why every number is bound to a parameter
//
// What the gear gets by being re-drawn on every read — change its tooth count and
// nothing else needs rewriting — the car gets by binding. Every station
// coordinate, every size and every placement is an EXPRESSION over the car's own
// parameters ("car_wheelbase / 2"), so a respec of one parameter moves the wheels,
// the wheel arches and the body stations together, through the binder that
// already exists (binding.go). Counts are the exception: a pattern's count is a
// literal (pattern.go), so lug_count and diffuser_fins are written as numbers.
//
// # What it deliberately does not do
//
// Stations are polygons with rounded roof corners and flanks bowed as exact circular
// arcs (a "via" on the outline, looks/spline-outlines), flaring over the wheels; see
// stationCorner and carFlankBulge. They are not splines. A fillet FEATURE on the lofted body was
// measured failing in OCCT (build123d 0.11.1) at 5 mm and at 15 mm, before and
// after the arches were cut, on this body (the 30 mm attempt did not finish within
// the spike's 900 s; docs/spikes/2026-09-18-car-template) — so the body's edges are
// rounded in its SECTIONS
// (a corner radius on every station corner), which lofts into rounded edges and
// never asks the kernel to fillet a lofted B-spline. The fillet feature is used
// where it holds: the splitter's edges.

// carShape is the word, spelled once.
const carShape = "car"

func isCar(p Part) bool { return strings.EqualFold(strings.TrimSpace(p.Shape), carShape) }

// carKind says how a key is read.
type carKind int

const (
	carLength carKind = iota // a length in the document's units
	carRatio                 // a share of something, unitless
	carCount                 // a whole number, written as a pattern's literal count
)

// carKey is one number a car is specified by.
type carKey struct {
	Key  string
	Kind carKind
	// Means is what the prompt says the key is.
	Means string
}

// carKeys is the whole contract, in the order the prompt lists it. The four
// overall dimensions are required: everything else follows from them, so a car
// FORGE sized itself would be a different car, not a guess about one number
// (the gear's rule for its module).
var carKeys = []carKey{
	{"length", carLength, "overall length, nose to tail (required)"},
	{"width", carLength, "overall body width, no mirrors (required)"},
	{"height", carLength, "overall height, ground to roof (required)"},
	{"wheelbase", carLength, "front axle to rear axle (required)"},
	{"track_front", carLength, "front wheel centre to wheel centre, across the car"},
	{"track_rear", carLength, "rear wheel centre to wheel centre"},
	{"wheel_diameter", carLength, "tyre outside diameter"},
	{"tyre_width", carLength, "tyre section width"},
	{"rim_diameter", carLength, "rim (wheel) diameter"},
	{"ride_height", carLength, "ground to the body's floor"},
	{"front_overhang", carLength, "front axle to the nose; the rear overhang is what is left of the length"},
	{"arch_clearance", carLength, "gap between the tyre and its wheel arch"},
	{"cabin_start", carRatio, "where the windscreen begins, as a share of length from the nose"},
	{"cabin_length", carRatio, "the cabin's length as a share of length"},
	{"nose_height", carRatio, "the nose's height as a share of height"},
	{"belt_height", carRatio, "the beltline (base of the side windows) as a share of height"},
	{"tail_height", carRatio, "the tail's height as a share of height"},
	{"roof_width", carRatio, "the roof's width as a share of the body's width"},
	{"edge_radius", carRatio, "how round the body's edges are, as a share of height"},
	{"lug_count", carCount, "wheel nuts on each wheel"},
	{"diffuser_fins", carCount, "fins under the tail"},
}

// carStyleDefaults are the style knobs' values when the model gives none. FORGE's
// own choices, and said to be: they shape the body, not its proportions, and no
// published figure states a nose height as a share of height.
var carStyleDefaults = map[string]float64{
	"cabin_start": 0.33, "cabin_length": 0.42, "nose_height": 0.42, "belt_height": 0.62,
	"tail_height": 0.78, "roof_width": 0.74, "edge_radius": 0.025, "lug_count": 5, "diffuser_fins": 4,
}

// carSpec is a car's numbers, every one read and checked.
type carSpec struct {
	Length, Width, Height, Wheelbase      float64
	TrackFront, TrackRear                 float64
	WheelDiameter, TyreWidth, RimDiameter float64
	RideHeight, FrontOverhang             float64
	ArchClearance                         float64
	CabinStart, CabinLength               float64
	NoseHeight, BeltHeight, TailHeight    float64
	RoofWidth, EdgeRadius                 float64
	LugCount, DiffuserFins                int
}

func (c carSpec) rearOverhang() float64 { return c.Length - c.Wheelbase - c.FrontOverhang }

// field is where a key's number lives: the one place a key name meets a field.
func (c *carSpec) field(key string) *float64 {
	switch key {
	case "length":
		return &c.Length
	case "width":
		return &c.Width
	case "height":
		return &c.Height
	case "wheelbase":
		return &c.Wheelbase
	case "track_front":
		return &c.TrackFront
	case "track_rear":
		return &c.TrackRear
	case "wheel_diameter":
		return &c.WheelDiameter
	case "tyre_width":
		return &c.TyreWidth
	case "rim_diameter":
		return &c.RimDiameter
	case "ride_height":
		return &c.RideHeight
	case "front_overhang":
		return &c.FrontOverhang
	case "arch_clearance":
		return &c.ArchClearance
	case "cabin_start":
		return &c.CabinStart
	case "cabin_length":
		return &c.CabinLength
	case "nose_height":
		return &c.NoseHeight
	case "belt_height":
		return &c.BeltHeight
	case "tail_height":
		return &c.TailHeight
	case "roof_width":
		return &c.RoofWidth
	case "edge_radius":
		return &c.EdgeRadius
	}
	return nil
}

// carReading is what readCar found: the numbers, which of them the model gave
// (rather than FORGE chose), the expressions it bound any of them to, and the
// sentences owed to the reader about the rest.
type carReading struct {
	Spec        carSpec
	Given       map[string]bool
	From        map[string]string
	Assumptions []string
}

// readCar reads a car's numbers and says what is wrong with them. An Error means
// FORGE cannot build this car and leaves the part as it is, where Faults hands it
// to the repair loop; a Warning is owed to the reader and the car is built.
// lookup evaluates a size_from expression; mmPerUnit is zero when the document's
// unit is not one FORGE can convert.
func readCar(p Part, lookup func(string) (float64, bool), mmPerUnit float64) (carReading, []Problem) {
	label := p.Label()
	var problems []Problem
	fail := func(format string, args ...any) {
		problems = append(problems, Problem{Severity: Error, Name: label, Detail: fmt.Sprintf(format, args...)})
	}
	note := func(format string, args ...any) {
		problems = append(problems, Problem{Severity: Warning, Name: label, Detail: fmt.Sprintf(format, args...)})
	}
	finite := func(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
	r := carReading{Given: map[string]bool{}, From: map[string]string{}}
	class := strings.ToLower(strings.TrimSpace(p.Class))

	known := map[string]bool{"track": true}
	for _, k := range carKeys {
		known[k.Key] = true
	}
	var unknown []string
	for k := range p.Size {
		if !known[k] {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	if len(unknown) > 0 {
		note("gives %s, which a car does not read; the keys are %s", strings.Join(unknown, ", "), carKeyList())
	}

	got := map[string]float64{}
	for _, k := range append(carKeys, carKey{Key: "track"}) {
		if expr := strings.TrimSpace(p.SizeFrom[k.Key]); expr != "" {
			node, err := parseExpression(expr)
			if err != nil {
				fail("binds %s to %q, which cannot be read: %v", k.Key, expr, err)
				continue
			}
			v, err := node.Eval(lookup)
			if err != nil {
				fail("binds %s to %q, which does not evaluate: %v", k.Key, expr, err)
				continue
			}
			got[k.Key], r.From[k.Key] = v, expr
			continue
		}
		if v, ok := p.Size[k.Key]; ok {
			got[k.Key] = v
		}
	}
	if v, ok := got["track"]; ok {
		for _, side := range []string{"track_front", "track_rear"} {
			if _, has := got[side]; !has {
				got[side] = v
				if e := r.From["track"]; e != "" {
					r.From[side] = e
				}
			}
		}
	}
	for _, key := range []string{"length", "width", "height", "wheelbase"} {
		if _, ok := got[key]; !ok {
			fail("is a car with no %q; a car's other numbers follow from its length, width, height and "+
				"wheelbase, so FORGE will not choose them", key)
		}
	}
	for key, v := range got {
		if !finite(v) || v <= 0 {
			fail("has %s %g; it must be a positive number", key, v)
		}
	}
	if len(problems) > 0 && anyError(problems) {
		return r, problems
	}
	for key := range got {
		if key != "track" {
			r.Given[key] = true
		}
	}

	c := &r.Spec
	for _, k := range carKeys {
		if k.Kind == carCount {
			continue
		}
		if v, ok := got[k.Key]; ok {
			*c.field(k.Key) = v
		}
	}
	chose := func(key string, v float64, why string) {
		*c.field(key) = v
		r.Assumptions = append(r.Assumptions, fmt.Sprintf("%s: %s %s — %s.", label, key, strconv.FormatFloat(round(v, 3), 'f', -1, 64), why))
	}
	fromClass := func(pooled bool) string {
		if class == "" || pooled {
			return "the midpoint of every class's cited cars in the UNVALIDATED proportion table"
		}
		return "the midpoint of the " + class + " cars in the UNVALIDATED proportion table"
	}
	// Proportions FORGE chose, from the table's cars (carproportions.go).
	if !r.Given["track_front"] || !r.Given["track_rear"] {
		share, pooled := defaultFrom(class, ruleNamed("track/width"))
		for _, side := range []string{"track_front", "track_rear"} {
			if !r.Given[side] {
				chose(side, share*c.Width, "FORGE chose it as a share of width, "+fromClass(pooled))
			}
		}
	}
	if !r.Given["wheel_diameter"] {
		share, pooled := defaultFrom(class, ruleNamed("wheel diameter/height"))
		chose("wheel_diameter", share*c.Height, "FORGE chose it as a share of height, "+fromClass(pooled))
	}
	widthShare, rimShare := tyreShares(class)
	if !r.Given["tyre_width"] {
		chose("tyre_width", widthShare*c.WheelDiameter, "FORGE chose it from the cited cars' tyre sizes")
	}
	if !r.Given["rim_diameter"] {
		chose("rim_diameter", rimShare*c.WheelDiameter, "FORGE chose it from the cited cars' tyre sizes")
	}
	if !r.Given["ride_height"] {
		mm, pooled := defaultFrom(class, ruleNamed("ride height"))
		if mmPerUnit > 0 {
			chose("ride_height", mm/mmPerUnit, "FORGE chose it, "+fromClass(pooled))
		} else {
			chose("ride_height", 0.1*c.Height, "FORGE chose a tenth of the height, because the document's "+
				"unit is not one the proportion table's millimetres convert to")
		}
	}
	if !r.Given["front_overhang"] {
		chose("front_overhang", overhangShare(class)*(c.Length-c.Wheelbase),
			"FORGE split the overhangs in the cited cars' front-to-rear share")
	}
	if !r.Given["arch_clearance"] {
		chose("arch_clearance", 0.06*c.WheelDiameter, "FORGE chose 6% of the wheel diameter")
	}
	for _, k := range carKeys {
		if k.Kind == carLength || r.Given[k.Key] {
			continue
		}
		v := carStyleDefaults[k.Key]
		if k.Kind == carCount {
			r.Assumptions = append(r.Assumptions, fmt.Sprintf("%s: %s %g — FORGE's own default.", label, k.Key, v))
		} else {
			chose(k.Key, v, "FORGE's own default style")
		}
	}
	count := func(key string, lo, hi int) int {
		v, ok := got[key]
		if !ok {
			return int(carStyleDefaults[key])
		}
		if math.Abs(v-math.Round(v)) > 1e-9 || v < float64(lo) || v > float64(hi) {
			fail("has %s %g; it is a whole number from %d to %d", key, v, lo, hi)
			return 0
		}
		if r.From[key] != "" {
			note("binds %s to %q; a count is written into a pattern as a number, so it will not follow "+
				"that expression later", key, r.From[key])
		}
		return int(math.Round(v))
	}
	c.LugCount = count("lug_count", 3, 10)
	c.DiffuserFins = count("diffuser_fins", 2, 9)

	checkCarGeometry(*c, fail)
	if anyError(problems) {
		return r, problems
	}
	problems = append(problems, proportionWarnings(label, class, *c, mmPerUnit)...)
	return r, problems
}

// checkCarGeometry refuses the cars this template cannot build, each named by the
// number that makes it impossible — never by what the kernel would say about a
// solid the model never described.
func checkCarGeometry(c carSpec, fail func(string, ...any)) {
	within := func(key string, v, lo, hi float64) bool {
		if v < lo || v > hi {
			fail("has %s %g; this template builds it between %g and %g", key, v, lo, hi)
			return false
		}
		return true
	}
	ok := within("cabin_start", c.CabinStart, 0.15, 0.55)
	ok = within("cabin_length", c.CabinLength, 0.2, 0.6) && ok
	ok = within("nose_height", c.NoseHeight, 0.15, 0.95) && ok
	ok = within("belt_height", c.BeltHeight, 0.3, 0.9) && ok
	ok = within("tail_height", c.TailHeight, 0.3, 1) && ok
	ok = within("roof_width", c.RoofWidth, 0.3, 0.95) && ok
	ok = within("edge_radius", c.EdgeRadius, 0, 0.08) && ok
	if !ok {
		return
	}
	archRadius := c.WheelDiameter/2 + c.ArchClearance
	switch {
	case c.Wheelbase >= c.Length:
		fail("has a wheelbase of %g and a length of %g; the wheels have to fit inside the car", c.Wheelbase, c.Length)
	case c.CabinStart+c.CabinLength > 0.88 || c.CabinStart < 0.12:
		fail("has a cabin from %g to %g of its length; the template puts stations at 0.1 and 0.92 of "+
			"the length, so the cabin must lie between them", c.CabinStart, c.CabinStart+c.CabinLength)
	case c.FrontOverhang <= archRadius:
		fail("has a front overhang of %g, no more than its wheel arch's radius (%g = wheel_diameter / 2 + "+
			"arch_clearance), so the arch would cut the nose off", c.FrontOverhang, archRadius)
	case c.rearOverhang() <= archRadius:
		fail("has a rear overhang of %g (length - wheelbase - front_overhang), no more than its wheel "+
			"arch's radius (%g), so the arch would cut the tail off", c.rearOverhang(), archRadius)
	case c.RimDiameter >= c.WheelDiameter*0.95:
		fail("has a rim of %g inside a tyre of %g; a tyre needs a sidewall", c.RimDiameter, c.WheelDiameter)
	case math.Min(c.TrackFront, c.TrackRear)/2-c.TyreWidth/2-c.ArchClearance <= 0:
		fail("has tyres %g wide on a track of %g; the wheel arches would meet in the middle of the car",
			c.TyreWidth, math.Min(c.TrackFront, c.TrackRear))
	case c.TyreWidth >= 0.5*c.TrackRear-0.01*c.Width:
		fail("has tyres %g wide on a rear track of %g; the diffuser's fins sit inside half the rear "+
			"track and would run into the tyres", c.TyreWidth, c.TrackRear)
	case c.RideHeight >= c.NoseHeight*c.Height*0.8:
		fail("has a ride height of %g against a nose %g high; the body would have no nose", c.RideHeight,
			c.NoseHeight*c.Height)
	case c.BeltHeight*c.Height <= c.RideHeight*1.2:
		fail("has a beltline (%g) at its ride height (%g); the body would have no sides",
			c.BeltHeight*c.Height, c.RideHeight)
	}
}

// carKeyList is the keys a car reads, for a sentence.
func carKeyList() string {
	names := make([]string, 0, len(carKeys))
	for _, k := range carKeys {
		names = append(names, k.Key)
	}
	return strings.Join(names, ", ")
}

// ex is a number and the expression that computes it from the car's parameters:
// the value checks the geometry now, the text binds it for later.
type ex struct {
	v float64
	s string
}

func lit(v float64) ex { return ex{v, strconv.FormatFloat(v, 'g', -1, 64)} }

func (a ex) plus(b ex) ex  { return ex{a.v + b.v, "(" + a.s + " + " + b.s + ")"} }
func (a ex) minus(b ex) ex { return ex{a.v - b.v, "(" + a.s + " - " + b.s + ")"} }
func (a ex) times(b ex) ex { return ex{a.v * b.v, wrap(a.s) + " * " + wrap(b.s)} }
func (a ex) scale(k float64) ex {
	return ex{k * a.v, strconv.FormatFloat(k, 'g', -1, 64) + " * " + wrap(a.s)}
}
func (a ex) neg() ex   { return ex{-a.v, "-" + wrap(a.s)} }
func exMin(a, b ex) ex { return ex{math.Min(a.v, b.v), "min(" + a.s + ", " + b.s + ")"} }
func exMax(a, b ex) ex { return ex{math.Max(a.v, b.v), "max(" + a.s + ", " + b.s + ")"} }
func (a ex) over(k float64) ex {
	return ex{a.v / k, wrap(a.s) + " / " + strconv.FormatFloat(k, 'g', -1, 64)}
}

// wrap parenthesises an expression unless it is one name or number.
func wrap(s string) string {
	for _, r := range s {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '.') {
			if strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") && balanced(s[1:len(s)-1]) {
				return s
			}
			return "(" + s + ")"
		}
	}
	return s
}

func balanced(s string) bool {
	depth := 0
	for _, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return false
			}
		}
	}
	return depth == 0
}

// stationCorner is one corner of a station's section, and the edge that ARRIVES
// at it from the corner before.
//
// # Bulged stations (looks integration, 2026-09-19)
//
// Bulge is how far that arriving edge bows OUTWARD, as a share of its chord: the
// edge becomes the exact circular arc through its two ends and a through-point (a
// "via", the outline vocabulary of looks/spline-outlines) that sits Bulge chords out
// from the chord's middle, square to it. The outline runs counter-clockwise, so
// "outward" is the chord turned a quarter clockwise. The via is written as an
// expression over the same parameters as the corners, so a respec moves it with
// them; Bulge itself is a number, like a station's WidthFrac. An arc, not a spline,
// because an arc is exact in OCCT, in STEP, in Go's measurement and in the browser
// (curve_guide.go says why).
//
// Where an arc meets a corner the corner is left sharp — the outline vocabulary
// ignores a radius there and reports it — so carStationProfile writes no radius on
// either end of a bowed edge rather than one that would be ignored.
type stationCorner struct {
	X, Y  ex
	Bulge float64
}

// carStations is how many sections the body is lofted through.
const carStations = 17

// carStationShape is one station's share of the side view, at a share of the
// length from the nose: its deck (the top it would have outside the cabin, as a
// share of height), how much of the cabin it is in, and its width as a share of the
// car's. Every piece is a smoothstep, so the side view the stations sample is C1
// and the loft through them does not overshoot it.
type carStationShape struct {
	Deck, Cabin ex
	WidthFrac   float64
}

// smoothstep is 0 below u = 0, 1 above u = 1, and 3u² - 2u³ between: a ramp with
// no corner at either end.
func smoothstep(u ex) ex {
	c := exMin(exMax(u, lit(0)), lit(1))
	return ex{c.v * c.v * (3 - 2*c.v), wrap(c.s) + " * " + wrap(c.s) + " * (3 - 2 * " + wrap(c.s) + ")"}
}

func smoothstepNum(u float64) float64 {
	c := math.Min(math.Max(u, 0), 1)
	return c * c * (3 - 2*c)
}

// sideView samples the side view at a share of the length from the nose.
//
// The deck climbs from the nose to the beltline over the bonnet, to the tail (or
// stays at the beltline, whichever is higher) over the cabin, and falls a little to
// the tail's end; the cabin rises over the first 30% of its length (the
// windscreen) and falls over the last 40% (the rear window). The width narrows to
// 80% at the nose and 90% at the tail.
func sideView(at float64, cs, cl, nose, belt, tail ex) carStationShape {
	T := lit(at)
	high := exMax(belt, tail)
	deck := nose.plus(belt.minus(nose).times(smoothstep(ex{at / cs.v, wrap(T.s) + " / " + cs.s}))).
		plus(high.minus(belt).times(smoothstep(ex{(at - cs.v) / cl.v, "(" + T.s + " - " + cs.s + ") / " + cl.s}))).
		plus(tail.scale(0.94).minus(high).times(smoothstep(ex{(at - cs.v - cl.v) / (1 - cs.v - cl.v),
			"(" + T.s + " - " + cs.s + " - " + cl.s + ") / (1 - " + cs.s + " - " + cl.s + ")"})))
	in := smoothstep(ex{(at - cs.v) / (0.3 * cl.v), "(" + T.s + " - " + cs.s + ") / (0.3 * " + cl.s + ")"})
	out := smoothstep(ex{(cs.v + cl.v - at) / (0.4 * cl.v), "(" + cs.s + " + " + cl.s + " - " + T.s + ") / (0.4 * " + cl.s + ")"})
	width := 0.8 + 0.2*smoothstepNum(at/0.2) - 0.1*smoothstepNum((at-0.8)/0.2)
	return carStationShape{Deck: deck, Cabin: in.times(out), WidthFrac: width}
}

// carFenderFlare is how far past its station's half width a flank reaches at the
// middle of a wheel, as a share of that half width; it falls off as a Gaussian
// carFenderReach of the car's length either side of each axle. FORGE's own numbers
// (not from the cited cars): enough to read as a fender, and never past the width
// the car was asked to be.
const (
	carFenderFlare = 0.025
	carFenderReach = 0.07
)

// carFlankBulge is the Bulge (share of chord) that bows the flank from (x0, y0) to
// (x1, y1) — a chord running up the car's right side — so that the ARC's outermost
// point reaches x = target. Found by bisection on the arc itself, sampled between its
// ends through its via: a tilted chord's arc reaches past its via, and the circle's
// own outermost point may lie off the arc. Rounded to 1e-4, so the expression it is
// written into reads as a plain decimal.
func carFlankBulge(x0, y0, x1, y1, target float64) float64 {
	lo, hi := 0.0, 1.0
	if arcReachX(x0, y0, x1, y1, hi) < target {
		return hi
	}
	for i := 0; i < 50; i++ {
		mid := (lo + hi) / 2
		if arcReachX(x0, y0, x1, y1, mid) < target {
			lo = mid
		} else {
			hi = mid
		}
	}
	return math.Round(hi*1e4) / 1e4
}

// arcReachX is the largest x on the arc from (x0, y0) to (x1, y1) bowed by b chords
// to the right of its direction of travel.
func arcReachX(x0, y0, x1, y1, b float64) float64 {
	dx, dy := x1-x0, y1-y0
	return arcThroughReachX(x0, y0, (x0+x1)/2+b*dy, (y0+y1)/2-b*dx, x1, y1)
}

// arcThroughReachX is the largest x on the arc from (x0, y0) through (mx, my) to
// (x1, y1): what a station's bowed flank reaches, read off its outline.
func arcThroughReachX(x0, y0, mx, my, x1, y1 float64) float64 {
	cx, cy, r, ok := circleThrough(x0, y0, mx, my, x1, y1)
	if !ok {
		return math.Max(x0, x1)
	}
	a0, am, a1 := math.Atan2(y0-cy, x0-cx), math.Atan2(my-cy, mx-cx), math.Atan2(y1-cy, x1-cx)
	// The sweep from a0 to a1 that passes through am.
	norm := func(a float64) float64 {
		for a < 0 {
			a += 2 * math.Pi
		}
		for a >= 2*math.Pi {
			a -= 2 * math.Pi
		}
		return a
	}
	sweep, toMid := norm(a1-a0), norm(am-a0)
	if toMid > sweep {
		sweep -= 2 * math.Pi
	}
	best := math.Max(x0, x1)
	for i := 0; i <= 256; i++ {
		best = math.Max(best, cx+r*math.Cos(a0+sweep*float64(i)/256))
	}
	return best
}

// circleThrough is the circle through three points, or ok=false when they are in a
// line.
func circleThrough(ax, ay, bx, by, cx, cy float64) (x, y, r float64, ok bool) {
	d := 2 * (ax*(by-cy) + bx*(cy-ay) + cx*(ay-by))
	if math.Abs(d) < 1e-12 {
		return 0, 0, 0, false
	}
	a2, b2, c2 := ax*ax+ay*ay, bx*bx+by*by, cx*cx+cy*cy
	x = (a2*(by-cy) + b2*(cy-ay) + c2*(ay-by)) / d
	y = (a2*(cx-bx) + b2*(ax-cx) + c2*(bx-ax)) / d
	return x, y, math.Hypot(ax-x, ay-y), true
}

// carStationProfile writes a station's corners as the outline a "section" part
// carries, every coordinate and corner radius bound to its expression, and every
// bulged edge as the via that bows it (see stationCorner).
func carStationProfile(corners []stationCorner, radius ex) ([]Point, error) {
	n := len(corners)
	out := make([]Point, 0, n)
	for _, c := range corners {
		out = append(out, Point{X: c.X.v, Y: c.Y.v, XFrom: c.X.s, YFrom: c.Y.s,
			Radius: radius.v, RadiusFrom: radius.s})
	}
	for i, c := range corners {
		if c.Bulge == 0 {
			continue
		}
		if c.Bulge < 0 || n < 3 {
			return nil, fmt.Errorf("station corner %d has a bulge of %g; a flank bows outward, by a "+
				"positive share of its chord", i+1, c.Bulge)
		}
		prev := (i + n - 1) % n
		a := corners[prev]
		// The middle of the chord, then Bulge chords out: the chord (dx, dy) turned a
		// quarter clockwise is (dy, -dx), outward for a counter-clockwise outline.
		dx, dy := c.X.minus(a.X), c.Y.minus(a.Y)
		k := ex{c.Bulge, strconv.FormatFloat(c.Bulge, 'f', -1, 64)}
		vx := a.X.plus(c.X).over(2).plus(dy.times(k))
		vy := a.Y.plus(c.Y).over(2).minus(dx.times(k))
		out[i].Via = &Point{X: vx.v, Y: vy.v, XFrom: vx.s, YFrom: vy.s}
		for _, k := range []int{i, prev} {
			out[k].Radius, out[k].RadiusFrom = 0, ""
		}
	}
	return out, nil
}

// carSectionProblems holds a car's station outlines to every outline rule. A
// variable only so a test can stand in a section the rules refuse: no car inside the
// template's ranges produces one any more (TestCar_AnEdgeRadiusTheSectionsCannotCarry-
// IsRefusedByName says both).
var carSectionProblems = func(sections Document) []Problem { return sections.ProfileProblems() }

// carTree is a car written out: what it adds to the document.
type carTree struct {
	Parameters  []Parameter
	Derived     []Derived
	Definitions []Part
	Assemblies  []Assembly
	// Car is the assembly that is the whole car, in its own frame: ground at y = 0,
	// the middle of the wheelbase at x = 0, the nose toward +x, the centreline at
	// z = 0.
	Car string
}

// paramPrefix is the car's parameter namespace: its id as a name an expression can
// refer to, and a trailing underscore.
func paramPrefix(id string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(id) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	s := strings.Trim(b.String(), "_")
	if s == "" || !unicode.IsLetter(rune(s[0])) {
		s = "car_" + s
	}
	return strings.TrimSuffix(s, "_") + "_"
}

// buildCar writes a checked car out as a tree. unit is the document's unit string,
// written on every length parameter.
func buildCar(p Part, r carReading, unit string) (carTree, error) {
	c := r.Spec
	pre := paramPrefix(p.ID)
	base := p.ID
	var t carTree
	for _, k := range carKeys {
		if k.Kind == carCount {
			continue
		}
		name := pre + k.Key
		if expr := r.From[k.Key]; expr != "" {
			t.Derived = append(t.Derived, Derived{Name: name, Expression: expr,
				Why: "the car's " + k.Key + ", as its part was bound"})
			continue
		}
		u := unit
		if k.Kind == carRatio {
			u = ""
		}
		t.Parameters = append(t.Parameters, Parameter{Name: name, Value: *c.field(k.Key), Unit: u, How: Chosen})
	}
	P := func(key string) ex { return ex{*c.field(key), pre + key} }
	t.Derived = append(t.Derived, Derived{Name: pre + "rear_overhang",
		Expression: pre + "length - " + pre + "wheelbase - " + pre + "front_overhang",
		Why:        "the length the wheelbase and the front overhang leave behind the rear axle"})

	L, W, H := P("length"), P("width"), P("height")
	WB, FO, RH := P("wheelbase"), P("front_overhang"), P("ride_height")
	D, TW, RD := P("wheel_diameter"), P("tyre_width"), P("rim_diameter")
	TF, TR, AC := P("track_front"), P("track_rear"), P("arch_clearance")
	cs, cl := P("cabin_start"), P("cabin_length")
	nose, belt, tail := P("nose_height"), P("belt_height"), P("tail_height")
	RO := L.minus(WB).minus(FO)
	front := WB.over(2).plus(FO) // the nose's x
	rear := WB.over(2).plus(RO).neg()
	archR := D.over(2).plus(AC)
	radius := P("edge_radius").times(H)
	// The axles as shares of the length from the nose, for the fender flare.
	axles := []float64{FO.v / L.v, (FO.v + WB.v) / L.v}

	id := func(suffix string) string { return base + "-" + suffix }
	pos := func(x, y, z ex) ([]float64, map[string]string) {
		return []float64{x.v, y.v, z.v}, map[string]string{"x": x.s, "y": y.s, "z": z.s}
	}
	zero := lit(0)
	const (
		bodyColour = "#2f4f6f"
		tyreColour = "#1b1b1b"
		rimColour  = "#c9ccd1"
		trimColour = "#202326"
		lugColour  = "#8c9096"
	)

	// The body: carStations sections lofted nose to tail, then its four arches cut.
	//
	// Each station is SAMPLED from one smooth side view (sideView), not placed
	// at the cabin's corners. Measured on the fixtures (docs/spikes/2026-09-18-car-
	// template): eight stations at the windscreen, roof and tail made OCCT's smooth
	// loft overshoot the roof by 13-61%, because a spline through a step bulges past
	// it; seventeen samples of a C1 profile stay within 1.2%.
	// Each station's numbers are derived values named for it, so a coordinate
	// reads "gt_station_5_top" rather than a paragraph of arithmetic.
	derive := func(name string, e ex, why string) ex {
		t.Derived = append(t.Derived, Derived{Name: pre + name, Expression: e.s, Why: why})
		return ex{e.v, pre + name}
	}
	body := Assembly{ID: id("body"), Name: "Body"}
	var loftWith []string
	for i := 0; i < carStations; i++ {
		at := float64(i) / float64(carStations-1)
		shape := sideView(at, cs, cl, nose, belt, tail)
		st := fmt.Sprintf("station_%d_", i)
		cab := derive(st+"cabin", shape.Cabin, "how much of the cabin this station is in, 0 to 1")
		deck := derive(st+"deck", shape.Deck, "this station's top outside the cabin, as a share of height")
		top := derive(st+"top", deck.plus(lit(1).minus(deck).times(cab)).times(H),
			"the height of this station's top: the deck, rising to the roof in the cabin")
		hw := derive(st+"half_width", W.scale(shape.WidthFrac/2), "half this station's width")
		deckBelt := RH.plus(top.minus(RH).scale(0.95))
		yb := derive(st+"belt", deckBelt.plus(belt.times(H).minus(deckBelt).times(cab)),
			"the height of this station's shoulder: just under the deck, or the beltline in the cabin")
		rw := derive(st+"roof", hw.times(lit(0.6).plus(P("roof_width").minus(lit(0.6)).times(cab))),
			"half this station's top: a crowned deck, widening to the roof in the cabin")
		// Each flank is ONE exact arc from the sill to the shoulder (looks integration,
		// 2026-09-19). It replaces the corner the station had at its widest point, so
		// the sides are round rather than creased, and it bows out to
		// (1 - carFenderFlare) of the station's half width between the wheels and to
		// the whole half width over them: a fender, and never wider than the car was
		// asked to be (the arch cutters reach W/2 + arch_clearance, so the arches still
		// cut clean through it).
		flare := 0.0
		for _, ax := range axles {
			u := (at - ax) / carFenderReach
			flare = math.Max(flare, carFenderFlare*math.Exp(-u*u))
		}
		reach := hw.v * (1 - carFenderFlare + flare)
		bulge := carFlankBulge(hw.v*0.85, RH.v, hw.v*0.955, yb.v, reach)
		corners := []stationCorner{
			{X: hw.scale(0.85).neg(), Y: RH, Bulge: bulge}, {X: hw.scale(0.85), Y: RH},
			{X: hw.scale(0.955), Y: yb, Bulge: bulge},
			{X: rw, Y: top}, {X: rw.neg(), Y: top},
			{X: hw.scale(0.955).neg(), Y: yb},
		}
		profile, err := carStationProfile(corners, radius)
		if err != nil {
			return carTree{}, err
		}
		def := id(fmt.Sprintf("station-%d", i))
		t.Definitions = append(t.Definitions, Part{ID: def, Name: fmt.Sprintf("Body station %d", i),
			Shape: "section", Size: map[string]float64{}, Profile: profile,
			Position: []float64{0, 0, 0}, Rotation: []float64{0, 90, 0}, Color: bodyColour, Opacity: 1,
			Note: "a section of the body, blended into the others by the body's loft"})
		x := front.minus(L.scale(at))
		child := fmt.Sprintf("station-%d", i)
		body.Children = append(body.Children, Child{ID: child, Ref: def,
			Position: []float64{x.v, 0, 0}, PositionFrom: map[string]string{"x": x.s}})
		if i > 0 {
			loftWith = append(loftWith, child)
		}
	}
	// An arch reaches from just inboard of the tyre to past the body's side.
	for _, axle := range []struct {
		name  string
		x     ex
		track ex
	}{{"front", WB.over(2), TF}, {"rear", WB.over(2).neg(), TR}} {
		inner := axle.track.over(2).minus(TW.over(2)).minus(AC)
		outer := W.over(2).plus(AC)
		def := id("arch-" + axle.name)
		span := outer.minus(inner)
		t.Definitions = append(t.Definitions, Part{ID: def, Name: "Wheel arch cutter (" + axle.name + ")",
			Shape: "cylinder", Size: map[string]float64{"radius": archR.v, "height": span.v},
			SizeFrom: map[string]string{"radius": archR.s, "height": span.s},
			Position: []float64{0, 0, 0}, Rotation: []float64{90, 0, 0}, Color: bodyColour, Opacity: 1,
			Note: "the material the " + axle.name + " wheel arches remove from the body"})
		centre := inner.plus(outer).over(2)
		for _, side := range []struct {
			name string
			z    ex
		}{{"left", centre}, {"right", centre.neg()}} {
			position, from := pos(axle.x, D.over(2), side.z)
			body.Children = append(body.Children, Child{ID: "arch-" + axle.name + "-" + side.name, Ref: def,
				Position: position, PositionFrom: from})
		}
	}
	body.Features = []Feature{
		{ID: "loft", Op: "loft", Of: "station-0", With: loftWith,
			Note: "the body, a smooth surface through its stations nose to tail"},
		{ID: "wheel-arches", Op: "cut", Of: "station-0",
			With: []string{"arch-front-left", "arch-front-right", "arch-rear-left", "arch-rear-right"},
			Note: "the wheel arches"},
	}

	// A wheel, in its own frame: the axle along z, the outside face toward +z.
	wheel := Assembly{ID: id("wheel"), Name: "Wheel"}
	circle := func(r ex) []Point {
		return []Point{
			{X: r.v, Y: 0, XFrom: r.s, Via: &Point{X: 0, Y: -r.v, YFrom: r.neg().s}},
			{X: -r.v, Y: 0, XFrom: r.neg().s, Via: &Point{X: 0, Y: r.v, YFrom: r.s}},
		}
	}
	rimDepth := TW.scale(0.7)
	tyreHole := circle(RD.over(2))
	t.Definitions = append(t.Definitions,
		Part{ID: id("tyre"), Name: "Tyre", Shape: "extrusion", Profile: circle(D.over(2)), Holes: [][]Point{tyreHole},
			Size: map[string]float64{"depth": TW.v}, SizeFrom: map[string]string{"depth": TW.s},
			Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}, Color: tyreColour, Opacity: 1,
			Note: "the tyre, its bore the rim's diameter"},
		Part{ID: id("rim"), Name: "Rim", Shape: "extrusion", Profile: circle(RD.over(2)),
			Size: map[string]float64{"depth": rimDepth.v}, SizeFrom: map[string]string{"depth": rimDepth.s},
			Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}, Color: rimColour, Opacity: 1,
			Note: "the wheel's rim, inside the tyre"})
	lugR, lugH := RD.scale(0.045), TW.scale(0.1)
	t.Definitions = append(t.Definitions, Part{ID: id("lug"), Name: "Wheel nut", Shape: "cylinder",
		Size:     map[string]float64{"radius": lugR.v, "height": lugH.v},
		SizeFrom: map[string]string{"radius": lugR.s, "height": lugH.s},
		Position: []float64{0, 0, 0}, Rotation: []float64{90, 0, 0}, Color: lugColour, Opacity: 1,
		Note: "a wheel nut, standing on the rim's outer face"})
	lugPos, lugFrom := pos(RD.scale(0.22), zero, rimDepth.over(2).plus(lugH.over(2)))
	delete(lugFrom, "y")
	wheel.Children = []Child{
		{ID: "tyre", Ref: id("tyre")},
		{ID: "rim", Ref: id("rim")},
		{ID: "lug", Ref: id("lug"), Position: lugPos, PositionFrom: lugFrom,
			Pattern: &Pattern{Kind: "polar", Count: c.LugCount, About: "z",
				Note: "the wheel nuts, evenly round the rim"}},
	}
	// The nuts are fused to the rim: measured on the fixtures, the interference
	// check paid a boolean against the body for every nut, which alone put a car past
	// the kernel's 30 s build limit (docs/spikes/2026-09-18-car-template).
	wheel.Features = []Feature{{ID: "nuts", Op: "fuse", Of: "rim", With: []string{"lug"},
		Note: "the wheel nuts, one solid with the rim"}}

	// The car: the body, four wheels, a splitter under the nose and fins under the tail.
	car := Assembly{ID: id("car"), Name: p.Label()}
	car.Children = append(car.Children, Child{ID: "body", Ref: body.ID})
	for _, w := range []struct {
		name   string
		x      ex
		track  ex
		mirror string
	}{
		{"front-left", WB.over(2), TF, ""}, {"front-right", WB.over(2), TF, "z"},
		{"rear-left", WB.over(2).neg(), TR, ""}, {"rear-right", WB.over(2).neg(), TR, "z"},
	} {
		z := w.track.over(2)
		if w.mirror != "" {
			z = z.neg()
		}
		position, from := pos(w.x, D.over(2), z)
		car.Children = append(car.Children, Child{ID: w.name, Ref: wheel.ID, Position: position,
			PositionFrom: from, Mirror: w.mirror})
	}
	splitLen := FO.minus(archR).scale(0.5)
	splitT := exMin(RH.scale(0.3), H.scale(0.012))
	splitW := W.scale(0.6)
	t.Definitions = append(t.Definitions, Part{ID: id("splitter"), Name: "Front splitter", Shape: "box",
		Size:     map[string]float64{"width": splitLen.v, "height": splitT.v, "depth": splitW.v},
		SizeFrom: map[string]string{"width": splitLen.s, "height": splitT.s, "depth": splitW.s},
		Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}, Color: trimColour, Opacity: 1,
		Note: "a splitter under the nose, clear of the front wheels"})
	sp, sf := pos(front.minus(splitLen.over(2)), RH.minus(splitT.over(2)), zero)
	delete(sf, "z")
	car.Children = append(car.Children, Child{ID: "splitter", Ref: id("splitter"), Position: sp, PositionFrom: sf})

	finLen := RO.minus(archR).scale(0.5)
	finH := RH.scale(0.5)
	finT := W.scale(0.01)
	finSpan := TR.scale(0.5)
	fin := []Point{
		{X: rear.v, Y: RH.v, XFrom: rear.s, YFrom: RH.s},
		{X: rear.plus(finLen).v, Y: RH.v, XFrom: rear.plus(finLen).s, YFrom: RH.s},
		{X: rear.v, Y: RH.minus(finH).v, XFrom: rear.s, YFrom: RH.minus(finH).s},
	}
	t.Definitions = append(t.Definitions, Part{ID: id("diffuser-fin"), Name: "Diffuser fin", Shape: "extrusion",
		Profile: fin, Size: map[string]float64{"depth": finT.v}, SizeFrom: map[string]string{"depth": finT.s},
		Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}, Color: trimColour, Opacity: 1,
		Note: "a diffuser fin under the tail, between the rear wheels"})
	step := finSpan.over(float64(c.DiffuserFins - 1))
	fz := finSpan.over(2).neg()
	car.Children = append(car.Children, Child{ID: "diffuser-fin", Ref: id("diffuser-fin"),
		Position: []float64{0, 0, fz.v}, PositionFrom: map[string]string{"z": fz.s},
		Pattern: &Pattern{Kind: "linear", Count: c.DiffuserFins, Offset: []float64{0, 0, step.v},
			OffsetFrom: map[string]string{"z": step.s}, Note: "the diffuser's fins, evenly across the tail"}})
	car.Features = []Feature{{ID: "splitter-edges", Op: "fillet", Of: "splitter", Edges: "all",
		Radius: splitT.scale(0.3).v, RadiusFrom: splitT.scale(0.3).s, Note: "the splitter's edges, rounded"}}

	t.Assemblies = []Assembly{car, body, wheel}
	t.Car = car.ID
	return t, nil
}

// ExpandTemplates writes every "car" part in the document out as the tree it is,
// in place, and says what it could not build and what it noticed.
//
// Called by the agent's settleDocument, the door every document a turn produces
// goes through, BEFORE the document is bound: the tree it writes carries
// expressions, and binding is what turns them into the numbers every reader
// draws. Idempotent: what it writes contains no "car" part, so a document settled
// twice is written out once.
//
// A car it cannot build is LEFT as a "car" part and its errors returned; Faults
// reports that part (carFaults), which is what hands it to the repair loop, the
// bargain an unreadable gear has.
func ExpandTemplates(d *Document) []Problem {
	if d == nil {
		return nil
	}
	var problems []Problem
	res := d.Resolve()
	lookup := func(n string) (float64, bool) {
		v, ok := res.Values[n]
		return v.Number, ok
	}
	mmPerUnit := 0.0
	if u, known := ParseUnit(d.Units); known {
		mmPerUnit, _ = u.toMM()
	}
	taken := map[string]bool{}
	for _, p := range d.Definitions {
		taken[p.ID] = true
	}
	for _, a := range d.Assemblies {
		taken[a.ID] = true
	}
	names := map[string]bool{}
	for _, p := range d.Parameters {
		names[p.Name] = true
	}
	for _, v := range d.Derived {
		names[v.Name] = true
	}

	build := func(p Part) (carTree, bool) {
		r, found := readCar(p, lookup, mmPerUnit)
		problems = append(problems, found...)
		if anyError(found) {
			return carTree{}, false
		}
		tree, err := buildCar(p, r, strings.TrimSpace(d.Units))
		if err != nil {
			problems = append(problems, Problem{Severity: Error, Name: p.Label(), Detail: err.Error()})
			return carTree{}, false
		}
		// The sections' outlines are held to every outline rule before they are
		// written — a corner radius or a bowed flank that does not fit a station is
		// refused here, by the number that controls the rounding, not left as a body
		// missing from the model. Since the flanks became arcs (2026-09-19) no car
		// inside the template's ranges trips this (the roof corners are the only
		// rounded ones, and they turn little); it stays as the net under both.
		sections := Document{Units: d.Units, Parts: tree.Definitions,
			Parameters: append(append([]Parameter(nil), d.Parameters...), tree.Parameters...),
			Derived:    append(append([]Derived(nil), d.Derived...), tree.Derived...)}
		for _, sp := range carSectionProblems(sections) {
			if sp.Severity == Error {
				problems = append(problems, Problem{Severity: Error, Name: p.Label(), Detail: fmt.Sprintf(
					"has an edge_radius of %g (a share of its height), and its body sections cannot be drawn "+
						"with corners that round and flanks that bow: %s %s. Use a smaller edge_radius",
					r.Spec.EdgeRadius, sp.Name, sp.Detail)})
				return carTree{}, false
			}
		}
		for _, def := range tree.Definitions {
			if taken[def.ID] {
				problems = append(problems, Problem{Severity: Error, Name: p.Label(), Detail: fmt.Sprintf(
					"would write a definition %q, which this document already has; give the car another id", def.ID)})
				return carTree{}, false
			}
		}
		for _, a := range tree.Assemblies {
			if taken[a.ID] {
				problems = append(problems, Problem{Severity: Error, Name: p.Label(), Detail: fmt.Sprintf(
					"would write an assembly %q, which this document already has; give the car another id", a.ID)})
				return carTree{}, false
			}
		}
		for _, q := range tree.Parameters {
			if names[q.Name] {
				problems = append(problems, Problem{Severity: Error, Name: p.Label(), Detail: fmt.Sprintf(
					"would write a parameter %q, which this document already has; give the car another id", q.Name)})
				return carTree{}, false
			}
		}
		for _, q := range tree.Parameters {
			names[q.Name] = true
		}
		for _, q := range tree.Definitions {
			taken[q.ID] = true
		}
		for _, q := range tree.Assemblies {
			taken[q.ID] = true
		}
		d.Parameters = append(d.Parameters, tree.Parameters...)
		d.Derived = append(d.Derived, tree.Derived...)
		d.Definitions = append(d.Definitions, tree.Definitions...)
		d.Assemblies = append(d.Assemblies, tree.Assemblies...)
		d.Assumptions = append(d.Assumptions, r.Assumptions...)
		return tree, true
	}
	place := func(p Part, ref string) Child {
		c := Child{ID: p.ID, Ref: ref, Position: p.Position, Rotation: p.Rotation, PositionFrom: p.PositionFrom}
		if p.Mirrored {
			c.Mirror = "x"
		}
		return c
	}

	// Cars a tree places: the definition becomes an assembly of the same id, so
	// every child that places it still does.
	if hasCar(d.Definitions) {
		walked := append([]Part(nil), d.Definitions...)
		drop := map[string]bool{}
		for _, p := range walked {
			if !isCar(p) {
				continue
			}
			tree, ok := build(p)
			if !ok {
				continue
			}
			drop[p.ID] = true
			wrapper := place(p, tree.Car)
			wrapper.ID = "car"
			d.Assemblies = append(d.Assemblies, Assembly{ID: p.ID, Name: p.Label(), Children: []Child{wrapper}})
		}
		kept := d.Definitions[:0:0]
		for _, p := range d.Definitions {
			if !(drop[p.ID] && isCar(p)) {
				kept = append(kept, p)
			}
		}
		d.Definitions = kept
	}

	// Cars at the top level: placed by the root, which is made if there is none.
	if hasCar(d.Parts) {
		kept := make([]Part, 0, len(d.Parts))
		var placed []Child
		for _, p := range d.Parts {
			if !isCar(p) {
				kept = append(kept, p)
				continue
			}
			tree, ok := build(p)
			if !ok {
				kept = append(kept, p)
				continue
			}
			placed = append(placed, place(p, tree.Car))
		}
		d.Parts = kept
		if len(placed) > 0 {
			if d.Root == "" {
				root := "scene"
				for i := 2; taken[root]; i++ {
					root = fmt.Sprintf("scene-%d", i)
				}
				d.Assemblies = append(d.Assemblies, Assembly{ID: root, Name: d.Name})
				d.Root = root
			}
			for i := range d.Assemblies {
				if d.Assemblies[i].ID == d.Root {
					d.Assemblies[i].Children = append(d.Assemblies[i].Children, placed...)
				}
			}
		}
	}
	return problems
}

func hasCar(parts []Part) bool {
	for _, p := range parts {
		if isCar(p) {
			return true
		}
	}
	return false
}

// carFaults is every "car" part still in the document, as the errors that kept it
// from being written out (or, for one that should have been written out and was
// not, that fact). Faults reads it: a car still a "car" part is a car NOT in the
// model, which is what the repair loop exists to be told.
func carFaults(d Document) []Problem {
	var out []Problem
	mmPerUnit := 0.0
	if u, known := ParseUnit(d.Units); known {
		mmPerUnit, _ = u.toMM()
	}
	res := d.Resolve()
	lookup := func(n string) (float64, bool) {
		v, ok := res.Values[n]
		return v.Number, ok
	}
	for _, list := range [][]Part{d.Parts, d.Definitions} {
		for _, p := range list {
			if !isCar(p) {
				continue
			}
			_, found := readCar(p, lookup, mmPerUnit)
			errs := 0
			for _, f := range found {
				if f.Severity == Error {
					out = append(out, f)
					errs++
				}
			}
			if errs == 0 {
				out = append(out, Problem{Severity: Error, Name: p.Label(), Detail: "is a car that was never " +
					"written out as its body, wheels and trim, so nothing of it is in the model"})
			}
		}
	}
	return out
}

// CarGuide is what the prompt says about the "car" shape's numbers, from the same
// table readCar reads — so a key the prompt offers is a key the template reads.
func CarGuide() string {
	var b strings.Builder
	for _, k := range carKeys {
		fmt.Fprintf(&b, "      %-15s %s\n", k.Key, k.Means)
	}
	fmt.Fprintf(&b, "    \"class\" is one of %s; the car's proportions are checked against\n"+
		"    published cars of that class and anything outside their range is WARNED, never refused.",
		strings.Join(CarClasses(), ", "))
	return b.String()
}
