package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// RFC 6238's published test vectors. The whole reason to implement this rather
// than take a dependency is that it can be checked against the specification,
// so it is.
//
// The RFC's vectors use the ASCII secret "12345678901234567890"; base32 of that
// is GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ.
func TestTOTP_MatchesRFC6238Vectors(t *testing.T) {
	const secret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	for _, tc := range []struct {
		unix int64
		want string
	}{
		{59, "287082"},
		{1111111109, "081804"},
		{1111111111, "050471"},
		{1234567890, "005924"},
		{2000000000, "279037"},
	} {
		got, err := TOTPCode(secret, time.Unix(tc.unix, 0).UTC())
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Fatalf("at %d the code is %s; RFC 6238 says %s", tc.unix, got, tc.want)
		}
	}
}

// A code is valid for at most ninety seconds. Zero tolerance would reject
// anybody whose phone clock is a few seconds out, which is most people.
func TestVerifyTOTP_AcceptsOneStepEitherSide(t *testing.T) {
	secret, err := NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 2, 12, 0, 30, 0, time.UTC)

	for _, offset := range []time.Duration{-30 * time.Second, 0, 30 * time.Second} {
		code, err := TOTPCode(secret, now.Add(offset))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyTOTP(secret, code, now, -1); err != nil {
			t.Fatalf("a code %s out was rejected: %v", offset, err)
		}
	}
	for _, offset := range []time.Duration{-90 * time.Second, 90 * time.Second} {
		code, _ := TOTPCode(secret, now.Add(offset))
		if _, err := VerifyTOTP(secret, code, now, -1); err == nil {
			t.Fatalf("a code %s out was accepted; the window is wider than it claims", offset)
		}
	}
}

// The window is ninety seconds in which anybody who saw the code can use it too.
// Recording the accepted step closes that to one use.
func TestVerifyTOTP_RefusesAReplay(t *testing.T) {
	secret, _ := NewTOTPSecret()
	now := time.Date(2026, 9, 2, 12, 0, 30, 0, time.UTC)
	code, _ := TOTPCode(secret, now)

	step, err := VerifyTOTP(secret, code, now, -1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = VerifyTOTP(secret, code, now, step)
	if err == nil {
		t.Fatal("the same code was accepted twice inside its window")
	}
	if !strings.Contains(err.Error(), "already been used") {
		t.Fatalf("a replay was reported as a wrong code, which sends the user to look at their "+
			"authenticator instead of at the clock: %v", err)
	}
	if !errs.Is(err, errs.CodeMFAInvalid) {
		t.Fatalf("got %s", errs.CodeOf(err))
	}
}

func TestVerifyTOTP_RejectsMalformedCodes(t *testing.T) {
	secret, _ := NewTOTPSecret()
	now := time.Now().UTC()
	for _, bad := range []string{"", "12345", "1234567", "abcdef"} {
		if _, err := VerifyTOTP(secret, bad, now, -1); err == nil {
			t.Fatalf("%q was accepted as a code", bad)
		}
	}
	// Spaces are noise, not an error: apps display codes as "123 456".
	code, _ := TOTPCode(secret, now)
	spaced := code[:3] + " " + code[3:]
	if _, err := VerifyTOTP(secret, spaced, now, -1); err != nil {
		t.Fatalf("a code with the space apps display was rejected: %v", err)
	}
}

// A secret with '=' padding is one users cannot type by hand, and apps do not
// use it.
func TestNewTOTPSecret_IsUnpaddedBase32(t *testing.T) {
	s, err := NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(s, "=") {
		t.Fatalf("the secret carries base32 padding: %q", s)
	}
	if len(s) < 32 {
		t.Fatalf("the secret is %d characters, shorter than 20 bytes of base32", len(s))
	}
	other, _ := NewTOTPSecret()
	if s == other {
		t.Fatal("two generated secrets were identical")
	}
}

func TestTOTPURI_CarriesWhatAnAppNeeds(t *testing.T) {
	uri := TOTPURI("FORGE", "someone@example.com", "JBSWY3DPEHPK3PXP")
	for _, want := range []string{"otpauth://totp/", "secret=JBSWY3DPEHPK3PXP", "issuer=FORGE",
		"digits=6", "period=30", "algorithm=SHA1"} {
		if !strings.Contains(uri, want) {
			t.Fatalf("the URI is missing %q: %s", want, uri)
		}
	}
}

// These are written on paper and retyped months later by somebody who has just
// lost their phone. Look-alike characters are a lockout with no benefit.
func TestNewRecoveryCodes_AreTypeableAndDistinct(t *testing.T) {
	codes, err := NewRecoveryCodes(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != 10 {
		t.Fatalf("%d codes", len(codes))
	}
	seen := map[string]bool{}
	for _, c := range codes {
		if seen[c] {
			t.Fatalf("duplicate recovery code %q", c)
		}
		seen[c] = true
		for _, bad := range []string{"i", "l", "o", "0", "1"} {
			if strings.Contains(c, bad) {
				t.Fatalf("code %q contains %q, which is retyped wrong from paper", c, bad)
			}
		}
	}
}

// Case and the grouping dash are noise. Rejecting a correct code because
// somebody typed it in capitals is a lockout with no security benefit.
func TestNormalizeRecoveryCode_IgnoresHowItWasTyped(t *testing.T) {
	want := NormalizeRecoveryCode("abcde-fghjk")
	for _, variant := range []string{"ABCDE-FGHJK", "abcdefghjk", "  abcde-fghjk  ", "AbCdE-fGhJk"} {
		if got := NormalizeRecoveryCode(variant); got != want {
			t.Fatalf("%q normalised to %q, want %q", variant, got, want)
		}
	}
}
