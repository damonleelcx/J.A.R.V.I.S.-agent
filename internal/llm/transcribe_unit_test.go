package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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
