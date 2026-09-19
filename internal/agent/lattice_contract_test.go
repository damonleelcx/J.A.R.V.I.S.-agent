package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// Mesh-only lattices in the contract and in the turn (stage E1 of the "looks
// designed" work; damon's decision, 2026-09-18). See geometry/lattice.go.

// The contract offers "lattice", teaches every pattern FORGE draws and no other,
// from geometry's one table, and says a lattice is decorative and never structural.
func TestTheContractTeachesLatticesFromTheTable(t *testing.T) {
	shapes := converseFraming[:strings.Index(converseFraming, "\n        \"shape_note\"")]
	if !strings.Contains(shapes, `"lattice"`) {
		t.Error(`the shape list does not offer "lattice"`)
	}
	if !strings.Contains(geometryContract, geometry.LatticeGuide()) {
		t.Fatal("the contract does not carry geometry.LatticeGuide")
	}
	guide := geometry.LatticeGuide()
	for _, name := range geometry.LatticePatternNames() {
		if !strings.Contains(guide, fmt.Sprintf("%q —", name)) {
			t.Errorf("the pattern %q is not taught", name)
		}
	}
	if taught := strings.Count(guide, "\" —"); taught != len(geometry.LatticePatternNames()) {
		t.Errorf("the contract teaches %d patterns; FORGE draws %d", taught, len(geometry.LatticePatternNames()))
	}
	flat := strings.Join(strings.Fields(guide), " ")
	for _, rule := range []string{"decorative and NEVER structural", geometry.MeshOnlyLabel,
		"left out of the STEP file", fmt.Sprint(geometry.MaxLatticeTriangles)} {
		if !strings.Contains(flat, rule) {
			t.Errorf("the lattice paragraph does not say %q", rule)
		}
	}
}

// The render carries the kernel's mesh-only parts onto the sheet the turn reads,
// or coverageNote has nothing to say about them.
func TestRender_CarriesTheMeshOnlyParts(t *testing.T) {
	c := &Conversation{solids: coverageSolids{built: Built{
		Parts: []geometry.RenderPart{cube("plate")}, MeshOnly: []string{"Infill"}}}}
	sheet := c.render(context.Background(), drilledPlate())
	if !sheet.FromKernel || len(sheet.MeshOnly) != 1 || sheet.MeshOnly[0] != "Infill" {
		t.Errorf("the sheet carries mesh-only %v (from kernel %v)", sheet.MeshOnly, sheet.FromKernel)
	}
}

// The turn says the interference check did not look at a mesh-only part.
func TestCoverageNoteSaysMeshOnlyPartsWereNotChecked(t *testing.T) {
	note := coverageNote(&builtSheet{FromKernel: true, MeshOnly: []string{"Infill"}})
	if !strings.Contains(note, "1 mesh-only part(s) are left out of the check for shared material: Infill") {
		t.Errorf("coverage note %q", note)
	}
	if note := coverageNote(&builtSheet{FromKernel: true}); strings.Contains(note, "mesh-only") {
		t.Errorf("a sheet with no mesh-only part: %q", note)
	}
}
