package agent

import (
	"strings"
	"testing"
)

// needs is the pointer form the planner schema uses. Written as a helper
// because `&[]string{...}` is not addressable inline and because the difference
// between needs() and omitting the field entirely is the whole subject of this
// file — spelling it out each time makes the tests read as the claim they make.
func needs(keys ...string) *[]string {
	if keys == nil {
		keys = []string{}
	}
	return &keys
}

func depsOf(tasks []PlannedTask, key string) []string {
	for _, t := range tasks {
		if t.Key == key {
			return t.DependsOn
		}
	}
	return nil
}

// ‼️ A task that says it needs nothing loses the edge the model wrote anyway.
//
// This is the whole point of issue 12. The planner writes tasks in the order it
// thought of them and chains each to the previous, because that is what a
// numbered list looks like from the inside — so a survey that reads nothing ends
// up waiting on a survey it has no relationship with, and the worker pool runs
// one task at a time for no reason. A declared empty `needs` is the model saying
// that edge is not real, and it is acted on.
func TestDerive_ATaskThatDeclaresItNeedsNothingLosesTheEdgeTheModelWroteAnyway(t *testing.T) {
	tasks := []PlannedTask{
		{Key: "a", Produces: []string{"alpha"}, Needs: needs()},
		// The model chained b to a out of habit; b reads nothing.
		{Key: "b", Produces: []string{"beta"}, Needs: needs(), DependsOn: []string{"a"}},
	}

	out, d := deriveDependencies(tasks)

	if got := depsOf(out, "b"); len(got) != 0 {
		t.Errorf("b still depends on %v although it declared it needs nothing; "+
			"a and b can run at the same time and this edge costs a whole task's wall clock on every run", got)
	}
	if len(d.Dropped) != 1 || d.Dropped[0].Task != "b" || d.Dropped[0].DependsOn != "a" {
		t.Errorf("the derivation reported dropped edges %v, want exactly b → a; "+
			"an edge removed without being reported is a plan silently reshaped", d.Dropped)
	}
	if d.Considered != 2 {
		t.Errorf("considered %d tasks, want 2; both declared a needs list", d.Considered)
	}
	if !strings.Contains(d.Summary(), "b → a") {
		t.Errorf("the summary does not name the dropped edge, so nothing a person reads says what changed:\n%s",
			d.Summary())
	}
	// The input is the model's reply and other code still reads it.
	if len(tasks[1].DependsOn) != 1 {
		t.Error("deriveDependencies mutated the tasks it was given; the model's own reply must survive intact")
	}
}

// ‼️ A task that needs another's output depends on it whatever the model wrote.
//
// The mirror of the test above, and the half that keeps this honest: derivation
// ADDS the edges the artifact keys imply, so a planner that forgot to write
// `depends_on` at all still gets a correct DAG rather than a flat plan where a
// synthesis runs before the surveys it reads.
func TestDerive_ATaskThatNeedsAnothersOutputDependsOnItWhateverTheModelWrote(t *testing.T) {
	tasks := []PlannedTask{
		{Key: "survey-cost", Produces: []string{"cost"}, Needs: needs()},
		{Key: "survey-risk", Produces: []string{"risk"}, Needs: needs()},
		// The model wrote no edges at all, and wrote the join first in its list.
		{Key: "synthesis", Produces: []string{"report"}, Needs: needs("cost", "risk")},
	}

	out, d := deriveDependencies(tasks)

	got := depsOf(out, "synthesis")
	if len(got) != 2 || got[0] != "survey-cost" || got[1] != "survey-risk" {
		t.Fatalf("synthesis depends on %v, want [survey-cost survey-risk] in that order; "+
			"a join that does not wait for what it reads runs on missing inputs, and sorted order "+
			"keeps two runs over the same plan byte-identical", got)
	}
	if len(d.Added) != 2 {
		t.Errorf("the derivation reported %d added edges, want 2; edges added without being "+
			"reported are a plan reshaped where nobody can see it", len(d.Added))
	}
	if len(d.Dropped) != 0 {
		t.Errorf("dropped %v, but the model declared no edges to drop", d.Dropped)
	}
	// An artifact nobody produces is not an error and not an edge: it is an
	// input from outside the plan.
	orphan := []PlannedTask{{Key: "solo", Needs: needs("something-from-outside")}}
	if got := depsOf(mustDerive(t, orphan), "solo"); len(got) != 0 {
		t.Errorf("solo depends on %v for an artifact no task in the plan produces; "+
			"Validate would then refuse a plan for naming a key that does not exist", got)
	}
}

