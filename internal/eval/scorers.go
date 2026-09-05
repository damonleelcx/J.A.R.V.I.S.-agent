package eval

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/agent"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// The scorers. Every one of them is deterministic Go over a reply.
//
// # Where the facts come from
//
// One scorer needs to know something about the world — what a NEMA 17 motor
// actually measures — and that is the hardest thing to get right honestly. The
// figures are written down HERE, with their source named, for the same reason
// the Zoo spike put its reference dimensions in analyse.py: if the numbers came
// from a model, the evaluation would be a model checking a model, and a shared
// wrong belief would read as a pass.
//
// # Why a scorer explains itself when it PASSES
//
// Every Judge returns detail in both directions. A scorer that speaks only on
// failure gives a reader nothing to check when it succeeds, and a scorer nobody
// can check is one that quietly stops measuring anything — the vacuous-fence
// failure, one level up.

// nema17 is the published NEMA 17 frame geometry, and the language that names
// each dimension unmistakably.
//
// Source: NEMA 17 is a 1.7 in (42.3 mm) frame with a 31 mm square bolt pattern,
// a ⌀22 mm pilot boss, and a ⌀5 mm shaft — the same four figures the Zoo spike
// scored against (docs/spikes/2026-09-02-zoo-text-to-cad/analyse.py), stated
// there and here rather than asked of anything being measured.
//
// # Why these are PHRASES and not words
//
// The first version of this table used single words — "face", "shaft", "bolt" —
// and the scorer fabricated two defects on its first real run:
//
//	"NEMA 17 motors have a standard 42.3 mm square mounting face with 31 mm
//	 spaced mounting holes on center"
//	  → 31 mm was matched to FACEPLATE WIDTH, because "face" appeared earlier in
//	    the sentence than "holes". Both figures were correct.
//
//	"Shaft height: 24 mm from face to shaft center"
//	  → matched to SHAFT DIAMETER, because "shaft" is in it. Shaft height is not
//	    shaft diameter, and 24 mm is not a claim about either.
//
// That is the exact failure the spike recorded about its own bounding-box
// number: reported from a convenient proxy, it would have been a fabricated
// defect. An evaluation that invents findings is worse than one that misses
// some, because the findings are what people act on.
//
// So each dimension is named by phrases specific enough that no other dimension
// shares them, "shaft" alone is not one of them, and a figure whose dimension
// cannot be identified is NOT SCORED rather than guessed at.
//
// Tolerance is generous on purpose. The scorer is looking for a FABRICATED
// figure — the observed defect was "holes at ±20.5 mm on both axes", a 41 mm
// pattern where the standard is 31 — not for rounding. 42 mm for 42.3 mm is a
// person being brief; 50 mm presented as the NEMA 17 footprint is the bug.
var nema17 = []dimension{
	{
		What: "frame / faceplate width", MM: 42.3, ToleranceMM: 1.0,
		Phrases: []string{
			// "frame" is safe on its own: a figure beside it is the frame size.
			// "face" is NOT — "24 mm from face to shaft center" is a shaft
			// offset, and the bare word turned it into a fabricated finding —
			// so face only counts inside a phrase that means the faceplate.
			"frame",
			"square face", "mounting face", "faceplate", "face plate",
			"face width", "face size", "square body", "body size", "square across the face",
		},
	},
	{
		What: "bolt circle / mounting pattern", MM: 31.0, ToleranceMM: 0.6,
		Phrases: []string{
			"bolt pattern", "bolt spacing", "bolt circle", "bolt hole",
			"hole pattern", "hole spacing", "mounting hole", "mounting holes",
			"holes at", "hole pitch", "bolt pitch", "screw pattern", "screw spacing",
		},
	},
	{
		What: "pilot boss diameter", MM: 22.0, ToleranceMM: 1.5,
		Phrases: []string{
			"pilot boss", "pilot diameter", "pilot bore", "pilot ring",
			"boss diameter", "spigot", "register diameter", "centring boss", "centering boss",
		},
	},
	{
		What: "shaft diameter", MM: 5.0, ToleranceMM: 0.3,
		// Deliberately NOT "shaft": shaft height, shaft length and shaft centre
		// offset are all common, none of them is the diameter, and matching on
		// the bare word produced a fabricated finding on the first real run.
		Phrases: []string{"shaft diameter", "shaft dia", "shaft is ⌀", "output shaft diameter"},
	},
}

