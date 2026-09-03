package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Speech to text, for the shared session's transcript (PRD AUD-03, COL-01).
//
// # Where this runs
//
// Not at /audio/transcriptions. The OpenAI-compatible surface this provider
// exposes does not implement that route — it returns 404 — and transcription is
// reached through the ordinary chat endpoint with an `input_audio` content part.
// Measured, not assumed: `docs/spikes/2026-09-03-webrtc-sfu/`.
//
// # The system context is a CORRECTNESS requirement, not a tuning parameter
//
// This is the finding that shapes this file. With no system context, the model
// drops decimal points out of realistic engineering speech:
//
//	spoken:   "wall thickness to two point five millimetres, tolerance ± zero point one"
//	returned: "wall thickness to two five millimeters tolerance plus or minus zero one"
//
// Reproducibly, 5 runs of 5. A 2.5 mm wall becomes "two five". In an engineering
// transcript that is not a transcription artefact — it is a wrong number that
// reads like a right one, arriving through a path RSN-06's "no fabricated
// measurements" rule does not currently watch.
//
// Two things were established by controlled experiment and both are encoded
// below:
//
//   - **Domain vocabulary alone does not fix it.** It corrects identifiers
//     ("AK73" rather than "AK seven three") and still drops the decimal on
//     shorter utterances, 5 of 5. AUD-03 lists vocabulary and unit-aware
//     transcription as separate capabilities; this is why.
//   - **An explicit decimal rule is the lever that worked in every case.**
//
// The failure is also shaped to survive a careless test: a bare "zero point
// five" transcribes correctly with no context at all, and the corruption appears
// only inside realistic sentences. Anything that changes `asrContext` must be
// re-checked against real speech — `make test-asr` is the fence.
const asrContext = "Engineering design review. Transcribe exactly what is said. " +
	"Vocabulary: bracket, fillet, chamfer, tolerance, wall thickness, bore, flange, " +
	"millimetres, microns, newton metres, datum, fixture, assembly, revision. " +
	"Write spoken decimals with the word point preserved: 'two point five' must be " +
	"written as 'two point five' and must not become 'two five'. Preserve units, " +
	"tolerances and part identifiers exactly as spoken. Return only the transcript."

// maxAudioBytes bounds one transcription request.
//
// The audio is base64'd into a JSON body, so the request is roughly a third
// larger again. A segment this size is already far longer than any utterance the
// segmenter should produce; reaching it means segmentation is broken, and
// failing loudly here is better than a multi-megabyte request that times out.
const maxAudioBytes = 8 << 20

// Transcript is what one segment of audio was heard to say.
type Transcript struct {
	Text string
	// Model is which model produced it, for the timeline and for cost review.
	Model string
	// AudioTokens is what the provider billed for the audio, when it says.
	AudioTokens int64
}

// Transcribe converts one segment of audio to text.
//
// mimeType is the container, e.g. "audio/ogg" for the Ogg Opus the media plane
// produces. It is passed through to the data URI rather than sniffed, because
// the caller packaged the bytes and knows what they are.
//
// An empty transcript is returned as an empty Transcript and no error: silence,
// a cough, and a segment of comfort noise are ordinary, and a room where every
// pause produced an error in the log would train everybody to ignore the log.
func (c *OpenAICompatible) Transcribe(ctx context.Context, audio []byte, mimeType string) (*Transcript, error) {
	const op = "llm.Transcribe"

	if len(audio) == 0 {
		return &Transcript{}, nil
	}
	if len(audio) > maxAudioBytes {
		return nil, errs.New(op, errs.CodePayloadTooLarge).
			WithDetail("an audio segment of %d bytes exceeds the %d byte limit; segmentation is producing segments far longer than an utterance",
				len(audio), maxAudioBytes)
	}
	model := c.models[RoleTranscriber]
	if model == "" {
		return nil, errs.New(op, errs.CodeConfigInvalid).
			WithDetail("no transcription model is configured; set FORGE_LLM_TRANSCRIBER_MODEL")
	}

	body, err := json.Marshal(map[string]any{
		"model": model,
		"messages": []any{
			map[string]any{"role": "system",
				"content": []any{map[string]any{"type": "text", "text": asrContext}}},
			map[string]any{"role": "user",
				"content": []any{map[string]any{
					"type": "input_audio",
					"input_audio": map[string]any{
						"data": "data:" + mimeType + ";base64," +
							base64.StdEncoding.EncodeToString(audio),
					},
				}}},
		},
	})
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeSerializationFail, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeInternal, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeExternalUnavailable, err).
			WithDetail("the transcription request did not complete: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokensDetails struct {
				AudioTokens int64 `json:"audio_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	raw := new(bytes.Buffer)
	if _, err := raw.ReadFrom(resp.Body); err != nil {
		return nil, errs.Wrap(op, errs.CodeExternalUnavailable, err)
	}
	if resp.StatusCode >= 400 {
		return nil, errs.New(op, errs.CodeExternalUnavailable).
			WithDetail("the transcription provider returned %d: %s",
				resp.StatusCode, truncate(raw.String(), 300))
	}
	if err := json.Unmarshal(raw.Bytes(), &parsed); err != nil {
		return nil, errs.Wrap(op, errs.CodeSerializationFail, err).
			WithDetail("the transcription response was not JSON: %s", truncate(raw.String(), 300))
	}
	if parsed.Error != nil {
		return nil, errs.New(op, errs.CodeExternalUnavailable).
			WithDetail("the transcription provider refused: %s (%s)", parsed.Error.Message, parsed.Error.Code)
	}
	if len(parsed.Choices) == 0 {
		// HTTP 200 with no choices is the shape a silently-changed wire format
		// takes. Warned rather than returned as empty, because "everybody has
		// stopped talking" and "the response no longer parses" must not look the
		// same in the transcript.
		c.log.Warn(ctx, logx.EventASREmptyResponse,
			"model", model, "bytes", len(audio), "body", truncate(raw.String(), 200))
		return &Transcript{Model: model}, nil
	}

	text := strings.TrimSpace(parsed.Choices[0].Message.Content)
	return &Transcript{
		Text:        text,
		Model:       model,
		AudioTokens: parsed.Usage.PromptTokensDetails.AudioTokens,
	}, nil
}

// TranscriberModel reports which model transcribes, for logs and the timeline.
func (c *OpenAICompatible) TranscriberModel() string { return c.models[RoleTranscriber] }
