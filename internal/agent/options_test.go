package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/memory"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Materially different options, with tradeoffs, against stated criteria
// (PRD RSN-03).
//
// The checks are arithmetic over ratings, so most of this needs no database.
// What does need one is everything that makes the feature more than a printout:
// that the criteria are stored before the options, that an open choice actually
// holds a goal through the path goals start on, that choosing lands in the
// decision log, and that the chosen approach reaches the planner.

func twoCriteria() []Criterion {
	return []Criterion{
		{Key: "lead-time", Statement: "How long until something works end to end"},
		{Key: "reversibility", Statement: "How hard it is to undo once it is live"},
	}
}

func rating(criterion string, s Standing) Rating {
	return Rating{Criterion: criterion, Standing: s, Note: "because of how it is built"}
}

// tradeoff is a set that passes every check: fast but hard to undo, against slow
// but easy to undo.
func tradeoff() *OptionSet {
	return &OptionSet{
		Question: "How should the migration be run?",
		Options: []Option{
			{Key: "in-place", Title: "Migrate in place", Approach: "ALTER the live tables",
				Ratings: []Rating{rating("lead-time", StandingStrong), rating("reversibility", StandingWeak)}},
			{Key: "shadow", Title: "Shadow table", Approach: "Copy, backfill, then swap",
				Ratings: []Rating{rating("lead-time", StandingWeak), rating("reversibility", StandingStrong)}},
		},
	}
}

// The happy path passes. Worth its own test: every other test here asserts a
// refusal, and a Validate that refused everything would satisfy all of them.
func TestARealTradeoffIsAccepted(t *testing.T) {
	if err := tradeoff().Validate(twoCriteria()); err != nil {
		t.Fatalf("a genuine tradeoff was refused: %v", err)
	}
}

// One criterion cannot express a tradeoff, and that is arithmetic rather than
// taste: on a single basis for choosing, either the options tie — in which case
// they are the same option — or one of them simply wins.
func TestChoosingNeedsAtLeastTwoCriteria(t *testing.T) {
	for _, tc := range []struct {
		name     string
		criteria []Criterion
	}{
		{"none", nil},
		{"one", []Criterion{{Key: "lead-time", Statement: "how long"}}},
	} {
		if err := ValidateCriteria(tc.criteria); err == nil {
			t.Errorf("%s: accepted as a basis for choosing", tc.name)
		}
	}
	// And criteria a rating could not refer to unambiguously.
	dup := []Criterion{{Key: "cost", Statement: "money"}, {Key: "cost", Statement: "time"}}
	if err := ValidateCriteria(dup); err == nil {
		t.Error("two criteria sharing a key were accepted; a rating naming it would be ambiguous")
	}
	blank := []Criterion{{Key: "cost", Statement: ""}, {Key: "speed", Statement: "how fast"}}
	if err := ValidateCriteria(blank); err == nil {
		t.Error("a criterion that says nothing about what it measures was accepted")
	}
}

