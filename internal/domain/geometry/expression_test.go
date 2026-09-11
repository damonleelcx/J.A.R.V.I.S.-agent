package geometry_test

import (
	"math"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// evalOne runs one expression against a fixed environment through the public
// door — Resolve — rather than through a test-only copy of the evaluator.
// A unit test that calls its own simplified parser proves nothing about the one
// that runs in production.
func evalOne(t *testing.T, expr string) (float64, geometry.Resolution) {
	t.Helper()
	d := &geometry.Document{
		Units: "mm",
		Parameters: []geometry.Parameter{
			{Name: "a", Value: 10, Unit: "mm"},
			{Name: "b", Value: 4, Unit: "mm"},
			{Name: "n", Value: 3, Unit: ""},
		},
		Derived: []geometry.Derived{{Name: "out", Expression: expr}},
	}
	res := d.Resolve()
	return res.Values["out"].Number, res
}

func TestExpression_ArithmeticAndPrecedence(t *testing.T) {
	cases := []struct {
		expr string
		want float64
	}{
		{"a + b", 14},
		{"a - b", 6},
		{"a * b", 40},
		{"a / b", 2.5},
		{"a - 2 * b", 2},     // precedence, not left-to-right
		{"(a - 2) * b", 32},  // brackets win
		{"-a + b", -6},       // unary minus
		{"a - -b", 14},       // unary after an operator
		{"2 ^ 3 ^ 2", 512},   // right-associative, as every calculator writes it
		{"sqrt(a * 10)", 10}, // the function the spike's model actually used
		{"(a / sqrt(2)) / 2", 3.5355339059327378},
		{"min(a, b)", 4},
		{"max(a, b)", 10},
		{"round(a / n)", 3},
		{"floor(a / n)", 3},
		{"ceil(a / n)", 4},
		{"abs(b - a)", 6},
		{"a * pi", 31.41592653589793},
	}
	for _, c := range cases {
		got, res := evalOne(t, c.expr)
		if !res.OK() {
			t.Errorf("%q: %+v", c.expr, res.Problems)
			continue
		}
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%q = %v, want %v", c.expr, got, c.want)
		}
	}
}

func TestExpression_RefusalsAreNamedAndPositioned(t *testing.T) {
	cases := []struct {
		expr, wants string
	}{
		{"a +", "ends where a value was expected"},
		{"(a + b", "never closed"},
		{"a + b)", "unexpected"},
		{"a $ b", "unexpected character"},
		{"a / 0", "division by zero"},
		{"sqrt(-a)", "sqrt of a negative"},
		{"sqrt(a, b)", "takes 1 argument"},
		{"min(a)", "takes 2 argument"},
		{"tan(a)", "not a function this build understands"},
		{"", "empty"},
	}
	for _, c := range cases {
		_, res := evalOne(t, c.expr)
		if res.OK() {
			t.Errorf("%q was accepted; it must not be", c.expr)
			continue
		}
		if !hasProblem(res, geometry.Error, "out", c.wants) {
			t.Errorf("%q: no problem containing %q; got %+v", c.expr, c.wants, res.Problems)
		}
	}
}

// The absence of UNSTATED-CONVENTION trigonometry is a feature, and this keeps
// it absent.
//
// sin/cos/tan are written in degrees by half the engineering world and radians
// by the other half; the two agree only at zero, and the wrong one produces a
// plausible number rather than an error. Adding them without stating a
// convention would build the exact failure this phase exists to catch.
//
// ‼️ 2026-09-10: the degree-named forms — cos_deg and friends — WERE added, and
// this fence was renamed rather than deleted, because its argument is the reason
// the naming was chosen. What must stay absent is the bare spelling: offering
// `cos` beside `cos_deg` hands the ambiguity straight back.
// TestExpression_BareTrigonometryIsStillAbsent is the same claim from the other
// side, over the exported function list.
func TestExpression_UnstatedConventionTrigonometryIsRefusedOnPurpose(t *testing.T) {
	for _, fn := range []string{"sin", "cos", "tan", "asin", "atan2", "radians"} {
		_, res := evalOne(t, fn+"(a)")
		if res.OK() {
			t.Fatalf("%s() resolved; only the forms that NAME their convention may exist", fn)
		}
	}
}

// An expression is a closed grammar over this document's own names, not an
// execution surface. Nothing in it can reach a package, a file, or a shell.
func TestExpression_CannotNameAnythingOutsideTheDocument(t *testing.T) {
	for _, expr := range []string{
		`os.Getenv("PATH")`,
		"math.Pi",
		"a; b",
		"a && b",
		"`whoami`",
		"$(whoami)",
		"a[0]",
		"eval(a)",
	} {
		_, res := evalOne(t, expr)
		if res.OK() {
			t.Errorf("%q was accepted by the grammar", expr)
		}
	}
}

func TestExpression_IdentifiersAreCaseInsensitive(t *testing.T) {
	// Models are inconsistent about case and a document that resolves or fails
	// on capitalisation would be maddening to author.
	got, res := evalOne(t, "A + B")
	if !res.OK() {
		t.Fatalf("%+v", res.Problems)
	}
	if got != 14 {
		t.Fatalf("got %v, want 14", got)
	}
}

