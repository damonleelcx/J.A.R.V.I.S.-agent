package geometry_test

import (
	"math"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// find returns the one relationship of this kind whose name contains want.
func find(t *testing.T, rs []geometry.Relationship, kind, want string) geometry.Relationship {
	t.Helper()
	for _, r := range rs {
		if r.Kind == kind && strings.Contains(r.Name, want) {
			return r
		}
	}
	t.Fatalf("no %s relationship named %q in %v", kind, want, names(rs))
	return geometry.Relationship{}
}

func names(rs []geometry.Relationship) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.Kind+": "+r.Name)
	}
	return out
}

func details(problems []geometry.Problem) string {
	var out []string
	for _, p := range problems {
		out = append(out, p.Name+" "+p.Detail)
	}
	return strings.Join(out, "\n")
}

// Issue 9, half one. Wave 13 measures a span and the honesty machinery drops it
// when the parts share no name — silently, so a reader cannot tell "checked and
// fine" from "not looked at". It comes back NOT CHECKED now, with the reason and
// with the name FORGE assigns from the parameters underneath it.
func TestRelationships_ASpanNothingCanNameIsReportedRatherThanDropped(t *testing.T) {
	doc := geometry.Document{
		Name: "mount", Units: "mm",
		Parameters: []geometry.Parameter{
			{Name: "nema17_face_size", Value: 42.3, Unit: "mm",
				How: geometry.FromStandard, Source: "NEMA 17"}},
		Derived: []geometry.Derived{{Name: "motor_mount_x", Expression: "nema17_face_size / 2"}},
		Parts: []geometry.Part{
			{ID: "aaa", Shape: "cylinder", Size: map[string]float64{"radius": 1.6, "height": 6},
				PositionFrom: map[string]string{"x": "motor_mount_x"}},
			{ID: "zzz", Shape: "cylinder", Size: map[string]float64{"radius": 1.6, "height": 6},
				PositionFrom: map[string]string{"x": "0 - motor_mount_x"}},
		},
	}
	r := find(t, doc.Relationships(), "distance", "the parts placed from")
	if r.Named || r.Checked {
		t.Errorf("a span nothing can name must be reported as unnamed and unchecked: %+v", r)
	}
	if !strings.Contains(r.Why, "share no name") {
		t.Errorf("the reason must say why it was not checked; got %q", r.Why)
	}
	if math.Abs(r.Value-42.3) > 1e-9 {
		t.Errorf("extent = %v, want 42.3 — the measurement is still reported", r.Value)
	}
	if !strings.Contains(details(doc.RelationshipProblems()), "did not check") {
		t.Errorf("it never reached the turn:\n%s", details(doc.RelationshipProblems()))
	}
}

// And the named case is still named — the same span with ids that share a word
// is checked, so the honest answer did not become a caveat on every document.
func TestRelationships_ASpanThePartsNameIsChecked(t *testing.T) {
	doc := geometry.Document{
		Name: "mount", Units: "mm",
		Parameters: []geometry.Parameter{{Name: "pitch", Value: 31, Unit: "mm"}},
		Parts: []geometry.Part{
			{ID: "mount-hole-l", Shape: "cylinder", Size: map[string]float64{"radius": 1.6},
				PositionFrom: map[string]string{"x": "pitch / 2"}},
			{ID: "mount-hole-r", Shape: "cylinder", Size: map[string]float64{"radius": 1.6},
				PositionFrom: map[string]string{"x": "0 - pitch / 2"}},
		},
	}
	r := find(t, doc.Relationships(), "distance", "mount-hole")
	if !r.Named || !r.Checked || r.Why != "" {
		t.Errorf("a named span must be checked with nothing to explain: %+v", r)
	}
}

