package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/access"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Recording who a raised ceiling rests on, over HTTP.
//
// # Why this endpoint exists, when `project character` deliberately has none
//
// cmd/forgectl/project.go says there is no project API and that adding a
// settings endpoint would mean inventing that surface for one pair of fields.
// That reasoning held for critique intensity and verbosity: they change how
// FORGE talks, everyone who needs them has a terminal, and neither is urgent.
//
// It does not hold here. This is the only mechanism in the product that WIDENS
// what may be done, and leaving it terminal-only meant the ceiling could be
// raised by whoever had shell access and by nobody else — while the people
// actually accountable for engineering work in a domain are the ones in the
// browser, looking at the refusal. A control that cannot be reached by the
// person it is about is not much of a control.
//
// # Why owner-only
//
// access.PermProjectManage, which only RoleOwner holds. Recording an authority
// is not deciding one approval — a maintainer's job — it changes the ceiling for
// every piece of work in the project from then on, which is administration of
// the project in the same sense as deciding who has access to it.
//
// # What this cannot do
//
// Verify anything. Every response carries the same caveat the CLI prints, in the
// same words, for the same reason: a client that could show a holder without it
// would present a claim as a credential. See docs/qualified-review.md.

// ReviewAuthorityHandlers serves the qualified-review claim on a project.
type ReviewAuthorityHandlers struct {
	deps Deps
	svc  *workspace.Service
}

// NewReviewAuthorityHandlers wires the handlers.
func NewReviewAuthorityHandlers(d Deps) *ReviewAuthorityHandlers {
	return &ReviewAuthorityHandlers{
		deps: d,
		svc:  workspace.NewService(d.Pool, d.Clock, d.Log),
	}
}

type recordReviewAuthorityRequest struct {
	// Holder is the named person accountable in this project's domain.
	Holder string `json:"holder"`
	// Note is what they were recorded as holding: a registration number, a role,
	// a scope. Free text, unverified.
	Note string `json:"note"`
	// Until is when the claim stops raising the ceiling, RFC3339 (PRD AGT-03).
	// Omitted means the default lifetime, never "forever": there is no way to
	// record a permanent authority, and that is what the column is for.
	Until string `json:"until"`
}

// theCaveat is the sentence that has to travel with every response.
//
// A constant rather than three string literals, because the one failure mode
// that matters is it going missing from one path — and a value used everywhere
// cannot drift between them.
const theCaveat = "RECORDED, NOT VERIFIED. This build cannot check a qualification: there is " +
	"no registry to consult and no credential to validate. The ceiling rises because a named " +
	"person accepted responsibility, not because a licence was established."

// Get handles GET /v1/projects/{id}/review-authority.
//
// Readable by anyone who can read the project: the same people who already see
// the domain and its ceiling on the graph response, and who need to know what a
// raised one rests on.
func (h *ReviewAuthorityHandlers) Get(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	projectID := r.PathValue("id")

	if err := h.deps.requirePermission(r, projectID, user.ID, access.PermProjectRead); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	h.respond(w, r, projectID)
}

