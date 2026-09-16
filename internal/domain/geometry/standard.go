package geometry

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Standard parts: a screw, a nut, a washer, a bearing or a section, named by the
// designation a catalogue sells it under.
//
// Phase 2, stage A3 of docs/plan-2026-09-13-millions-of-parts.md.
//
// # The problem this solves
//
// A car is thousands of fasteners. Before this, the only way a model could put an
// M8 cap screw in a document was to DRAW one — a cylinder for the head, a cylinder
// for the shank, two numbers it had to recall for each — and it had to do that
// again for every screw that was not the same definition. Two things go wrong at
// scale, and both were already measured on smaller things:
//
//   - the figures are recalled, and recalled figures are wrong often enough that
//     standards.go exists to label them (a NEMA 17 bolt pattern written as 41 mm
//     in four runs of the same prompt, once);
//   - every drawn screw is tokens, and a 30,000-part car spends its budget
//     describing heads and shanks nobody asked to see.
//
// # Why a shape word, and why it becomes other shapes
//
// For the reason the gear is one (gear.go): asking for the NAME and drawing the
// part here takes the recall away from the model entirely, and a panel, a
// revision and every check in a turn can still read it. A designation is
// written out as the revolve or extrusion it is BEFORE anything reads the part —
// the kernel, the mesh, the faults, the measurement — so none of them has a case
// for it and the sidecar needs no change. The browser cannot call this and holds
// a copy (standardPart in forge3d.js), held to this one figure for figure by
// TestRendererDrawsTheSameStandardPartAsTheExporter.
//
// # Why the table is small
//
// Every row below is a nominal figure copied from the standard it cites, and a
// figure in this table is not labelled recalled the way a model's is — it is
// asserted. So it is a short, exact list rather than a long, approximate one, and
// a designation not on it is REFUSED by name with the nearest ones that are,
// rather than guessed at from its shape.
//
// # What is deliberately not drawn
//
// Threads, a cap screw's hexagon socket, chamfers, a nut's washer face, a
// bearing's rings, balls and seals. A screw is the envelope a clearance hole and
// a counterbore are designed around; a bearing is the ring a housing is bored
// for. Everything the table carries is a MAXIMUM or nominal envelope figure, so
// what is drawn is never smaller than the part that will be fitted.

// standardShape is the word, spelled once.
const standardShape = "standard"

// isStandard reports whether a part is named by its designation.
func isStandard(p Part) bool {
	return strings.EqualFold(strings.TrimSpace(p.Shape), standardShape)
}

// ---- The catalogue -----------------------------------------------------------
//
// Millimetres throughout, which is what every one of these standards publishes.

// iso4762Lengths is ISO 4762's series of nominal lengths l, in mm, as far as the
// longest screw below.
var iso4762Lengths = []float64{5, 6, 8, 10, 12, 16, 20, 25, 30, 35, 40, 45, 50, 55, 60, 65, 70, 80, 90, 100, 110, 120}

// iso4762 is ISO 4762:2004, hexagon socket head cap screws: thread d, head
// diameter dk (max, plain head), head height k (max, equal to d), and the
// shortest and longest commercial length l the standard tabulates for that size.
var iso4762 = []struct {
	Size                          string
	D, HeadDiameter, HeadHeight   float64
	ShortestLength, LongestLength float64
}{
	{"M3", 3, 5.5, 3, 5, 30},
	{"M4", 4, 7, 4, 6, 40},
	{"M5", 5, 8.5, 5, 8, 50},
	{"M6", 6, 10, 6, 10, 60},
	{"M8", 8, 13, 8, 12, 80},
	{"M10", 10, 16, 10, 16, 100},
	{"M12", 12, 18, 12, 20, 120},
}

// iso4032 is ISO 4032:2012, hexagon regular nuts (style 1): thread d, width
// across flats s, and height m (max).
var iso4032 = []struct {
	Size                   string
	D, AcrossFlats, Height float64
}{
	{"M3", 3, 5.5, 2.4},
	{"M4", 4, 7, 3.2},
	{"M5", 5, 8, 4.7},
	{"M6", 6, 10, 5.2},
	{"M8", 8, 13, 6.8},
	{"M10", 10, 16, 8.4},
	{"M12", 12, 18, 10.8},
}

