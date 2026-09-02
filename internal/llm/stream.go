package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Chunk is one piece of a streamed response.
type Chunk struct {
	// Delta is the text produced since the previous chunk.
	Delta string
	// Done is set on the final chunk, which also carries Usage.
	Done         bool
	FinishReason string
	Usage        Usage
	Model        string
}

// Streamer is implemented by clients that can stream.
//
// Separate from Client because streaming is not always the right call: a planner
// producing a task list has nothing useful to say until the list is complete,
// and streaming it only complicates the caller. Conversation is the opposite —
// the first sentence is the whole point of arriving early.
type Streamer interface {
	Stream(ctx context.Context, req Request, onChunk func(Chunk) error) error
}

// Stream performs a streaming completion.
//
// # Why this exists
//
// Measured on this deployment's provider, a conversational turn that produces
// geometry takes ~33 seconds end to end. PRD AUD-02 asks for first audio inside
// 700ms. Waiting for the whole object before speaking makes that impossible by
// construction — the reply cannot start until the last brace arrives.
//
// Streaming does not make the model faster. It makes the FIRST SENTENCE arrive
// early, which is the number a person actually experiences. The structured tail
// (geometry, assumptions, what is not verified) still lands whole, and is still
// only applied once it parses.
func (c *OpenAICompatible) Stream(ctx context.Context, req Request, onChunk func(Chunk) error) error {
	const op = "llm.OpenAICompatible.Stream"

	if !req.Role.Valid() {
		return errs.New(op, errs.CodeInvariantViolated).WithDetail("unknown model role %q", req.Role)
	}
	model := c.models[req.Role]
	if model == "" {
		return errs.New(op, errs.CodeConfigInvalid).
			WithDetail("no model configured for role %q", req.Role)
	}

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}

	body := map[string]any{
		"model":      model,
		"messages":   req.Messages,
		"max_tokens": maxTokens,
		"stream":     true,
		// Usage is not reported on a streamed response by default, and a call
		// that reports no tokens is a call the budget cannot bind.
		"stream_options": map[string]any{"include_usage": true},
	}
	if len(req.Tools) > 0 {
		body["tools"] = req.Tools
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.JSONMode {
		body["response_format"] = map[string]string{"type": "json_object"}
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return errs.Wrap(op, errs.CodeSerializationFail, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return errs.Wrap(op, errs.CodeInternal, err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return errs.Wrap(op, errs.CodeExternalUnavailable, err).
			WithDetail("cannot reach the model endpoint at %s", c.baseURL)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return c.classifyHTTPError(ctx, resp.StatusCode, raw, model, req.Role)
	}

	reader := bufio.NewReaderSize(resp.Body, 64<<10)
	var usage Usage
	var finish string
	sawContent := false

	for {
		line, readErr := reader.ReadString('\n')
		if len(line) > 0 {
			line = strings.TrimRight(line, "\r\n")
			if strings.HasPrefix(line, "data: ") {
				data := strings.TrimPrefix(line, "data: ")
				if data == "[DONE]" {
					break
				}
				var frame struct {
					Model   string `json:"model"`
					Choices []struct {
						Delta struct {
							Content string `json:"content"`
						} `json:"delta"`
						FinishReason *string `json:"finish_reason"`
					} `json:"choices"`
					Usage *struct {
						PromptTokens     int64 `json:"prompt_tokens"`
						CompletionTokens int64 `json:"completion_tokens"`
						TotalTokens      int64 `json:"total_tokens"`
					} `json:"usage"`
				}
				if err := json.Unmarshal([]byte(data), &frame); err != nil {
					// A frame we cannot parse is skipped rather than fatal: one
					// malformed keep-alive must not discard a reply that is
					// otherwise arriving correctly. Warned, never silent.
					c.log.Warn(ctx, logx.EventLLMStreamFrame,
						"model", model, "detail", "skipped an unparseable stream frame")
					continue
				}
				if frame.Usage != nil {
					usage = Usage{
						PromptTokens:     frame.Usage.PromptTokens,
						CompletionTokens: frame.Usage.CompletionTokens,
						TotalTokens:      frame.Usage.TotalTokens,
					}
				}
				for _, ch := range frame.Choices {
					if ch.FinishReason != nil {
						finish = *ch.FinishReason
					}
					if ch.Delta.Content != "" {
						sawContent = true
						if err := onChunk(Chunk{Delta: ch.Delta.Content, Model: frame.Model}); err != nil {
							return err
						}
					}
				}
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return errs.Wrap(op, errs.CodeExternalUnavailable, readErr).
				WithDetail("the stream ended unexpectedly")
		}
	}

	if !sawContent {
		return errs.New(op, errs.CodeExternalProtocol).
			WithDetail("the stream completed without producing any content")
	}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	if usage.TotalTokens == 0 {
		c.log.Warn(ctx, logx.EventLLMUsageMissing,
			"model", model, "role", string(req.Role),
			"detail", "the stream reported no token usage; the goal's budget cannot be enforced against this call")
	}
	if finish == "length" {
		c.log.Warn(ctx, logx.EventLLMTruncated, "model", model, "role", string(req.Role))
	}

	return onChunk(Chunk{Done: true, FinishReason: finish, Usage: usage, Model: model})
}

var _ Streamer = (*OpenAICompatible)(nil)
