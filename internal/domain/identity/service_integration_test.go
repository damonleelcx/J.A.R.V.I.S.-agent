package identity_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/auth"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/identity"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/mail"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

// captureMailer records what would have been sent, and lets a test pull the
// token out of a message the way a user pulls it out of their inbox.
//
// It is a test double for the transport only — every layer below it (template
// rendering, token minting, hashing, storage) is the real thing. A double that
// replaced the service would test the double.
type captureMailer struct {
	mu   sync.Mutex
	sent []*mail.Message
	// failNext makes the next Send fail, to exercise the "account created but
	// mail undelivered" path.
	failNext bool
}

func (c *captureMailer) Send(_ context.Context, m *mail.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failNext {
		c.failNext = false
		return errs.New("test", errs.CodeMailDeliveryFail).WithDetail("injected failure")
	}
	c.sent = append(c.sent, m)
	return nil
}

func (c *captureMailer) Name() string { return "capture" }

func (c *captureMailer) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.sent)
}

func (c *captureMailer) last() *mail.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.sent) == 0 {
		return nil
	}
	return c.sent[len(c.sent)-1]
}

func (c *captureMailer) lastWithTag(tag string) *mail.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.sent) - 1; i >= 0; i-- {
		if c.sent[i].Tag == tag {
			return c.sent[i]
		}
	}
	return nil
}

func (c *captureMailer) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = nil
}

// tokenFromMessage extracts the token from a message body the way a recipient
// would: by reading the link out of the plain-text part.
func tokenFromMessage(t *testing.T, m *mail.Message) string {
	t.Helper()
	if m == nil {
		t.Fatal("no message was sent")
	}
	const marker = "token="
	i := strings.Index(m.Text, marker)
	if i < 0 {
		t.Fatalf("no token in message body:\n%s", m.Text)
	}
	rest := m.Text[i+len(marker):]
	if j := strings.IndexAny(rest, "\r\n \t"); j >= 0 {
		rest = rest[:j]
	}
	if !auth.LooksLikeToken(rest) {
		t.Fatalf("extracted %q, which is not a well-formed token", rest)
	}
	return rest
}

type harness struct {
	svc    *identity.Service
	repo   *identity.Repository
	pool   *pgxpool.Pool
	mailer *captureMailer
	clk    *clock.Fake
	cfg    config.AuthConfig
}

// newHarness builds a service against a freshly migrated, test-private schema.
func newHarness(t *testing.T) *harness { return newHarnessWith(t, nil) }

// newHarnessWith allows a test to adjust auth policy. Some properties can only
// be exercised at particular ratios — sliding idle expiry, for instance, needs
// an absolute TTL comfortably larger than the idle one — and bending the
// defaults for one test is better than choosing defaults that suit tests.
func newHarnessWith(t *testing.T, tune func(*config.AuthConfig)) *harness {
	t.Helper()

	url := os.Getenv("FORGE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("FORGE_TEST_DATABASE_URL is unset; skipping live-database tests. Run `make db-up` then `make test-integration`.")
	}

	// A schema per test, migrated with the real chain. Never an inline
	// CREATE TABLE: a fixture that approximates production tests the fixture.
	schema := "forge_it_" + strings.ToLower(strings.NewReplacer("/", "_", "-", "_").Replace(t.Name()))
	if len(schema) > 60 {
		schema = schema[:60]
	}
	ctx := context.Background()

	admin, err := db.Connect(ctx, config.DBConfig{
		URL: url, MaxConns: 4, MinConns: 1,
		MaxConnLifetime: time.Hour, MaxConnIdleTime: time.Minute, ConnectTimeout: 10 * time.Second,
	}, logx.Discard())
	if err != nil {
		t.Fatalf("cannot reach the test database: %v", err)
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
	pool, err := db.Connect(ctx, config.DBConfig{
		URL: url + sep + "search_path=" + schema + ",public", MaxConns: 8, MinConns: 1,
		MaxConnLifetime: time.Hour, MaxConnIdleTime: time.Minute, ConnectTimeout: 10 * time.Second,
	}, logx.Discard())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MigrateFS(ctx, pool, db.Files, db.MigrationsDir, logx.Discard()); err != nil {
		t.Fatalf("migrating the test schema: %v", err)
	}

	t.Cleanup(func() {
		pool.Close()
		cleanup, err := db.Connect(context.Background(), config.DBConfig{
			URL: url, MaxConns: 2, MinConns: 1,
			MaxConnLifetime: time.Minute, MaxConnIdleTime: time.Minute, ConnectTimeout: 10 * time.Second,
		}, logx.Discard())
		if err == nil {
			_, _ = cleanup.Exec(context.Background(), "drop schema if exists "+schema+" cascade")
			cleanup.Close()
		}
	})

	cfg := config.AuthConfig{
		SessionTTL:        30 * 24 * time.Hour,
		SessionIdleTTL:    14 * 24 * time.Hour,
		EmailVerifyTTL:    24 * time.Hour,
		PasswordResetTTL:  time.Hour,
		MinPasswordLength: 12,
		MaxSigninAttempts: 5,
		LockoutWindow:     15 * time.Minute,
	}
	if tune != nil {
		tune(&cfg)
	}
	clk := clock.NewFake(time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC))
	mailer := &captureMailer{}
	repo := identity.NewRepository()

	return &harness{
		svc:    identity.NewService(pool, repo, mailer, cfg, "https://forge.test", clk, logx.Discard()),
		repo:   repo,
		pool:   pool,
		mailer: mailer,
		clk:    clk,
		cfg:    cfg,
	}
}

