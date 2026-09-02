package identity

import (
	"context"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/auth"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/mail"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// touchGranularity is how stale last_seen_at may get before a request bothers
// to advance it. Without this, every authenticated read becomes a write on the
// hottest row in the system.
const touchGranularity = time.Minute

// Service implements the identity flows.
type Service struct {
	pool   *db.Pool
	repo   *Repository
	mailer mail.Sender
	cfg    config.AuthConfig
	// publicURL builds the links that appear in mail. Wrong value, dead links.
	publicURL string
	clock     clock.Clock
	log       *logx.Logger
	policy    auth.PasswordPolicy
}

// NewService wires the identity service.
func NewService(pool *db.Pool, repo *Repository, mailer mail.Sender, cfg config.AuthConfig, publicURL string, clk clock.Clock, log *logx.Logger) *Service {
	return &Service{
		pool: pool, repo: repo, mailer: mailer, cfg: cfg,
		publicURL: publicURL, clock: clk, log: log,
		policy: auth.PasswordPolicy{MinLength: cfg.MinPasswordLength},
	}
}

// RequestContext carries the caller's network identity for audit and lockout.
type RequestContext struct {
	IP        *netip.Addr
	UserAgent string
}

// ---------------------------------------------------------------------------
// Sign up
// ---------------------------------------------------------------------------

// SignUpInput is the sign-up request.
type SignUpInput struct {
	Email       string
	Password    string
	DisplayName string
}

// SignUpResult reports what happened.
type SignUpResult struct {
	User *User
	// VerificationSent is false when the account was created but the mail
	// transport failed. The caller must surface that rather than implying a
	// message is on its way — see the note in SignUp.
	VerificationSent bool
}

// SignUp creates an account and sends a verification email.
//
// # On user enumeration
//
// This endpoint reveals whether an address is already registered, by returning
// EMAIL_ALREADY_REGISTERED. That is a deliberate, documented trade, not an
// oversight.
//
// The privacy-preserving alternative is to always return success and send
// "someone tried to sign up with your address" to the existing account. It
// works, and it costs a user who genuinely forgot they had an account a
// confusing dead end: they see success, never receive a usable link, and have
// no idea they should be signing in instead.
//
// The information disclosed is small — for a product where accounts are created
// with a work address, whether that address is registered is rarely secret. The
// flows where disclosure is genuinely dangerous are password reset and sign-in,
// and both of those are enumeration-safe (see RequestPasswordReset and SignIn).
//
// If this trade is ever wrong for a deployment, this is the one function to
// change; nothing else depends on the disclosure.
func (s *Service) SignUp(ctx context.Context, in SignUpInput, rc RequestContext) (*SignUpResult, error) {
	const op = "identity.Service.SignUp"

	email := NormalizeEmail(in.Email)
	s.log.Info(ctx, logx.EventAuthSignupStarted, "email_domain", emailDomain(email))

	if err := ValidateEmail(email); err != nil {
		s.log.WarnWith(ctx, logx.EventAuthSignupRejected, err)
		return nil, err
	}
	if err := s.policy.Validate(in.Password); err != nil {
		s.log.WarnWith(ctx, logx.EventAuthSignupRejected, err)
		return nil, err
	}

	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		return nil, err
	}
	now := s.clock.Now()

	user := &User{
		ID:                id.New(id.PrefixUser),
		Email:             email,
		DisplayName:       sanitizeDisplayName(in.DisplayName),
		Status:            StatusActive,
		PasswordHash:      hash,
		PasswordAlgo:      auth.AlgoArgon2id,
		PasswordChangedAt: now,
		CreatedAt:         now,
		UpdatedAt:         now,
	}

	token, err := auth.NewToken()
	if err != nil {
		return nil, err
	}

	// The account and its verification token are written together. A crash
	// between two separate writes would leave an account nobody can verify and
	// nobody can re-register.
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.repo.CreateUser(ctx, tx, user); err != nil {
			return err
		}
		return s.repo.CreateAuthToken(ctx, tx, &AuthToken{
			ID:          id.New(id.PrefixToken),
			UserID:      user.ID,
			Purpose:     auth.PurposeEmailVerify,
			TokenHash:   token.Hash,
			CreatedAt:   now,
			ExpiresAt:   now.Add(s.cfg.EmailVerifyTTL),
			RequestedIP: rc.IP,
		})
	})
	if err != nil {
		s.log.WarnWith(ctx, logx.EventAuthSignupRejected, err, "email_domain", emailDomain(email))
		return nil, err
	}

	// Mail is sent after the commit, never inside it. Two reasons: a slow SMTP
	// server would otherwise hold a database transaction open for its whole
	// timeout, and a transaction that rolled back after the send would have
	// mailed a verification link for an account that does not exist.
	//
	// The residual failure — account created, mail not sent — is the recoverable
	// direction: the user asks for a new link. The reverse is not recoverable.
	sent := s.deliverVerification(ctx, user, token.Plaintext)

	s.log.Info(ctx, logx.EventAuthSignupCompleted,
		"user_id", user.ID, "verification_sent", sent)
	return &SignUpResult{User: user, VerificationSent: sent}, nil
}

