package httpapi

import (
	"context"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/access"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
)

// newProject creates a project AND its owner membership.
//
// Both, always. Authorisation reads forge_project_members, not
// forge_projects.owner_id (PRD SEC-02), so a fixture that inserted the row and
// stopped would produce a project its own creator cannot see — which is exactly
// what production does, and is why this helper exists rather than the checks
// being relaxed for tests.
func newProject(t *testing.T, pool *db.Pool, acc *access.Service, ownerID, name string, at time.Time) string {
	t.Helper()
	ctx := context.Background()

	projectID := id.New(id.PrefixProject)
	if _, err := pool.Exec(ctx, `
		insert into forge_projects (id, owner_id, name, created_at, updated_at)
		values ($1,$2,$3,$4,$4)`, projectID, ownerID, name, at); err != nil {
		t.Fatal(err)
	}
	if err := acc.EnsureOwner(ctx, pool, projectID, ownerID); err != nil {
		t.Fatal(err)
	}
	return projectID
}
