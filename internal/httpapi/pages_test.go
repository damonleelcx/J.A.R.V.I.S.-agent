package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

func testDeps() Deps {
	return Deps{
		Config: &config.Config{
			Env:  config.EnvDevelopment,
			Auth: config.AuthConfig{MinPasswordLength: 12},
			HTTP: config.HTTPConfig{PublicURL: "http://localhost:8080", MaxBodyBytes: 1 << 20},
		},
		Clock:   clock.System{},
		Log:     logx.Discard(),
		Version: "test",
		Commit:  "test",
	}
}

// scriptElement captures the body of every <script> element so each can be
// checked individually.
//
// An earlier version of this pattern tried to match "a script tag followed by
// non-whitespace" in one step and matched the `<` of the closing </script> tag,
// so it flagged every page including the correct ones. A fence that cries wolf
// gets deleted, which is how the bug it guards comes back.
var scriptElement = regexp.MustCompile(`(?is)<script(?:\s[^>]*)?>(.*?)</script>`)

// inlineScriptBodies returns the non-empty bodies of any inline <script>
// elements in html. A script that only references a src has an empty body and
// is exactly what the CSP permits.
func inlineScriptBodies(html string) []string {
	var out []string
	for _, m := range scriptElement.FindAllStringSubmatch(html, -1) {
		if body := strings.TrimSpace(m[1]); body != "" {
			out = append(out, body)
		}
	}
	return out
}

// TestPagesCarryNoInlineScript is the regression fence for
// docs/bugfix/2026-09-02-csp-blocked-inline-page-script.md.
//
// The application's CSP is `script-src 'self'`, which forbids inline script.
// Both email landing pages once carried their behaviour inline, so the browser
// refused to run it: the confirm button did nothing, and the reset form fell
// back to a native GET that dropped the token from the URL — meaning a user's
// own attempt to reset their password destroyed the link they were using.
//
// No Go test could observe that, because the failure happens in the browser's
// policy engine. This fence approaches it from the only angle a Go test can:
// the page must not contain the construct the policy forbids.
func TestPagesCarryNoInlineScript(t *testing.T) {
	pages := NewPageHandlers(testDeps())

	cases := map[string]struct {
		handler http.HandlerFunc
		target  string
	}{
		"index":  {pages.Index, "/"},
		"verify": {pages.VerifyEmailPage, "/auth/verify-email?token=" + strings.Repeat("A", 43)},
		"reset":  {pages.ResetPasswordPage, "/auth/reset-password?token=" + strings.Repeat("A", 43)},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			tc.handler(rr, httptest.NewRequest(http.MethodGet, tc.target, nil))

			body := rr.Body.String()
			if len(body) < 200 {
				t.Fatalf("page rendered only %d bytes; the fence would pass vacuously", len(body))
			}
			if bodies := inlineScriptBodies(body); len(bodies) > 0 {
				t.Errorf("page %q contains %d inline <script> element(s), which `script-src 'self'` blocks. "+
					"The browser will silently refuse to run them. Move the code into "+
					"internal/httpapi/assets/pages.js and reference it with src.\nfirst: %.100s",
					name, len(bodies), bodies[0])
			}
		})
	}
}

