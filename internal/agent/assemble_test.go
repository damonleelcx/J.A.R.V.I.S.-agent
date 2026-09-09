package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
)

// scriptedStub answers each call with the next reply in a list, and records
// every prompt it was sent.
type scriptedStub struct {
	replies []string
	asked   []string
	systems []string
	n       int
}

func (s *scriptedStub) Complete(_ context.Context, req llm.Request) (*llm.Response, error) {
	var sys string
	for _, m := range req.Messages {
		if m.Role == llm.User {
			s.asked = append(s.asked, m.Content)
		}
		if m.Role == llm.System {
			sys = m.Content
		}
	}
	s.systems = append(s.systems, sys)
	r := `{"speech":"done"}`
	if s.n < len(s.replies) {
		r = s.replies[s.n]
	}
	s.n++
	return &llm.Response{Content: r, FinishReason: "stop"}, nil
}

// No vision model, deliberately. repairIfItLooksWrong runs inside every pass and
// makes a real call whenever one is configured — with a scripted client that
// consumes the next scripted reply, so the pass after it is answered with
// somebody else's JSON and the fence measures the harness rather than the loop.
// The visual check has its own live test, which is the only thing that can
// actually judge it.
func (s *scriptedStub) ModelFor(r llm.Role) string {
	if r == llm.RoleVision {
		return ""
	}
	return "scripted"
}

func wholeDoc(id, name string) string {
	return `{"speech":"built ` + name + `","prototype":{"name":"m","units":"mm","parts":[
	  {"id":"` + id + `","name":"` + name + `","shape":"box","size":{"width":100,"height":100,"depth":100}}]}}`
}

func addPart(id, name string) string {
	return `{"speech":"added ` + name + `","prototype_edit":{"patch":{"parts":[
	  {"id":"` + id + `","name":"` + name + `","shape":"box","size":{"width":100,"height":100,"depth":100}}]}}}`
}

