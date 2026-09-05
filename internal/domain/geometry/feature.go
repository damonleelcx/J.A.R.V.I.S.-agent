package geometry

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Features: the operations that make an assembly a PART.
//
// # What was missing
//
// Wave 14 gave this deployment a real CAD kernel and then asked it to build a
// bag of primitives. Every solid it produced was a box or a cylinder sitting
// next to another one — a genuine B-Rep, and not a thing anybody machines. The
// 2026-09-05 spike's own reference bracket needs two operations this vocabulary
// could not express:
//
//	Circle(motor_hole_dia / 2, mode=Mode.SUBTRACT)   ← four holes through a plate
//	fillet(rib_verticals, radius=rib_fillet)          ← a rounded edge
//
// A hole is not a part. It is the ABSENCE of one, and a document that can only
// add material can describe a bracket with no way to bolt it to anything.
//
// # Why a hole is an existing part used as a tool
//
// The obvious design gives a hole its own vocabulary — a position, a diameter, a
// depth, an axis. It was rejected: that is a second way to say "a cylinder
// somewhere", and the two would drift on the day somebody adds a size key to one
// of them. A cut names parts that already exist and are already placed, sized
// and bound to parameters like everything else. A hole that follows plate_size
// does so because it is a cylinder whose position_from says so, with no new
// machinery at all.
//
// The cost is that a tool part is DRAWN by the renderer, which has no boolean
// operations: on screen the four holes are four small cylinders standing in the
// plate rather than voids through it. That is a real divergence and it is
// labelled rather than hidden — see FeatureNotes.
//
// # Why edges are selected by a rule and never by an index
//
// The spike is explicit about this, and it is the difference between a
// parametric model and one that works once:
//
//	The edges are selected by a RULE (the vertical edges of the ribs) rather
//	than by index. An index would silently select a different edge the moment a
//	parameter changed, which is the failure mode that makes naive parametric
//	scripts break on their second run.
//
// So Edges is a name from a closed table and there is nowhere to write a number.

// Feature is one operation applied after the parts are placed.
type Feature struct {
	ID string `json:"id"`
	// Op is "cut", "fuse", "fillet" or "chamfer".
	Op string `json:"op"`
	// Of is the part the operation applies to. It survives into the file.
	Of string `json:"of"`
	// With are the parts used as tools — the material removed by a cut, or added
	// by a fuse. They are CONSUMED: a tool does not also appear as a solid of
	// its own, or the hole would be filled by the thing that made it.
	With []string `json:"with,omitempty"`
	// Radius is the fillet radius or the chamfer length. RadiusFrom is the same
	// number as an EXPRESSION and wins when both are given, for the reason
	// size_from wins over size: it is the relationship and the other is a
	// snapshot of it.
	Radius     float64 `json:"radius,omitempty"`
	RadiusFrom string  `json:"radius_from,omitempty"`
	// Edges names which edges a fillet or chamfer touches, from the closed table
	// below. Empty means every edge.
	Edges string `json:"edges,omitempty"`
	Note  string `json:"note,omitempty"`
}

// featureOps is the closed set of operations, with what each one requires.
//
// A table rather than a chain of conditions: what a document may ask the kernel
// to do is a question people ask directly, and adding an operation should be one
// row plus one branch in the sidecar.
var featureOps = map[string]struct {
	NeedsTools  bool
	NeedsRadius bool
}{
	"cut":     {NeedsTools: true},
	"fuse":    {NeedsTools: true},
	"fillet":  {NeedsRadius: true},
	"chamfer": {NeedsRadius: true},
}

// edgeRules is how a fillet says WHICH edges, without ever naming an index.
//
// Y is up in this system, so "vertical" is the Y axis. The names are the ones a
// person would use looking at the thing on screen, because that is who writes
// them.
var edgeRules = map[string]string{
	"":           "all",
	"all":        "all",
	"vertical":   "vertical",
	"horizontal": "horizontal",
	"top":        "top",
	"bottom":     "bottom",
}

// Operation is a feature with its numbers resolved and its names checked.
type Operation struct {
	ID     string   `json:"id"`
	Op     string   `json:"op"`
	Of     string   `json:"of"`
	With   []string `json:"with,omitempty"`
	Radius float64  `json:"radius,omitempty"`
	Edges  string   `json:"edges"`
}

