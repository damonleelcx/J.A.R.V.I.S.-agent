// Package mfa is second factors and trusted devices (PRD SEC-02).
//
// # The two hazards this package is shaped around
//
//  1. **Lockout.** The obvious design enables a factor the moment somebody
//     enrols. That locks out every user whose authenticator did not actually end
//     up with the same secret — a mistyped code, a clock miles out, an app that
//     silently failed to save — and they cannot sign in to fix it, because
//     fixing it needs a code. So enrolment is two steps: a factor is `pending`
//     until the user has produced one correct code, and only an `active` factor
//     is ever required.
//
//  2. **Device trust as a bypass.** "Remember this device" is a way to opt out
//     of MFA unless trusting a device REQUIRES passing the second factor at that
//     moment. Otherwise: sign in with a password, mark the device trusted, never
//     be challenged again. So trust is granted by the same call that verifies a
//     code, and by nothing else.
package mfa

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/auth"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/secretbox"
)

// TrustWindow is how long a device stays trusted.
//
// Thirty days: long enough that somebody is not challenged daily on their own
// laptop, short enough that a device sold or lost eventually stops working. A
// device trusted forever is a device somebody sold two years ago.
const TrustWindow = 30 * 24 * time.Hour

// RecoveryCodeCount is how many single-use codes an enrolment produces.
const RecoveryCodeCount = 10

// Status is where a factor stands.
type Status string

const (
	// StatusPending — enrolled, never proven. Required by nothing.
	StatusPending Status = "pending"
	// StatusActive — proven with a code, and now demanded at sign-in.
	StatusActive Status = "active"
)

// Factor is a second factor.
//
// There is no Secret field. The plaintext exists twice: once at enrolment, when
// it is handed to the user, and once inside Verify. A struct with a secret field
// is a struct that gets logged.
type Factor struct {
	ID          string
	UserID      string
	Kind        string
	Label       string
	Status      Status
	ActivatedAt *time.Time
	LastUsedAt  *time.Time
	LastStep    *int64
	CreatedAt   time.Time
}

// Active reports whether this factor is demanded at sign-in.
func (f *Factor) Active() bool { return f.Status == StatusActive }

// Enrolment is what a user is shown once, at enrolment.
//
// Returned and never stored in this shape: the URI contains the secret, which is
// what enrolment transfers, and the recovery codes are shown once because they
// are stored hashed.
type Enrolment struct {
	FactorID string
	Secret   string
	URI      string
	// RecoveryCodes are shown once. Losing them means losing the way back in if
	// the authenticator is gone, which is why the message beside them matters as
	// much as the codes.
	RecoveryCodes []string
}

// Service manages second factors and device trust.
type Service struct {
	pool   *db.Pool
	box    *secretbox.Box
	issuer string
	clock  clock.Clock
	log    *logx.Logger
}

// NewService wires the service.
//
// A nil box is a legal deployment — one with no encryption key configured — and
// enrolment then refuses rather than storing a secret in the clear.
func NewService(pool *db.Pool, box *secretbox.Box, issuer string, clk clock.Clock, log *logx.Logger) *Service {
	if strings.TrimSpace(issuer) == "" {
		issuer = "FORGE"
	}
	return &Service{pool: pool, box: box, issuer: issuer, clock: clk, log: log}
}

