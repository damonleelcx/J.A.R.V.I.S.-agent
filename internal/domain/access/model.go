// Package access is FORGE's role-based access control (PRD SEC-02).
//
// # What it replaced
//
// Every authorisation check in this codebase used to be the same line:
// `where p.owner_id = $caller`. One owner, no members, no roles — which is why
// memory's organisation layer shipped documented as "declared, not enforced".
//
// # The single source of truth
//
// Membership decides access. `forge_projects.owner_id` records who created the
// project and is deliberately not consulted: two authorisation paths means two
// answers to "may this person read this", and the day they disagree is the day
// somebody sees something they should not.
//
// # Why the matrix is a table
//
// Four roles against nine permissions is a two-dimensional rule, and the whole
// value of writing it down is that somebody can read the grid and say "that is
// wrong". A switch statement spread across call sites cannot be read that way,
// and neither can a check constraint. So: one table, printed verbatim by
// `forgectl access matrix`, and a fence that every permission is reachable by
// somebody and that the roles are ordered.
//
// # Access runs out
//
// PRD AGT-03 asks for access that is "project-scoped, role-based, revocable,
// time-bound". The first three were built here in 0010. The fourth was not:
// there was no expiry column, so access lasted until somebody remembered to
// revoke it, and nothing surfaced membership that had gone stale.
//
// Every grant now carries an end — DefaultGrantLifetime unless the caller names
// one — and an expired grant is refused in RoleIn, which is the one function
// every authorisation in this build reaches the database through. The row is
// not deleted: expiry is not revocation, and who had access until when is the
// first question after anything goes wrong.
//
// The OWNER role is the exception, for the reason given at Service.expiryFor:
// an owner that lapsed on a timer is a project nobody can administer, which is
// the state wouldStrandProject already refuses to create by any other route.
package access

