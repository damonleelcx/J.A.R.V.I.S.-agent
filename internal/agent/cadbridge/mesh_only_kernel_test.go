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

// The surface a turn looks at names the mesh-only parts the kernel left out of its
// interference check (geometry/lattice.go), and still draws them.
func TestKernel_TheTurnsSurfaceNamesItsMeshOnlyParts(t *testing.T) {
	python := os.Getenv("FORGE_CAD_PYTHON")
	if python == "" {
		t.Skip("FORGE_CAD_PYTHON is unset; skipping the CAD kernel tests.")
	}
	k := cad.New(python, logx.Discard())
	defer k.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// A kernel whose venv has no manifold3d builds every exact solid and refuses
	// every lattice by name, so this fence fails with "the lattice is not drawn",
	// which reads as a defect in the lattice code and is a missing package. The
	// kernel is asked rather than the interpreter — see cad.MeshOnlySupport.
	if ok, why := k.MeshOnlySupport(ctx); !ok {
		t.Skipf("this kernel cannot build mesh-only parts, so the turn's surface would be naming "+
			"their absence — the kernel says %q. Run `make cad-venv` to install the pinned "+
			"internal/domain/cad/requirements.txt, which carries manifold3d (pinned by PR 156).", why)
	}

	doc := &geometry.Document{Name: "infill", Units: "mm", Parts: []geometry.Part{
		{ID: "plate", Name: "Plate", Shape: "box", Size: map[string]float64{"width": 60, "height": 6, "depth": 60},
			Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}},
		{ID: "infill", Name: "Infill", Shape: "lattice", Lattice: "gyroid",
			Size:     map[string]float64{"width": 30, "height": 30, "depth": 30, "cell": 15, "thickness": 2},
			Position: []float64{0, 18, 0}, Rotation: []float64{0, 0, 0}},
	}}
	built, err := Solids(k).BuildSurface(ctx, doc)
	if err != nil {
		t.Fatal(err)
	}
	if len(built.MeshOnly) != 1 || built.MeshOnly[0] != "Infill" {
		t.Errorf("the surface names %v as mesh-only; want [Infill]", built.MeshOnly)
	}
	drawn := false
	for _, p := range built.Parts {
		drawn = drawn || p.ID == "infill"
	}
	if !drawn {
		t.Error("the lattice is not drawn on the turn's surface")
	}
}
