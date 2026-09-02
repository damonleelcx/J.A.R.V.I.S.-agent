package identity

import (
	"context"
	"errors"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/auth"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// Repository is the persistence port for identity.
//
// Every method takes a db.Querier rather than holding a pool, so the same code
// runs inside or outside a transaction. Sign-up writes a user, a token, and an
// audit row; if those were three autocommit writes, a crash between them would
// leave an account nobody can verify.
type Repository struct{}

// NewRepository returns the Postgres implementation.
func NewRepository() *Repository { return &Repository{} }

// ---------------------------------------------------------------------------
// Users
// ---------------------------------------------------------------------------

const userColumns = `id, email, email_verified_at, display_name, status,
	password_hash, password_algo, password_changed_at, created_at, updated_at`

func scanUser(row pgx.Row) (*User, error) {
	var u User
	var status string
	err := row.Scan(&u.ID, &u.Email, &u.EmailVerifiedAt, &u.DisplayName, &status,
		&u.PasswordHash, &u.PasswordAlgo, &u.PasswordChangedAt, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, err
	}
	u.Status = Status(status)
	if !u.Status.Valid() {
		// The column has a check constraint, so this means the constraint and
		// this enum have drifted apart. Reported rather than coerced: silently
		// treating an unknown status as "active" would be an authorisation bug.
		return nil, errs.New("identity.scanUser", errs.CodeStateCorrupt).
			WithDetail("user %s has status %q, which this build does not recognise", u.ID, status)
	}
	return &u, nil
}

// CreateUser inserts a new account.
//
// A duplicate email is reported as EMAIL_ALREADY_REGISTERED rather than as a
// constraint violation, because the caller has a real decision to make about it.
func (r *Repository) CreateUser(ctx context.Context, q db.Querier, u *User) error {
	const op = "identity.Repository.CreateUser"

	if u.CreatedAt.IsZero() {
		return errs.New(op, errs.CodeInvariantViolated).
			WithDetail("user %s has no CreatedAt; the application clock owns every timestamp, see CreateSession", u.ID)
	}
	_, err := q.Exec(ctx, `
		insert into forge_users
			(id, email, display_name, status, password_hash, password_algo,
			 password_changed_at, created_at, updated_at)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $8)`,
		u.ID, u.Email, u.DisplayName, string(u.Status), u.PasswordHash, u.PasswordAlgo,
		u.PasswordChangedAt, u.CreatedAt)
	if err != nil {
		if isUniqueViolation(err, "forge_users_email_key") {
			return errs.Wrap(op, errs.CodeEmailAlreadyRegistered, err)
		}
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	return nil
}

// FindUserByEmail returns the account for an address, or NOT_FOUND.
func (r *Repository) FindUserByEmail(ctx context.Context, q db.Querier, email string) (*User, error) {
	const op = "identity.Repository.FindUserByEmail"

	u, err := scanUser(q.QueryRow(ctx,
		`select `+userColumns+` from forge_users where email = $1`, NormalizeEmail(email)))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errs.Wrap(op, errs.CodeNotFound, err)
		}
		return nil, wrapScan(op, err)
	}
	return u, nil
}

