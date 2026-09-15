package httpapi

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Speech to text for the workbench microphone (PRD AUD-03, AUD-06).
//
// # The problem this solves
//
// The workbench's push-to-talk used ONLY the browser's SpeechRecognition. In
// Chrome and Edge that is not on the device: it streams the audio to Google's
// servers. From mainland China those are unreachable, so a person held the
// button, spoke, let go — and nothing happened. Reported from production on
// 2026-09-15 by the deployment's owner, whose model endpoint is in Beijing.
//
// The server already had a transcriber. Rooms use it for every spoken segment
// (internal/media/transcribe.go); the workbench never called it. This endpoint
// is that same transcriber, reached by the page.
//
// # Why the body is the raw recording and not JSON
//
// MediaRecorder hands the page a Blob, and fetch sends a Blob as-is with its
// content type. Base64 in JSON would add a third to the size for nothing and a
// decoder that can fail. The content type IS the container, which is the one
// thing the transcriber needs to be told.
//
// # Why nothing is converted
//
// Chrome, Edge and Firefox record audio/webm (Opus); Safari records audio/mp4
// (AAC). Both go to the provider as recorded. The transcriber passes the
// container to the provider as a data: URI and the provider decodes it. If a
// model cannot take one of them, that is the provider's answer, and it reaches
// the person rather than being papered over by a transcode this server would
// then own. `make test-asr` holds a webm and an mp4 fixture for that reason.
//
// # The limits, and who enforces which
//
// 60 seconds per hold, enforced by the page: it stops the recorder and says so.
// The server cannot measure duration without parsing every container, so it
// enforces the byte cap instead: 2 MiB, which at the 32 kbit/s the page asks
// for is several minutes of speech, so a page within its 60 seconds never meets
// it. Both are refused by name, because a limit nobody can see is a bug report.
//
// See docs/bugfix/2026-09-15-the-microphone-sent-nothing.md

// maxRecordingBytes is the largest recording accepted.
//
// ‼️ Must stay below FORGE_HTTP_MAX_BODY_BYTES (4 MiB by default). BodyLimit
// wraps every request, and a recording that trips it first surfaces as a read
// error — handled below as the same 413, but the message would name this limit
// while the other one refused it.
const maxRecordingBytes = 2 << 20

// maxRecordingSeconds is the longest hold the page records. Stated here so the
// refusal can name it; the page reads the same number from voice.js.
const maxRecordingSeconds = 60

// recordingContainers are the containers a browser's MediaRecorder produces.
var recordingContainers = []string{"audio/webm", "audio/ogg", "audio/mp4", "audio/mpeg", "audio/wav"}

// speechToText is the part of a model client that can transcribe.
//
// An interface rather than *llm.OpenAICompatible so a deployment's client is
// asked what it can do rather than assumed to be one concrete type, and so the
// handler can be tested without a provider.
type speechToText interface {
	Transcribe(ctx context.Context, audio []byte, mimeType string) (*llm.Transcript, error)
	TranscriberModel() string
}

// transcriberOf returns the deployment's transcriber and its model, or nil.
//
// ‼️ A typed nil pointer in an interface is not a nil interface. A
// *llm.OpenAICompatible that was never built still satisfies speechToText, and
// calling TranscriberModel on it panics — so it is checked for by name, the way
// rooms.go checks before handing the same client to the media plane.
func transcriberOf(c llm.Client) (speechToText, string) {
	if c == nil {
		return nil, ""
	}
	if oc, ok := c.(*llm.OpenAICompatible); ok && oc == nil {
		return nil, ""
	}
	stt, ok := c.(speechToText)
	if !ok {
		return nil, ""
	}
	model := stt.TranscriberModel()
	if model == "" {
		return nil, ""
	}
	return stt, model
}

// recordingContainer reads the container from a Content-Type, parameters
// dropped.
//
// ‼️ The parameters MUST be dropped. Chrome labels its recording
// "audio/webm;codecs=opus", and the transcriber builds a data: URI from what it
// is given — "data:audio/webm;codecs=opus;base64," puts a second parameter
// where the provider expects the base64 marker.
func recordingContainer(contentType string) (string, bool) {
	if contentType == "" {
		return "", false
	}
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "", false
	}
	for _, c := range recordingContainers {
		if mt == c {
			return c, true
		}
	}
	return "", false
}

type transcribeResponse struct {
	Text  string `json:"text"`
	Model string `json:"model"`
}

