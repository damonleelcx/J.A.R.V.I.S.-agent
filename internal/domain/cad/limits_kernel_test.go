package cad_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// A design too large to build is refused by the kernel as an error that says why,
// not built as nothing. Phase 3, stage S0 of docs/plan-2026-09-13-millions-of-parts.md.
func TestKernel_ADesignTooLargeToBuildIsRefusedAndSaysWhy(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	stud := geometry.Part{ID: "stud", Shape: "box", Size: map[string]float64{"width": 1, "height": 1, "depth": 1},
		Repeat: &geometry.Repeat{Count: 500, Offset: []float64{2, 0, 0}}}
	doc := geometry.Document{Name: "panel", Units: "mm", Root: "panel", Definitions: []geometry.Part{stud},
		Assemblies: []geometry.Assembly{{ID: "panel", Children: []geometry.Child{
			{ID: "row", Ref: "stud", Pattern: &geometry.Pattern{Kind: "linear", Count: 10, Offset: []float64{0, 3, 0}}},
		}}}}
	_, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
	if err == nil || errs.CodeOf(err) != errs.CodeValidationFailed || !strings.Contains(err.Error(), "more than 4096 parts") {
		t.Fatalf("a 5000-part design was not refused with the reason: %v", err)
	}
}
