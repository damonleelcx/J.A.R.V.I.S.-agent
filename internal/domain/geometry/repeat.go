package geometry

import (
	"fmt"
	"math"
)

// Repeat says a part appears many times.
//
// # Why this and not generated code
//
// The wall Stage 3 was written for is that one JSON object means one part, so a
// wire wheel with sixty spokes is sixty hand-written objects — and the model
// starts losing parts at thirteen. The obvious answer was to let the model write
// build123d Python instead, where sixty spokes is a for-loop.
//
// It was the wrong answer for this system, and the reasoning is worth keeping
// because the pull towards it is strong:
//
//   - What the loop actually buys is REPETITION. Variables and expressions are
//     already here (parameters and derived), edge selection by rule is already
//     here (fillet "edges"), and conditionals are not what anyone was missing.
//     Repetition is the whole gap, and repetition is a pattern.
//   - Executing model-written Python means executing model-written Python. In
//     CPython a restricted-builtins sandbox is not a sandbox: __subclasses__
//     walks out of it. A real one is a separate process with dropped privileges,
//     scrubbed environment and rlimits — buildable, but it is a security
//     boundary bolted to a product feature, and this deployment holds a database
//     URL and a provider key in its environment.
//   - Everything downstream reads a Document: prototype_edit, compare, the parts
//     panel, the repair round-trip, every fence. Code produces a shape, not a
//     document, so all of that would need a bridge or would stop working.
//
// A pattern is declarative, costs no sandbox, keeps every one of those features
// working, and turns sixty objects into one. That is the same win with none of
// the bill.
//
// # What it deliberately does not do
//
// It repeats ONE part. It cannot repeat a group, and it cannot vary anything
// between copies except position and orientation. A staircase with treads that
// grow is not this, and should not be forced through it.
type Repeat struct {
	// Count is how many copies EXIST in total, the original included. Two is the
	// smallest that means anything.
	Count int `json:"count"`
	// About turns it into a circular pattern around "x", "y" or "z" through the
	// assembly origin. Empty means a straight line along Offset.
	About string `json:"about,omitempty"`
	// Angle is the total sweep of a circular pattern in DEGREES, the same unit
	// as Rotation. Zero means a full 360, which is what a wheel, a flange and a
	// bolt circle all want.
	Angle float64 `json:"angle,omitempty"`
	// Offset is the step between copies of a straight pattern, in the assembly's
	// units. Ignored when About is set.
	Offset []float64 `json:"offset,omitempty"`
	Note   string    `json:"note,omitempty"`
}

// expandRepeats returns the document with every repeated part written out, and
// says what it could not do.
//
// # Why it is a transform and not a shape
//
// The alternative was a "pattern" SHAPE, and it would have had to answer what a
// pattern's profile is, what its size is, and what a fillet on it means. A part
// that repeats is still a box or a sweep — the repetition is about where it is,
// not what it is — so it stays a modifier and every shape gets it for free.
//
// Idempotent: a document with nothing to expand comes back as it went in, which
// is what lets the three build entry points each call it without coordinating.
func expandRepeats(d Document) (Document, []Problem) {
	need := false
	for _, p := range d.Parts {
		if p.Repeat != nil {
			need = true
			break
		}
	}
	if !need {
		return d, nil
	}

	var problems []Problem
	out := d
	out.Parts = make([]Part, 0, len(d.Parts))
	// copies maps a patterned part's id to every id it became, so a feature that
	// names the original acts on all of them. A "fuse" of sixty spokes into a
	// hub is the commonest thing anyone will want to do with a pattern, and
	// making them name sixty ids by hand would give back what this just saved.
	copies := map[string][]string{}

	for _, p := range d.Parts {
		r := p.Repeat
		if r == nil {
			out.Parts = append(out.Parts, p)
			continue
		}
		if r.Count < 2 {
			problems = append(problems, Problem{Severity: Warning, Name: p.Label(),
				Detail: fmt.Sprintf("repeats %d time(s), which is not a repeat, so it is drawn once", r.Count)})
			q := p
			q.Repeat = nil
			out.Parts = append(out.Parts, q)
			continue
		}
		if r.Count > maxRepeat {
			problems = append(problems, Problem{Severity: Error, Name: p.Label(),
				Detail: fmt.Sprintf("repeats %d times, and %d is the most this build will draw: "+
					"past that a model stops being something a person can look at and starts "+
					"being something that stops the viewport", r.Count, maxRepeat)})
			continue
		}
		made := make([]string, 0, r.Count)
		for k := 0; k < r.Count; k++ {
			q := p
			q.Repeat = nil
			q.ID = fmt.Sprintf("%s-%d", p.ID, k+1)
			q.Name = fmt.Sprintf("%s %d", p.Label(), k+1)
			q.Position = placeCopy(p, r, k)
			q.Rotation = turnCopy(p, r, k)
			// Independent copies: a shared slice would make every spoke change
			// when one did.
			q.Size = cloneSize(p.Size)
			out.Parts = append(out.Parts, q)
			made = append(made, q.ID)
		}
		copies[p.ID] = made
	}

	if len(copies) > 0 {
		out.Features = retargetFeatures(d.Features, copies)
	}
	return out, problems
}

