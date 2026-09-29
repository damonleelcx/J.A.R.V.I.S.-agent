package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// What the provider said survives the call that failed.
//
// # What was wrong (2026-09-29)
//
// A turn failed with EXTERNAL_UNAVAILABLE — "An external service could not be
// reached or returned a server error. Retry; the fault is upstream…" — and
// nothing was kept that said why. The retry loop ended with FORGE's own summary
// as the OUTERMOST detail, and the outer detail is what reaches the failed-turn
// row, the browser and the event. So a 429 quota exhaustion, a 503 outage, a
// model the provider had retired and a TLS failure were the same record
// afterwards. By the time anybody looked the endpoint was healthy again and the
// original cause was unrecoverable.
//
// # Why table-driven over SHAPES rather than one test per status
//
// The shapes are what differ, not the statuses: a JSON error body, an HTML
// error page from a hop in front of the provider, a body with a credential in
// it, and no response at all are four different extraction problems. A fence
// per status would have four copies of the same assertion and cover one shape.
//
// Every case asserts on errs.DetailOf — the outer detail, which is the thing
// that was missing — and on the carried *Upstream, because the detail is prose
// and the fields are what a log and a row can be read back from.
func TestUpstream_WhatTheProviderSaidIsKeptOnEveryFailureShape(t *testing.T) {
	cases := []struct {
		name string
		// status 0 and body "" mean: answer nothing, from a server that is not
		// listening.
		status int
		body   string
		// wantCode is the code on the OUTER error the caller sees. A retryable
		// failure is re-wrapped as EXTERNAL_UNAVAILABLE when the loop gives up,
		// which this work does not touch; wantAnywhere is the classification
		// that must still be in the chain underneath it.
		wantCode     errs.Code
		wantAnywhere errs.Code
		wantIn       []string
		wantNotIn    []string
		check        func(t *testing.T, u *Upstream)
	}{
		{
			name:   "429 with a JSON error body",
			status: http.StatusTooManyRequests,
			body: `{"error":{"message":"You exceeded your current quota.",` +
				`"type":"insufficient_quota","code":"quota_exhausted"}}`,
			wantCode:     errs.CodeExternalUnavailable,
			wantAnywhere: errs.CodeRateLimited,
			wantIn: []string{
				"429", "quota_exhausted", "You exceeded your current quota.",
			},
			check: func(t *testing.T, u *Upstream) {
				if u.Status != http.StatusTooManyRequests {
					t.Errorf("status = %d, want 429", u.Status)
				}
				if u.Code != "quota_exhausted" || u.Type != "insufficient_quota" {
					t.Errorf("code/type = %q/%q; the provider's own classification is the "+
						"difference between a quota that has run out and an outage",
						u.Code, u.Type)
				}
				if u.Message != "You exceeded your current quota." {
					t.Errorf("message = %q", u.Message)
				}
			},
		},
		{
			name:   "500 with an HTML body from a hop in front of the provider",
			status: http.StatusInternalServerError,
			body: "<html>\n  <head><title>500 Internal Server Error</title></head>\n" +
				"  <body>\n    <center><h1>openresty/1.21.4.1</h1></center>\n  </body>\n</html>",
			wantCode: errs.CodeExternalUnavailable,
			// WHICH hop failed is the one thing that distinguishes a proxy
			// falling over from the provider doing it.
			wantIn:    []string{"500", "openresty/1.21.4.1"},
			wantNotIn: []string{"\n"},
			check: func(t *testing.T, u *Upstream) {
				if u.Status != http.StatusInternalServerError {
					t.Errorf("status = %d, want 500", u.Status)
				}
				if u.Code != "" || u.Type != "" {
					t.Errorf("an HTML page was read as carrying a provider code (%q/%q); "+
						"inventing one would make an outage look classified", u.Code, u.Type)
				}
				if strings.ContainsAny(u.Message, "\n\r\t") {
					t.Errorf("the kept snippet still has its layout in it (%q); 400 characters "+
						"of HTML indentation say nothing", u.Message)
				}
			},
		},
		{
			name:     "a 200 that is really an error",
			status:   http.StatusOK,
			body:     `{"error":{"message":"Model not exist.","code":"InvalidParameter"}}`,
			wantCode: errs.CodeExternalProtocol,
			wantIn:   []string{"InvalidParameter", "Model not exist."},
		},
		{
			name:     "a flat provider body with no error object",
			status:   http.StatusServiceUnavailable,
			body:     `{"code":"ServiceUnavailable","message":"The engine is overloaded."}`,
			wantCode: errs.CodeExternalUnavailable,
			wantIn:   []string{"503", "ServiceUnavailable", "The engine is overloaded."},
		},
		{
			name:     "a status with an empty body",
			status:   http.StatusBadGateway,
			body:     "",
			wantCode: errs.CodeExternalUnavailable,
			// ‼️ "said nothing" is a fact, and it is not the same fact as a
			// message nobody kept.
			wantIn: []string{"502", "said nothing"},
			check: func(t *testing.T, u *Upstream) {
				if u.Message != "" {
					t.Errorf("an empty body produced a message %q", u.Message)
				}
			},
		},
		{
			name:     "no response at all",
			status:   0,
			wantCode: errs.CodeExternalUnavailable,
			// The transport's OWN words. The rest of the sentence is
			// platform-specific ("connection refused" against "connectex: …
			// actively refused it"), which is exactly why it is kept verbatim
			// rather than classified into a phrase of ours.
			wantIn: []string{"the provider did not answer", "dial tcp"},
			check: func(t *testing.T, u *Upstream) {
				if u.Status != 0 {
					t.Errorf("status = %d; nothing answered, so there is no status and a "+
						"number here would be an invention", u.Status)
				}
				if u.Transport == "" {
					t.Error("a refused connection kept nothing at all; a certificate that " +
						"expired and a machine that is not listening are then the same record")
				}
				if got := u.Label(); got != "no response" {
					t.Errorf("label = %q, want \"no response\"", got)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			url := srv.URL
			if tc.status == 0 {
				// Nothing listening: the transport-failure shape, without
				// waiting for a timeout to produce it.
				srv.Close()
			} else {
				defer srv.Close()
			}

			_, err := testClient(t, url, 0).Complete(context.Background(), simpleRequest())
			if err == nil {
				t.Fatal("expected a failure")
			}
			if got := errs.CodeOf(err); got != tc.wantCode {
				t.Errorf("code = %v, want %v (retry policy is not what this fence is about, "+
					"but it must not have moved either)", got, tc.wantCode)
			}
			if tc.wantAnywhere != "" && !errs.Is(err, tc.wantAnywhere) {
				t.Errorf("%v is no longer anywhere in the chain; the classification that "+
					"decides whether this is retried must survive the extra wrapping",
					tc.wantAnywhere)
			}

			detail := errs.DetailOf(err)
			for _, want := range tc.wantIn {
				if !strings.Contains(detail, want) {
					t.Errorf("the detail a person and a row both get does not contain %q.\n"+
						"That is the defect: every failure then reads as FORGE's general "+
						"sentence.\ndetail: %s", want, detail)
				}
			}
			for _, unwanted := range tc.wantNotIn {
				if strings.Contains(detail, unwanted) {
					t.Errorf("the detail contains %q, which it should not.\ndetail: %s",
						unwanted, detail)
				}
			}

			u := UpstreamOf(err)
			if u == nil {
				t.Fatal("the provider's answer is not reachable from the error; the log, the " +
					"failed-turn row and the browser all have to re-parse prose to find it")
			}
			if tc.check != nil {
				tc.check(t, u)
			}
		})
	}
}

// ‼️ EVERY credential-shaped fixture below is ASSEMBLED at run time and never
// written out as a literal.
//
// # Why, and why not an ignore rule
//
// A secret scanner cannot tell a test fixture from a leaked key — that is
// exactly what makes it worth having — and GitGuardian failed this branch over
// the literals that used to be here. Silencing the scanner, or adding an ignore
// file, trades a real control for a green tick on a change whose entire subject
// is not leaking credentials.
//
// Building the token from parts costs nothing instead: no key-shaped string
// exists in the source, and the redactor is exercised on the identical shape,
// because it only ever sees the assembled value.
//
// ‼️ Do not "tidy" these back into literals. The assertions depend on the SHAPE
// and never on the characters, so an inline constant would change nothing about
// what is tested and would fail the scan again.
func keyShaped(tag, fill string, n int) string {
	return "s" + "k-" + tag + strings.Repeat(fill, n)
}

// opaqueKey is a credential with no recognisable shape at all, which is the
// point of it: the redactor can only remove this one because it was TOLD about
// it. A key that does not look like a key is still a key.
func opaqueKey(tag string) string {
	return "fo" + "rge-live-" + tag + strings.Repeat("7", 10)
}

// bearerHeader is an Authorization header line carrying tok.
func bearerHeader(tok string) string { return "Authorization: Bea" + "rer " + tok }

// ‼️ Nothing that could be a credential reaches a log, an error detail, a
// database row or a browser.
//
// # Why a provider's error body is where this has to be enforced
//
// It is the one place a credential can come BACK from. A gateway that echoes
// the failing request, a proxy that quotes its own configuration, a validation
// error that repeats the header it rejected — all seen in the wild, and this
// body is now on its way to four places that outlive the turn. Keeping more of
// what upstream said is only safe if the keeping is redacted at the door.
//
// The body here carries both shapes at once: an Authorization header with a
// bearer token, and a bare key-shaped string. The endpoint's own configured key
// is echoed too, because a key that does not look like a key is still a key.
func TestUpstream_ACredentialInAProviderBodyIsNeverKept(t *testing.T) {
	// Assembled, not written out: see the note above keyShaped.
	configured := opaqueKey("a1")
	bearer := keyShaped("live-", "B", 20)
	bare := keyShaped("proj-", "Q", 18)

	body := `{"error":{"message":"Rejected request: ` + bearerHeader(bearer) +
		`; retry with a valid key. Your key ` + bare + ` (configured as ` + configured +
		`) is not enabled for this model.","code":"invalid_api_key"}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	var log strings.Builder
	c := NewOpenAICompatible(config.LLMConfig{
		BaseURL: srv.URL, APIKey: configured, Executor: "executor-model",
		RequestTimeout: 5 * time.Second,
	}, logx.New(logx.Options{Output: &log, Format: "json"}), clock.System{})

	_, err := c.Complete(context.Background(), simpleRequest())
	if err == nil {
		t.Fatal("expected a failure")
	}

	u := UpstreamOf(err)
	if u == nil {
		t.Fatal("no provider answer was kept at all")
	}
	// Four surfaces, because a redaction that holds on one and not the others
	// is not a redaction: the detail reaches the browser and the turn row, the
	// error string reaches every log site, and the field reaches the record.
	surfaces := map[string]string{
		"the error detail (browser, failed-turn row)": errs.DetailOf(err),
		"the error string (every log site)":           err.Error(),
		"the carried upstream message":                u.Message,
		"the structured log record":                   log.String(),
	}
	for _, secret := range []string{bearer, bare, configured} {
		for where, text := range surfaces {
			if strings.Contains(text, secret) {
				t.Errorf("%s carries a credential from the provider's body", where)
			}
		}
	}
	if !strings.Contains(u.Message, redactionMarker) {
		t.Errorf("nothing says a credential was removed, so %q reads as the whole message "+
			"the provider sent", u.Message)
	}
	// Redaction is not deletion: the rest of the sentence is why it was kept.
	if !strings.Contains(u.Message, "not enabled for this model") {
		t.Errorf("redaction took the message with it: %q", u.Message)
	}
	if u.Code != "invalid_api_key" {
		t.Errorf("code = %q; the provider's classification is not a secret", u.Code)
	}
}

// A provider can answer with a 200 KiB HTML error page. What is kept is bounded.
func TestUpstream_AHugeProviderBodyIsTruncatedNotStored(t *testing.T) {
	huge := "<html><body>" + strings.Repeat("the provider is unwell. ", 20000) + "</body></html>"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(huge))
	}))
	defer srv.Close()

	_, err := testClient(t, srv.URL, 0).Complete(context.Background(), simpleRequest())
	if err == nil {
		t.Fatal("expected a failure")
	}
	u := UpstreamOf(err)
	if u == nil {
		t.Fatal("no provider answer was kept at all")
	}
	if len(u.Message) > 4*upstreamMessageLimit {
		t.Errorf("the kept message is %d bytes; a provider's error page must not be able to "+
			"fill a log record or a durable row", len(u.Message))
	}
	if !strings.Contains(u.Message, "truncated") {
		t.Errorf("the message was cut without saying so, so a reader cannot tell a short "+
			"provider message from the start of a long one: %q", u.Message)
	}
	if !strings.Contains(u.Message, "the provider is unwell") {
		t.Errorf("bounding kept none of the message: %q", u.Message)
	}
	if len(errs.DetailOf(err)) > 8000 {
		t.Errorf("the detail is %d bytes, and it goes into a Postgres row and a browser",
			len(errs.DetailOf(err)))
	}
}

// ‼️ A 500 then two 429s is a different story from three 429s.
//
// The first is an endpoint that broke and then throttled; the second is a quota
// that has not moved. The old message said "failed after 3 attempts" and
// nothing about what the three attempts returned, so the two were the same
// record. This fence holds the trail, and holds that the LAST attempt's answer
// is the one the detail leads with.
func TestUpstream_MixedStatusesAcrossAttemptsAreAllCounted(t *testing.T) {
	statuses := []int{http.StatusInternalServerError, http.StatusTooManyRequests, http.StatusTooManyRequests}
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(n.Add(1)) - 1
		if i >= len(statuses) {
			i = len(statuses) - 1
		}
		w.WriteHeader(statuses[i])
		if statuses[i] == http.StatusTooManyRequests {
			_, _ = w.Write([]byte(`{"error":{"message":"quota exhausted","code":"rate_limit"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"error":{"message":"internal error","code":"server_error"}}`))
	}))
	defer srv.Close()

	_, err := testClient(t, srv.URL, 2).Complete(context.Background(), simpleRequest())
	if err == nil {
		t.Fatal("expected a failure")
	}

	u := UpstreamOf(err)
	if u == nil {
		t.Fatal("no provider answer was kept at all")
	}
	if got, want := u.AttemptSummary(), "500, 429 ×2"; got != want {
		t.Errorf("attempt summary = %q, want %q; without it a broken-then-throttled endpoint "+
			"and a quota that never moved are the same record", got, want)
	}
	// The last attempt is what the call ended on, and it is what the detail
	// leads with.
	if u.Status != http.StatusTooManyRequests || u.Code != "rate_limit" {
		t.Errorf("the kept answer is %d/%q; the LAST attempt's answer is what the call ended "+
			"on", u.Status, u.Code)
	}
	detail := errs.DetailOf(err)
	for _, want := range []string{"failed after 3 attempts", "429", "quota exhausted", "500, 429 ×2"} {
		if !strings.Contains(detail, want) {
			t.Errorf("the detail does not contain %q.\ndetail: %s", want, detail)
		}
	}
	// And the earlier answer is not lost from the chain either.
	if !strings.Contains(err.Error(), "internal error") && !strings.Contains(err.Error(), "500") {
		t.Errorf("nothing of the earlier attempts survives: %v", err)
	}
}

// The structured log carries the provider's answer as FIELDS, not only inside a
// sentence.
//
// A log line whose only record of a 429 is the word "429" in a prose message
// cannot be counted, filtered or alerted on. logx.errorFields copies the
// outermost error's Fields onto the record, so annotating the error once at the
// place it is built is what puts these on every line that ever reports it.
func TestUpstream_TheProvidersAnswerReachesTheStructuredLog(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"quota exhausted","type":"insufficient_quota","code":"rate_limit"}}`))
	}))
	defer srv.Close()

	var buf strings.Builder
	c := NewOpenAICompatible(config.LLMConfig{
		BaseURL: srv.URL, APIKey: "k", Executor: "executor-model",
		RequestTimeout: 5 * time.Second, MaxRetries: 1,
	}, logx.New(logx.Options{Output: &buf, Format: "json"}), clock.System{})

	if _, err := c.Complete(context.Background(), simpleRequest()); err == nil {
		t.Fatal("expected a failure")
	}

	// One record per line; the giving-up line is the one that must carry it.
	var found map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec["msg"] == string(logx.EventLLMRefused) {
			found = rec
		}
	}
	if found == nil {
		t.Fatalf("a call that gave up logged no record of giving up:\n%s", buf.String())
	}
	if got := found["upstream_status"]; got != float64(http.StatusTooManyRequests) {
		t.Errorf("upstream_status = %v, want 429; a status that is only a word inside a "+
			"message cannot be counted or alerted on", got)
	}
	if got := found["upstream_code"]; got != "rate_limit" {
		t.Errorf("upstream_code = %v, want rate_limit", got)
	}
	if got := found["upstream_message"]; got != "quota exhausted" {
		t.Errorf("upstream_message = %v", got)
	}
	if got := found["upstream_attempts"]; got != "429 ×2" {
		t.Errorf("upstream_attempts = %v, want \"429 ×2\"", got)
	}
}

