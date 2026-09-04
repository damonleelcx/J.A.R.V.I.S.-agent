package agent

import (
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	domainpack "github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/pack"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/tools"
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

// A domain ceiling refusal names the authority that would raise it.
//
// # Why this is a fence
//
// The refusal used to read "this tool is tier r2, above this goal's ceiling of
// r1" for BOTH cases: a goal whose own tier the person can change in a second,
// and the rule set of a licensed domain they cannot change at all. Identical
// text, opposite remedies. A limit with no path is a dead end, and the pack
// table already states the path — it just never reached the place work is
// actually stopped.
func TestGrantFor_ADomainCeilingNamesItsAuthority(t *testing.T) {
	civil := packNamed(t, "civil")
	grant := grantFor(goalAt(engine.RiskR2), civil, false)

	if grant.CeilingSource == "" {
		t.Fatal("an r2 goal held to r1 by the civil pack carries no explanation.\n" +
			"The person is told they hit a ceiling and not that it belongs to a licensed " +
			"domain, so they will go looking for a setting to change")
	}
	for _, want := range []string{"civil", "licensed engineer"} {
		if !strings.Contains(grant.CeilingSource, want) {
			t.Errorf("the explanation does not mention %q: %q", want, grant.CeilingSource)
		}
	}
	// And it reaches the refusal the model and the person actually read.
	ok, reason := grant.Permits(tools.Contract{
		Name: "release-drawings", RiskTier: engine.RiskR2,
		Capabilities: []tools.Capability{tools.CapWrite}, Reversibility: tools.ReversibleManual,
	})
	if ok {
		t.Fatal("an r2 tool was permitted under an r1 domain ceiling")
	}
	if !strings.Contains(reason, "licensed engineer") {
		t.Errorf("the refusal does not carry the authority: %q", reason)
	}
}

// When the GOAL's own tier is the binding limit, no domain is named.
//
// The half that keeps the message honest. Naming the pack when the pack was not
// what stopped the work sends somebody to change an industry that was never in
// their way — and teaches them the explanation is noise.
func TestGrantFor_AGoalCeilingNamesNoDomain(t *testing.T) {
	// software permits r2; the goal is the thing holding this to r0.
	grant := grantFor(goalAt(engine.RiskR0), packNamed(t, "software"), false)
	if grant.CeilingSource != "" {
		t.Errorf("the goal's own ceiling was attributed to the domain: %q", grant.CeilingSource)
	}
	// Equal ceilings are the goal's too: nothing was taken away by the domain.
	equal := grantFor(goalAt(engine.RiskR1), packNamed(t, "architecture"), false)
	if equal.CeilingSource != "" {
		t.Errorf("a domain that removed nothing was named as the limit: %q", equal.CeilingSource)
	}
}
