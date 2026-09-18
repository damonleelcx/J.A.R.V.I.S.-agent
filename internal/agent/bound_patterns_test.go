package agent

import (
	"context"
	"math"
	"regexp"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
)

// Fences for a pattern's offsets and a polar pattern's angle written with a parameter's
// name (2026-09-17, bound patterns; geometry/pattern_binding.go).

// sweep is the angle in degrees between two points round the y axis.
func sweep(a, b []float64) float64 {
	cos := (a[0]*b[0] + a[2]*b[2]) / (math.Hypot(a[0], a[2]) * math.Hypot(b[0], b[2]))
	return math.Acos(math.Max(-1, math.Min(1, cos))) * 180 / math.Pi
}

// ‼️ The contract teaches the pattern fields geometry's table binds, in both the
// conversation and every build step, and each example it gives, read as a reply,
// stored and re-specified, moves its copies: what is taught is what is bound.
func TestTheContractTeachesPatternBindingsAsTheBinderReadsThem(t *testing.T) {
	guide := geometry.PatternBindingGuide()
	for name, contract := range map[string]string{"build": buildContract, "conversation": converseFraming} {
		if !strings.Contains(contract, guide) {
			t.Errorf("the %s contract does not carry the pattern binding guide", name)
		}
		for _, b := range geometry.PatternBindings() {
			if !strings.Contains(contract, `"`+b.Field+`" (`+b.Kind+`)`) || !strings.Contains(contract, `"`+b.From+`"`) {
				t.Errorf("the %s contract does not teach %s / %s", name, b.Field, b.From)
			}
		}
	}
	examples := regexp.MustCompile(`\{"kind": [^{}]*\}`).FindAllString(guide, -1)
	if len(examples) < 2 {
		t.Fatalf("the guide gives %d examples; want a step and an angle: %s", len(examples), guide)
	}
	for _, example := range examples {
		reply, err := parseReply(&llm.Response{FinishReason: "stop", Content: `{"speech":"p","prototype":{"name":"p","units":"mm",
		  "parameters":[{"name":"bolt_pitch","value":30,"unit":"mm","how":"chosen"},{"name":"fan_sweep","value":90,"unit":"deg","how":"chosen"}],
		  "definitions":[{"id":"bolt","shape":"cylinder","size":{"radius":3,"height":10}}],
		  "assemblies":[{"id":"a","children":[{"id":"c","ref":"bolt","position":[50,0,0],"pattern":` + example + `}]}],"root":"a"}}`})
		if err != nil || reply.Prototype == nil {
			t.Fatalf("the contract's example %s was not read: %v", example, err)
		}
		doc := settleDocument(reply.Prototype)
		moved := storedAndRespecified(t, doc, map[string]float64{"bolt_pitch": 40, "fan_sweep": 120})
		was, now := doc.PlacedParts(), moved.PlacedParts()
		if len(was) < 2 || len(was) != len(now) {
			t.Fatalf("the example %s placed %d copies, then %d", example, len(was), len(now))
		}
		last := len(was) - 1
		if near(now[last].Position, was[last].Position...) {
			t.Errorf("the contract's example %s did not follow its parameter: %s stayed at %v", example, was[last].ID, was[last].Position)
		}
	}
}

// Every build step, the first and the later ones, is told which pattern fields take a
// parameter's name, in the words geometry's table renders.
func TestAssemble_AStepIsTaughtToBindAPatternsStep(t *testing.T) {
	stub := &scriptedStub{}
	c := &Conversation{client: stub}
	c.buildOneStep(context.Background(), &geometry.Document{Name: "car", Units: "mm"}, "a car",
		buildTask{Name: "Chassis", What: "the chassis", Assembly: "chassis"}, 1, 3)
	c.buildOneStep(context.Background(), carOnSuspension(), "a car", buildTask{Name: "Brakes", What: "brakes", Assembly: "brakes"}, 2, 3)
	if len(stub.systems) != 2 {
		t.Fatalf("want two steps asked, got %d", len(stub.systems))
	}
	fields := `in a child's pattern's "offset", "row_offset", "column_offset" or "angle"`
	for i, sys := range stub.systems {
		if !strings.Contains(sys, geometry.PatternBindingGuide()) || !strings.Contains(sys, fields) {
			t.Errorf("step %d is not taught to bind a pattern's step", i+1)
		}
	}
	if !strings.Contains(stub.systems[1], `and in a child's pattern's "offset", "row_offset", "column_offset" or "angle",
  which FORGE keeps bound to the parameters`) {
		t.Errorf("a later step's binding rule does not name a pattern's fields")
	}
}