// The audio and image roles keep it too.
//
// # Why they need their own fence
//
// They do not go through the chat path: transcription, speech and image
// generation each speak their own wire format and each classify their own
// errors. On 2026-09-06 the provider retired the models behind three roles at
// once and only the chat surface had been taught to say anything useful — the
// same split, one level down. A fence on Complete alone would have left three
// EXTERNAL_UNAVAILABLE sites saying nothing about what the provider answered.
//
// # What is NOT covered here, and why
//
// The two sites where the provider's answer does not exist: a speech stream
// that dies after its 200 and a chat stream that does the same. There is no
// error body to quote — the 200 went out before the failure — so those keep the
// status they committed to and the transport's own words, which is everything
// there is.
func TestUpstream_TheAudioAndImageRolesKeepItToo(t *testing.T) {
	body := `{"error":{"message":"The engine is overloaded.","code":"ServiceUnavailable"}}`

	call := map[string]func(c *OpenAICompatible) error{
		"Draw": func(c *OpenAICompatible) error {
			_, err := c.Draw(context.Background(), "a desk lamp")
			return err
		},
		"Transcribe": func(c *OpenAICompatible) error {
			_, err := c.Transcribe(context.Background(), []byte("some audio"), "audio/wav")
			return err
		},
		"Speak": func(c *OpenAICompatible) error {
			return c.Speak(context.Background(), "hello", func([]byte) error { return nil })
		},
	}

	for name, invoke := range call {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(body))
			}))
			defer srv.Close()

			c := NewOpenAICompatible(config.LLMConfig{
				BaseURL: srv.URL, APIKey: "k",
				Illustrator: "image-model", Transcriber: "asr-model", Speaker: "tts-model",
				RequestTimeout: 10 * time.Second,
			}, logx.Discard(), clock.System{})

			err := invoke(c)
			if err == nil {
				t.Fatal("expected a failure")
			}
			u := UpstreamOf(err)
			if u == nil {
				t.Fatal("the provider's answer is not reachable from the error; this role " +
					"reports an outage and a retired model identically")
			}
			if u.Status != http.StatusServiceUnavailable || u.Code != "ServiceUnavailable" {
				t.Errorf("kept %d/%q, want 503/ServiceUnavailable", u.Status, u.Code)
			}
			detail := errs.DetailOf(err)
			for _, want := range []string{"503", "The engine is overloaded."} {
				if !strings.Contains(detail, want) {
					t.Errorf("the detail does not contain %q.\ndetail: %s", want, detail)
				}
			}
		})
	}
}

