package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// TOTP, as RFC 6238 defines it (PRD SEC-02).
//
// # Why this is thirty lines rather than a dependency
//
// The algorithm is HMAC-SHA1 over a counter, truncated. It is small enough to
// read, it has published test vectors, and it does not change. A dependency here
// would be a supply-chain surface on the authentication path in exchange for
// code somebody can check by eye.
//
// SHA-1 is not a mistake: RFC 6238 specifies it, every authenticator app
// implements it, and its use here is as an HMAC, where the collision weaknesses
// that retired SHA-1 elsewhere do not apply.

const (
	// totpPeriod is the step length. Thirty seconds, because that is what every
	// authenticator app assumes and this is not a place to be different.
	totpPeriod = 30 * time.Second
	// totpDigits is the code length.
	totpDigits = 6
	// totpSkew is how many steps either side of now are accepted.
	//
	// One step, so a code is valid for at most ninety seconds. Zero would reject
	// anybody whose phone clock is a few seconds out, which is most people;
	// larger windows widen the guessing surface for no benefit somebody asked
	// for.
	totpSkew = 1
	// totpSecretBytes is the shared secret's length. Twenty bytes is RFC 4226's
	// recommendation and what authenticator apps expect.
	totpSecretBytes = 20
)

// NewTOTPSecret generates a shared secret, base32-encoded as apps expect.
func NewTOTPSecret() (string, error) {
	raw := make([]byte, totpSecretBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", errs.Wrap("auth.NewTOTPSecret", errs.CodeInternal, err).
			WithDetail("the system's entropy source is unavailable")
	}
	// No padding: authenticator apps and otpauth:// URIs do not use it, and a
	// secret with '=' in it is one users cannot type in by hand.
	return strings.TrimRight(base32.StdEncoding.EncodeToString(raw), "="), nil
}

// TOTPCode computes the code for a secret at an instant.
func TOTPCode(secret string, at time.Time) (string, error) {
	step := at.UTC().Unix() / int64(totpPeriod.Seconds())
	return totpAtStep(secret, step)
}

func totpAtStep(secret string, step int64) (string, error) {
	const op = "auth.TOTPCode"

	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).
		DecodeString(strings.ToUpper(strings.TrimSpace(strings.ReplaceAll(secret, " ", ""))))
	if err != nil {
		return "", errs.Wrap(op, errs.CodeStateCorrupt, err).
			WithDetail("the stored TOTP secret is not valid base32")
	}
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(step))

	mac := hmac.New(sha1.New, key)
	mac.Write(counter[:])
	sum := mac.Sum(nil)

	// Dynamic truncation, RFC 4226 §5.3.
	offset := sum[len(sum)-1] & 0x0f
	value := (uint32(sum[offset]&0x7f) << 24) |
		(uint32(sum[offset+1]) << 16) |
		(uint32(sum[offset+2]) << 8) |
		uint32(sum[offset+3])

	mod := uint32(1)
	for i := 0; i < totpDigits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", totpDigits, value%mod), nil
}

// VerifyTOTP checks a code and returns the step it matched.
//
// # Why the step comes back
//
// So the caller can refuse a replay. A code is valid for up to ninety seconds,
// which is ninety seconds in which anybody who saw it — over a shoulder, in a
// screenshot, in a log — can use it too. Recording the last accepted step and
// refusing anything at or below it closes that window to one use.
//
// The comparison is constant-time. A code is a six-digit secret and an
// early-exit comparison leaks how much of it was right.
func VerifyTOTP(secret, code string, at time.Time, lastStep int64) (int64, error) {
	const op = "auth.VerifyTOTP"

	code = strings.TrimSpace(strings.ReplaceAll(code, " ", ""))
	if len(code) != totpDigits {
		return 0, errs.New(op, errs.CodeMFAInvalid).
			WithDetail("a code is %d digits; that one is %d", totpDigits, len(code))
	}
	now := at.UTC().Unix() / int64(totpPeriod.Seconds())

	// Every candidate step is evaluated, and the loop does not exit early on a
	// match. Returning as soon as one matches would make an accepted code
	// measurably faster than a rejected one, which says which step it was.
	matched := int64(-1)
	for step := now - totpSkew; step <= now+totpSkew; step++ {
		candidate, err := totpAtStep(secret, step)
		if err != nil {
			return 0, err
		}
		if hmac.Equal([]byte(candidate), []byte(code)) && matched < 0 {
			matched = step
		}
	}
	if matched < 0 {
		return 0, errs.New(op, errs.CodeMFAInvalid).
			WithDetail("that code is not valid now. Check the device's clock: codes change every %d seconds "+
				"and one either side of the current step is accepted.", int(totpPeriod.Seconds()))
	}
	if matched <= lastStep {
		// The replay case, named as itself. "Wrong code" would send somebody
		// looking at their authenticator when the real answer is that this code
		// has already been used.
		return 0, errs.New(op, errs.CodeMFAInvalid).
			WithDetail("that code has already been used. Each one works once; wait for the next.")
	}
	return matched, nil
}

// TOTPURI renders the otpauth:// URI an authenticator app scans.
//
// The secret is in it, necessarily — that is what enrolment transfers. It is
// therefore shown once, at enrolment, and never stored or logged in this form.
func TOTPURI(issuer, account, secret string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprintf("%d", totpDigits))
	q.Set("period", fmt.Sprintf("%d", int(totpPeriod.Seconds())))
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// NewRecoveryCodes generates single-use codes for when the authenticator is gone.
//
// Grouped with a dash and drawn from an alphabet without look-alike characters,
// because these are written down and retyped months later by somebody who has
// just lost their phone.
func NewRecoveryCodes(n int) ([]string, error) {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789" // no i, l, o, 0, 1
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		raw := make([]byte, 10)
		if _, err := rand.Read(raw); err != nil {
			return nil, errs.Wrap("auth.NewRecoveryCodes", errs.CodeInternal, err)
		}
		var b strings.Builder
		for j, c := range raw {
			if j == 5 {
				b.WriteByte('-')
			}
			b.WriteByte(alphabet[int(c)%len(alphabet)])
		}
		out = append(out, b.String())
	}
	return out, nil
}

// NormalizeRecoveryCode canonicalises a code for comparison.
//
// People retype these from paper. Case and the grouping dash are noise, and
// rejecting a correct code because somebody typed it in capitals is a lockout
// with no security benefit.
func NormalizeRecoveryCode(s string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), "-", ""))
}
