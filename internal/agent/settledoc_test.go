package agent

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// Settling a document twice leaves it exactly as settling it once.
//
// # Why this matters now
//
// Every producer of a turn's document settles what it produces — the reply, an
// edit, a repair, a build pass — and a repair is SENT the settled document,
// not_verified included, and sends it back. So the same document is settled
// again, and anything that is not idempotent shows up as a reader being told the
// same thing twice, or twice in two wordings.
//
// The fixture carries one of each kind of note that could repeat: an
// unconvertible unit (whose note would otherwise reword itself on the second
// pass, because the first pass clears the unit), a bound dimension whose typed
// number disagrees with its expression, and an outline that cannot be read.
func TestSettle_IsIdempotent(t *testing.T) {
	d := boundBody(1900)
	d.Units = "furlongs"
	d.Parts = append(d.Parts, geometry.Part{ID: "wing", Name: "Wing", Shape: "extrusion",
		Profile:  []geometry.Point{{X: -400, Y: 0}, {X: 400, Y: 0}},
		Size:     map[string]float64{"depth": 5},
		Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}})

	once := settleDocument(d)
	if once == nil {
		t.Fatal("a document with parts was settled away")
	}
	first := append([]string(nil), once.NotVerified...)
	for _, want := range []string{"unitless", "body_width", "Wing"} {
		if !strings.Contains(strings.Join(first, "\n"), want) {
			t.Fatalf("the fixture did not produce a note about %q, so this test proves less than "+
				"it claims:\n%s", want, strings.Join(first, "\n"))
		}
	}

	// The way a repair sees it: the whole settled document, through JSON and back.
	body, err := json.Marshal(once)
	if err != nil {
		t.Fatal(err)
	}
	var back Prototype
	if err := json.Unmarshal(body, &back); err != nil {
		t.Fatal(err)
	}
	twice := settleDocument(&back)

	if !reflect.DeepEqual(first, twice.NotVerified) {
		t.Errorf("settling the document again changed what the reader is told.\nonce:\n  %s\ntwice:\n  %s",
			strings.Join(first, "\n  "), strings.Join(twice.NotVerified, "\n  "))
	}
	if twice.Parts[0].Size["width"] != 2000 || twice.Units != "" {
		t.Errorf("settling again changed the document: width %v, units %q",
			twice.Parts[0].Size["width"], twice.Units)
	}
}
