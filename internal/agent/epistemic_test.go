package agent

import (
	"strings"
	"testing"
)

// The ledger is derived, never taken from the model. These hold the derivation.

func TestClaimLedger_AssumptionsAreAssumedAndGeometryIsProposed(t *testing.T) {
	r := &Reply{
		Speech: "Here it is.",
		Prototype: &Prototype{
			Name: "bracket", Units: "mm",
			Parts:       []PrototypePart{{ID: "p", Shape: "box"}},
			Assumptions: []string{"Base plate is 50x50x5 mm", "Boss height is 15 mm"},
			NotVerified: []string{"nothing checked"},
		},
	}
	if err := r.validate(); err != nil {
		t.Fatal(err)
	}
	byHow := map[Epistemic][]string{}
	for _, c := range r.Claims {
		byHow[c.How] = append(byHow[c.How], c.Statement)
	}
	if len(byHow[Assumed]) != 2 {
		t.Errorf("want both assumptions labelled assumed, got %v", byHow[Assumed])
	}
	if len(byHow[Proposed]) != 1 {
		t.Errorf("the geometry itself must be labelled proposed, got %v", byHow[Proposed])
	}
	// Nothing may claim to have been observed or calculated: nothing was.
	for _, forbidden := range []Epistemic{Observed, Calculated, Simulated} {
		if got := byHow[forbidden]; len(got) > 0 {
			t.Errorf("a claim was labelled %q in a deployment that checked nothing: %v", forbidden, got)
		}
	}
}

// A standards reference is RETRIEVED, and because nothing here can be retrieved
// FROM anywhere, the source must say so in as many words.
func TestClaimLedger_StandardsAreRetrievedFromMemory(t *testing.T) {
	r := &Reply{
		Speech: "The NEMA 17 bolt pattern is 31 mm square.",
		Prototype: &Prototype{
			Parts:       []PrototypePart{{ID: "p", Shape: "box"}},
			NotVerified: []string{"nothing checked"},
		},
	}
	if err := r.validate(); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, c := range r.Claims {
		if c.How != Retrieved {
			continue
		}
		found = true
		if !strings.Contains(strings.ToLower(c.Source), "memory") {
			t.Errorf("a retrieved claim's source is %q; in this deployment it came from memory", c.Source)
		}
		if c.Actionableish() {
			t.Errorf("a recalled figure was marked actionable")
		}
	}
	if !found {
		t.Fatalf("the standards reference produced no retrieved claim: %+v", r.Claims)
	}
}

// The two derivations must not disagree: a sentence already recorded as a
// standards reference must not ALSO appear as a bare assumption.
func TestClaimLedger_DoesNotDoubleCountAStandardsAssumption(t *testing.T) {
	r := &Reply{
		Speech: "Done.",
		Prototype: &Prototype{
			Parts:       []PrototypePart{{ID: "p", Shape: "box"}},
			Assumptions: []string{"Bolt pattern is 31mm x 31mm (standard NEMA 17)"},
			NotVerified: []string{"nothing checked"},
		},
	}
	if err := r.validate(); err != nil {
		t.Fatal(err)
	}
	var assumedCount, retrievedCount int
	for _, c := range r.Claims {
		switch c.How {
		case Assumed:
			assumedCount++
		case Retrieved:
			retrievedCount++
		}
	}
	if retrievedCount != 1 {
		t.Errorf("the standards sentence should be retrieved once, got %d", retrievedCount)
	}
	if assumedCount != 0 {
		t.Errorf("the same sentence was also counted as an assumption (%d) — two derivations that will drift", assumedCount)
	}
}

// Every claim that reaches a caller carries a valid label. An unlabelled claim
// is exactly what this package exists to prevent, so it must be impossible
// rather than merely discouraged.
func TestClaimLedger_EveryClaimIsLabelled(t *testing.T) {
	r := &Reply{
		Speech: "A NEMA 17 face is 42.3 mm. I picked 60 mm for the plate.",
		Prototype: &Prototype{
			Name: "thing", Parts: []PrototypePart{{ID: "p", Shape: "box"}},
			Assumptions: []string{"plate 60 mm", ""},
			NotVerified: []string{"nothing checked"},
		},
		ProposedGoal: &ProposedGoal{Title: "Do the thing", Statement: "…"},
	}
	if err := r.validate(); err != nil {
		t.Fatal(err)
	}
	if len(r.Claims) == 0 {
		t.Fatal("a reply with assumptions, a standard, geometry and a goal produced no claims")
	}
	for i, c := range r.Claims {
		if !c.How.Valid() {
			t.Errorf("claim %d carries an invalid label %q", i, c.How)
		}
		if strings.TrimSpace(c.Statement) == "" {
			t.Errorf("claim %d has no statement", i)
		}
	}
}