// Issue 9, half two. A parameter or a derived value nothing reads names no
// dimension the document exposes: the document reads as parametric and nothing
// binds. Reported with the value named, and never refused.
func TestRelationships_AValueNothingReadsIsReportedWithItsName(t *testing.T) {
	doc := geometry.Document{
		Name: "plate", Units: "mm",
		Parameters: []geometry.Parameter{
			{Name: "plate_size", Value: 60, Unit: "mm"},
			{Name: "motor_mount_x", Value: 21.15, Unit: "mm"},
		},
		Derived: []geometry.Derived{{Name: "decorative", Expression: "motor_mount_x * 2"}},
		Parts: []geometry.Part{{ID: "plate", Shape: "box",
			Size:     map[string]float64{"width": 60, "height": 6, "depth": 60},
			SizeFrom: map[string]string{"width": "plate_size"}}},
	}
	said := details(doc.RelationshipProblems())
	for _, want := range []string{"motor_mount_x", "decorative", "nothing in this design reads it"} {
		if !strings.Contains(said, want) {
			t.Errorf("the report does not mention %q:\n%s", want, said)
		}
	}
	if strings.Contains(said, "plate_size is a parameter nothing") {
		t.Errorf("plate_size IS read, and was reported anyway:\n%s", said)
	}
	for _, p := range doc.RelationshipProblems() {
		if p.Severity != geometry.Warning {
			t.Errorf("issue 9 says reported, not refused; got %v for %q", p.Severity, p.Name)
		}
	}
}

// The narrowing that keeps it usable: a document that binds NOTHING is not told
// that every parameter it has is unread. That is a warning firing on ordinary
// input, and a warning that fires on ordinary input is one people stop reading.
// Such a document is answered by the unbound-pattern report instead.
func TestRelationships_ADocumentThatBindsNothingIsNotToldEveryParameterIsUnread(t *testing.T) {
	doc := geometry.Document{
		Name: "plate", Units: "mm",
		Parameters: []geometry.Parameter{{Name: "plate_size", Value: 60, Unit: "mm"}},
		Parts: []geometry.Part{{ID: "plate", Shape: "box",
			Size: map[string]float64{"width": 60, "height": 6, "depth": 60}}},
	}
	if said := details(doc.RelationshipProblems()); strings.Contains(said, "nothing in this design reads") {
		t.Errorf("a non-parametric document was nagged about every parameter:\n%s", said)
	}
}

// Issue 10. Holes at hardcoded coordinates describe no pattern: there is a set
// of positions and nothing connecting them to a parameter. The document looks
// parametric and behaves as a snapshot — change the plate and the holes stay.
func TestRelationships_EvenlySpacedPartsWithNoBindingAreReported(t *testing.T) {
	hole := func(id string, x float64) geometry.Part {
		return geometry.Part{ID: id, Shape: "cylinder",
			Size:     map[string]float64{"radius": 2, "height": 6},
			Position: []float64{x, 0, 0}}
	}
	doc := geometry.Document{
		Name: "plate", Units: "mm",
		Parameters: []geometry.Parameter{{Name: "plate_size", Value: 120, Unit: "mm"}},
		Parts: []geometry.Part{
			{ID: "plate", Shape: "box", Size: map[string]float64{"width": 120, "height": 6, "depth": 40},
				SizeFrom: map[string]string{"width": "plate_size"}},
			hole("bolt-1", -45), hole("bolt-2", -15), hole("bolt-3", 15), hole("bolt-4", 45),
		},
	}
	said := details(doc.RelationshipProblems())
	for _, want := range []string{"bolt", "evenly spaced 30", "along x", "pattern"} {
		if !strings.Contains(said, want) {
			t.Errorf("the report does not say %q:\n%s", want, said)
		}
	}
}

