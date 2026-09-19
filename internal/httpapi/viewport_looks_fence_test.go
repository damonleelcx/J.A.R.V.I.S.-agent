package httpapi

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// The viewport's presentation: light, materials, grounding, the camera. 2026-09-18.
//
// # What changed that needs holding
//
// damon, 2026-09-18: "looks designed" is a FORGE goal. forge3d.js lit every part with one
// directional light and Blinn-Phong, straight to the screen, over a grid: no reflections,
// no tone mapping, no shadow, no occlusion. It now lights from a studio environment with a
// metal/roughness material per finish, tone maps and writes sRGB, lays a contact shadow,
// darkens creases with screen-space occlusion (WebGL2), draws a studio backdrop from a
// three-quarter hero view, draws curves as finely as the screen needs, smooths curved
// facets and can draw feature lines. Its clip planes follow the model's size — the fixed
// 0.05 near plane is what streaked the wheel faces of a car in millimetres
// (docs/bugfix/2026-09-18-wheel-faces-streaked-because-the-near-plane-ignored-the-models-size.md).
//
// None of it changes a shape, a placement or an export. These fences hold the rules that
// can be read without a GPU — the tables and curves the shaders are written from, which
// pass runs when, what reaches the screen — through the shipped forge3d.js and
// scripts/webgl-stub.js on all three paths. What a frame LOOKS like is in
// docs/spikes/2026-09-18-presentation (screenshots in both themes, before and after).

const looksHarness = `
  const fs = require('fs');
  const stub = require(process.argv[2]);
  const F = stub.loadForge3D(process.argv[3]);
  const input = JSON.parse(fs.readFileSync(process.argv[4], 'utf8'));
  const P = F.presentation;
  if (!P) { process.stdout.write('{"missing":true}'); return; }
  const out = {};
  function studioFor(mode, spec) {
    const made = stub.makeCanvas(mode, 640, 480);
    const studio = new F.Studio(made.canvas, { onError() {} });
    if (spec) studio.load(spec);
    return { studio, record: made.record };
  }
  function frame(s) {
    s.record.reset();
    s.studio.draw();
    const r = s.record;
    return { stats: Object.assign({}, s.studio.stats), problems: r.problems.slice(), sequence: r.sequence.slice(),
      draws: r.draws.map((d) => ({ elements: d.elements, instances: d.instances.length })),
      lines: r.lineInstanceDraws.map((d) => ({ elements: d.elements, instances: d.instances.length })),
      uniforms: r.uniforms, resolves: r.resolves, near: s.studio._frame.near, far: s.studio._frame.far,
      proj: Array.from(s.studio._frame.proj), distance: s.studio.camera.distance };
  }
  switch (input.case) {
    case 'materials': {
      out.names = Object.keys(P.materials);
      out.table = P.materials;
      out.unknown = P.shadingFor({ finish: 'velvet' });
      out.none = P.shadingFor(null);
      out.frames = {};
      for (const mode of ['webgl2', 'webgl1', 'webgl1-noext']) {
        const s = studioFor(mode, input.spec);
        out.frames[mode] = frame(s);
      }
      break;
    }
    case 'tone': {
      out.aces = P.aces;
      out.curve = input.xs.map((x) => P.acesFilm(x));
      out.toSrgb = input.xs.map((x) => P.linearToSrgb(Math.min(1, x)));
      out.roundTrip = input.xs.map((x) => P.srgbToLinear(P.linearToSrgb(Math.min(1, x))));
      out.vert = P.shaders.part[0];
      out.frag = P.shaders.part[1];
      break;
    }
    case 'clip': {
      out.cases = [];
      let seed = 20260918;
      const rnd = () => (seed = (seed * 1103515245 + 12345) % 2147483648) / 2147483648;
      for (const span of input.spans) {
        const bounds = { min: [-span / 2, 0, -span * 0.2], max: [span / 2, span * 0.3, span * 0.2], span: span,
                         centre: [0, span * 0.15, 0] };
        for (let k = 0; k < input.cameras; k++) {
          const d = span * (0.02 + Math.pow(rnd(), 2) * 300);
          const yaw = rnd() * 6.283, pitch = rnd() * 3 - 1.5;
          const target = bounds.centre.map((c) => c + (rnd() - 0.5) * span * 0.5);
          const dir = [Math.cos(pitch) * Math.sin(yaw), Math.sin(pitch), Math.cos(pitch) * Math.cos(yaw)];
          const eye = target.map((t, i) => t + dir[i] * d);
          const c = P.clipPlanes(eye, target, bounds, 0);
          out.cases.push({ span, distance: d, eye, target, near: c.near, far: c.far, bounds });
        }
      }
      const s = studioFor('webgl2', input.spec);
      out.car = frame(s);
      out.carSpan = s.studio.bounds.span;
      break;
    }
    case 'zoom': {
      const s = studioFor('webgl2', input.spec);
      out.span = s.studio.bounds.span;
      out.framed = s.studio.camera.distance;
      s.studio.zoomBy(1.0013);
      out.oneTick = s.studio.camera.distance;
      for (let i = 0; i < 400; i++) s.studio.zoomBy(0.9);
      out.nearest = s.studio.camera.distance;
      for (let i = 0; i < 400; i++) s.studio.zoomBy(1.1);
      out.farthest = s.studio.camera.distance;
      out.limits = P.zoomLimits(out.span);
      break;
    }
    case 'detail': {
      out.table = input.pixels.map((px) => P.detailFor(px, 40));
      out.frames = [];
      const s = studioFor('webgl2', input.spec);
      const b = s.studio.batches[0];
      out.base = b.count;
      out.baseVertices = b.geo.positions.length / 3;
      for (const px of input.pixels) {
        s.studio.camera.target = b.bounds.centre.slice();
        s.studio.camera.distance = b.radius[0] * s.studio._frame.pixels / px;
        const f = frame(s);
        out.frames.push({ px, elements: f.draws.map((d) => d.elements), detail: b.detail, problems: f.problems });
      }
      out.pickGeometry = b.geo.indices.length;
      break;
    }
    case 'shapes': {
      out.shapes = {};
      for (const name of Object.keys(input.shapes)) {
        const g = F.buildGeometry(input.shapes[name]).geo;
        out.shapes[name] = { positions: Array.from(g.positions), normals: Array.from(g.normals), indices: Array.from(g.indices) };
      }
      out.crease = P.creaseDegrees;
      break;
    }
    case 'shadow': {
      out.frames = {};
      for (const mode of ['webgl2', 'webgl1', 'webgl1-noext']) {
        const s = studioFor(mode, null);
        s.record.reset();
        s.studio.load(input.spec);
        const loaded = { stats: Object.assign({}, s.studio.stats), sequence: s.record.sequence.slice(), problems: s.record.problems.slice() };
        s.studio.camera.yaw += 0.7;
        const turned = frame(s);
        s.studio.setExplode(0.4);
        const exploded = { stats: Object.assign({}, s.studio.stats) };
        s.studio.setContactShadow(false);
        const off = frame(s);
        out.frames[mode] = { loaded, turned, exploded, off };
      }
      break;
    }
    case 'passes': {
      out.frames = {};
      for (const mode of ['webgl2', 'webgl1', 'webgl1-noext']) {
        const s = studioFor(mode, input.spec);
        const on = frame(s);
        s.studio.setAmbientOcclusion(false);
        const off = frame(s);
        s.studio.setFeatureLines(true);
        const lines = frame(s);
        out.frames[mode] = { on, off, lines, programs: s.record.programs.length };
      }
      out.hero = P.hero;
      const s = studioFor('webgl2', input.spec);
      out.camera = { yaw: s.studio.camera.yaw, pitch: s.studio.camera.pitch };
      s.studio.camera.yaw = 2; s.studio.camera.pitch = -1;
      s.studio.resetView();
      out.reset = { yaw: s.studio.camera.yaw, pitch: s.studio.camera.pitch };
      break;
    }
  }
  process.stdout.write(JSON.stringify(out));
`

