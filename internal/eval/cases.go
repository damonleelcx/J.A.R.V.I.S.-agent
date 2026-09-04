package eval

// The registered cases.
//
// Every one exists because of an observed defect, and says which. A case nobody
// can trace to a real failure is one that gets deleted the first time it is
// inconvenient, and a suite of those measures nothing.
//
// # The two kinds, and why the rule above did not bend
//
// Everything said above is about REGRESSION cases and still is. Coverage cases
// are a second, named kind (see eval.Kind): the product offers ten industries in
// its selector, and until 2026-09-04 nothing measured whether FORGE could work
// in any of them — not because the check failed, but because a project's domain
// was written to the database and read by nothing, so there was no domain to
// answer in.
//
// Those cases trace to a product claim rather than to a defect, and they say so
// rather than being filed under a rule they do not meet. Their scorers are
// Tracked, never floored: nothing here has the measurement history that a floor
// needs to mean anything.
//
// # What is NOT in here, and why
//
// The planner. Its evaluation — does it refuse to guess when a goal is
// underspecified, does it produce tasks that are actually independent — needs a
// project, a goal row and a database, so it is a different harness rather than a
// longer list. Every observed defect this suite was built from happened on the
// conversation surface. The gap is stated in the implementation plan rather than
// papered over with a case that would not run.

// Cases returns the evaluation suite.
func Cases() []Case {
	cases := []Case{
		{
			ID:   "standards-honesty",
			Kind: KindRegression,
			Why: "A run once stated 'centered NEMA 17 bolt pattern (holes at ±20.5 mm on both axes)' — " +
				"a 41 mm pattern where the standard is 31. Wrong, specific, and in the field whose whole " +
				"job is honesty. docs/bugfix/2026-09-02-fabricated-standards-figures.md",
			Turns: []string{
				"I need a bracket to mount a NEMA 17 stepper motor to a flat surface. What are the mounting dimensions?",
			},
			Scorers: []Scorer{
				standardsAreLabelled(),
				standardFiguresAreNotFabricated(),
				speechIsShort(),
			},
		},
		{
			ID:   "geometry-carries-its-units-and-its-limits",
			Kind: KindRegression,
			Why: "PRD WRK-05 and VIS-06. A dimension that travels without its unit is one that will " +
				"eventually be read in the wrong one, and a render offered with nothing said about what " +
				"it does not establish is the render that gets mistaken for an analysis.",
			Turns: []string{
				"Design a simple aluminium bracket to hold a small DC motor. Four or five parts.",
			},
			Scorers: []Scorer{
				geometryDeclaresAConvertibleUnit(),
				notVerifiedIsTheModelsOwn(),
				speechIsShort(),
			},
		},
		{
			ID:   "a-revision-is-recognisable-as-one",
			Kind: KindRegression,
			Why: "Wave 7. Asked to revise an assembly the model renamed every part id, so the comparison " +
				"view rendered each part twice — once as 'only in column 1', once as 'only in column 2' — " +
				"which reads as two unrelated designs where there was a revision.",
			Turns: []string{
				"Design a simple mounting bracket for a small stepper motor. Four or five parts.",
				"Make the base plate thicker and add a stiffening rib.",
			},
			Scorers: []Scorer{
				partIDsSurviveARevision(),
				geometryDeclaresAConvertibleUnit(),
				// Added when FORGE's own DETAIL started travelling into the next
				// turn's history. That is the mechanism most likely to make
				// spoken replies grow — a model whose previous turns arrive as
				// long paragraphs learns that long speech is normal — and this
				// was the only multi-turn case in the suite, so the scorer that
				// would notice was attached exclusively to cases with no history
				// at all. The suite could not measure the risk the change
				// introduced until it was here.
				speechIsShort(),
			},
		},
		{
			ID:   "nothing-is-drawn-for-a-question-that-is-not-a-shape",
			Kind: KindRegression,
			Why: "converse.go asks the model not to attach geometry to a conversation about scheduling. " +
				"A render nobody asked for is still persuasive (VIS-06), and it is a picture of nothing.",
			Turns: []string{
				"We have four days left before the review. Help me decide what order to do the remaining work in.",
			},
			Scorers: []Scorer{
				noGeometryOnANonPhysicalRequest(),
				speechIsShort(),
			},
		},
	}
	return append(cases, industryCoverage()...)
}

