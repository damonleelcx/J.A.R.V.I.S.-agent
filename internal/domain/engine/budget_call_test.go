package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
)

// ‼️ A goal never places a model call its ceiling cannot pay for (2026-09-17).
//
// CheckGoal refused once the spend REACHED the ceiling, so the last call placed under
// it landed past it (docs/spikes/2026-09-17-live-verification, follow-up 4). The next
// call is assumed to cost the largest call the goal has made and a quarter more, for
// it and for every call already in flight, and is placed only if that still fits.
func TestCheckCall_AGoalNeverPlacesACallItsCeilingCannotPay(t *testing.T) {
	g := NewBudgetGuard(config.EngineConfig{MaxTokensPerGoal: 1_000_000})
	ceiling := int64(60_000)
	goal := func(spent, largest int64) *Goal {
		gl := &Goal{}
		gl.Budget.MaxTokens = &ceiling
		gl.Spend.Tokens, gl.Spend.LargestCall = spent, largest
		return gl
	}
	now := time.Now()

	// 40,000 + 17,500 fits; 45,000 + 17,500 does not, though 15,000 are left and the
	// old rule (45,000 < 60,000) would have placed it and landed at ~59,000–62,500.
	if b := g.CheckCall(goal(40_000, 14_000), now, 0); b != nil {
		t.Errorf("a call that fits was refused: %+v", b)
	}
	b := g.CheckCall(goal(45_000, 14_000), now, 0)
	if b == nil {
		t.Fatal("a call that may cost 17,500 was placed with 15,000 left")
	}
	if b.Kind != LimitTokens {
		t.Errorf("refused on %s, want tokens", b.Kind)
	}
	detail := b.Error().Detail
	for _, want := range []string{"goal budget exhausted on tokens: used 45000 tokens of 60000.",
		"15000 tokens were left, and the next model call was not placed because it may cost 17500 " +
			"(the largest call this goal has made, 14000 tokens, and a quarter more), which would pass the ceiling.",
		"The goal stops here rather than spend past it.", "Raise FORGE_MAX_TOKENS_PER_GOAL"} {
		if !strings.Contains(detail, want) {
			t.Errorf("the stop does not say %q:\n%s", want, detail)
		}
	}
	if s := b.Summary(); !strings.HasPrefix(s, "Budget exhausted on tokens: used 45000 tokens of 60000. 15000 tokens were left") {
		t.Errorf("the timeline's summary reads %q", s)
	}

	// Calls in flight reserve their share: 20,000 + 2 × 17,500 fits, + 3 × does not.
	if b := g.CheckCall(goal(20_000, 14_000), now, 1); b != nil {
		t.Errorf("a call with one other in flight was refused with room for both: %+v", b)
	}
	if b := g.CheckCall(goal(20_000, 14_000), now, 2); b == nil || !strings.Contains(b.Why, "2 call(s) already in flight") {
		t.Errorf("a call with two others in flight was placed, or not said so: %+v", b)
	}

	// Nothing known yet: the goal's first call reserves the documented default,
	// which a 60,000 ceiling pays for easily. TestCheckCall_AGoalsFirstCallReserves-
	// TheDocumentedDefault below is what holds that case. A ceiling already reached
	// is said exactly as it always was.
	if b := g.CheckCall(goal(0, 0), now, 0); b != nil {
		t.Errorf("a goal's first call was refused under a ceiling that pays for it: %+v", b)
	}
	if b := g.CheckCall(goal(60_000, 14_000), now, 0); b == nil || b.Why != "" || b.Used != "60000 tokens" {
		t.Errorf("a reached ceiling reads %+v", b)
	}
	// And a goal with no ceiling never reserves.
	open := NewBudgetGuard(config.EngineConfig{})
	if b := open.CheckCall(&Goal{Spend: Spend{Tokens: 1 << 40, LargestCall: 1 << 30}}, now, 3); b != nil {
		t.Errorf("a goal with no ceiling was refused: %+v", b)
	}
}

