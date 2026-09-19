package geometry

import (
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
)

// Comparing designs placed in assemblies.
//
// Phase 7, stage E2 of docs/plan-2026-09-13-millions-of-parts.md.
//
// # What this adds, and what it leaves alone
//
// The part rows (compare.go) read Document.Parts: parts at absolute positions. A
// document written as a tree keeps almost nothing there. Its wheel is ONE
// definition placed by four children, and it only becomes four parts
// ("front-left/wheel" …) when something expands it. So until this stage two
// versions of a car compared as two documents with no parts, and the answer to
// "what changed" was "nothing".
//
// Structure answers in the document's own terms, matched by id:
//
//   - definitions: which variants have each one, which of its fields changed, and
//     how many times each variant places it;
//   - assemblies: which variants have each one, and within it which children,
//     interfaces and features exist where, and which of their fields changed;
//   - the root each variant names.
//
// The part rows are untouched, and a comparison in which no variant is a tree has
// no Structure at all (nil). Comparing flat documents is therefore exactly what it
// was, on the wire as well: httpapi's compare fence holds the response byte for
// byte.
//
// # Why counts, and not occurrence by occurrence
//
// The obvious implementation expands every variant and compares the parts — the
// flat comparison again, over ids like "front-left/wheel". It is wrong twice. It
// costs what the design PLACES, a million rows for a million parts to say that one
// definition changed. And it reports one edit as many: change the wheel and four
// rows differ, and the reader has to work out that they are one change. The
// definition is where the edit was made, so that is where the difference is
// reported, once, with how many times each variant places it beside it.
//
// The counts come from placements, a walk that multiplies patterns instead of
// writing them out, so they cost what the document STORES. Not Expanded: its parts
// no longer say which definition they came from, and it is the O(occurrences) walk
// this stage exists to avoid.
//
// # Why field names and not values
//
// A changed definition says WHICH fields changed — "size", "profile", "repeat" —
// rather than restating them. The part rows put values into words because a flat
// part is little more than its values; a definition's profile is a list of points
// and a pattern's path another, and the variants' documents, which travel with the
// comparison, hold them exactly. What the reader needs from this is where to look.
//
// # The same trap as the part rows
//
// Lengths are compared in millimetres (sameLength), so a definition authored 60 mm
// wide in one variant and 6 cm wide in another has NOT changed size. A length that
// cannot be converted is called neither changed nor unchanged: the pair goes to
// NotComparable, in the part rows' own words.

// Structure is how the trees of several variants differ, matched by id. Nil when no
// variant is a tree.
type Structure struct {
	// Root is the assembly each variant names as its root, one value per variant.
	Root FieldRow
	// Definitions is one row per definition id seen in any variant, first seen first.
	Definitions []DefinitionRow
	// Assemblies is one row per assembly id seen in any variant, first seen first.
	Assemblies []AssemblyRow
}

// DefinitionRow is one definition across every variant.
type DefinitionRow struct {
	ID    string
	Label string
	// Occurrences is how many times each variant places this definition, one entry
	// per variant: every copy of every pattern, on every path down from the root.
	// Zero where the variant does not define it or places it nowhere.
	//
	// A definition's own repeat multiplies it, as it multiplies what the tree places
	// (limits.go, repeatCount): a bolt with "repeat": {"count": 6} placed at four
	// corners is 24 bolts, the number a person counting them sees. So how many copies
	// the repeat makes is shown here, once, and "repeat" in Changed means the copies
	// are LAID OUT differently (repeatChanged has the one exception). Settled
	// 2026-09-17: it was reported as a changed field and left out of the count, so 6
	// bolts becoming 8 read "placed 4×, changed: repeat".
	Occurrences []int
	// MissingFrom lists the variants, by 1-based column, that do not define it.
	MissingFrom []int
	// Changed names the fields that differ from the first variant that has it, in
	// the order of definitionFields.
	Changed []string
}

// Differs reports whether this definition is not the same everywhere: absent
// somewhere, changed, or placed a different number of times.
func (r DefinitionRow) Differs() bool {
	return len(r.MissingFrom) > 0 || len(r.Changed) > 0 || !slices.Equal(r.Occurrences, uniform(r.Occurrences))
}

