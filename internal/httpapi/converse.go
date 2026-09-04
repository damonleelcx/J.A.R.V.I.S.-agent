package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/agent"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/access"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// ConverseHandlers serve the workbench conversation.
type ConverseHandlers struct {
	deps Deps
	conv *agent.Conversation
	// geo keeps the geometry a turn proposes. It lives HERE rather than behind a
	// POST endpoint because this is the only place that knows the prompt, the
	// model and the shape at the same moment. A client posting geometry would be
	// naming its own generator and its own inputs, which is a fabricated
	// provenance record — the same reason RecordChange is not on the HTTP
	// surface (see router.go).
	geo *geometry.Service
	// workspace reads recorded requirements a turn is based on (PRD VIS-01).
	// Optional: a deployment without it simply has no requirements to build
	// from, and a turn that names some gets an ordinary answer.
	workspace *workspace.Service
}

// NewConverseHandlers wires the conversation endpoint.
func NewConverseHandlers(d Deps) *ConverseHandlers {
	if d.LLM == nil {
		return &ConverseHandlers{deps: d}
	}
	return &ConverseHandlers{
		deps: d,
		conv: agent.NewConversation(d.LLM, persona.DefaultCharacter()).
			WithCharacters(agent.NewCharacterStore(d.Pool, d.Log)),
		geo:       geometry.NewService(d.Pool, d.Clock, d.Log),
		workspace: workspace.NewService(d.Pool, d.Clock, d.Log),
	}
}

type converseRequest struct {
	Message string       `json:"message"`
	History []agent.Turn `json:"history"`
	// ProjectID is where geometry proposed in this turn is kept (PRD VIS-04).
	// Empty on the first turn of a workbench session: the server makes a project
	// then and returns its id in the `variant` event, and the client sends it
	// back on every turn afterwards so a conversation accumulates ONE history of
	// variants rather than one project per turn.
	//
	// The id is checked against the caller's membership on every turn, so a
	// client naming somebody else's project is refused rather than trusted.
	ProjectID string `json:"project_id"`
	// OnScreen describes what the workspace is currently showing, so a phrase
	// like "make that taller" resolves against what the person is looking at
	// rather than against the transcript. PRD WRK-02: a spoken reference should
	// land on the thing being referred to.
	OnScreen string `json:"on_screen"`
	// Images are data URIs for a sketch or photograph attached to this turn
	// (PRD VIS-01). Bounded by the HTTP body limit like everything else, and
	// routed to the vision model — a deployment without one refuses rather than
	// asking a text model to look at a picture.
	Images []string `json:"images,omitempty"`
	// FromNodes names recorded requirements or constraints this turn is based on
	// (PRD VIS-01: geometry generated from requirements).
	//
	// Ids only. The server loads those nodes and writes their text into the
	// turn itself — it does not take the text from the client. A client that
	// could send both would be able to name requirement A and paste the words
	// of B, and the variant's provenance would say the geometry came from a
	// requirement the model never saw.
	FromNodes []string `json:"from_nodes,omitempty"`
}

// requirementBlockPrefix opens the block the server writes into a turn. It
// announces itself so the model can tell a recorded requirement from the
// person's own words, and so a reader of the transcript can too.
const requirementBlockPrefix = "[Recorded requirements this is to be built from:\n"

