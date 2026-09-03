package httpapi

import (
	"net/http"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/access"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// The one authorisation call the HTTP surface makes (PRD SEC-02).
//
// # What this replaced
//
// Nine handlers, each with its own `where p.owner_id = $caller` joined into
// whatever query it happened to be running. Nine chances to get it wrong, and no
// way to answer "what can a viewer actually see" without reading all of them.
//
// Now the query says what it wants and this says who may have it. A handler that
// forgets to call it does not silently permit — it does not compile, because the
// data it needs comes back through the same call.

// requirePermission checks a caller's permission in a project.
//
// A project the caller is not a member of is reported as NOT FOUND, not
// FORBIDDEN. FORBIDDEN would confirm the project exists, which turns every
// endpoint into a way to enumerate other people's work. A caller who IS a member
// but lacks the permission gets FORBIDDEN, because at that point they already
// know the project exists and the useful answer is which role they would need.
func (d Deps) requirePermission(r *http.Request, projectID, userID string, p access.Permission) error {
	if d.Access == nil {
		// A deployment with no access service configured must refuse rather than
		// permit. Nil here is a wiring mistake, and the safe reading of a wiring
		// mistake in an authorisation path is "no".
		return errs.New("httpapi.requirePermission", errs.CodeForbidden).
			WithDetail("no access control is configured in this deployment, so nothing is permitted")
	}
	if projectID == "" {
		return errs.New("httpapi.requirePermission", errs.CodeValidationFailed).
			WithDetail("project_id is required; permissions are project-scoped")
	}
	return d.Access.Require(r.Context(), projectID, userID, p)
}

// requireGoalPermission resolves a goal's project and checks a permission there.
//
// The project comes from the goal's row rather than from the request, so a
// caller cannot name a project they do have access to and a goal they do not.
func (d Deps) requireGoalPermission(r *http.Request, goalID, userID string, p access.Permission) (string, error) {
	const op = "httpapi.requireGoalPermission"

	notFound := errs.New(op, errs.CodeNotFound).WithDetail("no goal %s", goalID)

	var projectID string
	if err := d.Pool.QueryRow(r.Context(),
		`select project_id from forge_goals where id = $1`, goalID).Scan(&projectID); err != nil {
		return "", notFound
	}
	if err := d.requirePermission(r, projectID, userID, p); err != nil {
		if errs.Is(err, errs.CodeNotFound) {
			return "", notFound
		}
		return "", err
	}
	return projectID, nil
}

// visibleProjects returns the projects a caller may read, for listing endpoints.
//
// This is where the wave's difference shows most plainly: before it, the listing
// query was `where owner_id = $1` and being added to somebody else's project gave
// you nothing to look at.
func (d Deps) visibleProjects(r *http.Request, userID string) ([]string, error) {
	if d.Access == nil {
		return nil, errs.New("httpapi.visibleProjects", errs.CodeForbidden).
			WithDetail("no access control is configured in this deployment, so nothing is visible")
	}
	roles, err := d.Access.Projects(r.Context(), userID)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(roles))
	for projectID := range roles {
		out = append(out, projectID)
	}
	return out, nil
}