// iso7089 is ISO 7089:2000, plain washers, normal series: hole d1 (min), outside
// diameter d2 (max) and thickness h (nominal), for the thread size named.
var iso7089 = []struct {
	Size                    string
	Inner, Outer, Thickness float64
}{
	{"M3", 3.2, 7, 0.5},
	{"M4", 4.3, 9, 0.8},
	{"M5", 5.3, 10, 1},
	{"M6", 6.4, 12, 1.6},
	{"M8", 8.4, 16, 1.6},
	{"M10", 10.5, 20, 2},
	{"M12", 13, 24, 2.5},
}

// iso15 is ISO 15:2017's boundary dimensions for single-row deep-groove ball
// bearings: bore d, outside diameter D and width B. 608 is diameter series 0 in
// the 8 mm bore; the 6000 series continues it.
var iso15 = []struct {
	Series             string
	Bore, Outer, Width float64
}{
	{"608", 8, 22, 7},
	{"6000", 10, 26, 8},
	{"6001", 12, 28, 8},
	{"6002", 15, 32, 9},
	{"6003", 17, 35, 10},
	{"6004", 20, 42, 12},
	{"6005", 25, 47, 12},
}

// en10219 is EN 10219-2:2006, cold-formed square (SHS) and rectangular (RHS)
// hollow sections: outside B × H and wall t. The corners are the radii the
// standard gives for calculation when t is 6 mm or less: 2t outside, t inside.
var en10219 = []struct {
	Kind    string
	B, H, T float64
}{
	{"SHS", 20, 20, 2},
	{"SHS", 30, 30, 3},
	{"SHS", 40, 40, 3},
	{"SHS", 40, 40, 4},
	{"SHS", 50, 50, 5},
	{"RHS", 40, 20, 2},
	{"RHS", 60, 40, 3},
}

// en10056 is EN 10056-1:2017, hot-rolled equal-leg angles: leg a, thickness t,
// root radius r1 and toe radius r2.
var en10056 = []struct {
	A, T, RootRadius, ToeRadius float64
}{
	{20, 3, 3.5, 2},
	{30, 3, 5, 2.5},
	{40, 4, 6, 3},
	{50, 5, 7, 3.5},
}

// standardDrawing is what a designation becomes, in the document's units.
type standardDrawing struct {
	shape   string // "revolve" or "extrusion"
	profile []Point
	holes   [][]Point
	depth   float64 // an extrusion's; zero for a revolve
	// turned stands an extrusion's local Z up along Y, so a nut's axis is the same
	// axis a screw's is.
	turned bool
}

// standardSpec is one designation in the catalogue.
type standardSpec struct {
	Designation string
	// family is the standard's own prefix ("ISO 4762"), and size what follows it
	// ("M8x30") — which is what a near match compares against a designation
	// written under the wrong standard's number.
	family, size string
	// needsLength marks a section, which is sold by the metre and has no length
	// of its own.
	needsLength bool
	// draw is the part at scale k (document units per millimetre) and, for a
	// section, length (already in document units).
	draw func(k, length float64) standardDrawing
}