// Enrol begins enrolment. The factor is PENDING and blocks nothing.
//
// An existing pending factor is replaced rather than refused: somebody who
// abandoned an enrolment half way and started again is the common case, and
// making them find a "cancel" button first is a worse experience than letting
// the second attempt win. An ACTIVE factor is not replaced — that would be a way
// to swap somebody's second factor without proving you hold the current one.
func (s *Service) Enrol(ctx context.Context, userID, email, label string) (*Enrolment, error) {
	const op = "mfa.Service.Enrol"

	if s.box == nil {
		return nil, errs.New(op, errs.CodeConfigInvalid).
			WithDetail("no encryption key is configured, so a second factor's secret cannot be stored. " +
				"Set FORGE_ENCRYPTION_KEY and restart. FORGE will not hold it in the clear.")
	}
	if active, err := s.ActiveFactor(ctx, userID); err == nil && active != nil {
		return nil, errs.New(op, errs.CodeConflict).
			WithDetail("this account already has an active second factor. Remove it with a current code " +
				"before enrolling another — otherwise anybody with a session could swap it.")
	} else if err != nil && !errs.Is(err, errs.CodeNotFound) {
		return nil, err
	}

	secret, err := auth.NewTOTPSecret()
	if err != nil {
		return nil, err
	}
	sealed, err := s.box.Seal([]byte(secret))
	if err != nil {
		return nil, err
	}
	codes, err := auth.NewRecoveryCodes(RecoveryCodeCount)
	if err != nil {
		return nil, err
	}

	now := s.clock.Now()
	factorID := id.New(id.PrefixFactor)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`delete from forge_mfa_factors where user_id = $1 and status = 'pending'`, userID); err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	if _, err := tx.Exec(ctx, `
		insert into forge_mfa_factors
			(id, user_id, kind, label, secret_ciphertext, key_id, status, created_at, updated_at)
		values ($1,$2,'totp',$3,$4,$5,'pending',$6,$6)`,
		factorID, userID, label, sealed, s.box.KeyID(), now); err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	// Recovery codes are replaced wholesale: a set half from one enrolment and
	// half from another is a set nobody can reason about.
	if _, err := tx.Exec(ctx, `delete from forge_mfa_recovery_codes where user_id = $1`, userID); err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	for _, code := range codes {
		hash, err := auth.HashPassword(auth.NormalizeRecoveryCode(code))
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `
			insert into forge_mfa_recovery_codes (id, user_id, code_hash, created_at)
			values ($1,$2,$3,$4)`, id.New(id.PrefixToken), userID, hash, now); err != nil {
			return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}

	s.log.Info(ctx, logx.EventMFAEnrolled, "user_id", userID, "factor_id", factorID,
		"detail", "pending until a code proves the authenticator holds the same secret")
	return &Enrolment{
		FactorID: factorID, Secret: secret,
		URI:           auth.TOTPURI(s.issuer, email, secret),
		RecoveryCodes: codes,
	}, nil
}

// Activate proves the authenticator holds the secret and turns the factor on.
//
// This is the step that prevents the lockout. Until it succeeds, the factor
// exists and is demanded by nothing.
func (s *Service) Activate(ctx context.Context, userID, code string) error {
	const op = "mfa.Service.Activate"

	factor, secret, err := s.pendingFactor(ctx, userID)
	if err != nil {
		return err
	}
	step, err := auth.VerifyTOTP(secret, code, s.clock.Now(), -1)
	if err != nil {
		s.log.Info(ctx, logx.EventMFARejected, "user_id", userID, "phase", "activation")
		return err
	}
	now := s.clock.Now()
	if _, err := s.pool.Exec(ctx, `
		update forge_mfa_factors
		   set status = 'active', activated_at = $2, last_used_at = $2, last_step = $3, updated_at = $2
		 where id = $1 and status = 'pending'`, factor.ID, now, step); err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	s.log.Info(ctx, logx.EventMFAActivated, "user_id", userID, "factor_id", factor.ID)
	return nil
}

// Required reports whether a sign-in must present a second factor.
//
// A user with no active factor is not challenged. A trusted, unexpired device
// skips the challenge — which is the whole point of device trust, and is why
// trusting one requires a code in the first place.
func (s *Service) Required(ctx context.Context, userID, deviceFingerprint string) (bool, error) {
	factor, err := s.ActiveFactor(ctx, userID)
	if err != nil {
		if errs.Is(err, errs.CodeNotFound) {
			return false, nil
		}
		return false, err
	}
	if factor == nil {
		return false, nil
	}
	if deviceFingerprint == "" {
		return true, nil
	}
	trusted, err := s.deviceTrusted(ctx, userID, deviceFingerprint)
	if err != nil {
		// A device lookup that fails must not skip the challenge. Failing closed
		// here costs one code; failing open costs the factor entirely.
		return true, nil
	}
	return !trusted, nil
}

