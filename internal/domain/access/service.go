package access

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// ErrGrantExpired marks a refusal that happened because a grant ran out, rather
// than because there was never one (PRD AGT-03).
//
// # Why the two are told apart at all
//
// Require deliberately flattens "you are not a member" into "no such project":
// a project you cannot see reads exactly like one that does not exist, so no
// endpoint can be used to enumerate other people's work. An EXPIRED member is
// not in that position — they were in the project, they know it exists, and
// telling them their access lapsed discloses nothing they were not already
// holding. Refusing them with "no project" would send somebody who worked there
// yesterday looking for a deleted project.
var ErrGrantExpired = errors.New("access: the grant expired")

// Service answers "may this person do this here", and manages membership.
type Service struct {
	pool  *db.Pool
	clock clock.Clock
	log   *logx.Logger
}

// NewService wires the service.
func NewService(pool *db.Pool, clk clock.Clock, log *logx.Logger) *Service {
	return &Service{pool: pool, clock: clk, log: log}
}

// RoleIn returns somebody's role in a project, or NOT_FOUND when they have none.
//
// NOT_FOUND rather than an empty role: "no membership" and "a role this build
// does not recognise" are different problems and only one of them is a bug.
func (s *Service) RoleIn(ctx context.Context, q db.Querier, projectID, userID string) (Role, error) {
	const op = "access.Service.RoleIn"

	var raw string
	var expires *time.Time
	err := q.QueryRow(ctx,
		`select role, expires_at from forge_project_members where project_id = $1 and user_id = $2`,
		projectID, userID).Scan(&raw, &expires)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", errs.Wrap(op, errs.CodeNotFound, err).
				WithDetail("no membership for that person in project %s", projectID)
		}
		return "", errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	// An expired grant is not access (PRD AGT-03, "time-bound").
	//
	// # Why here and not in each caller's query
	//
	// Every authorisation in this build reaches the database through this
	// function — Require, Can, SetRole's granter check, wouldStrandProject. A
	// clause added to each of their queries would be four places to forget it,
	// and the fourth is the one that matters. This is the same argument that put
	// Require here in the first place.
	//
	// # Why the row is refused rather than deleted
	//
	// Deleting on read would destroy the answer to "who had access, and until
	// when" at exactly the moment somebody starts asking it. The row stays,
	// marked in every listing, and an owner re-grants it if the access is still
	// wanted. Said out loud as an audit event for the same reason a revocation
	// is: the access somebody had a minute ago is gone, and nothing else would
	// record why.
	if expires != nil && !expires.After(s.clock.Now()) {
		s.log.Info(ctx, logx.EventAccessExpired, "project_id", projectID,
			"user_id", userID, "role", raw, "expired_at", expires.UTC().Format(time.RFC3339))
		return "", errs.Wrap(op, errs.CodeNotFound, ErrGrantExpired).
			WithDetail("that person's %s access to project %s expired at %s. It is not revoked and "+
				"not deleted — it ran out. An owner can grant it again.",
				raw, projectID, expires.UTC().Format(time.RFC3339))
	}
	role := Role(raw)
	if !role.Valid() {
		// The column has a check constraint, so this means the constraint and
		// this build drifted apart. Reported rather than coerced: silently
		// treating an unknown role as viewer would be an authorisation bug in
		// the safe-looking direction, which is still one.
		return "", errs.New(op, errs.CodeStateCorrupt).
			WithDetail("membership in %s carries role %q, which this build does not recognise", projectID, raw)
	}
	return role, nil
}

