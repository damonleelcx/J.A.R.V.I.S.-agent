package geometry

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Expressions: the relationship, recorded.
//
// # The failure this exists to prevent
//
// The 2026-09-05 kernel spike swept a parametric bracket through nine parameter
// changes. Eight held. The one that broke is the finding:
//
//	rib_length was an independent 52 mm while plate_size moved. At plate_size=50
//	the ribs overhung the plate, the fillet had no valid edge to sit on, and the
//	kernel refused the solid.
//
// Making rib_length DERIVED — `plate_size - 2 * fillet_radius` — held the same
// model across a 3.4x size range (35 mm to 120 mm), including the case that had
// broken. The conclusion, recorded in docs/spikes/2026-09-05-parametric-cad-kernel/:
//
//	Naming a parameter is not enough. What makes a model survive a change is the
//	RELATIONSHIP being the thing recorded.
//
// So a derived value is stored as its EXPRESSION and never as a number. This
// file is what turns one back into a number, and — the half that matters more —
// what lets everything downstream see WHICH parameters a figure actually came
// from.
//
// # Why a parser and not anything that evaluates code
//
// These expressions arrive from a language model, over the network, inside a
// document that is also persisted and replayed. Handing that string to any
// general evaluator would make the model contract an execution surface. The
// grammar below is closed, total, and has no way to name anything outside the
// document: identifiers resolve against this document's own parameters or they
// fail.
//
// # What is deliberately NOT here
//
// AMBIGUOUS TRIGONOMETRY. Bare sin/cos/tan are absent, and their absence is
// still the feature. Half the engineering world writes them in degrees and half
// in radians, the two agree only at zero, and a wrong one produces a plausible
// number rather than an error — precisely the failure mode this phase exists to
// catch. A function whose meaning depends on a convention nobody stated would be
// building that in.
//
// # ‼️ What answered that, 2026-09-10, and why the argument above still holds
//
// The convention is now stated IN THE NAME. `cos_deg` cannot be read two ways,
// so there is no unstated convention left to get wrong, and the ambiguous
// spellings are deliberately NOT offered — adding `cos` beside `cos_deg` would
// hand the ambiguity straight back.
//
// Degrees rather than radians because this is a drawing: a pressure angle is
// 20°, a draft is 3°, a chamfer is 45°. Nobody writes a document in radians, and
// the one place radians are wanted — inside a script — has Python's own `math`.
//
// It was added because the absence had a measured cost. A model asked for a gear
// declares `base_radius = pitch_radius * cos(pressure_angle)`, which could not
// resolve, so the value was not in scope for the part's script, so the script
// naming it was refused: six of nine failures in one live run. Carrying "the
// resolved length as a parameter instead" is what the old advice said, and a
// model does not do it — it writes the relationship, which is the whole point of
// Derived existing.
//
// UNIT ALGEBRA. Resolve tracks the unit a derived value inherits (see
// resolution.go) but does not multiply or divide units. `a * b` of two lengths
// is reported as a length, which is wrong for an area — and is honest about
// being a single declared unit per document, which is what Document.Units is.

