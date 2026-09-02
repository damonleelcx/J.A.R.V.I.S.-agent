package auth

import (
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

func TestHashAndVerifyRoundTrip(t *testing.T) {
	const pw = "correct horse battery staple"
	encoded, err := HashPassword(pw)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	ok, err := VerifyPassword(encoded, pw)
	if err != nil {
		t.Fatalf("VerifyPassword: %v", err)
	}
	if !ok {
		t.Fatal("the correct password did not verify")
	}
}

func TestWrongPasswordIsRejected(t *testing.T) {
	encoded, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	for _, wrong := range []string{
		"",
		"correct horse battery stapl",   // one char short
		"correct horse battery staple ", // trailing space
		"Correct horse battery staple",  // case
		"wrong entirely",
	} {
		ok, err := VerifyPassword(encoded, wrong)
		if err != nil {
			t.Fatalf("VerifyPassword(%q): %v", wrong, err)
		}
		if ok {
			t.Errorf("password %q was accepted against a different hash", wrong)
		}
	}
}

// TestHashIsSaltedPerCall is the property that makes a stolen database
// unsortable: two users with the same password must not share a hash, or an
// attacker learns which accounts to attack once.
func TestHashIsSaltedPerCall(t *testing.T) {
	const pw = "the same password twice"
	a, err := HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	b, err := HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two hashes of the same password are identical; the salt is not random per call")
	}
	// Both must still verify — a salt that breaks verification is worse than none.
	for i, h := range []string{a, b} {
		ok, err := VerifyPassword(h, pw)
		if err != nil || !ok {
			t.Errorf("hash %d did not verify: ok=%v err=%v", i, ok, err)
		}
	}
}

// TestHashNeverContainsPlaintext is blunt but load-bearing: it is the assertion
// that would fail loudly if someone "simplified" hashing into encoding.
func TestHashNeverContainsPlaintext(t *testing.T) {
	const pw = "UNIQUEPLAINTEXTMARKER12345"
	encoded, err := HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(encoded, pw) {
		t.Fatalf("the stored hash contains the plaintext: %s", encoded)
	}
}

// TestHashIsSelfDescribing is why a future cost increase is not a migration:
// the verifier reads parameters from the stored string, not from today's
// constants.
func TestHashIsSelfDescribing(t *testing.T) {
	encoded, err := HashPassword("whatever")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"$argon2id$", "v=19", "m=", "t=", "p="} {
		if !strings.Contains(encoded, want) {
			t.Errorf("hash %q is missing %q; it is not self-describing", encoded, want)
		}
	}
	if n := len(strings.Split(encoded, "$")); n != 6 {
		t.Errorf("hash has %d $-separated fields, want 6 (PHC format)", n)
	}
}