// AssemblyRow is one assembly across every variant.
type AssemblyRow struct {
	ID          string
	Label       string
	MissingFrom []int
	// Changed is the assembly's OWN fields that differ ("name"). What it holds is in
	// the three member lists.
	Changed []string
	// Children, Interfaces and Features are matched by id within this assembly. A
	// variant without the assembly has none of them, so they are missing from it too.
	Children   []MemberRow
	Interfaces []MemberRow
	Features   []MemberRow
}

// Differs reports whether this assembly, or anything it holds, is not the same
// everywhere.
func (r AssemblyRow) Differs() bool {
	if len(r.MissingFrom) > 0 || len(r.Changed) > 0 {
		return true
	}
	for _, list := range [][]MemberRow{r.Children, r.Interfaces, r.Features} {
		for _, m := range list {
			if m.Differs() {
				return true
			}
		}
	}
	return false
}

// MemberRow is one child, interface or feature of an assembly across every variant.
//
// An interface whose position or rotation changed has MOVED, and says so here, on
// the assembly that declares it. Everything attached at it moves with it, and the
// children attached there are, correctly, unchanged: their `at` still names it.
type MemberRow struct {
	ID          string
	MissingFrom []int
	Changed     []string
}

// Differs reports whether this member is absent somewhere or changed.
func (r MemberRow) Differs() bool { return len(r.MissingFrom) > 0 || len(r.Changed) > 0 }

// structureOf compares the variants' trees, and returns what it could not judge.
func structureOf(vs []Variant) (*Structure, []string) {
	trees := false
	for _, v := range vs {
		trees = trees || v.Document.hasTree()
	}
	if !trees {
		return nil, nil
	}

	var notes []string
	noted := map[string]bool{}
	incomparable := func(what string, pairs [][2]int) {
		for _, p := range pairs {
			s := fmt.Sprintf("%s could not be compared between columns %d and %d: "+
				"one of them has no unit FORGE can convert, so its numbers mean no particular length.",
				what, p[0], p[1])
			if !noted[s] {
				noted[s] = true
				notes = append(notes, s)
			}
		}
	}

	s := &Structure{Root: FieldRow{Field: "root"}}
	units := make([]Unit, len(vs))
	defs := make([][]Part, len(vs))
	asms := make([][]Assembly, len(vs))
	counts := make([]map[string]int, len(vs))
	for i, v := range vs {
		units[i] = v.Units
		defs[i] = v.Document.Definitions
		asms[i] = v.Document.Assemblies
		counts[i] = placements(v.Document)
		switch {
		case !v.Document.hasTree():
			s.Root.Values = append(s.Root.Values, "not a tree")
		case v.Document.Root == "":
			s.Root.Values = append(s.Root.Values, "none named")
		default:
			s.Root.Values = append(s.Root.Values, v.Document.Root)
		}
	}
	s.Root.Differs = !allEqual(s.Root.Values)

	for _, m := range matchByID(defs, func(_ int, p Part) string { return p.ID }) {
		missing, changed, pairs := changes(m, units, definitionFields)
		row := DefinitionRow{ID: m.id, Label: m.first().Label(), MissingFrom: missing, Changed: changed,
			Occurrences: make([]int, len(vs))}
		for i := range vs {
			if m.cells[i] != nil {
				row.Occurrences[i] = counts[i][m.id]
			}
		}
		row.Changed = repeatChanged(row, m)
		incomparable("definition "+row.Label, pairs)
		s.Definitions = append(s.Definitions, row)
	}

	for _, m := range matchByID(asms, func(_ int, a Assembly) string { return a.ID }) {
		missing, changed, pairs := changes(m, units, assemblyFields)
		label := m.first().Name
		if label == "" {
			label = m.id
		}
		row := AssemblyRow{ID: m.id, Label: label, MissingFrom: missing, Changed: changed}
		children := make([][]Child, len(vs))
		interfaces := make([][]Interface, len(vs))
		features := make([][]Feature, len(vs))
		for i, a := range m.cells {
			if a != nil {
				children[i], interfaces[i], features[i] = a.Children, a.Interfaces, a.Features
			}
		}
		var childPairs, interfacePairs, featurePairs [][2]int
		row.Children, childPairs = memberRows(children, func(_ int, c Child) string { return c.ID }, units, childFields)
		row.Interfaces, interfacePairs = memberRows(interfaces, func(_ int, f Interface) string { return f.ID },
			units, interfaceFields)
		row.Features, featurePairs = memberRows(features, featureID, units, featureFields)
		// Once per assembly rather than once per member: when a variant has no
		// convertible unit, EVERY length in it is incomparable, and a note per child
		// buries the reason under its own repetitions.
		incomparable("assembly "+label, slices.Concat(pairs, childPairs, interfacePairs, featurePairs))
		s.Assemblies = append(s.Assemblies, row)
	}
	return s, notes
}

