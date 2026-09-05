package httpapi

import (
	"strings"
	"testing"
)

// The renderer must know that a cut tool is material being REMOVED.
//
// # What breaks without this
//
// A cut feature names a part as the tool that makes a hole, and the CAD kernel
// consumes it: the exported solid has a void where it was. The renderer has no
// boolean operations and cannot make that void — so if it does not read
// `features` at all, the four bolt holes of a bracket are drawn as four solid
// posts standing on the plate. That is not an approximation of a hole. It is the
// opposite of one, and a person looking at it would conclude the design is
// wrong.
//
// The document's own banner says the exported file is the one with the hole
// (geometry.FeatureNotes). This holds the other half: that the picture does not
// actively contradict it.
//
// # Why it is a text check and not a render
//
// There is no JavaScript test runner here, and standing up WebGL to assert a
// pixel would be a great deal of machinery to prove one branch is present.
// What this can do is fail when the branch is DELETED, which is the realistic
// failure: somebody refactors the loader, drops a field nothing in Go referred
// to, and every test stays green while the viewport starts lying.
func TestRendererKnowsWhatMaterialIsBeingRemoved(t *testing.T) {
	src, err := assetFS.ReadFile("assets/forge3d.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(src)

	for _, want := range []struct{ needle, why string }{
		{"spec.features", "the renderer never looks at the document's features, so it cannot " +
			"know which parts are holes"},
		{"'cut'", "the renderer does not single out the cut operation; a fuse tool is real " +
			"material and must not be ghosted"},
		{"removed[part.id]", "the loader does not mark the tool parts, so nothing downstream " +
			"can draw them differently"},
		{"REMOVED_ALPHA", "there is no distinct treatment for removed material, so a hole is " +
			"drawn as a solid post"},
	} {
		if !strings.Contains(js, want.needle) {
			t.Errorf("forge3d.js no longer contains %q: %s", want.needle, want.why)
		}
	}

	// The sort and the draw must agree about a part's alpha. A part sorted as
	// opaque and drawn translucent erases whatever is behind it, and a ghost is
	// translucent by definition — so this is exactly the case where two copies
	// of the rule would show.
	if n := strings.Count(js, "alphaOf("); n < 3 {
		t.Errorf("alphaOf is used %d times; the transparency sort and the draw call must both "+
			"go through it, or a ghosted part is sorted as opaque and erases what is behind it", n)
	}
}

// The rail must offer whatever the DEPLOYMENT can write, not two hardcoded
// formats.
//
// # What this caught
//
// The variant rail listed OBJ and STL as two literal buttons. So a deployment
// with a CAD kernel configured could build a real B-Rep that no button ever
// asked for: STEP was reachable from the API and from nowhere a person could
// click. The whole of wave 14 had no producer in the product, which is the
// failure this session has now hit three times in three different places.
func TestWorkbenchOffersWhateverTheDeploymentCanWrite(t *testing.T) {
	src, err := assetFS.ReadFile("assets/workbench.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(src)

	if !strings.Contains(js, "/v1/geometry/formats") {
		t.Error("the workbench never asks what this deployment can write, so a format that " +
			"exists only when a kernel is configured can never be offered")
	}
	// The literal pair is what the bug looked like. Either of them written as a
	// fixed data-format in the rail means the list is hardcoded again.
	for _, dead := range []string{`data-format="obj"`, `data-format="stl"`} {
		if strings.Contains(js, dead) {
			t.Errorf("the rail still hardcodes %s; the export list has to come from the server", dead)
		}
	}
	// An unavailable format is SHOWN, disabled, with the server's reason — a
	// person who cannot find STEP concludes it was forgotten.
	if !strings.Contains(js, "f.reason") {
		t.Error("the rail drops the server's reason for an unavailable format")
	}
	if !strings.Contains(js, "disabled") {
		t.Error("the rail has no disabled state, so an unavailable format is either missing " +
			"entirely or looks clickable")
	}
}

// The viewport must draw the outline shapes, and must not fan a concave one.
//
// # What breaks without this
//
// An unknown shape falls back to a bounding box, so a missing extrusion case
// draws every L-bracket as a rectangular block — a shape the design does not
// have, with a note calling it approximate. And a triangle FAN across a concave
// outline puts triangles outside the part, which is worse: no note, and a
// picture that looks fine and is wrong.
func TestRendererDrawsOutlineShapes(t *testing.T) {
	src, err := assetFS.ReadFile("assets/forge3d.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(src)

	for _, want := range []struct{ needle, why string }{
		{"case 'extrusion'", "the renderer has no extrusion case, so every profile is drawn " +
			"as a bounding box"},
		{"earClip", "there is no ear clipping, so a concave outline is fanned and its " +
			"triangles fall outside the part"},
		{"pointInTriangle2D", "the ear test does not check for enclosed vertices, which is " +
			"the half of ear clipping that makes it correct rather than a fan"},
		{"signedArea2D", "the winding is not normalised, so a clockwise outline is drawn " +
			"inside out"},
		{"case 'revolve'", "the renderer has no revolve case, so every turned part — a shaft, " +
			"a boss, a dome — is drawn as a bounding box"},
		{"revolveGeometry", "there is no revolve builder at all"},
	} {
		if !strings.Contains(js, want.needle) {
			t.Errorf("forge3d.js no longer contains %q: %s", want.needle, want.why)
		}
	}
	// earClip must return the points it reordered. Keeping a separate copy is
	// how the caps come out normalised and the side walls do not — the exact
	// defect the Go implementation shipped and a test caught.
	if !strings.Contains(js, "clipped.pts") {
		t.Error("the caller does not use the ordering earClip returned, so the caps and the " +
			"side walls can disagree about which way round the outline is")
	}
}
