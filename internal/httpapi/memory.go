package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/claim"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/memory"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// The user's control over what FORGE remembers (PRD MEM-02).
//
// MEM-02 is a USER requirement — inspect, correct, pin, expire, export, delete,
// and see why an item was retrieved. forgectl covers the operator's questions;
// this is the surface the person whose memory it is actually uses.

// MemoryHandlers serve memory and the decision log.
type MemoryHandlers struct {
	deps Deps
	svc  *memory.Service
}

// NewMemoryHandlers wires the memory endpoints.
func NewMemoryHandlers(d Deps) *MemoryHandlers {
	return &MemoryHandlers{deps: d, svc: memory.NewService(d.Pool, d.Clock, d.Log)}
}

// ItemDTO is one memory item as its owner sees it.
//
// HowMeans travels with How for the same reason the export carries it: a label
// the reader cannot interpret is not an explanation, and the browser must not
// be the place that decides what "retrieved" means.
type ItemDTO struct {
	ID              string          `json:"id"`
	Scope           string          `json:"scope"`
	Layer           string          `json:"layer"`
	Key             string          `json:"key"`
	Value           json.RawMessage `json:"value"`
	How             string          `json:"how"`
	HowMeans        string          `json:"how_means"`
	Actionable      bool            `json:"actionable"`
	Source          string          `json:"source,omitempty"`
	Pinned          bool            `json:"pinned"`
	ExpiresAt       string          `json:"expires_at,omitempty"`
	Forgotten       bool            `json:"forgotten"`
	ForgottenAt     string          `json:"forgotten_at,omitempty"`
	ForgottenReason string          `json:"forgotten_reason,omitempty"`
	CreatedAt       string          `json:"created_at"`
	UpdatedAt       string          `json:"updated_at"`
}

// RecalledDTO is an item together with why this query returned it.
type RecalledDTO struct {
	Item ItemDTO `json:"item"`
	// Why and WhyDetail are the answer to MEM-02's "show why an item was
	// retrieved". Never empty: an item with no reason is a defect, not a blank.
	Why       string `json:"why"`
	WhyDetail string `json:"why_detail"`
}

