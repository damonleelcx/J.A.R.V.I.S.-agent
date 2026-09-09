package agent

import (
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

func onScreenModel() *Prototype {
	return &geometry.Document{
		Name: "Bracket", Units: "mm",
		Parts: []geometry.Part{
			{ID: "plate", Name: "Plate", Shape: "box",
				Size: map[string]float64{"width": 60, "height": 6, "depth": 60}},
		},
	}
}

// An edit becomes a whole document before it leaves the agent.
//
// This is the property everything downstream depends on: the viewport, the
// store, compare, export and the CAD kernel all consume a Document, and none of
// them was taught about edits. If resolution ever stopped happening here, a turn
// would emit a reply whose prototype is nil and the change would vanish while
// FORGE said it had been made.
func TestResolveEdit_BecomesAWholeDocument(t *testing.T) {
	r := &Reply{PrototypeEdit: &geometry.Edit{
		Patch: &geometry.Document{Parts: []geometry.Part{
			{ID: "rib", Name: "Rib", Shape: "box", Size: map[string]float64{"width": 4}},
		}},
	}}
	if err := r.resolveEdit(onScreenModel()); err != nil {
		t.Fatalf("a valid edit was refused: %v", err)
	}
	if r.PrototypeEdit != nil {
		t.Error("the edit survived resolution; downstream would see a shape it does not know")
	}
	if r.Prototype == nil {
		t.Fatal("resolution produced no prototype, so the change would vanish silently")
	}
	if len(r.Prototype.Parts) != 2 {
		t.Fatalf("wanted the plate plus the rib, got %d parts", len(r.Prototype.Parts))
	}
	// The untouched part kept its dimensions — the whole point of the shape.
	for _, p := range r.Prototype.Parts {
		if p.ID == "plate" && p.Size["width"] != 60 {
			t.Errorf("the plate drifted to %g in an edit that never mentioned it", p.Size["width"])
		}
	}
}

// Both forms at once is RESOLVED, not refused.
//
// Refusing lost the whole turn. Measured on a real turn (2026-09-09, "add wheel
// wells"): the model sent both, the reply was refused, and the person got
// "I've added wheel arches to the body" with no geometry at all — strictly worse
// than either reading, because the speech had already said the work was done.
//
// The edit wins when there is something to apply it to, because it is the form
// that cannot drift. With nothing on screen only the whole prototype can be used.
func TestResolveEdit_ChoosesAFormRatherThanLosingTheTurn(t *testing.T) {
	t.Run("with a model on screen the edit wins", func(t *testing.T) {
		r := &Reply{
			Prototype: &geometry.Document{Name: "Wrong", Units: "mm",
				Parts: []geometry.Part{{ID: "plate", Shape: "box",
					Size: map[string]float64{"width": 999}}}},
			PrototypeEdit: &geometry.Edit{Patch: &geometry.Document{
				Parts: []geometry.Part{{ID: "rib", Name: "Rib", Shape: "box",
					Size: map[string]float64{"width": 4}}}}},
		}
		if err := r.resolveEdit(onScreenModel()); err != nil {
			t.Fatalf("a reply carrying both forms was refused, which loses the turn: %v", err)
		}
		if r.Prototype == nil {
			t.Fatal("no geometry survived")
		}
		if len(r.Prototype.Parts) != 2 {
			t.Fatalf("wanted the on-screen plate plus the rib, got %d parts", len(r.Prototype.Parts))
		}
		for _, p := range r.Prototype.Parts {
			if p.ID == "plate" && p.Size["width"] != 60 {
				t.Errorf("the plate is %g wide; the edit was supposed to win, leaving parts it "+
					"does not mention exactly as they were", p.Size["width"])
			}
		}
		if r.Repaired == "" {
			t.Error("the reader was not told that one of the two forms was dropped")
		}
	})

	t.Run("with nothing on screen the prototype wins", func(t *testing.T) {
		r := &Reply{
			Prototype:     onScreenModel(),
			PrototypeEdit: &geometry.Edit{Remove: geometry.Removals{Parts: []string{"plate"}}},
		}
		if err := r.resolveEdit(nil); err != nil {
			t.Fatalf("refused with nothing on screen: %v", err)
		}
		if r.Prototype == nil || len(r.Prototype.Parts) != 1 {
			t.Error("the whole prototype should have been used; there was no base to edit")
		}
	})
}

// The remaining refusals. Each is a case where continuing produces a version
// that looks deliberate and is not.
func TestResolveEdit_RefusesTheThreeAmbiguousCases(t *testing.T) {
	for _, tc := range []struct {
		name    string
		reply   *Reply
		current *Prototype
		wantIn  string
		why     string
	}{
		{
			name:    "an edit with nothing to edit",
			reply:   &Reply{PrototypeEdit: &geometry.Edit{Remove: geometry.Removals{Parts: []string{"plate"}}}},
			current: nil,
			wantIn:  "no model on screen",
			why:     "applying a patch to nothing would invent a design from a fragment",
		},
		{
			name:    "an edit that changes nothing",
			reply:   &Reply{PrototypeEdit: &geometry.Edit{}},
			current: onScreenModel(),
			wantIn:  "changes nothing",
			why:     "a version recording a change nobody made is a false entry in the history",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.reply.resolveEdit(tc.current)
			if err == nil {
				t.Fatalf("accepted %s — %s", tc.name, tc.why)
			}
			if errs.CodeOf(err) != errs.CodeValidationFailed {
				t.Errorf("failed with %s, wanted a validation failure", errs.CodeOf(err))
			}
			if !strings.Contains(errs.DetailOf(err), tc.wantIn) {
				t.Errorf("the refusal does not say what was wrong.\ngot: %s", errs.DetailOf(err))
			}
			if tc.reply.PrototypeEdit != nil {
				t.Error("a refused edit was left on the reply; it must be consumed either way, " +
					"or a later stage could apply what was already rejected")
			}
		})
	}
}

// A removal naming something absent stops the turn rather than half-applying.
func TestResolveEdit_RefusesAnEditItCannotApply(t *testing.T) {
	r := &Reply{PrototypeEdit: &geometry.Edit{
		Remove: geometry.Removals{Parts: []string{"cabin"}},
	}}
	err := r.resolveEdit(onScreenModel())
	if err == nil {
		t.Fatal("removing a part that is not on screen was accepted")
	}
	if !strings.Contains(errs.DetailOf(err), "cabin") {
		t.Errorf("the refusal does not name what could not be removed.\ngot: %s", errs.DetailOf(err))
	}
	if r.Prototype != nil {
		t.Error("a half-applied document was produced from an edit that failed")
	}
}

// No edit is the ordinary case and must pass through untouched.
func TestResolveEdit_LeavesAWholePrototypeAlone(t *testing.T) {
	r := &Reply{Prototype: onScreenModel()}
	if err := r.resolveEdit(nil); err != nil {
		t.Fatalf("a reply proposing a whole model was refused: %v", err)
	}
	if r.Prototype == nil || len(r.Prototype.Parts) != 1 {
		t.Error("a whole prototype was altered by a resolution that had nothing to do")
	}
}
