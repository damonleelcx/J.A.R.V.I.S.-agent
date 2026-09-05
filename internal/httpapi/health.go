package httpapi

import (
	"net/http"
	"sort"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/pack"
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

// Industries handles GET /v1/meta/industries.
//
// # Why this is an endpoint rather than a list in the page
//
// The workbench needs the industry selector's options, and the obvious shortcut
// is to write them into workbench.js. That would be a second copy of a closed
// set whose whole purpose is to be the one place the rules are filed under — and
// the copy in the JavaScript is the one that silently goes stale, so a person
// would pick an industry the server no longer knows and be refused for a name
// the product itself showed them.
//
// Same reasoning as ErrorCodes directly above, and the same /v1/meta namespace:
// a client builds its vocabulary from the running server rather than from a copy
// that drifts.
//
// Unauthenticated, like the error registry. This is a product catalogue — which
// domains this build can work in and how far — and it says nothing about any
// project, user or piece of work.
func (h *HealthHandlers) Industries(w http.ResponseWriter, r *http.Request) {
	defs := pack.Industries()
	out := make([]map[string]any, 0, len(defs))
	for _, d := range defs {
		out = append(out, map[string]any{
			// The id is what a caller sends back; the label is what a person reads.
			// Both, because sending the label back must also work — Lookup accepts
			// it — and a UI that only had one of them would have to invent the other.
			"id":       string(d.Pack),
			"label":    d.Industry,
			"ceiling":  string(d.MaxTier),
			"boundary": d.Summary,
			// What would be needed to work above the ceiling. Published so a UI can
			// say why a limit exists at the moment somebody meets it, rather than
			// only reporting that it does.
			"requires": d.Requires,
		})
	}
	WriteJSON(w, http.StatusOK, map[string]any{"industries": out, "count": len(out)})
}
