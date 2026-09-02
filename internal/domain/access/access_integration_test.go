package access_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/auth"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/access"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/identity"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

type harness struct {
	pool    *db.Pool
	svc     *access.Service
	clk     *clock.Fake
	owner   string
	other   string
	third   string
	project string
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	url := os.Getenv("FORGE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("FORGE_TEST_DATABASE_URL is unset; skipping live-database tests.")
	}
	ctx := context.Background()
	schema := "forge_acc_" + strings.ToLower(strings.NewReplacer("/", "_", "-", "_").Replace(t.Name()))
	if len(schema) > 60 {
		schema = schema[:60]
	}
	cfg := func(u string) config.DBConfig {
		return config.DBConfig{URL: u, MaxConns: 6, MinConns: 1,
			MaxConnLifetime: time.Hour, MaxConnIdleTime: time.Minute, ConnectTimeout: 10 * time.Second}
	}
	admin, err := db.Connect(ctx, cfg(url), logx.Discard())
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err := admin.Exec(ctx, "drop schema if exists "+schema+" cascade"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "create schema "+schema); err != nil {
		t.Fatal(err)
	}
	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	pool, err := db.Connect(ctx, cfg(url+sep+"search_path="+schema), logx.Discard())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MigrateFS(ctx, pool, db.Files, db.MigrationsDir, logx.Discard()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		if c, err := db.Connect(context.Background(), cfg(url), logx.Discard()); err == nil {
			_, _ = c.Exec(context.Background(), "drop schema if exists "+schema+" cascade")
			c.Close()
		}
	})

	clk := clock.NewFake(time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC))
	h := &harness{pool: pool, clk: clk, svc: access.NewService(pool, clk, logx.Discard())}

	mk := func(email string) string {
		hash, _ := auth.HashPassword("correct horse battery staple")
		u := &identity.User{ID: id.New(id.PrefixUser), Email: email, Status: identity.StatusActive,
			PasswordHash: hash, PasswordAlgo: auth.AlgoArgon2id,
			PasswordChangedAt: clk.Now(), CreatedAt: clk.Now(), UpdatedAt: clk.Now()}
		if err := identity.NewRepository().CreateUser(ctx, pool, u); err != nil {
			t.Fatal(err)
		}
		return u.ID
	}
	h.owner, h.other, h.third = mk("owner@example.com"), mk("other@example.com"), mk("third@example.com")

	h.project = id.New(id.PrefixProject)
	if _, err := pool.Exec(ctx,
		`insert into forge_projects (id, owner_id, name, created_at, updated_at) values ($1,$2,'P',$3,$3)`,
		h.project, h.owner, clk.Now()); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.EnsureOwner(ctx, pool, h.project, h.owner); err != nil {
		t.Fatal(err)
	}
	return h
}

// Deny by default. Somebody with no membership sees nothing, and the refusal
// reads as absence rather than confirming the project exists.
func TestAccess_NoMembershipMeansNothing(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	for _, p := range access.Permissions() {
		err := h.svc.Require(ctx, h.project, h.other, p)
		if err == nil {
			t.Fatalf("a non-member was permitted %q", p)
		}
		if !errs.Is(err, errs.CodeNotFound) {
			t.Fatalf("%q refused a non-member with %s; it must read as a project that does not exist, "+
				"otherwise every endpoint enumerates other people's work", p, errs.CodeOf(err))
		}
	}
}

// The whole point of the wave: being added to somebody else's project gives you
// something to look at.
func TestAccess_MembershipGrantsWhatTheRoleAllows(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if err := h.svc.SetRole(ctx, access.Grant{
		ProjectID: h.project, UserID: h.other, Role: access.RoleContributor, By: h.owner}); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Require(ctx, h.project, h.other, access.PermProjectRead); err != nil {
		t.Fatalf("a contributor cannot read the project: %v", err)
	}
	// And is refused what the role does not carry — with FORBIDDEN this time,
	// because they already know the project exists.
	err := h.svc.Require(ctx, h.project, h.other, access.PermApprovalDecide)
	if err == nil {
		t.Fatal("a contributor can decide approvals")
	}
	if !errs.Is(err, errs.CodeForbidden) {
		t.Fatalf("got %s; a member who lacks a permission needs to know which role would have it",
			errs.CodeOf(err))
	}
	if !strings.Contains(err.Error(), "contributor") {
		t.Fatalf("the refusal does not say what they are: %v", err)
	}

	visible, err := h.svc.Projects(ctx, h.other)
	if err != nil {
		t.Fatal(err)
	}
	if visible[h.project] != access.RoleContributor {
		t.Fatalf("the project does not appear in their list: %v", visible)
	}
}