// Verify checks a code or a recovery code, and optionally trusts the device.
//
// # Why trusting happens here and nowhere else
//
// Device trust is only safe if granting it requires the second factor. A
// separate "trust this device" call reachable with a session cookie would be a
// way to opt out of MFA permanently after one password.
func (s *Service) Verify(ctx context.Context, userID, code, deviceFingerprint, userAgent string, trustDevice bool) error {
	const op = "mfa.Service.Verify"

	factor, secret, err := s.activeFactorWithSecret(ctx, userID)
	if err != nil {
		return err
	}
	now := s.clock.Now()

	last := int64(-1)
	if factor.LastStep != nil {
		last = *factor.LastStep
	}
	step, verr := auth.VerifyTOTP(secret, code, now, last)
	if verr != nil {
		// A recovery code is the way back in when the authenticator is gone, so
		// it is tried second rather than first: the common case is a live app,
		// and burning a recovery code on a mistyped digit would be cruel.
		used, rerr := s.consumeRecoveryCode(ctx, userID, code, now)
		if rerr != nil || !used {
			s.log.Info(ctx, logx.EventMFARejected, "user_id", userID)
			return verr
		}
		s.log.Warn(ctx, logx.EventMFARecoveryUsed, "user_id", userID,
			"detail", "a recovery code was used; the authenticator may be gone")
	} else {
		if _, err := s.pool.Exec(ctx,
			`update forge_mfa_factors set last_used_at = $2, last_step = $3, updated_at = $2 where id = $1`,
			factor.ID, now, step); err != nil {
			return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
		}
	}

	if trustDevice && deviceFingerprint != "" {
		if err := s.trustDevice(ctx, userID, deviceFingerprint, userAgent, now); err != nil {
			// The factor was accepted; failing the whole sign-in because the
			// device could not be remembered would be the wrong trade.
			s.log.WarnWith(ctx, logx.EventDeviceTrusted, err, "user_id", userID,
				"detail", "the second factor was accepted but the device could not be remembered")
		}
	}
	s.log.Info(ctx, logx.EventMFAAccepted, "user_id", userID, "factor_id", factor.ID)
	return nil
}

// Disable removes a second factor, and requires a current code to do it.
//
// Without the code, anybody holding a live session could turn MFA off — which
// makes the factor protect only the sign-in it was already past.
func (s *Service) Disable(ctx context.Context, userID, code string) error {
	const op = "mfa.Service.Disable"

	if err := s.Verify(ctx, userID, code, "", "", false); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `delete from forge_mfa_factors where user_id = $1`, userID); err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	if _, err := tx.Exec(ctx, `delete from forge_mfa_recovery_codes where user_id = $1`, userID); err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	// Device trust is derived from the factor, so it goes with it. A device
	// trusted under a factor that no longer exists is trusted on the strength of
	// nothing.
	if _, err := tx.Exec(ctx,
		`update forge_devices set trusted_at = null, trust_expires_at = null, updated_at = $2
		  where user_id = $1 and trusted_at is not null`, userID, s.clock.Now()); err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	s.log.Warn(ctx, logx.EventMFARejected, "user_id", userID, "detail", "second factor removed by the user")
	return nil
}

// ActiveFactor returns the user's active factor, or nil when they have none.
func (s *Service) ActiveFactor(ctx context.Context, userID string) (*Factor, error) {
	f, _, err := s.factor(ctx, userID, StatusActive, false)
	if err != nil && errs.Is(err, errs.CodeNotFound) {
		return nil, nil
	}
	return f, err
}

func (s *Service) activeFactorWithSecret(ctx context.Context, userID string) (*Factor, string, error) {
	f, secret, err := s.factor(ctx, userID, StatusActive, true)
	if err != nil {
		if errs.Is(err, errs.CodeNotFound) {
			return nil, "", errs.New("mfa.Service.Verify", errs.CodeMFAInvalid).
				WithDetail("this account has no active second factor, so there is nothing to verify against")
		}
		return nil, "", err
	}
	return f, secret, nil
}

func (s *Service) pendingFactor(ctx context.Context, userID string) (*Factor, string, error) {
	f, secret, err := s.factor(ctx, userID, StatusPending, true)
	if err != nil {
		if errs.Is(err, errs.CodeNotFound) {
			return nil, "", errs.New("mfa.Service.Activate", errs.CodeNotFound).
				WithDetail("there is no enrolment in progress for this account")
		}
		return nil, "", err
	}
	return f, secret, nil
}