// deliverVerification sends the verification mail, reporting success rather
// than failing the caller.
func (s *Service) deliverVerification(ctx context.Context, u *User, plaintext string) bool {
	msg := mail.VerificationEmail(u.Email, u.DisplayName,
		s.publicURL+"/auth/verify-email?token="+plaintext, s.cfg.EmailVerifyTTL)

	if err := s.mailer.Send(ctx, msg); err != nil {
		s.log.WarnWith(ctx, logx.EventMailFailed, err,
			"user_id", u.ID, "purpose", string(auth.PurposeEmailVerify),
			"detail", "the account exists but no verification mail was delivered; the user must request a new link")
		return false
	}
	s.log.Info(ctx, logx.EventAuthEmailVerifySent, "user_id", u.ID)
	return true
}

// ---------------------------------------------------------------------------
// Email verification
// ---------------------------------------------------------------------------

// VerifyEmail redeems a verification token.
func (s *Service) VerifyEmail(ctx context.Context, plaintext string) (*User, error) {
	const op = "identity.Service.VerifyEmail"

	if !auth.LooksLikeToken(plaintext) {
		// Structural rejection before a database round trip. Not a security
		// check — ConsumeAuthToken is — just a cheap filter on obvious junk.
		err := errs.New(op, errs.CodeTokenInvalid).WithDetail("token is not structurally valid")
		s.log.WarnWith(ctx, logx.EventAuthEmailVerifyFailed, err)
		return nil, err
	}
	now := s.clock.Now()
	hash := auth.HashToken(plaintext)

	var user *User
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		tok, err := s.repo.ConsumeAuthToken(ctx, tx, hash, auth.PurposeEmailVerify, now)
		if err != nil {
			return err
		}
		if err := s.repo.MarkEmailVerified(ctx, tx, tok.UserID, now); err != nil {
			return err
		}
		user, err = s.repo.FindUserByID(ctx, tx, tok.UserID)
		return err
	})
	if err != nil {
		s.log.WarnWith(ctx, logx.EventAuthEmailVerifyFailed, err)
		return nil, err
	}

	s.log.Info(ctx, logx.EventAuthEmailVerified, "user_id", user.ID)
	return user, nil
}

