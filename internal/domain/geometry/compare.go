package geometry

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Variants side by side (PRD VIS-04).
//
// # What makes this a comparison rather than three pictures
//
// Putting renders next to each other is the easy half and the useless half. The
// question a person is actually holding is "what is different, and does the
// difference matter" — and three viewports answer it only for differences big
// enough to see. A 3 mm plate beside a 5 mm plate looks like the same bracket.
//
// So the comparison computes the differences and names them. Everything below is
// DERIVED from the variants on every call and none of it is stored: a saved
// comparison goes stale the moment somebody verifies a variant or rules on it,
// and it is precisely the document a person leans on to choose between designs.
//
// # The trap this is arranged around
//
// Two variants can hold the same number and mean different lengths. 60 declared
// in millimetres and 60 declared in centimetres are not a match, and 60 mm
// against 6 cm is. Every dimension is therefore compared in millimetres and
// RENDERED in the unit it was authored in — and when either side has no
// convertible unit, the pair is reported as NOT COMPARABLE rather than as equal
// or different. Calling a bare 60 equal to 60 mm is the wrong answer with the
// most convincing appearance.

// Comparison is several variants read side by side.
type Comparison struct {
	ProjectID string
	// Variants in the order they were NAMED, which is the order they appear on
	// screen. Re-sorting them would rearrange the comparison somebody asked for.
	Variants []Variant
	// Provenance is VIS-04's six facts as one row per fact, so a reader scans
	// ACROSS to see whether the thing they care about differs.
	Provenance []FieldRow
	// Parts is one row per part id seen in any variant.
	Parts []PartRow
	// NotComparable is every pair of values this comparison declined to judge,
	// and why. Never merged into the rows: "these differ" and "these could not
	// be compared" are different answers and only one of them is a finding.
	NotComparable []string
	// MatchNotes is where a part was paired with another BY NAME rather than by
	// identity, and which ids were involved.
	//
	// A third list rather than a third entry in NotComparable, which is where
	// these first landed. "We could not compare these" and "we guessed these are
	// the same part and then compared them" are different claims: the first
	// withholds a judgement and the second qualifies one. A reader scanning a box
	// headed "not compared" and finding rows that WERE compared learns to skim
	// the box.
	MatchNotes []string
}

// FieldRow is one provenance fact across every variant.
type FieldRow struct {
	Field string
	// Values has one entry per variant, in the same order as Comparison.Variants.
	Values []string
	// Differs is true when the variants do not agree.
	Differs bool
	// Why explains what a difference in this field MEANS, for the fields where
	// that is not obvious. Empty where it is.
	Why string
}

// PartRow is one part across every variant.
type PartRow struct {
	PartID string
	Label  string
	// MatchedBy is how this part was decided to be the same part across
	// variants. Never omitted: a name match is a guess and a reader who is not
	// told will read it as identity. See MatchBasis.
	MatchedBy MatchBasis
	Cells     []PartCell
	// Differences are the specific things that are not the same, already in
	// words: "thickness: 3 mm / 5 mm".
	Differences []string
	// Missing lists the variants (by 1-based column) that do not have this part
	// at all. A part present in one design and absent in another is the biggest
	// difference there is, and it is invisible in a dimension table.
	MissingFrom []int
}

// PartCell is one part as one variant has it.
type PartCell struct {
	Present    bool
	Shape      string
	Dimensions string
	Position   string
}

// Differs reports whether this part is not identical across the variants.
func (r PartRow) Differs() bool { return len(r.Differences) > 0 || len(r.MissingFrom) > 0 }

// comparisonEpsilonMM is how close two lengths must be, in millimetres, to be
// called the same.
//
// A nanometre. Not a tolerance in any engineering sense — it exists only so that
// converting 60 mm to 6 cm and back does not report a difference of 7e-15 mm.
// Anything a person could mean by "these are different sizes" is many orders of
// magnitude above it, so it cannot hide a real difference.
const comparisonEpsilonMM = 1e-6

// Compare builds the side-by-side view.
func Compare(variants []Variant) *Comparison {
	c := &Comparison{Variants: variants}
	if len(variants) > 0 {
		c.ProjectID = variants[0].ProjectID
	}
	c.Provenance = provenanceRows(variants)
	c.Parts, c.NotComparable, c.MatchNotes = partRows(variants)
	return c
}

