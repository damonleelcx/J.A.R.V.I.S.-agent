package agent

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// What one of FORGE's replies looks like to the turn after it.
//
// # Why both halves travel, and why they stay apart
//
// Only the speech used to reach the next turn, so FORGE could explain a choice
// at length and then have no idea why it had chosen it one question later. The
// detail travels now — and it travels LABELLED, because what keeps speech short
// is largely the model's sense of how it spoke last time, and previous turns
// arriving as long paragraphs teach it that long speech is normal.

func TestHistoryContentCarriesTheSpeechAlone(t *testing.T) {
	got := HistoryContent("Three millimetres is standard.", "")
	if got != "Three millimetres is standard." {
		t.Errorf("a reply with no detail should reach the next turn as itself, got %q", got)
	}
}

func TestHistoryContentCarriesTheDetailAndSaysItWasNotSpoken(t *testing.T) {
	got := HistoryContent("Three millimetres is standard.",
		"ISO 7089 lists 3mm for an M24 washer; thinner ones exist for shim stacks.")

	if !strings.Contains(got, "Three millimetres is standard.") {
		t.Errorf("the spoken half is missing: %q", got)
	}
	if !strings.Contains(got, "ISO 7089") {
		t.Fatalf("the detail did not travel, so FORGE still cannot say why it answered as it "+
			"did one turn later: %q", got)
	}
	if !strings.Contains(got, "not spoken aloud") {
		t.Errorf("the two halves are run together with nothing to tell them apart.\n"+
			"A model whose own previous turns arrive as long paragraphs learns that long SPOKEN "+
			"replies are normal, and speech being short is a product rule (PRD §5.3) that the "+
			"evaluation suite floors at 70 words.\nGot: %q", got)
	}
}

// A reply may be all detail and no speech. An empty assistant message is
// rejected outright by some providers, and an unlabelled one would read as
// something FORGE said out loud.
func TestHistoryContentHandlesAReplyThatWasNeverSpoken(t *testing.T) {
	got := HistoryContent("", "Here is the parts table.")
	if strings.TrimSpace(got) == "" {
		t.Fatal("a detail-only reply reaches the next turn as an empty message")
	}
	if !strings.Contains(got, "not spoken aloud") {
		t.Errorf("a reply that was never spoken is presented as one that was: %q", got)
	}
}

// A long detail is shortened, SAYS so, and is still text.
//
// The window bounds how many turns come back; nothing bounded how long one of
// them could be, and the record is permanent — so one reply carrying a long
// table would ride along in every request for the rest of the conversation.
//
// The rune check is not incidental. Cutting a UTF-8 sequence in half produces
// bytes that are not text, and the model would receive a replacement character
// where a dimension used to be.
func TestALongDetailIsShortenedAndSaysSo(t *testing.T) {
	long := strings.Repeat("ø 24 mm — ", 900) // multi-byte, and far past the limit
	got := HistoryContent("Short.", long)

	if len([]rune(got)) > historyDetailLimit+400 {
		t.Errorf("a %d-rune detail travelled at %d runes; nothing is bounding it",
			len([]rune(long)), len([]rune(got)))
	}
	if !strings.Contains(got, "truncated") {
		t.Error("the detail was shortened silently. FORGE would reason from part of its own " +
			"argument with nothing to indicate the rest existed.")
	}
	if !utf8.ValidString(got) {
		t.Error("truncation cut a character in half; what reaches the model is not text")
	}
}

// The window and the caller agree by construction.
func TestTheHistoryWindowIsAPositiveNumberBothSidesCanRead(t *testing.T) {
	if HistoryWindow < 2 {
		t.Fatalf("a window of %d turns is not a conversation", HistoryWindow)
	}
}