// The narrow first cut the issue asks for, fenced from the other side: three is
// the floor, because two points are evenly spaced by definition, and a group
// that IS bound is not reported.
func TestRelationships_AnUnevenPairAndABoundRowAreNotCalledAPattern(t *testing.T) {
	hole := func(id string, x float64, from map[string]string) geometry.Part {
		return geometry.Part{ID: id, Shape: "cylinder",
			Size:         map[string]float64{"radius": 2},
			Position:     []float64{x, 0, 0},
			PositionFrom: from}
	}
	pair := geometry.Document{
		Name: "plate", Units: "mm",
		Parameters: []geometry.Parameter{{Name: "plate_size", Value: 120, Unit: "mm"}},
		Parts:      []geometry.Part{hole("a-1", -10, nil), hole("a-2", 10, nil)},
	}
	if said := details(pair.RelationshipProblems()); strings.Contains(said, "evenly spaced") {
		t.Errorf("two parts are not a pattern:\n%s", said)
	}
	bound := geometry.Document{
		Name: "plate", Units: "mm",
		Parameters: []geometry.Parameter{{Name: "pitch", Value: 30, Unit: "mm"}},
		Parts: []geometry.Part{
			hole("b-1", -30, map[string]string{"x": "0 - pitch"}),
			hole("b-2", 0, map[string]string{"x": "0"}),
			hole("b-3", 30, map[string]string{"x": "pitch"}),
		},
	}
	if said := details(bound.RelationshipProblems()); strings.Contains(said, "evenly spaced") {
		t.Errorf("a bound row is a pattern that follows its parameter:\n%s", said)
	}
}

// Issue 11, angles. A polar pattern's sweep is an angle the document states as a
// relationship: it rests on parameters and moves when they do, so the angle
// between one copy's axis and the next is checked the way a distance is.
func TestRelationships_APolarPatternsBoundSweepIsCheckedAsAnAngle(t *testing.T) {
	doc := geometry.Document{
		Name: "fan", Units: "mm",
		Parameters:  []geometry.Parameter{{Name: "fan_sweep", Value: 120, Unit: "deg"}},
		Definitions: []geometry.Part{{ID: "blade", Shape: "box", Size: map[string]float64{"width": 4, "height": 4, "depth": 40}}},
		Assemblies: []geometry.Assembly{{ID: "rotor", Children: []geometry.Child{
			{ID: "blade", Ref: "blade", Pattern: &geometry.Pattern{
				Kind: "polar", Count: 4, About: "y", Angle: 120, AngleFrom: "fan_sweep"}},
		}}},
		Root: "rotor",
	}
	r := find(t, doc.Relationships(), "angle", "step angle")
	if !r.Checked || r.Unit != "deg" {
		t.Fatalf("a bound sweep must be a checked angle in degrees: %+v", r)
	}
	// Four copies over 120°: three gaps, so 40° between one axis and the next.
	if math.Abs(r.Value-40) > 1e-9 {
		t.Errorf("step angle = %v°, want 40", r.Value)
	}
	if len(r.Depends) != 1 || r.Depends[0] != "fan_sweep" {
		t.Errorf("depends = %v, want the parameter it rests on", r.Depends)
	}
}

// And the other way an angle is stated: two parts the BINDINGS relate, turned
// differently. The pair is read from the bindings and never from proximity, so
// nothing here decides that two cylinders near each other are related.
func TestRelationships_TwoRelatedPartsTurnedApartStateAnAngle(t *testing.T) {
	doc := geometry.Document{
		Name: "vee", Units: "mm",
		Parameters: []geometry.Parameter{{Name: "half_vee", Value: 40, Unit: "mm"}},
		Parts: []geometry.Part{
			{ID: "vee-arm-l", Shape: "cylinder", Size: map[string]float64{"radius": 3, "height": 50},
				PositionFrom: map[string]string{"x": "half_vee"}, Rotation: []float64{0, 0, 30}},
			{ID: "vee-arm-r", Shape: "cylinder", Size: map[string]float64{"radius": 3, "height": 50},
				PositionFrom: map[string]string{"x": "0 - half_vee"}, Rotation: []float64{0, 0, -30}},
		},
	}
	r := find(t, doc.Relationships(), "angle", "vee-arm")
	if !r.Checked || math.Abs(r.Value-60) > 1e-6 {
		t.Errorf("axis angle = %v°, want 60 (checked=%v)", r.Value, r.Checked)
	}
}

