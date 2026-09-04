package agent

import (
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	domainpack "github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/pack"
)

// The domain pack limits what a goal may do (PRD §"Domain packs", §8.1).
//
// # What these hold
//
// `forge_projects.pack` was written by EnsureProject and read by nothing, so the
// domain a project worked in had no effect on anything. A goal created at r2
// reached r2 whether the project was software — where a merge is reviewed and
// reversible — or civil, where the equivalent act needs a licensed engineer this
// build cannot represent.
//
// grantFor is now the read. These assert the lower of the two ceilings wins, in
// both directions, because a rule that only ever narrows in the case somebody
// tested is a rule with an untested half.
//
// See docs/bugfix/2026-09-04-the-pack-was-written-and-never-read.md.

func packNamed(t *testing.T, name string) domainpack.Definition {
	t.Helper()
	d, ok := domainpack.Lookup(name)
	if !ok {
		t.Fatalf("%s is not a pack", name)
	}
	return d
}

func goalAt(tier engine.RiskTier) *engine.Goal {
	return &engine.Goal{RiskTier: tier, Autonomy: engine.AutonomySandboxExecute}
}

// A pack ceiling below the goal's tier lowers it.
func TestGrantFor_PackCeilingLowersTheGoalsTier(t *testing.T) {
	civil := packNamed(t, "civil")

	got := grantFor(goalAt(engine.RiskR2), civil, false).MaxRiskTier
	if got != engine.RiskR1 {
		t.Errorf("an r2 goal in a civil project was granted %s; expected r1, the pack's ceiling.\n"+
			"Consequential work in a licensed domain is exactly what this build cannot "+
			"stand behind: it implements no qualified review and can name no licensed "+
			"engineer", got)
	}
}

// And a pack ceiling ABOVE the goal's tier does not raise it.
//
// The half that is easy to get wrong. A project must never widen what a goal may
// do, or lowering a goal's tier would stop being a way to narrow it.
func TestGrantFor_PackCannotRaiseTheGoalsTier(t *testing.T) {
	software := packNamed(t, "software") // ceiling r2

	got := grantFor(goalAt(engine.RiskR0), software, false).MaxRiskTier
	if got != engine.RiskR0 {
		t.Errorf("an r0 goal in a software project was granted %s; a pack may only ever "+
			"narrow what a goal permits", got)
	}
}

// Equal ceilings leave the tier alone.
func TestGrantFor_EqualCeilingsAreUnchanged(t *testing.T) {
	if got := grantFor(goalAt(engine.RiskR1), packNamed(t, "architecture"), false).MaxRiskTier; got != engine.RiskR1 {
		t.Errorf("an r1 goal in an r1 pack was granted %s", got)
	}
}

// Every industry the product offers permits R1 work through a real grant.
//
// The pack-level fence proves the TABLE permits it. This proves the thing that
// actually decides what a tool may do agrees — the two could disagree, and the
// grant is the one that runs.
func TestGrantFor_EveryIndustryStillPermitsDraftWork(t *testing.T) {
	for _, label := range []string{
		"Mechanical engineering", "Manufacturing", "Automotive", "Aerospace",
		"Civil engineering", "Electrical engineering", "Construction",
		"Product design", "Architecture", "Other",
	} {
		d := packNamed(t, label)
		grant := grantFor(goalAt(engine.RiskR1), d, false)
		if grant.MaxRiskTier != engine.RiskR1 {
			t.Errorf("%s: an r1 goal was granted %s, so reversible draft work — concept "+
				"geometry, a render, a revision — cannot happen in an industry this "+
				"product offers", label, grant.MaxRiskTier)
		}
	}
}

// A zero Definition falls back to the goal's own ceiling rather than to none.
//
// Reached only by a caller holding no pack at all. Widening to "unlimited" on an
// absent limit is the worst available reading of it.
func TestGrantFor_AnAbsentPackDoesNotWidenAnything(t *testing.T) {
	got := grantFor(goalAt(engine.RiskR1), domainpack.Definition{}, false).MaxRiskTier
	if got != engine.RiskR1 {
		t.Errorf("with no pack the grant was %s; expected the goal's own r1", got)
	}
	if lowerTier(engine.RiskR1, "") != engine.RiskR1 {
		t.Error("an invalid pack ceiling did not fall back to the goal's tier")
	}
}
