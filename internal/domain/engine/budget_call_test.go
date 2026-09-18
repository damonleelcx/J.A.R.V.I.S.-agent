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

	// Nothing known yet: the goal's first call reserves nothing. A ceiling already
	// reached is said exactly as it always was.
	if b := g.CheckCall(goal(0, 0), now, 0); b != nil {
		t.Errorf("a goal's first call was refused: %+v", b)
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
