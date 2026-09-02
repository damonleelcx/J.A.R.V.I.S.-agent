package httpapi

import (
	"embed"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/auth"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// assetFS carries the page stylesheet and script.
//
// They are served as files rather than inlined for a concrete reason: the
// application's CSP is `script-src 'self'`, which blocks inline <script>
// entirely. An earlier version of these pages inlined their script and the
// browser silently refused to run it — the confirm button did nothing and the
// reset form fell back to a native GET that dropped the token. Serving the
// script satisfies the policy without weakening it.
//
//go:embed assets/shell.css assets/avatar.css assets/pages.js
//go:embed assets/portrait/*.png
var assetFS embed.FS

// PageHandlers render the small set of browser pages reached from email.
//
// These are server-rendered with html/template rather than served from a
// front-end build. The reason is reliability, not preference: they are opened
// from a mail client, often on a phone, sometimes on a network that blocks
// third-party origins. A page that needs a bundle to load is a page that can
// fail to load at the exact moment a locked-out user needs it.
type PageHandlers struct {
	d    Deps
	tmpl *template.Template
}

// NewPageHandlers parses the templates once at startup.
func NewPageHandlers(d Deps) *PageHandlers {
	return &PageHandlers{d: d, tmpl: template.Must(template.New("pages").Parse(pageTemplates))}
}

type pageData struct {
	// Presence is FORGE's portrait with the state sigil badged onto it. Used on
	// surfaces with room for it; the sigil alone is used where there is not.
	Presence template.HTML
	// Sigil is FORGE's mark, rendered inline so the identity appears with the
	// page rather than after a second request that may not arrive.
	Sigil template.HTML
	// Page names which behaviour the shared script should attach. Rendered into
	// a data- attribute so one served script serves every page.
	Page string
	// Token is rendered into a data- attribute, not into script source. It is
	// already in the URL the reader followed, so this discloses nothing new —
	// but keeping it out of executable text means no escaping mistake can turn
	// it into script.
	Token    string
	MinChars int
	Title    string
}

func (p *PageHandlers) render(w http.ResponseWriter, r *http.Request, name string, data pageData) {
	data.MinChars = p.d.Config.Auth.MinPasswordLength
	if data.Sigil == "" {
		data.Sigil = template.HTML(persona.AvatarSVG(persona.StateIdle, 26))
	}
	if data.Presence == "" {
		data.Presence = persona.PresenceHTML(persona.StateIdle, 88)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// These pages carry a live credential in their URL. Caching one — in the
	// browser, or worse in a shared proxy — leaves that credential readable
	// after the fact.
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, private")
	// Referrer-Policy is already strict-origin-when-cross-origin globally, but
	// these pages need no-referrer specifically: a query-string token must never
	// leave in a Referer header when the page loads anything at all.
	w.Header().Set("Referrer-Policy", "no-referrer")

	if err := p.tmpl.ExecuteTemplate(w, name, data); err != nil {
		p.d.Log.ErrorWith(r.Context(), logx.EventHTTPPanic, err, "template", name)
	}
}

// VerifyEmailPage handles GET /auth/verify-email.
//
// It renders; it does not verify. Redemption happens on the POST the page makes
// when the reader presses the button.
//
// Why: mail scanners, link previewers, and corporate security gateways follow
// every URL in an incoming message. If GET consumed the token, the recipient
// would routinely arrive at "this link was already used" — used by a robot,
// seconds after delivery, before they ever saw the mail. This is the single most
// common way an email verification flow silently breaks in production.
func (p *PageHandlers) VerifyEmailPage(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	p.render(w, r, "verify", pageData{Page: "verify", Token: token, Title: "Confirm your email · FORGE"})
}

// ResetPasswordPage handles GET /auth/reset-password.
//
// Same reasoning as VerifyEmailPage, with more at stake: a scanner consuming a
// reset token would leave the user unable to reset their password at all.
func (p *PageHandlers) ResetPasswordPage(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	p.render(w, r, "reset", pageData{Page: "reset", Token: token, Title: "Set a new password · FORGE"})
}

// Index handles GET /.
func (p *PageHandlers) Index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		notFound(p.d.Log)(w, r)
		return
	}
	p.render(w, r, "index", pageData{Page: "index", Title: "FORGE"})
}