const goodPassword = "correct horse battery staple"

func (h *harness) signUp(t *testing.T, email string) *identity.User {
	t.Helper()
	res, err := h.svc.SignUp(context.Background(), identity.SignUpInput{
		Email: email, Password: goodPassword, DisplayName: "Test User",
	}, identity.RequestContext{UserAgent: "go-test"})
	if err != nil {
		t.Fatalf("SignUp(%s): %v", email, err)
	}
	return res.User
}

func (h *harness) signUpAndVerify(t *testing.T, email string) *identity.User {
	t.Helper()
	u := h.signUp(t, email)
	tok := tokenFromMessage(t, h.mailer.lastWithTag("email_verify"))
	if _, err := h.svc.VerifyEmail(context.Background(), tok); err != nil {
		t.Fatalf("VerifyEmail: %v", err)
	}
	return u
}

// ---------------------------------------------------------------------------
// happy path
// ---------------------------------------------------------------------------

func TestSignUpVerifySignIn(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	res, err := h.svc.SignUp(ctx, identity.SignUpInput{
		Email: "  Ada.Lovelace@Example.COM ", Password: goodPassword, DisplayName: " Ada ",
	}, identity.RequestContext{UserAgent: "go-test"})
	if err != nil {
		t.Fatalf("SignUp: %v", err)
	}
	if !res.VerificationSent {
		t.Error("VerificationSent should be true when the transport succeeded")
	}
	// Normalisation: a trailing space in a form field must not create a second,
	// unreachable account that renders identically to the first.
	if res.User.Email != "ada.lovelace@example.com" {
		t.Errorf("email stored as %q; it was not normalised", res.User.Email)
	}
	if res.User.DisplayName != "Ada" {
		t.Errorf("display name stored as %q; it was not trimmed", res.User.DisplayName)
	}
	if res.User.EmailVerified() {
		t.Error("a brand new account must not be verified")
	}

	// Sign-in works before verification, deliberately: blocking it would trap a
	// user whose verification mail was lost behind a door they cannot open.
	if _, err := h.svc.SignIn(ctx, "ada.lovelace@example.com", goodPassword,
		identity.RequestContext{}); err != nil {
		t.Fatalf("sign-in before verification should succeed: %v", err)
	}

	tok := tokenFromMessage(t, h.mailer.lastWithTag("email_verify"))
	verified, err := h.svc.VerifyEmail(ctx, tok)
	if err != nil {
		t.Fatalf("VerifyEmail: %v", err)
	}
	if !verified.EmailVerified() {
		t.Fatal("account is still unverified after redeeming its token")
	}

	// Case-insensitive sign-in: the column is citext.
	signIn, err := h.svc.SignIn(ctx, "ADA.LOVELACE@EXAMPLE.COM", goodPassword,
		identity.RequestContext{UserAgent: "go-test"})
	if err != nil {
		t.Fatalf("SignIn with different case: %v", err)
	}
	if signIn.Token == "" {
		t.Fatal("no session token was returned")
	}

	user, session, err := h.svc.Authenticate(ctx, signIn.Token)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if user.ID != res.User.ID || session.UserID != res.User.ID {
		t.Error("Authenticate resolved the wrong account")
	}
}

func TestDuplicateEmailIsRejected(t *testing.T) {
	h := newHarness(t)
	h.signUp(t, "dup@example.com")

	_, err := h.svc.SignUp(context.Background(), identity.SignUpInput{
		Email: "DUP@example.com", Password: goodPassword,
	}, identity.RequestContext{})
	if err == nil {
		t.Fatal("a second account on the same address (differing only in case) was created")
	}
	if errs.CodeOf(err) != errs.CodeEmailAlreadyRegistered {
		t.Errorf("code = %v, want EMAIL_ALREADY_REGISTERED", errs.CodeOf(err))
	}
}

func TestAccountExistsEvenWhenVerificationMailFails(t *testing.T) {
	// The recoverable direction: the account exists and the user can ask for a
	// new link. The reverse — mail sent for an account that was rolled back —
	// is not recoverable, which is why mail is sent after the commit.
	h := newHarness(t)
	h.mailer.failNext = true

	res, err := h.svc.SignUp(context.Background(), identity.SignUpInput{
		Email: "nomail@example.com", Password: goodPassword,
	}, identity.RequestContext{})
	if err != nil {
		t.Fatalf("a mail failure must not fail sign-up: %v", err)
	}
	if res.VerificationSent {
		t.Error("VerificationSent must be false when the transport failed; claiming otherwise sends the user to an empty inbox")
	}
	if _, err := h.svc.SignIn(context.Background(), "nomail@example.com", goodPassword,
		identity.RequestContext{}); err != nil {
		t.Errorf("the account should exist and be usable: %v", err)
	}
}

// ---------------------------------------------------------------------------
// clock-source regression
// ---------------------------------------------------------------------------

