package workspace

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/claim"
)

func ptr(s string) *string { return &s }

// RSN-01 and WRK-03 name their nouns. A test that enumerates them is what keeps
// "one graph over both lists" from quietly becoming a graph over one of them.
func TestKinds_CoverBothRequirementsLists(t *testing.T) {
	// RSN-01: goals, requirements, constraints, assumptions, decisions, risks,
	// success criteria.
	rsn := []Kind{KindGoal, KindRequirement, KindConstraint, KindAssumption,
		KindDecision, KindRisk, KindCriterion}
	// WRK-03: requirements, components, interfaces, files, tests, hazards,
	// decisions, owners, evidence.
	wrk := []Kind{KindRequirement, KindComponent, KindInterface, KindArtifact,
		KindTest, KindHazard, KindDecision, KindOwner, KindEvidence}

	for _, k := range append(append([]Kind{}, rsn...), wrk...) {
		if !k.Valid() {
			t.Fatalf("the PRD names %q and the graph has no such kind", k)
		}
	}
	for _, d := range Kinds() {
		if strings.TrimSpace(d.Gloss) == "" {
			t.Fatalf("kind %q has no gloss; a kind nobody can explain will be used for the wrong thing", d.Kind)
		}
		if strings.TrimSpace(d.PRDName) == "" {
			t.Fatalf("kind %q does not say what the PRD calls it", d.Kind)
		}
	}

	// The overlap is the whole reason this is one graph rather than two.
	shared := 0
	for _, a := range rsn {
		for _, b := range wrk {
			if a == b {
				shared++
			}
		}
	}
	if shared < 2 {
		t.Fatalf("only %d kinds are named by both RSN-01 and WRK-03; if that is really true, "+
			"the argument for one graph instead of two needs rewriting", shared)
	}
}

// Risks and hazards are different things and the PRD names both. Collapsing
// them loses the distinction safety work is built on.
func TestKinds_RiskAndHazardAreNotTheSameKind(t *testing.T) {
	if KindRisk == KindHazard {
		t.Fatal("risk and hazard are the same value")
	}
	r, _ := KindOf(KindRisk)
	h, _ := KindOf(KindHazard)
	if r.Gloss == h.Gloss {
		t.Fatal("risk and hazard have the same gloss, so nothing tells a reader which to use")
	}
}

// The rule the graph turns on. An assumption filed as "observed" is not an
// assumption, and permitting it would let the most important distinction in the
// graph be erased by a typo.
func TestNode_AnAssumptionCanOnlyBeAssumed(t *testing.T) {
	base := func(how claim.Epistemic) *Node {
		return &Node{ProjectID: "prj_1", CreatedBy: "usr_1", Kind: KindAssumption,
			Title: "the plate is 3 mm", How: how, Status: StatusOpen}
	}
	for _, how := range []claim.Epistemic{claim.Observed, claim.Retrieved, claim.Calculated,
		claim.Simulated, claim.Inferred, claim.Proposed} {
		if err := base(how).Validate(); err == nil {
			t.Fatalf("an assumption was accepted as %q", how)
		}
	}
	if err := base(claim.Assumed).Validate(); err != nil {
		t.Fatalf("an assumption labelled assumed was refused: %v", err)
	}
}

// An empty label takes the kind's default rather than being rejected — but the
// default must itself be permitted, or a kind could be impossible to create.
func TestNode_EveryKindsDefaultLabelIsOneItPermits(t *testing.T) {
	for _, d := range Kinds() {
		if len(d.Allowed) == 0 {
			continue
		}
		var ok bool
		for _, a := range d.Allowed {
			if a == d.Default {
				ok = true
			}
		}
		if !ok {
			t.Fatalf("kind %q defaults to %q, which it does not permit; nothing of that kind could be created",
				d.Kind, d.Default)
		}
	}
}

// Evidence is the one kind whose whole job is to be checkable, so it must not
// be allowed to be a guess.
func TestNode_EvidenceCannotBeAssumed(t *testing.T) {
	n := &Node{ProjectID: "prj_1", CreatedBy: "usr_1", Kind: KindEvidence,
		Title: "the run passed", How: claim.Assumed, Status: StatusOpen}
	if err := n.Validate(); err == nil {
		t.Fatal("evidence was accepted as assumed; evidence that is assumed is not evidence")
	}
}