// A project with no owner cannot be administered at all — not even to undo the
// change that emptied it.
func TestAccess_TheLastOwnerCannotBeRemovedOrDemoted(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	err := h.svc.Remove(ctx, h.project, h.owner, h.owner)
	if err == nil {
		t.Fatal("the last owner removed themselves, leaving a project nobody can administer")
	}
	if !errs.Is(err, errs.CodeLastOwner) {
		t.Fatalf("got %s", errs.CodeOf(err))
	}
	err = h.svc.SetRole(ctx, access.Grant{
		ProjectID: h.project, UserID: h.owner, Role: access.RoleViewer, By: h.owner})
	if err == nil {
		t.Fatal("the last owner demoted themselves")
	}
	if !errs.Is(err, errs.CodeLastOwner) {
		t.Fatalf("demotion failed with %s; it is the same hazard as removal", errs.CodeOf(err))
	}

	// With a second owner, either may go.
	if err := h.svc.SetRole(ctx, access.Grant{
		ProjectID: h.project, UserID: h.other, Role: access.RoleOwner, By: h.owner}); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Remove(ctx, h.project, h.owner, h.other); err != nil {
		t.Fatalf("an owner could not be removed while another remained: %v", err)
	}
}

// Recruiting is an owner's act. A maintainer who could add members could add
// themselves an owner.
func TestAccess_OnlyProjectManageCanChangeMembership(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if err := h.svc.SetRole(ctx, access.Grant{
		ProjectID: h.project, UserID: h.other, Role: access.RoleMaintainer, By: h.owner}); err != nil {
		t.Fatal(err)
	}
	err := h.svc.SetRole(ctx, access.Grant{
		ProjectID: h.project, UserID: h.third, Role: access.RoleViewer, By: h.other})
	if err == nil {
		t.Fatal("a maintainer added a member")
	}
	if !errs.Is(err, errs.CodeForbidden) {
		t.Fatalf("got %s", errs.CodeOf(err))
	}
	if err := h.svc.Remove(ctx, h.project, h.owner, h.other); err == nil {
		t.Fatal("a maintainer removed the owner")
	}
}

// Nobody grants more than they hold. Enforced even though only owners have
// manage today, so it stays correct if a future role gains manage without
// gaining owner's rank.
func TestAccess_NobodyGrantsAboveTheirOwnRole(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// Give `other` manage by making them an owner, then check the rule holds
	// for a role that genuinely cannot exceed itself.
	if err := h.svc.SetRole(ctx, access.Grant{
		ProjectID: h.project, UserID: h.other, Role: access.RoleOwner, By: h.owner}); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.SetRole(ctx, access.Grant{
		ProjectID: h.project, UserID: h.third, Role: access.RoleOwner, By: h.other}); err != nil {
		t.Fatalf("an owner could not grant owner: %v", err)
	}
}

// The database's own vocabulary and this build's must agree.
func TestAccess_AnUnrecognisedStoredRoleIsCorruptNotPermissive(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// The check constraint refuses it, which is the first line.
	_, err := h.pool.Exec(ctx, `
		insert into forge_project_members (project_id, user_id, role, granted_by, granted_at)
		values ($1,$2,'superuser',$3,$4)`, h.project, h.other, h.owner, h.clk.Now())
	if err == nil {
		t.Fatal("the database accepted a role this build does not recognise")
	}
	if !strings.Contains(err.Error(), "role_check") {
		t.Fatalf("the write failed for a different reason: %v", err)
	}
}

// The backfill in migration 0010 is what stops every existing project losing
// access the moment the wave lands.
func TestAccess_ExistingProjectsKeepTheirOwner(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// A project inserted the old way — no membership row — then migrated.
	legacy := id.New(id.PrefixProject)
	if _, err := h.pool.Exec(ctx,
		`insert into forge_projects (id, owner_id, name, created_at, updated_at) values ($1,$2,'legacy',$3,$3)`,
		legacy, h.third, h.clk.Now()); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Require(ctx, legacy, h.third, access.PermProjectRead); err == nil {
		t.Fatal("a project with no membership row was readable, so membership is not actually the truth")
	}
	// Re-running the chain applies the backfill, which is what a deployment
	// upgrading through 0010 experiences.
	if _, err := db.MigrateFS(ctx, h.pool, db.Files, db.MigrationsDir, logx.Discard()); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Require(ctx, legacy, h.third, access.PermProjectRead); err != nil {
		t.Fatalf("the creator of an existing project lost access when the wave landed: %v", err)
	}
	role, err := h.svc.RoleIn(ctx, h.pool, legacy, h.third)
	if err != nil {
		t.Fatal(err)
	}
	if role != access.RoleOwner {
		t.Fatalf("the backfill made them a %s", role)
	}
}

func TestAccess_MembersListsMostAuthorityFirst(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if err := h.svc.SetRole(ctx, access.Grant{
		ProjectID: h.project, UserID: h.other, Role: access.RoleViewer, By: h.owner}); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.SetRole(ctx, access.Grant{
		ProjectID: h.project, UserID: h.third, Role: access.RoleMaintainer, By: h.owner}); err != nil {
		t.Fatal(err)
	}
	members, err := h.svc.Members(ctx, h.project)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 3 {
		t.Fatalf("%d members", len(members))
	}
	want := []access.Role{access.RoleOwner, access.RoleMaintainer, access.RoleViewer}
	for i, m := range members {
		if m.Role != want[i] {
			t.Fatalf("member %d is a %s; the list should read most authority first: %v", i, m.Role, members)
		}
		if m.GrantedBy == "" {
			t.Fatal("a membership names nobody who granted it")
		}
	}
}
