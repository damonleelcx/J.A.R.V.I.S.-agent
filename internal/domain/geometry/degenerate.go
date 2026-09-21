package geometry

import (
	"fmt"
	"sort"
	"strings"
)

// A dimension that collapses the solid is refused, by name.
//
// # What happened
//
// A part with radius 0 built successfully and came back a solid of volume 0.
// OCCT accepts it, the mesh path agrees with OCCT, and the result is internally
// consistent all the way to the exported file — and meaningless. Issue 7.
//
// Volume 0 is not a shape. It is almost always an upstream mistake: a parameter
// that failed to resolve, an expression that evaluated to zero, a unit
// conversion that collapsed. Every one of those is worth surfacing at the moment
// the solid is built, which is the last point where the cause is still nearby.
// Accepting it instead means the failure travels — into the assembly, the
// overlay, the export — and surfaces somewhere the radius is no longer in view.
//
// # The open question the issue left, and the answer
//
// "Refuse, or refuse unless explicitly allowed? A zero-height sketch plane may
// be legitimate in a way a zero-radius cylinder is not."
//
// Neither, and the distinction is already in the vocabulary: it is refused
// PER SHAPE, from the table below, which lists only the dimensions that shape is
// actually BUILT FROM. A "plane" is width and depth and has no height in this
// contract, and a "section" is a drawing with no thickness and has no dimension
// of any kind — so neither can be refused for a dimension it never reads, and no
// escape hatch is needed to say so. A cylinder's radius, by contrast, is the
// whole of its cross-section, and zero is not a smaller one.
//
// The same reason keeps "radius_top" OUT of the table: a cylinder with a zero
// top radius is a cone, which is a real shape solid.go builds on purpose, and
// cone sets it to zero itself.
//
// # Why a table, read by the validator and by the contract
//
// One list. degenerateProblems refuses from it and CollapsingDimensionGuide
// teaches the model from it, so the sentence in the prompt cannot drift from the
// rule in the code — the arrangement retired.go, designwords.go and curve_guide.go
// already use here.
//
// Not everything is here. A pattern's "count" below 2 is already answered by
// pattern.go (it is placed once, and said so) and a shell or thicken feature's
// "thickness" at or below zero is already refused by feature.go:
// "is a %s and needs a thickness greater than zero". Repeating either would give
// one document two voices saying the same thing.

// collapsingDimensions is, per shape, every dimension that shape is built from
// and that cannot be zero or negative.
//
// Ordered, and each shape's keys ordered, so a part missing two of them is
// reported in the same order twice running.
var collapsingDimensions = []struct {
	Shape string
	Keys  []string
}{
	{"box", []string{"width", "height", "depth"}},
	{"plane", []string{"width", "depth"}},
	{"sphere", []string{"radius"}},
	{"cylinder", []string{"radius", "height"}},
	{"cone", []string{"radius", "height"}},
	// An extrusion's section is checked by profile.go, which needs three points
	// before it is an outline at all. What it does not check is how far the
	// outline is carried, which is this.
	{"extrusion", []string{"depth"}},
}

// collapsingKeys is the table, by shape.
func collapsingKeys(shape string) []string {
	for _, row := range collapsingDimensions {
		if row.Shape == shape {
			return row.Keys
		}
	}
	return nil
}

// CollapsingDimensionGuide is the paragraph the model is taught, printed from
// the table the refusal is written from.
//
// Fence: TestContract_TeachesTheCollapsingDimensionsFromTheValidatorsTable.
func CollapsingDimensionGuide() string {
	items := make([]string, 0, len(collapsingDimensions))
	for _, row := range collapsingDimensions {
		quoted := make([]string, 0, len(row.Keys))
		for _, k := range row.Keys {
			quoted = append(quoted, fmt.Sprintf("%q", k))
		}
		items = append(items, fmt.Sprintf("    %q — %s", row.Shape, strings.Join(quoted, ", ")))
	}
	return fmt.Sprintf(`  A dimension a shape is BUILT FROM may not be zero or negative:
%s
  Zero is not a small size — it is a solid of no volume, which the kernel will
  build and the file will carry, so FORGE refuses the part by the name of the
  dimension that collapsed instead. If a number you wrote came out at zero, the
  parameter or the expression behind it is what to fix. Dimensions a shape does
  not read are not checked: a "section" has no thickness and a "plane" has no
  height, and neither is an error.
`, strings.Join(items, "\n"))
}

// statedSize reads one dimension the way sizeOr reads it — the key itself, or
// the one synonym that can only mean it — and says which word it was read from.
// No default: a dimension nobody stated is one sizeOr fills in from a number
// FORGE chose, and refusing a part over a number FORGE chose would be refusing
// its own arithmetic.
func statedSize(p Part, key string) (float64, string, bool) {
	if v, ok := p.Size[key]; ok {
		return v, key, true
	}
	shape, _ := resolveShape(strings.ToLower(strings.TrimSpace(p.Shape)), p.Label())
	aliases := make([]string, 0, len(sizeSynonyms[shape]))
	for alias, means := range sizeSynonyms[shape] {
		if means == key {
			aliases = append(aliases, alias)
		}
	}
	sort.Strings(aliases)
	for _, alias := range aliases {
		if v, ok := p.Size[alias]; ok {
			return v, alias, true
		}
	}
	return 0, "", false
}

// degenerateProblems is every part whose stated dimensions collapse it.
//
// Errors, not warnings: the part is not in the model afterwards, which is what
// Faults exists to separate from a note about a model that exists.
func degenerateProblems(parts []Part) []Problem {
	var out []Problem
	for _, p := range parts {
		out = append(out, degenerateProblemsFor(p)...)
	}
	return out
}

// degenerateProblemsFor is one part's, so the builder can ask about the part it
// is holding without walking the list again.
func degenerateProblemsFor(p Part) []Problem {
	shape, _ := resolveShape(strings.ToLower(strings.TrimSpace(p.Shape)), p.Label())
	var out []Problem
	for _, key := range collapsingKeys(shape) {
		value, read, stated := statedSize(p, key)
		if !stated || value > 0 {
			continue
		}
		out = append(out, Problem{
			Severity: Error, Name: p.Label(),
			Detail: fmt.Sprintf("states %s = %g, and a %s built from that %s has no volume at all; "+
				"give %q a number greater than zero%s",
				read, value, shape, key, key, resolvedFrom(p, key, read)),
		})
	}
	return out
}

// resolvedFrom says where the number came from when the document says: the
// expression the dimension is bound to, which is the thing to fix. Empty when
// the number was simply typed, because "you typed it" adds nothing.
func resolvedFrom(p Part, key, read string) string {
	for _, k := range []string{key, read} {
		if expr, bound := p.SizeFrom[k]; bound {
			return fmt.Sprintf(" (it is bound to %q, which works out to zero or less)", expr)
		}
	}
	return ""
}
