package httpapi

import (
	"regexp"
	"strings"
	"testing"
)

// The hands-free loop, and the two things that stop it.
//
// In hands-free the microphone is open while FORGE speaks, so it hears the
// speakers. Every word she said came back as a transcript, was submitted as
// though the person had said it, answered, spoken, and heard again — an endless
// conversation with herself. Observed in production on 2026-09-07: "hello I am"
// and "Forge" appeared as user messages, both fragments of her own replies.
//
// Barge-in and echo are indistinguishable at the transcript layer — both are
// "speech arriving while FORGE is talking" — so the code separates them by
// WHOSE WORDS THEY ARE. This asserts the two properties that keeps true:
//
//  1. the guard runs BEFORE the transcript is submitted, and
//  2. an autoplay refusal does not disable her voice for the page.
//
// The second is here because it produced the other half of the same report:
// /v1/speech answered 200 once, and the voice was never heard again, because a
// play() rejected for want of a user gesture was treated as a vendor failure.
func TestHandsFreeEchoGuardsArePresent(t *testing.T) {
	b, err := assetFS.ReadFile("assets/voice.js")
	if err != nil {
		t.Fatalf("reading voice.js: %v", err)
	}
	js := string(b)

	// 1. The echo check must come before onTranscript, or her words are
	//    submitted no matter what the check concludes.
	guard := strings.Index(js, "_isOwnEcho(heard)")
	submit := strings.Index(js, "self.onTranscript(final.trim())")
	if guard < 0 {
		t.Fatal("the echo guard is gone: nothing separates FORGE's own voice from an " +
			"interruption, so hands-free will submit her replies back to her")
	}
	if submit < 0 {
		t.Fatal("onTranscript call not found; this test no longer checks what it thinks")
	}
	if guard > submit {
		t.Error("the echo guard runs AFTER the transcript is submitted. Her own words " +
			"reach the conversation regardless of what the guard decides, which is the " +
			"infinite loop this exists to prevent.")
	}

	// 2. The tail must exist: recognition lags the audio, so the last words of
	//    every utterance arrive after speaking has stopped.
	if !strings.Contains(js, "_doneSpeaking") || !regexp.MustCompile(`_echoTail`).MatchString(js) {
		t.Error("the echo tail is gone. Recognition lags playback, so the final fragment " +
			"of each reply arrives after speaking ends and would be submitted — the same " +
			"loop, one message per turn instead of continuously.")
	}

	// 3. An autoplay refusal must not latch the remote voice off.
	if !strings.Contains(js, "NotAllowedError") {
		t.Error("an autoplay refusal is no longer distinguished from a vendor failure. " +
			"In hands-free the person speaks rather than clicks, so there may be no user " +
			"gesture and play() is rejected; treating that as a vendor failure disables " +
			"FORGE's own voice for the rest of the page after a synthesis that already " +
			"succeeded. Observed once in production: 200 from /v1/speech, never heard.")
	}
	if !strings.Contains(js, "SILENT_WAV") {
		t.Error("the first-gesture audio unlock is gone, so the autoplay refusal will " +
			"happen on every session that starts hands-free rather than being avoided")
	}
}