// Parallel parts describe no angle, and reporting 0° for every bolt in a pattern
// is the warning-on-correct-input failure this package keeps naming.
//
// # Why the second case is here and not in its own test
//
// The obvious case — two parts with the SAME rotation — is held by turnedApart
// finding no pair at all, and it would pass however far apart the floor was set.
// A drill on the floor proved that: it stayed GREEN, because nothing in the test
// reached the floor. So the case that actually exercises it is a pair turned by
// LESS than the floor, which is the one a real document produces — a tenth of a
// degree of rounding is not an angle somebody stated.
func TestRelationships_PartsThatAreNotTurnedApartDescribeNoAngle(t *testing.T) {
	hole := func(id, x string, turn float64) geometry.Part {
		return geometry.Part{ID: id, Shape: "cylinder",
			Size:         map[string]float64{"radius": 1.6},
			Rotation:     []float64{0, 0, turn},
			PositionFrom: map[string]string{"x": x}}
	}
	for what, doc := range map[string]geometry.Document{
		"turned exactly alike": {
			Name: "mount", Units: "mm",
			Parameters: []geometry.Parameter{{Name: "pitch", Value: 31, Unit: "mm"}},
			Parts: []geometry.Part{
				hole("mount-hole-l", "pitch / 2", 0),
				hole("mount-hole-r", "0 - pitch / 2", 0)},
		},
		"a tenth of a degree apart": {
			Name: "mount", Units: "mm",
			Parameters: []geometry.Parameter{{Name: "pitch", Value: 31, Unit: "mm"}},
			Parts: []geometry.Part{
				hole("mount-hole-l", "pitch / 2", 0),
				hole("mount-hole-r", "0 - pitch / 2", 0.1)},
		},
	} {
		for _, r := range doc.Relationships() {
			if r.Kind == "angle" {
				t.Errorf("parts %s were given an angle: %+v", what, r)
			}
		}
	}
}

// Issue 11, ratios. A derived value that divides one named measurement by
// another IS a ratio, and its result has no unit whatever Resolve inherited —
// Resolve does unit agreement and not unit algebra, and a ratio carrying "mm"
// would be a wrong unit rather than a missing one.
func TestRelationships_ADerivedQuotientOfTwoMeasurementsIsCheckedAsARatio(t *testing.T) {
	doc := geometry.Document{
		Name: "plate", Units: "mm",
		Parameters: []geometry.Parameter{
			{Name: "plate_width", Value: 120, Unit: "mm"},
			{Name: "plate_height", Value: 80, Unit: "mm"},
		},
		Derived: []geometry.Derived{{Name: "aspect", Expression: "plate_width / plate_height"}},
		Parts: []geometry.Part{{ID: "plate", Shape: "box",
			Size:     map[string]float64{"width": 120, "height": 6, "depth": 80},
			SizeFrom: map[string]string{"width": "plate_width", "depth": "plate_height"}}},
	}
	r := find(t, doc.Relationships(), "ratio", "aspect")
	if !r.Checked || r.Unit != "" || math.Abs(r.Value-1.5) > 1e-9 {
		t.Errorf("ratio = %v %q (checked=%v), want 1.5 with no unit", r.Value, r.Unit, r.Checked)
	}
	if len(r.Parts) != 2 || r.Parts[0] != "plate_width" || r.Parts[1] != "plate_height" {
		t.Errorf("the two named measurements are %v, want plate_width over plate_height", r.Parts)
	}
}

