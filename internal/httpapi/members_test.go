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

// Who is in a project, over HTTP.
//
// # What these hold, and what they deliberately do not
//
// The READ is gated here and nowhere else — access.Service.Members takes no
// caller and checks nothing, so this handler is the only thing between a
// membership list and anyone who asks. That is the fence that matters most.
//
// The WRITES are not gated in this layer at all: SetRole and Remove authorise
// themselves and refuse to strand a project by removing its last owner. The
// tests below assert those rules SURVIVE the HTTP path rather than re-testing
// them — a handler that quietly bypassed the service would pass a domain test
// suite untouched.

func membersReq(t *testing.T, h *wsHarness, method, projectID, userID, body string,
	as *identity.User) *httptest.ResponseRecorder {
	t.Helper()
	path := "/v1/projects/" + projectID + "/members"
	if userID != "" {
		path += "/" + userID
	}
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	r.SetPathValue("id", projectID)
	if userID != "" {
		r.SetPathValue("user_id", userID)
	}
	r = r.WithContext(context.WithValue(context.Background(), ctxKeyUser, as))
	rec := httptest.NewRecorder()

	mh := NewMemberHandlers(h.h.deps)
	switch method {
	case http.MethodGet:
		mh.List(rec, r)
	case http.MethodPost:
		mh.Add(rec, r)
	case http.MethodPut:
		mh.SetRole(rec, r)
	case http.MethodDelete:
		mh.Remove(rec, r)
	}
	return rec
}

