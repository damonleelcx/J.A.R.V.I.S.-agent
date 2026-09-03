package access

import (
	"strings"
	"testing"
)

// The matrix is the thing people audit. These hold its shape.

// Four roles, ordered, each with a gloss somebody can read.
func TestRoles_AreOrderedAndExplained(t *testing.T) {
	all := Roles()
	if len(all) != 4 {
		t.Fatalf("%d roles; the model is owner, maintainer, contributor, viewer", len(all))
	}
	seen := map[int]Role{}
	for _, d := range all {
		if strings.TrimSpace(d.Gloss) == "" {
			t.Fatalf("role %q has no gloss; a role nobody can explain gets handed out for the wrong reasons", d.Role)
		}
		if prior, dup := seen[d.Rank]; dup {
			t.Fatalf("roles %q and %q share rank %d, so neither outranks the other", prior, d.Role, d.Rank)
		}
		seen[d.Rank] = d.Role
	}
}

// Deny by default. Somebody with no membership has no permissions at all, and an
// unrecognised role is not safer than a recognised one.
func TestRole_UnknownRolesPermitNothing(t *testing.T) {
	for _, r := range []Role{"", "admin", "OWNER", "superuser"} {
		for _, p := range Permissions() {
			if r.Allows(p) {
				t.Fatalf("role %q was allowed %q", r, p)
			}
		}
	}
}

// The permission set is closed. A typo must not read as a legitimate refusal.
func TestPermission_UnknownOnesAreNotSilentlyRefused(t *testing.T) {
	if Permission("project.delete").Valid() {
		t.Fatal("an undeclared permission reported itself valid")
	}
	for _, p := range Permissions() {
		if !p.Valid() {
			t.Fatalf("declared permission %q is not valid", p)
		}
	}
}

// Every permission is granted to somebody. One granted to no role refuses
// everybody, which reads like a deliberate lockdown and is an omission.
//
// Also asserted in the package's init so a build with the hole cannot start;
// this is the version that names which one when it fails.
func TestMatrix_EveryPermissionIsReachable(t *testing.T) {
	for _, row := range Matrix() {
		if len(row.Roles) == 0 {
			t.Fatalf("permission %q is granted to no role, so it refuses everybody", row.Permission)
		}
	}
}

// The authority ordering has to be real, because SetRole refuses a grant above
// the granter's own role on the strength of it.
func TestRole_AtLeastOrdersAuthority(t *testing.T) {
	if !RoleOwner.AtLeast(RoleMaintainer) || !RoleOwner.AtLeast(RoleOwner) {
		t.Fatal("an owner does not outrank a maintainer")
	}
	if RoleMaintainer.AtLeast(RoleOwner) {
		t.Fatal("a maintainer outranks an owner")
	}
	if RoleViewer.AtLeast(RoleContributor) {
		t.Fatal("a viewer outranks a contributor")
	}
	if Role("nonsense").AtLeast(RoleViewer) {
		t.Fatal("an unrecognised role outranks a viewer")
	}
}

// The three separations the matrix exists to express. Each is a product decision
// that a future edit could quietly undo, so each is named.
func TestMatrix_TheSeparationsThatMatter(t *testing.T) {
	// A contributor creates work and signs nothing off. PRD SAF-05: the
	// accountable human is named, and this decides who may be that human.
	if RoleContributor.Allows(PermApprovalDecide) {
		t.Fatal("a contributor can decide approvals; the person who makes the work would be signing it off")
	}
	if RoleContributor.Allows(PermArtifactDispose) {
		t.Fatal("a contributor can accept their own artifact versions")
	}
	// Planning and starting are two deliberate acts (PRD AGT-02).
	if !RoleContributor.Allows(PermGoalCreate) {
		t.Fatal("a contributor cannot draft a goal, which is the role's whole purpose")
	}
	if RoleContributor.Allows(PermGoalStart) {
		t.Fatal("a contributor can set workers loose on a plan nobody reviewed")
	}
	// A maintainer runs the work and cannot change who has access.
	if RoleMaintainer.Allows(PermProjectManage) {
		t.Fatal("a maintainer can change membership, so the owner role is decorative")
	}
	if !RoleMaintainer.Allows(PermApprovalDecide) {
		t.Fatal("a maintainer cannot decide approvals, which is what the role is for")
	}
	// A viewer reads and does nothing else.
	for _, p := range Permissions() {
		if p == PermProjectRead {
			continue
		}
		if RoleViewer.Allows(p) {
			t.Fatalf("a viewer holds %q", p)
		}
	}
	// Credentials and incidents are not contributor-level.
	if RoleContributor.Allows(PermSecretGrant) || RoleContributor.Allows(PermIncidentRespond) {
		t.Fatal("a contributor can grant credentials or respond to incidents")
	}
}

// Personal memory is outside the matrix on purpose: needing a project permission
// to record how you like to work would be absurd. Asserted so a future
// "tidy-up" that folds it in has to argue with something.
func TestMatrix_PersonalMemoryIsNotAProjectPermission(t *testing.T) {
	for _, p := range Permissions() {
		if strings.Contains(string(p), "memory") && !strings.Contains(string(p), "content") {
			t.Fatalf("permission %q singles memory out; shared memory is project content and personal "+
				"memory needs no project permission at all", p)
		}
	}
	if !RoleContributor.Allows(PermContentWrite) {
		t.Fatal("a contributor cannot write shared content")
	}
}

func TestGrant_MustNameItsParts(t *testing.T) {
	for _, g := range []Grant{
		{UserID: "usr_1", Role: RoleViewer, By: "usr_2"},
		{ProjectID: "prj_1", Role: RoleViewer, By: "usr_2"},
		{ProjectID: "prj_1", UserID: "usr_1", Role: RoleViewer},
		{ProjectID: "prj_1", UserID: "usr_1", Role: "admin", By: "usr_2"},
	} {
		if err := g.Validate(); err == nil {
			t.Fatalf("an incomplete grant was accepted: %+v", g)
		}
	}
	good := Grant{ProjectID: "prj_1", UserID: "usr_1", Role: RoleViewer, By: "usr_2"}
	if err := good.Validate(); err != nil {
		t.Fatalf("a complete grant was refused: %v", err)
	}
}