// Require is the one authorisation call the rest of the system makes.
//
// # Why everything goes through here
//
// Before this, each handler wrote its own `where owner_id = $caller`. Nine
// handlers, nine chances to get it wrong, and no way to answer "what does a
// viewer actually see" without reading all of them. One function means one
// place to audit and one place to change.
//
// A person with no membership is refused as NOT_FOUND rather than FORBIDDEN, so
// the endpoints stay what they already were: a project you cannot see reads
// exactly like one that does not exist.
func (s *Service) Require(ctx context.Context, projectID, userID string, p Permission) error {
	const op = "access.Service.Require"

	if !p.Valid() {
		// A typo'd permission must not pass. Without this it would fall through
		// to "no role allows it" and read as a legitimate refusal.
		return errs.New(op, errs.CodeStateCorrupt).
			WithDetail("%q is not a declared permission; this is a defect in the caller, not a refusal", p)
	}
	role, err := s.RoleIn(ctx, s.pool, projectID, userID)
	if err != nil {
		// An expiry keeps its own words — see ErrGrantExpired.
		if errors.Is(err, ErrGrantExpired) {
			return err
		}
		if errs.Is(err, errs.CodeNotFound) {
			return errs.New(op, errs.CodeNotFound).WithDetail("no project %s", projectID)
		}
		return err
	}
	if !role.Allows(p) {
		def, _ := RoleOf(role)
		return errs.New(op, errs.CodeForbidden).
			WithDetail("%s cannot %s here — a %s %s. Ask an owner to change your role.",
				role, p, role, def.Gloss)
	}
	return nil
}

// Can reports whether somebody holds a permission, without producing an error.
//
// For rendering: a console deciding whether to show a button asks this, and a
// handler deciding whether to act calls Require. Two functions rather than one
// returning a bool, so a call site that forgets to check the result of Require
// does not compile into a permitted action.
func (s *Service) Can(ctx context.Context, projectID, userID string, p Permission) bool {
	return s.Require(ctx, projectID, userID, p) == nil
}

// Members lists a project's membership, most authority first.
func (s *Service) Members(ctx context.Context, projectID string) ([]Member, error) {
	const op = "access.Service.Members"

	rows, err := s.pool.Query(ctx, `
		select project_id, user_id, role, granted_by, granted_at, expires_at
		  from forge_project_members where project_id = $1`, projectID)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	defer rows.Close()

	now := s.clock.Now()
	out := []Member{}
	for rows.Next() {
		var m Member
		var role string
		var grantedAt time.Time
		var expires *time.Time
		if err := rows.Scan(&m.ProjectID, &m.UserID, &role, &m.GrantedBy, &grantedAt, &expires); err != nil {
			return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
		}
		m.Role, m.GrantedAt = Role(role), grantedAt.UTC().Format(time.RFC3339)
		// Lapsed rows are LISTED, marked, not filtered out. This is the one
		// surface where somebody asks "who is in this project", and a member
		// whose access ran out yesterday is the row they most need to see —
		// silently dropping it would read as "they were removed", which nobody
		// did.
		if expires != nil {
			m.ExpiresAt = expires.UTC().Format(time.RFC3339)
			m.Expired = !expires.After(now)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, _ := RoleOf(out[i].Role)
		b, _ := RoleOf(out[j].Role)
		if a.Rank != b.Rank {
			return a.Rank < b.Rank
		}
		return out[i].UserID < out[j].UserID
	})
	return out, nil
}

// Projects lists the projects somebody is a member of, with their role.
//
// This replaced `where owner_id = $1` in the listing endpoints. The difference
// is the whole point of the wave: before it, being added to a project gave you
// nothing to look at.
func (s *Service) Projects(ctx context.Context, userID string) (map[string]Role, error) {
	const op = "access.Service.Projects"

	// Expired memberships are excluded here, where Members lists them marked:
	// this answers "what may I open", and a project somebody can no longer read
	// must not be in it. The listing and the permission check must agree, and
	// RoleIn is what decides — so the clause is the same one.
	rows, err := s.pool.Query(ctx,
		`select project_id, role from forge_project_members
		  where user_id = $1 and (expires_at is null or expires_at > $2)`, userID, s.clock.Now())
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	defer rows.Close()

	out := map[string]Role{}
	for rows.Next() {
		var projectID, role string
		if err := rows.Scan(&projectID, &role); err != nil {
			return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
		}
		out[projectID] = Role(role)
	}
	return out, rows.Err()
}

