package agent

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
)

// Fences for attaching one subsystem to another from the root, and for binding
// positions to parameters (2026-09-15, attach and bind).
// docs/bugfix/2026-09-15-an-attachment-into-another-assembly-was-refused-without-the-fix.md

// carOnSuspension is a root "car" placing one suspension design twice, the right
// side mirrored, each declaring a "hub", with the parameters a car places against.
func carOnSuspension() *Prototype {
	return &geometry.Document{Name: "car", Units: "mm", Root: "car",
		Parameters: []geometry.Parameter{
			{Name: "half_wheelbase", Value: 1350, Unit: "mm", How: "chosen"},
			{Name: "half_track", Value: 800, Unit: "mm", How: "chosen"},
		},
		Definitions: []geometry.Part{{ID: "arm", Shape: "box", Size: map[string]float64{"width": 300, "height": 30, "depth": 60}}},
		Assemblies: []geometry.Assembly{
			{ID: "suspension", Interfaces: []geometry.Interface{{ID: "hub", Position: []float64{-160, 0, 0}}},
				Children: []geometry.Child{{ID: "arm", Ref: "arm"}}},
			{ID: "car", Children: []geometry.Child{
				{ID: "suspension-left", Ref: "suspension", Position: []float64{-640, 330, 1350}},
				{ID: "suspension-right", Ref: "suspension", Position: []float64{640, 330, 1350}, Mirror: "x"},
			}},
		}}
}

const wheelPatch = `"patch":{
	  "definitions":[{"id":"tyre","shape":"cylinder","size":{"radius":330,"height":240}}],
	  "assemblies":[{"id":"wheel","children":[{"id":"tyre","ref":"tyre"}]}]}`

func placedAt(t *testing.T, d *Prototype, id string) []float64 {
	t.Helper()
	for _, p := range d.PlacedParts() {
		if p.ID == id {
			return p.Position
		}
	}
	t.Fatalf("no part %q is placed; the model places %v", id, placedIDs(d))
	return nil
}

func placedIDs(d *Prototype) []string {
	var ids []string
	for _, p := range d.PlacedParts() {
		ids = append(ids, p.ID)
	}
	return ids
}

func near(a []float64, want ...float64) bool {
	if len(a) != len(want) {
		return false
	}
	for i := range a {
		if d := a[i] - want[i]; d > 1e-6 || d < -1e-6 {
			return false
		}
	}
	return true
}

// ‼️ A step that says where the root places its assembly has it attached there, not
// at the root's origin, and is told which attachment was used.
func TestAssemble_AStepsDeclaredPlacementAttachesItsAssemblyFromTheRoot(t *testing.T) {
	before := carOnSuspension()
	stub := &scriptedStub{replies: []string{`{"speech":"wheels","prototype_edit":{` + wheelPatch + `,
	  "placements":[{"id":"left-wheel","at":"suspension-left/hub","rotation":[0,0,90]},
	                {"id":"right-wheel","at":"suspension-right/hub","rotation":[0,0,90]}]}}`}}
	c := &Conversation{client: stub}
	next, note := c.buildOneStep(context.Background(), before, "a car",
		buildTask{Name: "Wheels", What: "wheels", Assembly: "wheel"}, 3, 5)
	if next == nil {
		t.Fatalf("the step was refused: %q", note)
	}
	// The left suspension at x = -640, its hub 160 further out; the right mirrored.
	if got := placedAt(t, next, "left-wheel/tyre"); !near(got, -800, 330, 1350) {
		t.Errorf("the left wheel is at %v; want the left hub, (-800, 330, 1350)", got)
	}
	if got := placedAt(t, next, "right-wheel/tyre"); !near(got, 800, 330, 1350) {
		t.Errorf("the right wheel is at %v; want the right hub, (800, 330, 1350)", got)
	}
	want := `FORGE placed the assembly "wheel" from the root "car" where this step declared it: ` +
		`"left-wheel" at "suspension-left/hub"; "right-wheel" at "suspension-right/hub".`
	if !strings.Contains(note, want) || strings.Contains(note, "root's origin") {
		t.Errorf("the note does not say which attachment was used:\n%s", note)
	}
	if len(before.Assemblies[1].Children) != 2 {
		t.Errorf("placing the wheels changed the model before the step: %+v", before.Assemblies[1].Children)
	}
}

