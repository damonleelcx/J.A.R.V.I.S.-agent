package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
)

func carWithSpoiler() *Prototype {
	return &geometry.Document{
		Name: "Car", Units: "mm",
		Parts: []geometry.Part{
			{ID: "chassis-body", Name: "Main Body", Shape: "box",
				Size: map[string]float64{"width": 1900, "height": 800, "depth": 4500}},
			{ID: "spoiler-wing", Name: "Spoiler Wing", Shape: "box",
				Size: map[string]float64{"width": 1800, "height": 50, "depth": 400}},
		},
	}
}

// A revision that drops a part says so.
//
// # What this closes
//
// A whole prototype deletes by OMISSION — a document that does not mention a
// part no longer has one — so a turn can destroy something nobody discussed.
// Measured against qwen3.7-plus: asked to make a car body less boxy, two runs in
// three returned a document with no spoiler. The reply talked about the body,
// said nothing about the spoiler, and the spoiler was gone. The person is
// looking at the shape they asked about, so the thing that vanished is the thing
// they are least likely to notice.
func TestVanished_SaysWhatARevisionRemoved(t *testing.T) {
	before := carWithSpoiler()
	after := &geometry.Document{Name: "Car", Units: "mm",
		Parts: []geometry.Part{before.Parts[0]}} // the spoiler is simply not mentioned

	reply := &Reply{Prototype: after}
	noteVanished(reply, before)

	if reply.Repaired == "" {
		t.Fatal("a revision deleted a part and said nothing. The reply talks about the body, " +
			"so the vanished spoiler is exactly what nobody will notice.")
	}
	if !strings.Contains(reply.Repaired, "Spoiler Wing") {
		t.Errorf("the note does not name what went.\ngot: %s", reply.Repaired)
	}
}

// Nothing removed, nothing said.
func TestVanished_IsSilentWhenNothingWent(t *testing.T) {
	before := carWithSpoiler()
	after := carWithSpoiler()
	after.Parts[0].Size["height"] = 700 // reshaped, not removed

	reply := &Reply{Prototype: after}
	noteVanished(reply, before)
	if reply.Repaired != "" {
		t.Errorf("a revision that removed nothing produced a removal note: %s", reply.Repaired)
	}
}

// A cutter the revision no longer needs is not a loss.
//
// A tool "does not also appear as a solid of its own" — it became the void it
// was drawn to make, so the reader never saw it as a part. A revision that
// reworks a wheel arch and drops the cutter it no longer needs has taken
// nothing away from what is on screen, and announcing it would cry wolf on the
// commonest correct use of a feature. A note that fires on the ordinary case is
// one people learn to skip.
func TestVanished_DoesNotCountARetiredCutterAsALoss(t *testing.T) {
	before := &geometry.Document{Name: "Car", Units: "mm",
		Parts: []geometry.Part{
			{ID: "chassis-body", Name: "Main Body", Shape: "box"},
			{ID: "arch-cutter-l", Name: "Left Arch Cutter", Shape: "cylinder"},
		},
		Features: []geometry.Feature{
			{ID: "arch-l", Op: "cut", Of: "chassis-body", With: []string{"arch-cutter-l"}},
		}}
	// The revision reshapes the body and no longer needs that cutter: both the
	// cutter and the feature that used it are gone.
	after := &geometry.Document{Name: "Car", Units: "mm",
		Parts: []geometry.Part{{ID: "chassis-body", Name: "Main Body", Shape: "extrusion"}}}

	reply := &Reply{Prototype: after}
	noteVanished(reply, before)
	if reply.Repaired != "" {
		t.Errorf("a retired cutting tool was announced as a lost part. "+
			"It was never visible, so this note teaches people to ignore notes.\ngot: %s",
			reply.Repaired)
	}
}

// A part still used as a tool has not vanished either.
func TestVanished_DoesNotCountAnActiveToolAsALoss(t *testing.T) {
	before := &geometry.Document{Name: "Plate", Units: "mm",
		Parts: []geometry.Part{
			{ID: "plate", Name: "Plate", Shape: "box"},
			{ID: "bore", Name: "Bore", Shape: "cylinder"},
		}}
	after := &geometry.Document{Name: "Plate", Units: "mm",
		Parts: []geometry.Part{{ID: "plate", Name: "Plate", Shape: "box"}},
		Features: []geometry.Feature{
			{ID: "drill", Op: "cut", Of: "plate", With: []string{"bore"}},
		}}
	reply := &Reply{Prototype: after}
	noteVanished(reply, before)
	if reply.Repaired != "" {
		t.Errorf("a part still named as a cutting tool was reported gone: %s", reply.Repaired)
	}
}

// The turn itself carries the notice — not just the helper.
//
// # Why this exists separately
//
// Every test above calls noteVanished directly, so all of them stay green if
// the call is deleted from Respond. The bug this whole file is about is a
// person not being told; a helper nobody invokes tells nobody. This drives a
// real turn through Respond with a model that returns the model minus the
// spoiler, which is exactly what qwen3.7-plus did on two runs in three.
func TestVanished_TheTurnSaysWhatItRemoved(t *testing.T) {
	before := carWithSpoiler()
	revised := &geometry.Document{Name: "Car", Units: "mm",
		Parts: []geometry.Part{before.Parts[0]}}
	stub := &repairStub{reply: `{"speech":"I have rounded the body.","prototype":` +
		mustJSONInner(t, revised) + `}`}
	c := &Conversation{client: stub}

	reply, err := c.Respond(context.Background(), "proj", nil,
		"make the body less boxy", "", before, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reply.Repaired == "" || !strings.Contains(reply.Repaired, "Spoiler Wing") {
		t.Fatalf("the turn deleted the spoiler and the reply never mentions it.\n"+
			"speech: %s\nnotice: %q", reply.Speech, reply.Repaired)
	}
}

// streamingStub answers RespondStream with one chunk carrying the whole reply.
//
// repairStub alone is not enough: a client that cannot stream makes
// RespondStream fall back to Respond, so a fence built on it would pass
// whatever the streaming path does. The streaming path is the one the product
// actually runs.
type streamingStub struct{ repairStub }

func (s *streamingStub) Stream(_ context.Context, _ llm.Request, onChunk func(llm.Chunk) error) error {
	if err := onChunk(llm.Chunk{Delta: s.reply}); err != nil {
		return err
	}
	return onChunk(llm.Chunk{Done: true, FinishReason: "stop"})
}

// The streaming turn emits the notice too.
func TestVanished_TheStreamingTurnSaysWhatItRemoved(t *testing.T) {
	before := carWithSpoiler()
	revised := &geometry.Document{Name: "Car", Units: "mm",
		Parts: []geometry.Part{before.Parts[0]}}
	stub := &streamingStub{repairStub{reply: `{"speech":"I have rounded the body.","prototype":` +
		mustJSONInner(t, revised) + `}`}}
	c := &Conversation{client: stub}

	var notices []string
	err := c.RespondStream(context.Background(), "proj", nil,
		"make the body less boxy", "", before, nil,
		func(e StreamEvent) error {
			if e.Kind == "notice" {
				notices = append(notices, e.Text)
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(notices, " "), "Spoiler Wing") {
		t.Fatalf("the streaming turn deleted the spoiler and emitted no notice naming it.\n"+
			"notices: %q", notices)
	}
}
