package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
)

// Fences for what the first live car-quality run (2026-09-15) found.

// ‼️ An expression typed into a DEFINITION's size is read, not the reason the reply is lost.
//
// The live run's first step, a chassis written as a tree, lost everything to
// "size": {"width": "beam_width"} on a definition: the dimension repair read
// prototype.parts only, and a tree's parts are its definitions.
// docs/bugfix/2026-09-15-an-expression-in-a-definition-lost-the-whole-reply.md
func TestParseReply_ReadsAnExpressionInADefinitionsSize(t *testing.T) {
	reply, err := parseReply(&llm.Response{FinishReason: "stop", Content: `{"speech":"frame","prototype":{"name":"c","units":"mm",
	  "parameters":[{"name":"beam_width","value":80,"unit":"mm","how":"chosen","source":""}],
	  "definitions":[{"id":"rail","shape":"box","size":{"width":"beam_width","height":"120","depth":4000}}],
	  "assemblies":[{"id":"chassis","children":[{"id":"left-rail","ref":"rail"}]}],"root":"chassis"}}`})
	if err != nil || reply.Prototype == nil || len(reply.Prototype.Definitions) != 1 {
		t.Fatalf("the reply was not read: %v %+v", err, reply)
	}
	def := reply.Prototype.Definitions[0]
	if def.SizeFrom["width"] != "beam_width" || def.Size["height"] != 120 {
		t.Errorf("the definition's size was not read as an expression and a number: size %v, size_from %v", def.Size, def.SizeFrom)
	}
	if reply.Repaired == "" {
		t.Error("the reply was rewritten and nobody was told")
	}
}

// The same reading for an edit's patch, which is how every later step arrives.
func TestParseReply_ReadsAnExpressionInAnEditsDefinitionAndPart(t *testing.T) {
	reply, err := parseReply(&llm.Response{FinishReason: "stop", Content: `{"speech":"brakes","prototype_edit":{"patch":{
	  "definitions":[{"id":"disc","shape":"cylinder","size":{"radius":"disc_radius","height":28}}],
	  "parts":[{"id":"line","shape":"box","size":{"width":4,"height":4,"depth":"line_length"},"position":[0,"line_y",0]}]}}}`})
	if err != nil || reply.PrototypeEdit == nil || reply.PrototypeEdit.Patch == nil {
		t.Fatalf("the edit was not read: %v %+v", err, reply)
	}
	patch := reply.PrototypeEdit.Patch
	if patch.Definitions[0].SizeFrom["radius"] != "disc_radius" {
		t.Errorf("the patch's definition size_from is %v", patch.Definitions[0].SizeFrom)
	}
	if patch.Parts[0].SizeFrom["depth"] != "line_length" || patch.Parts[0].PositionFrom["y"] != "line_y" {
		t.Errorf("the patch's part is size_from %v position_from %v", patch.Parts[0].SizeFrom, patch.Parts[0].PositionFrom)
	}
}

// ‼️ A step that builds its assembly and does not place it has it placed from the root, and says so.
func TestAssemble_ANewAssemblyTheStepDidNotPlaceIsPlacedFromTheRoot(t *testing.T) {
	before := carWithChassis()
	stub := &scriptedStub{replies: []string{`{"speech":"brakes","prototype_edit":{"patch":{
	  "definitions":[{"id":"disc","shape":"cylinder","size":{"radius":150,"height":30}}],
	  "assemblies":[{"id":"brakes","children":[{"id":"disc","ref":"disc","position":[800,0,1400]}]}]}}}`}}
	c := &Conversation{client: stub}
	next, note := c.buildOneStep(context.Background(), before, "a car",
		buildTask{Name: "Brakes", What: "brakes", Assembly: "brakes"}, 6, 8)
	if next == nil {
		t.Fatalf("the step was refused: %q", note)
	}
	var placed []string
	for _, p := range next.PlacedParts() {
		placed = append(placed, p.ID)
	}
	if got := strings.Join(placed, ","); got != "chassis/left-rail,chassis/right-rail,brakes/disc" {
		t.Errorf("the car places %s; want the chassis and the brakes it just built", got)
	}
	if !strings.Contains(note, `FORGE placed it from the root "car"`) {
		t.Errorf("the step did not say FORGE placed its assembly: %q", note)
	}
	for _, a := range before.Assemblies {
		if a.ID == "car" && len(a.Children) != 1 {
			t.Errorf("placing the new assembly changed the model before the step: %+v", a.Children)
		}
	}
}

// A step that placed its own assembly, or built one the plan did not name, is left as it wrote it.
func TestAssemble_AnAssemblyTheStepPlacedItselfIsLeftAlone(t *testing.T) {
	d := carWithChassis()
	d.Assemblies = append(d.Assemblies, geometry.Assembly{ID: "brakes"})
	d.Assemblies[1].Children = append(d.Assemblies[1].Children, geometry.Child{ID: "stoppers", Ref: "brakes", Position: []float64{0, 5, 0}})
	if note := placeStepAssembly(d, "brakes"); note != "" || len(d.Assemblies[1].Children) != 2 {
		t.Errorf("an assembly the step placed was placed again: %q %+v", note, d.Assemblies[1].Children)
	}
	d = carWithChassis()
	d.Assemblies = append(d.Assemblies, geometry.Assembly{ID: "spare"})
	if note := placeStepAssembly(d, "brakes"); note != "" {
		t.Errorf("an assembly the plan did not name for this step was placed: %q", note)
	}
}
