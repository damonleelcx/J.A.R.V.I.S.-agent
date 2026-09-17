package geometry

import (
	"strings"
	"testing"
)

// E1's open item, settled 2026-09-17: an edit that changes a parameter, a derived
// value or a feature says which placed parts it reached, measured rather than read
// off the edit. The tree is axleTree (edit_tree_test.go) with its spokes and left
// wheel bound to parameters, as a stored document is: already bound.

func boundAxle(t *testing.T) Document {
	t.Helper()
	d := axleTree()
	d.Parameters = []Parameter{
		{Name: "spoke_len", Value: 60, Unit: "mm", How: Chosen},
		{Name: "half_axle", Value: 200, Unit: "mm", How: Chosen},
		{Name: "paint_code", Value: 7, Unit: "mm", How: Chosen},
	}
	d.Derived = []Derived{{Name: "spoke_reach", Expression: "spoke_len / 2"}}
	d.Definitions = append([]Part(nil), d.Definitions...)
	d.Definitions[1].SizeFrom = map[string]string{"depth": "spoke_len"}
	d.Definitions[1].PositionFrom = map[string]string{"z": "spoke_reach"}
	d.Assemblies = append([]Assembly(nil), d.Assemblies...)
	d.Assemblies[1].Interfaces = []Interface{
		{ID: "left-end", Position: []float64{-200, 0, 0}, PositionFrom: map[string]string{"x": "-half_axle"}},
		{ID: "right-end", Position: []float64{200, 0, 0}},
	}
	if problems := d.Bind(); len(problems) != 0 {
		t.Fatalf("the fixture does not bind: %+v", problems)
	}
	return d
}

func reachedLines(reached []Reached) string {
	var lines []string
	for _, r := range reached {
		lines = append(lines, r.Kind+" "+r.ID+" ["+strings.Join(r.Occurrences, ",")+"]")
	}
	return strings.Join(lines, "\n")
}

func TestEdit_AParameterChangeReportsThePlacedPartsThatFollowIt(t *testing.T) {
	spokes := "left-wheel/spoke-1,left-wheel/spoke-2,left-wheel/spoke-3,left-wheel/spoke-4,left-wheel/spoke-5,left-wheel/spoke-6," +
		"right-wheel/spoke-1,right-wheel/spoke-2,right-wheel/spoke-3,right-wheel/spoke-4,right-wheel/spoke-5,right-wheel/spoke-6"
	leftWheel := "left-wheel/hub,left-wheel/spoke-1,left-wheel/spoke-2,left-wheel/spoke-3,left-wheel/spoke-4,left-wheel/spoke-5,left-wheel/spoke-6"
	for _, tc := range []struct {
		name  string
		patch Document
		want  string
	}{
		{"a size and a position through a derived value", Document{Parameters: []Parameter{{Name: "spoke_len", Value: 80, Unit: "mm", How: Chosen}}},
			"parameter spoke_len [" + spokes + "]"},
		{"a derived value's own expression", Document{Derived: []Derived{{Name: "spoke_reach", Expression: "spoke_len / 3"}}},
			"parameter spoke_reach [" + spokes + "]"},
		{"a bound interface moves everything attached at it", Document{Parameters: []Parameter{{Name: "half_axle", Value: 250, Unit: "mm", How: Chosen}}},
			"parameter half_axle [" + leftWheel + "]"},
		{"nothing follows it", Document{Parameters: []Parameter{{Name: "paint_code", Value: 8, Unit: "mm", How: Chosen}}},
			"parameter paint_code []"},
		{"two at once are one entry", Document{Parameters: []Parameter{{Name: "paint_code", Value: 8, Unit: "mm", How: Chosen},
			{Name: "half_axle", Value: 250, Unit: "mm", How: Chosen}}},
			"parameter paint_code, half_axle [" + leftWheel + "]"},
		{"restated at its value", Document{Parameters: []Parameter{{Name: "spoke_len", Value: 60, Unit: "mm", How: Chosen}}}, ""},
		{"a new parameter", Document{Parameters: []Parameter{{Name: "fresh", Value: 3, Unit: "mm", How: Chosen}}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := boundAxle(t)
			patch := tc.patch
			_, reached, problems := Edit{Patch: &patch}.ApplyAndReport(base)
			if len(problems) != 0 {
				t.Fatalf("problems: %s", problemText(problems))
			}
			if got := reachedLines(reached); got != tc.want {
				t.Errorf("reported:\n%s\nwant exactly:\n%s", got, tc.want)
			}
		})
	}

	// What another entry already reports is not reported again: the spokes are the
	// definition's, and the parameter reached nothing else.
	base := boundAxle(t)
	spoke := base.Definitions[1]
	spoke.Name = "Spoke"
	_, reached, problems := Edit{Patch: &Document{Definitions: []Part{spoke},
		Parameters: []Parameter{{Name: "spoke_len", Value: 80, Unit: "mm", How: Chosen}}}}.ApplyAndReport(base)
	if len(problems) != 0 {
		t.Fatalf("problems: %s", problemText(problems))
	}
	if got, want := reachedLines(reached), "definition spoke ["+spokes+"]\nparameter spoke_len []"; got != want {
		t.Errorf("reported:\n%s\nwant exactly:\n%s", got, want)
	}
}

// A feature reaches the placed parts it acts on as the build reads it: a repeated
// tool is every copy, and a removed feature what it acted on.
func TestEdit_AFeatureChangeReportsThePartsItActsOn(t *testing.T) {
	base := Document{Name: "plate", Units: "mm", Parts: []Part{
		{ID: "plate", Shape: "box", Size: map[string]float64{"width": 100, "height": 6, "depth": 100}},
		{ID: "hole", Shape: "cylinder", Size: map[string]float64{"radius": 3, "height": 20},
			Repeat: &Repeat{Count: 4, Offset: []float64{20, 0, 0}}},
		{ID: "boss", Shape: "cylinder", Size: map[string]float64{"radius": 8, "height": 10}},
	}, Features: []Feature{{ID: "boss-on", Op: "fuse", Of: "plate", With: []string{"boss"}}}}

	_, reached, problems := Edit{Patch: &Document{Features: []Feature{{ID: "holes", Op: "cut", Of: "plate", With: []string{"hole"}}}},
		Remove: Removals{Features: []string{"boss-on"}}}.ApplyAndReport(base)
	if len(problems) != 0 {
		t.Fatalf("problems: %s", problemText(problems))
	}
	want := "feature boss-on [plate,boss]\nfeature holes [plate,hole-1,hole-2,hole-3,hole-4]"
	if got := reachedLines(reached); got != want {
		t.Errorf("reported:\n%s\nwant exactly:\n%s", got, want)
	}
	if len(reached) == 2 && strings.Join(reached[1].Named, ",") != "plate,hole" {
		t.Errorf("the feature named plate and hole; the report says it named %v", reached[1].Named)
	}
}
