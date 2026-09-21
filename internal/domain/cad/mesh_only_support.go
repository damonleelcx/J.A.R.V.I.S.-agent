package cad

import (
	"context"
	"strings"
	"sync"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// Whether THIS kernel can build mesh-only parts.
//
// # Why this is asked rather than assumed
//
// Mesh-only parts (geometry/lattice.go) are built by manifold3d and by nothing
// else. It is one line of internal/domain/cad/requirements.txt, pinned by PR 156,
// and the sidecar is deliberately built to survive its absence: every exact part
// still builds and each mesh-only part is refused BY NAME (sidecar.py,
// `_lattice_mesh`). That is the right behaviour for a deployment.
//
// It is the wrong behaviour for a fence. A developer whose `.cadvenv` predates
// the pin has a kernel that answers every request, builds every solid, and
// refuses exactly the lattices — so the lattice fences fail with "the lattice is
// not drawn", which reads like a defect in the lattice code and is a missing
// package. CI installs the pin and stays green, so the failure only ever lands on
// the person least able to read it. Reported 2026-09-20 against
// TestKernel_TheTurnsSurfaceNamesItsMeshOnlyParts.
//
// This repository already has the answer to that shape of problem: a kernel test
// SKIPS, with the sentence that fixes it, when the kernel is not there
// (FORGE_CAD_PYTHON). A kernel that is there but cannot build the thing under
// test is the same situation one level down.
//
// # Why it asks the kernel instead of the interpreter
//
// The question is "can this kernel build a lattice?", not "is a module
// importable?". Asking the kernel is the only way to get the first answer:
// shelling out to pip or importing manifold3d from Go would be a different
// process, a different interpreter, possibly a different machine, and would still
// not say whether the sidecar's own code path works. So it builds one small
// lattice and reads the refusal the sidecar already writes.

// meshOnlyProbe is the smallest document that answers the question: one solid, so
// the reply is an ordinary build rather than the mesh-only-only path, and one
// lattice small enough (two cells across) that building it costs nothing worth
// measuring.
var meshOnlyProbe = geometry.Document{
	Name: "mesh-only probe", Units: "mm",
	Parts: []geometry.Part{
		{ID: "anchor", Name: "Anchor", Shape: "box",
			Size:     map[string]float64{"width": 10, "height": 10, "depth": 10},
			Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}},
		{ID: "probe", Name: "Probe", Shape: "lattice", Lattice: "gyroid",
			Size:     map[string]float64{"width": 30, "height": 30, "depth": 30, "cell": 15, "thickness": 2},
			Position: []float64{0, 25, 0}, Rotation: []float64{0, 0, 0}},
	},
}

// meshOnlySupport caches the probe: the answer cannot change while a process
// lives, and a fence that asked per test would pay for a build per test.
type meshOnlySupport struct {
	once sync.Once
	ok   bool
	why  string
}

// MeshOnlySupport reports whether this kernel can build mesh-only parts, and when
// it cannot, the kernel's own words for why.
//
// Built for the lattice fences, which skip on a false. It is exported rather than
// duplicated as a test helper because two packages need it — internal/domain/cad
// and internal/agent/cadbridge — and two copies of a capability probe drift.
//
// It answers TRUE for anything it cannot attribute: a kernel that failed the
// probe outright, or refused the lattice for some other reason, is a kernel with
// a real problem, and the caller's own fence must be allowed to fail with its own
// message rather than be skipped by a probe that could not tell.
func (k *Kernel) MeshOnlySupport(ctx context.Context) (bool, string) {
	if k == nil || !k.Available() {
		return false, "this deployment has no CAD kernel"
	}
	k.meshOnly.once.Do(func() {
		built, err := k.BuildMesh(ctx, meshOnlyProbe, geometry.Millimetre)
		k.meshOnly.ok, k.meshOnly.why = meshOnlyFromProbe(built, err)
	})
	return k.meshOnly.ok, k.meshOnly.why
}

// meshOnlyMissing is the sidecar's own word for the package that is not there
// (`_lattice_mesh` raises "this kernel has no manifold3d, …"). Matched on the
// package name alone: the rest of that sentence is prose and may be rewritten,
// the package name is the fact.
const meshOnlyMissing = "manifold3d"

// meshOnlyFromProbe is the decision the probe makes, separated from the build so
// it can be fenced on both answers without a kernel.
func meshOnlyFromProbe(built *Build, err error) (bool, string) {
	if err != nil || built == nil {
		return true, ""
	}
	for _, skipped := range built.Skipped {
		if strings.Contains(skipped, meshOnlyMissing) {
			return false, skipped
		}
	}
	return true, ""
}