// TestSessionSurvivesAppClockAheadOfDatabase is a regression fence.
//
// The bug: forge_sessions.created_at defaulted to the DATABASE's now(), while
// forge_users.password_changed_at was written from the APPLICATION's clock.
// Session.Live compares those two values, so any skew between the machines was
// a live defect — an app clock even slightly ahead made every freshly issued
// session look like it predated the last password change, and the user was
// signed out the instant they signed in.
//
// This test reproduces it by running the service on a fake clock set well ahead
// of the database's real time. Before the fix, Authenticate failed here with
// SESSION_REVOKED("session predates the last password change").
//
// Fix: the application clock owns every timestamp that is compared to another
// timestamp. See identity.Repository.CreateSession.
func TestSessionSurvivesAppClockAheadOfDatabase(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// The harness clock is already set to a fixed instant unrelated to the
	// database's wall clock; push it further ahead to make the skew unambiguous.
	h.clk.Advance(72 * time.Hour)

	h.signUp(t, "skew@example.com")
	res, err := h.svc.SignIn(ctx, "skew@example.com", goodPassword, identity.RequestContext{})
	if err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	if _, _, err := h.svc.Authenticate(ctx, res.Token); err != nil {
		t.Fatalf("a session issued under app-clock skew was rejected: %v\n"+
			"this means a timestamp compared against password_changed_at is again "+
			"coming from the database default instead of the application clock", err)
	}
}

// ---------------------------------------------------------------------------
// sign-in, lockout
// ---------------------------------------------------------------------------

func TestWrongPasswordIsRejected(t *testing.T) {
	h := newHarness(t)
	h.signUpAndVerify(t, "wrong@example.com")

	_, err := h.svc.SignIn(context.Background(), "wrong@example.com", "not the password",
		identity.RequestContext{})
	if err == nil {
		t.Fatal("a wrong password produced a session")
	}
	if errs.CodeOf(err) != errs.CodeInvalidCredentials {
		t.Errorf("code = %v, want INVALID_CREDENTIALS", errs.CodeOf(err))
	}
}

// TestSignInDoesNotRevealWhetherAnAccountExists is an enumeration fence.
// An unauthenticated endpoint that distinguishes "no such user" from "wrong
// password" hands an attacker a free membership oracle.
func TestSignInDoesNotRevealWhetherAnAccountExists(t *testing.T) {
	h := newHarness(t)
	h.signUpAndVerify(t, "exists@example.com")
	ctx := context.Background()

	_, errKnown := h.svc.SignIn(ctx, "exists@example.com", "wrong password", identity.RequestContext{})
	_, errUnknown := h.svc.SignIn(ctx, "absent@example.com", "wrong password", identity.RequestContext{})

	if errKnown == nil || errUnknown == nil {
		t.Fatal("both sign-ins should have failed")
	}
	if errs.CodeOf(errKnown) != errs.CodeOf(errUnknown) {
		t.Errorf("distinguishable codes: known=%v unknown=%v", errs.CodeOf(errKnown), errs.CodeOf(errUnknown))
	}
	if errKnown.Error() != errUnknown.Error() {
		t.Errorf("distinguishable messages:\n known:   %v\n unknown: %v", errKnown, errUnknown)
	}
}

// TestLockoutEngagesAfterRepeatedFailures proves the counter works and, more
// importantly, that it counts attempts on addresses with NO account. If it
// only counted real users, the absence of a lockout would itself reveal that an
// address is unregistered — enumeration through a side door.
func TestLockoutEngagesAfterRepeatedFailures(t *testing.T) {
	h := newHarness(t)
	h.signUpAndVerify(t, "locked@example.com")
	ctx := context.Background()

	for i := 0; i < h.cfg.MaxSigninAttempts; i++ {
		if _, err := h.svc.SignIn(ctx, "locked@example.com", "wrong", identity.RequestContext{}); err == nil {
			t.Fatalf("attempt %d unexpectedly succeeded", i)
		}
	}
	// The correct password must now be refused too — otherwise the lockout does
	// not actually slow a credential-stuffing run.
	_, err := h.svc.SignIn(ctx, "locked@example.com", goodPassword, identity.RequestContext{})
	if err == nil {
		t.Fatal("the account should be locked out")
	}
	if errs.CodeOf(err) != errs.CodeAccountLocked {
		t.Errorf("code = %v, want ACCOUNT_LOCKED", errs.CodeOf(err))
	}

	// Non-existent addresses are rate-limited on the same counter.
	for i := 0; i < h.cfg.MaxSigninAttempts; i++ {
		_, _ = h.svc.SignIn(ctx, "ghost@example.com", "wrong", identity.RequestContext{})
	}
	_, err = h.svc.SignIn(ctx, "ghost@example.com", "wrong", identity.RequestContext{})
	if errs.CodeOf(err) != errs.CodeAccountLocked {
		t.Errorf("attempts on a non-existent address are not rate-limited (code %v); "+
			"the absence of a lockout would itself reveal that the address is unregistered", errs.CodeOf(err))
	}
}

func TestLockoutClearsAfterTheWindow(t *testing.T) {
	h := newHarness(t)
	h.signUpAndVerify(t, "window@example.com")
	ctx := context.Background()

	for i := 0; i < h.cfg.MaxSigninAttempts; i++ {
		_, _ = h.svc.SignIn(ctx, "window@example.com", "wrong", identity.RequestContext{})
	}
	if _, err := h.svc.SignIn(ctx, "window@example.com", goodPassword, identity.RequestContext{}); err == nil {
		t.Fatal("precondition: the account should be locked")
	}

	h.clk.Advance(h.cfg.LockoutWindow + time.Minute)

	if _, err := h.svc.SignIn(ctx, "window@example.com", goodPassword, identity.RequestContext{}); err != nil {
		t.Errorf("the lockout should have elapsed: %v", err)
	}
}

