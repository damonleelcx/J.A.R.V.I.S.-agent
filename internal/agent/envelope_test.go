package agent_test

import (
	"encoding/json"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// The envelope check sees the square slab, and leaves a real restyle alone.
//
// # What this closes
//
// envelopeGrowth is the only thing in the live revision test that can catch the
// 2026-09-09 failure, and a measure that quietly returns 0 for everything would
// make that test pass forever while measuring nothing. Both documents here are
// taken from the live record: the body as it was (a box) and as it came back.
func TestEnvelopeGrowth_SeesTheLengthCountedTwice(t *testing.T) {
	box := mustDoc(t, `{"name":"c","units":"mm","parts":[
	  {"id":"chassis-body","name":"Main Body","shape":"box",
	   "size":{"width":1900,"height":800,"depth":4500},"position":[0,0,0]}]}`)

	// Verbatim from the live turn: the side elevation drawn into the outline,
	// and 4500 given AGAIN as the depth.
	slab := mustDoc(t, `{"name":"c","units":"mm","parts":[
	  {"id":"chassis-body","name":"Main Body","shape":"extrusion",
	   "size":{"depth":4500},"position":[0,0,0],
	   "profile":[{"x":-2250,"y":0},{"x":2250,"y":0},{"x":2250,"y":800},
	              {"x":1500,"y":800},{"x":0,"y":600},{"x":-1500,"y":800},
	              {"x":-2250,"y":800}]}]}`)

	// The same silhouette extruded by the car's actual width — the shape the
	// model was reaching for, which must NOT be flagged.
	proper := mustDoc(t, `{"name":"c","units":"mm","parts":[
	  {"id":"chassis-body","name":"Main Body","shape":"extrusion",
	   "size":{"depth":1900},"position":[0,0,0],"rotation":[0,90,0],
	   "profile":[{"x":-2250,"y":0},{"x":2250,"y":0},{"x":2250,"y":800},
	              {"x":1500,"y":800},{"x":0,"y":600},{"x":-1500,"y":800},
	              {"x":-2250,"y":800}]}]}`)

	if g := envelopeGrowth(box, slab, "chassis-body"); g <= 1.5 {
		t.Errorf("the square slab measured %.2fx growth and would not be flagged. "+
			"This is the exact document that shipped to a person as a sports car", g)
	}
	if g := envelopeGrowth(box, proper, "chassis-body"); g > 1.5 {
		t.Errorf("a correct restyle measured %.2fx and would be flagged. A check that "+
			"fires on the right answer is worse than none: it would be turned off", g)
	}
	// A part that cannot be measured reports 0, which is a miss, not a pass.
	if g := envelopeGrowth(box, slab, "no-such-part"); g != 0 {
		t.Errorf("an unmeasurable part reported %v rather than 0", g)
	}
}

func mustDoc(t *testing.T, s string) *geometry.Document {
	t.Helper()
	var d geometry.Document
	if err := json.Unmarshal([]byte(s), &d); err != nil {
		t.Fatal(err)
	}
	return &d
}
