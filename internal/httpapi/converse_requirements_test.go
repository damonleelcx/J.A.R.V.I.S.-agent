package httpapi

import (
	"context"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/claim"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
)

// Geometry generated from recorded requirements (PRD VIS-01).
//
// The rule this turns on: the client sends IDS, and the server reads the
// requirement's own words out of the project graph. A client able to send both
// could name requirement A and paste the words of B, and the variant's
// provenance would record a requirement the model never saw.

// The request carries ids and nothing else about the requirement.
//
// A structural fence rather than a behavioural one, and it earns its place: the
// moment somebody adds a `from_text` field for convenience, the provenance
// guarantee is gone and nothing else would notice.
func TestTheRequestCannotCarryRequirementText(t *testing.T) {
	for _, forbidden := range []string{"FromText", "Requirements", "RequirementText", "NodeText"} {
		if hasField(converseRequest{}, forbidden) {
			t.Errorf("converseRequest has a %s field. The server reads requirement text from the "+
				"graph; a client that could send it too could name one requirement and paste "+
				"another's words, and the variant would record a provenance that never happened",
				forbidden)
		}
	}
	if !hasField(converseRequest{}, "FromNodes") {
		t.Error("converseRequest cannot name the requirements a turn is built from")
	}
	if !strings.Contains(requirementBlockPrefix, "Recorded requirements") {
		t.Error("the injected block does not announce itself to the model")
	}
}

// hasField reports whether v has a field with this name.
func hasField(v any, name string) bool {
	t := reflect.TypeOf(v)
	for i := 0; i < t.NumField(); i++ {
		if t.Field(i).Name == name {
			return true
		}
	}
	return false
}

// The server reads the requirement's own words out of the graph.
//
// # Why this exists on top of the structural fence above
//
// That one asserts the request cannot carry requirement text. This asserts the
// server actually goes and gets it — a build where `from_nodes` is recorded as
// provenance and nothing is injected would pass the first test while making the
// provenance a lie: the variant would say it was built from a requirement the
// model never saw.
func TestRequirementTextIsReadFromTheGraphNotTheRequest(t *testing.T) {
	w := workspaceHarness(t)
	ctx := context.Background()

	req, err := w.svc.Add(ctx, workspace.NewNode{
		ProjectID: w.project, Kind: workspace.KindRequirement,
		Title: "Must bolt to a 40mm hole pattern",
		Body:  "Two M5 clearance holes, 40mm apart.",
		How:   claim.Observed, Source: "drawing SP-114", CreatedBy: w.owner.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	// A kind that is not buildable, to confirm resolution is by id rather than
	// by "everything in the project".
	other, err := w.svc.Add(ctx, workspace.NewNode{
		ProjectID: w.project, Kind: workspace.KindRequirement,
		Title: "Must survive a salt-spray test", How: claim.Observed, CreatedBy: w.owner.ID,
	})
	if err != nil {
		t.Fatal(err)
	}

	h := &ConverseHandlers{deps: w.h.deps, workspace: w.svc}
	r := httptest.NewRequest("POST", "/v1/converse", nil).WithContext(ctx)

	message, used := h.requirementsFor(r, w.project, []string{req.ID}, "Model the bracket to meet that.")

	if len(used) != 1 || used[0] != req.ID {
		t.Fatalf("resolved %v; only the requirement that was named and exists may be recorded", used)
	}
	for _, want := range []string{"Must bolt to a 40mm hole pattern", "Two M5 clearance holes",
		"Model the bracket to meet that."} {
		if !strings.Contains(message, want) {
			t.Errorf("the turn does not carry %q:\n%s", want, message)
		}
	}
	if strings.Contains(message, "salt-spray") {
		t.Error("a requirement nobody selected was written into the turn")
	}

	// An id that does not resolve is skipped, not fatal — the person is
	// mid-sentence, and a requirement deleted a moment ago must not cost them
	// the message they typed. What is recorded is what was actually used.
	message, used = h.requirementsFor(r, w.project, []string{"nod_gone"}, "Model it.")
	if len(used) != 0 {
		t.Errorf("an unresolved id was recorded as provenance: %v", used)
	}
	if message != "Model it." {
		t.Errorf("the message was altered by an id that resolved to nothing: %q", message)
	}

	// And a node in somebody else's project resolves to nothing here, because
	// the graph loaded is this project's.
	if _, used := h.requirementsFor(r, w.project, []string{other.ID}, "x"); len(used) != 1 {
		t.Error("a requirement in this project failed to resolve, so the check above proves nothing")
	}
}

// stubLLM records the messages a turn actually sent.
type stubLLM struct{ saw []llm.Message }

func (s *stubLLM) Complete(_ context.Context, req llm.Request) (*llm.Response, error) {
	s.saw = req.Messages
	return &llm.Response{
		Content:      `{"speech":"here","detail":"","prototype":null,"proposed_goal":null}`,
		FinishReason: "stop",
	}, nil
}
func (s *stubLLM) ModelFor(llm.Role) string { return "stub" }

// The HANDLER resolves requirements, not just the helper.
//
// # Why this exists on top of the test above
//
// That one calls requirementsFor directly, so it passes whether or not the
// endpoint ever calls it. A drill proved it: deleting the call from Converse
// left it green — the fourth time in this codebase that a fence has guarded a
// function instead of the behaviour, after SAF-02, RSN-02 and VIS-03's own
// tolerance rule.
//
// This drives POST /v1/converse and asserts what reached the model.
func TestTheEndpointWritesRequirementsIntoTheTurn(t *testing.T) {
	w := workspaceHarness(t)
	ctx := context.Background()

	req, err := w.svc.Add(ctx, workspace.NewNode{
		ProjectID: w.project, Kind: workspace.KindRequirement,
		Title: "Must bolt to a 40mm hole pattern",
		Body:  "Two M5 clearance holes, 40mm apart.",
		How:   claim.Observed, CreatedBy: w.owner.ID,
	})
	if err != nil {
		t.Fatal(err)
	}

	stub := &stubLLM{}
	deps := w.h.deps
	deps.LLM = stub
	h := NewConverseHandlers(deps)

	body := `{"message":"Model the bracket to meet that.","project_id":"` + w.project +
		`","from_nodes":["` + req.ID + `"]}`
	r := httptest.NewRequest("POST", "/v1/converse", strings.NewReader(body))
	r = r.WithContext(context.WithValue(ctx, ctxKeyUser, w.owner))
	rec := httptest.NewRecorder()
	h.Converse(rec, r)

	if len(stub.saw) == 0 {
		t.Fatalf("the turn never reached a model: %d %s", rec.Code, rec.Body.String())
	}
	last := stub.saw[len(stub.saw)-1].Content
	if !strings.Contains(last, "Two M5 clearance holes") {
		t.Fatalf("the endpoint did not write the requirement into the turn.\n"+
			"requirementsFor may be correct and simply not called — which makes the from_nodes "+
			"recorded against the variant a claim about a requirement the model never saw.\n"+
			"Message was:\n%s", last)
	}
	if !strings.Contains(last, "Model the bracket to meet that.") {
		t.Errorf("the person's own words were lost:\n%s", last)
	}
}
