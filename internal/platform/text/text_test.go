package text

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The bug this package exists for: `s[:n]` counts bytes, and a byte offset
// inside a UTF-8 sequence splits a character in half. Nothing complains —
// json.Marshal substitutes a replacement character and the record keeps it.

func TestClipLeavesShortTextAlone(t *testing.T) {
	if got := Clip("3mm washer", 100); got != "3mm washer" {
		t.Errorf("text under the limit was changed: %q", got)
	}
	// Exactly at the limit is under it, not over it.
	if got := Clip("abcde", 5); got != "abcde" {
		t.Errorf("text exactly at the limit was truncated: %q", got)
	}
}

func TestClipCutsOnACharacterBoundary(t *testing.T) {
	// Every rune here is three bytes, so any byte-based cut lands inside one.
	s := strings.Repeat("毫米", 50) // 100 characters, 300 bytes
	got := Clip(s, 10)

	if !utf8.ValidString(got) {
		t.Fatal("the cut landed inside a character; what comes out is not text.\n" +
			"json.Marshal will substitute a replacement character and the record will keep it.")
	}
	if head := strings.SplitN(got, "…", 2)[0]; utf8.RuneCountInString(head) != 10 {
		t.Errorf("asked for 10 characters and kept %d — the limit is being read as bytes",
			utf8.RuneCountInString(head))
	}
}

func TestClipSaysItCutAndCountsInCharacters(t *testing.T) {
	s := strings.Repeat("毫", 100) // 100 characters, 300 bytes
	got := Clip(s, 10)

	if !strings.Contains(got, "truncated") {
		t.Fatal("the text was shortened silently. A ledger field that quietly loses its ending " +
			"misrepresents what a change was made from.")
	}
	if !strings.Contains(got, "100 characters") {
		t.Errorf("the notice does not count in the same unit as the limit — a reader is told how "+
			"many BYTES were in a string measured in characters: %q", got)
	}
}

// Defined rather than accidental. This runs on paths that record what somebody
// did, and a bad constant should cost a truncated field rather than a panic in
// the middle of writing the record.
func TestClipSurvivesAnImpossibleLimit(t *testing.T) {
	got := Clip("something", -3)
	if !strings.Contains(got, "truncated") {
		t.Errorf("a negative limit produced %q", got)
	}
	if strings.HasPrefix(got, "s") {
		t.Errorf("a negative limit kept characters: %q", got)
	}
}

func TestClipHandlesEmptyText(t *testing.T) {
	if got := Clip("", 10); got != "" {
		t.Errorf("empty text came back as %q", got)
	}
}
