package geometry

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Binding: the parameters actually drive the shape.
//
// # What was missing after the parametric phase
//
// Wave 10 gave a document parameters and the expressions that must follow when
// they change, and then did nothing with them. A part's width was still the
// number the model typed. The document DESCRIBED a relationship and the geometry
// did not obey it, which is the worst of the three available positions: a reader
// sees named parameters beside a shape and reasonably concludes that changing
// one would move it.
//
// So a part dimension can now name an EXPRESSION instead of carrying only a
// number, and this file evaluates them into the numbers everything downstream
// already reads. mesh.go, compare.go, overlay.go and the exporter are untouched:
// by the time they see a Part, its Size and Position are plain floats, exactly
// as before.
//
// # Why the expression wins when the two disagree
//
// A part can carry both `size.width = 60` and `size_from.width = "plate_size"`.
// They can disagree, and one of them has to win.
//
// The expression wins, because it is the RELATIONSHIP and the number is only a
// snapshot of it. A model that writes plate_size = 80 and then types 60 into the
// width has made an arithmetic slip; honouring the 60 would freeze the slip into
// the geometry and lose the relationship that was correctly stated.
//
// But it is never silent. Agreement is the normal case and says nothing;
// DISAGREEMENT is reported, because "your own relationship and your own number
// do not match" is a real finding about the document and there is nowhere else
// it would surface.
//
// # Why rotation cannot be bound
//
// Deliberately absent. Every parametric part in the 2026-09-05 spike drives
// sizes and positions — ribs at ±plate_size/4, holes at ±pitch/2 — and not one
// drives an angle. A RotationFrom would be API surface added on a guess, and the
// repository's rule is that a binding nobody has needed is a binding that gets
// designed wrong. It can be added the day a document needs one.

// bindingEpsilon is how far a stated number may sit from its own expression
// before that counts as a disagreement.
//
// Relative, because the tolerance that is invisible on a 120 mm plate is the
// whole dimension on a 0.2 mm fillet. The value is a float-noise threshold, not
// an engineering tolerance: this asks "did the model mean the same number", not
// "is this within manufacturing limits", and nothing here has any business
// deciding the second question.
const bindingEpsilon = 1e-9

// Bind evaluates every part's bound dimensions and writes them into the numbers
// the renderer reads.
//
// Mutates the document. Resolve stays pure and side-effect free — typedClaims
// and the honesty machinery call it on documents they must not touch — so the
// mutation lives here, where a caller has asked for it by name.
//
// Returns problems rather than an error, for the same reason Resolve does: a
// document with one unreadable expression still has every other part in it, and
// the person is mid-design.
func (d *Document) Bind() []Problem { return d.bind(true) }