// The placements are read wherever a model nests them, and a position in one may name
// the model's parameters.
func TestAssemble_DeclaredPlacementsAreReadWhereverTheStepPutThem(t *testing.T) {
	for _, tc := range []struct{ name, reply, part string }{
		{"beside the edit", `{"speech":"w","prototype_edit":{` + wheelPatch + `},
		  "placements":[{"id":"left-wheel","at":"suspension-left/hub"}]}`, "left-wheel/tyre"},
		{"inside the patch", `{"speech":"w","prototype_edit":{"patch":{
		  "definitions":[{"id":"tyre","shape":"cylinder","size":{"radius":330,"height":240}}],
		  "assemblies":[{"id":"wheel","children":[{"id":"tyre","ref":"tyre"}]}],
		  "placements":[{"id":"left-wheel","at":"suspension-left/hub"}]}}}`, "left-wheel/tyre"},
		{"at a position naming the model's parameters", `{"speech":"w","prototype_edit":{` + wheelPatch + `,
		  "placements":[{"id":"spare","position":["-half_track", 330, "half_wheelbase"]}]}}`, "spare/tyre"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &scriptedStub{replies: []string{tc.reply}}
			c := &Conversation{client: stub}
			next, note := c.buildOneStep(context.Background(), carOnSuspension(), "a car",
				buildTask{Name: "Wheels", What: "wheels", Assembly: "wheel"}, 3, 5)
			if next == nil {
				t.Fatalf("the step was refused: %q", note)
			}
			if got := placedAt(t, next, tc.part); !near(got, -800, 330, 1350) {
				t.Errorf("%s is at %v; want (-800, 330, 1350)", tc.part, got)
			}
			if !strings.Contains(note, "where this step declared it") {
				t.Errorf("the note does not say the declaration was used: %q", note)
			}
		})
	}
}

// A declaration never places twice, never places what is not an assembly, and says so.
func TestAssemble_ADeclaredPlacementThatCannotBeUsedIsNotUsedAndSaysSo(t *testing.T) {
	root := `{"id":"car","children":[{"id":"suspension-left","ref":"suspension","position":[-640,330,1350]},
	  {"id":"suspension-right","ref":"suspension","position":[640,330,1350],"mirror":"x"},
	  {"id":"front-wheel","ref":"wheel","at":"suspension-left/hub"}]}`
	stub := &scriptedStub{replies: []string{`{"speech":"w","prototype_edit":{"patch":{
	  "definitions":[{"id":"tyre","shape":"cylinder","size":{"radius":330,"height":240}}],
	  "assemblies":[{"id":"wheel","children":[{"id":"tyre","ref":"tyre"}]},` + root + `]},
	  "placements":[{"id":"left-wheel","at":"suspension-left/hub"},{"id":"x","ref":"tyre"}]}}`}}
	c := &Conversation{client: stub}
	next, note := c.buildOneStep(context.Background(), carOnSuspension(), "a car",
		buildTask{Name: "Wheels", What: "wheels", Assembly: "wheel"}, 3, 5)
	if next == nil {
		t.Fatalf("the step was refused: %q", note)
	}
	if got := strings.Join(placedIDs(next), ","); strings.Contains(got, "left-wheel") || !strings.Contains(got, "front-wheel/tyre") {
		t.Errorf("the model places %s; want the step's own placement and not the declared one", got)
	}
	for _, want := range []string{`This step placed the assembly "wheel" itself, so the placements it declared for it were not used.`,
		`This step declared placements of "tyre", which is not an assembly in the model, so they were not used.`} {
		if !strings.Contains(note, want) {
			t.Errorf("the note does not say %q:\n%s", want, note)
		}
	}
}

