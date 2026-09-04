package llm

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// A provider's response is quoted into an error an operator reads.
//
// It used to cut at n BYTES. A provider's error message is exactly the kind of
// text that is not ASCII, and these strings end up in errs.Error details, which
// are marshalled into an HTTP response — so a broken sequence becomes a
// replacement character in the answer somebody is reading to work out what went
// wrong.
func TestAQuotedResponseIsCutOnACharacterBoundary(t *testing.T) {
	body := strings.Repeat("请求参数错误，请检查模型名称。", 80)

	got := truncate(body, 120)

	if !utf8.ValidString(got) {
		t.Fatal("the quoted response is not valid UTF-8: the cut landed inside a character, and " +
			"the operator reading the error sees a symbol the provider never sent")
	}
	if head := strings.SplitN(got, "…", 2)[0]; utf8.RuneCountInString(head) != 120 {
		t.Errorf("kept %d characters against a limit of 120 — the limit is being read as bytes",
			utf8.RuneCountInString(head))
	}
	if !strings.Contains(got, "truncated") {
		t.Error("the response was shortened silently")
	}
}

// A body that is not text at all must not become an error of its own.
func TestQuotingABinaryBodyDoesNotPanic(t *testing.T) {
	binary := string([]byte{0xff, 0xfe, 0x00, 0x01, 0xff, 0xfe, 0x00, 0x01})
	if got := truncate(binary, 4); got == "" {
		t.Error("a binary body was quoted as nothing at all")
	}
}

func TestAShortResponseIsQuotedWhole(t *testing.T) {
	if got := truncate("model not found", 300); got != "model not found" {
		t.Errorf("got %q", got)
	}
}
