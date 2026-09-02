// Package identity owns accounts, credentials, sessions, and the flows that
// connect them: sign up, sign in, email verification, and password reset.
package identity

import (
	"net/netip"
	"strings"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/auth"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// Status is an account's lifecycle state.
type Status string

const (
	// StatusActive is a usable account.
	StatusActive Status = "active"
	// StatusLocked is a temporary state entered after repeated failed sign-ins.
	// It clears on its own, or immediately on a successful password reset.
	StatusLocked Status = "locked"
	// StatusDisabled is an administrative state. It does not clear on its own.
	StatusDisabled Status = "disabled"
)

// Valid reports whether s is a recognised status.
func (s Status) Valid() bool {
	return s == StatusActive || s == StatusLocked || s == StatusDisabled
}

// User is an account.
//
// PasswordHash is present on the struct because the sign-in path needs it, but
// it is never serialised: see MarshalJSON on the API DTO rather than here.
type User struct {
	ID              string
	Email           string
	EmailVerifiedAt *time.Time
	DisplayName     string
	Status          Status
	PasswordHash    string
	PasswordAlgo    string
	// PasswordChangedAt invalidates every session issued before it. This is how
	// a password change signs the account out everywhere in one write, instead
	// of a multi-row delete that could partially fail and leave live sessions
	// the user believes are gone.
	PasswordChangedAt time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// EmailVerified reports whether the address has been proven.
func (u *User) EmailVerified() bool { return u.EmailVerifiedAt != nil }

// CanSignIn reports whether this account may start a session, and why not when
// it may not.
//
// Note what is *not* here: an unverified email does not block sign-in. Blocking
// it would trap a user whose verification mail was lost behind a wall with no
// door — they could not sign in to ask for a new link. Verification instead
// gates the consequential surface (creating goals, running tools), which is
// where the guarantee actually matters.
func (u *User) CanSignIn() error {
	const op = "identity.User.CanSignIn"
	switch u.Status {
	case StatusActive:
		return nil
	case StatusLocked:
		return errs.New(op, errs.CodeAccountLocked)
	case StatusDisabled:
		return errs.New(op, errs.CodeForbidden).
			WithDetail("this account has been disabled by an administrator")
	default:
		return errs.New(op, errs.CodeStateCorrupt).
			WithDetail("account has unrecognised status %q", u.Status)
	}
}

// Session is a live authenticated session.
//
// The token plaintext is not a field: it exists once, at issue, and is returned
// separately by the service. Only TokenHash is ever persisted.
type Session struct {
	ID            string
	UserID        string
	TokenHash     []byte
	CreatedAt     time.Time
	LastSeenAt    time.Time
	ExpiresAt     time.Time
	RevokedAt     *time.Time
	RevokedReason string
	UserAgent     string
	IP            *netip.Addr
}

// RevocationReason explains why a session ended. Recorded rather than inferred,
// because "why am I signed out?" is a question users actually ask and an
// auditor has to answer.
type RevocationReason string

const (
	ReasonSignOut        RevocationReason = "sign_out"
	ReasonSignOutAll     RevocationReason = "sign_out_all_devices"
	ReasonPasswordChange RevocationReason = "password_changed"
	ReasonPasswordReset  RevocationReason = "password_reset"
	ReasonAdmin          RevocationReason = "administrative"
)

// Live reports whether s is usable at instant now, given the account's
// password-change watermark and the configured idle timeout.
//
// All four conditions are checked here rather than in SQL so that the reason a
// session is dead is a value the caller can act on, not an empty result set.
func (s *Session) Live(now time.Time, passwordChangedAt time.Time, idleTTL time.Duration) error {
	const op = "identity.Session.Live"

	if s.RevokedAt != nil {
		return errs.New(op, errs.CodeSessionRevoked).
			WithDetail("session was revoked at %s (%s)", s.RevokedAt.UTC().Format(time.RFC3339), s.RevokedReason)
	}
	if !now.Before(s.ExpiresAt) {
		return errs.New(op, errs.CodeSessionExpired).
			WithDetail("session reached its absolute expiry at %s", s.ExpiresAt.UTC().Format(time.RFC3339))
	}
	// A session issued before the password last changed is dead, even though
	// nothing revoked it explicitly. This is what makes "reset my password"
	// actually sign out a stolen session.
	if s.CreatedAt.Before(passwordChangedAt) {
		return errs.New(op, errs.CodeSessionRevoked).
			WithDetail("session predates the last password change")
	}
	if idleTTL > 0 && now.Sub(s.LastSeenAt) > idleTTL {
		return errs.New(op, errs.CodeSessionExpired).
			WithDetail("session has been idle since %s", s.LastSeenAt.UTC().Format(time.RFC3339))
	}
	return nil
}

// AuthToken is a single-use, hashed, expiring credential for email verification
// or password reset.
type AuthToken struct {
	ID          string
	UserID      string
	Purpose     auth.TokenPurpose
	TokenHash   []byte
	CreatedAt   time.Time
	ExpiresAt   time.Time
	ConsumedAt  *time.Time
	RequestedIP *netip.Addr
}

// SignInAttempt is one row of the sign-in audit, and the input to lockout.
type SignInAttempt struct {
	ID        string
	Email     string
	Succeeded bool
	IP        *netip.Addr
	UserAgent string
	CreatedAt time.Time
}

// NormalizeEmail canonicalises an address for storage and comparison.
//
// The column is citext, so case is already handled by the database. Trimming is
// done here because a trailing space is invisible in a form field and would
// otherwise create a second, unreachable account that looks identical to the
// first in every UI that renders it.
func NormalizeEmail(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// ValidateEmail checks that an address is structurally usable.
//
// This is deliberately permissive. The only real proof that an address works is
// that mail sent to it was received and its link clicked, which is exactly what
// the verification flow does. An over-strict regex here rejects valid addresses
// (plus-addressing, new TLDs, unicode local parts) and buys nothing, because the
// verification step is the actual check.
func ValidateEmail(raw string) error {
	const op = "identity.ValidateEmail"

	e := NormalizeEmail(raw)
	if e == "" {
		return errs.New(op, errs.CodeEmailInvalid).WithDetail("email address is empty")
	}
	if len(e) > 254 { // RFC 5321 maximum path length
		return errs.New(op, errs.CodeEmailInvalid).
			WithDetail("email address is %d characters; the maximum is 254", len(e))
	}
	at := strings.LastIndex(e, "@")
	if at <= 0 || at == len(e)-1 {
		return errs.New(op, errs.CodeEmailInvalid).
			WithDetail("email address must contain a local part and a domain separated by @")
	}
	local, domain := e[:at], e[at+1:]
	if len(local) > 64 {
		return errs.New(op, errs.CodeEmailInvalid).
			WithDetail("the local part is %d characters; the maximum is 64", len(local))
	}
	if strings.ContainsAny(e, " \t\r\n") {
		return errs.New(op, errs.CodeEmailInvalid).WithDetail("email address contains whitespace")
	}
	if !strings.Contains(domain, ".") {
		return errs.New(op, errs.CodeEmailInvalid).
			WithDetail("the domain %q has no dot; it does not look like a deliverable address", domain)
	}
	if strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") || strings.Contains(domain, "..") {
		return errs.New(op, errs.CodeEmailInvalid).
			WithDetail("the domain %q has a misplaced or repeated dot", domain)
	}
	return nil
}
