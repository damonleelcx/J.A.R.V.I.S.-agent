package geometry

import (
	"fmt"
	"strings"
)

// Pattern offsets and a polar pattern's angle bound to parameters.
//
// # The problem this solves
//
// 2026-09-15 (bound child positions, PR 112) kept a child's position written with a
// parameter's name as its "position_from", so a respec moved it. Its own list left
// the other half of a tree's dimensions typed as numbers: a pattern's step. A row of
// bolts at bolt_pitch, or a fan of blades over fan_sweep, stayed at the old pitch
// when the parameter changed, while every definition bound to it moved — the same
// failure as a child placed at 1350 where half_wheelbase is 1350.
//
// # How, and why the same way as a position
//
// A pattern carries "offset_from", "row_offset_from" and "column_offset_from" (axes
// "x", "y", "z", exactly like position_from) and "angle_from" (one expression, in
// degrees, like "angle"). Bind evaluates them into the numbers beside them, so
// expansion, the kernel request and forge3d.js read the numbers they always read, and
// a respec re-binds and the copies move. The number is still stored: it is what is
// drawn when an expression cannot be read, and a stated number that disagrees with
// its own expression is reported in the words a position's is.
//
// # Why not count, about or a path
//
// "count" changes how many parts exist, which the occurrence limit and every
// comparison count; a respec that multiplied it would add parts nobody placed. "about"
// is an axis name, not a number. A path pattern's points are refused as expressions
// by name (pattern.go), as before. Each is a binding nobody has needed yet.
//
// # One table
//
// patternBindings is read by the binder, by clone, by the agent's repair of a reply
// (which fields may carry a parameter's name) and by the contract (PatternBindingGuide),
// so what the model is taught is what is bound.
// Fence: TestPatternBinding_TheTableIsWhatTheBinderReads.

// PatternBinding names one pattern field that may be bound to parameters.
type PatternBinding struct {
	// Field is the number's JSON key, and From the binding's.
	Field, From string
	// Kind is the pattern kind that reads it.
	Kind string
	// Vector is true for an [x, y, z] step bound per axis, false for one number.
	Vector bool
}

type patternBinding struct {
	PatternBinding
	vector func(p *Pattern) (*[]float64, *map[string]string)
	number func(p *Pattern) (*float64, *string)
}

var patternBindings = []patternBinding{
	{PatternBinding{Field: "offset", From: "offset_from", Kind: "linear", Vector: true},
		func(p *Pattern) (*[]float64, *map[string]string) { return &p.Offset, &p.OffsetFrom }, nil},
	{PatternBinding{Field: "row_offset", From: "row_offset_from", Kind: "grid", Vector: true},
		func(p *Pattern) (*[]float64, *map[string]string) { return &p.RowOffset, &p.RowOffsetFrom }, nil},
	{PatternBinding{Field: "column_offset", From: "column_offset_from", Kind: "grid", Vector: true},
		func(p *Pattern) (*[]float64, *map[string]string) { return &p.ColumnOffset, &p.ColumnOffsetFrom }, nil},
	{PatternBinding{Field: "angle", From: "angle_from", Kind: "polar"},
		nil, func(p *Pattern) (*float64, *string) { return &p.Angle, &p.AngleFrom }},
}

// PatternBindings is every pattern field that may be bound, in the contract's order.
func PatternBindings() []PatternBinding {
	out := make([]PatternBinding, len(patternBindings))
	for i, b := range patternBindings {
		out[i] = b.PatternBinding
	}
	return out
}

