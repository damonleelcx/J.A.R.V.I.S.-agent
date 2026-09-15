package geometry

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// Copies written out one by one that could be one pattern.
//
// Phase 2, stage A3 of docs/plan-2026-09-13-millions-of-parts.md.
//
// # The problem this solves
//
// A pattern (pattern.go) is how a design places the same thing many times, and the
// contract says so. A model can still write forty bolts as forty children, each at
// a position it worked out: forty times the tokens, forty numbers to get wrong, and
// a revision that moves the row has to move every one of them. Nothing refuses
// that document — it builds — so nothing said anything.
//
// # Why a warning, never a refusal
//
// Forty children at equal steps are a correct model, just an expensive one, and
// some rows are equal by coincidence and meant to be edited apart later. Refusing
// would take a buildable design away from the person over a matter of spelling. So
// this names what it found and the exact pattern that places the same copies, and
// the turn passes that on (internal/agent/repetition.go).
//
// # What counts
//
// Siblings in ONE assembly that place the same ref with the same mirror and the
// same attachment and no pattern of their own, at least minEnumeratedRun of them,
// whose placements are exactly what one pattern would produce from the first:
//
//	linear  positions in written order a constant step apart, rotations equal;
//	grid    rows × columns in written order, row by row, rotations equal;
//	polar   positions turned by a constant angle about x, y or z through the
//	        assembly's origin — which is where a polar pattern turns — with each
//	        rotation turned with them, or all the same (said, see Warning).
//
// A group that is one line is reported as a line, never as a grid of one row; a
// group that is neither a grid nor a ring is searched for straight runs, each
// reported on its own.
//
// # Tolerance, and why it is not floating point's
//
// Two placements agree when they differ by less than one part in a thousand of the
// step (or the ring's radius). That is far looser than floating point needs and it
// is on purpose: a model rounds a bolt circle to two or three decimals, and a ring
// at 34.64 and 34.641 is still a ring. It is far tighter than any spacing somebody
// chose on purpose — 10, 20, 30, 31 is not a row of four.
//
// # Deterministic
//
// Assemblies in document order, groups in the order their first child is written,
// runs in written order. No map is ranged over. The same document gives the same
// warnings in the same order, which is what lets a turn's note be compared with the
// last one.

// minEnumeratedRun is the fewest copies worth a pattern: three bolts written out is
// not what makes a car expensive, and flagging every pair of wheels would teach a
// reader to ignore the note.
const minEnumeratedRun = 4

// repetitionTolerance is the fraction of a step two placements may differ by.
const repetitionTolerance = 1e-3

// Repetition is one run of children that could be one patterned child.
type Repetition struct {
	Assembly string
	Ref      string
	// Children are the ids written out, in written order. The first one's position,
	// rotation and mirror are the patterned child's.
	Children []string
	Pattern  Pattern
	// Unturned marks a polar run whose copies all face the same way. A polar
	// pattern turns each copy with the circle, so it places the same thing only
	// when the part is round about that axis — a screw about its own, not a bracket.
	Unturned bool
}

// EnumeratedRepetition finds every run of placements in the document's assemblies
// that one pattern would place. Empty for a document with no tree.
func (d Document) EnumeratedRepetition() []Repetition {
	var out []Repetition
	for _, a := range d.Assemblies {
		out = append(out, assemblyRepetition(a)...)
	}
	return out
}

type sibling struct {
	id  string
	pos [3]float64
	m   [9]float64
}

