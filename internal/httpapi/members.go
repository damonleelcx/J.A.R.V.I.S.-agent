package httpapi

import (
	"net/http"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/access"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Who is in a project, over HTTP (PRD SEC-02, AGT-03).
//
// # Why this exists
//
// Membership is the single authorisation path in this build — `owner_id` records
// who created a project and is not an access path — so "who can see and do what
// here" is answered by this list and nothing else. It was answerable only from a
// terminal, which meant the browser could show a person that their own work was
// governed by rules they could not see the shape of: they could learn they were
// not an owner, and not who was.
//
// # What is NOT duplicated here
//
// The rules. access.Service.SetRole and Remove each check
// access.PermProjectManage themselves and each refuse to strand a project by
// removing or demoting its last owner. This layer must never restate those: two
// copies of an authorisation rule is two answers to the same question, and the
// day they disagree is the day somebody administers a project they should not.
//
// The one check this layer DOES own is the read. access.Service.Members takes no
// caller and checks nothing — it is a query — so the handler is the only thing
// standing between a members list and anyone who asks.

// MemberHandlers serves a project's membership.
type MemberHandlers struct{ deps Deps }

// NewMemberHandlers wires the handlers.
func NewMemberHandlers(d Deps) *MemberHandlers { return &MemberHandlers{deps: d} }

type memberDTO struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
	// Email is present only for a caller who may administer the project.
	//
	// A name is what a collaborator needs to know who they are working with. An
	// address is the identifier somebody would act on — and only an owner can
	// act. Showing it to everyone would be normal for this kind of product and
	// tighter is free here, so it is tighter.
	Email     string `json:"email,omitempty"`
	Role      string `json:"role"`
	GrantedBy string `json:"granted_by"`
	GrantedAt string `json:"granted_at"`
	// IsYou lets a client mark the caller's own row without comparing ids it
	// would have to be given separately.
	IsYou bool `json:"is_you"`
}

// List handles GET /v1/projects/{id}/members.
func (h *MemberHandlers) List(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	projectID := r.PathValue("id")

	// The only gate on this read. Members() is a query and checks nothing.
	if err := h.deps.requirePermission(r, projectID, user.ID, access.PermProjectRead); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	members, err := h.deps.Access.Members(r.Context(), projectID)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	canManage := h.deps.Access.Can(r.Context(), projectID, user.ID, access.PermProjectManage)

	// Names are resolved here rather than in the domain service. Who somebody IS
	// is a presentation concern; the access model deals in ids on purpose, and
	// widening its return shape to carry a display name would put identity into
	// a package whose whole job is permissions.
	names, emails := h.resolveIdentities(r, members)

	out := make([]memberDTO, 0, len(members))
	for _, m := range members {
		dto := memberDTO{
			UserID: m.UserID, DisplayName: names[m.UserID], Role: string(m.Role),
			GrantedBy: m.GrantedBy, GrantedAt: m.GrantedAt, IsYou: m.UserID == user.ID,
		}
		if canManage {
			dto.Email = emails[m.UserID]
		}
		out = append(out, dto)
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"project_id": projectID,
		"members":    out,
		// An affordance, not the gate — SetRole and Remove authorise themselves.
		// See internal/httpapi/review_authority.go for the same distinction.
		"can_manage": canManage,
		"roles":      roleCatalogue(),
	})
}

// resolveIdentities maps user ids to names and addresses.
//
// A miss is not an error and not a blank: a membership row whose user is gone is
// a real state, and rendering it as an empty name would read as a member with no
// name rather than as a row worth looking at.
func (h *MemberHandlers) resolveIdentities(r *http.Request, members []access.Member) (map[string]string, map[string]string) {
	names, emails := map[string]string{}, map[string]string{}
	ids := make([]string, 0, len(members))
	for _, m := range members {
		ids = append(ids, m.UserID)
	}
	if len(ids) == 0 {
		return names, emails
	}
	rows, err := h.deps.Pool.Query(r.Context(),
		`select id, coalesce(nullif(display_name, ''), email), email
		   from forge_users where id = any($1)`, ids)
	if err != nil {
		h.deps.Log.WarnWith(r.Context(), logx.EventNodeAdded, err, "project_id", r.PathValue("id"),
			"detail", "member identities could not be read; the list is returned with ids only")
		return names, emails
	}
	defer rows.Close()
	for rows.Next() {
		var id, name, email string
		if err := rows.Scan(&id, &name, &email); err != nil {
			continue
		}
		names[id], emails[id] = name, email
	}
	for _, id := range ids {
		if names[id] == "" {
			names[id] = "(account no longer exists)"
		}
	}
	return names, emails
}

