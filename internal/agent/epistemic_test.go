package agent

import (
	"strings"
	"testing"
)

// The vocabulary is closed and complete: exactly the seven the PRD names, no
// more and no fewer. A test that enumerates them is the only thing standing
// between "seven categories" and someone adding an eighth that means "sort of".
func TestEpistemic_IsExactlyTheSevenThePRDNames(t *testing.T) {
	want := map[Epistemic]bool{
		Observed: true, Retrieved: true, Calculated: true, Simulated: true,
		Inferred: true, Assumed: true, Proposed: true,
	}
	got := AllEpistemics()
	if len(got) != len(want) {
		t.Fatalf("vocabulary has %d entries, PRD RSN-05 names %d: %v", len(got), len(want), got)
	}
	for _, e := range got {
		if !want[e] {
			t.Errorf("%q is not one of the seven", e)
		}
		if e.Gloss() == "" || e.Gloss() == "unrecognised" {
			t.Errorf("%q has no gloss, so it cannot be shown to a reader", e)
		}
	}
}

// Only what this deployment actually checked may be acted on without checking.
// Getting this wrong is the whole failure mode: a guess that reads as a
// measurement.
func TestEpistemic_OnlyCheckedCategoriesAreActionable(t *testing.T) {
	for _, e := range []Epistemic{Observed, Calculated} {
		if !e.Actionable() {
			t.Errorf("%q should be actionable: it was checked here", e)
		}
	}
	for _, e := range []Epistemic{Retrieved, Simulated, Inferred, Assumed, Proposed} {
		if e.Actionable() {
			t.Errorf("%q is marked actionable, but nothing in this deployment checked it", e)
		}
	}
}

// Arithmetic does not launder a guess. A value calculated from an assumed input
// is assumed with extra steps.
func TestEpistemic_WeakestWins(t *testing.T) {
	for _, tc := range []struct {
		in   []Epistemic
		want Epistemic
	}{
		{[]Epistemic{Calculated, Assumed}, Assumed},
		{[]Epistemic{Observed, Calculated}, Calculated},
		{[]Epistemic{Observed, Retrieved, Inferred}, Inferred},
		{[]Epistemic{Assumed, Proposed}, Proposed},
		{[]Epistemic{Observed}, Observed},
	} {
		if got := Weakest(tc.in...); got != tc.want {
			t.Errorf("Weakest(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// No inputs is not "observed". It is the weakest thing there is.
	if got := Weakest(); got != Proposed {
		t.Errorf("Weakest() with no inputs = %q, want %q", got, Proposed)
	}
}

// An unrecognised label must be treated as WORSE than every recognised one.
// The opposite — unknown defaulting to something safe-looking — is how a typo
// becomes a measurement.
func TestEpistemic_UnknownLabelIsNeverSafer(t *testing.T) {
	unknown := Epistemic("probably-fine")
	if unknown.Valid() {
		t.Fatal("an invented label validated")
	}
	if unknown.Actionable() {
		t.Fatal("an unrecognised label is actionable")
	}
	if got := Weakest(Observed, unknown); got != unknown {
		t.Fatalf("Weakest(observed, unknown) = %q; an unknown label must outrank a known one for weakness", got)
	}
	c := Claim{Statement: "x", How: unknown}
	if changed := c.Validate(); changed == "" || c.How != Inferred {
		t.Fatalf("an unrecognised label was not downgraded: how=%q changed=%q", c.How, changed)
	}
}

// The fabricated-standard case, given its true name: a retrieved figure with no
// source IS a recalled one, and the claim must say so rather than being dropped.
func TestClaim_RetrievedWithoutASourceIsNamedAsRecalled(t *testing.T) {
	c := Claim{Statement: "NEMA 17 bolt pattern is 31mm square", How: Retrieved}
	changed := c.Validate()
	if changed == "" {
		t.Fatal("a retrieved claim with no source passed validation unchanged")
	}
	if c.How != Retrieved {
		t.Errorf("the category was changed to %q; it is still retrieved, just from memory", c.How)
	}
	if !strings.Contains(strings.ToLower(c.Source), "memory") {
		t.Errorf("source is %q; it must say where the figure actually came from", c.Source)
	}
}

// There is no solver in this deployment, so nothing may claim to be simulated.
// PRD's own carve-out: a fabricated solver result is the single most dangerous
// output this system could produce.
func TestClaim_SimulatedIsRefusedBecauseThereIsNoSolver(t *testing.T) {
	c := Claim{Statement: "peak stress is 42 MPa", How: Simulated}
	changed := c.Validate()
	if c.How == Simulated {
		t.Fatal("a claim was allowed to say it was simulated; there is no solver here")
	}
	if c.How != Inferred {
		t.Errorf("downgraded to %q, want inferred", c.How)
	}
	if !strings.Contains(changed, "no solver") {
		t.Errorf("the downgrade did not say why: %q", changed)
	}
}

// A simulated claim WITH a real source is a different thing and is not
// downgraded — the rule is about this deployment having no solver, not about the
// category being forbidden forever.
func TestClaim_SimulatedWithASourceSurvives(t *testing.T) {
	c := Claim{Statement: "peak stress 42 MPa", How: Simulated, Source: "ansys-run-4417"}
	c.Validate()
	if c.How != Simulated {
		t.Fatalf("a sourced simulation was downgraded to %q", c.How)
	}
}

func TestClaim_EmptyStatementIsNotSilentlyKept(t *testing.T) {
	c := Claim{How: Observed}
	if changed := c.Validate(); changed == "" {
		t.Fatal("an empty statement passed validation without comment")
	}
}

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
