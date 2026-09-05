package geometry

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

// Parameters: what a person can change, and what must follow when they do.
//
// # Why this is on the document and not somewhere else
//
// Document is already the model's contract AND the stored variant shape — one
// type, deliberately, so a replay cannot differ from what the person saw. A
// parametric document is still that document; the parameters are what the
// numbers inside it were derived FROM. Putting them anywhere else would create
// the second source of truth this package's own header warns about.
//
// # What the 2026-09-05 spike established
//
// Premise B asked qwen-plus for exactly this shape over three runs. The
// structure arrived every time: derived expressions rather than fixed numbers
// 3/3, a unit on every parameter 3/3, a source on every "standard" claim 3/3.
// One run produced, unprompted, the very relationship whose absence had broken
// Premise A's sweep:
//
//	rib_length = plate_height - 2 * edge_margin
//
// And the figures were wrong in all three: 42.3 mm was labelled a NEMA 17 bolt
// "diagonal" — it is the FRAME size — carrying `how: "standard"` and a citation.
// So the representation below is built on the assumption that a typed
// provenance field is a claim to be CHECKED, never a claim to be trusted. See
// standards.go, which reads these fields and propagates a recalled claim along
// the dependency edges Resolve computes.

// Provenance is how a parameter's value was arrived at.
//
// Two categories that the 2026-09-02 fabricated-figures bug showed were being
// laundered into one: a number FORGE chose, and a number FORGE recalled from a
// published standard. The first is an assumption and legitimately so; the second
// is a claim about the world this deployment cannot check.
type Provenance string

const (
	// ProvenanceUnstated is a parameter that did not say. Not an error, and not
	// silently promoted to "chosen": nobody said, and the reader is told that.
	ProvenanceUnstated Provenance = ""
	// Chosen is a value FORGE picked because nothing constrained it.
	Chosen Provenance = "chosen"
	// FromStandard is a value quoted from a published figure. The wire token is
	// "standard" because that is the token the spike measured the model
	// emitting, 3/3 with a source attached. Changing it here to something
	// tidier would be changing the contract away from the one thing about this
	// output that was actually verified.
	FromStandard Provenance = "standard"
)

// Parameter is one number a person could change.
type Parameter struct {
	Name  string  `json:"name"`
	Value float64 `json:"value"`
	// Unit is the AUTHORED unit string, kept as text rather than parsed into
	// the length Unit type.
	//
	// Why: a parameter is not always a length. rib_count has no unit, a draft
	// angle is in degrees, and ParseUnit knows only lengths — so forcing every
	// parameter through it would report "deg" and "count" as unrecognised and
	// bury the one case that matters (mm mixed with in) in noise. Resolve
	// compares these strings for agreement; Value.Quantity converts only when
	// the unit really is a length.
	Unit string `json:"unit"`
	// How the value was arrived at, and where from. A FromStandard parameter
	// with an empty Source is a claim with no provenance and Resolve says so.
	How    Provenance `json:"how,omitempty"`
	Source string     `json:"source,omitempty"`
}

// Derived is a value that must follow when another parameter changes.
//
// Stored as an EXPRESSION and never as a number. That is the whole finding of
// the 2026-09-05 spike: rib_length as an independent 52 mm broke the model at
// plate_size=50, and the identical model held across a 3.4x size range once the
// same value was written as plate_size - 2 * fillet_radius.
type Derived struct {
	Name       string `json:"name"`
	Expression string `json:"expression"`
	// Why states the relationship this keeps true. Prose, and load-bearing: an
	// expression says what is computed and only this says what it is FOR, which
	// is what a person needs to judge whether it is still right after a change.
	Why string `json:"why,omitempty"`
}

// Severity separates "this document does not resolve" from "this resolves and
// smells".
//
// Both are reported, and the distinction is kept because they are acted on
// differently: an Error means a number is missing, a Warning means a number is
// present and something about how it got there is worth a person's attention.
// Collapsing them would either hide a broken document among smells or make
// every smell look like a failure.
type Severity string

