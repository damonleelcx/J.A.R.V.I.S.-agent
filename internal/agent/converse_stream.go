package agent

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// StreamEvent is one thing the workbench should act on.
type StreamEvent struct {
	// Kind is "speech", "detail", "prototype", "goal", "recalled", "claims",
	// "done" or "error".
	Kind string `json:"kind"`
	// Text carries speech or detail.
	Text string `json:"text,omitempty"`
	// Prototype and Goal carry the structured tail.
	Prototype *Prototype    `json:"prototype,omitempty"`
	Goal      *ProposedGoal `json:"goal,omitempty"`
	// Recalled lists figures the reply attributed to a published standard.
	// Emitted whether or not there is geometry: a standard quoted in prose is
	// exactly as unverifiable as one quoted in an assumption.
	Recalled []StandardsClaim `json:"recalled,omitempty"`
	// Claims is the epistemic ledger for the turn (PRD RSN-05).
	Claims []Claim `json:"claims,omitempty"`
	// Variant reports what happened to the geometry after the turn: kept as a
	// version somebody can come back to and compare (PRD VIS-04), or not kept
	// and why. Emitted by the HTTP layer rather than by the model loop, because
	// storing is not part of the conversation — see httpapi/converse.go.
	Variant *VariantSaved `json:"variant,omitempty"`
	// FirstTokenMS and TotalMS are measured, not targeted. PRD AUD-02 names
	// ≤700ms; this reports what actually happened so the claim is checkable.
	FirstTokenMS int64  `json:"first_token_ms,omitempty"`
	TotalMS      int64  `json:"total_ms,omitempty"`
	Model        string `json:"model,omitempty"`
	Tokens       int64  `json:"tokens,omitempty"`
	Error        string `json:"error,omitempty"`
}

