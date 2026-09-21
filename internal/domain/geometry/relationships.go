package geometry

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Relationship checking: the three shapes wave 13 left out.
//
// # Where this starts
//
// Wave 13 (binding.go, Spans) checks exactly one relationship shape: a DISTANCE
// between two positions placed by expressions over the same parameters. Its own
// record names what it does not reach, and those three became issues 9, 10 and 11:
//
//	9   a relationship whose RESULT nothing can name. `motor_mount_x` is a number
//	    with a label, and if the label points at nothing the geometry exposes,
//	    the document reads as parametric while nothing binds.
//	10  a relationship with NO binding. Holes at hardcoded coordinates describe
//	    no pattern: there is a set of positions and nothing connecting them to a
//	    parameter.
//	11  everything that is not a distance: angles, ratios, wall thickness.
//
// # The principle this keeps
//
// Wave 13's grouping is read from the BINDINGS and never from the geometry, and
// that is what makes it safe to act on: nothing here decides that four cylinders
// near each other must be a bolt pattern. Every check below keeps it. The one
// that reads geometry rather than bindings — wall thickness — reads the loops
// the document itself DREW, which is the document saying so in another form, and
// it reports a measurement rather than a verdict.
//
// # The honest answer, which is the point of issues 9 and 10
//
// The failure being fixed is not "these are unchecked". It is that they were
// unchecked SILENTLY, so a clean result meant "nothing to say" and "nothing I
// can see" at once. So every relationship this file finds comes back either
//
//   - CHECKED, with the figure and the name the document gave it, or
//   - NOT CHECKED, with the reason, and with the name FORGE assigns so a reader
//     can find it.
//
// A name FORGE assigns is built only from what the document already says — the
// parameters a placement rests on, or the part ids — and never from what the
// shape looks like. It is a handle for a reader, and it is deliberately NOT fed
// to the dimension table in standards_typed.go: scoring a span under a name
// FORGE made up is the fabricated finding that file's own fences exist to catch.
//
// # What is still not covered, and said so in the guide
//
// The angle between two FACES. A face's normal is not something a document
// exposes — it is something the kernel computes — so the angle checked here is
// between AXES, which the document states. Wall thickness between two separate
// SOLIDS is the same: it needs the kernel's distance query, and issue 6's
// evaluation stage is where it belongs. What is measured here is the wall a
// section draws, between its outline and a hole inside it, which is the one a
// manufacturability review opens with and the one the document already holds.

// RelationshipKind is one shape of relationship this package checks.
//
// # One table
//
// The checker reads it and RelationshipGuide prints it, so what the model is
// taught is what is actually checked and neither can drift from the other.
// Fence: TestRelationships_TheTableIsWhatTheCheckerReads.
type RelationshipKind struct {
	// Kind is the word every Relationship of this shape carries.
	Kind string
	// Reads is what the document must say for this to be checkable at all.
	Reads string
	// Result is what the checked figure IS, in a reader's words.
	Result string
}

var relationshipKinds = []RelationshipKind{
	{Kind: relationshipDistance,
		Reads:  `two parts placed by expressions over the same parameters ("position_from")`,
		Result: "the distance between them, in the document's unit"},
	{Kind: relationshipAngle,
		Reads:  `a polar pattern whose sweep is bound ("angle_from"), or two related parts turned differently`,
		Result: "the angle between one axis and the next, in degrees"},
	{Kind: relationshipRatio,
		Reads:  `a "derived" value that divides one named measurement by another`,
		Result: "the ratio itself, which has no unit"},
	{Kind: relationshipWall,
		Reads:  `an outline with a hole loop inside it`,
		Result: "the least material between the two, in the document's unit"},
}

const (
	relationshipDistance = "distance"
	relationshipAngle    = "angle"
	relationshipRatio    = "ratio"
	relationshipWall     = "wall thickness"
)

// RelationshipKinds is the table, for a caller that wants to say what is covered.
func RelationshipKinds() []RelationshipKind {
	return append([]RelationshipKind(nil), relationshipKinds...)
}

