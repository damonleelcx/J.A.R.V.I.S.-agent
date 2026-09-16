package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// Replays of what the live car run of 2026-09-15 sent when its step 2 declared a
// placement whose `at` began with the root's own id. The fixtures are the saved
// replies, unedited (docs/spikes/2026-09-15-car-verified/data/calls.jsonl, calls 5, 6
// and 8), and the model the step was shown, rebuilt from the document its first repair
// was sent with the step's own additions taken out:
//
//   - car-verified-before-step2.json: the model after step 1, root "chassis";
//   - car-verified-step2-reply.json: step 2's reply, declaring
//     {"id":"suspension-mounts","ref":"suspension-mounts","at":"chassis/cockpit-floor"};
//   - car-verified-step2-repair1-reply.json: the fault repair's whole document, which
//     moved the declared path to "cockpit-floor" and left the step's own children alone;
//   - car-verified-step2-repair2-reply.json: the look repair's, which moved it back.
//
// docs/bugfix/2026-09-15-a-placement-from-the-root-could-not-name-the-root.md

// carVerifiedBeforeStepTwo is the model step 2 was given. It has no faults, so every
// fault below is one this step added.
func carVerifiedBeforeStepTwo(t *testing.T) *Prototype {
	t.Helper()
	var d geometry.Document
	if err := json.Unmarshal([]byte(runTwoFixture(t, "car-verified-before-step2.json")), &d); err != nil {
		t.Fatal(err)
	}
	if faults := d.Faults(); len(faults) != 0 {
		t.Fatalf("the model before step 2 had faults: %+v", faults)
	}
	return &d
}

// carVerifiedStepTwo is how the harness asked the live step 2.
var carVerifiedStepTwo = buildTask{Name: "Suspension Uprights and Hubs", Assembly: "suspension-mounts",
	What: "Mount the front and rear suspension uprights and wheel hubs to the chassis pickup points to establish the wheel geometry."}

// ‼️ The live declared placement, replayed: "chassis/cockpit-floor" on the root
// "chassis" now resolves, exactly as "cockpit-floor" does. The step is still refused —
// its OWN children reach out of their assembly, which the refused placement had hidden
// by stopping the walk — but the refusal is now one the repair can act on.
func TestReplay_CarVerifiedsDeclaredPlacementFromTheRootNamesTheRoot(t *testing.T) {
	// The path itself, on the live model: it names the frame "cockpit-floor" names.
	probe := *carVerifiedBeforeStepTwo(t)
	probe.Assemblies = append([]geometry.Assembly(nil), probe.Assemblies...)
	for i := range probe.Assemblies {
		if probe.Assemblies[i].ID != probe.Root {
			continue
		}
		probe.Assemblies[i].Children = append(append([]geometry.Child(nil), probe.Assemblies[i].Children...),
			geometry.Child{ID: "with-root", Ref: "main-tube", At: "chassis/cockpit-floor"},
			geometry.Child{ID: "without", Ref: "main-tube", At: "cockpit-floor"})
	}
	if faults := probe.Faults(); len(faults) != 0 {
		t.Fatalf(`"chassis/cockpit-floor" is still refused on the live model: %+v`, faults)
	}
	withRoot, without := placedAt(t, &probe, "with-root"), placedAt(t, &probe, "without")
	if !near(withRoot, without...) {
		t.Errorf(`"chassis/cockpit-floor" put the part at %v; "cockpit-floor" put it at %v`, withRoot, without)
	}

	stub := &scriptedStub{replies: []string{runTwoFixture(t, "car-verified-step2-reply.json")}}
	c := &Conversation{client: stub}
	next, note := c.buildOneStep(context.Background(), carVerifiedBeforeStepTwo(t),
		"a sports car, in as much mechanical detail as you can manage", carVerifiedStepTwo, 2, 8)

	// What the live run said, and no longer says.
	if strings.Contains(note, `has no child "chassis"`) {
		t.Errorf("the declared placement is still refused as it was live:\n%s", note)
	}
	// The step is still refused, and honestly: its four uprights, written inside
	// "suspension-mounts", reach the chassis's own pickup interfaces. That is the rule
	// an assembly placed many times rests on, and the refusal now says where they go.
	if next != nil || StepGateOf(note) != "faults-added" {
		t.Fatalf("the step was not refused for the faults its own children add: %v %q", next != nil, note)
	}
	// ‼️ The note clips each fault at 200 characters, which is why the fix comes before
	// the reason in the sentence: the instruction survives the clip and the essay does
	// not. The whole of it reaches the repair, asserted below.
	for _, want := range []string{
		`suspension-mounts/front-left-upright is attached at "chassis/front-suspension-left", outside its assembly "suspension-mounts"`,
		`attach it from the root "chassis" instead`,
	} {
		if !strings.Contains(note, want) {
			t.Errorf("the step's note does not say %q:\n%s", want, note)
		}
	}

	// ‼️ And the repair is told all of it: which child, the path it asked for, and what
	// to write instead. Live it was shown `- is attached at "chassis/cockpit-floor",
	// but …` and nothing else, and both attempts failed.
	repair := repairPrompt(stub.asked)
	if repair == "" {
		t.Fatalf("the fault repair was never asked: %d call(s)", len(stub.asked))
	}
	for _, want := range []string{
		`- suspension-mounts/front-left-upright is attached at "chassis/front-suspension-left"`,
		`as a child there with "at": "front-suspension-left"`,
		`- suspension-mounts/front-left-hub-assembly is attached at "front-left-upright", but assembly ` +
			`"suspension-mounts" declares no interface "front-left-upright"`,
		`write "at" on this child as one of front-left-hub, front-right-hub, rear-left-hub, rear-right-hub, ` +
			`or leave it out to place it in "suspension-mounts"'s own frame`,
	} {
		if !strings.Contains(repair, want) {
			t.Errorf("the repair is not told %q:\n%s", want, repair)
		}
	}
	if strings.Contains(repair, "- is attached at") {
		t.Errorf("a fault reached the repair with no child named:\n%s", repair)
	}
}