// ResendVerification issues a fresh verification link.
//
// Enumeration-safe: it reports nothing about whether the address exists or is
// already verified. The caller returns the same response either way.
func (s *Service) ResendVerification(ctx context.Context, email string, rc RequestContext) error {
	now := s.clock.Now()
	normalized := NormalizeEmail(email)

	user, err := s.repo.FindUserByEmail(ctx, s.pool, normalized)
	if err != nil {
		if errs.Is(err, errs.CodeNotFound) {
			s.log.Info(ctx, logx.EventAuthEmailVerifySent,
				"outcome", "no_account", "email_domain", emailDomain(normalized))
			return nil
		}
		return err
	}
	if user.EmailVerified() {
		s.log.Info(ctx, logx.EventAuthEmailVerifySent, "outcome", "already_verified", "user_id", user.ID)
		return nil
	}

	token, err := auth.NewToken()
	if err != nil {
		return err
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Retire outstanding links so a mailbox with three of them has exactly
		// one that works: the newest.
		if _, err := s.repo.InvalidateOutstandingTokens(ctx, tx, user.ID, auth.PurposeEmailVerify, now); err != nil {
			return err
		}
		return s.repo.CreateAuthToken(ctx, tx, &AuthToken{
			ID:          id.New(id.PrefixToken),
			UserID:      user.ID,
			Purpose:     auth.PurposeEmailVerify,
			TokenHash:   token.Hash,
			CreatedAt:   now,
			ExpiresAt:   now.Add(s.cfg.EmailVerifyTTL),
			RequestedIP: rc.IP,
		})
	})
	if err != nil {
		return err
	}
	s.deliverVerification(ctx, user, token.Plaintext)
	return nil
}

// ---------------------------------------------------------------------------
// Sign in
// ---------------------------------------------------------------------------

// SignInResult carries a new session and its one-time plaintext token.
type SignInResult struct {
	User    *User
	Session *Session
	// Token is the plaintext session credential. It exists only here, and is
	// handed straight to a cookie. It is never persisted and never logged.
	Token string
}

// SignIn authenticates and starts a session.
//
// # On user enumeration
//
// Every failure returns INVALID_CREDENTIALS, whether the address is unknown or
// the password is wrong. The lockout counter is keyed on the address rather
// than on a user row precisely so that attempts against non-existent accounts
// are rate-limited too — otherwise the *absence* of a lockout would itself
// reveal that an address is unregistered.
//
// The one deliberate exception is ACCOUNT_LOCKED, which reveals that an address
// exists. Hiding it would mean telling a genuine locked-out user that their
// password is wrong, sending them to reset a password that was never the
// problem. An attacker who has already made enough attempts to trigger a
// lockout has learned very little more.
func (s *Service) SignIn(ctx context.Context, email, password string, rc RequestContext) (*SignInResult, error) {
	const op = "identity.Service.SignIn"

	now := s.clock.Now()
	normalized := NormalizeEmail(email)

	failures, err := s.repo.CountRecentFailures(ctx, s.pool, normalized, now.Add(-s.cfg.LockoutWindow))
	if err != nil {
		return nil, err
	}
	if failures >= s.cfg.MaxSigninAttempts {
		s.log.Warn(ctx, logx.EventAuthLockoutEngaged,
			"email_domain", emailDomain(normalized), "failures", failures,
			"window", s.cfg.LockoutWindow.String())
		s.recordAttempt(ctx, normalized, false, rc)
		return nil, errs.New(op, errs.CodeAccountLocked).
			WithDetail("%d failed attempts within %s; try again after the window elapses, or reset your password to clear it now",
				failures, s.cfg.LockoutWindow)
	}

	user, err := s.repo.FindUserByEmail(ctx, s.pool, normalized)
	if err != nil {
		if errs.Is(err, errs.CodeNotFound) {
			// Spend comparable time on a non-existent account. Returning
			// immediately would make "unknown address" measurably faster than
			// "wrong password", which is enumeration by stopwatch.
			auth.DummyVerify(password)
			s.recordAttempt(ctx, normalized, false, rc)
			s.log.Info(ctx, logx.EventAuthSigninFailed,
				"reason", "no_account", "email_domain", emailDomain(normalized))
			return nil, errs.New(op, errs.CodeInvalidCredentials)
		}
		return nil, err
	}

	ok, err := auth.VerifyPassword(user.PasswordHash, password)
	if err != nil {
		// A corrupt stored hash. Never treat an unverifiable credential as a
		// match; report it so the row can be investigated.
		s.log.ErrorWith(ctx, logx.EventAuthSigninFailed, err, "user_id", user.ID)
		return nil, err
	}
	if !ok {
		s.recordAttempt(ctx, normalized, false, rc)
		s.log.Info(ctx, logx.EventAuthSigninFailed, "reason", "wrong_password", "user_id", user.ID)
		return nil, errs.New(op, errs.CodeInvalidCredentials)
	}

	if err := user.CanSignIn(); err != nil {
		s.recordAttempt(ctx, normalized, false, rc)
		s.log.Info(ctx, logx.EventAuthSigninFailed, "reason", string(user.Status), "user_id", user.ID)
		return nil, err
	}

	token, err := auth.NewToken()
	if err != nil {
		return nil, err
	}
	session := &Session{
		ID:        id.New(id.PrefixSession),
		UserID:    user.ID,
		TokenHash: token.Hash,
		CreatedAt: now,
		ExpiresAt: now.Add(s.cfg.SessionTTL),
		UserAgent: truncateString(rc.UserAgent, 512),
		IP:        rc.IP,
	}
	if err := s.repo.CreateSession(ctx, s.pool, session); err != nil {
		return nil, err
	}
	s.recordAttempt(ctx, normalized, true, rc)

	// Opportunistic credential upgrade. A successful sign-in is the only moment
	// the plaintext is legitimately available, so it is the only moment a hash
	// stored under weaker parameters can be strengthened. Failure here is
	// logged, never surfaced: the user signed in correctly and must not be
	// punished for our housekeeping.
	if auth.NeedsRehash(user.PasswordHash) {
		if newHash, hashErr := auth.HashPassword(password); hashErr == nil {
			if upErr := s.repo.UpdatePasswordHashOnly(ctx, s.pool, user.ID, newHash, auth.AlgoArgon2id); upErr != nil {
				s.log.WarnWith(ctx, logx.EventAuthSigninSucceeded, upErr,
					"user_id", user.ID, "detail", "password hash upgrade failed; sign-in still succeeded")
			}
		}
	}

	s.log.Info(ctx, logx.EventAuthSigninSucceeded, "user_id", user.ID, "session_id", session.ID)
	return &SignInResult{User: user, Session: session, Token: token.Plaintext}, nil
}

