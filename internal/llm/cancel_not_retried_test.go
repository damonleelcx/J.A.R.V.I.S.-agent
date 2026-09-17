package llm

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// A call its caller cancels is not retried and is not logged as an endpoint that could
// not be reached.
//
// Found stopping a real forge-worker with SIGTERM inside a model call
// (docs/spikes/2026-09-17-unverified-paths): the transport reported the cancel as
// EXTERNAL_UNAVAILABLE "cannot reach the model endpoint", which is retryable, so the
// stopping worker logged forge.llm.retrying for a healthy endpoint it had hung up on.
// docs/bugfix/2026-09-17-a-stopped-worker-logged-a-retry-of-a-model-it-had-hung-up-on.md
func TestComplete_ACallItsCallerCancelsIsNotRetriedOrBlamedOnTheEndpoint(t *testing.T) {
	var calls atomic.Int64
	arrived := make(chan struct{}, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		// Read first: net/http notices a client hanging up only once the body is read.
		_, _ = io.Copy(io.Discard, r.Body)
		arrived <- struct{}{}
		select { // holds the reply until the caller goes, and never past the test
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	}))
	defer srv.Close()

	var logged bytes.Buffer
	c := NewOpenAICompatible(config.LLMConfig{
		BaseURL: srv.URL, APIKey: "test-key", Executor: "executor-model",
		RequestTimeout: 30 * time.Second, MaxRetries: 3,
	}, logx.New(logx.Options{Output: &logged, Format: "json"}), clock.System{})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-arrived
		cancel()
	}()
	start := time.Now()
	_, err := c.Complete(ctx, simpleRequest())
	took := time.Since(start)

	if err == nil {
		t.Fatal("a cancelled call returned no error")
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("the endpoint was asked %d times; a cancelled call is asked once", n)
	}
	if strings.Contains(logged.String(), string(logx.EventLLMRetrying)) {
		t.Errorf("a cancelled call logged a retry of the endpoint:\n%s", logged.String())
	}
	if took > 5*time.Second {
		t.Errorf("Complete returned %s after its caller cancelled", took)
	}
}