// exprFunc is one built-in, with its arity stated so a wrong call is an error
// rather than a silently dropped argument.
//
// A table rather than a switch: the set of things an expression may invoke is a
// security boundary as much as a feature list, and a boundary should be readable
// in one place.
var exprFuncs = map[string]struct {
	Arity int
	Apply func([]float64) (float64, error)
}{
	"sqrt": {1, func(a []float64) (float64, error) {
		if a[0] < 0 {
			return 0, fmt.Errorf("sqrt of a negative number (%g)", a[0])
		}
		return math.Sqrt(a[0]), nil
	}},
	"abs":   {1, func(a []float64) (float64, error) { return math.Abs(a[0]), nil }},
	"floor": {1, func(a []float64) (float64, error) { return math.Floor(a[0]), nil }},
	"ceil":  {1, func(a []float64) (float64, error) { return math.Ceil(a[0]), nil }},
	"round": {1, func(a []float64) (float64, error) { return math.Round(a[0]), nil }},
	"min":   {2, func(a []float64) (float64, error) { return math.Min(a[0], a[1]), nil }},
	"max":   {2, func(a []float64) (float64, error) { return math.Max(a[0], a[1]), nil }},

	// Trigonometry, in DEGREES, and saying so in the name. See the header on why
	// the bare spellings are not here and must not be added.
	//
	// finite() guards each one, because the interesting failures here are values
	// rather than errors: tan_deg(90) is a vertical line and Go returns 1.6e16
	// for it rather than an error, and a document that quietly carried 1.6e16 mm
	// would draw a part the size of the solar system. A number that is not
	// finite is a broken relationship, not a big one.
	"sin_deg": {1, func(a []float64) (float64, error) {
		return finite("sin_deg", math.Sin(a[0]*math.Pi/180))
	}},
	"cos_deg": {1, func(a []float64) (float64, error) {
		return finite("cos_deg", math.Cos(a[0]*math.Pi/180))
	}},
	"tan_deg": {1, func(a []float64) (float64, error) {
		// 90° and every 180° after it. Named, because "tan_deg produced +Inf"
		// tells a reader nothing about their document and this does.
		if math.Mod(math.Abs(a[0])-90, 180) == 0 {
			return 0, fmt.Errorf("tan_deg(%g) has no value: the tangent of a right angle is "+
				"undefined", a[0])
		}
		return finite("tan_deg", math.Tan(a[0]*math.Pi/180))
	}},
	// The inverse, so an angle can be DERIVED from two lengths — a taper, a
	// chamfer, the lead of a helix. Two arguments rather than one because
	// atan2 is the form that works in every quadrant and needs no sign rules.
	"atan2_deg": {2, func(a []float64) (float64, error) {
		return finite("atan2_deg", math.Atan2(a[0], a[1])*180/math.Pi)
	}},
}

// finite refuses a result that is not a number a document can carry.
func finite(name string, v float64) (float64, error) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("%s produced a value that is not a finite number", name)
	}
	return v, nil
}

