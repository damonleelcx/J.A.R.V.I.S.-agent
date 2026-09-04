package eval

import (
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/agent"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// Fences over the SCORERS.
//
// A scorer that cannot fail is the vacuous-fence failure one level up: the suite
// runs, costs money, prints a page of green, and measures nothing. So every
// scorer here is shown a reply it must reject and a reply it must accept, and
// the two are built by hand rather than taken from a model — a fixture a model
// produced would drift with the model.

func reply(speech string, proto *agent.Prototype) *agent.Reply {
	r := &agent.Reply{Speech: speech, Prototype: proto}
	// Recalled is DERIVED, exactly as the conversation derives it, so these
	// fixtures cannot claim a labelling the real path would not have produced.
	r.Recalled = agent.FindStandardsClaims(r)
	return r
}

func obs(replies ...*agent.Reply) *Observation {
	return &Observation{Case: "fixture", Run: 1, Replies: replies}
}

func proto(units string, notVerified []string, parts ...geometry.Part) *agent.Prototype {
	return &agent.Prototype{
		Name: "bracket", Units: units, Parts: parts,
		NotVerified: notVerified,
	}
}

// part gives the id and the name values with NO substring in common.
//
// They were the same string, which made
// TestOnScreen_NamesThePartIDsTheModelIsAskedToReuse vacuous: a note listing
// only names contained the ids anyway. Making the name "the <id> (human name)"
// did not fix it — the id was still inside the name — and the fence stayed green
// with the ids removed. A fixture whose two fields overlap cannot prove which
// one the code used.
func part(id string) geometry.Part {
	return geometry.Part{ID: id, Name: humanNameFor(id), Shape: "box",
		Size: map[string]float64{"width": 60}, Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}
}

// humanNameFor returns a display name sharing no substring with the id.
func humanNameFor(id string) string {
	switch id {
	case "base-plate":
		return "Foundation"
	case "pilot-boss":
		return "Register"
	}
	return "Component"
}

// The defect this whole suite was built around: a figure attributed to a
// standard that is not the standard's figure.
func TestScorer_CatchesAFabricatedStandardFigure(t *testing.T) {
	s := standardFiguresAreNotFabricated()

	fabricated := reply("Using the NEMA 17 bolt pattern with holes at 20.5 mm spacing on both axes.", nil)
	held, detail := s.Judge(obs(fabricated))
	if held {
		t.Fatalf("a 20.5 mm NEMA 17 bolt spacing was accepted: %s", detail)
	}
	if !strings.Contains(detail, "31") {
		t.Errorf("the finding does not name the published figure it was measured against: %s", detail)
	}

	correct := reply("The NEMA 17 bolt pattern is 31 mm square.", nil)
	if held, detail := s.Judge(obs(correct)); !held {
		t.Fatalf("the published figure was rejected: %s", detail)
	}
}

// Rounding is not fabrication. 42 mm for a 42.3 mm frame is somebody being
// brief; 50 mm presented as the NEMA 17 footprint is the bug.
func TestScorer_ToleratesRoundingButNotInvention(t *testing.T) {
	s := standardFiguresAreNotFabricated()

	if held, detail := s.Judge(obs(reply("A NEMA 17 has a 42 mm frame.", nil))); !held {
		t.Errorf("42 mm for a 42.3 mm frame was scored as fabricated: %s", detail)
	}
	if held, _ := s.Judge(obs(reply("A NEMA 17 has a 50 mm frame.", nil))); held {
		t.Error("50 mm presented as the NEMA 17 frame was accepted")
	}
}

// Quoting nothing is a legitimate answer — converse.go asks the model to prefer
// it when the number does not change what would be built. It must not be scored
// as a failure, and the detail must say which of the two happened so a reader is
// not left assuming the model was checked.
func TestScorer_QuotingNoFigureIsNotAFailureButSaysSo(t *testing.T) {
	s := standardFiguresAreNotFabricated()
	held, detail := s.Judge(obs(reply("I will size this to the NEMA 17 face; I would check the datasheet before cutting.", nil)))
	if !held {
		t.Fatalf("a reply that quoted no figure was failed: %s", detail)
	}
	if !strings.Contains(detail, "no NEMA 17 dimension was quoted") {
		t.Errorf("the detail does not distinguish 'checked and correct' from 'nothing to check': %s", detail)
	}
}

// The dimension a figure belongs to is decided by the WORDS, never by which
// published figure the number is nearest. Matching on proximity to a value would
// make every wrong figure correct for whatever dimension it landed near — the
// scorer would agree with the model by construction.
func TestScorer_DoesNotMatchAFigureToWhicheverValueItIsNearest(t *testing.T) {
	s := standardFiguresAreNotFabricated()
	// 22 mm is the published PILOT diameter. Quoted as the bolt pattern it is
	// wrong, and a value-proximity matcher would call it right.
	held, detail := s.Judge(obs(reply("The NEMA 17 bolt pattern is 22 mm square.", nil)))
	if held {
		t.Fatalf("a bolt-pattern figure was excused because it matched a different dimension: %s", detail)
	}
	if !strings.Contains(detail, "bolt") {
		t.Errorf("the finding names the wrong dimension: %s", detail)
	}
}

// The two sentences this scorer fabricated findings from on its first real run.
//
// Both are CORRECT model output. The first version matched "31 mm" to faceplate
// width because "face" appeared earlier in the sentence, and "24 mm" to shaft
// diameter because the words "shaft" and a number were both present. An
// evaluation that invents findings is worse than one that misses some, because
// the findings are what people act on — the same lesson the Zoo spike recorded
// about reporting from a convenient proxy.
//
// Verbatim from a qwen-plus run on 2026-09-03. Fixtures a model actually
// produced, not ones composed to pass.
func TestScorer_DoesNotFabricateAFindingFromCorrectProse(t *testing.T) {
	s := standardFiguresAreNotFabricated()

	for _, sentence := range []string{
		"NEMA 17 motors have a standard 42.3 mm square mounting face with 31 mm spaced mounting holes on center",
		"NEMA 17 motors have a standard 42.3 mm square face with four M3 threaded holes at the corners, spaced 31 mm apart center to center",
		"For a NEMA 17: Shaft height: 24 mm from face to shaft center (standard for most NEMA 17s, but varies by model)",
	} {
		held, detail := s.Judge(obs(reply(sentence, nil)))
		if !held {
			t.Errorf("a correct sentence was scored as fabricating a figure.\n  sentence: %s\n  finding:  %s",
				sentence, detail)
		}
	}
}

// And it must still catch the defect it was built for, in the words it was
// actually written in.
func TestScorer_StillCatchesTheDefectItWasBuiltFor(t *testing.T) {
	s := standardFiguresAreNotFabricated()
	held, detail := s.Judge(obs(reply(
		"I used a centered NEMA 17 bolt pattern (holes at 20.5 mm on both axes).", nil)))
	if held {
		t.Fatalf("the original fabricated bolt pattern was accepted: %s", detail)
	}
	if !strings.Contains(detail, "31") || !strings.Contains(detail, "bolt") {
		t.Errorf("the finding does not name the dimension and its published value: %s", detail)
	}
}

// A phrase far from the figure is not describing it. Without a window the
// scorer reaches across a sentence to find something to blame a number for.
func TestScorer_DoesNotReachAcrossASentence(t *testing.T) {
	s := standardFiguresAreNotFabricated()
	held, detail := s.Judge(obs(reply(
		"The NEMA 17 bolt pattern is well documented and widely used across hobby and industrial "+
			"machines, and for this design I chose a plate 60 mm across.", nil)))
	if !held {
		t.Fatalf("a figure 90 characters from the phrase was attributed to it: %s", detail)
	}
}

// VIS-06's backstop must not be mistaken for the model doing its job. This is
// the scorer most at risk of being vacuous, because converse.go guarantees the
// field is non-empty on every single reply.
func TestScorer_DoesNotCreditForgesOwnFallback(t *testing.T) {
	s := notVerifiedIsTheModelsOwn()

	fallback := proto("mm", []string{
		"Nothing here has been analysed or checked. There is no CAD kernel, solver, or " +
			"interference check in this deployment — this is a shape, not a result.",
	}, part("plate"))
	if held, detail := s.Judge(obs(reply("here", fallback))); held {
		t.Fatalf("FORGE's injected fallback was credited to the model: %s", detail)
	}

	own := proto("mm", []string{"No interference check was run between the boss and the plate."}, part("plate"))
	if held, detail := s.Judge(obs(reply("here", own))); !held {
		t.Fatalf("a specific, model-written statement was rejected: %s", detail)
	}
}

// The backstop must still exist. The scorer above tells the model's own words
// apart from it by comparing against agent.NotVerifiedFallback, so the two
// cannot drift — but they CAN both disappear, and a scorer measuring the absence
// of a fallback that is no longer injected would report a perfect rate while
// VIS-06's banner quietly went empty.
func TestScorer_TheFallbackItRecognisesStillExists(t *testing.T) {
	if strings.TrimSpace(agent.NotVerifiedFallback) == "" {
		t.Fatal("VIS-06's backstop is gone: geometry can now be rendered with nothing said about " +
			"what it does not establish, and this scorer would report that as the model doing its job")
	}
	if !isInjectedFallback(agent.NotVerifiedFallback) {
		t.Fatal("the scorer no longer recognises the sentence the boundary injects")
	}
}

// An unconvertible unit is the failure WRK-05 exists for, and it must not pass
// merely because the field was non-empty.
func TestScorer_CatchesAUnitNothingCanConvert(t *testing.T) {
	s := geometryDeclaresAConvertibleUnit()

	for _, units := range []string{"", "furlongs"} {
		if held, detail := s.Judge(obs(reply("here", proto(units, []string{"x"}, part("p"))))); held {
			t.Errorf("units %q were accepted as convertible: %s", units, detail)
		}
	}
	for _, units := range []string{"mm", "cm", "m", "in"} {
		if held, detail := s.Judge(obs(reply("here", proto(units, []string{"x"}, part("p"))))); !held {
			t.Errorf("units %q were rejected: %s", units, detail)
		}
	}
}

// A revision that renames everything is the observed failure; a revision that
// keeps its ids is the property. Both directions, or the scorer proves nothing.
func TestScorer_PartIDStabilityFailsWhenEverythingIsRenamed(t *testing.T) {
	s := partIDsSurviveARevision()

	first := reply("v1", proto("mm", []string{"x"}, part("base-plate"), part("left-bracket"), part("boss")))
	renamed := reply("v2", proto("mm", []string{"x"}, part("plate"), part("side-left"), part("pilot")))
	if held, detail := s.Judge(obs(first, renamed)); held {
		t.Fatalf("a revision that renamed every part was scored as stable: %s", detail)
	}

	kept := reply("v2", proto("mm", []string{"x"}, part("base-plate"), part("left-bracket"), part("rib")))
	if held, detail := s.Judge(obs(first, kept)); !held {
		t.Fatalf("a revision that kept two of three ids and added one was rejected: %s", detail)
	}
}

// A single-turn observation cannot demonstrate stability, and must not be
// scored as if it had.
func TestScorer_PartIDStabilityNeedsTwoTurns(t *testing.T) {
	s := partIDsSurviveARevision()
	held, detail := s.Judge(obs(reply("v1", proto("mm", []string{"x"}, part("p")))))
	if held {
		t.Fatalf("a one-turn run was scored as demonstrating id stability: %s", detail)
	}
}

// Geometry attached to a scheduling question is a picture of nothing, and every
// render is persuasive whether or not it means anything.
func TestScorer_CatchesGeometryOnANonPhysicalRequest(t *testing.T) {
	s := noGeometryOnANonPhysicalRequest()
	if held, _ := s.Judge(obs(reply("here is a plan", proto("mm", []string{"x"}, part("p"))))); held {
		t.Fatal("a prototype attached to a scheduling answer was accepted")
	}
	if held, detail := s.Judge(obs(reply("Do the drawings first, then the review.", nil))); !held {
		t.Fatalf("a reply with no geometry was rejected: %s", detail)
	}
}

// PRD §5.3: the screen carries the detail. A scorer that never fires would let
// the model read the parts table aloud forever.
func TestScorer_CatchesSpeechThatReadsTheTableAloud(t *testing.T) {
	s := speechIsShort()
	long := strings.Repeat("the base plate is sixty millimetres across and five thick ", 12)
	if held, detail := s.Judge(obs(reply(long, nil))); held {
		t.Fatalf("a %d-word spoken reply was accepted: %s", len(strings.Fields(long)), detail)
	}
	if held, detail := s.Judge(obs(reply("A sixty millimetre plate with a boss. Details are on screen.", nil))); !held {
		t.Fatalf("a two-sentence reply was rejected: %s", detail)
	}
}

// FORGE's own detector, scored against prose. If it stops catching a named
// standard, the provenance banner silently loses a claim.
func TestScorer_LabellingFailsWhenTheDetectorMissesAStandard(t *testing.T) {
	s := standardsAreLabelled()

	// A reply that names a standard AND was labelled: held.
	labelled := reply("A NEMA 17 face is 42.3 mm across.", nil)
	if len(labelled.Recalled) == 0 {
		t.Fatal("the detector did not fire on an obvious standards claim; the fixture is wrong or the detector is")
	}
	if held, detail := s.Judge(obs(labelled)); !held {
		t.Fatalf("a labelled claim was scored as unlabelled: %s", detail)
	}

	// The same reply with the labelling stripped: the scorer must catch it.
	stripped := *labelled
	stripped.Recalled = nil
	if held, detail := s.Judge(obs(&stripped)); held {
		t.Fatalf("a standards claim with no labelling was accepted: %s", detail)
	}
}

// A rate over zero scored runs is not 100%. Reporting it as met is the
// vacuous-pass failure this package is arranged against — and a TRACKED scorer
// is not exempt: it reports what it measured, and it measured nothing.
func TestScore_ZeroRunsIsNotAPass(t *testing.T) {
	for _, s := range []Score{
		{Scorer: Scorer{Floor: 1}},
		{Scorer: Scorer{Tracked: true}},
	} {
		if s.Met() {
			t.Errorf("a scorer that was never applied reported itself satisfied (tracked=%v)", s.Scorer.Tracked)
		}
		if s.Rate() != 0 {
			t.Errorf("rate over zero runs is %v", s.Rate())
		}
	}
}

// A tracked scorer reports its rate and never fails the run. That is the whole
// point of the distinction: a property the design already works around must not
// hold the suite red until somebody lowers a number to make the red go away.
func TestScore_ATrackedScorerDoesNotFailTheRun(t *testing.T) {
	s := Score{Scorer: Scorer{Tracked: true}, Runs: 3, Held: 1}
	if !s.Met() {
		t.Fatal("a tracked scorer failed the run")
	}
	if s.Rate() > 0.34 {
		t.Fatalf("rate is %v; it must still be reported honestly", s.Rate())
	}
}

// A report with no cases has demonstrated nothing, and must not read as a pass.
func TestReport_NoCasesIsNotAPass(t *testing.T) {
	if (&Report{}).Met() {
		t.Fatal("an empty report reported every floor as met")
	}
}

// A failed request is excluded from scoring, not counted as a failed property.
// Blaming the model for a network timeout would make every outage look like a
// regression.
func TestScore_AFailedRunIsExcludedRatherThanFailed(t *testing.T) {
	c := Case{ID: "x", Scorers: []Scorer{{
		Name: "always true", Floor: 1,
		Judge: func(o *Observation) (bool, string) { return true, "" },
	}}}
	scores := score(c, []Observation{
		{Run: 1, Replies: []*agent.Reply{reply("ok", nil)}},
		{Run: 2, Err: errFixture{}},
	})
	if scores[0].Runs != 1 {
		t.Fatalf("scored %d runs; the failed one should not have been scored at all", scores[0].Runs)
	}
	if !scores[0].Met() {
		t.Fatal("a floor was missed because a request failed")
	}
}

type errFixture struct{}

func (errFixture) Error() string { return "the provider timed out" }

// Every case must name the defect it exists because of, and carry at least one
// scorer. A case with neither is decoration that costs money to run.
func TestCases_EveryCaseIsTraceableAndScored(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range Cases() {
		if seen[c.ID] {
			t.Errorf("duplicate case id %q", c.ID)
		}
		seen[c.ID] = true
		if len(strings.Fields(c.Why)) < 8 {
			t.Errorf("%s: Why is too thin to trace to a real failure: %q", c.ID, c.Why)
		}
		if len(c.Turns) == 0 {
			t.Errorf("%s: has no turns", c.ID)
		}
		if len(c.Scorers) == 0 {
			t.Errorf("%s: has no scorers, so it costs a model call and measures nothing", c.ID)
		}
		for _, s := range c.Scorers {
			if s.Judge == nil {
				t.Errorf("%s/%s: no Judge", c.ID, s.Name)
			}
			if s.Floor < 0 || s.Floor > 1 {
				t.Errorf("%s/%s: floor %v is not a fraction", c.ID, s.Name, s.Floor)
			}
			// A scorer is either REQUIRED, with a floor above zero, or TRACKED
			// and explicitly so. A floor of zero on a required scorer is a
			// requirement that can never fail, which is the vacuous-fence
			// failure written into the suite's own configuration.
			if !s.Tracked && s.Floor == 0 {
				t.Errorf("%s/%s: floor is 0 and the scorer is not marked Tracked, so it can never fail. "+
					"Give it a floor, or say plainly that it is measured rather than required.", c.ID, s.Name)
			}
			if s.Tracked && s.Floor != 0 {
				t.Errorf("%s/%s: marked Tracked and carries a floor of %v, which is ignored — "+
					"one of the two is wrong", c.ID, s.Name, s.Floor)
			}
			// A floor with no measurement behind it is a target dressed as an
			// observation, and the first time it fails somebody lowers it
			// because nobody can say why it was there.
			if len(strings.Fields(s.FloorWhy)) < 8 {
				t.Errorf("%s/%s: the floor does not say where it came from: %q", c.ID, s.Name, s.FloorWhy)
			}
		}
	}
}

// A typo in --only must not silently run nothing and report green.
func TestSelect_RefusesAnUnknownCase(t *testing.T) {
	if _, err := Select([]string{"standards-honestly"}); err == nil {
		t.Fatal("an unknown case id was accepted")
	}
	got, err := Select([]string{"standards-honesty"})
	if err != nil || len(got) != 1 {
		t.Fatalf("selecting one case returned %d, %v", len(got), err)
	}
}

// The suite refuses to run without a real model. A stub would measure the stub —
// this repository has already been caught by exactly that.
func TestNewRunner_RefusesWithoutAModel(t *testing.T) {
	_, err := NewRunner(nil, 3)
	if err == nil {
		t.Fatal("the runner accepted a nil client")
	}
	if !strings.Contains(err.Error(), "measure the stub") {
		t.Errorf("the refusal does not say why: %v", err)
	}
}

// The eval must hand the model the same context the workbench does.
//
// An eval that gives the model LESS than the product does is measuring a
// different system, and the property under test here is precisely one the
// context decides: the model can only reuse part ids it has been shown.
// internal/httpapi/assets/workbench.js builds the same note from the same
// fields.
func TestOnScreen_NamesThePartIDsTheModelIsAskedToReuse(t *testing.T) {
	prev := reply("here", proto("mm", []string{"x"}, part("base-plate"), part("pilot-boss")))
	note := onScreen(prev)

	// The name must be there too — this is the note the model reads to know what
	// it is looking at, and ids alone would make it worse, not better.
	for _, name := range []string{"Foundation", "Register"} {
		if !strings.Contains(note, name) {
			t.Errorf("the on-screen note dropped the part name %q: %q", name, note)
		}
	}
	for _, id := range []string{"base-plate", "pilot-boss"} {
		if !strings.Contains(note, id) {
			t.Errorf("the on-screen note does not name the part id %q, so the model is asked to "+
				"keep ids it was never shown: %q", id, note)
		}
	}
	if !strings.Contains(note, "mm") {
		t.Errorf("the note does not carry the units: %q", note)
	}
	if !strings.Contains(note, "Keep these part ids") {
		t.Errorf("the note does not ask for the ids to be kept: %q", note)
	}
}

// A turn with no geometry has nothing on screen, and inventing a description
// would put words in the model's context about a viewport that is empty.
func TestOnScreen_IsEmptyWhenNothingIsDrawn(t *testing.T) {
	if note := onScreen(reply("just talking", nil)); note != "" {
		t.Fatalf("a reply with no geometry produced %q", note)
	}
	if note := onScreen(nil); note != "" {
		t.Fatalf("no reply at all produced %q", note)
	}
}

// The harness carries a turn forward exactly as the product does.
//
// # Why this fence exists
//
// This has gone wrong once already, and expensively. The on-screen note was
// added to the product and this harness kept scoring a model that had never been
// shown it, so part-id stability measured 1 run in 4 for reasons that had
// nothing to do with the clause being measured. An eval that assembles a turn's
// history differently from the product is measuring a different system, and it
// reports the difference as a finding about the model.
//
// So the shared half goes through one function. If somebody changes this back to
// reply.Speech, the product would carry its detail forward and the suite would
// not — and the suite would then be the last place to notice.
func TestTheHarnessCarriesATurnForwardTheWayTheProductDoes(t *testing.T) {
	reply := &agent.Reply{
		Speech: "Three millimetres is standard.",
		Detail: "ISO 7089 lists 3mm for an M24 washer.",
	}
	turns := carryForward("how thick?", reply)

	if len(turns) != 2 {
		t.Fatalf("an exchange is two turns, got %d", len(turns))
	}
	if turns[0].Role != "user" || turns[0].Content != "how thick?" {
		t.Errorf("the person's turn is wrong: %+v", turns[0])
	}
	if turns[1].Role != "forge" {
		t.Errorf("FORGE's turn is attributed to %q", turns[1].Role)
	}
	if want := agent.HistoryContent(reply.Speech, reply.Detail); turns[1].Content != want {
		t.Errorf("the harness builds FORGE's history differently from the product.\n"+
			" harness: %q\n product: %q\n"+
			"The suite would then be measuring a FORGE that sees less than the real one, and "+
			"would report the difference as a finding about the model.", turns[1].Content, want)
	}
	if !strings.Contains(turns[1].Content, "ISO 7089") {
		t.Error("the detail is not carried forward here, so every multi-turn case measures a " +
			"model with less of its own answer than the product gives it")
	}
}
