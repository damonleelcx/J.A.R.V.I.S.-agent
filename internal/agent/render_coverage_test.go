package agent

import (
	"context"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// coverageSolids builds a surface whose interference check covered only part of
// the model.
type coverageSolids struct{ built Built }

func (s coverageSolids) BuildSurface(context.Context, *Prototype) (Built, error) { return s.built, nil }

// The render carries how much of the model the kernel checked onto the sheet the
// turn reads. Phase 5, stage V2: without this, repairIfPartsOverlap sees a sheet
// that says nothing was truncated and nothing was skipped, and a partial check
// reads as a whole one however carefully the note is written.
func TestRender_CarriesHowMuchTheCheckCovered(t *testing.T) {
	c := &Conversation{solids: coverageSolids{built: Built{
		Parts:     []geometry.RenderPart{cube("plate")},
		Truncated: true, Checked: 2000, Pairs: 5000, Found: 11175, Buried: 768, BuriedCounted: true,
		Skipped: []string{"Bracket: Standard_Failure"},
	}}}

	sheet := c.render(context.Background(), drilledPlate())

	if !sheet.FromKernel {
		t.Fatal("the fixture did not produce a kernel render")
	}
	if !sheet.Truncated || sheet.Checked != 2000 || sheet.Pairs != 5000 {
		t.Errorf("the sheet says truncated=%v, checked %d of %d; the kernel said true, 2000 of 5000",
			sheet.Truncated, sheet.Checked, sheet.Pairs)
	}
	// Added 2026-09-15 (next scale walls): how many were found, when the list is
	// the worst of them.
	if sheet.Found != 11175 {
		t.Errorf("the sheet says %d clashes were found; the kernel said 11,175", sheet.Found)
	}
	// Added 2026-09-15 (repair judged by the kernel total): how many are buried, the
	// number an overlap repair is judged by.
	if sheet.Buried != 768 || !sheet.BuriedCounted {
		t.Errorf("the sheet says %d buried, counted=%v; the kernel counted 768", sheet.Buried, sheet.BuriedCounted)
	}
	if len(sheet.Skipped) != 1 || sheet.Skipped[0] != "Bracket: Standard_Failure" {
		t.Errorf("the sheet lost the part the kernel could not build: %v", sheet.Skipped)
	}
}