// TestSuccessfulSignInResetsTheFailureCount guards the subtle half of lockout:
// counting must restart after a success. Otherwise a user who mistypes four
// times, signs in correctly, then mistypes once is locked out — punished for
// typos they already recovered from.
func TestSuccessfulSignInResetsTheFailureCount(t *testing.T) {
	h := newHarness(t)
	h.signUpAndVerify(t, "reset-count@example.com")
	ctx := context.Background()

	for i := 0; i < h.cfg.MaxSigninAttempts-1; i++ {
		_, _ = h.svc.SignIn(ctx, "reset-count@example.com", "wrong", identity.RequestContext{})
	}
	if _, err := h.svc.SignIn(ctx, "reset-count@example.com", goodPassword, identity.RequestContext{}); err != nil {
		t.Fatalf("the last attempt before the limit should succeed: %v", err)
	}
	h.clk.Advance(time.Second)

	// One more failure. If the counter did not reset, this trips the lockout.
	_, _ = h.svc.SignIn(ctx, "reset-count@example.com", "wrong", identity.RequestContext{})
	h.clk.Advance(time.Second)

	if _, err := h.svc.SignIn(ctx, "reset-count@example.com", goodPassword, identity.RequestContext{}); err != nil {
		t.Errorf("a successful sign-in should have reset the failure count, but got: %v", err)
	}
}

func TestDisabledAccountCannotSignIn(t *testing.T) {
	h := newHarness(t)
	u := h.signUpAndVerify(t, "disabled@example.com")
	ctx := context.Background()

	if err := h.repo.SetStatus(ctx, h.pool, u.ID, identity.StatusDisabled); err != nil {
		t.Fatal(err)
	}
	_, err := h.svc.SignIn(ctx, "disabled@example.com", goodPassword, identity.RequestContext{})
	if err == nil {
		t.Fatal("a disabled account signed in")
	}
	if errs.CodeOf(err) != errs.CodeForbidden {
		t.Errorf("code = %v, want FORBIDDEN", errs.CodeOf(err))
	}
}

// TestDisabledAccountCannotUseAnExistingSession covers the case that matters
// more than sign-in: disabling an account must end access that is already in
// progress, not merely prevent new sign-ins.
func TestDisabledAccountCannotUseAnExistingSession(t *testing.T) {
	h := newHarness(t)
	u := h.signUpAndVerify(t, "revoke-live@example.com")
	ctx := context.Background()

	res, err := h.svc.SignIn(ctx, "revoke-live@example.com", goodPassword, identity.RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.svc.Authenticate(ctx, res.Token); err != nil {
		t.Fatalf("precondition: the session should work: %v", err)
	}

	if err := h.repo.SetStatus(ctx, h.pool, u.ID, identity.StatusDisabled); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.svc.Authenticate(ctx, res.Token); err == nil {
		t.Fatal("a live session kept working after the account was disabled")
	}
}

// ---------------------------------------------------------------------------
// tokens: single use, expiry, invalidation
// ---------------------------------------------------------------------------

func TestVerificationTokenIsSingleUse(t *testing.T) {
	h := newHarness(t)
	h.signUp(t, "single@example.com")
	tok := tokenFromMessage(t, h.mailer.lastWithTag("email_verify"))
	ctx := context.Background()

	if _, err := h.svc.VerifyEmail(ctx, tok); err != nil {
		t.Fatalf("first redemption: %v", err)
	}
	_, err := h.svc.VerifyEmail(ctx, tok)
	if err == nil {
		t.Fatal("a token was redeemed twice")
	}
	if errs.CodeOf(err) != errs.CodeTokenAlreadyUsed {
		t.Errorf("code = %v, want TOKEN_ALREADY_USED (the user should be told it was already used, not that it is invalid)", errs.CodeOf(err))
	}
}

// TestConcurrentRedemptionAdmitsExactlyOne is the fence behind
// ConsumeAuthToken's single-statement design.
//
// The obvious implementation — SELECT, check consumed_at, then UPDATE — passes
// every sequential test in this file and loses this one: under READ COMMITTED,
// two requests carrying the same stolen reset link can both see consumed_at as
// null before either writes, and both then set a password. Performing the check
// inside the UPDATE's own WHERE clause makes Postgres serialise on the row lock,
// so exactly one caller can ever observe rows_affected = 1.
//
// # Why this runs several rounds
//
// The race is probabilistic. A single round of twelve goroutines caught the
// naive implementation only some of the time — during development one drill run
// reported a clean pass against code that was genuinely broken, and a fence that
// reports green on broken code is worse than no fence, because it is believed.
// Repeating the scenario with a fresh token each round drives the chance of a
// false pass to negligible while keeping the test under a second.
func TestConcurrentRedemptionAdmitsExactlyOne(t *testing.T) {
	h := newHarness(t)
	h.signUpAndVerify(t, "race@example.com")
	ctx := context.Background()

	const (
		rounds = 8
		racers = 12
	)
	password := goodPassword

	for round := 0; round < rounds; round++ {
		if err := h.svc.RequestPasswordReset(ctx, "race@example.com", identity.RequestContext{}); err != nil {
			t.Fatalf("round %d: RequestPasswordReset: %v", round, err)
		}
		tok := tokenFromMessage(t, h.mailer.lastWithTag("password_reset"))
		// A distinct password per round, so a round cannot accidentally pass by
		// setting the value that was already there.
		password = fmt.Sprintf("round %d replacement passphrase", round)

		var wg sync.WaitGroup
		results := make([]error, racers)
		start := make(chan struct{})

		for i := 0; i < racers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start // release together to maximise overlap
				_, results[i] = h.svc.ResetPassword(ctx, tok, password)
			}(i)
		}
		close(start)
		wg.Wait()

		successes := 0
		for i, err := range results {
			if err == nil {
				successes++
				continue
			}
			// Losers must fail for a token reason. An internal or database error
			// would mean the race is being lost to a deadlock rather than
			// resolved by the guard, which is a different bug wearing the same
			// clothes.
			switch errs.CodeOf(err) {
			case errs.CodeTokenAlreadyUsed, errs.CodeTokenInvalid, errs.CodeTokenExpired:
			default:
				t.Errorf("round %d racer %d: failed with %v (%v); expected a token-state error",
					round, i, errs.CodeOf(err), err)
			}
		}
		if successes != 1 {
			t.Fatalf("round %d: %d of %d concurrent redemptions succeeded; exactly 1 must win, "+
				"or a used reset link stays live for whoever else holds it",
				round, successes, racers)
		}
		h.clk.Advance(time.Second)
	}

	// The last winner's password is the one in force.
	if _, err := h.svc.SignIn(ctx, "race@example.com", password, identity.RequestContext{}); err != nil {
		t.Errorf("the surviving password does not work: %v", err)
	}
}