// Assets handles GET /assets/{file}.
//
// Only the two files embedded above are reachable: the handler resolves through
// the embedded FS, so there is no filesystem path to traverse out of.
func (p *PageHandlers) Assets(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/assets/")
	switch {
	case name == "shell.css", name == "avatar.css":
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case name == "pages.js":
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	case isPortraitAsset(name):
		w.Header().Set("Content-Type", "image/png")
	default:
		notFound(p.d.Log)(w, r)
		return
	}
	body, err := assetFS.ReadFile("assets/" + name)
	if err != nil {
		notFound(p.d.Log)(w, r)
		return
	}
	// Assets are versioned by the build, not by name, so caching is bounded
	// rather than immutable: a redeploy must not leave a stale script behind a
	// year-long max-age.
	w.Header().Set("Cache-Control", "public, max-age=300")
	http.ServeContent(w, r, name, time.Time{}, strings.NewReader(string(body)))
}

// isPortraitAsset reports whether name is one of the character portraits.
//
// Matched against the manifest rather than by extension, so the handler can only
// ever serve the four files the persona package declares — there is no path to
// traverse and no directory to enumerate.
func isPortraitAsset(name string) bool {
	for _, a := range persona.PortraitManifest() {
		if a.File == name {
			return true
		}
	}
	return false
}

// tokenLooksUsable is exported through the templates only as a rendering hint,
// never as authorisation. The server decides.
var _ = auth.LooksLikeToken

const pageTemplates = `
{{define "head"}}<!doctype html>
<html lang="en"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="referrer" content="no-referrer">
<title>{{.Title}}</title>
<link rel="stylesheet" href="/assets/shell.css">
<link rel="stylesheet" href="/assets/avatar.css">
</head><body><main class="panel" data-page="{{.Page}}" data-token="{{.Token}}">
<div class="mark">{{.Sigil}}<div class="wordmark">FORGE</div></div>
{{end}}

{{define "foot"}}</main><script src="/assets/pages.js"></script></body></html>{{end}}

{{define "index"}}{{template "head" .}}
<div class="forge-presence">
  {{.Presence}}
  <div>
    <h1 style="margin:0 0 4px">FORGE</h1>
    <p class="dim" style="margin:0">A durable engineering partner.</p>
  </div>
</div>
<p class="dim" style="margin-top:20px">FORGE reconstructs its state from a database
on every cycle, so it can be interrupted, restarted, and resumed without losing
what it was doing. It never claims a tool ran, a check passed, or a person
approved something that did not happen.</p>
<hr>
<p class="dim">This is the API host. The console is not part of this build yet —
identity is, and it is what everything else will authenticate against.</p>
<p class="dim">Health: <a href="/healthz">/healthz</a> · <a href="/readyz">/readyz</a><br>
Error dictionary: <a href="/v1/meta/error-codes">/v1/meta/error-codes</a></p>
{{template "foot" .}}{{end}}

{{define "verify"}}{{template "head" .}}
<h1>Confirm your email</h1>
{{if .Token}}
<p>Press the button to confirm this address and finish setting up your account.</p>
<p class="dim">The link is not used until you press it, so a mail scanner
following it cannot consume it before you do.</p>
<button class="btn" id="go">Confirm address</button>
<div class="note hidden" id="note"></div>
{{else}}
<div class="note bad"><strong>This link is missing its token.</strong><br>
Open the link from your email exactly as it was sent — some clients truncate
long URLs. If it keeps failing, request a new confirmation email.</div>
{{end}}
{{template "foot" .}}{{end}}

{{define "reset"}}{{template "head" .}}
<h1>Set a new password</h1>
{{if .Token}}
<p class="dim">Choose something long. Length is what is enforced —
{{.MinChars}} characters minimum — not a mix of symbols, because a passphrase
beats a mangled word.</p>
<form id="form" autocomplete="off">
  <label for="pw">New password</label>
  <input type="password" id="pw" autocomplete="new-password" minlength="{{.MinChars}}" required>
  <label for="pw2">Confirm new password</label>
  <input type="password" id="pw2" autocomplete="new-password" minlength="{{.MinChars}}" required>
  <p></p>
  <button class="btn" type="submit" id="go">Set password</button>
</form>
<div class="note hidden" id="note"></div>
<p class="dim" style="margin-top:16px">Setting a new password signs out every
device currently signed in to this account.</p>
{{else}}
<div class="note bad"><strong>This link is missing its token.</strong><br>
Open the link from your email exactly as it was sent. If it keeps failing,
request a new password reset.</div>
{{end}}
{{template "foot" .}}{{end}}
`
