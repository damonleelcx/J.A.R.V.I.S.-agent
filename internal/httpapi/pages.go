package httpapi

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"html/template"
	"io/fs"
	"net/http"
	"strconv"
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
//go:embed assets/shell.css assets/avatar.css assets/console.css assets/workbench.css
//go:embed assets/pages.js assets/console.js assets/workbench.js assets/forge3d.js
//go:embed assets/voice.js assets/orb.js
//go:embed assets/audio-input.js assets/room.js assets/room-page.js assets/room.css
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
	return &PageHandlers{
		d: d,
		tmpl: template.Must(template.New("pages").
			Funcs(template.FuncMap{"asset": assetURL}).
			Parse(pageTemplates)),
	}
}

// Asset URLs carry a content hash, and that is what makes caching them safe.
//
// # The defect this replaces
//
// Assets were served `public, max-age=300` from a URL with nothing version-like
// in it and no ETag. So for five minutes after a deploy, browsers served the
// PREVIOUS build's CSS and JavaScript against the new HTML — silently, and with
// no way for anybody to tell. The comment here used to claim assets were
// "versioned by the build"; they were not, and the claim is what kept anybody
// from noticing. It cost real time during wave 9.5, where a stylesheet change
// appeared not to work.
//
// # Why a content hash rather than the build version
//
// A build version changes on every release and would expire every asset each
// time, including the ones that did not change. A content hash changes exactly
// when the bytes change, which is the only thing that should invalidate a cache.
// It also means two builds that produce identical assets keep their caches.
var assetVersions = buildAssetVersions()

func buildAssetVersions() map[string]string {
	out := map[string]string{}
	// Walked rather than globbed, so nested assets — the portraits — are hashed
	// too. They are referenced by persona.PortraitURL, which builds a bare path
	// and knows nothing about this file; they therefore arrive unversioned and
	// take the revalidating branch below. Hashing them anyway gives them an ETag,
	// so that revalidation is a 304 rather than a full download of a PNG.
	_ = fs.WalkDir(assetFS, "assets", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		body, readErr := assetFS.ReadFile(path)
		if readErr != nil {
			return nil
		}
		sum := sha256.Sum256(body)
		out[strings.TrimPrefix(path, "assets/")] = hex.EncodeToString(sum[:])[:12]
		return nil
	})
	return out
}

// assetURL renders a cache-safe URL for an embedded asset.
//
// A name with no known hash is returned unversioned rather than failing the
// render: a page that loads without a stylesheet is bad, and a page that does
// not load at all is worse.
func assetURL(name string) string {
	if v, ok := assetVersions[name]; ok {
		return "/assets/" + name + "?v=" + v
	}
	return "/assets/" + name
}

type pageData struct {
	// Presence is FORGE's portrait with the state sigil badged onto it. Used on
	// surfaces with room for it; the sigil alone is used where there is not.
	Presence template.HTML
	// Sigil is FORGE's mark, rendered inline so the identity appears with the
	// page rather than after a second request that may not arrive.
	//
	// Used where a STATE indicator is wanted — badged onto the portrait on the
	// workbench stage. Headers use Avatar instead: see persona.AvatarHTML.
	Sigil template.HTML
	// Avatar is FORGE's portrait, for page headers. It is what people recognise
	// FORGE as; the sigil on its own is an abstract mark beside a wordmark that
	// already says the same thing.
	Avatar template.HTML
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
	// RoomID is rendered into a data- attribute for the room page, for the same
	// reason Token is: it stays out of executable text, so no escaping mistake
	// can turn a path segment into script.
	RoomID string

	// Tagline, PersonaVersion and Soul carry FORGE's identity onto the page.
	//
	// # Why the soul is server-rendered rather than fetched
	//
	// persona.Soul exists so that "a user can read exactly what FORGE was told
	// to be, at the version that was in force when it acted" (see the package
	// doc). A commitment set that is only readable by someone with repository
	// access is not that; it is a comment. Rendering it into the page the person
	// actually talks to FORGE on is what makes the claim true.
	//
	// Rendered rather than served from a new endpoint because it is static text
	// that changes with a version bump: an endpoint would add a public contract
	// to maintain for something the page already has at render time.
	Tagline        string
	PersonaVersion int
	Soul           []persona.Commitment
}