// A quotient of two DIFFERENT units is a rate and not a ratio, and nothing here
// checks a rate. Said rather than silently reported as a ratio.
func TestRelationships_AQuotientOfUnlikeUnitsIsNotCalledARatio(t *testing.T) {
	doc := geometry.Document{
		Name: "cam", Units: "mm",
		Parameters: []geometry.Parameter{
			{Name: "lift", Value: 12, Unit: "mm"},
			{Name: "sweep", Value: 90, Unit: "deg"},
		},
		Derived: []geometry.Derived{{Name: "lift_rate", Expression: "lift / sweep"}},
		Parts: []geometry.Part{{ID: "cam", Shape: "box", Size: map[string]float64{"width": 12},
			SizeFrom: map[string]string{"width": "lift"}}},
	}
	r := find(t, doc.Relationships(), "ratio", "lift_rate")
	if r.Checked || !strings.Contains(r.Why, "rate") {
		t.Errorf("a rate must be reported as unchecked with the reason: %+v", r)
	}
}

// Issue 11, wall thickness — the one a manufacturability review opens with, and
// the one that is NOT another case in the same switch: it is the material
// between two surfaces, measured on the loops the section is drawn from.
func TestRelationships_TheLeastMaterialBetweenAnOutlineAndItsHoleIsMeasured(t *testing.T) {
	doc := geometry.Document{
		Name: "tube", Units: "mm",
		Parts: []geometry.Part{{ID: "tube", Name: "Tube", Shape: "extrusion",
			Size: map[string]float64{"depth": 50},
			// A 40 x 40 square with a 20 x 20 square bore: 10 mm of wall.
			Profile: []geometry.Point{{X: -20, Y: -20}, {X: 20, Y: -20}, {X: 20, Y: 20}, {X: -20, Y: 20}},
			Holes: [][]geometry.Point{{
				{X: -10, Y: -10}, {X: 10, Y: -10}, {X: 10, Y: 10}, {X: -10, Y: 10}}},
		}},
	}
	r := find(t, doc.Relationships(), "wall thickness", "Tube wall")
	if !r.Checked || math.Abs(r.Value-10) > 1e-9 || r.Unit != "mm" {
		t.Errorf("wall = %v %q (checked=%v), want 10 mm", r.Value, r.Unit, r.Checked)
	}
	if !strings.Contains(r.Name, "the outline") || !strings.Contains(r.Name, "hole 1") {
		t.Errorf("the name must say which two the material is between; got %q", r.Name)
	}
}

// A measurement, never a verdict. Whether 0.4 mm of wall is too thin depends on
// the material and the process, neither of which is this package's business.
func TestRelationships_AThinWallIsMeasuredAndNotJudged(t *testing.T) {
	doc := geometry.Document{
		Name: "tube", Units: "mm",
		Parts: []geometry.Part{{ID: "tube", Name: "Tube", Shape: "extrusion",
			Size:    map[string]float64{"depth": 50},
			Profile: []geometry.Point{{X: -20, Y: -20}, {X: 20, Y: -20}, {X: 20, Y: 20}, {X: -20, Y: 20}},
			Holes: [][]geometry.Point{{
				{X: -19.6, Y: -19.6}, {X: 19.6, Y: -19.6}, {X: 19.6, Y: 19.6}, {X: -19.6, Y: 19.6}}},
		}},
	}
	r := find(t, doc.Relationships(), "wall thickness", "Tube wall")
	if math.Abs(r.Value-0.4) > 1e-9 {
		t.Fatalf("wall = %v, want 0.4", r.Value)
	}
	for _, word := range []string{"too thin", "thin", "minimum"} {
		if strings.Contains(strings.ToLower(details(doc.RelationshipProblems())), word) {
			t.Errorf("a verdict was invented about %q", word)
		}
	}
}

