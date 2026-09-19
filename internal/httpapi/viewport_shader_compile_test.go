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

// The viewport's shaders compile and link in a real GLSL compiler, on both paths.
// 2026-09-18.
//
// # Why a browser
//
// scripts/webgl-stub.js records calls and lints each program's text (one GLSL version
// per program, 3.00 only on WebGL2, every uniform asked for is declared), but it is not
// a compiler: a type error, a missing semicolon or an implicit int-to-float conversion
// only shows when a driver compiles the source, and a shader that fails to compile leaves
// the stage blank for everyone. This drives the shipped forge3d.js in headless Chrome
// (ANGLE on SwiftShader, so no GPU is needed): a Studio on WebGL2, and one whose canvas
// refuses WebGL2 so it falls back to WebGL1 with ANGLE_instanced_arrays — the path W1
// kept. Each builds every program it uses, loads the presentation car and draws a frame
// with every pass on; any compile or link error throws, and the frame must leave no GL
// error.
//
// Skipped where there is no Chrome or Chromium (FORGE_CHROME names one explicitly); CI's
// ubuntu runners have google-chrome.
func TestShadersCompileInARealBrowser(t *testing.T) {
	chrome := findChrome()
	if chrome == "" {
		t.Skip("no Chrome or Chromium found (set FORGE_CHROME); the shaders were not compiled")
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
	page := `<!doctype html><meta charset="utf-8"><body>
<canvas id="a" width="320" height="200"></canvas><canvas id="b" width="320" height="200"></canvas>
<pre id="result">pending</pre>
<script src="forge3d.js"></script>
<script>
  var DOC = ` + string(car) + `;
  var out = {};
  function run(name, canvas, webgl1) {
    if (webgl1) {
      var own = canvas.getContext.bind(canvas);
      canvas.getContext = function (type, attrs) { return type === 'webgl2' ? null : own(type, attrs); };
    }
    var errors = [], r = { errors: errors };
    try {
      var studio = new Forge3D.Studio(canvas, { onError: function (m) { errors.push(m); } });
      if (studio.gl) {
        studio.load(DOC);
        studio.setFeatureLines(true);
        studio.draw();
        r.path = studio.renderPath;
        r.post = studio.stats.post;
        r.glError = studio.gl.getError();
        r.instances = studio.stats.instances;
      }
    } catch (e) { errors.push(String(e && e.message || e)); }
    out[name] = r;
  }
  run('webgl2', document.getElementById('a'), false);
  run('webgl1', document.getElementById('b'), true);
  document.getElementById('result').textContent = JSON.stringify(out);
</script>`
	for name, data := range map[string][]byte{"forge3d.js": src, "index.html": []byte(page)} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	url := "file://" + filepath.ToSlash(filepath.Join(dir, "index.html"))
	if runtime.GOOS == "windows" {
		url = "file:///" + filepath.ToSlash(filepath.Join(dir, "index.html"))
	}
	cmd := exec.CommandContext(ctx, chrome, "--headless=new", "--no-sandbox", "--disable-dev-shm-usage",
		"--use-angle=swiftshader", "--enable-unsafe-swiftshader", "--ignore-gpu-blocklist",
		"--user-data-dir="+filepath.Join(dir, "profile"), "--virtual-time-budget=3000", "--dump-dom", url)
	dom, err := cmd.Output()
	if err != nil {
		t.Fatalf("headless Chrome failed: %v", err)
	}
	s := string(dom)
	i, j := strings.Index(s, `<pre id="result">`), strings.Index(s, "</pre>")
	if i < 0 || j < i {
		t.Fatalf("no result in the page Chrome rendered: %.400s", s)
	}
	raw := html.UnescapeString(s[i+len(`<pre id="result">`) : j])
	var got map[string]struct {
		Errors    []string
		Path      string
		Post      string
		GlError   int
		Instances int
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("the page reported %q: %v", raw, err)
	}
	want := map[string]string{"webgl2": "webgl2", "webgl1": "webgl1-instanced"}
	for name, path := range want {
		r := got[name]
		if len(r.Errors) > 0 {
			t.Errorf("%s: %v", name, r.Errors)
			continue
		}
		if r.Path == "" {
			t.Skipf("%s: this Chrome gave no WebGL context (headless without SwiftShader?); nothing was compiled", name)
		}
		if r.Path != path || r.GlError != 0 || r.Instances == 0 {
			t.Errorf("%s: drew on %q with GL error %d and %d instances; want %q, no error", name, r.Path, r.GlError, r.Instances, path)
		}
		if (name == "webgl2") != (r.Post != "none") {
			t.Errorf("%s: post-processing %q", name, r.Post)
		}
	}
}

func findChrome() string {
	if c := os.Getenv("FORGE_CHROME"); c != "" {
		return c
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	for _, p := range []string{
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}