const (
	Error   Severity = "error"
	Warning Severity = "warning"
)

// Problem is one thing wrong with a document's parameters.
type Problem struct {
	Severity Severity `json:"severity"`
	// Name is the parameter or derived value it concerns, empty for a
	// document-level problem.
	Name   string `json:"name,omitempty"`
	Detail string `json:"detail"`
}

// Value is one resolved number.
type Value struct {
	Name   string  `json:"name"`
	Number float64 `json:"number"`
	// Unit is the authored unit for a parameter, or the unit a derived value
	// INHERITS when every parameter it reads agrees on one. Empty means either
	// nobody said or they disagreed — and the disagreement is reported as a
	// Problem rather than resolved by picking one.
	Unit string `json:"unit,omitempty"`
	// Expression is the source text for a derived value, empty for a parameter.
	// Kept so that everything downstream can show the RELATIONSHIP rather than
	// only the number it currently produces.
	Expression string `json:"expression,omitempty"`
	// Depends is every PARAMETER this value transitively rests on, sorted. For
	// a parameter it is the parameter itself.
	//
	// This is the edge provenance travels along. A derived figure is exactly as
	// checkable as the parameters underneath it, so standards.go asks this
	// field which of them were quoted from a standard — which is how
	// bolt_pitch = bolt_diagonal / sqrt(2) becomes a NEMA 17 claim carrying
	// 29.91 mm, and gets compared against the published 31 mm.
	Depends []string `json:"depends,omitempty"`
}

// Quantity converts a resolved value to a formattable length, when it is one.
//
// Returns false for a count, an angle, a ratio or a mixed-unit result. A caller
// that wants to print such a value must decide what to print, rather than being
// handed a Quantity that claims a unit nobody established.
func (v Value) Quantity() (Quantity, bool) {
	u, ok := ParseUnit(v.Unit)
	if !ok {
		return Quantity{}, false
	}
	return NewQuantity(v.Number, u), true
}

// Resolution is every number a parametric document produces, and everything
// wrong with it.
type Resolution struct {
	Values map[string]Value `json:"values"`
	// Order is the derived names in the order they were evaluated. Deterministic,
	// so a test can assert on it and a person reading the panel sees the chain
	// in the order it actually resolves rather than the order it was authored.
	Order    []string  `json:"order,omitempty"`
	Problems []Problem `json:"problems,omitempty"`
}

// OK reports whether every declared value resolved to a number.
func (r Resolution) OK() bool {
	for _, p := range r.Problems {
		if p.Severity == Error {
			return false
		}
	}
	return true
}

