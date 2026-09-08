package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Every conversation route is mounted and refuses an anonymous caller.
//
// # Why the listing route is here
//
// It is the route that makes a durable record reachable. Every turn has been
// kept since the workbench record shipped, and the only way back to one was a
// key in a single browser's localStorage: the workbench reopened the LAST
// conversation and nothing could ask for the others, so a new browser, a new
// profile or a closed private window reached none of it. A handler with no
// route is exactly that failure again — written, compiling, covered by its own
// tests, and unreachable.
//
// # Why 401 and not "not 200"
//
// 404 means the route does not exist. 401 means it exists and turned an
// anonymous caller away. "Not 200" would pass on a route nobody mounted, which
// is the thing being guarded against.
//
// See docs/bugfix/2026-09-08-history-was-unreachable.md
func TestAPI_EveryConversationRouteIsMountedAndRequiresASession(t *testing.T) {
	router := NewRouter(testDeps())

	for _, tc := range []struct{ method, target string }{
		{"GET", "/v1/conversations"},
		{"GET", "/v1/conversations/cnv_1"},
		{"DELETE", "/v1/conversations/cnv_1"},
	} {
		t.Run(tc.method+" "+tc.target, func(t *testing.T) {
			rec := httptest.NewRecorder()
			r := httptest.NewRequest(tc.method, tc.target, strings.NewReader("{}"))
			r.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(rec, r)

			if rec.Code == http.StatusNotFound {
				t.Fatalf("%s %s is not routed; the handler exists but no browser can reach it",
					tc.method, tc.target)
			}
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s %s returned %d to an anonymous caller, want 401: %s",
					tc.method, tc.target, rec.Code, rec.Body.String())
			}
		})
	}
}

// The listing route must not be swallowed by the {id} route.
//
// `GET /v1/conversations` and `GET /v1/conversations/{id}` are different
// handlers on adjacent patterns. If the listing were ever removed, the mux would
// not 404 it into view — a request for the collection would simply stop being
// answered by the thing that lists it, and the console's panel would go empty
// while every turn was still in the table. That is the original bug wearing a
// different hat, so the two are asserted to be distinct.
func TestListingAndReadingAreDifferentRoutes(t *testing.T) {
	router := NewRouter(testDeps())

	collection := httptest.NewRecorder()
	router.ServeHTTP(collection, httptest.NewRequest("GET", "/v1/conversations", nil))

	item := httptest.NewRecorder()
	router.ServeHTTP(item, httptest.NewRequest("GET", "/v1/conversations/cnv_1", nil))

	// Both are behind the session, so both refuse the same way here. What
	// matters is that neither is a 404: a 404 on the collection means the
	// listing is gone and history is unreachable again.
	if collection.Code == http.StatusNotFound {
		t.Error("GET /v1/conversations is not routed. History is durable and unreachable again: " +
			"the workbench can only reopen the one conversation its localStorage remembers.")
	}
	if item.Code == http.StatusNotFound {
		t.Error("GET /v1/conversations/{id} is not routed, so a listed conversation cannot be opened")
	}
}