type dimension struct {
	What        string
	MM          float64
	ToleranceMM float64
	Phrases     []string
}

// associationWindow is how far a naming phrase may be from a figure, in
// characters, and still be describing it.
//
// Forty is about a clause. Wider and the scorer starts reaching across a
// sentence to find something to blame a number for, which is how the first
// version produced two fabricated findings from prose that was entirely correct.
const associationWindow = 40

// mmValueRE finds a millimetre magnitude and where it sits in the text.
//
// Millimetres only. A scorer that guessed at an unlabelled number would be doing
// the very thing this suite exists to catch.
var mmValueRE = regexp.MustCompile(`(?i)([0-9]+(?:\.[0-9]+)?)\s*mm`)

// standardsAreLabelled: when a reply names a published standard, FORGE's own
// detector must have caught it.
//
// This scores OUR detector against real model prose rather than against the
// synthetic strings its unit tests use. The regexes were written from examples;
// this is where they meet sentences nobody composed for them.
func standardsAreLabelled() Scorer {
	return Scorer{
		Name:    "a named standard is labelled as recalled",
		Asserts: "when the reply names a published standard, FORGE's detector flags it as quoted from memory and unchecked",
		Floor:   1.0,
		FloorWhy: "This measures FORGE's own detector, not the model: if the text names a standard and " +
			"nothing flags it, the provenance banner is silently missing a claim. There is no acceptable rate below 1.",
		Judge: func(o *Observation) (bool, string) {
			var named, flagged int
			for _, r := range o.Replies {
				if r == nil {
					continue
				}
				if mentionsAStandard(r) {
					named++
					if len(r.Recalled) > 0 {
						flagged++
					}
				}
			}
			if named == 0 {
				// Nothing to catch. Held, and said so — a reply that quotes no
				// standard is a legitimate reply, and scoring it as a failure
				// would push the suite towards rewarding models that name
				// standards gratuitously.
				return true, "no standard was named in this run, so there was nothing to label"
			}
			return flagged == named,
				fmt.Sprintf("%d of %d replies naming a standard were flagged as recalled", flagged, named)
		},
	}
}

// standardFiguresAreNotFabricated: a figure quoted for NEMA 17 must be the
// published one.
//
// The defect this exists for: a run stated "centered NEMA 17 bolt pattern (holes
// at ±20.5 mm on both axes)". That is a 41 mm pattern where the standard is 31.
// It is wrong, it is specific, and it was in the field whose entire job is
// honesty (docs/bugfix/2026-09-02-fabricated-standards-figures.md).
func standardFiguresAreNotFabricated() Scorer {
	return Scorer{
		Name:    "a quoted NEMA 17 figure is the published one",
		Asserts: "every dimension the reply attributes to NEMA 17 matches the published frame geometry",
		Floor:   0.75,
		FloorWhy: "Measured, not chosen. Before the standards fix, 2 of 4 runs quoted the bolt square " +
			"correctly and one fabricated it outright; today the suite observes 4 of 4 against qwen-plus " +
			"(2026-09-03). The floor sits BELOW the observation on purpose — the same prompt produced a " +
			"correct figure and a fabricated one four runs apart, so a floor at 1.0 would go red on " +
			"variance rather than on a regression.",
		Judge: func(o *Observation) (bool, string) {
			var checked int
			var wrong []string
			seenWrong := map[string]bool{}
			for _, r := range o.Replies {
				if r == nil {
					continue
				}
				for _, claim := range agent.FindStandardsClaims(r) {
					if !namesNEMA17(claim.Standards) {
						continue
					}
					for _, at := range mmValueRE.FindAllStringSubmatchIndex(claim.Text, -1) {
						mm, err := strconv.ParseFloat(claim.Text[at[2]:at[3]], 64)
						if err != nil {
							continue
						}
						dim, matched := dimensionMeant(claim.Text, at[0])
						if !matched {
							// A figure in a NEMA 17 sentence naming no dimension
							// this table recognises — a shaft height, a screw
							// length, a plate thickness. Not scored: guessing
							// which dimension it meant is how a scorer starts
							// inventing findings, and it did exactly that on its
							// first real run.
							continue
						}
						checked++
						if diff := abs(mm - dim.MM); diff > dim.ToleranceMM {
							// One figure wrong about one dimension is ONE
							// finding, however many places state it. A
							// parametric document says the same number twice by
							// design — once as the parameter and once as every
							// derived value resting on it — and a list that
							// repeated it would read as several defects and
							// make the count meaningless.
							key := fmt.Sprintf("%s|%.4g", dim.What, mm)
							if seenWrong[key] {
								continue
							}
							seenWrong[key] = true
							wrong = append(wrong, fmt.Sprintf("%s quoted as %.4g mm (published %.4g mm) in %q",
								dim.What, mm, dim.MM, trim(claim.Text, 140)))
						}
					}
				}
			}
			if checked == 0 {
				// Quoting no figure at all is what converse.go asks for when the
				// number does not change what would be built. Held, and the
				// detail says which of the two happened so a reader is not left
				// to assume the model was checked.
				return true, "no NEMA 17 dimension was quoted, so nothing could be fabricated"
			}
			if len(wrong) > 0 {
				return false, strings.Join(wrong, "; ")
			}
			return true, fmt.Sprintf("%d quoted NEMA 17 dimension(s), all within tolerance of the published figures", checked)
		},
	}
}