// And it will not put a number on a wall it cannot measure honestly. At a
// rounded corner the material is not where the straight drawing puts it, and a
// figure off by up to the radius reported as a wall thickness is worse than no
// figure at all.
func TestRelationships_AWallWithARoundedCornerIsNotGivenANumber(t *testing.T) {
	doc := geometry.Document{
		Name: "tube", Units: "mm",
		Parts: []geometry.Part{{ID: "tube", Name: "Tube", Shape: "extrusion",
			Size: map[string]float64{"depth": 50},
			Profile: []geometry.Point{{X: -20, Y: -20, Radius: 4}, {X: 20, Y: -20, Radius: 4},
				{X: 20, Y: 20, Radius: 4}, {X: -20, Y: 20, Radius: 4}},
			Holes: [][]geometry.Point{{
				{X: -10, Y: -10}, {X: 10, Y: -10}, {X: 10, Y: 10}, {X: -10, Y: 10}}},
		}},
	}
	r := find(t, doc.Relationships(), "wall thickness", "Tube wall")
	if r.Checked || !strings.Contains(r.Why, "corner radius") {
		t.Errorf("a rounded loop must come back unmeasured with the reason: %+v", r)
	}
	if r.Value != 0 {
		t.Errorf("a wall that was not measured must carry no figure; got %v", r.Value)
	}
}

// A definition's wall is measured on the DEFINITION's own drawing.
//
// binding.go resolves definitions against their own list because a definition
// and a top-level part may share an id and must not pick up each other's
// outline. The same holds here: the wall below is 10 mm in the definition and
// would be 1 mm if the top-level part of the same name were measured instead.
func TestRelationships_ADefinitionsWallIsMeasuredOnItsOwnDrawing(t *testing.T) {
	square := func(half float64) []geometry.Point {
		return []geometry.Point{{X: -half, Y: -half}, {X: half, Y: -half},
			{X: half, Y: half}, {X: -half, Y: half}}
	}
	doc := geometry.Document{
		Name: "tube", Units: "mm",
		Parts: []geometry.Part{{ID: "tube", Name: "Top-level tube", Shape: "extrusion",
			Size: map[string]float64{"depth": 50}, Profile: square(20),
			Holes: [][]geometry.Point{square(19)}}},
		Definitions: []geometry.Part{{ID: "tube", Name: "Defined tube", Shape: "extrusion",
			Size: map[string]float64{"depth": 50}, Profile: square(20),
			Holes: [][]geometry.Point{square(10)}}},
		Assemblies: []geometry.Assembly{{ID: "rig",
			Children: []geometry.Child{{ID: "one", Ref: "tube"}}}},
		Root: "rig",
	}
	r := find(t, doc.Relationships(), "wall thickness", "Defined tube wall")
	if !r.Checked || math.Abs(r.Value-10) > 1e-9 {
		t.Errorf("the definition's wall = %v (checked=%v), want 10 — it was measured from "+
			"the top-level part of the same id", r.Value, r.Checked)
	}
}

// And an angle is never measured ACROSS two frames. The ids below exist in both
// lists, so nothing can say which pair of parts a span means, and a measurement
// taken across two coordinate frames is a measurement of nothing.
func TestRelationships_AnAngleIsNotMeasuredAcrossTwoFrames(t *testing.T) {
	post := func(id, x string, turn float64) geometry.Part {
		return geometry.Part{ID: id, Shape: "cylinder",
			Size:         map[string]float64{"radius": 3, "height": 40},
			Rotation:     []float64{0, 0, turn},
			PositionFrom: map[string]string{"x": x}}
	}
	doc := geometry.Document{
		Name: "rig", Units: "mm",
		Parameters: []geometry.Parameter{{Name: "half_span", Value: 40, Unit: "mm"}},
		Parts: []geometry.Part{
			post("post-l", "half_span", 0), post("post-r", "0 - half_span", 0)},
		Definitions: []geometry.Part{
			post("post-l", "half_span", 40), post("post-r", "0 - half_span", -40)},
	}
	for _, r := range doc.Relationships() {
		if r.Kind == "angle" {
			t.Errorf("an angle was measured across two frames: %+v", r)
		}
	}
}

