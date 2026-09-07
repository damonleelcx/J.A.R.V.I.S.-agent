package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// Fish renders speech through Fish Audio, in one named voice.
//
// # Why the voice is configuration and the vendor is not
//
// The voice is chosen by `reference_id`, a published model id from fish.audio.
// The timbre is not a parameter picked from a list — it is a specific published
// voice, and swapping vendors means finding a different voice rather than
// changing a setting. Naming it in configuration keeps that swap to one
// variable.
//
// The `model` header selects the synthesis BACKBONE and is separate from the
// voice. It travels as a header, not in the body: the body is accepted and
// ignored, which would silently bill a different backbone than the one asked
// for.
//
// # PCM, not MP3
//
// Fish will emit 16-bit PCM at a requested sample rate, and 24 kHz is one of
// them — which is exactly llm.SpeechSampleRate. So this asks for PCM directly
// and streams the response body through. That avoids an MP3 decoder entirely,
// and there is no usable pure-Go one to reach for: the same constraint that
// shaped the G.711 path in internal/media.
//
// A resample here would be a second place that knows FORGE's sample rate, and a
// disagreement between the two plays her voice at the wrong pitch.
type Fish struct {
	Endpoint string
	APIKey   string
	// VoiceID is Fish's `reference_id` — which voice, not which model.
	VoiceID string
	// Model is the synthesis backbone: s2.1-pro-free | s2.1-pro | s2-pro | s1.
	Model      string
	SampleRate int
	Client     *http.Client
}

const (
	DefaultEndpoint = "https://api.fish.audio/v1/tts"
	// DefaultModel is the free backbone. See TrainsOnRequests for what that
	// costs instead of money.
	DefaultModel = "s2.1-pro-free"

	// MaxChars caps one utterance. The caller caps too, but this side cannot
	// rely on that: an uncapped synthesis path is a vendor bill with a network
	// endpoint in front of it.
	MaxChars = 3000

	// streamChunk is how much PCM is handed onward at a time. 4 KiB is ~85 ms at
	// 24 kHz 16-bit mono — small enough that an interruption stops the voice
	// promptly, large enough not to make a syscall per syllable.
	streamChunk = 4096
)

// paidBackbones are the Fish backbones whose terms do NOT permit using requests
// to improve the vendor's models.
//
// An allowlist rather than a denylist, and the direction is the point: a
// backbone this table has not heard of is treated as one that trains.
// Over-warning costs some caution nobody needed; under-warning costs something
// that cannot be taken back.
//
// `s1` is deliberately absent — its terms were not checked, so it warns.
var paidBackbones = map[string]bool{
	"s2.1-pro": true,
	"s2-pro":   true,
}

// TrainsOnRequests reports whether this deployment's backbone may be trained on
// the text sent to it.
//
// ‼️ This is the sentence FORGE_DATA_BOUNDARY does not cover. That variable
// states what the contract with the MODEL endpoint says; speech is a second
// vendor and a second egress. A deployment can be truthfully `no_training`
// about its LLM while reading every answer aloud through a backbone that trains
// on it. config.Load surfaces this at startup for exactly that reason.
//
// An empty model resolves the way NewFish resolves it, so the answer cannot
// disagree with what is actually being called.
func TrainsOnRequests(model string) bool { return !paidBackbones[resolveModel(model)] }

func resolveModel(model string) string {
	if model == "" {
		return DefaultModel
	}
	return model
}

