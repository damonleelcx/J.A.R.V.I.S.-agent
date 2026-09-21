package agent

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// Recalled figures, part two: the typed fields.
//
// # Why standards.go was not enough
//
// standards.go catches a recalled standard in PROSE, because in 2026-09-02 that
// is the only place a figure could appear: the reply said "centered NEMA 17 bolt
// pattern (holes at ±20.5 mm)" and the detector read the sentence.
//
// The 2026-09-05 parametric phase moved the figures somewhere else. A document
// now carries parameters, and a parameter says where its number came from in a
// FIELD rather than in a sentence. Three live runs against qwen-plus produced,
// every single time, this:
//
//	{"name": "motor_mount_hole_spacing", "value": 42.3, "unit": "mm",
//	 "how": "standard", "source": "NEMA 17 standard mounting pattern"}
//
// 42.3 mm is the NEMA 17 FRAME size. The bolt pattern is 31 mm square. The
// figure is wrong in all three runs — and it is wearing a typed provenance field
// and a citation, which is the 2026-09-02 defect with better clothes on.
//
//	Structure makes a wrong figure MORE convincing, not less.
//
// A detector that reads only prose would have gone quiet at exactly the moment
// the claims became easier to believe. So this file reads the typed fields, and
// it reads them the same way: server-side, from the document itself, never asked
// of the model.
//
// # The dependency edge, and why it is the point
//
// Every one of those runs then wrote:
//
//	mount_hole_x_offset = (motor_mount_hole_spacing / sqrt(2)) / 2
//
// A derived figure is exactly as checkable as the parameters underneath it, and
// nothing about 14.955 mm reveals that it descends from a NEMA 17 claim. So
// Resolve computes what each value rests on (geometry.Value.Depends) and a
// recalled claim travels along those edges. That is how a figure the model never
// stated becomes attributable to the standard it came from.
//
// # Why the claim text is deliberately BARE
//
// The eval scorer identifies which dimension a figure refers to by looking for a
// naming phrase within about a clause of it. That works on prose written by a
// person. It is a trap for text synthesised here: putting the expression next to
// the figure would place "motor_mount_hole_spacing" — and therefore the phrase
// "hole spacing" — 28 characters from 14.955 mm, and the scorer would report a
// hole offset as a wrong bolt pitch. A fabricated finding.
//
// The codebase has already decided which way that trade goes, in this file's own
// predecessor: an evaluation that invents findings is worse than one that misses
// some, because the findings are what people act on. So Text carries the name
// and the figure and NOTHING else that could be mistaken for a dimension, and
// everything a reader needs about where it came from goes in Via.

// noSourceNamed is what the banner calls a figure that names nothing at all.
//
// A phrase and not an empty string: the banner groups claims by this field and
// renders it as the heading, so an empty one would produce a bullet with no
// subject — and the reader would be told a figure was quoted from memory without
// being told the one thing that matters about it, which is that there is nothing
// behind it.
const noSourceNamed = "a figure with no source"