// ‼️ A task that declared no needs keeps exactly what the model wrote.
//
// The conservatism is the safety argument, and this is where it is held. Nil and
// empty are opposite claims that both marshal to nothing: "I did not mention
// needs" must never be read as "I need nothing", or every planner that has not
// been taught the new fields — a different provider, an older prompt,
// buildgoal.go's deliberately hand-built chains — silently loses its edges and
// starts running steps that edit the same model at the same time.
func TestDerive_ATaskThatDeclaredNoNeedsKeepsExactlyWhatTheModelWrote(t *testing.T) {
	// No task mentions needs: today's behaviour, byte for byte.
	chain := []PlannedTask{
		{Key: "one"},
		{Key: "two", DependsOn: []string{"one"}},
		{Key: "three", DependsOn: []string{"two"}},
	}
	out, d := deriveDependencies(chain)
	for _, want := range [][2]string{{"two", "one"}, {"three", "two"}} {
		if got := depsOf(out, want[0]); len(got) != 1 || got[0] != want[1] {
			t.Errorf("%s depends on %v, want [%s]; a planner that never mentioned needs must be "+
				"left alone entirely", want[0], got, want[1])
		}
	}
	if d.Considered != 0 || d.Changed() {
		t.Errorf("the derivation claims to have considered %d tasks and changed the graph (%t); "+
			"nothing declared a needs list, so it must have done nothing at all", d.Considered, d.Changed())
	}
	if !strings.Contains(d.Summary(), "did not run") {
		t.Errorf("the summary does not distinguish \"ran and agreed\" from \"never ran\", which is "+
			"the first thing somebody debugging a linear plan needs to know:\n%s", d.Summary())
	}

	// And it is per task, not per plan: one task declaring needs must not
	// recompute a neighbour that stayed quiet.
	mixed := []PlannedTask{
		{Key: "quiet", DependsOn: []string{"loud"}},
		{Key: "loud", Produces: []string{"x"}, Needs: needs()},
	}
	got := depsOf(mustDerive(t, mixed), "quiet")
	if len(got) != 1 || got[0] != "loud" {
		t.Errorf("quiet depends on %v, want [loud]; it declared no needs, and its neighbour "+
			"declaring one is not its consent", got)
	}
}

// ‼️ A derivation that would cycle is discarded for what the model declared.
//
// A cycle inserts cleanly and then deadlocks: every task in it waits forever,
// the goal never finishes, and nothing reports an error. Artifact keys that
// imply one are keys that do not describe a DAG, and the safe answer is to stop
// using them rather than to guess which edge to cut. The model's own graph then
// faces Validate exactly as it did before this code existed — so a plan that was
// executable stays executable.
func TestDerive_ADerivationThatWouldCycleIsDiscardedForWhatTheModelDeclared(t *testing.T) {
	// The declared edges are a clean chain. The artifact keys are circular: each
	// task claims to read what the next one produces.
	tasks := []PlannedTask{
		{Key: "a", Title: "a", Instruction: "do a",
			Produces: []string{"alpha"}, Needs: needs("beta"), DependsOn: nil},
		{Key: "b", Title: "b", Instruction: "do b",
			Produces: []string{"beta"}, Needs: needs("alpha"), DependsOn: []string{"a"}},
	}

	out, d := deriveDependencies(tasks)

	if len(d.Cycle) == 0 {
		t.Fatal("the derivation did not notice the cycle its own edges make; it would have been " +
			"inserted, and both tasks would wait for each other forever with nothing reporting it")
	}
	if len(d.Added) != 0 || len(d.Dropped) != 0 {
		t.Errorf("the discarded derivation still reports added %v and dropped %v; the report must "+
			"describe what was applied, not what was contemplated", d.Added, d.Dropped)
	}
	if got := depsOf(out, "a"); len(got) != 0 {
		t.Errorf("a depends on %v; the whole derivation was discarded, so a keeps the nothing "+
			"the model wrote", got)
	}
	if got := depsOf(out, "b"); len(got) != 1 || got[0] != "a" {
		t.Errorf("b depends on %v, want [a]; the whole derivation was discarded, so b keeps the "+
			"model's edge", got)
	}
	// Discarding is all or nothing: a plan that was executable before must still
	// pass the planner's own check.
	if err := (&PlanResult{Tasks: out}).Validate(); err != nil {
		t.Errorf("the plan the derivation handed back is not executable: %v", err)
	}
	if !strings.Contains(d.Summary(), "DISCARDED") {
		t.Errorf("the summary does not say the derivation was discarded, so a plan quietly keeps "+
			"edges a reader will assume were derived:\n%s", d.Summary())
	}
}

func mustDerive(t *testing.T, tasks []PlannedTask) []PlannedTask {
	t.Helper()
	out, _ := deriveDependencies(tasks)
	return out
}
