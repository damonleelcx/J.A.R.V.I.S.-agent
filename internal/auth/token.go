package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// tokenEntropyBytes is the size of the random component of every credential
// this package mints: session tokens, email-verification tokens, and
// password-reset tokens.
//
// 32 bytes is 256 bits. These values travel in URLs and cookies where they may
// be logged by intermediaries, so the guessing margin needs to be absurd rather
// than merely adequate.
const tokenEntropyBytes = 32

// Token is a freshly minted credential.
//
// It carries the plaintext exactly once — the only moment it exists in this
// system. The plaintext goes to the user (in a link or a cookie); only Hash is
// persisted. There is deliberately no way to recover the plaintext from stored
// state, which is what makes a database dump useless as a set of live
// credentials.
type Token struct {
	// Plaintext is what the user receives. Never store, never log.
	Plaintext string
	// Hash is what the database holds.
	Hash []byte
}

// NewToken mints a credential.
func NewToken() (Token, error) {
	const op = "auth.NewToken"

	raw := make([]byte, tokenEntropyBytes)
	if _, err := rand.Read(raw); err != nil {
		return Token{}, errs.Wrap(op, errs.CodeInternal, err).
			WithDetail("cannot read entropy to mint a token")
	}
	// URL-safe and unpadded, because these appear in verification links and a
	// '+' or '=' survives a mail client's URL rewriting far less reliably.
	plaintext := base64.RawURLEncoding.EncodeToString(raw)
	return Token{Plaintext: plaintext, Hash: HashToken(plaintext)}, nil
}

// HashToken returns the digest stored for a token.
//
// SHA-256 rather than argon2, and that difference is deliberate: a password is
// low-entropy and human-chosen, so it needs a slow hash to survive an offline
// attack. A 256-bit random token has no dictionary to attack — slowing the hash
// would buy nothing and would put a memory-hard computation on the path of
// every single authenticated request.
func HashToken(plaintext string) []byte {
	sum := sha256.Sum256([]byte(plaintext))
	return sum[:]
}

// TokensEqual compares a candidate against a stored digest in constant time.
func TokensEqual(storedHash []byte, candidatePlaintext string) bool {
	return subtle.ConstantTimeCompare(storedHash, HashToken(candidatePlaintext)) == 1
}

// TokenPurpose discriminates the rows in forge_auth_tokens.
type TokenPurpose string

const (
	// PurposeEmailVerify proves control of an email address.
	PurposeEmailVerify TokenPurpose = "email_verify"
	// PurposePasswordReset authorises setting a new password without knowing
	// the old one. It is a live credential and is given a shorter life than
	// verification for exactly that reason.
	PurposePasswordReset TokenPurpose = "password_reset"
)

// Valid reports whether p is a recognised purpose. The database enforces this
// too; checking here turns a constraint violation into a typed error at the
// boundary, where the caller can still say something useful.
func (p TokenPurpose) Valid() bool {
	return p == PurposeEmailVerify || p == PurposePasswordReset
}

// String implements fmt.Stringer.
func (p TokenPurpose) String() string { return string(p) }

// LooksLikeToken reports whether s is structurally one of our tokens. It is a
// cheap pre-filter so an obviously malformed value is rejected before it costs
// a database round trip — not a security check, and never used as one.
func LooksLikeToken(s string) bool {
	// RawURLEncoding of 32 bytes is 43 characters.
	const encodedLen = 43
	if len(s) != encodedLen {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// RedactToken renders a token safe for a log line: enough to correlate two
// records, not enough to use. Nothing in this codebase should log a token at
// all, but a redacting helper means the mistake is survivable when it happens.
func RedactToken(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 6 {
		return strings.Repeat("*", len(s))
	}
	return s[:6] + "…" + strings.Repeat("*", 6)
}
