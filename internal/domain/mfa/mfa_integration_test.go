package mfa_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/auth"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/identity"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/mfa"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/secretbox"
)

// Second factors and device trust, against a real database.
//
// Two properties carry this package and both are about failure modes rather than
// happy paths: an enrolment must not lock anybody out, and device trust must not
// be a way around the factor it depends on.

const testKey = "a-test-encryption-key-long-enough-to-be-accepted"

type harness struct {
	pool   *db.Pool
	svc    *mfa.Service
	clk    *clock.Fake
	userID string
	email  string
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	url := os.Getenv("FORGE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("FORGE_TEST_DATABASE_URL is unset; skipping live-database tests.")
	}
	ctx := context.Background()
	schema := "forge_mfa_" + strings.ToLower(strings.NewReplacer("/", "_", "-", "_").Replace(t.Name()))
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

	box, err := secretbox.New(testKey)
	if err != nil {
		t.Fatal(err)
	}
	clk := clock.NewFake(time.Date(2026, 9, 2, 12, 0, 30, 0, time.UTC))
	h := &harness{pool: pool, clk: clk, svc: mfa.NewService(pool, box, "FORGE", clk, logx.Discard())}

	now := clk.Now()
	hash, _ := auth.HashPassword("correct horse battery staple")
	u := &identity.User{ID: id.New(id.PrefixUser), Email: "mfa@example.com",
		Status: identity.StatusActive, PasswordHash: hash, PasswordAlgo: auth.AlgoArgon2id,
		PasswordChangedAt: now, CreatedAt: now, UpdatedAt: now}
	if err := identity.NewRepository().CreateUser(ctx, pool, u); err != nil {
		t.Fatal(err)
	}
	h.userID, h.email = u.ID, u.Email
	return h
}

