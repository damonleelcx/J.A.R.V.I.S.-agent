package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

func boxCar() *Prototype {
	return &geometry.Document{Name: "Car", Units: "mm", Parts: []geometry.Part{{
		ID: "chassis-body", Name: "Main Body", Shape: "box",
		Size: map[string]float64{"width": 1900, "height": 800, "depth": 4500},
	}}}
}

// The real failing document: a sweep whose outline is the side elevation and
// whose path runs along Z, so the car's LENGTH lands on the X axis.
func sweptSideways() *Prototype {
	var d geometry.Document
	if err := json.Unmarshal([]byte(`{"name":"Car","units":"mm","parts":[
	  {"id":"chassis-body","name":"Main Body","shape":"sweep",
	   "profile":[{"x":-2250,"y":0},{"x":2250,"y":0},{"x":2250,"y":600},{"x":-2250,"y":600}],
	   "path":[{"x":0,"y":0,"z":-950},{"x":0,"y":0,"z":950}]}]}`), &d); err != nil {
		panic(err)
	}
	return &d
}

// A restyle that turned the body on its side is caught, named, and explained.
//
// # What this closes
//
// Three contract wordings were measured against qwen3.7-plus and none beat
// saying nothing (2/10, 3/10, 2/10 — see turned.go). The failure reaches a
// person as a reply saying the body was reshaped, over a model that is a slab.
func TestTurned_CatchesTheLengthLandingAcrossTheCar(t *testing.T) {
	problems := turnedOnItsSide(boxCar(), sweptSideways())
	if len(problems) != 1 {
		t.Fatalf("wanted the body flagged, got %d problem(s): %+v", len(problems), problems)
	}
	d := problems[0].Detail
	// The numbers, so the model can act; the mechanism, so it fixes rather than
	// rescales.
	for _, want := range []string{"Main Body", "4500", "1900", "turned on its side"} {
		if !strings.Contains(d, want) {
			t.Errorf("the problem does not mention %q.\ngot: %s", want, d)
		}
	}
}

// A genuine restyle is left alone. A check that fires on the right answer is
// worse than none, because it will be switched off.
func TestTurned_LeavesARealRestyleAlone(t *testing.T) {
	var restyled geometry.Document
	// The same silhouette, correctly oriented: length along Z, width 1900.
	json.Unmarshal([]byte(`{"name":"Car","units":"mm","parts":[
	  {"id":"chassis-body","name":"Main Body","shape":"sweep",
	   "profile":[{"x":-950,"y":0},{"x":950,"y":0},{"x":950,"y":700},{"x":-950,"y":700}],
	   "path":[{"x":0,"y":0,"z":-2250},{"x":0,"y":0,"z":2250}]}]}`), &restyled)
	if p := turnedOnItsSide(boxCar(), &restyled); len(p) != 0 {
		t.Errorf("a correctly oriented restyle was flagged: %+v", p)
	}
}

// A part the revision deleted is not reported here — vanished.go owns that, and
// two notices for one event teaches people to skip both.
func TestTurned_SaysNothingAboutAPartThatWent(t *testing.T) {
	empty := &geometry.Document{Name: "Car", Units: "mm"}
	if p := turnedOnItsSide(boxCar(), empty); len(p) != 0 {
		t.Errorf("a deleted part was reported as resized: %+v", p)
	}
}

// When the repair cannot put it back, the person is told in the reply.
func TestTurned_ReportsWhatItCouldNotFix(t *testing.T) {
	// A stub that returns the same broken document: repair achieves nothing.
	stub := &repairStub{reply: mustJSON(t, sweptSideways())}
	c := &Conversation{client: stub}
	reply := &Reply{Prototype: sweptSideways(), Speech: "I have reshaped the body."}

	c.repairIfTurned(context.Background(), reply, boxCar())

	if reply.Repaired == "" {
		t.Fatal("the body came back turned on its side, the repair did not help, and the " +
			"reply says only that it was reshaped")
	}
	if !strings.Contains(reply.Repaired, "Main Body") {
		t.Errorf("the notice does not name the part.\ngot: %s", reply.Repaired)
	}
}

// And when the repair DOES fix it, the fix is taken and said.
func TestTurned_TakesAWorkingRepair(t *testing.T) {
	var good geometry.Document
	json.Unmarshal([]byte(`{"name":"Car","units":"mm","parts":[
	  {"id":"chassis-body","name":"Main Body","shape":"sweep",
	   "profile":[{"x":-950,"y":0},{"x":950,"y":0},{"x":950,"y":700},{"x":-950,"y":700}],
	   "path":[{"x":0,"y":0,"z":-2250},{"x":0,"y":0,"z":2250}]}]}`), &good)

	stub := &repairStub{reply: mustJSON(t, &good)}
	c := &Conversation{client: stub}
	reply := &Reply{Prototype: sweptSideways()}

	c.repairIfTurned(context.Background(), reply, boxCar())

	if len(turnedOnItsSide(boxCar(), reply.Prototype)) != 0 {
		t.Error("a repair that put the body back was not taken")
	}
	if !strings.Contains(reply.Repaired, "put it back") {
		t.Errorf("the correction was made silently.\ngot: %q", reply.Repaired)
	}
}