// SetRole adds somebody to a project or changes their role.
//
// # The two rules that keep this from being an escalation path
//
//  1. The granter needs project.manage — so a maintainer cannot recruit.
//  2. The granter cannot grant a role above their own. A maintainer promoting
//     somebody to owner would be choosing who may remove them, which is not a
//     maintainer's decision to make. In practice only an owner has manage, so
//     this is belt and braces — and it stays correct if a future role gains
//     manage without gaining owner's rank.
func (s *Service) SetRole(ctx context.Context, g Grant) error {
	const op = "access.Service.SetRole"

	if err := g.Validate(); err != nil {
		return err
	}
	if err := s.Require(ctx, g.ProjectID, g.By, PermProjectManage); err != nil {
		return err
	}
	granterRole, err := s.RoleIn(ctx, s.pool, g.ProjectID, g.By)
	if err != nil {
		return err
	}
	if !granterRole.AtLeast(g.Role) {
		return errs.New(op, errs.CodeForbidden).
			WithDetail("a %s cannot grant %s: that is more authority than they hold, and it would let them "+
				"choose who may remove them", granterRole, g.Role)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Demoting the last owner is the same hazard as removing them, so it is the
	// same check.
	if err := s.wouldStrandProject(ctx, tx, g.ProjectID, g.UserID, g.Role); err != nil {
		return err
	}
	now := s.clock.Now()
	expires, err := s.expiryFor(g, now)
	if err != nil {
		return err
	}
	var expiresArg any
	if !expires.IsZero() {
		expiresArg = expires
	}
	if _, err := tx.Exec(ctx, `
		insert into forge_project_members (project_id, user_id, role, granted_by, granted_at, updated_at, expires_at)
		values ($1,$2,$3,$4,$5,$5,$6)
		on conflict (project_id, user_id) do update
		   set role = excluded.role, granted_by = excluded.granted_by,
		       updated_at = excluded.updated_at, expires_at = excluded.expires_at`,
		g.ProjectID, g.UserID, string(g.Role), g.By, now, expiresArg); err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	until := "no expiry"
	if !expires.IsZero() {
		until = expires.UTC().Format(time.RFC3339)
	}
	// The expiry is in the grant event, not only in the row. "When did they get
	// access" is the first question after anything goes wrong, and "until when"
	// is the second one; an audit line that answers only the first sends the
	// reader back to a table that has since been re-granted.
	s.log.Info(ctx, logx.EventAccessGranted, "project_id", g.ProjectID,
		"user_id", g.UserID, "role", string(g.Role), "by", g.By, "expires_at", until)
	return nil
}

// expiryFor decides when a grant lapses, and refuses the one shape that would
// make a project unadministrable.
//
// # Why an owner is different
//
// Every other role expiring is least privilege working. An OWNER expiring is a
// project with nobody who can add a member, change a role or restore access —
// including the person who lost it — which is not a state anybody recovers from
// through the product, only through the database. wouldStrandProject already
// refuses to create that state by removal or demotion; an expiry that produced
// it on a timer would be the same hazard with a delay on it.
//
// So an owner grant is permanent unless somebody explicitly names a date, and
// naming one is allowed — a temporary owner for a handover is a real thing — but
// it is a decision typed on purpose, never a default that arrives because a form
// had no date field.
func (s *Service) expiryFor(g Grant, now time.Time) (time.Time, error) {
	const op = "access.Service.expiryFor"

	if g.Role == RoleOwner && g.Until.IsZero() {
		return time.Time{}, nil
	}
	if g.Forever && g.Role != RoleOwner {
		return time.Time{}, errs.New(op, errs.CodeValidationFailed).
			WithDetail("a %s grant cannot be made without an expiry (PRD AGT-03 requires access to be "+
				"time-bound). Give it an end date, or make them an owner if the access really is "+
				"permanent — an owner is the one role that administers the project and so cannot be "+
				"allowed to lapse.", g.Role)
	}
	return g.ExpiresAt(now), nil
}

// Remove takes somebody out of a project.
func (s *Service) Remove(ctx context.Context, projectID, userID, byUserID string) error {
	const op = "access.Service.Remove"

	if err := s.Require(ctx, projectID, byUserID, PermProjectManage); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := s.wouldStrandProject(ctx, tx, projectID, userID, ""); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx,
		`delete from forge_project_members where project_id = $1 and user_id = $2`, projectID, userID)
	if err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	if tag.RowsAffected() == 0 {
		return errs.New(op, errs.CodeNotFound).WithDetail("that person is not a member of project %s", projectID)
	}
	if err := tx.Commit(ctx); err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	s.log.Info(ctx, logx.EventAccessRevoked, "project_id", projectID, "user_id", userID, "by", byUserID)
	return nil
}

