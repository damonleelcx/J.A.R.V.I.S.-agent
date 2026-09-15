package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/conversation"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// A workbench turn whose reply could not be used, through the endpoint.
//
// # What went wrong (2026-09-15)
//
// A live turn failed after 2.7 s with "An external service replied in a shape
// this build cannot use". The record held only the person's half; the ~11,300
// tokens the reply cost were counted nowhere; the reply itself was gone.
// docs/bugfix/2026-09-15-a-failed-workbench-turn-left-no-trace.md
//
// Against Postgres and through Converse, because every claim is about the row
// the endpoint writes — and the record is written from inside a stream, where a
// helper that is correct and never called looks exactly like one that works.

const refusedReply = `{"proposed_goal":{"title":"Build a desk lamp","statement":"a small desk lamp"}}`

// refusedLLM answers with a reply that parses and says nothing, and streams it
// as the production client does, with usage on the final chunk.
type refusedLLM struct{ tokens int64 }

func (s *refusedLLM) Complete(context.Context, llm.Request) (*llm.Response, error) {
	return &llm.Response{Content: refusedReply, FinishReason: "stop", Model: "stub-model",
		Usage: llm.Usage{TotalTokens: s.tokens}}, nil
}

func (s *refusedLLM) ModelFor(r llm.Role) string {
	if r == llm.RoleVision {
		return ""
	}
	return "stub-model"
}

func (s *refusedLLM) Stream(_ context.Context, _ llm.Request, onChunk func(llm.Chunk) error) error {
	if err := onChunk(llm.Chunk{Delta: refusedReply, Model: "stub-model"}); err != nil {
		return err
	}
	return onChunk(llm.Chunk{Done: true, FinishReason: "stop", Model: "stub-model",
		Usage: llm.Usage{TotalTokens: s.tokens}})
}

func refusedTurn(t *testing.T, w *wsHarness, message string) string {
	t.Helper()
	deps := w.h.deps
	deps.LLM = &refusedLLM{tokens: 11275}
	h := NewConverseHandlers(deps)
	body, _ := json.Marshal(map[string]string{"message": message})
	r := httptest.NewRequest("POST", "/v1/converse", strings.NewReader(string(body)))
	r = r.WithContext(context.WithValue(context.Background(), ctxKeyUser, w.owner))
	rec := httptest.NewRecorder()
	h.Converse(rec, r)
	stream := rec.Body.String()
	if !strings.Contains(stream, `"kind":"error"`) {
		t.Fatalf("a reply with nothing to say or show did not fail the turn:\n%s", stream)
	}
	return stream
}

// The turn is kept: what the person was shown, the refused reply, and its cost.
func TestConverse_AReplyThatCouldNotBeUsedIsKeptWithWhatItCost(t *testing.T) {
	w := workspaceHarness(t)
	ctx := context.Background()

	convID := conversationIDFrom(t, refusedTurn(t, w, "Build a small desk lamp as a model, three steps at most."))

	turns, err := conversation.NewRepository().List(ctx, w.pool, convID, w.owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 2 {
		t.Fatalf("the record holds %d turn(s); a failed turn left only the person's half, and the "+
			"reply it paid for nowhere", len(turns))
	}
	forge := turns[1]
	if forge.Role != conversation.RoleForge || forge.Failure != string(errs.CodeExternalProtocol) {
		t.Errorf("FORGE's half is %s, failure %q; want forge, %s", forge.Role, forge.Failure, errs.CodeExternalProtocol)
	}
	if !strings.Contains(forge.UnusableReply, `"proposed_goal"`) {
		t.Errorf("the refused reply was not kept, so why the turn failed cannot be seen: %q", forge.UnusableReply)
	}
	if forge.Timing == nil || forge.Timing.Tokens == nil || *forge.Timing.Tokens != 11275 {
		t.Errorf("the failed turn's cost was not recorded (timing %+v); the reply cost 11275 tokens", forge.Timing)
	}
	if !strings.Contains(forge.Text, "cannot use") {
		t.Errorf("the failed turn's text is not what the person was shown: %q", forge.Text)
	}

	// And the Telemetry history lists it, marked, out of the medians.
	tel := NewTelemetryHandlers(w.h.deps)
	trec := httptest.NewRecorder()
	tel.Turns(trec, getAs(w.owner, "/v1/telemetry/turns"))
	var got struct {
		Turns []struct {
			Tokens *int64 `json:"tokens"`
			Failed bool   `json:"failed"`
		} `json:"turns"`
		MedianRoundTrip *int `json:"median_total_ms"`
	}
	if err := json.Unmarshal(trec.Body.Bytes(), &got); err != nil {
		t.Fatalf("%v: %s", err, trec.Body.String())
	}
	if len(got.Turns) != 1 || !got.Turns[0].Failed || got.Turns[0].Tokens == nil || *got.Turns[0].Tokens != 11275 {
		t.Errorf("telemetry does not list the failed turn with its cost: %s", trec.Body.String())
	}
}

// ‼️ A failed turn is not history. Its text is an error message and its kept
// reply is refused model output; either handed back as FORGE's words would be a
// turn that never happened.
func TestConverse_AFailedTurnIsNotGivenToTheModelAsHistory(t *testing.T) {
	w := workspaceHarness(t)

	convID := conversationIDFrom(t, refusedTurn(t, w, "The lamp is called Copper Heron."))
	saw, _ := converseWith(t, w, `{"message":"What is the lamp called?","conversation_id":"`+convID+`"}`)

	human := false
	for _, m := range saw {
		if m.Role == llm.System {
			// The contract names "proposed_goal" itself; history is everything after it.
			continue
		}
		if strings.Contains(m.Content, "proposed_goal") || strings.Contains(m.Content, "cannot use") {
			t.Errorf("the failed turn reached the model as history (%s): %.200s", m.Role, m.Content)
		}
		if m.Role == llm.Assistant {
			t.Errorf("an assistant turn reached the model, and the only FORGE turn so far failed: %.200s", m.Content)
		}
		if m.Role == llm.User && strings.Contains(m.Content, "Copper Heron") {
			human = true
		}
	}
	if !human {
		t.Error("what the person said in the failed turn is missing from the history; it was said")
	}
}