// ‼️ A step is shown, by the path it writes, every interface on what the root places.
func TestAssemble_AStepIsShownTheInterfacesItCanAttachAtFromTheRoot(t *testing.T) {
	view := SubtreeModel(carOnSuspension(), "wheel")
	want := `"root_interfaces":[{"at":"suspension-left/hub","position":[-800,330,1350]},` +
		`{"at":"suspension-right/hub","position":[800,330,1350],"mirrored":true}]`
	if !strings.Contains(view, want) {
		t.Errorf("a new assembly's view does not list where it can attach from the root:\n%s", view)
	}
	stub := &scriptedStub{}
	c := &Conversation{client: stub}
	c.buildOneStep(context.Background(), carOnSuspension(), "a car", buildTask{Name: "Wheels", What: "wheels", Assembly: "wheel"}, 3, 5)
	if len(stub.asked) != 1 || !strings.Contains(stub.asked[0], want) {
		t.Errorf("the step's prompt does not carry the root interfaces:\n%v", stub.asked)
	}
	// A step extending an assembly the root already places sees the others', not its own.
	d := carOnSuspension()
	d.Assemblies = append(d.Assemblies, geometry.Assembly{ID: "engine", Interfaces: []geometry.Interface{{ID: "mount", Position: []float64{0, 400, 0}}},
		Children: []geometry.Child{{ID: "arm", Ref: "arm"}}})
	d.Assemblies[1].Children = append(d.Assemblies[1].Children, geometry.Child{ID: "engine", Ref: "engine"})
	existing := SubtreeModel(d, "suspension")
	if !strings.Contains(existing, `"root_interfaces":[{"at":"engine/mount","position":[0,400,0]}]`) {
		t.Errorf("an existing assembly's view lists the wrong root interfaces:\n%s", existing)
	}
}

// ‼️ A later step is taught to mount from the root with "placements" and to bind
// positions; the first step to declare what later steps place against.
func TestAssemble_AStepIsTaughtToAttachFromTheRootAndToBindPositions(t *testing.T) {
	stub := &scriptedStub{}
	c := &Conversation{client: stub}
	c.buildOneStep(context.Background(), &geometry.Document{Name: "car", Units: "mm"}, "a car",
		buildTask{Name: "Chassis", What: "the chassis", Assembly: "chassis"}, 1, 3)
	c.buildOneStep(context.Background(), carOnSuspension(), "a car", buildTask{Name: "Wheels", What: "wheels", Assembly: "wheel"}, 2, 3)
	if len(stub.systems) != 2 {
		t.Fatalf("want two steps asked, got %d", len(stub.systems))
	}
	first, later := stub.systems[0], stub.systems[1]
	for _, want := range []string{`as "parameters", and write positions and sizes with their names, not
  their values`, `expression only in a "_from" field or in a child's or an interface's
  "position"`} {
		if !strings.Contains(first, want) {
			t.Errorf("the first step is not told %q", want)
		}
	}
	for _, want := range []string{"Mount on ANOTHER subsystem from the root, never from inside your assembly",
		`FORGE refuses one that reaches another subsystem`, `under "placements" beside "patch"`, `"root_interfaces"`,
		`"placements": [{"id": "left-wheel", "ref": "wheel", "at": "suspension-left/hub", "rotation": [0, 0, 90]}`,
		`the parameter's name in a child's or an interface's "position"`, "Never\n  retype a parameter's value as a number",
		`"placements": [...]}}`} {
		if !strings.Contains(later, want) {
			t.Errorf("a later step is not told %q", want)
		}
	}
}

