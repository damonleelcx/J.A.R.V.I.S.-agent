package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	domainpack "github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/pack"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
)

// Fences for a placement written twice (doubled.go): live run 3 of 2026-09-17 put the
// lug nut's position and rotation on the definition AND on the child placing it, and
// the nuts landed 91% inside the tyre with nothing saying why.

// keptWheel is the wheel run 3 kept, as the goal kept it.
func keptWheel(t *testing.T) *Prototype {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "spikes", "2026-09-17-live-verification", "data", "run3", "wheel-goal.json"))
	if err != nil {
		t.Fatal(err)
	}
	var d Prototype
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	return &d
}

const keptWheelNote = `Definition "lug-nut" and the child "hub/nut" that places it both carry position [50, 20, 0] ` +
	`(the same amounts on x, y) and rotation [90, 0, 0], and the two add up: a definition's position and rotation ` +
	`are measured inside whatever places it. So it lands at [100, 20, 20] (the first copy of its pattern) instead ` +
	`of [50, 20, 0]. Keep the placement in one place: give the definition "position": [0, 0, 0] and no ` +
	`"position_from" and "rotation": [0, 0, 0], and place it with the child.`

// ‼️ The kept wheel is named: which definition, which child, where it lands, and the fix.
// And an offset written once, in either place, is not.
func TestDoubledOffsets_ADefinitionAndItsChildCarryingTheSamePositionAreNamed(t *testing.T) {
	wheel := keptWheel(t)
	found := doubledPlacements(wheel)
	if len(found) != 1 || found[0].sentence != keptWheelNote {
		t.Fatalf("the kept wheel's doubled lug nut reads %+v;\nwant %s", found, keptWheelNote)
	}
	// Where it says the nut lands is where FORGE draws it.
	drawn := ""
	for _, p := range wheel.PlacedParts() {
		if p.ID == "nut-1" {
			drawn = vec3(p.Position)
		}
	}
	if drawn != "[100, 20, 20]" {
		t.Errorf("the note says the first nut lands at [100, 20, 20]; FORGE draws it at %q", drawn)
	}

	// The fix it names clears it: the definition at its origin, placed by its child.
	fixed := keptWheel(t)
	for i := range fixed.Definitions {
		if fixed.Definitions[i].ID == "lug-nut" {
			fixed.Definitions[i].Position, fixed.Definitions[i].Rotation, fixed.Definitions[i].PositionFrom = []float64{0, 0, 0}, []float64{0, 0, 0}, nil
		}
	}
	if got := doubledPlacements(fixed); len(got) != 0 {
		t.Errorf("the wheel with the fix applied is still reported: %+v", got)
	}

	def := func(pos, rot []float64) []geometry.Part {
		return []geometry.Part{{ID: "nut", Shape: "cylinder", Size: map[string]float64{"radius": 8, "height": 15}, Position: pos, Rotation: rot}}
	}
	for _, tc := range []struct {
		name string
		doc  *Prototype
		want int
	}{
		{"an offset on the definition only (the contract's spoke)", &Prototype{Units: "mm", Root: "w", Definitions: def([]float64{0, 0, 30}, nil),
			Assemblies: []geometry.Assembly{{ID: "w", Children: []geometry.Child{{ID: "n", Ref: "nut"}}}}}, 0},
		{"a different offset on each", &Prototype{Units: "mm", Root: "w", Definitions: def([]float64{0, 0, 20}, nil),
			Assemblies: []geometry.Assembly{{ID: "w", Children: []geometry.Child{{ID: "n", Ref: "nut", Position: []float64{300, 0, 0}}}}}}, 0},
		{"the same position on a child placing an assembly", &Prototype{Units: "mm", Root: "w", Definitions: def([]float64{50, 0, 0}, nil),
			Assemblies: []geometry.Assembly{{ID: "w", Children: []geometry.Child{{ID: "s", Ref: "sub", Position: []float64{50, 0, 0}}}},
				{ID: "sub", Children: []geometry.Child{{ID: "n", Ref: "nut"}}}}}, 0},
		{"one axis the same", &Prototype{Units: "mm", Root: "w", Definitions: def([]float64{57, 0, 10}, nil),
			Assemblies: []geometry.Assembly{{ID: "w", Children: []geometry.Child{{ID: "n", Ref: "nut", Position: []float64{57, 0, 0}}}}}}, 1},
		{"the same rotation only", &Prototype{Units: "mm", Root: "w", Definitions: def(nil, []float64{0, 0, 90}),
			Assemblies: []geometry.Assembly{{ID: "w", Children: []geometry.Child{{ID: "n", Ref: "nut", Rotation: []float64{0, 0, 90}}}}}}, 1},
	} {
		if got := doubledPlacements(tc.doc); len(got) != tc.want {
			t.Errorf("%s: %d reported, want %d: %+v", tc.name, len(got), tc.want, got)
		}
	}
}

