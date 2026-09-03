package secretbox

import (
	"bytes"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

const goodKey = "a-passphrase-long-enough-to-be-taken-seriously-1234"

func TestBox_RoundTrips(t *testing.T) {
	b, err := New(goodKey)
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte("JBSWY3DPEHPK3PXP")
	sealed, err := b.Seal(secret)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, secret) {
		t.Fatal("the plaintext is present in the ciphertext")
	}
	opened, err := b.Open(sealed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(opened, secret) {
		t.Fatalf("round trip produced %q", opened)
	}
}

// A repeated nonce under GCM is catastrophic rather than merely weak, so the
// same plaintext must not seal to the same bytes twice.
func TestBox_SealingTwiceProducesDifferentCiphertext(t *testing.T) {
	b, _ := New(goodKey)
	a, _ := b.Seal([]byte("same"))
	c, _ := b.Seal([]byte("same"))
	if bytes.Equal(a, c) {
		t.Fatal("two seals of the same value produced identical ciphertext, so the nonce is being reused")
	}
}

// GCM authenticates as well as encrypts. Without that, somebody with write
// access to the database could flip bits until a factor verified against a
// secret they chose.
func TestBox_AnAlteredCiphertextDoesNotOpen(t *testing.T) {
	b, _ := New(goodKey)
	sealed, _ := b.Seal([]byte("JBSWY3DPEHPK3PXP"))

	altered := append([]byte(nil), sealed...)
	altered[len(altered)-1] ^= 0x01
	if _, err := b.Open(altered); err == nil {
		t.Fatal("a ciphertext with one flipped bit opened")
	}

	truncated := sealed[:len(sealed)-1]
	if _, err := b.Open(truncated); err == nil {
		t.Fatal("a truncated ciphertext opened")
	}
	if _, err := b.Open([]byte("short")); err == nil {
		t.Fatal("a value too short to be a ciphertext opened")
	}
}

// A different key must not open it, and the failure has to say so — an operator
// who rotated the key needs to know that is what happened.
func TestBox_ADifferentKeyCannotOpenIt(t *testing.T) {
	a, _ := New(goodKey)
	sealed, _ := a.Seal([]byte("JBSWY3DPEHPK3PXP"))

	b, _ := New("a-completely-different-passphrase-also-long-enough")
	_, err := b.Open(sealed)
	if err == nil {
		t.Fatal("a value sealed with one key opened with another")
	}
	if !errs.Is(err, errs.CodeStateCorrupt) {
		t.Fatalf("got %s; a key mismatch is corrupt state, not a bad request", errs.CodeOf(err))
	}
	if !strings.Contains(err.Error(), "has changed") {
		t.Fatalf("the error does not tell the operator what to look at: %v", err)
	}
}

// The key is stretched to 32 bytes whatever its length, so a short passphrase
// produces something that looks correct everywhere and protects nothing. It is
// refused where somebody can still be told.
func TestNew_RefusesAShortKey(t *testing.T) {
	for _, bad := range []string{"", "short", strings.Repeat("x", 31)} {
		if _, err := New(bad); err == nil {
			t.Fatalf("a %d-character key was accepted", len(bad))
		}
	}
	if _, err := New(strings.Repeat("x", 32)); err != nil {
		t.Fatalf("a 32-character key was refused: %v", err)
	}
}

// A nil box refuses rather than panicking. A deployment with no key configured
// must fail at the point of use with something an operator can read.
func TestBox_NilRefusesRatherThanPanics(t *testing.T) {
	var b *Box
	if _, err := b.Seal([]byte("x")); err == nil {
		t.Fatal("a nil box sealed something")
	}
	if _, err := b.Open([]byte("x")); err == nil {
		t.Fatal("a nil box opened something")
	}
}

// The fingerprint lets two deployments confirm they hold the same key without
// either printing it.
func TestFingerprint_IsStableAndDoesNotRevealTheKey(t *testing.T) {
	if Fingerprint(goodKey) != Fingerprint(goodKey) {
		t.Fatal("the fingerprint is not stable")
	}
	if Fingerprint(goodKey) == Fingerprint(goodKey+"x") {
		t.Fatal("two different keys share a fingerprint")
	}
	fp := Fingerprint(goodKey)
	if strings.Contains(goodKey, fp) || len(fp) > 16 {
		t.Fatalf("the fingerprint %q is long enough to be worth attacking", fp)
	}
}