func toItemDTO(i memory.Item) ItemDTO {
	layer, _ := memory.LayerOf(i.Scope)
	c := claim.Claim{How: i.How, Source: i.Source}
	return ItemDTO{
		ID: i.ID, Scope: string(i.Scope), Layer: layer.PRDName, Key: i.Key,
		Value: json.RawMessage(i.Value), How: string(i.How), HowMeans: i.How.Gloss(),
		Actionable: c.Actionableish(), Source: i.Source, Pinned: i.Pinned,
		ExpiresAt: timeString(i.ExpiresAt), Forgotten: i.Forgotten(),
		ForgottenAt: timeString(i.ForgottenAt), ForgottenReason: i.ForgottenReason,
		CreatedAt: i.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: i.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func timeString(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// Layers handles GET /v1/memory/layers.
//
// Unauthenticated data about the build rather than about anybody's memory: it
// says what the layers ARE. A user deciding where something belongs, or reading
// why an item vanished, needs this and it reveals nothing.
func (h *MemoryHandlers) Layers(w http.ResponseWriter, r *http.Request) {
	type layerDTO struct {
		Scope     string `json:"scope"`
		Name      string `json:"name"`
		OwnedBy   string `json:"owned_by"`
		Lifetime  string `json:"default_lifetime"`
		VisibleTo string `json:"visible_to"`
		Enforced  bool   `json:"visibility_enforced"`
		Describes string `json:"describes"`
	}
	out := []layerDTO{}
	for _, l := range memory.Layers() {
		lifetime := "never expires"
		if l.DefaultTTL > 0 {
			lifetime = l.DefaultTTL.String()
		}
		out = append(out, layerDTO{
			Scope: string(l.Scope), Name: l.PRDName, OwnedBy: string(l.Owner),
			Lifetime: lifetime, VisibleTo: string(l.Visibility),
			// Said plainly rather than implied. Organisation visibility is
			// declared and not yet enforced — there is no membership model — and
			// a client that assumed otherwise would be building on nothing.
			Enforced:  l.Visibility != memory.VisibilityOrganisation,
			Describes: l.Gloss,
		})
	}
	WriteJSON(w, http.StatusOK, map[string]any{"layers": out})
}

// Recall handles GET /v1/memory/recall.
//
// Every item comes back with why it came back. This is the endpoint MEM-02's
// "show why an item was retrieved" is about, and it runs the agent's own recall
// rather than a report about it — so what a user sees here is what FORGE was
// actually given.
func (h *MemoryHandlers) Recall(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	q := r.URL.Query()

	rc := memory.Recall{
		GoalID: q.Get("goal_id"), ProjectID: q.Get("project_id"),
		// Personal memory is always the caller's own. Taking a user id from the
		// query string would make this endpoint a way to read somebody else's.
		UserID: user.ID,
		Prefix: q.Get("prefix"),
	}
	if key := q.Get("key"); key != "" {
		rc.Keys = []string{key}
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			WriteError(w, r, h.deps.Log, errs.New("httpapi.Recall", errs.CodeValidationFailed).
				WithDetail("limit must be a positive whole number; got %q", raw))
			return
		}
		rc.Limit = n
	}
	if rc.GoalID != "" {
		if err := h.authoriseGoal(r, rc.GoalID, user.ID); err != nil {
			WriteError(w, r, h.deps.Log, err)
			return
		}
	}
	if rc.ProjectID != "" {
		if err := h.authoriseProject(r, rc.ProjectID, user.ID); err != nil {
			WriteError(w, r, h.deps.Log, err)
			return
		}
	}

	got, err := h.svc.Recall(r.Context(), rc)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	out := make([]RecalledDTO, 0, len(got))
	for _, g := range got {
		out = append(out, RecalledDTO{Item: toItemDTO(g.Item), Why: string(g.Why), WhyDetail: g.Detail})
	}
	WriteJSON(w, http.StatusOK, map[string]any{"recalled": out})
}

// List handles GET /v1/memory?scope=&owner=.
//
// Unlike Recall this shows everything held, forgotten and expired rows
// included: it is the inspection surface, and a user checking that their
// deletion took has to be able to see the deletion.
func (h *MemoryHandlers) List(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	scope := memory.Scope(r.URL.Query().Get("scope"))
	owner := r.URL.Query().Get("owner")

	owner, err := h.authoriseLayer(r, scope, owner, user.ID)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	items, err := h.svc.Inspect(r.Context(), scope, owner)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	out := make([]ItemDTO, 0, len(items))
	for _, i := range items {
		out = append(out, toItemDTO(i))
	}
	WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

// Export handles GET /v1/memory/export?scope=&owner=.
func (h *MemoryHandlers) Export(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	scope := memory.Scope(r.URL.Query().Get("scope"))

	owner, err := h.authoriseLayer(r, scope, r.URL.Query().Get("owner"), user.ID)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	export, err := h.svc.ExportLayer(r.Context(), scope, owner)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	WriteJSON(w, http.StatusOK, export)
}

// UpdateItem handles PATCH /v1/memory/{id} — correct, pin, or expire.
//
// One endpoint for the three because they are three edits to one item, and a
// client that wants to correct a value and pin it in the same breath should not
// have to make two requests that can half-succeed.
type updateItemRequest struct {
	// Value, when present, replaces the content. How must accompany it: a
	// corrected value with a stale epistemic label is worse than the error it
	// replaced, because it now looks freshly checked.
	Value  json.RawMessage `json:"value,omitempty"`
	How    string          `json:"how,omitempty"`
	Source *string         `json:"source,omitempty"`
	Pinned *bool           `json:"pinned,omitempty"`
	// ExpiresIn is a duration such as "24h". "never" clears the expiry.
	ExpiresIn *string `json:"expires_in,omitempty"`
}

// UpdateItem applies a correction, a pin, and an expiry in that order.
func (h *MemoryHandlers) UpdateItem(w http.ResponseWriter, r *http.Request) {
	const op = "httpapi.UpdateItem"
	user, _ := UserFrom(r.Context())

	var req updateItemRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	item, err := h.authoriseItem(r, r.PathValue("id"), user.ID)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}

	if len(req.Value) > 0 {
		how := claim.Epistemic(req.How)
		if !how.Valid() {
			WriteError(w, r, h.deps.Log, errs.New(op, errs.CodeValidationFailed).
				WithDetail("a corrected value must say how it is now known; %q is not one of the seven categories", req.How))
			return
		}
		source := item.Source
		if req.Source != nil {
			source = *req.Source
		}
		var decoded any
		if err := json.Unmarshal(req.Value, &decoded); err != nil {
			WriteError(w, r, h.deps.Log, errs.Wrap(op, errs.CodeValidationFailed, err).
				WithDetail("the corrected value is not valid JSON"))
			return
		}
		if _, err := h.svc.Correct(r.Context(), item.ID, decoded, how, source); err != nil {
			WriteError(w, r, h.deps.Log, err)
			return
		}
	}
	if req.Pinned != nil {
		if err := h.svc.Pin(r.Context(), item.ID, *req.Pinned); err != nil {
			WriteError(w, r, h.deps.Log, err)
			return
		}
	}
	if req.ExpiresIn != nil {
		if *req.ExpiresIn == "never" {
			err = h.svc.Expire(r.Context(), item.ID, nil)
		} else {
			d, perr := time.ParseDuration(*req.ExpiresIn)
			if perr != nil {
				WriteError(w, r, h.deps.Log, errs.Wrap(op, errs.CodeValidationFailed, perr).
					WithDetail("expires_in must be a duration such as \"24h\", or \"never\"; got %q", *req.ExpiresIn))
				return
			}
			err = h.svc.ExpireIn(r.Context(), item.ID, d)
		}
		if err != nil {
			WriteError(w, r, h.deps.Log, err)
			return
		}
	}

	updated, err := h.svc.Repo().FindByID(r.Context(), h.deps.Pool, item.ID)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"item": toItemDTO(*updated)})
}