// RelationshipGuide is the contract's paragraph, printed from the table above.
//
// Fence: TestContract_TeachesRelationshipCheckingFromTheCheckersTable.
func RelationshipGuide() string {
	var lines []string
	for _, k := range relationshipKinds {
		lines = append(lines, fmt.Sprintf("    %s — from %s; FORGE reports %s.", k.Kind, k.Reads, k.Result))
	}
	return fmt.Sprintf(`  FORGE CHECKS THE RELATIONSHIP, NOT ONLY THE FIGURE. A recalled number can be
  right and the relationship built on it wrong, and only the result shows it.
  These are the shapes it can check:
%s
  A relationship it cannot name and a set of positions with no binding are
  reported as NOT checked, with the reason — they are not silently accepted. So
  bind what follows a parameter, and give the parts of one pattern ids that share
  a prefix ("mount-hole-1", "mount-hole-2"), which is what lets a distance be
  named. Evenly spaced parts belong in one "pattern" or one "repeat", never
  written out at typed coordinates.
`, strings.Join(lines, "\n"))
}

// Relationship is one relationship the document states, checked or honestly not.
type Relationship struct {
	// Kind is one of the kinds in relationshipKinds.
	Kind string `json:"kind"`
	// Name is what the result is called. When Named is false this is a name
	// FORGE assigned from what the document says, so a reader has a handle.
	Name  string `json:"name"`
	Named bool   `json:"named"`
	// Checked says whether there is a figure worth comparing against anything.
	// When it is false, Why says why not — and Why is never empty in that case.
	Checked bool   `json:"checked"`
	Why     string `json:"why,omitempty"`
	Value   float64
	Unit    string   `json:"unit,omitempty"`
	Parts   []string `json:"parts,omitempty"`
	// Depends is every parameter the relationship rests on, sorted. Provenance
	// travels along these exactly as it does for a derived value.
	Depends []string `json:"depends,omitempty"`
}

