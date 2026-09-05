package eval

import (
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/pack"
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

	// The CONSTANT, not a copy of it. A literal here is the drift the constant's
	// own comment warns about, and it drifted: wave 14 reworded the sentence
	// (a CAD kernel can now be configured, so "there is no CAD kernel" stopped
	// being true of every deployment) and this fixture went on asserting that
	// the scorer rejects a sentence nothing injects any more. It went red, which
	// is the fence working — and the fix is to stop having two copies.
	fallback := proto("mm", []string{agent.NotVerifiedFallback}, part("plate"))
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

// Industry coverage (2026-09-04).
//
// The suite now carries two kinds of case, and the value of that distinction is
// entirely in it being enforced. A coverage case that drifted into claiming a
// regression would put an unmeasured property behind a rule that says every case
// traces to an observed defect — which is how the rule stops being true.

// theIndustriesTheProductOffers is the selector's list, written out.
//
// Hardcoded rather than read from pack.Industries(): a fence that enumerates
// what it checks cannot fail, because deleting the industry deletes its
// assertion in the same motion.
var theIndustriesTheProductOffers = []string{
	"Mechanical engineering", "Manufacturing", "Automotive", "Aerospace",
	"Civil engineering", "Electrical engineering", "Construction",
	"Product design", "Architecture", "Other",
}

// Every industry the product offers has a case measuring it.
//
// The whole point of the coverage kind. A selector entry with no case behind it
// is a claim nobody checks — and the industries were added precisely because
// nothing could be asked about them.
func TestEveryIndustryOfferedHasACoverageCase(t *testing.T) {
	covered := map[string]string{}
	for _, c := range Cases() {
		if c.Kind == KindCoverage {
			covered[c.Industry] = c.ID
		}
	}
	for _, industry := range theIndustriesTheProductOffers {
		if covered[industry] == "" {
			t.Errorf("the product offers %q in its industry selector and no case measures whether "+
				"FORGE can work in it.\n"+
				"A dropdown entry with nothing behind it is a claim nobody checks", industry)
		}
	}
	if len(covered) != len(theIndustriesTheProductOffers) {
		t.Errorf("%d industries have coverage cases and the selector offers %d; one has been "+
			"changed without the other", len(covered), len(theIndustriesTheProductOffers))
	}
}

// Every coverage case names an industry that actually resolves to a pack.
//
// Without this the runner falls back to answering with no domain at all, and the
// case would quietly measure the default while its id claimed an industry.
func TestEveryCoverageCaseNamesARealIndustry(t *testing.T) {
	for _, c := range Cases() {
		if c.Kind != KindCoverage {
			continue
		}
		if c.Industry == "" {
			t.Errorf("%s is a coverage case naming no industry, so it is answered with no domain "+
				"and measures the default", c.ID)
			continue
		}
		if _, ok := pack.Lookup(c.Industry); !ok {
			t.Errorf("%s names the industry %q, which resolves to no pack. The case would run "+
				"with no domain conventions while claiming to measure that industry",
				c.ID, c.Industry)
		}
	}
}

// Every case declares which kind it is, and regression cases name no industry.
//
// The second half matters: a regression case pinned to a domain would be scored
// under framing the original defect never had, so a fix could be masked by
// vocabulary the model was handed.
func TestEveryCaseDeclaresItsKind(t *testing.T) {
	for _, c := range Cases() {
		switch c.Kind {
		case KindRegression:
			if c.Industry != "" {
				t.Errorf("%s is a regression case pinned to the %q domain. The defect it traces "+
					"to happened without that framing, so scoring it with the framing measures "+
					"a different system", c.ID, c.Industry)
			}
		case KindCoverage:
			// Checked above.
		case KindCapability:
			// A capability case measures whether a model reaches for a shipped
			// vocabulary, which has nothing to do with a domain — and pinning
			// one to an industry would hand the model conventions the question
			// is not about.
			if c.Industry != "" {
				t.Errorf("%s is a capability case pinned to the %q domain. Whether a model "+
					"reaches for a sweep is not a question about an industry, and answering "+
					"it under one measures something else", c.ID, c.Industry)
			}
		default:
			t.Errorf("%s declares no kind. A case is either a regression — traceable to an "+
				"observed defect — coverage of an industry the product offers, or a "+
				"capability this build shipped, and which one decides how its result "+
				"should be read", c.ID)
		}
	}
}

// Coverage scorers are Tracked, never floored.
//
// A floor needs a measurement behind it, and none of these has one: no industry
// case has ever run. A floor invented here would be a target dressed as an
// observation, and the first time it failed somebody would lower it.
func TestCoverageScorersAreTrackedNotFloored(t *testing.T) {
	for _, c := range Cases() {
		if c.Kind != KindCoverage {
			continue
		}
		for _, s := range c.Scorers {
			if s.Tracked {
				continue
			}
			// The honesty scorers a coverage case shares with the regression
			// suite keep their own floors — they are measured properties, and
			// suspending them in a new domain is exactly what must not happen.
			if s.Floor > 0 && isSharedHonestyScorer(s.Name) {
				continue
			}
			t.Errorf("%s/%s: a coverage scorer carries floor %v with no measurement behind it",
				c.ID, s.Name, s.Floor)
		}
	}
}

// isSharedHonestyScorer names the floored scorers a coverage or capability case
// legitimately carries: the rules that apply to any physical proposal, in any
// domain and whatever shape it was drawn as.
//
// outlinesResolveIntoShapes is deliberately NOT here. It measures something a
// capability case badly wants — that what the model drew can be built — and it
// is tracked rather than floored, because three of its four observed refusals
// were FORGE refusing a drawing that has exactly one reading. Flooring it would
// measure whether that decision has been taken. See the scorer.
func isSharedHonestyScorer(name string) bool {
	for _, s := range []Scorer{standardsAreLabelled(), speechIsShort()} {
		if s.Name == name {
			return true
		}
	}
	return false
}

// Capability scorers are Tracked, except the shared requirements.
//
// The rate a capability case reports — did the model reach for a sweep — must
// never carry a floor. The design does not depend on it, the alternative is
// sometimes the right answer, and a floor would sit red until somebody lowered
// it to make the red go away. What the case DOES require is the same thing every
// physical proposal requires: that what was drawn can be built.
func TestCapabilityRatesAreTrackedAndOnlyTheRequirementsAreFloored(t *testing.T) {
	seen := 0
	for _, c := range Cases() {
		if c.Kind != KindCapability {
			continue
		}
		seen++
		floored := 0
		for _, s := range c.Scorers {
			if s.Tracked {
				continue
			}
			if isSharedHonestyScorer(s.Name) {
				floored++
				continue
			}
			t.Errorf("%s/%s: a capability rate carries floor %v. Whether a model reaches for "+
				"a shape is an observation, not a requirement — a floor here demands a "+
				"vocabulary rather than a shape, and is the first number somebody lowers",
				c.ID, s.Name, s.Floor)
		}
		if floored == 0 {
			t.Errorf("%s: every scorer is tracked, so the case can never fail. Whatever the "+
				"model drew still has to be readable", c.ID)
		}
	}
	if seen == 0 {
		t.Error("no capability cases at all: the drawing vocabulary shipped and nothing " +
			"measures whether a model reaches for it")
	}
}

// ---------------------------------------------------------------------------
// fences over the drawing-vocabulary scorers
// ---------------------------------------------------------------------------

// swept builds a part the way the model would draw one, so these fixtures are
// documents FORGE can actually read rather than shapes only the test believes in.
func swept(id string, holes [][]geometry.Point, closed bool, bend float64) geometry.Part {
	p := geometry.Part{ID: id, Name: humanNameFor(id), Shape: "sweep",
		Profile:  []geometry.Point{{X: -10, Y: -10}, {X: 10, Y: -10}, {X: 10, Y: 10}, {X: -10, Y: 10}},
		Path:     []geometry.Point{{}, {Z: 100, Radius: bend}, {X: 80, Z: 100}},
		Holes:    holes,
		Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}
	if closed {
		p.Path = []geometry.Point{{}, {X: 80}, {X: 80, Y: 60}, {Y: 60}}
		p.PathClosed = true
	}
	return p
}

func bore() [][]geometry.Point {
	return [][]geometry.Point{{{X: -6, Y: -6}, {X: 6, Y: -6}, {X: 6, Y: 6}, {X: -6, Y: 6}}}
}

// The defect the readability scorer was built for: a radius on a path's END.
//
// This is the exact document qwen-plus produced on 2026-09-05 — a correct bent
// tube with an inert radius on the last point — which was a REFUSAL before wave
// 22 and cost the whole part. The scorer must accept it now, and must still
// reject a document FORGE genuinely cannot read.
func TestScorer_ReadabilityAcceptsAnInertRadiusAndRejectsAnUnreadableOutline(t *testing.T) {
	s := outlinesResolveIntoShapes()

	inert := swept("coolant-line", nil, false, 25)
	inert.Path[len(inert.Path)-1].Radius = 8 // the end: names no corner, changes nothing
	if held, detail := s.Judge(obs(reply("here", proto("mm", []string{"x"}, inert)))); !held {
		t.Errorf("a buildable bent tube was scored as unreadable because of an inert radius "+
			"on its end — the whole part would be missing from the file: %s", detail)
	}

	// A hole outside the outline it claims to be inside. FORGE refuses it, the
	// part is not in the export, and a scorer that shrugged would be measuring
	// nothing.
	broken := swept("coolant-line", [][]geometry.Point{{
		{X: 90, Y: 90}, {X: 100, Y: 90}, {X: 100, Y: 100}, {X: 90, Y: 100}}}, false, 25)
	held, detail := s.Judge(obs(reply("here", proto("mm", []string{"x"}, broken))))
	if held {
		t.Errorf("a part FORGE drops from the build was scored as readable: %s", detail)
	}
	if !strings.Contains(detail, "not inside the outline") {
		t.Errorf("the detail does not say what FORGE refused, so a reader cannot act on it: %q", detail)
	}
}

// A reply with no geometry has not demonstrated a bad outline, and must not be
// scored as if it had — the same rule every geometry scorer here follows.
func TestScorer_ReadabilityIsNotAFailureWhenNothingWasDrawn(t *testing.T) {
	held, detail := outlinesResolveIntoShapes().Judge(obs(reply("no shape here", nil)))
	if !held {
		t.Errorf("a reply with no geometry was scored as an unreadable outline: %s", detail)
	}
}

// The vocabulary rate has to distinguish the shape asked about from every other
// shape — including a document that drew plenty of geometry of the wrong kind,
// which is the observed failure it exists to count.
func TestScorer_TheVocabularyRateCountsTheShapeAndNotTheEffort(t *testing.T) {
	s := aPartIsDrawnAs("sweep", "line")

	// Three extrusions butted end to end: what qwen-plus produced for a bent
	// tube before the vocabulary was expanded. Buildable, and not a bent tube.
	butted := proto("mm", []string{"x"},
		geometry.Part{ID: "a", Shape: "extrusion", Size: map[string]float64{"depth": 300}},
		geometry.Part{ID: "b", Shape: "extrusion", Size: map[string]float64{"depth": 200}},
		geometry.Part{ID: "c", Shape: "extrusion", Size: map[string]float64{"depth": 150}})
	held, detail := s.Judge(obs(reply("here", butted)))
	if held {
		t.Fatalf("three extrusions were counted as a sweep: %s", detail)
	}
	if !strings.Contains(detail, "extrusion") {
		t.Errorf("the detail does not say what WAS drawn, so a reader cannot tell a near "+
			"miss from an empty reply: %q", detail)
	}

	if held, detail := s.Judge(obs(reply("here", proto("mm", []string{"x"},
		swept("line", nil, false, 0))))); !held {
		t.Errorf("a sweep was not counted as one: %s", detail)
	}

	// No geometry at all is a failure of this scorer and not a free pass: the
	// case asked for a part.
	if held, _ := s.Judge(obs(reply("here", nil))); held {
		t.Error("a reply with no geometry was counted as having drawn the shape")
	}
}

// A bore in the SECTION and a cylinder CUT through the part are different
// answers, and the scorer must not accept the second for the first — that is
// the whole distinction holes were added for.
func TestScorer_AVoidInTheSectionIsNotACutFeature(t *testing.T) {
	s := aSectionCarriesItsOwnVoid()

	cutInstead := proto("mm", []string{"x"}, swept("line", nil, false, 25),
		geometry.Part{ID: "drill", Shape: "cylinder", Size: map[string]float64{"radius": 6}})
	cutInstead.Features = []geometry.Feature{{ID: "bore-it", Op: "cut", Of: "line", With: []string{"drill"}}}
	held, detail := s.Judge(obs(reply("here", cutInstead)))
	if held {
		t.Fatalf("a cylinder cut through a bent tube was counted as a hollow section — the "+
			"bore would be straight through a part that turns a corner: %s", detail)
	}
	if !strings.Contains(detail, "cut") {
		t.Errorf("the detail does not mention what the model reached for instead: %q", detail)
	}

	if held, detail := s.Judge(obs(reply("here", proto("mm", []string{"x"},
		swept("line", bore(), false, 25))))); !held {
		t.Errorf("a section with a loop inside it was not counted: %s", detail)
	}
}

// A loop made of four separate bars and a loop bent from one length are
// different parts, and the scorer must tell them apart.
func TestScorer_AClosedLoopIsNotFourBars(t *testing.T) {
	s := aPathComesBackOnItself()

	fourBars := proto("mm", []string{"x"},
		swept("side-a", nil, false, 0), swept("side-b", nil, false, 0),
		swept("side-c", nil, false, 0), swept("side-d", nil, false, 0))
	held, detail := s.Judge(obs(reply("here", fourBars)))
	if held {
		t.Fatalf("four separate swept bars were counted as a closed loop: %s", detail)
	}
	if !strings.Contains(detail, "4") {
		t.Errorf("the detail does not say how many were swept, so a reader cannot see how "+
			"close it came: %q", detail)
	}

	if held, detail := s.Judge(obs(reply("here", proto("mm", []string{"x"},
		swept("handle", nil, true, 0))))); !held {
		t.Errorf("a closed path was not counted: %s", detail)
	}
}

// The radius scorer must see a radius wherever it legitimately lives — on an
// outline, on a hole, or on a path — and must not be satisfied by a drawing with
// none, however many points it has.
func TestScorer_ARadiusIsCountedWhereverItLives(t *testing.T) {
	s := aCornerCarriesARadius()

	sharp := swept("line", bore(), false, 0)
	held, detail := s.Judge(obs(reply("here", proto("mm", []string{"x"}, sharp))))
	if held {
		t.Fatalf("a drawing with no radius anywhere was counted as having one: %s", detail)
	}
	if !strings.Contains(detail, "drawn points") {
		t.Errorf("the detail does not say how much drawing it looked at: %q", detail)
	}

	// On the path: a bend radius.
	if held, detail := s.Judge(obs(reply("here", proto("mm", []string{"x"},
		swept("line", bore(), false, 25))))); !held {
		t.Errorf("a bend radius on a path was not counted: %s", detail)
	}
	// On a hole: the same field, and the same idea.
	inBore := swept("line", bore(), false, 0)
	inBore.Holes[0][2].Radius = 2
	if held, detail := s.Judge(obs(reply("here", proto("mm", []string{"x"}, inBore)))); !held {
		t.Errorf("a radius on a hole's corner was not counted: %s", detail)
	}
	// And as an EXPRESSION, which is the form the contract asks for whenever the
	// radius follows a parameter — a scorer reading only the number would report
	// the better-written document as the one that did not use the feature.
	bound := swept("line", nil, false, 0)
	bound.Profile[1].RadiusFrom = "corner_radius"
	if held, detail := s.Judge(obs(reply("here", proto("mm", []string{"x"}, bound)))); !held {
		t.Errorf("a radius written as an expression was not counted: %s", detail)
	}
}
