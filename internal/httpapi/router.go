package httpapi

import (
	"net/http"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/access"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/identity"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Deps is everything the HTTP surface needs.
type Deps struct {
	// Access decides who may do what (PRD SEC-02). Every handler asks this and
	// none of them queries forge_projects.owner_id: membership is the single
	// authorisation truth, and owner_id records only who created the project.
	Access   *access.Service
	Config   *config.Config
	Pool     *db.Pool
	Identity *identity.Service
	// LLM backs the workbench conversation. Nil is legal: the API and the
	// operations console work without a model, and the workbench says so rather
	// than failing to load.
	LLM   llm.Client
	Clock clock.Clock
	Log   *logx.Logger
	// Version and Commit are reported by the health endpoint so an operator can
	// confirm which build is actually answering.
	Version string
	Commit  string
}

// authRateLimits are the per-address ceilings on the unauthenticated endpoints.
//
// These are a blunt second line, not the primary control — account lockout in
// the database is the real defence against credential guessing, and it is the
// one every instance sees. The values are deliberately loose enough that a
// person retrying a forgotten password never meets them.
var authRateLimits = struct {
	SignIn, SignUp, Reset, Verify int
	Window                        time.Duration
}{
	SignIn: 20,
	SignUp: 10,
	Reset:  8,
	Verify: 30,
	Window: time.Minute,
}

// NewRouter builds the HTTP handler.
func NewRouter(d Deps) http.Handler {
	mux := http.NewServeMux()

	// Access control is wired here rather than by every caller, so a deployment
	// cannot start with it nil by omission. requirePermission refuses outright
	// when it is, but a server that refuses everything is a bad way to find out.
	if d.Access == nil && d.Pool != nil {
		d.Access = access.NewService(d.Pool, d.Clock, d.Log)
	}

	authHandlers := NewAuthHandlers(d.Identity, d.Config.Auth, d.Log)
	health := NewHealthHandlers(d)

	// Per-endpoint limiters rather than one shared bucket: a burst of sign-ups
	// must not consume the allowance that a legitimate user's sign-in needs.
	limitSignIn := NewRateLimiter(authRateLimits.SignIn, authRateLimits.Window, d.Clock)
	limitSignUp := NewRateLimiter(authRateLimits.SignUp, authRateLimits.Window, d.Clock)
	limitReset := NewRateLimiter(authRateLimits.Reset, authRateLimits.Window, d.Clock)
	limitVerify := NewRateLimiter(authRateLimits.Verify, authRateLimits.Window, d.Clock)

	limited := func(rl *RateLimiter, h http.HandlerFunc) http.Handler {
		return LimitByIP(rl, d.Log)(h)
	}
	authed := func(h http.HandlerFunc) http.Handler {
		return authHandlers.RequireAuth(h)
	}

	// --- health and metadata (unauthenticated) ---
	mux.HandleFunc("GET /healthz", health.Live)
	mux.HandleFunc("GET /readyz", health.Ready)
	mux.HandleFunc("GET /v1/meta/error-codes", health.ErrorCodes)

	// --- identity (unauthenticated, rate limited) ---
	mux.Handle("POST /v1/auth/sign-up", limited(limitSignUp, authHandlers.SignUp))
	mux.Handle("POST /v1/auth/sign-in", limited(limitSignIn, authHandlers.SignIn))
	mux.Handle("POST /v1/auth/verify-email", limited(limitVerify, authHandlers.VerifyEmail))
	mux.Handle("POST /v1/auth/resend-verification", limited(limitReset, authHandlers.ResendVerification))
	mux.Handle("POST /v1/auth/forgot-password", limited(limitReset, authHandlers.RequestPasswordReset))
	mux.Handle("POST /v1/auth/reset-password", limited(limitReset, authHandlers.ResetPassword))

	// Sign-out tolerates an absent or dead session: the caller's goal is "end my
	// session", and a request that arrives with nothing to end has already
	// achieved it. Requiring auth here would answer 401 to someone asking to be
	// signed out, which is absurd.
	mux.Handle("POST /v1/auth/sign-out", authHandlers.OptionalAuth(http.HandlerFunc(authHandlers.SignOut)))

	// --- identity (authenticated) ---
	mux.Handle("GET /v1/auth/me", authed(authHandlers.Me))
	mux.Handle("GET /v1/auth/sessions", authed(authHandlers.ListSessions))
	mux.Handle("POST /v1/auth/sign-out-all", authed(authHandlers.SignOutAll))
	mux.Handle("POST /v1/auth/change-password", authed(authHandlers.ChangePassword))

	// --- goals and approvals (authenticated) ---
	goals := NewGoalHandlers(d)
	mux.Handle("GET /v1/goals", authed(goals.ListGoals))
	// Creating a goal PLANS it and starts nothing; starting it is the separate
	// act on the line below. PRD AGT-02 wants the plan visible before the work,
	// and one endpoint that did both would hide it. See goals_start.go.
	mux.Handle("POST /v1/goals", authed(goals.CreateGoal))
	// Replanning DRAFTS work and authorises none, so it needs goal.create rather
	// than goal.start. It exists because planning is a model call that can time
	// out, and what survives is a draft with no tasks that cannot be started.
	mux.Handle("POST /v1/goals/{id}/plan", authed(goals.Replan))
	mux.Handle("POST /v1/goals/{id}/start", authed(goals.StartGoal))
	mux.Handle("GET /v1/goals/{id}", authed(goals.GetGoal))
	mux.Handle("GET /v1/goals/{id}/timeline", authed(goals.Timeline))
	mux.Handle("GET /v1/approvals", authed(goals.ListApprovals))
	mux.Handle("POST /v1/approvals/{id}", authed(goals.Decide))

	// --- memory and the decision log (authenticated) ---
	// PRD MEM-02 is a USER requirement, so it is an API rather than only a
	// forgectl command: the person whose memory it is has to be able to inspect,
	// correct, pin, expire, export and delete it, and to see why an item came
	// back. The layer table itself is unauthenticated — it describes the build,
	// not anybody's memory.
	mem := NewMemoryHandlers(d)
	mux.HandleFunc("GET /v1/memory/layers", mem.Layers)
	mux.Handle("GET /v1/memory", authed(mem.List))
	mux.Handle("GET /v1/memory/recall", authed(mem.Recall))
	mux.Handle("GET /v1/memory/export", authed(mem.Export))
	mux.Handle("PATCH /v1/memory/{id}", authed(mem.UpdateItem))
	// DELETE forgets rather than erases: the key stays claimed so the agent
	// cannot re-learn what a user deleted. Purging is deliberately NOT here —
	// it is the act that undoes a user's deletion record, and it belongs to an
	// operator with a shell, not to a stray DELETE with a query parameter.
	mux.Handle("DELETE /v1/memory/{id}", authed(mem.ForgetItem))

	mux.Handle("GET /v1/decisions", authed(mem.ListDecisions))
	mux.Handle("POST /v1/decisions", authed(mem.RecordDecision))
	mux.Handle("GET /v1/decisions/{id}", authed(mem.GetDecision))

	// --- the workspace model (PRD RSN-01, WRK-03, WRK-04) ---
	// RSN-01 asks for an EDITABLE structure separate from the transcript, so it
	// is an API rather than only a forgectl command. Note what is NOT here: no
	// way to change a node's kind. An assumption that turns out to be true does
	// not become a requirement — it is promoted, and both stay readable.
	ws := NewWorkspaceHandlers(d)
	mux.HandleFunc("GET /v1/workspace/kinds", ws.Kinds)
	mux.Handle("GET /v1/workspace/graph", authed(ws.Graph))
	mux.Handle("GET /v1/workspace/review", authed(ws.Review))
	mux.Handle("POST /v1/workspace/nodes", authed(ws.AddNode))
	mux.Handle("PATCH /v1/workspace/nodes/{id}", authed(ws.EditNode))
	mux.Handle("POST /v1/workspace/nodes/{id}/promote", authed(ws.Promote))
	mux.Handle("POST /v1/workspace/edges", authed(ws.Relate))
	mux.Handle("DELETE /v1/workspace/edges/{id}", authed(ws.Unrelate))
	mux.Handle("GET /v1/workspace/artifacts/{id}", authed(ws.ArtifactHistory))
	// Recording a change is the AGENT's path, not a person's: WRK-04's seven
	// facts include the tool call that made it, and a change arriving over HTTP
	// with a tool call named by the client would be a fabricated ledger entry.
	// Only the disposition — what a person decided — is exposed here.
	mux.Handle("POST /v1/workspace/versions/{id}/disposition", authed(ws.Dispose))

	// --- geometry: variants, comparison, export (PRD VIS-04, VIS-05) ---
	// A variant is an artifact VERSION, so there is no create endpoint here:
	// geometry is written by the server at the moment it is produced, in
	// /v1/converse, because that is the only place that knows the prompt, the
	// model and the shape together. A client posting geometry would be naming
	// its own generator, which is a fabricated ledger entry — the same rule that
	// keeps RecordChange off the HTTP surface.
	geo := NewGeometryHandlers(d)
	// Unauthenticated: it describes what this build can write, not anybody's work.
	mux.HandleFunc("GET /v1/geometry/formats", geo.Formats)
	mux.Handle("GET /v1/geometry", authed(geo.List))
	// Registered before the {id} pattern reads: Go's mux prefers the literal
	// segment, so "compare" cannot be swallowed as a variant id.
	mux.Handle("GET /v1/geometry/compare", authed(geo.Compare))
	mux.Handle("GET /v1/geometry/{id}", authed(geo.Get))
	// Adopting an earlier variant PROPOSES it again as the current version; it
	// signs nothing off. The sign-off is the separate act on the version this
	// creates, through the disposition endpoint above.
	mux.Handle("POST /v1/geometry/{id}/adopt", authed(geo.Adopt))
	mux.Handle("GET /v1/geometry/{id}/export", authed(geo.Export))
	mux.Handle("GET /v1/geometry/{id}/export/label", authed(geo.ExportLabel))

	// --- the shared session (PRD COL-01) ---
	//
	// One RoomHandlers for the process, not one per request: it owns the live
	// fan-out hub, and a hub per request would fan out to nobody.
	rooms := NewRoomHandlers(d)
	mux.Handle("GET /v1/rooms", authed(rooms.List))
	mux.Handle("POST /v1/rooms", authed(rooms.OpenRoom))
	mux.Handle("GET /v1/rooms/{id}", authed(rooms.Get))
	mux.Handle("POST /v1/rooms/{id}/join", authed(rooms.Join))
	mux.Handle("POST /v1/rooms/{id}/leave", authed(rooms.Leave))
	mux.Handle("POST /v1/rooms/{id}/turns", authed(rooms.Say))
	mux.Handle("POST /v1/rooms/{id}/close", authed(rooms.Close))
	mux.Handle("GET /v1/rooms/{id}/events", authed(rooms.Events))
	mux.Handle("POST /v1/rooms/{id}/media/offer", authed(rooms.MediaOffer))
	mux.Handle("POST /v1/rooms/{id}/media/answer", authed(rooms.MediaAnswer))
	// AUD-07's controls and SEC-06's deletion.
	mux.Handle("POST /v1/rooms/{id}/media/state", authed(rooms.SetMediaState))
	mux.Handle("POST /v1/rooms/{id}/transcribing", authed(rooms.SetTranscribing))
	mux.Handle("DELETE /v1/rooms/{id}/voice", authed(rooms.DeleteVoice))
	mux.Handle("POST /v1/rooms/{id}/ask", authed(rooms.Ask))
	mux.Handle("POST /v1/rooms/{id}/stop-speaking", authed(rooms.StopSpeaking))

	// --- workbench conversation ---
	converse := NewConverseHandlers(d)
	mux.Handle("POST /v1/converse", authed(converse.Converse))
	mux.HandleFunc("GET /v1/meta/models", converse.Models)

	// --- browser landing pages for emailed links ---
	// A verification link in an email is a GET navigation, but redemption is a
	// state change and must not happen on GET: mail scanners and link previewers
	// follow every URL they see, and would silently burn the token before the
	// recipient ever clicked. These pages render and let the page POST.
	pages := NewPageHandlers(d)
	mux.HandleFunc("GET /assets/", pages.Assets)
	mux.HandleFunc("GET /v1/meta/sigil", pages.Sigil)
	mux.HandleFunc("GET /v1/meta/portrait", pages.Portrait)
	// The console page itself is unauthenticated; every byte of DATA in it is
	// not. An unauthenticated shell that then shows "sign in" is a better
	// experience than a redirect that loses the URL someone was sent.
	mux.HandleFunc("GET /console", pages.Console)
	// The workbench is the product's primary surface (PRD §1.2): voice and the
	// 3D studio. /console is the operations view beside it.
	mux.HandleFunc("GET /workbench", pages.Workbench)
	mux.HandleFunc("GET /rooms/{id}", pages.RoomPage)
	mux.HandleFunc("GET /auth/verify-email", pages.VerifyEmailPage)
	mux.HandleFunc("GET /auth/reset-password", pages.ResetPasswordPage)
	mux.HandleFunc("GET /", pages.Index)

	// 404 in the API's own error shape, so a client never has to parse two
	// different failure formats.
	mux.HandleFunc("GET /v1/", notFound(d.Log))
	mux.HandleFunc("POST /v1/", notFound(d.Log))
	mux.HandleFunc("PATCH /v1/", notFound(d.Log))
	mux.HandleFunc("DELETE /v1/", notFound(d.Log))

	return Chain(mux,
		RequestID(),
		Recover(d.Log),
		SecurityHeaders(d.Config.Env == config.EnvProduction),
		AccessLog(d.Log, d.Clock),
		BodyLimit(d.Config.HTTP.MaxBodyBytes),
	)
}

func notFound(log *logx.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, r, log, errs.New("httpapi.notFound", errs.CodeNotFound).
			WithDetail("no endpoint at %s %s", r.Method, r.URL.Path))
	}
}