func (h *harness) code(t *testing.T, secret string) string {
	t.Helper()
	code, err := auth.TOTPCode(secret, h.clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	return code
}

// The lockout hazard, in one test. An enrolment that has not been proven must
// block nothing — otherwise a user whose authenticator did not save the secret
// cannot sign in to fix it.
func TestMFA_APendingEnrolmentBlocksNothing(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	enrolment, err := h.svc.Enrol(ctx, h.userID, h.email, "phone")
	if err != nil {
		t.Fatal(err)
	}
	required, err := h.svc.Required(ctx, h.userID, "")
	if err != nil {
		t.Fatal(err)
	}
	if required {
		t.Fatal("an unproven enrolment already demands a code; a user whose app did not save the " +
			"secret is now locked out and cannot sign in to fix it")
	}
	if active, _ := h.svc.ActiveFactor(ctx, h.userID); active != nil {
		t.Fatal("a pending factor reported itself active")
	}

	if err := h.svc.Activate(ctx, h.userID, h.code(t, enrolment.Secret)); err != nil {
		t.Fatal(err)
	}
	required, err = h.svc.Required(ctx, h.userID, "")
	if err != nil {
		t.Fatal(err)
	}
	if !required {
		t.Fatal("an activated factor is not demanded at sign-in")
	}
}

// Activation is the proof. A wrong code must not turn it on.
func TestMFA_ActivationNeedsTheRightCode(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.svc.Enrol(ctx, h.userID, h.email, "phone"); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Activate(ctx, h.userID, "000000"); err == nil {
		t.Fatal("a factor was activated with a code that did not come from its secret")
	}
	if required, _ := h.svc.Required(ctx, h.userID, ""); required {
		t.Fatal("a failed activation still turned the factor on")
	}
}

// Device trust is only safe if granting it requires the factor. This is the
// bypass the design exists to prevent.
func TestMFA_TrustingADeviceRequiresTheSecondFactor(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	enrolment, _ := h.svc.Enrol(ctx, h.userID, h.email, "phone")
	if err := h.svc.Activate(ctx, h.userID, h.code(t, enrolment.Secret)); err != nil {
		t.Fatal(err)
	}
	fp := mfa.Fingerprint("this-laptop")

	// Merely being seen does not trust it.
	if err := h.svc.Seen(ctx, h.userID, fp, "Test/1.0"); err != nil {
		t.Fatal(err)
	}
	required, _ := h.svc.Required(ctx, h.userID, fp)
	if !required {
		t.Fatal("a device that had only been SEEN skipped the second factor")
	}

	// A failed verification must not trust it either.
	h.clk.Advance(30 * time.Second)
	if err := h.svc.Verify(ctx, h.userID, "000000", fp, "Test/1.0", true); err == nil {
		t.Fatal("a wrong code was accepted")
	}
	if required, _ := h.svc.Required(ctx, h.userID, fp); !required {
		t.Fatal("a FAILED verification trusted the device; MFA can now be skipped forever after one password")
	}

	// A correct one does.
	if err := h.svc.Verify(ctx, h.userID, h.code(t, enrolment.Secret), fp, "Test/1.0", true); err != nil {
		t.Fatal(err)
	}
	if required, _ := h.svc.Required(ctx, h.userID, fp); required {
		t.Fatal("a device trusted with a correct code is still being challenged")
	}
	// And only that device.
	other := mfa.Fingerprint("someone-elses-laptop")
	if required, _ := h.svc.Required(ctx, h.userID, other); !required {
		t.Fatal("trusting one device trusted another")
	}
}

// A device trusted forever is a device somebody sold two years ago.
func TestMFA_DeviceTrustExpires(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	enrolment, _ := h.svc.Enrol(ctx, h.userID, h.email, "phone")
	if err := h.svc.Activate(ctx, h.userID, h.code(t, enrolment.Secret)); err != nil {
		t.Fatal(err)
	}
	fp := mfa.Fingerprint("this-laptop")
	h.clk.Advance(30 * time.Second)
	if err := h.svc.Verify(ctx, h.userID, h.code(t, enrolment.Secret), fp, "Test/1.0", true); err != nil {
		t.Fatal(err)
	}
	if required, _ := h.svc.Required(ctx, h.userID, fp); required {
		t.Fatal("a freshly trusted device is being challenged")
	}
	h.clk.Advance(mfa.TrustWindow + time.Hour)
	if required, _ := h.svc.Required(ctx, h.userID, fp); !required {
		t.Fatal("device trust did not expire")
	}
}

// Removing the factor removes what device trust rested on.
func TestMFA_DisablingTheFactorUntrustsEveryDevice(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	enrolment, _ := h.svc.Enrol(ctx, h.userID, h.email, "phone")
	if err := h.svc.Activate(ctx, h.userID, h.code(t, enrolment.Secret)); err != nil {
		t.Fatal(err)
	}
	fp := mfa.Fingerprint("this-laptop")
	h.clk.Advance(30 * time.Second)
	if err := h.svc.Verify(ctx, h.userID, h.code(t, enrolment.Secret), fp, "Test/1.0", true); err != nil {
		t.Fatal(err)
	}

	// Disabling needs a current code: otherwise anybody with a live session can
	// turn the factor off, and it only ever protected the sign-in it was past.
	if err := h.svc.Disable(ctx, h.userID, "000000"); err == nil {
		t.Fatal("MFA was disabled without a code")
	}
	h.clk.Advance(30 * time.Second)
	if err := h.svc.Disable(ctx, h.userID, h.code(t, enrolment.Secret)); err != nil {
		t.Fatal(err)
	}

	devices, err := h.svc.Devices(ctx, h.userID)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range devices {
		if d.Trusted(h.clk.Now()) {
			t.Fatal("a device is still trusted after the factor it depended on was removed")
		}
	}
}

// The way back in when the authenticator is gone — used once, and only once.
func TestMFA_RecoveryCodesWorkOnceEach(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	enrolment, _ := h.svc.Enrol(ctx, h.userID, h.email, "phone")
	if err := h.svc.Activate(ctx, h.userID, h.code(t, enrolment.Secret)); err != nil {
		t.Fatal(err)
	}
	if len(enrolment.RecoveryCodes) != mfa.RecoveryCodeCount {
		t.Fatalf("%d recovery codes", len(enrolment.RecoveryCodes))
	}
	before, _ := h.svc.RemainingRecoveryCodes(ctx, h.userID)
	if before != mfa.RecoveryCodeCount {
		t.Fatalf("%d unused codes recorded", before)
	}

	code := enrolment.RecoveryCodes[0]
	h.clk.Advance(30 * time.Second)
	if err := h.svc.Verify(ctx, h.userID, code, "", "", false); err != nil {
		t.Fatalf("a recovery code was rejected: %v", err)
	}
	if err := h.svc.Verify(ctx, h.userID, code, "", "", false); err == nil {
		t.Fatal("the same recovery code worked twice")
	}
	// Typed the way somebody reads it off paper.
	if err := h.svc.Verify(ctx, h.userID,
		strings.ToUpper(enrolment.RecoveryCodes[1]), "", "", false); err != nil {
		t.Fatalf("a recovery code typed in capitals was rejected: %v", err)
	}
	after, _ := h.svc.RemainingRecoveryCodes(ctx, h.userID)
	if after != before-2 {
		t.Fatalf("%d codes remain; two were used", after)
	}
}

// The secret is stored encrypted. `select * from forge_mfa_factors` must not be
// a list of second factors.
func TestMFA_TheStoredSecretIsCiphertext(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	enrolment, err := h.svc.Enrol(ctx, h.userID, h.email, "phone")
	if err != nil {
		t.Fatal(err)
	}
	var stored []byte
	if err := h.pool.QueryRow(ctx,
		`select secret_ciphertext from forge_mfa_factors where user_id = $1`, h.userID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), enrolment.Secret) {
		t.Fatal("the shared secret is in the database in the clear")
	}
	// And it still works, so the encryption is not merely mangling it.
	if err := h.svc.Activate(ctx, h.userID, h.code(t, enrolment.Secret)); err != nil {
		t.Fatalf("the encrypted secret could not be used: %v", err)
	}
}

// Swapping somebody's second factor must require holding the current one.
func TestMFA_CannotEnrolOverAnActiveFactor(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	enrolment, _ := h.svc.Enrol(ctx, h.userID, h.email, "phone")
	if err := h.svc.Activate(ctx, h.userID, h.code(t, enrolment.Secret)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Enrol(ctx, h.userID, h.email, "new phone"); err == nil {
		t.Fatal("a second factor was replaced without proving possession of the current one")
	} else if !errs.Is(err, errs.CodeConflict) {
		t.Fatalf("got %s", errs.CodeOf(err))
	}
}

// A deployment with no encryption key must refuse rather than store a secret in
// the clear.
func TestMFA_NoEncryptionKeyMeansNoEnrolment(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	naked := mfa.NewService(h.pool, nil, "FORGE", h.clk, logx.Discard())
	_, err := naked.Enrol(ctx, h.userID, h.email, "phone")
	if err == nil {
		t.Fatal("a second factor was enrolled with no encryption key configured")
	}
	if !errs.Is(err, errs.CodeConfigInvalid) {
		t.Fatalf("got %s", errs.CodeOf(err))
	}
	if !strings.Contains(err.Error(), "FORGE_ENCRYPTION_KEY") {
		t.Fatalf("the error does not say what to set: %v", err)
	}
}