func (s *Service) factor(ctx context.Context, userID string, status Status, withSecret bool) (*Factor, string, error) {
	const op = "mfa.Service.factor"

	var f Factor
	var raw string
	var sealed []byte
	err := s.pool.QueryRow(ctx, `
		select id, user_id, kind, label, status, activated_at, last_used_at, last_step,
		       secret_ciphertext, created_at
		  from forge_mfa_factors where user_id = $1 and status = $2`, userID, string(status)).
		Scan(&f.ID, &f.UserID, &f.Kind, &f.Label, &raw, &f.ActivatedAt, &f.LastUsedAt, &f.LastStep,
			&sealed, &f.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", errs.Wrap(op, errs.CodeNotFound, err)
		}
		return nil, "", errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	f.Status = Status(raw)
	if !withSecret {
		return &f, "", nil
	}
	if s.box == nil {
		return nil, "", errs.New(op, errs.CodeConfigInvalid).
			WithDetail("no encryption key is configured, so the stored second factor cannot be read. " +
				"Every account with MFA enabled is locked out until FORGE_ENCRYPTION_KEY is restored.")
	}
	plaintext, err := s.box.Open(sealed)
	if err != nil {
		return nil, "", err
	}
	return &f, string(plaintext), nil
}

func (s *Service) consumeRecoveryCode(ctx context.Context, userID, code string, now time.Time) (bool, error) {
	normalised := auth.NormalizeRecoveryCode(code)
	if normalised == "" {
		return false, nil
	}
	rows, err := s.pool.Query(ctx,
		`select id, code_hash from forge_mfa_recovery_codes where user_id = $1 and used_at is null`, userID)
	if err != nil {
		return false, errs.Wrap("mfa.Service.consumeRecoveryCode", errs.CodeDatabaseUnavail, err)
	}
	type candidate struct{ id, hash string }
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.hash); err != nil {
			rows.Close()
			return false, errs.Wrap("mfa.Service.consumeRecoveryCode", errs.CodeDatabaseUnavail, err)
		}
		candidates = append(candidates, c)
	}
	rows.Close()

	for _, c := range candidates {
		ok, err := auth.VerifyPassword(c.hash, normalised)
		if err != nil || !ok {
			continue
		}
		// Conditional on still being unused, so two simultaneous attempts with
		// the same code cannot both succeed.
		tag, err := s.pool.Exec(ctx,
			`update forge_mfa_recovery_codes set used_at = $2 where id = $1 and used_at is null`, c.id, now)
		if err != nil {
			return false, errs.Wrap("mfa.Service.consumeRecoveryCode", errs.CodeDatabaseUnavail, err)
		}
		return tag.RowsAffected() == 1, nil
	}
	return false, nil
}

// RemainingRecoveryCodes reports how many are left, so a user can be warned
// before there are none.
func (s *Service) RemainingRecoveryCodes(ctx context.Context, userID string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`select count(*) from forge_mfa_recovery_codes where user_id = $1 and used_at is null`, userID).Scan(&n)
	if err != nil {
		return 0, errs.Wrap("mfa.Service.RemainingRecoveryCodes", errs.CodeDatabaseUnavail, err)
	}
	return n, nil
}

// ---------------------------------------------------------------------------
// Devices
// ---------------------------------------------------------------------------

// Fingerprint hashes a client identifier.
//
// Hashed rather than stored: a raw fingerprint is a correlatable identifier and
// the database has no use for the original.
func Fingerprint(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("forge.device.v1" + raw))
	return hex.EncodeToString(sum[:])
}

// Device is something somebody signs in from.
type Device struct {
	ID             string
	UserID         string
	Label          string
	UserAgent      string
	FirstSeenAt    time.Time
	LastSeenAt     time.Time
	TrustedAt      *time.Time
	TrustExpiresAt *time.Time
	RevokedAt      *time.Time
}

// Trusted reports whether this device currently skips the second factor.
func (d *Device) Trusted(now time.Time) bool {
	return d.RevokedAt == nil && d.TrustedAt != nil &&
		d.TrustExpiresAt != nil && now.Before(*d.TrustExpiresAt)
}