// provenanceRows renders VIS-04's six facts, one row each.
func provenanceRows(vs []Variant) []FieldRow {
	get := func(f func(v Variant) string) []string {
		out := make([]string, 0, len(vs))
		for _, v := range vs {
			out = append(out, f(v))
		}
		return out
	}
	rows := []FieldRow{
		{Field: "geometry version", Values: get(func(v Variant) string {
			return fmt.Sprintf("%s v%d", v.Path, v.Version)
		})},
		{Field: "name", Values: get(func(v Variant) string { return v.Name })},
		{Field: "units", Values: get(func(v Variant) string {
			if v.Units.Known() {
				return string(v.Units)
			}
			if strings.TrimSpace(v.UnitsDeclared) == "" {
				return "not stated"
			}
			return fmt.Sprintf("%q — not convertible", v.UnitsDeclared)
		}), Why: "Variants in different units are compared by converting to millimetres. " +
			"A variant with no convertible unit is not compared at all."},
		{Field: "assumptions", Values: get(func(v Variant) string {
			n := len(v.Assumptions())
			if n == 0 {
				return "none recorded"
			}
			return fmt.Sprintf("%d", n)
		}), Why: "Every dimension FORGE chose rather than was told. A variant with more of them " +
			"is not a worse design — it is a design resting on more guesses."},
		{Field: "generator", Values: get(func(v Variant) string { return v.Generator })},
		{Field: "verification", Values: get(func(v Variant) string {
			s := string(v.Verification)
			if v.VerificationNote != "" {
				s += " — " + v.VerificationNote
			}
			return s
		}), Why: "What a MACHINE found. Nothing in this deployment verifies geometry, so this " +
			"reads 'unverified' until something outside it does."},
		{Field: "human disposition", Values: get(func(v Variant) string { return string(v.Disposition) }),
			Why: "What a PERSON decided. Never derived from the line above it: a machine's pass is " +
				"not a sign-off, and a person's acceptance is not a check. " +
				// Said out loud because the word is easy to misread as a
				// judgement. It is not one: proposing a new version of the same
				// assembly marks the earlier one superseded automatically, and a
				// superseded version can no longer be accepted or rejected. That
				// is correct for a file and wrong for alternatives, which is what
				// Adopt exists for: it brings the chosen geometry forward as a
				// new version, which CAN be ruled on.
				"'superseded' is not a verdict: a later proposal of the same assembly replaced it before " +
				"anybody ruled on it, and it can no longer be accepted or rejected. To choose it, ADOPT " +
				"it — that brings this exact geometry forward as the current version, which you can then " +
				"accept."},
	}
	for i := range rows {
		rows[i].Differs = !allEqual(rows[i].Values)
	}
	return rows
}

func allEqual(vals []string) bool {
	for _, v := range vals[1:] {
		if v != vals[0] {
			return false
		}
	}
	return true
}

// placedPart is one part as one variant holds it, carrying that variant's unit
// so a comparison never has to look the unit up again from the wrong side.
type placedPart struct {
	part   Part
	unit   Unit
	column int // 0-based index into Comparison.Variants
}

// MatchBasis is how a part in one variant was decided to be the same part as one
// in another.
//
// # Why this is reported rather than assumed
//
// Nothing in this system makes a part's identity stable across turns. The model
// invents the ids, and asked twice for the same bracket it will happily emit
// `nema-17-motor` once and `motor` the next time. So cross-variant matching is a
// JUDGEMENT, and the two ways of making it are not equally reliable:
//
//	MatchByID    the ids are the same string. Certain.
//	MatchByName  the ids differ and the names agree once punctuation and case
//	             are set aside. Probable, and it can be wrong — two variants may
//	             genuinely each have their own "spacer" that are not the same
//	             spacer.
//
// The first version of this file matched on id alone. Against a real
// conversation — propose a bracket, then "make the base plate thicker" — the
// model renamed every id, and the comparison rendered every part TWICE, once as
// "only in column 1" and once as "only in column 2". It read as two unrelated
// designs, which is the opposite of what had happened.
//
// Falling back silently would have been worse than the bug: a name match
// presented as identity is the interface asserting something the system does not
// know. So the basis travels with the row, and the surfaces say so when it is a
// name.
type MatchBasis string

const (
	MatchByID   MatchBasis = "id"
	MatchByName MatchBasis = "name"
	// MatchNone is a part that appears in one variant only.
	MatchNone MatchBasis = "none"
)