// A drawing too big to measure honestly says so rather than making a turn slow.
// The least material is every edge against every edge, and a check that makes a
// turn slow is a check somebody switches off.
func TestRelationships_AWallDrawnWithTooManyPointsIsNotMeasured(t *testing.T) {
	ring := func(radius float64, n int) []geometry.Point {
		out := make([]geometry.Point, 0, n)
		for i := 0; i < n; i++ {
			a := 2 * math.Pi * float64(i) / float64(n)
			out = append(out, geometry.Point{X: radius * math.Cos(a), Y: radius * math.Sin(a)})
		}
		return out
	}
	doc := geometry.Document{
		Name: "tube", Units: "mm",
		Parts: []geometry.Part{{ID: "tube", Name: "Tube", Shape: "extrusion",
			Size: map[string]float64{"depth": 50}, Profile: ring(20, 400),
			Holes: [][]geometry.Point{ring(10, 400)}}},
	}
	r := find(t, doc.Relationships(), "wall thickness", "Tube wall")
	if r.Checked || !strings.Contains(r.Why, "points between them") {
		t.Errorf("a drawing past the ceiling must say it was not measured: %+v", r)
	}
}

// The table is what the checker reads, so what the contract teaches and what is
// actually checked cannot drift apart.
func TestRelationships_TheTableIsWhatTheCheckerReads(t *testing.T) {
	kinds := geometry.RelationshipKinds()
	if len(kinds) != 4 {
		t.Fatalf("%d kinds, want 4 — the guide and the checker read this list", len(kinds))
	}
	doc := geometry.Document{
		Name: "everything", Units: "mm",
		Parameters: []geometry.Parameter{
			{Name: "pitch", Value: 31, Unit: "mm"},
			{Name: "plate_width", Value: 120, Unit: "mm"},
			{Name: "plate_height", Value: 80, Unit: "mm"},
			{Name: "fan_sweep", Value: 120, Unit: "deg"},
		},
		Derived:     []geometry.Derived{{Name: "aspect", Expression: "plate_width / plate_height"}},
		Definitions: []geometry.Part{{ID: "blade", Shape: "box", Size: map[string]float64{"width": 4}}},
		Parts: []geometry.Part{
			{ID: "mount-hole-l", Shape: "cylinder", Size: map[string]float64{"radius": 1.6},
				PositionFrom: map[string]string{"x": "pitch / 2"}, Rotation: []float64{0, 0, 30}},
			{ID: "mount-hole-r", Shape: "cylinder", Size: map[string]float64{"radius": 1.6},
				PositionFrom: map[string]string{"x": "0 - pitch / 2"}},
			{ID: "tube", Name: "Tube", Shape: "extrusion",
				Size:     map[string]float64{"depth": 50},
				SizeFrom: map[string]string{"depth": "plate_width"},
				Profile:  []geometry.Point{{X: -20, Y: -20}, {X: 20, Y: -20}, {X: 20, Y: 20}, {X: -20, Y: 20}},
				Holes: [][]geometry.Point{{
					{X: -10, Y: -10}, {X: 10, Y: -10}, {X: 10, Y: 10}, {X: -10, Y: 10}}}},
		},
		Assemblies: []geometry.Assembly{{ID: "rotor", Children: []geometry.Child{
			{ID: "blade", Ref: "blade", Pattern: &geometry.Pattern{
				Kind: "polar", Count: 4, About: "y", Angle: 120, AngleFrom: "fan_sweep"}}}}},
	}
	seen := map[string]bool{}
	for _, r := range doc.Relationships() {
		seen[r.Kind] = true
	}
	for _, k := range kinds {
		if !seen[k.Kind] {
			t.Errorf("the table offers %q and the checker produced none: %v", k.Kind, names(doc.Relationships()))
		}
		if !strings.Contains(geometry.RelationshipGuide(), k.Kind) ||
			!strings.Contains(geometry.RelationshipGuide(), k.Result) {
			t.Errorf("the guide does not teach %q from the table", k.Kind)
		}
	}
}