// recordAttempt appends to the sign-in audit.
//
// Best effort by design: this feeds lockout and audit, both of which are
// important, but neither is worth failing an otherwise valid sign-in over. A
// failure here is warned, not returned — a side path must never take down the
// main path.
func (s *Service) recordAttempt(ctx context.Context, email string, succeeded bool, rc RequestContext) {
	err := s.repo.RecordSignInAttempt(ctx, s.pool, &SignInAttempt{
		ID:        id.New(id.PrefixEvent),
		Email:     email,
		Succeeded: succeeded,
		IP:        rc.IP,
		UserAgent: truncateString(rc.UserAgent, 512),
		CreatedAt: s.clock.Now(),
	})
	if err != nil {
		s.log.WarnWith(ctx, logx.EventAuthSigninFailed, err,
			"detail", "could not record a sign-in attempt; lockout counting is degraded for this address")
	}
}

// ---------------------------------------------------------------------------
// Session authentication
// ---------------------------------------------------------------------------

// Authenticate resolves a session token to its account.
//
// Called on every authenticated request, so it is deliberately two reads and at
// most one narrow write.
func (s *Service) Authenticate(ctx context.Context, plaintext string) (*User, *Session, error) {
	const op = "identity.Service.Authenticate"

	if !auth.LooksLikeToken(plaintext) {
		return nil, nil, errs.New(op, errs.CodeNotAuthenticated).
			WithDetail("the presented credential is not structurally a session token")
	}
	session, err := s.repo.FindSessionByTokenHash(ctx, s.pool, auth.HashToken(plaintext))
	if err != nil {
		if errs.Is(err, errs.CodeNotFound) {
			return nil, nil, errs.New(op, errs.CodeNotAuthenticated).
				WithDetail("no session matches this credential")
		}
		return nil, nil, err
	}
	user, err := s.repo.FindUserByID(ctx, s.pool, session.UserID)
	if err != nil {
		if errs.Is(err, errs.CodeNotFound) {
			// The account was deleted but a session row survived. The cascade
			// should prevent it; if it happens, refuse rather than proceed with
			// a nil principal.
			return nil, nil, errs.New(op, errs.CodeStateCorrupt).
				WithDetail("session %s references user %s, which does not exist", session.ID, session.UserID)
		}
		return nil, nil, err
	}

	now := s.clock.Now()
	if err := session.Live(now, user.PasswordChangedAt, s.cfg.SessionIdleTTL); err != nil {
		return nil, nil, err
	}
	if err := user.CanSignIn(); err != nil {
		return nil, nil, err
	}

	// Advance idle expiry, throttled so the common case stays read-only.
	if err := s.repo.TouchSession(ctx, s.pool, session.ID, now, touchGranularity); err != nil {
		s.log.WarnWith(ctx, logx.EventHTTPRequest, err,
			"session_id", session.ID, "detail", "could not advance last_seen_at; idle expiry may fire early")
	}
	return user, session, nil
}

