package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
)

// repairStub answers the repair call with whatever the test supplies, and
// records what it was asked.
type repairStub struct {
	reply  string
	asked  string
	calls  int
	failed bool
}

func (r *repairStub) Complete(_ context.Context, req llm.Request) (*llm.Response, error) {
	r.calls++
	for _, m := range req.Messages {
		if m.Role == llm.User {
			r.asked = m.Content
		}
	}
	if r.failed {
		return nil, context.DeadlineExceeded
	}
	return &llm.Response{Content: r.reply, FinishReason: "stop"}, nil
}

func (r *repairStub) ModelFor(llm.Role) string { return "repair-stub" }

// A wing whose outline is a line: the exact fault seen on a real turn.
func faultyCar() *Prototype {
	return &geometry.Document{
		Name: "Car", Units: "mm",
		Parts: []geometry.Part{
			{ID: "body", Name: "Body", Shape: "box",
				Size: map[string]float64{"width": 1900, "height": 800, "depth": 4500}},
			{ID: "wing", Name: "Wing", Shape: "sweep",
				Profile: []geometry.Point{{X: -400, Y: 0}, {X: 400, Y: 0}},
				Path:    []geometry.Point{{X: 0, Y: 0}, {X: 0, Y: 120, Z: -80}}},
		},
	}
}