// partRows matches parts across variants and reports what differs.
//
// Two passes, most certain first: ids, then names among whatever the ids left
// unmatched. A name pass never merges two parts from the SAME variant — a
// variant with two parts called "spacer" has two spacers, and folding them
// together would invent a difference out of the fold.
func partRows(vs []Variant) (rows []PartRow, notComparable, matchNotes []string) {
	byID := map[string][]placedPart{}
	var order []string
	for i, v := range vs {
		for _, p := range v.Document.Parts {
			if _, seen := byID[p.ID]; !seen {
				order = append(order, p.ID)
			}
			byID[p.ID] = append(byID[p.ID], placedPart{part: p, unit: v.Units, column: i})
		}
	}

	type bucket struct {
		label string
		found []placedPart
		basis MatchBasis
	}
	var buckets []*bucket
	byName := map[string]*bucket{}

	for _, partID := range order {
		found := byID[partID]
		b := &bucket{label: found[0].part.Label(), found: found, basis: MatchByID}
		if len(found) == 1 {
			b.basis = MatchNone
		}
		buckets = append(buckets, b)
	}

	// Second pass. Only buckets that did not already span every variant are
	// eligible: a part matched by id in all columns is settled, and pulling
	// another one into it by name could only make it wrong.
	for _, b := range buckets {
		if len(b.found) == len(vs) {
			continue
		}
		key := normaliseLabel(b.label)
		if key == "" {
			continue
		}
		existing, ok := byName[key]
		if !ok {
			byName[key] = b
			continue
		}
		if len(existing.found) == len(vs) || collides(existing.found, b.found) {
			// Same name, same column: two genuinely different parts that happen
			// to share a label. Left apart.
			continue
		}
		existing.found = append(existing.found, b.found...)
		existing.basis = MatchByName
		b.found = nil
	}

	noted := map[string]bool{}
	once := func(dst *[]string, format string, args ...any) {
		s := fmt.Sprintf(format, args...)
		if !noted[s] {
			noted[s] = true
			*dst = append(*dst, s)
		}
	}
	note := func(format string, args ...any) { once(&notComparable, format, args...) }

	rows = make([]PartRow, 0, len(buckets))
	for _, b := range buckets {
		if len(b.found) == 0 {
			continue // folded into another bucket by the name pass
		}
		sort.SliceStable(b.found, func(i, j int) bool { return b.found[i].column < b.found[j].column })

		row := PartRow{PartID: b.found[0].part.ID, Label: b.label, MatchedBy: b.basis,
			Cells: make([]PartCell, len(vs))}
		for _, f := range b.found {
			row.Cells[f.column] = PartCell{
				Present:    true,
				Shape:      f.part.Shape,
				Dimensions: Dimensions(f.part, f.unit),
				Position:   position(f.part, f.unit),
			}
		}
		for i := range vs {
			if !row.Cells[i].Present {
				row.MissingFrom = append(row.MissingFrom, i+1)
			}
		}
		if row.MatchedBy == MatchByName {
			// Reported once per part. It changes how much weight the row's
			// differences carry, and a reader who is not told will read a name
			// match as identity.
			ids := make([]string, 0, len(b.found))
			for _, f := range b.found {
				ids = append(ids, fmt.Sprintf("%q in column %d", f.part.ID, f.column+1))
			}
			once(&matchNotes, "%s: matched BY NAME rather than by identity — %s. "+
				"FORGE renamed the part between proposals, so this row assumes they are the same part.",
				b.label, strings.Join(ids, " and "))
		}
		row.Differences = partDifferences(b.label, b.found, note)
		rows = append(rows, row)
	}
	return rows, notComparable, matchNotes
}

// collides reports whether two groups both hold a part from the same variant.
func collides(a, b []placedPart) bool {
	seen := map[int]bool{}
	for _, p := range a {
		seen[p.column] = true
	}
	for _, p := range b {
		if seen[p.column] {
			return true
		}
	}
	return false
}