// Seen records a device without trusting it.
//
// Every sign-in calls this, so "where has this account been used from" is
// answerable. Seeing is not trusting: the row exists with trusted_at null, which
// is the correct state for a device that has just appeared.
func (s *Service) Seen(ctx context.Context, userID, fingerprint, userAgent string) error {
	if fingerprint == "" {
		return nil
	}
	now := s.clock.Now()
	_, err := s.pool.Exec(ctx, `
		insert into forge_devices (id, user_id, fingerprint_hash, user_agent, first_seen_at, last_seen_at, created_at, updated_at)
		values ($1,$2,$3,$4,$5,$5,$5,$5)
		on conflict (user_id, fingerprint_hash) do update
		   set last_seen_at = excluded.last_seen_at, updated_at = excluded.last_seen_at`,
		id.New(id.PrefixDevice), userID, fingerprint, userAgent, now)
	if err != nil {
		return errs.Wrap("mfa.Service.Seen", errs.CodeDatabaseUnavail, err)
	}
	return nil
}

func (s *Service) trustDevice(ctx context.Context, userID, fingerprint, userAgent string, now time.Time) error {
	expires := now.Add(TrustWindow)
	_, err := s.pool.Exec(ctx, `
		insert into forge_devices
			(id, user_id, fingerprint_hash, user_agent, first_seen_at, last_seen_at,
			 trusted_at, trust_expires_at, created_at, updated_at)
		values ($1,$2,$3,$4,$5,$5,$5,$6,$5,$5)
		on conflict (user_id, fingerprint_hash) do update
		   set last_seen_at = excluded.last_seen_at, trusted_at = excluded.trusted_at,
		       trust_expires_at = excluded.trust_expires_at, revoked_at = null,
		       updated_at = excluded.last_seen_at`,
		id.New(id.PrefixDevice), userID, fingerprint, userAgent, now, expires)
	if err != nil {
		return errs.Wrap("mfa.Service.trustDevice", errs.CodeDatabaseUnavail, err)
	}
	s.log.Info(ctx, logx.EventDeviceTrusted, "user_id", userID,
		"expires_at", expires.UTC().Format(time.RFC3339))
	return nil
}

func (s *Service) deviceTrusted(ctx context.Context, userID, fingerprint string) (bool, error) {
	var d Device
	err := s.pool.QueryRow(ctx, `
		select trusted_at, trust_expires_at, revoked_at
		  from forge_devices where user_id = $1 and fingerprint_hash = $2`, userID, fingerprint).
		Scan(&d.TrustedAt, &d.TrustExpiresAt, &d.RevokedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, errs.Wrap("mfa.Service.deviceTrusted", errs.CodeDatabaseUnavail, err)
	}
	return d.Trusted(s.clock.Now()), nil
}

// Devices lists what an account has been used from, most recent first.
func (s *Service) Devices(ctx context.Context, userID string) ([]Device, error) {
	rows, err := s.pool.Query(ctx, `
		select id, user_id, label, user_agent, first_seen_at, last_seen_at,
		       trusted_at, trust_expires_at, revoked_at
		  from forge_devices where user_id = $1 order by last_seen_at desc`, userID)
	if err != nil {
		return nil, errs.Wrap("mfa.Service.Devices", errs.CodeDatabaseUnavail, err)
	}
	defer rows.Close()
	out := []Device{}
	for rows.Next() {
		var d Device
		if err := rows.Scan(&d.ID, &d.UserID, &d.Label, &d.UserAgent, &d.FirstSeenAt,
			&d.LastSeenAt, &d.TrustedAt, &d.TrustExpiresAt, &d.RevokedAt); err != nil {
			return nil, errs.Wrap("mfa.Service.Devices", errs.CodeDatabaseUnavail, err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// RevokeDevice withdraws trust from one device.
func (s *Service) RevokeDevice(ctx context.Context, userID, deviceID, reason string) error {
	now := s.clock.Now()
	tag, err := s.pool.Exec(ctx, `
		update forge_devices
		   set trusted_at = null, trust_expires_at = null, revoked_at = $3,
		       revoked_reason = $4, updated_at = $3
		 where id = $1 and user_id = $2`, deviceID, userID, now, reason)
	if err != nil {
		return errs.Wrap("mfa.Service.RevokeDevice", errs.CodeDatabaseUnavail, err)
	}
	if tag.RowsAffected() == 0 {
		return errs.New("mfa.Service.RevokeDevice", errs.CodeNotFound).
			WithDetail("no device %s belongs to this account", deviceID)
	}
	s.log.Warn(ctx, logx.EventDeviceRevoked, "user_id", userID, "device_id", deviceID, "reason", reason)
	return nil
}