// VariantSaved is the fate of a turn's geometry.
//
// Both outcomes are reported. A workbench that showed nothing when a save failed
// would leave somebody believing they could come back to a shape that was never
// written down, and they would find out at the moment they went looking for it.
type VariantSaved struct {
	VersionID string `json:"version_id,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
	Path      string `json:"path,omitempty"`
	Version   int    `json:"version,omitempty"`
	// Name and Generator are what the STORED row says, not what the client
	// believes. VIS-04 makes the generator one of six things a render must link
	// to, and a client assembling it from its own state gets it wrong in exactly
	// the case that matters: this event is emitted before `done`, so the browser
	// does not yet know which model answered and would fall back to "FORGE".
	Name      string `json:"name,omitempty"`
	Generator string `json:"generator,omitempty"`
	Units     string `json:"units,omitempty"`
	// UnitsNote explains an absent or unconvertible unit in one sentence, so the
	// rail does not compose its own wording for a state the server already has
	// a sentence for.
	UnitsNote string `json:"units_note,omitempty"`
	Parts     int    `json:"parts,omitempty"`
	// Assumptions is the COUNT. The list itself is already in the provenance
	// banner beside the render; repeating it in the rail would push the variant
	// list off the screen on the second proposal.
	Assumptions int `json:"assumptions"`
	// NotKept says why this geometry was not stored. Empty when it was.
	NotKept string `json:"not_kept,omitempty"`
}

// RespondStream produces one conversational turn, emitting the SPEECH as soon
// as it is complete rather than waiting for the whole object.
//
// # How the early speech is extracted
//
// The model is asked to put "speech" first in its JSON. As tokens arrive, the
// accumulated text is scanned for that one field being closed. The moment it is,
// the speech is emitted and the browser can start talking — typically seconds
// before the geometry finishes generating.
//
// This is deliberately a scan for ONE known field rather than an incremental
// JSON parser. A partial parser that tries to build the whole object as it
// arrives would have to decide what a half-finished parts array means, and the
// answer is "nothing" — geometry is applied only once the complete document
// parses. The scan buys the latency; the full parse keeps the correctness.
func (c *Conversation) RespondStream(
	ctx context.Context,
	projectID string,
	history []Turn,
	message string,
	workspaceNote string,
	emit func(StreamEvent) error,
) error {
	const op = "agent.Conversation.RespondStream"

	streamer, ok := c.client.(llm.Streamer)
	if !ok {
		// Fall back to the buffered path rather than failing. A client that
		// cannot stream still produces correct answers, just later.
		reply, err := c.Respond(ctx, projectID, history, message, workspaceNote)
		if err != nil {
			return err
		}
		if reply.Speech != "" {
			if err := emit(StreamEvent{Kind: "speech", Text: reply.Speech}); err != nil {
				return err
			}
		}
		if reply.Detail != "" {
			if err := emit(StreamEvent{Kind: "detail", Text: reply.Detail}); err != nil {
				return err
			}
		}
		if reply.Prototype != nil {
			if err := emit(StreamEvent{Kind: "prototype", Prototype: reply.Prototype}); err != nil {
				return err
			}
		}
		if reply.ProposedGoal != nil {
			if err := emit(StreamEvent{Kind: "goal", Goal: reply.ProposedGoal}); err != nil {
				return err
			}
		}
		if len(reply.Recalled) > 0 {
			if err := emit(StreamEvent{Kind: "recalled", Recalled: reply.Recalled}); err != nil {
				return err
			}
		}
		if len(reply.Claims) > 0 {
			if err := emit(StreamEvent{Kind: "claims", Claims: reply.Claims}); err != nil {
				return err
			}
		}
		return emit(StreamEvent{Kind: "done", TotalMS: reply.LatencyMS, Model: reply.Model,
			Tokens: reply.Usage.TotalTokens})
	}

	if strings.TrimSpace(message) == "" {
		return errs.New(op, errs.CodeValidationFailed).WithDetail("empty message")
	}

	messages := c.buildMessages(c.characters.For(ctx, projectID, c.char), history, message, workspaceNote)

	var accumulated strings.Builder
	speechSent := false
	var firstTokenMS int64
	started := false

	err := streamer.Stream(ctx, llm.Request{
		Role:      llm.RoleConverse,
		Messages:  messages,
		JSONMode:  true,
		MaxTokens: 6000,
	}, func(chunk llm.Chunk) error {
		if chunk.Delta != "" {
			if !started {
				started = true
				// Recorded at the first token, which is the number a listener
				// experiences — not the last one.
				firstTokenMS = elapsedMS(ctx)
			}
			accumulated.WriteString(chunk.Delta)

			if !speechSent {
				if speech, ok := extractCompletedStringField(accumulated.String(), "speech"); ok {
					speechSent = true
					if err := emit(StreamEvent{
						Kind: "speech", Text: speech, FirstTokenMS: firstTokenMS,
					}); err != nil {
						return err
					}
				}
			}
		}

		if !chunk.Done {
			return nil
		}

		// The tail. Parsed as a whole: geometry is applied only when the
		// complete document is valid, never from a partial one.
		var reply Reply
		if err := json.Unmarshal([]byte(extractJSON(accumulated.String())), &reply); err != nil {
			if !speechSent {
				// Nothing was said and nothing parsed. Speak the raw text rather
				// than leaving the person with silence.
				text := strings.TrimSpace(accumulated.String())
				if text == "" {
					return errs.Wrap(op, errs.CodeExternalProtocol, err).
						WithDetail("the stream produced neither usable JSON nor any text")
				}
				return emit(StreamEvent{Kind: "speech", Text: text, FirstTokenMS: firstTokenMS})
			}
			// Speech already went out; the structured tail did not parse. Say so
			// rather than silently dropping geometry the person was told about.
			return emit(StreamEvent{
				Kind:  "error",
				Error: "The spoken reply arrived, but the geometry that came with it could not be read and was not applied.",
			})
		}
		if err := reply.validate(); err != nil {
			return err
		}

		if !speechSent && reply.Speech != "" {
			if err := emit(StreamEvent{Kind: "speech", Text: reply.Speech, FirstTokenMS: firstTokenMS}); err != nil {
				return err
			}
		}
		if reply.Detail != "" {
			if err := emit(StreamEvent{Kind: "detail", Text: reply.Detail}); err != nil {
				return err
			}
		}
		if reply.Prototype != nil {
			if err := emit(StreamEvent{Kind: "prototype", Prototype: reply.Prototype}); err != nil {
				return err
			}
		}
		if reply.ProposedGoal != nil {
			if err := emit(StreamEvent{Kind: "goal", Goal: reply.ProposedGoal}); err != nil {
				return err
			}
		}
		// Last of the content events, so it is never dropped by an early return
		// above and the browser has the whole reply to attach it to.
		if len(reply.Recalled) > 0 {
			if err := emit(StreamEvent{Kind: "recalled", Recalled: reply.Recalled}); err != nil {
				return err
			}
		}
		if len(reply.Claims) > 0 {
			if err := emit(StreamEvent{Kind: "claims", Claims: reply.Claims}); err != nil {
				return err
			}
		}
		return emit(StreamEvent{
			Kind: "done", FirstTokenMS: firstTokenMS, TotalMS: elapsedMS(ctx),
			Model: chunk.Model, Tokens: chunk.Usage.TotalTokens,
		})
	})
	return err
}

// buildMessages assembles the request, shared by both paths.
func (c *Conversation) buildMessages(char persona.Character, history []Turn, message, workspaceNote string) []llm.Message {
	messages := []llm.Message{
		{Role: llm.System, Content: persona.SystemPrompt(char, converseFraming)},
	}
	const keep = 16
	if len(history) > keep {
		history = history[len(history)-keep:]
	}
	for _, t := range history {
		role := llm.User
		if t.Role == "forge" {
			role = llm.Assistant
		}
		messages = append(messages, llm.Message{Role: role, Content: t.Content})
	}
	user := message
	if workspaceNote != "" {
		user = "[What is on screen right now: " + workspaceNote + "]\n\n" + message
	}
	return append(messages, llm.Message{Role: llm.User, Content: user})
}

// extractCompletedStringField returns the value of a top-level JSON string field
// once it has been fully received.
//
// It looks for `"name"` followed by a colon and an opening quote, then walks to
// the closing quote while honouring backslash escapes. Returns false until the
// closing quote has actually arrived — emitting a half-finished sentence would
// be worse than waiting, because the listener cannot tell it was cut off.
func extractCompletedStringField(s, name string) (string, bool) {
	key := `"` + name + `"`
	i := strings.Index(s, key)
	if i < 0 {
		return "", false
	}
	rest := s[i+len(key):]

	// Skip whitespace and the colon.
	j := 0
	for j < len(rest) && (rest[j] == ' ' || rest[j] == '\t' || rest[j] == '\n' || rest[j] == '\r') {
		j++
	}
	if j >= len(rest) || rest[j] != ':' {
		return "", false
	}
	j++
	for j < len(rest) && (rest[j] == ' ' || rest[j] == '\t' || rest[j] == '\n' || rest[j] == '\r') {
		j++
	}
	if j >= len(rest) || rest[j] != '"' {
		return "", false
	}
	j++

	var out strings.Builder
	for j < len(rest) {
		ch := rest[j]
		if ch == '\\' {
			if j+1 >= len(rest) {
				return "", false // escape is still arriving
			}
			switch rest[j+1] {
			case 'n':
				out.WriteByte('\n')
			case 't':
				out.WriteByte('\t')
			case 'r':
				out.WriteByte('\r')
			case '"':
				out.WriteByte('"')
			case '\\':
				out.WriteByte('\\')
			case 'u':
				if j+5 >= len(rest) {
					return "", false
				}
				// Passed through: the browser is going to render it anyway, and
				// decoding surrogate pairs here to speak them is not worth the
				// failure modes.
				out.WriteString(rest[j : j+6])
				j += 4
			default:
				out.WriteByte(rest[j+1])
			}
			j += 2
			continue
		}
		if ch == '"' {
			return out.String(), true
		}
		out.WriteByte(ch)
		j++
	}
	return "", false // closing quote has not arrived yet
}
