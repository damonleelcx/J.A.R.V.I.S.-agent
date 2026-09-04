package agent

import (
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	domainpack "github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/pack"
	domainworkspace "github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
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

// noAuthority is the ordinary state: nobody has been recorded as accountable,
// so no ceiling is raised. Named rather than passed inline so every call below
// reads as "and nothing raised this".
var noAuthority = domainworkspace.ReviewAuthority{}

func goalAt(tier engine.RiskTier) *engine.Goal {
	return &engine.Goal{RiskTier: tier, Autonomy: engine.AutonomySandboxExecute}
}

// A pack ceiling below the goal's tier lowers it.
func TestGrantFor_PackCeilingLowersTheGoalsTier(t *testing.T) {
	civil := packNamed(t, "civil")

	got := grantFor(goalAt(engine.RiskR2), civil, noAuthority, false).MaxRiskTier
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

	got := grantFor(goalAt(engine.RiskR0), software, noAuthority, false).MaxRiskTier
	if got != engine.RiskR0 {
		t.Errorf("an r0 goal in a software project was granted %s; a pack may only ever "+
			"narrow what a goal permits", got)
	}
}

// Equal ceilings leave the tier alone.
func TestGrantFor_EqualCeilingsAreUnchanged(t *testing.T) {
	if got := grantFor(goalAt(engine.RiskR1), packNamed(t, "architecture"), noAuthority, false).MaxRiskTier; got != engine.RiskR1 {
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
		grant := grantFor(goalAt(engine.RiskR1), d, noAuthority, false)
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
	got := grantFor(goalAt(engine.RiskR1), domainpack.Definition{}, noAuthority, false).MaxRiskTier
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
	grant := grantFor(goalAt(engine.RiskR2), civil, noAuthority, false)

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
	grant := grantFor(goalAt(engine.RiskR0), packNamed(t, "software"), noAuthority, false)
	if grant.CeilingSource != "" {
		t.Errorf("the goal's own ceiling was attributed to the domain: %q", grant.CeilingSource)
	}
	// Equal ceilings are the goal's too: nothing was taken away by the domain.
	equal := grantFor(goalAt(engine.RiskR1), packNamed(t, "architecture"), noAuthority, false)
	if equal.CeilingSource != "" {
		t.Errorf("a domain that removed nothing was named as the limit: %q", equal.CeilingSource)
	}
}

// ---------------------------------------------------------------------------
// Qualified review: the only mechanism that raises a ceiling
// ---------------------------------------------------------------------------
//
// # Why these are the most important fences in this area
//
// Everything else here NARROWS what may happen. This one widens it, and it
// widens it on the strength of something this build cannot check: whether the
// named person actually holds the qualification. What it establishes is that
// somebody accepted responsibility, in an attributed record. What it does not
// establish is a licence.
//
// So the fences hold three things: nothing rises without a record, the raised
// state says "recorded, not verified" where a person will read it, and the two
// domains that must never rise cannot be raised by any record at all.

// anAuthority is a recorded claim. Attributed, because an unattributed one is
// the shape the database refuses and the shape that must never raise anything.
var anAuthority = domainworkspace.ReviewAuthority{
	Holder: "R. Okonkwo", Note: "CEng MICE 481920", RecordedBy: "usr_someone",
}

// A recorded authority raises the domain ceiling, and only then.
func TestGrantFor_AnAuthorityRaisesTheDomainCeiling(t *testing.T) {
	civil := packNamed(t, "civil")
	goal := goalAt(engine.RiskR2)

	if got := grantFor(goal, civil, noAuthority, false).MaxRiskTier; got != engine.RiskR1 {
		t.Fatalf("setup: with nobody recorded the ceiling should be r1, got %s", got)
	}
	got := grantFor(goal, civil, anAuthority, false).MaxRiskTier
	if got != engine.RiskR2 {
		t.Errorf("with a named authority recorded the ceiling is %s; expected r2.\n"+
			"A deployment that HAS a licensed engineer has no way to say so, which makes "+
			"the r1 limit a dead end rather than a boundary", got)
	}
}

// The raised state says, in words, that nothing was verified.
//
// Without this sentence the mechanism launders authority: the person doing r2
// work would believe a qualification had been established, and none was.
func TestGrantFor_ARaisedCeilingSaysItWasNotVerified(t *testing.T) {
	// r3 so the grant is still ABOVE the raised ceiling and the explanation is
	// produced — this is the text somebody reads at the moment they are refused.
	grant := grantFor(goalAt(engine.RiskR3), packNamed(t, "civil"), anAuthority, false)

	if grant.CeilingSource == "" {
		t.Fatal("a raised ceiling carries no explanation at all")
	}
	for _, want := range []string{"R. Okonkwo", "RECORDED, NOT VERIFIED", "cannot check"} {
		if !strings.Contains(grant.CeilingSource, want) {
			t.Errorf("the explanation does not contain %q.\n"+
				"A raised ceiling that does not say what was NOT established is a way to "+
				"launder authority nothing checked.\ngot: %s", want, grant.CeilingSource)
		}
	}
}

// An unattributed claim raises nothing.
//
// The database refuses to store one, and this holds the same rule in the code
// that reads it: a ceiling resting on a value with no author is a ceiling
// resting on nobody.
func TestGrantFor_AnUnattributedClaimRaisesNothing(t *testing.T) {
	civil := packNamed(t, "civil")
	for _, a := range []domainworkspace.ReviewAuthority{
		{Holder: "R. Okonkwo"},                     // nobody recorded it
		{RecordedBy: "usr_someone"},                // nobody named
		{Holder: "   ", RecordedBy: "usr_someone"}, // named with whitespace
	} {
		if got := grantFor(goalAt(engine.RiskR2), civil, a, false).MaxRiskTier; got != engine.RiskR1 {
			t.Errorf("%+v raised the ceiling to %s", a, got)
		}
	}
}

// No record raises a domain that offers no raised ceiling.
//
// `general` is the unknown domain — "who is qualified here" has no meaning — and
// medical and robotics permit no work at any tier. A record on any of them must
// be inert, whatever it claims.
func TestGrantFor_NoAuthorityRaisesADomainThatOffersNone(t *testing.T) {
	for _, name := range []string{"general", "medical", "robotics"} {
		d, ok := domainpack.Lookup(name)
		if !ok {
			t.Fatalf("%s is not a pack", name)
		}
		if d.ReviewAuthority != "" {
			t.Errorf("the %s pack offers a raised ceiling, which it must not", name)
		}
		with := d.CeilingWith(true)
		if with != d.MaxTier {
			t.Errorf("recording an authority on %s moved its ceiling from %q to %q",
				name, d.MaxTier, with)
		}
	}
	// Medical and robotics specifically: nothing, at any tier, ever.
	for _, name := range []string{"medical", "robotics"} {
		d, _ := domainpack.Lookup(name)
		for _, tier := range engine.AllRiskTiers() {
			if d.CeilingWith(true).Valid() && d.CeilingWith(true).AtLeast(tier) {
				t.Errorf("with an authority recorded, %s permits %s work", name, tier)
			}
		}
	}
}

// A raised ceiling can never reach r5, whatever a table row says.
func TestPackCeilingWith_NeverReachesProhibited(t *testing.T) {
	for _, d := range domainpack.All() {
		if got := d.CeilingWith(true); got.Prohibited() {
			t.Errorf("the %s pack reaches r5 with an authority recorded", d.Pack)
		}
	}
}
