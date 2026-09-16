package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
)

// Fences for placements written with a parameter's name being kept as bindings, and for
// the literal note reading a multiple of a parameter (2026-09-15, bound child positions).

// storedAndRespecified is d as a store would read it back, re-specified.
func storedAndRespecified(t *testing.T, d *Prototype, overrides map[string]float64) *Prototype {
	t.Helper()
	body, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var stored Prototype
	if err := json.Unmarshal(body, &stored); err != nil {
		t.Fatal(err)
	}
	out, problems := stored.WithParameters(overrides)
	for _, p := range problems {
		if p.Severity == geometry.Error {
			t.Fatalf("the respec refused: %s: %s", p.Name, p.Detail)
		}
	}
	return out
}

func placementIn(d *Prototype, asm, id string) (geometry.Child, geometry.Interface) {
	for _, a := range d.Assemblies {
		if a.ID != asm {
			continue
		}
		for _, c := range a.Children {
			if c.ID == id {
				return c, geometry.Interface{}
			}
		}
		for _, f := range a.Interfaces {
			if f.ID == id {
				return geometry.Child{}, f
			}
		}
	}
	return geometry.Child{}, geometry.Interface{}
}

// ‼️ A step's child and interface placed by the model's parameters keep the expression as
// their binding: stored and re-specified, the disc follows the track and the wheelbase.
// #111 read the same child at its number, and a respec left it behind.
func TestAssemble_AStepsChildPlacedByAParameterFollowsItAfterStorageAndRespec(t *testing.T) {
	reply := brakesStep(`{"id":"left-disc","ref":"disc","position":["-half_track", 0, "half_wheelbase"]}`,
		// Three coordinates on the interface alone, each a parameter's value, so the literal
		// note fires on the interface by itself if its binding is ever ignored.
		`{"id":"caliper-mount","position":["half_track", "half_wheelbase", "-half_track"]}`)
	stub := &scriptedStub{replies: []string{reply}}
	c := &Conversation{client: stub}
	next, note := c.buildOneStep(context.Background(), carOnSuspension(), "a car",
		buildTask{Name: "Brakes", What: "brakes", Assembly: "brakes"}, 4, 5)
	if next == nil {
		t.Fatalf("the step was refused: %q", note)
	}
	disc, _ := placementIn(next, "brakes", "left-disc")
	if disc.PositionFrom["x"] != "-half_track" || disc.PositionFrom["z"] != "half_wheelbase" || len(disc.PositionFrom) != 2 {
		t.Fatalf("the disc's expressions were not kept as its binding: %+v", disc)
	}
	if _, mount := placementIn(next, "brakes", "caliper-mount"); mount.PositionFrom["z"] != "-half_track" || len(mount.PositionFrom) != 3 {
		t.Errorf("the interface's expression was not kept as its binding: %+v", mount)
	}
	if !strings.Contains(note, childPositionNote) || strings.Contains(note, "as the number a parameter already holds") {
		t.Errorf("the note does not say the binding was kept, or calls bound positions literals: %q", note)
	}
	if got := placedAt(t, next, "brakes/left-disc"); !near(got, -800, 0, 1350) {
		t.Errorf("the disc is at %v; want (-800, 0, 1350)", got)
	}
	moved := storedAndRespecified(t, next, map[string]float64{"half_track": 900, "half_wheelbase": 1400})
	if got := placedAt(t, moved, "brakes/left-disc"); !near(got, -900, 0, 1400) {
		t.Errorf("after half_track = 900 and half_wheelbase = 1400 the disc is at %v; want (-900, 0, 1400)", got)
	}
	if _, mount := placementIn(moved, "brakes", "caliper-mount"); !near(mount.Position, 900, 1400, -900) {
		t.Errorf("after the respec the caliper mount is at %v; want (900, 1400, -900)", mount.Position)
	}
}