func (p *PageHandlers) render(w http.ResponseWriter, r *http.Request, name string, data pageData) {
	data.MinChars = p.d.Config.Auth.MinPasswordLength
	if data.Sigil == "" {
		data.Sigil = template.HTML(persona.AvatarSVG(persona.StateIdle, 26))
	}
	if data.Presence == "" {
		data.Presence = persona.PresenceHTML(persona.StateIdle, 88)
	}
	if data.Avatar == "" {
		data.Avatar = persona.AvatarHTML(30)
	}
	data.Tagline = persona.Tagline
	data.PersonaVersion = persona.Version
	data.Soul = persona.Soul

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

// Console handles GET /console.
//
// The page is a shell; everything in it is fetched from the API. That keeps ONE
// implementation of every rule — which avatar state a goal is in, whether a task
// counts as verified — on the server, where the engine already decides it. A
// console that recomputes those rules is a second authority that will eventually
// disagree with the first.
func (p *PageHandlers) Console(w http.ResponseWriter, r *http.Request) {
	p.render(w, r, "console", pageData{Page: "console", Title: "FORGE"})
}

// Workbench handles GET /workbench — the product's primary surface.
//
// Voice and the 3D studio, per PRD §1.2. The operations console at /console is
// the surface beside it, not the main one.
func (p *PageHandlers) Workbench(w http.ResponseWriter, r *http.Request) {
	// The presence is rendered at the size the workbench header uses. The state
	// it starts in is idle, and the page updates it from the server's own
	// endpoints as the conversation moves — the state-to-expression rule stays
	// on the server, where the console already gets it from.
	p.render(w, r, "workbench", pageData{
		Page:     "workbench",
		Title:    "FORGE workbench",
		Presence: persona.PresenceHTML(persona.StateIdle, 36),
	})
}

// RoomPage handles GET /rooms/{id} — a shared session in the browser.
//
// # Why this is its own surface rather than part of the workbench
//
// The workbench is one person building with FORGE. A room is several people
// talking, and it carries a live media connection, a microphone, and a privacy
// state that must be visible at all times. Folding that into the workbench would
// put every room defect inside the product's primary surface — and the workbench
// works today.
//
// The room client is written as a module (assets/room.js) with no dependency on
// this page, so the workbench can mount it later without either being rewritten.
// That is the evolutionary order: make it work somewhere it cannot break
// anything, then place it.
func (p *PageHandlers) RoomPage(w http.ResponseWriter, r *http.Request) {
	p.render(w, r, "room", pageData{
		Page:     "room",
		Title:    "FORGE room",
		RoomID:   r.PathValue("id"),
		Presence: persona.PresenceHTML(persona.StateIdle, 36),
	})
}

// Sigil handles GET /v1/meta/sigil.
//
// Served rather than drawn in the browser so there is one implementation of
// FORGE's mark and one place its state rules live.
func (p *PageHandlers) Sigil(w http.ResponseWriter, r *http.Request) {
	state := persona.AvatarState(r.URL.Query().Get("state"))
	if !state.Valid() {
		state = persona.StateIdle
	}
	size := 26
	if v := r.URL.Query().Get("size"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 512 {
			size = n
		}
	}
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write([]byte(persona.AvatarSVG(state, size)))
}

// Portrait handles GET /v1/meta/portrait — the expression for a given state.
//
// Mapping state to expression on the server keeps that decision in one place
// too: several states share one expression, and which ones is a product
// judgement rather than a lookup a client should re-derive.
func (p *PageHandlers) Portrait(w http.ResponseWriter, r *http.Request) {
	state := persona.AvatarState(r.URL.Query().Get("state"))
	if !state.Valid() {
		state = persona.StateIdle
	}
	expr := persona.ExpressionFor(state)
	body, err := assetFS.ReadFile("assets/portrait/" + string(expr) + ".png")
	if err != nil {
		// A missing portrait falls back to the sigil rather than a broken image:
		// a decorative asset must never be able to take out a status indicator.
		p.d.Log.WarnWith(r.Context(), logx.EventHTTPRejected, err,
			"expression", string(expr), "detail", "portrait asset missing; serving the sigil instead")
		w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
		_, _ = w.Write([]byte(persona.AvatarSVG(state, 128)))
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(body)
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
	case name == "shell.css", name == "avatar.css", name == "console.css",
		name == "workbench.css", name == "room.css":
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case name == "pages.js", name == "console.js", name == "workbench.js",
		name == "forge3d.js", name == "voice.js", name == "orb.js",
		name == "audio-input.js", name == "room.js", name == "room-page.js":
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
	// An ETag on every response, so even a request that arrives without a
	// version — a bookmark, an old cached page — revalidates cheaply into a 304
	// rather than being served stale or re-downloaded whole.
	version := assetVersions[name]
	if version != "" {
		w.Header().Set("ETag", `"`+version+`"`)
	}
	if r.URL.Query().Get("v") == version && version != "" {
		// The URL names these exact bytes, so it can never go stale: a different
		// build produces a different URL. This is the case every page hits.
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		// Unversioned, or naming a version this build does not have. Must be
		// revalidated: this is precisely how a stale asset used to be served.
		w.Header().Set("Cache-Control", "no-cache")
	}
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
<link rel="stylesheet" href="{{asset "shell.css"}}">
<link rel="stylesheet" href="{{asset "avatar.css"}}">
</head><body><main class="panel" data-page="{{.Page}}" data-token="{{.Token}}">
<div class="mark">{{.Avatar}}<div class="wordmark">FORGE</div></div>
{{end}}

{{define "foot"}}</main><script src="{{asset "pages.js"}}"></script></body></html>{{end}}

{{define "workbench"}}<!doctype html>
<html lang="en"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Title}}</title>
<link rel="stylesheet" href="{{asset "shell.css"}}">
<link rel="stylesheet" href="{{asset "avatar.css"}}">
<link rel="stylesheet" href="{{asset "console.css"}}">
<link rel="stylesheet" href="{{asset "workbench.css"}}">
</head><body class="wb">

<div class="wb-top">
  <!-- The sigil only. FORGE's portrait is on the stage, at the centre of the
       workbench, and two of her on one screen is one too many. -->
  <button type="button" class="whois" id="whois" aria-expanded="false" aria-controls="soul"
          title="Who FORGE is, and what it will not do">
    <span id="top-sigil" class="top-sigil">{{.Avatar}}</span>
    <span class="whois-txt">
      <span class="wordmark">FORGE</span>
      <span class="tag">{{.Tagline}}</span>
    </span>
  </button>
  <span id="meta" style="font-size:11.5px;color:var(--ink-dim)"></span>
  <div class="spacer"></div>
  <span id="models" style="font-size:11px;color:var(--ink-dim)"></span>
  <span class="who" id="who"></span>
  <a href="/console" style="font-size:12px">Operations</a>
</div>

<!-- The soul. Not decoration and not marketing: this is the exact commitment
     set FORGE is given, at the version that was in force, with the reason each
     one exists. PRD RSN-04 makes the immutable ones un-relaxable by any
     configuration, and a commitment nobody can read is one nobody can hold it
     to. -->
<div class="soul hidden" id="soul" role="dialog" aria-label="What FORGE will and will not do">
  <div class="soul-head">
    <div>
      <b>What I will and will not do</b>
      <div class="soul-sub">persona v{{.PersonaVersion}} · the same text FORGE is given before every call</div>
    </div>
    <button type="button" class="ghost" id="soul-close" aria-label="Close">Close</button>
  </div>
  <div class="soul-body">
    {{range .Soul}}
    <div class="vow{{if .Immutable}} fixed{{end}}">
      <div class="vow-id">{{.ID}}{{if .Immutable}} · cannot be relaxed{{end}}</div>
      <div class="vow-text">{{.Text}}</div>
      <div class="vow-why">{{.Why}}</div>
    </div>
    {{end}}
  </div>
</div>

<div class="wb-body">

  <!-- Conversation. The control plane (PRD §2.3). -->
  <div class="wb-left">
    <div class="railhead">Conversation</div>
    <div class="transcript" id="transcript"></div>
  </div>

  <!-- The stage. Holds the model AND the voice surface: the voice surface is
       centred here until there is something to look at, then docks to the
       bottom-left corner. One element, two placements — see workbench.css. -->
  <div class="stage" id="stage">
    <canvas id="canvas"></canvas>
    <!-- Dimension text, as HTML over the canvas rather than glyphs in GL (PRD
         VIS-03). aria-hidden because every number here is also in the parts
         panel, and a screen reader walking absolutely-positioned fragments in
         paint order would read them in an arbitrary sequence. -->
    <div class="dimlayer" id="dimlayer" aria-hidden="true"></div>
    <div class="stage-empty hidden" id="stage-empty"></div>

    <div class="viewctl">
      <button data-view="iso" aria-pressed="true">Iso</button>
      <button data-view="front" aria-pressed="false">Front</button>
      <button data-view="top" aria-pressed="false">Top</button>
      <button data-view="side" aria-pressed="false">Side</button>
      <button id="reset">Fit</button>
      <label style="font-size:11.5px;color:var(--ink-dim);display:flex;align-items:center;gap:5px;padding-left:5px">
        <input type="checkbox" id="grid" checked> grid
      </label>
      <label style="font-size:11.5px;color:var(--ink-dim);display:flex;align-items:center;gap:5px">
        <input type="checkbox" id="dims"> dimensions
      </label>
    </div>

    <!-- Assembly states (PRD VIS-02). Hidden until an assembly has any: an
         empty picker implies configurations somebody chose not to make. -->
    <div class="states hidden" id="states"></div>

    <div class="sliders">
      <label for="explode">Exploded view</label>
      <input type="range" id="explode" min="0" max="1" step="0.01" value="0">
      <label for="opacity">Transparency</label>
      <input type="range" id="opacity" min="0.15" max="1" step="0.01" value="1">
      <label for="section">Section cut</label>
      <select id="section" style="width:100%;margin-bottom:8px;background:#0f131b;color:var(--ink);border:1px solid var(--edge-solid);border-radius:6px;padding:4px">
        <option value="none">none</option>
        <option value="x">along X</option>
        <option value="y">along Y</option>
        <option value="z">along Z</option>
      </select>
      <input type="range" id="sectionat" min="0" max="1" step="0.01" value="0.5">
    </div>

    <!-- Never dismissible: PRD VIS-06. -->
    <div class="provenance hidden" id="provenance"></div>

    <div class="voice" id="voice" data-place="hero">
      <!-- FORGE. The character portrait and the state sigil, both served by the
           application (/v1/meta/portrait and /v1/meta/sigil) so the rule mapping
           a state onto an expression stays in persona.ExpressionFor and is not
           re-decided here. The canvas behind them is the aura only — it draws no
           part of her. Reuses .forge-portrait from avatar.css, which already
           carries the circular crop and the gold trim. -->
      <div class="orb">
        <canvas id="orb"></canvas>
        <div class="orb-face forge-portrait" id="orb-face">
          <img id="orb-portrait" src="/v1/meta/portrait?state=idle" alt="" aria-hidden="true"
               width="512" height="512" decoding="async">
          <span class="forge-portrait__badge" id="orb-badge">{{.Sigil}}</span>
        </div>
      </div>
      <div class="voice-said" id="caption">Describe something you are building.</div>
      <div class="voice-state" id="statusword">Ready</div>
      <div class="voice-ctl">
        <button type="button" class="node mic" id="mic" aria-pressed="false"
                title="Hold to talk (or hold the space bar)" aria-label="Hold to talk">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"
               stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <rect x="9" y="2" width="6" height="11" rx="3"></rect>
            <path d="M5 11a7 7 0 0 0 14 0"></path>
            <path d="M12 18v3"></path>
          </svg>
        </button>
        <form id="sayform">
          <input type="text" id="say" placeholder="…or type it" autocomplete="off">
          <!-- PRD VIS-01: a sketch or a photograph is an input to geometry, not
               an attachment to a message. Read in the browser and sent with the
               turn — there is no asset store to upload to, and a picture used
               once does not need one. -->
          <label class="node attach" id="attachbtn" title="Attach a sketch or photograph">
            <input type="file" id="attach" accept="image/*" multiple hidden>
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"
                 stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
              <rect x="3" y="3" width="18" height="18" rx="2"></rect>
              <circle cx="8.5" cy="8.5" r="1.5"></circle>
              <path d="M21 15l-5-5L5 21"></path>
            </svg>
          </label>
          <!-- An explicit submit control. Implicit submission (Enter in a
               single-field form) is not relied on: it is easy to lose to a
               stray handler, and losing it silently would leave the ONLY
               non-audio path with no way to send. PRD AUD-06. -->
          <button type="submit" class="node send" id="send" title="Send" aria-label="Send">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"
                 stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
              <path d="M5 12h13"></path><path d="M12 5l7 7-7 7"></path>
            </svg>
          </button>
        </form>
        <div class="attached hidden" id="attached"></div>
        <span class="ctl-div" aria-hidden="true"></span>
        <!-- Stop speaking. Icon-only inside the console bar, but always present
             and always clickable (PRD AUD-07), with the accessible name on the
             button rather than in a label that only exists when it is relevant.
             It brightens while FORGE is actually speaking, so the affordance is
             loudest at the moment it means something. -->
        <button type="button" class="node stop" id="stopspeak"
                title="Stop speaking (Escape)" aria-label="Stop speaking">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"
               stroke-linejoin="round" aria-hidden="true">
            <rect x="7" y="7" width="10" height="10" rx="2"></rect>
          </svg>
        </button>
      </div>
      <div class="voice-opts">
        <label class="sw"><input type="checkbox" id="handsfree"><span>hands-free</span></label>
        <label class="sw"><input type="checkbox" id="speakback" checked><span>speak replies</span></label>
        <span id="voice-note" class="bad hidden"></span>
      </div>
    </div>
  </div>

  <!-- Artifacts, evidence, and the one place work can be started. -->
  <div class="wb-right rail">
    <div class="h">Parts</div>
    <div id="parts"></div>

    <!-- Variants (PRD VIS-04). Every shape FORGE proposes in this conversation
         is kept as a version, so an earlier one is still there to be compared
         with a later one. Pick two or more and open them side by side. -->
    <div class="h" id="variants-head" style="display:none">Variants</div>
    <div id="variants"></div>

    <div class="h" id="proposal-head" style="display:none">Proposed work</div>
    <div id="proposal" class="hidden"></div>
  </div>

</div>

<!-- Side by side. A dialog rather than a route: the conversation underneath is
     the thing being decided about, and navigating away from it to compare two of
     its outputs would lose the context that made the comparison worth doing. -->
<div class="compare hidden" id="compare" role="dialog" aria-modal="true" aria-label="Variants side by side">
  <div class="compare-head">
    <div class="compare-title" id="compare-title">Side by side</div>
    <button type="button" class="node" id="compare-close" aria-label="Close">Close</button>
  </div>
  <div class="compare-body" id="compare-body"></div>
</div>

<script src="{{asset "forge3d.js"}}"></script>
<script src="{{asset "voice.js"}}"></script>
<script src="{{asset "orb.js"}}"></script>
<script src="{{asset "workbench.js"}}"></script>
</body></html>{{end}}

{{define "room"}}<!doctype html>
<html lang="en"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Title}}</title>
<link rel="stylesheet" href="{{asset "shell.css"}}">
<link rel="stylesheet" href="{{asset "avatar.css"}}">
<link rel="stylesheet" href="{{asset "console.css"}}">
<link rel="stylesheet" href="{{asset "room.css"}}">
</head><body class="room" data-room="{{.RoomID}}">