// ‼️ The contract says an "at" never leaves its assembly, and its example of mounting
// one subsystem on another from the root is a child FORGE attaches — while the same
// child written inside another assembly is refused with the fix.
func TestTheContractSaysAnAtNeverLeavesItsAssemblyAndItsExampleAttaches(t *testing.T) {
	for _, want := range []string{`An "at" never leaves its own assembly`, "FORGE refuses a path that leaves it",
		"where BOTH are placed, from the root"} {
		if !strings.Contains(geometryContract, want) {
			t.Errorf("the contract does not say %q", want)
		}
	}
	line := regexp.MustCompile(`\{"id": "left-wheel", "ref": "wheel", "at": "suspension-left/hub"[^\n]*\}`).FindString(geometryContract)
	if line == "" {
		t.Fatal("the contract has no example of a wheel mounted from the root at suspension-left/hub")
	}
	dec := json.NewDecoder(strings.NewReader(line))
	dec.DisallowUnknownFields()
	var child geometry.Child
	if err := dec.Decode(&child); err != nil {
		t.Fatalf("the contract's example child is not a child a model could copy: %v\n%s", err, line)
	}
	build := func(wheels bool) *Prototype {
		d := carOnSuspension()
		d.Definitions = append(d.Definitions, geometry.Part{ID: "tyre", Shape: "cylinder", Size: map[string]float64{"radius": 330, "height": 240}})
		d.Assemblies = append(d.Assemblies, geometry.Assembly{ID: "wheel", Children: []geometry.Child{{ID: "tyre", Ref: "tyre"}}})
		if wheels {
			d.Assemblies = append(d.Assemblies, geometry.Assembly{ID: "wheels", Children: []geometry.Child{child}})
			d.Assemblies[1].Children = append(d.Assemblies[1].Children, geometry.Child{ID: "wheels", Ref: "wheels"})
		} else {
			d.Assemblies[1].Children = append(d.Assemblies[1].Children, child)
		}
		return d
	}
	fromRoot := build(false)
	if faults := fromRoot.Faults(); len(faults) != 0 {
		t.Fatalf("the contract's example is refused from the root: %+v", faults)
	}
	for _, p := range fromRoot.PlacedParts() {
		if p.ID == "left-wheel/tyre" && (!near(p.Position, -800, 330, 1350) || len(p.Rotation) != 3 || p.Rotation[2] != 90) {
			t.Errorf("the example's wheel is at %v turned %v; want on the left hub, turned [0, 0, 90]", p.Position, p.Rotation)
		}
	}
	inside := build(true).Faults()
	if len(inside) != 1 || !strings.Contains(inside[0].Detail, `attach it from the root "car" instead, as a child there with "at": "suspension-left/hub"`) {
		t.Errorf("the example written inside a wheels assembly is not refused with the fix: %+v", inside)
	}
}

// ‼️ The contract shows a size and a position written with a parameter, and both read.
func TestTheContractShowsASizeAndAPositionWrittenWithAParameter(t *testing.T) {
	def := regexp.MustCompile(`\{"id": "rail", [^\n]*"size_from": \{"depth": "wheelbase"\}\}`).FindString(geometryContract)
	child := regexp.MustCompile(`\{"id": "front-axle", "ref": "axle", "position": \["half_wheelbase", 0, 0\]\}`).FindString(geometryContract)
	if def == "" || child == "" || !strings.Contains(geometryContract, "Never type the number a parameter holds") {
		t.Fatalf("the contract does not show a bound definition and a child placed by a parameter (%q, %q)", def, child)
	}
	reply, err := parseReply(&llm.Response{FinishReason: "stop", Content: `{"speech":"c","prototype":{"name":"c","units":"mm",
	  "parameters":[{"name":"wheelbase","value":2600,"unit":"mm","how":"chosen"},{"name":"half_wheelbase","value":1300,"unit":"mm","how":"chosen"}],
	  "definitions":[` + def + `,{"id":"axle","shape":"box","size":{"width":1600,"height":40,"depth":40}}],
	  "assemblies":[{"id":"chassis","children":[{"id":"rail","ref":"rail"},` + child + `]}],"root":"chassis"}}`})
	if err != nil || reply.Prototype == nil {
		t.Fatalf("a reply written as the contract shows was not read: %v %+v", err, reply)
	}
	doc := settleDocument(reply.Prototype)
	if got := doc.Definitions[0].Size["depth"]; got != 2600 {
		t.Errorf("the rail's depth is %v; bound to wheelbase = 2600 it is 2600", got)
	}
	if got := placedAt(t, doc, "front-axle"); !near(got, 1300, 0, 0) {
		t.Errorf("the front axle is at %v; half_wheelbase = 1300 puts it at (1300, 0, 0)", got)
	}
}

