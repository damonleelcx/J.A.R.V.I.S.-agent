package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
)

// Fences for what a failed build step says (2026-09-15, live car findings).
// docs/bugfix/2026-09-15-a-failed-build-step-said-no-geometry-whatever-refused-it.md

// finishStub answers one call with a reply and a finish reason of its choosing.
type finishStub struct {
	reply, finish string
}

func (s *finishStub) Complete(context.Context, llm.Request) (*llm.Response, error) {
	return &llm.Response{Content: s.reply, FinishReason: s.finish}, nil
}

func (s *finishStub) ModelFor(r llm.Role) string {
	if r == llm.RoleVision {
		return ""
	}
	return "finish"
}

func onePlate() *Prototype {
	return &geometry.Document{Name: "car", Units: "mm", Parts: []geometry.Part{{ID: "plate", Name: "Plate",
		Shape: "box", Size: map[string]float64{"width": 100, "height": 10, "depth": 100}}}}
}

// ‼️ Each gate that can refuse a step says it is the one, and why.
//
// Two live builds lost their first step and every refusal read "produced no
// geometry": offline, seven different causes produced that one sentence. Each case
// here is one of them, with the words a person or the next pass needs to act.
func TestAssemble_AFailedStepSaysWhichGateRefusedIt(t *testing.T) {
	empty := &geometry.Document{Name: "car", Units: "mm"}
	tree := `"definitions":[{"id":"rail","shape":"box","size":{"width":50,"height":80,"depth":4000}}],
	  "assemblies":[{"id":"chassis","children":[{"id":"left-rail","ref":"rail","position":[-400,0,0]}]}]`
	for _, tc := range []struct {
		name, reply, finish string
		doc                 *Prototype
		gate                string
		says                []string
	}{
		{name: "an expression typed where a number goes", doc: empty, gate: "unreadable",
			reply: `{"speech":"frame","prototype":{"name":"c","units":"mm","parts":[{"id":"a","shape":"box","position":[0, track/2, 0]}]}}`,
			says:  []string{gateUnreadable, "does not parse", "track/2"}},
		{name: "a comment in the JSON", doc: empty, gate: "unreadable",
			reply: "{\"speech\":\"frame\",\"prototype\":{\"name\":\"c\",\"units\":\"mm\",\"parts\":[{\"id\":\"a\",\"shape\":\"box\"} // the frame\n]}}",
			says:  []string{gateUnreadable, "// the frame"}},
		{name: "a reply cut off at the limit", doc: empty, gate: "unreadable", finish: "length",
			reply: `{"speech":"frame","prototype":{"name":"c","units":"mm","parts":[{"id":"a","shape":"bo`,
			says:  []string{gateUnreadable, "cut off at the reply limit"}},
		{name: "a reply that asks to be built in passes", doc: empty, gate: "no-geometry",
			reply: `{"speech":"A car is too big for one document, so I will build it in passes.","build_in_passes":true}`,
			says:  []string{gateNoGeometry, "build_in_passes", "too big for one document"}},
		{name: "an edit sent to a model that does not exist", doc: empty, gate: "edit-refused",
			reply: `{"speech":"frame","prototype_edit":{"patch":{` + tree + `,"root":"chassis"}}}`,
			says:  []string{gateEditRefused, "no model on screen"}},
		{name: "a tree with no root", doc: empty, gate: "places-nothing",
			reply: `{"speech":"frame","prototype":{"name":"c","units":"mm",` + tree + `}}`,
			says:  []string{gatePlacesNothing, `1 definition(s) and 1 assembly(ies) and no "root"`}},
		{name: "a step that adds a fault", doc: onePlate(), gate: "faults-added",
			reply: `{"speech":"holes","prototype_edit":{"patch":{"features":[{"id":"hole","op":"cut","of":"plate","with":["ghost-cutter"]}]}}}`,
			says:  []string{gateBroke, "had 1 fault(s) where the model before it had 0", "ghost-cutter"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			finish := tc.finish
			if finish == "" {
				finish = "stop"
			}
			c := &Conversation{client: &finishStub{reply: tc.reply, finish: finish}}
			next, note := c.buildOneStep(context.Background(), tc.doc, "a car",
				buildTask{Name: "Chassis Frame", What: "the frame"}, 1, 7)
			if next != nil {
				t.Fatalf("the step was built; note %q", note)
			}
			for _, want := range tc.says {
				if !strings.Contains(note, want) {
					t.Errorf("the note does not say %q:\n%s", want, note)
				}
			}
			if got := StepGateOf(note); got != tc.gate {
				t.Errorf("StepGateOf = %q, want %q, for %q", got, tc.gate, note)
			}
		})
	}
}

// A refusal stays short however much was wrong: it names the first few and counts the rest.
func TestAssemble_ARefusedStepsNoteIsBounded(t *testing.T) {
	var features []string
	for i := 0; i < 50; i++ {
		features = append(features, fmt.Sprintf(`{"id":"hole-%d","op":"cut","of":"plate","with":["ghost-%d"]}`, i, i))
	}
	c := &Conversation{client: &scriptedStub{replies: []string{
		`{"speech":"holes","prototype_edit":{"patch":{"features":[` + strings.Join(features, ",") + `]}}}`}}}
	_, note := c.buildOneStep(context.Background(), onePlate(), "a car", buildTask{Name: "Holes", What: "holes"}, 2, 3)
	if !strings.Contains(note, "and 47 more") {
		t.Errorf("fifty added faults were not counted after the first three:\n%s", note)
	}
	if n := utf8.RuneCountInString(note); n > maxStepNoteDetail+100 {
		t.Errorf("the note is %d characters; it must not grow with the model", n)
	}

	huge := `{"speech":"frame","prototype":{"name":"c","parts":[` + strings.Repeat(`{"id":"a","shape":"box"},`, 2000) + `{"id": oops}]}}`
	c = &Conversation{client: &finishStub{reply: huge, finish: "stop"}}
	_, note = c.buildOneStep(context.Background(), &geometry.Document{}, "a car", buildTask{Name: "Frame", What: "frame"}, 1, 3)
	if !strings.Contains(note, gateUnreadable) {
		t.Fatalf("an unparseable reply was not called unreadable: %.300s", note)
	}
	if n := utf8.RuneCountInString(note); n > maxStepNoteDetail+100 {
		t.Errorf("an unreadable reply of %d bytes made a note of %d characters", len(huge), n)
	}
}

