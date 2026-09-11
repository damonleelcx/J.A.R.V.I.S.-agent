package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// boundCar is faultyCar with the body's width bound to a parameter, and the
// number typed beside the expression set to whatever the caller says.
func boundCar(typedWidth float64) *Prototype {
	d := faultyCar()
	d.Parameters = []geometry.Parameter{{Name: "body_width", Value: 2000, Unit: "mm"}}
	d.Parts[0].Size["width"] = typedWidth
	d.Parts[0].SizeFrom = map[string]string{"width": "body_width"}
	return d
}

func turnJSON(t *testing.T, speech string, d *Prototype) string {
	t.Helper()
	b, err := json.Marshal(struct {
		Speech    string     `json:"speech"`
		Prototype *Prototype `json:"prototype"`
	}{speech, d})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// boundBody is a sound, one-part model whose width is bound to a parameter, with
// the number typed beside the expression set to whatever the caller says.
func boundBody(typedWidth float64) *Prototype {
	return &geometry.Document{
		Name: "Car", Units: "mm",
		Parameters: []geometry.Parameter{{Name: "body_width", Value: 2000, Unit: "mm"}},
		Parts: []geometry.Part{{ID: "body", Name: "Body", Shape: "box",
			Size:     map[string]float64{"width": typedWidth, "height": 800, "depth": 4500},
			SizeFrom: map[string]string{"width": "body_width"},
			Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}},
		NotVerified: []string{"a concept, not an analysis"},
	}
}

// A document an EDIT produces is bound, like a whole prototype.
//
// validate() runs before resolveEdit on both reply paths, and resolveEdit sets
// the Prototype after it — so the document a revision produces from
// "prototype_edit" was never bound either.
func TestEdit_TheEditedDocumentIsBound(t *testing.T) {
	current := boundBody(2000)
	patched := boundBody(1900).Parts[0]
	body, err := json.Marshal(struct {
		Speech string         `json:"speech"`
		Edit   *geometry.Edit `json:"prototype_edit"`
	}{"made it wider", &geometry.Edit{Patch: &geometry.Document{Parts: []geometry.Part{patched}}}})
	if err != nil {
		t.Fatal(err)
	}
	c := &Conversation{client: &scriptedStub{replies: []string{string(body)}}}

	reply, err := c.Respond(context.Background(), "", nil, "make the body wider", "", current, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reply.Prototype == nil {
		t.Fatal("the edit produced no document")
	}
	if got := reply.Prototype.Parts[0].Size["width"]; got != 2000 {
		t.Errorf("the edited body is %v wide; its width is bound to body_width = 2000. The "+
			"edited document was never bound.", got)
	}
}

// A document a build PASS produces is bound, like a whole prototype.
//
// A pass goes through parseReply and resolveEdit and never through validate(),
// and the finished build is installed into the reply whole — so a multi-pass
// build was never bound at all.
func TestAssemble_APassIsSettled(t *testing.T) {
	// Two steps, because a one-step plan is not worth planning and no pass runs.
	// Both passes return a WHOLE document: an edit would be settled by
	// resolveEdit on its own, and then this could not tell whether the pass
	// itself settles what it installs.
	withWheel := boundBody(1900)
	withWheel.Parts = append(withWheel.Parts, geometry.Part{ID: "wheel", Name: "Wheel", Shape: "cylinder",
		Size:     map[string]float64{"radius": 350, "height": 250},
		Position: []float64{900, -400, 1400}, Rotation: []float64{0, 0, 90}})
	stub := &scriptedStub{replies: []string{
		`{"speech":"I'll build this a subsystem at a time.","build_in_passes":true}`,
		`{"steps":[{"name":"body","what":"the body"},{"name":"wheel","what":"a wheel"}]}`,
		turnJSON(t, "built the body", boundBody(1900)),
		turnJSON(t, "added a wheel", withWheel),
	}}
	c := &Conversation{client: stub}

	reply, err := c.Respond(context.Background(), "", nil, "a sports car", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reply.Prototype == nil || len(reply.Prototype.Parts) != 2 {
		t.Fatalf("the build did not produce the two-part model its passes built: %+v", reply.Prototype)
	}
	if got := reply.Prototype.Parts[0].Size["width"]; got != 2000 {
		t.Errorf("the built body is %v wide; its width is bound to body_width = 2000. A build "+
			"pass was never bound.", got)
	}
}

// A document a repair hands back is BOUND, like the one the model first wrote.
//
// # The failure this pins
//
// Bind is what turns `size_from` into the number the renderer, the tessellator
// and the measurement path read. It runs once per turn, in validate() — and the
// repairs run after validate() and replace the whole document. So a repaired
// body whose width follows body_width = 2000 kept the 1900 the repair typed
// beside the expression: the relationship said one thing and the part drew
// another, and nothing reported it, because the note Bind writes about exactly
// that disagreement is written by Bind.
func TestRepair_TheRepairedDocumentIsBound(t *testing.T) {
	first := boundCar(2000) // faulty wing; width typed in agreement with its expression
	if len(first.Faults()) == 0 {
		t.Fatal("the fixture is not faulty, so no repair runs and this proves nothing")
	}
	repaired := boundCar(1900) // the wing fixed; the body's typed width now stale
	repaired.Parts[1].Profile = []geometry.Point{{X: -400, Y: 0}, {X: 400, Y: 0}, {X: 400, Y: 40}, {X: -400, Y: 40}}
	if len(repaired.Faults()) != 0 {
		t.Fatalf("the repaired fixture still has faults, so the repair would be refused: %v", repaired.Faults())
	}

	stub := &scriptedStub{replies: []string{
		turnJSON(t, "added a wing", first),
		turnJSON(t, "", repaired),
	}}
	c := &Conversation{client: stub}

	reply, err := c.Respond(context.Background(), "", nil, "add a rear wing", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reply.Prototype == nil || len(reply.Prototype.Faults()) != 0 {
		t.Fatalf("the repair did not land, so this test is not measuring the repaired document: %+v", reply.Prototype)
	}
	if stub.n < 2 {
		t.Fatalf("the model was called %d time(s); the repair never ran", stub.n)
	}
	if got := reply.Prototype.Parts[0].Size["width"]; got != 2000 {
		t.Errorf("the repaired body is %v wide; its width is bound to body_width = 2000. The "+
			"repaired document was never bound, so the part draws the number the repair typed "+
			"rather than the relationship it states.", got)
	}
}
