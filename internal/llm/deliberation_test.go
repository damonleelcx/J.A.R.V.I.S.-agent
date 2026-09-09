package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// The fences over deliberation.go. Each one holds a property that was measured
// on 2026-09-06 and cost 28.7 seconds per conversational turn when it was
// absent.

// clientAt builds a driver whose baseURL is what the test wants to say
// something about, while every request still goes to `target`.
//
// Two URLs, because the property under test is a function of the ENDPOINT'S
// HOSTNAME and httptest can only ever hand out 127.0.0.1. The rewrite is in one
// place so no test invents its own.
func clientAt(t *testing.T, declared, target string) *OpenAICompatible {
	t.Helper()
	c := NewOpenAICompatible(config.LLMConfig{
		BaseURL: declared, APIKey: "test-key",
		Converse: "converse-model", Executor: "executor-model",
		RequestTimeout: 10 * time.Second,
	}, logx.Discard(), clock.System{})
	c.baseURL = strings.TrimRight(target, "/")
	return c
}

func bodyOf(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body
}

func okReply(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"model":"m","choices":[{"index":0,
		"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
}

func TestTheConversationRoleIsToldNotToDeliberate(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = bodyOf(t, r)
		okReply(w)
	}))
	defer srv.Close()

	c := clientAt(t, "https://dashscope.aliyuncs.com/compatible-mode/v1", srv.URL)
	if _, err := c.Complete(context.Background(),
		Request{Role: RoleConverse, Messages: []Message{{Role: User, Content: "hello"}}}); err != nil {
		t.Fatal(err)
	}
	v, ok := got["enable_thinking"]
	if !ok {
		t.Fatal("the conversation request carried no enable_thinking field. " +
			"Measured 2026-09-06: without it the first content token arrives 28.7s late, " +
			"against PRD AUD-02's 700ms — and nothing fails, so nothing notices.")
	}
	if v != false {
		t.Errorf("enable_thinking = %v; want false", v)
	}
}

func TestAThinkingRoleIsLeftAlone(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = bodyOf(t, r)
		okReply(w)
	}))
	defer srv.Close()

	c := clientAt(t, "https://dashscope.aliyuncs.com/compatible-mode/v1", srv.URL)
	if _, err := c.Complete(context.Background(),
		Request{Role: RoleExecutor, Messages: []Message{{Role: User, Content: "hello"}}}); err != nil {
		t.Fatal(err)
	}
	if _, present := got["enable_thinking"]; present {
		t.Error("the executor was told not to deliberate. It is not latency-bound and thinking " +
			"is what it is for; turning it off everywhere would trade a slow conversation for " +
			"worse answers on every other role.")
	}
}

func TestAnUnknownEndpointIsNotSentAProviderExtension(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = bodyOf(t, r)
		okReply(w)
	}))
	defer srv.Close()

	c := clientAt(t, "https://api.openai.com/v1", srv.URL)
	if _, err := c.Complete(context.Background(),
		Request{Role: RoleConverse, Messages: []Message{{Role: User, Content: "hello"}}}); err != nil {
		t.Fatal(err)
	}
	if _, present := got["enable_thinking"]; present {
		t.Error("enable_thinking was sent to an endpoint not known to understand it. " +
			"It is a DashScope extension, not OpenAI wire format, and a strict endpoint " +
			"rejects the whole request — trading a slow conversation for no conversation.")
	}
}