// wouldStrandProject refuses a change that would leave a project with no owner.
//
// # Why this is worth a transaction and a query
//
// A project with no owner cannot be administered: nobody can add a member,
// change a role, or restore access — including the person who lost it. It is not
// a state anybody recovers from through the product, only through the database.
// So it is refused rather than warned about, and checked inside the same
// transaction as the write so two concurrent demotions cannot both pass.
//
// newRole empty means the person is being removed entirely.
func (s *Service) wouldStrandProject(ctx context.Context, q db.Querier, projectID, userID string, newRole Role) error {
	const op = "access.Service.wouldStrandProject"

	current, err := s.RoleIn(ctx, q, projectID, userID)
	if err != nil {
		if errs.Is(err, errs.CodeNotFound) {
			return nil // not a member yet; adding somebody cannot strand anything
		}
		return err
	}
	if current != RoleOwner || newRole == RoleOwner {
		return nil
	}
	// Expired owners do not count. An owner whose grant has lapsed cannot
	// administer anything, so treating them as one of the owners a project has
	// would let the last USABLE owner be demoted on the strength of a row that
	// refuses at every permission check.
	var owners int
	if err := q.QueryRow(ctx,
		`select count(*) from forge_project_members
		  where project_id = $1 and role = 'owner'
		    and (expires_at is null or expires_at > $2)`,
		projectID, s.clock.Now()).Scan(&owners); err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	if owners <= 1 {
		what := "removing"
		if newRole != "" {
			what = "demoting"
		}
		return errs.New(op, errs.CodeLastOwner).
			WithDetail("%s the last owner would leave project %s with nobody who can administer it — "+
				"not even to undo this. Make somebody else an owner first.", what, projectID)
	}
	return nil
}

// EnsureOwner records the creator of a new project as its owner.
//
// Called at project creation. Separate from SetRole because there is no granter
// yet: the first membership cannot be authorised by an existing one, and
// pretending otherwise would mean a project can never be created.
func (s *Service) EnsureOwner(ctx context.Context, q db.Querier, projectID, userID string) error {
	const op = "access.Service.EnsureOwner"

	// expires_at is written NULL on purpose, not left out by omission: the row
	// that keeps a project administrable is the one that must not lapse. See
	// expiryFor.
	now := s.clock.Now()
	if _, err := q.Exec(ctx, `
		insert into forge_project_members (project_id, user_id, role, granted_by, granted_at, updated_at, expires_at)
		values ($1,$2,'owner',$2,$3,$3,null)
		on conflict (project_id, user_id) do nothing`, projectID, userID, now); err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	return nil
}

func init() {
	// Guard against a permission being declared and never appearing in any
	// role. Such a permission refuses everybody, which reads like a deliberate
	// lockdown and is actually an omission.
	//
	// In init rather than a test so it cannot be skipped: a build with an
	// unreachable permission has a hole in its access control and should not
	// start.
	for _, p := range Permissions() {
		reachable := false
		for _, d := range roles {
			if d.Role.Allows(p) {
				reachable = true
				break
			}
		}
		if !reachable {
			panic("access: permission " + string(p) + " is granted to no role, so it refuses everybody")
		}
	}
	_ = strings.TrimSpace
}