func TestExpiredTokenIsRejectedAndSaysSo(t *testing.T) {
	h := newHarness(t)
	h.signUp(t, "expired@example.com")
	tok := tokenFromMessage(t, h.mailer.lastWithTag("email_verify"))

	h.clk.Advance(h.cfg.EmailVerifyTTL + time.Minute)

	_, err := h.svc.VerifyEmail(context.Background(), tok)
	if err == nil {
		t.Fatal("an expired token was accepted")
	}
	if errs.CodeOf(err) != errs.CodeTokenExpired {
		t.Errorf("code = %v, want TOKEN_EXPIRED", errs.CodeOf(err))
	}
	// The three failure modes must stay distinguishable: "expired", "already
	// used", and "never existed" call for three different things from a user.
	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("the message should say the link expired: %v", err)
	}
}

func TestUnknownTokenIsRejected(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	forged, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	// Structurally perfect, never issued.
	_, verifyErr := h.svc.VerifyEmail(ctx, forged.Plaintext)
	if verifyErr == nil {
		t.Fatal("a forged token was accepted")
	}
	if errs.CodeOf(verifyErr) != errs.CodeTokenInvalid {
		t.Errorf("code = %v, want TOKEN_INVALID", errs.CodeOf(verifyErr))
	}
}

// TestIssuingANewLinkRetiresTheOldOne stops a mailbox from accumulating live
// credentials. Without it, an old reset link recovered from a forwarded email
// or a browser history stays usable for its whole window.
func TestIssuingANewLinkRetiresTheOldOne(t *testing.T) {
	h := newHarness(t)
	h.signUpAndVerify(t, "rotate@example.com")
	ctx := context.Background()

	if err := h.svc.RequestPasswordReset(ctx, "rotate@example.com", identity.RequestContext{}); err != nil {
		t.Fatal(err)
	}
	first := tokenFromMessage(t, h.mailer.lastWithTag("password_reset"))

	h.clk.Advance(time.Minute)
	if err := h.svc.RequestPasswordReset(ctx, "rotate@example.com", identity.RequestContext{}); err != nil {
		t.Fatal(err)
	}
	second := tokenFromMessage(t, h.mailer.lastWithTag("password_reset"))

	if first == second {
		t.Fatal("the second request reissued the same token")
	}
	if _, err := h.svc.ResetPassword(ctx, first, "some other password here"); err == nil {
		t.Fatal("the superseded link still worked")
	}
	if _, err := h.svc.ResetPassword(ctx, second, "some other password here"); err != nil {
		t.Errorf("the newest link should work: %v", err)
	}
}

// TestVerificationTokenCannotBeUsedForPasswordReset stops a purpose confusion.
// A verification link is far easier to obtain than a reset link — anyone who
// can sign up gets one — so if purposes were interchangeable, signing up would
// yield a password-reset credential.
func TestVerificationTokenCannotBeUsedForPasswordReset(t *testing.T) {
	h := newHarness(t)
	h.signUp(t, "confuse@example.com")
	verifyTok := tokenFromMessage(t, h.mailer.lastWithTag("email_verify"))

	_, err := h.svc.ResetPassword(context.Background(), verifyTok, "a completely new password")
	if err == nil {
		t.Fatal("a verification token was accepted as a password-reset token")
	}
	if errs.CodeOf(err) != errs.CodeTokenInvalid {
		t.Errorf("code = %v, want TOKEN_INVALID", errs.CodeOf(err))
	}
}