// repeatChanged adds "repeat" to a definition row's Changed list, in
// definitionFields order, when variants make a different number of copies of it and
// the occurrence counts cannot show that: the definition is placed nowhere, so every
// count is the same.
//
// Where the counts differ the copy count is already said, as a count; naming the
// field as well would report one edit twice.
func repeatChanged(row DefinitionRow, m matched[Part]) []string {
	if slices.Contains(row.Changed, "repeat") || !slices.Equal(row.Occurrences, uniform(row.Occurrences)) {
		return row.Changed
	}
	copies, differs := -1, false
	for _, c := range m.cells {
		if c == nil {
			continue
		}
		if n := repeatCount(*c); copies < 0 {
			copies = n
		} else if n != copies {
			differs = true
		}
	}
	if !differs {
		return row.Changed
	}
	at := 0
	for _, f := range definitionFields {
		if f.name == "repeat" {
			break
		}
		if at < len(row.Changed) && row.Changed[at] == f.name {
			at++
		}
	}
	return slices.Insert(slices.Clone(row.Changed), at, "repeat")
}

// featureID is the id a feature on an assembly is known by: its own, or the one
// occurrenceFeatures gives a feature that has none.
func featureID(i int, f Feature) string {
	if id := strings.TrimSpace(f.ID); id != "" {
		return id
	}
	return fmt.Sprintf("feature-%d", i+1)
}

// matched is one id's member in every variant: nil where the variant has none.
type matched[T any] struct {
	id    string
	cells []*T
}

func (m matched[T]) first() T {
	for _, c := range m.cells {
		if c != nil {
			return *c
		}
	}
	var zero T
	return zero
}

// matchByID groups members across variants by id, in the order ids are first seen.
//
// The FIRST member with an id in a variant is the one compared. A second one with
// the same id is a mistake expandAssemblies already refuses by name, and comparing
// it as well would report that refusal again as a change.
func matchByID[T any](lists [][]T, id func(i int, m T) string) []matched[T] {
	var out []matched[T]
	at := map[string]int{}
	for col, list := range lists {
		for i := range list {
			key := id(i, list[i])
			k, ok := at[key]
			if !ok {
				k = len(out)
				at[key] = k
				out = append(out, matched[T]{id: key, cells: make([]*T, len(lists))})
			}
			if out[k].cells[col] == nil {
				out[k].cells[col] = &list[i]
			}
		}
	}
	return out
}

// memberRows matches one kind of member across variants and names what differs.
func memberRows[T any](lists [][]T, id func(i int, m T) string, units []Unit, fields []field[T]) ([]MemberRow, [][2]int) {
	var rows []MemberRow
	var pairs [][2]int
	for _, m := range matchByID(lists, id) {
		missing, changed, incomparable := changes(m, units, fields)
		rows = append(rows, MemberRow{ID: m.id, MissingFrom: missing, Changed: changed})
		pairs = append(pairs, incomparable...)
	}
	return rows, pairs
}