<div class="room-top">
  {{.Avatar}}
  <span class="wordmark">FORGE</span>
  <span class="room-title" id="room-title">Room</span>
  <span class="room-status" id="room-status">connecting</span>
</div>

<!-- SEC-06's visible state. Never dismissible, and never abbreviated to an icon:
     it is the only place the room says that transcription sends audio to a
     speech provider, and an icon cannot say that. Rendered from the server's own
     sentence so every client says the same thing. -->
<div class="room-policy" id="policy" role="status">
  <span class="room-policy__dot" id="policy-dot" aria-hidden="true"></span>
  <span id="policy-text">Reading what happens to what is said here…</span>
</div>

<main class="room-main">
  <section class="room-transcript" aria-label="Transcript">
    <div class="room-h">Transcript</div>

    <!-- AUD-06: transcript search.
         Asks GET /v1/rooms/{id}/search, which matches words rather than
         characters. This filtered in the browser first, and that was right for
         what it could do: the whole transcript is already in the page, so
         filtering it was instant and worked the same on a closed room.

         What it could not do is agree with itself. A substring filter finds
         "brackets" when you type "bracket" and finds nothing when you type
         "brackets", because containment runs one way only. Postgres stems both
         to the same lexeme. The cost paid for that is a round trip per pause in
         typing, and one matcher instead of two.

         Clearing the box still needs no server — the record is all here.
         Why the search moved, and what was verified: docs/bugfix/2026-09-03-the-transcript-could-not-be-searched.md

         type=search so the browser offers its own clear affordance; the Clear
         button and Escape are the paths that do not depend on the browser
         providing one. -->
    <div class="room-find">
      <label class="vis" for="find">Search the transcript</label>
      <input type="search" id="find" class="room-find__in" autocomplete="off"
             placeholder="Search the transcript">
      <button type="button" class="room-btn" id="find-clear" hidden>Clear</button>
    </div>
    <!-- The count is its own status region. #turns below is aria-live for turns
         ARRIVING; a filter rewrites all of it at once, which is not news and must
         not be read out. What a screen reader needs after typing is how many
         turns matched, and that is this line. -->
    <div id="find-count" class="room-find__count" role="status"></div>

    <div id="turns" class="turns" aria-live="polite"></div>
  </section>

  <aside class="room-side">
    <div class="room-h">In the room</div>
    <ul id="roster" class="roster"></ul>

    <div class="room-h">Microphone</div>
    <div class="room-mic">
      <label class="vis" for="micpick">Microphone</label>
      <select id="micpick" aria-label="Microphone"></select>
      <button type="button" class="room-btn primary" id="joinaudio">Join audio</button>
      <div id="micnote" class="note"></div>
    </div>
  </aside>
