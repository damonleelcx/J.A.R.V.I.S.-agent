package geometry

import (
	"strings"
	"testing"
)

// Attaching one subsystem to another: from the root, never from inside an assembly.
// Live car-quality run 2 (2026-09-15) lost its wheels to an `at` written inside the
// wheels assembly that reached into the suspension.
// docs/bugfix/2026-09-15-an-attachment-into-another-assembly-was-refused-without-the-fix.md

// carWithSuspension is a root "car" placing a suspension twice (the right mirrored),
// a part, and a wheels assembly whose one child is attached at at.
func carWithSuspension(at string) Document {
	return Document{Name: "car", Units: "mm", Root: "car",
		Definitions: []Part{block("block"), block("tyre")},
		Assemblies: []Assembly{
			{ID: "car", Interfaces: []Interface{{ID: "hub-face", Position: []float64{0, 0, 900}}}, Children: []Child{
				{ID: "suspension-left", Ref: "suspension", Position: []float64{-800, 300, 0}},
				{ID: "suspension-right", Ref: "suspension", Position: []float64{800, 300, 0}, Mirror: "x"},
				{ID: "plain", Ref: "block"},
				{ID: "wheels", Ref: "wheels"},
			}},
			{ID: "suspension", Interfaces: []Interface{{ID: "hub", Position: []float64{-100, 0, 0}}},
				Children: []Child{{ID: "arm", Ref: "block"}, {ID: "knuckle", Ref: "knuckle", Position: []float64{-50, 0, 0}}}},
			{ID: "knuckle", Interfaces: []Interface{{ID: "hub", Position: []float64{-20, 0, 0}, Rotation: []float64{0, 0, 90}}},
				Children: []Child{{ID: "pin", Ref: "block"}}},
			{ID: "wheels", Children: []Child{{ID: "left", Ref: "tyre", At: at}, {ID: "rim", Ref: "rim"}}},
			{ID: "rim", Children: []Child{{ID: "disc", Ref: "block"}}},
		}}
}

// ‼️ A child inside an assembly whose `at` reaches another subsystem is refused by
// name, with the path to write from the root and — when that would fail too — why.
func TestInterface_AnAttachmentThatLeavesItsAssemblyIsRefusedWithTheFix(t *testing.T) {
	for _, tc := range []struct {
		name, at string
		says     []string
		not      []string
	}{
		{"a sibling of its assembly, from inside it", "suspension-left/hub",
			[]string{`outside its assembly "wheels"`, `attach it from the root "car" instead, as a child there with "at": "suspension-left/hub"`,
				"written once and may be placed anywhere"},
			[]string{"fails too", "has no child"}},
		{"the path run 2 wrote, starting at the root's own id", "car/suspension-left/knuckle/hub",
			[]string{`outside its assembly "wheels"`, `with "at": "suspension-left/knuckle/hub"`},
			[]string{"fails too"}},
		{"an interface of the root, by its id alone", "hub-face",
			[]string{`outside its assembly "wheels"`, `with "at": "hub-face"`}, []string{"fails too"}},
		{"a path that would fail from the root too", "car/plain/hub-face",
			[]string{`attach it from the root "car" instead, as a child there with "at": "plain/hub-face"`,
				`from the root that path fails too: "plain" places "block", which is not an assembly`,
				"wrap the part in a one-child assembly"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := carWithSuspension(tc.at)
			var details []string
			for _, p := range d.Faults() {
				if p.Name == "wheels/left" {
					details = append(details, p.Detail)
				}
			}
			if len(details) != 1 {
				t.Fatalf("want one fault on wheels/left, got %q (all: %+v)", details, d.Faults())
			}
			for _, want := range tc.says {
				if !strings.Contains(details[0], want) {
					t.Errorf("the refusal does not say %q:\n%s", want, details[0])
				}
			}
			for _, bad := range tc.not {
				if strings.Contains(details[0], bad) {
					t.Errorf("the refusal says %q:\n%s", bad, details[0])
				}
			}
			for _, p := range d.PlacedParts() {
				if p.ID == "wheels/left" {
					t.Error("the refused child was placed anyway")
				}
			}
		})
	}
}