// FindUserByID returns the account with the given id, or NOT_FOUND.
func (r *Repository) FindUserByID(ctx context.Context, q db.Querier, id string) (*User, error) {
	const op = "identity.Repository.FindUserByID"

	u, err := scanUser(q.QueryRow(ctx,
		`select `+userColumns+` from forge_users where id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errs.Wrap(op, errs.CodeNotFound, err)
		}
		return nil, wrapScan(op, err)
	}
	return u, nil
}

// MarkEmailVerified records that an address has been proven.
//
// The `email_verified_at is null` guard makes this idempotent: clicking a
// verification link twice must not move the timestamp, because "when did this
// become trusted?" is an audit question with one correct answer.
func (r *Repository) MarkEmailVerified(ctx context.Context, q db.Querier, userID string, at time.Time) error {
	const op = "identity.Repository.MarkEmailVerified"

	_, err := q.Exec(ctx,
		`update forge_users set email_verified_at = $2
		  where id = $1 and email_verified_at is null`, userID, at)
	if err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	return nil
}

// SetPassword writes a new credential and advances the revocation watermark.
//
// Both fields move in one statement, deliberately. If password_changed_at were
// a second write, a crash between them would leave a changed password with
// every pre-existing session still live — the precise failure a password reset
// exists to prevent. It also clears a lockout, because someone who has just
// proven control of the mailbox is not the attacker the lockout was for.
func (r *Repository) SetPassword(ctx context.Context, q db.Querier, userID, hash, algo string, at time.Time) error {
	const op = "identity.Repository.SetPassword"

	tag, err := q.Exec(ctx, `
		update forge_users
		   set password_hash = $2, password_algo = $3, password_changed_at = $4,
		       status = case when status = 'locked' then 'active' else status end
		 where id = $1`, userID, hash, algo, at)
	if err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	if tag.RowsAffected() == 0 {
		return errs.New(op, errs.CodeNotFound).WithDetail("no user with id %s", userID)
	}
	return nil
}

// UpdatePasswordHashOnly rewrites the credential without moving the revocation
// watermark. It exists for one case: transparently upgrading a hash to stronger
// parameters during a successful sign-in. The password did not change, so
// signing every other device out would be a confusing lie.
func (r *Repository) UpdatePasswordHashOnly(ctx context.Context, q db.Querier, userID, hash, algo string) error {
	const op = "identity.Repository.UpdatePasswordHashOnly"

	_, err := q.Exec(ctx,
		`update forge_users set password_hash = $2, password_algo = $3 where id = $1`,
		userID, hash, algo)
	if err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	return nil
}

// SetStatus changes an account's lifecycle state.
func (r *Repository) SetStatus(ctx context.Context, q db.Querier, userID string, s Status) error {
	const op = "identity.Repository.SetStatus"

	if !s.Valid() {
		return errs.New(op, errs.CodeInvariantViolated).WithDetail("status %q is not recognised", s)
	}
	_, err := q.Exec(ctx, `update forge_users set status = $2 where id = $1`, userID, string(s))
	if err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Sessions
// ---------------------------------------------------------------------------

const sessionColumns = `id, user_id, token_hash, created_at, last_seen_at,
	expires_at, revoked_at, revoked_reason, user_agent, ip`

func scanSession(row pgx.Row) (*Session, error) {
	var s Session
	var reason *string
	var ip *netip.Addr
	err := row.Scan(&s.ID, &s.UserID, &s.TokenHash, &s.CreatedAt, &s.LastSeenAt,
		&s.ExpiresAt, &s.RevokedAt, &reason, &s.UserAgent, &ip)
	if err != nil {
		return nil, err
	}
	if reason != nil {
		s.RevokedReason = *reason
	}
	s.IP = ip
	return &s, nil
}

// CreateSession persists a new session.
//
// created_at and last_seen_at are supplied by the caller rather than defaulted
// to the database's now().
//
// Why this matters: Session.Live compares created_at against the user's
// password_changed_at, which is written from the application clock. If one side
// of that comparison came from Postgres and the other from the app server, then
// any skew between the two machines is a live bug — an app clock running a
// second ahead makes every freshly issued session look like it predates the
// last password change, and the user is signed out the instant they sign in.
// One clock owns every timestamp that is compared to another.
func (r *Repository) CreateSession(ctx context.Context, q db.Querier, s *Session) error {
	const op = "identity.Repository.CreateSession"

	if s.CreatedAt.IsZero() {
		return errs.New(op, errs.CodeInvariantViolated).
			WithDetail("session %s has no CreatedAt; it must come from the application clock, not the database default", s.ID)
	}
	_, err := q.Exec(ctx, `
		insert into forge_sessions
			(id, user_id, token_hash, created_at, last_seen_at, expires_at, user_agent, ip)
		values ($1, $2, $3, $4, $4, $5, $6, $7)`,
		s.ID, s.UserID, s.TokenHash, s.CreatedAt, s.ExpiresAt, s.UserAgent, s.IP)
	if err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	return nil
}

// FindSessionByTokenHash looks a session up by digest.
//
// Lookup is by hash, never by plaintext, so the query itself cannot leak a
// usable credential into a slow-query log or an EXPLAIN plan.
func (r *Repository) FindSessionByTokenHash(ctx context.Context, q db.Querier, hash []byte) (*Session, error) {
	const op = "identity.Repository.FindSessionByTokenHash"

	s, err := scanSession(q.QueryRow(ctx,
		`select `+sessionColumns+` from forge_sessions where token_hash = $1`, hash))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errs.Wrap(op, errs.CodeNotFound, err)
		}
		return nil, wrapScan(op, err)
	}
	return s, nil
}

// TouchSession advances last_seen_at, which is what drives idle expiry.
//
// Written with a granularity guard: updating on every single request would make
// each authenticated read a write, and this row is on the hot path of the whole
// API. Only advancing when the value is already stale keeps the common case
// read-only.
func (r *Repository) TouchSession(ctx context.Context, q db.Querier, sessionID string, at time.Time, granularity time.Duration) error {
	const op = "identity.Repository.TouchSession"

	_, err := q.Exec(ctx, `
		update forge_sessions set last_seen_at = $2
		 where id = $1 and last_seen_at < $3`,
		sessionID, at, at.Add(-granularity))
	if err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	return nil
}

// RevokeSession ends one session.
//
// The `revoked_at is null` guard preserves the first revocation's timestamp and
// reason. Overwriting them would erase why the session actually ended.
func (r *Repository) RevokeSession(ctx context.Context, q db.Querier, sessionID string, at time.Time, reason RevocationReason) error {
	const op = "identity.Repository.RevokeSession"

	_, err := q.Exec(ctx, `
		update forge_sessions set revoked_at = $2, revoked_reason = $3
		 where id = $1 and revoked_at is null`, sessionID, at, string(reason))
	if err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	return nil
}

// RevokeAllSessions ends every live session for a user, optionally sparing one
// (so "sign out my other devices" does not sign out the device asking).
func (r *Repository) RevokeAllSessions(ctx context.Context, q db.Querier, userID string, exceptSessionID string, at time.Time, reason RevocationReason) (int64, error) {
	const op = "identity.Repository.RevokeAllSessions"

	tag, err := q.Exec(ctx, `
		update forge_sessions set revoked_at = $2, revoked_reason = $3
		 where user_id = $1 and revoked_at is null and ($4 = '' or id <> $4)`,
		userID, at, string(reason), exceptSessionID)
	if err != nil {
		return 0, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	return tag.RowsAffected(), nil
}

// ListLiveSessions returns a user's usable sessions, newest first, for the
// account-security surface where someone reviews their own devices.
func (r *Repository) ListLiveSessions(ctx context.Context, q db.Querier, userID string, now time.Time) ([]*Session, error) {
	const op = "identity.Repository.ListLiveSessions"

	rows, err := q.Query(ctx, `
		select `+sessionColumns+` from forge_sessions
		 where user_id = $1 and revoked_at is null and expires_at > $2
		 order by created_at desc`, userID, now)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	defer rows.Close()

	var out []*Session
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, wrapScan(op, err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Auth tokens
// ---------------------------------------------------------------------------

// CreateAuthToken persists a minted token's digest.
func (r *Repository) CreateAuthToken(ctx context.Context, q db.Querier, t *AuthToken) error {
	const op = "identity.Repository.CreateAuthToken"

	if !t.Purpose.Valid() {
		return errs.New(op, errs.CodeInvariantViolated).WithDetail("purpose %q is not recognised", t.Purpose)
	}
	if t.CreatedAt.IsZero() {
		return errs.New(op, errs.CodeInvariantViolated).
			WithDetail("token %s has no CreatedAt; see CreateSession for why the application clock owns these", t.ID)
	}
	_, err := q.Exec(ctx, `
		insert into forge_auth_tokens
			(id, user_id, purpose, token_hash, created_at, expires_at, requested_ip)
		values ($1, $2, $3, $4, $5, $6, $7)`,
		t.ID, t.UserID, string(t.Purpose), t.TokenHash, t.CreatedAt, t.ExpiresAt, t.RequestedIP)
	if err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	return nil
}

// ConsumeAuthToken atomically redeems a token and returns it.
//
// This is the single most safety-critical statement in the package, so it is
// one statement.
//
// The obvious implementation — SELECT, check consumed_at, then UPDATE — has a
// race: two requests carrying the same reset link can both pass the check
// before either writes, and both then set a password. A stolen link that has
// already been used would still work. Doing the check *inside* the UPDATE's
// WHERE clause means Postgres serialises it on the row lock, so exactly one
// caller can ever observe rows_affected = 1.
//
// The distinct outcomes are then recovered with a follow-up read, so the caller
// can tell "expired" from "already used" from "never existed" — three different
// things to tell a user.
func (r *Repository) ConsumeAuthToken(ctx context.Context, q db.Querier, hash []byte, purpose auth.TokenPurpose, now time.Time) (*AuthToken, error) {
	const op = "identity.Repository.ConsumeAuthToken"

	var t AuthToken
	var purposeStr string
	err := q.QueryRow(ctx, `
		update forge_auth_tokens
		   set consumed_at = $4
		 where token_hash = $1
		   and purpose    = $2
		   and consumed_at is null
		   and expires_at > $3
		returning id, user_id, purpose, token_hash, created_at, expires_at, consumed_at, requested_ip`,
		hash, string(purpose), now, now,
	).Scan(&t.ID, &t.UserID, &purposeStr, &t.TokenHash, &t.CreatedAt, &t.ExpiresAt, &t.ConsumedAt, &t.RequestedIP)

	if err == nil {
		t.Purpose = auth.TokenPurpose(purposeStr)
		return &t, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}

	// The update matched nothing. Find out which of the three reasons it was,
	// so the user is told something true and specific.
	var consumedAt *time.Time
	var expiresAt time.Time
	lookupErr := q.QueryRow(ctx,
		`select consumed_at, expires_at from forge_auth_tokens where token_hash = $1 and purpose = $2`,
		hash, string(purpose)).Scan(&consumedAt, &expiresAt)
	switch {
	case errors.Is(lookupErr, pgx.ErrNoRows):
		return nil, errs.New(op, errs.CodeTokenInvalid)
	case lookupErr != nil:
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, lookupErr)
	case consumedAt != nil:
		return nil, errs.New(op, errs.CodeTokenAlreadyUsed).
			WithDetail("this link was already used at %s", consumedAt.UTC().Format(time.RFC3339))
	case !now.Before(expiresAt):
		return nil, errs.New(op, errs.CodeTokenExpired).
			WithDetail("this link expired at %s", expiresAt.UTC().Format(time.RFC3339))
	default:
		// Matched nothing, is unconsumed, and is unexpired: the three conditions
		// are contradictory, so the row changed underneath us or an invariant is
		// broken. Reported rather than retried.
		return nil, errs.New(op, errs.CodeInvariantViolated).
			WithDetail("token row is live but the conditional update matched no rows")
	}
}

// InvalidateOutstandingTokens consumes every unused token of a purpose for a
// user.
//
// Called when a new one is issued, so that a mailbox holding three reset links
// only ever has one that works — the newest. Otherwise an old link recovered
// from a forwarded email or a browser history stays live for its full window.
func (r *Repository) InvalidateOutstandingTokens(ctx context.Context, q db.Querier, userID string, purpose auth.TokenPurpose, at time.Time) (int64, error) {
	const op = "identity.Repository.InvalidateOutstandingTokens"

	tag, err := q.Exec(ctx, `
		update forge_auth_tokens set consumed_at = $3
		 where user_id = $1 and purpose = $2 and consumed_at is null`,
		userID, string(purpose), at)
	if err != nil {
		return 0, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	return tag.RowsAffected(), nil
}

// ---------------------------------------------------------------------------
// Sign-in attempts
// ---------------------------------------------------------------------------

// RecordSignInAttempt appends to the sign-in audit.
//
// created_at is caller-supplied for the same reason as CreateSession:
// CountRecentFailures compares these rows against a window computed from the
// application clock, and mixing clocks there skews the lockout window by
// exactly the machines' offset.
func (r *Repository) RecordSignInAttempt(ctx context.Context, q db.Querier, a *SignInAttempt) error {
	const op = "identity.Repository.RecordSignInAttempt"

	if a.CreatedAt.IsZero() {
		return errs.New(op, errs.CodeInvariantViolated).
			WithDetail("sign-in attempt has no CreatedAt; the lockout window depends on a single clock")
	}
	_, err := q.Exec(ctx, `
		insert into forge_signin_attempts (id, email, succeeded, ip, user_agent, created_at)
		values ($1, $2, $3, $4, $5, $6)`,
		a.ID, NormalizeEmail(a.Email), a.Succeeded, a.IP, a.UserAgent, a.CreatedAt)
	if err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	return nil
}

// CountRecentFailures counts consecutive-window failures for an address.
//
// Counted since the last *success*, not merely within the window: otherwise a
// user who signs in correctly still carries their earlier typos toward a
// lockout, and can be locked out immediately after a successful sign-in.
func (r *Repository) CountRecentFailures(ctx context.Context, q db.Querier, email string, since time.Time) (int, error) {
	const op = "identity.Repository.CountRecentFailures"

	var n int
	err := q.QueryRow(ctx, `
		select count(*) from forge_signin_attempts
		 where email = $1
		   and succeeded = false
		   and created_at > greatest($2, coalesce(
		         (select max(created_at) from forge_signin_attempts
		           where email = $1 and succeeded = true), $2))`,
		NormalizeEmail(email), since).Scan(&n)
	if err != nil {
		return 0, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	return n, nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// isUniqueViolation reports whether err is a Postgres unique-constraint
// violation, optionally for a named constraint.
func isUniqueViolation(err error, constraint string) bool {
	var pgErr interface{ SQLState() string }
	if !errors.As(err, &pgErr) || pgErr.SQLState() != "23505" {
		return false
	}
	if constraint == "" {
		return true
	}
	var named interface{ ConstraintName() string }
	if errors.As(err, &named) {
		return named.ConstraintName() == constraint
	}
	// The driver did not surface a constraint name. Treat any unique violation
	// on this path as the one we asked about rather than masking it as an
	// internal error — the caller's query only has one unique constraint in play.
	return true
}

// wrapScan classifies a scan failure. A STATE_CORRUPT error raised inside
// scanUser must keep its code rather than being flattened into a database
// error, because the two demand different operator responses.
func wrapScan(op string, err error) error {
	if errs.Is(err, errs.CodeStateCorrupt) {
		return err
	}
	return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
}
