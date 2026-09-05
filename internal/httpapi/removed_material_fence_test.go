package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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

	// The same thing with radii on it. Corner arithmetic is a second place the
	// two implementations can disagree, and it disagrees SILENTLY: a corner
	// rounded to a different radius, or an arc stepped the other way round, is
	// still a closed solid of almost the right volume.
	roundedProfile := []geometry.Point{{X: -2, Y: -6}, {X: 6, Y: -6, Radius: 1.5},
		{X: 6, Y: -2, Radius: 1}, {X: 2, Y: -2}, {X: 2, Y: 6, Radius: 1}, {X: -2, Y: 6}}
	bentPath := []geometry.Point{{}, {Z: 30, Radius: 12}, {X: 40, Z: 30, Radius: 8},
		{X: 40, Y: 25, Z: 30}}

	// And hollow. A hole adds INNER WALLS that have to face into the void, and a
	// bridge that splices the hole into the outline for triangulation — two more
	// places the two implementations can part company, and neither changes the
	// silhouette.
	hollow := []geometry.Point{{X: -10, Y: -10}, {X: 10, Y: -10, Radius: 3},
		{X: 10, Y: 10}, {X: -10, Y: 10}}
	bore := [][]geometry.Point{{{X: -5, Y: -5}, {X: 5, Y: -5}, {X: 5, Y: 5, Radius: 2},
		{X: -5, Y: 5}}}

	for _, tc := range []struct {
		name    string
		profile []geometry.Point
		holes   [][]geometry.Point
		path    []geometry.Point
	}{
		{"sharp corners", profile, nil, path},
		{"rounded corners and bend radii", roundedProfile, nil, bentPath},
		{"a hollow section", hollow, bore, bentPath},
	} {
		t.Run(tc.name, func(t *testing.T) {
			compareSweptFacets(t, node, dir, asset, tc.profile, tc.holes, tc.path)
		})
	}
}

// compareSweptFacets runs the renderer's own builder in node and checks its
// facets against the ones geometry.Tessellate produces for the same document.
func compareSweptFacets(t *testing.T, node, dir, asset string, profile []geometry.Point,
	holes [][]geometry.Point, path []geometry.Point) {
	t.Helper()
	harness := filepath.Join(dir, "run.js")
	script := `
      // The asset is browser code and attaches itself to a global. Nothing else
      // about it is touched: it is loaded exactly as a page loads it.
      const fs = require('fs'), vm = require('vm');
      const sandbox = { window: {}, console };
      vm.createContext(sandbox);
      vm.runInContext(fs.readFileSync(process.argv[2], 'utf8'), sandbox);
      const part = JSON.parse(fs.readFileSync(process.argv[3], 'utf8'));
      const built = sandbox.window.Forge3D.geometry.sweep(part.profile, part.path, part.holes);
      if (built.approximated) { console.error(built.approximated); process.exit(2); }
      process.stdout.write(JSON.stringify({p: built.geo.positions, n: built.geo.normals}));
    `
	if err := os.WriteFile(harness, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	partJSON, err := json.Marshal(map[string]any{"profile": profile, "holes": holes, "path": path})
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
		Parts: []geometry.Part{{ID: "s", Shape: "sweep", Profile: profile, Holes: holes, Path: path}}}
	exported := geometry.Tessellate(doc, geometry.Millimetre).Triangles()

	// Compared as a SET of facets, NUMERICALLY.
	//
	// The order each builder emits its caps and walls in is not a property of the
	// solid, so the facets are canonicalised and sorted rather than zipped. And
	// the comparison is a tolerance rather than an equality, because the two
	// arrive at the same facet by different arithmetic: measured 2026-09-05, a
	// wall whose normal is exactly (0, 1, 0) in Go came back as
	// (-4.12e-16, 1, 0) from the browser, and one vertex that is exactly 46 there
	// is 45.999999999999993 here. Neither is a disagreement about the shape.
	//
	// The NORMAL is compared and not just the positions. This renderer is handed
	// each normal explicitly and draws with back-face culling off, so a facet lit
	// from the wrong side looks like a shading bug rather than like a solid that
	// is inside out — while the exported file, built from the same winding, would
	// be the one that is actually wrong.
	drawnFacets := make([][12]float64, 0, len(flat)/9)
	for i := 0; i < len(flat); i += 9 {
		drawnFacets = append(drawnFacets, canonicalFacet(
			[3]float64{flat[i], flat[i+1], flat[i+2]},
			[3]float64{flat[i+3], flat[i+4], flat[i+5]},
			[3]float64{flat[i+6], flat[i+7], flat[i+8]},
			[3]float64{drawnGeo.N[i], drawnGeo.N[i+1], drawnGeo.N[i+2]}))
	}
	builtFacets := make([][12]float64, 0, len(exported))
	for _, tr := range exported {
		builtFacets = append(builtFacets, canonicalFacet(tr.A, tr.B, tr.C, tr.Normal))
	}
	if len(builtFacets) != len(drawnFacets) {
		t.Fatalf("the exporter built %d facets and the renderer drew %d",
			len(builtFacets), len(drawnFacets))
	}
	sortFacets(builtFacets)
	sortFacets(drawnFacets)

	// Facets that match exactly are consumed. What is allowed to be left over is
	// the CAPS, and only the caps.
	//
	// # Why anything is allowed to differ at all
	//
	// The walls are determined: a ring per path point, a quad per outline edge,
	// no choices. Every convention worth guarding — the section frame, the
	// mitre, whether the section rolls, which side is material — lives there, and
	// those must match to the last bit.
	//
	// The caps are ear-clipped, and TWO CORRECT EAR CLIPPINGS OF ONE OUTLINE ARE
	// NOT THE SAME TRIANGLES. That is the property this codebase deliberately
	// shares instead of the code. It never came up before arcs because both
	// sides made identical decisions from identical arithmetic; a rounded corner
	// puts forty nearly-collinear points on the outline, where an ear test turns
	// on a cross product of about 1e-16 and the two sides can legitimately part
	// company. Measured 2026-09-05.
	//
	// So the leftovers must be two triangulations of the SAME REGION: the same
	// vertices, the same total area, and few enough of them to be caps.
	leftBuilt, leftDrawn := consumeMatching(builtFacets, drawnFacets)
	if len(leftBuilt) != len(leftDrawn) {
		t.Fatalf("%d facets of the exporter's and %d of the renderer's have no counterpart",
			len(leftBuilt), len(leftDrawn))
	}
	if len(leftBuilt) == 0 {
		return
	}
	if len(leftBuilt)*4 > len(builtFacets) {
		t.Fatalf("%d of %d facets differ — far more than the caps, so this is not two ear "+
			"clippings of one outline but two different solids",
			len(leftBuilt), len(builtFacets))
	}
	if a, b := facetArea(leftBuilt), facetArea(leftDrawn); math.Abs(a-b) > 1e-6*math.Max(1, a) {
		t.Errorf("the facets that differ cover %.9f on the exporter and %.9f on the renderer; "+
			"any correct triangulation of an outline covers exactly the outline", a, b)
	}
	if a, b := facetVertices(leftBuilt), facetVertices(leftDrawn); !sameVertexSet(a, b) {
		t.Errorf("the facets that differ are drawn between different points (%d and %d "+
			"distinct), so they are not two triangulations of one outline", len(a), len(b))
	}
}

