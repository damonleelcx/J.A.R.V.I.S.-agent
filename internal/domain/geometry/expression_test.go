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

// The absence of trigonometry is a feature, and this is what keeps it absent.
//
// sin/cos/tan are written in degrees by half the engineering world and radians
// by the other half; the two agree only at zero, and the wrong one produces a
// plausible number rather than an error. Adding them without stating a
// convention would build the exact failure this phase exists to catch.
func TestExpression_TrigonometryIsRefusedOnPurpose(t *testing.T) {
	for _, fn := range []string{"sin", "cos", "tan", "asin", "atan2"} {
		_, res := evalOne(t, fn+"(a)")
		if res.OK() {
			t.Fatalf("%s() resolved; it must stay absent until a document can state "+
				"degrees or radians", fn)
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
