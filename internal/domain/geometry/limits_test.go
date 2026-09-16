package geometry

import (
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// What one stored design may be, and how much of one is drawn or built. Phase 3,
// stage S0 of docs/plan-2026-09-13-millions-of-parts.md.

// withLimits sets the process's limits for one test and puts them back.
func withLimits(t *testing.T, l Limits) {
	t.Helper()
	was := CurrentLimits()
	SetLimits(l)
	t.Cleanup(func() { SetLimits(was) })
}

func stud(id string) Part {
	return Part{ID: id, Shape: "box", Size: map[string]float64{"width": 1, "height": 1, "depth": 1}}
}

// rows places `rows` copies of a definition repeated `perRow` times.
func rows(n, perRow int) Document {
	rivet := stud("rivet")
	rivet.Repeat = &Repeat{Count: perRow, Offset: []float64{2, 0, 0}}
	return Document{Name: "panel", Units: "mm", Root: "panel", NotVerified: []string{"concept"},
		Definitions: []Part{rivet},
		Assemblies: []Assembly{{ID: "panel", Children: []Child{
			{ID: "row", Ref: "rivet", Pattern: &Pattern{Kind: "linear", Count: n, Offset: []float64{0, 3, 0}}},
		}}}}
}

func TestOccurrences_CountsWithoutPlacing(t *testing.T) {
	hub := stud("hub")
	spoke := stud("spoke")
	spoke.Repeat = &Repeat{Count: 2, Offset: []float64{0, 0, 1}}
	big := stud("big")
	big.Repeat = &Repeat{Count: 3, Offset: []float64{1, 0, 0}}
	d := Document{Name: "car", Root: "car",
		Parts:       []Part{big, stud("frame")},
		Definitions: []Part{hub, spoke},
		Assemblies: []Assembly{
			{ID: "car", Children: []Child{
				{ID: "fl", Ref: "corner"}, {ID: "fr", Ref: "corner", Mirror: "x"},
				{ID: "rear", Ref: "corner", Pattern: &Pattern{Kind: "linear", Count: 2, Offset: []float64{0, 0, 9}}},
				{ID: "refused", Ref: "hub", Pattern: &Pattern{Kind: "linear", Count: 600, Offset: []float64{1, 0, 0}}},
				{ID: "loop", Ref: "car"},
			}},
			{ID: "corner", Children: []Child{
				{ID: "hub", Ref: "hub"},
				{ID: "spoke", Ref: "spoke", Pattern: &Pattern{Kind: "polar", Count: 6, About: "y"}},
			}},
		}}
	// top level: 3 + 1; each corner: 1 + 6x2 = 13; four corners: 52; the refused
	// pattern and the cycle place nothing.
	if got := occurrences(d, 1_000_000); got != 56 {
		t.Fatalf("counted %d occurrences, want 56", got)
	}
	if got := occurrences(d, 10); got != 11 {
		t.Errorf("past a limit of 10 the count must saturate at 11, got %d", got)
	}
}

// Sixteen nested patterns of 512 describe 512^16 parts in a few kilobytes. The
// count saturates instead of multiplying them out, and every reader refuses the
// design without expanding it.
func TestOccurrences_ARunawayNestIsRefusedWithoutExpanding(t *testing.T) {
	d := Document{Name: "runaway", Units: "mm", Root: "l0", NotVerified: []string{"concept"},
		Definitions: []Part{stud("bolt")}}
	for i := 0; i < maxTreeDepth; i++ {
		next := "bolt"
		if i+1 < maxTreeDepth {
			next = "l" + string(rune('a'+i+1))
		}
		id := "l" + string(rune('a'+i))
		if i == 0 {
			id = "l0"
		}
		d.Assemblies = append(d.Assemblies, Assembly{ID: id, Children: []Child{
			{ID: "c", Ref: next, Pattern: &Pattern{Kind: "linear", Count: maxRepeat, Offset: []float64{1, 0, 0}}}}})
	}
	start := time.Now()
	problems := d.TreeProblems()
	placed := d.PlacedParts()
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Errorf("refusing the runaway design took %v", elapsed)
	}
	if len(placed) != 0 {
		t.Errorf("the runaway design was expanded to %d parts", len(placed))
	}
	found := false
	for _, p := range problems {
		found = found || (p.Severity == Error && strings.Contains(p.Detail, "FORGE_GEOMETRY_MAX_OCCURRENCES"))
	}
	if !found {
		t.Errorf("no problem names the occurrence setting: %+v", problems)
	}
}