const patternStepReply = `{"speech":"brakes","prototype_edit":{"patch":{
  "definitions":[{"id":"stud","shape":"cylinder","size":{"radius":5,"height":10}}],
  "assemblies":[{"id":"brakes","children":[` + "%s" + `]}]}}}`

// ‼️ A step's pattern whose step and angle name the model's parameters keeps them as its
// bindings: stored and re-specified, every copy follows, and none is called a literal.
func TestAssemble_AStepsPatternWrittenWithAParameterFollowsIt(t *testing.T) {
	base := carOnSuspension()
	base.Parameters = append(base.Parameters, geometry.Parameter{Name: "caliper_sweep", Value: 90, Unit: "deg", How: "chosen"})
	reply := strings.Replace(patternStepReply, "%s",
		`{"id":"bolt","ref":"stud","pattern":{"kind":"linear","count":3,"offset":[0,0,"half_wheelbase / 10"]}},
		 {"id":"pad","ref":"stud","position":[200,0,0],"pattern":{"kind":"polar","count":3,"about":"y","angle":"caliper_sweep"}},
		 {"id":"pin","ref":"stud","position":[0,500,0],"pattern":{"kind":"grid","rows":2,"columns":2,
		   "row_offset":[0,0,"half_track / 8"],"column_offset":["half_track / 4",0,0]}}`, 1)
	c := &Conversation{client: &scriptedStub{replies: []string{reply}}}
	next, note := c.buildOneStep(context.Background(), base, "a car", buildTask{Name: "Brakes", What: "brakes", Assembly: "brakes"}, 4, 5)
	if next == nil {
		t.Fatalf("the step was refused: %q", note)
	}
	bolt, _ := placementIn(next, "brakes", "bolt")
	pad, _ := placementIn(next, "brakes", "pad")
	pin, _ := placementIn(next, "brakes", "pin")
	if bolt.Pattern == nil || bolt.Pattern.OffsetFrom["z"] != "half_wheelbase / 10" || pad.Pattern == nil || pad.Pattern.AngleFrom != "caliper_sweep" ||
		pin.Pattern == nil || pin.Pattern.RowOffsetFrom["z"] != "half_track / 8" || pin.Pattern.ColumnOffsetFrom["x"] != "half_track / 4" {
		t.Fatalf("the pattern's expressions were not kept as its bindings: %+v %+v %+v", bolt.Pattern, pad.Pattern, pin.Pattern)
	}
	if !strings.Contains(note, childPositionNote) || strings.Contains(note, "as the number a parameter already holds") {
		t.Errorf("the note does not say the binding was kept, or calls a bound pattern a literal: %q", note)
	}
	if got := placedAt(t, next, "brakes/bolt-3"); !near(got, 0, 0, 270) {
		t.Errorf("bolt-3 is at %v; want (0, 0, 270)", got)
	}
	moved := storedAndRespecified(t, next, map[string]float64{"half_wheelbase": 1400, "half_track": 900, "caliper_sweep": 120})
	if got := placedAt(t, moved, "brakes/bolt-3"); !near(got, 0, 0, 280) {
		t.Errorf("after half_wheelbase = 1400 bolt-3 is at %v; want (0, 0, 280)", got)
	}
	if got := placedAt(t, moved, "brakes/pin-4"); !near(got, 225, 500, 112.5) {
		t.Errorf("after half_track = 900 pin-4 is at %v; want (225, 500, 112.5)", got)
	}
	if a := sweep(placedAt(t, moved, "brakes/pad-1"), placedAt(t, moved, "brakes/pad-3")); math.Abs(a-120) > 1e-6 {
		t.Errorf("after caliper_sweep = 120 the pads sweep %v degrees", a)
	}
}