// changes names the variants a member is missing from and the fields that differ,
// comparing every variant that has it with the FIRST that does — partDifferences'
// rule, for partDifferences' reason. incomparable is the column pairs (1-based)
// whose lengths could not be converted.
func changes[T any](m matched[T], units []Unit, fields []field[T]) (missing []int, changed []string, incomparable [][2]int) {
	base := -1
	for col, c := range m.cells {
		if c == nil {
			missing = append(missing, col+1)
			continue
		}
		if base < 0 {
			base = col
		}
	}
	differs := make([]bool, len(fields))
	for col := base + 1; base >= 0 && col < len(m.cells); col++ {
		if m.cells[col] == nil {
			continue
		}
		l := &lengths{a: units[base], b: units[col]}
		for i, f := range fields {
			if !f.same(*m.cells[base], *m.cells[col], l) {
				differs[i] = true
			}
		}
		if l.incomparable {
			incomparable = append(incomparable, [2]int{base + 1, col + 1})
		}
	}
	for i, f := range fields {
		if differs[i] {
			changed = append(changed, f.name)
		}
	}
	return missing, changed, incomparable
}

// field is one thing compared about a T, named as a person reading the document
// would name it.
type field[T any] struct {
	name string
	same func(a, b T, l *lengths) bool
}

// The closed lists of what is compared. A table rather than reflection, so that a
// field added to Part is a decision about whether it is a difference worth
// reporting, and so that what "size" covers (size and size_from) is written down.
// A note is not compared anywhere: it explains a design and does not change it.
var definitionFields = []field[Part]{
	{"name", func(a, b Part, _ *lengths) bool { return a.Name == b.Name }},
	{"shape", func(a, b Part, _ *lengths) bool { return a.Shape == b.Shape }},
	{"size", func(a, b Part, l *lengths) bool {
		return l.sizes(a.Size, b.Size) && sameStringMaps(a.SizeFrom, b.SizeFrom)
	}},
	{"position", func(a, b Part, l *lengths) bool {
		return l.vector(a.Position, b.Position) && sameStringMaps(a.PositionFrom, b.PositionFrom)
	}},
	{"rotation", func(a, b Part, _ *lengths) bool { return sameAngles(a.Rotation, b.Rotation) }},
	{"mirrored", func(a, b Part, _ *lengths) bool { return a.Mirrored == b.Mirrored }},
	{"profile", func(a, b Part, l *lengths) bool { return l.points(a.Profile, b.Profile) }},
	{"holes", func(a, b Part, l *lengths) bool {
		if len(a.Holes) != len(b.Holes) {
			return false
		}
		for i := range a.Holes {
			if !l.points(a.Holes[i], b.Holes[i]) {
				return false
			}
		}
		return true
	}},
	{"path", func(a, b Part, l *lengths) bool {
		return a.PathClosed == b.PathClosed && l.points(a.Path, b.Path)
	}},
	{"axis", func(a, b Part, _ *lengths) bool { return a.Axis == b.Axis }},
	{"script", func(a, b Part, _ *lengths) bool { return a.Script == b.Script }},
	// "repeat" is how the copies are LAID OUT, judged only where both variants make
	// copies: how MANY is multiplied into Occurrences (repeatChanged covers a
	// definition no count can show). A definition drawn once has no layout, so a
	// repeat of 1 beside none is the same design, as expandRepeats draws it.
	{"repeat", func(a, b Part, l *lengths) bool {
		if repeatCount(a) < 2 || repeatCount(b) < 2 {
			return true
		}
		ra, rb := a.Repeat, b.Repeat
		return ra.About == rb.About && sameAngle(ra.Angle, rb.Angle) && l.vector(ra.Offset, rb.Offset)
	}},
	{"material", func(a, b Part, _ *lengths) bool { return reflect.DeepEqual(a.Material, b.Material) }},
	{"appearance", func(a, b Part, _ *lengths) bool { return a.Color == b.Color && a.Opacity == b.Opacity }},
}

var assemblyFields = []field[Assembly]{
	{"name", func(a, b Assembly, _ *lengths) bool { return a.Name == b.Name }},
}

var childFields = []field[Child]{
	{"ref", func(a, b Child, _ *lengths) bool { return a.Ref == b.Ref }},
	{"name", func(a, b Child, _ *lengths) bool { return a.Name == b.Name }},
	{"position", func(a, b Child, l *lengths) bool { return l.vector(a.Position, b.Position) }},
	{"rotation", func(a, b Child, _ *lengths) bool { return sameAngles(a.Rotation, b.Rotation) }},
	{"mirror", func(a, b Child, _ *lengths) bool { return a.Mirror == b.Mirror }},
	{"at", func(a, b Child, _ *lengths) bool { return a.At == b.At }},
	{"pattern", func(a, b Child, l *lengths) bool { return samePattern(a.Pattern, b.Pattern, l) }},
}

