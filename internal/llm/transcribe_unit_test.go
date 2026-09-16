package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// How a transcription that did not happen is reported.
//
// Measured 2026-09-15 against the production endpoint, which is what these
// fake: the configured transcriber answers 404 `Model not exist.`, and the one
// audio model it does list answers 200 with a status body and no choices. The
// workbench has to tell a person which of those happened, so neither may look
// like an outage and neither may look like silence.
//
// See docs/bugfix/2026-09-15-the-microphone-sent-nothing.md

func transcriberClient(baseURL string) *OpenAICompatible {
	return NewOpenAICompatible(config.LLMConfig{
		BaseURL: baseURL, APIKey: "test-key", Transcriber: "asr-model",
		RequestTimeout: 10 * time.Second,
	}, logx.Discard(), clock.System{})
}

func TestTranscribe_AModelTheEndpointDoesNotServeIsUnavailableHereNotAnOutage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"qwen3.7-plus"},{"id":"qwen-audio-3.0-realtime-plus"}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"Model not exist.","code":"model_not_found"}}`))
	}))
	defer srv.Close()

	_, err := transcriberClient(srv.URL).Transcribe(context.Background(), []byte{1, 2, 3}, "audio/webm")
	if err == nil {
		t.Fatal("a 404 was not reported")
	}
	if !errs.Is(err, errs.CodeConnectorUnavailable) {
		t.Errorf("an unserved model is %s, want CONNECTOR_UNAVAILABLE. As an outage (503) its "+
			"detail is withheld from the response, and the person holding the microphone is told "+
			"a service is down when the fix is one setting", errs.CodeOf(err))
	}
	for _, want := range []string{"qwen3.7-plus", "FORGE_LLM_TRANSCRIBER_MODEL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %q: %s", want, err.Error())
		}
	}
}

func TestTranscribe_AnAnswerWithNoTranscriptIsMarkedUnanswered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status_message":"Success.","status_name":"Success"}`))
	}))
	defer srv.Close()

	got, err := transcriberClient(srv.URL).Transcribe(context.Background(), []byte{1, 2, 3}, "audio/webm")
	if err != nil {
		t.Fatalf("the media plane reads this as an empty segment and must go on doing so: %v", err)
	}
	if !got.Unanswered {
		t.Error("a 200 with no choices is indistinguishable from silence, so the workbench tells " +
			"somebody who just spoke that nothing was heard")
	}
}

func TestTranscribe_SilenceIsNotMarkedUnanswered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":""}}]}`))
	}))
	defer srv.Close()

	got, err := transcriberClient(srv.URL).Transcribe(context.Background(), []byte{1, 2, 3}, "audio/webm")
	if err != nil {
		t.Fatal(err)
	}
	if got.Unanswered {
		t.Error("an answer with an empty transcript is silence, and was marked as no answer")
	}
}

func TestTranscribe_AProviderOutageIsStillAnOutage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	_, err := transcriberClient(srv.URL).Transcribe(context.Background(), []byte{1, 2, 3}, "audio/webm")
	if !errs.Is(err, errs.CodeExternalUnavailable) {
		t.Errorf("a 502 is %s, want EXTERNAL_UNAVAILABLE", errs.CodeOf(err))
	}
}

// A transcriber endpoint of its own (FORGE_LLM_TRANSCRIBER_BASE_URL).
//
// The production chat endpoint serves no speech model, so transcription moves
// to one that does. What these hold is where the audio and the KEYS go: two
// httptest servers stand in for the two hosts, and each records every
// Authorization header it was sent.

// hostRecorder is one fake provider that remembers what reached it.
type hostRecorder struct {
	mu    sync.Mutex
	auths []string
	paths []string
}

func (h *hostRecorder) record(r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.auths = append(h.auths, r.Header.Get("Authorization"))
	h.paths = append(h.paths, r.URL.Path)
}

func (h *hostRecorder) seen() (auths, paths []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.auths...), append([]string(nil), h.paths...)
}

func twoHosts(t *testing.T, asr http.HandlerFunc) (chat, stt *hostRecorder, chatURL, sttURL string) {
	t.Helper()
	chat, stt = &hostRecorder{}, &hostRecorder{}
	chatSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chat.record(r)
		if r.URL.Path == "/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"qwen3.7-plus"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"from the chat host"}}]}`))
	}))
	sttSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stt.record(r)
		asr(w, r)
	}))
	t.Cleanup(chatSrv.Close)
	t.Cleanup(sttSrv.Close)
	return chat, stt, chatSrv.URL, sttSrv.URL
}

func splitClient(chatURL, sttURL, sttKey string) *OpenAICompatible {
	return NewOpenAICompatible(config.LLMConfig{
		BaseURL: chatURL, APIKey: "sk-chat-key", Transcriber: "qwen3-asr-flash",
		TranscriberBaseURL: sttURL, TranscriberAPIKey: sttKey,
		RequestTimeout: 10 * time.Second,
	}, logx.Discard(), clock.System{})
}