// Operations resolves the document's features, and reports everything wrong.
//
// A feature that does not check out is DROPPED rather than approximated: an
// assembly missing a hole is wrong in a way a reader can see and is told about,
// and one where the hole landed somewhere else is wrong in a way nobody notices.
func (d *Document) Operations() ([]Operation, []Problem) {
	if d == nil || len(d.Features) == 0 {
		return nil, nil
	}
	res := d.Resolve()
	lookup := func(n string) (float64, bool) {
		v, ok := res.Values[n]
		return v.Number, ok
	}

	parts := map[string]bool{}
	for _, p := range d.Parts {
		parts[p.ID] = true
	}

	var problems []Problem
	add := func(name, format string, args ...any) {
		problems = append(problems, Problem{Severity: Error, Name: name,
			Detail: fmt.Sprintf(format, args...)})
	}

	var out []Operation
	consumed := map[string]string{} // part id -> the feature that consumed it
	produced := map[string]bool{}   // part id -> has been operated on
	seen := map[string]bool{}

	for i, f := range d.Features {
		name := strings.TrimSpace(f.ID)
		if name == "" {
			name = fmt.Sprintf("feature-%d", i+1)
		}
		if seen[name] {
			add(name, "is declared more than once; a feature id has to name one operation")
			continue
		}
		seen[name] = true

		op := strings.ToLower(strings.TrimSpace(f.Op))
		spec, known := featureOps[op]
		if !known {
			add(name, "%q is not an operation FORGE can perform; it can cut, fuse, fillet or chamfer", f.Op)
			continue
		}
		if !parts[f.Of] {
			add(name, "applies to %q, which is not a part of this assembly", f.Of)
			continue
		}
		if who, gone := consumed[f.Of]; gone {
			add(name, "applies to %q, which %s already consumed as a tool; a part cannot be "+
				"both the material removed and the thing it was removed from", f.Of, who)
			continue
		}

		tools := make([]string, 0, len(f.With))
		bad := false
		for _, t := range f.With {
			switch {
			case t == f.Of:
				add(name, "uses %q as a tool on itself, which would remove the whole part", t)
				bad = true
			case !parts[t]:
				add(name, "uses %q as a tool, which is not a part of this assembly", t)
				bad = true
			case consumed[t] != "":
				add(name, "uses %q, which %s already consumed; a tool is used once or the "+
					"second operation is cutting with something that no longer exists", t, consumed[t])
				bad = true
			case produced[t]:
				add(name, "uses %q as a tool, but an earlier feature has already operated on it; "+
					"cutting with a part that has itself been modified is an order nobody stated", t)
				bad = true
			default:
				tools = append(tools, t)
			}
		}
		if bad {
			continue
		}
		if spec.NeedsTools && len(tools) == 0 {
			add(name, "is a %s and names nothing to %s with", op, op)
			continue
		}
		if !spec.NeedsTools && len(tools) > 0 {
			add(name, "is a %s and does not take tools; remove \"with\"", op)
			continue
		}

		radius := f.Radius
		if expr := strings.TrimSpace(f.RadiusFrom); expr != "" {
			node, err := parseExpression(expr)
			if err != nil {
				add(name, "radius %q cannot be read: %v", expr, err)
				continue
			}
			value, err := node.Eval(lookup)
			if err != nil {
				add(name, "radius %q does not evaluate: %v", expr, err)
				continue
			}
			radius = value
		}
		if spec.NeedsRadius {
			if math.IsNaN(radius) || math.IsInf(radius, 0) || radius <= 0 {
				add(name, "is a %s and needs a radius greater than zero; got %g", op, radius)
				continue
			}
		}

		rule, ok := edgeRules[strings.ToLower(strings.TrimSpace(f.Edges))]
		if !ok {
			add(name, "selects edges by %q, which is not a rule FORGE knows; use all, vertical, "+
				"horizontal, top or bottom. There is deliberately no way to name an edge by "+
				"number: an index selects a different edge the moment a parameter changes", f.Edges)
			continue
		}

		for _, t := range tools {
			consumed[t] = name
		}
		produced[f.Of] = true
		out = append(out, Operation{ID: name, Op: op, Of: f.Of, With: tools,
			Radius: radius, Edges: rule})
	}
	sortProblems(problems)
	return out, problems
}

// ConsumedParts names the parts that exist only as tools.
//
// They are built and then used up, so nothing downstream should list them as
// parts of the assembly — and the renderer, which has no boolean operations,
// draws them anyway. FeatureNotes is what tells the reader about that.
func (d *Document) ConsumedParts() map[string]bool {
	ops, _ := d.Operations()
	out := map[string]bool{}
	for _, op := range ops {
		for _, t := range op.With {
			out[t] = true
		}
	}
	return out
}

// FeatureNotes says what the picture on screen does not show.
//
// # Why this exists rather than a fix
//
// The renderer draws primitives and has no boolean operations, so a part used to
// cut a hole appears as a small solid standing in the plate rather than as a void
// through it. The exported B-Rep has the hole; the viewport does not.
//
// That is a real divergence between two things this product shows the same
// person, and the rule here has never been to hide one — it is the same stance
// as "Drawn approximately" for shapes the renderer cannot draw. Saying it costs
// a sentence. Teaching the tessellator to do booleans costs a CSG engine in
// JavaScript, which is the kernel this deployment just stopped pretending it
// could do without.
func (d *Document) FeatureNotes() []string {
	ops, _ := d.Operations()
	if len(ops) == 0 {
		return nil
	}
	label := map[string]string{}
	for _, p := range d.Parts {
		label[p.ID] = p.Label()
	}
	var cuts, fuses, rounds []string
	for _, op := range ops {
		names := make([]string, 0, len(op.With))
		for _, t := range op.With {
			names = append(names, label[t])
		}
		switch op.Op {
		case "cut":
			cuts = append(cuts, fmt.Sprintf("%s from %s", strings.Join(names, ", "), label[op.Of]))
		case "fuse":
			fuses = append(fuses, fmt.Sprintf("%s into %s", strings.Join(names, ", "), label[op.Of]))
		case "fillet", "chamfer":
			rounds = append(rounds, fmt.Sprintf("%s (%s, %s edges)", label[op.Of], op.Op, op.Edges))
		}
	}
	var out []string
	if len(cuts) > 0 {
		sort.Strings(cuts)
		out = append(out, "The viewport draws solid primitives and cannot show a hole: "+
			strings.Join(cuts, "; ")+" is cut in the exported solid and drawn as a part standing "+
			"in it on screen. The exported file is the one with the hole.")
	}
	if len(fuses) > 0 {
		sort.Strings(fuses)
		out = append(out, "Fused in the exported solid and drawn as separate parts on screen: "+
			strings.Join(fuses, "; ")+".")
	}
	if len(rounds) > 0 {
		sort.Strings(rounds)
		out = append(out, "Rounded in the exported solid and drawn square on screen: "+
			strings.Join(rounds, "; ")+".")
	}
	return out
}