func membersOf(t *testing.T, rec *httptest.ResponseRecorder) (map[string]any, []map[string]any) {
	t.Helper()
	var body struct {
		Members   []map[string]any `json:"members"`
		CanManage bool             `json:"can_manage"`
		Roles     []map[string]any `json:"roles"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding %s: %v", rec.Body.String(), err)
	}
	return map[string]any{"can_manage": body.CanManage, "roles": body.Roles}, body.Members
}

// A non-member cannot read the list.
//
// The load-bearing fence. Members() checks nothing, so if this handler stopped
// checking, a project's membership — who works on what, and their addresses when
// the caller can manage — would be readable by any authenticated account.
func TestMembersHTTP_ANonMemberCannotRead(t *testing.T) {
	h := workspaceHarness(t)

	rec := membersReq(t, h, http.MethodGet, h.project, "", "", h.other)
	if rec.Code == http.StatusOK {
		t.Fatalf("a non-member read the membership list.\n"+
			"access.Service.Members takes no caller and checks nothing, so this handler is "+
			"the only thing standing between that list and anyone who asks: %s",
			rec.Body.String())
	}
	// NOT FOUND rather than forbidden, and that is the stronger answer: a
	// non-member does not learn the project exists. access.Service.Require draws
	// the distinction deliberately — a MEMBER whose role is too low gets 403,
	// because they already know the project is there.
	if rec.Code != http.StatusNotFound {
		t.Errorf("a non-member was refused with %d. 403 would confirm the project exists "+
			"to somebody who has no business knowing that", rec.Code)
	}
}

// A member whose role is too low is told so, rather than told nothing exists.
//
// The other half of the distinction above. Answering 404 here would send
// somebody who legitimately works on the project hunting for a broken link,
// when what they need is "ask an owner to change your role".
func TestMembersHTTP_AMemberWithTooLowARoleIsToldWhy(t *testing.T) {
	h := workspaceHarness(t)
	ctx := context.Background()

	if err := h.access.SetRole(ctx, access.Grant{
		ProjectID: h.project, UserID: h.other.ID, Role: access.RoleViewer, By: h.owner.ID,
	}); err != nil {
		t.Fatal(err)
	}
	// A viewer CAN read the list — they are governed by it.
	if rec := membersReq(t, h, http.MethodGet, h.project, "", "", h.other); rec.Code != http.StatusOK {
		t.Fatalf("a viewer cannot see who else is on their own project (%d)", rec.Code)
	}
	// And is refused with a reason when they try to change it.
	rec := membersReq(t, h, http.MethodPut, h.project, h.other.ID, `{"role":"owner"}`, h.other)
	if rec.Code != http.StatusForbidden {
		t.Errorf("a viewer's write was refused with %d; a member who knows the project "+
			"exists should be told what they lack, not that it does not", rec.Code)
	}
}

// An owner sees the list with addresses; a lesser member sees names only.
func TestMembersHTTP_AddressesAreForThoseWhoCanAct(t *testing.T) {
	h := workspaceHarness(t)
	ctx := context.Background()

	if err := h.access.SetRole(ctx, access.Grant{
		ProjectID: h.project, UserID: h.other.ID, Role: access.RoleContributor, By: h.owner.ID,
	}); err != nil {
		t.Fatal(err)
	}

	meta, members := membersOf(t, membersReq(t, h, http.MethodGet, h.project, "", "", h.owner))
	if meta["can_manage"] != true {
		t.Error("the owner is not told they can manage membership")
	}
	if len(members) != 2 {
		t.Fatalf("expected 2 members, got %d: %v", len(members), members)
	}
	var sawEmail, sawYou bool
	for _, m := range members {
		if s, _ := m["email"].(string); s != "" {
			sawEmail = true
		}
		if m["is_you"] == true {
			sawYou = true
		}
		if s, _ := m["display_name"].(string); s == "" {
			t.Errorf("a member has no display name, so the list shows an opaque id: %v", m)
		}
	}
	if !sawEmail {
		t.Error("an owner sees no addresses, so they cannot tell two similar names apart")
	}
	if !sawYou {
		t.Error("nobody is marked as the caller, so a client must be told its own id separately")
	}

	meta, members = membersOf(t, membersReq(t, h, http.MethodGet, h.project, "", "", h.other))
	if meta["can_manage"] != false {
		t.Error("a contributor is told they can manage membership")
	}
	for _, m := range members {
		if s, _ := m["email"].(string); s != "" {
			t.Errorf("a contributor can read %v's address. A name is what a collaborator "+
				"needs; an address is the identifier somebody would act on, and only an "+
				"owner can act", m["display_name"])
		}
	}
}

// The last owner cannot be removed or demoted THROUGH THE ENDPOINT.
//
// The rule lives in access.Service and is tested there. This asserts the HTTP
// path goes through it: a handler that wrote the membership row itself would
// leave that suite green and strand a project with nobody who can administer it.
func TestMembersHTTP_TheLastOwnerSurvivesTheEndpoint(t *testing.T) {
	h := workspaceHarness(t)

	rec := membersReq(t, h, http.MethodDelete, h.project, h.owner.ID, "", h.owner)
	if rec.Code == http.StatusOK {
		t.Fatal("the last owner removed themselves over HTTP, leaving the project with " +
			"nobody who can administer it — not even to undo it")
	}
	rec = membersReq(t, h, http.MethodPut, h.project, h.owner.ID, `{"role":"viewer"}`, h.owner)
	if rec.Code == http.StatusOK {
		t.Fatal("the last owner demoted themselves over HTTP, which strands the project " +
			"exactly as removing them would")
	}
}

// Nobody below owner may change membership.
func TestMembersHTTP_OnlyAnOwnerMayChangeMembership(t *testing.T) {
	h := workspaceHarness(t)
	ctx := context.Background()

	if err := h.access.SetRole(ctx, access.Grant{
		ProjectID: h.project, UserID: h.other.ID, Role: access.RoleMaintainer, By: h.owner.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if rec := membersReq(t, h, http.MethodPut, h.project, h.other.ID,
		`{"role":"owner"}`, h.other); rec.Code == http.StatusOK {
		t.Error("a maintainer promoted themselves to owner")
	}
	if rec := membersReq(t, h, http.MethodDelete, h.project, h.owner.ID, "", h.other); rec.Code == http.StatusOK {
		t.Error("a maintainer removed the owner")
	}
	// And adding somebody is refused BEFORE the address is resolved, so this
	// endpoint cannot be used to ask whether an account exists.
	//
	// # Why the address must be one that does NOT exist
	//
	// The first version of this used an address that did, and it was VACUOUS:
	// with the checks in either order a maintainer got 403, because resolving a
	// real address succeeds and the permission check then refuses. The oracle
	// only shows itself on an address with no account — resolve-first answers
	// "no such account" (404) and check-first answers "not your business" (403).
	// Caught by mutation drill: moving the gate after the lookup left the old
	// assertion green.
	for _, addr := range []string{h.owner.Email, "definitely-nobody@example.com"} {
		rec := membersReq(t, h, http.MethodPost, h.project, "",
			`{"email":"`+addr+`","role":"viewer"}`, h.other)
		if rec.Code != http.StatusForbidden {
			t.Errorf("a maintainer's add of %q was refused with %d, not 403. If the address "+
				"is resolved before authority is checked, the difference between 403 and "+
				"404 tells anyone with any role whether an account exists", addr, rec.Code)
		}
	}
}

// The published role catalogue is the server's own.
//
// A client that kept its own list would eventually offer a role the server does
// not have, and the person choosing it would be refused for picking what they
// were shown.
func TestMembersHTTP_TheRoleCatalogueComesFromTheServer(t *testing.T) {
	h := workspaceHarness(t)
	meta, _ := membersOf(t, membersReq(t, h, http.MethodGet, h.project, "", "", h.owner))

	roles, _ := meta["roles"].([]map[string]any)
	if len(roles) != len(access.Roles()) {
		t.Fatalf("the catalogue publishes %d roles and the server has %d",
			len(roles), len(access.Roles()))
	}
	for _, r := range roles {
		if s, _ := r["does"].(string); strings.TrimSpace(s) == "" {
			t.Errorf("the %v role is published with no description, so a person choosing "+
				"between them is choosing between words", r["role"])
		}
	}
}

// The projects a person is in, and nothing else.
//
// # The fence that matters
//
// This endpoint takes no project id, so there is no permission check to get
// wrong — it is scoped by construction. Which makes the scoping itself the
// thing worth holding: if it ever stopped reading the CALLER's rows and started
// listing what exists, every project in the deployment would be disclosed to
// every account, and no authorisation test would fail because none is involved.
func TestMyProjects_ListsOnlyWhatTheCallerIsIn(t *testing.T) {
	h := workspaceHarness(t)
	ctx := context.Background()

	mine, err := h.svc.EnsureProject(ctx, h.pool, "", h.owner.ID, "Bridge study", "Civil engineering")
	if err != nil {
		t.Fatal(err)
	}
	// A project the OTHER user owns and the caller is not in.
	theirs, err := h.svc.EnsureProject(ctx, h.pool, "", h.other.ID, "Not yours", "Architecture")
	if err != nil {
		t.Fatal(err)
	}

	seen := myProjects(t, h, h.owner)
	if !seen[mine] {
		t.Errorf("a project the caller owns is missing from their own list: %v", seen)
	}
	if seen[theirs] {
		t.Fatal("a project the caller is NOT a member of appeared in their list.\n" +
			"This endpoint has no permission check because it is scoped by construction; " +
			"if the scoping goes, nothing else is standing there")
	}
	// h.project comes from the harness and the owner is a member of it.
	if !seen[h.project] {
		t.Errorf("the harness project is missing: %v", seen)
	}
}

// Each row says what the project is FOR, not only what it is called.
//
// A switcher is asked "which of these is the one I want", and four names answer
// that badly. The domain and the ceiling are what distinguish them.
func TestMyProjects_EachRowCarriesItsDomain(t *testing.T) {
	h := workspaceHarness(t)
	ctx := context.Background()

	id, err := h.svc.EnsureProject(ctx, h.pool, "", h.owner.ID, "Bridge study", "Civil engineering")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range myProjectRows(t, h, h.owner) {
		if row["id"] != id {
			continue
		}
		if row["industry"] != "Civil engineering" {
			t.Errorf("industry = %v", row["industry"])
		}
		if row["ceiling"] != "r1" {
			t.Errorf("ceiling = %v", row["ceiling"])
		}
		if row["role"] != "owner" {
			t.Errorf("role = %v; a person needs to know what they may do in each", row["role"])
		}
		if s, _ := row["name"].(string); s == "" {
			t.Error("the row has no name, so the list is a column of ids")
		}
		return
	}
	t.Fatalf("the project was not in the list at all")
}

// The order is stable between calls.
//
// Projects returns a MAP, and a map ranges differently every time. A list that
// reshuffled itself between two refreshes would make somebody believe something
// had changed when nothing had.
func TestMyProjects_TheOrderDoesNotMoveOnItsOwn(t *testing.T) {
	h := workspaceHarness(t)
	ctx := context.Background()

	for _, name := range []string{"Cee", "Aay", "Bee", "Dee"} {
		if _, err := h.svc.EnsureProject(ctx, h.pool, "", h.owner.ID, name, "Other"); err != nil {
			t.Fatal(err)
		}
	}
	var first []string
	for i := 0; i < 4; i++ {
		var order []string
		for _, row := range myProjectRows(t, h, h.owner) {
			order = append(order, row["id"].(string))
		}
		if i == 0 {
			first = order
			continue
		}
		if strings.Join(order, ",") != strings.Join(first, ",") {
			t.Fatalf("the order changed between calls:\n%v\n%v", first, order)
		}
	}
	if len(first) < 4 {
		t.Fatalf("expected at least four projects, got %d", len(first))
	}
}

func myProjectRows(t *testing.T, h *wsHarness, as *identity.User) []map[string]any {
	t.Helper()
	r := httptest.NewRequest("GET", "/v1/projects", nil)
	r = r.WithContext(context.WithValue(context.Background(), ctxKeyUser, as))
	rec := httptest.NewRecorder()
	NewMemberHandlers(h.h.deps).Mine(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/projects returned %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Projects []map[string]any `json:"projects"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Projects
}

func myProjects(t *testing.T, h *wsHarness, as *identity.User) map[string]bool {
	t.Helper()
	seen := map[string]bool{}
	for _, row := range myProjectRows(t, h, as) {
		seen[row["id"].(string)] = true
	}
	return seen
}