func mustJSON(t *testing.T, d *Prototype) string {
	t.Helper()
	b, err := json.Marshal(struct {
		Prototype *Prototype `json:"prototype"`
	}{d})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A part that cannot be built is repaired before the turn is emitted.
//
// # What this closes
//
// A turn could say "I have added a rear spoiler" and store a document with no
// wing in it. Observed twice on real turns against qwen3.7-plus: a two-point
// outline, and a wheel arch whose loop repeated a point — the second dropped the
// car's entire body. Both were reported honestly and both still left somebody
// with a model that did not contain what they had just been told it contained.
func TestRepair_FixesGeometryThatWouldNotBuild(t *testing.T) {
	fixed := faultyCar()
	fixed.Parts[1].Profile = []geometry.Point{{X: -400, Y: 0}, {X: 400, Y: 0}, {X: 400, Y: 40}, {X: -400, Y: 40}}
	stub := &repairStub{reply: mustJSON(t, fixed)}
	c := &Conversation{client: stub}

	reply := &Reply{Prototype: faultyCar()}
	if len(reply.Prototype.Faults()) == 0 {
		t.Fatal("the fixture is not faulty, so this test proves nothing")
	}

	if !c.repairIfFaulty(context.Background(), reply) {
		t.Fatal("a repairable fault was not repaired")
	}
	if got := len(reply.Prototype.Faults()); got != 0 {
		t.Errorf("the reply still carries %d fault(s) after a successful repair", got)
	}
	if stub.calls != 1 {
		t.Errorf("the model was called %d times; exactly one attempt is the contract, because "+
			"retrying further turns a document the model cannot get right into a loop that "+
			"bills for every pass while somebody waits", stub.calls)
	}
	// The model must be TOLD what was wrong. A repair prompt that does not name
	// the fault is asking for a rewrite and will get one.
	if !strings.Contains(stub.asked, "at least 3") && !strings.Contains(stub.asked, "enclose") {
		t.Errorf("the repair request does not carry the builder's reason.\nasked: %.300s", stub.asked)
	}
}

// A pass that reduces the faults without curing them is kept.
//
// Faults cascade — the validator reports the first thing wrong with a loop and
// stops, so fixing it exposes the next. Measured against qwen3.7-plus on the
// real wheel-arch document: the repeated point was removed correctly and a
// containment problem appeared underneath. Demanding a perfect result threw that
// improvement away and reported failure, which is how this was first written.
func TestRepair_KeepsAPassThatReducesTheFaults(t *testing.T) {
	// Two faults: a two-point wing outline AND a two-point second part.
	two := faultyCar()
	two.Parts = append(two.Parts, geometry.Part{
		ID: "fin", Name: "Fin", Shape: "extrusion",
		Size:    map[string]float64{"depth": 10},
		Profile: []geometry.Point{{X: 0, Y: 0}, {X: 10, Y: 0}},
	})
	if len(two.Faults()) < 2 {
		t.Fatalf("the fixture has %d fault(s); this test needs at least 2", len(two.Faults()))
	}

	// The answer fixes only the wing, leaving the fin broken.
	half := faultyCar()
	half.Parts[1].Profile = []geometry.Point{{X: -400, Y: 0}, {X: 400, Y: 0}, {X: 400, Y: 40}}
	half.Parts = append(half.Parts, two.Parts[2])
	stub := &repairStub{reply: mustJSON(t, half)}
	c := &Conversation{client: stub}
	reply := &Reply{Prototype: two}

	if !c.repairIfFaulty(context.Background(), reply) {
		t.Fatal("a pass that halved the faults was discarded, so real progress was thrown away " +
			"and the turn reported failure")
	}
	if got := len(reply.Prototype.Faults()); got != 1 {
		t.Errorf("after repair there are %d fault(s), wanted the 1 that was not fixable", got)
	}
}

// A repair that does not fix it is not accepted.
func TestRepair_RefusesAnAnswerWithTheSameHoleInIt(t *testing.T) {
	stub := &repairStub{reply: mustJSON(t, faultyCar())} // unchanged: still faulty
	c := &Conversation{client: stub}
	reply := &Reply{Prototype: faultyCar()}

	if c.repairIfFaulty(context.Background(), reply) {
		t.Error("a repair that returned the same fault was accepted, so the model's own work " +
			"was replaced by an equally broken rewrite for no gain")
	}
	if len(reply.Prototype.Faults()) == 0 {
		t.Error("the original was altered by a repair that failed")
	}
}

// A repair must not become a redesign.
//
// A model asked to fix a coordinate can return a different design. Swapping what
// somebody is looking at for something else is worse than the fault being fixed,
// so a reply with a different number of parts is refused even when it builds.
func TestRepair_RefusesARedesign(t *testing.T) {
	redesign := &geometry.Document{
		Name: "Car", Units: "mm",
		Parts: []geometry.Part{{ID: "body", Name: "Body", Shape: "box",
			Size: map[string]float64{"width": 1900, "height": 800, "depth": 4500}}},
	}
	if len(redesign.Faults()) != 0 {
		t.Fatal("the redesign fixture is itself faulty; this test would pass for the wrong reason")
	}
	stub := &repairStub{reply: mustJSON(t, redesign)}
	c := &Conversation{client: stub}
	reply := &Reply{Prototype: faultyCar()}

	if c.repairIfFaulty(context.Background(), reply) {
		t.Error("a repair that dropped a part was accepted; the person would be shown a " +
			"different design from the one they were discussing")
	}
	if len(reply.Prototype.Parts) != 2 {
		t.Error("the original was replaced by the redesign")
	}
}

// A failed repair costs one call and loses nothing.
func TestRepair_LeavesTheTurnIntactWhenItCannotRun(t *testing.T) {
	stub := &repairStub{failed: true}
	c := &Conversation{client: stub}
	reply := &Reply{Prototype: faultyCar()}

	if c.repairIfFaulty(context.Background(), reply) {
		t.Error("a failed call reported success")
	}
	if reply.Prototype == nil || len(reply.Prototype.Parts) != 2 {
		t.Error("a failed repair took the turn's geometry away. The speech is already true and " +
			"the notes already say what is missing; losing the turn is strictly worse than the " +
			"state this exists to improve.")
	}
}

// Sound geometry is never sent for repair.
func TestRepair_DoesNothingWhenNothingIsWrong(t *testing.T) {
	sound := &geometry.Document{
		Name: "Plate", Units: "mm",
		Parts: []geometry.Part{{ID: "plate", Name: "Plate", Shape: "box",
			Size: map[string]float64{"width": 60, "height": 6, "depth": 60}}},
	}
	stub := &repairStub{reply: mustJSON(t, sound)}
	c := &Conversation{client: stub}
	reply := &Reply{Prototype: sound}

	if c.repairIfFaulty(context.Background(), reply) {
		t.Error("a sound document was 'repaired'")
	}
	if stub.calls != 0 {
		t.Errorf("the model was called %d times for a document with nothing wrong; every turn "+
			"that draws anything would pay for it", stub.calls)
	}
}