type looksFrame struct {
	Stats struct {
		Path                                        string
		Post                                        string
		Shadow                                      bool
		DrawCalls, Instances, ShadowPass, EdgeDraws int
		Finer                                       int
		Translucent                                 int
	}
	Problems []string
	Sequence []string
	Draws    []struct{ Elements, Instances int }
	Lines    []struct{ Elements, Instances int }
	Uniforms map[string][]float64
	Resolves int
	Near     float64
	Far      float64
	Proj     []float64
	Distance float64
}

func runLooks(t *testing.T, input map[string]any, out any) {
	t.Helper()
	raw := runNodeHarness(t, looksHarness, input)
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("unreadable renderer output %.300q: %v", raw, err)
	}
}

// presentationCar is the fixed car of docs/spikes/2026-09-18-presentation: a body in
// millimetres, a see-through glasshouse and wheels whose rim sits 2.5 mm proud of its tyre.
func presentationCar(t *testing.T) geometry.Document {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "spikes", "2026-09-18-presentation", "data", "car.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc geometry.Document
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func noLooksProblems(t *testing.T, label string, problems []string) {
	t.Helper()
	if len(problems) > 0 {
		t.Fatalf("%s: the stub refused %v", label, problems)
	}
}

// Every finish Go accepts has a material, and the table orders them the way the model
// contract describes them: glass the sharpest highlight, then metal's "hard, tight" one,
// painted's "soft sheen", plastic's "broad, dull" one, rubber "almost none". Metal is the
// one conductor; paint carries a clear coat. A part's finish reaches the shader as those
// three numbers on every path, and an unknown finish draws as unfinished.
func TestRendererHasAMaterialForEveryFinishGoAccepts(t *testing.T) {
	part := func(id, finish string, x float64) geometry.Part {
		return geometry.Part{ID: id, Shape: "box", Size: map[string]float64{"width": 1, "height": 1, "depth": 1},
			Position: []float64{x, 0, 0}, Color: "#888888", Opacity: 1, Material: &geometry.Material{Name: id, Finish: geometry.Finish(finish)}}
	}
	doc := geometry.Document{Name: "finishes", Units: "mm", Parts: []geometry.Part{part("m", "metal", 0)}}
	var got struct {
		Names         []string
		Table         map[string]struct{ Metallic, Roughness, Coat float64 }
		Unknown, None []float64
		Frames        map[string]looksFrame
	}
	runLooks(t, map[string]any{"case": "materials", "spec": doc}, &got)
	names := append([]string(nil), got.Names...)
	want := geometry.FinishNames()
	sort.Strings(names)
	sort.Strings(want)
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("the renderer's materials are %v; Go accepts %v — every finish needs exactly one material", names, want)
	}
	order := []string{"glass", "metal", "painted", "plastic", "rubber"}
	for i := 1; i < len(order); i++ {
		if !(got.Table[order[i-1]].Roughness < got.Table[order[i]].Roughness) {
			t.Errorf("%s (roughness %v) must be sharper than %s (%v), as the contract describes them",
				order[i-1], got.Table[order[i-1]].Roughness, order[i], got.Table[order[i]].Roughness)
		}
	}
	for name, m := range got.Table {
		if (name == "metal") != (m.Metallic == 1) || m.Roughness <= 0 || m.Roughness > 1 {
			t.Errorf("%s is metallic %v, roughness %v; only metal is a conductor, and roughness is in (0, 1]", name, m.Metallic, m.Roughness)
		}
	}
	if !(got.Table["painted"].Coat > 0) {
		t.Errorf("painted has no clear coat")
	}
	un := got.Table["unfinished"]
	for label, s := range map[string][]float64{"an unknown finish": got.Unknown, "no material": got.None} {
		if len(s) != 3 || s[0] != un.Metallic || s[1] != un.Roughness || s[2] != un.Coat {
			t.Errorf("%s shades as %v; want unfinished %v", label, s, un)
		}
	}
	metal := got.Table["metal"]
	for mode, f := range got.Frames {
		noLooksProblems(t, mode, f.Problems)
		u := f.Uniforms["uMaterial"]
		if len(u) != 3 || u[0] != metal.Metallic || u[1] != metal.Roughness || u[2] != metal.Coat {
			t.Errorf("%s: a metal part is drawn with uMaterial %v; want %v", mode, u, metal)
		}
	}
}