// typedClaims reads a document's parameters and resolved derived values.
//
// Returns nil for a document with no parameters — every stored variant predates
// these fields, and a non-parametric document has nothing here to find.
func typedClaims(doc *geometry.Document) []StandardsClaim {
	if doc == nil || len(doc.Parameters) == 0 {
		return nil
	}

	// byName carries the authored provenance; Resolve deliberately does not,
	// because a resolved value is a number and provenance is a claim about it.
	byName := map[string]geometry.Parameter{}
	for _, p := range doc.Parameters {
		byName[strings.ToLower(strings.TrimSpace(p.Name))] = p
	}

	res := doc.Resolve()
	var out []StandardsClaim

	for _, p := range doc.Parameters {
		name := strings.ToLower(strings.TrimSpace(p.Name))
		if p.How != geometry.FromStandard {
			continue
		}
		v, ok := res.Values[name]
		if !ok {
			// It did not resolve, so there is no figure to check. Resolve has
			// already reported why; saying it twice in a different voice would
			// make the panel argue with itself.
			continue
		}
		refs := standardRefsFor(p)
		if len(refs) == 0 {
			// Marked as quoted from a standard, naming nothing at all. Resolve
			// raises that as a warning on the parameter; there is no reference
			// here to report, and inventing one would be the failure this file
			// is about.
			continue
		}
		figure := figureText(v)
		out = append(out, StandardsClaim{
			Standards: refs,
			Figures:   []string{figure},
			Where:     "parameter",
			Text:      fmt.Sprintf("%s = %s", name, figure),
			Via:       viaSource(p.Source),
		})
	}

	// Patterns: what the placement actually MEASURES (wave 13).
	//
	// A figure can be right and the relationship built on it wrong, and only the
	// result shows it. The live run of 2026-09-05 recalled 42.3 mm correctly as
	// the NEMA 17 frame and then placed four mounting holes on a 42.3 mm square,
	// where the bolt pattern is 31 mm. Every input checked out; the part cannot
	// be bolted to the motor.
	//
	// The parts are grouped by their BINDINGS — Spans reads which placements rest
	// on the same parameters — so nothing here decides that a group of parts is a
	// bolt pattern. What names it is the parts' own shared id, and if that id
	// says nothing a dimension table recognises, the figure goes unscored rather
	// than guessed at. Under-reporting is the safe direction, and it is the same
	// direction the rest of this file takes.
	for _, span := range doc.Spans() {
		var refs []string
		for _, dep := range span.Depends {
			p, ok := byName[dep]
			if !ok || p.How != geometry.FromStandard {
				continue
			}
			refs = append(refs, standardRefsFor(p)...)
		}
		if len(refs) == 0 {
			continue
		}
		label, named := span.Name()
		if !named {
			// Nothing shared to call it. A span nobody can name cannot be
			// attributed to a dimension, and the parameters underneath it are
			// already reported above — so this would be a second copy of a
			// figure with no new information in it.
			continue
		}
		figure := figureText(geometry.Value{Number: span.Extent, Unit: span.Unit})
		out = append(out, StandardsClaim{
			Standards: dedupeFold(refs),
			Figures:   []string{figure},
			Where:     "placement",
			// "spacing" rather than "span" because Spans reports only groups
			// at exactly two distinct positions, where the extent between them
			// IS the spacing — and because the dimension table already knows
			// that word. Adding a new phrase for this instead ("mount hole")
			// was tried and reverted: it matched mount_hole_x_OFFSET too, which
			// is half a pitch, and scoring that against the published 31 mm is
			// the fabricated finding this file's own fences exist to catch.
			Text: fmt.Sprintf("%s spacing = %s", label, figure),
			Via: fmt.Sprintf("measured across %d parts placed on the %s axis from %s",
				len(span.Parts), span.Axis, strings.Join(span.Depends, ", ")),
		})
	}

	// A recalled figure written as a BARE CONSTANT in `derived` (issue 8).
	//
	// Everything else in this file travels along a dependency edge: a derived
	// figure is attributable to whatever the parameters underneath it claimed.
	// A bare constant has no edges — it depends on nothing — so nothing
	// propagates to it and the check said nothing at all about it. In the live
	// run the issue records that was 2 of 3 derived figures, and the reader took
	// a clean banner to mean they had been traced.
	//
	// A bare constant answers "where did this come from" with silence, and
	// silence is indistinguishable from a number the model invented. So it is
	// reported here with the one thing that IS known about it: it has no source.
	// Not as a refusal — Resolve already tells the model where the number
	// belongs, in its own voice — but as a line in the banner whose whole job is
	// to say which figures were not checked.
	//
	// noSourceNamed stands where a standard's name stands, because the banner
	// groups by that field and this is exactly the same kind of statement: here
	// is a figure, and here is what is known about where it came from.
	for _, v := range doc.ConstantDerived() {
		figure := figureText(v)
		out = append(out, StandardsClaim{
			Standards: []string{noSourceNamed},
			Figures:   []string{figure},
			Where:     "derived value",
			Text:      fmt.Sprintf("%s = %s", v.Name, figure),
			Via: fmt.Sprintf("written in \"derived\" as %s, which names no parameter, so this "+
				"figure rests on nothing and there is no source to check it against",
				strconv.Quote(v.Expression)),
		})
	}

	// Derived values, in the order they resolve, so the chain reads forwards.
	for _, name := range res.Order {
		v, ok := res.Values[name]
		if !ok {
			continue
		}
		var refs, restingOn []string
		for _, dep := range v.Depends {
			p, ok := byName[dep]
			if !ok || p.How != geometry.FromStandard {
				continue
			}
			if r := standardRefsFor(p); len(r) > 0 {
				refs = append(refs, r...)
				restingOn = append(restingOn, dep)
			}
		}
		if len(refs) == 0 {
			continue
		}
		figure := figureText(v)
		out = append(out, StandardsClaim{
			Standards: dedupeFold(refs),
			Figures:   []string{figure},
			Where:     "derived value",
			Text:      fmt.Sprintf("%s = %s", name, figure),
			Via: fmt.Sprintf("derived as %s, resting on %s",
				v.Expression, strings.Join(dedupeFold(restingOn), ", ")),
		})
	}
	return out
}