// SignOut revokes one session.
func (s *Service) SignOut(ctx context.Context, sessionID string) error {
	if err := s.repo.RevokeSession(ctx, s.pool, sessionID, s.clock.Now(), ReasonSignOut); err != nil {
		return err
	}
	s.log.Info(ctx, logx.EventAuthSignedOut, "session_id", sessionID)
	return nil
}

// SignOutAllDevices revokes every session except the one making the request.
func (s *Service) SignOutAllDevices(ctx context.Context, userID, keepSessionID string) (int64, error) {
	n, err := s.repo.RevokeAllSessions(ctx, s.pool, userID, keepSessionID, s.clock.Now(), ReasonSignOutAll)
	if err != nil {
		return 0, err
	}
	s.log.Info(ctx, logx.EventAuthSessionsRevoked,
		"user_id", userID, "revoked", n, "reason", string(ReasonSignOutAll))
	return n, nil
}

// ListSessions returns a user's live sessions for the account-security surface.
func (s *Service) ListSessions(ctx context.Context, userID string) ([]*Session, error) {
	return s.repo.ListLiveSessions(ctx, s.pool, userID, s.clock.Now())
}

// ---------------------------------------------------------------------------
// Password reset
// ---------------------------------------------------------------------------

// RequestPasswordReset issues a reset link.
//
// # On user enumeration
//
// This returns nil for an unknown address, exactly as it does for a known one.
// Unlike sign-up, the disclosure here is genuinely dangerous: "which of these
// leaked addresses has an account?" is precisely the question an attacker asks
// before a credential-stuffing run, and this endpoint is unauthenticated.
//
// The caller must therefore return an identical response in both cases. A
// handler that branches on this function's error would reintroduce the leak it
// exists to prevent.
func (s *Service) RequestPasswordReset(ctx context.Context, email string, rc RequestContext) error {
	now := s.clock.Now()
	normalized := NormalizeEmail(email)

	s.log.Info(ctx, logx.EventAuthResetRequested, "email_domain", emailDomain(normalized))

	user, err := s.repo.FindUserByEmail(ctx, s.pool, normalized)
	if err != nil {
		if errs.Is(err, errs.CodeNotFound) {
			// Deliberately silent. Same response, same shape, no mail.
			s.log.Info(ctx, logx.EventAuthResetRequested,
				"outcome", "no_account", "email_domain", emailDomain(normalized))
			return nil
		}
		return err
	}
	if user.Status == StatusDisabled {
		// A disabled account must not be recoverable by its former holder.
		s.log.Info(ctx, logx.EventAuthResetRequested, "outcome", "disabled", "user_id", user.ID)
		return nil
	}

	token, err := auth.NewToken()
	if err != nil {
		return err
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Retire earlier reset links. Without this, every request adds another
		// live credential to the mailbox, and an old one recovered from a
		// forwarded message stays usable for its whole window.
		if _, err := s.repo.InvalidateOutstandingTokens(ctx, tx, user.ID, auth.PurposePasswordReset, now); err != nil {
			return err
		}
		return s.repo.CreateAuthToken(ctx, tx, &AuthToken{
			ID:          id.New(id.PrefixToken),
			UserID:      user.ID,
			Purpose:     auth.PurposePasswordReset,
			TokenHash:   token.Hash,
			CreatedAt:   now,
			ExpiresAt:   now.Add(s.cfg.PasswordResetTTL),
			RequestedIP: rc.IP,
		})
	})
	if err != nil {
		return err
	}

	msg := mail.PasswordResetEmail(user.Email, user.DisplayName,
		s.publicURL+"/auth/reset-password?token="+token.Plaintext, s.cfg.PasswordResetTTL)
	if err := s.mailer.Send(ctx, msg); err != nil {
		s.log.WarnWith(ctx, logx.EventMailFailed, err,
			"user_id", user.ID, "purpose", string(auth.PurposePasswordReset))
		// Still nil: the caller must not distinguish this from any other
		// outcome, or a delivery failure becomes an enumeration oracle.
		return nil
	}
	s.log.Info(ctx, logx.EventAuthResetRequested, "outcome", "sent", "user_id", user.ID)
	return nil
}