// The tone curve is ACES filmic from one table, and the part shader is written from the
// same numbers: colours go sRGB → linear per vertex, are lit in linear, and come out
// through aces() then the exact sRGB transfer — not a 2.2 power, and not raw.
func TestRendererToneMapsWithTheCurveItsShaderIsWrittenFrom(t *testing.T) {
	xs := []float64{0, 0.01, 0.05, 0.18, 0.5, 1, 2, 4, 8, 16}
	var got struct {
		Aces                     struct{ A, B, C, D, E float64 }
		Curve, ToSrgb, RoundTrip []float64
		Vert, Frag               string
	}
	runLooks(t, map[string]any{"case": "tone", "xs": xs}, &got)
	a := got.Aces
	for i, x := range xs {
		want := math.Max(0, math.Min(1, x*(a.A*x+a.B)/(x*(a.C*x+a.D)+a.E)))
		if math.Abs(got.Curve[i]-want) > 1e-12 {
			t.Errorf("acesFilm(%v) = %v; the table's curve gives %v", x, got.Curve[i], want)
		}
		if i > 0 && !(got.Curve[i] > got.Curve[i-1]) && got.Curve[i] < 1 {
			t.Errorf("the tone curve is not increasing at %v", x)
		}
	}
	if got.Curve[0] != 0 || got.Curve[len(xs)-1] < 0.97 || got.Curve[len(xs)-1] > 1 || got.Curve[3] < 0.2 || got.Curve[3] > 0.35 {
		t.Errorf("the tone curve maps 0 → %v, mid-grey 0.18 → %v, 16 → %v; want 0, a mid-grey near 0.27, and a shoulder below 1",
			got.Curve[0], got.Curve[3], got.Curve[len(xs)-1])
	}
	srgb := func(c float64) float64 {
		if c <= 0.0031308 {
			return 12.92 * c
		}
		return 1.055*math.Pow(c, 1/2.4) - 0.055
	}
	for i, x := range xs {
		c := math.Min(1, x)
		if math.Abs(got.ToSrgb[i]-srgb(c)) > 1e-12 || math.Abs(got.RoundTrip[i]-c) > 1e-9 {
			t.Errorf("sRGB at %v: %v (want %v), round trip %v", c, got.ToSrgb[i], srgb(c), got.RoundTrip[i])
		}
	}
	curve := "(x * (" + num(a.A) + " * x + " + num(a.B) + ")) / (x * (" + num(a.C) + " * x + " + num(a.D) + ") + " + num(a.E) + ")"
	for label, ok := range map[string]bool{
		"the part shader applies the table's ACES curve":     strings.Contains(got.Frag, curve),
		"the part shader tone maps before it writes sRGB":    strings.Contains(got.Frag, "toSrgb(aces("),
		"the part shader writes the exact sRGB curve":        strings.Contains(got.Frag, "step(0.0031308, c)") && strings.Contains(got.Frag, "1.0 / 2.4"),
		"the vertex shader takes the colour from sRGB first": strings.Contains(got.Vert, "vColour = vec4(toLinear(aColour.rgb)") && strings.Contains(got.Vert, "step(0.04045, c)"),
	} {
		if !ok {
			t.Errorf("%s: it does not", label)
		}
	}
}

