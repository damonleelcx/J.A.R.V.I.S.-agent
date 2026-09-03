package eval

// The registered cases.
//
// Every one exists because of an observed defect, and says which. A case nobody
// can trace to a real failure is one that gets deleted the first time it is
// inconvenient, and a suite of those measures nothing.
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
	return []Case{
		{
			ID: "standards-honesty",
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
			ID: "geometry-carries-its-units-and-its-limits",
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
			ID: "a-revision-is-recognisable-as-one",
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
			},
		},
		{
			ID: "nothing-is-drawn-for-a-question-that-is-not-a-shape",
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
}