// A build adds each step to what the last one produced.
//
// # What this closes
//
// One reply must hold the entire model, so complexity is capped by a single
// generation — and it frays well below that cap: a 13-part car lost its spoiler
// in 2 runs of 5. This is the loop that lifts the cap, and the property that
// makes it worth anything is that step 5 still has steps 1 to 4 in it.
func TestAssemble_EachStepKeepsTheOnesBefore(t *testing.T) {
	stub := &scriptedStub{replies: []string{
		`{"steps":[{"name":"chassis","what":"the chassis"},
		           {"name":"wheels","what":"four wheels"},
		           {"name":"body","what":"the body"}]}`,
		wholeDoc("chassis", "Chassis"),
		addPart("wheels", "Wheels"),
		addPart("body", "Body"),
	}}
	c := &Conversation{client: stub}

	var seen []BuildStep
	doc, _, err := c.assemble(context.Background(), "a car", nil,
		func(s BuildStep) error { seen = append(seen, s); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Parts) != 3 {
		t.Fatalf("the finished model has %d parts; three steps each added one, so a step "+
			"dropped what came before it: %+v", len(doc.Parts), doc.Parts)
	}
	if len(seen) != 3 {
		t.Errorf("reported %d steps of 3; a person watching a long build needs to see it move", len(seen))
	}
	// Each pass must be SHOWN what exists, or it cannot position anything
	// against it — and will place a wheel where the chassis is not.
	last := stub.asked[len(stub.asked)-1]
	if !strings.Contains(last, "Chassis") || !strings.Contains(last, "Wheels") {
		t.Errorf("the last pass was not shown the parts already built, so it is positioning "+
			"blind.\nprompt: %.400s", last)
	}
}

// A pass that would break the model is refused, and the work so far survives.
func TestAssemble_ABadPassDoesNotDestroyTheGoodOnes(t *testing.T) {
	// Step 2 replaces the chassis with a sweep whose outline is two points —
	// a line, which encloses nothing and cannot be built.
	broken := `{"speech":"reshaping","prototype_edit":{"patch":{"parts":[
	  {"id":"chassis","name":"Chassis","shape":"sweep",
	   "profile":[{"x":0,"y":0},{"x":10,"y":0}],
	   "path":[{"x":0,"y":0,"z":0},{"x":0,"y":0,"z":100}]}]}}}`
	stub := &scriptedStub{replies: []string{
		`{"steps":[{"name":"chassis","what":"the chassis"},{"name":"reshape","what":"reshape it"}]}`,
		wholeDoc("chassis", "Chassis"),
		broken,
		// the repair pass is asked next and returns the same broken thing
		broken, broken,
	}}
	c := &Conversation{client: stub}

	doc, notes, err := c.assemble(context.Background(), "a car", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Faults()) != 0 {
		t.Errorf("a pass that could not be built was accepted, so every later pass now "+
			"builds on a broken model: %+v", doc.Faults())
	}
	if len(doc.Parts) != 1 {
		t.Errorf("the good pass was lost along with the bad one: %d parts", len(doc.Parts))
	}
	if !strings.Contains(strings.Join(notes, " "), "left out") {
		t.Errorf("a step was silently skipped. The reader asked for it and it is not there.\nnotes: %v", notes)
	}
}

// A one-step plan is not a build, and says so rather than paying for a loop.
func TestAssemble_RefusesWhatDoesNotNeedPlanning(t *testing.T) {
	stub := &scriptedStub{replies: []string{`{"steps":[{"name":"it","what":"make it"}]}`}}
	c := &Conversation{client: stub}
	if _, _, err := c.assemble(context.Background(), "a washer", nil, nil); err == nil {
		t.Error("a single-step plan was run as a build. That is an ordinary turn with " +
			"extra latency and an extra model call")
	}
}

// A step that drops a part says so — the same standard a single turn is held to.
func TestAssemble_SaysWhatAStepRemoved(t *testing.T) {
	base := &geometry.Document{Name: "car", Units: "mm", Parts: []geometry.Part{
		{ID: "keep", Name: "Keeper", Shape: "box", Size: map[string]float64{"width": 10, "height": 10, "depth": 10}},
	}}
	stub := &scriptedStub{replies: []string{
		`{"steps":[{"name":"a","what":"add a"},{"name":"b","what":"add b"}]}`,
		`{"speech":"replacing","prototype":` + mustJSONInner(t, &geometry.Document{
			Name: "car", Units: "mm", Parts: []geometry.Part{{ID: "other", Name: "Other", Shape: "box",
				Size: map[string]float64{"width": 10, "height": 10, "depth": 10}}}}) + `}`,
		addPart("b", "B"),
	}}
	c := &Conversation{client: stub}
	_, notes, err := c.assemble(context.Background(), "a car", base, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(notes, " "), "Keeper") {
		t.Errorf("a step deleted a part and nothing said so.\nnotes: %v", notes)
	}
}

var _ = json.Marshal

// Every pass is told what geometry looks like.
//
// # What this closes
//
// The step prompts were written from scratch and never carried the document
// format. Measured on the first live eight-step build: the model answered with a
// schema it invented — {"type": "box_beam", "dimensions": {"length": 4200},
// "position": {"x": -400}} instead of shape/size/position — and SEVEN OF EIGHT
// steps produced no geometry at all. The one that worked did so by luck.
//
// The failure is silent in the worst way: each step reports "produced no
// geometry" and the build finishes, so it reads as a model that could not do the
// job rather than a prompt that never asked properly.
func TestAssemble_EveryPassCarriesTheContract(t *testing.T) {
	stub := &scriptedStub{replies: []string{
		`{"steps":[{"name":"a","what":"first"},{"name":"b","what":"second"}]}`,
		wholeDoc("a", "A"),
		addPart("b", "B"),
	}}
	c := &Conversation{client: stub}
	if _, _, err := c.assemble(context.Background(), "a car", nil, nil); err != nil {
		t.Fatal(err)
	}
	// The plan call is allowed to be contract-free: it produces step names, not
	// geometry. Every call AFTER it must carry the vocabulary.
	if len(stub.systems) < 3 {
		t.Fatalf("expected a plan call and two build passes, saw %d", len(stub.systems))
	}
	for i, sys := range stub.systems[1:] {
		for _, want := range []string{`"shape"`, `"size"`, `"position"`} {
			if !strings.Contains(sys, want) {
				t.Errorf("build pass %d was asked for geometry without being told what %s is. "+
					"It will answer with a schema it invented and the pass will produce nothing.",
					i+1, want)
			}
		}
	}
}

// The build loop is REACHABLE from a turn.
//
// # What this closes
//
// The loop was built, measured live at 60 parts, and fenced — and nothing in the
// shipping binary called it. Only the test helper did, so the linker dropped it
// and a deploy verified the running binary did not contain the planner's prompt
// at all. A feature with no producer: built, tested, and absent.
//
// This drives a real turn and asserts geometry came back from the passes, so the
// wiring cannot be removed without something going red.
func TestAssemble_ATurnCanAskForABuild(t *testing.T) {
	stub := &scriptedStub{replies: []string{
		// The turn itself: no geometry, asks to build in passes.
		`{"speech":"I'll build this a subsystem at a time.","build_in_passes":true}`,
		`{"steps":[{"name":"a","what":"first"},{"name":"b","what":"second"}]}`,
		wholeDoc("a", "A"),
		addPart("b", "B"),
	}}
	c := &Conversation{client: stub}

	reply, err := c.Respond(context.Background(), "", nil, "a sports car", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reply.Prototype == nil {
		t.Fatal("the model asked for a multi-pass build and the turn came back with no " +
			"geometry. The build loop is not connected to anything")
	}
	if len(reply.Prototype.Parts) != 2 {
		t.Errorf("the build produced %d parts; two passes each added one", len(reply.Prototype.Parts))
	}
	if reply.BuildInPasses {
		t.Error("the flag was not consumed, so a later reader would run the build again")
	}
}

// A reply that already carried geometry is left alone.
func TestAssemble_DoesNotBuildOverAnAnswerItAlreadyGave(t *testing.T) {
	stub := &scriptedStub{replies: []string{
		`{"speech":"here","build_in_passes":true,"prototype":{"name":"m","units":"mm","parts":[
		  {"id":"x","name":"X","shape":"box","size":{"width":10,"height":10,"depth":10}}]}}`,
	}}
	c := &Conversation{client: stub}
	reply, err := c.Respond(context.Background(), "", nil, "a bracket", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(reply.Prototype.Parts) != 1 || reply.Prototype.Parts[0].ID != "x" {
		t.Errorf("a reply that already answered was rebuilt over: %+v", reply.Prototype.Parts)
	}
}
