package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// A reply that arrived and could not be used comes back with the reply and what
// it cost. docs/bugfix/2026-09-15-a-failed-workbench-turn-left-no-trace.md

// saysNothing parses and carries nothing to say or show: a proposed goal alone,
// which Reply.validate refuses whole. The live failure's reply was not kept, so
// this is the likeliest shape of it rather than a copy of it.
const saysNothing = `{"proposed_goal":{"title":"Build a desk lamp","statement":"a small desk lamp"}}`

type refusedStub struct{ tokens int64 }

func (s *refusedStub) Complete(context.Context, llm.Request) (*llm.Response, error) {
	return &llm.Response{Content: saysNothing, FinishReason: "stop", Model: "refused-stub",
		Usage: llm.Usage{TotalTokens: s.tokens}}, nil
}

func (s *refusedStub) ModelFor(r llm.Role) string {
	if r == llm.RoleVision {
		return ""
	}
	return "refused-stub"
}

// refusedStreamStub streams, as the production client does, and reports usage
// on the final chunk. A stub that cannot stream sends RespondStream down the
// buffered path, which is not the one a person uses.
type refusedStreamStub struct{ refusedStub }

func (s *refusedStreamStub) Stream(_ context.Context, _ llm.Request, onChunk func(llm.Chunk) error) error {
	if err := onChunk(llm.Chunk{Delta: saysNothing, Model: "refused-stub"}); err != nil {
		return err
	}
	return onChunk(llm.Chunk{Done: true, FinishReason: "stop", Model: "refused-stub",
		Usage: llm.Usage{TotalTokens: s.tokens}})
}

func checkUnusable(t *testing.T, err error, tokens int64) {
	t.Helper()
	if err == nil {
		t.Fatal("a reply with nothing to say or show was accepted")
	}
	var refused *UnusableReply
	if !errors.As(err, &refused) {
		t.Fatalf("the refusal carries neither the reply nor its cost, so the caller can keep "+
			"neither: %v", err)
	}
	if !strings.Contains(refused.Raw, `"proposed_goal"`) {
		t.Errorf("the refused reply was not kept: %q", refused.Raw)
	}
	if refused.Tokens != tokens || refused.Model != "refused-stub" {
		t.Errorf("the refusal reports %d tokens from %q; the call cost %d from refused-stub",
			refused.Tokens, refused.Model, tokens)
	}
	// The code still reaches the reader, or the person is told "Something went
	// wrong" instead of what did.
	if errs.CodeOf(err) != errs.CodeExternalProtocol {
		t.Errorf("the refusal's code is %s, want %s", errs.CodeOf(err), errs.CodeExternalProtocol)
	}
}

func TestRespondStream_AReplyThatCouldNotBeUsedComesBackWithTheReplyAndItsCost(t *testing.T) {
	c := &Conversation{client: &refusedStreamStub{refusedStub{tokens: 11275}}}
	err := c.RespondStream(context.Background(), "proj", nil, "a desk lamp, three steps at most", "", nil, nil,
		func(StreamEvent) error { return nil })
	checkUnusable(t, err, 11275)
}

func TestRespond_AReplyThatCouldNotBeUsedComesBackWithTheReplyAndItsCost(t *testing.T) {
	c := &Conversation{client: &refusedStub{tokens: 900}}
	_, err := c.Respond(context.Background(), "proj", nil, "a desk lamp", "", nil, nil)
	checkUnusable(t, err, 900)
}

// Kept bounded, and the cut is marked.
func TestUnusable_AReplyIsKeptBoundedAndSaysItWasCut(t *testing.T) {
	err := unusable(errs.New("t", errs.CodeExternalProtocol), strings.Repeat("x", UnusableReplyLimit*3), "m", llm.Usage{})
	var refused *UnusableReply
	if !errors.As(err, &refused) {
		t.Fatal("not an UnusableReply")
	}
	if n := len([]rune(refused.Raw)); n > UnusableReplyLimit+100 || !strings.Contains(refused.Raw, "truncated") {
		t.Errorf("a %d-character reply was kept as %d characters without saying so", UnusableReplyLimit*3, n)
	}
}