// ExpressionFunctions is every function a derived expression may call, sorted.
//
// Exported so the model's contract can be BUILT from this table rather than
// repeating it. The two were written separately and the contract said "there is
// no sine or cosine here" for as long as that was true and would have gone on
// saying it — a rule the model reads, describing a grammar that had changed
// underneath it, is worse than no rule.
func ExpressionFunctions() []string {
	out := make([]string, 0, len(exprFuncs))
	for name := range exprFuncs {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// exprConsts are the names that resolve without a parameter.
//
// pi earns its place because a bolt circle is the commonest derived figure in
// this domain and writing 3.14159 by hand is how precision quietly leaves a
// document. A parameter that shadows one of these is reported as a problem
// rather than silently winning or silently losing — see Resolve.
var exprConsts = map[string]float64{"pi": math.Pi}

// exprNode is one node of a parsed expression.
//
// A single struct with a kind tag rather than an interface: the tree is tiny,
// total, and never extended by anything outside this file, so the type switch an
// interface would buy is cost without a payer.
type exprNode struct {
	kind  string // "num", "ref", "call", "binary", "neg"
	num   float64
	name  string      // ref name, call name, or binary operator
	args  []*exprNode // call arguments, or the two operands of a binary
	child *exprNode   // negation operand
}

// parseExpression turns source text into a tree, or says where it gave up.
//
// The position is included in every syntax error because these strings are
// authored by a model and read by a person trying to work out what it meant;
// "unexpected ')' at 24" is actionable and "syntax error" is not.
func parseExpression(src string) (*exprNode, error) {
	toks, err := lexExpression(src)
	if err != nil {
		return nil, err
	}
	if len(toks) == 0 {
		return nil, fmt.Errorf("the expression is empty")
	}
	p := &exprParser{toks: toks}
	n, err := p.parseSum()
	if err != nil {
		return nil, err
	}
	if !p.done() {
		return nil, fmt.Errorf("unexpected %q at position %d", p.peek().text, p.peek().pos)
	}
	return n, nil
}

// References returns every identifier the expression reads, sorted and
// deduplicated.
//
// This is the part the honesty machinery uses. A derived figure is only as
// trustworthy as the parameters underneath it, so provenance has to travel along
// these edges — see standards.go, which propagates a "quoted from a standard"
// claim from a parameter to every derived value that reads it.
// unitConsuming are the functions whose RESULT does not carry the unit of what
// they read.
//
// # Why this is not the beginning of unit algebra
//
// The header says this file does not multiply or divide units, and that is still
// true: `a * b` of two lengths is still reported as a length. This is a much
// smaller statement — that a trigonometric function EATS its argument's unit. A
// cosine of an angle is a ratio and carries no unit at all; an atan2 of two
// lengths is an angle and carries neither of theirs.
//
// It is here because without it degree trigonometry is unusable. The live case:
//
//	base_radius = pitch_radius * cos_deg(pressure_angle)
//
// reads a parameter in mm and one in deg, and inheritedUnit reported "mixes
// units: deg and mm" and refused the whole value — so adding cos_deg without
// this would have moved the failure rather than removed it, which the fence
// caught on the first run.
//
// Only unit INHERITANCE is affected. References() is untouched, so the
// dependency edges are unchanged and a standards claim still propagates along
// them: pressure_angle is still a thing base_radius depends on, and is still
// checkable, it just does not lend it "deg".
var unitConsuming = map[string]bool{
	"sin_deg": true, "cos_deg": true, "tan_deg": true, "atan2_deg": true,
}

// UnitReferences are the references whose unit this expression INHERITS.
//
// The same walk as References, skipping the arguments of a unit-consuming
// function. A value that reads nothing but those inherits no unit and is
// reported without one, which is the honest answer: nobody said what a ratio is
// measured in, because nothing is.
func (n *exprNode) UnitReferences() []string {
	seen := map[string]bool{}
	var walk func(*exprNode)
	walk = func(x *exprNode) {
		if x == nil {
			return
		}
		if x.kind == "ref" {
			seen[x.name] = true
		}
		if x.kind == "call" && unitConsuming[x.name] {
			return
		}
		walk(x.child)
		for _, a := range x.args {
			walk(a)
		}
	}
	walk(n)
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (n *exprNode) References() []string {
	seen := map[string]bool{}
	var walk func(*exprNode)
	walk = func(x *exprNode) {
		if x == nil {
			return
		}
		if x.kind == "ref" {
			seen[x.name] = true
		}
		walk(x.child)
		for _, a := range x.args {
			walk(a)
		}
	}
	walk(n)
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Eval computes the value, resolving identifiers through lookup.
//
// lookup returning false is an error rather than a zero: a missing parameter
// that evaluated to 0 would turn a broken document into a plausible one, which
// is the whole class of failure this package is built against.
func (n *exprNode) Eval(lookup func(string) (float64, bool)) (float64, error) {
	if n == nil {
		return 0, fmt.Errorf("the expression is empty")
	}
	switch n.kind {
	case "num":
		return n.num, nil
	case "ref":
		if v, ok := exprConsts[n.name]; ok {
			return v, nil
		}
		v, ok := lookup(n.name)
		if !ok {
			return 0, fmt.Errorf("%q is not a parameter of this document", n.name)
		}
		return v, nil
	case "neg":
		v, err := n.child.Eval(lookup)
		return -v, err
	case "call":
		fn, ok := exprFuncs[n.name]
		if !ok {
			return 0, fmt.Errorf("%q is not a function this build understands", n.name)
		}
		if len(n.args) != fn.Arity {
			return 0, fmt.Errorf("%s takes %d argument(s), got %d", n.name, fn.Arity, len(n.args))
		}
		vals := make([]float64, len(n.args))
		for i, a := range n.args {
			v, err := a.Eval(lookup)
			if err != nil {
				return 0, err
			}
			vals[i] = v
		}
		return fn.Apply(vals)
	case "binary":
		l, err := n.args[0].Eval(lookup)
		if err != nil {
			return 0, err
		}
		r, err := n.args[1].Eval(lookup)
		if err != nil {
			return 0, err
		}
		switch n.name {
		case "+":
			return l + r, nil
		case "-":
			return l - r, nil
		case "*":
			return l * r, nil
		case "/":
			if r == 0 {
				return 0, fmt.Errorf("division by zero")
			}
			return l / r, nil
		case "^":
			return math.Pow(l, r), nil
		}
	}
	return 0, fmt.Errorf("unsupported expression node %q", n.kind)
}

// --- lexer ---

type exprToken struct {
	kind string // "num", "ident", "op", "(", ")", ","
	text string
	num  float64
	pos  int
}

func lexExpression(src string) ([]exprToken, error) {
	var out []exprToken
	r := []rune(src)
	for i := 0; i < len(r); {
		c := r[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c >= '0' && c <= '9' || c == '.':
			j := i
			for j < len(r) && (r[j] >= '0' && r[j] <= '9' || r[j] == '.') {
				j++
			}
			text := string(r[i:j])
			v, err := strconv.ParseFloat(text, 64)
			if err != nil {
				return nil, fmt.Errorf("%q at position %d is not a number", text, i)
			}
			out = append(out, exprToken{kind: "num", text: text, num: v, pos: i})
			i = j
		case c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
			j := i
			for j < len(r) && (r[j] == '_' || r[j] >= 'a' && r[j] <= 'z' ||
				r[j] >= 'A' && r[j] <= 'Z' || r[j] >= '0' && r[j] <= '9') {
				j++
			}
			out = append(out, exprToken{kind: "ident", text: strings.ToLower(string(r[i:j])), pos: i})
			i = j
		case strings.ContainsRune("+-*/^", c):
			out = append(out, exprToken{kind: "op", text: string(c), pos: i})
			i++
		case c == '(' || c == ')' || c == ',':
			out = append(out, exprToken{kind: string(c), text: string(c), pos: i})
			i++
		default:
			// Named rather than skipped. A stray character silently dropped is
			// how "plate_size ; 2" becomes a valid-looking halving.
			return nil, fmt.Errorf("unexpected character %q at position %d", string(c), i)
		}
	}
	return out, nil
}

// --- parser: precedence climbing over the closed grammar above ---

type exprParser struct {
	toks []exprToken
	i    int
}

func (p *exprParser) done() bool      { return p.i >= len(p.toks) }
func (p *exprParser) peek() exprToken { return p.toks[p.i] }

func (p *exprParser) parseSum() (*exprNode, error) {
	n, err := p.parseProduct()
	if err != nil {
		return nil, err
	}
	for !p.done() && p.peek().kind == "op" && (p.peek().text == "+" || p.peek().text == "-") {
		op := p.peek().text
		p.i++
		rhs, err := p.parseProduct()
		if err != nil {
			return nil, err
		}
		n = &exprNode{kind: "binary", name: op, args: []*exprNode{n, rhs}}
	}
	return n, nil
}

func (p *exprParser) parseProduct() (*exprNode, error) {
	n, err := p.parsePower()
	if err != nil {
		return nil, err
	}
	for !p.done() && p.peek().kind == "op" && (p.peek().text == "*" || p.peek().text == "/") {
		op := p.peek().text
		p.i++
		rhs, err := p.parsePower()
		if err != nil {
			return nil, err
		}
		n = &exprNode{kind: "binary", name: op, args: []*exprNode{n, rhs}}
	}
	return n, nil
}

// parsePower is right-associative, so 2^3^2 is 2^(3^2) as every calculator and
// every engineering text writes it.
func (p *exprParser) parsePower() (*exprNode, error) {
	base, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	if !p.done() && p.peek().kind == "op" && p.peek().text == "^" {
		p.i++
		exp, err := p.parsePower()
		if err != nil {
			return nil, err
		}
		return &exprNode{kind: "binary", name: "^", args: []*exprNode{base, exp}}, nil
	}
	return base, nil
}

func (p *exprParser) parseUnary() (*exprNode, error) {
	if !p.done() && p.peek().kind == "op" && (p.peek().text == "-" || p.peek().text == "+") {
		op := p.peek().text
		p.i++
		operand, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		if op == "-" {
			return &exprNode{kind: "neg", child: operand}, nil
		}
		return operand, nil
	}
	return p.parsePrimary()
}

func (p *exprParser) parsePrimary() (*exprNode, error) {
	if p.done() {
		return nil, fmt.Errorf("the expression ends where a value was expected")
	}
	t := p.peek()
	switch t.kind {
	case "num":
		p.i++
		return &exprNode{kind: "num", num: t.num}, nil
	case "ident":
		p.i++
		if !p.done() && p.peek().kind == "(" {
			p.i++
			var args []*exprNode
			if !p.done() && p.peek().kind != ")" {
				for {
					a, err := p.parseSum()
					if err != nil {
						return nil, err
					}
					args = append(args, a)
					if p.done() || p.peek().kind != "," {
						break
					}
					p.i++
				}
			}
			if p.done() || p.peek().kind != ")" {
				return nil, fmt.Errorf("%s( at position %d is never closed", t.text, t.pos)
			}
			p.i++
			return &exprNode{kind: "call", name: t.text, args: args}, nil
		}
		return &exprNode{kind: "ref", name: t.text}, nil
	case "(":
		p.i++
		n, err := p.parseSum()
		if err != nil {
			return nil, err
		}
		if p.done() || p.peek().kind != ")" {
			return nil, fmt.Errorf("the bracket opened at position %d is never closed", t.pos)
		}
		p.i++
		return n, nil
	}
	return nil, fmt.Errorf("unexpected %q at position %d", t.text, t.pos)
}