// ForgetItem handles DELETE /v1/memory/{id}.
//
// The response says what actually happened, because "deleted" would be a
// half-truth: the value is gone and the key is held, and the user needs to know
// the second part — it is why FORGE will not learn the same thing tomorrow.
func (h *MemoryHandlers) ForgetItem(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())

	item, err := h.authoriseItem(r, r.PathValue("id"), user.ID)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	reason := r.URL.Query().Get("reason")
	if err := h.svc.Forget(r.Context(), item.ID, user.ID, reason); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"forgotten": item.Key,
		"effect": "The value is cleared and the key stays claimed, so FORGE will not learn it again. " +
			"Purging the entry re-opens the key; that is a separate, recorded act.",
	})
}

// ---------------------------------------------------------------------------
// Authorisation
// ---------------------------------------------------------------------------
//
// Every one of these reports a resource the caller cannot see exactly as one
// that does not exist, so the endpoints are not membership oracles — the same
// rule the goal endpoints follow.

func (h *MemoryHandlers) authoriseProject(r *http.Request, projectID, userID string) error {
	var found string
	err := h.deps.Pool.QueryRow(r.Context(),
		`select id from forge_projects where id = $1 and owner_id = $2`, projectID, userID).Scan(&found)
	if err != nil {
		return errs.New("httpapi.authoriseProject", errs.CodeNotFound).
			WithDetail("no project %s", projectID)
	}
	return nil
}

func (h *MemoryHandlers) authoriseGoal(r *http.Request, goalID, userID string) error {
	var found string
	err := h.deps.Pool.QueryRow(r.Context(), `
		select g.id from forge_goals g
		  join forge_projects p on p.id = g.project_id
		 where g.id = $1 and p.owner_id = $2`, goalID, userID).Scan(&found)
	if err != nil {
		return errs.New("httpapi.authoriseGoal", errs.CodeNotFound).
			WithDetail("no goal %s", goalID)
	}
	return nil
}

// authoriseLayer checks the caller may read a layer for an owner, and returns
// the owner id to use.
//
// Personal memory ignores whatever owner the caller supplied and substitutes
// their own: an endpoint that let a user name whose preferences to read would
// be the leak the layer exists to prevent.
func (h *MemoryHandlers) authoriseLayer(r *http.Request, scope memory.Scope, owner, userID string) (string, error) {
	layer, err := memory.LayerOf(scope)
	if err != nil {
		return "", err
	}
	switch layer.Owner {
	case memory.OwnerUser:
		return userID, nil
	case memory.OwnerProject:
		return owner, h.authoriseProject(r, owner, userID)
	case memory.OwnerGoal:
		return owner, h.authoriseGoal(r, owner, userID)
	default:
		return "", nil
	}
}

// authoriseItem loads an item and checks the caller may act on it.
//
// The check is on the item's OWNER, resolved from the row rather than from the
// request: an item id is guessable in principle and supplied by the client in
// every case, so the row itself has to say who it belongs to.
func (h *MemoryHandlers) authoriseItem(r *http.Request, itemID, userID string) (*memory.Item, error) {
	const op = "httpapi.authoriseItem"

	item, err := h.svc.Repo().FindByID(r.Context(), h.deps.Pool, itemID)
	if err != nil {
		return nil, errs.New(op, errs.CodeNotFound).WithDetail("no memory item %s", itemID)
	}
	notFound := errs.New(op, errs.CodeNotFound).WithDetail("no memory item %s", itemID)

	switch {
	case item.UserID != nil:
		if *item.UserID != userID {
			return nil, notFound
		}
	case item.ProjectID != nil:
		if h.authoriseProject(r, *item.ProjectID, userID) != nil {
			return nil, notFound
		}
	case item.GoalID != nil:
		if h.authoriseGoal(r, *item.GoalID, userID) != nil {
			return nil, notFound
		}
	default:
		// Organisation memory. Editable by any authenticated user today, for the
		// same reason it is readable by them: there is no membership model to
		// check against yet. Stated here rather than left as a silent fallthrough
		// so that when SEC-02 lands, this is the line that changes.
	}
	return item, nil
}