func num(v float64) string {
	b, _ := json.Marshal(v)
	s := string(b)
	if !strings.ContainsAny(s, ".e") {
		s += ".0"
	}
	return s
}

// depthStep is the smallest depth difference a 24-bit buffer resolves at view distance z
// under a perspective projection with these planes.
func depthStep(z, near, far float64) float64 {
	return z * z * (far - near) / (far * near * float64(1<<24))
}

// The clip planes follow the model: on random cameras round models from 1 to 100,000 units
// across, nothing of the model's box is cut by either plane when the eye is outside it,
// and at the distance of what the camera looks at a 24-bit depth buffer resolves a
// ten-thousandth of the model. The fixed 0.05 near plane resolved ~140 mm on a car in
// millimetres framed whole — the streaks on its wheel faces.
func TestRendererDepthResolvesTheModelAtAnyScale(t *testing.T) {
	var got struct {
		Cases []struct {
			Span, Distance, Near, Far float64
			Eye, Target               []float64
			Bounds                    struct{ Min, Max []float64 }
		}
		Car     looksFrame
		CarSpan float64
	}
	runLooks(t, map[string]any{"case": "clip", "spans": []float64{1, 90, 4550, 100000}, "cameras": 150,
		"spec": presentationCar(t)}, &got)
	if len(got.Cases) != 600 {
		t.Fatalf("%d cameras checked", len(got.Cases))
	}
	bad := 0
	for _, c := range got.Cases {
		sub := func(a, b []float64) []float64 { return []float64{a[0] - b[0], a[1] - b[1], a[2] - b[2]} }
		dot := func(a, b []float64) float64 { return a[0]*b[0] + a[1]*b[1] + a[2]*b[2] }
		look := sub(c.Target, c.Eye)
		l := math.Sqrt(dot(look, look))
		look = []float64{look[0] / l, look[1] / l, look[2] / l}
		inside := true
		for k := 0; k < 3; k++ {
			if c.Eye[k] < c.Bounds.Min[k] || c.Eye[k] > c.Bounds.Max[k] {
				inside = false
			}
		}
		msg := ""
		if !(c.Near > 0) || !(c.Far > c.Near) {
			msg = "planes out of order"
		}
		if step := depthStep(c.Distance, c.Near, c.Far); step > c.Span*1e-4 {
			msg = "depth step " + num(step) + " at the target"
		}
		for i := 0; i < 8 && msg == ""; i++ {
			corner := []float64{c.Bounds.Min[0], c.Bounds.Min[1], c.Bounds.Min[2]}
			for k := 0; k < 3; k++ {
				if i>>k&1 == 1 {
					corner[k] = c.Bounds.Max[k]
				}
			}
			z := dot(sub(corner, c.Eye), look)
			if z > c.Far || (!inside && z > 0 && z < c.Near) {
				msg = "a corner of the model at depth " + num(z) + " is clipped"
			}
		}
		if msg != "" {
			bad++
			if bad <= 3 {
				t.Errorf("span %v, camera %v away: near %v far %v — %s", c.Span, c.Distance, c.Near, c.Far, msg)
			}
		}
	}
	if bad > 0 {
		t.Errorf("%d of %d cameras clip the model or cannot resolve it", bad, len(got.Cases))
	}
	car := got.Car
	noLooksProblems(t, "car", car.Problems)
	if step := depthStep(car.Distance, car.Near, car.Far); step > 0.5 || car.Near <= 0.05 {
		t.Errorf("the car (%v mm across) framed whole draws with near %v, far %v: its depth resolves %v mm at the model, "+
			"and its rims sit 2.5 mm proud of the tyres", got.CarSpan, car.Near, car.Far, step)
	}
	// The projection the frame used is the one those planes make.
	if p := car.Proj; len(p) != 16 || math.Abs(p[14]-2*car.Far*car.Near/(car.Near-car.Far)) > 1e-6*math.Abs(p[14]) {
		t.Errorf("the frame's projection %v was not made from near %v, far %v", p, car.Near, car.Far)
	}
}

