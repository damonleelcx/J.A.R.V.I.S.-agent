package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The record, through the endpoint that writes it.
//
// The domain fences prove that a recorded turn comes back. They pass whether or
// not anything ever RECORDS one — and /v1/converse is the only thing that does,
// from inside a stream, at two different moments. That is the same shape as the
// defect already recorded in converse_requirements_test.go: a helper that is
// correct and simply not called.

// A turn is kept as it happens, and both halves come back.
func TestATurnIsKeptAndComesBack(t *testing.T) {
	w := workspaceHarness(t)
	ctx := context.Background()

	deps := w.h.deps
	deps.LLM = &stubLLM{}
	h := NewConverseHandlers(deps)

	body := `{"message":"How thick should a 24mm washer be?"}`
	r := httptest.NewRequest("POST", "/v1/converse", strings.NewReader(body))
	r = r.WithContext(context.WithValue(ctx, ctxKeyUser, w.owner))
	rec := httptest.NewRecorder()
	h.Converse(rec, r)

	convID := conversationIDFrom(t, rec.Body.String())

	read := NewConversationHandlers(w.h.deps)
	rr := httptest.NewRecorder()
	gr := httptest.NewRequest("GET", "/v1/conversations/"+convID, nil)
	gr.SetPathValue("id", convID)
	gr = gr.WithContext(context.WithValue(ctx, ctxKeyUser, w.owner))
	read.Get(rr, gr)

	if rr.Code != http.StatusOK {
		t.Fatalf("the conversation could not be read back: %d %s", rr.Code, rr.Body.String())
	}
	var got struct {
		Turns []struct {
			Role string `json:"role"`
			Text string `json:"text"`
		} `json:"turns"`
		Note string `json:"note"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Turns) != 2 {
		t.Fatalf("a turn is two halves — what was asked and what was answered — and %d came back: %s",
			len(got.Turns), rr.Body.String())
	}
	if got.Turns[0].Role != "human" || !strings.Contains(got.Turns[0].Text, "24mm washer") {
		t.Errorf("the person's own words are not first in the record: %+v", got.Turns[0])
	}
	if got.Turns[1].Role != "forge" || got.Turns[1].Text == "" {
		t.Errorf("FORGE's reply was not kept: %+v", got.Turns[1])
	}
	// A restored transcript looks exactly like a live one and is not: the
	// epistemic labels, the recalled standards and the render provenance are
	// derived as a reply lands and are not stored. Saying so is part of the
	// response, not a client's guess.
	if !strings.Contains(got.Note, "derived at the time") {
		t.Errorf("the response does not say what the record leaves out: %q", got.Note)
	}
}

// The id reaches the client before the reply does.
//
// # Why the order is the fence
//
// A turn can fail — the model times out, the provider refuses. If the id only
// arrived with a successful answer, a failed FIRST turn would leave the person
// with a conversation that exists in the database and that their browser cannot
// name, so the next reload would start a second one and the first would be
// unreachable forever.
func TestTheConversationIsNamedBeforeTheReply(t *testing.T) {
	w := workspaceHarness(t)

	deps := w.h.deps
	deps.LLM = &stubLLM{}
	h := NewConverseHandlers(deps)

	r := httptest.NewRequest("POST", "/v1/converse", strings.NewReader(`{"message":"hello"}`))
	r = r.WithContext(context.WithValue(context.Background(), ctxKeyUser, w.owner))
	rec := httptest.NewRecorder()
	h.Converse(rec, r)

	stream := rec.Body.String()
	named := strings.Index(stream, `"kind":"conversation"`)
	spoke := strings.Index(stream, `"kind":"speech"`)
	if named < 0 {
		t.Fatalf("the stream never named the conversation, so the client has no way to come "+
			"back to it:\n%s", stream)
	}
	if spoke >= 0 && named > spoke {
		t.Errorf("the conversation is named after the reply. A turn that fails would then leave " +
			"a record the browser cannot name, and the next reload would start a second one.")
	}
}

// Somebody else's conversation is not readable, through the endpoint.
func TestAnotherPersonsConversationIsNotReadable(t *testing.T) {
	w := workspaceHarness(t)
	ctx := context.Background()

	deps := w.h.deps
	deps.LLM = &stubLLM{}
	h := NewConverseHandlers(deps)
	r := httptest.NewRequest("POST", "/v1/converse", strings.NewReader(`{"message":"private"}`))
	r = r.WithContext(context.WithValue(ctx, ctxKeyUser, w.owner))
	rec := httptest.NewRecorder()
	h.Converse(rec, r)
	convID := conversationIDFrom(t, rec.Body.String())

	stranger := *w.owner
	stranger.ID = "usr_someone_else"

	read := NewConversationHandlers(w.h.deps)
	for _, tc := range []struct {
		name   string
		method string
		call   func(http.ResponseWriter, *http.Request)
	}{
		{"read", "GET", read.Get},
		{"delete", "DELETE", read.Forget},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, "/v1/conversations/"+convID, nil)
			req.SetPathValue("id", convID)
			req = req.WithContext(context.WithValue(ctx, ctxKeyUser, &stranger))
			tc.call(rr, req)
			if rr.Code != http.StatusNotFound {
				t.Fatalf("a stranger got %d from %s. It must be 404: any other answer tells them "+
					"the conversation exists, which is the fact they were probing for.",
					rr.Code, tc.method)
			}
		})
	}
}

func conversationIDFrom(t *testing.T, stream string) string {
	t.Helper()
	for _, line := range strings.Split(stream, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev struct {
			Kind         string `json:"kind"`
			Conversation *struct {
				ID      string `json:"id"`
				NotKept string `json:"not_kept"`
			} `json:"conversation"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			continue
		}
		if ev.Kind == "conversation" && ev.Conversation != nil {
			if ev.Conversation.NotKept != "" {
				t.Fatalf("the turn was not kept: %s", ev.Conversation.NotKept)
			}
			return ev.Conversation.ID
		}
	}
	t.Fatalf("no conversation event in the stream:\n%s", stream)
	return ""
}