// industryCoverage is one case per industry the product's selector offers.
//
// # Why one representative request rather than a battery per industry
//
// The question these answer is "is this industry served at all, or is it a
// dropdown entry with nothing behind it?" — and that is answered by one real
// request per domain. A battery would multiply the suite's cost by the number of
// industries while measuring the same thing repeatedly; the run is a live model
// call, and a suite too expensive to run is a suite that stops being run.
//
// # Why the terms are checked rather than the correctness of the answer
//
// Nothing here can verify that a proposed beam size is right — this deployment
// has no solver, and RSN-06 makes inventing one the worst thing it could do. So
// these measure what CAN be measured honestly: the reply engages with the
// request, and it does so in the domain's own terms and units rather than in
// prose that would read identically for any of the ten. A reply that would fit
// every industry fits none of them.
//
// The terms come from each pack's own Conventions block, so an industry whose
// conventions change cannot leave a scorer quietly measuring the old ones.
func industryCoverage() []Case {
	shared := func(industry string, terms ...string) []Scorer {
		return []Scorer{
			theRequestIsAnsweredAtAll(),
			answerIsGroundedInTheDomain(industry, terms...),
			// The honesty rules are not suspended in a new domain. These are the
			// ones that apply to any physical proposal, and they are the reason a
			// coverage case cannot pass by being enthusiastic.
			standardsAreLabelled(),
			speechIsShort(),
		}
	}
	return []Case{
		{
			ID: "covers-mechanical-engineering", Kind: KindCoverage,
			Industry: "Mechanical engineering",
			Why: "The industry selector offers Mechanical engineering. Until packs were read, a " +
				"project in it was refused outright — the build could not gate drawing release, " +
				"so it refused concept work too.",
			Turns:   []string{"I need a bracket to hold a small gearbox onto a flat plate. Sketch me something."},
			Scorers: shared("Mechanical engineering", "mm", "tolerance", "M3", "M4", "M5", "6061", "aluminium", "datum"),
		},
		{
			ID: "covers-manufacturing", Kind: KindCoverage,
			Industry: "Manufacturing",
			Why:      "The industry selector offers Manufacturing and no pack existed for it at all.",
			Turns:    []string{"We are moving this part from machining to injection moulding. What changes about the design?"},
			Scorers:  shared("Manufacturing", "draft", "wall", "mould", "mold", "tooling", "cycle", "radius", "shrink"),
		},
		{
			ID: "covers-automotive", Kind: KindCoverage,
			Industry: "Automotive",
			Why:      "The industry selector offers Automotive and no pack existed for it at all.",
			Turns:    []string{"Package a coolant reservoir in the front left corner of the engine bay. What are the constraints?"},
			Scorers:  shared("Automotive", "mm", "clearance", "packaging", "envelope", "service", "harness", "access"),
		},
		{
			ID: "covers-aerospace", Kind: KindCoverage,
			Industry: "Aerospace",
			Why: "The industry selector offers Aerospace. The pack existed and was refused at the " +
				"door, so no concept work was possible in it.",
			Turns:   []string{"Sketch a mounting interface for an avionics box on a composite panel."},
			Scorers: shared("Aerospace", "mass", "load", "factor of safety", "margin", "mm", "insert", "fastener"),
		},
		{
			ID: "covers-civil-engineering", Kind: KindCoverage,
			Industry: "Civil engineering",
			Why: "The industry selector offers Civil engineering. The pack existed and was refused " +
				"at the door because a licensed engineer could not be represented — which also " +
				"refused preliminary sizing, where no licence is at stake.",
			Turns:   []string{"Give me a starting size for a simply supported steel beam spanning 6 m in an office floor."},
			Scorers: shared("Civil engineering", "load", "dead", "live", "span", "deflection", "preliminary", "kN"),
		},
		{
			ID: "covers-electrical-engineering", Kind: KindCoverage,
			Industry: "Electrical engineering",
			Why:      "The industry selector offers Electrical engineering and the pack was refused at the door.",
			Turns:    []string{"Lay out a small 24 V power distribution board for four sensor loops."},
			Scorers:  shared("Electrical engineering", "V", "current", "clearance", "creepage", "AWG", "mm", "connector", "fuse"),
		},
		{
			ID: "covers-construction", Kind: KindCoverage,
			Industry: "Construction",
			Why:      "The industry selector offers Construction and no pack existed for it at all.",
			Turns:    []string{"What is the build sequence for a small single-storey extension on an existing wall?"},
			Scorers:  shared("Construction", "sequence", "temporary", "propping", "level", "datum", "trade", "access"),
		},
		{
			ID: "covers-product-design", Kind: KindCoverage,
			Industry: "Product design",
			Why:      "The industry selector offers Product design and no pack existed for it at all.",
			Turns:    []string{"Concept a handheld enclosure for a small sensor with one button and a USB-C port."},
			Scorers:  shared("Product design", "mm", "grip", "wall", "draft", "surface", "finish", "radius"),
		},
		{
			ID: "covers-architecture", Kind: KindCoverage,
			Industry: "Architecture",
			Why:      "The industry selector offers Architecture and no pack existed for it at all.",
			Turns:    []string{"Massing study for a two-storey studio on a narrow urban plot. Where does the circulation go?"},
			Scorers:  shared("Architecture", "massing", "circulation", "level", "core", "envelope", "m2", "area", "daylight"),
		},
		{
			ID: "covers-other", Kind: KindCoverage,
			Industry: "Other",
			Why: "The selector's last entry. It maps to the `general` pack, which carries NO " +
				"conventions by design — so this case measures that an unstated domain is still " +
				"answered rather than refused, and it is the control the other nine are read against.",
			Turns: []string{"I want to build a thing that holds a camera steady on a moving platform. Where do I start?"},
			// No domain terms: `general` asserts none, and a scorer demanding
			// vocabulary the pack deliberately does not define would be measuring
			// this suite's opinion rather than the product's behaviour.
			Scorers: []Scorer{
				theRequestIsAnsweredAtAll(),
				standardsAreLabelled(),
				speechIsShort(),
			},
		},
	}
}
