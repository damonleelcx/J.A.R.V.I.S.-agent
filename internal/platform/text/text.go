// Package text holds string handling that has to be correct in every caller.
//
// It exists because of one bug, found twice. Shortening a string with `s[:n]`
// counts BYTES, and a byte offset inside a UTF-8 sequence splits a character in
// half: what comes out is no longer text. Go does not complain — `json.Marshal`
// quietly substitutes a replacement character, Postgres stores it, and a
// millimetre figure or a Chinese word ends its life as "�" in a record kept
// for provenance.
//
// The rule is small enough that every caller wrote its own version, and small
// enough that every version got it wrong the same way.
package text

import (
	"fmt"
	"unicode/utf8"
)

// Clip shortens s to at most limit characters and SAYS that it did.
//
// # Characters, not bytes
//
// The limits callers choose are written as character counts — "2000 is longer
// than any workbench utterance observed" — and counting bytes silently makes the
// limit three times tighter for text that is not ASCII, while cutting the last
// character in half on the way out.
//
// # Why the notice is not optional
//
// Silent truncation is the worse half of this bug. A ledger field that quietly
// loses its ending misrepresents what a change was made from, and a model handed
// a shortened document reasons from part of an argument with nothing to indicate
// the rest existed. The count in the notice is in characters too, so it agrees
// with the limit that caused it.
//
// A negative limit is treated as zero rather than panicking: this is called on
// paths that record what somebody did, and a bad constant should cost a truncated
// field rather than the thing being recorded.
func Clip(s string, limit int) string {
	if limit < 0 {
		limit = 0
	}
	total := utf8.RuneCountInString(s)
	if total <= limit {
		return s
	}
	// Walk to the byte offset that begins the (limit+1)th rune, rather than
	// materialising the whole string as runes. Callers pass command output and
	// uploaded documents through here, and the common case — nothing to cut —
	// already returned above.
	cut := len(s)
	n := 0
	for i := range s {
		if n == limit {
			cut = i
			break
		}
		n++
	}
	return s[:cut] + fmt.Sprintf("… [truncated; %d characters in the original]", total)
}

// TrimPartialRune removes an incomplete UTF-8 sequence from the end of s.
//
// # What this is for, and what it is NOT for
//
// A byte budget applied to a stream — bounding how much of a command's output is
// held in memory — has to cut at a byte offset, and that offset can fall inside
// a character. The tail bytes are then dropped and never completed, so the
// string ends in a sequence that is not text. This removes that stub.
//
// It is not a repair function. Invalid bytes in the MIDDLE of s are left exactly
// where they are: they came from the data, and quietly rewriting somebody's
// output is a different and worse problem than the one this solves. Only a
// broken ENDING — the kind a cut creates — is removed.
//
// A tail of four or more continuation bytes cannot come from a cut sequence
// (UTF-8 is at most four bytes) so it is left alone as data.
func TrimPartialRune(s string) string {
	for i := len(s) - 1; i >= 0 && i >= len(s)-3; i-- {
		if !utf8.RuneStart(s[i]) {
			continue // a continuation byte; keep walking back to the start byte
		}
		// The last sequence begins here. If it decodes, s ends cleanly — a
		// complete sequence consumes exactly the bytes that remain, because
		// everything after i is a continuation byte.
		if r, size := utf8.DecodeRuneInString(s[i:]); r == utf8.RuneError && size == 1 {
			return s[:i]
		}
		return s
	}
	return s
}