func TestNode_OwnedKindsNeedATitleAndAnchorsDoNot(t *testing.T) {
	untitled := &Node{ProjectID: "prj_1", CreatedBy: "usr_1", Kind: KindRequirement, Status: StatusOpen}
	if err := untitled.Validate(); err == nil {
		t.Fatal("an untitled requirement was accepted; it would appear in every traversal saying nothing")
	}
	anchor := &Node{ProjectID: "prj_1", CreatedBy: "usr_1", Kind: KindGoal,
		GoalID: ptr("gol_1"), Status: StatusAccepted}
	if err := anchor.Validate(); err != nil {
		t.Fatalf("a goal anchor was refused for having no title of its own: %v", err)
	}
}

// An anchor names exactly one row. Two refs would make it ambiguous which table
// the content lives in, and a traversal would have to guess.
func TestNode_AnAnchorNamesExactlyOneRow(t *testing.T) {
	none := &Node{ProjectID: "prj_1", CreatedBy: "usr_1", Kind: KindGoal, Status: StatusAccepted}
	if err := none.Validate(); err == nil {
		t.Fatal("a goal anchor with no goal was accepted")
	}
	two := &Node{ProjectID: "prj_1", CreatedBy: "usr_1", Kind: KindGoal,
		GoalID: ptr("gol_1"), OwnerID: ptr("usr_2"), Status: StatusAccepted}
	if err := two.Validate(); err == nil {
		t.Fatal("an anchor pointing at two different rows was accepted")
	}
	wrong := &Node{ProjectID: "prj_1", CreatedBy: "usr_1", Kind: KindGoal,
		OwnerID: ptr("usr_2"), Status: StatusAccepted}
	if err := wrong.Validate(); err == nil {
		t.Fatal("a goal anchor pointing at a user was accepted")
	}
	owned := &Node{ProjectID: "prj_1", CreatedBy: "usr_1", Kind: KindRequirement,
		Title: "t", GoalID: ptr("gol_1"), Status: StatusOpen}
	if err := owned.Validate(); err == nil {
		t.Fatal("a requirement that also anchored a goal was accepted")
	}
}

// The point of typed edges: "test verifies requirement" and "requirement
// verifies test" are not both sentences. A graph that accepts either has an
// arbitrary direction on every edge and every query over it is wrong half the
// time.
func TestEdges_DirectionIsNotAToss(t *testing.T) {
	verifies, err := EdgeKindOf(EdgeVerifies)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifies.Permits(KindTest, KindRequirement); err != nil {
		t.Fatalf("a test could not verify a requirement: %v", err)
	}
	if err := verifies.Permits(KindRequirement, KindTest); err == nil {
		t.Fatal("a requirement was allowed to verify a test")
	}

	// The two halves of the rule are checked separately on purpose. A drill
	// found that the case above is caught by the FROM list alone, so disabling
	// the TO list entirely left this test green — the fence held the property by
	// luck rather than by covering it. A test verifying another test has a legal
	// left-hand side and an illegal right-hand one, so it can only be refused by
	// the TO check.
	if err := verifies.Permits(KindTest, KindTest); err == nil {
		t.Fatal("a test was allowed to verify another test; the right-hand side of the rule is not enforced")
	}
	// And the mirror: an owner has an illegal left-hand side for verifies, with a
	// legal right-hand one.
	if err := verifies.Permits(KindOwner, KindRequirement); err == nil {
		t.Fatal("an owner was allowed to verify a requirement; the left-hand side of the rule is not enforced")
	}
}

// Satisfying and verifying are different claims (PRD AGT-08 makes the same
// distinction for tasks). Nothing may quietly do both by accident.
func TestEdges_SatisfiesAndVerifiesAreDifferentRelations(t *testing.T) {
	sat, _ := EdgeKindOf(EdgeSatisfies)
	ver, _ := EdgeKindOf(EdgeVerifies)
	if sat.Gloss == ver.Gloss {
		t.Fatal("satisfies and verifies read the same; nothing tells a user which to draw")
	}
	// A component meets a requirement; it does not check it.
	if err := sat.Permits(KindComponent, KindRequirement); err != nil {
		t.Fatalf("a component could not satisfy a requirement: %v", err)
	}
	if err := ver.Permits(KindComponent, KindRequirement); err == nil {
		t.Fatal("a component was allowed to verify a requirement; building a thing is not checking it")
	}
}

func TestEdges_EveryKindReadsAsASentence(t *testing.T) {
	for _, d := range EdgeKinds() {
		if !strings.Contains(d.Reads, "%s") {
			t.Fatalf("edge %q has no readable form", d.Kind)
		}
		if strings.TrimSpace(d.Gloss) == "" {
			t.Fatalf("edge %q has no gloss", d.Kind)
		}
		if !d.Kind.Valid() {
			t.Fatalf("edge kind %q is not in its own vocabulary", d.Kind)
		}
	}
}

