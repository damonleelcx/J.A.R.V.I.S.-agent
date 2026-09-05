package agent

import (
	"context"
	"strings"
	"testing"

	domainpack "github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/pack"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// Images as an input to geometry (PRD VIS-01).

// visionStub records what it was asked and which model was asked for.
type visionStub struct {
	reply   string
	models  map[llm.Role]string
	sawRole llm.Role
	sawMsgs []llm.Message
}

func (v *visionStub) Complete(_ context.Context, req llm.Request) (*llm.Response, error) {
	v.sawRole, v.sawMsgs = req.Role, req.Messages
	return &llm.Response{Content: v.reply, FinishReason: "stop"}, nil
}

func (v *visionStub) ModelFor(r llm.Role) string { return v.models[r] }

const sawIt = `{"speech":"a bracket","detail":"","prototype":null,"proposed_goal":null}`

// A deployment with no vision model refuses the picture rather than sending it
// to a model that cannot see.
//
// The dangerous alternative is not an error. A text model handed an image does
// not fail — it describes what it imagines, in the same confident prose it uses
// for what it read, and nothing downstream can tell the two apart.
func TestWithoutAVisionModelAnImageIsRefusedNotGuessedAt(t *testing.T) {
	stub := &visionStub{reply: sawIt, models: map[llm.Role]string{llm.RoleConverse: "text-only"}}
	conv := NewConversation(stub, persona.DefaultCharacter())

	_, err := conv.Respond(context.Background(), "", nil, "model this",
		"", []string{"data:image/png;base64,AAAA"})
	if err == nil {
		t.Fatal("an image was accepted by a deployment with no vision model")
	}
	if errs.CodeOf(err) != errs.CodeConnectorUnavailable {
		t.Errorf("refused with %s; a missing capability is reported the way a missing CAD "+
			"kernel is, not as a validation error", errs.CodeOf(err))
	}
	if !strings.Contains(err.Error(), "FORGE_LLM_VISION_MODEL") {
		t.Errorf("the refusal does not say what would make it work: %v", err)
	}
	if stub.sawRole != "" {
		t.Error("the picture was sent anyway")
	}
}

// With one configured, the turn routes to the vision model and carries the
// image.
func TestAnImageTurnGoesToTheVisionModel(t *testing.T) {
	stub := &visionStub{reply: sawIt, models: map[llm.Role]string{
		llm.RoleConverse: "text-only", llm.RoleVision: "sees-things"}}
	conv := NewConversation(stub, persona.DefaultCharacter())

	if _, err := conv.Respond(context.Background(), "", nil, "model this",
		"", []string{"data:image/png;base64,AAAA"}); err != nil {
		t.Fatal(err)
	}
	if stub.sawRole != llm.RoleVision {
		t.Errorf("the turn went to %q; the conversation model is chosen for latency and "+
			"most text models cannot see at all", stub.sawRole)
	}
	last := stub.sawMsgs[len(stub.sawMsgs)-1]
	if len(last.Images) != 1 {
		t.Fatalf("the image did not reach the request: %+v", last)
	}

	// A turn with no images is unaffected: the guard must not fire on the
	// ordinary case, and must not route text to a vision model.
	plain := &visionStub{reply: sawIt, models: map[llm.Role]string{
		llm.RoleConverse: "text-only", llm.RoleVision: "sees-things"}}
	conv2 := NewConversation(plain, persona.DefaultCharacter())
	if _, err := conv2.Respond(context.Background(), "", nil, "make it taller", "", nil); err != nil {
		t.Fatal(err)
	}
	if plain.sawRole != llm.RoleConverse {
		t.Errorf("a text turn went to %q", plain.sawRole)
	}
}

// Both ways into a conversation build the same request.
//
// buildMessages has always said it was "shared by both paths" and Respond kept
// its own copy of it, so the streaming path and the buffered path assembled the
// request separately. An image added to one would simply not exist in the
// other — and the streaming path is the one the workbench uses.
func TestBothPathsBuildOneRequest(t *testing.T) {
	conv := NewConversation(&visionStub{reply: sawIt}, persona.DefaultCharacter())

	history := []Turn{{Role: "user", Content: "hello"}, {Role: "forge", Content: "hi"}}
	built := conv.buildMessages(persona.DefaultCharacter(), domainpack.Definition{}, history,
		"model this", "a bracket is on screen", []string{"data:image/png;base64,AAAA"})

	last := built[len(built)-1]
	if len(last.Images) != 1 {
		t.Fatal("the shared builder drops images")
	}
	if !strings.Contains(last.Content, "a bracket is on screen") {
		t.Error("the shared builder drops what is on screen")
	}
	// Images ride on the CURRENT turn only. A sketch from four turns ago is not
	// what "make that taller" refers to, and re-sending every image every turn
	// would grow the request without bound.
	for i, m := range built[:len(built)-1] {
		if len(m.Images) != 0 {
			t.Errorf("message %d carries an image from an earlier turn", i)
		}
	}
}