// bind does the work, and compareToAuthored says whether a stated number that
// disagrees with its own expression is worth reporting.
//
// It is true when a document arrives from the model: the number and the
// relationship are two things the model said, and them disagreeing is a real
// finding about the document.
//
// It is FALSE after WithParameters, and that is not a softening. Once somebody
// has changed plate_size from 60 to 90, every stated number that followed
// plate_size is stale BY CONSTRUCTION — that is what was asked for. Reporting it
// would put a caveat on every bound dimension of every re-specified variant,
// which is the failure this codebase keeps naming: a warning that fires on
// correct input is a warning people stop reading. Observed live 2026-09-05,
// where one respec produced eight of them and every one was noise.
func (d *Document) bind(compareToAuthored bool) []Problem {
	if d == nil {
		return nil
	}
	res := d.Resolve()
	problems := res.Problems

	lookup := func(n string) (float64, bool) {
		v, ok := res.Values[n]
		return v.Number, ok
	}

	// Profile coordinates written as expressions become NUMBERS here, exactly as
	// a bound size does, and for the same reason: nothing downstream evaluates
	// anything. The renderer, the tessellator and the measurement path all read
	// a stored document with no parameter context, and a coordinate they cannot
	// read would become a zero that silently folds the outline flat.
	//
	// The expressions stay in the document beside the numbers. The relationship
	// is the thing recorded; the coordinate is the snapshot of it.
	//
	// Only the COORDINATES are taken. The problems belong to ProfileProblems,
	// which has its own voice at the boundary: "a number is missing" and "a
	// whole part is not in the shape" are different things to be told, and one
	// wording for both makes the second read like the first.
	profiles, _ := d.resolvedProfiles()

	for i := range d.Parts {
		p := &d.Parts[i]
		label := p.Label()

		if pts, ok := profiles[p.ID]; ok && len(pts) == len(p.Profile) {
			for j := range p.Profile {
				p.Profile[j].X = pts[j][0]
				p.Profile[j].Y = pts[j][1]
			}
		}

		for _, key := range sortedKeys(p.SizeFrom) {
			expr := p.SizeFrom[key]
			value, prob := evalBinding(expr, label, key, lookup)
			if prob != nil {
				problems = append(problems, *prob)
				continue
			}
			if p.Size == nil {
				p.Size = map[string]float64{}
			}
			if was, had := p.Size[key]; compareToAuthored && had && !nearlyEqual(was, value) {
				problems = append(problems, Problem{
					Severity: Warning, Name: label,
					Detail: fmt.Sprintf("states %s = %g but its own expression %q works out to %g; "+
						"the expression was used, because it is the relationship and the number is "+
						"only a snapshot of it", key, was, expr, value),
				})
			}
			p.Size[key] = value
		}

		for _, axis := range sortedKeys(p.PositionFrom) {
			expr := p.PositionFrom[axis]
			index, ok := axisIndex(axis)
			if !ok {
				problems = append(problems, Problem{
					Severity: Error, Name: label,
					Detail: fmt.Sprintf("binds position %q, which is not an axis; use x, y or z", axis),
				})
				continue
			}
			value, prob := evalBinding(expr, label, "position "+axis, lookup)
			if prob != nil {
				problems = append(problems, *prob)
				continue
			}
			for len(p.Position) < 3 {
				p.Position = append(p.Position, 0)
			}
			if was := p.Position[index]; compareToAuthored && !nearlyEqual(was, value) && was != 0 {
				problems = append(problems, Problem{
					Severity: Warning, Name: label,
					Detail: fmt.Sprintf("states position %s = %g but its own expression %q works out "+
						"to %g; the expression was used", axis, was, expr, value),
				})
			}
			p.Position[index] = value
		}
	}

	sortProblems(problems)
	return problems
}

// evalBinding parses and evaluates one expression, naming the part and the
// dimension in anything that goes wrong.
//
// The part is named because a document has many, and "plate_size is not a
// parameter" sends a reader looking through all of them for the one that said
// it.
func evalBinding(expr, label, what string, lookup func(string) (float64, bool)) (float64, *Problem) {
	node, err := parseExpression(expr)
	if err != nil {
		return 0, &Problem{Severity: Error, Name: label,
			Detail: fmt.Sprintf("%s is bound to %q, which cannot be read: %v. "+
				"The number already on the part was kept", what, expr, err)}
	}
	value, err := node.Eval(lookup)
	if err != nil {
		return 0, &Problem{Severity: Error, Name: label,
			Detail: fmt.Sprintf("%s is bound to %q, which does not evaluate: %v. "+
				"The number already on the part was kept", what, expr, err)}
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, &Problem{Severity: Error, Name: label,
			Detail: fmt.Sprintf("%s is bound to %q, which does not produce a finite number. "+
				"The number already on the part was kept", what, expr)}
	}
	return value, nil
}

