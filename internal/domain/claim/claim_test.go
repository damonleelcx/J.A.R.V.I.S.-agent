package claim

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