import (
	"sort"
	"strings"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// Role is what somebody is in a project.
type Role string

const (
	// RoleOwner administers the project, including who else is in it.
	RoleOwner Role = "owner"
	// RoleMaintainer runs the work and decides approvals, but cannot change who
	// has access.
	RoleMaintainer Role = "maintainer"
	// RoleContributor creates and edits work, and decides nothing.
	RoleContributor Role = "contributor"
	// RoleViewer reads.
	RoleViewer Role = "viewer"
)

// Permission is one thing somebody may do.
//
// Every value here has a real call site. There are no permissions declared for a
// feature that does not exist: an unenforced permission reads like a control and
// is not one.
type Permission string

const (
	// PermProjectRead — see the project and everything inside it.
	PermProjectRead Permission = "project.read"
	// PermProjectManage — rename, archive, and change who has access.
	PermProjectManage Permission = "project.manage"
	// PermGoalCreate — plan work.
	PermGoalCreate Permission = "goal.create"
	// PermGoalStart — let workers begin it. Separate from creating, because
	// PRD AGT-02 makes planning and starting two deliberate acts, and the
	// person who may draft a plan is not always the one who may run it.
	PermGoalStart Permission = "goal.start"
	// PermApprovalDecide — answer a human gate. The sharpest one: PRD SAF-05
	// says the accountable human is named, and this decides who may be that
	// human.
	PermApprovalDecide Permission = "approval.decide"
	// PermArtifactDispose — accept or reject an artifact version (PRD WRK-04).
	// Distinct from approving a task: one signs off on an action, the other on
	// a result.
	PermArtifactDispose Permission = "artifact.dispose"
	// PermContentWrite — create and edit the project's working content: graph
	// nodes and edges, decisions, and shared memory.
	//
	// One permission rather than one per surface, because they are one act from
	// the reader's side: "may this person change what the project says". Splitting
	// it into memory.write, graph.write and decision.write would produce three
	// rows that are granted together in every role and separately in none, which
	// is a matrix that looks more precise than it is.
	//
	// PERSONAL memory is deliberately outside it: an item in your own layer is
	// yours, and needing a project permission to record how you like to work
	// would be absurd.
	PermContentWrite Permission = "content.write"
	// PermSecretGrant — decide which tools may receive a credential (SEC-03).
	PermSecretGrant Permission = "secret.grant"
	// PermIncidentRespond — open incidents and take the seven actions (SAF-07).
	PermIncidentRespond Permission = "incident.respond"
)

// RoleDef is one row of the matrix.
type RoleDef struct {
	Role Role
	// Rank orders the roles from most to least authority. Used to answer "is
	// this at least a maintainer" without enumerating, and to refuse a grant
	// that would exceed the granter's own authority.
	Rank        int
	Permissions []Permission
	Gloss       string
}

// roles is the authoritative matrix.
//
// Read down the column, not along the row: the question people actually ask is
// "who can decide approvals", and the answer should be findable by scanning one
// permission across four lines.
var roles = []RoleDef{
	{RoleOwner, 0, []Permission{
		PermProjectRead, PermProjectManage, PermGoalCreate, PermGoalStart,
		PermApprovalDecide, PermArtifactDispose, PermContentWrite,
		PermSecretGrant, PermIncidentRespond,
	}, "administers the project, including who else is in it"},

	{RoleMaintainer, 1, []Permission{
		PermProjectRead, PermGoalCreate, PermGoalStart,
		PermApprovalDecide, PermArtifactDispose, PermContentWrite,
		PermSecretGrant, PermIncidentRespond,
	}, "runs the work and decides approvals; cannot change who has access"},

	{RoleContributor, 2, []Permission{
		PermProjectRead, PermGoalCreate, PermContentWrite,
	}, "creates and edits work; starts nothing and decides nothing"},

	{RoleViewer, 3, []Permission{
		PermProjectRead,
	}, "reads"},
}

// Roles returns the four, most authority first.
func Roles() []RoleDef { return append([]RoleDef(nil), roles...) }

// Permissions returns every declared permission, in a stable order.
func Permissions() []Permission {
	return []Permission{
		PermProjectRead, PermProjectManage, PermGoalCreate, PermGoalStart,
		PermApprovalDecide, PermArtifactDispose, PermContentWrite,
		PermSecretGrant, PermIncidentRespond,
	}
}

// RoleOf returns the definition for a role.
//
// An unrecognised role is an error, never a default. Defaulting would give an
// unknown role somebody else's permissions, and the safest-looking default —
// viewer — is still read access to everything in the project.
func RoleOf(r Role) (RoleDef, error) {
	for _, d := range roles {
		if d.Role == r {
			return d, nil
		}
	}
	return RoleDef{}, errs.New("access.RoleOf", errs.CodeStateCorrupt).
		WithDetail("%q is not a role; the roles are %s", r, strings.Join(roleNames(), ", "))
}

// Valid reports whether r is one of the four.
func (r Role) Valid() bool { _, err := RoleOf(r); return err == nil }

// Valid reports whether p is a declared permission.
func (p Permission) Valid() bool {
	for _, known := range Permissions() {
		if known == p {
			return true
		}
	}
	return false
}

func roleNames() []string {
	out := make([]string, 0, len(roles))
	for _, d := range roles {
		out = append(out, string(d.Role))
	}
	return out
}

// Allows reports whether a role carries a permission.
func (r Role) Allows(p Permission) bool {
	def, err := RoleOf(r)
	if err != nil {
		// An unrecognised role permits nothing. It must never be safer than a
		// recognised one — the same rule the epistemic vocabulary follows.
		return false
	}
	for _, held := range def.Permissions {
		if held == p {
			return true
		}
	}
	return false
}

// AtLeast reports whether r carries at least the authority of other.
//
// Used to refuse a grant that would exceed the granter's own role: a maintainer
// promoting somebody to owner is a maintainer making themselves removable by
// somebody they chose, which is not a decision a maintainer gets to make.
func (r Role) AtLeast(other Role) bool {
	a, errA := RoleOf(r)
	b, errB := RoleOf(other)
	if errA != nil || errB != nil {
		return false
	}
	return a.Rank <= b.Rank
}

// DefaultGrantLifetime is how long a grant lasts when nobody says (PRD AGT-03,
// "time-bound").
//
// # Why there is a default at all
//
// Because the alternative is that every grant made through a UI with no date
// field lasts forever, and that is exactly the state issue 20 found: access
// permanent until somebody remembered to revoke it. A default is the only way
// "time-bound" survives the ordinary path where nobody thinks about time.
//
// # Why ninety days
//
// Long enough that it is not in the way of a piece of work — a project phase,
// a contractor's engagement, a quarter — and short enough that access granted
// for something that finished does not outlive it by a year. It is a judgement,
// not a derivation, and it is deliberately one number rather than one per role:
// a lifetime that varied by role would be a second permission matrix.
//
// 0027_access_expiry.sql writes this same interval as a literal, because a
// migration cannot read Go. TestMigrationAndCodeAgreeOnTheDefaultGrantLifetime
// holds the two together.
const DefaultGrantLifetime = 90 * 24 * time.Hour

// Member is somebody's place in a project.
type Member struct {
	ProjectID string
	UserID    string
	Role      Role
	GrantedBy string
	GrantedAt string
	// ExpiresAt is when this grant stops being access, RFC3339, or empty for a
	// grant with no expiry. Owner memberships and everything granted before
	// 0027 have none.
	ExpiresAt string
	// Expired is whether it already has, as of the read. Carried as a field
	// rather than recomputed by every caller so that one clock decides, and so
	// a listing can SHOW a lapsed row instead of hiding it: who had access, and
	// until when, is the first question after anything goes wrong.
	Expired bool
}

// Grant is a request to add or change somebody's role.
type Grant struct {
	ProjectID string
	UserID    string
	Role      Role
	// By is the person making the grant. Their own role is checked against it.
	By string
	// Until is when this grant lapses. Zero means "unstated", which is
	// DefaultGrantLifetime from now — NOT "forever". A caller that means forever
	// says so with Forever, so that the permanent case is always a decision
	// somebody typed rather than a field they left blank.
	Until time.Time
	// Forever grants access with no expiry. Refused for every role but owner:
	// see Service.SetRole.
	Forever bool
}

// Validate checks a grant's shape before any database work.
func (g *Grant) Validate() error {
	const op = "access.Grant.Validate"

	if strings.TrimSpace(g.ProjectID) == "" || strings.TrimSpace(g.UserID) == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("a grant names a project and a person")
	}
	if strings.TrimSpace(g.By) == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("a grant must name who made it; access nobody granted cannot be questioned")
	}
	if !g.Role.Valid() {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("%q is not a role; the roles are %s", g.Role, strings.Join(roleNames(), ", "))
	}
	if g.Forever && !g.Until.IsZero() {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("a grant is either until a date or forever, not both; this one names %s and "+
				"also asks for no expiry", g.Until.UTC().Format(time.RFC3339))
	}
	return nil
}

