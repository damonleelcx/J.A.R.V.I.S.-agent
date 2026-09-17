package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/conversation"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// "New conversation", through the endpoint the workbench sends it to (2026-09-17).
//
// # Why
//
// The workbench's control drops the conversation id and keeps the project, so
// the first turn after it arrives as {"conversation_id":"", "project_id":<same>}.
// workbench_new_conversation_test.go proves the page sends exactly that. This
// file proves what the SERVER does with it, which is the half that decides what
// the model sees: a pane that is empty while the request replays the old
// conversation would be the worst version of this feature, and nothing on screen
// would ever show it.
//
// ‼️ The history is read from the record by conversation id (historyFor →
// Repository.Recent). Anything that widened that read — to the owner's latest
// conversation, to the project, to "recent turns by this person" — would put the
// old conversation back into a new one while every id on the wire still looked
// right. So the assertions are on the MESSAGES the stub model received, not on
// ids.

// oldWords are said in the conversation that is then left. Distinctive enough
// that finding them anywhere in a later request can only mean they were carried.
var oldWords = []string{"Blue Heron", "Kestrel-7"}

// leaveAConversation holds a two-turn conversation (four recorded halves) in the
// harness's project and returns its id, then starts a new one the way the
// workbench does and returns the new id and what the model was given for it.
func leaveAConversation(t *testing.T, w *wsHarness) (oldID, newID string, saw []llm.Message) {
	t.Helper()
	_, stream := converseWith(t, w,
		`{"message":"the code name is Blue Heron","project_id":"`+w.project+`","conversation_id":""}`)
	oldID = conversationIDFrom(t, stream)
	converseWith(t, w,
		`{"message":"and the bracket part number is Kestrel-7","project_id":"`+w.project+`","conversation_id":"`+oldID+`"}`)

	// The request shape converseRequest builds after "New conversation": same
	// project, same description of the stage, no conversation id.
	saw, stream = converseWith(t, w,
		`{"message":"start over: a shelf bracket","project_id":"`+w.project+`","conversation_id":"",`+
			`"on_screen":"Bracket — 1 part(s): Plate [id: plate] (units: mm)"}`)
	newID = conversationIDFrom(t, stream)
	return oldID, newID, saw
}

// The first message of a new conversation reaches the model with none of the old
// conversation's history — no earlier human turn, no FORGE turn, and no
// "earlier turns are not shown" notice implying there were any.
func TestANewConversation_TheModelIsGivenNoneOfTheConversationThatWasLeft(t *testing.T) {
	w := workspaceHarness(t)
	oldID, newID, saw := leaveAConversation(t, w)

	if newID == "" || newID == oldID {
		t.Fatalf("the turn after \"New conversation\" was filed under %q, the conversation that was left (%q)",
			newID, oldID)
	}

	var said []llm.Message
	for _, m := range saw {
		for _, word := range oldWords {
			// System included: a history folded into the system prompt is still
			// history.
			if strings.Contains(m.Content, word) {
				t.Errorf("the model was given %q from the conversation that was left, in a %s message:\n%.400s",
					word, m.Role, m.Content)
			}
		}
		if strings.Contains(m.Content, "Earlier in this conversation") {
			t.Errorf("the model was told earlier turns exist in a conversation that has none:\n%.400s", m.Content)
		}
		if m.Role != llm.System {
			said = append(said, m)
		}
	}
	if len(said) != 1 || said[0].Role != llm.User || !strings.Contains(said[0].Content, "start over: a shelf bracket") {
		t.Fatalf("a new conversation's first turn should reach the model as exactly one user message — the one "+
			"being answered. It carried %d non-system message(s):\n%+v", len(said), said)
	}

	// And the new conversation's OWN history works: its second turn carries its
	// first, and still nothing from the one before it. An isolation that only
	// held because the first turn of anything has no history would pass above.
	saw2, _ := converseWith(t, w,
		`{"message":"make it 4mm thick","project_id":"`+w.project+`","conversation_id":"`+newID+`"}`)
	var carried bool
	for _, m := range saw2 {
		if m.Role == llm.User && strings.Contains(m.Content, "start over: a shelf bracket") {
			carried = true
		}
		for _, word := range oldWords {
			if strings.Contains(m.Content, word) {
				t.Errorf("the new conversation's second turn was given %q from the conversation that was left", word)
			}
		}
	}
	if !carried {
		t.Errorf("the new conversation's second turn was not given its first, so the new conversation has no memory "+
			"of itself:\n%+v", saw2)
	}
}

