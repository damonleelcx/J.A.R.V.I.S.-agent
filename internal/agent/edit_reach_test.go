package agent

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// An edit says, in the turn, every placed part it reached. Phase 7, stage E1 of
// docs/plan-2026-09-13-millions-of-parts.md: a path names the design it places,
// so a change that reads like one part can change many, and the person has to
// hear that where they are looking.

// The streamed turn is the one the product runs, and a note resolveEdit makes but
// no event carries is a note nobody reads.
func TestResolveEdit_TheStreamedTurnSaysEveryOccurrenceAnEditReached(t *testing.T) {
	stub := &streamingStub{repairStub{reply: `{"speech":"I have made the left wheel larger.","prototype_edit":` +
		`{"patch":{"definitions":[{"id":"left","name":"Wheel","shape":"cylinder","size":{"radius":35,"height":12}}]}}}`}}
	c := &Conversation{client: stub}

	var notices []string
	var drawn *Prototype
	err := c.RespondStream(context.Background(), "proj", nil,
		"make the left wheel bigger", "", treeOnlyModel(), nil,
		func(e StreamEvent) error {
			if e.Kind == "notice" {
				notices = append(notices, e.Text)
			}
			if e.Prototype != nil {
				drawn = e.Prototype
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	want := "changed definition wheel (named by its placement left): 2 occurrences (left, right)"
	if !strings.Contains(strings.Join(notices, " "), want) {
		t.Fatalf("the turn changed both wheels through a path to one and did not say so.\nwant a notice containing %q\nnotices: %q",
			want, notices)
	}
	if drawn == nil {
		t.Fatal("the turn emitted no model")
	}
	for _, p := range drawn.PlacedParts() {
		if p.Size["radius"] != 35 {
			t.Errorf("%s has radius %v; the path names the definition, and every placement follows it", p.ID, p.Size["radius"])
		}
	}
}

// A design placed many times is counted in full and listed in part.
func TestResolveEdit_ALongListOfOccurrencesIsCountedAndBounded(t *testing.T) {
	fence := &geometry.Document{Name: "Fence", Units: "mm", Root: "fence",
		Definitions: []geometry.Part{{ID: "post", Name: "Post", Shape: "box",
			Size: map[string]float64{"width": 10, "height": 100, "depth": 10}}},
		Assemblies: []geometry.Assembly{{ID: "fence", Children: []geometry.Child{
			{ID: "post", Ref: "post", Pattern: &geometry.Pattern{Kind: "linear", Count: 20, Offset: []float64{50, 0, 0}}},
		}}}}
	post := fence.Definitions[0]
	post.ID = "post-7"
	post.Size = map[string]float64{"width": 12, "height": 100, "depth": 12}
	r := &Reply{PrototypeEdit: &geometry.Edit{Patch: &geometry.Document{Definitions: []geometry.Part{post}}}}
	if err := r.resolveEdit(fence); err != nil {
		t.Fatalf("refused: %v", err)
	}
	for _, want := range []string{"20 occurrences (post-1, post-2, post-3, post-4, post-5, post-6, post-7, post-8, and 12 more not listed)"} {
		if !strings.Contains(r.Repaired, want) {
			t.Errorf("want the note to contain %q, got %q", want, r.Repaired)
		}
	}
	if strings.Contains(r.Repaired, "post-9") {
		t.Errorf("the note lists past its bound: %q", r.Repaired)
	}
}

// An edit that reached exactly the one part it named says nothing more: every pass
// of a build in passes is such an edit, and a notice on each would bury the one
// that matters.
func TestResolveEdit_AnEditThatReachesOnlyWhatItNamedAddsNoNote(t *testing.T) {
	plate := onScreenModel().Parts[0]
	plate.Size = map[string]float64{"width": 80, "height": 6, "depth": 60}
	r := &Reply{PrototypeEdit: &geometry.Edit{Patch: &geometry.Document{Parts: []geometry.Part{plate}}}}
	if err := r.resolveEdit(onScreenModel()); err != nil {
		t.Fatalf("refused: %v", err)
	}
	if r.Repaired != "" {
		t.Errorf("an edit to one flat part left a note: %q", r.Repaired)
	}
}

// The path the contract teaches is a path its own worked example places, and it
// names the definition the contract says it does. Built rather than read, like
// the tree example's own fence: a model copying the sentence must get an edit
// FORGE resolves, not a refusal.
func TestTheContractTeachesAPathItsOwnExampleResolves(t *testing.T) {
	const marker = "Worked example — two welded wheels on an axle:\n"
	i := strings.Index(geometryContract, marker)
	if i < 0 {
		t.Fatal("the contract has no worked tree example")
	}
	rest := geometryContract[i+len(marker):]
	const last = `"root": "axle"`
	end := strings.Index(rest, last)
	if end < 0 {
		t.Fatal(`the contract's tree example does not end with "root"`)
	}
	var doc geometry.Document
	if err := json.Unmarshal([]byte(`{"name": "axle", "units": "mm", `+rest[:end+len(last)]+`}`), &doc); err != nil {
		t.Fatalf("the contract's tree example does not decode: %v", err)
	}

	m := regexp.MustCompile(`\{"id": "([^"]+)", \.\.\.\} in\s+"definitions" is read as the definition "([^"]+)"`).
		FindStringSubmatch(geometryContract)
	if m == nil {
		t.Fatal("the contract does not teach naming a definition by a placed path")
	}
	path, named := m[1], m[2]
	if !strings.Contains(path, geometry.PathSeparator) {
		t.Errorf("the contract's path example %q is not a path", path)
	}
	out, reached, problems := geometry.Edit{Patch: &geometry.Document{Definitions: []geometry.Part{
		{ID: path, Shape: "box", Size: map[string]float64{"width": 4, "height": 4, "depth": 70}},
	}}}.ApplyAndReport(doc)
	for _, p := range problems {
		t.Errorf("the contract's own path example is refused: %s", p.Detail)
	}
	if len(reached) != 1 || reached[0].ID != named || reached[0].Path != path {
		t.Fatalf("the contract says %q names the definition %q; it resolved as %+v", path, named, reached)
	}
	if len(out.Definitions) != len(doc.Definitions) {
		t.Errorf("the path added a definition; it must change the one it names")
	}
	// "every spoke on both wheels": six per wheel.
	if len(reached[0].Occurrences) != 12 {
		t.Errorf("the contract says the path changes every spoke on both wheels; it reached %v", reached[0].Occurrences)
	}
}