// Only an owner owns. An "owns" edge from a component would make accountability
// a property of a thing rather than of a person.
func TestEdges_OnlyAPersonOwnsSomething(t *testing.T) {
	owns, _ := EdgeKindOf(EdgeOwns)
	if err := owns.Permits(KindOwner, KindRequirement); err != nil {
		t.Fatalf("an owner could not own a requirement: %v", err)
	}
	if err := owns.Permits(KindComponent, KindRequirement); err == nil {
		t.Fatal("a component was allowed to own a requirement; accountability belongs to people")
	}
}

// ---------------------------------------------------------------------------
// WRK-04
// ---------------------------------------------------------------------------

func validVersion() *Version {
	return &Version{
		ArtifactID: "art_1", InitiatorID: "usr_1", Agent: AgentExecutor,
		ToolCallID: ptr("tcl_1"), Inputs: json.RawMessage(`{}`), Diff: "",
		Verification: Unverified, Disposition: Pending,
	}
}

// The requirement read literally: seven facts, and a version missing any of
// them is refused rather than stored with a blank.
func TestVersion_AllSevenFactsAreRequired(t *testing.T) {
	for _, tc := range []struct {
		what     string
		breaking func(*Version)
	}{
		{"initiator", func(v *Version) { v.InitiatorID = "" }},
		{"agent", func(v *Version) { v.Agent = "" }},
		{"tool", func(v *Version) { v.ToolCallID = nil }},
		{"inputs", func(v *Version) { v.Inputs = nil }},
		{"verification state", func(v *Version) { v.Verification = "probably fine" }},
		{"human disposition", func(v *Version) { v.Disposition = "shipped" }},
	} {
		v := validVersion()
		tc.breaking(v)
		if err := v.Validate(); err == nil {
			t.Fatalf("WRK-04 names %s and a version without it was accepted", tc.what)
		}
	}
	if err := validVersion().Validate(); err != nil {
		t.Fatalf("a complete version was refused: %v", err)
	}
}

// A human works without a tool; nothing else does. Inventing a tool call to fill
// the field would put a fabricated row in the idempotency ledger.
func TestVersion_OnlyAHumanWorksWithoutATool(t *testing.T) {
	human := validVersion()
	human.Agent, human.ToolCallID = AgentHuman, nil
	if err := human.Validate(); err != nil {
		t.Fatalf("a human edit with no tool call was refused: %v", err)
	}

	humanWithTool := validVersion()
	humanWithTool.Agent = AgentHuman
	if err := humanWithTool.Validate(); err == nil {
		t.Fatal("a version claiming both a human author and a tool call was accepted; one of the two is wrong")
	}

	for _, a := range []Agent{AgentPlanner, AgentExecutor, AgentVerifier, AgentScheduler, AgentSystem} {
		v := validVersion()
		v.Agent, v.ToolCallID = a, nil
		if err := v.Validate(); err == nil {
			t.Fatalf("a change by the %s with no tool call was accepted; nothing traces it to an action", a)
		}
	}
}

// Absent inputs and empty inputs are different statements.
func TestVersion_EmptyInputsAreAStatementAndAbsentOnesAreAGap(t *testing.T) {
	absent := validVersion()
	absent.Inputs = nil
	if err := absent.Validate(); err == nil {
		t.Fatal("a version with no inputs field was accepted")
	}
	empty := validVersion()
	empty.Inputs = json.RawMessage(`{}`)
	if err := empty.Validate(); err != nil {
		t.Fatalf("a version stating it had no inputs was refused: %v", err)
	}
	broken := validVersion()
	broken.Inputs = json.RawMessage(`{not json`)
	if err := broken.Validate(); err == nil {
		t.Fatal("a version with unparseable inputs was accepted")
	}
}

// PRD SAF-05 in storage form.
func TestVersion_AHumanDecisionNamesTheHuman(t *testing.T) {
	for _, d := range []Disposition{Accepted, Rejected} {
		v := validVersion()
		v.Disposition = d
		if err := v.Validate(); err == nil {
			t.Fatalf("a version %s by nobody was accepted", d)
		}
		at := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
		v.DispositionedBy, v.DispositionedAt = ptr("usr_2"), &at
		if err := v.Validate(); err != nil {
			t.Fatalf("a properly attributed %s was refused: %v", d, err)
		}
	}
	pending := validVersion()
	pending.DispositionedBy = ptr("usr_2")
	if err := pending.Validate(); err == nil {
		t.Fatal("a pending version that names who decided it was accepted")
	}
}