// NewFish builds the provider. sampleRate must be the rate the media plane
// expects; passing 0 is a programming error rather than a default, because the
// wrong rate is audible and easy to mistake for a bad voice.
func NewFish(endpoint, apiKey, voiceID, model string, sampleRate int) (*Fish, error) {
	const op = "tts.NewFish"
	if strings.TrimSpace(apiKey) == "" {
		return nil, errs.New(op, errs.CodeInvariantViolated).
			WithDetail("a speech provider was configured with no API key; " +
				"set FORGE_TTS_API_KEY or leave FORGE_TTS_PROVIDER unset")
	}
	if strings.TrimSpace(voiceID) == "" {
		return nil, errs.New(op, errs.CodeInvariantViolated).
			WithDetail("a speech provider was configured with no voice id. The voice is not " +
				"a default this code may pick: it is a specific published voice, and guessing " +
				"one would give FORGE a different voice than the rest of this estate. " +
				"Set FORGE_TTS_VOICE_ID")
	}
	if sampleRate <= 0 {
		return nil, errs.New(op, errs.CodeInvariantViolated).
			WithDetail("a speech provider was built with sample rate %d; it must match the "+
				"media plane's rate or FORGE speaks at the wrong pitch", sampleRate)
	}
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	return &Fish{
		Endpoint: endpoint, APIKey: apiKey, VoiceID: voiceID,
		Model: resolveModel(model), SampleRate: sampleRate,
		// Generous: synthesis runs faster than real time but a long utterance
		// legitimately takes many seconds. Cutting it short would read as "the
		// vendor is broken" when it is merely working.
		Client: &http.Client{Timeout: 90 * time.Second},
	}, nil
}

func (f *Fish) Name() string { return "fish" }

// fishRequest carries only the fields this product sets. The API has many more,
// and leaving them out takes the vendor's defaults — each field added here is a
// number somebody has to maintain a reason for.
type fishRequest struct {
	Text        string `json:"text"`
	ReferenceID string `json:"reference_id,omitempty"`
	Format      string `json:"format"`
	SampleRate  int    `json:"sample_rate,omitempty"`
	MP3Bitrate  int    `json:"mp3_bitrate,omitempty"`
	// Latency trades quality for time-to-first-byte. "balanced" because this is
	// somebody waiting to hear an answer in a conversation.
	Latency string `json:"latency"`
	// Normalize expands numbers, dates and units into spoken words. Load-bearing
	// for this product: PRD AUD-04 requires tolerances, coordinates and part ids
	// to be read back unambiguously, and read digit by digit they are useless.
	Normalize bool `json:"normalize"`
}

// SpeakMP3 synthesises text and returns MP3 bytes, for a browser.
//
// # Why the browser gets a different format than the media plane
//
// The media plane consumes raw PCM, so Speak streams PCM. A browser wants a
// CONTAINER it can hand to an <audio> element, and the obvious move — wrapping
// that same PCM in a WAV header — was the one this shipped with and it was
// wrong twice over.
//
// Wrong on size: 500KB of WAV for one utterance, against roughly 30KB of MP3 for
// the same seconds, on a path that fires on every reply.
//
// Wrong on support: a WAV is PCM, and PCM is the codec a browser is LEAST likely
// to have optimised. MP3 answers "probably" to canPlayType everywhere; WAV
// answers "maybe" and some engines refuse it outright.
//
// Fish emits both, so nothing is converted here either way — the endpoint asks
// for what its consumer can actually play, which is the thing the WAV wrapper
// was avoiding having to decide.
func (f *Fish) SpeakMP3(ctx context.Context, text string) ([]byte, string, error) {
	const op = "tts.Fish.SpeakMP3"

	body, err := f.request(text, fishRequest{Format: "mp3", MP3Bitrate: 128})
	if err != nil {
		return nil, "", err
	}
	resp, err := f.do(ctx, op, body)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	audio, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", errs.Wrap(op, errs.CodeExternalUnavailable, err).
			WithDetail("reading audio from %s", f.Name())
	}
	if len(audio) == 0 {
		// Silence is what a broken voice sounds like; it is reported, never
		// served as valid audio nobody can hear.
		return nil, "", errs.New(op, errs.CodeExternalUnavailable).
			WithDetail("%s answered 200 with no audio", f.Name())
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "audio/mpeg"
	}
	return audio, ct, nil
}