var interfaceFields = []field[Interface]{
	{"name", func(a, b Interface, _ *lengths) bool { return a.Name == b.Name }},
	{"position", func(a, b Interface, l *lengths) bool { return l.vector(a.Position, b.Position) }},
	{"rotation", func(a, b Interface, _ *lengths) bool { return sameAngles(a.Rotation, b.Rotation) }},
}

var featureFields = []field[Feature]{
	{"op", func(a, b Feature, _ *lengths) bool { return a.Op == b.Op }},
	{"of", func(a, b Feature, _ *lengths) bool { return a.Of == b.Of }},
	{"with", func(a, b Feature, _ *lengths) bool { return slices.Equal(a.With, b.With) }},
	{"radius", func(a, b Feature, l *lengths) bool {
		if a.RadiusFrom != b.RadiusFrom {
			return false
		}
		// Zero is "no radius" — a cut has none — not a length of nothing, so two
		// features without one agree whatever their units.
		return (a.Radius == 0 && b.Radius == 0) || l.same(a.Radius, b.Radius)
	}},
	{"edges", func(a, b Feature, _ *lengths) bool { return a.Edges == b.Edges }},
	{"edge_length", func(a, b Feature, l *lengths) bool {
		return (a.EdgeLength == 0 && b.EdgeLength == 0) || l.same(a.EdgeLength, b.EdgeLength)
	}},
	{"thickness", func(a, b Feature, l *lengths) bool {
		if a.ThicknessFrom != b.ThicknessFrom {
			return false
		}
		return (a.Thickness == 0 && b.Thickness == 0) || l.same(a.Thickness, b.Thickness)
	}},
	{"open", func(a, b Feature, _ *lengths) bool { return slices.Equal(a.Open, b.Open) }},
	{"ruled", func(a, b Feature, _ *lengths) bool { return a.Ruled == b.Ruled }},
}

func samePattern(a, b *Pattern, l *lengths) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return strings.EqualFold(strings.TrimSpace(a.Kind), strings.TrimSpace(b.Kind)) &&
		a.Count == b.Count && a.About == b.About && sameAngle(a.Angle, b.Angle) &&
		a.Rows == b.Rows && a.Columns == b.Columns && a.Align == b.Align &&
		l.vector(a.Offset, b.Offset) && l.vector(a.RowOffset, b.RowOffset) &&
		l.vector(a.ColumnOffset, b.ColumnOffset) && l.points(a.Path, b.Path)
}

// lengths judges lengths authored in two variants' units. A pair it cannot convert
// is not judged — same answers true, so nothing is called changed on its account —
// and it remembers that, so the caller can say so.
type lengths struct {
	a, b         Unit
	incomparable bool
}

func (l *lengths) same(x, y float64) bool {
	same, comparable := sameLength(x, l.a, y, l.b)
	if !comparable {
		l.incomparable = true
		return true
	}
	return same
}

// vector compares two positions or offsets. Absent on both sides agrees; absent on
// one is the origin, which is what every reader takes it to mean.
func (l *lengths) vector(x, y []float64) bool {
	if len(x) == 0 && len(y) == 0 {
		return true
	}
	px, py := padTo3(x), padTo3(y)
	return l.same(px[0], py[0]) && l.same(px[1], py[1]) && l.same(px[2], py[2])
}

// sizes compares two size maps: the same keys, the same lengths. Every key is
// judged even after a difference is found, so whether an unconvertible unit is
// noted never depends on the order a map happens to be walked in.
func (l *lengths) sizes(x, y map[string]float64) bool {
	same := len(x) == len(y)
	for k, v := range x {
		w, ok := y[k]
		if !ok {
			same = false
			continue
		}
		if !l.same(v, w) {
			same = false
		}
	}
	return same
}

