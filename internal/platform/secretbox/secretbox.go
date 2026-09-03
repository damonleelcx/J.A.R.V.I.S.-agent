// Package secretbox encrypts small values at rest (PRD SEC-02).
//
// # What this is for, and what it is not
//
// Wave 5 established that FORGE does not store credentials: it brokers them,
// reading a value from the process environment when a granted tool needs one.
// That works because those credentials belong to the DEPLOYMENT, and something
// already holds them.
//
// A TOTP shared secret is different. It belongs to a user, there is no
// environment variable an operator could put it in, and FORGE must hold it in
// order to verify anything at all. So it is stored — encrypted.
//
// # The honest claim
//
// AES-256-GCM with a key read from the process environment. This defends a
// stolen database: a backup, a dump, a replica somebody got at. It does NOT
// defend an attacker who has the database AND the process environment, because
// they have both halves.
//
// That is the boundary, and it is stated rather than implied. This is not a KMS,
// there is no hardware in it, and the key does not rotate on its own. What it
// buys is that `select * from forge_mfa_factors` is not a list of second
// factors, which is the specific thing that would otherwise be true.
//
// # Why AEAD rather than plain encryption
//
// GCM authenticates as well as encrypts, so a ciphertext altered in the database
// fails to open rather than decrypting to something else. Without that, an
// attacker with write access could flip bits until a factor verified against a
// secret they chose.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// KeyID names the key a ciphertext was sealed with.
//
// Stored beside every ciphertext so a future rotation can tell what still needs
// re-wrapping. One value today; the column exists so that rotation is a
// migration rather than a redesign.
const DefaultKeyID = "default"

// minKeyLength is the shortest passphrase accepted.
//
// The key is stretched to 32 bytes by SHA-256 regardless, which means a short
// passphrase produces a perfectly well-formed key with almost no entropy behind
// it — the dangerous case, because nothing downstream looks wrong. So the length
// is checked here, where the operator can still be told.
const minKeyLength = 32

// Box seals and opens values.
type Box struct {
	aead  cipher.AEAD
	keyID string
}

// New derives a box from a passphrase.
//
// The passphrase is hashed to 32 bytes rather than used directly, so an operator
// is not required to produce exactly 32 bytes of key material by hand — which is
// the kind of requirement that gets met with a padded word.
func New(passphrase string) (*Box, error) {
	const op = "secretbox.New"

	if len(strings.TrimSpace(passphrase)) < minKeyLength {
		return nil, errs.New(op, errs.CodeConfigInvalid).
			WithDetail("the encryption key is shorter than %d characters. It is stretched to a "+
				"well-formed 32-byte key whatever its length, so a short one produces something that "+
				"looks correct everywhere and protects nothing. Generate one with "+
				"`openssl rand -base64 48`.", minKeyLength)
	}
	sum := sha256.Sum256([]byte(passphrase))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeConfigInvalid, err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeConfigInvalid, err)
	}
	return &Box{aead: aead, keyID: DefaultKeyID}, nil
}

// KeyID returns the identifier to store beside a ciphertext.
func (b *Box) KeyID() string { return b.keyID }

// Seal encrypts a value.
//
// The nonce is random per call and prefixed to the ciphertext. Random rather
// than a counter because there is no counter this package could keep that would
// survive a restart, and a repeated nonce under GCM is catastrophic rather than
// merely weak.
func (b *Box) Seal(plaintext []byte) ([]byte, error) {
	const op = "secretbox.Box.Seal"

	if b == nil {
		return nil, errs.New(op, errs.CodeConfigInvalid).
			WithDetail("no encryption key is configured, so nothing that must be stored encrypted can be stored at all")
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, errs.Wrap(op, errs.CodeInternal, err).
			WithDetail("the system's entropy source is unavailable")
	}
	return b.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Open decrypts a value.
//
// A ciphertext that fails to open is reported as corrupt state rather than as a
// bad input: it means either the key changed or somebody altered the row, and
// both are conditions an operator has to know about rather than a request that
// can be retried.
func (b *Box) Open(ciphertext []byte) ([]byte, error) {
	const op = "secretbox.Box.Open"

	if b == nil {
		return nil, errs.New(op, errs.CodeConfigInvalid).
			WithDetail("no encryption key is configured, so stored values cannot be read")
	}
	n := b.aead.NonceSize()
	if len(ciphertext) < n {
		return nil, errs.New(op, errs.CodeStateCorrupt).
			WithDetail("the stored value is %d bytes, shorter than a nonce; it is not a ciphertext this build wrote",
				len(ciphertext))
	}
	plaintext, err := b.aead.Open(nil, ciphertext[:n], ciphertext[n:], nil)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeStateCorrupt, err).
			WithDetail("a stored value could not be decrypted. Either FORGE_ENCRYPTION_KEY has changed " +
				"since it was written, or the row was altered. Neither is recoverable by retrying, and " +
				"a changed key means every value sealed with the old one is unreadable.")
	}
	return plaintext, nil
}

// Fingerprint returns a short, stable identifier for a key.
//
// For an operator confirming that two deployments hold the same key without
// either of them printing it. Truncated deliberately: enough to compare, not
// enough to attack.
func Fingerprint(passphrase string) string {
	sum := sha256.Sum256([]byte("forge.secretbox.fingerprint.v1" + passphrase))
	return hex.EncodeToString(sum[:4])
}
