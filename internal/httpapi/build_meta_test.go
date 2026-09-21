package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/buildinfo"
)

// ‼️ GET /v1/meta/build says which build is answering, or says it does not know
// (2026-09-20).
//
// Production runs an image and the first question of an incident is which commit
// it was built from. That could only be answered from outside the system — by
// trusting the tag on the image — because nothing in the image knew. This is the
// endpoint that answers it, and the case that matters most is the one where it
// cannot: an unstamped build must be recognisable as unstamped rather than
// report "dev", which reads like an answer.
func TestMetaBuild_ReportsTheBuildOrSaysUnknown(t *testing.T) {
	restore := buildinfo.Get()
	t.Cleanup(func() { buildinfo.Set(restore.Version, restore.Commit, restore.Date) })

	ask := func(t *testing.T) map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		(&HealthHandlers{}).Build(rec, httptest.NewRequest(http.MethodGet, "/v1/meta/build", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decoding %s: %v", rec.Body.String(), err)
		}
		return body
	}

	// An image built without deploy/Dockerfile's version arguments.
	buildinfo.Set("", "", "")
	body := ask(t)
	for _, field := range []string{"version", "commit", "built"} {
		if body[field] != "unknown" {
			t.Errorf("an unstamped build answers %s=%v, want %q", field, body[field], "unknown")
		}
	}
	if body["stamped"] != false {
		t.Errorf("an unstamped build answers stamped=%v", body["stamped"])
	}

	// The same, for the placeholder this repository actually shipped. "dev" in
	// production is not a version; it is the absence of one.
	buildinfo.Set("dev", "unknown", "unknown")
	if body := ask(t); body["version"] != "unknown" || body["stamped"] != false {
		t.Errorf(`a build stamped "dev" answers %v; it is not a build anybody can look up`, body)
	}

	// A stamped image answers with exactly what it was built with.
	buildinfo.Set("v0.9.1", "ab12cd3", "2026-09-20T09:41:07Z")
	body = ask(t)
	for field, want := range map[string]string{
		"version": "v0.9.1", "commit": "ab12cd3", "built": "2026-09-20T09:41:07Z",
	} {
		if body[field] != want {
			t.Errorf("a stamped build answers %s=%v, want %q", field, body[field], want)
		}
	}
	if body["stamped"] != true {
		t.Errorf("a stamped build answers stamped=%v", body["stamped"])
	}

	// It is reachable without a session: an operator asking which build is
	// running has not signed in, and an incident is the worst time to need to.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/meta/build", (&HealthHandlers{}).Build)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/meta/build", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("the route answers %d", rec.Code)
	}
}
