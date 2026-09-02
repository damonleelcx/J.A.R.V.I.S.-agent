package httpapi

import (
	"net/http"
	"net/http/httptest"
	"regexp"
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
	for _, want := range []string{`src="/assets/pages.js"`, `href="/assets/shell.css"`, `data-page="verify"`} {
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

	for _, ok := range []string{"/assets/pages.js", "/assets/shell.css"} {
		rr := httptest.NewRecorder()
		pages.Assets(rr, httptest.NewRequest(http.MethodGet, ok, nil))
		if rr.Code != http.StatusOK {
			t.Errorf("%s returned %d, want 200", ok, rr.Code)
		}
		if rr.Body.Len() == 0 {
			t.Errorf("%s served an empty body", ok)
		}
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
