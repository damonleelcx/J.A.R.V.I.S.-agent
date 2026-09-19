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

// Looks designed, stage B1: what the kernel says about the features reaches the
// turn. A round built smaller than asked, and a feature it could not apply, are
// carried from the build onto agent.Built, where the turn's note reads them.
func TestBuildSurface_CarriesWhatTheKernelSaidAboutTheFeatures(t *testing.T) {
	python := os.Getenv("FORGE_CAD_PYTHON")
	if python == "" {
		t.Skip("FORGE_CAD_PYTHON is unset; skipping the CAD kernel tests")
	}
	k := cad.New(python, logx.Discard())
	t.Cleanup(k.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	plate := func(id string, x float64) geometry.Part {
		return geometry.Part{ID: id, Name: id, Shape: "box", Size: map[string]float64{"width": 60, "height": 6, "depth": 60},
			Position: []float64{x, 0, 0}, Rotation: []float64{0, 0, 0}}
	}
	doc := &geometry.Document{Name: "plates", Units: "mm", Parts: []geometry.Part{plate("a", 0), plate("b", 100)},
		Features: []geometry.Feature{
			{ID: "smaller", Op: "fillet", Of: "a", Radius: 45, Edges: "vertical"},
			{ID: "impossible", Op: "fillet", Of: "b", Radius: 400, Edges: "vertical"}}}
	built, err := Solids(k).BuildSurface(ctx, doc)
	if err != nil {
		t.Fatal(err)
	}
	if len(built.FeatureReductions) != 1 || len(built.FeatureFailures) != 1 {
		t.Errorf("the turn is not told what the kernel did to the features: reductions=%v failures=%v",
			built.FeatureReductions, built.FeatureFailures)
	}
}
