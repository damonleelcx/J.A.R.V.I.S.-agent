package tools

import (
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
)

// The gate classifies the call rather than reading the tool's label (PRD SAF-01).

func writeContract() Contract {
	return Contract{
		Name:          "workspace.write",
		Capabilities:  []Capability{CapWrite},
		RiskTier:      engine.RiskR1,
		Reversibility: ReversibleAutomatic,
		Timeout:       time.Second,
		Available:     true,
	}
}

func fullGrant(ceiling engine.RiskTier, production bool) Grant {
	return Grant{
		Capabilities: []Capability{CapRead, CapWrite, CapExecute, CapSimulate, CapDeploy},
		MaxRiskTier:  ceiling,
		Autonomy:     engine.AutonomyApprovalGated,
		Production:   production,
	}
}

// The case the whole change exists for: identical tool, identical grant ceiling,
// different deployment context, different answer.
func TestTheSameToolIsRefusedInProductionAndAllowedInDevelopment(t *testing.T) {
	c := writeContract()

	if ok, why := fullGrant(engine.RiskR1, false).Permits(c); !ok {
		t.Fatalf("a reversible write was refused on a development deployment: %s", why)
	}
	ok, why := fullGrant(engine.RiskR1, true).Permits(c)
	if ok {
		t.Fatal("a write against PRODUCTION was permitted at an R1 ceiling.\n" +
			"SAF-01 asks for the tier to rise with deployment context; if this passes, " +
			"the classifier is not reaching the gate and the tier is still the tool's label")
	}
	// The refusal has to be actionable. "This tool is r2" against a contract
	// that says r1 is confusing on its own.
	for _, want := range []string{"declared r1", "classified higher", "production"} {
		if !strings.Contains(why, want) {
			t.Errorf("the refusal does not mention %q, so nobody can tell why it moved: %s", want, why)
		}
	}
	// And it is allowed against production once the ceiling admits the real tier.
	if ok, why := fullGrant(engine.RiskR2, true).Permits(c); !ok {
		t.Fatalf("the same call was refused at an R2 ceiling: %s", why)
	}
}

// Reading production is not treated as a change to it.
func TestReadingProductionIsNotRaised(t *testing.T) {
	c := Contract{
		Name: "workspace.read", Capabilities: []Capability{CapRead},
		RiskTier: engine.RiskR0, Reversibility: ReversibleNone,
		Timeout: time.Second, Available: true,
	}
	if ok, why := fullGrant(engine.RiskR0, true).Permits(c); !ok {
		t.Fatalf("reading was refused in production: %s\nRaising reads would push every "+
			"tier up until the ceiling stopped discriminating between them", why)
	}
}

// An irreversible tool is gated on its irreversibility even where the ceiling
// would have admitted its declared tier.
func TestIrreversibilityReachesTheGate(t *testing.T) {
	c := Contract{
		Name: "connector.publish", Capabilities: []Capability{CapWrite},
		RiskTier: engine.RiskR2, Reversibility: Irreversible,
		Timeout: time.Second, Available: true,
	}
	// R2 declared, R2 ceiling, development: the floor for irreversible is R2, so
	// this is admitted — the rule is a floor, not a penalty.
	if ok, why := fullGrant(engine.RiskR2, false).Permits(c); !ok {
		t.Fatalf("an irreversible R2 tool was refused at an R2 ceiling: %s", why)
	}
	// In production the same call is R3 and the ceiling no longer covers it.
	if ok, _ := fullGrant(engine.RiskR2, true).Permits(c); ok {
		t.Error("an irreversible change to production passed an R2 ceiling")
	}
}