// normaliseLabel reduces a part name to what two turns are likely to agree on.
//
// Case and punctuation only. Nothing cleverer — no stemming, no synonyms, no
// edit distance: a matcher that decides "base plate" and "baseplate" are the
// same thing is defensible, and one that decides "front spacer" and "rear
// spacer" are is a fabricated comparison.
func normaliseLabel(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// partDifferences names what is not the same about one part across the variants
// that have it.
//
// Compared against the FIRST variant that has the part rather than pairwise:
// with six columns, pairwise produces fifteen comparisons of which fourteen are
// noise, and the reader's question is "how do the others differ from this one".
func partDifferences(label string, found []placedPart, note func(string, ...any)) []string {
	if len(found) < 2 {
		return nil
	}
	base := found[0]
	var out []string

	for _, other := range found[1:] {
		if base.part.Shape != other.part.Shape {
			out = append(out, fmt.Sprintf("shape: %s in column %d, %s in column %d",
				base.part.Shape, base.column+1, other.part.Shape, other.column+1))
		}
	}

	// Where a part SITS is as much a difference as how big it is: a bracket
	// whose boss moved 8 mm is a different bracket, and a dimension table alone
	// says the two are identical.
	for _, other := range found[1:] {
		for axis := 0; axis < 3 && axis < len(base.part.Position) && axis < len(other.part.Position); axis++ {
			same, comparable := sameLength(base.part.Position[axis], base.unit, other.part.Position[axis], other.unit)
			if !comparable {
				note("%s could not be compared between columns %d and %d: "+
					"one of them has no unit FORGE can convert, so its numbers mean no particular length.",
					label, base.column+1, other.column+1)
				break
			}
			if !same {
				out = append(out, fmt.Sprintf("position %s: %s in column %d, %s in column %d",
					axisName(axis),
					NewQuantity(base.part.Position[axis], base.unit), base.column+1,
					NewQuantity(other.part.Position[axis], other.unit), other.column+1))
			}
		}
	}

	parts := make([]Part, 0, len(found))
	for _, f := range found {
		parts = append(parts, f.part)
	}
	for _, key := range sizeKeys(parts) {
		baseVal, baseHas := base.part.Size[key]
		for _, other := range found[1:] {
			otherVal, otherHas := other.part.Size[key]
			switch {
			case baseHas && !otherHas:
				out = append(out, fmt.Sprintf("%s: %s in column %d, not given in column %d",
					key, NewQuantity(baseVal, base.unit), base.column+1, other.column+1))
			case !baseHas && otherHas:
				out = append(out, fmt.Sprintf("%s: not given in column %d, %s in column %d",
					key, base.column+1, NewQuantity(otherVal, other.unit), other.column+1))
			case !baseHas && !otherHas:
				// Neither states it. Nothing to say.
			default:
				same, comparable := sameLength(baseVal, base.unit, otherVal, other.unit)
				if !comparable {
					// Reported once per part rather than once per dimension:
					// when a variant has no convertible unit, EVERY dimension of
					// it is incomparable, and a note per key buries the reason
					// under its own repetitions.
					note("%s could not be compared between columns %d and %d: "+
						"one of them has no unit FORGE can convert, so its numbers mean no particular length.",
						label, base.column+1, other.column+1)
					continue
				}
				if !same {
					out = append(out, fmt.Sprintf("%s: %s in column %d, %s in column %d",
						key, NewQuantity(baseVal, base.unit), base.column+1,
						NewQuantity(otherVal, other.unit), other.column+1))
				}
			}
		}
	}
	return out
}

// axisName names a coordinate for a reader. The frame is assembly-origin, Y up
// (see Frame), so Y is height and saying "axis 1" instead would make somebody
// go and look it up.
func axisName(i int) string {
	switch i {
	case 0:
		return "X"
	case 1:
		return "Y (up)"
	default:
		return "Z"
	}
}

// position renders a part's origin with every coordinate carrying its unit.
func position(p Part, u Unit) string {
	if len(p.Position) != 3 {
		return ""
	}
	parts := make([]string, 0, 3)
	for _, v := range p.Position {
		parts = append(parts, NewQuantity(v, u).String())
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

// sizeKeys returns every dimension key used by any of these parts, in a stable
// order.
//
// The UNION rather than the first part's keys: a cylinder that gained a
// radius_top in one variant and not another differs precisely in the key one of
// them does not have, and iterating over either side's keys alone would miss it.
func sizeKeys(parts []Part) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range parts {
		for k := range p.Size {
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	sort.Strings(out)
	return out
}

// sameLength reports whether two authored values mean the same length, and
// whether the question could be answered at all.
//
// The second return is the whole point. A variant with no convertible unit
// cannot be compared with anything, and reporting such a pair as equal — which
// a naive numeric comparison does whenever the numbers match — is the most
// convincing wrong answer this file could give.
func sameLength(a float64, ua Unit, b float64, ub Unit) (same bool, comparable bool) {
	qa, okA := NewQuantity(a, ua).In(Millimetre)
	qb, okB := NewQuantity(b, ub).In(Millimetre)
	if !okA || !okB {
		return false, false
	}
	return math.Abs(qa.Value()-qb.Value()) <= comparisonEpsilonMM, true
}