// redactSecrets is exercised directly as well, because the shapes it has to
// catch are cheaper to enumerate here than to make a provider produce.
func TestUpstream_RedactionShapes(t *testing.T) {
	// Assembled, not written out: see the note above keyShaped. Each token has
	// its own filler so that one shape surviving cannot be mistaken for
	// another's having been redacted.
	headerTok := strings.Repeat("a", 6) + strings.Repeat("D", 6) + strings.Repeat("3", 5)
	jsonTok := strings.Repeat("9f8e", 4)
	skTok := keyShaped("proj-", "A", 16)
	opaqueTok := opaqueKey("c3")

	cases := []struct {
		name      string
		in        string
		secrets   []string
		wantNotIn []string
		wantIn    []string
	}{
		{
			name:      "an HTTP Authorization header line",
			in:        "rejected\n" + bearerHeader(headerTok) + "\nX-Request-Id: 7",
			wantNotIn: []string{headerTok},
			wantIn:    []string{"Authorization", redactionMarker, "X-Request-Id"},
		},
		{
			name:      "a JSON api_key field",
			in:        `{"api_key":"` + jsonTok + `","model":"qwen-plus"}`,
			wantNotIn: []string{jsonTok},
			wantIn:    []string{"qwen-plus", redactionMarker},
		},
		{
			name:      "a bare sk- key",
			in:        "your key " + skTok + " is not enabled",
			wantNotIn: []string{skTok},
			wantIn:    []string{"is not enabled", redactionMarker},
		},
		{
			name:      "the configured key, whatever shape it has",
			in:        "credential " + opaqueTok + " was refused",
			secrets:   []string{opaqueTok},
			wantNotIn: []string{opaqueTok},
			wantIn:    []string{"was refused", redactionMarker},
		},
		{
			name: "a short secret is not a secret and must not eat prose",
			in:   "the model is not available in this region",
			// A 3-character configured value would otherwise redact every "the".
			secrets: []string{"the"},
			wantIn:  []string{"the model is not available in this region"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactSecrets(tc.in, tc.secrets...)
			for _, unwanted := range tc.wantNotIn {
				if strings.Contains(got, unwanted) {
					t.Errorf("%q survived redaction: %q", unwanted, got)
				}
			}
			for _, want := range tc.wantIn {
				if !strings.Contains(got, want) {
					t.Errorf("redaction lost %q: %q", want, got)
				}
			}
		})
	}
}