</main>

<!-- AUD-07. Always present, always reachable, never hidden behind a menu: these
     are the controls somebody reaches for when they want something to stop, and
     hunting for a control is the one thing that must not be required then. They
     are disabled rather than removed when audio is not running, so their
     position never moves. -->
<div class="room-ctl" role="group" aria-label="Room controls">
  <button type="button" class="room-btn" id="mute" aria-pressed="false" disabled>Mute</button>
  <button type="button" class="room-btn" id="pause" aria-pressed="false" disabled>Pause</button>
  <button type="button" class="room-btn" id="transcribe" aria-pressed="true">Stop transcribing</button>
  <button type="button" class="room-btn danger" id="delvoice">Delete my speech</button>
  <button type="button" class="room-btn" id="leave">Leave</button>

  <!-- AUD-06: every critical interaction has a path that needs no microphone.
       This is not a fallback for a broken mic; it is the equal path. -->
  <form id="sayform" class="room-say">
    <input type="text" id="say" placeholder="Type instead of speaking" autocomplete="off">
    <button type="submit" class="room-btn primary" id="send">Send</button>
  </form>
</div>

<div id="err" class="room-err hidden" role="alert"></div>

<script src="{{asset "audio-input.js"}}"></script>
<script src="{{asset "room.js"}}"></script>
<script src="{{asset "room-page.js"}}"></script>
</body></html>{{end}}