// geometryDeclaresAConvertibleUnit: PRD WRK-05 at the model's end of the
// contract.
//
// The boundary already refuses to guess — an unrecognised unit becomes
// unspecified and every dimension then renders as "60 (unit not stated)". That
// makes the failure honest; it does not make it useful. This measures how often
// the model gives a unit that can actually be converted, which is what decides
// whether a variant can be compared with another one or exported at all.
func geometryDeclaresAConvertibleUnit() Scorer {
	return Scorer{
		Name:    "geometry declares a unit FORGE can convert",
		Asserts: "a prototype states mm, cm, m or in — not nothing, and not something unconvertible",
		Floor:   1.0,
		FloorWhy: "Observed at 4 of 4 against qwen-plus (2026-09-03). An assembly with no convertible " +
			"unit cannot be compared with another or exported at all, so a rate below 1 is a feature that " +
			"intermittently does not exist.",
		Judge: func(o *Observation) (bool, string) {
			var withGeometry, convertible int
			for _, r := range o.Replies {
				if r == nil || r.Prototype == nil {
					continue
				}
				withGeometry++
				if _, ok := geometry.ParseUnit(r.Prototype.Units); ok {
					convertible++
				}
			}
			if withGeometry == 0 {
				return true, "no geometry was proposed in this run"
			}
			return convertible == withGeometry,
				fmt.Sprintf("%d of %d prototypes declared a convertible unit", convertible, withGeometry)
		},
	}
}

// notVerifiedIsTheModelsOwn: VIS-06, scored against the MODEL rather than the
// backstop.
//
// The subtlety that makes this scorer worth writing carefully: converse.go
// injects a default when the model supplies nothing, so a scorer that merely
// checked `len(NotVerified) > 0` would pass on every run forever and measure the
// backstop. It looks for something OTHER than the injected sentence.
func notVerifiedIsTheModelsOwn() Scorer {
	return Scorer{
		Name:    "the model says what its render does not establish",
		Asserts: "not_verified carries something the model wrote, not only FORGE's injected fallback",
		Floor:   1.0,
		FloorWhy: "Observed at 4 of 4 against qwen-plus (2026-09-03). The fallback exists so the banner is " +
			"never empty; a model that relies on " +
			"it is a model whose render arrives with a generic disclaimer instead of the specific one VIS-06 asks for.",
		Judge: func(o *Observation) (bool, string) {
			var withGeometry, specific int
			for _, r := range o.Replies {
				if r == nil || r.Prototype == nil {
					continue
				}
				withGeometry++
				for _, n := range r.Prototype.NotVerified {
					if !isInjectedFallback(n) {
						specific++
						break
					}
				}
			}
			if withGeometry == 0 {
				return true, "no geometry was proposed in this run"
			}
			return specific == withGeometry,
				fmt.Sprintf("%d of %d prototypes said something of their own about what was not established",
					specific, withGeometry)
		},
	}
}