func assemblyRepetition(a Assembly) []Repetition {
	type group struct {
		ref      string
		siblings []sibling
	}
	var groups []*group
	byKey := map[string]*group{}
	for _, c := range a.Children {
		if c.Pattern != nil || strings.TrimSpace(c.ID) == "" || strings.TrimSpace(c.Ref) == "" {
			continue
		}
		var pos [3]float64
		copy(pos[:], padTo3(c.Position))
		finite := true
		for _, v := range append(append([]float64(nil), pos[:]...), c.Rotation...) {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				finite = false
			}
		}
		if !finite {
			continue
		}
		key := c.Ref + "\x00" + c.Mirror + "\x00" + c.At
		g := byKey[key]
		if g == nil {
			g = &group{ref: c.Ref}
			byKey[key] = g
			groups = append(groups, g)
		}
		g.siblings = append(g.siblings, sibling{id: c.ID, pos: pos, m: RotationMatrix(degreesToRadians3(c.Rotation))})
	}

	var out []Repetition
	for _, g := range groups {
		if len(g.siblings) < minEnumeratedRun {
			continue
		}
		runs := linearRuns(g.siblings)
		found := func(s []sibling, p Pattern, unturned bool) {
			ids := make([]string, len(s))
			for i, x := range s {
				ids[i] = x.id
			}
			out = append(out, Repetition{Assembly: a.ID, Ref: g.ref, Children: ids, Pattern: p, Unturned: unturned})
		}
		if len(runs) == 1 && runs[0].length == len(g.siblings) {
			found(g.siblings, runs[0].pattern, false)
			continue
		}
		if p, ok := gridOf(g.siblings); ok {
			found(g.siblings, p, false)
			continue
		}
		if p, unturned, ok := ringOf(g.siblings); ok {
			found(g.siblings, p, unturned)
			continue
		}
		for _, r := range runs {
			found(g.siblings[r.start:r.start+r.length], r.pattern, false)
		}
	}
	return out
}

func norm3(a [3]float64) float64 { return math.Sqrt(a[0]*a[0] + a[1]*a[1] + a[2]*a[2]) }
func along(base, step [3]float64, k float64) [3]float64 {
	return [3]float64{base[0] + step[0]*k, base[1] + step[1]*k, base[2] + step[2]*k}
}

func sameMatrix(a, b [9]float64) bool {
	for i := range a {
		if math.Abs(a[i]-b[i]) > repetitionTolerance {
			return false
		}
	}
	return true
}

type linearRun struct {
	start, length int
	pattern       Pattern
}

// linearRuns is every maximal straight run of at least minEnumeratedRun in written
// order: each child the same step from the one before, measured from the run's
// first child so rounding does not accumulate.
func linearRuns(s []sibling) []linearRun {
	var out []linearRun
	for i := 0; i+minEnumeratedRun <= len(s); {
		step := sub3(s[i+1].pos, s[i].pos)
		tol := repetitionTolerance * norm3(step)
		if norm3(step) < 1e-12 {
			i++
			continue
		}
		j := i
		for j+1 < len(s) && sameMatrix(s[j+1].m, s[i].m) &&
			norm3(sub3(s[j+1].pos, along(s[i].pos, step, float64(j+1-i)))) <= tol {
			j++
		}
		if n := j - i + 1; n >= minEnumeratedRun {
			out = append(out, linearRun{start: i, length: n,
				pattern: Pattern{Kind: "linear", Count: n, Offset: roundedVector(step)}})
			i = j + 1
			continue
		}
		i++
	}
	return out
}

// gridOf reads the siblings as rows × columns written row by row, trying the
// fewest columns first. Both must be at least two: one row is a line.
func gridOf(s []sibling) (Pattern, bool) {
	n := len(s)
	for columns := 2; columns <= n/2; columns++ {
		if n%columns != 0 {
			continue
		}
		rows := n / columns
		col, row := sub3(s[1].pos, s[0].pos), sub3(s[columns].pos, s[0].pos)
		unit := math.Min(norm3(col), norm3(row))
		if unit < 1e-12 {
			continue
		}
		ok := true
		for k := 0; k < n && ok; k++ {
			want := along(along(s[0].pos, row, float64(k/columns)), col, float64(k%columns))
			ok = sameMatrix(s[k].m, s[0].m) && norm3(sub3(s[k].pos, want)) <= repetitionTolerance*unit
		}
		if ok {
			return Pattern{Kind: "grid", Rows: rows, Columns: columns,
				RowOffset: roundedVector(row), ColumnOffset: roundedVector(col)}, true
		}
	}
	return Pattern{}, false
}

