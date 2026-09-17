package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/identity"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// The workbench's server-side speech to text.
//
// # Why these exist
//
// The workbench microphone used only the browser's SpeechRecognition, which in
// Chrome is Google's service — unreachable from mainland China, where this
// deployment's owner is. The server already had a transcriber for rooms and the
// workbench never used it. So push-to-talk now records and uploads here.
//
// Every refusal below is by NAME, because the person holding the button cannot
// read a server log: an empty transcript that was really a provider failure, or
// a 413 with no limit in it, is the silent failure this fix exists to remove.
//
// See docs/bugfix/2026-09-15-the-microphone-sent-nothing.md

// sttStub is a model client that can transcribe, and records what it was sent.
type sttStub struct {
	model      string
	text       string
	err        error
	unanswered bool

	calls int
	audio []byte
	mime  string
}

func (s *sttStub) Complete(context.Context, llm.Request) (*llm.Response, error) {
	return nil, errors.New("sttStub does not converse")
}
func (s *sttStub) ModelFor(llm.Role) string { return "test-model" }
func (s *sttStub) Transcribe(_ context.Context, audio []byte, mime string) (*llm.Transcript, error) {
	s.calls++
	s.audio, s.mime = audio, mime
	if s.err != nil {
		return nil, s.err
	}
	return &llm.Transcript{Text: s.text, Model: s.model, Unanswered: s.unanswered}, nil
}
func (s *sttStub) TranscriberModel() string { return s.model }

