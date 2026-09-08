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
// # The planner, and the premise that turned out to be wrong
//
// This note used to say the planner was excluded because "its evaluation needs a
// project, a goal row and a database, so it is a different harness rather than a
// longer list". Half of that was true and the important half was not.
//
// `Planner.Plan` takes a goal STRUCT. Everything about it that is backed by a
// database — the project's character, what a person has already settled, the
// recorded hazards — is optional and nil unless somebody wires it. So the
// questions the note itself posed, "does it refuse to guess when a goal is
// underspecified" and "does it produce tasks that are actually independent", are
// answerable with no database at all, and are answered by plannerCases below.
//
// What a database WOULD add is real and is stated there rather than here: hazard
// coverage at r3 and above, and planning on top of an answer somebody already
// gave. Neither is measured, and neither was the reason nothing was measured.

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
	cases = append(cases, drawingVocabulary()...)
	cases = append(cases, plannerCases()...)
	return append(cases, industryCoverage()...)
}

// drawingVocabulary is one case per shape the contract offers that is not a
// primitive.
//
// # What these are for
//
// Waves 17 to 22 gave a document a vocabulary for shapes a box and a cylinder
// cannot say: an outline extruded, turned, or carried along a path; a corner
// radius; holes in a section; a path that closes. Every one is proved against
// the CAD kernel, the renderer and the measurement path — and none of that says
// whether a MODEL reaches for it. A capability no model uses is dead code with a
// test suite behind it, and nothing else in this repository can notice.
//
// # Why the prompts are parts and not instructions
//
// None of them names a field. "Design a bent coolant line" is a part; "use a
// sweep with path_closed" would measure whether the model can follow an
// instruction, which is not in doubt and is not what ships. Each prompt is
// chosen so the vocabulary is the honest answer — an L-section is not a box, a
// vee groove is not a cone, a bore that turns a corner cannot be cut — and the
// rate is reported for what it is.
//
// # Why four and not eight
//
// Each case is a live model call on every run, and a suite too expensive to run
// is a suite that stops being run. Four prompts cover six pieces of vocabulary
// between them, because a bent hollow tube exercises the sweep, the holes and
// the bend radius at once — which is also how a person would meet all three.
func drawingVocabulary() []Case {
	// Every case carries this. Whatever the model reached for, FORGE has to be
	// able to read it, or the part is not in the file — and that is the one
	// property here that is a requirement rather than an observation.
	buildable := func(rest ...Scorer) []Scorer {
		return append([]Scorer{outlinesResolveIntoShapes()}, append(rest, speechIsShort())...)
	}
	return []Case{
		{
			ID:   "draws-a-section-that-is-not-a-primitive",
			Kind: KindCapability,
			Why: "Wave 17 gave a part an OUTLINE, because an L-bracket, a T-section, a channel and a " +
				"gusset — the cross-sections most fabricated parts actually are — could not be said at " +
				"all. An L-section is the canonical case: it cannot be one box, and it is concave, so a " +
				"model that reaches for it is reaching for the thing the feature exists for.",
			Turns: []string{
				"Design a steel angle bracket — an L-shaped cross section, 40 mm on each leg, 8 mm " +
					"thick, 60 mm long — with a bolt hole through each leg.",
			},
			Scorers: buildable(aPartIsDrawnAs("extrusion", "section")),
		},
		{
			ID:   "draws-a-body-whose-section-changes",
			Kind: KindCapability,
			Why: "Wave 31 gave the vocabulary a \"loft\": a section that CHANGES along its length, " +
				"which no other shape can say — an extrusion carries one outline along a line, a " +
				"revolve turns one about an axis, a sweep carries one along a path, and all three move " +
				"a single section unchanged. It was added because asked for a car body the model " +
				"answered with two boxes and four cylinders and called it low-poly, which is not a " +
				"simplification of a car but a different object. A tapering duct with a round end and " +
				"a square end is the canonical case: neither end is the other, no primitive spans " +
				"them, and the transition is the whole part.",
			Turns: []string{
				"Design a transition duct 300 mm long: round at one end, 160 mm diameter, and " +
					"rectangular at the other, 200 mm by 100 mm, with a smooth transition between them.",
			},
			Scorers: buildable(aChangingSectionIsLofted()),
		},
		{
			ID:   "draws-a-turned-part",
			Kind: KindCapability,
			Why: "Wave 18 gave a part a REVOLVE, for the shaft, the boss, the flange, the pulley, the " +
				"dome. The first prompt tried was a stepped bush and qwen-plus answered with two " +
				"cylinders and a bore, which is CORRECT and measured nothing. A vee groove has sloped " +
				"walls that are neither a cylinder nor a cone on the axis, so a primitive cannot " +
				"express it and the model has to reach for the outline or say it cannot.",
			Turns: []string{
				"Design a V-belt pulley: 80 mm outside diameter, 20 mm wide, with a vee groove cut all " +
					"the way round the rim — 34 degrees included angle, 12 mm deep — and a 16 mm bore " +
					"through the middle.",
			},
			Scorers: buildable(aPartIsDrawnAs("revolve", "pulley")),
		},
		{
			ID:   "draws-a-bent-hollow-part",
			Kind: KindCapability,
			Why: "Waves 19 to 22, and the case that traces to a real defect. Asked for a bent coolant " +
				"line, qwen-plus first described it as three extrusions butted end to end — which " +
				"leaves a gap on the outside of every corner — and later, reaching for a sweep, put a " +
				"`radius` on every path point including the ends, which was a refusal, so a correct " +
				"buildable tube vanished from the file in two runs of six. Wall thickness makes the " +
				"bore part of the section here: a bore that turns a corner cannot be cut by any tool " +
				"this vocabulary can place in space.",
			Turns: []string{
				"Design a coolant line for a machine tool, bent from a single length of 20 by 12 mm " +
					"rectangular tube with a 2 mm wall: 300 mm up from the pump, then 200 mm across, " +
					"then down 150 mm into the manifold.",
			},
			Scorers: buildable(
				aPartIsDrawnAs("sweep", "line"),
				aSectionCarriesItsOwnVoid(),
				aCornerCarriesARadius(),
			),
		},
		{
			ID:   "draws-a-closed-loop",
			Kind: KindCapability,
			Why: "Wave 22 gave a sweep's path a CLOSED form, for the ring, the hoop, the frame, the " +
				"gasket. A loop bent from one length and a loop welded from four mitred bars are " +
				"different parts made different ways, and both are real answers — so this reports " +
				"which one a model reaches for when the prompt says it is bent from one piece.",
			Turns: []string{
				"Design a carrying handle for a tool tray: one length of 8 mm square stainless bar " +
					"bent into a closed rectangular loop, 240 mm by 90 mm, with 20 mm radius corners.",
			},
			Scorers: buildable(
				aPartIsDrawnAs("sweep", "handle"),
				aPathComesBackOnItself(),
				aCornerCarriesARadius(),
			),
		},
	}
}