// paramNameRE is the identifier shape the expression lexer can actually read
// back. A name outside it can be declared but never referenced, so declaring one
// is reported rather than accepted — a parameter nothing can read is not a
// parameter, it is a number with a label.
var paramNameRE = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// Resolve computes every derived value and reports everything wrong.
//
// # Why this returns problems instead of an error
//
// The same stance as the rest of this package: a document with one broken
// expression still contains the other nine values and the person is mid-design.
// Refusing the whole thing would throw away work to punish a typo. So every
// value that CAN resolve does, every one that cannot is absent rather than zero,
// and the reasons are carried alongside where a reader sees them.
//
// A missing value is deliberately ABSENT and never zero. A parameter that
// silently evaluated to 0 turns a broken document into a plausible one, which is
// the class of failure this whole phase exists against.
func (d *Document) Resolve() Resolution {
	res := Resolution{Values: map[string]Value{}}
	if d == nil {
		return res
	}
	add := func(sev Severity, name, format string, args ...any) {
		res.Problems = append(res.Problems, Problem{
			Severity: sev, Name: name, Detail: fmt.Sprintf(format, args...),
		})
	}

	// --- parameters ---
	declared := map[string]string{} // name -> "parameter" | "derived"
	for _, p := range d.Parameters {
		name := strings.ToLower(strings.TrimSpace(p.Name))
		switch {
		case name == "":
			add(Error, "", "a parameter has no name")
			continue
		case !paramNameRE.MatchString(name):
			add(Error, name, "%q is not a name an expression can refer to; "+
				"use lower-case letters, digits and underscores", p.Name)
			continue
		case declared[name] != "":
			add(Error, name, "declared more than once; a name can only mean one number")
			continue
		}
		if _, isConst := exprConsts[name]; isConst {
			// Loud rather than resolved by precedence. Either answer — the
			// parameter wins or the constant does — is a silent reinterpretation
			// of every expression that reads the name.
			add(Error, name, "collides with the built-in constant %q; rename the parameter", name)
			continue
		}
		if math.IsNaN(p.Value) || math.IsInf(p.Value, 0) {
			add(Error, name, "value is not a finite number")
			continue
		}
		// There is deliberately NO "this parameter has no unit" warning.
		//
		// WRK-05 is emphatic that a dimension without its unit will be read in
		// the wrong one, so the check looks obvious. It cannot be written here:
		// a parameter is not always a length, and rib_count = 2 and hole_ratio
		// = 0.6 are CORRECT with no unit. Nothing at this level distinguishes
		// them from a length that forgot one, so the warning would fire on
		// correct documents — and a warning that fires on correct input is how
		// a panel stops being read, which is the reasoning standards.go already
		// records for not scanning NotVerified. Telling the two apart needs
		// dimensional analysis, which expression.go states it does not do.
		if p.How == FromStandard && strings.TrimSpace(p.Source) == "" {
			// A warning, not an error: the number is usable. But "quoted from a
			// standard" with nothing naming the standard is the exact shape of
			// the 2026-09-02 fabrication, wearing provenance it does not have.
			add(Warning, name, "is marked as quoted from a standard but names no source")
		}
		declared[name] = "parameter"
		res.Values[name] = Value{
			Name: name, Number: p.Value,
			Unit:    strings.TrimSpace(p.Unit),
			Depends: []string{name},
		}
	}

	// --- derived: parse first, so a cycle is found before anything evaluates ---
	type pending struct {
		name string
		expr string
		node *exprNode
		refs []string
	}
	var queue []pending
	for _, dv := range d.Derived {
		name := strings.ToLower(strings.TrimSpace(dv.Name))
		switch {
		case name == "":
			add(Error, "", "a derived value has no name")
			continue
		case !paramNameRE.MatchString(name):
			add(Error, name, "%q is not a name an expression can refer to; "+
				"use lower-case letters, digits and underscores", dv.Name)
			continue
		case declared[name] == "parameter":
			add(Error, name, "is both a parameter and a derived value; "+
				"one name cannot have two sources of truth")
			continue
		case declared[name] == "derived":
			add(Error, name, "declared more than once; a name can only mean one number")
			continue
		}
		node, err := parseExpression(dv.Expression)
		if err != nil {
			add(Error, name, "expression %q cannot be read: %v", dv.Expression, err)
			continue
		}
		refs := node.References()
		free := make([]string, 0, len(refs))
		for _, r := range refs {
			if _, isConst := exprConsts[r]; !isConst {
				free = append(free, r)
			}
		}
		if len(free) == 0 {
			// The single most important check in this file. A derived value
			// that reads nothing is a FIXED NUMBER in a field whose entire
			// purpose is to hold a relationship — which is precisely what broke
			// the spike's sweep: rib_length sat at 52 mm while plate_size moved.
			add(Warning, name, "expression %q reads no parameter, so it is a fixed "+
				"number wearing a relationship's clothes; it will not follow when "+
				"anything else changes", dv.Expression)
		}
		declared[name] = "derived"
		queue = append(queue, pending{name: name, expr: dv.Expression, node: node, refs: free})
	}

	// --- evaluate in dependency order ---
	//
	// Repeated passes rather than a topological sort: the list is small, the
	// loop is obviously terminating, and "nothing resolved this pass" is exactly
	// the condition that separates a cycle from a missing parameter without
	// needing a second algorithm to tell them apart.
	remaining := queue
	for len(remaining) > 0 {
		var stuck []pending
		progress := false
		for _, p := range remaining {
			ready := true
			for _, r := range p.refs {
				if _, ok := res.Values[r]; !ok {
					ready = false
					break
				}
			}
			if !ready {
				stuck = append(stuck, p)
				continue
			}
			num, err := p.node.Eval(func(n string) (float64, bool) {
				v, ok := res.Values[n]
				return v.Number, ok
			})
			if err != nil {
				add(Error, p.name, "expression %q does not evaluate: %v", p.expr, err)
				progress = true // it is settled, even though it failed
				continue
			}
			if math.IsNaN(num) || math.IsInf(num, 0) {
				add(Error, p.name, "expression %q does not produce a finite number", p.expr)
				progress = true
				continue
			}
			unit, mixed := inheritedUnit(p.refs, res.Values)
			if mixed != "" {
				add(Error, p.name, "mixes units: %s. A derived value cannot inherit a "+
					"unit from parameters that disagree, so it is reported without one", mixed)
			}
			res.Values[p.name] = Value{
				Name: p.name, Number: num, Unit: unit,
				Expression: p.expr,
				Depends:    dependenciesOf(p.refs, res.Values),
			}
			res.Order = append(res.Order, p.name)
			progress = true
		}
		if !progress {
			// Nothing moved: every survivor is waiting on something that will
			// never arrive. Each is named with what it is waiting for, because
			// "there is a cycle" is not something a person can act on.
			for _, p := range stuck {
				var missing []string
				for _, r := range p.refs {
					if _, ok := res.Values[r]; !ok {
						missing = append(missing, r)
					}
				}
				sort.Strings(missing)
				add(Error, p.name, "cannot resolve: it waits on %s, which %s never resolves "+
					"(an undeclared parameter, or a cycle)",
					strings.Join(missing, ", "), plural(len(missing)))
			}
			break
		}
		remaining = stuck
	}

	sortProblems(res.Problems)
	return res
}