// standardRefsFor names what a parameter says it was quoted from.
//
// Recognised families first, because "NEMA 17" is a reference a reader can go
// and check. When the source names nothing this build has a pattern for, the
// source itself is reported verbatim: the model asserted a provenance, and
// reporting "quoted from something we do not recognise" is still the true and
// useful statement. Falling silent there would mean the guard is weakest exactly
// where the citation is vaguest.
func standardRefsFor(p geometry.Parameter) []string {
	source := strings.TrimSpace(p.Source)
	if source == "" {
		return nil
	}
	// Underscores read as spaces so a snake_case name is searched as words.
	haystack := source + " " + strings.ReplaceAll(p.Name, "_", " ")
	if names := dedupeFold(normaliseAll(standardsRE.FindAllString(haystack, -1))); len(names) > 0 {
		sort.Strings(names)
		return names
	}
	return []string{trimTo(source, 60)}
}

func viaSource(source string) string {
	source = strings.TrimSpace(source)
	if source == "" {
		return ""
	}
	return fmt.Sprintf("quoted from %q", source)
}

// figureText renders a resolved value the way the rest of the system prints a
// dimension: through Quantity when it really is a length, so precision travels
// with the number instead of being invented here.
func figureText(v geometry.Value) string {
	if q, ok := v.Quantity(); ok {
		return q.String()
	}
	n := strconv.FormatFloat(v.Number, 'f', -1, 64)
	if v.Unit == "" {
		return n
	}
	return n + " " + v.Unit
}

func trimTo(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n]) + "…"
}

// parameterNote renders one resolution problem as a sentence for the reader.
//
// Two voices, because the two severities mean different things to somebody
// looking at a render: an Error means a number this design refers to is not
// there at all, and a Warning means every number is present and something about
// how one of them was arrived at will not hold up. Printing both the same way
// would make the first look survivable.
func parameterNote(p geometry.Problem) string {
	lead := "FORGE could not resolve part of this design's parametric model"
	if p.Severity == geometry.Warning {
		lead = "This design's parametric model resolves, with a caveat"
	}
	if p.Name != "" {
		return fmt.Sprintf("%s — %s %s.", lead, p.Name, p.Detail)
	}
	return fmt.Sprintf("%s — %s.", lead, p.Detail)
}

// relationshipNote renders one thing relationship checking found, for the reader.
//
// Its own lead, and not parameterNote's. "This design's parametric model
// resolves, with a caveat" is about ARITHMETIC — a value that would not
// evaluate, a number disagreeing with its own expression. These are about
// whether the document's relationships mean anything: a parameter nothing reads
// and four holes at typed coordinates both resolve perfectly. Telling a reader
// the model "resolves with a caveat" about them would point them at the one
// place the problem is not.
func relationshipNote(p geometry.Problem) string {
	if p.Name != "" {
		return fmt.Sprintf("FORGE checked this design's relationships — %s %s.", p.Name, p.Detail)
	}
	return fmt.Sprintf("FORGE checked this design's relationships — %s.", p.Detail)
}

// The name a group of parts shares now lives on Span itself (geometry's
// relationships.go, issue 9), because two readers need it and they must not
// disagree: this file, which scores a NAMED span against the dimension table,
// and the relationship report, which has to say something honest about an
// UNNAMED one instead of dropping it.
//
// The rule is unchanged. It is taken from the parts' own ids rather than
// composed: "motor-mount-hole-bl" and "motor-mount-hole-tr" are called
// "motor-mount-hole" because that is what their author called them, and
// inventing a name for a group would be deciding what the group IS, which is the
// one thing this file must not do. Span.Name's second return value is what keeps
// that true here: a name FORGE assigned is never scored.

// featureNote renders one rejected feature for the reader.
//
// A separate voice from parameterNote because it says something different: not
// "a number is missing" but "an operation you asked for is NOT in this
// assembly". The render looks the same either way, which is exactly why it has
// to be said.
func featureNote(p geometry.Problem) string {
	return fmt.Sprintf("FORGE could not apply one of this design's features, so it is not in "+
		"the shape — %s %s.", p.Name, p.Detail)
}

// templateNote renders what the car template said about a car (geometry/car.go):
// an Error is a car that is NOT in the shape, a Warning a car built as asked with
// something worth a look — usually a proportion outside its class's published cars.
func templateNote(p geometry.Problem) string {
	if p.Severity == geometry.Error {
		return fmt.Sprintf("FORGE could not build the car from its numbers, so it is not in the "+
			"shape — %s %s.", p.Name, p.Detail)
	}
	return fmt.Sprintf("The car was built as asked, with a caveat — %s %s.", p.Name, p.Detail)
}

// profileNote renders one unreadable outline for the reader.
//
// Its own voice again: a parameter problem means a number is missing, a feature
// problem means an operation was not applied, and this means an entire PART is
// absent from the shape. The render shows a design with a piece missing, which
// looks like a design decision rather than like a failure.
func profileNote(p geometry.Problem) string {
	return fmt.Sprintf("FORGE could not read one of this design's outlines, so that part is "+
		"not in the shape at all — %s %s.", p.Name, p.Detail)
}
