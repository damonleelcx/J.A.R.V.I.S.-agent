package geometry_test

import (
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

func bracket() geometry.Document {
	return geometry.Document{
		Name: "Bracket", Units: "mm",
		Parameters: []geometry.Parameter{{Name: "plate_size", Value: 60, How: "chosen"}},
		Parts: []geometry.Part{
			{ID: "plate", Name: "Plate", Shape: "box",
				Size:     map[string]float64{"width": 60, "height": 6, "depth": 60},
				Position: []float64{0, 0, 0}},
			{ID: "boss", Name: "Boss", Shape: "cylinder",
				Size:     map[string]float64{"radius": 11, "height": 8},
				Position: []float64{0, 7, 0}},
		},
		Features:    []geometry.Feature{{ID: "round", Op: "fillet", Of: "plate", Radius: 3}},
		Assumptions: []string{"60mm plate was chosen"},
	}
}

// The parts an edit does not mention come back EXACTLY as they were.
//
// # What this closes
//
// A revision was a full rewrite: to add one part the agent re-emitted every
// other part, and every number was retyped. Ask for a spoiler and the wheelbase
// could change, silently, because nothing compared the two documents.
//
// An edit makes that impossible rather than unlikely — an untouched part is not
// in the payload at all, so there is nothing to retype. This asserts exactly
// that, because it is the whole reason the shape exists.
func TestEdit_LeavesUntouchedPartsByteIdentical(t *testing.T) {
	base := bracket()
	edit := geometry.Edit{Patch: &geometry.Document{
		Parts: []geometry.Part{{ID: "rib", Name: "Rib", Shape: "box",
			Size: map[string]float64{"width": 4, "height": 20, "depth": 60}}},
		Assumptions: []string{"the rib is 4mm, chosen"},
	}}

	got, problems := edit.Apply(base)
	if len(problems) != 0 {
		t.Fatalf("adding a part reported problems: %v", problems)
	}
	if len(got.Parts) != 3 {
		t.Fatalf("wanted 3 parts after adding one to two, got %d", len(got.Parts))
	}
	for _, want := range base.Parts {
		var found *geometry.Part
		for i := range got.Parts {
			if got.Parts[i].ID == want.ID {
				found = &got.Parts[i]
			}
		}
		if found == nil {
			t.Fatalf("part %q vanished from an edit that never mentioned it", want.ID)
		}
		for key, value := range want.Size {
			if found.Size[key] != value {
				t.Errorf("part %q %s drifted from %g to %g in an edit that did not touch it. "+
					"That drift is the entire reason this shape exists.",
					want.ID, key, value, found.Size[key])
			}
		}
	}
	// The earlier assumption still holds; the new one is added, not substituted.
	if len(got.Assumptions) != 2 {
		t.Errorf("assumptions are %v; a patch ADDS to them, because a choice made two turns "+
			"ago is still a choice this design rests on", got.Assumptions)
	}
	// And the base is untouched, or a failed edit would corrupt what it failed to change.
	if len(base.Parts) != 2 {
		t.Errorf("Apply mutated the stored document: it now has %d parts", len(base.Parts))
	}
}

// Removal is a statement, and omission is not removal.
func TestEdit_RemovesOnlyWhatItNames(t *testing.T) {
	base := bracket()
	got, problems := geometry.Edit{
		Remove: geometry.Removals{Parts: []string{"boss"}},
	}.Apply(base)
	if len(problems) != 0 {
		t.Fatalf("removing a part that exists reported problems: %v", problems)
	}
	if len(got.Parts) != 1 || got.Parts[0].ID != "plate" {
		t.Fatalf("wanted only the plate left, got %d parts", len(got.Parts))
	}

	// A patch that simply does not mention a part must NOT delete it: an edit
	// says what changes, so absence has to mean "unchanged" or nothing could
	// ever be edited without restating everything — which is the shape this
	// replaces.
	kept, _ := geometry.Edit{Patch: &geometry.Document{
		Parts: []geometry.Part{{ID: "plate", Name: "Plate", Shape: "box",
			Size: map[string]float64{"width": 80, "height": 6, "depth": 60}}},
	}}.Apply(base)
	if len(kept.Parts) != 2 {
		t.Errorf("patching one part dropped the other; omission must mean unchanged, "+
			"got %d parts", len(kept.Parts))
	}
	for _, p := range kept.Parts {
		if p.ID == "plate" && p.Size["width"] != 80 {
			t.Errorf("the patched part was not replaced: width is %g", p.Size["width"])
		}
	}
}

// Removing something that is not there is a disagreement, not a no-op.
//
// "Remove the cabin" when there is no cabin means the agent and the person do
// not agree about what is on screen. Applying it quietly would hide that behind
// a version that looks deliberate.
func TestEdit_RefusesToRemoveWhatIsNotThere(t *testing.T) {
	_, problems := geometry.Edit{
		Remove: geometry.Removals{Parts: []string{"cabin"}},
	}.Apply(bracket())
	if len(problems) == 0 {
		t.Fatal("removing a part that does not exist was accepted silently, so a disagreement " +
			"about what is on screen becomes a version that looks successful")
	}
	if problems[0].Severity != geometry.Error {
		t.Errorf("severity is %v; naming something absent is an error, not a note", problems[0].Severity)
	}
}

// An edit that changes nothing is not an edit.
func TestEdit_KnowsWhenItWouldChangeNothing(t *testing.T) {
	if !(geometry.Edit{}).Empty() {
		t.Error("an empty edit did not report itself as empty, so it would append a version " +
			"recording a change nobody made")
	}
	if (geometry.Edit{Remove: geometry.Removals{Parts: []string{"boss"}}}).Empty() {
		t.Error("a removal was reported as changing nothing")
	}
	if (geometry.Edit{Patch: &geometry.Document{Parts: []geometry.Part{{ID: "rib"}}}}).Empty() {
		t.Error("a patch adding a part was reported as changing nothing")
	}
}
