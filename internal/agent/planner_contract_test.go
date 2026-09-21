package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
)

// ‼️ The planner's contract says independent tasks run at the same time, and the
// code agrees with the sentence.
//
// # Why a test reads a prompt
//
// deriveDependencies discards `depends_on` for any task that declares `needs`.
// That is a load-bearing thing to do to a model's output, and it is only safe if
// the model was TOLD. A prompt that still says "declare dependencies honestly"
// while the code quietly recomputes them is worse than either half alone: the
// planner writes edges it believes in, they are thrown away, and the reason is
// in a Go file the planner never sees.
//
// The same reasoning as the geometry contract fences (TestTheContractSays…): a
// sentence about behaviour is only worth having if it is the code's behaviour,
// so the sentence is asserted here and the behaviour is asserted next to it.
func TestThePlannerContractSaysIndependentTasksRunAtTheSameTime(t *testing.T) {
	// The rule the derivation applies, stated to the model that is subject to it.
	for _, want := range []string{
		`Put in "produces" the stable keys of what a task leaves behind, and in "needs"`,
		`has its "depends_on" RECOMPUTED as exactly the tasks producing what it needs`,
		`Omit "needs" and your`,
		`"depends_on" is kept as written.`,
		// Concurrency is a fact about the worker pool, not a hope. It is in the
		// prompt because an edge the planner adds out of tidiness costs real
		// wall-clock time on every single run of that goal.
		`Tasks with no path between them RUN AT THE SAME TIME on a pool of workers`,
	} {
		if !strings.Contains(plannerFraming, want) {
			t.Errorf("the planner contract does not say %q, so the model is not told that its "+
				"\"needs\" list decides its edges", want)
		}
	}

	// The schema has to offer the fields, or nothing above can be obeyed.
	for _, want := range []string{`"produces": [`, `"needs": [`} {
		if !strings.Contains(plannerFraming, want) {
			t.Errorf("the reply schema does not contain %q; the model cannot fill in a field it "+
				"was never shown", want)
		}
	}

	// One worked example, consistent with itself: three independent producers
	// and a join that names all three. A worked example that contradicted the
	// rule would teach the contradiction.
	for _, want := range []string{
		`survey-cost    produces ["cost"]   needs []                       depends_on []`,
		`needs ["cost","supply","risk"] depends_on ["survey-cost","survey-supply","survey-risk"]`,
		`The three surveys hold three workers at once; only the synthesis waits.`,
	} {
		if !strings.Contains(plannerFraming, want) {
			t.Errorf("the fan-out example is missing %q; without it the rule is stated and never shown", want)
		}
	}

	// And the derivation is on the path a plan actually takes.
	//
	// Asserting the prompt alone would leave a fence guarding a string: deleting
	// the deriveDependencies call from Plan would keep every sentence above true
	// and every plan a chain. This goes through Plan, with the example from the
	// contract as the plan, so removing the call turns it red.
	reply, err := json.Marshal(PlanResult{
		Rationale: "three surveys and a join",
		Tasks: []PlannedTask{
			{Key: "survey-cost", Title: "cost", Instruction: "survey cost",
				Produces: []string{"cost"}, Needs: &[]string{}},
			{Key: "survey-supply", Title: "supply", Instruction: "survey supply",
				Produces: []string{"supply"}, Needs: &[]string{},
				// The chain the planner writes out of habit, and the thing the
				// derivation exists to undo.
				DependsOn: []string{"survey-cost"}},
			{Key: "synthesis", Title: "synthesis", Instruction: "join them",
				Produces: []string{"report"}, Needs: &[]string{"cost", "supply"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	planner := NewPlanner(&planStub{plan: string(reply)}, persona.DefaultCharacter())
	got, err := planner.Plan(context.Background(),
		&engine.Goal{ID: "goal_contract", Title: "survey", Statement: "survey three things"}, nil, "")
	if err != nil {
		t.Fatalf("planning: %v", err)
	}

	deps := map[string][]string{}
	for _, task := range got.Tasks {
		deps[task.Key] = task.DependsOn
	}
	if len(deps["survey-supply"]) != 0 {
		t.Errorf("survey-supply came back depending on %v although it declared it needs nothing; "+
			"the derivation is not on the path Plan takes, so the contract above is a sentence "+
			"about nothing", deps["survey-supply"])
	}
	if d := deps["synthesis"]; len(d) != 2 || d[0] != "survey-cost" || d[1] != "survey-supply" {
		t.Errorf("synthesis came back depending on %v, want [survey-cost survey-supply]", d)
	}
	// Not silent: what was changed travels with the plan.
	if !strings.Contains(got.Rationale, "survey-supply → survey-cost") {
		t.Errorf("the rationale does not record the edge that was dropped, so a plan was reshaped "+
			"where only a log line could show it:\n%s", got.Rationale)
	}
}
