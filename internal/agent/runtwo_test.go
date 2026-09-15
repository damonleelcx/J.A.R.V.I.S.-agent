package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
)

// Fences for what the second live car-quality run (2026-09-15) found, and the build
// contract's material paragraph.
// docs/bugfix/2026-09-15-a-build-steps-edit-replaced-the-models-root.md

// ‼️ A position on a child written as an expression is read at its value, not the
// reason the whole reply is lost.
func TestParseReply_ReadsAnExpressionInAChildsPositionAtItsValue(t *testing.T) {
	reply, err := parseReply(&llm.Response{FinishReason: "stop", Content: `{"speech":"frame","prototype":{"name":"c","units":"mm",
	  "parameters":[{"name":"half_wheelbase","value":1300,"unit":"mm","how":"chosen","source":""}],
	  "definitions":[{"id":"rail","shape":"box","size":{"width":50,"height":80,"depth":400}}],
	  "assemblies":[{"id":"chassis","interfaces":[{"id":"front","position":["half_wheelbase",0,0]}],
	    "children":[{"id":"front-cross","ref":"rail","position":["-half_wheelbase + 200","12",0]}]}],"root":"chassis"}}`})
	if err != nil || reply.Prototype == nil || len(reply.Prototype.Assemblies) != 1 {
		t.Fatalf("the reply was not read: %v %+v", err, reply)
	}
	a := reply.Prototype.Assemblies[0]
	if got := a.Children[0].Position; len(got) != 3 || got[0] != -1100 || got[1] != 12 || got[2] != 0 {
		t.Errorf("the child is at %v; -half_wheelbase + 200 over half_wheelbase = 1300 is -1100, and \"12\" is 12", got)
	}
	if got := a.Interfaces[0].Position; len(got) != 3 || got[0] != 1300 {
		t.Errorf("the interface is at %v, want x = 1300", got)
	}
	if !strings.Contains(reply.Repaired, `no "position_from"`) || !strings.Contains(reply.Repaired, "will not follow") {
		t.Errorf("the reader is not told the placement lost its binding: %q", reply.Repaired)
	}
}

// An expression FORGE cannot work out is not guessed: the reply fails as before, by name.
func TestParseReply_AChildPositionItCannotEvaluateIsNotGuessed(t *testing.T) {
	resp := &llm.Response{FinishReason: "stop", Content: `{"speech":"frame","prototype":{"name":"c","units":"mm",
	  "definitions":[{"id":"rail","shape":"box","size":{"width":50,"height":80,"depth":400}}],
	  "assemblies":[{"id":"chassis","children":[{"id":"front-cross","ref":"rail","position":["no_such_parameter * 2",0,0]}]}],"root":"chassis"}}`}
	reply, _ := parseReply(resp)
	if reply.Prototype != nil {
		t.Fatalf("a position over an unknown name was read as %v", reply.Prototype.Assemblies[0].Children[0].Position)
	}
	if !strings.Contains(unreadableDetail(resp), "position") {
		t.Errorf("the refusal does not name the position: %q", unreadableDetail(resp))
	}
}

// ‼️ A build step's edit cannot replace the model's root; what it named as root is placed.
func TestAssemble_AStepsEditDoesNotReplaceTheModelsRoot(t *testing.T) {
	for _, tc := range []struct{ name, focus, sent, want string }{
		{"the root it sent is the step's assembly", "brakes", "brakes", "chassis/left-rail,chassis/right-rail,brakes/disc"},
		{"the root it sent is another assembly", "interior", "brakes", "chassis/left-rail,chassis/right-rail,brakes/disc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &scriptedStub{replies: []string{`{"speech":"brakes","prototype_edit":{"patch":{
			  "definitions":[{"id":"disc","shape":"cylinder","size":{"radius":150,"height":30}}],
			  "assemblies":[{"id":"brakes","children":[{"id":"disc","ref":"disc"}]}],"root":"` + tc.sent + `"}}}`}}
			c := &Conversation{client: stub}
			next, note := c.buildOneStep(context.Background(), carWithChassis(), "a car",
				buildTask{Name: "Brakes", What: "brakes", Assembly: tc.focus}, 6, 8)
			if next == nil {
				t.Fatalf("the step was refused: %q", note)
			}
			if next.Root != "car" {
				t.Errorf("the step replaced the root with %q", next.Root)
			}
			var placed []string
			for _, p := range next.PlacedParts() {
				placed = append(placed, p.ID)
			}
			if got := strings.Join(placed, ","); got != tc.want {
				t.Errorf("the car places %s; want %s", got, tc.want)
			}
			if !strings.Contains(note, `as the model's root`) || strings.Contains(note, "removed") {
				t.Errorf("the note does not say the root was kept, or says something was removed: %q", note)
			}
		})
	}
	if !strings.Contains(geometryContract, `"root": "ONLY when the model has no root yet`) {
		t.Error("the contract's patch does not say root is only for a model without one")
	}
}

// ‼️ Every build step reads the material paragraph whole: the finishes and the density rule.
func TestAssemble_EveryStepIsTaughtFinishesAndDensity(t *testing.T) {
	stub := &scriptedStub{}
	c := &Conversation{client: stub}
	c.buildOneStep(context.Background(), &geometry.Document{Name: "car", Units: "mm"}, "a car",
		buildTask{Name: "Chassis", What: "the chassis", Assembly: "chassis"}, 1, 3)
	c.buildOneStep(context.Background(), onePlate(), "a car", buildTask{Name: "Wheels", What: "wheels"}, 2, 3)
	if len(stub.systems) != 2 {
		t.Fatalf("want two steps asked, got %d", len(stub.systems))
	}
	for i, sys := range stub.systems {
		for _, want := range []string{geometry.FinishGuide(),
			"KILOGRAMS PER CUBIC METRE", "FORGE reports the model's MASS only when every part has a density"} {
			if !strings.Contains(sys, want) {
				t.Errorf("step %d is not taught %.60q", i+1, want)
			}
		}
		// The finishes complete the sentence that introduces them, not some later paragraph.
		if light, list := strings.Index(sys, `"finish" is only how it catches light:`), strings.Index(sys, geometry.FinishGuide()); light < 0 || list < light || list-light > 80 {
			t.Errorf("step %d's material paragraph stops at \"catches light:\" without its finishes", i+1)
		}
	}
}
