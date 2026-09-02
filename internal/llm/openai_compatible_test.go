package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

func testClient(t *testing.T, baseURL string, retries int) *OpenAICompatible {
	t.Helper()
	return NewOpenAICompatible(config.LLMConfig{
		BaseURL: baseURL, APIKey: "test-key",
		Planner: "planner-model", Executor: "executor-model",
		Verifier: "verifier-model", Summarizer: "summarizer-model",
		RequestTimeout: 10 * time.Second, MaxRetries: retries,
	}, logx.Discard(), clock.System{})
}

func simpleRequest() Request {
	return Request{Role: RoleExecutor, Messages: []Message{{Role: User, Content: "hello"}}}
}

func TestSuccessfulCompletion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		var body chatRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "executor-model" {
			t.Errorf("model = %q; the role was not resolved to its configured model", body.Model)
		}
		if body.MaxTokens != defaultMaxTokens {
			t.Errorf("max_tokens = %d; an unbounded response can consume a whole goal's budget", body.MaxTokens)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"executor-model","choices":[{"index":0,
			"message":{"role":"assistant","content":"hi there"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":10,"completion_tokens":3,"total_tokens":13}}`))
	}))
	defer srv.Close()

	resp, err := testClient(t, srv.URL, 0).Complete(context.Background(), simpleRequest())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "hi there" {
		t.Errorf("content = %q", resp.Content)
	}
	if resp.Usage.TotalTokens != 13 {
		t.Errorf("total tokens = %d", resp.Usage.TotalTokens)
	}
	if resp.Truncated() {
		t.Error("a 'stop' finish reason reported as truncated")
	}
}

// TestTokenTotalIsDerivedWhenAbsent — a provider that omits the total must not
// leave usage at zero, because zero-cost calls make every budget ceiling
// unreachable and the agent becomes unbounded without anything looking wrong.
func TestTokenTotalIsDerivedWhenAbsent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"x"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":100,"completion_tokens":25}}`))
	}))
	defer srv.Close()

	resp, err := testClient(t, srv.URL, 0).Complete(context.Background(), simpleRequest())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.TotalTokens != 125 {
		t.Errorf("total = %d, want 125 derived from the parts", resp.Usage.TotalTokens)
	}
}

// TestMissingUsageIsWarnedNotSwallowed — the budget cannot be enforced against a
// call that reports nothing, so it must be visible rather than silently free.
func TestMissingUsageIsWarnedNotSwallowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"x"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	var buf strings.Builder
	c := NewOpenAICompatible(config.LLMConfig{
		BaseURL: srv.URL, APIKey: "k", Executor: "m", RequestTimeout: 5 * time.Second,
	}, logx.New(logx.Options{Output: &buf, Format: "json"}), clock.System{})

	if _, err := c.Complete(context.Background(), simpleRequest()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), string(logx.EventLLMUsageMissing)) {
		t.Errorf("a response with no usage must warn; the token budget silently stops being enforced.\n%s", buf.String())
	}
}

// TestTruncationIsSurfaced — a truncated plan is not a plan. The driver reports
// it rather than deciding, because a truncated summary is survivable and a
// truncated plan is not.
func TestTruncationIsSurfaced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"half a plan"},
			"finish_reason":"length"}],"usage":{"total_tokens":8192}}`))
	}))
	defer srv.Close()

	var buf strings.Builder
	c := NewOpenAICompatible(config.LLMConfig{
		BaseURL: srv.URL, APIKey: "k", Executor: "m", RequestTimeout: 5 * time.Second,
	}, logx.New(logx.Options{Output: &buf, Format: "json"}), clock.System{})

	resp, err := c.Complete(context.Background(), simpleRequest())
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Truncated() {
		t.Fatal("a 'length' finish reason did not report as truncated")
	}
	if !strings.Contains(buf.String(), string(logx.EventLLMTruncated)) {
		t.Error("truncation must be logged; a silently truncated plan is missing its tail with no error anywhere")
	}
}

// TestErrorObjectInA200IsCaught — some providers return HTTP 200 carrying an
// error object. A caller that only branches on status code reads empty content
// and treats it as a model with nothing to say.
func TestErrorObjectInA200IsCaught(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"error":{"message":"quota exhausted","type":"billing","code":"insufficient_quota"}}`))
	}))
	defer srv.Close()

	_, err := testClient(t, srv.URL, 0).Complete(context.Background(), simpleRequest())
	if err == nil {
		t.Fatal("a 200 carrying an error object was treated as success")
	}
	if !strings.Contains(err.Error(), "quota exhausted") {
		t.Errorf("the provider's message should reach the operator: %v", err)
	}
}

func TestEmptyChoicesIsRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[],"usage":{"total_tokens":5}}`))
	}))
	defer srv.Close()

	if _, err := testClient(t, srv.URL, 0).Complete(context.Background(), simpleRequest()); err == nil {
		t.Fatal("200 with zero choices was returned as an empty answer")
	}
}

// TestRetryClassification decides whether the engine spins forever or drops a
// recoverable failure. Both are bad in different ways, so each status is pinned.
func TestRetryClassification(t *testing.T) {
	cases := []struct {
		status      int
		wantCode    errs.Code
		wantRetried bool
	}{
		{http.StatusTooManyRequests, errs.CodeRateLimited, true},
		{http.StatusInternalServerError, errs.CodeExternalUnavailable, true},
		{http.StatusBadGateway, errs.CodeExternalUnavailable, true},
		{http.StatusUnauthorized, errs.CodeConfigInvalid, false},
		{http.StatusForbidden, errs.CodeConfigInvalid, false},
		{http.StatusNotFound, errs.CodeConfigInvalid, false},
		{http.StatusBadRequest, errs.CodeExternalProtocol, false},
	}

	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			var calls atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"error":{"message":"upstream said no"}}`))
			}))
			defer srv.Close()

			_, err := testClient(t, srv.URL, 2).Complete(context.Background(), simpleRequest())
			if err == nil {
				t.Fatal("expected a failure")
			}
			if got := errs.CodeOf(err); got != tc.wantCode && !tc.wantRetried {
				t.Errorf("code = %v, want %v", got, tc.wantCode)
			}
			n := calls.Load()
			if tc.wantRetried && n == 1 {
				t.Errorf("%d was not retried; a transient upstream fault was dropped permanently", tc.status)
			}
			if !tc.wantRetried && n != 1 {
				t.Errorf("%d was retried %d times; it will fail identically forever and the retries "+
					"only delay the real error reaching the operator", tc.status, n)
			}
		})
	}
}

// TestAuthFailureMentionsTheHost is a usability fence with real history behind
// it: a DashScope key for the wrong regional endpoint fails with "Incorrect API
// key provided", which sends everyone to check the key rather than the host.
func TestAuthFailureMentionsTheHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Incorrect API key provided.","code":"invalid_api_key"}}`))
	}))
	defer srv.Close()

	_, err := testClient(t, srv.URL, 0).Complete(context.Background(), simpleRequest())
	if err == nil {
		t.Fatal("expected a failure")
	}
	msg := err.Error()
	if !strings.Contains(msg, srv.URL) {
		t.Error("the error should name the host that rejected us")
	}
	if !strings.Contains(strings.ToLower(msg), "region") {
		t.Errorf("the error should raise the regional-endpoint possibility, which is the most "+
			"common cause of this exact provider message: %v", err)
	}
}