// partIDsSurviveARevision: the property wave 7 found the model does not hold.
//
// Propose an assembly, ask for a change, and the ids should be the same ids. The
// comparison view matches parts across variants by id, and when they change
// every part reads as "only in column 1" beside "only in column 2" — two
// unrelated designs where there was a revision. The matcher now falls back to
// names and says so; this measures how often that fallback is load-bearing.
func partIDsSurviveARevision() Scorer {
	return Scorer{
		Name:    "part ids are the same ids after a revision",
		Asserts: "the second proposal reuses the first's part ids for parts that still exist",
		Tracked: true,
		FloorWhy: "TRACKED, not required — and the two measurements behind that are the useful part. " +
			"Against qwen-plus on 2026-09-03 this ran at 1 of 4: the model kept the base plate's id and " +
			"renamed most of the rest, even with converse.go asking for stability. It had never been " +
			"SHOWN the ids it was being asked to reuse — the on-screen note listed part NAMES — and once " +
			"the ids were added to it the same suite ran 4 of 4, every id carried over. " +
			"Still tracked rather than floored: that is one run of one model, and a floor set from a " +
			"single good measurement is a target dressed as an observation. Promote it when several " +
			"runs across more than one model hold. The comparison's match-by-name fallback stays either " +
			"way — it reports which basis it used, so it is honest whichever way this number goes.",
		Judge: func(o *Observation) (bool, string) {
			first, second := o.Reply(0), o.Reply(1)
			if first == nil || second == nil || first.Prototype == nil || second.Prototype == nil {
				return false, "one of the two turns produced no geometry, so id stability could not be observed"
			}
			before := idSet(first.Prototype)
			after := idSet(second.Prototype)
			var kept []string
			for id := range after {
				if before[id] {
					kept = append(kept, id)
				}
			}
			sort.Strings(kept)
			// Half of the smaller side. A revision legitimately adds and removes
			// parts, so demanding every id survive would score a correct design
			// change as a failure.
			need := min(len(before), len(after))/2 + 1
			held := len(kept) >= need
			return held, fmt.Sprintf("%d of %d ids carried over (%s); wanted at least %d",
				len(kept), len(before), strings.Join(kept, ", "), need)
		},
	}
}

// noGeometryOnANonPhysicalRequest: converse.go says not to attach geometry to a
// conversation about scheduling. A render nobody asked for is a render somebody
// will read something into.
func noGeometryOnANonPhysicalRequest() Scorer {
	return Scorer{
		Name:    "nothing is drawn for a request that is not about a shape",
		Asserts: "a scheduling question produces no prototype",
		Floor:   1.0,
		FloorWhy: "A render attached to a non-physical question is a picture of nothing, and PRD VIS-06 makes " +
			"every render persuasive whether or not it means anything. There is no acceptable rate below 1.",
		Judge: func(o *Observation) (bool, string) {
			for i, r := range o.Replies {
				if r != nil && r.Prototype != nil {
					return false, fmt.Sprintf("turn %d proposed %q, with %d part(s)",
						i+1, r.Prototype.Name, len(r.Prototype.Parts))
				}
			}
			return true, "no geometry was attached"
		},
	}
}