// maxRepeat bounds a pattern. Chosen from what the viewport can carry rather
// than from taste: past this the tessellation budget is spent on one part.
const maxRepeat = 512

func placeCopy(p Part, r *Repeat, k int) []float64 {
	pos := [3]float64{}
	copy(pos[:], padTo3(p.Position))
	if r.About == "" {
		step := [3]float64{}
		copy(step[:], padTo3(r.Offset))
		return []float64{pos[0] + step[0]*float64(k), pos[1] + step[1]*float64(k), pos[2] + step[2]*float64(k)}
	}
	a := sweepAngle(r) * float64(k)
	rot := axisRotation(r.About, a)
	turned := rotate(pos, rot)
	return []float64{turned[0], turned[1], turned[2]}
}

// turnCopy turns each copy with the pattern, so a spoke points outward rather
// than every copy facing the way the first one did.
func turnCopy(p Part, r *Repeat, k int) []float64 {
	base := [3]float64{}
	copy(base[:], padTo3(p.Rotation))
	if r.About == "" {
		return p.Rotation
	}
	a := sweepAngle(r) * float64(k) * 180 / math.Pi // Rotation is in DEGREES
	switch r.About {
	case "x":
		return []float64{base[0] + a, base[1], base[2]}
	case "y":
		return []float64{base[0], base[1] + a, base[2]}
	default:
		return []float64{base[0], base[1], base[2] + a}
	}
}

// sweepAngle is the angle BETWEEN copies, in radians.
//
// A full circle divides by Count, so 60 spokes land 6 degrees apart and the last
// one does not sit on the first. A partial sweep divides by Count-1, so the
// first and last copies land on the ends of the arc — which is what "five bolts
// across 90 degrees" means to the person asking for it.
func sweepAngle(r *Repeat) float64 {
	if r.Angle == 0 || math.Abs(r.Angle) >= 360 {
		return 2 * math.Pi / float64(r.Count)
	}
	return (r.Angle * math.Pi / 180) / float64(r.Count-1)
}

func axisRotation(about string, radians float64) [3]float64 {
	switch about {
	case "x":
		return [3]float64{radians, 0, 0}
	case "y":
		return [3]float64{0, radians, 0}
	default:
		return [3]float64{0, 0, radians}
	}
}

// retargetFeatures points a feature that named a patterned part at every copy.
func retargetFeatures(features []Feature, copies map[string][]string) []Feature {
	out := make([]Feature, 0, len(features))
	for _, f := range features {
		// "of" takes the FIRST copy: an operation applies to one solid, and
		// applying it to sixty separately would silently make sixty results.
		if made, ok := copies[f.Of]; ok && len(made) > 0 {
			f.Of = made[0]
		}
		var with []string
		for _, id := range f.With {
			if made, ok := copies[id]; ok {
				with = append(with, made...)
				continue
			}
			with = append(with, id)
		}
		f.With = with
		out = append(out, f)
	}
	return out
}

func cloneSize(in map[string]float64) map[string]float64 {
	if in == nil {
		return nil
	}
	out := make(map[string]float64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
