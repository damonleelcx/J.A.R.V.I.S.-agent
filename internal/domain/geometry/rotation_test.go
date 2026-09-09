package geometry_test

import (
	"encoding/json"
	"math"
	"os/exec"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// A quarter turn is 90, because that is what a model writes.
//
// # What this closes
//
// The document's rotation was in radians and nothing said so. A model asked for
// a wheel writes [0, 0, 90] meaning a quarter turn; read as radians that is
// 116.8 degrees, so the wheels on the live sports car sat tilted 27 degrees off
// vertical. Every non-zero rotation ever stored in this deployment — eight
// values, 2026-09-09 — was that same [0, 0, 90], and not one meant radians.
//
// See docs/bugfix/2026-09-09-the-body-used-its-length-twice.md
func TestAQuarterTurnIsNinety(t *testing.T) {
	p := geometry.Part{ID: "wheel", Shape: "cylinder", Rotation: []float64{0, 0, 90}}
	got := p.RotationRadians()
	if math.Abs(got[2]-math.Pi/2) > 1e-9 {
		t.Errorf("rotation 90 became %v rad (%.1f deg). A model writing 90 for a wheel means "+
			"a quarter turn; read as radians it is 116.8 deg and the wheel is visibly tilted",
			got[2], got[2]*180/math.Pi)
	}

	// It must reach the geometry, not merely the helper: a cylinder's axis is
	// local Y, so a quarter turn about Z lays it across X — which is what makes
	// it a wheel rather than a roller standing on end.
	doc := geometry.Document{Name: "w", Units: "mm", Parts: []geometry.Part{{
		ID: "wheel", Name: "Wheel", Shape: "cylinder",
		Size:     map[string]float64{"radius": 350, "depth": 250},
		Rotation: []float64{0, 0, 90},
	}}}
	ext := extentOf(t, doc, "wheel")
	// Across X it is as long as the cylinder (250); up Y and along Z it is the
	// diameter (700). Tolerance is the tessellation's, not the rotation's.
	if math.Abs(ext[0]-250) > 5 || math.Abs(ext[1]-700) > 5 {
		t.Errorf("the turned wheel measures %.0f x %.0f x %.0f mm; a quarter turn about Z "+
			"should lay a d700 x 250 cylinder across X. Anything else is the wrong angle",
			ext[0], ext[1], ext[2])
	}
}

// The renderer turns a part by the same angle the builder does.
//
// Not a string check: the two implementations are compared over real angles.
// A picture and an exported file that disagree about where a part points is the
// one failure a downloaded file cannot be labelled out of, and it is invisible
// on screen — the render looks like a design decision.
func TestTheRendererTurnsDegreesLikeTheBuilderDoes(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not on PATH, so the viewport half of this cannot be run")
	}
	const script = `
	  const fs = require('fs');
	  globalThis.window = globalThis;
	  globalThis.document = { getElementById: () => null, querySelector: () => null,
	    querySelectorAll: () => [], addEventListener(){},
	    createElement: () => ({ style:{}, classList:{add(){},remove(){}}, getContext: () => null }),
	    body:{appendChild(){},classList:{add(){},remove(){}}},
	    documentElement:{classList:{add(){},remove(){},contains:()=>false},setAttribute(){},getAttribute:()=>null,style:{}} };
	  globalThis.localStorage = { getItem:()=>null, setItem(){}, removeItem(){} };
	  globalThis.addEventListener = () => {};
	  globalThis.requestAnimationFrame = () => 0;
	  globalThis.matchMedia = () => ({matches:false, addEventListener(){}, addListener(){}});
	  new Function(fs.readFileSync(process.argv[1], 'utf8'))();
	  if (!globalThis.Forge3D || typeof globalThis.Forge3D.rotationRadians !== 'function') {
	    console.log('MISSING'); process.exit(0);
	  }
	  console.log(JSON.stringify(JSON.parse(process.argv[2]).map(r => globalThis.Forge3D.rotationRadians(r))));
	`
	cases := [][]float64{{0, 0, 0}, {0, 0, 90}, {0, 90, 0}, {90, 0, 0}, {30, -45, 180}, {0, 0, 360}}
	in, _ := json.Marshal(cases)
	out, err := exec.Command("node", "-e", script, "--",
		"../../httpapi/assets/forge3d.js", string(in)).CombinedOutput()
	if err != nil {
		t.Fatalf("running the viewport: %v\n%s", err, out)
	}
	text := strings.TrimSpace(string(out))
	if text == "MISSING" {
		t.Fatal("the viewport does not export rotationRadians, so it is turning parts by " +
			"whatever unit the document happens to carry while the builder turns them by " +
			"degrees. The render and the exported file will disagree.")
	}
	var got [][]float64
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("viewport said %q: %v", text, err)
	}
	for i, c := range cases {
		want := geometry.Part{Rotation: c}.RotationRadians()
		for a := 0; a < 3; a++ {
			if math.Abs(got[i][a]-want[a]) > 1e-9 {
				t.Errorf("rotation %v axis %d: viewport %v, builder %v", c, a, got[i][a], want[a])
			}
		}
	}
}

func extentOf(t *testing.T, d geometry.Document, id string) [3]float64 {
	t.Helper()
	m := geometry.Tessellate(d, geometry.Millimetre)
	lo := [3]float64{math.Inf(1), math.Inf(1), math.Inf(1)}
	hi := [3]float64{math.Inf(-1), math.Inf(-1), math.Inf(-1)}
	seen := false
	for _, g := range m.Groups {
		if g.PartID != id {
			continue
		}
		for _, tr := range g.Triangles {
			for _, v := range [3][3]float64{tr.A, tr.B, tr.C} {
				for i := 0; i < 3; i++ {
					lo[i] = math.Min(lo[i], v[i])
					hi[i] = math.Max(hi[i], v[i])
				}
			}
			seen = true
		}
	}
	if !seen {
		t.Fatalf("no triangles for %q", id)
	}
	return [3]float64{hi[0] - lo[0], hi[1] - lo[1], hi[2] - lo[2]}
}