// Transcribe handles POST /v1/transcribe.
func (h *ConverseHandlers) Transcribe(w http.ResponseWriter, r *http.Request) {
	const op = "httpapi.Transcribe"
	ctx := r.Context()

	stt, model := transcriberOf(h.deps.LLM)
	if stt == nil {
		// Refused, not degraded, and 501 so the page can tell "this deployment
		// has none" from "it failed just now" — the first latches the page onto
		// the browser's recogniser, the second is worth another try.
		WriteError(w, r, h.deps.Log, errs.New(op, errs.CodeConnectorUnavailable).
			WithDetail("this deployment has no speech to text. Set FORGE_LLM_TRANSCRIBER_MODEL to a "+
				"transcription model the endpoint serves — FORGE_LLM_TRANSCRIBER_BASE_URL with "+
				"FORGE_LLM_TRANSCRIBER_API_KEY when set, otherwise FORGE_LLM_BASE_URL; until then the "+
				"microphone uses the browser's own recognition where there is one, and typing always works"))
		return
	}

	container, ok := recordingContainer(r.Header.Get("Content-Type"))
	if !ok {
		WriteError(w, r, h.deps.Log, errs.New(op, errs.CodeUnsupportedMedia).
			WithDetail("Content-Type was %q; send the recording as recorded, as one of %s",
				r.Header.Get("Content-Type"), strings.Join(recordingContainers, ", ")))
		return
	}

	audio, err := io.ReadAll(io.LimitReader(r.Body, maxRecordingBytes+1))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			WriteError(w, r, h.deps.Log, recordingTooLarge(op))
			return
		}
		WriteError(w, r, h.deps.Log, errs.Wrap(op, errs.CodeValidationFailed, err).
			WithDetail("the recording could not be read: %v", err))
		return
	}
	if len(audio) > maxRecordingBytes {
		WriteError(w, r, h.deps.Log, recordingTooLarge(op))
		return
	}
	if len(audio) == 0 {
		WriteError(w, r, h.deps.Log, errs.New(op, errs.CodeValidationFailed).
			WithDetail("the recording was empty: no audio was captured, so there is nothing to transcribe"))
		return
	}

	out, err := stt.Transcribe(ctx, audio, container)
	if err != nil {
		// WARN, not ERROR: the conversation is untouched and the person is told
		// on the page. The detail goes to the log in full because a 503 withholds
		// it from the response, and the log is where an operator will look.
		h.deps.Log.Warn(ctx, logx.EventASRFailed, "surface", "workbench", "model", model,
			"bytes", len(audio), "content_type", container, "error", err.Error())
		WriteError(w, r, h.deps.Log, err)
		return
	}

	// ‼️ A 200 with no transcript is NOT silence here.
	//
	// The media plane reads it as an empty segment, which is right for a room
	// that is mostly quiet. A person who just held the button and spoke is not a
	// quiet room: returning "" would tell them nothing was heard when the model
	// never answered the question. Measured 2026-09-15 on the production
	// endpoint: qwen-audio-3.0-realtime-plus answers every input_audio request
	// with 200 and a status body, and no choices at all.
	if out.Unanswered {
		h.deps.Log.Warn(ctx, logx.EventASRFailed, "surface", "workbench", "model", model,
			"bytes", len(audio), "content_type", container, "reason", "answered without a transcript")
		WriteError(w, r, h.deps.Log, errs.New(op, errs.CodeConnectorUnavailable).
			WithDetail("the transcription model %s answered without a transcript, so it is not a "+
				"speech-to-text model this endpoint can run through chat completions. Set "+
				"FORGE_LLM_TRANSCRIBER_MODEL to one that is", model))
		return
	}

	heardBy := out.Model
	if heardBy == "" {
		heardBy = model
	}
	text := strings.TrimSpace(out.Text)
	h.deps.Log.Info(ctx, logx.EventASRTranscribed, "surface", "workbench", "model", heardBy,
		"bytes", len(audio), "content_type", container, "chars", len([]rune(text)),
		"audio_tokens", out.AudioTokens)

	WriteJSON(w, http.StatusOK, transcribeResponse{Text: text, Model: heardBy})
}

func recordingTooLarge(op string) error {
	return errs.New(op, errs.CodePayloadTooLarge).
		WithDetail("a recording over %d MiB is refused. Hold to talk is for one utterance, and the "+
			"workbench stops recording at %d seconds", maxRecordingBytes>>20, maxRecordingSeconds)
}