// The most useful check in the file: an option that skips a criterion is
// hiding the one it loses on, and the omission is invisible in a rendered
// comparison unless something counts.
func TestAnOptionMustAnswerForEveryCriterion(t *testing.T) {
	set := tradeoff()
	set.Options[0].Ratings = set.Options[0].Ratings[:1] // drops reversibility, where it is weak

	err := set.Validate(twoCriteria())
	if err == nil {
		t.Fatal("an option that skipped the criterion it loses on was accepted")
	}
	for _, want := range []string{"in-place", "reversibility"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
}

// "Materially different" is decided by the criteria, not by reading the prose.
// Two options that stand the same everywhere are one option written twice.
func TestTwoOptionsThatStandTheSameAreOneOption(t *testing.T) {
	set := tradeoff()
	set.Options[1].Ratings = []Rating{
		rating("lead-time", StandingStrong), rating("reversibility", StandingWeak),
	}
	set.Options[1].Title = "A completely different sounding name"
	set.Options[1].Approach = "Words that are not the same words as the other option at all"

	err := set.Validate(twoCriteria())
	if err == nil {
		t.Fatal("two options identical on every criterion were accepted as materially different.\n" +
			"They differ only in wording, which is the thing prose cannot be checked for")
	}
	if !strings.Contains(err.Error(), "no_material_choice") {
		t.Errorf("the refusal does not offer the honest way out: %v", err)
	}
}

// An option that is at least as good as every other on everything is a
// recommendation with company. The others are strawmen, or a criterion is
// missing — and either way the comparison is not one.
func TestAnOptionThatWinsOnEverythingIsNotAChoice(t *testing.T) {
	set := tradeoff()
	set.Options[1].Ratings = []Rating{
		rating("lead-time", StandingWeak), rating("reversibility", StandingWeak),
	}
	err := set.Validate(twoCriteria())
	if err == nil {
		t.Fatal("a set where one option dominates on every criterion was accepted")
	}
	if !strings.Contains(err.Error(), "in-place") {
		t.Errorf("the refusal does not name the dominant option: %v", err)
	}

	// Equal-on-one, better-on-the-other still dominates: nothing is being traded.
	set = tradeoff()
	set.Options[0].Ratings = []Rating{
		rating("lead-time", StandingStrong), rating("reversibility", StandingStrong),
	}
	set.Options[1].Ratings = []Rating{
		rating("lead-time", StandingStrong), rating("reversibility", StandingAdequate),
	}
	if err := set.Validate(twoCriteria()); err == nil {
		t.Error("an option equal on one criterion and better on the other was accepted as a tradeoff")
	}
}

// A criterion invented while rating is a criterion chosen after the options were
// known, which is the whole thing the prior-criteria rule prevents.
func TestACriterionInventedWhileArguingIsRefused(t *testing.T) {
	set := tradeoff()
	set.Options[0].Ratings = append(set.Options[0].Ratings, rating("elegance", StandingStrong))
	if err := set.Validate(twoCriteria()); err == nil {
		t.Fatal("an option rated itself on a criterion nobody stated, and it was accepted")
	}
}

// A standing with no reason is a score. Nobody can disagree with a score.
func TestAStandingWithNoReasonIsRefused(t *testing.T) {
	set := tradeoff()
	set.Options[0].Ratings[1].Note = "   "
	if err := set.Validate(twoCriteria()); err == nil {
		t.Fatal("a bare standing with no reasoning was accepted")
	}
	set = tradeoff()
	set.Options[0].Ratings[1].Standing = "excellent"
	if err := set.Validate(twoCriteria()); err == nil {
		t.Fatal("a standing outside the three levels was accepted")
	}
}

// Declining is a complete answer, and it has to be, or the checks above become
// pressure to fabricate two losers.
func TestDecliningToOfferAChoiceIsValid(t *testing.T) {
	set := &OptionSet{NoMaterialChoice: "there is one sensible way to do this"}
	if err := set.Validate(twoCriteria()); err != nil {
		t.Fatalf("an honest refusal to manufacture options was rejected: %v", err)
	}
	// And it holds nothing, at any tier: nobody was asked anything.
	hold := &optionHold{Set: &storedSet{NoMaterialChoice: "one sensible way"}}
	if err := gateOnOptions(hold, &engine.Goal{RiskTier: engine.RiskR4}); err != nil {
		t.Errorf("a goal was held on a choice FORGE explicitly declined to offer: %v", err)
	}
	// A single option is not the same thing as saying so out loud.
	single := &OptionSet{Question: "how?", Options: tradeoff().Options[:1]}
	if err := single.Validate(twoCriteria()); err == nil {
		t.Error("one option was presented as a choice")
	}
}

// Criteria the options do not differ on are reported, not refused. Refusing
// would push the model to invent a difference, which is the opposite of the
// point; leaving them unmarked lets a padded comparison look considered.
func TestCriteriaThatDidNotSeparateAnythingAreNamed(t *testing.T) {
	criteria := append(twoCriteria(), Criterion{Key: "safety", Statement: "who can get hurt"})
	set := tradeoff()
	for i := range set.Options {
		set.Options[i].Ratings = append(set.Options[i].Ratings, rating("safety", StandingAdequate))
	}
	if err := set.Validate(criteria); err != nil {
		t.Fatalf("options equal on one criterion were refused: %v", err)
	}
	separates, flat := set.Separating(criteria)
	if len(separates) != 2 || len(flat) != 1 || flat[0].Key != "safety" {
		t.Errorf("separating criteria %v, flat %v; expected safety to be the flat one", separates, flat)
	}
}

// The gate holds consequential work and lets exploration through, on the same
// boundary and for the same reason as RSN-02.
func TestAnOpenChoiceHoldsConsequentialWorkOnly(t *testing.T) {
	open := &optionHold{Set: &storedSet{
		Question: "How should the migration be run?",
		Options:  tradeoff().Options,
	}}
	for _, tc := range []struct {
		tier     engine.RiskTier
		wantHeld bool
	}{
		{engine.RiskR0, false}, {engine.RiskR1, false},
		{engine.RiskR2, true}, {engine.RiskR3, true}, {engine.RiskR4, true},
	} {
		err := gateOnOptions(open, &engine.Goal{ID: "gol_1", RiskTier: tc.tier})
		if tc.wantHeld && err == nil {
			t.Errorf("%s: consequential work started with the choice still open", tc.tier)
		}
		if !tc.wantHeld && err != nil {
			t.Errorf("%s: exploration was blocked by an open choice: %v", tc.tier, err)
		}
	}
	// The refusal has to be actionable: the question, the keys, and the command.
	err := gateOnOptions(open, &engine.Goal{ID: "gol_abc", RiskTier: engine.RiskR3})
	for _, want := range []string{"How should the migration be run?", "in-place", "shadow",
		"forgectl goal choose gol_abc"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
	// A made choice releases it, and no options at all hold nothing.
	chosen := &optionHold{Set: open.Set, Chosen: "shadow"}
	if err := gateOnOptions(chosen, &engine.Goal{RiskTier: engine.RiskR4}); err != nil {
		t.Errorf("a goal was held after somebody chose: %v", err)
	}
	if err := gateOnOptions(nil, &engine.Goal{RiskTier: engine.RiskR4}); err != nil {
		t.Errorf("a goal nobody offered anything was held: %v", err)
	}
}

// Rejections are derived from the criteria where they can be, and refuse to be
// invented where they cannot.
func TestALosingChoiceHasToBeExplainedInWords(t *testing.T) {
	set := tradeoff()
	criteria := twoCriteria()

	// Choosing the option that wins on lead-time: the criteria say why the other
	// lost, so nothing more is needed.
	alts, needsWhy := rejections(set.Options[0], set.Options, criteria, "")
	if needsWhy {
		t.Error("a choice the criteria explain was said to need words")
	}
	if len(alts) != 1 || alts[0].WhyNot == "" {
		t.Fatalf("the rejected option carries no reason: %+v", alts)
	}
	if !strings.Contains(alts[0].WhyNot, "lead-time") {
		t.Errorf("the derived reason does not name the criterion it turned on: %q", alts[0].WhyNot)
	}

	// Now a set where the chosen option is not preferred by ANY criterion —
	// somebody picked the one that loses on paper. The record cannot say why,
	// so it has to be told.
	dominated := &OptionSet{
		Question: "which?",
		Options: []Option{
			{Key: "loser", Title: "Loses", Approach: "x", Ratings: []Rating{
				rating("lead-time", StandingWeak), rating("reversibility", StandingAdequate)}},
			{Key: "winner", Title: "Wins", Approach: "y", Ratings: []Rating{
				rating("lead-time", StandingStrong), rating("reversibility", StandingAdequate)}},
		},
	}
	_, needsWhy = rejections(dominated.Options[0], dominated.Options, criteria, "")
	if !needsWhy {
		t.Error("choosing the option no criterion prefers was recorded without a word of explanation.\n" +
			"The derived reason would have been untrue: nothing in the criteria says why it won")
	}
}

// threeWayTradeoff has an option — "middling" — that no criterion prefers to
// "fast", while still passing every check: nothing dominates the set and no two
// options stand alike.
//
// It exists because a two-option set cannot produce this case. Non-dominance
// means each option beats the other somewhere, so with two options there is
// always a derivable reason. The case only appears with three, which is exactly
// why it is worth a fixture rather than a mental note.
func threeWayTradeoff() *OptionSet {
	return &OptionSet{
		Question: "How should the migration be run?",
		Options: []Option{
			{Key: "fast", Title: "In place", Approach: "ALTER the live tables", Ratings: []Rating{
				rating("lead-time", StandingStrong), rating("reversibility", StandingWeak)}},
			{Key: "safe", Title: "Shadow table", Approach: "Copy, backfill, swap", Ratings: []Rating{
				rating("lead-time", StandingWeak), rating("reversibility", StandingStrong)}},
			{Key: "middling", Title: "Batched in place", Approach: "ALTER in small batches",
				Ratings: []Rating{
					rating("lead-time", StandingAdequate), rating("reversibility", StandingWeak)}},
		},
	}
}

// ChooseOption itself refuses a choice the criteria cannot explain.
//
// # Why this exists on top of TestALosingChoiceHasToBeExplainedInWords
//
// That test calls rejections directly and asserts the flag it returns. A drill
// found it vacuous: disabling the refusal in ChooseOption left it green, because
// nothing in it went through the function that acts on the flag. Third time in
// this package — SAF-02, RSN-02, and now here — and the same shape every time.
func TestChoosingAnOptionNoCriterionPrefersIsRefusedWithoutWords(t *testing.T) {
	f := newRecoveryFixture(t)
	ctx := context.Background()

	goalID := f.goalLike(t, "Run the migration")
	var owner string
	if err := f.pool.QueryRow(ctx,
		`select created_by from forge_goals where id = $1`, goalID).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if err := StateCriteria(ctx, f.pool, goalID, twoCriteria()); err != nil {
		t.Fatal(err)
	}
	set := threeWayTradeoff()
	if err := set.Validate(twoCriteria()); err != nil {
		t.Fatalf("the fixture itself does not pass the checks: %v", err)
	}
	if err := StoreOptions(ctx, f.pool, goalID, set); err != nil {
		t.Fatal(err)
	}

	_, err := ChooseOption(ctx, f.pool, clock.System{}, logx.Discard(), ChooseRequest{
		GoalID: goalID, OptionKey: "middling", ByUserID: owner})
	if err == nil {
		t.Fatal("an option no stated criterion prefers was recorded with a derived reason.\n" +
			"That reason would be untrue: nothing in the criteria says why it beat `fast`")
	}
	if !strings.Contains(err.Error(), "--why") {
		t.Errorf("the refusal does not say how to satisfy it: %v", err)
	}
	// And nothing was written: a refused choice must not leave the goal settled.
	hold, err := optionsFor(ctx, f.pool, goalID)
	if err != nil {
		t.Fatal(err)
	}
	if !hold.Open() {
		t.Fatalf("a refused choice settled the goal anyway: %+v", hold)
	}

	// With the words, it goes through — and they are what the record carries.
	result, err := ChooseOption(ctx, f.pool, clock.System{}, logx.Discard(), ChooseRequest{
		GoalID: goalID, OptionKey: "middling", ByUserID: owner,
		Why: "the batches fit inside our nightly window, which no criterion knew about"})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := memory.NewService(f.pool, clock.System{}, logx.Discard()).
		FindDecision(ctx, result.DecisionID)
	if err != nil {
		t.Fatal(err)
	}
	for _, alt := range decision.Alternatives {
		if !strings.Contains(alt.WhyNot, "nightly window") {
			t.Errorf("a rejected option carries a reason that is not the real one: %q", alt.WhyNot)
		}
	}
}

// ---------------------------------------------------------------------------
// Against a real database, because the failures these prevent are all about
// what survives the process
// ---------------------------------------------------------------------------

// Criteria are stored before options exist, and restating them throws the
// options away.
//
// Keeping them would leave a comparison whose ratings refer to criteria nobody
// can read any more — and would let somebody keep the options they liked while
// changing the basis those options were judged on, which is the laundering the
// prior-criteria rule exists to prevent, one step later.
func TestRestatingCriteriaClearsTheOptionsTheyJudged(t *testing.T) {
	f := newRecoveryFixture(t)
	ctx := context.Background()

	if c, err := CriteriaFor(ctx, f.pool, f.goalID); err != nil || len(c) != 0 {
		t.Fatalf("a fresh goal already had criteria: %v (%v)", c, err)
	}
	// Options cannot be stored before criteria exist. This is the storage-level
	// half of the same rule the CLI enforces.
	if err := StoreOptions(ctx, f.pool, f.goalID, tradeoff()); err == nil {
		t.Fatal("options were stored against a goal with no stated criteria")
	}

	if err := StateCriteria(ctx, f.pool, f.goalID, twoCriteria()); err != nil {
		t.Fatal(err)
	}
	if err := StoreOptions(ctx, f.pool, f.goalID, tradeoff()); err != nil {
		t.Fatal(err)
	}
	hold, err := optionsFor(ctx, f.pool, f.goalID)
	if err != nil {
		t.Fatal(err)
	}
	if !hold.Open() {
		t.Fatalf("the stored options are not an open choice: %+v", hold)
	}

	if err := StateCriteria(ctx, f.pool, f.goalID, []Criterion{
		{Key: "cost", Statement: "money"}, {Key: "risk", Statement: "what can go wrong"},
	}); err != nil {
		t.Fatal(err)
	}
	hold, err = optionsFor(ctx, f.pool, f.goalID)
	if err != nil {
		t.Fatal(err)
	}
	if hold != nil {
		t.Fatalf("options argued against replaced criteria survived the replacement: %+v", hold)
	}
}

// Choosing lands in the decision log, releases the hold, and cannot be quietly
// done twice.
func TestChoosingRecordsADecisionAndReleasesTheGoal(t *testing.T) {
	f := newRecoveryFixture(t)
	ctx := context.Background()

	var owner, project string
	if err := f.pool.QueryRow(ctx,
		`select created_by, project_id from forge_goals where id = $1`, f.goalID).
		Scan(&owner, &project); err != nil {
		t.Fatal(err)
	}
	if err := StateCriteria(ctx, f.pool, f.goalID, twoCriteria()); err != nil {
		t.Fatal(err)
	}
	if err := StoreOptions(ctx, f.pool, f.goalID, tradeoff()); err != nil {
		t.Fatal(err)
	}

	// An option nobody offered cannot be chosen.
	if _, err := ChooseOption(ctx, f.pool, clock.System{}, logx.Discard(), ChooseRequest{
		GoalID: f.goalID, OptionKey: "something-else", ByUserID: owner}); err == nil {
		t.Error("an option that was never offered was chosen")
	}
	// And a choice has to name somebody.
	if _, err := ChooseOption(ctx, f.pool, clock.System{}, logx.Discard(), ChooseRequest{
		GoalID: f.goalID, OptionKey: "shadow"}); err == nil {
		t.Error("an unattributed choice was accepted; nobody could question it later")
	}

	result, err := ChooseOption(ctx, f.pool, clock.System{}, logx.Discard(), ChooseRequest{
		GoalID: f.goalID, OptionKey: "shadow", ByUserID: owner,
		Why: "we can absorb the extra week; we cannot absorb an unrecoverable migration",
	})
	if err != nil {
		t.Fatal(err)
	}

	// The decision log, not just the column. The column answers "is this goal
	// held"; the log is where "why is it built this way" lives once the goal is
	// long gone.
	decision, err := memory.NewService(f.pool, clock.System{}, logx.Discard()).
		FindDecision(ctx, result.DecisionID)
	if err != nil {
		t.Fatalf("the choice was made and nothing reached the decision log: %v", err)
	}
	if !strings.Contains(decision.Decision, "Shadow table") {
		t.Errorf("the decision does not say what was chosen: %q", decision.Decision)
	}
	if len(decision.Alternatives) != 1 {
		t.Fatalf("the rejected option is not recorded as an alternative: %+v", decision.Alternatives)
	}
	if decision.Alternatives[0].WhyNot == "" {
		t.Error("the rejected option carries no reason, which reads as though it was never weighed")
	}
	if !strings.Contains(decision.Rationale, "absorb the extra week") {
		t.Errorf("the chooser's own reason is not in the rationale: %q", decision.Rationale)
	}

	hold, err := optionsFor(ctx, f.pool, f.goalID)
	if err != nil {
		t.Fatal(err)
	}
	if hold.Open() || hold.Chosen != "shadow" {
		t.Fatalf("the choice did not settle the hold: %+v", hold)
	}

	// A second choice on a settled question is refused rather than silently
	// producing a second current decision about the same thing.
	if _, err := ChooseOption(ctx, f.pool, clock.System{}, logx.Discard(), ChooseRequest{
		GoalID: f.goalID, OptionKey: "in-place", ByUserID: owner}); err == nil {
		t.Error("the same question was decided twice, leaving two live answers to it")
	}

	// Re-opening the choice and choosing again SUPERSEDES the first decision, so
	// the log still has one current answer per question.
	if err := StoreOptions(ctx, f.pool, f.goalID, tradeoff()); err != nil {
		t.Fatal(err)
	}
	again, err := ChooseOption(ctx, f.pool, clock.System{}, logx.Discard(), ChooseRequest{
		GoalID: f.goalID, OptionKey: "in-place", ByUserID: owner,
		Why: "the deadline moved and we now need it this week",
	})
	if err != nil {
		t.Fatal(err)
	}
	if again.Superseded != result.DecisionID {
		t.Errorf("the second choice supersedes %q, not the first decision %q",
			again.Superseded, result.DecisionID)
	}
	first, err := memory.NewService(f.pool, clock.System{}, logx.Discard()).
		FindDecision(ctx, result.DecisionID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Current() {
		t.Error("the original decision is still current after the question was decided again.\n" +
			"Two current answers to one question is what supersession exists to prevent")
	}
}

// The gate is wired into the path a goal actually starts through.
//
// # Why this exists on top of TestAnOpenChoiceHoldsConsequentialWorkOnly
//
// That test calls gateOnOptions directly, so it passes whether or not anything
// calls it. This package has now shipped that mistake twice — SAF-02's coverage
// check and RSN-02's question-recording were both fenced by tests of the
// function rather than of the behaviour, and both times the drill that found it
// was "delete the call and see if anything goes red".
//
// So this drives Activate, the one place forgectl and the HTTP API both pass
// through, and asserts that the goal is still a draft afterwards.
func TestActivateHoldsAGoalWithAnOpenChoice(t *testing.T) {
	f := newRecoveryFixture(t)
	ctx := context.Background()
	applier := NewPlanApplier(engine.NewRepository(), engine.NewQueue(),
		engine.NewBudgetGuard(config.EngineConfig{}), clock.System{})

	start := func(t *testing.T, tier engine.RiskTier, choose string) (string, error) {
		t.Helper()
		goalID := f.goalLike(t, "Work at "+string(tier))
		if _, err := f.pool.Exec(ctx,
			`update forge_goals set risk_tier = $2 where id = $1`, goalID, string(tier)); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(ctx, `
			insert into forge_tasks (id, goal_id, plan_id, title, instruction, status, risk_tier,
				idempotency_key, created_at, updated_at)
			values ($1,$2,$3,'do the work','do it','pending','r1',$1,now(),now())`,
			id.New(id.PrefixTask), goalID, f.planID); err != nil {
			t.Fatal(err)
		}
		if err := StateCriteria(ctx, f.pool, goalID, twoCriteria()); err != nil {
			t.Fatal(err)
		}
		if err := StoreOptions(ctx, f.pool, goalID, tradeoff()); err != nil {
			t.Fatal(err)
		}
		if choose != "" {
			var owner string
			if err := f.pool.QueryRow(ctx,
				`select created_by from forge_goals where id = $1`, goalID).Scan(&owner); err != nil {
				t.Fatal(err)
			}
			if _, err := ChooseOption(ctx, f.pool, clock.System{}, logx.Discard(), ChooseRequest{
				GoalID: goalID, OptionKey: choose, ByUserID: owner}); err != nil {
				t.Fatal(err)
			}
		}
		goal := &engine.Goal{ID: goalID, Status: engine.GoalDraft, RiskTier: tier}
		return goalID, applier.Activate(ctx, f.pool, goal, engine.ActorHuman, nil)
	}

	held, err := start(t, engine.RiskR3, "")
	if err == nil {
		t.Fatal("Activate started an r3 goal with the choice still open.\n" +
			"gateOnOptions may be correct and simply not called — the mutation that left both " +
			"SAF-02 and RSN-02 fully green")
	}
	if !strings.Contains(err.Error(), "How should the migration be run?") {
		t.Errorf("the refusal does not carry the question: %v", err)
	}
	var status string
	if err := f.pool.QueryRow(ctx, `select status from forge_goals where id = $1`, held).
		Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != string(engine.GoalDraft) {
		t.Errorf("the goal is %s after a refused activation; it must not have started", status)
	}

	if _, err := start(t, engine.RiskR1, ""); err != nil {
		t.Errorf("an r1 goal was blocked by an open choice: %v\n"+
			"Exploration that stops dead on an open choice teaches people not to ask for one", err)
	}
	if _, err := start(t, engine.RiskR3, "shadow"); err != nil {
		t.Errorf("an r3 goal was still held after somebody chose: %v", err)
	}
}

// The chosen approach reaches the planner, through the path planning takes.
//
// # Why this is the fence that matters most
//
// Everything else here can be right and the feature still be a survey: if the
// planner does not see the choice, the work is planned on whichever approach the
// model prefers on this roll, and the person who chose is worse off than if they
// had never been asked — the record now says they picked something the plan then
// ignored.
//
// It goes through Intake.Plan rather than Planner.Plan, so removing the
// WithSettled wiring — not just the prompt block — turns it red.
func TestTheChosenApproachReachesThePlan(t *testing.T) {
	f := newRecoveryFixture(t)
	ctx := context.Background()

	const plan = `{"rationale":"do it","clarification_needed":"","tasks":[
		{"key":"copy","title":"copy the table","instruction":"copy it","inputs":{},
		 "expected_output":{"description":"a table"},"depends_on":[],"risk_tier":"r1"}]}`

	goalID := f.goalLike(t, "Run the migration")
	var project, owner string
	if err := f.pool.QueryRow(ctx,
		`select project_id, created_by from forge_goals where id = $1`, goalID).
		Scan(&project, &owner); err != nil {
		t.Fatal(err)
	}
	if err := StateCriteria(ctx, f.pool, goalID, twoCriteria()); err != nil {
		t.Fatal(err)
	}
	if err := StoreOptions(ctx, f.pool, goalID, tradeoff()); err != nil {
		t.Fatal(err)
	}
	if _, err := ChooseOption(ctx, f.pool, clock.System{}, logx.Discard(), ChooseRequest{
		GoalID: goalID, OptionKey: "in-place", ByUserID: owner}); err != nil {
		t.Fatal(err)
	}

	stub := &planStub{plan: plan}
	goal := &engine.Goal{ID: goalID, ProjectID: project, CreatedBy: owner,
		Status: engine.GoalDraft, RiskTier: engine.RiskR1}
	if _, err := NewIntake(stub, persona.DefaultCharacter(), config.EngineConfig{}, clock.System{}).
		Plan(ctx, f.pool, goal); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(stub.system, "ALTER the live tables") {
		t.Fatalf("the planner was never told which approach was chosen.\n"+
			"It plans whichever one this roll of the model prefers, and the record says "+
			"somebody chose something else.\nPrompt was:\n%s", stub.system)
	}
	// And the costs of the chosen option, which are the part the plan has to
	// survive rather than a reason to pick something else.
	if !strings.Contains(stub.system, "How hard it is to undo once it is live") {
		t.Errorf("the plan was told what was chosen but not what it costs:\n%s", stub.system)
	}
}

// A row naming a choice that is not in its set is reported, not absorbed.
//
// Only a hand-edited row produces this. The quiet path — planning as though
// nobody had chosen — is the one outcome the person who chose would never
// expect, so it is said out loud rather than recovered from silently.
func TestAChoiceTheSetDoesNotContainIsReported(t *testing.T) {
	hold := &optionHold{
		Set:      &storedSet{Question: "how?", Options: tradeoff().Options},
		Criteria: twoCriteria(),
		Chosen:   "an-option-that-was-never-offered",
	}
	brief, unreadable := settledChoiceBrief(hold)
	if !unreadable {
		t.Error("a choice that names nothing in the set passed as readable")
	}
	if brief != "" {
		t.Errorf("the planner was told something about an unreadable choice: %q", brief)
	}
}

// sequenceStub returns a different answer to each call, so a retry can be
// distinguished from a repeat.
type sequenceStub struct {
	answers []string
	calls   int
	// lastUser is the final user turn, which on a retry is the refusal.
	lastUser string
}

func (s *sequenceStub) Complete(_ context.Context, req llm.Request) (*llm.Response, error) {
	for _, m := range req.Messages {
		if m.Role == llm.User {
			s.lastUser = m.Content
		}
	}
	i := s.calls
	if i >= len(s.answers) {
		i = len(s.answers) - 1
	}
	s.calls++
	return &llm.Response{Content: s.answers[i], FinishReason: "stop"}, nil
}

func (s *sequenceStub) ModelFor(llm.Role) string { return "stub" }

// A refused set goes back to the model with the reason, once.
//
// Written after the first live run of `goal options` was refused for an option
// that skipped the criterion it loses on. The check was right; handing that to a
// person as an error was not, because they cannot fix a model's answer.
func TestARefusedSetIsHandedBackToTheModelWithTheReason(t *testing.T) {
	// Skips reversibility, where it would be weak — the live failure, exactly.
	const skips = `{"question":"how?","no_material_choice":"","options":[
		{"key":"fast","title":"Fast","approach":"do it directly","ratings":[
			{"criterion":"lead-time","standing":"strong","note":"one step"}]},
		{"key":"safe","title":"Safe","approach":"do it carefully","ratings":[
			{"criterion":"lead-time","standing":"weak","note":"many steps"},
			{"criterion":"reversibility","standing":"strong","note":"nothing is dropped"}]}]}`
	const fixed = `{"question":"how?","no_material_choice":"","options":[
		{"key":"fast","title":"Fast","approach":"do it directly","ratings":[
			{"criterion":"lead-time","standing":"strong","note":"one step"},
			{"criterion":"reversibility","standing":"weak","note":"the old column is dropped"}]},
		{"key":"safe","title":"Safe","approach":"do it carefully","ratings":[
			{"criterion":"lead-time","standing":"weak","note":"many steps"},
			{"criterion":"reversibility","standing":"strong","note":"nothing is dropped"}]}]}`

	goal := &engine.Goal{ID: "gol_1", ProjectID: "prj_1", Title: "Migrate", Statement: "migrate it"}

	stub := &sequenceStub{answers: []string{skips, fixed}}
	set, err := NewAdviser(stub, persona.DefaultCharacter()).Offer(context.Background(), goal, twoCriteria())
	if err != nil {
		t.Fatalf("a set that was fixable on the second try was refused outright: %v", err)
	}
	if stub.calls != 2 {
		t.Errorf("the adviser made %d call(s); a refused set was not handed back", stub.calls)
	}
	if !strings.Contains(stub.lastUser, "reversibility") {
		t.Errorf("the retry was not told what it broke, so it is just another roll of the "+
			"same dice:\n%s", stub.lastUser)
	}
	if !strings.Contains(stub.lastUser, "no_material_choice") {
		t.Error("the retry was not offered the honest way out, so the only way to satisfy the " +
			"check is to invent a difference")
	}
	if len(set.Refused) != 1 {
		t.Errorf("the set does not carry what the first attempt was refused for: %v", set.Refused)
	}

	// And it is bounded: a model that keeps failing is reported, not looped on.
	stubborn := &sequenceStub{answers: []string{skips}}
	_, err = NewAdviser(stubborn, persona.DefaultCharacter()).
		Offer(context.Background(), goal, twoCriteria())
	if err == nil {
		t.Fatal("a model that never produced a valid set was not reported")
	}
	if stubborn.calls != offerAttempts {
		t.Errorf("the adviser made %d calls; the retry is meant to be bounded at %d",
			stubborn.calls, offerAttempts)
	}
	if !strings.Contains(err.Error(), "goal criteria") {
		t.Errorf("the refusal does not say what a person can actually do next: %v", err)
	}
}