// Zooming is limited in the model's own terms: one wheel tick on a car in millimetres moves
// the camera by the tick, not to the 400 units the old limit allowed (inside the body).
func TestRendererZoomsInTheModelsOwnUnits(t *testing.T) {
	var got struct {
		Span, Framed, OneTick, Nearest, Farthest float64
		Limits                                   struct{ Min, Max float64 }
	}
	runLooks(t, map[string]any{"case": "zoom", "spec": presentationCar(t)}, &got)
	if math.Abs(got.OneTick/got.Framed-1.0013) > 1e-9 {
		t.Errorf("one wheel tick took the camera from %v to %v on a car %v across", got.Framed, got.OneTick, got.Span)
	}
	if math.Abs(got.Nearest-got.Span*0.02) > 1e-6*got.Span || math.Abs(got.Farthest-got.Span*100) > 1e-6*got.Span {
		t.Errorf("zoom stops at %v and %v on a model %v across; want %v and %v", got.Nearest, got.Farthest, got.Span,
			got.Span*0.02, got.Span*100)
	}
}

// A curved primitive is drawn as finely as its largest copy on screen needs — the fewest
// of 1, 2, 4 or 8 times its export count that keeps the chord's sag under half a pixel —
// and never below the export's count; picking and the export keep the export's triangles.
func TestRendererDrawsCurvesAsFineAsTheScreenNeeds(t *testing.T) {
	pixels := []float64{30, 100, 200, 1000, 100000}
	doc := geometry.Document{Name: "wheel", Units: "mm", Parts: []geometry.Part{{ID: "wheel", Shape: "cylinder",
		Size: map[string]float64{"radius": 300, "height": 200}, Color: "#333333", Opacity: 1}}}
	var got struct {
		Table        []int
		Base         int
		BaseVertices int
		PickGeometry int
		Frames       []struct {
			Px       float64
			Elements []int
			Detail   int
			Problems []string
		}
	}
	runLooks(t, map[string]any{"case": "detail", "pixels": pixels, "spec": doc}, &got)
	radial, _, _ := geometry.TessellationCounts()
	factor := func(px float64) int {
		need := math.Pi / math.Acos(1-0.5/px)
		f := 1
		for f < 8 && float64(radial*f) < need {
			f *= 2
		}
		return f
	}
	for i, px := range pixels {
		if got.Table[i] != factor(px) {
			t.Errorf("a curve %v px across is drawn %d× finer; want %d× (the export has %d segments)", px, got.Table[i], factor(px), radial)
		}
	}
	if got.Base != radial*12 || got.PickGeometry != got.Base {
		t.Errorf("the cylinder's own geometry is %d indices (picking reads %d); the export's %d segments make %d",
			got.Base, got.PickGeometry, radial, radial*12)
	}
	// The frames place the camera by the copy's BOUNDING radius (the box round a
	// 300 × 200 cylinder: 435.9); the curve it refines is the 300 mm rim.
	bound := math.Sqrt(300*300 + 100*100 + 300*300)
	finer := 0
	for _, f := range got.Frames {
		noLooksProblems(t, "detail", f.Problems)
		want := factor(f.Px * 300 / bound)
		if want > 1 {
			finer++
		}
		if len(f.Elements) != 1 || f.Detail != want || f.Elements[0] != got.Base*want {
			t.Errorf("a cylinder %v px across is drawn with %v indices at detail %d; want detail %d (%d indices; the export's is %d)",
				f.Px, f.Elements, f.Detail, want, got.Base*want, got.Base)
		}
	}
	if finer < 2 {
		t.Errorf("only %d of the frames needed a finer level; the fixture no longer tests refinement", finer)
	}
}

type meshOut struct {
	Positions, Normals []float64
	Indices            []int
}

func (m meshOut) vertex(i int) [3]float64 {
	return [3]float64{m.Positions[i*3], m.Positions[i*3+1], m.Positions[i*3+2]}
}
func (m meshOut) normal(i int) [3]float64 {
	return [3]float64{m.Normals[i*3], m.Normals[i*3+1], m.Normals[i*3+2]}
}