// ---------------------------------------------------------------------------
// password reset
// ---------------------------------------------------------------------------

// TestPasswordResetRevokesEveryExistingSession is the point of a reset. If a
// stolen session survived it, the user would have changed their password and
// still be compromised — while believing they had fixed it.
func TestPasswordResetRevokesEveryExistingSession(t *testing.T) {
	h := newHarness(t)
	h.signUpAndVerify(t, "revoke@example.com")
	ctx := context.Background()

	var tokens []string
	for i := 0; i < 3; i++ {
		res, err := h.svc.SignIn(ctx, "revoke@example.com", goodPassword, identity.RequestContext{})
		if err != nil {
			t.Fatal(err)
		}
		tokens = append(tokens, res.Token)
		h.clk.Advance(time.Second)
	}
	for i, tok := range tokens {
		if _, _, err := h.svc.Authenticate(ctx, tok); err != nil {
			t.Fatalf("precondition: session %d should be live: %v", i, err)
		}
	}

	if err := h.svc.RequestPasswordReset(ctx, "revoke@example.com", identity.RequestContext{}); err != nil {
		t.Fatal(err)
	}
	resetTok := tokenFromMessage(t, h.mailer.lastWithTag("password_reset"))
	h.clk.Advance(time.Second)

	const newPassword = "an entirely different passphrase"
	if _, err := h.svc.ResetPassword(ctx, resetTok, newPassword); err != nil {
		t.Fatalf("ResetPassword: %v", err)
	}

	for i, tok := range tokens {
		if _, _, err := h.svc.Authenticate(ctx, tok); err == nil {
			t.Errorf("session %d survived a password reset", i)
		}
	}
	if _, err := h.svc.SignIn(ctx, "revoke@example.com", goodPassword, identity.RequestContext{}); err == nil {
		t.Error("the old password still works after a reset")
	}
	if _, err := h.svc.SignIn(ctx, "revoke@example.com", newPassword, identity.RequestContext{}); err != nil {
		t.Errorf("the new password does not work after a reset: %v", err)
	}
}

func TestPasswordResetNotifiesTheAccountHolder(t *testing.T) {
	// If an attacker performs the reset, this message is the account holder's
	// only signal that it happened.
	h := newHarness(t)
	h.signUpAndVerify(t, "notify@example.com")
	ctx := context.Background()

	if err := h.svc.RequestPasswordReset(ctx, "notify@example.com", identity.RequestContext{}); err != nil {
		t.Fatal(err)
	}
	tok := tokenFromMessage(t, h.mailer.lastWithTag("password_reset"))
	h.mailer.reset()

	if _, err := h.svc.ResetPassword(ctx, tok, "a totally different password"); err != nil {
		t.Fatal(err)
	}
	notice := h.mailer.lastWithTag("password_changed")
	if notice == nil {
		t.Fatal("no password-changed notification was sent")
	}
	if !strings.Contains(notice.Text, "did NOT make this change") {
		t.Error("the notification should tell the reader what to do if the change was not theirs")
	}
}

// TestPasswordResetIsEnumerationSafe is the fence behind the documented
// asymmetry: sign-up may reveal that an address is registered, this must not.
// "Which of these leaked addresses has an account?" is exactly the question an
// attacker asks before a credential-stuffing run, and this endpoint is
// unauthenticated.
func TestPasswordResetIsEnumerationSafe(t *testing.T) {
	h := newHarness(t)
	h.signUpAndVerify(t, "known@example.com")
	ctx := context.Background()
	h.mailer.reset()

	errKnown := h.svc.RequestPasswordReset(ctx, "known@example.com", identity.RequestContext{})
	errUnknown := h.svc.RequestPasswordReset(ctx, "definitely-not-here@example.com", identity.RequestContext{})

	if errKnown != nil || errUnknown != nil {
		t.Fatalf("both must return nil so the caller cannot branch: known=%v unknown=%v", errKnown, errUnknown)
	}
	// Exactly one message: the known address got mail, the unknown one did not.
	// The *caller* returns an identical response either way; the difference must
	// live only in the mailbox of whoever owns the address.
	if got := h.mailer.count(); got != 1 {
		t.Errorf("sent %d messages, want exactly 1 (only the real account)", got)
	}
}

func TestResetRejectsAWeakNewPassword(t *testing.T) {
	h := newHarness(t)
	h.signUpAndVerify(t, "weak@example.com")
	ctx := context.Background()

	if err := h.svc.RequestPasswordReset(ctx, "weak@example.com", identity.RequestContext{}); err != nil {
		t.Fatal(err)
	}
	tok := tokenFromMessage(t, h.mailer.lastWithTag("password_reset"))

	_, err := h.svc.ResetPassword(ctx, tok, "short")
	if err == nil {
		t.Fatal("a short password was accepted")
	}
	if errs.CodeOf(err) != errs.CodePasswordTooWeak {
		t.Errorf("code = %v, want PASSWORD_TOO_WEAK", errs.CodeOf(err))
	}
	// The token must survive a rejected attempt. Burning it on a policy failure
	// would strand the user: they would need a fresh link merely for typing a
	// password that was too short.
	if _, err := h.svc.ResetPassword(ctx, tok, "a properly long password"); err != nil {
		t.Errorf("the token should still be usable after a policy rejection: %v", err)
	}
}

