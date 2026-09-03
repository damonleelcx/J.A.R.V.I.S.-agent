package llm_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// The transcription fence, against the REAL provider and real speech.
//
// # Why this cannot be a fake
//
// The defect it guards is a property of the model, not of this code. A stub that
// returned "two point five" would pass forever while production quietly wrote
// "two five" into engineering transcripts — which is precisely the failure being
// fenced, dressed up as a passing test.
//
// So it calls the provider. It costs a fraction of a cent and takes about a
// second, and it is skipped unless FORGE_LLM_API_KEY is set — `make test-asr`.
//
// # What the fixture is
//
// `testdata/engineering-utterance.ogg` is Ogg Opus, the container the media
// plane produces from forwarded RTP. Spoken content, known exactly:
//
//	"Set the bracket wall thickness to two point five millimetres,
//	 tolerance plus or minus zero point one."
//
// They are committed rather than generated so this runs somewhere other than a
// Mac.
//
// # Why there are TWO fixtures, and why neither alone is enough
//
// This was established by mutation, not by guessing. The two ways the system
// context can be broken are caught by different utterances:
//
//	                          empty context   vocabulary only   full context
//	long  "two point five"      passes         FAILS             passes
//	short "one point five"      FAILS          passes            passes
//
// A fence built on the long fixture alone stays green when somebody deletes
// `asrContext` outright — the likeliest mutation of all. One built on the short
// fixture alone stays green when the decimal rule is dropped and the vocabulary
// kept. Only the pair catches both, so both are asserted and neither may be
// removed without re-running the drill.
type asrFixture struct {
	file   string
	spoken string
	// must are phrases that have to survive verbatim. Each is a value that
	// changes meaning when its decimal point is lost.
	must []string
}

var asrFixtures = []asrFixture{
	{
		file:   "testdata/engineering-utterance.ogg",
		spoken: "Set the bracket wall thickness to two point five millimetres, tolerance plus or minus zero point one.",
		must:   []string{"two point five", "zero point one"},
	},
	{
		file:   "testdata/short-utterance.ogg",
		spoken: "Increase the fillet to one point five millimetres.",
		must:   []string{"one point five"},
	},
}

func transcriberForTest(t *testing.T) *llm.OpenAICompatible {
	t.Helper()
	if os.Getenv("FORGE_LLM_API_KEY") == "" {
		t.Skip("FORGE_LLM_API_KEY is unset; run with `make test-asr`")
	}
	cfg, _, err := config.Load(config.SectionNone)
	if err != nil {
		t.Fatal(err)
	}
	cfg.LLM.RequestTimeout = 60 * time.Second
	return llm.NewOpenAICompatible(cfg.LLM, logx.Discard(), clock.System{})
}

// Spoken decimals must survive transcription.
//
// A 2.5 mm wall arriving in the transcript as "two five" is a wrong number that
// reads like a right one. RSN-06 forbids fabricated measurements; this is the
// path by which one would arrive without anybody deciding to fabricate it.
func TestTranscriptionPreservesSpokenDecimals(t *testing.T) {
	c := transcriberForTest(t)

	for _, fx := range asrFixtures {
		t.Run(fx.file, func(t *testing.T) {
			audio, err := os.ReadFile(fx.file)
			if err != nil {
				t.Fatal(err)
			}
			got, err := c.Transcribe(context.Background(), audio, "audio/ogg")
			if err != nil {
				t.Fatalf("transcription failed: %v", err)
			}
			t.Logf("spoken:     %q", fx.spoken)
			t.Logf("transcript: %q", got.Text)

			if got.Text == "" {
				t.Fatal("the transcript is empty; the fixture contains clear speech")
			}
			lower := strings.ToLower(got.Text)
			for _, want := range fx.must {
				if !strings.Contains(lower, want) {
					t.Errorf("the spoken value %q did not survive.\n  spoken:   %q\n  returned: %q\n"+
						"This is the defect in docs/spikes/2026-09-03-webrtc-sfu/: with the wrong "+
						"system context this model writes 'two five' for 'two point five'. Check "+
						"asrContext in transcribe.go before changing this test.",
						want, fx.spoken, got.Text)
				}
			}
			if got.Model == "" {
				t.Error("the transcript does not say which model produced it")
			}
		})
	}
}

// Silence is ordinary, not an error.
//
// A room is mostly quiet. If an empty segment produced an error, the log would
// fill with them and everybody would learn to ignore it — which is how a real
// transcription failure goes unnoticed.
func TestTranscribingNothingIsNotAnError(t *testing.T) {
	c := transcriberForTest(t)

	got, err := c.Transcribe(context.Background(), nil, "audio/ogg")
	if err != nil {
		t.Fatalf("an empty segment was treated as a failure: %v", err)
	}
	if got.Text != "" {
		t.Errorf("an empty segment produced text: %q", got.Text)
	}
}

// A segment far larger than any utterance is refused before it is sent.
//
// Reaching this size means segmentation is broken. Failing here names that,
// rather than sending a multi-megabyte request that times out and looks like a
// provider problem.
func TestAnAbsurdlyLargeSegmentIsRefusedLocally(t *testing.T) {
	if os.Getenv("FORGE_LLM_API_KEY") == "" {
		t.Skip("FORGE_LLM_API_KEY is unset; run with `make test-asr`")
	}
	cfg, _, err := config.Load(config.SectionNone)
	if err != nil {
		t.Fatal(err)
	}
	c := llm.NewOpenAICompatible(cfg.LLM, logx.Discard(), clock.System{})

	// No network call should happen, so the timeout being irrelevant is the point.
	if _, err := c.Transcribe(context.Background(), make([]byte, 9<<20), "audio/ogg"); err == nil {
		t.Fatal("a 9 MiB segment was accepted; segmentation problems would present as provider timeouts")
	}
}
