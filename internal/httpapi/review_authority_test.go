package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/access"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/identity"
)

// Recording a qualified-review authority over HTTP.
//
// # Why these are the strictest fences on any endpoint here
//
// This is the only endpoint in the product that WIDENS what may be done. Every
// other authorisation mistake leaks a read or permits an edit; a mistake here
// permits consequential engineering work in a licensed domain on the strength of
// a claim nothing verified.
//
// So three things are held: only an owner may write, the recorder is the
// authenticated user and can never be chosen by the caller, and the caveat
// travels on every response whether or not anything is recorded.

func reviewAuthReq(t *testing.T, h *wsHarness, method, projectID, body string,
	as *identity.User) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, "/v1/projects/"+projectID+"/review-authority", nil)
	} else {
		r = httptest.NewRequest(method, "/v1/projects/"+projectID+"/review-authority",
			strings.NewReader(body))
	}
	r.SetPathValue("id", projectID)
	r = r.WithContext(context.WithValue(context.Background(), ctxKeyUser, as))
	rec := httptest.NewRecorder()

	ra := NewReviewAuthorityHandlers(h.h.deps)
	switch method {
	case http.MethodGet:
		ra.Get(rec, r)
	case http.MethodPut:
		ra.Put(rec, r)
	case http.MethodDelete:
		ra.Delete(rec, r)
	}
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding %s: %v", rec.Body.String(), err)
	}
	return body
}

// civilProjectOwnedBy makes a project in a domain that HAS a raised ceiling.
func civilProjectOwnedBy(t *testing.T, h *wsHarness) string {
	t.Helper()
	ctx := context.Background()
	id, err := h.svc.EnsureProject(ctx, h.pool, "", h.owner.ID, "Bridge", "Civil engineering")
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// An owner records, and the ceiling in the response rises with it.
func TestReviewAuthorityHTTP_AnOwnerCanRecordAndClear(t *testing.T) {
	h := workspaceHarness(t)
	id := civilProjectOwnedBy(t, h)

	rec := reviewAuthReq(t, h, http.MethodGet, id, "", h.owner)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET returned %d: %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["ceiling"] != "r1" {
		t.Errorf("ceiling before recording = %v; expected r1", body["ceiling"])
	}
	if _, ok := body["authority"]; ok {
		t.Error("a project with nobody recorded reports an authority")
	}

	rec = reviewAuthReq(t, h, http.MethodPut, id,
		`{"holder":"R. Okonkwo","note":"CEng MICE 481920"}`, h.owner)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT returned %d: %s", rec.Code, rec.Body.String())
	}
	body = decodeBody(t, rec)
	if body["ceiling"] != "r2" {
		t.Errorf("ceiling after recording = %v; expected r2", body["ceiling"])
	}
	if body["ordinary_ceiling"] != "r1" {
		t.Errorf("ordinary_ceiling = %v; a client cannot say 'raised from' without it",
			body["ordinary_ceiling"])
	}
	a, _ := body["authority"].(map[string]any)
	if a == nil || a["holder"] != "R. Okonkwo" {
		t.Fatalf("the recorded authority is not in the response: %v", body)
	}
	if a["verified"] != false {
		t.Errorf("verified = %v; this build verifies nothing and must not imply it", a["verified"])
	}

	// Down again, gated the same way. A mechanism that raises a ceiling and
	// cannot lower it is one nobody should switch on.
	rec = reviewAuthReq(t, h, http.MethodDelete, id, "", h.owner)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE returned %d: %s", rec.Code, rec.Body.String())
	}
	if body := decodeBody(t, rec); body["ceiling"] != "r1" {
		t.Errorf("ceiling after clearing = %v; expected r1 again", body["ceiling"])
	}
}

// Nobody below owner may write, and a non-member may not even read.
//
// A maintainer decides individual approvals; recording an authority changes the
// ceiling for every piece of work in the project from then on, which is
// administration of the project rather than a decision inside it.
func TestReviewAuthorityHTTP_OnlyAnOwnerMayRecord(t *testing.T) {
	h := workspaceHarness(t)
	ctx := context.Background()
	id := civilProjectOwnedBy(t, h)

	for _, role := range []access.Role{access.RoleMaintainer, access.RoleContributor, access.RoleViewer} {
		if err := h.access.SetRole(ctx, access.Grant{
			ProjectID: id, UserID: h.other.ID, Role: role, By: h.owner.ID,
		}); err != nil {
			t.Fatalf("granting %s: %v", role, err)
		}
		rec := reviewAuthReq(t, h, http.MethodPut, id, `{"holder":"Someone Else"}`, h.other)
		if rec.Code != http.StatusForbidden {
			t.Errorf("a %s recorded an authority (status %d).\n"+
				"This is the only control in the product that widens what may be done; "+
				"deciding an approval is a maintainer's job and raising the ceiling for all "+
				"future work is not", role, rec.Code)
		}
		rec = reviewAuthReq(t, h, http.MethodDelete, id, "", h.other)
		if rec.Code != http.StatusForbidden {
			t.Errorf("a %s lowered the ceiling (status %d)", role, rec.Code)
		}
		// A member may still READ it: they are governed by the ceiling and need
		// to know what it rests on.
		if rec := reviewAuthReq(t, h, http.MethodGet, id, "", h.other); rec.Code != http.StatusOK {
			t.Errorf("a %s cannot read the authority governing their own work (%d)", role, rec.Code)
		}
	}
}

