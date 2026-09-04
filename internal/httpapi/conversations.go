package httpapi

import (
	"net/http"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/conversation"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// What was said at the workbench, over HTTP (PRD RSN-07, AUD-07).
//
// # Why there are only two verbs
//
// Turns are WRITTEN by /v1/converse, at the moment both halves of a turn exist
// and are known to be real. A POST here would let a client file a record of a
// conversation nobody can check it had — putting words in FORGE's mouth in the
// permanent record, which is a worse version of the same reason RecordChange is
// not on the HTTP surface. So this endpoint reads and it deletes, and nothing
// else.
//
// # Why delete is here at all rather than being an operator's job
//
// PRD AUD-07 requires deletion of a session to be reachable at all times, and
// MEM-01 asks every layer to state its retention. This layer's retention is
// "until the person says otherwise" — which is only true if saying otherwise is
// a thing they can actually do.
type ConversationHandlers struct {
	deps Deps
	svc  *conversation.Service
}

// NewConversationHandlers wires the record's read side.
func NewConversationHandlers(d Deps) *ConversationHandlers {
	return &ConversationHandlers{deps: d, svc: conversation.NewService(d.Pool, d.Clock, d.Log)}
}

type turnDTO struct {
	Seq  int    `json:"seq"`
	Role string `json:"role"`
	Text string `json:"text"`
	// Detail travels separately from Text because the two are different halves
	// of a reply — one is spoken and one is read — and a client that concatenated
	// them would speak the screen's half aloud.
	Detail string `json:"detail,omitempty"`
	// Images is how many pictures were attached. The pictures themselves are not
	// kept, and saying "3" is the honest form of that: a restored turn that
	// showed nothing would read as a turn with no attachments.
	Images    int    `json:"images,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
	SaidAt    string `json:"said_at"`
}

// Get handles GET /v1/conversations/{id} — the record, in order.
func (h *ConversationHandlers) Get(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())

	turns, err := h.svc.History(r.Context(), r.PathValue("id"), user.ID)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	out := make([]turnDTO, 0, len(turns))
	for i := range turns {
		t := turns[i]
		out = append(out, turnDTO{
			Seq: t.Seq, Role: string(t.Role), Text: t.Text, Detail: t.Detail,
			Images: t.Images, ProjectID: t.ProjectID,
			SaidAt: t.SaidAt.UTC().Format(time.RFC3339),
		})
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"conversation_id": r.PathValue("id"),
		"turns":           out,
		// Said plainly, because a restored transcript looks exactly like a live
		// one and is not. The epistemic ledger, the recalled standards and the
		// provenance banner are DERIVED from a reply as it arrives; they are not
		// stored, so they do not come back. A client that showed a restored turn
		// as an ordinary one would be showing "FORGE made no claims here", which
		// is a different statement from "nobody kept them".
		"note": "What was said, and only that. The epistemic labels, recalled standards and " +
			"render provenance shown beside a live reply are derived at the time and are not " +
			"part of this record.",
	})
}

// Forget handles DELETE /v1/conversations/{id}.
func (h *ConversationHandlers) Forget(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())

	n, err := h.svc.Forget(r.Context(), r.PathValue("id"), user.ID)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	h.deps.Log.Info(r.Context(), logx.EventConversationForgotten,
		"conversation_id", r.PathValue("id"), "turns", n)
	WriteJSON(w, http.StatusOK, map[string]any{
		"conversation_id": r.PathValue("id"),
		// The count, because "deleted" and "there was nothing there" are
		// different answers to somebody who pressed the button expecting the
		// first one.
		"turns_deleted": n,
	})
}