{{define "console"}}<!doctype html>
<html lang="en"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Title}}</title>
<link rel="stylesheet" href="{{asset "shell.css"}}">
<link rel="stylesheet" href="{{asset "avatar.css"}}">
<link rel="stylesheet" href="{{asset "console.css"}}">
</head><body class="console">
<div class="topbar">
  {{.Avatar}}
  <div class="wordmark">FORGE</div>
  <div class="who"><span id="whoami"></span><a href="/">Home</a></div>
</div>
<div id="err" class="note bad hidden" style="margin:16px 22px"></div>

<div id="signin" class="hidden" style="max-width:380px;margin:64px auto;padding:0 22px">
  <div class="card">
    <h2 style="margin-bottom:16px">Sign in</h2>
    <div id="signin-note" class="note hidden" style="margin-bottom:14px"></div>
    <form id="signin-form" autocomplete="on">
      <label for="email">Email</label>
      <input type="email" id="email" autocomplete="username" required>
      <label for="password">Password</label>
      <input type="password" id="password" autocomplete="current-password" required>
      <p></p>
      <button class="btn" type="submit" id="signin-go">Sign in</button>
    </form>
  </div>
</div>

<div id="main" class="layout">
  <div>
    <div class="card"><h2>Waiting for you</h2><div id="approvals"><div class="spin">Loading…</div></div></div>
    <div class="card"><h2>Goals</h2><div id="goals"><div class="spin">Loading…</div></div></div>
  </div>
  <div id="detail" class="hidden"></div>
</div>
<script src="{{asset "console.js"}}"></script>
</body></html>{{end}}

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
<p><a class="btn" href="/workbench">Open the workbench</a></p>
<p class="dim" style="margin-top:10px"><a href="/console">Operations console</a> — goals, timeline, approvals.</p>
<p class="dim" style="margin-top:16px">Health: <a href="/healthz">/healthz</a> · <a href="/readyz">/readyz</a><br>
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
