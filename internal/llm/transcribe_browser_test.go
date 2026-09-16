package llm_test

import (
	"context"
	"os"
	"strings"
	"testing"
)

// What browsers record must be something the provider transcribes — against the
// REAL provider, so run with `make test-asr`.
//
// # Why this exists
//
// The workbench microphone uploads whatever MediaRecorder produced, unconverted
// (see internal/httpapi/transcribe.go for why nothing is transcoded). The media
// plane only ever sent Ogg Opus, so nothing had asked the provider about the two
// containers a browser actually records:
//
//	Chrome, Edge, Firefox   audio/webm   Opus in WebM
//	Safari                  audio/mp4    AAC in MP4
//
// The fixtures are the committed short-utterance.ogg re-muxed/encoded with
// ffmpeg ("Increase the fillet to one point five millimetres."):
//
//	ffmpeg -i short-utterance.ogg -c:a libopus -b:a 32k -f webm short-utterance.webm
//	ffmpeg -i short-utterance.ogg -c:a aac -b:a 64k -f mp4 short-utterance.mp4
//
// ‼️ Not yet seen green. On 2026-09-15 the production endpoint served no
// speech-to-text model — FORGE_LLM_TRANSCRIBER_MODEL answered 404 for every
// container, Ogg included — so this has only ever run red, for that reason. When
// a transcription model is configured, this is the first thing to run.
//
// See docs/bugfix/2026-09-15-the-microphone-sent-nothing.md
func TestTranscriptionOfWhatBrowsersRecord(t *testing.T) {
	c := transcriberForTest(t)

	for _, tc := range []struct{ file, mime, browser string }{
		{"testdata/short-utterance.webm", "audio/webm", "Chrome, Edge and Firefox"},
		{"testdata/short-utterance.mp4", "audio/mp4", "Safari"},
	} {
		t.Run(tc.mime, func(t *testing.T) {
			audio, err := os.ReadFile(tc.file)
			if err != nil {
				t.Fatal(err)
			}
			got, err := c.Transcribe(context.Background(), audio, tc.mime)
			if err != nil {
				t.Fatalf("the provider refused what %s records (%s): %v", tc.browser, tc.mime, err)
			}
			if got.Unanswered {
				t.Fatalf("%s answered without a transcript for %s — it is not a transcription model",
					got.Model, tc.mime)
			}
			if !strings.Contains(strings.ToLower(got.Text), "fillet") {
				t.Errorf("%s from %s came back as %q, want the utterance about the fillet",
					tc.mime, tc.browser, got.Text)
			}
		})
	}
}
