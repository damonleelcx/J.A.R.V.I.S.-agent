// Package llm is FORGE's model client.
//
// It is provider-agnostic by interface and ships one driver, for
// OpenAI-compatible chat APIs — which is what Alibaba DashScope exposes for
// Qwen, and what most other hosted providers expose too.
//
// # Why roles rather than one model
//
// The engine addresses models by ROLE (planner, executor, verifier,
// summarizer), never by name. That indirection is not tidiness: PRD SAF-03
// requires a high-risk conclusion to be checked by a method independent of the
// path that produced it, and a model grading its own output is not independent.
// Roles let the verifier be routed to a different vendor family, and make that
// property visible in configuration rather than buried in call sites.
package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// Role names what a request is for.
type Role string

const (
	// RolePlanner decomposes goals and replans. The highest-capability role.
	RolePlanner Role = "planner"
	// RoleExecutor drives the tool loop for one task.
	RoleExecutor Role = "executor"
	// RoleVerifier independently checks claimed results. Routed to a different
	// model family from the executor (PRD SAF-03).
	RoleVerifier Role = "verifier"
	// RoleSummarizer compresses history. The cheapest role.
	RoleSummarizer Role = "summarizer"
)

// AllRoles returns every role, for configuration and the coherence fence.
func AllRoles() []Role { return []Role{RolePlanner, RoleExecutor, RoleVerifier, RoleSummarizer} }

// Valid reports whether r is a recognised role.
func (r Role) Valid() bool {
	switch r {
	case RolePlanner, RoleExecutor, RoleVerifier, RoleSummarizer:
		return true
	}
	return false
}

// MessageRole is a chat participant.
type MessageRole string

const (
	System    MessageRole = "system"
	User      MessageRole = "user"
	Assistant MessageRole = "assistant"
	Tool      MessageRole = "tool"
)

// Message is one turn.
type Message struct {
	Role    MessageRole `json:"role"`
	Content string      `json:"content"`
	// ToolCalls are present on an assistant turn that requested tools.
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// ToolCallID links a tool result back to the call that asked for it. The
	// provider rejects a tool message without it, and — worse — a mismatched one
	// silently associates a result with the wrong call.
	ToolCallID string `json:"tool_call_id,omitempty"`
	// Name identifies the tool on a tool-result turn.
	Name string `json:"name,omitempty"`
}

// ToolCall is a model's request to invoke a tool.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// FunctionCall carries the tool name and its JSON arguments.
type FunctionCall struct {
	Name string `json:"name"`
	// Arguments is a JSON *string*, not an object — that is the wire format, and
	// it is always parsed rather than string-matched, because escaping varies
	// between providers and between models from one provider.
	Arguments string `json:"arguments"`
}

// ParseArguments decodes a tool call's arguments.
func (f FunctionCall) ParseArguments(dst any) error {
	const op = "llm.FunctionCall.ParseArguments"

	if strings.TrimSpace(f.Arguments) == "" {
		// A tool with no required parameters legitimately produces "" or "{}".
		return nil
	}
	if err := json.Unmarshal([]byte(f.Arguments), dst); err != nil {
		return errs.Wrap(op, errs.CodeValidationFailed, err).
			WithDetail("tool %q was called with arguments that are not valid JSON: %s",
				f.Name, truncate(f.Arguments, 200))
	}
	return nil
}

// ToolDefinition describes a tool to the model.
type ToolDefinition struct {
	Type     string             `json:"type"`
	Function FunctionDefinition `json:"function"`
}

// FunctionDefinition is the tool's contract.
type FunctionDefinition struct {
	Name string `json:"name"`
	// Description is what the model reads to decide when to call this. It is the
	// single highest-leverage string in a tool definition — a vague one produces
	// a tool that is called at the wrong times and blamed for the result.
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// Request is one model call.
type Request struct {
	Role     Role
	Messages []Message
	Tools    []ToolDefinition
	// MaxTokens bounds the response. Left at zero, the driver applies a default
	// rather than letting the provider choose — an unbounded response on a
	// long-running agent is how a single call consumes a goal's whole budget.
	MaxTokens int
	// Temperature is a pointer so "unset" is distinguishable from "zero".
	Temperature *float64
	// JSONMode asks the provider for a JSON object. Availability varies, so the
	// caller must still validate the response rather than trusting the flag.
	JSONMode bool
}

// Response is one model reply.
type Response struct {
	Content   string
	ToolCalls []ToolCall
	// FinishReason is the provider's own token: "stop", "tool_calls", "length",
	// "content_filter". Preserved verbatim rather than normalised, because a
	// truncated response ("length") and a completed one ("stop") demand
	// different handling and a normalised enum tends to lose that.
	FinishReason string
	Usage        Usage
	Model        string
	// Latency is measured client-side, so it includes queueing and network.
	Latency time.Duration
}

// Truncated reports whether the model was cut off mid-answer.
//
// Checked explicitly at every call site: a truncated JSON plan is not a plan,
// and treating it as one produces a task list missing its tail with no error
// anywhere.
func (r *Response) Truncated() bool { return r.FinishReason == "length" }

// Usage is what a call consumed.
type Usage struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	// CachedTokens is reported by providers that support prompt caching. Zero
	// when unknown rather than assumed.
	CachedTokens int64
}

// Client is the model interface.
type Client interface {
	// Complete performs one request.
	Complete(ctx context.Context, req Request) (*Response, error)
	// ModelFor reports which model backs a role, for logs and the timeline.
	ModelFor(role Role) string
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("… (%d bytes)", len(s))
}
