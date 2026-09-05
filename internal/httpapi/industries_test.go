package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/pack"
)

// The industry catalogue the workbench builds its selector from.
//
// # Why this endpoint has fences at all
//
// The alternative to it was writing the ten industries into workbench.js, and
// the reason that was rejected is the reason these exist: a second copy of a
// closed set goes stale silently, and somebody then picks an industry the server
// no longer knows and is refused for a name the page itself showed them.
//
// So the fence is not "the endpoint returns 200" — it is that what it publishes
// can actually be sent back and accepted.

func TestIndustries_PublishesEveryIndustryTheProductOffers(t *testing.T) {
	h := &HealthHandlers{}
	rec := httptest.NewRecorder()
	h.Industries(rec, httptest.NewRequest(http.MethodGet, "/v1/meta/industries", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var body struct {
		Industries []struct {
			ID       string `json:"id"`
			Label    string `json:"label"`
			Ceiling  string `json:"ceiling"`
			Boundary string `json:"boundary"`
			Requires string `json:"requires"`
		} `json:"industries"`
		Count int `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding: %v", err)
	}

	// Written out rather than read from pack.Industries(): a fence that
	// enumerates what it checks cannot fail.
	want := []string{
		"Mechanical engineering", "Manufacturing", "Automotive", "Aerospace",
		"Civil engineering", "Electrical engineering", "Construction",
		"Product design", "Architecture", "Other",
	}
	got := map[string]bool{}
	for _, d := range body.Industries {
		got[d.Label] = true
	}
	for _, label := range want {
		if !got[label] {
			t.Errorf("the catalogue does not offer %q, so the workbench selector cannot", label)
		}
	}
	if body.Count != len(want) {
		t.Errorf("count = %d; the selector offers %d", body.Count, len(want))
	}
}

// Every row carries what a person needs to choose, and to understand a refusal.
//
// A selector that showed only names would let somebody pick a licensed domain
// with no idea that work in it stops at a reversible draft. The ceiling and the
// authority are published so the UI can say why a limit exists at the moment
// somebody meets it, rather than only that it does.
func TestIndustries_EveryRowExplainsItself(t *testing.T) {
	h := &HealthHandlers{}
	rec := httptest.NewRecorder()
	h.Industries(rec, httptest.NewRequest(http.MethodGet, "/v1/meta/industries", nil))

	var body struct {
		Industries []map[string]string `json:"industries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Industries) == 0 {
		t.Fatal("no industries published")
	}
	for _, d := range body.Industries {
		for _, field := range []string{"id", "label", "ceiling", "boundary", "requires"} {
			if strings.TrimSpace(d[field]) == "" {
				t.Errorf("industry %q publishes no %s", d["label"], field)
			}
		}
	}
}

// What the catalogue publishes as an id is what the create-goal endpoint takes.
//
// The two are separate code paths — a catalogue and a validator — and nothing
// but this holds them to the same vocabulary. If they drift, every industry in
// the selector becomes a request the server rejects.
func TestIndustries_PublishedIDsAreAcceptedByLookup(t *testing.T) {
	h := &HealthHandlers{}
	rec := httptest.NewRecorder()
	h.Industries(rec, httptest.NewRequest(http.MethodGet, "/v1/meta/industries", nil))

	var body struct {
		Industries []struct {
			ID    string `json:"id"`
			Label string `json:"label"`
		} `json:"industries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, d := range body.Industries {
		// Both forms, because the UI sends the id and a person typing into the
		// CLI sends the label, and EnsureProject is reached by both.
		for _, form := range []string{d.ID, d.Label} {
			def, ok := pack.Lookup(form)
			if !ok {
				t.Errorf("the catalogue publishes %q and nothing resolves it", form)
				continue
			}
			if !def.Available() {
				t.Errorf("the catalogue offers %q, which no project may be created in", form)
			}
		}
	}
}
