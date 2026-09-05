package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
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
		{"case 'sweep'", "the renderer has no sweep case, so every part that bends — a pipe " +
			"run, a handrail, a cable tray — is drawn as a bounding box"},
		{"sweepSections", "there is no sweep builder at all"},
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

// The viewport and the exporter must produce the SAME swept solid, facet for
// facet.
//
// # Why a string fence is not enough for this one
//
// The fences above check that the renderer HAS an extrusion case and a revolve
// case, which is all a text search can do. That is enough for those two, because
// what they could get wrong is caught elsewhere: an extrusion has no convention
// beyond the outline itself, and a revolve's only choice is an axis.
//
// A sweep has three, and none of them changes the volume: which way up the
// section starts, how it is carried round a bend, and where the mitre puts the
// corner. A browser copy that rolled the section as it went would draw a rail
// with its flat face pointing somewhere nobody asked for, export a different
// solid, and pass every volume test on both sides.
//
// So this runs the renderer's own builder in node and compares its facets with
// the ones geometry.Tessellate produces — the two implementations, on the same
// document, meeting at the geometry rather than at a substring.
//
// # Why it skips rather than fails without node
//
// Node is a development tool, not a deployment one: this asset runs in a
// browser, and nothing about the product requires node to exist. It is the same
// bargain the CAD kernel tests make with FORGE_CAD_PYTHON.
func TestRendererSweepsTheSameSolidAsTheExporter(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node on PATH; skipping the renderer/exporter sweep comparison")
	}
	src, err := assetFS.ReadFile("assets/forge3d.js")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	asset := filepath.Join(dir, "forge3d.js")
	if err := os.WriteFile(asset, src, 0o600); err != nil {
		t.Fatal(err)
	}

	// A section that is neither round nor square, on a path that bends twice and
	// leaves its first plane — so a rolled section, a mitre computed the wrong
	// way and a section framed differently at the start all show up.
	profile := []geometry.Point{{X: -2, Y: -6}, {X: 6, Y: -6}, {X: 6, Y: -2},
		{X: 2, Y: -2}, {X: 2, Y: 6}, {X: -2, Y: 6}}
	path := []geometry.Point{{}, {Z: 30}, {X: 40, Z: 30}, {X: 40, Y: 25, Z: 30}}

	harness := filepath.Join(dir, "run.js")
	script := `
      // The asset is browser code and attaches itself to a global. Nothing else
      // about it is touched: it is loaded exactly as a page loads it.
      const fs = require('fs'), vm = require('vm');
      const sandbox = { window: {}, console };
      vm.createContext(sandbox);
      vm.runInContext(fs.readFileSync(process.argv[2], 'utf8'), sandbox);
      const part = JSON.parse(fs.readFileSync(process.argv[3], 'utf8'));
      const built = sandbox.window.Forge3D.geometry.sweep(part.profile, part.path);
      if (built.approximated) { console.error(built.approximated); process.exit(2); }
      process.stdout.write(JSON.stringify({p: built.geo.positions, n: built.geo.normals}));
    `
	if err := os.WriteFile(harness, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	partJSON, err := json.Marshal(map[string]any{"profile": profile, "path": path})
	if err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(dir, "part.json")
	if err := os.WriteFile(input, partJSON, 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(node, harness, asset, input)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("the renderer could not build this sweep: %v %s", err, stderr.String())
	}
	var drawnGeo struct {
		P []float64 `json:"p"`
		N []float64 `json:"n"`
	}
	if err := json.Unmarshal(out, &drawnGeo); err != nil {
		t.Fatal(err)
	}
	flat := drawnGeo.P
	if len(flat)%9 != 0 || len(flat) == 0 {
		t.Fatalf("the renderer returned %d position values, which is not whole triangles", len(flat))
	}
	if len(drawnGeo.N) != len(flat) {
		t.Fatalf("%d normals for %d positions", len(drawnGeo.N), len(flat))
	}

	doc := geometry.Document{Name: "rail", Units: "mm",
		Parts: []geometry.Part{{ID: "s", Shape: "sweep", Profile: profile, Path: path}}}
	exported := geometry.Tessellate(doc, geometry.Millimetre).Triangles()

	// Compared as a SET of facets. The order each builder emits its caps and
	// walls in is not a property of the solid, but every corner and every
	// winding is.
	//
	// The NORMAL is part of the key, and not decoration. This renderer is handed
	// each normal explicitly and draws with back-face culling off, so a facet
	// lit from the wrong side looks like a shading bug rather than like a solid
	// that is inside out — and the exported file, built from the same winding,
	// would be the one that is actually wrong. Comparing positions alone would
	// let the two agree about the shape and disagree about which side of it is
	// material.
	key := func(a, b, c, n [3]float64) string {
		corner := [3][3]float64{a, b, c}
		first := 0
		for i := 1; i < 3; i++ {
			if fmt.Sprint(corner[i]) < fmt.Sprint(corner[first]) {
				first = i
			}
		}
		// Negative zero is folded away: the two arrive at the same normal by
		// different arithmetic and differ in the sign of zero on axes the facet
		// does not face at all.
		flatten := func(v [3]float64) [3]float64 { return [3]float64{v[0] + 0, v[1] + 0, v[2] + 0} }
		return fmt.Sprintf("%.6v/%.6v/%.6v n%.5v", flatten(corner[first]),
			flatten(corner[(first+1)%3]), flatten(corner[(first+2)%3]), flatten(n))
	}
	drawn := map[string]int{}
	for i := 0; i < len(flat); i += 9 {
		// Every vertex of a facet carries the same normal here; the first is the
		// facet's.
		drawn[key([3]float64{flat[i], flat[i+1], flat[i+2]},
			[3]float64{flat[i+3], flat[i+4], flat[i+5]},
			[3]float64{flat[i+6], flat[i+7], flat[i+8]},
			[3]float64{drawnGeo.N[i], drawnGeo.N[i+1], drawnGeo.N[i+2]})]++
	}
	if len(exported) != len(flat)/9 {
		t.Errorf("the exporter built %d facets and the renderer drew %d", len(exported), len(flat)/9)
	}
	for _, tr := range exported {
		k := key(tr.A, tr.B, tr.C, tr.Normal)
		if drawn[k] == 0 {
			t.Fatalf("the exporter built the facet %s and the renderer did not draw it — the "+
				"two disagree about where this sweep's material is, so the file and the "+
				"picture are different shapes", k)
		}
		drawn[k]--
	}
	for k, n := range drawn {
		if n > 0 {
			t.Fatalf("the renderer drew %d of the facet %s that the exporter did not build", n, k)
		}
	}
}