// Every primitive faces outward: its triangles enclose a positive volume and each is wound
// the way its normals point. Until 2026-09-18 cylinders, cones and spheres were wound
// inside out, so with back faces culled the stage drew the INSIDE of their far wall — a
// wheel, a hub, a shaft read as an open tube.
func TestRendererPrimitivesFaceOutward(t *testing.T) {
	shapes := map[string]geometry.Part{
		"box":       {Shape: "box", Size: map[string]float64{"width": 2, "height": 2, "depth": 2}},
		"cylinder":  {Shape: "cylinder", Size: map[string]float64{"radius": 1, "height": 2}},
		"cone":      {Shape: "cone", Size: map[string]float64{"radius": 1, "height": 2}},
		"sphere":    {Shape: "sphere", Size: map[string]float64{"radius": 1}},
		"extrusion": {Shape: "extrusion", Size: map[string]float64{"depth": 2}, Profile: []geometry.Point{{X: -1, Y: -1}, {X: 1, Y: -1, Radius: 0.4}, {X: 1, Y: 1}, {X: -1, Y: 1}}},
		"revolve":   {Shape: "revolve", Profile: []geometry.Point{{X: 0.5, Y: -1}, {X: 1, Y: -1}, {X: 1, Y: 1}, {X: 0.5, Y: 1}}},
		"gear":      {Shape: "gear", Size: map[string]float64{"module": 1, "teeth": 12, "face_width": 2}},
	}
	var got struct{ Shapes map[string]meshOut }
	runLooks(t, map[string]any{"case": "shapes", "shapes": shapes}, &got)
	for name, m := range got.Shapes {
		vol, against := 0.0, 0
		for tri := 0; tri+2 < len(m.Indices); tri += 3 {
			a, b, c := m.vertex(m.Indices[tri]), m.vertex(m.Indices[tri+1]), m.vertex(m.Indices[tri+2])
			u := [3]float64{b[0] - a[0], b[1] - a[1], b[2] - a[2]}
			w := [3]float64{c[0] - a[0], c[1] - a[1], c[2] - a[2]}
			n := [3]float64{u[1]*w[2] - u[2]*w[1], u[2]*w[0] - u[0]*w[2], u[0]*w[1] - u[1]*w[0]}
			vol += (a[0]*n[0] + a[1]*n[1] + a[2]*n[2]) / 6
			if l := math.Sqrt(n[0]*n[0] + n[1]*n[1] + n[2]*n[2]); l > 1e-12 {
				g := m.normal(m.Indices[tri])
				if (n[0]*g[0]+n[1]*g[1]+n[2]*g[2])/l < 0 {
					against++
				}
			}
		}
		if vol <= 0 || against > 0 {
			t.Errorf("%s encloses a volume of %.3f and %d of its %d triangles are wound against their normals",
				name, vol, against, len(m.Indices)/3)
		}
	}
}

// A curved surface built from flat facets is shaded smooth, and an edge stays an edge: a
// revolved tube's wall normals point straight out from its axis, its end faces straight
// along it, and a square outline's walls keep their own face normals. Positions and
// triangles are what the export has.
func TestRendererShadesCurvedPrimitivesSmooth(t *testing.T) {
	shapes := map[string]geometry.Part{
		"tube":   {Shape: "revolve", Profile: []geometry.Point{{X: 5, Y: 0}, {X: 10, Y: 0}, {X: 10, Y: 20}, {X: 5, Y: 20}}},
		"square": {Shape: "extrusion", Size: map[string]float64{"depth": 4}, Profile: []geometry.Point{{X: 0, Y: 0}, {X: 10, Y: 0}, {X: 10, Y: 10}, {X: 0, Y: 10}}},
	}
	var got struct {
		Shapes map[string]meshOut
		Crease float64
	}
	runLooks(t, map[string]any{"case": "shapes", "shapes": shapes}, &got)
	tube := got.Shapes["tube"]
	worst := 0.0
	caps := 0
	// Every vertex of this profile is on an end ring (y 0 or 20): it belongs either to a
	// flat end (normal along the axis) or to a wall (normal straight out from the axis at
	// that vertex's angle — a flat facet's normal is 4.5° off it). A normal between the two
	// is the 90° edge smoothed away.
	for i := 0; i < len(tube.Positions)/3; i++ {
		p, n := tube.vertex(i), tube.normal(i)
		r := math.Hypot(p[0], p[2])
		if math.Abs(math.Abs(n[1])-1) < 1e-9 {
			caps++
			continue
		}
		if math.Abs(n[1]) > 1e-9 {
			t.Fatalf("a vertex on the tube's end ring has normal %v: the 90° edge was smoothed", n)
		}
		want := [3]float64{p[0] / r, 0, p[2] / r}
		if r < 7.5 {
			want = [3]float64{-want[0], 0, -want[2]}
		}
		worst = math.Max(worst, math.Acos(math.Min(1, n[0]*want[0]+n[1]*want[1]+n[2]*want[2]))*180/math.Pi)
	}
	if worst > 0.5 || caps == 0 {
		t.Errorf("the tube's wall normals are up to %.2f° off radial (flat facets are 4.5°), %d end-face normals", worst, caps)
	}
	sq := got.Shapes["square"]
	for i := 0; i < len(sq.Normals)/3; i++ {
		n := sq.normal(i)
		ones := 0
		for _, v := range n {
			if math.Abs(math.Abs(v)-1) < 1e-12 {
				ones++
			}
		}
		if ones != 1 {
			t.Fatalf("a square extrusion's vertex has normal %v; its faces are flat and meet at right angles", n)
		}
	}
	if got.Crease < 20 || got.Crease > 60 {
		t.Errorf("the crease angle is %v°", got.Crease)
	}
}

