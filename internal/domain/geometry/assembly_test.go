package geometry

import (
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/claim"
)

// Materials and assembly states (PRD VIS-02).

func parts() []Part {
	return []Part{
		{ID: "body", Shape: "box", Size: map[string]float64{"width": 40}},
		{ID: "lid", Shape: "box", Size: map[string]float64{"width": 40}},
	}
}

// Naming a material is a claim, so it carries how FORGE came to it.
func TestAMaterialCarriesHowItWasArrivedAt(t *testing.T) {
	m := &Material{Name: "aluminium 6061-T6", Finish: FinishMetal}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	if m.How != claim.Assumed {
		t.Errorf("an unlabelled material became %q; FORGE choosing one because a bracket is "+
			"usually aluminium is a guess, and it must read as one", m.How)
	}

	// An unusable finish loses the LOOK and keeps the NAME. The name is the
	// claim; refusing the part over how it catches light would be the larger harm.
	odd := &Material{Name: "ABS", Finish: "brushed-anodised"}
	if err := odd.Validate(); err != nil {
		t.Fatalf("a part was refused over an unknown finish: %v", err)
	}
	if odd.Finish != FinishUnfinished {
		t.Errorf("finish fell back to %q", odd.Finish)
	}
	if odd.Name != "ABS" {
		t.Errorf("the material name was lost: %q", odd.Name)
	}

	// A finish with nothing behind it is not a material.
	if err := (&Material{Finish: FinishMetal}).Validate(); err == nil {
		t.Error("a material with no name was accepted")
	}
}

// The finish set is closed, and the model contract is generated from it.
//
// Two lists would drift, and the copy in a prompt is the one that goes stale
// silently: the model keeps offering a finish the viewer stopped drawing.
func TestTheFinishGuideIsGeneratedFromTheClosedSet(t *testing.T) {
	guide := FinishGuide()
	for _, name := range FinishNames() {
		if !strings.Contains(guide, name) {
			t.Errorf("the contract shown to the model does not mention the %q finish", name)
		}
		if !Finish(name).Valid() {
			t.Errorf("%q is listed and is not valid", name)
		}
	}
	if Finish("chrome").Valid() {
		t.Error("an unlisted finish reported itself valid")
	}
}

// Every part a state names must exist.
func TestAStateThatNamesAMissingPartIsRefused(t *testing.T) {
	hides := []AssemblyState{{Name: "closed", Hidden: []string{"gasket"}}}
	err := ValidateStates(hides, parts())
	if err == nil {
		t.Fatal("a state hiding a part that does not exist was accepted.\n" +
			"The viewer shows the assembly unchanged, and a reader takes that for the state " +
			"making no difference rather than for a broken reference")
	}
	if !strings.Contains(err.Error(), "gasket") {
		t.Errorf("the refusal does not name the missing part: %v", err)
	}

	moves := []AssemblyState{{Name: "open", Offsets: map[string][]float64{"hinge": {0, 10, 0}}}}
	if err := ValidateStates(moves, parts()); err == nil {
		t.Error("a state moving a part that does not exist was accepted")
	}
	short := []AssemblyState{{Name: "open", Offsets: map[string][]float64{"lid": {0, 10}}}}
	if err := ValidateStates(short, parts()); err == nil {
		t.Error("a two-component translation was accepted")
	}
	unnamed := []AssemblyState{{Name: "  "}}
	if err := ValidateStates(unnamed, parts()); err == nil {
		t.Error("a state with no name was accepted; nobody could pick it")
	}
}

// A state FORGE composed is a guess, and gets an id it can be picked by.
func TestAStateIsProposedUnlessSomebodySaidOtherwise(t *testing.T) {
	states := []AssemblyState{{Name: "service position", Offsets: map[string][]float64{"lid": {0, 20, 0}}}}
	if err := ValidateStates(states, parts()); err != nil {
		t.Fatal(err)
	}
	if states[0].How != claim.Proposed {
		t.Errorf("an unlabelled state became %q; how a thing comes apart is a guess "+
			"until somebody says", states[0].How)
	}
	if states[0].ID == "" {
		t.Error("a state with no id cannot be selected")
	}
	if err := ValidateStates([]AssemblyState{
		{ID: "a", Name: "one"}, {ID: "a", Name: "two"},
	}, parts()); err == nil {
		t.Error("two states shared an id")
	}
}