func (l *lengths) points(x, y []Point) bool {
	if len(x) != len(y) {
		return false
	}
	for i := range x {
		if !l.point(x[i], y[i]) {
			return false
		}
	}
	return true
}

func (l *lengths) point(p, q Point) bool {
	if p.XFrom != q.XFrom || p.YFrom != q.YFrom || p.ZFrom != q.ZFrom || p.RadiusFrom != q.RadiusFrom {
		return false
	}
	if (p.Via == nil) != (q.Via == nil) || (p.Via != nil && !l.point(*p.Via, *q.Via)) {
		return false
	}
	return l.same(p.X, q.X) && l.same(p.Y, q.Y) && l.same(p.Z, q.Z) && l.same(p.Radius, q.Radius)
}

// angleEpsilonDegrees is how close two angles must be to be called the same: noise,
// not tolerance, for the reason comparisonEpsilonMM gives.
const angleEpsilonDegrees = 1e-9

func sameAngle(a, b float64) bool { return math.Abs(a-b) <= angleEpsilonDegrees }

func sameAngles(x, y []float64) bool {
	px, py := padTo3(x), padTo3(y)
	return sameAngle(px[0], py[0]) && sameAngle(px[1], py[1]) && sameAngle(px[2], py[2])
}

func sameStringMaps(x, y map[string]string) bool {
	if len(x) != len(y) {
		return false
	}
	for k, v := range x {
		if w, ok := y[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// uniform is a slice as long as xs holding its first value throughout, so "are
// these all equal" is one slices.Equal.
func uniform(xs []int) []int {
	out := make([]int, len(xs))
	for i := range out {
		out[i] = xs[0]
	}
	return out
}

// placements counts how many times a document places each definition, without
// placing any of them: a pattern of k copies multiplies what its child places by k,
// and an assembly's count is worked out once however often it is placed.
//
// So it costs what the document stores (assemblies × the definitions beneath them),
// never what it places. The rules are occurrences' (limits.go), the count the
// document's limit is enforced on: a refused pattern places nothing, a cycle counts
// nothing, and a count saturates just past MaxOccurrences — a design that large was
// refused expansion, and "more than the limit" is all that can honestly be said.
//
// A definition's own repeat multiplies every placement of it, by repeatCount, the
// rule the occurrence limit counts with; see DefinitionRow.Occurrences.
func placements(d Document) map[string]int {
	if d.Root == "" {
		return nil
	}
	limit := CurrentLimits().MaxOccurrences
	sat := func(n int) int {
		if n > limit {
			return limit + 1
		}
		return n
	}
	mul := func(a, b int) int {
		if a == 0 || b == 0 {
			return 0
		}
		if a > (limit+1)/b {
			return limit + 1
		}
		return sat(a * b)
	}

	// How many copies each definition makes of itself, read from the first with its
	// id, the one occurrences (limits.go) counts.
	copiesOf := map[string]int{}
	for _, p := range d.Definitions {
		if _, seen := copiesOf[p.ID]; strings.TrimSpace(p.ID) != "" && !seen {
			copiesOf[p.ID] = repeatCount(p)
		}
	}
	asms := map[string]Assembly{}
	for _, a := range d.Assemblies {
		if _, isDef := copiesOf[a.ID]; strings.TrimSpace(a.ID) != "" && asms[a.ID].ID == "" && !isDef {
			asms[a.ID] = a
		}
	}
	memo := map[string]map[string]int{}
	onPath := map[string]bool{}
	var count func(id string) map[string]int
	count = func(id string) map[string]int {
		a, ok := asms[id]
		if !ok || onPath[id] {
			return nil
		}
		if m, ok := memo[id]; ok {
			return m
		}
		onPath[id] = true
		m := map[string]int{}
		for _, c := range a.Children {
			slots, _ := c.Pattern.copies()
			k := len(slots)
			if own, isDef := copiesOf[c.Ref]; isDef {
				m[c.Ref] = sat(m[c.Ref] + mul(k, own))
				continue
			}
			for def, n := range count(c.Ref) {
				m[def] = sat(m[def] + mul(k, n))
			}
		}
		delete(onPath, id)
		memo[id] = m
		return m
	}
	return count(d.Root)
}