// The contact shadow is captured when what is drawn changes — the load, an explode — and
// not on a frame that only turns the camera; switched off, the floor carries none. Its
// draws go to its own target and never reach the screen, so the frame counts nothing more.
func TestRendererCastsAContactShadowOnlyWhenTheModelChanges(t *testing.T) {
	var got struct {
		Frames map[string]struct {
			Loaded struct {
				Stats    struct{ ShadowPass int }
				Sequence []string
				Problems []string
			}
			Turned, Off looksFrame
			Exploded    struct{ Stats struct{ ShadowPass int } }
		}
	}
	doc := presentationCar(t)
	runLooks(t, map[string]any{"case": "shadow", "spec": doc}, &got)
	opaque := 0
	for _, p := range doc.Parts {
		if p.Opacity >= 1 {
			opaque++
		}
	}
	for mode, f := range got.Frames {
		noLooksProblems(t, mode, append(f.Loaded.Problems, f.Turned.Problems...))
		if f.Loaded.Stats.ShadowPass != opaque {
			t.Errorf("%s: loading drew %d copies into the shadow; the car has %d opaque parts", mode, f.Loaded.Stats.ShadowPass, opaque)
		}
		if f.Turned.Stats.ShadowPass != 0 || !f.Turned.Stats.Shadow || count(f.Turned.Sequence, "shadow") != 0 {
			t.Errorf("%s: turning the camera drew the shadow again (%d copies, %v)", mode, f.Turned.Stats.ShadowPass, f.Turned.Sequence)
		}
		if count(f.Turned.Sequence, "screen:ground") != 1 {
			t.Errorf("%s: the floor under the model was drawn %d times", mode, count(f.Turned.Sequence, "screen:ground"))
		}
		if f.Exploded.Stats.ShadowPass != opaque {
			t.Errorf("%s: an exploded view kept the assembled model's shadow (%d copies redrawn)", mode, f.Exploded.Stats.ShadowPass)
		}
		if f.Off.Stats.Shadow || count(f.Off.Sequence, "screen:ground") != 0 {
			t.Errorf("%s: with the contact shadow off the floor still carries one", mode)
		}
		if f.Turned.Stats.Instances != len(doc.Parts) {
			t.Errorf("%s: a frame drew %d copies of %d parts", mode, f.Turned.Stats.Instances, len(doc.Parts))
		}
	}
}

func count(list []string, s string) int {
	n := 0
	for _, v := range list {
		if v == s {
			n++
		}
	}
	return n
}

func index(list []string, s string, last bool) int {
	at := -1
	for i, v := range list {
		if v == s {
			at = i
			if !last {
				break
			}
		}
	}
	return at
}

// The frame is drawn in order on every path: the studio backdrop first, the model, then —
// on WebGL2 only — the occlusion laid over what is opaque and BEFORE anything see-through,
// and the whole resolved to the canvas once. WebGL1 links no GLSL ES 3.00 program and has
// no occlusion. Feature lines are drawn only when asked for, one call per batch with sharp
// edges, never for a sphere. The default and reset view is the hero view.
func TestRendererDrawsItsPassesInOrderOnEveryPath(t *testing.T) {
	var got struct {
		Frames map[string]struct {
			On, Off, Lines looksFrame
			Programs       int
		}
		Hero, Camera, Reset struct{ Yaw, Pitch float64 }
	}
	doc := presentationCar(t)
	runLooks(t, map[string]any{"case": "passes", "spec": doc}, &got)
	for mode, f := range got.Frames {
		for _, fr := range []looksFrame{f.On, f.Off, f.Lines} {
			noLooksProblems(t, mode, fr.Problems)
		}
		seq := f.On.Sequence
		if len(seq) == 0 || seq[0] != "screen:backdrop" {
			t.Errorf("%s: the frame starts %v; the backdrop goes first", mode, seq[:min(3, len(seq))])
		}
		lastOpaque, firstGlass := index(seq, "opaque", true), index(seq, "translucent", false)
		occlusion := index(seq, "screen:occlusion", false)
		if mode == "webgl2" {
			if f.On.Stats.Post != "msaa4+ssao" || f.On.Resolves != 1 || count(seq, "screen:ssao") != 1 {
				t.Errorf("webgl2: post %q, %d resolves, sequence %v", f.On.Stats.Post, f.On.Resolves, seq)
			}
			if !(lastOpaque >= 0 && firstGlass > 0 && occlusion > lastOpaque && occlusion < firstGlass) {
				t.Errorf("webgl2: occlusion at %d, last opaque draw at %d, first translucent at %d: %v", occlusion, lastOpaque, firstGlass, seq)
			}
			if f.Off.Stats.Post != "none" || f.Off.Resolves != 0 || count(f.Off.Sequence, "screen:occlusion") != 0 {
				t.Errorf("webgl2 with occlusion off: post %q, sequence %v", f.Off.Stats.Post, f.Off.Sequence)
			}
			if f.Programs != 9 {
				t.Errorf("webgl2 linked %d programs; want the part, line, backdrop, shadow, blur, ground, edge and two occlusion programs", f.Programs)
			}
		} else {
			if f.On.Stats.Post != "none" || occlusion >= 0 || f.Programs != 7 {
				t.Errorf("%s: post %q, occlusion at %d, %d programs linked; WebGL1 has no occlusion", mode, f.On.Stats.Post, occlusion, f.Programs)
			}
		}
		if len(f.On.Lines) != 0 || f.On.Stats.EdgeDraws != 0 {
			t.Errorf("%s: feature lines were drawn without being asked for", mode)
		}
		if f.On.Stats.DrawCalls != f.Off.Stats.DrawCalls || len(f.On.Draws) != len(f.Off.Draws) {
			t.Errorf("%s: occlusion changed what reaches the screen: %d/%d draw calls", mode, f.On.Stats.DrawCalls, f.Off.Stats.DrawCalls)
		}
		if mode == "webgl1-noext" {
			continue
		}
		// Box-like bodies and cylinders have sharp edges; the headlights are spheres.
		if f.Lines.Stats.EdgeDraws == 0 || len(f.Lines.Lines) != f.Lines.Stats.EdgeDraws {
			t.Errorf("%s: asked for, feature lines made %d calls (%d recorded)", mode, f.Lines.Stats.EdgeDraws, len(f.Lines.Lines))
		}
		for _, l := range f.Lines.Lines {
			if l.Elements%2 != 0 {
				t.Errorf("%s: a feature line draw of %d indices", mode, l.Elements)
			}
		}
	}
	if got.Camera != got.Hero || got.Reset != got.Hero || got.Hero.Pitch <= 0.2 || got.Hero.Pitch >= 0.5 {
		t.Errorf("the default view is %+v and reset returns %+v; the hero view is %+v, a three-quarter view from a little above",
			got.Camera, got.Reset, got.Hero)
	}
}

