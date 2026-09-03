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
			"face width", "square body", "body size", "square across the face",
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
	lower := strings.ToLower(sentence)

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