// ExpiresAt resolves when a grant lapses, given the moment it is being made.
//
// One function so the terminal, the browser and the tests cannot each decide
// what an unstated expiry means. A zero return is "no expiry", which only
// Forever produces.
func (g *Grant) ExpiresAt(now time.Time) time.Time {
	if g.Forever {
		return time.Time{}
	}
	if !g.Until.IsZero() {
		return g.Until
	}
	return now.Add(DefaultGrantLifetime)
}

// Matrix renders the permission table, one row per permission.
//
// Oriented by permission rather than by role because that is the question people
// ask of it: "who can decide approvals" should be one line, not four lookups.
type MatrixRow struct {
	Permission Permission
	Roles      []Role
}

// Matrix returns the grid.
func Matrix() []MatrixRow {
	out := make([]MatrixRow, 0, len(Permissions()))
	for _, p := range Permissions() {
		row := MatrixRow{Permission: p}
		for _, d := range roles {
			if d.Role.Allows(p) {
				row.Roles = append(row.Roles, d.Role)
			}
		}
		sort.SliceStable(row.Roles, func(i, j int) bool {
			a, _ := RoleOf(row.Roles[i])
			b, _ := RoleOf(row.Roles[j])
			return a.Rank < b.Rank
		})
		out = append(out, row)
	}
	return out
}
