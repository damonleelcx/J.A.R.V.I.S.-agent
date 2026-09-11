package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
)

// deadlineLLM records the deadline the turn gave it.
type deadlineLLM struct {
	prototypeLLM
	deadline time.Time
	had      bool
}

func (s *deadlineLLM) Complete(ctx context.Context, req llm.Request) (*llm.Response, error) {
	s.deadline, s.had = ctx.Deadline()
	return s.prototypeLLM.Complete(ctx, req)
}

// A turn is bounded by the TURN budget, not by one model call's timeout.
//
// # What this closes
//
// The handler used `RequestTimeout + 15s`. The ordering was right — a deadline
// shorter than the client's own kills a call mid-backoff and blames the model —
// and the unit was wrong: a turn is not a call. It plans a build, runs a pass
// per subsystem, repairs geometry, runs and rewrites scripts, and looks at the
// render. A multi-pass build measured at 25 minutes could not finish here: it
// was cancelled at 3m15s, mid-stream, and from inside the turn a cancelled
// context is indistinguishable from a model that failed. Nothing reported it.
//
// # Why it asserts a floor and not the exact value
//
// The exact number is configuration and a deployment may set it. What must never
// come back is the ARITHMETIC — a budget derived from one call's timeout — so the
// floor is set above anything that derivation can produce.
func TestConverse_TurnBudgetBoundsTheTurnNotOneCall(t *testing.T) {
	w := workspaceHarness(t)

	deps := w.h.deps
	stub := &deadlineLLM{}
	deps.LLM = stub
	deps.Config.LLM.RequestTimeout = 3 * time.Minute
	deps.Config.LLM.TurnBudget = 21 * time.Minute
	h := NewConverseHandlers(deps)

	r := httptest.NewRequest("POST", "/v1/converse",
		strings.NewReader(`{"message":"Sketch a bracket.","project_id":""}`))
	r = r.WithContext(context.WithValue(context.Background(), ctxKeyUser, w.owner))
	h.Converse(httptest.NewRecorder(), r)

	if !stub.had {
		t.Fatal("the turn ran with no deadline at all: a person waiting on a workbench is " +
			"owed an end, and a hung provider would hold the connection forever")
	}
	left := time.Until(stub.deadline)
	// Anything the old derivation could produce is below this: it was
	// RequestTimeout + 15s, and RequestTimeout is 3m here.
	if left < 10*time.Minute {
		t.Errorf("the turn had %s, which is one model call's timeout rather than a turn's "+
			"budget of %s. A multi-pass build is dozens of calls plus kernel time, and this "+
			"is the arithmetic that cancelled a 25-minute build at 3m15s mid-stream",
			left.Round(time.Second), deps.Config.LLM.TurnBudget)
	}
	if left > 21*time.Minute {
		t.Errorf("the turn had %s, more than the configured budget of %s",
			left.Round(time.Second), deps.Config.LLM.TurnBudget)
	}
}

// An unset budget is "not configured", never "no time".
//
// A Config assembled in code has this field zero. Read literally, a zero
// deadline cancels the turn before its first call: every database read inside it
// then fails and the person is told the database could not be reached — true,
// and pointing at the wrong thing entirely. Two existing tests reported exactly
// that the moment the handler started reading the field, which is the only
// reason this case is known.
func TestConverse_AnUnsetTurnBudgetIsNotAnExpiredOne(t *testing.T) {
	w := workspaceHarness(t)

	deps := w.h.deps
	stub := &deadlineLLM{}
	deps.LLM = stub
	deps.Config.LLM.TurnBudget = 0
	h := NewConverseHandlers(deps)

	r := httptest.NewRequest("POST", "/v1/converse",
		strings.NewReader(`{"message":"Sketch a bracket.","project_id":""}`))
	r = r.WithContext(context.WithValue(context.Background(), ctxKeyUser, w.owner))
	rec := httptest.NewRecorder()
	h.Converse(rec, r)

	if !stub.had {
		t.Fatal("the model was never called at all with an unset budget")
	}
	if left := time.Until(stub.deadline); left <= 0 {
		t.Errorf("an unset budget produced a deadline %s in the past, so the turn was "+
			"cancelled before it began", (-left).Round(time.Millisecond))
	}
	if strings.Contains(rec.Body.String(), "DATABASE_UNAVAILABLE") {
		t.Errorf("an unset budget was reported to the person as a database failure:\n%s",
			rec.Body.String())
	}
}

// slowLLM answers after a pause, so a turn outlives a short write timeout.
type slowLLM struct {
	prototypeLLM
	pause time.Duration
}

func (s *slowLLM) Complete(ctx context.Context, req llm.Request) (*llm.Response, error) {
	select {
	case <-time.After(s.pause):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.prototypeLLM.Complete(ctx, req)
}

// A turn longer than the server's write timeout still reaches the person.
//
// # What this closes
//
// http.Server.WriteTimeout is a deadline on the WHOLE response, set when the
// request arrives — five minutes in production. That is generous for a page and
// far too short for a stream a turn may spend half an hour filling, and raising
// the turn budget without lifting it would have produced the same silent
// truncation one layer down: the turn keeps working, the connection is closed
// under it, and the person watches a build stop moving.
//
// # Why a real server and not a recorder
//
// httptest.ResponseRecorder has no connection and no deadline, so it cannot
// observe this at all — a recorder-based test passes whether or not the deadline
// is lifted. This one starts a real server with a write timeout deliberately
// shorter than the turn.
func TestConverse_AStreamOutlivesTheServerWriteTimeout(t *testing.T) {
	w := workspaceHarness(t)

	deps := w.h.deps
	deps.LLM = &slowLLM{pause: 1200 * time.Millisecond}
	h := NewConverseHandlers(deps)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		h.Converse(rw, r.WithContext(context.WithValue(r.Context(), ctxKeyUser, w.owner)))
	}))
	// Shorter than the turn above, on purpose. This is production's five minutes
	// against a twenty-five minute build, scaled to something a test can wait for.
	srv.Config.WriteTimeout = 300 * time.Millisecond
	srv.Start()
	defer srv.Close()

	resp, err := srv.Client().Post(srv.URL+"/v1/converse", "application/json",
		strings.NewReader(`{"message":"Sketch a bracket.","project_id":""}`))
	if err != nil {
		t.Fatalf("the request failed outright: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, readErr := io.ReadAll(resp.Body)

	if readErr != nil {
		t.Fatalf("the stream was cut off after %d bytes: %v\n"+
			"The connection's write deadline is still the server's, so a turn longer than "+
			"FORGE_HTTP_WRITE_TIMEOUT is closed under the person watching it.", len(body), readErr)
	}
	if !strings.Contains(string(body), `"kind":"prototype"`) {
		t.Errorf("the turn's geometry never arrived, so the stream did not survive the "+
			"write timeout:\n%s", body)
	}
}
