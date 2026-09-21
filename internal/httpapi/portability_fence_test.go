package httpapi

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/identity"
)

// What NFR-06 actually claims here, held to (issue 15).
//
// > NFR-06 Portability: desktop and web first; platform-independent contracts
// > and state
//
// # The honest reading
//
// "Desktop" means the workbench in a DESKTOP BROWSER. There is no Electron, no
// Tauri, no native shell, and none is planned in this repository — the issue
// that raised this is explicit that it is not an argument for building one. The
// half of NFR-06 that can be held and was not being held by anything is the
// second clause, and it is the one that decays quietly: every month the HTTP
// surface grows assuming a server-rendered page is the only client, a future
// shell of any kind gets more expensive.
//
// # What a platform-independent contract means in this codebase
//
// One thing, checkable: the `/v1` JSON API is the whole product, and the
// server-rendered pages are a shell around it. Concretely —
//
//   - nothing changes state except through `/v1`, so no client is privileged;
//   - every page route is a GET that renders, so nothing is reachable only by
//     navigating;
//   - a page carries no per-viewer state, so any other client gets everything
//     the workbench gets by asking `/v1` the same questions.
//
// A fence on each. They are cheap and they are the only thing standing between
// this claim and the ordinary drift of adding "just this one" form POST.

// pageRoute is one non-/v1 route, declared with what it is for.
//
// A table, so a new page route fails here by name rather than silently becoming
// a second surface. The reason column is the thing worth reading: every one of
// these exists because a browser must be given something before it can speak
// JSON, and none of them is a place where state lives.
var pageRoutes = []struct{ Method, Path, Why string }{
	{"GET", "/healthz", "liveness, for the orchestrator. No body worth parsing."},
	{"GET", "/readyz", "readiness, for the orchestrator."},
	{"GET", "/assets/", "the static files the shell loads."},
	{"GET", "/v1/meta/sigil", "an image."},
	{"GET", "/v1/meta/portrait", "an image."},
	{"GET", "/console", "the operations shell. Unauthenticated by design; every byte of DATA in it arrives over /v1."},
	{"GET", "/workbench", "the product's primary surface, and the whole of what 'desktop' means in NFR-06: this page in a desktop browser."},
	{"GET", "/rooms/{id}", "a room's shell."},
	{"GET", "/auth/sign-up", "a form that POSTs to /v1/auth/sign-up."},
	{"GET", "/auth/forgot-password", "a form that POSTs to /v1/auth/forgot-password."},
	{"GET", "/auth/verify-email", "a landing page for an emailed link. It RENDERS and lets the page POST: redemption is a state change and mail scanners follow every URL they see."},
	{"GET", "/auth/reset-password", "the same, for a reset link."},
	{"GET", "/", "the index."},
}

// routeRegistrations reads the mux registrations out of router.go's source.
//
// Source rather than the built mux because net/http's ServeMux does not expose
// what was registered on it. The AST rather than a line scan, for the reason
// TestNoHandlerAuthorisesByOwnerID gives: a pattern lives in a string literal,
// so that is what is examined.
func routeRegistrations(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "router.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc") {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		pattern, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		out = append(out, pattern)
		return true
	})
	if len(out) < 40 {
		t.Fatalf("only %d route(s) were read out of router.go; this fence would pass vacuously",
			len(out))
	}
	return out
}

// Nothing changes state except over the JSON contract.
//
// The moment one form POSTs to a page route, the server-rendered page stops
// being a client of the API and becomes a privileged one — and the second
// client anybody writes, of any kind, cannot do whatever that form did.
func TestEveryStateChangeIsOnTheJSONContract(t *testing.T) {
	for _, pattern := range routeRegistrations(t) {
		method, path, ok := strings.Cut(pattern, " ")
		if !ok {
			t.Errorf("route %q has no method; every registration in this router names one", pattern)
			continue
		}
		if method == http.MethodGet {
			continue
		}
		if !strings.HasPrefix(path, "/v1/") {
			t.Errorf("%s %s changes state and is not on the /v1 JSON contract.\n"+
				"PRD NFR-06 asks for platform-independent contracts: a mutation reachable only by "+
				"posting to a server-rendered page is one no other client can perform. Put it "+
				"under /v1 and have the page call it.", method, path)
		}
	}
}

// Every page route is declared, and every page route only reads.
func TestEveryPageRouteIsDeclaredAndOnlyRenders(t *testing.T) {
	declared := map[string]string{}
	for _, p := range pageRoutes {
		declared[p.Method+" "+p.Path] = p.Why
	}
	seen := map[string]bool{}

	for _, pattern := range routeRegistrations(t) {
		method, path, _ := strings.Cut(pattern, " ")
		// The /v1 surface is the contract, and its catch-all 404s are part of it.
		if strings.HasPrefix(path, "/v1/") && declared[pattern] == "" {
			continue
		}
		seen[pattern] = true
		if _, ok := declared[pattern]; !ok {
			t.Errorf("%s is a route outside the /v1 contract and is not declared in pageRoutes.\n"+
				"Every one of those is a surface a non-browser client cannot use, so each is "+
				"written down with why it has to exist. Add it with its reason, or serve it "+
				"under /v1.", pattern)
		}
		if method != http.MethodGet {
			t.Errorf("%s is a page route that is not a GET. A page renders; anything that acts "+
				"belongs on the JSON contract (PRD NFR-06).", pattern)
		}
	}
	for pattern := range declared {
		if !seen[pattern] {
			t.Errorf("pageRoutes declares %q and the router does not register it. A table that "+
				"outlives its routes stops describing anything.", pattern)
		}
	}
}

// A page carries no per-viewer state.
//
// This is the "and state" half of NFR-06, and it is what makes the shell
// replaceable. If the workbench HTML were personalised, a desktop shell — or a
// second web client, or a test harness — would have to scrape it to get what
// the browser gets. Rendered twice, once for somebody and once for nobody, and
// required to be byte-identical.
func TestAPageCarriesNoStateOfItsOwn(t *testing.T) {
	pages := map[string]http.HandlerFunc{}
	p := NewPageHandlers(testDeps())
	pages["/workbench"] = p.Workbench
	pages["/console"] = p.Console
	pages["/"] = p.Index
	pages["/auth/sign-up"] = p.SignUpPage
	pages["/auth/forgot-password"] = p.ForgotPasswordPage
	pages["/auth/verify-email"] = p.VerifyEmailPage
	pages["/auth/reset-password"] = p.ResetPasswordPage

	someone := &identity.User{ID: "usr_portability", Email: "someone@example.com",
		DisplayName: "Someone"}

	for path, handler := range pages {
		anon := httptest.NewRecorder()
		handler(anon, httptest.NewRequest(http.MethodGet, path, nil))

		signedIn := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		handler(signedIn, r.WithContext(context.WithValue(r.Context(), ctxKeyUser, someone)))

		if anon.Code != http.StatusOK {
			t.Errorf("GET %s answered %d", path, anon.Code)
			continue
		}
		if anon.Body.String() != signedIn.Body.String() {
			t.Errorf("GET %s renders differently for a signed-in caller.\n"+
				"PRD NFR-06 asks for platform-independent STATE: a page that carries who is "+
				"looking at it is a page another client would have to scrape. Everything "+
				"per-person comes over /v1, where any client can ask for it.", path)
		}
		if strings.Contains(signedIn.Body.String(), someone.Email) {
			t.Errorf("GET %s renders the caller's email address into the shell", path)
		}
	}
}
