package cad

import (
	"errors"
	"strings"
	"testing"
)

// ‼️ A kernel with no manifold3d is recognised, and nothing else is (2026-09-20).
//
// A `.cadvenv` built before PR 156 pinned manifold3d gives a kernel that answers
// every request, builds every exact solid, and refuses exactly the lattices. The
// lattice fences then failed with "the lattice is not drawn on the turn's
// surface" — a sentence that points at the lattice code and means a missing
// package. CI installs the pin, so it only ever landed on a developer.
//
// The probe turns that into a skip. Its whole job is telling ONE refusal apart
// from every other outcome, so both directions are held here: a kernel that
// cannot build mesh-only parts must be recognised, and a kernel with any other
// problem must NOT be — a probe that skipped on every refusal would silence the
// fences it exists to protect.
//
// No kernel is needed: the decision is a function of the reply.
func TestMeshOnlySupport_RecognisesAMissingManifold3dAndNothingElse(t *testing.T) {
	// The sidecar's own refusal, as `_lattice_mesh` writes it.
	missing := &Build{Skipped: []string{
		"Probe: this kernel has no manifold3d, which builds mesh-only parts"}}
	ok, why := meshOnlyFromProbe(missing, nil)
	if ok {
		t.Error("a kernel that said it has no manifold3d is reported as able to build mesh-only parts")
	}
	if !strings.Contains(why, "manifold3d") {
		t.Errorf("the reason given is %q; it must carry the kernel's own words, which name the package", why)
	}

	// A lattice the kernel COULD have built and refused for its own reasons. The
	// fence that asked must fail with its own message, not be skipped.
	for _, other := range []*Build{
		{Skipped: []string{"Probe: the lattice is 412000 triangles, past the 200000 a mesh-only part may have"}},
		{Skipped: []string{"Probe: 'spiral' is not a lattice pattern this kernel knows"}},
		{Skipped: []string{"Anchor: a box needs a width"}},
	} {
		if ok, why := meshOnlyFromProbe(other, nil); !ok {
			t.Errorf("%q was read as a missing manifold3d, so the lattice fences would skip on it: %q",
				other.Skipped[0], why)
		}
	}

	// A lattice that built, and a build that did not happen at all. Both are
	// "supported" as far as this is concerned: the second is a broken kernel, and
	// a broken kernel must reach the fence rather than be hidden by the probe.
	if ok, _ := meshOnlyFromProbe(&Build{Parts: 2}, nil); !ok {
		t.Error("a probe that built cleanly is reported as unable to build mesh-only parts")
	}
	if ok, _ := meshOnlyFromProbe(nil, errors.New("the kernel died")); !ok {
		t.Error("a kernel that failed the probe outright is reported as lacking manifold3d, which it did not say")
	}

	// And the probe itself is a document that exercises the path: one exact
	// solid, so the reply is an ordinary build, and one lattice, so a kernel that
	// cannot build lattices has something to refuse.
	var exact, lattices int
	for _, p := range meshOnlyProbe.Parts {
		if p.Shape == "lattice" {
			lattices++
		} else {
			exact++
		}
	}
	if exact < 1 || lattices < 1 {
		t.Errorf("the probe has %d exact part(s) and %d lattice(s); it needs one of each to answer anything",
			exact, lattices)
	}
}

// A kernel that is not configured at all answers the same question with the same
// shape of answer, rather than starting a process to find out.
func TestMeshOnlySupport_WithoutAKernelSaysSoWithoutStartingOne(t *testing.T) {
	var none *Kernel
	if ok, why := none.MeshOnlySupport(t.Context()); ok || why == "" {
		t.Errorf("a nil kernel answered ok=%v why=%q", ok, why)
	}
	empty := New("", nil)
	if ok, why := empty.MeshOnlySupport(t.Context()); ok || !strings.Contains(why, "no CAD kernel") {
		t.Errorf("an unconfigured kernel answered ok=%v why=%q", ok, why)
	}
}