// ‼️ The first step is asked for a tree with a root and interfaces, not flat parts,
// and told that this pass is the build.
//
// It used to be shown only {"parts": [...]} beside a contract that tells a model
// asked for a car to set "build_in_passes" and leave "prototype" out. The build-goal
// run's whole car came out flat with nothing to attach "at".
func TestAssemble_TheFirstStepIsAskedForATreeWithInterfaces(t *testing.T) {
	stub := &scriptedStub{}
	c := &Conversation{client: stub}
	c.buildOneStep(context.Background(), &geometry.Document{Name: "car", Units: "mm"}, "a car",
		buildTask{Name: "Chassis", What: "the chassis", Assembly: "chassis"}, 1, 5)
	if len(stub.systems) != 1 {
		t.Fatalf("want one call, got %d", len(stub.systems))
	}
	first := stub.systems[0]
	for _, want := range []string{`"root" assembly for the whole object`, `"interfaces"`, `Not
  "build_in_passes": this pass IS the build`, `Not "prototype_edit"`, "strict JSON", `"root": "..."`} {
		if !strings.Contains(first, want) {
			t.Errorf("the first step is not told %q", want)
		}
	}

	stub = &scriptedStub{}
	c = &Conversation{client: stub}
	c.buildOneStep(context.Background(), onePlate(), "a car", buildTask{Name: "Wheels", What: "wheels"}, 2, 5)
	later := stub.systems[0]
	for _, want := range []string{`"at"`, `"root_children"`, "REPLACED WHOLE", `not "build_in_passes"`, "strict JSON"} {
		if !strings.Contains(later, want) {
			t.Errorf("a later step is not told %q", want)
		}
	}
}

// Every step is told, by id, the assembly its plan says it builds.
func TestAssemble_AStepIsToldTheAssemblyItBuilds(t *testing.T) {
	stub := &scriptedStub{}
	c := &Conversation{client: stub}
	c.buildOneStep(context.Background(), &geometry.Document{Name: "car", Units: "mm"}, "a car",
		buildTask{Name: "Chassis", What: "the chassis", Assembly: "chassis"}, 1, 5)
	if len(stub.asked) != 1 || !strings.Contains(stub.asked[0], `This step builds the assembly "chassis".`) {
		t.Errorf("the step was not told the assembly it builds:\n%v", stub.asked)
	}
}

// carWithChassis is a tree whose root places a chassis.
func carWithChassis() *Prototype {
	return &geometry.Document{Name: "car", Units: "mm", Root: "car",
		Definitions: []geometry.Part{{ID: "rail", Shape: "box", Size: map[string]float64{"width": 50, "height": 80, "depth": 4000}}},
		Assemblies: []geometry.Assembly{
			{ID: "chassis", Children: []geometry.Child{{ID: "left-rail", Ref: "rail", Position: []float64{-400, 0, 0}},
				{ID: "right-rail", Ref: "rail", Position: []float64{400, 0, 0}}}},
			{ID: "car", Children: []geometry.Child{{ID: "chassis", Ref: "chassis"}}}}}
}

// ‼️ A step that patches the root with only its own child drops the rest, and says so.
//
// Reproduced offline 2026-09-15: a brakes step replaced the root whole, the chassis
// placement vanished, and the step's note was empty, because vanishedParts compared
// two trees' top-level part lists, which are both empty.
func TestAssemble_AStepThatDropsAPlacementSaysSo(t *testing.T) {
	stub := &scriptedStub{replies: []string{`{"speech":"brakes","prototype_edit":{"patch":{
	  "definitions":[{"id":"disc","shape":"cylinder","size":{"radius":150,"height":30}}],
	  "assemblies":[{"id":"brakes","children":[{"id":"disc","ref":"disc","position":[800,0,1400]}]},
	                {"id":"car","children":[{"id":"brakes","ref":"brakes"}]}]}}}`}}
	c := &Conversation{client: stub}
	next, note := c.buildOneStep(context.Background(), carWithChassis(), "a car",
		buildTask{Name: "Brakes", What: "brakes", Assembly: "brakes"}, 5, 7)
	if next == nil {
		t.Fatalf("the step was refused: %q", note)
	}
	if !strings.Contains(note, "removed chassis (2 parts)") {
		t.Errorf("a step that dropped the chassis did not say so: %q", note)
	}
}

// A step creating an assembly is shown what the root already places, so it can keep it.
func TestSubtreeModel_ANewAssemblyIsShownWhatTheRootAlreadyPlaces(t *testing.T) {
	view := SubtreeModel(carWithChassis(), "brakes")
	if !strings.Contains(view, `"new":true`) {
		t.Fatalf("brakes is not a new assembly in the view: %s", view)
	}
	if !strings.Contains(view, `"root_children":[{"id":"chassis","ref":"chassis"}]`) {
		t.Errorf("a step creating an assembly is not shown the root's children: %s", view)
	}
	if strings.Contains(view, "left-rail") {
		t.Errorf("the view shows what the chassis contains, which the step does not need: %s", view)
	}
}
