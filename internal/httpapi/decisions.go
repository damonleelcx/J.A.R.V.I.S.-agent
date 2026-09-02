package httpapi

import (
	"net/http"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/claim"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/memory"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// The decision log over HTTP (PRD MEM-03).

// DecisionDTO is one decision as a reader sees it.
type DecisionDTO struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	GoalID    string `json:"goal_id,omitempty"`
	AuthorID  string `json:"author_id"`
	Title     string `json:"title"`
	Decision  string `json:"decision"`
	Rationale string `json:"rationale,omitempty"`

	Alternatives []AlternativeDTO `json:"alternatives"`
	Evidence     []EvidenceDTO    `json:"evidence"`
	Affected     []string         `json:"affected_artifacts"`

	// Current, SupersedesID and SupersededByID together let a reader see both
	// what was believed and what replaced it, which is the whole point of a log
	// that supersedes rather than edits.
	Current        bool   `json:"current"`
	SupersedesID   string `json:"supersedes_id,omitempty"`
	SupersededByID string `json:"superseded_by_id,omitempty"`

	DecidedAt string `json:"decided_at"`
}

// AlternativeDTO is an option that was considered and rejected.
type AlternativeDTO struct {
	Option string `json:"option"`
	WhyNot string `json:"why_not"`
}

// EvidenceDTO is one piece of evidence with how it is known.
//
// Actionable is computed here rather than left to the client, for the same
// reason the avatar state is: "may I act on this?" is a product decision, and a
// decision reimplemented in the browser is one that will eventually disagree
// with the server about a recalled figure.
type EvidenceDTO struct {
	Statement  string `json:"statement"`
	How        string `json:"how"`
	HowMeans   string `json:"how_means"`
	Source     string `json:"source,omitempty"`
	Actionable bool   `json:"actionable"`
}

func toDecisionDTO(d memory.Decision) DecisionDTO {
	out := DecisionDTO{
		ID: d.ID, ProjectID: d.ProjectID, AuthorID: d.AuthorID,
		Title: d.Title, Decision: d.Decision, Rationale: d.Rationale,
		Alternatives: make([]AlternativeDTO, 0, len(d.Alternatives)),
		Evidence:     make([]EvidenceDTO, 0, len(d.Evidence)),
		Affected:     d.Affected,
		Current:      d.Current(),
		DecidedAt:    d.DecidedAt.UTC().Format(time.RFC3339),
	}
	if out.Affected == nil {
		out.Affected = []string{}
	}
	if d.GoalID != nil {
		out.GoalID = *d.GoalID
	}
	if d.SupersedesID != nil {
		out.SupersedesID = *d.SupersedesID
	}
	if d.SupersededByID != nil {
		out.SupersededByID = *d.SupersededByID
	}
	for _, a := range d.Alternatives {
		out.Alternatives = append(out.Alternatives, AlternativeDTO{Option: a.Option, WhyNot: a.WhyNot})
	}
	for _, e := range d.Evidence {
		out.Evidence = append(out.Evidence, EvidenceDTO{
			Statement: e.Statement, How: string(e.How), HowMeans: e.How.Gloss(),
			Source: e.Source, Actionable: e.Actionableish(),
		})
	}
	return out
}

type recordDecisionRequest struct {
	ProjectID    string           `json:"project_id"`
	GoalID       string           `json:"goal_id,omitempty"`
	Title        string           `json:"title"`
	Decision     string           `json:"decision"`
	Rationale    string           `json:"rationale,omitempty"`
	Alternatives []AlternativeDTO `json:"alternatives,omitempty"`
	Evidence     []EvidenceDTO    `json:"evidence,omitempty"`
	Affected     []string         `json:"affected_artifacts,omitempty"`
	SupersedesID string           `json:"supersedes_id,omitempty"`
}

// RecordDecision handles POST /v1/decisions.
//
// The author is the authenticated caller, never a field in the body. A decision
// log whose authorship can be asserted by the writer records nothing.
func (h *MemoryHandlers) RecordDecision(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())

	var req recordDecisionRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	if err := h.authoriseProject(r, req.ProjectID, user.ID); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}

	d := &memory.Decision{
		ProjectID: req.ProjectID, AuthorID: user.ID, Title: req.Title,
		Decision: req.Decision, Rationale: req.Rationale, Affected: req.Affected,
	}
	if req.GoalID != "" {
		if err := h.authoriseGoal(r, req.GoalID, user.ID); err != nil {
			WriteError(w, r, h.deps.Log, err)
			return
		}
		d.GoalID = &req.GoalID
	}
	if req.SupersedesID != "" {
		d.SupersedesID = &req.SupersedesID
	}
	for _, a := range req.Alternatives {
		d.Alternatives = append(d.Alternatives, memory.Alternative{Option: a.Option, WhyNot: a.WhyNot})
	}
	for _, e := range req.Evidence {
		// The label is taken as given and then validated by the same rules
		// everything else goes through: an unrecognised one is downgraded, and a
		// retrieved figure with no source is named as recalled. The client
		// cannot talk its way past that by choosing a stronger word.
		d.Evidence = append(d.Evidence, claim.Claim{
			Statement: e.Statement, How: claim.Epistemic(e.How), Source: e.Source,
		})
	}

	saved, err := h.svc.RecordDecision(r.Context(), d)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	full, err := h.svc.FindDecision(r.Context(), saved.ID)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	WriteJSON(w, http.StatusCreated, map[string]any{"decision": toDecisionDTO(*full)})
}

// ListDecisions handles GET /v1/decisions?project_id=&current=.
func (h *MemoryHandlers) ListDecisions(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	q := r.URL.Query()

	projectID := q.Get("project_id")
	if projectID == "" {
		WriteError(w, r, h.deps.Log, errs.New("httpapi.ListDecisions", errs.CodeValidationFailed).
			WithDetail("project_id is required; decisions are project-scoped"))
		return
	}
	if err := h.authoriseProject(r, projectID, user.ID); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}

	list, err := h.svc.ListDecisions(r.Context(), memory.DecisionFilter{
		ProjectID: projectID, GoalID: q.Get("goal_id"), CurrentOnly: q.Get("current") == "true",
	})
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	out := make([]DecisionDTO, 0, len(list))
	for _, d := range list {
		out = append(out, toDecisionDTO(d))
	}
	WriteJSON(w, http.StatusOK, map[string]any{"decisions": out})
}

// GetDecision handles GET /v1/decisions/{id}, returning the whole supersession
// chain so the answer that was believed and the one that replaced it are read
// together rather than one at a time.
func (h *MemoryHandlers) GetDecision(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	decisionID := r.PathValue("id")

	d, err := h.svc.FindDecision(r.Context(), decisionID)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	if err := h.authoriseProject(r, d.ProjectID, user.ID); err != nil {
		// Reported as a missing decision rather than a forbidden one, so the
		// endpoint does not confirm that an id exists in somebody else's project.
		WriteError(w, r, h.deps.Log, errs.New("httpapi.GetDecision", errs.CodeNotFound).
			WithDetail("no decision %s", decisionID))
		return
	}
	chain, err := h.svc.DecisionChain(r.Context(), decisionID)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	out := make([]DecisionDTO, 0, len(chain))
	for _, c := range chain {
		out = append(out, toDecisionDTO(c))
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"decision": toDecisionDTO(*d),
		"chain":    out,
	})
}