// The refusals that are not about leaving the assembly keep their own words: a path
// inside the assembly that is wrong, and any path written on a child of the root.
func TestInterface_OnlyAPathThatLeavesItsAssemblyIsToldToAttachFromTheRoot(t *testing.T) {
	inside := carWithSuspension("rim/nowhere")
	rootLevel := carWithSuspension("")
	rootLevel.Assemblies[0].Children = append(rootLevel.Assemblies[0].Children, Child{ID: "lost", Ref: "block", At: "ghost/hub"})
	for _, tc := range []struct {
		name, child, want string
		d                 Document
	}{
		{"a wrong interface on its own child", "wheels/left", `assembly "rim" declares no interface "nowhere"`, inside},
		{"a child of the root", "lost", `assembly "car" has no child "ghost"`, rootLevel},
	} {
		t.Run(tc.name, func(t *testing.T) {
			found := false
			for _, p := range tc.d.Faults() {
				if p.Name != tc.child {
					continue
				}
				found = true
				if !strings.Contains(p.Detail, tc.want) || strings.Contains(p.Detail, "attach it from the root") {
					t.Errorf("want %q and no instruction to attach from the root, got %q", tc.want, p.Detail)
				}
			}
			if !found {
				t.Errorf("no fault on %s: %+v", tc.child, tc.d.Faults())
			}
		})
	}
}

// ‼️ What a step is shown it can attach at: every interface on what the root places,
// by the path a child of the root writes, deepest first-level-down included, and where
// each frame sits in the root's frame.
func TestInterfacesFromRoot_ListsWhereAChildOfTheRootCanAttach(t *testing.T) {
	d := carWithSuspension("")
	d.Assemblies[3].Children = d.Assemblies[3].Children[1:] // wheels holds only its rim
	list, more := d.InterfacesFromRoot("wheels", 10)
	var paths []string
	for _, f := range list {
		paths = append(paths, f.At)
	}
	want := "suspension-left/hub,suspension-left/knuckle/hub,suspension-right/hub,suspension-right/knuckle/hub"
	if got := strings.Join(paths, ","); got != want || more != 0 {
		t.Fatalf("listed %s (%d more); want %s", got, more, want)
	}
	// Composed by hand: the left suspension at (-800,300,0), its knuckle 50 further in
	// -x, the knuckle's hub 20 further, turned 90° about z.
	if f := list[1]; f.Position[0] != -870 || f.Position[1] != 300 || f.Position[2] != 0 || len(f.Rotation) != 3 || f.Rotation[2] != 90 || f.Mirrored {
		t.Errorf("suspension-left/knuckle/hub is listed at %v %v mirrored=%v; want (-870, 300, 0) turned 90 about z", f.Position, f.Rotation, f.Mirrored)
	}
	// The right side is the left mirrored across x: its hub is at +900 and reflected.
	if f := list[2]; f.Position[0] != 900 || !f.Mirrored {
		t.Errorf("suspension-right/hub is listed at %v mirrored=%v; want x = 900, mirrored", f.Position, f.Mirrored)
	}
	// Every listed path attaches when written on a child of the root.
	for _, f := range list {
		probe := d
		probe.Assemblies = cloneAssemblies(d.Assemblies)
		probe.Assemblies[0].Children = append(probe.Assemblies[0].Children, Child{ID: "probe", Ref: "block", At: f.At})
		for _, p := range probe.TreeProblems() {
			if p.Severity == Error {
				t.Errorf("the listed path %q does not attach: %s %s", f.At, p.Name, p.Detail)
			}
		}
	}
	// Bounded, and says how many it left out.
	if list, more := d.InterfacesFromRoot("wheels", 1); len(list) != 1 || more != 3 {
		t.Errorf("limited to 1, listed %d with %d more; want 1 and 3", len(list), more)
	}
	// The assembly being built is not offered as somewhere to attach itself.
	if list, _ := d.InterfacesFromRoot("suspension", 10); len(list) != 0 {
		t.Errorf("the step building the suspension is offered its own interfaces: %+v", list)
	}
}