func TestExpression_WhitespaceIsIrrelevant(t *testing.T) {
	spaced, _ := evalOne(t, "  a\t-\n2 * b  ")
	tight, _ := evalOne(t, "a-2*b")
	if spaced != tight || spaced != 2 {
		t.Fatalf("spaced=%v tight=%v, want both 2", spaced, tight)
	}
}

// A stray character silently dropped is how "plate_size ; 2" becomes a valid
// looking halving. The message must name the character and where it was.
func TestExpression_AStrayCharacterIsNeverDropped(t *testing.T) {
	_, res := evalOne(t, "a ; 2")
	if res.OK() {
		t.Fatal("a stray separator was swallowed")
	}
	var found bool
	for _, p := range res.Problems {
		if strings.Contains(p.Detail, ";") && strings.Contains(p.Detail, "position") {
			found = true
		}
	}
	if !found {
		t.Errorf("the message names neither the character nor its position: %+v", res.Problems)
	}
}

// Trigonometry, in degrees, named so nobody has to guess the convention.
//
// # Why this exists at all
//
// The absence of sin/cos was a documented decision — half the engineering world
// writes them in degrees, half in radians, they agree only at zero, and a wrong
// one produces a plausible number rather than an error. That argument still
// stands, and is answered by putting the convention in the NAME rather than by
// ignoring it. The bare spellings are deliberately still absent.
//
// It was added because the absence had a measured cost: a model asked for a gear
// declares base_radius = pitch_radius * cos(pressure_angle), which could not
// resolve, so the value was not in scope for the part's script, so the script
// naming it was refused — six of nine failures in one live run.
func TestExpression_TrigonometryInDegrees(t *testing.T) {
	doc := &geometry.Document{
		Units: "mm",
		Parameters: []geometry.Parameter{
			{Name: "pressure_angle", Value: 20, Unit: "deg"},
			{Name: "pitch_radius", Value: 20, Unit: "mm"},
			{Name: "rise", Value: 3, Unit: "mm"},
			{Name: "run", Value: 4, Unit: "mm"},
		},
		Derived: []geometry.Derived{
			// The live case, verbatim in shape.
			{Name: "base_radius", Expression: "pitch_radius * cos_deg(pressure_angle)"},
			{Name: "half", Expression: "sin_deg(30)"},
			{Name: "slope", Expression: "tan_deg(45)"},
			{Name: "taper", Expression: "atan2_deg(rise, run)"},
		},
	}
	res := doc.Resolve()
	for _, p := range res.Problems {
		if p.Severity == geometry.Error {
			t.Fatalf("a document using degree trigonometry did not resolve: %s: %s", p.Name, p.Detail)
		}
	}
	for _, tc := range []struct {
		name string
		want float64
	}{
		{"base_radius", 18.7938}, // 20 * cos 20°
		{"half", 0.5},
		{"slope", 1.0},
		{"taper", 36.8699}, // atan2(3,4)
	} {
		got, ok := res.Values[tc.name]
		if !ok {
			t.Errorf("%s did not resolve at all", tc.name)
			continue
		}
		if got.Number < tc.want-0.001 || got.Number > tc.want+0.001 {
			t.Errorf("%s = %v, want %v", tc.name, got.Number, tc.want)
		}
	}
}

// ‼️ The AMBIGUOUS spellings are still refused.
//
// Adding `cos` beside `cos_deg` would hand the ambiguity straight back, which is
// the whole thing the naming was chosen to avoid.
func TestExpression_BareTrigonometryIsStillAbsent(t *testing.T) {
	available := map[string]bool{}
	for _, n := range geometry.ExpressionFunctions() {
		available[n] = true
	}
	for _, name := range []string{"sin", "cos", "tan", "atan", "atan2", "radians", "degrees"} {
		if available[name] {
			t.Errorf("%q is callable in an expression. The convention it uses is unstated, "+
				"and an unstated convention produces a plausible wrong number rather than an "+
				"error — which is what naming the degree forms exists to prevent", name)
		}
	}
	doc := &geometry.Document{
		Units:      "mm",
		Parameters: []geometry.Parameter{{Name: "angle", Value: 20, Unit: "deg"}},
		Derived:    []geometry.Derived{{Name: "x", Expression: "cos(angle)"}},
	}
	if res := doc.Resolve(); res.OK() {
		t.Error("an expression calling bare cos() resolved")
	}
}

// A value that is not finite is a broken relationship, not a big number.
//
// tan_deg(90) is a vertical line; Go returns 1.6e16 for it rather than an error,
// and a document quietly carrying 1.6e16 mm would draw a part the size of the
// solar system.
func TestExpression_TrigonometryRefusesTheUndefined(t *testing.T) {
	for _, expr := range []string{"tan_deg(90)", "tan_deg(-90)", "tan_deg(270)"} {
		doc := &geometry.Document{
			Units:      "mm",
			Parameters: []geometry.Parameter{{Name: "a", Value: 1, Unit: "mm"}},
			Derived:    []geometry.Derived{{Name: "x", Expression: expr + " * a"}},
		}
		res := doc.Resolve()
		if res.OK() {
			t.Errorf("%s resolved to %v; the tangent of a right angle is undefined",
				expr, res.Values["x"].Number)
		}
	}
}