// TestPagesReferenceTheServedScript is the other half: proving the pages have no
// inline script is only meaningful if they load the external one.
func TestPagesReferenceTheServedScript(t *testing.T) {
	pages := NewPageHandlers(testDeps())
	rr := httptest.NewRecorder()
	pages.VerifyEmailPage(rr, httptest.NewRequest(http.MethodGet,
		"/auth/verify-email?token="+strings.Repeat("A", 43), nil))

	body := rr.Body.String()
	// Prefixes, because asset URLs carry a content hash (`?v=…`). Matching the
	// bare path would break every time an asset changed, which is the opposite
	// of what this fence is for.
	for _, want := range []string{`src="/assets/pages.js?v=`, `href="/assets/shell.css?v=`, `data-page="verify"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page is missing %s; the behaviour would never load", want)
		}
	}
}

// TestCSPForbidsInlineScript keeps the fence above meaningful. If somebody
// "fixes" a future inline script by adding 'unsafe-inline' to the policy, the
// other test would still pass while the protection is gone.
func TestCSPForbidsInlineScript(t *testing.T) {
	h := SecurityHeaders(true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	csp := rr.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("no Content-Security-Policy header")
	}
	scriptSrc := ""
	for _, directive := range strings.Split(csp, ";") {
		d := strings.TrimSpace(directive)
		if strings.HasPrefix(d, "script-src") {
			scriptSrc = d
		}
	}
	if scriptSrc == "" {
		t.Fatal("the policy has no script-src directive")
	}
	if strings.Contains(scriptSrc, "unsafe-inline") {
		t.Error("script-src allows 'unsafe-inline'. That removes the protection the header exists for. " +
			"If this was added to make an inline script work, serve the script from /assets instead.")
	}
	if strings.Contains(scriptSrc, "unsafe-eval") {
		t.Error("script-src allows 'unsafe-eval'")
	}
	for _, want := range []string{"frame-ancestors 'none'", "base-uri 'none'", "default-src 'self'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("policy is missing %q", want)
		}
	}
	if !strings.Contains(rr.Header().Get("Strict-Transport-Security"), "max-age=") {
		t.Error("production responses should carry HSTS")
	}
}

// TestPagesWithoutATokenExplainThemselves — a link that lost its token in
// transit (mail clients do truncate long URLs) must say so, not render a form
// that cannot possibly work.
func TestPagesWithoutATokenExplainThemselves(t *testing.T) {
	pages := NewPageHandlers(testDeps())

	for name, h := range map[string]http.HandlerFunc{
		"verify": pages.VerifyEmailPage,
		"reset":  pages.ResetPasswordPage,
	} {
		rr := httptest.NewRecorder()
		h(rr, httptest.NewRequest(http.MethodGet, "/auth/"+name, nil))
		body := rr.Body.String()

		if !strings.Contains(body, "missing its token") {
			t.Errorf("%s: a tokenless page should explain the problem", name)
		}
		if strings.Contains(body, `id="go"`) {
			t.Errorf("%s: a tokenless page still renders its action control, which cannot work", name)
		}
	}
}

// TestPagesCarryingATokenAreNotCached — these URLs contain a live credential.
// Caching one, in the browser or a shared proxy, leaves it readable afterwards.
func TestPagesCarryingATokenAreNotCached(t *testing.T) {
	pages := NewPageHandlers(testDeps())
	rr := httptest.NewRecorder()
	pages.ResetPasswordPage(rr, httptest.NewRequest(http.MethodGet,
		"/auth/reset-password?token="+strings.Repeat("A", 43), nil))

	cc := rr.Header().Get("Cache-Control")
	if !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control is %q; a page carrying a live token must be no-store", cc)
	}
	if ref := rr.Header().Get("Referrer-Policy"); ref != "no-referrer" {
		t.Errorf("Referrer-Policy is %q; a token in the query string must never leave in a Referer header", ref)
	}
}

// TestAssetsServeOnlyWhatIsEmbedded guards the asset handler against being used
// to read anything but the two files it exists for.
func TestAssetsServeOnlyWhatIsEmbedded(t *testing.T) {
	pages := NewPageHandlers(testDeps())

	// EVERY embedded asset, walked from the filesystem rather than listed here.
	//
	// The earlier version named two files it knew were fine, which meant a newly
	// embedded asset that nobody added to the handler's allowlist was served as
	// a 404 and no test noticed. That happened: room.css was embedded, missed
	// from the allowlist, and the page rendered unstyled with every fence green.
	// Enumerating what you check is what made the check vacuous.
	embedded, err := fs.Glob(assetFS, "assets/*")
	if err != nil {
		t.Fatal(err)
	}
	var checked int
	for _, path := range embedded {
		name := strings.TrimPrefix(path, "assets/")
		if name == "portrait" {
			continue // a directory, covered by isPortraitAsset below
		}
		checked++
		rr := httptest.NewRecorder()
		pages.Assets(rr, httptest.NewRequest(http.MethodGet, "/assets/"+name, nil))
		if rr.Code != http.StatusOK {
			t.Errorf("%s is embedded but served %d — it is missing from the allowlist in Assets, "+
				"so the page that references it loads without it", name, rr.Code)
		}
		if rr.Body.Len() == 0 {
			t.Errorf("%s served an empty body", name)
		}
		if rr.Header().Get("Content-Type") == "" {
			t.Errorf("%s served without a Content-Type", name)
		}
	}
	if checked < 8 {
		t.Fatalf("only %d embedded asset(s) were found; the glob is not seeing them", checked)
	}
	for _, bad := range []string{
		"/assets/pages.go",
		"/assets/../pages.go",
		"/assets/../../go.mod",
		"/assets/",
		"/assets/nonexistent.js",
	} {
		rr := httptest.NewRecorder()
		pages.Assets(rr, httptest.NewRequest(http.MethodGet, bad, nil))
		if rr.Code == http.StatusOK {
			t.Errorf("%s was served with 200; only the embedded assets may be reachable", bad)
		}
	}
}

// No page may use a class that is styled only in a stylesheet it does not load.
//
// # The defect this exists for
//
// The room page's controls were written with `class="node"` — the workbench's
// button class. `.node` is defined in workbench.css, which the room page does
// not link, so every control rendered as a raw browser default: harsh borders,
// wrong shape, no hover. Nothing failed. The markup was valid, the page loaded,
// and the fences were green; it was only visible by looking at it.
//
// # Why it only flags classes defined SOMEWHERE
//
// Plenty of classes are hooks for scripts or exist only in markup, and those are
// not defects. What is always a defect is a class that some stylesheet in this
// repository styles, used on a page that loads a different set of stylesheets —
// that is somebody reaching for a component the page cannot see.
// The transcript can be searched, and the search is usable without a mouse or
// eyes (PRD AUD-06).
//
// # Why this is a markup fence and not a behaviour test
//
// The search itself is in assets/room-page.js: the whole transcript is already
// in the page, so it filters what is there rather than asking the server again.
// There is no JavaScript test harness in this repository, so what CAN be fenced
// here is the part the script depends on and a person depends on — that the
// controls exist, are labelled, and announce their result. If the markup below
// is removed, the script silently does nothing, and AUD-06 regresses with no
// other signal.
//
// The behaviour was verified in a browser against the real page when it landed;
// see docs/bugfix/2026-09-03-the-transcript-could-not-be-searched.md.
func TestTheTranscriptCanBeSearched(t *testing.T) {
	pages := NewPageHandlers(testDeps())
	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/rooms/rom_1", nil)
	r.SetPathValue("id", "rom_1")
	pages.RoomPage(rr, r)
	body := rr.Body.String()

	for _, want := range []struct{ frag, why string }{
		{`id="find"`,
			"the search input itself; room-page.js binds its input and keydown to it"},
		{`type="search"`,
			"so the browser offers its own clear affordance and assistive technology " +
				"calls it a search rather than a text field"},
		{`<label class="vis" for="find">`,
			"a label a screen reader can read. The placeholder is not one: it disappears " +
				"as soon as anybody types, which is exactly when the field needs naming"},
		{`id="find-clear"`,
			"clearing without a keyboard. Escape is the keyboard path and this is the other one"},
		{`id="find-count"`,
			"where the result count is written"},
		{`role="status"`,
			"so the count is ANNOUNCED. Filtering a list silently tells a screen-reader " +
				"user nothing happened; the count is the only feedback they get"},
	} {
		if !strings.Contains(body, want.frag) {
			t.Errorf("the room page has no %s.\nAUD-06 needs it: %s.", want.frag, want.why)
		}
	}

	// The transcript stays a live region for turns ARRIVING. room-page.js sets
	// aria-busy around a filter rebuild precisely because this is here; if the
	// attribute were dropped, the rebuild would stop being the thing that needs
	// suppressing and the busy dance would become dead code.
	if !strings.Contains(body, `id="turns" class="turns" aria-live="polite"`) {
		t.Error("the transcript is no longer an aria-live region; new turns would arrive silently")
	}
}

func TestNoPageUsesAClassStyledOnlyInAStylesheetItDoesNotLoad(t *testing.T) {
	pages := NewPageHandlers(testDeps())

	// Which classes each stylesheet defines.
	sheets, err := fs.Glob(assetFS, "assets/*.css")
	if err != nil {
		t.Fatal(err)
	}
	definedIn := map[string]map[string]bool{} // class -> set of stylesheets
	classRe := regexp.MustCompile(`\.([a-zA-Z][\w-]*)`)
	for _, path := range sheets {
		body, err := assetFS.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		name := strings.TrimPrefix(path, "assets/")
		// Selectors only: a class name appearing inside a comment or a property
		// value is not a definition.
		for _, block := range strings.Split(string(body), "{") {
			if i := strings.LastIndex(block, "}"); i >= 0 {
				block = block[i+1:]
			}
			block = stripCSSComments(block)
			for _, m := range classRe.FindAllStringSubmatch(block, -1) {
				if definedIn[m[1]] == nil {
					definedIn[m[1]] = map[string]bool{}
				}
				definedIn[m[1]][name] = true
			}
		}
	}
	if len(definedIn) < 40 {
		t.Fatalf("only %d class(es) found across %d stylesheet(s); the parse is not working",
			len(definedIn), len(sheets))
	}

	linkRe := regexp.MustCompile(`<link rel="stylesheet" href="/assets/([^"]+)"`)
	useRe := regexp.MustCompile(`class="([^"{}]*)"`)

	for _, page := range []struct{ name, path string }{
		{"room", "/rooms/rom_1"},
		{"workbench", "/workbench"},
		{"console", "/console"},
	} {
		t.Run(page.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, page.path, nil)
			r.SetPathValue("id", "rom_1")
			switch page.name {
			case "room":
				pages.RoomPage(rr, r)
			case "workbench":
				pages.Workbench(rr, r)
			case "console":
				pages.Console(rr, r)
			}
			html := rr.Body.String()

			loaded := map[string]bool{}
			for _, m := range linkRe.FindAllStringSubmatch(html, -1) {
				// Asset URLs carry a content hash; the stylesheet is the path.
				name, _, _ := strings.Cut(m[1], "?")
				loaded[name] = true
			}
			if len(loaded) == 0 {
				t.Fatalf("%s links no stylesheets", page.name)
			}

			for _, m := range useRe.FindAllStringSubmatch(html, -1) {
				for _, class := range strings.Fields(m[1]) {
					where := definedIn[class]
					if len(where) == 0 {
						continue // styled nowhere: a script or markup hook, not a defect
					}
					reachable := false
					for sheet := range where {
						if loaded[sheet] {
							reachable = true
							break
						}
					}
					if !reachable {
						t.Errorf("%s uses .%s, which is styled only in %v — none of which this page loads (%v). "+
							"It will render unstyled.", page.name, class, keysOf(where), keysOf(loaded))
					}
				}
			}
		})
	}
}