const edgesHarness = `
  const fs = require('fs');
  const stub = require(process.argv[2]);
  const F = stub.loadForge3D(process.argv[3]);
  const input = JSON.parse(fs.readFileSync(process.argv[4], 'utf8'));
  const P = F.presentation;
  if (!P) { process.stdout.write('{"missing":true}'); return; }
  const out = {};
  for (const name of Object.keys(input.shapes)) {
    out[name] = P.featureEdges(F.buildGeometry(input.shapes[name]).geo).indices.length / 2;
  }
  process.stdout.write(JSON.stringify(out));
`

// Feature lines are the edges where facets turn by more than the crease angle: a box's
// twelve, a cylinder's two rims and nothing along its side, none on a sphere.
func TestRendererFindsTheFeatureEdgesOfAShape(t *testing.T) {
	shapes := map[string]geometry.Part{
		"box":      {Shape: "box", Size: map[string]float64{"width": 2, "height": 3, "depth": 4}},
		"cylinder": {Shape: "cylinder", Size: map[string]float64{"radius": 1, "height": 2}},
		"sphere":   {Shape: "sphere", Size: map[string]float64{"radius": 1}},
	}
	var got map[string]int
	if err := json.Unmarshal(runNodeHarness(t, edgesHarness, map[string]any{"shapes": shapes}), &got); err != nil {
		t.Fatal(err)
	}
	radial, _, _ := geometry.TessellationCounts()
	want := map[string]int{"box": 12, "cylinder": 2 * radial, "sphere": 0}
	for name, n := range want {
		if got[name] != n {
			t.Errorf("%s has %d feature edges; want %d", name, got[name], n)
		}
	}
}

// The stage draws the kernel-built mesh whenever there is one (A1, checked 2026-09-18): a
// restored or opened variant asks for its mesh as it loads (TestWorkbenchAsksNoWholeMesh-
// ForADesignItRefused holds that a design within the limit asks once), and a live turn
// asks the moment its geometry is saved — the "variant" event, which carries the id the
// kernel builds from. Nothing held that second path.
func TestWorkbenchRefinesALiveTurnWithTheKernelMesh(t *testing.T) {
	b, err := assetFS.ReadFile("assets/workbench.js")
	if err != nil {
		t.Fatal(err)
	}
	js := codeOnly(string(b))
	i := strings.Index(js, "case 'variant':")
	if i < 0 {
		t.Fatal("workbench.js handles no 'variant' event")
	}
	body := js[i+len("case 'variant':"):]
	if j := strings.Index(body, "case '"); j > 0 {
		body = body[:j]
	}
	if !strings.Contains(body, "refineWithBuiltSolid(ev.variant.version_id, state.prototype)") {
		t.Error("a live turn's saved variant no longer asks for its kernel mesh, so the stage keeps the primitives " +
			"it drew from the reply until the page is reloaded")
	}
}