// The recorder is the authenticated user and can never be named by the caller.
//
// Attribution is the entire value of the record. A client that could choose who
// attested would let somebody record an authority in another person's name, and
// the ceiling would then rest on a statement its supposed author never made.
func TestReviewAuthorityHTTP_TheRecorderCannotBeChosenByTheCaller(t *testing.T) {
	h := workspaceHarness(t)
	id := civilProjectOwnedBy(t, h)

	// Every plausible spelling somebody would reach for is REFUSED outright —
	// DecodeJSON rejects unknown fields, which is stronger than ignoring them: a
	// client that thought it was choosing the attester is told it was not,
	// instead of being quietly overruled and believing it succeeded.
	for _, spelling := range []string{"recorded_by", "recordedBy", "as", "user_id", "actor"} {
		rec := reviewAuthReq(t, h, http.MethodPut, id,
			`{"holder":"R. Okonkwo","`+spelling+`":"`+h.other.ID+`"}`, h.owner)
		if rec.Code == http.StatusOK {
			t.Errorf("a caller named the attester with %q and the write succeeded", spelling)
		}
	}

	rec := reviewAuthReq(t, h, http.MethodPut, id, `{"holder":"R. Okonkwo"}`, h.owner)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT returned %d: %s", rec.Code, rec.Body.String())
	}
	a, _ := decodeBody(t, rec)["authority"].(map[string]any)
	if a == nil {
		t.Fatal("nothing was recorded")
	}
	if a["recorded_by"] != h.owner.ID {
		t.Errorf("recorded_by = %v; expected the AUTHENTICATED user %s.\n"+
			"A caller who can name the attester can put a claim in somebody else's mouth, "+
			"and the ceiling then rests on a statement its author never made",
			a["recorded_by"], h.owner.ID)
	}
	if !hasField(recordReviewAuthorityRequest{}, "Holder") {
		t.Error("the request cannot carry a holder")
	}
	for _, forbidden := range []string{"RecordedBy", "As", "UserID", "Actor"} {
		if hasField(recordReviewAuthorityRequest{}, forbidden) {
			t.Errorf("recordReviewAuthorityRequest has a %s field, so a caller can choose "+
				"who the record is attributed to", forbidden)
		}
	}
}

// The caveat is on every response, recorded or not.
//
// A client that only received it alongside a holder would have to remember to
// render it. One that always has it cannot show a holder without it by accident.
func TestReviewAuthorityHTTP_TheCaveatIsAlwaysPresent(t *testing.T) {
	h := workspaceHarness(t)
	id := civilProjectOwnedBy(t, h)

	for _, step := range []struct {
		what   string
		method string
		body   string
	}{
		{"before anything is recorded", http.MethodGet, ""},
		{"on the write that records one", http.MethodPut, `{"holder":"R. Okonkwo"}`},
		{"on the read afterwards", http.MethodGet, ""},
		{"on the write that clears it", http.MethodDelete, ""},
	} {
		rec := reviewAuthReq(t, h, step.method, id, step.body, h.owner)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", step.what, rec.Code, rec.Body.String())
		}
		caveat, _ := decodeBody(t, rec)["caveat"].(string)
		if !strings.Contains(caveat, "RECORDED, NOT VERIFIED") {
			t.Errorf("%s: the response carries no caveat, so a client can render a claim "+
				"as a credential without doing anything wrong: %q", step.what, caveat)
		}
	}
}

// An empty holder is refused rather than treated as a clear.
//
// DELETE is the way down. A PUT with an empty body is far more likely to be a
// client bug than an intention to lower a ceiling, and guessing wrong in that
// direction silently removes a control.
func TestReviewAuthorityHTTP_AnEmptyHolderIsRefusedNotTreatedAsAClear(t *testing.T) {
	h := workspaceHarness(t)
	id := civilProjectOwnedBy(t, h)

	if rec := reviewAuthReq(t, h, http.MethodPut, id,
		`{"holder":"R. Okonkwo"}`, h.owner); rec.Code != http.StatusOK {
		t.Fatalf("setup: %d %s", rec.Code, rec.Body.String())
	}
	rec := reviewAuthReq(t, h, http.MethodPut, id, `{"holder":"   "}`, h.owner)
	if rec.Code == http.StatusOK {
		t.Fatal("an empty holder was accepted; it must not silently clear the record")
	}
	// And the existing record survived the refusal.
	body := decodeBody(t, reviewAuthReq(t, h, http.MethodGet, id, "", h.owner))
	if _, ok := body["authority"]; !ok {
		t.Error("the refused write cleared the authority anyway")
	}
}