func TestTranscribe_ASeparateEndpointGetsTheAudioWithItsOwnKeyAndTheChatHostGetsNothing(t *testing.T) {
	chat, stt, chatURL, sttURL := twoHosts(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"two point five"}}]}`))
	})

	got, err := splitClient(chatURL, sttURL, "sk-asr-key").
		Transcribe(context.Background(), []byte{1, 2, 3}, "audio/webm")
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "two point five" {
		t.Errorf("transcript %q did not come from the transcriber endpoint", got.Text)
	}
	auths, paths := stt.seen()
	if len(paths) != 1 || paths[0] != "/chat/completions" {
		t.Fatalf("the transcriber endpoint saw %v, want one /chat/completions", paths)
	}
	if auths[0] != "Bearer sk-asr-key" {
		t.Errorf("the transcriber endpoint was sent %q, want its own key", auths[0])
	}
	if chatAuths, _ := chat.seen(); len(chatAuths) != 0 {
		t.Errorf("the chat host was sent %d request(s) for a transcription that is not its to hear", len(chatAuths))
	}
}

func TestTranscribe_AnotherHostWithNoKeyOfItsOwnIsNeverSentTheChatKey(t *testing.T) {
	chat, stt, chatURL, sttURL := twoHosts(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})

	// Load refuses this configuration; a hand-built one does not pass through
	// Load, and must fail with no key rather than succeed with the wrong one.
	_, _ = splitClient(chatURL, sttURL, "").Transcribe(context.Background(), []byte{1, 2, 3}, "audio/webm")

	auths, _ := stt.seen()
	if len(auths) == 0 {
		t.Fatal("nothing reached the transcriber endpoint")
	}
	for _, a := range auths {
		if strings.Contains(a, "sk-chat-key") {
			t.Errorf("the chat endpoint's key was sent to the transcriber host: %q", a)
		}
	}
	if chatAuths, _ := chat.seen(); len(chatAuths) != 0 {
		t.Errorf("the chat host was asked %d time(s)", len(chatAuths))
	}
}

func TestTranscribe_WithNoEndpointOfItsOwnSpeechUsesTheChatEndpointAndKey(t *testing.T) {
	chat, _, chatURL, _ := twoHosts(t, func(http.ResponseWriter, *http.Request) {})

	if _, err := splitClient(chatURL, "", "").Transcribe(context.Background(), []byte{1, 2, 3}, "audio/webm"); err != nil {
		t.Fatal(err)
	}
	auths, _ := chat.seen()
	if len(auths) != 1 || auths[0] != "Bearer sk-chat-key" {
		t.Errorf("unset must behave as before this setting existed; the chat host saw %v", auths)
	}
}

// A 404 from the transcriber's own endpoint lists THAT endpoint's models, asked
// with THAT endpoint's key, and says which setting points there.
func TestTranscribe_AnUnservedModelOnItsOwnEndpointListsThatEndpointAndNamesItsSettings(t *testing.T) {
	chat, stt, chatURL, sttURL := twoHosts(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"qwen3-asr-flash"},{"id":"paraformer-v2"}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"Model not exist."}}`))
	})

	_, err := splitClient(chatURL, sttURL, "sk-asr-key").
		Transcribe(context.Background(), []byte{1, 2, 3}, "audio/webm")
	if err == nil {
		t.Fatal("a 404 was not reported")
	}
	msg := err.Error()
	if !strings.Contains(msg, "qwen3-asr-flash") || strings.Contains(msg, "qwen3.7-plus") {
		t.Errorf("the served list is not the transcriber endpoint's own: %s", msg)
	}
	if !strings.Contains(msg, "FORGE_LLM_TRANSCRIBER_BASE_URL") {
		t.Errorf("the error does not say speech is on its own endpoint: %s", msg)
	}
	auths, _ := stt.seen()
	for _, a := range auths {
		if a != "Bearer sk-asr-key" {
			t.Errorf("the transcriber endpoint was sent %q", a)
		}
	}
	if chatAuths, _ := chat.seen(); len(chatAuths) != 0 {
		t.Errorf("the chat host was asked for its model list with a transcriber 404 (%d request(s))", len(chatAuths))
	}
}

// On the chat endpoint, the 404 must say the ENDPOINT can move — "set the model
// to one of those" is no advice when none of those can hear.
func TestTranscribe_AnUnservedModelOnTheChatEndpointNamesTheSettingsThatMoveSpeech(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"qwen3.7-plus"}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := transcriberClient(srv.URL).Transcribe(context.Background(), []byte{1, 2, 3}, "audio/webm")
	if err == nil {
		t.Fatal("a 404 was not reported")
	}
	for _, want := range []string{"FORGE_LLM_TRANSCRIBER_BASE_URL", "FORGE_LLM_TRANSCRIBER_API_KEY"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %s: %s", want, err.Error())
		}
	}
}
