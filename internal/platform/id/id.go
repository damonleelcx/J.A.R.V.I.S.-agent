// Package id generates FORGE's identifiers.
//
// Why not bare UUIDv4: identifiers in this system are read by humans in logs
// and audit trails, and they are used as primary keys in a database whose
// hottest access pattern is "most recent rows for this project". A prefixed,
// time-sortable id gives us both — you can tell a task id from a session id at
// a glance, and index locality stays intact as the table grows.
//
// Format: <prefix>_<26-char Crockford base32 of 48-bit ms timestamp + 80 bits
// of crypto/rand>. This is the ULID layout, spelled out here rather than taken
// as a dependency because it is thirty lines and we need the prefix anyway.
package id

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"strings"
	"time"
)

// Prefix marks what kind of thing an identifier names. Values are part of the
// API surface: they appear in URLs and audit records.
type Prefix string

const (
	PrefixUser       Prefix = "usr"
	PrefixSession    Prefix = "ses"
	PrefixToken      Prefix = "tok"
	PrefixProject    Prefix = "prj"
	PrefixGoal       Prefix = "gol"
	PrefixPlan       Prefix = "pln"
	PrefixTask       Prefix = "tsk"
	PrefixRun        Prefix = "run"
	PrefixCheckpoint Prefix = "ckp"
	PrefixJob        Prefix = "job"
	PrefixApproval   Prefix = "apr"
	PrefixArtifact   Prefix = "art"
	PrefixEvidence   Prefix = "evd"
	PrefixEvent      Prefix = "evt"
	PrefixMemory     Prefix = "mem"
	PrefixDecision   Prefix = "dec"
	PrefixNode       Prefix = "nod"
	PrefixEdge       Prefix = "edg"
	PrefixVersion    Prefix = "ver"
	PrefixSecret     Prefix = "sec"
	PrefixIncident   Prefix = "inc"
	PrefixAction     Prefix = "act"
	PrefixFactor     Prefix = "mfa"
	PrefixDevice     Prefix = "dev"
	PrefixRoom       Prefix = "rom"
	PrefixTurn       Prefix = "trn"
	PrefixToolCall   Prefix = "tcl"
	PrefixRequest    Prefix = "req"
	PrefixTrace      Prefix = "trc"
	PrefixSpan       Prefix = "spn"
)

// crockford is the base32 alphabet: no I, L, O, or U, so an id read aloud or
// retyped from a screenshot cannot be corrupted by the usual confusions.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// New returns a fresh identifier with the given prefix.
func New(p Prefix) string { return NewAt(p, time.Now()) }

// NewAt returns an identifier whose sortable component encodes t. Exposed so
// tests can generate a deterministic ordering without waiting on the clock.
func NewAt(p Prefix, t time.Time) string {
	var raw [16]byte
	ms := uint64(t.UTC().UnixMilli())
	// 48-bit big-endian millisecond timestamp.
	raw[0] = byte(ms >> 40)
	raw[1] = byte(ms >> 32)
	raw[2] = byte(ms >> 24)
	raw[3] = byte(ms >> 16)
	raw[4] = byte(ms >> 8)
	raw[5] = byte(ms)
	// 80 bits of entropy. rand.Read on crypto/rand never returns an error in
	// Go's implementation; it panics internally on an unusable entropy source,
	// which is the correct behaviour for a system that mints session tokens.
	if _, err := rand.Read(raw[6:]); err != nil {
		panic(fmt.Sprintf("id: crypto/rand unavailable: %v", err))
	}
	return string(p) + "_" + encode(raw)
}

// encode renders 128 bits as 26 Crockford base32 characters.
func encode(raw [16]byte) string {
	hi := binary.BigEndian.Uint64(raw[0:8])
	lo := binary.BigEndian.Uint64(raw[8:16])
	var out [26]byte
	// 26 * 5 = 130 bits; the leading two bits are always zero.
	for i := 25; i >= 0; i-- {
		out[i] = crockford[lo&0x1f]
		lo = (lo >> 5) | (hi << 59)
		hi >>= 5
	}
	return string(out[:])
}

// HasPrefix reports whether s is an identifier of the given kind. Handlers use
// it to reject a well-formed id of the wrong type before it reaches the
// database, so "task not found" cannot really mean "you passed a user id".
func HasPrefix(s string, p Prefix) bool { return strings.HasPrefix(s, string(p)+"_") }

// Valid reports whether s is structurally a FORGE identifier of kind p.
func Valid(s string, p Prefix) bool {
	if !HasPrefix(s, p) {
		return false
	}
	body := s[len(p)+1:]
	if len(body) != 26 {
		return false
	}
	for i := 0; i < len(body); i++ {
		if !strings.ContainsRune(crockford, rune(body[i])) {
			return false
		}
	}
	return true
}