func stripCSSComments(s string) string {
	for {
		i := strings.Index(s, "/*")
		if i < 0 {
			return s
		}
		j := strings.Index(s[i:], "*/")
		if j < 0 {
			return s[:i]
		}
		s = s[:i] + s[i+j+2:]
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Assets may be cached hard, and only because their URL names their contents.
//
// # The defect this exists for
//
// Assets were served `public, max-age=300` from an unversioned URL with no
// ETag. For five minutes after every deploy, browsers ran the previous build's
// CSS and JavaScript against the new HTML — silently. The code comment claimed
// assets were "versioned by the build", which is what stopped anybody noticing
// that they were not.
//
// The properties below are what make a long cache safe, and each is asserted
// rather than assumed: a hard cache on a URL that does not name its contents is
// the bug, not the fix.
func TestAssetURLsNameTheirContentsSoTheyCanBeCachedHard(t *testing.T) {
	pages := NewPageHandlers(testDeps())

	// 1. The version is a content hash, not a constant. If it were the same for
	//    every asset, or the same across changes, the URL would name nothing.
	seen := map[string]string{}
	for name, version := range assetVersions {
		if version == "" {
			t.Errorf("%s has no version", name)
		}
		if other, clash := seen[version]; clash {
			t.Errorf("%s and %s share the version %q; it does not depend on their contents",
				name, other, version)
		}
		seen[version] = name

		body, err := assetFS.ReadFile("assets/" + name)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(body)
		if want := hex.EncodeToString(sum[:])[:12]; version != want {
			t.Errorf("%s is versioned %q but its contents hash to %q — the URL would not "+
				"change when the file does, which is the whole point", name, version, want)
		}
	}

	// 2. No asset a page references can go stale. Two ways to be safe, and every
	//    reference must take one of them: name its contents and be cached hard,
	//    or be revalidated on use. What must never happen again is the third
	//    thing — cached hard under a URL that does not name its contents.
	//
	//    The portraits take the second route: persona.PortraitURL builds a bare
	//    path and knows nothing about hashing, so they revalidate. That is a
	//    round trip, not a defect.
	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/rooms/rom_1", nil)
	r.SetPathValue("id", "rom_1")
	pages.RoomPage(rr, r)
	refs := regexp.MustCompile(`(?:href|src)="(/assets/[^"]+)"`).
		FindAllStringSubmatch(rr.Body.String(), -1)
	if len(refs) < 4 {
		t.Fatalf("the room page references only %d asset(s); the scan is not working", len(refs))
	}
	for _, ref := range refs {
		got := httptest.NewRecorder()
		pages.Assets(got, httptest.NewRequest(http.MethodGet, ref[1], nil))
		if got.Code != http.StatusOK {
			t.Errorf("the room page references %s, which serves %d", ref[1], got.Code)
			continue
		}
		cc := got.Header().Get("Cache-Control")
		versioned := strings.Contains(ref[1], "?v=")
		switch {
		case versioned && !strings.Contains(cc, "immutable"):
			t.Errorf("%s names its contents but is served %q; it could be cached hard", ref[1], cc)
		case !versioned && strings.Contains(cc, "immutable"):
			t.Errorf("%s is cached hard under a URL that does not name its contents (%q). "+
				"A deploy would leave it stale — this is the original defect.", ref[1], cc)
		case !versioned && got.Header().Get("ETag") == "":
			t.Errorf("%s revalidates but carries no ETag, so every check is a full download", ref[1])
		}
	}

	// 3. The versioned URL is immutable. This is the case every page hits, and it
	//    is safe precisely because a different build produces a different URL.
	got := httptest.NewRecorder()
	pages.Assets(got, httptest.NewRequest(http.MethodGet,
		"/assets/room.css?v="+assetVersions["room.css"], nil))
	if cc := got.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("a versioned asset is served %q; it names its own contents and can be cached hard", cc)
	}

	// 4. An UNVERSIONED request must revalidate. This is the exact shape of the
	//    original bug: served from cache without asking, and therefore stale.
	got = httptest.NewRecorder()
	pages.Assets(got, httptest.NewRequest(http.MethodGet, "/assets/room.css", nil))
	cc := got.Header().Get("Cache-Control")
	if strings.Contains(cc, "immutable") || strings.Contains(cc, "max-age=3") {
		t.Errorf("an unversioned asset is served %q; a deploy would leave it stale", cc)
	}
	if got.Header().Get("ETag") == "" {
		t.Error("an unversioned asset carries no ETag, so revalidation costs a full download")
	}

	// 5. And revalidation actually works, rather than merely being advertised.
	notModified := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/assets/room.css", nil)
	req.Header.Set("If-None-Match", `"`+assetVersions["room.css"]+`"`)
	pages.Assets(notModified, req)
	if notModified.Code != http.StatusNotModified {
		t.Errorf("a matching If-None-Match returned %d, want 304", notModified.Code)
	}
}