func mm(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// circleLoop is a round hole of radius r, two arcs through their midpoints — the
// spelling a gear's bore uses.
func circleLoop(r float64) []Point {
	return []Point{
		{X: r, Y: 0, Via: &Point{X: 0, Y: -r}},
		{X: -r, Y: 0, Via: &Point{X: 0, Y: r}},
	}
}

// ringSection is a washer's or a bearing's section: an annulus of inner and outer
// radius and axial width, centred on the part's origin, to be revolved about Y.
func ringSection(inner, outer, width float64) []Point {
	return []Point{{X: inner, Y: -width / 2}, {X: outer, Y: -width / 2}, {X: outer, Y: width / 2}, {X: inner, Y: width / 2}}
}

var standardCatalog, standardIndex = buildStandardCatalog()

func buildStandardCatalog() ([]standardSpec, map[string]int) {
	var out []standardSpec
	for _, s := range iso4762 {
		s := s
		for _, l := range iso4762Lengths {
			if l < s.ShortestLength || l > s.LongestLength {
				continue
			}
			l := l
			size := s.Size + "x" + mm(l)
			out = append(out, standardSpec{Designation: "ISO 4762 " + size, family: "ISO 4762", size: size,
				draw: func(k, _ float64) standardDrawing {
					// The underside of the head is the origin: that is the face that
					// sits on whatever the screw clamps, so a screw placed at a
					// surface's height sits on it. The shank runs down -Y.
					r, head, length, height := s.D/2*k, s.HeadDiameter/2*k, l*k, s.HeadHeight*k
					return standardDrawing{shape: "revolve", profile: []Point{
						{X: 0, Y: -length}, {X: r, Y: -length}, {X: r, Y: 0},
						{X: head, Y: 0}, {X: head, Y: height}, {X: 0, Y: height}}}
				}})
		}
	}
	for _, n := range iso4032 {
		n := n
		out = append(out, standardSpec{Designation: "ISO 4032 " + n.Size, family: "ISO 4032", size: n.Size,
			draw: func(k, _ float64) standardDrawing {
				// A hexagon with two flats across y, corners on the x axis; the
				// sqrt is correctly rounded in Go and in V8 alike, so the browser's
				// copy lands on the same corners bit for bit.
				corner, half := n.AcrossFlats/math.Sqrt(3)*k, n.AcrossFlats*k/2
				return standardDrawing{shape: "extrusion", turned: true, depth: n.Height * k,
					profile: []Point{{X: corner, Y: 0}, {X: corner / 2, Y: half}, {X: -corner / 2, Y: half},
						{X: -corner, Y: 0}, {X: -corner / 2, Y: -half}, {X: corner / 2, Y: -half}},
					holes: [][]Point{circleLoop(n.D / 2 * k)}}
			}})
	}
	for _, w := range iso7089 {
		w := w
		out = append(out, standardSpec{Designation: "ISO 7089 " + w.Size, family: "ISO 7089", size: w.Size,
			draw: func(k, _ float64) standardDrawing {
				return standardDrawing{shape: "revolve", profile: ringSection(w.Inner/2*k, w.Outer/2*k, w.Thickness*k)}
			}})
	}
	for _, b := range iso15 {
		b := b
		out = append(out, standardSpec{Designation: "ISO 15 " + b.Series, family: "ISO 15", size: b.Series,
			draw: func(k, _ float64) standardDrawing {
				return standardDrawing{shape: "revolve", profile: ringSection(b.Bore/2*k, b.Outer/2*k, b.Width*k)}
			}})
	}
	for _, h := range en10219 {
		h := h
		size := h.Kind + " " + mm(h.B) + "x" + mm(h.H) + "x" + mm(h.T)
		out = append(out, standardSpec{Designation: "EN 10219 " + size, family: "EN 10219", size: size, needsLength: true,
			draw: func(k, length float64) standardDrawing {
				// Centred on its own axis, which is the line a frame is drawn along.
				x, y, t := h.B/2*k, h.H/2*k, h.T*k
				outside, inside := 2*h.T*k, h.T*k
				return standardDrawing{shape: "extrusion", depth: length,
					profile: []Point{{X: -x, Y: -y, Radius: outside}, {X: x, Y: -y, Radius: outside},
						{X: x, Y: y, Radius: outside}, {X: -x, Y: y, Radius: outside}},
					holes: [][]Point{{{X: -(x - t), Y: -(y - t), Radius: inside}, {X: x - t, Y: -(y - t), Radius: inside},
						{X: x - t, Y: y - t, Radius: inside}, {X: -(x - t), Y: y - t, Radius: inside}}}}
			}})
	}
	for _, a := range en10056 {
		a := a
		size := "L" + mm(a.A) + "x" + mm(a.A) + "x" + mm(a.T)
		out = append(out, standardSpec{Designation: "EN 10056 " + size, family: "EN 10056", size: size, needsLength: true,
			draw: func(k, length float64) standardDrawing {
				// The heel at the origin and the legs along +x and +y, so an angle
				// placed at a corner sits in it.
				leg, t := a.A*k, a.T*k
				root, toe := a.RootRadius*k, a.ToeRadius*k
				return standardDrawing{shape: "extrusion", depth: length, profile: []Point{
					{X: 0, Y: 0}, {X: leg, Y: 0}, {X: leg, Y: t, Radius: toe},
					{X: t, Y: t, Radius: root}, {X: t, Y: leg, Radius: toe}, {X: 0, Y: leg}}}
			}})
	}
	index := make(map[string]int, len(out))
	for i, s := range out {
		index[normaliseDesignation(s.Designation)] = i
	}
	return out, index
}

// normaliseDesignation is how a written designation is compared: ASCII letters
// upper-cased, "×" read as "x", runs of spaces as one. Deliberately no more
// forgiving than that — "608-2RS" is a different bearing from "608", and the
// refusal names the one FORGE has.
func normaliseDesignation(s string) string {
	s = strings.ReplaceAll(s, "×", "x")
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' {
			r -= 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// StandardDesignations is every designation in the catalogue, in catalogue order.
func StandardDesignations() []string {
	out := make([]string, len(standardCatalog))
	for i, s := range standardCatalog {
		out[i] = s.Designation
	}
	return out
}

// StandardGuide is the catalogue as the contract teaches it, rendered from the
// table above rather than typed into the prompt: a designation added here is
// offered the day it is added, and one the prompt names is one this accepts.
// TestTheContractTeachesEveryStandardPartFORGEHas holds both directions.
func StandardGuide() string {
	var b strings.Builder
	b.WriteString("    Socket head cap screws, ISO 4762, written size x length:\n")
	for _, s := range iso4762 {
		fmt.Fprintf(&b, "      \"ISO 4762 %sx<length>\", a length from %s to %s\n", s.Size, mm(s.ShortestLength), mm(s.LongestLength))
	}
	lengths := make([]string, len(iso4762Lengths))
	for i, l := range iso4762Lengths {
		lengths[i] = mm(l)
	}
	fmt.Fprintf(&b, "      and the length one of %s.\n", strings.Join(lengths, ", "))
	family := func(title, prefix, note string) {
		var names []string
		for _, s := range standardCatalog {
			if s.family == prefix {
				names = append(names, `"`+s.Designation+`"`)
			}
		}
		fmt.Fprintf(&b, "    %s%s: %s\n", title, note, strings.Join(names, ", "))
	}
	family("Hexagon nuts, ISO 4032", "ISO 4032", "")
	family("Plain washers, ISO 7089", "ISO 7089", "")
	family("Deep-groove ball bearings, ISO 15", "ISO 15", "")
	family("Hollow sections, EN 10219-2", "EN 10219", ` (with "size": {"length": ...})`)
	family("Equal angles, EN 10056-1", "EN 10056", ` (with "size": {"length": ...})`)
	return strings.TrimRight(b.String(), "\n")
}

// expandStandards returns the document with every standard part written out as
// the revolve or extrusion it is, and says what it could not draw.
//
// Idempotent, like expandGears: what it writes is not a standard part, so a
// document expanded twice is expanded once. Called after the tree and before the
// gears and repeats, so a placed definition and a repeated screw are both a
// revolve by the time anything reads them.
func expandStandards(d Document) (Document, []Problem) {
	need := false
	for _, p := range d.Parts {
		if isStandard(p) {
			need = true
			break
		}
	}
	if !need {
		return d, nil
	}
	unit, known := ParseUnit(d.Units)
	toMM, _ := unit.toMM()

	var problems []Problem
	out := d
	out.Parts = make([]Part, 0, len(d.Parts))
	for _, p := range d.Parts {
		if !isStandard(p) {
			out.Parts = append(out.Parts, p)
			continue
		}
		fail := func(format string, args ...any) {
			problems = append(problems, Problem{Severity: Error, Name: p.Label(), Detail: fmt.Sprintf(format, args...)})
		}
		i, found := standardIndex[normaliseDesignation(p.Standard)]
		switch {
		case strings.TrimSpace(p.Standard) == "":
			fail("is a standard part that names no designation; set \"standard\" to one FORGE has, such as %q",
				standardCatalog[standardIndex[normaliseDesignation("ISO 4762 M8x30")]].Designation)
			continue
		case !found:
			fail("names the standard part %q, which is not in FORGE's catalogue; the nearest it has are %s. "+
				"Write one of those, or draw the part from shapes", p.Standard, quotedList(nearestStandards(p.Standard, 3)))
			continue
		case !known:
			// Left out rather than drawn as millimetres: a guess about scale is the
			// difference between a washer and a manhole cover.
			fail("is %q, whose figures are published in millimetres, and this document states no unit "+
				"FORGE can convert them to", p.Standard)
			continue
		}
		spec := standardCatalog[i]
		length := 0.0
		if spec.needsLength {
			l, ok := p.Size["length"]
			if !ok || math.IsNaN(l) || math.IsInf(l, 0) || l <= 0 {
				fail("is %q, a section sold by length, and gives no positive \"size\": {\"length\": ...} "+
					"to cut it to", spec.Designation)
				continue
			}
			length = l
		}
		drawn := spec.draw(1/toMM, length)
		q := p
		q.Shape = drawn.shape
		q.Standard = spec.Designation
		q.Profile, q.Holes = drawn.profile, drawn.holes
		q.Path, q.PathClosed, q.Script = nil, false, ""
		q.SizeFrom = nil
		q.Axis = ""
		q.Size = map[string]float64{}
		if drawn.shape == "revolve" {
			q.Axis = "y"
		} else {
			q.Size["depth"] = drawn.depth
		}
		if q.Name == "" {
			q.Name = spec.Designation
		}
		if drawn.turned {
			// Local Z up along +Y: a quarter turn about x, applied inside the part's
			// own placement so its position, rotation and mirror still mean what
			// they meant.
			q.Position, q.Rotation, q.Mirrored = placementOf(p.Position, p.Rotation, p.Mirrored).
				then(placementOf(nil, []float64{-90, 0, 0}, false)).stored()
		}
		out.Parts = append(out.Parts, q)
	}
	return out, problems
}

func quotedList(items []string) string {
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = strconv.Quote(s)
	}
	switch len(quoted) {
	case 0:
		return "none"
	case 1:
		return quoted[0]
	}
	return strings.Join(quoted[:len(quoted)-1], ", ") + " and " + quoted[len(quoted)-1]
}

// nearestStandards names the n designations closest to one that is not in the
// catalogue, nearest first and in catalogue order among equals.
//
// Each is scored twice and keeps the better: against the whole designation, and
// against its size alone compared with as many trailing words of what was
// written — so "DIN 912 M8x30", the same screw under its old German number,
// finds "ISO 4762 M8x30" first, and "608-2RS" finds "ISO 15 608".
func nearestStandards(asked string, n int) []string {
	norm := normaliseDesignation(asked)
	words := strings.Fields(norm)
	type scored struct {
		designation string
		score       int
	}
	all := make([]scored, 0, len(standardCatalog))
	for _, s := range standardCatalog {
		score := editDistance(norm, normaliseDesignation(s.Designation))
		size := normaliseDesignation(s.size)
		if k := len(strings.Fields(size)); len(words) >= k {
			if d := editDistance(strings.Join(words[len(words)-k:], " "), size); d < score {
				score = d
			}
		}
		all = append(all, scored{s.Designation, score})
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].score < all[j].score })
	if len(all) > n {
		all = all[:n]
	}
	out := make([]string, len(all))
	for i, s := range all {
		out[i] = s.designation
	}
	return out
}

// editDistance is the Levenshtein distance between two strings, by rune.
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

// StandardPartForTest writes out one standard part in a document of the given
// units, for the fence that holds the browser's copy to the same answer. ok is
// false when FORGE refuses it.
//
// Named for what it is, like GearOutlineForTest: nothing in a product path should
// reach for it.
func StandardPartForTest(p Part, units string) (Part, bool) {
	e, problems := expandStandards(Document{Units: units, Parts: []Part{p}})
	if anyError(problems) || len(e.Parts) != 1 {
		return Part{}, false
	}
	return e.Parts[0], true
}