func TestRetriesEventuallySucceed(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"finally"},
			"finish_reason":"stop"}],"usage":{"total_tokens":4}}`))
	}))
	defer srv.Close()

	resp, err := testClient(t, srv.URL, 3).Complete(context.Background(), simpleRequest())
	if err != nil {
		t.Fatalf("retries did not recover: %v", err)
	}
	if resp.Content != "finally" {
		t.Errorf("content = %q", resp.Content)
	}
}

func TestUnconfiguredRoleIsRefused(t *testing.T) {
	c := NewOpenAICompatible(config.LLMConfig{
		BaseURL: "http://unused", APIKey: "k", Executor: "m",
	}, logx.Discard(), clock.System{})

	_, err := c.Complete(context.Background(), Request{
		Role: RolePlanner, Messages: []Message{{Role: User, Content: "x"}},
	})
	if err == nil {
		t.Fatal("a role with no configured model was accepted")
	}
	if !strings.Contains(err.Error(), "FORGE_LLM_PLANNER_MODEL") {
		t.Errorf("the error should name the variable to set: %v", err)
	}
}

func TestToolArgumentsAreParsedNotMatched(t *testing.T) {
	// Escaping varies between providers and between models from one provider,
	// so arguments are always parsed rather than string-matched.
	fc := FunctionCall{Name: "list_dir", Arguments: `{"path":"\/tmp","recursive":true}`}
	var args struct {
		Path      string `json:"path"`
		Recursive bool   `json:"recursive"`
	}
	if err := fc.ParseArguments(&args); err != nil {
		t.Fatalf("escaped JSON failed to parse: %v", err)
	}
	if args.Path != "/tmp" || !args.Recursive {
		t.Errorf("parsed %+v", args)
	}

	// A tool with no parameters legitimately produces "" or "{}".
	if err := (FunctionCall{Name: "noop"}).ParseArguments(&args); err != nil {
		t.Errorf("empty arguments should not be an error: %v", err)
	}
	// Malformed arguments must be reported, not silently ignored.
	if err := (FunctionCall{Name: "bad", Arguments: "{not json"}).ParseArguments(&args); err == nil {
		t.Error("malformed tool arguments were accepted")
	}
}

func TestContextCancellationStopsRetrying(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := testClient(t, srv.URL, 8).Complete(ctx, simpleRequest()); err == nil {
		t.Fatal("expected a failure")
	}
	// With 8 retries and exponential backoff this would take minutes if
	// cancellation were ignored.
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("retrying continued for %s after the context was cancelled", elapsed)
	}
}

// ---------------------------------------------------------------------------
// live provider
// ---------------------------------------------------------------------------

// TestLiveProviderRoundTrip talks to the real endpoint.
//
// Skipped without credentials so CI stays hermetic and free. It exists because
// a mock proves the client speaks the format we THINK the provider uses, and
// nothing else — the tool-calling shape in particular differs between providers
// in ways a hand-written fake reproduces perfectly and reality does not.
func TestLiveProviderRoundTrip(t *testing.T) {
	key := os.Getenv("FORGE_LLM_API_KEY")
	if key == "" || os.Getenv("FORGE_LIVE_LLM_TESTS") == "" {
		t.Skip("set FORGE_LLM_API_KEY and FORGE_LIVE_LLM_TESTS=1 to exercise the real provider")
	}
	base := os.Getenv("FORGE_LLM_BASE_URL")
	if base == "" {
		base = "https://dashscope.aliyuncs.com/compatible-mode/v1"
	}

	c := NewOpenAICompatible(config.LLMConfig{
		BaseURL: base, APIKey: key,
		Planner:        envOr("FORGE_LLM_PLANNER_MODEL", "qwen3.8-max"),
		Executor:       envOr("FORGE_LLM_EXECUTOR_MODEL", "qwen3.8-max"),
		Verifier:       envOr("FORGE_LLM_VERIFIER_MODEL", "deepseek-v4-pro"),
		Summarizer:     envOr("FORGE_LLM_SUMMARIZER_MODEL", "qwen3.8-flash"),
		RequestTimeout: 90 * time.Second, MaxRetries: 2,
	}, logx.Discard(), clock.System{})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	t.Run("plain completion", func(t *testing.T) {
		resp, err := c.Complete(ctx, Request{
			Role:      RoleExecutor,
			Messages:  []Message{{Role: User, Content: "Reply with exactly the word: ready"}},
			MaxTokens: 32,
		})
		if err != nil {
			t.Fatalf("live completion failed: %v", err)
		}
		if !strings.Contains(strings.ToLower(resp.Content), "ready") {
			t.Errorf("content = %q", resp.Content)
		}
		if resp.Usage.TotalTokens == 0 {
			t.Error("the live provider reported no token usage; the budget would not be enforceable")
		}
		t.Logf("model=%s tokens=%d latency=%s", resp.Model, resp.Usage.TotalTokens, resp.Latency)
	})

	t.Run("tool calling", func(t *testing.T) {
		// This is the shape a hand-written fake reproduces perfectly and a real
		// provider may not.
		resp, err := c.Complete(ctx, Request{
			Role:     RoleExecutor,
			Messages: []Message{{Role: User, Content: "List the files in /tmp. Use the tool."}},
			Tools: []ToolDefinition{{
				Type: "function",
				Function: FunctionDefinition{
					Name:        "list_dir",
					Description: "List the entries in a directory.",
					Parameters: json.RawMessage(`{"type":"object",
						"properties":{"path":{"type":"string","description":"absolute path"}},
						"required":["path"]}`),
				},
			}},
			MaxTokens: 256,
		})
		if err != nil {
			t.Fatalf("live tool call failed: %v", err)
		}
		if len(resp.ToolCalls) == 0 {
			t.Fatalf("the provider returned no tool calls (finish_reason=%q, content=%q)",
				resp.FinishReason, resp.Content)
		}
		var args struct {
			Path string `json:"path"`
		}
		if err := resp.ToolCalls[0].Function.ParseArguments(&args); err != nil {
			t.Fatalf("the provider's tool arguments did not parse: %v", err)
		}
		if args.Path == "" {
			t.Error("the tool call carried no path argument")
		}
		t.Logf("tool=%s args=%s", resp.ToolCalls[0].Function.Name, resp.ToolCalls[0].Function.Arguments)
	})

	t.Run("verifier is a different family", func(t *testing.T) {
		// PRD SAF-03 in practice: prove the verifier route reaches a genuinely
		// different model, not the same one under another name.
		resp, err := c.Complete(ctx, Request{
			Role:      RoleVerifier,
			Messages:  []Message{{Role: User, Content: "Reply with exactly the word: checked"}},
			MaxTokens: 32,
		})
		if err != nil {
			t.Fatalf("the verifier route failed: %v", err)
		}
		if resp.Model == "" {
			t.Fatal("the verifier response did not report which model answered")
		}
		if resp.Model == c.ModelFor(RoleExecutor) {
			t.Errorf("the verifier was answered by the executor's model (%s); "+
				"a model grading its own output is not independent verification", resp.Model)
		}
		t.Logf("verifier model=%s", resp.Model)
	})
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