// ‼️ A step that changed a parameter and was refused leaves the model it was refused on
// exactly as it was. Binding the step's edit used to write the new parameter's numbers
// into the maps the edit shared with the model, and the kept model said half_track = 800
// beside an arm bound to it 900 wide.
// docs/bugfix/2026-09-15-binding-a-document-rewrote-the-document-it-was-made-from.md
func TestAssemble_ARefusedStepThatChangedAParameterLeavesTheModelAsItWas(t *testing.T) {
	before := carOnSuspension()
	before.Definitions[0].Size["width"] = 800
	before.Definitions[0].SizeFrom = map[string]string{"width": "half_track"}
	before.Assemblies[1].Children[0].PositionFrom = map[string]string{"x": "-half_track + 160"}
	kept, _ := json.Marshal(before)
	reply := `{"speech":"brakes","prototype_edit":{"patch":{
	  "parameters":[{"name":"half_track","value":900,"unit":"mm","how":"chosen"}],
	  "assemblies":[{"id":"brakes","children":[{"id":"disc","ref":"arm","at":"nowhere"}]}]}}}`
	c := &Conversation{client: &scriptedStub{replies: []string{reply, reply, reply, reply, reply, reply}}}
	next, note := c.buildOneStep(context.Background(), before, "a car", buildTask{Name: "Brakes", What: "brakes", Assembly: "brakes"}, 4, 5)
	if next != nil {
		t.Fatalf("the step that attaches at nothing was accepted; the fence proves nothing: %q", note)
	}
	if got, _ := json.Marshal(before); string(got) != string(kept) {
		t.Errorf("the refused step rewrote the model it was refused on (arm width %v, left corner %v).\n want %s\n  got %s",
			before.Definitions[0].Size["width"], before.Assemblies[1].Children[0].Position, kept, got)
	}
}

// Where the step declares the root places its assembly, a position written with a
// parameter's name is kept as the root child's binding too.
func TestAssemble_ADeclaredPlacementByAParameterKeepsItsBinding(t *testing.T) {
	stub := &scriptedStub{replies: []string{`{"speech":"wheels","prototype_edit":{` + wheelPatch + `,
	  "placements":[{"id":"left-wheel","position":["-half_track", 330, "half_wheelbase"],"rotation":[0,0,90]}]}}`}}
	c := &Conversation{client: stub}
	next, note := c.buildOneStep(context.Background(), carOnSuspension(), "a car",
		buildTask{Name: "Wheels", What: "wheels", Assembly: "wheel"}, 3, 5)
	if next == nil {
		t.Fatalf("the step was refused: %q", note)
	}
	if wheel, _ := placementIn(next, "car", "left-wheel"); wheel.PositionFrom["x"] != "-half_track" {
		t.Fatalf("the declared placement's expression was not kept: %+v", wheel)
	}
	moved := storedAndRespecified(t, next, map[string]float64{"half_track": 950})
	if got := placedAt(t, moved, "left-wheel/tyre"); !near(got, -950, 330, 1350) {
		t.Errorf("after half_track = 950 the declared wheel is at %v; want (-950, 330, 1350)", got)
	}
}

// The contract's own child, read from a whole model and from a conversational edit, is
// kept as a binding that a respec moves.
func TestParseReply_TheContractsChildPlacedByAParameterFollowsARespec(t *testing.T) {
	reply, err := parseReply(&llm.Response{FinishReason: "stop", Content: `{"speech":"c","prototype":{"name":"c","units":"mm",
	  "parameters":[{"name":"half_wheelbase","value":1300,"unit":"mm","how":"chosen"}],
	  "definitions":[{"id":"axle","shape":"box","size":{"width":1600,"height":40,"depth":40}}],
	  "assemblies":[{"id":"chassis","children":[{"id": "front-axle", "ref": "axle", "position": ["half_wheelbase", 0, 0]}]}],"root":"chassis"}}`})
	if err != nil || reply.Prototype == nil {
		t.Fatalf("the reply was not read: %v", err)
	}
	doc := settleDocument(reply.Prototype)
	moved := storedAndRespecified(t, doc, map[string]float64{"half_wheelbase": 1450})
	if got := placedAt(t, moved, "front-axle"); !near(got, 1450, 0, 0) {
		t.Errorf("after half_wheelbase = 1450 the front axle is at %v; want (1450, 0, 0)", got)
	}

	c := &Conversation{client: &scriptedStub{replies: []string{`{"speech":"added a damper","prototype_edit":{"patch":{
	  "assemblies":[{"id":"suspension","interfaces":[{"id":"hub","position":["-half_track / 5",0,0]}],
	    "children":[{"id":"arm","ref":"arm"},{"id":"damper","ref":"arm","position":["-half_track", 0, "half_wheelbase"]}]}]}}}`}}}
	edited, err := c.Respond(context.Background(), "", nil, "add a damper to the suspension", "", carOnSuspension(), nil)
	if err != nil || edited.Prototype == nil {
		t.Fatalf("the edit was lost: %v", err)
	}
	moved = storedAndRespecified(t, edited.Prototype, map[string]float64{"half_track": 1000})
	if damper, _ := placementIn(moved, "suspension", "damper"); !near(damper.Position, -1000, 0, 1350) {
		t.Errorf("after half_track = 1000 the edited damper is at %v; want (-1000, 0, 1350)", damper.Position)
	}
	if _, hub := placementIn(moved, "suspension", "hub"); !near(hub.Position, -200, 0, 0) {
		t.Errorf("after half_track = 1000 the edited hub is at %v; want (-200, 0, 0)", hub.Position)
	}
}

