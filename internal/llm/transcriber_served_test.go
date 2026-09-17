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
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Whether the transcription endpoint serves the transcription model.
//
// # Why these exist
//
// On 2026-09-17 production advertised server transcription because a model NAME
// was configured, from an endpoint that serves no speech-to-text model. The page
// recorded the owner's first sentence, uploaded it, got a 501, and only then
// said so. These hold the three properties the fix depends on: an unlisted model
// is not advertised; the answer is cached, so no page pays a request for it; and
// the answer expires, so a provider that later adds the model turns the
// microphone back on without a restart.
//
// See internal/llm/transcriber_served.go

// catalogue is a fake endpoint whose model list can change mid-test, counting
// how often it is asked.
type catalogue struct {
	mu     sync.Mutex
	models string // the JSON body of GET /models; "" answers 404
	asked  int
}

func (c *catalogue) serve(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"Model not exist."}}`))
			return
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		c.asked++
		if c.models == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(c.models))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func (c *catalogue) set(models string) { c.mu.Lock(); c.models = models; c.mu.Unlock() }
func (c *catalogue) count() int        { c.mu.Lock(); defer c.mu.Unlock(); return c.asked }

func checkedClient(url string, clk clock.Clock) *OpenAICompatible {
	return NewOpenAICompatible(config.LLMConfig{
		BaseURL: url, APIKey: "test-key", Transcriber: "qwen3-asr-flash-2026-02-10",
		RequestTimeout: 10 * time.Second,
	}, logx.Discard(), clk)
}

// The production answer, replayed: the endpoint lists fourteen models and none of
// them is the transcriber. The page must be told "off", with a reason that names
// what to set — and not the host, because the reason reaches a public endpoint.
func TestTranscriberAvailability_AModelTheEndpointDoesNotListIsNotAdvertised(t *testing.T) {
	cat := &catalogue{models: `{"data":[{"id":"qwen3.8-flash"},{"id":"qwen-audio-3.0-realtime-plus"}]}`}
	url := cat.serve(t)
	c := checkedClient(url, clock.System{})

	got := c.CheckTranscriber(context.Background())
	if got.Served {
		t.Fatalf("a model the endpoint does not list is advertised as served: %+v", got)
	}
	if !got.Verified {
		t.Errorf("a list that was read and lacks the model is reported as unverified: %+v", got)
	}
	for _, want := range []string{"off", "does not serve qwen3-asr-flash-2026-02-10", "FORGE_LLM_TRANSCRIBER_BASE_URL", "FORGE_LLM_TRANSCRIBER_API_KEY"} {
		if !strings.Contains(got.Reason, want) {
			t.Errorf("the reason does not say %q: %s", want, got.Reason)
		}
	}
	// ‼️ GET /v1/meta/models is unauthenticated; the host can carry a workspace id.
	if strings.Contains(got.Reason, strings.TrimPrefix(url, "http://")) {
		t.Errorf("the public reason names the provider host: %s", got.Reason)
	}

	cat.set(`{"data":[{"id":"qwen3.8-flash"},{"id":"qwen3-asr-flash-2026-02-10"}]}`)
	if got := c.CheckTranscriber(context.Background()); !got.Served || !got.Verified || got.Reason != "" {
		t.Errorf("a listed model is not advertised as served and verified: %+v", got)
	}
}

// An unreadable list proves nothing. Turning the microphone off because /models
// was down at boot would be the same bug pointing the other way.
func TestTranscriberAvailability_AListThatCannotBeReadLeavesTranscriptionOfferedButUnverified(t *testing.T) {
	cat := &catalogue{} // /models answers 404
	c := checkedClient(cat.serve(t), clock.System{})
	got := c.CheckTranscriber(context.Background())
	if !got.Served || got.Verified {
		t.Errorf("an unreadable list changed what is offered: %+v", got)
	}
	if !strings.Contains(got.Reason, "unverified") {
		t.Errorf("the reason does not say the answer is unverified: %s", got.Reason)
	}

	// And a provider that cannot be reached at all answers the same way, without
	// the check hanging: forged starts it in a goroutine and must not care.
	dead := checkedClient("http://127.0.0.1:1", clock.System{})
	done := make(chan TranscriberAvailability, 1)
	go func() { done <- dead.CheckTranscriber(context.Background()) }()
	select {
	case got := <-done:
		if !got.Served || got.Verified {
			t.Errorf("an unreachable provider changed what is offered: %+v", got)
		}
	case <-time.After(modelListTimeout + 5*time.Second):
		t.Fatal("the check did not finish against an unreachable provider")
	}
}

// No page pays a request for the answer, and the answer does not last forever.
func TestTranscriberAvailability_TheAnswerIsCachedAndRefreshedInTheBackgroundOnceStale(t *testing.T) {
	cat := &catalogue{models: `{"data":[{"id":"qwen3.8-flash"}]}`}
	clk := clock.NewFake(time.Date(2026, 9, 17, 7, 0, 0, 0, time.UTC))
	c := checkedClient(cat.serve(t), clk)

	// Before the first answer: the first reader waits for it, briefly.
	if got := c.TranscriberAvailability(context.Background(), 10*time.Second); got.Served {
		t.Fatalf("the first page was told the unlisted model is served: %+v", got)
	}
	for i := 0; i < 50; i++ {
		if got := c.TranscriberAvailability(context.Background(), 0); got.Served {
			t.Fatalf("a cached answer changed: %+v", got)
		}
	}
	if n := cat.count(); n != 1 {
		t.Fatalf("51 reads asked the endpoint %d times, want 1: the check is on the request path", n)
	}

	// The provider adds the model. Within the negative lifetime nothing changes…
	cat.set(`{"data":[{"id":"qwen3.8-flash"},{"id":"qwen3-asr-flash-2026-02-10"}]}`)
	clk.Advance(notServedTTL - time.Second)
	if got := c.TranscriberAvailability(context.Background(), 0); got.Served || cat.count() != 1 {
		t.Fatalf("the answer was refreshed before it was stale (%d asks): %+v", cat.count(), got)
	}
	// …and after it, the stale read still returns at once, and starts a refresh.
	clk.Advance(2 * time.Second)
	if got := c.TranscriberAvailability(context.Background(), 0); got.Served {
		t.Fatalf("a stale read waited for the refresh instead of returning the cached answer: %+v", got)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if got := c.TranscriberAvailability(context.Background(), 0); got.Served && got.Verified {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("a provider that added the model is never advertised (%d asks): the answer went stale forever", cat.count())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := cat.count(); n != 2 {
		t.Errorf("one stale answer caused %d asks in total, want 2", n)
	}
}

// A served answer that the endpoint contradicts with a 404 is forgotten, so the
// next page does not advertise a model that has just been refused.
func TestTranscriberAvailability_AnUploadRefusedWith404ForgetsTheServedAnswer(t *testing.T) {
	cat := &catalogue{models: `{"data":[{"id":"qwen3-asr-flash-2026-02-10"}]}`}
	clk := clock.NewFake(time.Date(2026, 9, 17, 7, 0, 0, 0, time.UTC))
	c := checkedClient(cat.serve(t), clk)
	if got := c.CheckTranscriber(context.Background()); !got.Served {
		t.Fatalf("setup: %+v", got)
	}
	cat.set(`{"data":[{"id":"qwen3.8-flash"}]}`) // retired
	if _, err := c.Transcribe(context.Background(), []byte{1, 2, 3}, "audio/webm"); err == nil {
		t.Fatal("setup: the upload was not refused")
	}
	asked := cat.count()
	_ = c.TranscriberAvailability(context.Background(), 0) // still served, once; starts the refresh
	deadline := time.Now().Add(10 * time.Second)
	for {
		if got := c.TranscriberAvailability(context.Background(), 0); !got.Served {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("a model refused with 404 is still advertised %s after the refusal (asks %d→%d)",
				"with no clock movement", asked, cat.count())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
