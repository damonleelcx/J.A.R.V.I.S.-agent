package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// defaultMaxTokens bounds a response when the caller does not.
//
// Not left to the provider: an unbounded response on a long-running agent is
// how one call consumes a goal's entire token budget, and the failure looks like
// "the budget was wrong" rather than "one call ran away".
const defaultMaxTokens = 8192

// OpenAICompatible drives any chat API speaking the OpenAI wire format.
//
// Verified against Alibaba DashScope (Qwen). Note that DashScope's China and
// international endpoints issue SEPARATE keys — a key for one returns 401 on the
// other, and the error says "Incorrect API key provided", which reads like a bad
// key rather than a wrong host.
type OpenAICompatible struct {
	baseURL string
	apiKey  string
	models  map[Role]string
	client  *http.Client
	log     *logx.Logger
	clock   clock.Clock

	maxRetries int
	// voice is which synthesised voice FORGE speaks in (PRD AUD-05: "voice
	// identity and tone"). One value for the deployment, so the character sounds
	// the same in every room.
	voice string

	// warnedPricing remembers which models we have already complained about, so
	// an unknown price is reported once rather than on every call.
	warnedPricing sync.Map

	// thinkingField is how this endpoint is told not to deliberate, or "" when
	// it is not known to have a way (see deliberation.go). Resolved once at
	// construction because it is a property of the endpoint, not of a call.
	thinkingField string
	// warnedThinking makes the "this endpoint deliberates and cannot be told
	// not to" warning arrive once rather than every turn.
	warnedThinking sync.Once
}

// NewOpenAICompatible builds the driver from configuration.
func NewOpenAICompatible(cfg config.LLMConfig, log *logx.Logger, clk clock.Clock) *OpenAICompatible {
	base := strings.TrimRight(cfg.BaseURL, "/")
	field, _ := noDeliberation(base)
	return &OpenAICompatible{
		baseURL:       base,
		apiKey:        cfg.APIKey,
		thinkingField: field,
		models: map[Role]string{
			RoleVision:      cfg.Vision,
			RolePlanner:     cfg.Planner,
			RoleExecutor:    cfg.Executor,
			RoleVerifier:    cfg.Verifier,
			RoleSummarizer:  cfg.Summarizer,
			RoleConverse:    cfg.Converse,
			RoleTranscriber: cfg.Transcriber,
			RoleSpeaker:     cfg.Speaker,
			RoleIllustrator: cfg.Illustrator,
		},
		client:     &http.Client{Timeout: cfg.RequestTimeout},
		log:        log,
		clock:      clk,
		maxRetries: cfg.MaxRetries,
		voice:      cfg.Voice,
	}
}

// ModelFor reports which model backs a role.
func (c *OpenAICompatible) ModelFor(role Role) string { return c.models[role] }