// ‼️ A step that types a pattern's step as the number a parameter holds, exactly or to
// the digits written, is told which parameter and where to write the name; a bound step
// is never counted.
func TestAssemble_AStepThatRetypesAParameterInAPatternIsTold(t *testing.T) {
	step := func(base *Prototype, children string) string {
		t.Helper()
		c := &Conversation{client: &scriptedStub{replies: []string{strings.Replace(patternStepReply, "%s", children, 1)}}}
		next, note := c.buildOneStep(context.Background(), base, "a car", buildTask{Name: "Brakes", What: "brakes", Assembly: "brakes"}, 4, 5)
		if next == nil {
			t.Fatalf("the step was refused: %q", note)
		}
		return note
	}
	exact := step(carOnSuspension(), `{"id":"bolt","ref":"stud","pattern":{"kind":"linear","count":3,"offset":[0,0,1350]}},
	  {"id":"pin","ref":"stud","position":[0,500,0],"pattern":{"kind":"grid","rows":2,"columns":2,"row_offset":[0,0,800],"column_offset":[1350,0,0]}},
	  {"id":"pad","ref":"stud","position":[0,-400,0],"pattern":{"kind":"linear","count":2,"offset":[0,0,"half_track"]}}`)
	want := `This step typed 3 position(s) as the number a parameter already holds: ` +
		`1350 is half_wheelbase (brakes/bolt offset z = 1350, brakes/pin column_offset x = 1350); ` +
		`800 is half_track (brakes/pin row_offset z = 800). ` +
		`Write the parameter's name so the position follows it: "position_from": {"z": "half_wheelbase"} on a part or a definition, ` +
		`and "half_wheelbase" in a child's or an interface's "position", which FORGE keeps bound to the parameters. ` +
		`A pattern's step takes the name the same way, in its "offset", "row_offset", "column_offset" or "angle".`
	if !strings.Contains(exact, want) {
		t.Errorf("the note does not name the pattern's retyped steps:\n got %s\nwant %s", exact, want)
	}

	rounded := carOnSuspension()
	rounded.Derived = []geometry.Derived{{Name: "rotor_pitch", Expression: "half_wheelbase * 2 / 7", Why: "seven rotors"}}
	note := step(rounded, `{"id":"bolt","ref":"stud","pattern":{"kind":"linear","count":3,"offset":[0,0,385.71]}},
	  {"id":"pin","ref":"stud","position":[0,500,0],"pattern":{"kind":"grid","rows":2,"columns":2,"row_offset":[0,0,385.7],"column_offset":[386,0,0]}}`)
	for _, part := range []string{"as the number a parameter already holds to the digits written",
		"385.714286 is rotor_pitch (brakes/bolt offset z = 385.71, brakes/pin row_offset z = 385.7, brakes/pin column_offset x = 386)"} {
		if !strings.Contains(note, part) {
			t.Errorf("a pattern's step rounded from a parameter was not named: missing %q in\n%s", part, note)
		}
	}

	bound := step(carOnSuspension(), `{"id":"bolt","ref":"stud","pattern":{"kind":"linear","count":3,"offset":["half_track",0,"half_wheelbase"]}},
	  {"id":"pin","ref":"stud","position":[0,500,0],"pattern":{"kind":"grid","rows":2,"columns":2,"row_offset":[0,0,"half_track"],"column_offset":["half_wheelbase",0,0]}}`)
	if strings.Contains(bound, "a parameter already holds") {
		t.Errorf("bound pattern steps were counted as literals: %s", bound)
	}
}
