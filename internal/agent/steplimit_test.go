package agent

import (
	"context"
	"strings"
	"testing"
)

// A build honours a maximum number of steps the request states.
//
// Live, 2026-09-15: asked for a desk lamp "Keep it simple, three steps at most",
// the planner planned five. The limit never reached it, and a model told it may
// still ignore it, so it is both said and enforced.
// docs/bugfix/2026-09-15-a-build-planned-more-steps-than-it-was-allowed.md

const fiveLampSteps = `{"steps":[
	{"name":"Base","what":"a round weighted base","assembly":"base"},
	{"name":"Lower Arm Segment","what":"the lower arm on the base","assembly":"arm"},
	{"name":"Elbow Joint","what":"a hinge at the top of the lower arm","assembly":"arm"},
	{"name":"Upper Arm Segment","what":"the upper arm from the hinge","assembly":"arm"},
	{"name":"Lampshade","what":"a conical shade on the upper arm","assembly":"shade"}]}`

func TestStatedStepLimit_ReadsAMaximumAndNothingElse(t *testing.T) {
	for _, tc := range []struct {
		asked string
		want  int // 0: no limit stated
	}{
		{"I want a small desk lamp. Keep it simple, three steps at most. Please propose it.", 3},
		{"a desk lamp in at most 3 steps", 3},
		{"no more than four steps, please", 4},
		{"Build a gearbox, 5 steps max", 5},
		{"a maximum of 6 build steps", 6},
		{"up to two passes", 2},
		{"a bracket in fewer than four steps", 3},
		{"seven steps or fewer, and at most five steps", 5},
		// Not limits.
		{"a desk lamp in three steps", 0},
		{"a lamp with three parts at most", 0},
		{"a car with 4 wheels", 0},
		{"a staircase with at most 12 stairs", 0},
	} {
		got, ok := statedStepLimit(tc.asked)
		if (tc.want == 0 && ok) || (tc.want != 0 && (!ok || got != tc.want)) {
			t.Errorf("statedStepLimit(%q) = %d, %v; want %d", tc.asked, got, ok, tc.want)
		}
	}
}

// The planner is told the limit, and a plan over it is combined down to it and
// said — the lamp keeps its shade.
func TestPlanBuild_AStatedMaximumOfStepsIsHonouredAndSaid(t *testing.T) {
	stub := &scriptedStub{replies: []string{fiveLampSteps}}
	c := &Conversation{client: stub}

	steps, note, err := c.planBuildNoted(context.Background(),
		"a small desk lamp: a weighted base, a two-segment arm with a hinge, a conical shade. Three steps at most.")
	if err != nil {
		t.Fatal(err)
	}
	if len(stub.asked) != 1 || !strings.Contains(stub.asked[0], "at most 3 steps") {
		t.Errorf("the planner was not told the limit: %q", stub.asked)
	}
	if len(steps) != 3 {
		t.Fatalf("a request for at most 3 steps planned %d: %+v", len(steps), steps)
	}
	last := steps[2]
	for _, want := range []string{"hinge", "upper arm", "conical shade"} {
		if !strings.Contains(last.What, want) {
			t.Errorf("step 3 does not carry %q, so a combined plan dropped work that was asked for: %q", want, last.What)
		}
	}
	if last.Assembly != "" {
		t.Errorf("a step combining the arm and the shade is shown only %q", last.Assembly)
	}
	if !strings.Contains(note, "at most 3") || !strings.Contains(note, "5") {
		t.Errorf("the plan was changed and the note does not say so: %q", note)
	}

	// Within the limit: nothing changes and nothing is said.
	stub = &scriptedStub{replies: []string{fiveLampSteps}}
	c = &Conversation{client: stub}
	steps, note, err = c.planBuildNoted(context.Background(), "a desk lamp, no more than 8 steps")
	if err != nil || len(steps) != 5 || note != "" {
		t.Errorf("a plan within its limit came back as %d step(s), note %q, err %v", len(steps), note, err)
	}
}

// The limit reaches the goal's plan: its tasks, and the rationale the proposal
// card, the API reply and the plan's timeline entry show. Against Postgres.
func TestPlanBuildGoal_AStatedMaximumOfStepsIsTheGoalsPlanAndItsRationaleSaysSo(t *testing.T) {
	h := newBuildHarness(t)
	goal := h.goal(t, nil)
	goal.Statement = "I want a small desk lamp. Keep it simple, three steps at most."

	outcome, err := planBuildGoal(context.Background(), h.pool, &Conversation{client: &goalStub{replies: []string{fiveLampSteps}}},
		h.applier, goal, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Tasks) != 3 {
		t.Fatalf("a goal asking for at most three steps was planned as %d task(s)", len(outcome.Tasks))
	}
	if !strings.Contains(outcome.Tasks[2].Title, "Upper Arm Segment + Lampshade") {
		t.Errorf("step 3 is not the steps combined into it: %q", outcome.Tasks[2].Title)
	}
	if !strings.Contains(outcome.Rationale, "Build it in 3 steps") || !strings.Contains(outcome.Rationale, "combined") {
		t.Errorf("the rationale does not say the plan was brought within the limit: %q", outcome.Rationale)
	}
}
