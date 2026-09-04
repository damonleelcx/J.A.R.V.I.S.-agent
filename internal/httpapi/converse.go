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
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/conversation"
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
	// talk keeps what was said (PRD RSN-07). Here for the same reason geo is:
	// this is the only place that sees both halves of a turn at the moment they
	// happen, and a client posting its own transcript would be writing a record
	// of a conversation nobody can check it had.
	talk *conversation.Service
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
		talk:      conversation.NewService(d.Pool, d.Clock, d.Log),
	}
}

type converseRequest struct {
	Message string `json:"message"`
	// There is deliberately no History field.
	//
	// The client used to send one, and the server used it verbatim. That let a
	// caller put words in FORGE's mouth — an `assistant` turn saying whatever it
	// liked — and steer the next reply with a conversation that never happened.
	// PRD SEC-04 treats tool output, documents and imported results as untrusted
	// input; a transcript asserted by the caller is the same kind of thing and
	// was the one place it was taken at face value.
	//
	// The history now comes from the server's own record of this conversation,
	// the way the room path has always built its own (see roomHistory). The
	// field is REMOVED rather than ignored, so a client still sending one is
	// refused by the strict decoder instead of quietly having it dropped.
	// ConversationID names the record this turn joins (PRD RSN-07). Empty on the
	// first turn: the server mints one and returns it in the `conversation`
	// event, and the client sends it back afterwards — the same shape ProjectID
	// follows below, and for the same reason.
	//
	// An id that names somebody else's conversation, or one that does not exist,
	// is REFUSED rather than silently replaced with a fresh one. A client that
	// asked to continue a specific conversation and was quietly given a new one
	// would go on believing the old one was being appended to.
	ConversationID string `json:"conversation_id,omitempty"`
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

	// PRD RSN-07. The record is opened BEFORE the model is called, and the id
	// goes out first, so that a turn which then fails still leaves a
	// conversation the person can come back to. A record that only exists once
	// an answer arrives is a record that loses exactly the turns somebody would
	// most want to see again.
	//
	// A refusal here is fatal to the turn on purpose: the caller asked to
	// continue a NAMED conversation, and answering into a different one — or
	// into none — while saying nothing about it is the silent substitution this
	// whole endpoint's provenance rules exist to prevent.
	convID, err := h.talk.Resolve(ctx, req.ConversationID, user.ID)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}

	// BEFORE the turn is recorded, and the order is load-bearing: this turn's
	// message is passed to the model separately, so reading the record after
	// writing it would hand the model the same sentence twice — once as history
	// and once as the thing being answered.
	history, earlier := h.historyFor(ctx, convID, user.ID)
	if earlier > 0 {
		// Said to the model rather than left implicit. The window is a real
		// limit and a persisted conversation will meet it; a model given the
		// last sixteen turns with nothing to say otherwise will answer "we have
		// not discussed that" about something the record plainly contains, which
		// is a fabricated claim about the person's own history (PRD RSN-06).
		message = fmt.Sprintf("[Earlier in this conversation: %d turn(s) before these are not "+
			"shown to you. Do not treat what you cannot see as something that was never said.]\n\n%s",
			earlier, message)
	}

	kept := h.keepSaid(ctx, conversation.Said{
		ConversationID: convID, OwnerID: user.ID, ProjectID: req.ProjectID,
		Role: conversation.RoleHuman, Text: req.Message, Images: len(req.Images),
	})
	if err := send(agent.StreamEvent{Kind: "conversation", Conversation: kept}); err != nil {
		return
	}

	// What FORGE said, accumulated as it streams. Each `speech` event carries
	// the whole reply so far rather than a fragment — the workbench replaces the
	// bubble's text with it — so the last one is the complete utterance and
	// overwriting is correct. Details are appended, because more than one can
	// arrive and each is a separate paragraph of the same answer.
	var spokeText string
	var detailText []string

	emitErr := h.conv.RespondStream(ctx, req.ProjectID, history, message, req.OnScreen, req.Images,
		func(ev agent.StreamEvent) error {
			switch ev.Kind {
			case "speech":
				spoke = true
				spokeText = ev.Text
				firstTokenMS = ev.FirstTokenMS
			case "prototype":
				hadPrototype = true
				// Held rather than saved here: the generator is the model that
				// actually answered, and that is only known once the turn
				// finishes. Saving now would have to guess it from
				// configuration, and a variant whose generator is a guess is one
				// of VIS-04's six facts recorded wrongly.
				pending = ev.Prototype
			case "detail":
				if strings.TrimSpace(ev.Text) != "" {
					detailText = append(detailText, ev.Text)
				}
			case "done":
				totalMS, tokens, model = ev.TotalMS, ev.Tokens, ev.Model
				// Written before `done` reaches the client, for the same reason
				// the variant is: `done` stays the last event anybody has to
				// listen for.
				reply := h.keepSaid(ctx, conversation.Said{
					ConversationID: convID, OwnerID: user.ID, ProjectID: req.ProjectID,
					Role: conversation.RoleForge, Text: spokeText,
					Detail: strings.Join(detailText, "\n\n"),
				})
				if reply.NotKept != "" {
					if err := send(agent.StreamEvent{Kind: "conversation", Conversation: reply}); err != nil {
						return err
					}
				}
				if pending != nil {
					// Before `done`, so `done` stays the last event a client
					// sees and nothing has to listen past it.
					saved := h.keepGeometry(r, req, pending, model, len(history))
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
// keepGeometry stores what a turn proposed.
//
// historyTurns is passed rather than read off the request: it is one of WRK-04's
// facts about how this geometry came to be — how much of the conversation the
// model could see when it drew this — and since the history is now the server's
// own record, so is the count. A number taken from the request would be the
// client describing a context the server assembled.
func (h *ConverseHandlers) keepGeometry(r *http.Request, req converseRequest, proto *agent.Prototype, model string, historyTurns int) *agent.VariantSaved {
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
			"history_turns": historyTurns,
			"model":         model,
		},
		// PRD WRK-03. The same server-resolved ids as `from_nodes` above, so the
		// project graph records that this geometry was produced from those
		// requirements — a derives_from edge, which is provenance and not a
		// claim that the shape meets them. Until this, the graph held a column
		// of requirements and a column of files with nothing between them, and
		// "what was built from this requirement?" had no answer.
		DerivedFrom: req.FromNodes,
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

// historyFor reads what was said before this turn, and how much of it is not
// being shown.
//
// # Why a read failure is not fatal
//
// The person is mid-sentence. Refusing to answer because the record could not be
// read would lose the message they just typed to a database blip, and a turn
// with no history is a turn FORGE can still hold — it simply holds it without
// context, which is what the first turn of every conversation looks like.
//
// It is not silent either: from the outside, a model that has forgotten the last
// ten minutes and a model that is being asked its first question look identical,
// and only the log can tell them apart.
func (h *ConverseHandlers) historyFor(ctx context.Context, convID, userID string) ([]agent.Turn, int) {
	turns, total, err := h.talk.Recent(ctx, convID, userID, agent.HistoryWindow)
	if err != nil {
		h.deps.Log.WarnWith(ctx, logx.EventConversationUnreadable, err,
			"conversation_id", convID,
			"detail", "this turn was answered with no memory of the ones before it")
		return nil, 0
	}
	out := make([]agent.Turn, 0, len(turns))
	for i := range turns {
		out = append(out, agent.Turn{Role: modelRole(turns[i].Role), Content: agent.HistoryContent(turns[i].Text, turns[i].Detail)})
	}
	return out, total - len(turns)
}

// modelRole maps a recorded speaker onto the role the model loop understands.
//
// # Why this is a function with a fence rather than a comparison inline
//
// The two vocabularies are nearly the same and not quite: the record says human
// and forge, the loop says user and forge, and buildMessages maps ANYTHING that
// is not "forge" to the user role. So a wrong mapping does not fail — it
// silently reassigns every one of FORGE's own turns to the person, and the model
// then reads a transcript in which it never spoke.
func modelRole(r conversation.Role) string {
	if r == conversation.RoleForge {
		return "forge"
	}
	return "user"
}

// keepSaid records one half of a turn, and never fails the turn.
//
// # Why a failure here is reported rather than raised
//
// The person has their answer. Losing it because the transcript could not be
// written would trade the thing they asked for against the record of having
// asked — the wrong way round. But the failure cannot be silent either: a turn
// that was not kept is invisible until somebody reloads and finds a gap, which
// is the worst moment to learn it. So it travels on the stream the way a variant
// that could not be saved does, and it goes in the log with a reason.
func (h *ConverseHandlers) keepSaid(ctx context.Context, said conversation.Said) *agent.ConversationKept {
	out := &agent.ConversationKept{ID: said.ConversationID}
	if _, err := h.talk.Record(ctx, said); err != nil {
		h.deps.Log.WarnWith(ctx, logx.EventConversationNotKept, err,
			"conversation_id", said.ConversationID, "role", string(said.Role))
		out.NotKept = "This turn was not added to the record: " + userFacing(err)
	}
	return out
}