// requirementsFor reads the named requirements and writes them into the turn.
//
// Returns the message the model will see and the ids that were actually
// resolved. Membership is checked by the same authorisation the rest of this
// handler uses, so naming a node in somebody else's project resolves nothing.
func (h *ConverseHandlers) requirementsFor(r *http.Request, projectID string,
	ids []string, message string) (string, []string) {

	if len(ids) == 0 || projectID == "" || h.workspace == nil {
		return message, nil
	}
	g, err := h.workspace.Load(r.Context(), projectID)
	if err != nil {
		// Read failure loses the requirement block, not the turn. Logged rather
		// than swallowed: a workbench where "model this requirement" silently
		// becomes an ordinary message is one nobody can debug from the outside.
		h.deps.Log.WarnWith(r.Context(), logx.EventWorkspaceUnreadable, err, "project_id", projectID)
		return message, nil
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}

	var block strings.Builder
	var used []string
	for _, n := range g.Nodes {
		if !want[n.ID] {
			continue
		}
		if block.Len() == 0 {
			block.WriteString(requirementBlockPrefix)
		}
		fmt.Fprintf(&block, "- (%s) %s", n.Kind, n.Title)
		if n.Body != "" {
			fmt.Fprintf(&block, " — %s", n.Body)
		}
		block.WriteString("\n")
		used = append(used, n.ID)
	}
	if block.Len() == 0 {
		return message, nil
	}
	block.WriteString("]\n\n")
	return block.String() + message, used
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
	var pending *agent.Prototype
	var savedVariant string

	send := func(ev agent.StreamEvent) error {
		payload, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
			// The reader went away — a closed tab, a navigation. Returning the
			// error stops the model call rather than letting it run on spending
			// tokens nobody will read.
			return err
		}
		flusher.Flush()
		return nil
	}

	// PRD VIS-01. The requirement text is read from the graph HERE, server-side,
	// and prepended to the turn — so what the model sees is what the recorded
	// requirement says, and the ids written into the variant's inputs are ids
	// this server resolved rather than ones a client asserted.
	//
	// A node that cannot be resolved is skipped rather than failing the turn:
	// the person is mid-sentence, and refusing to answer because one selected
	// requirement was deleted a moment ago would lose the message they typed.
	// What was actually used is recorded, so the record never overstates.
	message, usedNodes := h.requirementsFor(r, req.ProjectID, req.FromNodes, req.Message)
	req.FromNodes = usedNodes

	emitErr := h.conv.RespondStream(ctx, req.ProjectID, req.History, message, req.OnScreen, req.Images,
		func(ev agent.StreamEvent) error {
			switch ev.Kind {
			case "speech":
				spoke = true
				firstTokenMS = ev.FirstTokenMS
			case "prototype":
				hadPrototype = true
				// Held rather than saved here: the generator is the model that
				// actually answered, and that is only known once the turn
				// finishes. Saving now would have to guess it from
				// configuration, and a variant whose generator is a guess is one
				// of VIS-04's six facts recorded wrongly.
				pending = ev.Prototype
			case "done":
				totalMS, tokens, model = ev.TotalMS, ev.Tokens, ev.Model
				if pending != nil {
					// Before `done`, so `done` stays the last event a client
					// sees and nothing has to listen past it.
					saved := h.keepGeometry(r, req, pending, model)
					savedVariant = saved.VersionID
					if err := send(agent.StreamEvent{Kind: "variant", Variant: saved}); err != nil {
						return err
					}
				}
			}
			return send(ev)
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
		"has_prototype", hadPrototype,
		"variant_id", savedVariant)
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

// keepGeometry stores a turn's geometry, and never fails the turn.
//
// # Why a failure here is reported and not raised
//
// The conversation is the main path and keeping a variant is beside it. A person
// mid-sentence must not be stopped because a database write failed, a project
// they named is not theirs, or the assembly could not be attributed — none of
// those make the shape on their screen less useful. So every outcome comes back
// as a sentence the workbench shows, and the turn completes either way.
//
// What it must NOT do is stay quiet. A workbench that showed nothing after a
// failed save leaves somebody believing they can come back to a shape that was
// never written down, and they find out when they go looking for it.
func (h *ConverseHandlers) keepGeometry(r *http.Request, req converseRequest, proto *agent.Prototype, model string) *agent.VariantSaved {
	projectID := req.ProjectID
	if h.geo == nil || h.deps.Pool == nil {
		return &agent.VariantSaved{NotKept: "This deployment has no database, so geometry is not kept between turns."}
	}
	user, _ := UserFrom(r.Context())

	// An existing project is checked against the caller's membership every turn.
	// content.write rather than read, because keeping a variant changes what the
	// project holds — a viewer sees variants and does not add to them.
	if projectID != "" {
		if err := h.deps.requirePermission(r, projectID, user.ID, access.PermContentWrite); err != nil {
			return &agent.VariantSaved{NotKept: "This geometry was not kept: " + userFacing(err)}
		}
	}
	if model == "" {
		// VIS-04 requires a render to name its generator, and the save would be
		// refused for exactly this. Reported here in the caller's own terms
		// rather than as a validation error from three layers down.
		return &agent.VariantSaved{NotKept: "This geometry was not kept: the reply did not report which " +
			"model produced it, and a variant that cannot name its generator is not worth keeping."}
	}

	// A short deadline of its own. The request's context is already close to its
	// budget by the time the turn finishes, and a slow write must not turn a
	// completed answer into a failed one.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()

	v, err := h.geo.Save(ctx, geometry.NewVariant{
		ProjectID:   projectID,
		InitiatorID: user.ID,
		// The workbench conversation, which is neither a person nor the goal
		// engine. See migration 0011 for why the vocabulary gained this actor
		// rather than reusing 'human' or 'system'.
		Agent:     workspace.AgentConverse,
		Generator: model,
		Document:  *proto,
		// What it was made from: the turn. Recorded server-side from what the
		// request actually carried, never from anything the client asserts about
		// its own provenance.
		Inputs: map[string]any{
			"source":  "workbench conversation",
			"message": forLedger(req.Message),
			// PRD VIS-01 and VIS-04: what this geometry was made FROM. Only the
			// nodes the server resolved appear here.
			"from_nodes":    req.FromNodes,
			"images":        len(req.Images),
			"on_screen":     forLedger(req.OnScreen),
			"history_turns": len(req.History),
			"model":         model,
		},
	})
	if err != nil {
		h.deps.Log.WarnWith(r.Context(), logx.EventGeometrySaved, err, "user_id", user.ID)
		return &agent.VariantSaved{NotKept: "This geometry was not kept: " + userFacing(err)}
	}
	return &agent.VariantSaved{
		VersionID: v.VersionID, ProjectID: v.ProjectID, Path: v.Path, Version: v.Version,
		Name: v.Name, Generator: v.Generator,
		Units: string(v.Units), UnitsNote: v.UnitsNote(),
		Parts: len(v.Document.Parts), Assumptions: len(v.Assumptions()),
	}
}

// ledgerFieldLimit bounds one recorded input.
//
// The inputs column answers "what was this made from", and the answer is a
// sentence somebody said. Two thousand characters is longer than any workbench
// utterance observed and short enough that a variant row stays a row. Truncation
// is MARKED rather than silent: a stored prompt that quietly loses its ending
// would misrepresent what the geometry was actually asked for, which is the one
// thing this field exists to record.
const ledgerFieldLimit = 2000

func forLedger(s string) string {
	if len(s) <= ledgerFieldLimit {
		return s
	}
	return s[:ledgerFieldLimit] + fmt.Sprintf("… [truncated; %d characters in the original]", len(s))
}
