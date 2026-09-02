// Package auth holds FORGE's security primitives: password hashing, token
// minting and redemption, and session credentials.
//
// Everything here is written on the assumption that the database will one day
// be read by someone who should not have it. Nothing stored is directly usable:
// passwords are memory-hard hashes, and every token is stored only as a digest.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// Argon2id parameters.
//
// These follow the OWASP Password Storage Cheat Sheet's argon2id baseline
// (19 MiB memory, 2 iterations, 1 lane). Memory cost is the parameter that
// actually resists GPU cracking; iteration count mostly buys time on the same
// hardware. The values are encoded into every hash string, so raising them
// later does not invalidate existing passwords — NeedsRehash reports which rows
// are stale and they are upgraded on the user's next successful sign-in.
const (
	argonMemoryKiB  uint32 = 19 * 1024
	argonIterations uint32 = 2
	argonLanes      uint8  = 1
	argonKeyLength  uint32 = 32
	argonSaltLength uint32 = 16
)

// AlgoArgon2id is the value stored in forge_users.password_algo.
const AlgoArgon2id = "argon2id"

// HashPassword returns a PHC-format argon2id hash.
//
// The output embeds the algorithm, version, and every cost parameter, e.g.
//
//	$argon2id$v=19$m=19456,t=2,p=1$<salt>$<hash>
//
// Self-describing hashes are what make a future parameter increase a
// non-migration: a verifier reads the cost from the stored string rather than
// from today's constants.
func HashPassword(plaintext string) (string, error) {
	const op = "auth.HashPassword"

	if plaintext == "" {
		return "", errs.New(op, errs.CodePasswordTooWeak).WithDetail("password is empty")
	}
	salt := make([]byte, argonSaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", errs.Wrap(op, errs.CodeInternal, err).WithDetail("cannot read entropy for a password salt")
	}
	key := argon2.IDKey([]byte(plaintext), salt, argonIterations, argonMemoryKiB, argonLanes, argonKeyLength)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemoryKiB, argonIterations, argonLanes,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword reports whether plaintext matches encoded.
//
// The comparison is constant-time. A byte-by-byte comparison would leak how
// much of the hash matched through timing, and password verification is the one
// endpoint an attacker can call as often as they like.
func VerifyPassword(encoded, plaintext string) (bool, error) {
	const op = "auth.VerifyPassword"

	params, salt, want, err := decodeHash(encoded)
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(plaintext), salt, params.iterations, params.memoryKiB, params.lanes, uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) == 1 {
		return true, nil
	}
	_ = op
	return false, nil
}

// NeedsRehash reports whether a stored hash was produced with weaker parameters
// than the current policy, so it can be upgraded during a successful sign-in —
// the one moment the plaintext is legitimately available.
func NeedsRehash(encoded string) bool {
	params, _, _, err := decodeHash(encoded)
	if err != nil {
		// An unparseable hash cannot be verified against either, so treating it
		// as "needs rehash" is the safe reading.
		return true
	}
	return params.memoryKiB < argonMemoryKiB ||
		params.iterations < argonIterations ||
		params.lanes < argonLanes
}

type argonParams struct {
	memoryKiB  uint32
	iterations uint32
	lanes      uint8
}