type setRoleRequest struct {
	Role string `json:"role"`
}

type addMemberRequest struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

// SetRole handles PUT /v1/projects/{id}/members/{user_id}.
//
// No permission check here: access.Service.SetRole makes it, and also refuses to
// demote the last owner. Restating either would be a second copy of a rule that
// must have exactly one.
func (h *MemberHandlers) SetRole(w http.ResponseWriter, r *http.Request) {
	const op = "httpapi.SetMemberRole"

	user, _ := UserFrom(r.Context())
	var req setRoleRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	if err := h.deps.Access.SetRole(r.Context(), access.Grant{
		ProjectID: r.PathValue("id"),
		UserID:    r.PathValue("user_id"),
		Role:      access.Role(strings.TrimSpace(req.Role)),
		// The granter is the authenticated user and can never be named by the
		// caller, for the reason the review authority follows: a grant is a record
		// of who decided, and one that could be attributed elsewhere is worthless.
		By: user.ID,
	}); err != nil {
		WriteError(w, r, h.deps.Log, errs.Wrap(op, errs.CodeOf(err), err))
		return
	}
	h.List(w, r)
}

// Add handles POST /v1/projects/{id}/members.
//
// # Why by email and why that is not a new disclosure
//
// A browser has no way to know somebody's user id. Resolving an address does
// reveal whether it has an account — and that is already true of sign-up, by a
// documented decision: "Sign-up returns EMAIL_ALREADY_REGISTERED for a taken
// address… discloses little for a product where accounts use work addresses."
// This path is strictly tighter than that one: it is authenticated AND
// owner-only, where sign-up is neither.
func (h *MemberHandlers) Add(w http.ResponseWriter, r *http.Request) {
	const op = "httpapi.AddMember"

	user, _ := UserFrom(r.Context())
	projectID := r.PathValue("id")

	var req addMemberRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	// Checked BEFORE the address is resolved, so somebody without authority
	// cannot use this endpoint to ask whether an account exists.
	if err := h.deps.requirePermission(r, projectID, user.ID, access.PermProjectManage); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	email := strings.TrimSpace(req.Email)
	if email == "" {
		WriteError(w, r, h.deps.Log, errs.New(op, errs.CodeValidationFailed).
			WithDetail("an email address is required to name who is being added"))
		return
	}
	var userID string
	if err := h.deps.Pool.QueryRow(r.Context(),
		`select id from forge_users where lower(email) = lower($1)`, email).Scan(&userID); err != nil {
		WriteError(w, r, h.deps.Log, errs.New(op, errs.CodeNotFound).
			WithDetail("no account with email %q. They have to sign up before they can be "+
				"added to a project.", email))
		return
	}
	if err := h.deps.Access.SetRole(r.Context(), access.Grant{
		ProjectID: projectID, UserID: userID,
		Role: access.Role(strings.TrimSpace(req.Role)), By: user.ID,
	}); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	h.List(w, r)
}

// Remove handles DELETE /v1/projects/{id}/members/{user_id}.
//
// As with SetRole, the rules live in the service — including the refusal to
// remove a project's last owner, which would leave it with nobody who can
// administer it, not even to undo the removal.
func (h *MemberHandlers) Remove(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())

	if err := h.deps.Access.Remove(r.Context(),
		r.PathValue("id"), r.PathValue("user_id"), user.ID); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	h.List(w, r)
}

// roleCatalogue publishes the four roles and what each may do.
//
// From access.Roles() rather than written out, for the industries list's reason:
// a second copy in a client is the copy that goes stale, and somebody would then
// be offered a role the server does not have.
func roleCatalogue() []map[string]any {
	defs := access.Roles()
	out := make([]map[string]any, 0, len(defs))
	for _, d := range defs {
		perms := make([]string, 0, len(d.Permissions))
		for _, p := range d.Permissions {
			perms = append(perms, string(p))
		}
		out = append(out, map[string]any{
			"role": string(d.Role), "does": d.Gloss, "permissions": perms,
		})
	}
	return out
}
