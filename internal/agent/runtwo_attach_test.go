package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
)

// Replays of what the live car-quality run 2 (2026-09-15) sent when its wheels were
// attached across assemblies. The fixtures are the saved replies, unedited
// (docs/spikes/2026-09-15-car-quality/data/run2/calls.jsonl, calls 24 and 25), and the
// model the step was shown, rebuilt from the whole document its first repair was sent
// with the step's own additions taken out:
//
//   - run2-before-wheels.json: the model before step 6;
//   - run2-wheels-step-reply.json: step 6's reply, wheels attached at
//     "rear-suspension/left-hub/hub-face" from inside "wheels";
//   - run2-wheels-repair1-reply.json: the first fault repair's whole document, which
//     moved the path to "left-hub/hub-face", still inside "wheels";
//   - run2-wheels-step-reply-as-taught.json: the same wheel, rewritten as the contract
//     now teaches — the wheel as the step's assembly, placed from the root under
//     "placements".
// docs/bugfix/2026-09-15-an-attachment-into-another-assembly-was-refused-without-the-fix.md

func runTwoFixture(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func runTwoBeforeWheels(t *testing.T) *Prototype {
	t.Helper()
	var d geometry.Document
	if err := json.Unmarshal([]byte(runTwoFixture(t, "run2-before-wheels.json")), &d); err != nil {
		t.Fatal(err)
	}
	if faults := d.Faults(); len(faults) != 0 {
		t.Fatalf("the model before the wheels step had faults: %+v", faults)
	}
	return &d
}

// runTwoWheelStep is how the harness asked run 2's step 6.
var runTwoWheelStep = buildTask{Name: "Wheels and Tires", Assembly: "wheels",
	What: "Mount the lightweight alloy wheels with performance tires onto the hub assemblies and torque the lug nuts."}

func repairPrompt(asked []string) string {
	for _, a := range asked {
		if strings.HasPrefix(a, "This document has parts that cannot be built:") {
			return a
		}
	}
	return ""
}

// ‼️ Run 2's wheel step, replayed: refused, by name, with the fix — for the person in
// the step's note and for the fault repair in its prompt — never "has no child".
func TestReplay_RunTwosWheelStepIsRefusedWithTheFixAndItsRepairIsToldIt(t *testing.T) {
	stub := &scriptedStub{replies: []string{runTwoFixture(t, "run2-wheels-step-reply.json")}}
	c := &Conversation{client: stub}
	next, note := c.buildOneStep(context.Background(), runTwoBeforeWheels(t),
		"a sports car, in as much mechanical detail as you can manage", runTwoWheelStep, 6, 8)
	if next != nil || StepGateOf(note) != "faults-added" {
		t.Fatalf("the step was not refused for the faults it added: %v %q", next != nil, note)
	}
	for _, want := range []string{`wheels/left-wheel-assembly is attached at "rear-suspension/left-hub/hub-face", outside its assembly "wheels"`,
		`attach it from the root "rear-suspension" instead`} {
		if !strings.Contains(note, want) {
			t.Errorf("the step's note does not say %q:\n%s", want, note)
		}
	}
	if strings.Contains(note, `has no child "rear-suspension"`) {
		t.Errorf("the note still says only what run 2 said:\n%s", note)
	}
	repair := repairPrompt(stub.asked)
	if repair == "" {
		t.Fatalf("the fault repair was never asked: %v", stub.asked)
	}
	for _, want := range []string{
		`is attached at "rear-suspension/left-hub/hub-face", outside its assembly "wheels"; attach it from the root "rear-suspension" instead, as a child there with "at": "left-hub/hub-face"`,
		`is attached at "rear-suspension/right-hub/hub-face", outside its assembly "wheels"`,
		// And what is still wrong from the root: run 2's hubs are parts, which have no interfaces.
		`from the root that path fails too: "left-hub" places "rear-hub", which is not an assembly`,
		"wrap the part in a one-child assembly that declares the frame",
	} {
		if !strings.Contains(repair, want) {
			t.Errorf("the repair is not told %q", want)
		}
	}
}

// ‼️ Run 2's first repair of the wheels, replayed: the path it moved to is still inside
// "wheels", and it is now told so, with the same fix.
func TestReplay_RunTwosFirstRepairOfTheWheelsIsToldToAttachFromTheRoot(t *testing.T) {
	var repaired struct {
		Prototype *Prototype `json:"prototype"`
	}
	if err := json.Unmarshal([]byte(extractJSON(runTwoFixture(t, "run2-wheels-repair1-reply.json"))), &repaired); err != nil || repaired.Prototype == nil {
		t.Fatalf("the saved repair does not read: %v", err)
	}
	faults := repaired.Prototype.Faults()
	if len(faults) != 2 {
		t.Fatalf("want run 2's two faults, got %+v", faults)
	}
	for i, side := range []string{"left", "right"} {
		want := `is attached at "` + side + `-hub/hub-face", outside its assembly "wheels"; attach it from the root ` +
			`"rear-suspension" instead, as a child there with "at": "` + side + `-hub/hub-face"`
		if faults[i].Name != "wheels/"+side+"-wheel-assembly" || !strings.Contains(faults[i].Detail, want) {
			t.Errorf("fault %d is %s: %s\nwant %s", i, faults[i].Name, faults[i].Detail, want)
		}
	}
}

// ‼️ The same wheel written as the contract now teaches: accepted on a suspension that
// declares its hub faces, and — on run 2's own suspension, whose hubs are parts —
// refused by name with what to change.
func TestReplay_RunTwosWheelsWrittenAsTheContractTeachesAreAttachedFromTheRoot(t *testing.T) {
	taught := runTwoFixture(t, "run2-wheels-step-reply-as-taught.json")
	if strings.Contains(taught, "rear-suspension/") || !strings.Contains(taught, `"placements"`) {
		t.Fatal("the as-taught fixture is not the corrected shape")
	}

	t.Run("on a suspension that declares its hub faces", func(t *testing.T) {
		before := runTwoBeforeWheels(t)
		// The hubs as the first-step prompt asks: a one-child assembly with a wheel face.
		before.Assemblies = append(before.Assemblies, geometry.Assembly{ID: "rear-hub-unit",
			Interfaces: []geometry.Interface{{ID: "hub-face"}}, Children: []geometry.Child{{ID: "hub", Ref: "rear-hub"}}})
		for i := range before.Assemblies {
			if before.Assemblies[i].ID != before.Root {
				continue
			}
			for j, c := range before.Assemblies[i].Children {
				if c.Ref == "rear-hub" {
					before.Assemblies[i].Children[j].Ref = "rear-hub-unit"
				}
			}
		}
		stub := &scriptedStub{replies: []string{taught}}
		c := &Conversation{client: stub}
		next, note := c.buildOneStep(context.Background(), before,
			"a sports car, in as much mechanical detail as you can manage", runTwoWheelStep, 6, 8)
		if next == nil {
			t.Fatalf("the corrected wheel step was refused: %q", note)
		}
		if len(stub.asked) == 0 || !strings.Contains(stub.asked[0], `"root_interfaces":[`) ||
			!strings.Contains(stub.asked[0], `{"at":"left-hub/hub-face","position":[-300,0,100],"mirrored":true}`) {
			t.Errorf("the step was not shown the hub faces it can attach at from the root:\n%s", stub.asked[0])
		}
		want := `FORGE placed the assembly "wheels" from the root "rear-suspension" where this step declared it: ` +
			`"left-wheel" at "left-hub/hub-face"; "right-wheel" at "right-hub/hub-face", mirrored across x.`
		if !strings.Contains(note, want) {
			t.Errorf("the note does not say where the wheels were attached:\n%s", note)
		}
		if got := placedAt(t, next, "left-wheel/rim"); !near(got, -300, 0, 100) {
			t.Errorf("the left rim is at %v; want on the left hub, (-300, 0, 100)", got)
		}
		if got := placedAt(t, next, "right-wheel/rim"); !near(got, 300, 0, 100) {
			t.Errorf("the right rim is at %v; want on the right hub, (300, 0, 100)", got)
		}
		nuts := 0
		for _, id := range placedIDs(next) {
			if strings.HasPrefix(id, "left-wheel/lug-nuts-") || strings.HasPrefix(id, "right-wheel/lug-nuts-") {
				nuts++
			}
		}
		if nuts != 10 {
			t.Errorf("the wheels carry %d lug nuts; want five on each", nuts)
		}
	})

	t.Run("on run 2's own suspension, whose hubs are parts", func(t *testing.T) {
		stub := &scriptedStub{replies: []string{taught}}
		c := &Conversation{client: stub}
		next, note := c.buildOneStep(context.Background(), runTwoBeforeWheels(t),
			"a sports car, in as much mechanical detail as you can manage", runTwoWheelStep, 6, 8)
		if next != nil || StepGateOf(note) != "faults-added" || !strings.Contains(note, `"left-hub" places "rear-hub", which is not an assembly`) {
			t.Fatalf("the step was not refused naming the hub that has no frame: %v %q", next != nil, note)
		}
		if repair := repairPrompt(stub.asked); !strings.Contains(repair, "wrap the part in a one-child assembly that declares the frame") {
			t.Errorf("the repair is not told how to give the hub a frame:\n%.600s", repair)
		}
	})
}

// A reply that parses is handed on untouched by the over-model reading.
func TestPlacementsOverModel_LeavesAReplyThatParsesAlone(t *testing.T) {
	resp := &llm.Response{Content: runTwoFixture(t, "run2-wheels-step-reply.json"), FinishReason: "stop"}
	if got, changed := placementsOverModel(resp, carOnSuspension()); changed || got != resp {
		t.Error("a reply that parses as sent was rewritten")
	}
}

// ‼️ An edit's placement over the model's parameters is read at its value, and the
// model's parameters it was read over are not added to what the model sent.
func TestPlacementsOverModel_ReadsOverTheModelWithoutAddingToTheReply(t *testing.T) {
	sent := brakesStep(`{"id":"left-disc","ref":"disc","position":["-half_track", 0, "half_wheelbase"]}`, "")
	resp := &llm.Response{Content: sent, FinishReason: "stop"}
	read, changed := placementsOverModel(resp, carOnSuspension())
	if !changed || read == resp {
		t.Fatal("a placement over the model's parameters was not read")
	}
	if resp.Content != sent {
		t.Error("the model's own reply was changed in place")
	}
	for _, unwanted := range []string{"half_track", `"parameters"`, `"derived"`, `"units"`} {
		if strings.Contains(read.Content, unwanted) {
			t.Errorf("what was read carries %s, which the step did not send:\n%s", unwanted, read.Content)
		}
	}
	if !strings.Contains(read.Content, `"position":[-800,0,1350]`) {
		t.Errorf("the position was not read at its value:\n%s", read.Content)
	}
}
