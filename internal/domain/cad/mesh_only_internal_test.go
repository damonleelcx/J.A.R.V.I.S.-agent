package cad

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// The kernel's own triangle budget for a mesh-only part (sidecar.py,
// _LATTICE_BUDGET): Go refuses past its estimate first, and this is what stops a
// lattice whose estimate ran low. Sent straight to the process, past Go's checks,
// with numbers Go would refuse.
func TestKernel_ALatticePastTheKernelsOwnBudgetIsRefusedByName(t *testing.T) {
	python := os.Getenv("FORGE_CAD_PYTHON")
	if python == "" {
		t.Skip("FORGE_CAD_PYTHON is unset; skipping the CAD kernel tests.")
	}
	k := New(python, logx.Discard())
	defer k.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// A kernel with no manifold3d refuses this lattice for THAT reason instead of
	// for its triangle count, so the fence would be asserting the wrong refusal.
	// See MeshOnlySupport.
	if ok, why := k.MeshOnlySupport(ctx); !ok {
		t.Skipf("this kernel cannot build mesh-only parts at all, so its triangle budget cannot be "+
			"reached — the kernel says %q. Run `make cad-venv` to install the pinned "+
			"internal/domain/cad/requirements.txt, which carries manifold3d (pinned by PR 156).", why)
	}
	s, err := k.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer k.release(s)

	lattice := geometry.Solid{ID: "infill", Label: "Infill", Shape: "lattice", MeshOnly: true, Lattice: "gyroid",
		Dims:   map[string]float64{"width": 60, "height": 40, "depth": 30, "cell": 10, "thickness": 1, "edge": 1 / 1.5},
		Matrix: [9]float64{1, 0, 0, 0, 1, 0, 0, 0, 1}}
	plate := geometry.Solid{ID: "plate", Label: "Plate", Shape: "box",
		Dims:   map[string]float64{"width": 10, "height": 10, "depth": 10},
		Matrix: [9]float64{1, 0, 0, 0, 1, 0, 0, 0, 1}}
	res, err := s.roundTrip(ctx, request{Solids: []geometry.Solid{plate, lattice}, Format: "mesh"}, buildTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Fatalf("the build failed whole: %s", res.Error)
	}
	for _, m := range res.Mesh {
		if m.ID == "infill" {
			t.Fatalf("a lattice of %d triangles came back", len(m.Triangles)/3)
		}
	}
	joined := strings.Join(res.Skipped, "; ")
	if !strings.Contains(joined, "Infill: the lattice is ") || !strings.Contains(joined, "past the 200000 a mesh-only part may have") {
		t.Errorf("skipped %q; want the lattice refused by name, with its count and the budget", joined)
	}
}