// ringOf reads the siblings as a polar pattern about x, y or z through the origin:
// the second child's angle from the first is the step, and every child must sit at
// the first one's position turned by its multiple of that step. Checked by turning,
// with the same rotation the pattern uses, rather than by comparing angles — so the
// sign convention is whatever pattern.go's is, not a second opinion about it.
func ringOf(s []sibling) (p Pattern, unturned, ok bool) {
	n := len(s)
	// The two coordinates across each axis, in the order a positive turn carries
	// the first towards the second.
	across := map[string][2]int{"x": {1, 2}, "y": {2, 0}, "z": {0, 1}}
	for _, about := range []string{"x", "y", "z"} {
		i, j := across[about][0], across[about][1]
		radius := math.Hypot(s[0].pos[i], s[0].pos[j])
		if radius < 1e-12 {
			continue
		}
		step := math.Atan2(s[1].pos[j], s[1].pos[i]) - math.Atan2(s[0].pos[j], s[0].pos[i])
		for step <= -math.Pi {
			step += 2 * math.Pi
		}
		for step > math.Pi {
			step -= 2 * math.Pi
		}
		if math.Abs(step) < 1e-9 || float64(n)*math.Abs(step) > 2*math.Pi*(1+repetitionTolerance) {
			continue
		}
		for _, angle := range []float64{step, -step} {
			turnedAll, sameAll, fits := true, true, true
			for k := 0; k < n && fits; k++ {
				turn := RotationMatrix(axisRotation(about, angle*float64(k)))
				fits = norm3(sub3(s[k].pos, mulMatVec(turn, s[0].pos))) <= repetitionTolerance*radius
				turnedAll = turnedAll && sameMatrix(s[k].m, mulMat3(turn, s[0].m))
				sameAll = sameAll && sameMatrix(s[k].m, s[0].m)
			}
			if !fits || (!turnedAll && !sameAll) {
				continue
			}
			degrees := angle * 180 / math.Pi
			// Zero is a whole turn. A whole turn written the other way round places the
			// same copies, numbered the other way, so it is a whole turn too.
			sweep := 0.0
			if math.Abs(float64(n)*math.Abs(degrees)-360) > 360*repetitionTolerance {
				sweep = round(degrees*float64(n-1), 6)
			}
			return Pattern{Kind: "polar", Count: n, About: about, Angle: sweep}, !turnedAll, true
		}
	}
	return Pattern{}, false, false
}

// roundedVector is a step as it would be written: nine decimals, and no "-0".
func roundedVector(v [3]float64) []float64 {
	out := make([]float64, 3)
	for i, x := range v {
		if out[i] = round(x, 9); out[i] == 0 {
			out[i] = 0 // and not -0, which JSON writes as "-0"
		}
	}
	return out
}

// Warning is the sentence a turn passes on: where, what, how many, and the
// pattern that places the same copies, spelled as the contract spells it.
func (r Repetition) Warning() string {
	body, _ := json.Marshal(r.Pattern)
	var how string
	switch r.Pattern.Kind {
	case "linear":
		how = fmt.Sprintf("in a straight line, each %s from the last", vectorText(r.Pattern.Offset))
	case "grid":
		how = fmt.Sprintf("in a grid of %d rows and %d columns", r.Pattern.Rows, r.Pattern.Columns)
	case "polar":
		how = fmt.Sprintf("evenly round the %s axis", r.Pattern.About)
	}
	names := r.Children
	if len(names) > 6 {
		names = append(append(append([]string(nil), names[:3]...), "…"), names[len(names)-1])
	}
	out := fmt.Sprintf("Assembly %q places %q %d times as separate children (%s) %s. One child with "+
		"\"pattern\": %s at the first one's place puts the same copies there, and a change to it changes every one.",
		r.Assembly, r.Ref, len(r.Children), strings.Join(names, ", "), how, body)
	if r.Unturned {
		out += " They all face the same way, and a polar pattern turns each copy with the circle, so it is " +
			"the same only if the part is round about that axis."
	}
	return out
}

func vectorText(v []float64) string {
	parts := make([]string, len(v))
	for i, x := range v {
		parts[i] = fmt.Sprint(x)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}