// ‼️ A step is shown each parameter and derived value with what it works out to now.
func TestAssemble_AStepIsShownTheParametersItCanBindTo(t *testing.T) {
	d := carOnSuspension()
	d.Derived = []geometry.Derived{{Name: "hub_offset", Expression: "half_track - 640", Why: "the hub sits outboard of the pick-ups"}}
	stub := &scriptedStub{}
	c := &Conversation{client: stub}
	c.buildOneStep(context.Background(), d, "a car", buildTask{Name: "Wheels", What: "wheels", Assembly: "wheel"}, 2, 3)
	c.buildOneStep(context.Background(), onePlate(), "a car", buildTask{Name: "Wheels", What: "wheels"}, 2, 3)
	want := "Parameters to write positions and sizes with, as they stand now: half_wheelbase = 1350 mm, " +
		"half_track = 800 mm, hub_offset = 160 mm (half_track - 640). Write the name, not the number."
	if len(stub.asked) != 2 || !strings.Contains(stub.asked[0], want) {
		t.Errorf("the step is not shown its parameters' values:\n%v", stub.asked)
	}
	if strings.Contains(stub.asked[1], "Parameters to write") {
		t.Errorf("a model with no parameters is shown a parameter line:\n%s", stub.asked[1])
	}
}

// brakesStep is a step reply adding a brakes assembly with the given children and interfaces.
func brakesStep(children, interfaces string) string {
	return `{"speech":"brakes","prototype_edit":{"patch":{
	  "definitions":[{"id":"disc","shape":"cylinder","size":{"radius":150,"height":30}` + `}],
	  "assemblies":[{"id":"brakes","interfaces":[` + interfaces + `],"children":[` + children + `]}]}}}`
}

// ‼️ A step that types, as positions, numbers its parameters already hold is told which
// parameter each one is, by name, in document order — and not for fewer than three, for
// numbers no parameter holds, for positions it did not introduce, or for bound ones.
func TestAssemble_AStepThatRetypesAParametersValueIsToldWhichParameter(t *testing.T) {
	retyped := brakesStep(`{"id":"left-disc","ref":"disc","position":[-800,0,1350]},{"id":"right-disc","ref":"disc","position":[800,0,1350]}`,
		`{"id":"caliper-mount","position":[0,0,-1350]}`)
	want := `This step typed 5 position(s) as the number a parameter already holds: ` +
		`800 is half_track (brakes/left-disc x = -800, brakes/right-disc x = 800); ` +
		`1350 is half_wheelbase (brakes/left-disc z = 1350, brakes/right-disc z = 1350, brakes interface caliper-mount z = -1350). ` +
		`Write the parameter's name so the position follows it: "position_from": {"x": "-half_track"} on a part or a definition, ` +
		`and "-half_track" in a child's or an interface's "position", which FORGE works out from the parameters.`
	var notes []string
	for i := 0; i < 5; i++ {
		stub := &scriptedStub{replies: []string{retyped}}
		c := &Conversation{client: stub}
		next, note := c.buildOneStep(context.Background(), carOnSuspension(), "a car", buildTask{Name: "Brakes", What: "brakes", Assembly: "brakes"}, 4, 5)
		if next == nil {
			t.Fatalf("a step that retyped parameters was refused: %q", note)
		}
		notes = append(notes, note)
	}
	if !strings.Contains(notes[0], want) {
		t.Errorf("the note does not name the parameters in document order:\n got %s\nwant %s", notes[0], want)
	}
	for _, n := range notes[1:] {
		if n != notes[0] {
			t.Errorf("the same step said two different things:\n%s\n%s", notes[0], n)
		}
	}

	alreadyThere := carOnSuspension()
	alreadyThere.Assemblies = append(alreadyThere.Assemblies, geometry.Assembly{ID: "frame", Children: []geometry.Child{
		{ID: "a", Ref: "arm", Position: []float64{800, 0, 1350}}, {ID: "b", Ref: "arm", Position: []float64{-800, 0, 1350}}}})
	for _, tc := range []struct {
		name, reply string
		doc         *Prototype
	}{
		{"two retyped numbers", brakesStep(`{"id":"left-disc","ref":"disc","position":[-800,0,1350]}`, ""), carOnSuspension()},
		{"numbers no parameter holds", brakesStep(`{"id":"l","ref":"disc","position":[-801,0,1351]},{"id":"r","ref":"disc","position":[801,0,1351]}`,
			`{"id":"m","position":[0,0,-1351]}`), carOnSuspension()},
		{"positions the model already had", brakesStep(`{"id":"left-disc","ref":"disc","position":[0,40,0]}`, ""), alreadyThere},
		{"positions bound to the parameters", `{"speech":"brakes","prototype_edit":{"patch":{
		  "definitions":[{"id":"disc","shape":"cylinder","size":{"radius":150,"height":30},"position":[800,800,1350],
		    "position_from":{"x":"half_track","y":"half_track","z":"half_wheelbase"}}],
		  "assemblies":[{"id":"brakes","children":[{"id":"disc","ref":"disc"}]}]}}}`, carOnSuspension()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &scriptedStub{replies: []string{tc.reply}}
			c := &Conversation{client: stub}
			next, note := c.buildOneStep(context.Background(), tc.doc, "a car", buildTask{Name: "Brakes", What: "brakes", Assembly: "brakes"}, 4, 5)
			if next == nil {
				t.Fatalf("the step was refused: %q", note)
			}
			if strings.Contains(note, "as the number a parameter already holds") {
				t.Errorf("the step was told it retyped parameters:\n%s", note)
			}
		})
	}
}

