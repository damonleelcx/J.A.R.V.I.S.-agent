package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// A build step on a tree is shown the assembly it builds. Phase 2, stage A2.

// The prompt a step sends carries its own assembly and not the others.
func TestAssemble_AStepOnATreeIsShownOnlyItsAssembly(t *testing.T) {
	stub := &scriptedStub{replies: []string{`{"speech":"ok"}`}}
	c := &Conversation{client: stub}
	doc := subsystems(50)

	c.buildOneStep(context.Background(), doc, "a plant", buildTask{Name: "covers", What: "add covers", Assembly: "sub-7"}, 2, 3)

	if len(stub.asked) == 0 {
		t.Fatal("the step asked nothing")
	}
	prompt := stub.asked[0]
	if !strings.Contains(prompt, "sub-7-a") || !strings.Contains(prompt, "The assembly this step builds") {
		t.Errorf("the step building sub-7 was not shown sub-7:\n%.400s", prompt)
	}
	if strings.Contains(prompt, "sub-8-a") || strings.Contains(prompt, "sub-49-b") {
		t.Errorf("the step building sub-7 was shown other subsystems' parts:\n%.400s", prompt)
	}
}

// Over the ceiling, a step is refused by name and nothing is asked.
func TestAssemble_AStepShownMoreThanItsCeilingIsRefused(t *testing.T) {
	stub := &scriptedStub{}
	c := &Conversation{client: stub}
	doc := &geometry.Document{Name: "grid", Units: "mm", Root: "grid"}
	huge := geometry.Assembly{ID: "huge"}
	for i := 0; i < 800; i++ {
		id := fmt.Sprintf("panel-%03d", i)
		doc.Definitions = append(doc.Definitions, geometry.Part{ID: id, Name: "A panel with a long descriptive name " + id,
			Shape: "box", Size: map[string]float64{"width": 10, "height": 10, "depth": 10}})
		huge.Children = append(huge.Children, geometry.Child{ID: id, Ref: id, Position: []float64{float64(i), 0, 0}})
	}
	doc.Assemblies = []geometry.Assembly{huge, {ID: "grid", Children: []geometry.Child{{ID: "all", Ref: "huge"}}}}

	next, note := c.buildOneStep(context.Background(), doc, "a grid", buildTask{Name: "panels", What: "panels", Assembly: "huge"}, 2, 2)

	if next != nil || stub.n != 0 {
		t.Errorf("a step over the ceiling was built (%v) after %d model call(s)", next != nil, stub.n)
	}
	if !strings.Contains(note, "KiB") || !strings.Contains(note, fmt.Sprint(maxStepContextBytes>>10)) {
		t.Errorf("the refusal does not name the ceiling: %q", note)
	}
}

// The plan names the sub-assembly each step builds, and the planner is asked to.
func TestPlanBuild_ReadsTheAssemblyAStepBuilds(t *testing.T) {
	stub := &scriptedStub{replies: []string{`{"steps":[
		{"name":"chassis","what":"the chassis","assembly":"chassis"},
		{"name":"wheels","what":"four wheels","assembly":"wheels"}]}`}}
	c := &Conversation{client: stub}

	steps, err := c.planBuild(context.Background(), "a car")
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 2 || steps[1].Assembly != "wheels" {
		t.Errorf("steps %+v; the second builds the wheels assembly", steps)
	}
	if !strings.Contains(planSystem, `"assembly"`) {
		t.Error("the planner is not asked to name the sub-assembly a step builds")
	}
}