// The conversation that was left keeps every turn; the new one holds only its
// own, in the same project; both are listed; and a freshly minted conversation
// has no turns at all.
func TestANewConversation_TheOldOneKeepsItsTurnsAndTheNewOneStartsEmptyInTheSameProject(t *testing.T) {
	w := workspaceHarness(t)
	ctx := context.Background()

	// Before any turn, a new conversation is empty: the id the server mints
	// for "" names nothing, so there is no earlier turn it could read.
	talk := conversation.NewService(w.h.deps.Pool, w.h.deps.Clock, w.h.deps.Log)
	fresh, err := talk.Resolve(ctx, "", w.owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if turns, total, err := talk.Recent(ctx, fresh, w.owner.ID, 50); err != nil || total != 0 || len(turns) != 0 {
		t.Fatalf("a freshly minted conversation already has %d turn(s) (%d in all, err %v)", len(turns), total, err)
	}

	oldID, newID, _ := leaveAConversation(t, w)

	read := NewConversationHandlers(w.h.deps)
	get := func(id string) (int, []turnDTO) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/v1/conversations/"+id, nil)
		req.SetPathValue("id", id)
		read.Get(rr, req.WithContext(context.WithValue(ctx, ctxKeyUser, w.owner)))
		var body struct {
			Turns []turnDTO `json:"turns"`
		}
		_ = json.Unmarshal(rr.Body.Bytes(), &body)
		return rr.Code, body.Turns
	}

	code, old := get(oldID)
	if code != http.StatusOK || len(old) != 4 {
		t.Fatalf("the conversation that was left came back %d with %d turn(s); want all 4 — starting a new "+
			"conversation must not delete or truncate the old one", code, len(old))
	}
	if !strings.Contains(old[0].Text, "Blue Heron") || !strings.Contains(old[2].Text, "Kestrel-7") {
		t.Errorf("the old conversation's turns changed: %+v", old)
	}

	code, fresh2 := get(newID)
	if code != http.StatusOK || len(fresh2) != 2 {
		t.Fatalf("the new conversation came back %d with %d turn(s); want exactly its own 2", code, len(fresh2))
	}
	for _, turn := range fresh2 {
		for _, word := range oldWords {
			if strings.Contains(turn.Text+turn.Detail, word) {
				t.Errorf("the new conversation's record holds %q from the old one: %+v", word, turn)
			}
		}
		// A fresh conversation, not a fresh project: the design being worked on
		// is still where this conversation's work goes.
		if turn.ProjectID != w.project {
			t.Errorf("a turn of the new conversation is filed under project %q, not the design's project %q",
				turn.ProjectID, w.project)
		}
	}

	rr := httptest.NewRecorder()
	read.List(rr, httptest.NewRequest("GET", "/v1/conversations", nil).WithContext(
		context.WithValue(ctx, ctxKeyUser, w.owner)))
	var list struct {
		Conversations []conversationDTO `json:"conversations"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	listed := map[string]int{}
	for _, c := range list.Conversations {
		listed[c.ID] = c.Turns
	}
	if listed[oldID] != 4 || listed[newID] != 2 {
		t.Errorf("the console's listing (the way back to the old conversation) shows %v; want %s:4 and %s:2",
			listed, oldID, newID)
	}

	// The minted-but-unused id stays nothing: a conversation exists when it has a turn.
	if _, err := talk.History(ctx, fresh, w.owner.ID); errs.CodeOf(err) != errs.CodeNotFound {
		t.Errorf("an id minted for a new conversation that was never spoken in reads as %v; want NOT_FOUND", err)
	}
}
