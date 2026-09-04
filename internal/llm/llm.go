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
	"strings"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/text"
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
	// RoleConverse holds the workbench conversation.
	//
	// Separate from RoleExecutor because the two want opposite things. An
	// executor should think hard and may take a minute; a conversation is
	// someone waiting mid-sentence, and PRD AUD-02 asks for first audio inside
	// 700ms. Measured on this deployment's provider, the deep-reasoning model
	// took 19s to produce a structured reply and the fast conversational one
	// took 6.8s — neither meets the target, but routing conversation through the
	// reasoning model guarantees it never will.
	RoleConverse Role = "converse"
	// RoleTranscriber turns spoken audio into transcript text (PRD AUD-03).
	//
	// A role of its own rather than a reuse of RoleConverse: it is a different
	// model family entirely — speech recognition, not chat — and routing it
	// through the conversation model would silently produce nothing usable.
	RoleTranscriber Role = "transcriber"
	// RoleVision reads images: a sketch, a photograph of a part, a screenshot of
	// a drawing (PRD VIS-01).
	//
	// A role of its own rather than a flag on RoleConverse, because it is a
	// different model — the conversation model is chosen for latency and most
	// text models cannot see at all. A deployment that has not configured one
	// has no vision, and says so rather than sending an image to a model that
	// will describe its own confusion.
	RoleVision Role = "vision"
	// RoleSpeaker turns FORGE's words into audio for a room (PRD AUD-05).
	//
	// Its own role for the same reason as the transcriber: it is a different
	// model family, and routing speech through the conversation model would
	// produce a reply rather than a reading.
	RoleSpeaker Role = "speaker"
)

// AllRoles returns every role, for configuration and the coherence fence.
func AllRoles() []Role {
	return []Role{RolePlanner, RoleExecutor, RoleVerifier, RoleSummarizer, RoleConverse}
}

// Valid reports whether r is a recognised CHAT role.
//
// Both Complete and Stream gate on this, so a role missing here cannot make a
// request at all — which is how RoleVision first failed: it was added to the
// model map and left out of this switch, and every image turn was refused as an
// unknown role. Transcriber and Speaker are deliberately absent: they do not go
// through Complete or Stream, they have their own methods.
//
// RoleVision is here and NOT in AllRoles, which reads as "roles a deployment
// must configure". Vision is optional by design: no vision model means image
// input is unavailable and says so.
func (r Role) Valid() bool {
	switch r {
	case RolePlanner, RoleExecutor, RoleVerifier, RoleSummarizer, RoleConverse, RoleVision:
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
	// Images are data URIs attached to a user turn (PRD VIS-01): a sketch
	// somebody drew, a photograph of a part, a screenshot of a drawing.
	//
	// Marshalled into the provider's multi-part content array, and ONLY when
	// present — see MarshalJSON. Every existing request keeps its exact wire
	// shape, because a content array where a string used to be is the kind of
	// change that works against one provider and quietly fails against another.
	Images []string `json:"-"`
}

// contentPart is one element of the provider's multi-part content array.
type contentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

type imageURL struct {
	URL string `json:"url"`
}

// MarshalJSON emits the flat shape unless the turn carries images.
//
// The provider's OpenAI-compatible surface accepts either a string or an array
// for `content`, and this codebase already relies on the array form for audio
// (see transcribe.go). Switching shape only when there is a second part keeps
// every text-only request byte-identical to what shipped before.
func (m Message) MarshalJSON() ([]byte, error) {
	type flat Message // no MarshalJSON, so no recursion
	if len(m.Images) == 0 {
		return json.Marshal(flat(m))
	}
	parts := make([]contentPart, 0, len(m.Images)+1)
	if m.Content != "" {
		parts = append(parts, contentPart{Type: "text", Text: m.Content})
	}
	for _, img := range m.Images {
		parts = append(parts, contentPart{Type: "image_url", ImageURL: &imageURL{URL: img}})
	}
	return json.Marshal(struct {
		Role       MessageRole   `json:"role"`
		Content    []contentPart `json:"content"`
		ToolCalls  []ToolCall    `json:"tool_calls,omitempty"`
		ToolCallID string        `json:"tool_call_id,omitempty"`
		Name       string        `json:"name,omitempty"`
	}{m.Role, parts, m.ToolCalls, m.ToolCallID, m.Name})
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

// truncate shortens a provider's response before it is quoted into an error.
//
// # Two things changed here, and the second is deliberate
//
// It cut at n BYTES, landing inside a character for any body that is not ASCII —
// and a provider's error message is exactly the kind of text that is not. These
// strings end up in errs.Error details, which are marshalled into an HTTP
// response, so a broken sequence becomes a replacement character in an answer an
// operator is reading to work out what went wrong.
//
// The notice also changes unit: it said "(N bytes)" and now reports characters,
// because that is what the limit counts and one number describing the other was
// the confusion in the first place. A body that is not valid UTF-8 at all — a
// binary payload on a JSON path — is unharmed: each invalid byte counts as one
// and nothing new is broken.
func truncate(s string, n int) string { return text.Clip(s, n) }