// decodeHash parses a PHC-format argon2id string.
func decodeHash(encoded string) (argonParams, []byte, []byte, error) {
	const op = "auth.decodeHash"
	var zero argonParams

	parts := strings.Split(encoded, "$")
	// ["", "argon2id", "v=19", "m=...,t=...,p=...", salt, hash]
	if len(parts) != 6 || parts[0] != "" {
		return zero, nil, nil, errs.New(op, errs.CodeStateCorrupt).
			WithDetail("stored password hash is not in PHC format")
	}
	if parts[1] != AlgoArgon2id {
		return zero, nil, nil, errs.New(op, errs.CodeStateCorrupt).
			WithDetail("stored password hash uses algorithm %q, which this build cannot verify", parts[1])
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return zero, nil, nil, errs.Wrap(op, errs.CodeStateCorrupt, err).
			WithDetail("cannot read the argon2 version field")
	}
	if version != argon2.Version {
		return zero, nil, nil, errs.New(op, errs.CodeStateCorrupt).
			WithDetail("stored hash uses argon2 version %d, this build links version %d", version, argon2.Version)
	}

	var params argonParams
	kv := strings.Split(parts[3], ",")
	if len(kv) != 3 {
		return zero, nil, nil, errs.New(op, errs.CodeStateCorrupt).
			WithDetail("cost parameter field has %d entries, want 3", len(kv))
	}
	for _, pair := range kv {
		k, v, ok := strings.Cut(pair, "=")
		if !ok {
			return zero, nil, nil, errs.New(op, errs.CodeStateCorrupt).
				WithDetail("malformed cost parameter %q", pair)
		}
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return zero, nil, nil, errs.Wrap(op, errs.CodeStateCorrupt, err).
				WithDetail("cost parameter %q is not a number", pair)
		}
		switch k {
		case "m":
			params.memoryKiB = uint32(n)
		case "t":
			params.iterations = uint32(n)
		case "p":
			if n > 255 {
				return zero, nil, nil, errs.New(op, errs.CodeStateCorrupt).
					WithDetail("lane count %d exceeds the maximum of 255", n)
			}
			params.lanes = uint8(n)
		default:
			return zero, nil, nil, errs.New(op, errs.CodeStateCorrupt).
				WithDetail("unknown cost parameter %q", k)
		}
	}
	if params.memoryKiB == 0 || params.iterations == 0 || params.lanes == 0 {
		return zero, nil, nil, errs.New(op, errs.CodeStateCorrupt).
			WithDetail("cost parameters must all be non-zero")
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return zero, nil, nil, errs.Wrap(op, errs.CodeStateCorrupt, err).WithDetail("salt is not valid base64")
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return zero, nil, nil, errs.Wrap(op, errs.CodeStateCorrupt, err).WithDetail("hash is not valid base64")
	}
	if len(salt) == 0 || len(key) == 0 {
		return zero, nil, nil, errs.New(op, errs.CodeStateCorrupt).WithDetail("salt or hash is empty")
	}
	return params, salt, key, nil
}

// PasswordPolicy is the minimum a new password must satisfy.
//
// Length only, deliberately. Composition rules (a digit, a symbol, mixed case)
// measurably push people toward Password1! and its relatives while adding
// almost no real entropy; NIST SP 800-63B dropped them for that reason. A long
// passphrase beats a short mangled word, so length is what is enforced.
type PasswordPolicy struct {
	MinLength int
}

// Validate checks a candidate password against the policy.
func (p PasswordPolicy) Validate(plaintext string) error {
	const op = "auth.PasswordPolicy.Validate"

	// Count runes, not bytes: a passphrase in Chinese or emoji would otherwise
	// clear a byte-length check with far fewer characters than intended.
	if n := len([]rune(plaintext)); n < p.MinLength {
		return errs.New(op, errs.CodePasswordTooWeak).
			WithDetail("password is %d characters, minimum is %d", n, p.MinLength)
	}
	// An all-whitespace password passes a length check and is not a password.
	if strings.TrimSpace(plaintext) == "" {
		return errs.New(op, errs.CodePasswordTooWeak).WithDetail("password is only whitespace")
	}
	return nil
}

// hashPasswordWith is HashPassword with explicit cost parameters. It exists so
// that tests can produce hashes at settings the current policy no longer emits,
// which is the only way to prove that raising cost does not lock existing users
// out. Production code should call HashPassword.
func hashPasswordWith(plaintext string, memKiB, iters uint32, lanes uint8) (string, error) {
	const op = "auth.hashPasswordWith"

	salt := make([]byte, argonSaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", errs.Wrap(op, errs.CodeInternal, err)
	}
	key := argon2.IDKey([]byte(plaintext), salt, iters, memKiB, lanes, argonKeyLength)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, memKiB, iters, lanes,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// DummyVerify performs a password hash against a fixed, throwaway hash.
//
// It exists to be called when sign-in fails because no account matched. Without
// it, "unknown address" returns in microseconds while "wrong password" spends
// ~50ms in argon2 — a difference an attacker can measure remotely, turning the
// sign-in endpoint into an account enumeration oracle no matter how carefully
// the error messages are equalised.
//
// The result is deliberately discarded; only the elapsed work matters.
func DummyVerify(plaintext string) {
	_, _ = VerifyPassword(dummyHash, plaintext)
}

// dummyHash is a real argon2id hash at current parameters, of a value nobody
// will ever submit. Computed once at init rather than hardcoded so it always
// costs exactly what a live verification costs — a hardcoded hash would drift
// from the current parameters the moment they were raised, silently
// reintroducing the timing gap this function exists to close.
var dummyHash = func() string {
	h, err := HashPassword("forge-timing-equalisation-value-not-a-credential")
	if err != nil {
		// Cannot happen: the input is non-empty and entropy is available. If it
		// somehow does, an empty hash still forces decodeHash to run and fail,
		// which is cheap — so the timing gap reopens. Panicking at init is the
		// honest response to a broken security assumption.
		panic("auth: cannot build the timing-equalisation hash: " + err.Error())
	}
	return h
}()
