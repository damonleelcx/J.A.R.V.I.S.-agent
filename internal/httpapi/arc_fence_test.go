package httpapi

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// The viewport and the exporter must draw the same BOWED outline, facet for
// facet (wave 29).
//
// # Why a text fence is not enough for this one
//
// TestRendererDrawsOutlineShapes checks that the renderer HAS an extrusion case,
// which is all a text search can do, and that is enough for a plain outline: an
// extrusion has no convention beyond the points themselves.
//
// A bow has three, and each of them is silently wrong rather than obviously
// wrong:
//
//   - WHICH WAY ROUND the arc goes. Two arcs join any two points through a
//     circle; the one that misses the via is a mirror image with the same
//     endpoints, the same radius and a completely different part.
//   - HOW MANY chords stand in for it. A renderer stepping an arc more coarsely
//     than the exporter shows a shape whose file is a different one.
//   - WHERE the closing edge is emitted, and what happens when the loop repeats
//     its first point — the via has to travel to entry 0 exactly as the radius
//     does.
//
// None of those changes the silhouette enough to see. So the facets are
// compared, which is the same bargain TestRendererSweepsTheSameSolidAsTheExporter
// already makes for a sweep and for the same reason.
func TestRendererBowsTheSameOutlineAsTheExporter(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node on PATH; skipping the renderer/exporter bow comparison")
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

	at := func(x, y float64) geometry.Point { return geometry.Point{X: x, Y: y} }
	bow := func(x, y, vx, vy float64) geometry.Point {
		return geometry.Point{X: x, Y: y, Via: &geometry.Point{X: vx, Y: vy}}
	}

	for _, tc := range []struct {
		name    string
		profile []geometry.Point
	}{
		// One bowed edge on a rectangle: a circular segment on a plate.
		{"one bowed edge", []geometry.Point{
			at(-20, 0), at(20, 0), at(20, 20), bow(-20, 20, 0, 25)}},
		// A crescent: two arcs between two points, which no corner radius can
		// say and which has no polygon at all.
		{"a crescent", []geometry.Point{
			at(-20, 0), bow(20, 0, 0, 14), bow(-20, 0, 0, 6)}},
		// The other way round. An arc drawn the wrong way has the same
		// endpoints and the same radius, so this pair is the direct test of
		// which of the two circles-through-three-points each side picked.
		{"a crescent bowing the other way", []geometry.Point{
			at(-20, 0), bow(20, 0, 0, -14), bow(-20, 0, 0, -6)}},
		// A bow AND a corner radius elsewhere on the same outline, which is
		// where the "an arc edge has sharp ends" rule earns its keep: both
		// implementations must decline to round the two corners the arc meets
		// and must round the one it does not.
		{"a bow beside a rounded corner", []geometry.Point{
			at(-20, 0), {X: 20, Y: 0, Radius: 4}, at(20, 20), bow(-20, 20, 0, 26)}},
		// Closed the way every polygon format closes a ring, with the via on the
		// repeated point. Both sides have to carry it to entry 0.
		{"a ring closed by repeating its first point", []geometry.Point{
			at(-20, 0), at(20, 0), at(20, 20), at(-20, 20),
			bow(-20, 0, -26, 10)}},
		// A via that names no arc — its three points are in line. Both sides
		// must read it as the straight edge it is rather than one refusing the
		// part and the other drawing it.
		{"a via that names no arc", []geometry.Point{
			at(-20, 0), at(20, 0), at(20, 20),
			{X: -20, Y: 20, Via: &geometry.Point{X: 0, Y: 20}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			compareBowedDrawing(t, node, dir, asset, tc.profile)
		})
	}
}

// compareBowedDrawing checks the two flattened drawings point for point.
//
// # Why the DRAWING and not the facets
//
// The first version of this compared triangles, the way the sweep fence does,
// and it went red on a crescent — with the two flattened outlines byte-identical
// and the two solids the same solid. The difference was the ear clipping: two
// triangulations of one planar polygon, which is the same surface and has no
// observable consequence.
//
// So it asserts the thing this wave actually decides and the thing that IS a
// property of the shape: which way round each arc goes, how finely it is stepped,
// where a closed run starts, and what happens to a via on a repeated closing
// point. A facet comparison would assert all of that too — and an implementation
// detail besides, which is how a fence starts going red for reasons nobody can
// act on.
func compareBowedDrawing(t *testing.T, node, dir, asset string, profile []geometry.Point) {
	t.Helper()

	harness := filepath.Join(dir, "bow.js")
	script := `
      const fs = require('fs'), vm = require('vm');
      const sandbox = { window: {}, console };
      vm.createContext(sandbox);
      vm.runInContext(fs.readFileSync(process.argv[2], 'utf8'), sandbox);
      const profile = JSON.parse(fs.readFileSync(process.argv[3], 'utf8'));
      const flat = sandbox.window.Forge3D.flattenDrawing(profile, true);
      if (!flat) { console.error('the renderer refused this outline'); process.exit(2); }
      process.stdout.write(JSON.stringify(flat));
    `
	if err := os.WriteFile(harness, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	profileJSON, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(dir, "bow-profile.json")
	if err := os.WriteFile(input, profileJSON, 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(node, harness, asset, input)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("the renderer could not draw this outline: %v %s", err, stderr.String())
	}
	var drawn [][3]float64
	if err := json.Unmarshal(out, &drawn); err != nil {
		t.Fatal(err)
	}

	built := geometry.FlattenOutlineForTest(profile)
	if len(built) == 0 {
		t.Fatal("the exporter drew nothing for this outline")
	}
	if len(drawn) != len(built) {
		t.Fatalf("the renderer drew %d points and the exporter %d.\n"+
			"Most often this is the arc being stepped at a different fineness in the two, "+
			"which shows a shape whose file is a different one.\ndrawn: %v\nbuilt: %v",
			len(drawn), len(built), drawn, built)
	}
	for i := range drawn {
		for j := 0; j < 3; j++ {
			if math.Abs(drawn[i][j]-built[i][j]) > 1e-9 {
				t.Fatalf("point %d differs: drawn %v, exported %v.\n"+
					"The commonest cause is the arc going the OTHER way round — two arcs join "+
					"any two points through a circle, and the one that misses the via has the "+
					"same endpoints and the same radius.", i, drawn[i], built[i])
			}
		}
	}

	// And the solid still gets built from it, so this cannot pass by both sides
	// agreeing on a drawing nothing can use.
	doc := geometry.Document{Name: "bowed", Units: "mm", Parts: []geometry.Part{{
		ID: "p", Shape: "extrusion", Profile: profile,
		Size: map[string]float64{"depth": 5}}}}
	if len(geometry.Tessellate(doc, geometry.Millimetre).Triangles()) == 0 {
		t.Error("the outline flattened identically in both and then built nothing")
	}
}

// The viewport and the exporter must nest a section the same way (wave 30).
//
// # What is being held
//
// A loop inside a hole is an ISLAND — solid material standing in a void — and
// the reading is parity: contained in an odd number of others is a void, in an
// even number is solid. Both implementations decide it independently, and if
// they disagree the picture shows a hole where the file has a post, or the other
// way round. That is invisible in a silhouette and wrong in every file.
//
// Compared by VOLUME rather than facet for facet, for the reason
// compareBowedDrawing gives: two ear-clippings of the same polygon are the same
// surface, and a facet comparison over a concave cap asserts an implementation
// detail. A wrong nesting cannot hide inside the volume — the plate below is
// 12000 mm³ read correctly and 10000 with the island cut away.
func TestRendererNestsTheSameSectionAsTheExporter(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node on PATH; skipping the renderer/exporter nesting comparison")
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
	harness := filepath.Join(dir, "nest.js")
	script := `
      const fs = require('fs'), vm = require('vm');
      const sandbox = { window: {}, console };
      vm.createContext(sandbox);
      vm.runInContext(fs.readFileSync(process.argv[2], 'utf8'), sandbox);
      const part = JSON.parse(fs.readFileSync(process.argv[3], 'utf8'));
      const built = sandbox.window.Forge3D.buildGeometry(part);
      const g = built.geo;
      let p = [];
      if (g.indices) {
        for (const i of g.indices) p.push(g.positions[i*3], g.positions[i*3+1], g.positions[i*3+2]);
      } else { p = g.positions; }
      // The divergence theorem, which is what tells a post from a hole.
      let v = 0;
      for (let i = 0; i < p.length; i += 9) {
        const a = [p[i],p[i+1],p[i+2]], b = [p[i+3],p[i+4],p[i+5]], c = [p[i+6],p[i+7],p[i+8]];
        v += (a[0]*(b[1]*c[2]-c[1]*b[2]) - a[1]*(b[0]*c[2]-c[0]*b[2]) + a[2]*(b[0]*c[1]-c[0]*b[1]))/6;
      }
      process.stdout.write(JSON.stringify({volume: v, facets: p.length/9}));
    `
	if err := os.WriteFile(harness, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}

	sq := func(half float64) []geometry.Point {
		return []geometry.Point{{X: -half, Y: -half}, {X: half, Y: -half},
			{X: half, Y: half}, {X: -half, Y: half}}
	}
	for _, tc := range []struct {
		name  string
		holes [][]geometry.Point
		want  float64
	}{
		{"a plain pocket", [][]geometry.Point{sq(20)}, (60*60 - 40*40) * 5},
		{"a post in the pocket", [][]geometry.Point{sq(20), sq(10)}, (60*60 - 40*40 + 20*20) * 5},
		{"a bore through the post", [][]geometry.Point{sq(20), sq(10), sq(4)},
			(60*60 - 40*40 + 20*20 - 8*8) * 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			part := map[string]any{"shape": "extrusion", "profile": sq(30),
				"holes": tc.holes, "size": map[string]float64{"depth": 5}}
			partJSON, err := json.Marshal(part)
			if err != nil {
				t.Fatal(err)
			}
			input := filepath.Join(dir, "nest-part.json")
			if err := os.WriteFile(input, partJSON, 0o600); err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			cmd := exec.Command(node, harness, asset, input)
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("the renderer could not build this section: %v %s", err, stderr.String())
			}
			var drawn struct {
				Volume float64 `json:"volume"`
				Facets int     `json:"facets"`
			}
			if err := json.Unmarshal(out, &drawn); err != nil {
				t.Fatal(err)
			}
			if math.Abs(drawn.Volume-tc.want) > 0.01 {
				t.Errorf("the VIEWPORT encloses %.1f mm³, want %.1f. A smaller figure means "+
					"the island was drawn as a hole; a negative one means its wall faces "+
					"into the material.", drawn.Volume, tc.want)
			}
			doc := geometry.Document{Name: "n", Units: "mm", Parts: []geometry.Part{{
				ID: "p", Shape: "extrusion", Profile: sq(30), Holes: tc.holes,
				Size: map[string]float64{"depth": 5}}}}
			var exported float64
			for _, tr := range geometry.Tessellate(doc, geometry.Millimetre).Triangles() {
				exported += (tr.A[0]*(tr.B[1]*tr.C[2]-tr.C[1]*tr.B[2]) -
					tr.A[1]*(tr.B[0]*tr.C[2]-tr.C[0]*tr.B[2]) +
					tr.A[2]*(tr.B[0]*tr.C[1]-tr.C[0]*tr.B[1])) / 6
			}
			if math.Abs(exported-tc.want) > 0.01 {
				t.Errorf("the EXPORTER encloses %.1f mm³, want %.1f", exported, tc.want)
			}
		})
	}
}