// A state that MOVES parts claims they can move, and nothing here checked it.
func TestAMovingStateSaysWhatItDoesNotEstablish(t *testing.T) {
	still := []AssemblyState{{Name: "closed", Hidden: []string{"lid"}}}
	if StatesNotVerified(still) != "" {
		t.Error("a state that only hides parts claimed something about motion.\n" +
			"Hiding is a filter; only an offset is a statement about how the assembly comes apart")
	}

	moving := []AssemblyState{{Name: "open", Offsets: map[string][]float64{"lid": {0, 20, 0}}}}
	note := StatesNotVerified(moving)
	if note == "" {
		t.Fatal("a state that separates parts along a path established that silently")
	}
	for _, want := range []string{"interference", "clearance"} {
		if !strings.Contains(note, want) {
			t.Errorf("the note does not mention %q: %q", want, note)
		}
	}

	// A zero offset is not a move: it says the part stays put.
	zero := []AssemblyState{{Name: "as built", Offsets: map[string][]float64{"lid": {0, 0, 0}}}}
	if StatesNotVerified(zero) != "" {
		t.Error("an offset of zero was treated as motion")
	}
}

// An annotation is an overlay kind, so it inherits the provenance discipline
// rather than arriving as unmarked prose floating over a render.
func TestAnAnnotationIsAnOverlayAndCarriesItsLabel(t *testing.T) {
	n := Overlay{ID: "n1", Kind: Note, Label: "weld here", From: []float64{0, 0, 0},
		Note: "continuous fillet both sides"}
	if err := n.Validate(); err != nil {
		t.Fatalf("a pinned note was refused: %v", err)
	}
	if n.How != claim.Assumed {
		t.Errorf("an unlabelled note became %q", n.How)
	}
	// And it needs no unit or span — it is a comment, not a measurement.
	if n.Unit != "" {
		t.Errorf("a note acquired the unit %q", n.Unit)
	}
}

// The storage door checks all three (PRD VIS-02), not just overlays.
//
// The tests above call the validators directly and pass whether or not anything
// calls them.
func TestStoringAVariantChecksMaterialsAndStates(t *testing.T) {
	base := func() *NewVariant {
		return &NewVariant{
			InitiatorID: "usr_1", Agent: "system", Generator: "test-model", Inputs: []byte(`{}`),
			Document: Document{
				Name: "box", Units: "mm", Parts: parts(),
				NotVerified: []string{"nothing checked"},
			},
		}
	}

	bad := base()
	bad.Document.States = []AssemblyState{{Name: "open", Hidden: []string{"nonexistent"}}}
	if err := bad.Validate(); err == nil {
		t.Error("a variant with a state naming a missing part was stored")
	}

	noName := base()
	noName.Document.Parts[0].Material = &Material{Finish: FinishMetal}
	if err := noName.Validate(); err == nil {
		t.Error("a variant with an unnamed material was stored")
	}

	// A moving state gains the sentence saying what it does not establish, at
	// the door, so no caller has to remember it.
	moving := base()
	moving.Document.States = []AssemblyState{
		{Name: "open", Offsets: map[string][]float64{"lid": {0, 20, 0}}}}
	if err := moving.Validate(); err != nil {
		t.Fatal(err)
	}
	var told bool
	for _, n := range moving.Document.NotVerified {
		if strings.Contains(n, "interference") {
			told = true
		}
	}
	if !told {
		t.Errorf("a stored assembly that comes apart says nothing about that being unchecked: %v",
			moving.Document.NotVerified)
	}
	// And it is not added twice when a variant is validated again.
	if err := moving.Validate(); err != nil {
		t.Fatal(err)
	}
	var count int
	for _, n := range moving.Document.NotVerified {
		if strings.Contains(n, "interference") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("the note was added %d times; re-validating a variant must not repeat it", count)
	}
}