// ‼️ The two live repairs of step 2, replayed: every fault they are handed names its
// child and carries what to write instead, so neither is asked to guess again.
func TestReplay_CarVerifiedsRepairsAreToldWhichChildAndWhatToWrite(t *testing.T) {
	for _, name := range []string{"car-verified-step2-repair1-reply.json", "car-verified-step2-repair2-reply.json"} {
		t.Run(name, func(t *testing.T) {
			var out struct {
				Prototype *Prototype `json:"prototype"`
			}
			if err := json.Unmarshal([]byte(extractJSON(runTwoFixture(t, name))), &out); err != nil || out.Prototype == nil {
				t.Fatalf("the saved repair does not read: %v", err)
			}
			faults := settleDocument(out.Prototype).Faults()
			if len(faults) == 0 {
				t.Fatal("the saved repair has no faults left to be told about")
			}
			for _, f := range faults {
				if strings.TrimSpace(f.Name) == "" {
					t.Errorf("a fault names no child: %s", f.Detail)
				}
				if !strings.Contains(f.Detail, "attach it from the root") &&
					!strings.Contains(f.Detail, `write "at" on this child`) {
					t.Errorf("%s carries no remedy: %s", f.Name, f.Detail)
				}
				if strings.Contains(f.Detail, `has no child "chassis"`) {
					t.Errorf("%s is still refused for naming the root: %s", f.Name, f.Detail)
				}
			}
		})
	}
}

// ‼️ Every fault the repair is shown names the part it is about. The sentence alone
// does not: an attachment refusal begins "is attached at", and this prompt used to
// send the sentence and drop the name the step's note has always printed.
func TestRepair_EveryFaultTheRepairIsShownNamesThePartItIsAbout(t *testing.T) {
	doc := carOnSuspension()
	doc.Assemblies[1].Children = append(doc.Assemblies[1].Children,
		geometry.Child{ID: "lost", Name: "Lost Wheel", Ref: "arm", At: "nowhere"})
	faults := doc.Faults()
	if len(faults) != 1 {
		t.Fatalf("want one fault to repair, got %+v", faults)
	}
	stub := &scriptedStub{replies: []string{`{"speech":"nothing"}`}}
	c := &Conversation{client: stub}
	c.repairGeometry(context.Background(), doc, faults)
	repair := repairPrompt(stub.asked)
	if repair == "" {
		t.Fatalf("the repair was never asked: %d call(s)", len(stub.asked))
	}
	want := `- lost (Lost Wheel) is attached at "nowhere", but assembly "car" declares no interface "nowhere"` +
		` (it declares none); write "at" on this child as one of suspension-left/hub, suspension-right/hub,` +
		` or leave it out to place it in "car"'s own frame`
	if !strings.Contains(repair, want) {
		t.Errorf("the repair is not shown the child, the path and what to write:\nwant %s\n\n%s", want, repair)
	}
}
