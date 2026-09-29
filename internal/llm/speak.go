package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Speech, for FORGE's voice in a shared session (PRD AUD-01, AUD-05).
//
// # Where this runs, and why it must stream
//
// Not at /audio/speech — that route does not exist on this provider (404), the
// same shape as transcription. Speech comes from /chat/completions with
// `modalities: ["text","audio"]`, and **only when streaming**: the non-streaming
// call bills for audio tokens and returns no audio at all. Measured:
// `docs/spikes/2026-09-03-forge-voice-in-a-room/`.
//
// Streaming is also what makes the feature usable rather than merely possible.
// First audio arrives in about 0.6 s and the rest faster than real time, so
// FORGE begins speaking while the sentence is still being synthesised instead of
// after it.
//
// # What comes back
//
// Raw 16-bit PCM, 24 kHz, mono. No container, no Opus. Everything downstream —
// the resampling, the G.711 encoding — exists because of that one fact, and
// because there is no usable pure-Go Opus encoder to avoid it with.

// SpeechSampleRate is the rate the provider synthesises at.
//
// Exported because the media plane resamples from it and a disagreement between
// the two would produce speech at the wrong pitch — audible, but easy to mistake
// for a bad voice rather than a bug.
const SpeechSampleRate = 24000

// speechChunkLimit bounds one utterance.
//
// Roughly two minutes of 24 kHz PCM. A single spoken turn longer than that is
// not a turn, it is a runaway generation, and stopping it here is cheaper than
// discovering it as memory growth.
const speechChunkLimit = 2 * 60 * SpeechSampleRate * 2

// Speak synthesises text and delivers PCM as it arrives.
//
// onPCM is called with 16-bit little-endian mono samples at SpeechSampleRate, in
// whatever sizes the provider sends. Returning an error from it stops the
// stream — which is how barge-in cancels an utterance already in flight, without
// waiting for the provider to finish talking.
//
// The context is honoured throughout for the same reason: an interruption must
// take effect now, not at the end of the sentence.
func (c *OpenAICompatible) Speak(ctx context.Context, text string, onPCM func([]byte) error) error {
	const op = "llm.Speak"

	if strings.TrimSpace(text) == "" {
		return nil
	}
	model := c.models[RoleSpeaker]
	if model == "" {
		return errs.New(op, errs.CodeConfigInvalid).
			WithDetail("no speech model is configured; set FORGE_LLM_SPEAKER_MODEL")
	}

	body, err := json.Marshal(map[string]any{
		"model":      model,
		"modalities": []string{"text", "audio"},
		"audio":      map[string]any{"voice": c.voice, "format": "wav"},
		"stream":     true,
		// The instruction is explicit because these are conversational models:
		// asked to "say" something they will otherwise answer it, and FORGE would
		// speak a reply to its own sentence rather than the sentence.
		"messages": []any{map[string]any{
			"role":    "user",
			"content": "Read this aloud exactly as written, and say nothing else: " + text,
		}},
	})
	if err != nil {
		return errs.Wrap(op, errs.CodeSerializationFail, err)
	}

	// The general request timeout, for a caller that set no deadline. It used to
	// arrive as http.Client.Timeout; see requestTimeout in openai_compatible.go.
	ctx, cancelBound := c.boundAttempt(ctx)
	defer cancelBound()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return errs.Wrap(op, errs.CodeInternal, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.client.Do(req)
	if err != nil {
		// ‼️ The transport error was interpolated with %v and nothing else, so
		// a refused connection and an expired certificate were the same row
		// afterwards. Redacted through the one door now: a proxy's error string
		// can quote the Authorization header it rejected.
		up := newUpstream(0, nil, err, c.secrets()...)
		return up.annotate(errs.Wrap(op, errs.CodeExternalUnavailable, up).
			WithDetail("the speech request did not complete: %s", up.Transport))
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		// Bounded read: this body is a failure page, and the streaming buffer
		// below is sized for audio rather than for an HTML apology.
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		// Same reasoning as the transcriber's: a 404 is a retired model, and the
		// endpoint can be asked what replaced it.
		up := newUpstream(resp.StatusCode, raw, nil, c.secrets()...)
		return up.annotate(errs.Wrap(op, errs.CodeExternalUnavailable, up).
			WithDetail("the speech provider refused: %s%s",
				up.Sentence(), c.whatIsServed(ctx, resp.StatusCode, RoleSpeaker)))
	}

	var total int
	scanner := bufio.NewScanner(resp.Body)
	// Audio frames are large; the default 64 KiB line limit truncates them and
	// the failure looks like corrupt speech rather than a buffer size.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if !bytes.HasPrefix(line, []byte("data: ")) {
			continue
		}
		payload := bytes.TrimSpace(line[6:])
		if bytes.Equal(payload, []byte("[DONE]")) {
			break
		}
		var frame struct {
			Choices []struct {
				Delta struct {
					Audio *struct {
						Data string `json:"data"`
					} `json:"audio"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(payload, &frame); err != nil {
			continue // a keep-alive or a shape this build does not model
		}
		for _, ch := range frame.Choices {
			if ch.Delta.Audio == nil || ch.Delta.Audio.Data == "" {
				continue
			}
			pcm, err := base64.StdEncoding.DecodeString(ch.Delta.Audio.Data)
			if err != nil {
				c.log.Warn(ctx, logx.EventTTSFailed,
					"model", model, "reason", "an audio frame was not valid base64")
				continue
			}
			total += len(pcm)
			if total > speechChunkLimit {
				return errs.New(op, errs.CodeInvariantViolated).
					WithDetail("the speech stream passed %d bytes without ending; a single spoken turn should not run for minutes", speechChunkLimit)
			}
			if err := onPCM(pcm); err != nil {
				// The caller stopped us — a barge-in, or the room closing. Not a
				// failure, and not logged as one.
				return nil
			}
		}
	}
	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			return nil // interrupted on purpose
		}
		// The provider answered 200 and then stopped. No error body exists —
		// the 200 went out before the audio did — so the status it committed to
		// and the read failure are what upstream said.
		up := newUpstream(resp.StatusCode, nil, err, c.secrets()...)
		return up.annotate(errs.Wrap(op, errs.CodeExternalUnavailable, up).
			WithDetail("the speech stream ended early after %d bytes: %s", total, up.Transport))
	}
	if total == 0 {
		// HTTP 200 and no audio is the shape a changed wire format takes — and it
		// is exactly what the non-streaming call does, so it is a mistake this
		// endpoint invites. Warned rather than returned as success.
		c.log.Warn(ctx, logx.EventTTSEmpty, "model", model, "chars", len(text))
	}
	return nil
}

// SpeakerModel reports which model speaks, for logs and the timeline.
func (c *OpenAICompatible) SpeakerModel() string { return c.models[RoleSpeaker] }