// ‼️ A step that types small multiples of one parameter is told the form, with the sign
// the coordinate needs, deterministically; a value a parameter holds is named before a
// multiple; and multiples of a parameter under 50 mm are never read, because every round
// offset is a multiple of something that small.
func TestAssemble_AStepThatRetypesAMultipleOfAParameterIsToldTheForm(t *testing.T) {
	withHubOffset := func() *Prototype {
		d := carOnSuspension()
		d.Derived = []geometry.Derived{{Name: "hub_offset", Expression: "half_track - 640", Why: "the hub sits outboard of the pick-ups"}}
		return d
	}
	multiples := brakesStep(`{"id":"left-disc","ref":"disc","position":[-1600,0,4050]},{"id":"right-disc","ref":"disc","position":[1600,0,4050]}`,
		`{"id":"caliper-mount","position":[0,0,-640]}`)
	want := `This step typed 5 position(s) as the number a parameter already holds: ` +
		`1600 is 2 * half_track (brakes/left-disc x = -1600, brakes/right-disc x = 1600); ` +
		`4050 is 3 * half_wheelbase (brakes/left-disc z = 4050, brakes/right-disc z = 4050); ` +
		`640 is 4 * hub_offset (brakes interface caliper-mount z = -640). ` +
		`Write the parameter's name so the position follows it: "position_from": {"x": "-2 * half_track"} on a part or a definition, ` +
		`and "-2 * half_track" in a child's or an interface's "position", which FORGE keeps bound to the parameters.`
	step := func(d *Prototype, reply string) string {
		t.Helper()
		c := &Conversation{client: &scriptedStub{replies: []string{reply}}}
		next, note := c.buildOneStep(context.Background(), d, "a car", buildTask{Name: "Brakes", What: "brakes", Assembly: "brakes"}, 4, 5)
		if next == nil {
			t.Fatalf("the step was refused: %q", note)
		}
		return note
	}
	first := step(withHubOffset(), multiples)
	if !strings.Contains(first, want) {
		t.Errorf("the note does not name the multiples:\n got %s\nwant %s", first, want)
	}
	for i := 0; i < 4; i++ {
		if again := step(withHubOffset(), multiples); again != first {
			t.Errorf("the same step said two different things:\n%s\n%s", first, again)
		}
	}

	withTrack := withHubOffset()
	withTrack.Derived = append(withTrack.Derived, geometry.Derived{Name: "track", Expression: "2 * half_track", Why: "both sides"})
	if note := step(withTrack, multiples); !strings.Contains(note, "1600 is track (brakes/left-disc x = -1600") {
		t.Errorf("a value a derived value holds was named as a multiple instead:\n%s", note)
	}

	at := func(value float64, unit string) *Prototype {
		d := carOnSuspension()
		d.Parameters = append(d.Parameters, geometry.Parameter{Name: "wall", Value: value, Unit: unit, How: "chosen"})
		return d
	}
	for _, tc := range []struct {
		name, reply string
		doc         *Prototype
		noted       bool
	}{
		{"multiples of a 49 mm parameter", brakesStep(`{"id":"l","ref":"disc","position":[98,147,196]}`, ""), at(49, "mm"), false},
		{"multiples of a 20 mm pitch", brakesStep(`{"id":"l","ref":"disc","position":[40,60,80]}`, ""), at(20, "mm"), false},
		{"multiples of a 50 mm parameter", brakesStep(`{"id":"l","ref":"disc","position":[100,150,200]}`, ""), at(50, "mm"), true},
		{"multiples of a parameter that is not a length", brakesStep(`{"id":"l","ref":"disc","position":[100,150,200]}`, ""), at(50, ""), false},
		{"five times a parameter", brakesStep(`{"id":"l","ref":"disc","position":[4000,4000,4000]}`, ""), carOnSuspension(), false},
		{"multiples bound to the parameters", brakesStep(`{"id":"l","ref":"disc","position":[1600,2700,1600],
		  "position_from":{"x":"2 * half_track","y":"2 * half_wheelbase","z":"2 * half_track"}}`, ""), carOnSuspension(), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			note := step(tc.doc, tc.reply)
			if got := strings.Contains(note, "as the number a parameter already holds"); got != tc.noted {
				t.Errorf("noted = %v, want %v:\n%s", got, tc.noted, note)
			}
		})
	}
}
