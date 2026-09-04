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
