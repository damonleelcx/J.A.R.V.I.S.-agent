package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
)

func TestNewTokenShape(t *testing.T) {
	tok, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(tok.Plaintext)
	if err != nil {
		t.Fatalf("plaintext is not raw-url base64: %v", err)
	}
	if len(raw) != tokenEntropyBytes {
		t.Errorf("token carries %d bytes of entropy, want %d", len(raw), tokenEntropyBytes)
	}
	if len(tok.Hash) != sha256.Size {
		t.Errorf("hash is %d bytes, want %d", len(tok.Hash), sha256.Size)
	}
	if !LooksLikeToken(tok.Plaintext) {
		t.Errorf("a freshly minted token %q failed its own structural check", tok.Plaintext)
	}
}

// TestPlaintextIsUnrecoverableFromHash is the property that makes a database
// dump useless as a set of live credentials.
func TestPlaintextIsUnrecoverableFromHash(t *testing.T) {
	tok, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(tok.Hash), tok.Plaintext) {
		t.Fatal("the stored hash contains the plaintext")
	}
	if base64.RawURLEncoding.EncodeToString(tok.Hash) == tok.Plaintext {
		t.Fatal("the hash is an encoding of the plaintext, not a digest")
	}
}

func TestTokensAreUnique(t *testing.T) {
	const n = 10000
	seenPlain := make(map[string]bool, n)
	seenHash := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		tok, err := NewToken()
		if err != nil {
			t.Fatal(err)
		}
		if seenPlain[tok.Plaintext] {
			t.Fatalf("plaintext collision after %d tokens", i)
		}
		h := string(tok.Hash)
		if seenHash[h] {
			t.Fatalf("hash collision after %d tokens", i)
		}
		seenPlain[tok.Plaintext] = true
		seenHash[h] = true
	}
}

func TestTokensEqualMatchesOnlyTheRightToken(t *testing.T) {
	a, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if !TokensEqual(a.Hash, a.Plaintext) {
		t.Error("a token did not match its own hash")
	}
	if TokensEqual(a.Hash, b.Plaintext) {
		t.Error("a different token matched")
	}
	if TokensEqual(a.Hash, "") {
		t.Error("an empty candidate matched")
	}
	if TokensEqual(nil, a.Plaintext) {
		t.Error("a nil stored hash matched; an absent credential must never authenticate")
	}
	// A near-miss: same token with the last character changed.
	near := a.Plaintext[:len(a.Plaintext)-1] + "X"
	if near != a.Plaintext && TokensEqual(a.Hash, near) {
		t.Error("a one-character variation matched")
	}
}

func TestHashTokenIsDeterministic(t *testing.T) {
	// Redemption looks a token up by hash, so the same plaintext must always
	// produce the same digest — otherwise a valid link would never be found.
	const s = "some-token-value"
	if string(HashToken(s)) != string(HashToken(s)) {
		t.Fatal("HashToken is not deterministic; token lookup by hash would fail")
	}
	if string(HashToken(s)) == string(HashToken(s+"x")) {
		t.Fatal("different plaintexts produced the same digest")
	}
}

func TestLooksLikeTokenRejectsJunk(t *testing.T) {
	valid, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"empty":         "",
		"too short":     valid.Plaintext[:42],
		"too long":      valid.Plaintext + "A",
		"padding char":  strings.Repeat("A", 42) + "=",
		"standard b64+": strings.Repeat("A", 42) + "+",
		"slash":         strings.Repeat("A", 42) + "/",
		"space":         strings.Repeat("A", 42) + " ",
		"sql-ish":       strings.Repeat("A", 39) + "'--x",
	}
	for name, s := range cases {
		if LooksLikeToken(s) {
			t.Errorf("%s: %q was accepted as structurally valid", name, s)
		}
	}
	if !LooksLikeToken(valid.Plaintext) {
		t.Error("a real token was rejected")
	}
}

// TestLooksLikeTokenIsNotASecurityCheck guards against a future reader
// mistaking the cheap pre-filter for authentication. It only says "this could
// be one of ours", never "this is valid".
func TestLooksLikeTokenIsNotASecurityCheck(t *testing.T) {
	forged := strings.Repeat("A", 43)
	if !LooksLikeToken(forged) {
		t.Fatal("precondition: a well-formed forgery should pass the structural filter")
	}
	real, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if TokensEqual(real.Hash, forged) {
		t.Fatal("a structurally valid forgery authenticated; the filter is being used as a check")
	}
}

func TestPurposeValidity(t *testing.T) {
	if !PurposeEmailVerify.Valid() || !PurposePasswordReset.Valid() {
		t.Error("the two real purposes must be valid")
	}
	for _, p := range []TokenPurpose{"", "admin", "email_verify ", "EMAIL_VERIFY"} {
		if p.Valid() {
			t.Errorf("purpose %q was accepted", p)
		}
	}
}

func TestRedactTokenLeaksLittleAndNeverAll(t *testing.T) {
	tok, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	red := RedactToken(tok.Plaintext)
	if strings.Contains(red, tok.Plaintext) {
		t.Fatal("RedactToken returned the whole token")
	}
	if len(red) >= len(tok.Plaintext) {
		t.Errorf("redacted form %q is not shorter than the token", red)
	}
	if RedactToken("") != "" {
		t.Error("redacting an empty string should stay empty")
	}
	if strings.ContainsAny(RedactToken("abc"), "abc") {
		t.Error("a short value should be fully masked, not partially revealed")
	}
}