// A model that answered 200 with no transcript is not a person who said
// nothing. Measured on the production endpoint: qwen-audio-3.0-realtime-plus
// answers every input_audio request that way. As a 200 with empty text the page
// would say "no words were recognised" to somebody who spoke clearly.
func TestTranscribe_AModelThatAnsweredWithoutATranscriptIsNotSilence(t *testing.T) {
	stub := &sttStub{model: "qwen-audio-3.0-realtime-plus", unanswered: true}
	rec := postRecording(transcribeHandlers(stub), []byte{1, 2, 3}, "audio/webm")
	if rec.Code == http.StatusOK {
		t.Fatalf("an unanswered transcription was returned as a transcript: %s", rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"qwen-audio-3.0-realtime-plus", "FORGE_LLM_TRANSCRIBER_MODEL"} {
		if !strings.Contains(body, want) {
			t.Errorf("the refusal does not name %q: %s", want, body)
		}
	}
}

// chatOnly is a model client with no speech to text at all.
type chatOnly struct{}

func (chatOnly) Complete(context.Context, llm.Request) (*llm.Response, error) {
	return nil, errors.New("not used")
}
func (chatOnly) ModelFor(llm.Role) string { return "test-model" }

func transcribeHandlers(c llm.Client) *ConverseHandlers {
	d := testDeps()
	d.LLM = c
	return &ConverseHandlers{deps: d}
}

func postRecording(h *ConverseHandlers, body []byte, ctype string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/v1/transcribe", bytes.NewReader(body))
	if ctype != "" {
		r.Header.Set("Content-Type", ctype)
	}
	r = r.WithContext(context.WithValue(r.Context(), ctxKeyUser, &identity.User{ID: "usr_voice"}))
	rec := httptest.NewRecorder()
	h.Transcribe(rec, r)
	return rec
}

func TestTranscribe_TheRouteIsMountedAndRequiresASession(t *testing.T) {
	router := NewRouter(testDeps())
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/v1/transcribe", bytes.NewReader([]byte{1, 2, 3}))
	r.Header.Set("Content-Type", "audio/webm")
	router.ServeHTTP(rec, r)

	if rec.Code == http.StatusNotFound {
		t.Fatal("POST /v1/transcribe is not routed: the workbench records and uploads, and nothing answers")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("an anonymous upload got %d, want 401 — transcription spends the deployment's "+
			"model budget and must be behind a session: %s", rec.Code, rec.Body.String())
	}
}

func TestTranscribe_RecordedAudioComesBackAsText(t *testing.T) {
	stub := &sttStub{model: "asr-test", text: "set the wall to two point five millimetres"}
	h := transcribeHandlers(stub)
	audio := bytes.Repeat([]byte{0x1a, 0x45, 0xdf, 0xa3}, 256)

	rec := postRecording(h, audio, "audio/webm;codecs=opus")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Text  string `json:"text"`
		Model string `json:"model"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Text != stub.text {
		t.Errorf("text %q, want %q", got.Text, stub.text)
	}
	if got.Model != "asr-test" {
		t.Errorf("model %q: the workbench shows which model heard it, and it came back as %q", got.Model, got.Model)
	}
	if !bytes.Equal(stub.audio, audio) {
		t.Error("the transcriber was not handed the recorded bytes unchanged")
	}
	// ‼️ The parameters are dropped: the MIME type becomes a data: URI, and
	// "data:audio/webm;codecs=opus;base64," has a second parameter where the
	// provider expects base64.
	if stub.mime != "audio/webm" {
		t.Errorf("the transcriber was told %q, want the bare container audio/webm", stub.mime)
	}
}

func TestTranscribe_EveryBrowserRecordingContainerIsAccepted(t *testing.T) {
	for ctype, want := range map[string]string{
		"audio/webm;codecs=opus": "audio/webm", // Chrome, Edge, Firefox
		"audio/webm":             "audio/webm",
		"audio/mp4":              "audio/mp4", // Safari
		"audio/ogg; codecs=opus": "audio/ogg", // Firefox's other choice
		"audio/mpeg":             "audio/mpeg",
		"audio/wav":              "audio/wav",
	} {
		stub := &sttStub{model: "asr-test", text: "ok"}
		rec := postRecording(transcribeHandlers(stub), []byte{1, 2, 3, 4}, ctype)
		if rec.Code != http.StatusOK {
			t.Errorf("%s was refused (%d): %s", ctype, rec.Code, rec.Body.String())
			continue
		}
		if stub.mime != want {
			t.Errorf("%s reached the transcriber as %q, want %q", ctype, stub.mime, want)
		}
	}
}

func TestTranscribe_AnOversizedRecordingIsRefusedByName(t *testing.T) {
	stub := &sttStub{model: "asr-test", text: "never"}
	rec := postRecording(transcribeHandlers(stub), make([]byte, maxRecordingBytes+1), "audio/webm")

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, want 413: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "PAYLOAD_TOO_LARGE") || !strings.Contains(body, "seconds") {
		t.Errorf("the refusal does not name the limit a person can act on: %s", body)
	}
	if stub.calls != 0 {
		t.Error("an oversized recording was sent to the provider anyway")
	}
}

func TestTranscribe_AnUnsupportedContainerIsRefusedByName(t *testing.T) {
	for _, ctype := range []string{"", "application/json", "video/webm", "text/plain"} {
		stub := &sttStub{model: "asr-test", text: "never"}
		rec := postRecording(transcribeHandlers(stub), []byte{1, 2, 3}, ctype)
		if rec.Code != http.StatusUnsupportedMediaType {
			t.Errorf("%q: status %d, want 415: %s", ctype, rec.Code, rec.Body.String())
			continue
		}
		if !strings.Contains(rec.Body.String(), "audio/webm") {
			t.Errorf("%q: the refusal does not say what IS accepted: %s", ctype, rec.Body.String())
		}
		if stub.calls != 0 {
			t.Errorf("%q was sent to the provider anyway", ctype)
		}
	}
}

func TestTranscribe_AnEmptyRecordingIsRefusedRatherThanTranscribedAsSilence(t *testing.T) {
	stub := &sttStub{model: "asr-test", text: "never"}
	rec := postRecording(transcribeHandlers(stub), nil, "audio/webm")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "no audio") {
		t.Errorf("the refusal does not say nothing was captured: %s", rec.Body.String())
	}
	if stub.calls != 0 {
		t.Error("an empty recording was sent to the provider")
	}
}

func TestTranscribe_ADeploymentWithoutATranscriberSaysWhatTurnsItOn(t *testing.T) {
	for name, c := range map[string]llm.Client{
		"no model client":          nil,
		"a client that only chats": chatOnly{},
		"no transcription model":   &sttStub{model: ""},
	} {
		rec := postRecording(transcribeHandlers(c), []byte{1, 2, 3}, "audio/webm")
		if rec.Code != http.StatusNotImplemented {
			t.Errorf("%s: status %d, want 501: %s", name, rec.Code, rec.Body.String())
			continue
		}
		if !strings.Contains(rec.Body.String(), "FORGE_LLM_TRANSCRIBER_MODEL") {
			t.Errorf("%s: the refusal does not name the setting that turns it on: %s", name, rec.Body.String())
		}
		// The model alone is not enough where the chat endpoint serves no speech
		// model, which is production (measured 2026-09-15).
		if !strings.Contains(rec.Body.String(), "FORGE_LLM_TRANSCRIBER_BASE_URL") {
			t.Errorf("%s: the refusal does not name the endpoint that can serve one: %s", name, rec.Body.String())
		}
	}
}

func TestTranscribe_AProviderFailureIsNeverAnEmptyTranscript(t *testing.T) {
	t.Run("the provider is down", func(t *testing.T) {
		stub := &sttStub{model: "asr-test",
			err: errs.New("llm.Transcribe", errs.CodeExternalUnavailable).WithDetail("dial tcp: i/o timeout")}
		rec := postRecording(transcribeHandlers(stub), []byte{1, 2, 3}, "audio/webm")
		if rec.Code < 500 {
			t.Fatalf("a provider failure answered %d — a 2xx here is an empty transcript that was "+
				"really an outage: %s", rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), `"text"`) {
			t.Errorf("a failure carried a transcript field: %s", rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"message"`) {
			t.Errorf("a failure carried no readable message: %s", rec.Body.String())
		}
	})
	t.Run("the endpoint does not serve the model", func(t *testing.T) {
		detail := "the transcription provider returned 404: Model not exist. This endpoint currently " +
			"serves: qwen3.7-plus. Set FORGE_LLM_TRANSCRIBER_MODEL to one of those"
		stub := &sttStub{model: "asr-test",
			err: errs.New("llm.Transcribe", errs.CodeConnectorUnavailable).WithDetail("%s", detail)}
		rec := postRecording(transcribeHandlers(stub), []byte{1, 2, 3}, "audio/webm")
		if rec.Code != http.StatusNotImplemented {
			t.Fatalf("status %d, want 501: %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "currently serves: qwen3.7-plus") {
			t.Errorf("the person is not told which models the endpoint does serve: %s", rec.Body.String())
		}
	})
}