// ‼️ A build step that writes the offset twice is told where the part lands and the fix,
// its model is kept (never rewritten), and the next step's model is told in its prompt.
func TestAssemble_AStepThatDoublesAnOffsetIsToldWhereThePartLands(t *testing.T) {
	doubled := `{"speech":"hub","prototype_edit":{"patch":{
	  "definitions":[{"id":"lug-nut","shape":"cylinder","size":{"radius":8,"height":15},"position":[50,20,0],"rotation":[90,0,0]}],
	  "assemblies":[{"id":"brakes","children":[{"id":"nut","ref":"lug-nut","position":[50,20,0],"rotation":[90,0,0],
	    "pattern":{"kind":"polar","count":5,"about":"y"}}]}]}}}`
	c := &Conversation{client: &scriptedStub{replies: []string{doubled}}}
	next, note := c.buildOneStep(context.Background(), carOnSuspension(), "a car", buildTask{Name: "Hub", What: "hub", Assembly: "brakes"}, 2, 3)
	if next == nil {
		t.Fatalf("a step that doubled an offset was refused: %q", note)
	}
	for _, want := range []string{`Definition "lug-nut" and the child "brakes/nut" that places it both carry position [50, 20, 0]`,
		`So it lands at [100, 20, 20] (the first copy of its pattern) instead of [50, 20, 0]`,
		`Keep the placement in one place`} {
		if !strings.Contains(note, want) {
			t.Errorf("the step's note does not say %q:\n%s", want, note)
		}
	}
	for _, d := range next.Definitions {
		if d.ID == "lug-nut" && vec3(d.Position) != "[50, 20, 0]" {
			t.Errorf("the step's definition was rewritten to %v; FORGE says, it does not move parts", d.Position)
		}
	}

	stub := &scriptedStub{replies: []string{`{"speech":"ok"}`}}
	(&Conversation{client: stub}).buildOneStep(context.Background(), keptWheel(t), "a wheel", buildTask{Name: "Tyre", What: "the tyre"}, 3, 3)
	if len(stub.asked) != 1 || !strings.Contains(stub.asked[0], "Placed twice over in the model so far") ||
		!strings.Contains(stub.asked[0], keptWheelNote) {
		t.Errorf("the next step was not told the model so far places the lug nut twice over:\n%.1500s", strings.Join(stub.asked, "\n---\n"))
	}
	clean := &scriptedStub{replies: []string{`{"speech":"ok"}`}}
	(&Conversation{client: clean}).buildOneStep(context.Background(), carOnSuspension(), "a car", buildTask{Name: "Tyre", What: "the tyre"}, 3, 3)
	if len(clean.asked) == 1 && strings.Contains(clean.asked[0], "Placed twice over") {
		t.Error("a step on a model with nothing doubled was told there was")
	}
}

// ‼️ A turn that sends the doubled wheel says so, once, and the next turn's model is told
// next to the document it revises.
func TestConverse_ATurnThatDoublesAnOffsetSaysWhereThePartLands(t *testing.T) {
	body, err := json.Marshal(keptWheel(t))
	if err != nil {
		t.Fatal(err)
	}
	reply, err := (&Conversation{client: &repairStub{reply: `{"speech":"Here is the wheel.","prototype":` + string(body) + `}`}}).
		Respond(context.Background(), "proj", nil, "a wheel with five lug nuts", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(reply.Repaired, keptWheelNote) != 1 {
		t.Errorf("the turn's notice does not say the lug nut is placed twice over, once:\n%q", reply.Repaired)
	}

	var notices []string
	stub := &streamingStub{repairStub{reply: `{"speech":"Here is the wheel.","prototype":` + string(body) + `}`}}
	if err := (&Conversation{client: stub}).RespondStream(context.Background(), "proj", nil, "a wheel", "", nil, nil,
		func(e StreamEvent) error {
			if e.Kind == "notice" {
				notices = append(notices, e.Text)
			}
			return nil
		}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(notices, " "), keptWheelNote) {
		t.Errorf("the streamed turn emitted no notice naming the doubled lug nut: %q", notices)
	}

	c := &Conversation{}
	built := c.buildMessages(persona.DefaultCharacter(), domainpack.Definition{}, nil, "make the nuts bigger", "", keptWheel(t), nil)
	if last := built[len(built)-1].Content; !strings.Contains(last, "placements written twice in that model") ||
		!strings.Contains(last, keptWheelNote) {
		t.Errorf("the next turn's model is not told the wheel it revises places the lug nut twice over:\n%.2000s", last)
	}
}

// ‼️ The contract says a placement goes in one place, with the number FORGE draws.
func TestTheContractSaysAPlacementGoesInOnePlace(t *testing.T) {
	words := contractWords()
	for _, want := range []string{`A placement goes in ONE of the two places, because they add up: a lug nut written at [50, 20, 0] turned [90, 0, 0] on its definition AND on the child that places it lands at [100, 20, 20]`,
		`Write an offset on the definition or on the child, never the same one on both`} {
		if !strings.Contains(words, want) {
			t.Errorf("the contract does not say %q", want)
		}
	}
	d := &Prototype{Units: "mm", Root: "w",
		Definitions: []geometry.Part{{ID: "nut", Shape: "cylinder", Size: map[string]float64{"radius": 8, "height": 15},
			Position: []float64{50, 20, 0}, Rotation: []float64{90, 0, 0}}},
		Assemblies: []geometry.Assembly{{ID: "w", Children: []geometry.Child{{ID: "n", Ref: "nut",
			Position: []float64{50, 20, 0}, Rotation: []float64{90, 0, 0}}}}}}
	if got := vec3(d.PlacedParts()[0].Position); got != "[100, 20, 20]" {
		t.Errorf("the contract says the doubled nut lands at [100, 20, 20]; FORGE draws it at %s", got)
	}
}