// ResetPassword redeems a reset token and sets a new password.
//
// Everything that must happen together happens in one transaction: the token is
// consumed, the credential replaced, the revocation watermark advanced, and
// every existing session revoked. A partial application of that set is an
// account in a state nobody can reason about — a changed password with live
// sessions from before the change is exactly the compromise a reset undoes.
func (s *Service) ResetPassword(ctx context.Context, plaintext, newPassword string) (*User, error) {
	const op = "identity.Service.ResetPassword"

	if !auth.LooksLikeToken(plaintext) {
		err := errs.New(op, errs.CodeTokenInvalid).WithDetail("token is not structurally valid")
		s.log.WarnWith(ctx, logx.EventAuthResetFailed, err)
		return nil, err
	}
	if err := s.policy.Validate(newPassword); err != nil {
		return nil, err
	}
	hash, err := auth.HashPassword(newPassword)
	if err != nil {
		return nil, err
	}

	now := s.clock.Now()
	var user *User
	var revoked int64

	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		tok, err := s.repo.ConsumeAuthToken(ctx, tx, auth.HashToken(plaintext), auth.PurposePasswordReset, now)
		if err != nil {
			return err
		}
		if err := s.repo.SetPassword(ctx, tx, tok.UserID, hash, auth.AlgoArgon2id, now); err != nil {
			return err
		}
		revoked, err = s.repo.RevokeAllSessions(ctx, tx, tok.UserID, "", now, ReasonPasswordReset)
		if err != nil {
			return err
		}
		// Retire any other outstanding reset links: whoever completed this one
		// has control, and a second live link is only a liability.
		if _, err := s.repo.InvalidateOutstandingTokens(ctx, tx, tok.UserID, auth.PurposePasswordReset, now); err != nil {
			return err
		}
		user, err = s.repo.FindUserByID(ctx, tx, tok.UserID)
		return err
	})
	if err != nil {
		s.log.WarnWith(ctx, logx.EventAuthResetFailed, err)
		return nil, err
	}

	// Notify after the commit. If an attacker performed this reset, this message
	// is the account holder's only signal — so a delivery failure is warned
	// about loudly, never swallowed.
	if mailErr := s.mailer.Send(ctx, mail.PasswordChangedEmail(user.Email, user.DisplayName, now, revoked)); mailErr != nil {
		s.log.WarnWith(ctx, logx.EventMailFailed, mailErr,
			"user_id", user.ID, "purpose", "password_changed",
			"detail", "the password changed but the account holder was not notified; if this reset was hostile, they have no signal")
	}

	s.log.Info(ctx, logx.EventAuthResetCompleted, "user_id", user.ID, "sessions_revoked", revoked)
	return user, nil
}