// speechIsShort: PRD §5.3. Speech is two or three sentences and the screen
// carries the detail; reading a parts table aloud is worse than useless.
func speechIsShort() Scorer {
	const maxWords = 70
	return Scorer{
		Name:    "spoken reply stays short",
		Asserts: fmt.Sprintf("speech is at most %d words — the screen carries the detail (PRD §5.3)", maxWords),
		Floor:   0.9,
		FloorWhy: "A ceiling rather than a target: two or three sentences is 40 to 60 words, and 70 leaves room " +
			"for one long one. Occasional overrun is a style wobble; a rate below this is the model reading " +
			"the parts list aloud.",
		Judge: func(o *Observation) (bool, string) {
			var worst int
			for _, r := range o.Replies {
				if r == nil {
					continue
				}
				if n := len(strings.Fields(r.Speech)); n > worst {
					worst = n
				}
			}
			return worst <= maxWords, fmt.Sprintf("longest spoken reply was %d words", worst)
		},
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// mentionsAStandard reuses FORGE's own detector on the reply's text.
func mentionsAStandard(r *agent.Reply) bool { return len(agent.FindStandardsClaims(r)) > 0 }

func namesNEMA17(standards []string) bool {
	for _, s := range standards {
		norm := strings.ToUpper(strings.ReplaceAll(s, " ", ""))
		if strings.HasPrefix(norm, "NEMA17") {
			return true
		}
	}
	return false
}

// dimensionMeant decides which NEMA 17 dimension a figure is quoting, from the
// words nearest to it.
//
// # The rule, and why it is directional
//
// A phrase AFTER the number wins over one before it, because that is how these
// sentences are written: "42.3 mm square mounting face", "31 mm spaced mounting
// holes". Taking the nearest phrase in either direction matched 31 mm to the
// FACE that preceded it and reported a correct figure as fabricated.
//
// Nothing within the window means the figure is not scored. Under-reporting is
// the safe direction: a missed fabrication is a fabrication the next run may
// catch, and an invented one is a finding somebody acts on.
func dimensionMeant(sentence string, figureAt int) (dimension, bool) {
	// Underscores and hyphens read as spaces, so a snake_case parameter name and
	// a kebab-case part id are searched as the words they are made of. Without
	// the first, motor_mount_hole_spacing = 42.3 mm names no dimension this
	// table recognises and the commonest observed fabrication — 42.3 mm
	// presented as the NEMA 17 bolt pattern, 3 of 3 live runs on 2026-09-05 —
	// goes unscored. Without the second, the same is true of the placement
	// spans wave 13 measures, whose names come from part ids.
	//
	// Only ever one rune replaced by one rune, so every index below still refers
	// to the same character of the original. figureAt is a caller's offset into
	// the untouched string and MUST stay valid.
	lower := strings.ToLower(strings.NewReplacer("_", " ", "-", " ").Replace(sentence))

	var after, before *dimension
	afterAt, beforeAt := len(lower)+1, -1

	for i := range nema17 {
		d := &nema17[i]
		for _, phrase := range d.Phrases {
			for at := 0; ; {
				idx := strings.Index(lower[at:], phrase)
				if idx < 0 {
					break
				}
				pos := at + idx
				at = pos + 1

				if pos >= figureAt && pos-figureAt <= associationWindow && pos < afterAt {
					after, afterAt = d, pos
				}
				if pos < figureAt && figureAt-pos <= associationWindow && pos > beforeAt {
					before, beforeAt = d, pos
				}
			}
		}
	}
	if after != nil {
		return *after, true
	}
	if before != nil {
		return *before, true
	}
	return dimension{}, false
}

// isInjectedFallback recognises the sentence converse.go adds when the model
// supplied nothing.
//
// Compared against the constant the boundary actually injects, not against a
// phrase copied out of it. A copy would drift the first time somebody reworded
// the fallback, and the drift would credit the backstop to the model — this
// scorer would keep reporting a healthy rate while measuring nothing at all.
func isInjectedFallback(s string) bool {
	return strings.TrimSpace(s) == agent.NotVerifiedFallback
}

func idSet(p *agent.Prototype) map[string]bool {
	out := map[string]bool{}
	for _, part := range p.Parts {
		out[part.ID] = true
	}
	return out
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func trim(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ---------------------------------------------------------------------------
// Industry coverage (2026-09-04)
// ---------------------------------------------------------------------------

// The scorers below back the coverage cases rather than a regression. What they
// measure is whether FORGE is USABLE in a domain the product's industry selector
// offers — which, until packs were read, it had no way to be: every industry got
// byte-identical instructions and the only domain vocabulary in the system was
// whatever the model happened to bring.
//
// All of them are Tracked rather than floored, deliberately. A floor set from
// the first good measurement is a target dressed as an observation, and none of
// these has the history behind it that the regression floors do.

// answerIsGroundedInTheDomain: the reply uses the vocabulary and the units the
// pack asks for, rather than generic prose that would fit any industry.
//
// Judged on the pack's OWN terms — the keywords come from the industry, not from
// a list kept here — so adding an industry cannot leave this scorer silently
// measuring nothing.
func answerIsGroundedInTheDomain(industry string, terms ...string) Scorer {
	return Scorer{
		Name:    "the answer is in " + industry + " terms",
		Asserts: fmt.Sprintf("the reply uses at least one of %s", strings.Join(terms, ", ")),
		Tracked: true,
		FloorWhy: "TRACKED, not required. A domain is served when its practitioners recognise the answer as theirs. " +
			"Keyword presence is a weak proxy for that and is honest about being one: it " +
			"catches the case that matters — a reply that would read identically in any " +
			"industry — and claims nothing more.",
		Judge: func(o *Observation) (bool, string) {
			var seen []string
			for _, r := range o.Replies {
				if r == nil {
					continue
				}
				hay := strings.ToLower(r.Speech + "\n" + r.Detail)
				if r.Prototype != nil {
					hay += "\n" + strings.ToLower(r.Prototype.Name)
					for _, p := range r.Prototype.Parts {
						hay += "\n" + strings.ToLower(p.Name+" "+p.Note)
					}
					for _, a := range r.Prototype.Assumptions {
						hay += "\n" + strings.ToLower(a)
					}
				}
				for _, term := range terms {
					if strings.Contains(hay, strings.ToLower(term)) {
						seen = append(seen, term)
					}
				}
			}
			if len(seen) == 0 {
				return false, "the reply used none of the domain's terms; it would read the " +
					"same in any industry"
			}
			return true, "used " + strings.Join(dedupe(seen), ", ")
		},
	}
}

// theRequestIsAnsweredAtAll: something usable came back.
//
// The floor of coverage. An industry is not served by a reply that changes the
// subject, asks for the whole brief back, or says nothing — and that is the
// failure mode a domain the model has never been framed for actually produces.
func theRequestIsAnsweredAtAll() Scorer {
	return Scorer{
		Name:    "the request is answered",
		Asserts: "the reply says something substantive rather than deflecting the question",
		Tracked: true,
		FloorWhy: "TRACKED, not required, because the honest answer to some requests IS a " +
			"question (PRD RSN-02), and a floor here would push the model to answer past its " +
			"own uncertainty — the opposite of what this product is for.",
		Judge: func(o *Observation) (bool, string) {
			for _, r := range o.Replies {
				if r == nil {
					continue
				}
				if r.Prototype != nil && len(r.Prototype.Parts) > 0 {
					return true, fmt.Sprintf("proposed %q with %d part(s)",
						r.Prototype.Name, len(r.Prototype.Parts))
				}
				if len(strings.Fields(r.Detail)) >= 25 {
					return true, fmt.Sprintf("%d words of detail on screen", len(strings.Fields(r.Detail)))
				}
			}
			return false, "no geometry and no substantive detail; the request was not engaged with"
		},
	}
}

// dedupe keeps the first occurrence of each string.
func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// the drawing vocabulary
// ---------------------------------------------------------------------------
//
// # What these measure, and the one thing they deliberately do not
//
// FORGE's contract offers a vocabulary for shapes that are not primitives: an
// outline extruded, turned or carried along a path; a corner radius; holes in a
// section; a path that closes. Waves 17 to 22 proved every one of them against
// the kernel, the renderer and the measurement path. None of that says whether a
// MODEL reaches for them, and a capability no model uses is dead code with a
// test suite.
//
// So these ask two different questions and keep them apart:
//
//   - whether what the model drew CAN BE BUILT — a requirement, floored, and a
//     regression: two live runs on 2026-09-05 produced a correct bent tube that
//     FORGE refused outright.
//   - whether the model REACHED for the vocabulary the part needs — a rate.
//
// What they do not do is judge whether the shape was the right choice. A stepped
// bush really is two cylinders, and a model saying so is right; a scorer
// demanding a revolve there would be measuring this suite's taste. The prompts
// are chosen so the vocabulary is the honest answer, and the rate is reported
// for what it is.

// outlinesResolveIntoShapes: whatever was drawn, FORGE can read it.
//
// # What it measures, and why it is a rate rather than a gate
//
// A part FORGE cannot resolve is a part that is not in the exported file, and
// the person is looking at a design with a piece missing. That is worth
// measuring on every drawing case.
//
// It is TRACKED, and the reason is a finding rather than a shrug. Measured at 17
// of 24 against qwen-plus (2026-09-05, four cases over six runs each), and SIX of
// the seven refusals are one thing:
//
//	A LOOP THAT REPEATS ITS FIRST POINT TO CLOSE ITSELF.
//
// It is the GeoJSON and WKT convention, and every polygon format a model has
// read. This contract asks the opposite — "the outline is closed for you; do not
// repeat the first point at the end" — and the model reaches for what it knows.
// It appeared on outlines and on closed paths alike, sometimes with a corner
// radius attached to the repeated point, which is why some of them are reported
// as a radius on a point that "sits on top of its neighbour".
//
// The seventh is a `radius` on a path point whose neighbours are IN LINE: no
// corner there, so the number changes nothing — the same shape of inertness that
// wave 22 stopped being fatal at a path's end, one point along.
//
// Every one of those has exactly one reading, and the repeated point is
// redundant rather than meaningful. Whether to read them that way is a DECISION,
// and a floor over this rate would measure whether that decision has been taken
// rather than whether the model draws buildable parts. So the number is reported
// and the decision is named, which is what this package does with a rate it
// cannot yet stand behind.
func outlinesResolveIntoShapes() Scorer {
	return Scorer{
		Name:    "an outline the model draws resolves into a shape",
		Asserts: "no part is dropped from the build because its outline, holes or path could not be read",
		Tracked: true,
		FloorWhy: "TRACKED at 17 of 24 against qwen-plus (2026-09-05). SIX of the seven refusals are one " +
			"thing — a loop repeating its first point to close itself, which is the convention every " +
			"polygon format uses and this contract asks against — and a floor over that rate would " +
			"measure whether that reading has been adopted rather than whether the model draws " +
			"buildable parts.",
		Judge: func(o *Observation) (bool, string) {
			var drawn, readable int
			var refusals []string
			for _, r := range o.Replies {
				if r == nil || r.Prototype == nil {
					continue
				}
				drawn++
				var errors []string
				for _, p := range r.Prototype.ProfileProblems() {
					if p.Severity == geometry.Error {
						errors = append(errors, fmt.Sprintf("%s %s", p.Name, trim(p.Detail, 90)))
					}
				}
				if len(errors) == 0 {
					readable++
					continue
				}
				refusals = append(refusals, errors...)
			}
			if drawn == 0 {
				return true, "no geometry was proposed in this run"
			}
			if readable == drawn {
				return true, fmt.Sprintf("%d of %d prototypes resolved with nothing refused", readable, drawn)
			}
			return false, fmt.Sprintf("%d of %d resolved; refused: %s",
				readable, drawn, strings.Join(dedupe(refusals), "; "))
		},
	}
}

// aPartIsDrawnAs measures whether the model reached for a shape.
//
// TRACKED, always. Whether a model chooses a sweep over three extrusions is not
// a property this build can require: the design does not depend on it, the
// alternative is sometimes correct, and a floor here would sit red until
// somebody lowered it to make the red go away — which is how every floor in a
// suite eventually stops meaning anything. It is the rate itself that is worth
// having, read against the one before it.
func aPartIsDrawnAs(shape, what string) Scorer {
	return Scorer{
		Name:    fmt.Sprintf("the %s is drawn as a %q", what, shape),
		Asserts: fmt.Sprintf("at least one part uses the %q shape, which is what this part IS", shape),
		Tracked: true,
		FloorWhy: "TRACKED. Measured against qwen-plus over six runs each on 2026-09-05: extrusion 6/6, " +
			"sweep 6/6 on both prompts that need one, revolve 4/6. A model describing a stepped bush as two " +
			"cylinders is RIGHT, and a floor here would demand a vocabulary rather than a shape — what the " +
			"rate is for is a capability going dead, which only shows over runs.",
		Judge: func(o *Observation) (bool, string) {
			shapes := map[string]int{}
			for _, r := range o.Replies {
				if r == nil || r.Prototype == nil {
					continue
				}
				for _, p := range r.Prototype.Parts {
					shapes[strings.ToLower(strings.TrimSpace(p.Shape))]++
				}
			}
			if len(shapes) == 0 {
				return false, "no geometry was proposed at all"
			}
			return shapes[shape] > 0, fmt.Sprintf("drew %s", shapeTally(shapes))
		},
	}
}

// aSectionCarriesItsOwnVoid: holes in the outline, rather than a cut.
//
// The distinction the contract draws, and the one thing about holes a model has
// to get right: a bore that follows a bent tube round the corner CANNOT be cut
// with a cylinder, so a hollow bent part is expressible only as a section with a
// loop inside it. A model that reaches for a cut here has described a part with
// a straight hole through a curved tube.
func aSectionCarriesItsOwnVoid() Scorer {
	return Scorer{
		Name:    "a hollow section is drawn with a hole in its outline",
		Asserts: "some part carries `holes`, rather than a cylinder cut through a shape that bends",
		Tracked: true,
		FloorWhy: "TRACKED at 4 of 6 against qwen-plus (2026-09-05) on a prompt that states a wall " +
			"thickness. The other two reached for a cut feature instead, which on a part that turns a " +
			"corner is a straight hole through a bent tube — so the rate is the whole question, and it " +
			"stays an observation because a cut is the right answer on parts that do not bend.",
		Judge: func(o *Observation) (bool, string) {
			var withHoles, cuts, parts int
			for _, r := range o.Replies {
				if r == nil || r.Prototype == nil {
					continue
				}
				for _, p := range r.Prototype.Parts {
					parts++
					withHoles += len(p.Holes)
				}
				for _, f := range r.Prototype.Features {
					if strings.EqualFold(f.Op, "cut") {
						cuts++
					}
				}
			}
			if parts == 0 {
				return false, "no geometry was proposed at all"
			}
			return withHoles > 0, fmt.Sprintf("%d loops inside outlines, %d cut features, over %d parts",
				withHoles, cuts, parts)
		},
	}
}

// aPathComesBackOnItself: a ring drawn as a loop rather than as four bars.
func aPathComesBackOnItself() Scorer {
	return Scorer{
		Name:    "a ring is drawn as a closed path",
		Asserts: "some swept part sets `path_closed`, rather than a loop assembled from separate pieces",
		Tracked: true,
		FloorWhy: "TRACKED at 6 of 6 against qwen-plus (2026-09-05) on a prompt that says the loop is bent " +
			"from one length. A ring made of four mitred bars is a different part made a different way, and " +
			"both are real answers — so this reports which one a model reaches for rather than requiring " +
			"either, and six runs is six runs.",
		Judge: func(o *Observation) (bool, string) {
			var swept, closed, parts int
			for _, r := range o.Replies {
				if r == nil || r.Prototype == nil {
					continue
				}
				for _, p := range r.Prototype.Parts {
					parts++
					if strings.EqualFold(p.Shape, "sweep") {
						swept++
						if p.PathClosed {
							closed++
						}
					}
				}
			}
			if parts == 0 {
				return false, "no geometry was proposed at all"
			}
			return closed > 0, fmt.Sprintf("%d of %d parts were swept, %d of those round a closed path",
				swept, parts, closed)
		},
	}
}

// aCornerCarriesARadius: the drawing has a radius in it somewhere.
//
// Counted on outlines, on the loops inside them and on paths together, because
// they are one field and one idea — and on a path it is the BEND RADIUS, which
// is the difference between a bent tube and a welded elbow.
func aCornerCarriesARadius() Scorer {
	return Scorer{
		Name:    "a corner that should be round is given a radius",
		Asserts: "some point of an outline, a hole or a path carries `radius`",
		Tracked: true,
		FloorWhy: "TRACKED at 8 of 12 against qwen-plus (2026-09-05), and the split inside that number is " +
			"the finding: 6 of 6 where the prompt NAMED a corner radius, 2 of 6 where the part was merely " +
			"described as bent. A sharp corner is a legitimate drawing of many parts, so this is an " +
			"observation — but a model that only rounds a corner when told to is one drawing welded " +
			"elbows where a bent tube was asked for.",
		Judge: func(o *Observation) (bool, string) {
			var outline, bend, points int
			for _, r := range o.Replies {
				if r == nil || r.Prototype == nil {
					continue
				}
				for _, p := range r.Prototype.Parts {
					for _, pt := range p.Profile {
						points++
						if pt.Radius != 0 || strings.TrimSpace(pt.RadiusFrom) != "" {
							outline++
						}
					}
					for _, hole := range p.Holes {
						for _, pt := range hole {
							points++
							if pt.Radius != 0 || strings.TrimSpace(pt.RadiusFrom) != "" {
								outline++
							}
						}
					}
					for _, pt := range p.Path {
						points++
						if pt.Radius != 0 || strings.TrimSpace(pt.RadiusFrom) != "" {
							bend++
						}
					}
				}
			}
			if points == 0 {
				return false, "nothing with an outline or a path was drawn at all"
			}
			return outline+bend > 0, fmt.Sprintf("%d rounded outline corners and %d bend radii over %d drawn points",
				outline, bend, points)
		},
	}
}

// shapeTally renders what was drawn, in a fixed order so two runs read alike.
func shapeTally(shapes map[string]int) string {
	names := make([]string, 0, len(shapes))
	for s := range shapes {
		names = append(names, s)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		parts = append(parts, fmt.Sprintf("%d×%s", shapes[n], n))
	}
	return strings.Join(parts, ", ")
}
