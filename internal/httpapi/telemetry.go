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
// A latency history that silently omitted its failures would read best exactly
// when things were worst, so every failed turn is listed here, marked failed,
// with whatever was known about it.
//
// Two kinds arrive, knowing different amounts. A reply that ARRIVED and could
// not be used has been written since migration 0023 and carries what it cost. A
// turn that failed BEFORE any reply — a model that could not be reached, a turn
// cut off — used to be written nowhere at all, and this panel said so; it is now
// recorded too, with the code it failed with and the elapsed time, and with no
// token count, because nobody reported one. Both are left out of both medians:
// a failure's timing is not a reply's.

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
	// Failed marks a turn that did not produce a reply — either one that arrived
	// and could not be used, or one that failed before any reply arrived at all.
	// The first spent tokens and says how many; the second has none to report,
	// and its token count is null rather than zero.
	Failed bool `json:"failed,omitempty"`
	// ReplyArrived tells those two apart, and only means something when Failed.
	// They are different problems with different remedies — a model that
	// answered nonsense is not a model that could not be reached — and a panel
	// that drew them the same way would send the reader looking in the wrong
	// place. It is derived from whether a reply was kept, not asserted.
	ReplyArrived bool `json:"reply_arrived,omitempty"`
	// Failure is the error code the turn failed with, empty when it did not.
	// The code only; the refused reply itself stays in the conversation record,
	// which is deletable with the conversation (AUD-07).
	Failure string `json:"failure,omitempty"`
	// Why is the sentence the person was shown in place of a reply, which since
	// 2026-09-29 leads with what the PROVIDER answered — "The provider answered
	// 429 (quota_exhausted): You exceeded your current quota."
	//
	// ‼️ Why a code is not enough here. Every failed turn in this panel read
	// EXTERNAL_UNAVAILABLE, and a panel of identical codes cannot tell a quota
	// that has run out from an outage from a model the provider retired — which
	// is the whole question somebody opens it with. The code stays, because it
	// is what a report is filed against.
	//
	// It is FORGE's own sentence, not model output: a refused reply is not
	// here, and it never will be (SEC-04). It is redacted and bounded where the
	// provider's answer is built (internal/llm/upstream.go), and it is read
	// from the same conversation row the delete path takes (AUD-07).
	Why string `json:"why,omitempty"`
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
			Spoke:        t.Text != "" && !t.Failed(),
			Failed:       t.Failed(),
			ReplyArrived: t.UnusableReply != "",
			Failure:      t.Failure,
			// Only for a turn that failed: on a turn that worked this column
			// holds what FORGE said, and the panel is not a transcript.
			Why: whyItFailed(t),
		})
		if t.Failed() {
			continue
		}
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
		"excludes": "nothing that happened. Every failed turn is listed, marked failed: one " +
			"whose reply arrived and could not be used, with the tokens it cost, and one that " +
			"failed before any reply arrived — a model that could not be reached, a turn cut " +
			"off — with its error code and no token count, because nobody reported one. Both " +
			"are left out of the two medians, which are over replies. What is still not here " +
			"is anybody else's turns.",
	})
}

// median returns the middle value, or nil when there is nothing to take one of.
//
// Nil and not zero, for the reason the whole of this file exists: a statistic
// over no samples is not a fast one.
// whyItFailed is the sentence a failed turn was shown in place of a reply, and
// "" for a turn that did not fail.
//
// Read from the row rather than re-derived: what the person SAW is the record,
// and a panel that reconstructed a sentence from the code would print today's
// wording over yesterday's failure. It also means the provider's own answer —
// which converse.go puts at the front of that sentence — arrives here for free.
func whyItFailed(t conversation.Turn) string {
	if !t.Failed() {
		return ""
	}
	return t.Text
}

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