// WithParameters re-derives the whole document with some parameters changed.
//
// This is what "parametric" finally means here: hand it plate_size = 80 and
// every derived value and every bound dimension is recomputed from it, and the
// document that comes back is a different shape rather than the same shape with
// a different label. It is the operation the 2026-09-05 sweep performed by hand
// nine times, and the one that separated the model that survived a change from
// the model that did not.
//
// The receiver is NOT modified. A variant is a new document (PRD VIS-04): the
// stored one has to stay exactly what the model said, or a replay stops matching
// what the person saw.
//
// An override naming something that is not a parameter is refused rather than
// added. Silently accepting one would let a caller "set" a derived value, which
// reads as working and changes nothing, because the next Bind recomputes it from
// its expression.
func (d *Document) WithParameters(overrides map[string]float64) (*Document, []Problem) {
	if d == nil {
		return nil, nil
	}
	out := d.clone()
	var problems []Problem

	known := map[string]bool{}
	for _, p := range out.Parameters {
		known[strings.ToLower(strings.TrimSpace(p.Name))] = true
	}
	derived := map[string]bool{}
	for _, dv := range out.Derived {
		derived[strings.ToLower(strings.TrimSpace(dv.Name))] = true
	}

	for _, name := range sortedFloatKeys(overrides) {
		value := overrides[name]
		key := strings.ToLower(strings.TrimSpace(name))
		switch {
		case math.IsNaN(value) || math.IsInf(value, 0):
			problems = append(problems, Problem{Severity: Error, Name: key,
				Detail: "cannot be set to a value that is not a finite number"})
		case derived[key]:
			problems = append(problems, Problem{Severity: Error, Name: key,
				Detail: fmt.Sprintf("is derived, so it cannot be set directly. It follows %s; "+
					"change what it reads instead", expressionFor(out, key))})
		case !known[key]:
			problems = append(problems, Problem{Severity: Error, Name: key,
				Detail: "is not a parameter of this document, so setting it would change nothing"})
		default:
			for i := range out.Parameters {
				if strings.ToLower(strings.TrimSpace(out.Parameters[i].Name)) == key {
					out.Parameters[i].Value = value
					// The value is now the caller's, so the provenance is too.
					// Leaving how:"standard" on a number somebody typed over
					// would attribute their figure to a published standard —
					// the exact laundering standards.go exists to stop.
					out.Parameters[i].How = Chosen
					out.Parameters[i].Source = ""
				}
			}
		}
	}

	problems = append(problems, out.bind(false)...)
	sortProblems(problems)
	return out, problems
}

func expressionFor(d *Document, name string) string {
	for _, dv := range d.Derived {
		if strings.ToLower(strings.TrimSpace(dv.Name)) == name {
			return strconvQuote(dv.Expression)
		}
	}
	return "an expression"
}

func strconvQuote(s string) string { return fmt.Sprintf("%q", s) }

// clone deep-copies everything Bind and WithParameters can reach.
//
// Every slice and map below is written to by one of them. A shallow copy would
// share Size maps and Position slices with the stored document, so producing a
// variant would silently rewrite the original — the failure that makes a
// side-by-side comparison show two identical shapes.
func (d *Document) clone() *Document {
	out := *d
	out.Parameters = append([]Parameter(nil), d.Parameters...)
	out.Derived = append([]Derived(nil), d.Derived...)
	out.Assumptions = append([]string(nil), d.Assumptions...)
	out.NotVerified = append([]string(nil), d.NotVerified...)
	out.Overlays = append([]Overlay(nil), d.Overlays...)
	out.States = append([]AssemblyState(nil), d.States...)

	out.Parts = make([]Part, len(d.Parts))
	for i, p := range d.Parts {
		q := p
		q.Size = copyFloatMap(p.Size)
		q.SizeFrom = copyStringMap(p.SizeFrom)
		q.PositionFrom = copyStringMap(p.PositionFrom)
		q.Position = append([]float64(nil), p.Position...)
		q.Rotation = append([]float64(nil), p.Rotation...)
		if p.Material != nil {
			m := *p.Material
			q.Material = &m
		}
		out.Parts[i] = q
	}
	return &out
}