// consumeMatching pairs off facets that agree within tolerance and returns what
// is left on each side.
func consumeMatching(built, drawn [][12]float64) (leftBuilt, leftDrawn [][12]float64) {
	used := make([]bool, len(drawn))
	for _, b := range built {
		found := false
		for j, d := range drawn {
			if used[j] {
				continue
			}
			same := true
			for k := 0; k < 12 && same; k++ {
				same = math.Abs(b[k]-d[k]) <= 1e-9
			}
			if same {
				used[j], found = true, true
				break
			}
		}
		if !found {
			leftBuilt = append(leftBuilt, b)
		}
	}
	for j, d := range drawn {
		if !used[j] {
			leftDrawn = append(leftDrawn, d)
		}
	}
	return leftBuilt, leftDrawn
}

func facetArea(f [][12]float64) float64 {
	var total float64
	for _, v := range f {
		u := [3]float64{v[3] - v[0], v[4] - v[1], v[5] - v[2]}
		w := [3]float64{v[6] - v[0], v[7] - v[1], v[8] - v[2]}
		c := [3]float64{u[1]*w[2] - u[2]*w[1], u[2]*w[0] - u[0]*w[2], u[0]*w[1] - u[1]*w[0]}
		total += math.Sqrt(c[0]*c[0]+c[1]*c[1]+c[2]*c[2]) / 2
	}
	return total
}

func facetVertices(f [][12]float64) map[string]bool {
	out := map[string]bool{}
	for _, v := range f {
		for i := 0; i < 3; i++ {
			out[fmt.Sprintf("%.6f,%.6f,%.6f", v[i*3], v[i*3+1], v[i*3+2])] = true
		}
	}
	return out
}

func sameVertexSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// canonicalFacet is a facet written from a fixed starting vertex, so the same
// triangle written from a different corner reads the same.
//
// Rotating preserves the winding, so a facet turned inside out still differs —
// which is the half of this that matters.
func canonicalFacet(a, b, c, n [3]float64) [12]float64 {
	corner := [3][3]float64{a, b, c}
	first := 0
	for i := 1; i < 3; i++ {
		if less3(corner[i], corner[first]) {
			first = i
		}
	}
	var out [12]float64
	for i := 0; i < 3; i++ {
		v := corner[(first+i)%3]
		out[i*3], out[i*3+1], out[i*3+2] = v[0], v[1], v[2]
	}
	out[9], out[10], out[11] = n[0], n[1], n[2]
	return out
}

func less3(a, b [3]float64) bool {
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

func sortFacets(f [][12]float64) {
	sort.Slice(f, func(i, j int) bool {
		for k := 0; k < 12; k++ {
			if f[i][k] != f[j][k] {
				return f[i][k] < f[j][k]
			}
		}
		return false
	})
}
