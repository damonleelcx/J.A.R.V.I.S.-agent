package cad_test

import (
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/cad"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// ‼️ Lengths reach a script in MILLIMETRES, and everything else untouched.
//
// # Why this is not a detail
//
// A script hands back STEP, which build123d writes in millimetres, and every
// other part in the document has its dimensions converted the same way before
// the kernel sees them. A parameter authored in cm injected as its raw number
// would build a part ten times too small — silently, in the one shape nobody can
// read dimensions off, because a scripted part shows "? × ? × ?" in the panel.
//
// And a count, an angle or a ratio must NOT be scaled: there is nothing to
// convert them to, and multiplying a tooth count by ten is not a rounding error.
func TestScriptParameters_LengthsInMillimetresAndNothingElseTouched(t *testing.T) {
	doc := geometry.Document{
		Name: "Gear", Units: "mm",
		Parameters: []geometry.Parameter{
			{Name: "plate_width", Value: 5, Unit: "cm"},
			{Name: "bore", Value: 8, Unit: "mm"},
			{Name: "stock", Value: 1, Unit: "in"},
			{Name: "teeth_count", Value: 20, Unit: ""},
			{Name: "pressure_angle", Value: 20, Unit: "deg"},
		},
		Derived: []geometry.Derived{
			{Name: "pitch_radius", Expression: "bore * 2"},
		},
	}
	got := cad.ScriptParameters(doc)

	for _, tc := range []struct {
		name string
		want float64
		why  string
	}{
		{"plate_width", 50, "5 cm is 50 mm; injected raw it would build a part ten times too small"},
		{"bore", 8, "already millimetres"},
		{"stock", 25.4, "1 inch is 25.4 mm"},
		{"teeth_count", 20, "a count has no unit and must not be scaled"},
		{"pressure_angle", 20, "an angle is not a length"},
		{"pitch_radius", 16, "a derived value resolves and comes through too"},
	} {
		v, ok := got[tc.name]
		if !ok {
			t.Errorf("%s never reached the script at all", tc.name)
			continue
		}
		if v < tc.want-0.01 || v > tc.want+0.01 {
			t.Errorf("%s = %v, want %v — %s", tc.name, v, tc.want, tc.why)
		}
	}
}