func TestTheStreamingPathIsToldToo(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = bodyOf(t, r)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()

	c := clientAt(t, "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1", srv.URL)
	err := c.Stream(context.Background(),
		Request{Role: RoleConverse, Messages: []Message{{Role: User, Content: "hello"}}},
		func(Chunk) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if got["enable_thinking"] != false {
		t.Error("the STREAMING request did not carry enable_thinking=false. This is the path a " +
			"person actually waits on; a fence that only covered Complete would be green while " +
			"every real turn was slow.")
	}
}

// The host match is a suffix on the domain, because one provider's regional
// endpoints are separate hostnames under it. Table rather than four tests: what
// is being held is the RULE, and a rule is easier to read as its cases.
func TestWhichEndpointsUnderstandTheField(t *testing.T) {
	for _, tc := range []struct {
		url   string
		field string
	}{
		{"https://dashscope.aliyuncs.com/compatible-mode/v1", "enable_thinking"},
		{"https://dashscope-intl.aliyuncs.com/compatible-mode/v1", "enable_thinking"},
		{"https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1", "enable_thinking"},
		{"https://api.openai.com/v1", ""},
		{"http://localhost:8000/v1", ""},
		// Not a suffix of the domain, merely ends with the same letters. A
		// naive strings.HasSuffix on the bare domain would send a provider
		// extension to somebody else's server.
		{"https://notaliyuncs.com/v1", ""},
		{"://broken", ""},
	} {
		got, ok := noDeliberation(tc.url)
		if got != tc.field || ok != (tc.field != "") {
			t.Errorf("noDeliberation(%q) = %q,%v; want %q,%v", tc.url, got, ok, tc.field, tc.field != "")
		}
	}
}

// A 404 is what a provider returns when it RETIRES a model. The message has to
// carry the way out, because the deployment that hits it changed nothing.
func TestAMissingModelNamesWhatTheEndpointDoesServe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"qwen3.8-max"},{"id":"qwen3.7-plus"}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"Model not exist.","code":"model_not_found"}}`))
	}))
	defer srv.Close()

	_, err := testClient(t, srv.URL, 0).Complete(context.Background(), simpleRequest())
	if err == nil {
		t.Fatal("a 404 was not reported as an error")
	}
	msg := err.Error()
	for _, want := range []string{"qwen3.7-plus", "qwen3.8-max", "FORGE_LLM_EXECUTOR_MODEL"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the not-found message does not contain %q.\nGot: %s\n\n"+
				"On 2026-09-06 this error said only `Model not exist.` and was read as an unpaid "+
				"bill for two days. The endpoint knows the answer; it has to be asked.", want, msg)
		}
	}
}

// The list could not be read. Saying so is the point: a short list read as a
// complete one is how somebody concludes their key is scoped wrongly.
func TestAModelListThatCannotBeReadSaysSo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := testClient(t, srv.URL, 0).Complete(context.Background(), simpleRequest())
	if err == nil {
		t.Fatal("a 404 was not reported as an error")
	}
	if !strings.Contains(err.Error(), "could not be read") {
		t.Errorf("a failed model listing was not reported as one: %s", err.Error())
	}
}

// The vision role must not deliberate either.
//
// # What this closes
//
// The visual check runs INSIDE a turn, before the geometry is emitted, so every
// millisecond it spends is a millisecond before the model appears on screen.
// Measured 2026-09-09 against qwen3.8-max on the check's own contact sheet, one
// closed question about a picture:
//
//	123000 ms thinking · 1500 ms not
//
// 6562 reasoning tokens against 29 tokens of answer, and the SAME answer either
// way — correct in both. Deliberating turned a check worth having into one
// nobody could afford to run, and it fails in the direction nothing notices: the
// answer is right, it just arrives two minutes later.
func TestTheVisionRoleIsToldNotToDeliberate(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = bodyOf(t, r)
		okReply(w)
	}))
	defer srv.Close()

	// A vision model is configured, as a deployment that uses the visual check
	// must have one: clientAt leaves it empty, and an unconfigured role proves
	// nothing about the role that ships.
	c := NewOpenAICompatible(config.LLMConfig{
		BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", APIKey: "test-key",
		Converse: "converse-model", Vision: "vision-model",
		RequestTimeout: 10 * time.Second,
	}, logx.Discard(), clock.System{})
	c.baseURL = strings.TrimRight(srv.URL, "/")

	if _, err := c.Complete(context.Background(),
		Request{Role: RoleVision, Messages: []Message{{Role: User, Content: "what is this"}}}); err != nil {
		t.Fatal(err)
	}
	v, ok := got["enable_thinking"]
	if !ok {
		t.Fatal("the vision request carried no enable_thinking field. Measured 2026-09-09: " +
			"without it one visual check takes 123 SECONDS instead of 1.5, for the same " +
			"answer — and nothing fails, so nothing notices.")
	}
	if v != false {
		t.Errorf("enable_thinking = %v; want false", v)
	}
}