func plural(n int) string {
	if n == 1 {
		return "itself"
	}
	return "themselves"
}

// inheritedUnit is unit AGREEMENT, not unit algebra.
//
// A derived value takes the unit its inputs share. It does NOT multiply or
// divide units: area is not modelled, because Document declares one unit for the
// whole assembly and pretending otherwise here would be a second, richer unit
// system that nothing else in the package speaks. What this does buy is the one
// case that silently destroys a part — millimetres mixed with inches — reported
// rather than resolved by picking a winner.
//
// Values with no unit (a count, a ratio, a bare multiplier) do not vote. That is
// what makes plate_size - 2 * fillet_radius inherit mm rather than nothing.
func inheritedUnit(refs []string, values map[string]Value) (unit string, mixed string) {
	seen := map[string]bool{}
	var units []string
	for _, r := range refs {
		v, ok := values[r]
		if !ok || v.Unit == "" {
			continue
		}
		key := strings.ToLower(v.Unit)
		if !seen[key] {
			seen[key] = true
			units = append(units, v.Unit)
		}
	}
	switch len(units) {
	case 0:
		return "", ""
	case 1:
		return units[0], ""
	default:
		sort.Strings(units)
		return "", strings.Join(units, " and ")
	}
}

// dependenciesOf collapses a derived value's references to the PARAMETERS
// underneath them, following through other derived values.
func dependenciesOf(refs []string, values map[string]Value) []string {
	seen := map[string]bool{}
	for _, r := range refs {
		v, ok := values[r]
		if !ok {
			continue
		}
		for _, d := range v.Depends {
			seen[d] = true
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sortProblems makes the list deterministic: errors before warnings, then by
// name, then by text. Two runs over the same document produce the same list, so
// a test can assert on it and a panel does not reshuffle itself between renders.
func sortProblems(p []Problem) {
	sort.SliceStable(p, func(i, j int) bool {
		if p[i].Severity != p[j].Severity {
			return p[i].Severity == Error
		}
		if p[i].Name != p[j].Name {
			return p[i].Name < p[j].Name
		}
		return p[i].Detail < p[j].Detail
	})
}
