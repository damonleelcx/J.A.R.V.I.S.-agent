package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/agent"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// ConverseHandlers serve the workbench conversation.
type ConverseHandlers struct {
	deps Deps
	conv *agent.Conversation
}

// NewConverseHandlers wires the conversation endpoint.
func NewConverseHandlers(d Deps) *ConverseHandlers {
	if d.LLM == nil {
		return &ConverseHandlers{deps: d}
	}
	return &ConverseHandlers{
		deps: d,
		conv: agent.NewConversation(d.LLM, persona.DefaultCharacter()),
	}
}

type converseRequest struct {
	Message string       `json:"message"`
	History []agent.Turn `json:"history"`
	// OnScreen describes what the workspace is currently showing, so a phrase
	// like "make that taller" resolves against what the person is looking at
	// rather than against the transcript. PRD WRK-02: a spoken reference should
	// land on the thing being referred to.
	OnScreen string `json:"on_screen"`
}

// Converse handles POST /v1/converse as Server-Sent Events.
//
// # Why streamed, after initially not being
//
// The first version buffered the whole reply, on the reasoning that half a JSON
// object is not a partial answer. That is true of GEOMETRY and wrong about
// SPEECH. Measured end to end, a turn that produces a bracket took 33 seconds —
// and PRD AUD-02 asks for first audio inside 700ms. Waiting for the closing
// brace makes that impossible by construction.
//
// So speech is emitted the moment its field closes, usually seconds before the
// parts array finishes, and the structured tail is still applied only when the
// complete document parses. The latency buys the conversation; the whole-object
// parse keeps the geometry honest.
//
// Every event carries measured timings rather than the PRD's target, because a
// target asserted without measurement is a marketing claim.
func (h *ConverseHandlers) Converse(w http.ResponseWriter, r *http.Request) {
	if h.conv == nil {
		WriteError(w, r, h.deps.Log, errs.New("httpapi.Converse", errs.CodeConfigInvalid).
			WithDetail("no model is configured, so the workbench cannot hold a conversation. "+
				"Set FORGE_LLM_API_KEY and restart."))
		return
	}
	var req converseRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	user, _ := UserFrom(r.Context())

	start := h.deps.Clock.Now()

	// The deadline must be LONGER than the model client's own request timeout,
	// not shorter. An earlier version used a flat 90s while the client was
	// configured for 3m, so the handler always killed the call first — and
	// killed it mid-backoff, producing a context-deadline error that pointed at
	// the model rather than at the timeout hierarchy that caused it. Derived
	// from configuration so the two cannot drift apart again.
	budget := h.deps.Config.LLM.RequestTimeout + 15*time.Second
	ctx, cancel := context.WithTimeout(r.Context(), budget)
	defer cancel()
	ctx = agent.WithTurnStart(ctx, start)

	flusher, canFlush := w.(http.Flusher)
	if !canFlush {
		// Without flushing, "streaming" would buffer to the end and be slower
		// than the plain call for no benefit. Refused loudly rather than
		// silently degraded.
		WriteError(w, r, h.deps.Log, errs.New("httpapi.Converse", errs.CodeInternal).
			WithDetail("this server cannot flush a response, so it cannot stream"))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no") // tell an nginx in front not to buffer
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	var spoke bool
	var firstTokenMS, totalMS, tokens int64
	var model string
	var hadPrototype bool

	emitErr := h.conv.RespondStream(ctx, req.History, req.Message, req.OnScreen,
		func(ev agent.StreamEvent) error {
			switch ev.Kind {
			case "speech":
				spoke = true
				firstTokenMS = ev.FirstTokenMS
			case "prototype":
				hadPrototype = true
			case "done":
				totalMS, tokens, model = ev.TotalMS, ev.Tokens, ev.Model
			}
			payload, err := json.Marshal(ev)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
				// The reader went away — a closed tab, a navigation. Returning
				// the error stops the model call rather than letting it run on
				// spending tokens nobody will read.
				return err
			}
			flusher.Flush()
			return nil
		})

	if emitErr != nil {
		h.deps.Log.WarnWith(r.Context(), logx.EventConverseTurn, emitErr, "user_id", user.ID)
		// The status line is long gone, so the failure travels as an event.
		payload, _ := json.Marshal(agent.StreamEvent{
			Kind:  "error",
			Error: userFacing(emitErr),
		})
		fmt.Fprintf(w, "data: %s\n\n", payload)
		flusher.Flush()
		return
	}

	h.deps.Log.Info(r.Context(), logx.EventConverseTurn,
		"user_id", user.ID, "model", model,
		"first_token_ms", firstTokenMS,
		"total_ms", totalMS,
		"round_trip_ms", h.deps.Clock.Now().Sub(start).Milliseconds(),
		"tokens", tokens,
		"spoke", spoke,
		"has_prototype", hadPrototype)
}

// userFacing renders an error for a reader mid-conversation: what happened and
// what to do, without internal detail.
func userFacing(err error) string {
	code := errs.CodeOf(err)
	if d, ok := errs.Lookup(code); ok {
		return d.Cause + " " + d.Remedy
	}
	return "Something went wrong producing that reply. Try again."
}

// Models handles GET /v1/meta/models — which model backs which role.
//
// Exposed because PRD SAF-03's independence claim is only checkable if a user
// can see that the verifier is not the executor. A claim about independence that
// cannot be inspected is a claim, not a control.
func (h *ConverseHandlers) Models(w http.ResponseWriter, r *http.Request) {
	if h.deps.LLM == nil {
		WriteJSON(w, http.StatusOK, map[string]any{"configured": false})
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"configured": true,
		"roles": map[string]string{
			"planner":    h.deps.LLM.ModelFor(llm.RolePlanner),
			"executor":   h.deps.LLM.ModelFor(llm.RoleExecutor),
			"verifier":   h.deps.LLM.ModelFor(llm.RoleVerifier),
			"summarizer": h.deps.LLM.ModelFor(llm.RoleSummarizer),
			"converse":   h.deps.LLM.ModelFor(llm.RoleConverse),
		},
		"verifier_independent": h.deps.LLM.ModelFor(llm.RoleVerifier) != h.deps.LLM.ModelFor(llm.RoleExecutor),
	})
}