func copyFloatMap(in map[string]float64) map[string]float64 {
	if in == nil {
		return nil
	}
	out := make(map[string]float64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func copyStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// axisIndex maps an axis name to its slot in Position. Y is up, as the model
// contract states.
func axisIndex(axis string) (int, bool) {
	switch strings.ToLower(strings.TrimSpace(axis)) {
	case "x":
		return 0, true
	case "y":
		return 1, true
	case "z":
		return 2, true
	}
	return 0, false
}

// nearlyEqual compares against float noise, not against an engineering
// tolerance. See bindingEpsilon.
func nearlyEqual(a, b float64) bool {
	if a == b {
		return true
	}
	scale := math.Max(math.Abs(a), math.Abs(b))
	return math.Abs(a-b) <= bindingEpsilon*math.Max(scale, 1)
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedFloatKeys(m map[string]float64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Span is the distance between parts placed by expressions resting on the same
// parameters.
//
// # Why this exists, and what it is NOT
//
// The 2026-09-05 spike's third conclusion was that expressions are a new place
// for errors to hide: a wrong NUMBER can be checked against a published figure,
// and a wrong RELATIONSHIP produces plausible numbers from correct inputs. Wave
// 11's live run produced the textbook case:
//
//	nema17_face_size = 42.3 mm          how=standard   ← the figure is CORRECT
//	motor_mount_x    = nema17_face_size / 2
//	four holes at (±motor_mount_x, ±motor_mount_x)
//
// Every figure there checks out. The holes land on a 42.3 mm square, and NEMA
// 17's bolt pattern is 31 mm square, so the part cannot be bolted to the motor.
// Checking the inputs will never find this; only the RESULT will.
//
// The grouping is read from the BINDINGS and never from the geometry. Parts
// whose position on one axis is computed from the same parameters are related
// because the document says so — not because something here decided that four
// cylinders near each other must be a bolt pattern. That distinction is the
// whole reason this is safe to act on: guessing what a group of parts IS would
// be the fabricated-finding failure standards.go exists to avoid, and this does
// not guess.
//
// It reports a measurement. Whether the measurement is WRONG is not decided
// here: that needs to know which published dimension it should be compared
// against, which is the honesty machinery's job and not the domain's.
type Span struct {
	// Axis is "x", "y" or "z".
	Axis string
	// Parts are the ids taking part, sorted.
	Parts []string
	// Extent is the distance between the outermost two, in Unit.
	Extent float64
	Unit   string
	// Depends is every parameter the placement rests on, sorted. Provenance
	// travels along these, exactly as it does for a derived value.
	Depends []string
}

// Spans returns every pattern the document's position bindings describe.
//
// A group needs at least two parts at DIFFERENT positions: one part is not a
// pattern, and several parts at the same coordinate span nothing.
func (d *Document) Spans() []Span {
	if d == nil || len(d.Parts) == 0 {
		return nil
	}
	res := d.Resolve()
	lookup := func(n string) (float64, bool) {
		v, ok := res.Values[n]
		return v.Number, ok
	}

	type placed struct {
		part    string
		value   float64
		depends []string
	}
	// Keyed by axis and by the parameters the placement rests on, so parts
	// positioned from unrelated parameters are never compared.
	groups := map[string][]placed{}
	order := []string{}

	for _, axis := range []string{"x", "y", "z"} {
		for _, p := range d.Parts {
			expr, bound := p.PositionFrom[axis]
			if !bound {
				continue
			}
			node, err := parseExpression(expr)
			if err != nil {
				continue
			}
			value, err := node.Eval(lookup)
			if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
				continue
			}
			depends := dependenciesOf(node.References(), res.Values)
			if len(depends) == 0 {
				continue
			}
			key := axis + "|" + strings.Join(depends, ",")
			if _, seen := groups[key]; !seen {
				order = append(order, key)
			}
			groups[key] = append(groups[key], placed{part: p.ID, value: value, depends: depends})
		}
	}

	var out []Span
	for _, key := range order {
		members := groups[key]
		if len(members) < 2 {
			continue
		}
		lo, hi := members[0].value, members[0].value
		ids := make([]string, 0, len(members))
		distinct := map[float64]bool{}
		for _, m := range members {
			lo = math.Min(lo, m.value)
			hi = math.Max(hi, m.value)
			distinct[m.value] = true
			ids = append(ids, m.part)
		}
		// EXACTLY two distinct positions, and no more.
		//
		// With two, the extent between them is unambiguously the spacing — the
		// four holes of a bolt pattern sit at ±x, which is two positions on each
		// axis. With three or more in a row the extent is some multiple of the
		// pitch and nothing here knows which, so calling it a spacing would be
		// arithmetic invented to fill a gap. Those are left unreported: a missed
		// finding is recoverable and an invented one is acted on.
		if len(distinct) != 2 || hi-lo <= 0 {
			continue
		}
		sort.Strings(ids)
		unit, _ := inheritedUnit(members[0].depends, res.Values)
		out = append(out, Span{
			Axis: strings.SplitN(key, "|", 2)[0], Parts: ids,
			Extent: hi - lo, Unit: unit, Depends: members[0].depends,
		})
	}
	return out
}
