package httpapi

import (
	"net/http"
	"sort"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/conversation"
)

// The Telemetry panel's history (PRD NFR-05).
//
// # What was wrong
//
// Every turn was measured — time to first token, total time, tokens, which model
// answered — and every measurement went to the log and nowhere else. The panel
// could therefore only show what the browser tab in front of you had watched
// happen: it emptied on reload and knew nothing about yesterday. Its own "Not
// measured here" list said so in the first line. What was missing was never the
// measurement; it was a store and a way to read it back.
//
// # What this deliberately does NOT answer
//
// Anybody else's turns. A deployment-wide median would be a more interesting
// number and a worse idea: it means one signed-in account reading when everyone
// else was working. The reply names the population it covers rather than letting
// a reader assume a larger one.
//
// A failed turn. Nothing is written for one — the conversation record is
// appended when a reply lands, and a turn that produced no reply has nothing to
// append. That is stated in the reply too, because a latency history that
// silently omitted its failures would read best exactly when things were worst.

// TelemetryHandlers serves the measured history.
type TelemetryHandlers struct {
	deps Deps
	svc  *conversation.Service
}

// NewTelemetryHandlers wires the read side.
func NewTelemetryHandlers(d Deps) *TelemetryHandlers {
	return &TelemetryHandlers{deps: d, svc: conversation.NewService(d.Pool, d.Clock, d.Log)}
}

// measuredTurnDTO is one timed turn.
//
// Every duration is a POINTER, so a measurement that does not exist arrives as
// JSON null and renders as an em dash — never as a zero, which a reader takes as
// "instant". That rule is the panel's, and it only holds if the wire can carry
// the difference.
type measuredTurnDTO struct {
	At           string `json:"at"`
	Model        string `json:"model,omitempty"`
	FirstTokenMS *int   `json:"first_token_ms"`
	TotalMS      *int   `json:"total_ms"`
	RoundTripMS  *int   `json:"round_trip_ms"`
	Tokens       *int64 `json:"tokens"`
	ProjectID    string `json:"project_id,omitempty"`
	// Geometry says this turn produced a shape. Read from the record rather than
	// re-derived: a turn whose detail is empty produced speech only.
	Spoke bool `json:"spoke"`
}

// Turns handles GET /v1/telemetry/turns.
func (h *TelemetryHandlers) Turns(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())

	turns, err := h.svc.Measured(r.Context(), user.ID)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}

	out := make([]measuredTurnDTO, 0, len(turns))
	var first, total []int
	for i := range turns {
		t := turns[i]
		out = append(out, measuredTurnDTO{
			At:           t.SaidAt.UTC().Format(time.RFC3339),
			Model:        t.Timing.Model,
			FirstTokenMS: t.Timing.FirstTokenMS,
			TotalMS:      t.Timing.TotalMS,
			RoundTripMS:  t.Timing.RoundTripMS,
			Tokens:       t.Timing.Tokens,
			ProjectID:    t.ProjectID,
			Spoke:        t.Text != "",
		})
		if t.Timing.FirstTokenMS != nil {
			first = append(first, *t.Timing.FirstTokenMS)
		}
		if t.Timing.TotalMS != nil {
			total = append(total, *t.Timing.TotalMS)
		}
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"turns": out,
		// Computed here rather than in the browser, so that the panel's
		// session figures and its history figures are the same statistic
		// arrived at the same way. Null when there is nothing to take a
		// median of, which is not zero.
		"median_first_token_ms": median(first),
		"median_total_ms":       median(total),
		// Named, so nobody reads this as the deployment.
		"population": "your own turns on this deployment, newest first",
		// Named, so nobody reads a quiet history as a healthy one.
		"excludes": "turns that failed before producing a reply. Nothing is recorded for one — " +
			"the record is appended when a reply lands — so they are in the server log with " +
			"their error code and not in this list.",
	})
}

// median returns the middle value, or nil when there is nothing to take one of.
//
// Nil and not zero, for the reason the whole of this file exists: a statistic
// over no samples is not a fast one.
func median(xs []int) *int {
	if len(xs) == 0 {
		return nil
	}
	v := append([]int(nil), xs...)
	sort.Ints(v)
	m := len(v) / 2
	if len(v)%2 == 1 {
		return &v[m]
	}
	mid := (v[m-1] + v[m]) / 2
	return &mid
}
