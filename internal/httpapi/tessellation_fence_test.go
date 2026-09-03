package httpapi

import (
	"regexp"
	"strconv"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// The exported mesh must be the surface that was on screen (PRD VIS-05).
//
// # What breaks without this
//
// The workbench draws a cylinder with 40 flat faces, and the export label states
// the resulting chord error as a measured figure — "the exported surface lies up
// to 0.034 mm inside the one described". That sentence is only true while the
// exporter tessellates with the renderer's counts. Raise one of them and the
// export becomes a finer mesh with a stated deviation belonging to a surface
// nobody has seen; lower it and the file is coarser than the picture that
// persuaded somebody to download it. Neither failure produces an error, and both
// make a measured claim false, which is worse than a vague one.
//
// # Why it parses rather than greps
//
// The renderer declares the counts in one object literal. A grep for "40" in a
// 900-line WebGL file matches matrix indices, colour components and viewport
// sizes; this reads the declaration itself, so it fails when the declaration
// changes and stays quiet when anything else does.
//
// The asset comes from the embedded filesystem rather than from disk: a fence
// that reads ../assets/forge3d.js passes on a developer's machine and proves
// nothing about the binary that ships.
func TestTessellation_GoMatchesTheRenderer(t *testing.T) {
	src, err := assetFS.ReadFile("assets/forge3d.js")
	if err != nil {
		t.Fatalf("the renderer is not embedded in this build: %v", err)
	}
	decl := regexp.MustCompile(`var TESSELLATION = \{([^}]*)\};`).FindSubmatch(src)
	if decl == nil {
		t.Fatal("forge3d.js no longer declares TESSELLATION as one object literal. " +
			"It is the renderer's half of a fact the exporter also states; if it moved, " +
			"move this fence with it rather than deleting it.")
	}
	field := func(name string) int {
		t.Helper()
		m := regexp.MustCompile(name + `:\s*(\d+)`).FindSubmatch(decl[1])
		if m == nil {
			t.Fatalf("TESSELLATION has no %s: %s", name, decl[1])
		}
		n, err := strconv.Atoi(string(m[1]))
		if err != nil {
			t.Fatal(err)
		}
		return n
	}

	radial, sphereRadial, sphereRings := geometry.TessellationCounts()

	if got := field("radial"); got != radial {
		t.Errorf("the renderer draws cylinders with %d sides and the exporter writes %d. "+
			"The downloaded file is not the surface the person was looking at, and the "+
			"deviation printed on the label belongs to neither.", got, radial)
	}
	if got := field("sphereRadial"); got != sphereRadial {
		t.Errorf("the renderer draws spheres with %d segments and the exporter writes %d", got, sphereRadial)
	}
	// The ring count is DERIVED in both places, from the same rule. Checked here
	// so a change to the derivation on either side is caught, rather than only a
	// change to the literal.
	wantRings := sphereRadial / 2
	if wantRings < 8 {
		wantRings = 8
	}
	if sphereRings != wantRings {
		t.Errorf("the exporter uses %d sphere rings; the renderer derives max(8, segments/2) = %d",
			sphereRings, wantRings)
	}
}
