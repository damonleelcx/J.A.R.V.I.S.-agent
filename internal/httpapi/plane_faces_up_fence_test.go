package httpapi

import (
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// The renderer draws a plane facing UP, and winds it that way too.
//
// The convention and the reasons for it are on `func plane` in
// internal/domain/geometry/mesh.go. This is its fence in the browser; the other
// two are TestAPlaneFacesUp (geometry, the exporter) and TestKernel_APlaneFacesUp
// (internal/domain/cad, the real build123d kernel).
//
// # Why the WINDING is the part that mattered here
//
// The declared normals were already +Y. The indices were not: they ran
// 0,1,2 / 0,2,3 over corners whose cross product is -Y, so every plane the browser
// drew was wound face-down while its normals claimed face-up. The model pass runs
// with gl.enable(CULL_FACE) and gl.frontFace(gl.CCW), so the rasteriser believed
// the winding and threw the plane away whenever the camera was above it — which is
// where the default and "top" cameras are, and where anyone looking at a ground or
// datum plane is. It was visible only from underneath, lit with a normal pointing
// away from the viewer.
//
// Unlike the Go exporter there is no orient() here to repair a facet whose winding
// disagrees with its normal, so what is written is what is drawn, and the only
// thing that can hold it is a test that multiplies the corners out.
//
// # Driven, not read
//
// Through F.buildGeometry — the real dispatch — rather than by reading
// planeGeometry, and then compared FACET FOR FACET with what the Go exporter
// tessellates for the same part. A renderer that quietly stopped agreeing with the
// exporter is the same defect one level along, and the number that catches it is
// the one below.
func TestTheRendererDrawsAPlaneFacingUp(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node on PATH; skipping the renderer plane comparison")
	}
	src, err := assetFS.ReadFile("assets/forge3d.js")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	asset, harness := filepath.Join(dir, "forge3d.js"), filepath.Join(dir, "run.js")
	if err := os.WriteFile(asset, src, 0o600); err != nil {
		t.Fatal(err)
	}
	script := `
      const fs = require('fs'), vm = require('vm');
      const sandbox = { window: {}, console };
      vm.createContext(sandbox);
      vm.runInContext(fs.readFileSync(process.argv[2], 'utf8'), sandbox);
      const F = sandbox.window.Forge3D;
      const built = F.buildGeometry({ shape: 'plane', size: { width: 10, depth: 4 } });
      const g = built.geo, p = g.positions, idx = g.indices, out = [];
      for (let i = 0; i < idx.length; i += 3) {
        const c = [idx[i], idx[i + 1], idx[i + 2]];
        out.push({
          corners: c.map(k => [p[k * 3], p[k * 3 + 1], p[k * 3 + 2]]),
          normals: c.map(k => [g.normals[k * 3], g.normals[k * 3 + 1], g.normals[k * 3 + 2]])
        });
      }
      process.stdout.write(JSON.stringify({ triangles: out, approximated: built.approximated || '' }));
    `
	if err := os.WriteFile(harness, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, harness, asset).Output()
	if err != nil {
		t.Fatalf("the renderer could not be driven: %v", err)
	}
	var got struct {
		Triangles []struct {
			Corners [][]float64 `json:"corners"`
			Normals [][]float64 `json:"normals"`
		} `json:"triangles"`
		Approximated string `json:"approximated"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("reading the renderer's answer: %v\n%s", err, out)
	}
	if got.Approximated != "" {
		t.Fatalf("the renderer did not draw a plane as a plane: %q", got.Approximated)
	}
	if len(got.Triangles) != 2 {
		t.Fatalf("%d triangle(s), want the 2 a rectangle is cut into", len(got.Triangles))
	}

	for i, tr := range got.Triangles {
		for j, n := range tr.Normals {
			if len(n) != 3 || n[0] != 0 || n[1] != 1 || n[2] != 0 {
				t.Errorf("triangle %d, corner %d: normal %v, want [0 1 0]. A plane is "+
					"one-sided and faces UP (internal/domain/geometry/mesh.go, func plane).",
					i, j, n)
			}
		}
		a, b, c := tr.Corners[0], tr.Corners[1], tr.Corners[2]
		ab := [3]float64{b[0] - a[0], b[1] - a[1], b[2] - a[2]}
		ac := [3]float64{c[0] - a[0], c[1] - a[1], c[2] - a[2]}
		cross := [3]float64{
			ab[1]*ac[2] - ab[2]*ac[1],
			ab[2]*ac[0] - ab[0]*ac[2],
			ab[0]*ac[1] - ab[1]*ac[0],
		}
		if cross[1] <= 0 || math.Abs(cross[0]) > 1e-9 || math.Abs(cross[2]) > 1e-9 {
			t.Errorf("triangle %d is wound %v, which does not point +Y. The model pass runs "+
				"with CULL_FACE on and frontFace CCW, so a plane wound the other way is not "+
				"drawn at all when it is looked at from above — the view a ground or datum "+
				"plane exists for. There is no orient() here to repair it.", i, cross)
		}
	}

	// Facet for facet against what the exporter puts in an STL for the same part,
	// so the two copies of the convention cannot drift apart while each stays
	// internally tidy.
	doc := geometry.Document{Name: "sheet", Units: "mm", Parts: []geometry.Part{{
		ID: "sheet", Name: "Sheet", Shape: "plane",
		Size:     map[string]float64{"width": 10, "depth": 4},
		Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0},
	}}}
	var want []string
	for _, g := range geometry.Tessellate(doc, geometry.Millimetre).Groups {
		if g.Shape != "plane" {
			continue
		}
		for _, tr := range g.Triangles {
			want = append(want, facetKey(tr.A, tr.B, tr.C))
		}
	}
	if len(want) == 0 {
		t.Fatal("the exporter tessellated no plane; there was nothing to compare against")
	}
	var have []string
	for _, tr := range got.Triangles {
		have = append(have, facetKey(
			[3]float64{tr.Corners[0][0], tr.Corners[0][1], tr.Corners[0][2]},
			[3]float64{tr.Corners[1][0], tr.Corners[1][1], tr.Corners[1][2]},
			[3]float64{tr.Corners[2][0], tr.Corners[2][1], tr.Corners[2][2]}))
	}
	sort.Strings(want)
	sort.Strings(have)
	for i := range want {
		if i >= len(have) || want[i] != have[i] {
			t.Fatalf("the renderer's plane is not the exporter's:\n  renderer %v\n  exporter %v",
				have, want)
		}
	}
}

// facetKey names a triangle by its corners in the order they are wound, started at
// the lexicographically smallest one. Two facets have the same key when they are
// the same triangle wound the same way round, whichever corner each was written
// from — the comparison this fence wants, and not one that a rotation of the same
// three corners would fail.
func facetKey(a, b, c [3]float64) string {
	corners := [3][3]float64{a, b, c}
	at := func(v [3]float64) string {
		body, _ := json.Marshal([]float64{round12(v[0]), round12(v[1]), round12(v[2])})
		return string(body)
	}
	first := 0
	for i := 1; i < 3; i++ {
		if at(corners[i]) < at(corners[first]) {
			first = i
		}
	}
	return at(corners[first]) + at(corners[(first+1)%3]) + at(corners[(first+2)%3])
}

func round12(v float64) float64 {
	r := math.Round(v*1e9) / 1e9
	if r == 0 {
		return 0 // -0 and 0 are the same corner
	}
	return r
}
