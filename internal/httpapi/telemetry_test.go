package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The Telemetry history, through the endpoint that writes it.
//
// # Why this drives /v1/converse rather than the repository
//
// The domain fences prove that a timing which is RECORDED comes back. They pass
// whether or not anything ever records one — and /v1/converse is the only thing
// that does, from inside a stream, on the `done` event. That is the exact shape
// of defect this codebase has now recorded five times: a fence over a helper
// that is correct and simply not called. So this asks the endpoint for a turn
// and then asks the other endpoint what it measured.
func TestATurnsTimingReachesTheTelemetryEndpoint(t *testing.T) {
	w := workspaceHarness(t)
	ctx := context.Background()

	deps := w.h.deps
	deps.LLM = &stubLLM{}
	h := NewConverseHandlers(deps)

	r := httptest.NewRequest("POST", "/v1/converse",
		strings.NewReader(`{"message":"How thick should a 24mm washer be?"}`))
	r = r.WithContext(context.WithValue(ctx, ctxKeyUser, w.owner))
	rec := httptest.NewRecorder()
	h.Converse(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("the turn failed: %d %s", rec.Code, rec.Body.String())
	}

	tel := NewTelemetryHandlers(w.h.deps)
	tr := httptest.NewRequest("GET", "/v1/telemetry/turns", nil)
	tr = tr.WithContext(context.WithValue(ctx, ctxKeyUser, w.owner))
	trec := httptest.NewRecorder()
	tel.Turns(trec, tr)
	if trec.Code != http.StatusOK {
		t.Fatalf("telemetry could not be read: %d %s", trec.Code, trec.Body.String())
	}

	var got struct {
		Turns []struct {
			Model        string `json:"model"`
			FirstTokenMS *int   `json:"first_token_ms"`
			RoundTripMS  *int   `json:"round_trip_ms"`
		} `json:"turns"`
		Population string `json:"population"`
		Excludes   string `json:"excludes"`
	}
	if err := json.Unmarshal(trec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Turns) != 1 {
		t.Fatalf("%d measured turns after one turn. Before wave 28 every one of these numbers "+
			"was measured, written to the log, and read back by nothing — so the panel emptied "+
			"on reload and NFR-05 had a measurement nobody could look at.\n%s",
			len(got.Turns), trec.Body.String())
	}
	turn := got.Turns[0]
	if turn.Model != "stub" {
		t.Errorf("model = %q; NFR-05 names model selection", turn.Model)
	}
	// The handler's own elapsed time is always known, so it is always here.
	if turn.RoundTripMS == nil {
		t.Error("the round trip is null. It is measured unconditionally by the handler, so " +
			"there is no turn for which it is unknown.")
	}
	// And the reply says what it covers and what it leaves out, because a
	// latency history that silently omitted its failures would read best
	// exactly when things were worst.
	if !strings.Contains(got.Population, "your own") {
		t.Errorf("the reply does not name the population it covers: %q", got.Population)
	}
	if !strings.Contains(got.Excludes, "failed") {
		t.Errorf("the reply does not say that failed turns are absent: %q", got.Excludes)
	}
}

// A turn nobody timed is absent, not zero.
//
// This is the property the whole design rests on, held at the wire: JSON `null`
// renders as an em dash, and `0` renders as the best number on the panel.
func TestAnUnmeasuredFigureTravelsAsNullNotZero(t *testing.T) {
	// turnTiming is the one place a measurement becomes a stored figure, so it
	// is asked directly about each one it is allowed to drop.
	got := turnTiming("m", 0, 0, 0, 12)
	if got == nil {
		t.Fatal("a turn with a known round trip produced no timing at all")
	}
	if got.FirstTokenMS != nil {
		t.Errorf("an unmeasured time to first token became %d. Zero is the one value a reader "+
			"takes as good news, and once it is in the column it can never be told from a real "+
			"one again.", *got.FirstTokenMS)
	}
	if got.TotalMS != nil || got.Tokens != nil {
		t.Errorf("an unmeasured total or token count was stored as zero: %+v", got)
	}
	if got.RoundTripMS == nil || *got.RoundTripMS != 12 {
		t.Errorf("round trip = %v, want 12", got.RoundTripMS)
	}
}

// Telemetry is somebody's. The scope is in the query, and this is the fence over
// that being true through the endpoint.
func TestTelemetryIsScopedToTheCaller(t *testing.T) {
	w := workspaceHarness(t)
	ctx := context.Background()

	deps := w.h.deps
	deps.LLM = &stubLLM{}
	h := NewConverseHandlers(deps)
	r := httptest.NewRequest("POST", "/v1/converse", strings.NewReader(`{"message":"hello"}`))
	r = r.WithContext(context.WithValue(ctx, ctxKeyUser, w.owner))
	h.Converse(httptest.NewRecorder(), r)

	stranger := w.owner
	stranger.ID = "usr_someone_else"

	tel := NewTelemetryHandlers(w.h.deps)
	tr := httptest.NewRequest("GET", "/v1/telemetry/turns", nil)
	tr = tr.WithContext(context.WithValue(ctx, ctxKeyUser, stranger))
	trec := httptest.NewRecorder()
	tel.Turns(trec, tr)

	var got struct {
		Turns []json.RawMessage `json:"turns"`
	}
	if err := json.Unmarshal(trec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Turns) != 0 {
		t.Errorf("a second account read %d of somebody else's measured turns", len(got.Turns))
	}
}