// ‼️ A goal cannot pass its ceiling on its very first call (2026-09-20).
//
// PR 151 reserved the largest call this goal has made and a quarter more, which
// is nothing when there is no largest call yet: the plan — every goal's first
// call — was placed whatever the ceiling said, and spent past it before anything
// could stop it. Its own PR body recorded the gap. The first call now reserves
// engine.FirstCallReserve, and the refusal says the reserve came from the
// default rather than from a measured call, because a goal stopped before it
// started has nothing to narrow and only needs a bigger ceiling.
func TestCheckCall_AGoalsFirstCallReservesTheDocumentedDefault(t *testing.T) {
	g := NewBudgetGuard(config.EngineConfig{MaxTokensPerGoal: 1_000_000})
	now := time.Now()
	goal := func(ceiling, spent, largest int64) *Goal {
		gl := &Goal{}
		gl.Budget.MaxTokens = &ceiling
		gl.Spend.Tokens, gl.Spend.LargestCall = spent, largest
		return gl
	}

	// The rule itself: nothing known is the default, not zero.
	if got := CallReserve(0); got != FirstCallReserve {
		t.Errorf("a goal with no call behind it reserves %d, want %d", got, FirstCallReserve)
	}
	if FirstCallReserve <= 0 {
		t.Fatal("the first-call reserve is not positive, so the first call reserves nothing again")
	}

	// A ceiling the first call cannot fit inside stops the goal BEFORE the call.
	small := int64(350)
	b := g.CheckCall(goal(small, 0, 0), now, 0)
	if b == nil {
		t.Fatalf("a goal with a %d-token ceiling placed its first call, which may cost %d", small, FirstCallReserve)
	}
	if b.Kind != LimitTokens {
		t.Errorf("refused on %s, want tokens", b.Kind)
	}
	detail := b.Error().Detail
	for _, want := range []string{
		"350 tokens were left, and the next model call was not placed because it may cost 12000",
		"the documented default for a goal's first call, because this goal has not made one yet",
		"The goal stops here rather than spend past it.",
		"Raise FORGE_MAX_TOKENS_PER_GOAL",
	} {
		if !strings.Contains(detail, want) {
			t.Errorf("the stop does not say %q:\n%s", want, detail)
		}
	}
	// It must NOT read as a measured call: "the largest call this goal has made,
	// 0 tokens" is the sentence the old arithmetic would have produced.
	if strings.Contains(detail, "largest call this goal has made") {
		t.Errorf("the first-call stop is reported as if a call had been measured:\n%s", detail)
	}

	// A ceiling that pays for it places it, and calls in flight still reserve
	// their share of the default.
	if b := g.CheckCall(goal(FirstCallReserve, 0, 0), now, 0); b != nil {
		t.Errorf("a ceiling exactly the size of the reserve refused the first call: %+v", b)
	}
	if b := g.CheckCall(goal(2*FirstCallReserve, 0, 0), now, 1); b != nil {
		t.Errorf("two first calls reserving %d each were refused under %d: %+v", FirstCallReserve, 2*FirstCallReserve, b)
	}
	if b := g.CheckCall(goal(2*FirstCallReserve, 0, 0), now, 2); b == nil {
		t.Error("three calls reserving the default each were placed under a ceiling for two")
	}

	// Once a call HAS been measured the goal is back on its own numbers and the
	// default plays no further part: a 100-token call reserves 125, which the
	// same 350 ceiling that refused the first call pays for without trouble.
	if b := g.CheckCall(goal(small, 0, 100), now, 0); b != nil {
		t.Errorf("a measured 125-token reserve was refused with %d left: %+v", small, b)
	}
	// And when a measured goal IS stopped, it is stopped on its own figure.
	stopped := g.CheckCall(goal(small, 300, 100), now, 0)
	if stopped == nil {
		t.Fatal("a 125-token reserve was placed with 50 tokens left")
	}
	if !strings.Contains(stopped.Error().Detail, "the largest call this goal has made, 100 tokens") {
		t.Errorf("a measured stop no longer names the call it measured:\n%s", stopped.Error().Detail)
	}

	// A goal with no ceiling still reserves nothing anybody can breach.
	open := NewBudgetGuard(config.EngineConfig{})
	if b := open.CheckCall(&Goal{}, now, 0); b != nil {
		t.Errorf("a first call under no ceiling was refused: %+v", b)
	}
}
