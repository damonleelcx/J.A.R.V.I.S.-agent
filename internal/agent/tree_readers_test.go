package agent

import (
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// A design written as definitions and assemblies has no top-level parts, and the
// agent's readers took that as "no model": settling threw the design away, the
// current model the next turn revises from was empty, and an edit to it was refused
// as "there is no model on screen". Stage D1f of
// docs/plan-2026-09-13-millions-of-parts.md; they read HasGeometry now.

func treeOnlyModel() *Prototype {
	return &geometry.Document{Name: "Wheels", Units: "mm", Root: "pair",
		NotVerified: []string{"concept only"},
		Definitions: []geometry.Part{{ID: "wheel", Name: "Wheel", Shape: "cylinder",
			Size: map[string]float64{"radius": 30, "height": 12}}},
		Assemblies: []geometry.Assembly{{ID: "pair", Children: []geometry.Child{
			{ID: "left", Ref: "wheel", Position: []float64{-80, 0, 0}},
			{ID: "right", Ref: "wheel", Position: []float64{80, 0, 0}, Mirror: "x"},
		}}}}
}

func TestSettle_KeepsADesignWrittenAsATree(t *testing.T) {
	if settleDocument(treeOnlyModel()) == nil {
		t.Fatal("a design with no top-level parts but a tree was settled away as empty")
	}
}

func TestCurrentModel_ShowsTheTreeItIsRevising(t *testing.T) {
	got := CurrentModel(treeOnlyModel())
	if got == "" {
		t.Fatal("a tree on screen produced no current model, so a revision would restate it from recall")
	}
	for _, want := range []string{`"definitions"`, `"assemblies"`, `"root":"pair"`, `"ref":"wheel"`, `"mirror":"x"`, `"radius":30`} {
		if !strings.Contains(got, want) {
			t.Errorf("the current model does not carry %s:\n%s", want, got)
		}
	}
}

// An edit to a tree on screen is applied, and patching the definition changes both
// placements of it.
func TestResolveEdit_EditsATreeOnScreenThroughItsDesign(t *testing.T) {
	wheel := treeOnlyModel().Definitions[0]
	wheel.Size = map[string]float64{"radius": 35, "height": 12}
	r := &Reply{PrototypeEdit: &geometry.Edit{Patch: &geometry.Document{Definitions: []geometry.Part{wheel}}}}
	if err := r.resolveEdit(treeOnlyModel()); err != nil {
		t.Fatalf("an edit to a tree on screen was refused: %v", err)
	}
	if r.Prototype == nil {
		t.Fatal("resolution produced no prototype, so the change would vanish")
	}
	placed := r.Prototype.PlacedParts()
	if len(placed) != 2 {
		t.Fatalf("placed %d wheels, want 2", len(placed))
	}
	for _, p := range placed {
		if p.Size["radius"] != 35 {
			t.Errorf("%s has radius %v; patching the definition should change every placement", p.ID, p.Size["radius"])
		}
	}
}

// With a tree on screen, a reply carrying both forms applies its edit to that tree,
// as it would to flat parts: the tree IS a model on screen.
func TestResolveEdit_ATreeOnScreenIsAModelToEdit(t *testing.T) {
	r := &Reply{
		Prototype:     &geometry.Document{Name: "Other", Units: "mm", Parts: []geometry.Part{{ID: "x", Name: "X", Shape: "box"}}},
		PrototypeEdit: &geometry.Edit{Remove: geometry.Removals{Children: []string{"pair/right"}}},
	}
	if err := r.resolveEdit(treeOnlyModel()); err != nil {
		t.Fatalf("refused: %v", err)
	}
	if r.Prototype == nil || len(r.Prototype.PlacedParts()) != 1 || r.Prototype.PlacedParts()[0].ID != "left" {
		t.Errorf("want the edit applied to the tree on screen, leaving the left wheel; got %+v", r.Prototype)
	}
}