func TestTranscribe_TheWorkbenchIsToldWhetherTheServerTranscribes(t *testing.T) {
	read := func(c llm.Client) (bool, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		transcribeHandlers(c).Models(rec, httptest.NewRequest("GET", "/v1/meta/models", nil))
		var body struct {
			Transcription struct {
				Server bool   `json:"server"`
				Model  string `json:"model"`
			} `json:"transcription"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%v: %s", err, rec.Body.String())
		}
		return body.Transcription.Server, body.Transcription.Model
	}

	if ok, model := read(&sttStub{model: "asr-test"}); !ok || model != "asr-test" {
		t.Errorf("a deployment with a transcriber reports server=%v model=%q", ok, model)
	}
	if ok, _ := read(chatOnly{}); ok {
		t.Error("a client that cannot transcribe is reported as transcribing")
	}
	if ok, _ := read(nil); ok {
		t.Error("a deployment with no model client is reported as transcribing")
	}
}

// The production failure of 2026-09-17, replayed through the real model client.
//
// # Why
//
// The endpoint listed fourteen models, none of them speech to text, and
// /v1/meta/models still said "server": true because a model NAME was
// configured. The page believed it, recorded the owner's first sentence, lost it
// to a 501, and only then said so. "server" must now come from the endpoint's
// own list, and "reason" must say why it is off in words an owner can act on.
//
// ‼️ Through llm.OpenAICompatible, not a stub: the stub has no model list, and
// the property is exactly that the list is consulted.
func TestTranscribe_TheServerDoesNotAdvertiseTranscriptionTheEndpointDoesNotServe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"qwen3.8-flash"},{"id":"qwen-audio-3.0-realtime-plus"},{"id":"qwen-audio-3.0-tts-plus"}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	client := llm.NewOpenAICompatible(config.LLMConfig{
		BaseURL: srv.URL, APIKey: "test-key", Transcriber: "qwen3-asr-flash-2026-02-10",
		Converse: "qwen3.8-flash", RequestTimeout: 10 * time.Second,
	}, logx.Discard(), clock.System{})

	rec := httptest.NewRecorder()
	transcribeHandlers(client).Models(rec, httptest.NewRequest("GET", "/v1/meta/models", nil))
	var body struct {
		Transcription struct {
			Server   bool   `json:"server"`
			Model    string `json:"model"`
			Verified bool   `json:"verified"`
			Reason   string `json:"reason"`
		} `json:"transcription"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("%v: %s", err, rec.Body.String())
	}
	tr := body.Transcription
	if tr.Server {
		t.Fatalf("transcription is advertised from an endpoint that does not serve the model: %s", rec.Body.String())
	}
	if !tr.Verified || tr.Model != "qwen3-asr-flash-2026-02-10" {
		t.Errorf("verified=%v model=%q, want a verified answer about the configured model", tr.Verified, tr.Model)
	}
	for _, want := range []string{"server transcription is off", "does not serve qwen3-asr-flash-2026-02-10", "FORGE_LLM_TRANSCRIBER_API_KEY"} {
		if !strings.Contains(tr.Reason, want) {
			t.Errorf("the advertised reason does not say %q: %s", want, tr.Reason)
		}
	}
	if strings.Contains(rec.Body.String(), strings.TrimPrefix(srv.URL, "http://")) {
		t.Errorf("the public /v1/meta/models names the provider host: %s", rec.Body.String())
	}
}
