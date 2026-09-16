package agent_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/agent"
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

// unplacedAssemblies is every assembly that is neither the root nor any child's ref.
func unplacedAssemblies(d geometry.Document) []string {
	ref := map[string]bool{}
	for _, a := range d.Assemblies {
		for _, c := range a.Children {
			ref[c.Ref] = true
		}
	}
	var out []string
	for _, a := range d.Assemblies {
		if a.ID != d.Root && !ref[a.ID] {
			out = append(out, a.ID)
		}
	}
	return out
}

// An assembly built and placed by nothing is counted by name.
func TestCarMeasure_CountsAssembliesNothingPlaces(t *testing.T) {
	d := geometry.Document{Root: "car", Assemblies: []geometry.Assembly{
		{ID: "car", Children: []geometry.Child{{ID: "chassis", Ref: "chassis"}}},
		{ID: "chassis"}, {ID: "brakes"}, {ID: "steering"},
	}}
	if got := strings.Join(unplacedAssemblies(d), ","); got != "brakes,steering" {
		t.Errorf("unplaced = %q, want brakes,steering", got)
	}
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// Every refusal a build step can make is named by its gate, and a step that was
// built and corrected is not counted as refused (2026-09-15, live car findings).
func TestCarMeasure_ARefusedStepIsNamedByItsGate(t *testing.T) {
	for note, want := range map[string]string{
		"Step 1 (Chassis Frame) came back unreadable: its JSON does not parse.":                     "unreadable",
		"Step 1 (Chassis Frame) sent no geometry: it asked to be built in passes.":                  "no-geometry",
		"Step 2 (Wheels) sent an edit that could not be applied: there is no model on screen.":      "edit-refused",
		"Step 1 (Chassis Frame) produced a model that places nothing: no \"root\".":                 "places-nothing",
		"Step 4 (Drivetrain) was left out: it would have broken the model: it had 2 fault(s).":      "faults-added",
		"Step 3 (Brakes) could not be built: the budget is spent.":                                  "call-failed",
		"Looking at the model it had just built, FORGE found problems and corrected it: all parts.": "",
	} {
		if got := agent.StepGateOf(note); got != want {
			t.Errorf("StepGateOf(%q) = %q, want %q", note, got, want)
		}
	}
}

// The visual check is counted per step and per sub-assembly, from the calls alone.
func TestCarMeasure_LooksAreCountedPerStepAndPerSubAssembly(t *testing.T) {
	records := []callRecord{
		{Step: 2, Role: "vision", Images: 1, Prompt: "This was built in answer to: a car", Reply: `{"problems": []}`, Seconds: 2},
		{Step: 2, Role: "vision", Images: 1, Prompt: subAssemblyLook + "left-wheel (an occurrence of wheel), drawn on its own.",
			Reply: `{"problems": [{"part": "Nut", "detail": "inside the rim"}]}`, Seconds: 4, PromptTokens: 900, CompletionTokens: 40},
		{Step: 2, Role: "converse", Prompt: "Building: a car"},
	}
	lines := lookLines(looksOf(records))
	if len(lines) != 2 {
		t.Fatalf("want one line for step 2 and a total, got %q", lines)
	}
	for _, want := range []string{"step=2", "whole=1", "sub_assemblies=1", "findings=1", "looked_at=left-wheel", "tokens=940"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("the step's look line %q does not say %s", lines[0], want)
		}
	}
	if !strings.Contains(lines[1], "calls=2") || !strings.Contains(lines[1], "max_seconds=4.0") {
		t.Errorf("the total %q does not count both vision calls and the slowest", lines[1])
	}
}

// A tree's children are counted by how they are placed.
func TestCarMeasure_CountsChildrenAttachedAtAnInterface(t *testing.T) {
	d := geometry.Document{Root: "car", Assemblies: []geometry.Assembly{
		{ID: "car", Interfaces: []geometry.Interface{{ID: "front"}}, Children: []geometry.Child{
			{ID: "chassis", Ref: "chassis"}, {ID: "axle", Ref: "axle", At: "front"}}},
	}}
	d.Assemblies[0].Children[0].PositionFrom = map[string]string{"x": "half_wheelbase"}
	if children, attached, interfaces, bound := attachments(d); children != 2 || attached != 1 || interfaces != 1 || bound != 1 {
		t.Errorf("attachments = %d, %d, %d, %d; want 2 children, 1 attached, 1 interface, 1 bound", children, attached, interfaces, bound)
	}
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