// request builds the JSON body, applying the caps and defaults both formats share.
func (f *Fish) request(text string, r fishRequest) ([]byte, error) {
	const op = "tts.Fish.request"
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errs.New(op, errs.CodeValidationFailed).WithDetail("nothing to speak")
	}
	if rs := []rune(text); len(rs) > MaxChars {
		text = string(rs[:MaxChars])
	}
	r.Text = text
	r.ReferenceID = f.VoiceID
	r.Latency = "balanced"
	r.Normalize = true
	b, err := json.Marshal(r)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeInvariantViolated, err).
			WithDetail("the speech request could not be encoded")
	}
	return b, nil
}

// do performs the call and returns a response whose status is already 200.
func (f *Fish) do(ctx context.Context, op string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeInvariantViolated, err).
			WithDetail("the speech request could not be built")
	}
	req.Header.Set("Authorization", "Bearer "+f.APIKey)
	req.Header.Set("Content-Type", "application/json")
	// The backbone travels as a HEADER. In the body it is accepted and ignored,
	// which would silently use — and bill — a different backbone.
	req.Header.Set("model", f.Model)

	resp, err := f.Client.Do(req)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeExternalUnavailable, err).
			WithDetail("%s could not be reached to synthesise speech", f.Name())
	}
	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		resp.Body.Close()
		return nil, errs.New(op, errs.CodeExternalUnavailable).
			WithDetail("%s answered %d: %s", f.Name(), resp.StatusCode,
				strings.TrimSpace(string(detail)))
	}
	return resp, nil
}

// Speak synthesises text and streams PCM to onPCM as it arrives.
func (f *Fish) Speak(ctx context.Context, text string, onPCM func([]byte) error) error {
	const op = "tts.Fish.Speak"

	text = strings.TrimSpace(text)
	if text == "" {
		return nil // Nothing to say is not a failure.
	}
	if r := []rune(text); len(r) > MaxChars {
		// Truncated rather than refused: half an utterance is still useful and a
		// hard failure would silence a long one entirely. The caller logs it —
		// a silent truncation is the failure this codebase keeps fencing against.
		text = string(r[:MaxChars])
	}

	body, err := json.Marshal(fishRequest{
		Text: text, ReferenceID: f.VoiceID, Format: "pcm",
		SampleRate: f.SampleRate, Latency: "balanced", Normalize: true,
	})
	if err != nil {
		return errs.Wrap(op, errs.CodeInvariantViolated, err).
			WithDetail("the speech request could not be encoded")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.Endpoint, bytes.NewReader(body))
	if err != nil {
		return errs.Wrap(op, errs.CodeInvariantViolated, err).
			WithDetail("the speech request could not be built")
	}
	req.Header.Set("Authorization", "Bearer "+f.APIKey)
	req.Header.Set("Content-Type", "application/json")
	// The backbone travels as a HEADER. In the body it is accepted and ignored,
	// which would silently use — and bill — a different backbone.
	req.Header.Set("model", f.Model)

	resp, err := f.Client.Do(req)
	if err != nil {
		return errs.Wrap(op, errs.CodeExternalUnavailable, err).
			WithDetail("%s could not be reached to synthesise speech", f.Name())
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// The vendor's own reason, passed through. This is the difference
		// between "the voice is broken" and "the key expired".
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return errs.New(op, errs.CodeExternalUnavailable).
			WithDetail("%s answered %d: %s", f.Name(), resp.StatusCode,
				strings.TrimSpace(string(detail)))
	}

	buf := make([]byte, streamChunk)
	var total int
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			total += n
			// Odd byte counts would split a 16-bit sample across two calls. The
			// consumer reassembles from a stream, so this is safe to pass on as
			// read; what must not happen is reordering or dropping.
			if err := onPCM(buf[:n]); err != nil {
				// The consumer stopped us — an interruption, not a failure.
				return nil
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return errs.Wrap(op, errs.CodeExternalUnavailable, readErr).
				WithDetail("the audio stream from %s ended early after %d bytes", f.Name(), total)
		}
	}

	if total == 0 {
		// 200 with no audio. Called out rather than treated as valid silence,
		// because silence is exactly what a broken voice sounds like and it
		// would otherwise never be reported.
		return errs.New(op, errs.CodeExternalUnavailable).
			WithDetail("%s answered 200 with no audio", f.Name())
	}
	return nil
}
