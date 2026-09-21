package geometry

import (
	"strings"
	"testing"
)

// Every row of the design-word table moves a knob the car template reads, inside
// the range the template builds, through an operation it names — and none of them
// sets a count, which only the request may (the rule sketch.go holds a generated
// picture to).
func TestDesignWords_NeverSetACountAndStayInsideTheTemplate(t *testing.T) {
	kinds := map[string]carKind{"height": carLength}
	for _, k := range carKeys {
		kinds[k.Key] = k.Kind
	}
	// The ranges checkCarGeometry builds a style knob between.
	bounds := map[string][2]float64{"cabin_start": {0.15, 0.55}, "cabin_length": {0.2, 0.6},
		"nose_height": {0.15, 0.95}, "belt_height": {0.3, 0.9}, "tail_height": {0.3, 1},
		"roof_width": {0.3, 0.95}, "edge_radius": {0, 0.08}}
	if len(designWords) == 0 {
		t.Fatal("the design-word table is empty")
	}
	for _, r := range designWords {
		if len(r.Words) == 0 || r.Meaning == "" || len(r.Set) == 0 {
			t.Errorf("row %v says nothing", r.Words)
		}
		if _, ok := designOperations[r.Operation]; !ok {
			t.Errorf("%v names operation %q", r.Words, r.Operation)
		}
		for _, s := range r.Set {
			kind, ok := kinds[s.Key]
			switch {
			case !ok:
				t.Errorf("%v moves %q, which the car template does not read", r.Words, s.Key)
			case kind == carCount:
				t.Errorf("%v sets the count %q; a count comes from the request, never a word", r.Words, s.Key)
			case len(s.Band) == 2:
				b, has := bounds[s.Key]
				if !has || s.Band[0] < b[0] || s.Band[1] > b[1] || s.Band[0] > s.Band[1] {
					t.Errorf("%v band %v for %s is outside what the template builds (%v)", r.Words, s.Band, s.Key, b)
				}
			case s.Toward == "high" || s.Toward == "low":
			default:
				t.Errorf("%v %s neither bands nor leans", r.Words, s.Key)
			}
		}
	}
}

// The guide the prompt carries is the table, row for row.
func TestDesignWords_TheGuideIsTheTable(t *testing.T) {
	guide := DesignWordGuide()
	for _, r := range designWords {
		for _, w := range r.Words {
			if !strings.Contains(guide, `"`+w+`"`) {
				t.Errorf("the guide does not carry %q", w)
			}
		}
		if !strings.Contains(guide, r.Meaning) {
			t.Errorf("the guide does not carry %q", r.Meaning)
		}
	}
	if got := strings.Count(guide, "\n") + 1; got != len(designWords) {
		t.Errorf("the guide has %d line(s) for %d row(s)", got, len(designWords))
	}
}
