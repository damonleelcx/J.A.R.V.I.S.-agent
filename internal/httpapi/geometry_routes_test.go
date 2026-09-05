package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Every geometry route is mounted and refuses an anonymous caller.
//
// # Why 401 and not "not 200"
//
// The two codes are what make this precise. 404 means the route does not exist —
// the handler is written, compiles, is covered by its own tests, and no browser
// can reach it. 401 means it exists and turned an anonymous caller away.
// Asserting merely "not 200" would pass on a route nobody mounted, which is the
// failure this exists to catch: a handler with no producer.
//
// Known limit, stated rather than implied: this checks the routes it lists. A
// NEW geometry route added without a case here goes unnoticed; deleting or
// renaming any listed one turns its 401 into a 404 and fails.
func TestAPI_EveryGeometryRouteIsMountedAndRequiresASession(t *testing.T) {
	router := NewRouter(testDeps())

	for _, tc := range []struct{ method, target string }{
		{"GET", "/v1/geometry"},
		{"GET", "/v1/geometry/compare"},
		{"GET", "/v1/geometry/geo_1"},
		{"POST", "/v1/geometry/geo_1/adopt"},
		// Wave 11. Without this route the binding layer is reachable only from
		// a conversation turn, and "show me this with an 80 mm plate" has no
		// door into the product at all.
		{"POST", "/v1/geometry/geo_1/respec"},
		{"GET", "/v1/geometry/geo_1/export"},
		{"GET", "/v1/geometry/geo_1/export/label"},
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
