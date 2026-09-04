package httpapi

import (
	"context"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/claim"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/conversation"
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

// prototypeLLM answers with geometry, so a turn actually SAVES something.
//
// stubLLM above returns prototype:null, which is right for the tests that only
// care what reached the model — and useless for the one below, where the whole
// question is what the save wrote into the graph.
type prototypeLLM struct{ saw []llm.Message }

func (s *prototypeLLM) Complete(_ context.Context, req llm.Request) (*llm.Response, error) {
	s.saw = req.Messages
	return &llm.Response{
		Content: `{"speech":"Here is a bracket.","detail":"","proposed_goal":null,` +
			`"prototype":{"name":"bracket","units":"mm",` +
			`"parts":[{"id":"plate","name":"plate","shape":"box",` +
			`"size":{"width":40,"height":5,"depth":40},` +
			`"position":[0,0,0],"rotation":[0,0,0]}],` +
			`"assumptions":["5mm thick, chosen not given"],` +
			`"not_verified":["nothing here has been analysed"]}}`,
		FinishReason: "stop",
		// VIS-04 refuses a variant that cannot name its generator, and
		// keepGeometry reports that refusal instead of saving. Without this the
		// test below would fail for a reason that has nothing to do with edges.
		Model: "stub-model",
	}, nil
}
func (s *prototypeLLM) ModelFor(llm.Role) string { return "stub-model" }

// Building from a requirement joins the geometry to it, through the endpoint.
//
// # Why this exists on top of the domain fences
//
// internal/domain/workspace/provenance_test.go proves that a change carrying
// DerivedFrom draws the edge. It passes whether or not anything ever SETS
// DerivedFrom — and the field is set in exactly one place, three layers up, from
// the ids this handler resolved. That is the same shape as the defect recorded
// above this file: "requirementsFor may be correct and simply not called".
//
// So this drives POST /v1/converse with from_nodes and then reads the project
// graph, which is what a person opening the Diagram panel does.
func TestBuildingFromARequirementJoinsTheGeometryToIt(t *testing.T) {
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

	deps := w.h.deps
	deps.LLM = &prototypeLLM{}
	h := NewConverseHandlers(deps)

	body := `{"message":"Model the bracket to meet that.","project_id":"` + w.project +
		`","from_nodes":["` + req.ID + `"]}`
	r := httptest.NewRequest("POST", "/v1/converse", strings.NewReader(body))
	r = r.WithContext(context.WithValue(ctx, ctxKeyUser, w.owner))
	rec := httptest.NewRecorder()
	h.Converse(rec, r)

	if !strings.Contains(rec.Body.String(), `"kind":"variant"`) {
		t.Fatalf("the turn saved no geometry, so there was nothing to join: %d %s",
			rec.Code, rec.Body.String())
	}

	g, err := w.svc.Load(ctx, w.project)
	if err != nil {
		t.Fatal(err)
	}
	var joined []string
	for _, e := range g.Edges {
		if e.ToID != req.ID {
			continue
		}
		joined = append(joined, string(e.Kind))
		if e.Kind == workspace.EdgeSatisfies {
			t.Error("the endpoint recorded `satisfies`. Nothing checked that this shape meets " +
				"the requirement; the system would be asserting an unverified claim on its own " +
				"behalf (PRD RSN-06, SAF-05).")
		}
	}
	if len(joined) == 0 {
		t.Fatalf("geometry was built FROM %q and the graph does not say so.\n"+
			"DerivedFrom is set in one place — the NewVariant this handler builds — and it may "+
			"be correct in the domain and simply never populated, which leaves the project graph "+
			"a column of requirements and a column of files with nothing between them.",
			req.Title)
	}
	if len(joined) != 1 || joined[0] != string(workspace.EdgeDerivesFrom) {
		t.Fatalf("expected one derives_from edge into the requirement, got %v", joined)
	}
}

// The request cannot carry the conversation's history.
//
// # What this closes
//
// The client used to send one and the server used it verbatim, so a caller could
// include an `assistant` turn saying whatever it liked and steer the next reply
// with a conversation that never happened. PRD SEC-04 treats documents, tool
// output and imported results as untrusted input; a transcript asserted by the
// caller is the same kind of thing, and it was the one place taken at face
// value.
//
// Structural, and it earns its place for the same reason the requirement-text
// fence above does: the moment somebody adds the field back for convenience the
// guarantee is gone, and a behavioural test would still pass for every client
// that happens not to lie.
func TestTheRequestCannotCarryItsOwnHistory(t *testing.T) {
	for _, forbidden := range []string{"History", "Turns", "Transcript", "Messages", "Context"} {
		if hasField(converseRequest{}, forbidden) {
			t.Errorf("converseRequest has a %s field. The history comes from the server's own "+
				"record; a client that could send one could put words in FORGE's mouth and "+
				"steer the next reply with a conversation that never happened (PRD SEC-04).",
				forbidden)
		}
	}
	if !hasField(converseRequest{}, "ConversationID") {
		t.Error("converseRequest cannot name the conversation it continues, so the server has " +
			"nothing to read a history from")
	}
}

// Every speaker the record can hold maps to a DISTINCT role the model sees.
//
// # Why this is not obvious
//
// The two vocabularies are nearly the same and not quite — the record says human
// and forge, the model loop says user and forge — and buildMessages maps
// anything that is not "forge" onto the user role. So getting it wrong does not
// fail: it silently reassigns every one of FORGE's own turns to the person, and
// the model reads back a transcript in which it never spoke. Nothing about the
// reply would look wrong.
func TestEveryRecordedSpeakerMapsToADistinctModelRole(t *testing.T) {
	roles := conversation.Roles()
	if len(roles) < 2 {
		t.Fatalf("the record reports %d speaker(s); this fence is looking at nothing", len(roles))
	}
	seen := map[string]conversation.Role{}
	for _, r := range roles {
		got := modelRole(r)
		if got == "" {
			t.Errorf("recorded speaker %q maps to no model role", r)
			continue
		}
		if other, clash := seen[got]; clash {
			t.Errorf("%q and %q both map to the model role %q. One of them is being read as the "+
				"other, and a transcript in which FORGE never spoke reads as a normal one.",
				r, other, got)
		}
		seen[got] = r
	}
	// The one that matters, named rather than inferred: FORGE's own turns must
	// arrive as FORGE's.
	if modelRole(conversation.RoleForge) != "forge" {
		t.Errorf("FORGE's recorded turns reach the model as %q", modelRole(conversation.RoleForge))
	}
}

// A recorded input is cut on a character boundary, and says how much it lost.
//
// # The defect this holds
//
// forLedger counted BYTES while the constant beside it said characters. A
// message in any script that is not ASCII was therefore cut at a third of the
// stated length, and cut wherever the 2000th byte happened to land — for UTF-8,
// usually the middle of a character. Nothing failed loudly: json.Marshal
// substitutes a replacement character for the broken sequence and Postgres
// stores it, so a dimension or a word ends its life as "�" in a row kept
// for provenance (PRD WRK-04).
//
// Fenced here as well as in platform/text because this is the caller whose data
// is a permanent record rather than a log line.
func TestARecordedInputIsCutOnACharacterBoundary(t *testing.T) {
	// Every rune is three bytes, so a byte-based cut lands inside one, and the
	// whole thing is well past the limit either way.
	said := strings.Repeat("壁厚二点五毫米。", 400)

	got := forLedger(said)

	if !utf8.ValidString(got) {
		t.Fatal("the stored input is not valid UTF-8: the cut landed inside a character.\n" +
			"json.Marshal will substitute a replacement character and the ledger will keep it, " +
			"so the record of what the geometry was asked for contains a symbol nobody typed.")
	}
	head := strings.SplitN(got, "…", 2)[0]
	if n := utf8.RuneCountInString(head); n != ledgerFieldLimit {
		t.Errorf("kept %d characters against a limit of %d — the limit is being read as bytes, "+
			"so anything that is not ASCII is cut at a third of the stated length",
			n, ledgerFieldLimit)
	}
	if !strings.Contains(got, "truncated") {
		t.Error("the input was shortened silently; the record no longer says it is partial")
	}
	if !strings.Contains(got, fmt.Sprintf("%d characters", utf8.RuneCountInString(said))) {
		t.Errorf("the notice counts in a different unit from the limit: %q",
			got[len(got)-80:])
	}
}

// Anything that fits is stored exactly as it was said.
func TestAnOrdinaryInputIsStoredUntouched(t *testing.T) {
	const said = "make the base plate 8mm thick"
	if got := forLedger(said); got != said {
		t.Errorf("a short message was altered on the way into the record: %q", got)
	}
}
