package httpapi

import (
	"context"
	"encoding/json"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The surface-normals view, for the looks judge (looks, stage D). 2026-09-20.
//
// # What it is for
//
// The looks judge (internal/looks) puts two renders of one design change to a vision
// model and asks which looks more like a designed product. A studio light is exactly
// what makes that question hard to answer honestly: a rolling highlight looks the same
// over a fillet and over a chamfer, and over a curved panel and over eight flats. The
// normals view takes the light out — a pixel's colour is the direction the surface faces
// and nothing else — so a row of facets reads as bands and a real curve reads as a
// gradient. That is the one view where the gate can catch a change that only LOOKS
// smoother, which is why the judge asks it as a confirmation before accepting.
//
// # What must hold, and why each of these
//
//   - Nothing asks for it by default. This is a debug picture; a reader must never get
//     one because something was left switched on.
//   - The shader writes the normal and nothing else. Held by reading the part program's
//     source through the shipped file: the branch is there, it is taken on uSurfaceView,
//     and it returns before any light, material, tone map, rim or selection term.
//   - The occlusion pass does not run in it, and the stats line says so. A darkened
//     crease in a picture whose colour means "direction" is a turn in the surface that is
//     not there — and a stats line claiming a pass that did not run is worse than the
//     pass itself.
//   - A name nobody recognises is 'shaded'. setSurfaceView('wireframe') must not put a
//     workbench into an undefined state.
//
// Driven through scripts/webgl-stub.js, which records what reaches the GPU, so this runs
// without a GPU. Whether the shader COMPILES is TestShadersCompileInARealBrowser's job.
const normalsViewHarness = `
  const fs = require('fs');
  const stub = require(process.argv[2]);
  const F = stub.loadForge3D(process.argv[3]);
  const input = JSON.parse(fs.readFileSync(process.argv[4], 'utf8'));
  const P = F.presentation;
  if (!P || !F.Studio.prototype.setSurfaceView) { process.stdout.write('{"missing":true}'); return; }
  const out = { frag: P.shaders.part[1], frames: {} };
  for (const mode of ['webgl2', 'webgl1']) {
    const made = stub.makeCanvas(mode, 640, 480);
    const studio = new F.Studio(made.canvas, { onError() {} });
    studio.load(input.spec);
    const shot = (label) => {
      made.record.reset();
      studio.draw();
      out.frames[mode + '-' + label] = {
        view: studio.surfaceView,
        uniform: made.record.uniforms.uSurfaceView || null,
        post: studio.stats.post,
        sequence: made.record.sequence.slice(),
        problems: made.record.problems.slice(),
        draws: made.record.draws.length
      };
    };
    shot('default');
    studio.setSurfaceView('normals');
    shot('normals');
    studio.setSurfaceView('wireframe');
    shot('unknown-name');
    studio.setSurfaceView('shaded');
    shot('back');
  }
  process.stdout.write(JSON.stringify(out));
`

func TestRendererHasASurfaceNormalsViewAndDoesNotDefaultToIt(t *testing.T) {
	var got struct {
		Frag   string
		Frames map[string]normalsFrame
	}
	raw := runNodeHarness(t, normalsViewHarness, map[string]any{"spec": presentationCar(t)})
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unreadable renderer output %.300q: %v", raw, err)
	}

	// The shader branch, read through the shipped file: taken on uSurfaceView, writing
	// the normal, and BEFORE the light. "Before" is the whole point — a normals view
	// computed and then tone-mapped is a picture of the tone map.
	frag := got.Frag
	const branch = "if (uSurfaceView > 0.5)"
	const writes = "gl_FragColor = vec4(N * 0.5 + 0.5, vColour.a)"
	if !strings.Contains(frag, branch) || !strings.Contains(frag, writes) {
		t.Fatalf("the part shader has no surface-normals branch:\n%s", frag)
	}
	for _, light := range []string{"irradiance(N)", "aces(col * uExposure)", "uKeyColour * NoL"} {
		if !strings.Contains(frag, light) {
			t.Fatalf("the part shader no longer contains %q, so this fence cannot say the "+
				"normals branch comes first", light)
		}
		if strings.Index(frag, branch) > strings.Index(frag, light) {
			t.Errorf("the normals branch is written after %q, so the normals view is a picture "+
				"of the lighting rather than of the surface", light)
		}
	}
	if strings.Index(frag, writes) > strings.Index(frag, "vec3 V = normalize(uCamPos - vWorld)") {
		t.Error("the normals branch does not return before the shading is computed")
	}

	for _, mode := range []string{"webgl2", "webgl1"} {
		def, normals := got.Frames[mode+"-default"], got.Frames[mode+"-normals"]
		unknown, back := got.Frames[mode+"-unknown-name"], got.Frames[mode+"-back"]
		for label, f := range map[string]normalsFrame{
			"default": def, "normals": normals, "unknown-name": unknown, "back": back} {
			noLooksProblems(t, mode+" "+label, f.Problems)
			if f.Draws == 0 {
				t.Fatalf("%s %s: nothing was drawn, so this fence read nothing", mode, label)
			}
		}
		// Off unless asked for, on when asked, and off again.
		if def.View != "shaded" || len(def.Uniform) != 1 || def.Uniform[0] != 0 {
			t.Errorf("%s: a new Studio draws in %q with uSurfaceView %v; a reader must never be "+
				"given the debug picture by default", mode, def.View, def.Uniform)
		}
		if normals.View != "normals" || len(normals.Uniform) != 1 || normals.Uniform[0] != 1 {
			t.Errorf("%s: setSurfaceView('normals') drew in %q with uSurfaceView %v",
				mode, normals.View, normals.Uniform)
		}
		if unknown.View != "shaded" || len(unknown.Uniform) != 1 || unknown.Uniform[0] != 0 {
			t.Errorf("%s: setSurfaceView('wireframe') left the Studio in %q with uSurfaceView %v; "+
				"a name nobody recognises must be the ordinary picture", mode, unknown.View, unknown.Uniform)
		}
		if back.View != "shaded" || len(back.Uniform) != 1 || back.Uniform[0] != 0 {
			t.Errorf("%s: setSurfaceView('shaded') did not go back: %q, uSurfaceView %v",
				mode, back.View, back.Uniform)
		}
		// The occlusion pass — read from what was DRAWN, not only from the stats line.
		//
		// ‼️ The stats line alone is not evidence: forge3d.js writes it, so a fence
		// that reads nothing else holds the string and not the pass. A drill that
		// removed the guard on the pass and left the string alone stayed green
		// against exactly that fence (2026-09-20). The stub's sequence is what the
		// GPU was actually asked to do.
		if occlusionPasses(normals.Sequence) != 0 {
			t.Errorf("%s: the normals view drew the occlusion passes %v; a darkened crease in a "+
				"picture whose colour means \"which way this faces\" is a turn in the surface "+
				"that is not there", mode, normals.Sequence)
		}
		if strings.Contains(normals.Post, "ssao") {
			t.Errorf("%s: the normals view reports post=%q", mode, normals.Post)
		}
		if mode == "webgl2" {
			if occlusionPasses(def.Sequence) == 0 {
				t.Errorf("webgl2: the shaded picture drew no occlusion pass either (%v), so this "+
					"fence cannot say the normals view turned one off", def.Sequence)
			}
			if occlusionPasses(back.Sequence) != occlusionPasses(def.Sequence) {
				t.Errorf("webgl2: going back to 'shaded' drew %d occlusion passes, not the %d the "+
					"first shaded frame did", occlusionPasses(back.Sequence), occlusionPasses(def.Sequence))
			}
		}
		if def.Post != back.Post {
			t.Errorf("%s: the shaded picture's passes changed across a trip through the normals "+
				"view: %q then %q", mode, def.Post, back.Post)
		}
		if mode == "webgl2" && !strings.Contains(def.Post, "ssao") {
			t.Errorf("webgl2: the shaded picture reports post=%q, so this fence cannot say the "+
				"normals view turned the occlusion off", def.Post)
		}
	}
}