// Put handles PUT /v1/projects/{id}/review-authority.
//
// PUT rather than POST: recording an authority REPLACES whatever was there. A
// project holds one at a time, and a verb implying accumulation would suggest a
// history this does not keep.
func (h *ReviewAuthorityHandlers) Put(w http.ResponseWriter, r *http.Request) {
	const op = "httpapi.RecordReviewAuthority"

	user, _ := UserFrom(r.Context())
	projectID := r.PathValue("id")

	if err := h.deps.requirePermission(r, projectID, user.ID, access.PermProjectManage); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	var req recordReviewAuthorityRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	if strings.TrimSpace(req.Holder) == "" {
		// Refused rather than treated as a clear. DELETE is the way down, and a
		// PUT with an empty body is far more likely to be a client bug than an
		// intention to lower a ceiling.
		WriteError(w, r, h.deps.Log, errs.New(op, errs.CodeValidationFailed).
			WithDetail("holder is required: a raised ceiling rests on a NAMED person. "+
				"To lower it again, DELETE this resource."))
		return
	}

	var until time.Time
	if raw := strings.TrimSpace(req.Until); raw != "" {
		parsed, perr := time.Parse(time.RFC3339, raw)
		if perr != nil {
			WriteError(w, r, h.deps.Log, errs.New(op, errs.CodeValidationFailed).
				WithDetail("until must be an RFC3339 instant such as 2026-12-31T00:00:00Z; got %q", raw))
			return
		}
		until = parsed
	}

	// The recorder is the authenticated user, never a field in the body. A client
	// that could name who attested would let somebody record an authority in
	// another person's name — and attribution is the entire value of the record.
	if err := h.svc.RecordReviewAuthority(r.Context(), h.deps.Pool,
		projectID, req.Holder, req.Note, user.ID, until); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	h.deps.Log.Info(r.Context(), logx.EventNodeAdded,
		"project_id", projectID, "recorded_by", user.ID,
		"detail", "a qualified-review authority was recorded; the domain ceiling may now be higher")
	h.respond(w, r, projectID)
}

// Delete handles DELETE /v1/projects/{id}/review-authority.
//
// Lowering is as easy as raising, and gated the same way. A mechanism that
// raises a ceiling and cannot lower it is one nobody should switch on.
func (h *ReviewAuthorityHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	projectID := r.PathValue("id")

	if err := h.deps.requirePermission(r, projectID, user.ID, access.PermProjectManage); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	if err := h.svc.RecordReviewAuthority(r.Context(), h.deps.Pool,
		projectID, "", "", "", time.Time{}); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	h.respond(w, r, projectID)
}

// respond writes the current state, which is the same shape from all three
// verbs so a client never has to guess what a write left behind.
func (h *ReviewAuthorityHandlers) respond(w http.ResponseWriter, r *http.Request, projectID string) {
	def, err := h.svc.PackFor(r.Context(), h.deps.Pool, projectID)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	a, err := h.svc.ReviewAuthorityFor(r.Context(), h.deps.Pool, projectID)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	body := map[string]any{
		"project_id": projectID,
		"pack":       string(def.Pack),
		"industry":   def.Industry,
		"ceiling":    string(def.CeilingWith(a.Recorded())),
		// What the ordinary ceiling is, so a client can say "raised from" without
		// knowing the pack table.
		"ordinary_ceiling": string(def.MaxTier),
		"asks_for":         def.ReviewAuthority,
		// Present on EVERY response, recorded or not. A client that only saw it
		// alongside a holder would have to remember to render it; one that always
		// has it cannot show the holder by accident without it.
		"caveat": theCaveat,
	}
	if a.Recorded() {
		body["authority"] = map[string]any{
			"holder": a.Holder, "note": a.Note,
			"recorded_by": a.RecordedBy, "recorded_at": a.RecordedAt,
			// When it stops raising the ceiling (PRD AGT-03). A client that
			// showed a holder with no end would present a claim as a standing
			// fact, which is the same failure the caveat exists to prevent.
			"expires_at": a.ExpiresAt,
			"verified":   false,
		}
	}
	// A lapsed claim is REPORTED, never omitted. A ceiling that fell back to the
	// pack's ordinary one with nothing on screen saying why is the same silence
	// this feature was built to end, one step removed: a reader has to be able to
	// see that there WAS an authority and that it ran out.
	if a.Expired() {
		body["expired_authority"] = map[string]any{
			"holder": a.Holder, "note": a.Note,
			"recorded_by": a.RecordedBy, "recorded_at": a.RecordedAt,
			"expired_at": a.ExpiresAt,
			"detail": "This authority lapsed, so the ceiling above is the pack's ordinary one. " +
				"Record it again to raise the ceiling for a further period.",
		}
	}
	WriteJSON(w, http.StatusOK, body)
}
