package httpapi

import (
	"net/http"
	"sort"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// HealthHandlers serve liveness, readiness, and API metadata.
type HealthHandlers struct{ d Deps }

// NewHealthHandlers wires the health endpoints.
func NewHealthHandlers(d Deps) *HealthHandlers { return &HealthHandlers{d: d} }

// Live handles GET /healthz.
//
// Liveness answers only "is this process running?". It deliberately does not
// touch the database: if it did, a database outage would make every orchestrator
// restart every instance, turning a recoverable dependency failure into a
// restart storm at the worst possible moment.
func (h *HealthHandlers) Live(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": h.d.Version,
		"commit":  h.d.Commit,
	})
}

// Ready handles GET /readyz.
//
// Readiness answers "can this instance serve traffic?", so it does check the
// database. It reports latency rather than a bare boolean, because "healthy"
// and "answering in four seconds" are different states and only one of them
// precedes an outage.
func (h *HealthHandlers) Ready(w http.ResponseWriter, r *http.Request) {
	latency, err := db.HealthCheck(r.Context(), h.d.Pool, 3*time.Second)

	body := map[string]any{
		"version": h.d.Version,
		"commit":  h.d.Commit,
		"checks": map[string]any{
			"database": map[string]any{
				"ok":         err == nil,
				"latency_ms": latency.Milliseconds(),
			},
		},
	}
	if err != nil {
		body["status"] = "unavailable"
		body["checks"].(map[string]any)["database"].(map[string]any)["error"] = err.Error()
		WriteJSON(w, http.StatusServiceUnavailable, body)
		return
	}
	body["status"] = "ready"
	body["pool"] = db.Stat(h.d.Pool)
	WriteJSON(w, http.StatusOK, body)
}

// ErrorCodes handles GET /v1/meta/error-codes.
//
// It publishes the whole error registry so that a client, a CLI, or a
// translator can build its dictionary from the running server rather than from
// a copy that drifts. This is what keeps the API and the UI using one vocabulary
// instead of two that slowly diverge.
func (h *HealthHandlers) ErrorCodes(w http.ResponseWriter, r *http.Request) {
	all := errs.All()
	sort.Slice(all, func(i, j int) bool { return all[i].Code < all[j].Code })

	out := make([]map[string]any, 0, len(all))
	for _, d := range all {
		out = append(out, map[string]any{
			"code":        string(d.Code),
			"category":    string(d.Category),
			"http_status": d.HTTPStatus,
			"message":     d.Cause,
			"remedy":      d.Remedy,
			"retryable":   d.Retryable,
		})
	}
	WriteJSON(w, http.StatusOK, map[string]any{"error_codes": out, "count": len(out)})
}