// The occurrence bound is one rule, applied wherever a design is expanded or
// stored: the tree, a flat document's repeats, and the storage door.
func TestLimits_TheOccurrenceBoundHoldsEverywhere(t *testing.T) {
	withLimits(t, Limits{MaxOccurrences: 100})

	tree := rows(101, 1)
	if !hasOccurrenceError(tree.TreeProblems()) {
		t.Error("a tree of 101 occurrences over a bound of 100 was not refused by the tree checks")
	}
	flat := Document{Name: "flat", Units: "mm", NotVerified: []string{"concept"}, Parts: []Part{stud("a")}}
	flat.Parts[0].Repeat = &Repeat{Count: 101, Offset: []float64{1, 0, 0}}
	if e, problems := expandRepeats(flat); len(e.Parts) != 0 || !hasOccurrenceError(problems) {
		t.Errorf("a flat document of 101 copies was expanded to %d parts, problems %+v", len(e.Parts), problems)
	}
	for name, d := range map[string]Document{"tree": tree, "flat": flat} {
		err := (&NewVariant{InitiatorID: "u", Agent: "converse", Generator: "g", Inputs: map[string]any{}, Document: d}).Validate()
		if err == nil || !strings.Contains(err.Error(), "FORGE_GEOMETRY_MAX_OCCURRENCES") {
			t.Errorf("the storage door accepted the %s design over the occurrence bound: %v", name, err)
		}
	}
	if within := rows(100, 1); hasOccurrenceError(within.TreeProblems()) {
		t.Error("a design of exactly 100 occurrences was refused at a bound of 100")
	}
}

func hasOccurrenceError(problems []Problem) bool {
	for _, p := range problems {
		if p.Severity == Error && strings.Contains(p.Detail, "FORGE_GEOMETRY_MAX_OCCURRENCES") {
			return true
		}
	}
	return false
}

// A 30k-occurrence design is stored — the tree accepts it and places every part —
// and it is not meshed in Go, exported or sent to the kernel in part: that is the
// kernel's ceiling. Since Phase 6, stage W1 the viewport draws it (instanced), and
// refuses only past maxViewportParts.
func TestLimits_ThirtyThousandOccurrencesAreStoredButNotDrawn(t *testing.T) {
	d := rows(60, 500)
	for _, p := range d.TreeProblems() {
		if p.Severity == Error {
			t.Fatalf("the storage checks refused a 30k design: %s %s", p.Name, p.Detail)
		}
	}
	if err := (&NewVariant{InitiatorID: "u", Agent: "converse", Generator: "g", Inputs: map[string]any{}, Document: d}).Validate(); err != nil {
		t.Fatalf("the storage door refused a 30k design: %v", err)
	}
	if n := len(d.Expanded().Parts); n != 30000 {
		t.Fatalf("expanded to %d parts, want 30000", n)
	}
	refusal := d.DrawRefusal()
	if !strings.Contains(refusal, "more than 4096 parts") {
		t.Fatalf("no drawing refusal for 30k parts: %q", refusal)
	}
	if solids, ops, _, inferred := SolidsAndOperations(d, Millimetre); len(solids) != 0 || len(ops) != 0 || len(inferred) != 1 || inferred[0] != refusal {
		t.Errorf("the kernel request was %d solids, %d operations, notes %q; want nothing but the refusal", len(solids), len(ops), inferred)
	}
	if m := Tessellate(d, Millimetre); len(m.Groups) != 0 || len(m.Inferences) != 1 || m.Inferences[0] != refusal {
		t.Errorf("the mesh drew %d groups with notes %q; want nothing but the refusal", len(m.Groups), m.Inferences)
	}
	v := &Variant{Name: "panel", Document: d, Units: Millimetre}
	if _, err := Export(v, "stl"); err == nil || errs.CodeOf(err) != errs.CodeValidationFailed || !strings.Contains(err.Error(), "more than 4096 parts") {
		t.Errorf("exporting a 30k design as a mesh file was not refused with the reason: %v", err)
	}
	if r := d.ViewportRefusal(); r != "" {
		t.Errorf("the viewport refuses a 30k design, which instanced drawing draws: %q", r)
	}
	if small := rows(4, 1024/4); small.DrawRefusal() != "" {
		t.Errorf("a design of 1024 parts was refused for drawing: %q", small.DrawRefusal())
	}
}

// The viewport draws a design as large as storage accepts by default, and refuses
// the first one past it whole and in its own words — never the first 100,000 parts
// of it. Phase 6, stage W1.
func TestLimits_TheViewportDrawsWhatStorageAcceptsAndNoMore(t *testing.T) {
	if r := rows(200, 500).ViewportRefusal(); r != "" {
		t.Errorf("the viewport refuses a design of exactly 100,000 parts: %q", r)
	}
	over := rows(201, 500)
	r := over.ViewportRefusal()
	if !strings.Contains(r, "more than 100000 parts") || !strings.Contains(r, "nothing was drawn") {
		t.Fatalf("a design of 100,500 parts is not refused by the viewport in its words: %q", r)
	}
	if over.DrawRefusal() == "" {
		t.Error("a design the viewport refuses is not refused by the kernel either")
	}
}

func TestLimits_ANonPositiveSettingKeepsItsDefault(t *testing.T) {
	withLimits(t, Limits{MaxDocumentBytes: -1, MaxDefinitions: 0, MaxOccurrences: 500})
	l := CurrentLimits()
	if l.MaxDocumentBytes != DefaultMaxDocumentBytes || l.MaxDefinitions != DefaultMaxDefinitions || l.MaxOccurrences != 500 {
		t.Errorf("limits %+v; want the byte and definition defaults kept and occurrences set to 500", l)
	}
}
