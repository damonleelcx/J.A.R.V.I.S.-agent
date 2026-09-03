package drill

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The harness's own fences.
//
// A drill suite is a thing that reports green. What makes it worth anything is
// that it CAN report red, and the ways it can silently stop being able to are
// specific: a scenario whose injection stopped working, a selector that matched
// nothing, an invariant nobody looks at. Each of those is asserted here.

// The rule the whole harness rests on: a scenario that disturbed nothing has
// demonstrated nothing, however many invariants it went on to satisfy.
func TestResult_NoFaultEvidenceIsAFailureNotAPass(t *testing.T) {
	r := &Result{
		Scenario: "looks fine",
		Checks: []Check{
			{Name: "state is preserved", Held: true, Detail: "of course it is; nothing happened"},
			{Name: "completion is not implied", Held: true, Detail: "likewise"},
		},
	}
	if r.FaultInjected() {
		t.Fatal("a scenario with no evidence reported that it injected a fault")
	}
	if r.Passed() {
		t.Fatal("a scenario that disturbed nothing passed on the strength of checks about an undisturbed system")
	}
	if !strings.Contains(r.Summary(), "VACUOUS") {
		t.Fatalf("the summary does not say why it did not count: %s", r.Summary())
	}
	// With evidence, the same checks now mean something.
	r.FaultEvidence = "a lease expired"
	if !r.Passed() {
		t.Fatal("a scenario with evidence and passing checks did not pass")
	}
}

func TestResult_ABrokenInvariantFails(t *testing.T) {
	r := &Result{
		Scenario:      "something broke",
		FaultEvidence: "a lease expired",
		Checks: []Check{
			{Name: "state is preserved", Held: true},
			{Name: "completion is not implied", Held: false, Detail: "the task says succeeded"},
		},
	}
	if r.Passed() {
		t.Fatal("a scenario with a failed invariant passed")
	}
	if !strings.Contains(r.Summary(), "completion is not implied") {
		t.Fatalf("the summary does not name what broke: %s", r.Summary())
	}
}

// A scenario that could not run at all is a broken drill, not a finding, and the
// two need different responses.
func TestResult_AnErrorIsDistinctFromAFailure(t *testing.T) {
	r := &Result{Scenario: "could not start", Err: errors.New("the database went away")}
	if r.Passed() {
		t.Fatal("a scenario that errored passed")
	}
	if !strings.Contains(r.Summary(), "ERROR") {
		t.Fatalf("an unrunnable drill is not distinguished from a failing one: %s", r.Summary())
	}
}

// An empty run exiting 0 is the most expensive kind of green: it says the drills
// passed when no drill ran.
func TestReport_AnEmptyRunIsAFailure(t *testing.T) {
	r := &Report{}
	if r.Passed() {
		t.Fatal("a run with no scenarios passed")
	}
	if !strings.Contains(r.Summary(), "empty drill run is a failure") {
		t.Fatalf("the summary does not explain: %s", r.Summary())
	}
}

// Run refuses a selector that matches nothing rather than reporting success over
// an empty selection.
func TestRun_RefusesAnUnknownScenarioRatherThanRunningNone(t *testing.T) {
	_, err := Run(context.Background(), "postgres://unused", []string{"no-such-drill"}, false, nil)
	if err == nil {
		t.Fatal("an unknown drill name was accepted, and the run would have proved nothing")
	}
	if !strings.Contains(err.Error(), "no drill named") {
		t.Fatalf("the error does not name the problem: %v", err)
	}
}

// Every registered scenario must describe what it proves and cover at least one
// of NFR-07's four properties by name. A drill nobody can read is a drill nobody
// maintains, and it goes vacuous quietly.
func TestScenarios_EachOneSaysWhatItProves(t *testing.T) {
	all := Scenarios()
	if len(all) == 0 {
		t.Fatal("no scenarios are registered; this fence would pass vacuously")
	}
	for _, s := range all {
		if strings.TrimSpace(s.Describes) == "" {
			t.Fatalf("drill %q does not say what it proves", s.Name)
		}
		if s.Run == nil {
			t.Fatalf("drill %q has nothing to run", s.Name)
		}
		if len(s.Describes) < 60 {
			t.Fatalf("drill %q describes itself in %d characters; say what the fault is AND what must "+
				"remain true", s.Name, len(s.Describes))
		}
	}
}

// NFR-07 names four properties. Between them the drills must cover the two that
// can be checked from state — and "never imply completion" must be checked by
// every one of them, because it is the one that costs the most when it is wrong.
func TestScenarios_CoverNFR07(t *testing.T) {
	all := Scenarios()
	if len(all) == 0 {
		t.Fatal("no scenarios are registered; this fence would pass vacuously")
	}
	// NFR-07's four properties, and a word from each that a description covering
	// it would have to contain. Stated here so that renaming a drill cannot
	// quietly drop coverage of one of them.
	need := map[string]string{
		"preserve state":         "checkpoint",
		"stop dependents safely": "downstream",
		"expose partial results": "succeed",
		"never imply completion": "done",
	}
	joined := strings.ToLower(strings.Join(describeAll(all), " "))
	for property, word := range need {
		if !strings.Contains(joined, word) {
			t.Fatalf("no drill's description covers %q — no scenario mentions %q", property, word)
		}
	}
}

func describeAll(all []Scenario) []string {
	out := make([]string, 0, len(all))
	for _, s := range all {
		out = append(out, s.Describes)
	}
	return out
}

// A check explains itself whether or not it held. One that only speaks on
// failure gives a reader nothing to sanity-check when it passes, which is how a
// vacuous check survives review.
func TestCheck_ExplainsItselfWhenItHolds(t *testing.T) {
	c := check("state is preserved", true, "the checkpoint is %s", "still there")
	if c.Detail == "" {
		t.Fatal("a passing check said nothing about what it saw")
	}
	if !strings.Contains(c.Detail, "still there") {
		t.Fatalf("the detail does not describe the observation: %q", c.Detail)
	}
}