// The distinction the whole design turns on: a machine's pass is not a person's
// sign-off, and it never becomes one.
func TestVersion_VerificationIsNotASignOff(t *testing.T) {
	at := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)

	passedButUnseen := validVersion()
	passedButUnseen.Verification = Passed
	err := passedButUnseen.Usable()
	if err == nil {
		t.Fatal("a version nobody had looked at was usable because the tests passed")
	}
	if !strings.Contains(err.Error(), "not a sign-off") {
		t.Fatalf("the refusal did not explain the distinction: %v", err)
	}

	acceptedButFailing := validVersion()
	acceptedButFailing.Verification = Failed
	acceptedButFailing.Disposition, acceptedButFailing.DispositionedBy, acceptedButFailing.DispositionedAt =
		Accepted, ptr("usr_2"), &at
	if err := acceptedButFailing.Usable(); err == nil {
		t.Fatal("a version a person accepted was usable while its verification was failing")
	}

	both := validVersion()
	both.Verification = Passed
	both.Disposition, both.DispositionedBy, both.DispositionedAt = Accepted, ptr("usr_2"), &at
	if err := both.Usable(); err != nil {
		t.Fatalf("a verified and accepted version was not usable: %v", err)
	}
}

// The cycle finder must terminate on the input that would recurse forever, and
// must name the loop rather than merely reporting that one exists.
func TestFindCycle_TerminatesAndNamesTheLoop(t *testing.T) {
	edges := []Edge{
		{Kind: EdgeDependsOn, FromID: "a", ToID: "b"},
		{Kind: EdgeDependsOn, FromID: "b", ToID: "c"},
		{Kind: EdgeDependsOn, FromID: "c", ToID: "a"},
		{Kind: EdgeSatisfies, FromID: "x", ToID: "y"},
	}
	cycle := findCycle(edges, EdgeDependsOn)
	if len(cycle) < 3 {
		t.Fatalf("the cycle a→b→c→a was reported as %v", cycle)
	}
	seen := map[string]bool{}
	for _, n := range cycle {
		seen[n] = true
	}
	for _, want := range []string{"a", "b", "c"} {
		if !seen[want] {
			t.Fatalf("the reported cycle %v does not name %q", cycle, want)
		}
	}

	// A diamond is not a cycle. Reporting one would make the check useless,
	// because every real graph has diamonds.
	diamond := []Edge{
		{Kind: EdgeDependsOn, FromID: "top", ToID: "left"},
		{Kind: EdgeDependsOn, FromID: "top", ToID: "right"},
		{Kind: EdgeDependsOn, FromID: "left", ToID: "bottom"},
		{Kind: EdgeDependsOn, FromID: "right", ToID: "bottom"},
	}
	if c := findCycle(diamond, EdgeDependsOn); c != nil {
		t.Fatalf("a diamond was reported as a cycle: %v", c)
	}

	// A cycle on a DIFFERENT edge kind is not a depends_on cycle. derives_from
	// loops are impossible by construction and satisfies loops are merely odd.
	other := []Edge{
		{Kind: EdgeSatisfies, FromID: "a", ToID: "b"},
		{Kind: EdgeSatisfies, FromID: "b", ToID: "a"},
	}
	if c := findCycle(other, EdgeDependsOn); c != nil {
		t.Fatalf("a satisfies loop was reported as a dependency cycle: %v", c)
	}
}

// The review must not report a node deriving from a measurement as resting on a
// guess, or the check becomes noise and gets ignored.
func TestRestsOnlyOnGuesses_NeedsEveryInputToBeAGuess(t *testing.T) {
	byID := map[string]*Node{
		"guess":   {ID: "guess", Kind: KindAssumption, How: claim.Assumed},
		"measure": {ID: "measure", Kind: KindEvidence, How: claim.Observed},
	}
	n := &Node{ID: "req", Kind: KindRequirement, Status: StatusAccepted}

	onlyGuess := []Edge{{Kind: EdgeDerivesFrom, FromID: "req", ToID: "guess"}}
	if ok, _ := restsOnlyOnGuesses(n, onlyGuess, byID); !ok {
		t.Fatal("a requirement deriving only from an assumption was not reported")
	}

	mixed := append(onlyGuess, Edge{Kind: EdgeDerivesFrom, FromID: "req", ToID: "measure"})
	if ok, _ := restsOnlyOnGuesses(n, mixed, byID); ok {
		t.Fatal("a requirement with a measurement behind it was reported as resting on a guess")
	}

	// No inputs at all is a different observation, and not this one.
	if ok, _ := restsOnlyOnGuesses(n, nil, byID); ok {
		t.Fatal("a requirement with no recorded inputs was reported as resting on a guess")
	}
}