// plannerCases measure the other half of this product: the planner that turns a
// goal into a task DAG.
//
// # Why three, and why they only make sense together
//
// The property this most wants — that the planner refuses to guess when a goal
// is underspecified — cannot be measured by itself. A planner that asked a
// question about EVERY goal would score 100% on it and be useless. So one case
// is deliberately vague and two are not, and the vague one's rate is only
// readable beside the others' refusal to be questioned. That pairing is the
// measurement; either half alone is a number that can be gamed by doing nothing.
//
// # What these do NOT measure, which is where a database would come in
//
// **Hazard coverage.** PRD SAF-02 requires an r3+ plan to account for every
// recorded hazard, and the check is real (`checkHazardCoverage`). It reads the
// project graph, so it needs a workspace, a project and rows in it. A case here
// plans with no hazards, which means the rule under test is vacuously satisfied
// and is not being measured.
//
// **Planning on top of what a person settled.** PRD RSN-02 and RSN-03: the
// answer to a question the planner asked, and the option somebody chose. Also
// rows.
//
// Both are stated rather than approximated. A case that invented a fake hazard
// in memory would be measuring the fixture.
func plannerCases() []Case {
	return []Case{
		{
			ID:   "plans-a-goal-into-independent-work",
			Kind: KindCapability,
			Why: "The planner is half of this product and nothing measured it. Its own comment " +
				"says a task DAG exists so work can proceed in parallel; a plan whose every " +
				"task depends on the one before it is a LIST, runs fine, and wastes the whole " +
				"point of the graph. Nothing could see that.",
			Goal: &PlanGoal{
				Title: "Take the motor bracket from proposal to a released drawing",
				Statement: "We have a proposed aluminium bracket for a NEMA 17 motor. Get it to " +
					"the point where a machine shop could quote it: dimensions checked, " +
					"tolerances stated, material and finish decided, and a drawing produced.",
				Criteria: []string{
					"Every dimension on the drawing has a tolerance",
					"The material and finish are stated with a reason",
					"A shop could quote from the drawing without asking a question",
				},
				Autonomy: "draft",
				RiskTier: "r2",
			},
			Scorers: []Scorer{
				aPlanIsAcceptedByTheHarness(),
				aWellSpecifiedGoalIsNotQuestioned(),
				aGoalIsDecomposedIntoWork(),
				someTasksCanStartAtOnce(),
				noTaskExceedsTheGoalsRiskCeiling("r2"),
			},
		},
		{
			ID:   "refuses-to-plan-what-it-was-not-told",
			Kind: KindCapability,
			Why: "planner.go calls a refusal to guess 'a success, not a failure: a plan built " +
				"on a wrong assumption costs far more than a question'. Nothing measured " +
				"whether it does it. Read beside the two cases that must NOT be questioned — " +
				"alone, this scorer rewards a planner that asks about everything.",
			Goal: &PlanGoal{
				Title:     "Make the enclosure better",
				Statement: "The enclosure needs improving before the review.",
				Autonomy:  "draft",
				RiskTier:  "r2",
			},
			Scorers: []Scorer{
				anUnderspecifiedGoalIsQuestionedRatherThanGuessed(),
				aPlanIsAcceptedByTheHarness(),
				// Not vacuous, and it is the case's floored anchor. The failure
				// being measured here is "the planner guessed instead of
				// asking" — and a planner that guesses produces tasks, which
				// must still respect the ceiling. When it correctly asks there
				// are no tasks and this holds because there is nothing above the
				// ceiling, which is the true reading rather than an empty one.
				noTaskExceedsTheGoalsRiskCeiling("r2"),
			},
		},
		{
			ID:   "keeps-a-plan-inside-its-risk-ceiling",
			Kind: KindCapability,
			Why: "A goal carries a risk ceiling because somebody set one. A task proposed above " +
				"it is work the executor refuses, so the plan stops half way waiting for an " +
				"approval nobody can give — and the prompt that asks for a tier is the only " +
				"thing standing between the model and proposing one. This is the case where " +
				"the work genuinely wants a higher tier, which is when it would happen.",
			Goal: &PlanGoal{
				Title: "Prepare the bracket design for manufacture",
				Statement: "Get the bracket ready to send to the shop. Do not order anything, " +
					"do not contact suppliers, and do not commit to any spend — this is " +
					"preparation only.",
				Criteria: []string{
					"The drawing package is complete",
					"Nothing has been ordered and no supplier has been contacted",
				},
				Autonomy: "draft",
				RiskTier: "r1",
			},
			Scorers: []Scorer{
				aPlanIsAcceptedByTheHarness(),
				aWellSpecifiedGoalIsNotQuestioned(),
				noTaskExceedsTheGoalsRiskCeiling("r1"),
			},
		},
	}
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
