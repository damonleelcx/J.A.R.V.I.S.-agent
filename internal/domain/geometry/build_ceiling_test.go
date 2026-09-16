package geometry

import (
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// studPanel places exactly 16 × 512 = 8192 studs through a tree, and extra more as
// top-level parts.
func studPanel(extra int) Document {
	stud := Part{ID: "stud", Shape: "box", Size: map[string]float64{"width": 1, "height": 1, "depth": 1}}
	d := Document{Name: "panel", Units: "mm", Root: "panel", Definitions: []Part{stud},
		Assemblies: []Assembly{
			{ID: "panel", Children: []Child{{ID: "row", Ref: "row",
				Pattern: &Pattern{Kind: "linear", Count: 16, Offset: []float64{0, 3, 0}}}}},
			{ID: "row", Children: []Child{{ID: "stud", Ref: "stud",
				Pattern: &Pattern{Kind: "linear", Count: 512, Offset: []float64{2, 0, 0}}}}},
		}}
	for i := 0; i < extra; i++ {
		d.Parts = append(d.Parts, Part{ID: "extra", Shape: "box", Size: map[string]float64{"width": 1, "height": 1, "depth": 1},
			Position: []float64{0, -10, 0}})
	}
	return d
}

// The kernel builds a VIEW of 8192 parts — and not one more — and every other build
// and export keeps 4096. Raised on a measurement and only that one: 8,192 and 8,315
// parts built in 5.7–13.0 s through the mesh endpoint of forged limited like its pod
// (1 CPU, 1 GiB) with the 30 s timeout; 16,556 took up to 21.5 s. STEP export, mass
// properties and the Go mesh were not measured past 4096.
// docs/spikes/2026-09-15-ceiling-on-linux.
func TestLimits_TheKernelBuildsAViewOf8192PartsAndNothingElsePast4096(t *testing.T) {
	at, over := studPanel(0), studPanel(1)
	if n := len(at.Expanded().Parts); n != 8192 {
		t.Fatalf("the fixture places %d parts, want 8192", n)
	}
	if MaxBuiltParts() != 8192 {
		t.Fatalf("the kernel builds a view of %d parts; 8192 is what was measured", MaxBuiltParts())
	}

	if r := at.BuildRefusal(); r != "" {
		t.Errorf("a view of 8192 parts is refused: %q", r)
	}
	if r := over.BuildRefusal(); !strings.Contains(r, "more than 8192 parts") || !strings.Contains(r, "for a view") {
		t.Errorf("a view of 8193 parts is not refused by name: %q", r)
	}
	if solids, _, _, inferred := SolidsAndOperations(at, Millimetre); len(solids) != 8192 || len(inferred) != 0 {
		t.Errorf("the kernel request for 8192 parts is %d solids with notes %q", len(solids), inferred)
	}
	if solids, _, _, inferred := SolidsAndOperations(over, Millimetre); len(solids) != 0 || len(inferred) != 1 || inferred[0] != over.BuildRefusal() {
		t.Errorf("the kernel request for 8193 parts is %d solids with notes %q; want nothing but the refusal", len(solids), inferred)
	}

	// Everything else keeps the tighter ceiling.
	refusal := at.DrawRefusal()
	if !strings.Contains(refusal, "more than 4096 parts") {
		t.Fatalf("8192 parts pass the ceiling of STEP export, mass and the Go mesh: %q", refusal)
	}
	if m := Tessellate(at, Millimetre); len(m.Groups) != 0 || len(m.Inferences) != 1 || m.Inferences[0] != refusal {
		t.Errorf("the Go mesh drew %d groups of 8192 parts with notes %q; want nothing but the refusal", len(m.Groups), m.Inferences)
	}
	v := &Variant{Name: "panel", Document: at, Units: Millimetre}
	if _, err := Export(v, "stl"); err == nil || errs.CodeOf(err) != errs.CodeValidationFailed || !strings.Contains(err.Error(), "more than 4096 parts") {
		t.Errorf("a mesh file of 8192 parts was not refused at 4096: %v", err)
	}
	if small := studPanel(0); len(small.Definitions) == 0 || rows(4, 1024/4).DrawRefusal() != "" {
		t.Errorf("a design of 1024 parts is refused")
	}
}