// TestOldParametersStillVerifyAndAreFlaggedForRehash is the whole point of
// encoding cost into the hash. A password stored under weaker settings must
// keep working — locking users out is not an acceptable security upgrade — and
// must be reported as stale so it is upgraded at the next sign-in.
func TestOldParametersStillVerifyAndAreFlaggedForRehash(t *testing.T) {
	// Built at deliberately weak settings rather than pasted as a literal, so
	// the fixture cannot drift away from the encoder's actual format.
	encoded, err := hashWithParams("legacy password", 8192, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := VerifyPassword(encoded, "legacy password")
	if err != nil {
		t.Fatalf("a hash with older parameters failed to verify: %v", err)
	}
	if !ok {
		t.Fatal("a hash with older parameters did not verify; raising cost would lock every existing user out")
	}
	if !NeedsRehash(encoded) {
		t.Error("a weaker hash should be reported as needing rehash")
	}
	current, err := HashPassword("legacy password")
	if err != nil {
		t.Fatal(err)
	}
	if NeedsRehash(current) {
		t.Error("a hash written with current parameters should not need rehash")
	}
}

func TestCorruptHashesAreRejectedNotPanicked(t *testing.T) {
	// A malformed hash reaching VerifyPassword means the row is corrupt. It must
	// produce a typed error — never a panic, and never a silent "true".
	cases := map[string]string{
		"empty":              "",
		"not phc":            "just-a-string",
		"wrong algo":         "$bcrypt$v=19$m=19456,t=2,p=1$c2FsdA$aGFzaA",
		"missing fields":     "$argon2id$v=19$m=19456,t=2,p=1$c2FsdA",
		"bad version":        "$argon2id$v=13$m=19456,t=2,p=1$c2FsdA$aGFzaA",
		"non-numeric cost":   "$argon2id$v=19$m=abc,t=2,p=1$c2FsdA$aGFzaA",
		"unknown cost param": "$argon2id$v=19$m=19456,t=2,x=1$c2FsdA$aGFzaA",
		"zero memory":        "$argon2id$v=19$m=0,t=2,p=1$c2FsdA$aGFzaA",
		"bad base64 salt":    "$argon2id$v=19$m=19456,t=2,p=1$!!!!$aGFzaA",
		"empty salt":         "$argon2id$v=19$m=19456,t=2,p=1$$aGFzaA",
		"lanes overflow":     "$argon2id$v=19$m=19456,t=2,p=999$c2FsdA$aGFzaA",
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			ok, err := VerifyPassword(h, "anything")
			if ok {
				t.Fatalf("corrupt hash %q verified successfully", h)
			}
			if err == nil {
				t.Fatalf("corrupt hash %q returned no error; a corrupt credential row must be reported", h)
			}
			if errs.CodeOf(err) != errs.CodeStateCorrupt {
				t.Errorf("code = %v, want STATE_CORRUPT", errs.CodeOf(err))
			}
			if !NeedsRehash(h) {
				t.Error("an unverifiable hash should be reported as needing rehash")
			}
		})
	}
}

// TestPolicyEnforcesLengthNotComposition documents a deliberate choice.
// Composition rules push people toward Password1! and add almost no entropy;
// NIST SP 800-63B dropped them. Length is what is enforced.
func TestPolicyEnforcesLengthNotComposition(t *testing.T) {
	p := PasswordPolicy{MinLength: 12}

	if err := p.Validate("short"); err == nil {
		t.Error("a 5-character password should be refused")
	} else if errs.CodeOf(err) != errs.CodePasswordTooWeak {
		t.Errorf("code = %v, want PASSWORD_TOO_WEAK", errs.CodeOf(err))
	}

	// All-lowercase, no digits, no symbols — and entirely acceptable.
	if err := p.Validate("correct horse battery staple"); err != nil {
		t.Errorf("a long passphrase was refused: %v", err)
	}
}

func TestPolicyCountsRunesNotBytes(t *testing.T) {
	// A 12-rune Chinese passphrase is 36 bytes. A byte-length check would pass
	// a 4-character one, which is the bug this test exists to prevent.
	p := PasswordPolicy{MinLength: 12}
	if err := p.Validate("密码密码"); err == nil {
		t.Error("a 4-character passphrase passed a 12-character minimum; the check is counting bytes")
	}
	if err := p.Validate("密码密码密码密码密码密码"); err != nil {
		t.Errorf("a 12-character passphrase was refused: %v", err)
	}
}

func TestPolicyRejectsWhitespaceOnly(t *testing.T) {
	p := PasswordPolicy{MinLength: 12}
	if err := p.Validate(strings.Repeat(" ", 20)); err == nil {
		t.Error("20 spaces cleared the length check and is not a password")
	}
}

func TestEmptyPasswordCannotBeHashed(t *testing.T) {
	if _, err := HashPassword(""); err == nil {
		t.Fatal("hashing an empty password must be refused at the primitive, not only at the policy")
	}
}

// hashWithParams builds a hash at explicit cost settings, for the legacy-hash
// test. It intentionally mirrors HashPassword rather than calling it, because
// its whole purpose is to produce settings HashPassword no longer produces.
func hashWithParams(plaintext string, memKiB, iters uint32, lanes uint8) (string, error) {
	return hashPasswordWith(plaintext, memKiB, iters, lanes)
}