// PatternBindingGuide is the contract's paragraph on binding a pattern, rendered from
// patternBindings so it cannot teach a field the binder does not read.
func PatternBindingGuide() string {
	var fields, froms []string
	var vec, num *PatternBinding
	for i := range patternBindings {
		b := &patternBindings[i].PatternBinding
		fields = append(fields, fmt.Sprintf("%q (%s)", b.Field, b.Kind))
		froms = append(froms, fmt.Sprintf("%q", b.From))
		if b.Vector && vec == nil {
			vec = b
		}
		if !b.Vector && num == nil {
			num = b
		}
	}
	var examples []string
	if vec != nil {
		examples = append(examples, fmt.Sprintf(`{"kind": %q, "count": 4, %q: [0, 0, "bolt_pitch"]}`, vec.Kind, vec.Field))
	}
	if num != nil {
		examples = append(examples, fmt.Sprintf(`{"kind": %q, "count": 5, "about": "y", %q: "fan_sweep"}`, num.Kind, num.Field))
	}
	return fmt.Sprintf(`  A pattern's step follows a parameter the way a position does: write the
  parameter's name in its %s, as in %s,
  and FORGE works it out from the parameters and keeps it as its %s, so the
  copies follow the parameter when it changes. An angle is in degrees. "count"
  and "about" are read as written, and a path pattern's points are numbers.
`, joinList(fields), strings.Join(examples, " or "), joinList(froms))
}

// joinList is "a", "a or b", or "a, b or c".
func joinList(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " or " + items[len(items)-1]
}

// PatternStep is one [x, y, z] step of a pattern as it stands, with its binding.
type PatternStep struct {
	Field  string
	Values []float64
	From   map[string]string
}

// Steps is p's [x, y, z] steps that the table names, present or not, in its order: what
// the agent's literal note reads a pattern by. The slices and maps are p's own; a
// caller only reads them.
func (p *Pattern) Steps() []PatternStep {
	if p == nil {
		return nil
	}
	var out []PatternStep
	for _, b := range patternBindings {
		if b.vector != nil {
			values, from := b.vector(p)
			out = append(out, PatternStep{Field: b.Field, Values: *values, From: *from})
		}
	}
	return out
}

// bound reports whether any of p's fields is bound. A nil pattern binds nothing.
func (p *Pattern) bound() bool {
	if p == nil {
		return false
	}
	for _, b := range patternBindings {
		if b.vector != nil {
			if _, from := b.vector(p); len(*from) > 0 {
				return true
			}
		} else if _, from := b.number(p); strings.TrimSpace(*from) != "" {
			return true
		}
	}
	return false
}

// clone copies p and every slice and map in it, so a bind or a respec of the copy
// never writes into the pattern it was made from. nil stays nil.
func (p *Pattern) clone() *Pattern {
	if p == nil {
		return nil
	}
	q := *p
	q.Offset = append([]float64(nil), p.Offset...)
	q.RowOffset = append([]float64(nil), p.RowOffset...)
	q.ColumnOffset = append([]float64(nil), p.ColumnOffset...)
	q.Path = append([]Point(nil), p.Path...)
	q.OffsetFrom = copyStringMap(p.OffsetFrom)
	q.RowOffsetFrom = copyStringMap(p.RowOffsetFrom)
	q.ColumnOffsetFrom = copyStringMap(p.ColumnOffsetFrom)
	return &q
}

// bindPattern writes p's bound fields from their expressions. p must be the caller's
// own copy. A field is named "<label> pattern offset" and so on, and a broken binding
// keeps its number, in a position's words.
func bindPattern(p *Pattern, label string, lookup func(string) (float64, bool), compareToAuthored bool) []Problem {
	var problems []Problem
	for _, b := range patternBindings {
		what := "pattern " + b.Field
		if b.vector != nil {
			values, from := b.vector(p)
			problems = append(problems, bindVector(values, *from, label, what, lookup, compareToAuthored)...)
			continue
		}
		value, from := b.number(p)
		expr := strings.TrimSpace(*from)
		if expr == "" {
			continue
		}
		v, prob := evalBinding(expr, label, what, lookup)
		if prob != nil {
			problems = append(problems, *prob)
			continue
		}
		if was := *value; compareToAuthored && !nearlyEqual(was, v) && was != 0 {
			problems = append(problems, Problem{
				Severity: Warning, Name: label,
				Detail: fmt.Sprintf("states %s = %g but its own expression %q works out "+
					"to %g; the expression was used", what, was, expr, v),
			})
		}
		*value = v
	}
	return problems
}