// ‼️ A later step that places a child by the name of a parameter only the model has
// is read, not lost as unreadable.
func TestAssemble_AStepsPlacementExpressionReadsTheModelsParameters(t *testing.T) {
	reply := brakesStep(`{"id":"left-disc","ref":"disc","position":["-half_track", 0, "half_wheelbase"]}`, "")
	stub := &scriptedStub{replies: []string{reply}}
	c := &Conversation{client: stub}
	next, note := c.buildOneStep(context.Background(), carOnSuspension(), "a car", buildTask{Name: "Brakes", What: "brakes", Assembly: "brakes"}, 4, 5)
	if next == nil {
		t.Fatalf("the step was refused: %q", note)
	}
	if got := placedAt(t, next, "brakes/left-disc"); !near(got, -800, 0, 1350) {
		t.Errorf("the disc is at %v; over the model's parameters it is (-800, 0, 1350)", got)
	}
	if !strings.Contains(note, childPositionNote) {
		t.Errorf("the reader is not told the placement was read at its value: %q", note)
	}
	if len(next.Parameters) != 2 {
		t.Errorf("reading the placement changed the model's parameters: %+v", next.Parameters)
	}
	// With no such parameter anywhere, still refused by name, as before.
	bare := carOnSuspension()
	bare.Parameters = nil
	stub = &scriptedStub{replies: []string{reply}}
	c = &Conversation{client: stub}
	if next, note := c.buildOneStep(context.Background(), bare, "a car", buildTask{Name: "Brakes", What: "brakes", Assembly: "brakes"}, 4, 5); next != nil || StepGateOf(note) != "unreadable" {
		t.Errorf("a position over a name nothing declares was read: %v %q", next != nil, note)
	}
}

// ‼️ On the conversational path too: an edit that places a child by the name of a
// parameter the model on screen has, as the contract now teaches, is read, not lost.
func TestRespond_AnEditPlacingAChildByTheModelsParameterIsRead(t *testing.T) {
	c := &Conversation{client: &scriptedStub{replies: []string{`{"speech":"added a damper","prototype_edit":{"patch":{
	  "assemblies":[{"id":"suspension","interfaces":[{"id":"hub","position":[-160,0,0]}],
	    "children":[{"id":"arm","ref":"arm"},{"id":"damper","ref":"arm","position":["-half_track", 0, "half_wheelbase"]}]}]}}}`}}}
	reply, err := c.Respond(context.Background(), "", nil, "add a damper to the suspension", "", carOnSuspension(), nil)
	if err != nil || reply.Prototype == nil {
		t.Fatalf("the edit was lost: %v %+v", err, reply)
	}
	var got []float64
	for _, a := range reply.Prototype.Assemblies {
		for _, ch := range a.Children {
			if a.ID == "suspension" && ch.ID == "damper" {
				got = ch.Position
			}
		}
	}
	if !near(got, -800, 0, 1350) {
		t.Errorf("the damper is at %v; over the model's parameters it is (-800, 0, 1350)", got)
	}
	if !strings.Contains(reply.Repaired, childPositionNote) {
		t.Errorf("the reader is not told the placement was read at its value: %q", reply.Repaired)
	}
}
