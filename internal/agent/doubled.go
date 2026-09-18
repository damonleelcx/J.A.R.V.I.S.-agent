package agent

import (
	"fmt"
	"math"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// A placement written twice: on a definition and on the child that places it.
//
// # The problem this solves
//
// Live run 3 of 2026-09-17 (docs/spikes/2026-09-17-live-verification) wrote the lug
// nut's position [50, 20, 0], rotation [90, 0, 0] and position_from on BOTH the
// definition and the patterned child that placed it. A definition's position is where
// it sits inside whatever places it (the contract says so, and the expansion composes
// them: child position + child rotation · definition position), so the two added up:
// the first nut sat at [100, 20, 20], the ring had a radius of 102 mm round a 35 mm
// hub instead of the 50 mm bolt circle, 91% of the nuts were inside the tyre, and the
// nuts faced the wrong way. The look said "floating" and the interference repair said
// "91% inside"; neither named the cause, so nothing the model was told could fix it.
//
// # The rule
//
// A child that places a DEFINITION (not an assembly) is reported when, on some axis,
// the definition's position and the child's are both non-zero and the SAME amount —
// the same number, or the same parameter expression in "position_from" — or when both
// carry the same non-zero rotation. That is what run 3 wrote, and what a model writes
// when it copies a placement into both places. An offset that differs (a definition
// drawn with its base on its origin, [0, 0, 20], placed at [300, 0, 0]) is the
// ordinary use of both and is never reported. A pattern's own step is not an offset
// of the child: copy 1 sits where the child's position says, so only the child's
// position and rotation are compared.
//
// # Why a note and never a rewrite
//
// Which of the two was meant cannot be known from the document: a definition may be
// drawn off its origin on purpose, and FORGE does not move parts the model placed.
// So the turn and the step say it, with where the part lands and the fix, and the
// model making the next pass is told in its prompt (doubledForStep, doubledForTurn).
// Fence: TestDoubledOffsets_ADefinitionAndItsChildCarryingTheSamePositionAreNamed.

// maxDoubledNotes bounds the sentences, as repetition.go bounds its own.
const maxDoubledNotes = 5

// doubledPlacement is one child that places a definition with the definition's own offset.
type doubledPlacement struct {
	key      string // assembly/child, stable across passes
	sentence string
}

// sameAmount reports whether two coordinates are the same non-zero amount.
func sameAmount(a, b float64) bool {
	if a == 0 || b == 0 {
		return false
	}
	return math.Abs(a-b) <= 1e-6*math.Max(1, math.Abs(a))
}

func coord(v []float64, i int) float64 {
	if i < len(v) {
		return v[i]
	}
	return 0
}

func vec3(v []float64) string {
	return fmt.Sprintf("[%s, %s, %s]", formatNumber(coord(v, 0)), formatNumber(coord(v, 1)), formatNumber(coord(v, 2)))
}

// boundTo is the expression an axis is bound to, normalised, or "".
func boundTo(from map[string]string, axis string) string {
	return strings.Join(strings.Fields(strings.ToLower(from[axis])), "")
}

// doubledPlacements is every child in d that places a definition with the same offset
// the definition carries, in document order.
func doubledPlacements(d *Prototype) []doubledPlacement {
	if d == nil || len(d.Definitions) == 0 || len(d.Assemblies) == 0 {
		return nil
	}
	defs := map[string]geometry.Part{}
	for _, p := range d.Definitions {
		if _, dup := defs[p.ID]; !dup {
			defs[p.ID] = p
		}
	}
	var out []doubledPlacement
	for _, a := range d.Assemblies {
		for _, c := range a.Children {
			def, ok := defs[c.Ref]
			if !ok {
				continue
			}
			var axes []string
			for i, axis := range positionAxes {
				if i > 2 {
					break
				}
				bound := boundTo(def.PositionFrom, axis)
				if sameAmount(coord(def.Position, i), coord(c.Position, i)) ||
					(bound != "" && bound == boundTo(c.PositionFrom, axis)) {
					axes = append(axes, axis)
				}
			}
			turned := false
			for i := 0; i < 3; i++ {
				if coord(def.Rotation, i) != 0 {
					turned = true
				}
			}
			if turned {
				for i := 0; i < 3; i++ {
					if math.Abs(coord(def.Rotation, i)-coord(c.Rotation, i)) > 1e-6 {
						turned = false
					}
				}
			}
			if len(axes) == 0 && !turned {
				continue
			}
			out = append(out, doubledPlacement{key: a.ID + "/" + c.ID, sentence: doubledSentence(a, c, def, axes, turned)})
		}
	}
	return out
}

// landsAt is where the definition's origin lands in the frame the child is measured
// in, worked out by the expansion itself rather than restated here.
func landsAt(c geometry.Child, def geometry.Part) []float64 {
	probe := geometry.Document{Units: "mm", Root: "probe",
		Definitions: []geometry.Part{{ID: def.ID, Shape: "box", Size: map[string]float64{"width": 1, "height": 1, "depth": 1},
			Position: def.Position, Rotation: def.Rotation}},
		Assemblies: []geometry.Assembly{{ID: "probe", Children: []geometry.Child{{ID: "c", Ref: def.ID,
			Position: c.Position, Rotation: c.Rotation}}}}}
	placed := probe.PlacedParts()
	if len(placed) != 1 {
		return nil
	}
	return placed[0].Position
}

func doubledSentence(a geometry.Assembly, c geometry.Child, def geometry.Part, axes []string, turned bool) string {
	var what []string
	if len(axes) > 0 {
		what = append(what, fmt.Sprintf("position %s (the same %s on %s)", vec3(def.Position),
			map[bool]string{true: "amount", false: "amounts"}[len(axes) == 1], strings.Join(axes, ", ")))
	}
	if turned {
		what = append(what, "rotation "+vec3(def.Rotation))
	}
	carried := strings.Join(what, " and ")
	s := fmt.Sprintf("Definition %q and the child %q that places it both carry %s, and the two add up: a "+
		"definition's position and rotation are measured inside whatever places it.", def.ID, a.ID+"/"+c.ID, carried)
	if at := landsAt(c, def); at != nil && len(axes) > 0 {
		s += fmt.Sprintf(" So it lands at %s", vec3(at))
		if c.Pattern != nil {
			s += " (the first copy of its pattern)"
		}
		s += fmt.Sprintf(" instead of %s.", vec3(c.Position))
	} else if turned {
		s += " So it is turned twice."
	}
	var fix []string
	if len(axes) > 0 {
		fix = append(fix, `"position": [0, 0, 0]`)
		if len(def.PositionFrom) > 0 {
			fix = append(fix, `no "position_from"`)
		}
	}
	if turned {
		fix = append(fix, `"rotation": [0, 0, 0]`)
	}
	return s + " Keep the placement in one place: give the definition " + strings.Join(fix, " and ") +
		", and place it with the child."
}

// doubledNotes is one sentence per doubled placement, bounded, then a count of the rest.
func doubledNotes(found []doubledPlacement) []string {
	out := make([]string, 0, min(len(found), maxDoubledNotes+1))
	for i, f := range found {
		if i == maxDoubledNotes {
			out = append(out, fmt.Sprintf("%d more child(ren) carry the offset of the definition they place.", len(found)-i))
			break
		}
		out = append(out, f.sentence)
	}
	return out
}

// newlyDoubled is what after doubles that before did not, so a placement carried from
// an earlier pass is said once, where it was written, and not on every pass after.
func newlyDoubled(before, after *Prototype) []doubledPlacement {
	old := map[string]string{}
	for _, f := range doubledPlacements(before) {
		old[f.key] = f.sentence
	}
	var out []doubledPlacement
	for _, f := range doubledPlacements(after) {
		if old[f.key] != f.sentence {
			out = append(out, f)
		}
	}
	return out
}

// noteDoubledOffsets tells the reader of a turn or a step what it doubled. Said once
// however many times it is called, as noteRepetition is.
func noteDoubledOffsets(reply *Reply, before *Prototype) {
	if reply == nil || reply.Prototype == nil {
		return
	}
	for _, line := range doubledNotes(newlyDoubled(before, reply.Prototype)) {
		if !strings.Contains(reply.Repaired, line) {
			reply.noteRepair(line)
		}
	}
}

// doubledForStep is what a build step is told about the model so far, or "".
func doubledForStep(d *Prototype) string {
	notes := doubledNotes(doubledPlacements(d))
	if len(notes) == 0 {
		return ""
	}
	return "\n\nPlaced twice over in the model so far — a definition and the child that places it carry the same " +
		"offset, so the part sits at their sum. When this step touches them, keep each placement in one place:\n- " +
		strings.Join(notes, "\n- ")
}

// doubledForTurn is what an ordinary turn's model is told about the model it is revising, or "".
func doubledForTurn(d *Prototype) string {
	notes := doubledNotes(doubledPlacements(d))
	if len(notes) == 0 {
		return ""
	}
	return "\n\n[FORGE's check found placements written twice in that model — on a definition and on the child " +
		"that places it — so those parts sit at the sum of the two. If this turn touches them, keep each " +
		"placement in one place:\n- " + strings.Join(notes, "\n- ") + "]"
}