// chatRequest is the wire shape of a completion request.
//
// Kept as a type although the request is now BUILT as a map: the body carries
// provider extensions (see deliberation.go) that no fixed struct can hold, and a
// struct that silently dropped one would be a latency defect nothing could see.
// This is the reader's view of that wire format, and the tests decode into it.
type chatRequest struct {
	Model       string           `json:"model"`
	Messages    []Message        `json:"messages"`
	Tools       []ToolDefinition `json:"tools,omitempty"`
	MaxTokens   int              `json:"max_tokens,omitempty"`
	Temperature *float64         `json:"temperature,omitempty"`
	ResponseFmt *responseFormat  `json:"response_format,omitempty"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type chatResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int     `json:"index"`
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens        int64 `json:"prompt_tokens"`
		CompletionTokens    int64 `json:"completion_tokens"`
		TotalTokens         int64 `json:"total_tokens"`
		PromptTokensDetails struct {
			CachedTokens int64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	} `json:"error"`
}

// Complete performs one request, retrying transient failures.
func (c *OpenAICompatible) Complete(ctx context.Context, req Request) (*Response, error) {
	const op = "llm.OpenAICompatible.Complete"

	if !req.Role.Valid() {
		return nil, errs.New(op, errs.CodeInvariantViolated).
			WithDetail("unknown model role %q", req.Role)
	}
	model := c.models[req.Role]
	if model == "" {
		return nil, errs.New(op, errs.CodeConfigInvalid).
			WithDetail("no model configured for role %q; set FORGE_LLM_%s_MODEL",
				req.Role, strings.ToUpper(string(req.Role)))
	}
	if len(req.Messages) == 0 {
		return nil, errs.New(op, errs.CodeInvariantViolated).
			WithDetail("a completion request must carry at least one message")
	}

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}

	body := map[string]any{
		"model": model, "messages": req.Messages, "max_tokens": maxTokens,
	}
	if len(req.Tools) > 0 {
		body["tools"] = req.Tools
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.JSONMode {
		body["response_format"] = responseFormat{Type: "json_object"}
	}
	// Both request paths pass through here, so a role that must not deliberate
	// cannot start doing it by being called on the other one.
	c.applyDeliberation(ctx, req.Role, body)
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeSerializationFail, err)
	}

	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			delay := backoff(attempt)
			c.log.Info(ctx, logx.EventLLMRetrying,
				"role", string(req.Role), "model", model,
				"attempt", attempt+1, "of", c.maxRetries+1,
				"delay_ms", delay.Milliseconds(), "reason", lastErr.Error())
			select {
			case <-ctx.Done():
				return nil, errs.Wrap(op, errs.CodeInternal, ctx.Err()).
					WithDetail("cancelled while backing off before retry %d", attempt+1)
			case <-time.After(delay):
			}
		}

		resp, err := c.attempt(ctx, model, req.Role, payload)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !errs.IsRetryable(err) {
			// A 400 will fail identically forever. Retrying it burns budget and
			// delays the real error reaching the operator.
			return nil, err
		}
	}
	return nil, errs.Wrap(op, errs.CodeExternalUnavailable, lastErr).
		WithDetail("model %q (role %s) failed after %d attempts", model, req.Role, c.maxRetries+1)
}

// attempt performs one HTTP round trip.
func (c *OpenAICompatible) attempt(ctx context.Context, model string, role Role, payload []byte) (*Response, error) {
	const op = "llm.OpenAICompatible.attempt"

	start := c.clock.Now()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeInternal, err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeExternalUnavailable, err).
			WithDetail("cannot reach the model endpoint at %s", c.baseURL)
	}
	defer httpResp.Body.Close()

	// Bounded read: a misbehaving endpoint must not be able to exhaust memory.
	raw, readErr := io.ReadAll(io.LimitReader(httpResp.Body, 16<<20))
	if readErr != nil {
		return nil, errs.Wrap(op, errs.CodeExternalUnavailable, readErr).
			WithDetail("reading the response body failed after %d status", httpResp.StatusCode)
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, c.classifyHTTPError(ctx, httpResp.StatusCode, raw, model, role)
	}

	var parsed chatResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, errs.Wrap(op, errs.CodeExternalProtocol, err).
			WithDetail("the endpoint returned 200 with a body this build cannot parse: %s", truncate(string(raw), 300))
	}
	// A 200 carrying an error object. Some providers do this; a caller that only
	// branches on status code would treat it as success and read empty content.
	if parsed.Error != nil {
		return nil, errs.New(op, errs.CodeExternalProtocol).
			WithDetail("the endpoint returned 200 with an error object: %s (%v)",
				parsed.Error.Message, parsed.Error.Code)
	}
	if len(parsed.Choices) == 0 {
		// HTTP 200, valid JSON, no content. Warned and refused rather than
		// returned as an empty answer, which downstream would treat as a model
		// that had nothing to say.
		err := errs.New(op, errs.CodeExternalProtocol).
			WithDetail("the endpoint returned 200 with zero choices")
		c.log.WarnWith(ctx, logx.EventLLMEmptyResponse, err, "model", model, "role", string(role))
		return nil, err
	}

	choice := parsed.Choices[0]
	usage := Usage{
		PromptTokens:     parsed.Usage.PromptTokens,
		CompletionTokens: parsed.Usage.CompletionTokens,
		TotalTokens:      parsed.Usage.TotalTokens,
		CachedTokens:     parsed.Usage.PromptTokensDetails.CachedTokens,
	}
	// Some providers omit the total. Deriving it is safe; assuming zero is not,
	// because zero-cost calls make every budget ceiling unreachable.
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	if usage.TotalTokens == 0 {
		c.log.Warn(ctx, logx.EventLLMUsageMissing,
			"model", model, "role", string(role),
			"detail", "the response reported no token usage; the goal's token budget cannot be enforced against this call")
	}

	resp := &Response{
		Content:      choice.Message.Content,
		ToolCalls:    choice.Message.ToolCalls,
		FinishReason: choice.FinishReason,
		Usage:        usage,
		Model:        parsed.Model,
		Latency:      c.clock.Now().Sub(start),
	}
	if resp.Model == "" {
		resp.Model = model
	}
	if resp.Truncated() {
		// Not an error here — the caller decides, because a truncated summary is
		// survivable and a truncated plan is not. But it must never pass
		// unnoticed.
		c.log.Warn(ctx, logx.EventLLMTruncated,
			"model", resp.Model, "role", string(role),
			"completion_tokens", usage.CompletionTokens,
			"detail", "the model was cut off at the token limit; the response is incomplete")
	}

	c.log.Debug(ctx, logx.EventLLMCompleted,
		"role", string(role), "model", resp.Model,
		"prompt_tokens", usage.PromptTokens, "completion_tokens", usage.CompletionTokens,
		"tool_calls", len(resp.ToolCalls), "finish_reason", resp.FinishReason,
		"latency_ms", resp.Latency.Milliseconds())

	return resp, nil
}

// classifyHTTPError maps a provider status onto a FORGE error code.
//
// The classification decides whether the engine retries, so getting it wrong
// either spins forever on a permanent failure or drops a recoverable one.
func (c *OpenAICompatible) classifyHTTPError(ctx context.Context, status int, body []byte, model string, role Role) error {
	const op = "llm.OpenAICompatible.classifyHTTPError"

	snippet := truncate(strings.TrimSpace(string(body)), 400)

	switch {
	case status == http.StatusTooManyRequests:
		return errs.New(op, errs.CodeRateLimited).
			WithDetail("the model endpoint rate-limited role %s (%s): %s", role, model, snippet)

	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		// Worth spelling out: the most common cause is not a bad key but a key
		// for the wrong regional endpoint, and the provider's message says
		// "Incorrect API key provided" either way.
		return errs.New(op, errs.CodeConfigInvalid).
			WithDetail("the model endpoint rejected our credentials (%d) at %s. "+
				"Check FORGE_LLM_API_KEY — and check the HOST: providers with regional "+
				"endpoints issue separate keys per region, and a key for the wrong one "+
				"fails with exactly this message. Response: %s",
				status, c.baseURL, snippet)

	case status == http.StatusNotFound:
		// The endpoint is ASKED what it serves, rather than the operator being
		// told to go and look.
		//
		// # Why this is worth an extra request on a failing path
		//
		// This is the error a provider returns when it RETIRES a model, and it
		// arrives on a deployment that was working yesterday and changed
		// nothing. Measured 2026-09-06: `qwen-plus` — the conversation model's
		// default since this product had one — stopped existing at
		// token-plan.cn-beijing.maas.aliyuncs.com, and the reply was
		// `Model not exist.` and nothing else. That reads as an outage or an
		// unpaid bill, and it was neither; the catalogue had moved to
		// qwen3.7/3.8. It cost this repository a day and an entry in the
		// implementation plan under "Operational — blocking".
		//
		// One GET on a path that has already failed permanently, and it turns
		// "not found" into the list the operator would otherwise spend an hour
		// finding.
		return errs.New(op, errs.CodeConfigInvalid).WithDetail("%s",
			fmt.Sprintf("model %q (role %s) was not found at %s. Response: %s",
				model, role, c.baseURL, snippet)+c.whatIsServed(ctx, status, role))

	case status >= 500:
		return errs.New(op, errs.CodeExternalUnavailable).
			WithDetail("the model endpoint returned %d for role %s: %s", status, role, snippet)

	default:
		// 4xx other than the above: a malformed request. Retrying is pointless.
		return errs.New(op, errs.CodeExternalProtocol).
			WithDetail("the model endpoint rejected the request with %d for role %s (%s): %s",
				status, role, model, snippet)
	}
}

// servedModels asks the endpoint what it will answer for.
//
// Bounded, and never on a success path: it exists to turn one specific
// permanent failure into an actionable one. A provider that does not implement
// /models returns an error here, which is reported rather than swallowed — the
// caller's message says the list could not be read, so nobody reads a short list
// as a complete one.
func (c *OpenAICompatible) servedModels(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, modelListTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the endpoint answered %d when asked for its model list", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("its model list is not in the OpenAI format this build can read")
	}
	ids := make([]string, 0, len(list.Data))
	for _, m := range list.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// modelListTimeout bounds the extra request above. Short: the turn has already
// failed, and the operator is waiting for a message rather than a model.
const modelListTimeout = 10 * time.Second

// whatIsServed is the sentence a 404 owes an operator, wherever the 404 arrives.
//
// # Why this is shared rather than only in classifyHTTPError
//
// The audio roles do not go through the chat path — transcription and speech
// each speak their own wire format and each classify their own errors. On
// 2026-09-06 the provider retired the models behind ALL THREE roles at once, and
// the two audio ones reported "the provider returned 404: Model not exist." with
// nothing to act on, while the chat one had just been taught to name the
// survivors. Two of the three surfaces would have been fixed.
//
// Returns "" when there is nothing useful to add, so a caller can append it
// unconditionally.
func (c *OpenAICompatible) whatIsServed(ctx context.Context, status int, role Role) string {
	if status != http.StatusNotFound {
		return ""
	}
	served, err := c.servedModels(ctx)
	if err != nil {
		return fmt.Sprintf(" This endpoint's model list could not be read either (%v), so check "+
			"the model id against the provider's console.", err)
	}
	if len(served) == 0 {
		return " This endpoint lists NO models at all, which usually means the key is scoped to " +
			"a different product or region than the host."
	}
	return fmt.Sprintf(" This endpoint currently serves: %s. Set FORGE_LLM_%s_MODEL to one of "+
		"those — a provider that retires a model answers exactly like this, and the deployment "+
		"that was working yesterday changed nothing.",
		strings.Join(served, ", "), strings.ToUpper(string(role)))
}

// backoff returns an exponential delay with jitter.
//
// Jitter matters more than the exponent here. Without it, N workers that hit the
// same rate limit retry in lockstep and hit it again together — the thundering
// herd that turns a brief limit into a sustained one.
func backoff(attempt int) time.Duration {
	base := time.Second * time.Duration(math.Pow(2, float64(attempt-1)))
	if base > 30*time.Second {
		base = 30 * time.Second
	}
	jitter := time.Duration(rand.Int63n(int64(base / 2)))
	return base/2 + jitter
}

var _ Client = (*OpenAICompatible)(nil)

var _ = fmt.Sprintf
