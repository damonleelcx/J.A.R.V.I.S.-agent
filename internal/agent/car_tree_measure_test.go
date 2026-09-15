package agent_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/cad"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// What a car built as a tree is made of (Phase 2, stage A4).
//
// # The problem this solves
//
// The live car measurement counted `len(doc.Parts)`. A model written as a tree
// has no top-level parts — every part is a definition placed by an assembly — so
// the car this whole plan exists to build would have measured as zero parts,
// failed the measurement's one assertion, and reported its tokens against
// nothing. The milestone asks for parts, definitions, occurrences, tokens per
// definition, interference and coverage; these helpers are those numbers, kept
// out of the live test so they are fenced without spending a token.

type treeCounts struct {
	Definitions int
	Assemblies  int
	// Placed is every part the tree places, before patterns and repeats are
	// written out; Occurrences is after, which is what the kernel builds.
	Placed      int
	Occurrences int
	Standards   int
	// Repetitions is how many runs of identical siblings could have been one
	// pattern (stage A3).
	Repetitions int
}

func countTree(d geometry.Document) treeCounts {
	c := treeCounts{Definitions: len(d.Definitions), Assemblies: len(d.Assemblies),
		Repetitions: len(d.EnumeratedRepetition())}
	placed := d.PlacedParts()
	c.Placed = len(placed)
	for _, p := range placed {
		if p.Standard != "" {
			c.Standards++
		}
	}
	c.Occurrences = len(d.Expanded().Parts)
	return c
}

// Designs is how many distinct things were described: the definitions of a
// tree, or the parts of a flat document, where every part is its own design.
func (c treeCounts) Designs() int {
	if c.Definitions > 0 {
		return c.Definitions
	}
	return c.Placed
}

func (c treeCounts) line() string {
	return fmt.Sprintf("definitions=%d assemblies=%d placed=%d occurrences=%d standard_parts=%d could_be_patterns=%d",
		c.Definitions, c.Assemblies, c.Placed, c.Occurrences, c.Standards, c.Repetitions)
}

func tokensPer(spent int64, n int) int64 {
	if n <= 0 {
		return 0
	}
	return spent / int64(n)
}

// coverageLine is how much of the car the interference check covered (stage
// V2): a pair answered from a pose already measured counts as checked.
func coverageLine(b *cad.Build) string {
	return fmt.Sprintf("checked=%d of pairs=%d skipped_parts=%d truncated=%v",
		b.InterferenceBooleans+b.InterferenceReused, b.InterferencePairs, len(b.Skipped), b.InterferencesTruncated)
}

// A tree is counted by what it defines and what it places, not by its top-level parts.
func TestCarMeasure_CountsATreeByDefinitionAndByOccurrence(t *testing.T) {
	corner := geometry.Assembly{ID: "corner"}
	for i := 0; i < 4; i++ {
		corner.Children = append(corner.Children, geometry.Child{ID: fmt.Sprintf("screw-%d", i), Ref: "screw",
			Position: []float64{float64(i) * 20, 0, 0}})
	}
	corner.Children = append(corner.Children, geometry.Child{ID: "plate", Ref: "plate"})
	doc := geometry.Document{Name: "car", Units: "mm", Root: "car",
		Definitions: []geometry.Part{
			{ID: "screw", Name: "Screw", Shape: "standard", Standard: "ISO 4762 M8x30"},
			{ID: "plate", Name: "Plate", Shape: "box", Size: map[string]float64{"width": 100, "height": 10, "depth": 100}},
		},
		Assemblies: []geometry.Assembly{corner, {ID: "car", Children: []geometry.Child{
			{ID: "left", Ref: "corner"}, {ID: "right", Ref: "corner", Position: []float64{500, 0, 0}}}}},
	}

	got := countTree(doc)

	want := treeCounts{Definitions: 2, Assemblies: 2, Placed: 10, Occurrences: 10, Standards: 8, Repetitions: 1}
	if got != want {
		t.Errorf("counted %+v, want %+v", got, want)
	}
	if doc.Parts != nil {
		t.Fatal("precondition: the car has no top-level parts, which is the case the old count read as zero")
	}
	if n := tokensPer(300_000, got.Designs()); n != 150_000 {
		t.Errorf("300k tokens over two designs is %d each, want 150000", n)
	}
	flat := geometry.Document{Name: "cart", Units: "mm", Parts: []geometry.Part{
		{ID: "a", Name: "A", Shape: "box", Size: map[string]float64{"width": 1, "height": 1, "depth": 1}},
		{ID: "b", Name: "B", Shape: "box", Size: map[string]float64{"width": 1, "height": 1, "depth": 1}}}}
	if d := countTree(flat).Designs(); d != 2 {
		t.Errorf("a flat document of two parts is %d designs, want 2", d)
	}
}

// Coverage counts a pair answered from a measured pose as checked, and says what was skipped.
func TestCarMeasure_CoverageCountsReusedPairsAsChecked(t *testing.T) {
	line := coverageLine(&cad.Build{InterferencePairs: 12, InterferenceBooleans: 3, InterferenceReused: 9,
		Skipped: []string{"spring"}, InterferencesTruncated: false})
	if !strings.Contains(line, "checked=12 of pairs=12") || !strings.Contains(line, "skipped_parts=1") {
		t.Errorf("coverage line %q", line)
	}
}
