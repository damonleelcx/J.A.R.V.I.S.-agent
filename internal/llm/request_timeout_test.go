package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Whose timeout wins: the client's general one, or the caller's own.
//
// # What was wrong
//
// FORGE_LLM_REQUEST_TIMEOUT arrived as http.Client.Timeout, which is a CEILING
// on every request the process makes and outranks any context deadline, however
// long. That was invisible while every caller wanted less than the general
// bound. It stopped being invisible when the planner was given its own
// (FORGE_PLANNER_REQUEST_TIMEOUT, GitHub issue 13): a planner told it had five
// minutes still died at three, and the error it died with says the endpoint
// could not be reached — which sends the reader to the model provider for a
// fault that is in this file.
//
// # What is fenced
//
// Both halves, because either one alone is easy to satisfy by accident:
//
//  1. a caller that states a LONGER deadline keeps it;
//  2. a caller that states none is still bounded by the general timeout.
//
// Without 2, "fixing" 1 by deleting the timeout would pass.

// slowServer answers after hold, and reports whether it was left to finish.
func slowServer(t *testing.T, hold time.Duration) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(hold):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant",` +
			`"content":"ok"},"finish_reason":"stop"}],"usage":{"total_tokens":7}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// timeoutClient is testClient with the general timeout said explicitly, because
// this file is about that number and reading it from a shared helper would hide
// the thing under test.
func timeoutClient(t *testing.T, baseURL string, general time.Duration) *OpenAICompatible {
	t.Helper()
	return NewOpenAICompatible(config.LLMConfig{
		BaseURL: baseURL, APIKey: "test-key",
		Planner: "planner-model", Executor: "executor-model",
		RequestTimeout: general, MaxRetries: 0,
	}, logx.Discard(), clock.System{})
}

// 1. A caller that asked for longer gets longer.
func TestClient_ACallerWithALongerDeadlineOfItsOwnKeepsItRatherThanTheGeneralTimeout(t *testing.T) {
	// The endpoint answers after 400 ms. The general bound is 100 ms — shorter,
	// so the old http.Client.Timeout would have killed this — and the caller has
	// stated 5 s, which is what the planner does.
	srv := slowServer(t, 400*time.Millisecond)
	c := timeoutClient(t, srv.URL, 100*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := c.Complete(ctx, simpleRequest())
	if err != nil {
		t.Fatalf("a caller that gave itself 5 s was cut off at the general 100 ms: %v\n"+
			"A timeout on the http client outranks every context deadline in the process, so a "+
			"planner-specific timeout longer than the general one could never take effect.", err)
	}
	if resp.Content != "ok" {
		t.Errorf("content = %q; the reply did not survive", resp.Content)
	}
}

// 2. A caller that asked for nothing is still bounded.
func TestClient_ACallerWithNoDeadlineIsStillBoundedByTheGeneralTimeout(t *testing.T) {
	// Twenty times the bound: long enough that an unbounded call is unmistakable,
	// short enough that the httptest server's Close does not sit waiting on a
	// handler this test has already stopped caring about.
	srv := slowServer(t, 3*time.Second)
	c := timeoutClient(t, srv.URL, 150*time.Millisecond)

	start := time.Now()
	_, err := c.Complete(context.Background(), simpleRequest())
	took := time.Since(start)

	if err == nil {
		t.Fatal("a call with no deadline of its own waited out a 3 s endpoint against a 150 ms " +
			"bound and came back happy; the general request timeout is not being applied at " +
			"all, which is an unbounded call in every path that sets no deadline of its own")
	}
	if code := errs.CodeOf(err); code != errs.CodeExternalUnavailable {
		t.Errorf("code = %s; want %s — a call cut off at our own bound must still read as the "+
			"endpoint being unreachable, which is what every caller handles", code,
			errs.CodeExternalUnavailable)
	}
	// Generous: this laptop runs several builds at once. The claim is that the
	// bound applied at all, not that it applied to the millisecond.
	if took > 10*time.Second {
		t.Errorf("the call took %s against a 150 ms bound; it was not bounded by it", took)
	}
}

// 3. The bound is per attempt, as http.Client.Timeout was.
//
// Moving it to cover the whole retry loop would quietly shorten every retried
// call: three attempts under one deadline is not three attempts.
func TestClient_TheGeneralTimeoutBoundsEachAttemptAndNotTheWholeRetryLoop(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts <= 2 {
			// Held past the bound, so this attempt dies on it.
			select {
			case <-time.After(2 * time.Second):
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant",` +
			`"content":"ok"},"finish_reason":"stop"}],"usage":{"total_tokens":7}}`))
	}))
	defer srv.Close()

	c := timeoutClient(t, srv.URL, 200*time.Millisecond)
	c.maxRetries = 2

	resp, err := c.Complete(context.Background(), simpleRequest())
	if err != nil {
		t.Fatalf("two attempts were cut off at the bound and the third was not reached (%v); "+
			"the bound is covering the retry loop rather than one attempt, so a retried call "+
			"gets less time than a first one", err)
	}
	if resp.Content != "ok" {
		t.Errorf("content = %q", resp.Content)
	}
	if attempts != 3 {
		t.Errorf("the endpoint saw %d attempt(s); want 3", attempts)
	}
}
