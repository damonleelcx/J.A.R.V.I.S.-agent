package tools

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Partial command output is quoted back to the MODEL when a command times out.
//
// It used to cut at n BYTES, landing inside a character for any output that is
// not ASCII — and a replacement character where a value used to be is a value
// the model will read as something else.
func TestQuotedOutputIsCutOnACharacterBoundary(t *testing.T) {
	long := strings.Repeat("测量值为二点五毫米。", 200)

	got := truncateStr(long, 100)

	if !utf8.ValidString(got) {
		t.Fatal("the quoted output is not valid UTF-8: the cut landed inside a character, and " +
			"the model reads a replacement character where a measurement used to be")
	}
	if head := strings.SplitN(got, "…", 2)[0]; utf8.RuneCountInString(head) != 100 {
		t.Errorf("kept %d characters against a limit of 100 — the limit is being read as bytes",
			utf8.RuneCountInString(head))
	}
	if !strings.Contains(got, "truncated") {
		t.Error("output was shortened silently; the model would treat a partial result as whole")
	}
}

func TestShortOutputIsQuotedWhole(t *testing.T) {
	if got := truncateStr("ok", 100); got != "ok" {
		t.Errorf("short output was altered: %q", got)
	}
}

// A byte budget must not leave half a character behind.
//
// # Why this is separate from the truncation notice
//
// TestLimitedWriterAlwaysReportsTruncation holds the invariant that a clipped
// result SAYS it was clipped. This holds the other half: that what it kept is
// still text. The budget is in bytes and that is correct — it bounds memory
// while output streams in — but a byte offset can land inside a character, and
// the tail of that character is then dropped and never completed. The model
// reads a replacement character where a value used to be.
func TestALimitedWriterDoesNotKeepHalfACharacter(t *testing.T) {
	// A limit that deliberately falls inside a three-byte character: 10 runes
	// is 30 bytes, so 29 cuts the last one with one byte to go.
	for _, limit := range []int{29, 28, 30} {
		var buf strings.Builder
		w := &limitedWriter{w: &buf, limit: limit}
		payload := []byte(strings.Repeat("毫", 50))
		if _, err := w.Write(payload); err != nil {
			t.Fatal(err)
		}

		got := w.text()
		if !utf8.ValidString(got) {
			t.Errorf("limit %d left half a character in the output. The model reads a "+
				"replacement character where a value used to be.", limit)
		}
		if !strings.Contains(got, "truncated") {
			t.Errorf("limit %d clipped the output and said nothing", limit)
		}
	}
}

// Output the writer did NOT clip is relayed exactly, even when the command's own
// bytes are malformed. Quietly editing somebody's output is a different and
// worse problem than the one the trim solves.
func TestALimitedWriterRelaysUnclippedOutputUntouched(t *testing.T) {
	var buf strings.Builder
	w := &limitedWriter{w: &buf, limit: 1024}
	broken := "value: " + string([]byte{0xe6, 0xaf}) // a deliberate partial character
	if _, err := w.Write([]byte(broken)); err != nil {
		t.Fatal(err)
	}
	if got := w.text(); got != broken {
		t.Errorf("output that was never clipped was altered: %q became %q", broken, got)
	}
}
