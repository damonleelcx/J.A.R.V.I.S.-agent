package cadbridge

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/cad"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// The surface a turn looks at carries what the kernel MEASURED about making it,
// and any named section — from the same build that drew the picture.
//
// addresses issue 6. One build per turn was settled in Stage 7; asking a second
// time for "could this be made" would reverse it, so the one build carries both.
func TestKernel_TheTurnsSurfaceCarriesWhatTheKernelMeasured(t *testing.T) {
	python := os.Getenv("FORGE_CAD_PYTHON")
	if python == "" {
		t.Skip("FORGE_CAD_PYTHON is unset; skipping the CAD kernel tests.")
	}
	k := cad.New(python, logx.Discard())
	defer k.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	doc := &geometry.Document{Name: "bracket", Units: "mm", Parts: []geometry.Part{
		{ID: "web", Name: "Web", Shape: "box", Process: string(geometry.ProcessPrintingFDM),
			Size:     map[string]float64{"width": 60, "height": 0.4, "depth": 60},
			Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}},
	}}
	doc.Sections = []geometry.Section{{ID: "mid", Part: "web", Axis: "x", At: 0}}

	built, err := Solids(k).BuildSurface(ctx, doc)
	if err != nil {
		t.Fatal(err)
	}
	if len(built.Manufacturability) != 1 || built.Manufacturability[0].ID != "web" {
		t.Fatalf("the turn's surface carries %d measurement(s): %+v",
			len(built.Manufacturability), built.Manufacturability)
	}
	// 60 x 0.4 x 60: the thin way through is 0.4, which is under FDM's floor.
	if w := built.Manufacturability[0].MinWall; w == nil || *w < 0.399 || *w > 0.401 {
		t.Errorf("the web's wall reached the turn as %v, want 0.4", w)
	}
	// And how many parts there were to measure, so a truncated check can be told
	// from a model with one part in it.
	if built.ManufacturabilityParts != 1 {
		t.Errorf("the surface says the model has %d part(s) to measure", built.ManufacturabilityParts)
	}
	if len(built.Sections) != 1 || built.Sections[0].Unmeasured != "" {
		t.Fatalf("the named section did not reach the turn: %+v", built.Sections)
	}
	// 0.4 (y) by 60 (z): area 24 mm².
	if a := built.Sections[0].Area; a < 23.99 || a > 24.01 {
		t.Errorf("the section's area reached the turn as %g mm², want 24", a)
	}
}
