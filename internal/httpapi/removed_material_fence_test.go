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