// normalsFrame is one frame as the stub recorded it.
type normalsFrame struct {
	View     string
	Uniform  []float64
	Post     string
	Sequence []string
	Problems []string
	Draws    int
}

// occlusionPasses counts the screen passes the occlusion is made of, by the names
// scripts/webgl-stub.js gives them (screenName: a program with uDepth is the SSAO
// itself, one with uAO is the composite that multiplies it in).
func occlusionPasses(sequence []string) int {
	n := 0
	for _, s := range sequence {
		if s == "screen:ssao" || s == "screen:occlusion" {
			n++
		}
	}
	return n
}

// The normals view is a DIFFERENT picture, in a real GLSL compiler. 2026-09-20.
//
// # Why a browser is needed for this one
//
// scripts/webgl-stub.js can say the uniform reached the GPU; it cannot say the
// shader did anything with it, because it compiles nothing and draws no pixels. A
// branch that sets a uniform nothing reads is exactly the failure mode a stub
// fence blesses — and the looks judge (internal/looks) asks the normals pair as
// its confirmation step, so a normals view that is quietly the shaded one would
// turn that confirmation into a second vote for the same picture.
//
// So: the shipped forge3d.js, the presentation car, headless Chrome on
// SwiftShader, and two snapshots. Both must be real pictures (a blank canvas
// encodes to a few hundred bytes) and they must differ. This is also the exact
// path internal/agent's looks benchmark takes to produce the images it judges, so
// a break here is a break there.
func TestTheNormalsViewIsADifferentPictureInARealBrowser(t *testing.T) {
	chrome := findChrome()
	if chrome == "" {
		t.Skip("no Chrome or Chromium found (set FORGE_CHROME); nothing was drawn")
	}
	src, err := assetFS.ReadFile("assets/forge3d.js")
	if err != nil {
		t.Fatal(err)
	}
	car, err := json.Marshal(presentationCar(t))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	page := `<!doctype html><meta charset="utf-8"><body style="margin:0">
<canvas id="c" width="480" height="320"></canvas>
<pre id="result">pending</pre>
<script src="forge3d.js"></script>
<script>
  var DOC = ` + string(car) + `;
  var out = {};
  /* ‼️ A picture is reported as its LENGTH and a hash, never as the data URI.
   * Headless Chrome writes a large --dump-dom and then does not exit: measured
   * 2026-09-20, a 6 KB dump exits in 21 s and a 295 KB one is still alive two
   * minutes later. Five 640x400 PNGs in a <pre> is three quarters of a megabyte,
   * and this fence hung on exactly that. The assertions want "is it blank" and
   * "are these two the same", and 32 bits of FNV-1a answers both. */
  function fingerprint(s) {
    var h = 2166136261;
    for (var i = 0; i < s.length; i++) { h ^= s.charCodeAt(i); h = Math.imul(h, 16777619); }
    return { len: s.length, hash: (h >>> 0).toString(16), png: s.slice(0, 22) };
  }
  try {
    var studio = new Forge3D.Studio(document.getElementById('c'), { onError: function (m) { out.error = m; } });
    if (!studio.gl) { out.error = 'no WebGL context'; }
    else {
      studio.load(DOC);
      studio.resetView();
      out.shaded = fingerprint(studio.snapshot());
      studio.setSurfaceView('normals');
      out.normals = fingerprint(studio.snapshot());
      studio.setSurfaceView('shaded');
      out.again = fingerprint(studio.snapshot());
      /* The same two, with the occlusion already off on BOTH, so the only thing
       * left that can differ between them is the shader's own branch. */
      studio.setAmbientOcclusion(false);
      out.shadedFlat = fingerprint(studio.snapshot());
      studio.setSurfaceView('normals');
      out.normalsFlat = fingerprint(studio.snapshot());
      out.path = studio.renderPath;
    }
  } catch (e) { out.error = String(e && e.message || e); }
  document.getElementById('result').textContent = JSON.stringify(out);
</script>`
	for name, data := range map[string][]byte{"forge3d.js": src, "index.html": []byte(page)} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	url := "file://" + filepath.ToSlash(filepath.Join(dir, "index.html"))
	if runtime.GOOS == "windows" {
		url = "file:///" + filepath.ToSlash(filepath.Join(dir, "index.html"))
	}
	cmd := exec.CommandContext(ctx, chrome, "--headless=new", "--no-sandbox", "--disable-dev-shm-usage",
		"--use-angle=swiftshader", "--enable-unsafe-swiftshader", "--ignore-gpu-blocklist",
		"--user-data-dir="+filepath.Join(dir, "profile"), "--virtual-time-budget=6000", "--dump-dom", url)
	dom, err := cmd.Output()
	if err != nil {
		t.Fatalf("headless Chrome failed: %v", err)
	}
	s := string(dom)
	i, j := strings.Index(s, `<pre id="result">`), strings.Index(s, "</pre>")
	if i < 0 || j < i {
		t.Fatalf("no result in the page Chrome rendered: %.300s", s)
	}
	type picture struct {
		Len  int
		Hash string
		PNG  string
	}
	var got struct {
		Shaded, Normals, Again, ShadedFlat, NormalsFlat picture
		Error, Path                                     string
	}
	if err := json.Unmarshal([]byte(html.UnescapeString(s[i+len(`<pre id="result">`):j])), &got); err != nil {
		t.Fatalf("unreadable page output: %v", err)
	}
	if got.Error != "" {
		t.Fatalf("the renderer refused: %s", got.Error)
	}
	if got.Path == "" {
		t.Skip("this Chrome gave no WebGL context (headless without SwiftShader?); nothing was drawn")
	}
	// A blank canvas encodes to almost nothing, so size is the cheap test for
	// "something was actually drawn" without decoding the PNG.
	const leastRealPicture = 3000
	for name, p := range map[string]picture{"shaded": got.Shaded, "normals": got.Normals,
		"shaded with the occlusion off": got.ShadedFlat, "normals with the occlusion off": got.NormalsFlat} {
		if p.PNG != "data:image/png;base64," {
			t.Fatalf("%s is not a PNG: %.60q", name, p.PNG)
		}
		if p.Len < leastRealPicture {
			t.Errorf("%s encodes to %d characters, which is a blank canvas rather than a picture "+
				"of a car", name, p.Len)
		}
	}
	// ‼️ The pair that matters is the one with the occlusion off on BOTH sides.
	// Compared against the ordinary shaded frame, the normals view differs for two
	// reasons — the shader branch and the occlusion pass it also turns off — so a
	// drill that disabled the branch alone still produced a different picture and
	// this fence stayed green (2026-09-20). With the occlusion off on both, the
	// branch is the only thing left that can make them differ.
	if got.NormalsFlat == got.ShadedFlat {
		t.Error("with the occlusion off on both, the normals view drew exactly the shaded " +
			"picture, so the shader's uSurfaceView branch does nothing — and the looks judge's " +
			"confirmation step would be a second vote on the same image")
	}
	if got.Shaded == got.Normals {
		t.Error("the normals view drew exactly the shaded picture")
	}
	if got.Again != got.Shaded {
		t.Error("going back to 'shaded' did not reproduce the shaded picture, so the normals view " +
			"leaves the stage in a state a reader would see")
	}
}