// ---------------------------------------------------------------------------
// sessions
// ---------------------------------------------------------------------------

func TestSignOutRevokesOnlyThatSession(t *testing.T) {
	h := newHarness(t)
	h.signUpAndVerify(t, "signout@example.com")
	ctx := context.Background()

	a, err := h.svc.SignIn(ctx, "signout@example.com", goodPassword, identity.RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	h.clk.Advance(time.Second)
	b, err := h.svc.SignIn(ctx, "signout@example.com", goodPassword, identity.RequestContext{})
	if err != nil {
		t.Fatal(err)
	}

	if err := h.svc.SignOut(ctx, a.Session.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.svc.Authenticate(ctx, a.Token); err == nil {
		t.Error("the signed-out session still authenticates")
	}
	if _, _, err := h.svc.Authenticate(ctx, b.Token); err != nil {
		t.Errorf("signing out one device signed out another: %v", err)
	}
}

func TestSignOutAllDevicesKeepsTheCurrentOne(t *testing.T) {
	h := newHarness(t)
	u := h.signUpAndVerify(t, "signoutall@example.com")
	ctx := context.Background()

	var sessions []*identity.SignInResult
	for i := 0; i < 3; i++ {
		r, err := h.svc.SignIn(ctx, "signoutall@example.com", goodPassword, identity.RequestContext{})
		if err != nil {
			t.Fatal(err)
		}
		sessions = append(sessions, r)
		h.clk.Advance(time.Second)
	}
	current := sessions[2]

	n, err := h.svc.SignOutAllDevices(ctx, u.ID, current.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("revoked %d sessions, want 2", n)
	}
	for i := 0; i < 2; i++ {
		if _, _, err := h.svc.Authenticate(ctx, sessions[i].Token); err == nil {
			t.Errorf("session %d survived sign-out-all", i)
		}
	}
	// Signing a user out of the browser they just used is a confusing way to
	// confirm that other devices were signed out.
	if _, _, err := h.svc.Authenticate(ctx, current.Token); err != nil {
		t.Errorf("the requesting session was signed out too: %v", err)
	}
}

func TestSessionExpiresAbsolutely(t *testing.T) {
	h := newHarness(t)
	h.signUpAndVerify(t, "absolute@example.com")
	ctx := context.Background()

	res, err := h.svc.SignIn(ctx, "absolute@example.com", goodPassword, identity.RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	h.clk.Advance(h.cfg.SessionTTL + time.Minute)

	_, _, err = h.svc.Authenticate(ctx, res.Token)
	if err == nil {
		t.Fatal("an expired session still authenticates")
	}
	if errs.CodeOf(err) != errs.CodeSessionExpired {
		t.Errorf("code = %v, want SESSION_EXPIRED", errs.CodeOf(err))
	}
}

func TestSessionExpiresWhenIdle(t *testing.T) {
	h := newHarness(t)
	h.signUpAndVerify(t, "idle@example.com")
	ctx := context.Background()

	res, err := h.svc.SignIn(ctx, "idle@example.com", goodPassword, identity.RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	// Well within the absolute TTL, well past the idle TTL.
	h.clk.Advance(h.cfg.SessionIdleTTL + time.Hour)

	_, _, err = h.svc.Authenticate(ctx, res.Token)
	if err == nil {
		t.Fatal("an idle session still authenticates")
	}
	if errs.CodeOf(err) != errs.CodeSessionExpired {
		t.Errorf("code = %v, want SESSION_EXPIRED", errs.CodeOf(err))
	}
}

// TestActivityPostponesIdleExpiry proves the idle timer is a sliding window
// rather than a second absolute deadline.
func TestActivityPostponesIdleExpiry(t *testing.T) {
	// A wide absolute-to-idle ratio, so several sliding steps fit inside the
	// absolute lifetime and the test measures what it claims to.
	h := newHarnessWith(t, func(c *config.AuthConfig) {
		c.SessionTTL = 30 * 24 * time.Hour
		c.SessionIdleTTL = 24 * time.Hour
	})
	h.signUpAndVerify(t, "active@example.com")
	ctx := context.Background()

	res, err := h.svc.SignIn(ctx, "active@example.com", goodPassword, identity.RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	// Use the session repeatedly, each time just under the idle limit. Bounded
	// so the total stays inside the ABSOLUTE ttl — an earlier version of this
	// test advanced past it and then blamed idle expiry for an absolute one.
	steps := int(h.cfg.SessionTTL/(h.cfg.SessionIdleTTL-time.Hour)) - 1
	if steps < 2 {
		t.Fatalf("test config leaves no room to exercise idle sliding: absolute=%s idle=%s", h.cfg.SessionTTL, h.cfg.SessionIdleTTL)
	}
	for i := 0; i < steps; i++ {
		h.clk.Advance(h.cfg.SessionIdleTTL - time.Hour)
		if _, _, err := h.svc.Authenticate(ctx, res.Token); err != nil {
			t.Fatalf("iteration %d of %d: an actively used session expired: %v", i, steps, err)
		}
	}
}

func TestForgedSessionTokenIsRejected(t *testing.T) {
	h := newHarness(t)
	h.signUpAndVerify(t, "forged@example.com")
	ctx := context.Background()

	forged, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	for name, tok := range map[string]string{
		"never issued": forged.Plaintext,
		"empty":        "",
		"junk":         "not-a-token",
		"sql-ish":      "' or 1=1 --",
	} {
		if _, _, err := h.svc.Authenticate(ctx, tok); err == nil {
			t.Errorf("%s: authenticated with %q", name, tok)
		}
	}
}

func TestListSessionsShowsOnlyLiveOnes(t *testing.T) {
	h := newHarness(t)
	u := h.signUpAndVerify(t, "list@example.com")
	ctx := context.Background()

	a, err := h.svc.SignIn(ctx, "list@example.com", goodPassword, identity.RequestContext{UserAgent: "device-a"})
	if err != nil {
		t.Fatal(err)
	}
	h.clk.Advance(time.Second)
	if _, err := h.svc.SignIn(ctx, "list@example.com", goodPassword, identity.RequestContext{UserAgent: "device-b"}); err != nil {
		t.Fatal(err)
	}

	live, err := h.svc.ListSessions(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 2 {
		t.Fatalf("listed %d sessions, want 2", len(live))
	}

	if err := h.svc.SignOut(ctx, a.Session.ID); err != nil {
		t.Fatal(err)
	}
	live, err = h.svc.ListSessions(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 {
		t.Fatalf("listed %d sessions after signing one out, want 1", len(live))
	}
	if live[0].UserAgent != "device-b" {
		t.Errorf("the wrong session survived: %q", live[0].UserAgent)
	}
}

// TestSessionTokenIsNeverStored is the property that makes a database dump
// useless as a set of live sessions.
func TestSessionTokenIsNeverStored(t *testing.T) {
	h := newHarness(t)
	h.signUpAndVerify(t, "nostore@example.com")
	ctx := context.Background()

	res, err := h.svc.SignIn(ctx, "nostore@example.com", goodPassword, identity.RequestContext{})
	if err != nil {
		t.Fatal(err)
	}

	// Scan every text column of every identity table for the plaintext.
	var found int
	err = h.pool.QueryRow(ctx, `
		select count(*) from forge_sessions
		 where token_hash::text like '%' || $1 || '%'`, res.Token).Scan(&found)
	if err != nil {
		t.Fatal(err)
	}
	if found != 0 {
		t.Error("the session token plaintext appears in forge_sessions")
	}

	// And prove the stored digest is a digest: it must not equal the plaintext.
	var stored []byte
	if err := h.pool.QueryRow(ctx,
		`select token_hash from forge_sessions where id = $1`, res.Session.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if string(stored) == res.Token {
		t.Fatal("the stored value IS the token; sessions are being stored in the clear")
	}
	if !auth.TokensEqual(stored, res.Token) {
		t.Fatal("the stored digest does not match the issued token")
	}
}

// ---------------------------------------------------------------------------
// change password
// ---------------------------------------------------------------------------

func TestChangePasswordRequiresTheCurrentOne(t *testing.T) {
	h := newHarness(t)
	u := h.signUpAndVerify(t, "change@example.com")
	ctx := context.Background()

	_, err := h.svc.ChangePassword(ctx, u.ID, "the wrong current password", "a valid new password",
		identity.RequestContext{})
	if err == nil {
		t.Fatal("the password was changed without knowing the current one")
	}
	if errs.CodeOf(err) != errs.CodeInvalidCredentials {
		t.Errorf("code = %v, want INVALID_CREDENTIALS", errs.CodeOf(err))
	}
}

// TestChangePasswordRotatesTheCallersSession pins the resolution of a real
// conflict between two mechanisms.
//
// "Revoke everything except the caller's session" is impossible here: the
// password_changed_at watermark invalidates every session created before it and
// admits no exceptions, which is precisely what makes a password change take
// effect everywhere in one unfailable write. So ChangePassword issues a
// REPLACEMENT session instead — which is also the right security behaviour,
// since reissuing a credential after a privilege change defeats session
// fixation.
func TestChangePasswordRotatesTheCallersSession(t *testing.T) {
	h := newHarness(t)
	u := h.signUpAndVerify(t, "change2@example.com")
	ctx := context.Background()

	other, err := h.svc.SignIn(ctx, "change2@example.com", goodPassword, identity.RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	h.clk.Advance(time.Second)
	current, err := h.svc.SignIn(ctx, "change2@example.com", goodPassword, identity.RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	h.clk.Advance(time.Second)

	const newPassword = "a brand new long password"
	rotated, err := h.svc.ChangePassword(ctx, u.ID, goodPassword, newPassword, identity.RequestContext{})
	if err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}

	// Every pre-existing session is dead, including the caller's own.
	if _, _, err := h.svc.Authenticate(ctx, other.Token); err == nil {
		t.Error("another device stayed signed in after a password change")
	}
	if _, _, err := h.svc.Authenticate(ctx, current.Token); err == nil {
		t.Error("the caller's OLD token still works; the credential was not rotated")
	}
	// The replacement works, and is a different credential.
	if rotated.Token == current.Token {
		t.Fatal("the rotated token is identical to the old one")
	}
	if _, _, err := h.svc.Authenticate(ctx, rotated.Token); err != nil {
		t.Errorf("the replacement session does not authenticate: %v", err)
	}
	if _, err := h.svc.SignIn(ctx, "change2@example.com", newPassword, identity.RequestContext{}); err != nil {
		t.Errorf("the new password does not work: %v", err)
	}
}