// Relationships is every relationship the document states, in a fixed order:
// by kind as the table lists them, then by name.
func (d *Document) Relationships() []Relationship {
	if d == nil {
		return nil
	}
	out := d.distanceRelationships()
	out = append(out, d.angleRelationships()...)
	out = append(out, d.ratioRelationships()...)
	out = append(out, d.wallRelationships()...)

	rank := map[string]int{}
	for i, k := range relationshipKinds {
		rank[k.Kind] = i
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return rank[out[i].Kind] < rank[out[j].Kind]
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Name is what a span's result is called, and whether the DOCUMENT named it.
//
// The document names it when the parts taking part share an id prefix: four
// holes called "mount-hole-1".."mount-hole-4" say what they are, and that shared
// word is the only thing in the document that can say which dimension the
// distance between them IS.
//
// When they do not, FORGE assigns one from the parameters the placement rests
// on — which is the other end of the same relationship, and is in the document
// too. It is a handle for a reader and NOT an identification: the caller that
// scores figures against published dimensions must skip it, which is what the
// second return value is for.
func (s Span) Name() (string, bool) {
	if label := commonLabel(s.Parts); label != "" {
		return label, true
	}
	if len(s.Depends) > 0 {
		return "the parts placed from " + strings.Join(s.Depends, " and "), false
	}
	return "the parts " + strings.Join(s.Parts, " and "), false
}

// commonLabel is the shared id prefix of a group of parts, or "".
//
// Two characters is not a name. Below that the "shared" prefix is an accident of
// spelling rather than something the author meant.
func commonLabel(ids []string) string {
	if len(ids) < 2 {
		return ""
	}
	prefix := ids[0]
	for _, id := range ids[1:] {
		n := 0
		for n < len(prefix) && n < len(id) && prefix[n] == id[n] {
			n++
		}
		prefix = prefix[:n]
	}
	prefix = strings.TrimRight(prefix, "-_ ")
	if len(prefix) < 3 {
		return ""
	}
	return prefix
}

// distanceRelationships is wave 13's spans, with issue 9's half answered: a span
// nothing can name is reported as unnamed rather than dropped.
func (d *Document) distanceRelationships() []Relationship {
	var out []Relationship
	for _, s := range d.Spans() {
		name, named := s.Name()
		r := Relationship{
			Kind: relationshipDistance, Name: name, Named: named, Checked: named,
			Value: s.Extent, Unit: s.Unit, Parts: s.Parts, Depends: s.Depends,
		}
		if !named {
			r.Why = fmt.Sprintf("the parts it places (%s) share no name, so nothing in this "+
				"document says which dimension this distance is and there is no published "+
				"figure to compare it against. FORGE calls it %q, after the parameters "+
				"underneath it; giving the parts ids that share a prefix is what would make "+
				"it checkable", strings.Join(s.Parts, ", "), name)
		}
		out = append(out, r)
	}
	return out
}

// angleRelationships covers issue 11's first case, both ways an angle is stated.
//
// A polar pattern's bound sweep is an angle the document states as a
// RELATIONSHIP — it rests on parameters and moves when they do — so it is
// checked the way a distance is. Two related parts turned differently state one
// as geometry, and the pair is taken from the bindings rather than from
// proximity: they are related because the document places them from the same
// parameters.
func (d *Document) angleRelationships() []Relationship {
	res := d.Resolve()
	lookup := func(n string) (float64, bool) {
		v, ok := res.Values[n]
		return v.Number, ok
	}
	var out []Relationship

	for _, a := range d.Assemblies {
		for _, c := range a.Children {
			p := c.Pattern
			if p == nil || !strings.EqualFold(strings.TrimSpace(p.Kind), "polar") || p.AngleFrom == "" {
				continue
			}
			name := a.ID + PathSeparator + c.ID + " step angle"
			if p.Count < 2 {
				// pattern.go has already said it is placed once. An angle
				// between one copy and no other copy is not a relationship.
				out = append(out, Relationship{Kind: relationshipAngle, Name: name, Named: true,
					Why: fmt.Sprintf("the pattern places %d copy, so there is no second axis to "+
						"measure an angle to", p.Count)})
				continue
			}
			node, err := parseExpression(p.AngleFrom)
			if err != nil {
				continue // bindPattern has already reported it, in its own voice.
			}
			sweep, err := node.Eval(lookup)
			if err != nil || math.IsNaN(sweep) || math.IsInf(sweep, 0) {
				continue
			}
			step := sweepAngle(&Repeat{Count: p.Count, Angle: sweep}) * 180 / math.Pi
			out = append(out, Relationship{
				Kind: relationshipAngle, Name: name, Named: true, Checked: true,
				Value: step, Unit: "deg", Parts: []string{c.ID},
				Depends: dependenciesOf(node.References(), res.Values),
			})
		}
	}

	// And the angle between the axes of two parts the bindings relate.
	//
	// Looked up IN ONE FRAME. A definition's position is in its own frame and a
	// definition may share an id with a top-level part — binding.go resolves them
	// against their own lists for exactly that reason, and Spans measures the two
	// separately. A span whose ids are not all in one list is left alone rather
	// than measured across two frames, which would be an angle between nothing.
	frames := []map[string]Part{partsByID(d.Parts), partsByID(d.Definitions)}
	for _, s := range d.Spans() {
		byID, oneFrame := frameFor(s.Parts, frames)
		if !oneFrame {
			continue
		}
		first, second, ok := turnedApart(s.Parts, byID)
		if !ok {
			continue
		}
		degrees := axisAngle(byID[first], byID[second])
		if degrees < axisAngleFloor {
			continue
		}
		name, named := s.Name()
		r := Relationship{
			Kind: relationshipAngle, Name: name + " axes", Named: named, Checked: named,
			Value: degrees, Unit: "deg", Parts: []string{first, second}, Depends: s.Depends,
		}
		if !named {
			r.Why = fmt.Sprintf("%s and %s are turned %.1f° apart, and the parts share no name, "+
				"so nothing in this document says which angle this is", first, second, degrees)
		}
		out = append(out, r)
	}
	return out
}

// axisAngleFloor is how far two axes must be apart before there is an angle to
// report. Parts that are parallel describe no angle, and reporting 0° for every
// bolt in a pattern is the warning-on-correct-input failure this package keeps
// naming.
const axisAngleFloor = 0.5

// partsByID indexes one frame's parts. First wins, because a list that declares
// an id twice is already reported by the tree and by Resolve.
func partsByID(parts []Part) map[string]Part {
	out := make(map[string]Part, len(parts))
	for _, p := range parts {
		if _, seen := out[p.ID]; !seen {
			out[p.ID] = p
		}
	}
	return out
}

// frameFor is the one frame holding every id, or false when they are spread
// across more than one — which is two coordinate frames and no measurement.
func frameFor(ids []string, frames []map[string]Part) (map[string]Part, bool) {
	var found map[string]Part
	for _, frame := range frames {
		all := true
		for _, id := range ids {
			if _, ok := frame[id]; !ok {
				all = false
				break
			}
		}
		if !all {
			continue
		}
		if found != nil {
			return nil, false
		}
		found = frame
	}
	return found, found != nil
}

// turnedApart finds the first two parts in a group whose rotations differ.
func turnedApart(ids []string, byID map[string]Part) (string, string, bool) {
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	for i, a := range sorted {
		pa, ok := byID[a]
		if !ok {
			continue
		}
		for _, b := range sorted[i+1:] {
			pb, ok := byID[b]
			if !ok {
				continue
			}
			if pa.RotationRadians() != pb.RotationRadians() {
				return a, b, true
			}
		}
	}
	return "", "", false
}

// axisAngle is the angle in degrees between two parts' own axes.
//
// A part's axis is its local +Y, which is the axis this system builds a
// cylinder, a screw and a bearing about, turned by the part's own rotation
// through the one rotation convention everything here shares (rotation.go).
func axisAngle(a, b Part) float64 {
	up := [3]float64{0, 1, 0}
	u, v := rotate(up, a.RotationRadians()), rotate(up, b.RotationRadians())
	dot := u[0]*v[0] + u[1]*v[1] + u[2]*v[2]
	dot = math.Max(-1, math.Min(1, dot))
	return math.Acos(dot) * 180 / math.Pi
}

// ratioRelationships covers issue 11's second case: a derived value that divides
// one named measurement by another.
//
// Narrow on purpose. Only `a / b` where both sides are a single NAME is a ratio
// between two named measurements; `(a - 2 * b) / c` has a result too, and
// nothing in the document names what its numerator is, so calling the quotient a
// ratio between two measurements would be naming something that was never
// measured.
func (d *Document) ratioRelationships() []Relationship {
	res := d.Resolve()
	var out []Relationship
	for _, dv := range d.Derived {
		name := strings.ToLower(strings.TrimSpace(dv.Name))
		v, ok := res.Values[name]
		if !ok {
			continue
		}
		node, err := parseExpression(dv.Expression)
		if err != nil {
			continue
		}
		over, under, ok := namedQuotient(node)
		if !ok {
			continue
		}
		top, hasTop := res.Values[over]
		bottom, hasBottom := res.Values[under]
		if !hasTop || !hasBottom {
			continue
		}
		r := Relationship{
			Kind: relationshipRatio, Name: name, Named: true, Checked: true,
			Value: v.Number, Parts: []string{over, under},
			Depends: dependenciesOf([]string{over, under}, res.Values),
		}
		// A ratio has NO unit, whatever Resolve inherited. Resolve does unit
		// agreement and not unit algebra (parameters.go says so in as many
		// words), so `a / b` of two millimetre figures is reported as
		// millimetres there. Here the quotient is the thing being reported, and
		// a ratio carrying "mm" would be a wrong unit rather than a missing one.
		if !strings.EqualFold(top.Unit, bottom.Unit) {
			r.Checked = false
			r.Why = fmt.Sprintf("%s is in %s and %s is in %s, so %s is a rate rather than a "+
				"ratio, and nothing here checks a rate",
				over, unitWord(top.Unit), under, unitWord(bottom.Unit), name)
		}
		out = append(out, r)
	}
	return out
}

func unitWord(u string) string {
	if strings.TrimSpace(u) == "" {
		return "no unit"
	}
	return u
}

// namedQuotient reports whether an expression is exactly one name divided by
// another, and which.
func namedQuotient(n *exprNode) (string, string, bool) {
	if n == nil || n.kind != "binary" || n.name != "/" || len(n.args) != 2 {
		return "", "", false
	}
	over, under := n.args[0], n.args[1]
	if over == nil || under == nil || over.kind != "ref" || under.kind != "ref" {
		return "", "", false
	}
	if _, isConst := exprConsts[over.name]; isConst {
		return "", "", false
	}
	if _, isConst := exprConsts[under.name]; isConst {
		return "", "", false
	}
	return over.name, under.name, true
}

// wallRelationships covers issue 11's third case, and the issue is right that it
// is not another case in the same switch.
//
// A distance is between two POSITIONS the document states. A wall is the
// material between two SURFACES, and this package holds a surface in exactly one
// form: the loops a section is drawn from. So this measures there — the least
// distance between an outline and each hole loop inside it, and between two hole
// loops — on the loops AFTER their expressions are evaluated, which is the same
// outline the kernel is sent.
//
// It reports a measurement and never a verdict. Whether 3.1 mm of wall is too
// thin depends on the material and the process, neither of which is the domain's
// business, and a "too thin" invented here would be the fabricated finding this
// package refuses everywhere else.
//
// # What it will not measure, and says so
//
// A loop with a CORNER RADIUS or a bowed edge. The measurement is taken on the
// straight polygon, which at a rounded corner is not where the material is, and
// a figure off by up to the radius reported as a wall thickness is worse than no
// figure. Those come back not checked, naming the reason.
func (d *Document) wallRelationships() []Relationship {
	var out []Relationship
	for _, frame := range d.drawnFrames() {
		for _, p := range frame.parts {
			section, ok := frame.profiles[p.ID]
			if !ok || len(section.Holes) == 0 {
				continue
			}
			loops := append([]polyline{section.Outer}, section.Holes...)
			if curvedLoops(loops) {
				out = append(out, Relationship{
					Kind: relationshipWall, Name: p.Label() + " wall", Named: true,
					Why: "its outline or one of its holes carries a corner radius or a bowed edge, " +
						"and the material at a rounded corner is not where the straight drawing " +
						"puts it, so FORGE will not put a number on this wall",
				})
				continue
			}
			if n := loopPoints(loops); n > maxWallLoopPoints {
				out = append(out, Relationship{
					Kind: relationshipWall, Name: p.Label() + " wall", Named: true,
					Why: fmt.Sprintf("its outline and holes carry %d points between them, past the "+
						"%d this measures to: the least material is every edge against every edge, "+
						"and a check that makes a turn slow is a check somebody switches off",
						n, maxWallLoopPoints),
				})
				continue
			}
			least, between := leastMaterial(loops)
			if between == "" {
				continue
			}
			out = append(out, Relationship{
				Kind: relationshipWall, Named: true, Checked: true,
				// Which two loops the least material sits between is part of the
				// answer: "3.1 mm" of a plate with four holes says nothing about
				// where to look, and "between hole 2 and hole 3" does.
				Name:  fmt.Sprintf("%s wall between %s", p.Label(), between),
				Value: least, Unit: strings.TrimSpace(d.Units), Parts: []string{p.ID},
			})
		}
	}
	return out
}

// maxWallLoopPoints bounds the measurement.
//
// The least material between two loops is every edge of one against every edge
// of the other, which is quadratic in the points, and this runs on every turn. A
// part drawn with thousands of points would make a turn slow, and a check that
// makes a turn slow is a check somebody switches off. Past this it SAYS it did
// not measure, rather than measuring slowly or skipping silently — which is the
// same bargain the rest of this file makes.
const maxWallLoopPoints = 600

func loopPoints(loops []polyline) int {
	n := 0
	for _, l := range loops {
		n += len(l.Points)
	}
	return n
}

// drawnFrame is one list of parts with the outlines resolved against THAT list.
type drawnFrame struct {
	parts    []Part
	profiles map[string]outline
}

// drawnFrames keeps the top-level parts and the definitions apart, the way
// binding.go does: a definition and a top-level part may share an id, and
// resolving a definition against the top-level map would measure the wall of
// somebody else's drawing. The definitions' map is only built when a definition
// actually has a hole in it, because resolving a whole list to find nothing is
// work every turn would pay for.
func (d *Document) drawnFrames() []drawnFrame {
	profiles, _, _ := d.resolvedProfiles()
	frames := []drawnFrame{{parts: d.Parts, profiles: profiles}}
	if !anyHoles(d.Definitions) {
		return frames
	}
	defs := Document{Parts: d.Definitions, Parameters: d.Parameters, Derived: d.Derived}
	defProfiles, _, _ := defs.resolvedProfiles()
	return append(frames, drawnFrame{parts: d.Definitions, profiles: defProfiles})
}

func anyHoles(parts []Part) bool {
	for _, p := range parts {
		if len(p.Holes) > 0 {
			return true
		}
	}
	return false
}

// curvedLoops reports whether any loop carries a corner radius or a bowed edge.
func curvedLoops(loops []polyline) bool {
	for _, l := range loops {
		for _, r := range l.Radii {
			if r != 0 {
				return true
			}
		}
		for _, v := range l.Vias {
			if v != nil {
				return true
			}
		}
	}
	return false
}

// leastMaterial is the smallest gap between any two of the loops, and which two.
// loops[0] is the outline and the rest are its holes.
func leastMaterial(loops []polyline) (float64, string) {
	least, between := math.Inf(1), ""
	for i := 0; i < len(loops); i++ {
		for j := i + 1; j < len(loops); j++ {
			gap, ok := loopGap(loops[i], loops[j])
			if !ok || gap >= least {
				continue
			}
			least, between = gap, fmt.Sprintf("%s and %s", loopName(i), loopName(j))
		}
	}
	if between == "" || math.IsInf(least, 0) {
		return 0, ""
	}
	return least, between
}

func loopName(i int) string {
	if i == 0 {
		return "the outline"
	}
	return fmt.Sprintf("hole %d", i)
}

// loopGap is the least distance between two closed loops in the section plane.
//
// Every helper it stands on is one this package already has: flat2D drops the
// section's Z (curve.go), segmentsCross is the crossing test the triangulator
// and the island check share (triangulate.go), and distanceToSegment is the
// gear's (gear.go). A second copy of any of them would be a second opinion about
// where a loop is.
func loopGap(a, b polyline) (float64, bool) {
	u, v := flat2D(a.Points), flat2D(b.Points)
	if len(u) < 2 || len(v) < 2 {
		return 0, false
	}
	least := math.Inf(1)
	for i := range u {
		p1, p2 := u[i], u[(i+1)%len(u)]
		for j := range v {
			q1, q2 := v[j], v[(j+1)%len(v)]
			if gap := segmentGap(p1, p2, q1, q2); gap < least {
				least = gap
			}
		}
	}
	if math.IsInf(least, 0) {
		return 0, false
	}
	return least, true
}

// segmentGap is the least distance between two segments in the section plane.
// Zero when they cross, which is a hole breaking out of its own outline —
// reported by profile.go rather than here, and a wall of zero is a true answer
// to "how much material is between these" either way.
func segmentGap(p1, p2, q1, q2 [2]float64) float64 {
	if segmentsCross(p1, p2, q1, q2) {
		return 0
	}
	return math.Min(
		math.Min(distanceToSegment(p1, q1, q2), distanceToSegment(p2, q1, q2)),
		math.Min(distanceToSegment(q1, p1, p2), distanceToSegment(q2, p1, p2)))
}

// RelationshipProblems is what a reader has to be told about this document's
// relationships, and what Bind and Resolve do not already say.
//
// Warnings, every one of them. Issue 9 is explicit that a decorative binding is
// REPORTED and not refused — "so a reader can tell a real binding from a
// decorative one" — and a set of hardcoded positions builds perfectly well; it
// simply does not follow anything. Refusing either would throw away a document
// over a judgement the person may have made on purpose.
//
// Sorted by sortProblems, so two runs over one document say the same thing in
// the same order.
func (d *Document) RelationshipProblems() []Problem {
	if d == nil {
		return nil
	}
	out := d.unreadValueProblems()
	out = append(out, d.unboundPatternProblems()...)
	for _, r := range d.Relationships() {
		if r.Checked || r.Why == "" {
			continue
		}
		out = append(out, Problem{Severity: Warning, Name: r.Name,
			Detail: fmt.Sprintf("is a %s FORGE did not check: %s", r.Kind, r.Why)})
	}
	sortProblems(out)
	return out
}

// unreadValueProblems is issue 9: a value whose result names no dimension the
// document exposes.
//
// A parameter or a derived value EARNS its place by reaching geometry: some
// size, position, outline coordinate, pattern step or feature dimension is bound
// to it, directly or through another value that is. One that reaches none is a
// number with a label, and the document reads as parametric while nothing binds.
//
// # The narrowing, and why it is not optional
//
// Reported only for a document that binds SOMETHING. A document with no bindings
// at all has every parameter unread by this test, and saying so about each of
// them would fire on every non-parametric design in the product — the
// warning-on-correct-input failure that stops a panel being read. A document
// that binds nothing is answered by unboundPatternProblems instead, which speaks
// about the positions rather than about every parameter at once.
func (d *Document) unreadValueProblems() []Problem {
	read := d.boundReferences()
	if len(read) == 0 {
		return nil
	}
	res := d.Resolve()

	// Backwards closure: a value is reached when geometry names it, or when a
	// value that is itself reached names it. Repeated passes rather than a
	// graph, for the reason Resolve gives: the list is small and the loop is
	// obviously terminating.
	reached := map[string]bool{}
	for name := range read {
		reached[name] = true
	}
	for changed := true; changed; {
		changed = false
		for name := range reached {
			v, ok := res.Values[name]
			if !ok || v.Expression == "" {
				continue
			}
			node, err := parseExpression(v.Expression)
			if err != nil {
				continue
			}
			for _, ref := range node.References() {
				if _, isConst := exprConsts[ref]; isConst || reached[ref] {
					continue
				}
				reached[ref] = true
				changed = true
			}
		}
	}

	var out []Problem
	for _, p := range d.Parameters {
		name := strings.ToLower(strings.TrimSpace(p.Name))
		if name == "" || reached[name] {
			continue
		}
		if _, resolved := res.Values[name]; !resolved {
			continue // Resolve has already said why, in its own voice.
		}
		out = append(out, Problem{Severity: Warning, Name: name,
			Detail: "is a parameter nothing in this design reads: no size, position, outline, " +
				"pattern step or feature is bound to it, and no value that is read follows it. " +
				"It names no dimension the geometry exposes, so changing it would move nothing"})
	}
	for _, dv := range d.Derived {
		name := strings.ToLower(strings.TrimSpace(dv.Name))
		if name == "" || reached[name] {
			continue
		}
		if _, resolved := res.Values[name]; !resolved {
			continue
		}
		out = append(out, Problem{Severity: Warning, Name: name,
			Detail: fmt.Sprintf("is derived as %q and nothing in this design reads it: the "+
				"relationship has a result and no dimension it names, so nothing can check it. "+
				"Bind the size or position it was meant to drive, or drop it", dv.Expression)})
	}
	return out
}

// boundReferences is every parameter name the GEOMETRY reads, from every place a
// document may hold an expression.
//
// One list, walked once. A site left out here becomes a false "nothing reads
// this", which is the expensive direction of the mistake.
func (d *Document) boundReferences() map[string]bool {
	out := map[string]bool{}
	add := func(expr string) {
		node, err := parseExpression(expr)
		if err != nil {
			return
		}
		for _, ref := range node.References() {
			if _, isConst := exprConsts[ref]; !isConst {
				out[ref] = true
			}
		}
	}
	addAll := func(m map[string]string) {
		for _, expr := range m {
			add(expr)
		}
	}
	parts := append(append([]Part(nil), d.Parts...), d.Definitions...)
	for _, p := range parts {
		addAll(p.SizeFrom)
		addAll(p.PositionFrom)
		for _, pt := range append(append([]Point(nil), p.Profile...), p.Path...) {
			add(pt.XFrom)
			add(pt.YFrom)
			add(pt.ZFrom)
			add(pt.RadiusFrom)
		}
		for _, loop := range p.Holes {
			for _, pt := range loop {
				add(pt.XFrom)
				add(pt.YFrom)
				add(pt.ZFrom)
				add(pt.RadiusFrom)
			}
		}
	}
	for _, a := range d.Assemblies {
		for _, c := range a.Children {
			addAll(c.PositionFrom)
			if c.Pattern != nil {
				addAll(c.Pattern.OffsetFrom)
				addAll(c.Pattern.RowOffsetFrom)
				addAll(c.Pattern.ColumnOffsetFrom)
				add(c.Pattern.AngleFrom)
			}
		}
		for _, f := range a.Interfaces {
			addAll(f.PositionFrom)
		}
		for _, f := range a.Features {
			add(f.RadiusFrom)
			add(f.ThicknessFrom)
		}
	}
	for _, f := range d.Features {
		add(f.RadiusFrom)
		add(f.ThicknessFrom)
	}
	return out
}

// unboundPatternProblems is issue 10: a set of positions that could be a pattern
// and carries no binding.
//
// # The narrow first cut, as the issue asks for
//
// Three or more parts of the SAME shape and the SAME size, sitting on one axis
// at evenly spaced coordinates, identical on the other two axes, none of which
// binds that axis. Three and not two, because two points are evenly spaced by
// definition and a pair is not yet a pattern. Same shape and size, because a
// row of different parts that happen to line up is a layout and not a repeat.
// Identical on the other axes, because "a grid whose rows are evenly spaced" is
// a second, harder question and guessing at it is how a checker starts inventing
// findings.
//
// Reported only for a document that HAS parameters: with nothing to bind to,
// "bind this" is advice nobody can take.
func (d *Document) unboundPatternProblems() []Problem {
	if len(d.Parameters) == 0 && len(d.Derived) == 0 {
		return nil
	}
	var out []Problem
	// Top-level parts and definitions separately: a definition's position is in
	// its own frame, and a row across two frames is a row nothing measured.
	for _, parts := range [][]Part{d.Parts, d.Definitions} {
		out = append(out, evenlySpacedAndUnbound(parts)...)
	}
	return out
}

// spacingEpsilon is how unequal two gaps may be and still count as evenly
// spaced: a part per thousand of the gap. Relative, for the reason
// bindingEpsilon is — the tolerance that is invisible on a 120 mm pitch is the
// whole gap on a 0.2 mm one.
const spacingEpsilon = 1e-3

// rowMember is one part of a candidate row: where it sits, and the part itself
// so the check can ask whether it binds the axis it sits on.
type rowMember struct {
	id string
	at [3]float64
	p  Part
}

func evenlySpacedAndUnbound(parts []Part) []Problem {
	groups := map[string][]rowMember{}
	var order []string
	for _, p := range parts {
		var at [3]float64
		copy(at[:], padTo3(p.Position))
		key := strings.ToLower(strings.TrimSpace(p.Shape)) + "|" + sizeSignature(p.Size)
		if _, seen := groups[key]; !seen {
			order = append(order, key)
		}
		groups[key] = append(groups[key], rowMember{id: p.ID, at: at, p: p})
	}

	var out []Problem
	for _, key := range order {
		members := groups[key]
		if len(members) < 3 {
			continue
		}
		for axis, name := range []string{"x", "y", "z"} {
			sorted := append([]rowMember(nil), members...)
			sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].at[axis] < sorted[j].at[axis] })
			if !inOneRow(sorted, axis) || !evenlySpaced(sorted, axis) {
				continue
			}
			bound := false
			for _, m := range sorted {
				if _, has := m.p.PositionFrom[name]; has {
					bound = true
				}
			}
			if bound {
				continue
			}
			ids := make([]string, 0, len(sorted))
			for _, m := range sorted {
				ids = append(ids, m.id)
			}
			step := sorted[1].at[axis] - sorted[0].at[axis]
			label := commonLabel(ids)
			if label == "" {
				label = sorted[0].id
			}
			out = append(out, Problem{Severity: Warning, Name: label,
				Detail: fmt.Sprintf("is %d parts evenly spaced %g apart along %s (%s) with nothing "+
					"bound: their coordinates are typed numbers, so this reads as a pattern and "+
					"behaves as a snapshot — change a parameter and everything else moves while "+
					"these stay. Place them with one \"pattern\", or bind each \"position_from\" %q",
					len(ids), step, name, strings.Join(ids, ", "), name)})
			break // One axis per group. A row is a row on one axis.
		}
	}
	return out
}

// inOneRow reports whether every member shares the other two coordinates, so
// the group really is a row along one axis and not a scatter.
func inOneRow(members []rowMember, axis int) bool {
	for other := 0; other < 3; other++ {
		if other == axis {
			continue
		}
		for _, m := range members[1:] {
			if m.at[other] != members[0].at[other] {
				return false
			}
		}
	}
	return true
}

// evenlySpaced reports whether successive coordinates on axis differ by the same
// non-zero step.
func evenlySpaced(members []rowMember, axis int) bool {
	step := members[1].at[axis] - members[0].at[axis]
	if step == 0 {
		return false
	}
	for i := 2; i < len(members); i++ {
		gap := members[i].at[axis] - members[i-1].at[axis]
		if math.Abs(gap-step) > spacingEpsilon*math.Max(math.Abs(step), 1) {
			return false
		}
	}
	return true
}

// sizeSignature is a part's size map as one deterministic string, so two parts
// of the same shape and the same dimensions group together.
func sizeSignature(size map[string]float64) string {
	keys := sortedFloatKeys(size)
	items := make([]string, 0, len(keys))
	for _, k := range keys {
		items = append(items, fmt.Sprintf("%s=%g", k, size[k]))
	}
	return strings.Join(items, ",")
}