// ChangePassword sets a new password for an authenticated user who can present
// their current one, and returns a REPLACEMENT session.
//
// # Why this rotates the session instead of sparing it
//
// The obvious API — "revoke every session except the one asking" — cannot work
// here, and the reason is worth stating because it is not obvious and a future
// refactor will be tempted to reintroduce it.
//
// Session validity is governed by two mechanisms. RevokeAllSessions is
// selective and can spare a session. The password_changed_at watermark is not:
// Session.Live treats *any* session created before that instant as dead, which
// is exactly what makes a password change take effect everywhere in a single
// write that cannot partially fail. The watermark therefore overrides the
// exemption — the spared session was created before the change, so it dies
// anyway. An earlier version of this function passed keepSessionID to
// RevokeAllSessions and the caller was signed out regardless, silently.
//
// Rotating is the correct resolution rather than a workaround: reissuing the
// session credential after a privilege change is standard practice against
// session fixation, and it keeps the watermark's all-or-nothing guarantee
// intact. The caller must replace its cookie with the returned token.
func (s *Service) ChangePassword(ctx context.Context, userID, currentPassword, newPassword string, rc RequestContext) (*SignInResult, error) {
	const op = "identity.Service.ChangePassword"

	user, err := s.repo.FindUserByID(ctx, s.pool, userID)
	if err != nil {
		return nil, err
	}
	ok, err := auth.VerifyPassword(user.PasswordHash, currentPassword)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errs.New(op, errs.CodeInvalidCredentials).
			WithDetail("the current password did not match")
	}
	if err := s.policy.Validate(newPassword); err != nil {
		return nil, err
	}
	hash, err := auth.HashPassword(newPassword)
	if err != nil {
		return nil, err
	}
	token, err := auth.NewToken()
	if err != nil {
		return nil, err
	}

	now := s.clock.Now()
	session := &Session{
		ID:        id.New(id.PrefixSession),
		UserID:    userID,
		TokenHash: token.Hash,
		// Equal to password_changed_at, not before it, so the new session sits
		// on the live side of the watermark.
		CreatedAt: now,
		ExpiresAt: now.Add(s.cfg.SessionTTL),
		UserAgent: truncateString(rc.UserAgent, 512),
		IP:        rc.IP,
	}

	var revoked int64
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.repo.SetPassword(ctx, tx, userID, hash, auth.AlgoArgon2id, now); err != nil {
			return err
		}
		revoked, err = s.repo.RevokeAllSessions(ctx, tx, userID, "", now, ReasonPasswordChange)
		if err != nil {
			return err
		}
		return s.repo.CreateSession(ctx, tx, session)
	})
	if err != nil {
		return nil, err
	}

	if mailErr := s.mailer.Send(ctx, mail.PasswordChangedEmail(user.Email, user.DisplayName, now, revoked)); mailErr != nil {
		s.log.WarnWith(ctx, logx.EventMailFailed, mailErr, "user_id", userID, "purpose", "password_changed")
	}
	s.log.Info(ctx, logx.EventAuthResetCompleted,
		"user_id", userID, "sessions_revoked", revoked, "via", "change_password",
		"new_session_id", session.ID)

	return &SignInResult{User: user, Session: session, Token: token.Plaintext}, nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// emailDomain returns only the domain part, for logging.
//
// Full addresses are personal data and end up in log aggregators that outlive
// any retention policy anyone agreed to. The domain is enough to answer the
// operational questions — "is one tenant failing?", "is this a bot signing up
// with disposable addresses?" — without storing who.
func emailDomain(email string) string {
	for i := len(email) - 1; i >= 0; i-- {
		if email[i] == '@' {
			return email[i+1:]
		}
	}
	return "invalid"
}

// sanitizeDisplayName trims and bounds a user-supplied name.
func sanitizeDisplayName(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		// Strip control characters: they corrupt log lines and mail headers.
		if r < 0x20 || r == 0x7f {
			continue
		}
		out = append(out, r)
	}
	trimmed := trimSpaceRunes(out)
	if len(trimmed) > 100 {
		trimmed = trimmed[:100]
	}
	return string(trimmed)
}

func trimSpaceRunes(r []rune) []rune {
	start, end := 0, len(r)
	for start < end && (r[start] == ' ' || r[start] == '\t') {
		start++
	}
	for end > start && (r[end-1] == ' ' || r[end-1] == '\t') {
		end--
	}
	return r[start:end]
}

func truncateString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
