package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/access"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/claim"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// The workspace model over HTTP (PRD RSN-01, WRK-03, WRK-04).
//
// RSN-01 asks for an EDITABLE structure separate from the transcript. Editable
// by a person means an API, not a forgectl command — the CLI is for the
// operator's questions, this is for the people doing the work.

// WorkspaceHandlers serve the graph and the artifact lifecycle.
type WorkspaceHandlers struct {
	deps Deps
	svc  *workspace.Service
}

// NewWorkspaceHandlers wires the workspace endpoints.
func NewWorkspaceHandlers(d Deps) *WorkspaceHandlers {
	return &WorkspaceHandlers{deps: d, svc: workspace.NewService(d.Pool, d.Clock, d.Log)}
}

// NodeDTO is one thing in the graph as a reader sees it.
type NodeDTO struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
	// How, HowMeans and Actionable travel together for the same reason they do
	// in memory: a label the reader cannot interpret is not an explanation, and
	// "may I act on this?" is a rule that must not be re-derived in a browser.
	How        string `json:"how"`
	HowMeans   string `json:"how_means"`
	Actionable bool   `json:"actionable"`
	Source     string `json:"source,omitempty"`
	Status     string `json:"status"`
	// Label is how this node is NAMED to a reader. Title is what was stored,
	// which is empty for an anchor whose content lives in another table — a
	// decision, an owner — and a client filling that gap itself would be a
	// second producer of the same string, drifting from the one the edge
	// sentences below are already built with. Set by Graph, which is the only
	// caller that holds the whole graph and can resolve it.
	Label string `json:"label,omitempty"`
	// Anchors resolve to the row they point at, so a client never has to guess
	// which of four columns carries it.
	AnchorKind string `json:"anchor_kind,omitempty"`
	AnchorID   string `json:"anchor_id,omitempty"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

// EdgeDTO is one typed relation, carrying the sentence it reads as.
type EdgeDTO struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	FromID string `json:"from_id"`
	ToID   string `json:"to_id"`
	// Reads is the relation in words, rendered server-side. The pairing rules
	// live in Go; a client composing its own sentence would eventually compose
	// one the rules do not permit.
	Reads string `json:"reads"`
	Note  string `json:"note,omitempty"`
}

func toNodeDTO(n workspace.Node) NodeDTO {
	dto := NodeDTO{
		ID: n.ID, Kind: string(n.Kind), Title: n.Title, Body: n.Body,
		How: string(n.How), HowMeans: n.How.Gloss(),
		Actionable: (claim.Claim{How: n.How, Source: n.Source}).Actionableish(),
		Source:     n.Source, Status: string(n.Status),
		CreatedAt: n.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: n.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if ref, ok := n.AnchorRef(); ok {
		dto.AnchorKind, dto.AnchorID = string(n.Kind), ref
	}
	return dto
}

// Kinds handles GET /v1/workspace/kinds.
//
// Unauthenticated: it describes the build, not anybody's project. A person
// deciding whether something is a risk or a hazard needs this, and it reveals
// nothing.
func (h *WorkspaceHandlers) Kinds(w http.ResponseWriter, r *http.Request) {
	type kindDTO struct {
		Kind         string   `json:"kind"`
		PRDName      string   `json:"prd_name"`
		ContentLives string   `json:"content_lives"`
		MayBeKnownAs []string `json:"may_be_known_as"`
		Describes    string   `json:"describes"`
	}
	type edgeDTO struct {
		Kind  string   `json:"kind"`
		From  []string `json:"from"`
		To    []string `json:"to"`
		Means string   `json:"means"`
	}
	kinds := []kindDTO{}
	for _, d := range workspace.Kinds() {
		lives := "graph"
		if d.Anchor != workspace.AnchorNone {
			lives = string(d.Anchor)
		}
		labels := []string{}
		for _, e := range d.Allowed {
			labels = append(labels, string(e))
		}
		if len(labels) == 0 {
			for _, e := range claim.AllEpistemics() {
				labels = append(labels, string(e))
			}
		}
		kinds = append(kinds, kindDTO{string(d.Kind), d.PRDName, lives, labels, d.Gloss})
	}
	edgeKinds := []edgeDTO{}
	for _, d := range workspace.EdgeKinds() {
		edgeKinds = append(edgeKinds, edgeDTO{string(d.Kind), kindNames(d.From), kindNames(d.To), d.Gloss})
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"node_kinds": kinds,
		"edge_kinds": edgeKinds,
		"rules": []string{
			"A node's kind never changes. Promote it instead: the new node derives_from the old one and both stay readable.",
			"Verification state is what a machine found. Human disposition is what a person decided. They are never the same field.",
		},
	})
}

func kindNames(ks []workspace.Kind) []string {
	if len(ks) == 0 {
		return []string{"any"}
	}
	out := make([]string, 0, len(ks))
	for _, k := range ks {
		out = append(out, string(k))
	}
	return out
}

// Graph handles GET /v1/workspace/graph?project_id=.
func (h *WorkspaceHandlers) Graph(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	projectID := r.URL.Query().Get("project_id")

	if err := h.authoriseProject(r, projectID, user.ID); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	g, err := h.svc.Load(r.Context(), projectID)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	nodes := make([]NodeDTO, 0, len(g.Nodes))
	for _, n := range g.Nodes {
		dto := toNodeDTO(n)
		dto.Label = g.Title(n.ID)
		nodes = append(nodes, dto)
	}
	edges := make([]EdgeDTO, 0, len(g.Edges))
	for _, e := range g.Edges {
		def, _ := workspace.EdgeKindOf(e.Kind)
		edges = append(edges, EdgeDTO{
			ID: e.ID, Kind: string(e.Kind), FromID: e.FromID, ToID: e.ToID,
			Reads: fmt.Sprintf(def.Reads, g.Title(e.FromID), g.Title(e.ToID)), Note: e.Note,
		})
	}
	WriteJSON(w, http.StatusOK, map[string]any{"nodes": nodes, "edges": edges})
}

// Review handles GET /v1/workspace/review?project_id=.
//
// Defects and gaps stay separate on the wire, for the same reason they are
// separate in the domain: a client that merged them would show a permanent red
// badge on every project that has ever existed.
func (h *WorkspaceHandlers) Review(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	projectID := r.URL.Query().Get("project_id")

	if err := h.authoriseProject(r, projectID, user.ID); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	rev, err := h.svc.Review(r.Context(), projectID)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	render := func(fs []workspace.Finding) []map[string]any {
		out := make([]map[string]any, 0, len(fs))
		for _, f := range fs {
			out = append(out, map[string]any{
				"problem": f.Problem, "node_ids": f.NodeIDs, "detail": f.Detail,
			})
		}
		return out
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"consistent": rev.Sound(),
		"summary":    rev.Summary(),
		"nodes":      rev.Nodes,
		"edges":      rev.Edges,
		"defects":    render(rev.Defects),
		"gaps":       render(rev.Gaps),
	})
}

type addNodeRequest struct {
	ProjectID string `json:"project_id"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	Body      string `json:"body,omitempty"`
	How       string `json:"how,omitempty"`
	Source    string `json:"source,omitempty"`
	Status    string `json:"status,omitempty"`
}

// AddNode handles POST /v1/workspace/nodes.
func (h *WorkspaceHandlers) AddNode(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())

	var req addNodeRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	// Adding to the graph is changing what the project says, so it needs
	// content.write rather than read. A viewer sees the graph and cannot edit it.
	if err := h.deps.requirePermission(r, req.ProjectID, user.ID, access.PermContentWrite); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	node, err := h.svc.Add(r.Context(), workspace.NewNode{
		ProjectID: req.ProjectID, Kind: workspace.Kind(req.Kind),
		Title: req.Title, Body: req.Body, How: claim.Epistemic(req.How),
		Source: req.Source, Status: workspace.Status(req.Status), CreatedBy: user.ID,
	})
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	WriteJSON(w, http.StatusCreated, map[string]any{"node": toNodeDTO(*node)})
}

type editNodeRequest struct {
	Title  string `json:"title,omitempty"`
	Body   string `json:"body,omitempty"`
	How    string `json:"how,omitempty"`
	Status string `json:"status,omitempty"`
}

// EditNode handles PATCH /v1/workspace/nodes/{id}.
//
// There is no `kind` field, and that is the design. A node's kind never changes:
// an assumption that turns out to be true does not become a requirement, because
// then nobody could ask what was built on a guess. Use POST .../promote.
func (h *WorkspaceHandlers) EditNode(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())

	var req editNodeRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	node, err := h.authoriseNode(r, r.PathValue("id"), user.ID)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	updated, err := h.svc.Edit(r.Context(), node.ID, req.Title, req.Body,
		claim.Epistemic(req.How), workspace.Status(req.Status))
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"node": toNodeDTO(*updated)})
}

type promoteRequest struct {
	Kind   string `json:"kind"`
	Title  string `json:"title,omitempty"`
	Body   string `json:"body,omitempty"`
	How    string `json:"how,omitempty"`
	Source string `json:"source,omitempty"`
	Status string `json:"status,omitempty"`
	// RetireSource marks the original as no longer in force. It stays readable
	// either way; this only says whether it still applies.
	RetireSource bool `json:"retire_source,omitempty"`
}

// Promote handles POST /v1/workspace/nodes/{id}/promote.
func (h *WorkspaceHandlers) Promote(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())

	var req promoteRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	node, err := h.authoriseNode(r, r.PathValue("id"), user.ID)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	created, edge, err := h.svc.Promote(r.Context(), node.ID, workspace.NewNode{
		ProjectID: node.ProjectID, Kind: workspace.Kind(req.Kind), Title: req.Title,
		Body: req.Body, How: claim.Epistemic(req.How), Source: req.Source,
		Status: workspace.Status(req.Status), CreatedBy: user.ID,
	}, req.RetireSource)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	WriteJSON(w, http.StatusCreated, map[string]any{
		"node":         toNodeDTO(*created),
		"derives_from": edge.ToID,
		"effect": "The original is unchanged and still readable. Its history as a " +
			string(node.Kind) + " is what lets anyone ask later what this was built on.",
	})
}

type relateRequest struct {
	Kind   string `json:"kind"`
	FromID string `json:"from_id"`
	ToID   string `json:"to_id"`
	Note   string `json:"note,omitempty"`
}

// Relate handles POST /v1/workspace/edges.
func (h *WorkspaceHandlers) Relate(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())

	var req relateRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	// Both endpoints are authorised, not just one: an edge from a node the
	// caller owns to one they do not would otherwise reveal that the other
	// exists, and let them attach to it.
	from, err := h.authoriseNode(r, req.FromID, user.ID)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	if _, err := h.authoriseNode(r, req.ToID, user.ID); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	edge, err := h.svc.Relate(r.Context(), workspace.EdgeKind(req.Kind), from.ID, req.ToID, req.Note, user.ID)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	def, _ := workspace.EdgeKindOf(edge.Kind)
	WriteJSON(w, http.StatusCreated, map[string]any{"edge": EdgeDTO{
		ID: edge.ID, Kind: string(edge.Kind), FromID: edge.FromID, ToID: edge.ToID,
		Reads: def.Gloss, Note: edge.Note,
	}})
}

// Unrelate handles DELETE /v1/workspace/edges/{id}.
//
// Edges may be withdrawn where nodes may not be re-kinded: an edge is an
// assertion about how two things relate, and that genuinely changes. A node's
// kind is what it IS, and rewriting that erases history.
func (h *WorkspaceHandlers) Unrelate(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	edgeID := r.PathValue("id")

	// Authorised through the edge's own endpoint: the node check resolves the
	// project from the row and reports anything unreachable as absent.
	edge, err := h.findEdge(r, edgeID)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	if _, err := h.authoriseNode(r, edge.FromID, user.ID); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	if err := h.svc.Repo().DeleteEdge(r.Context(), h.deps.Pool, edgeID); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"deleted": edgeID})
}

// ArtifactHistory handles GET /v1/workspace/artifacts/{id}.
func (h *WorkspaceHandlers) ArtifactHistory(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())

	hist, err := h.svc.History(r.Context(), r.PathValue("id"))
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	if err := h.authoriseProject(r, hist.Artifact.ProjectID, user.ID); err != nil {
		WriteError(w, r, h.deps.Log, errs.New("httpapi.ArtifactHistory", errs.CodeNotFound).
			WithDetail("no artifact %s", r.PathValue("id")))
		return
	}
	versions := make([]map[string]any, 0, len(hist.Versions))
	for i := range hist.Versions {
		v := hist.Versions[i]
		usableErr := v.Usable()
		versions = append(versions, map[string]any{
			// WRK-04's seven, named as the requirement names them.
			"version":            v.Version,
			"initiator_id":       v.InitiatorID,
			"agent":              string(v.Agent),
			"tool_call_id":       v.ToolCallID,
			"inputs":             json.RawMessage(v.Inputs),
			"diff":               v.Diff,
			"verification_state": string(v.Verification),
			"verification_note":  v.VerificationNote,
			"human_disposition":  string(v.Disposition),
			"dispositioned_by":   v.DispositionedBy,
			"disposition_reason": v.DispositionReason,
			// Computed server-side. "May this be relied on?" needs BOTH facts,
			// and a client deciding for itself would eventually decide that a
			// passing test suite is a sign-off.
			"usable":     usableErr == nil,
			"usable_why": usableWhy(usableErr),
			"event_id":   v.EventID,
			"created_at": v.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"artifact": map[string]any{
			"id": hist.Artifact.ID, "path": hist.Artifact.Path, "kind": string(hist.Artifact.Kind),
		},
		"versions": versions,
	})
}

// usableWhy renders the reason in words a person can act on.
//
// The DETAIL, not the whole error. err.Error() prefixes the operation and the
// registry's generic cause — "workspace.Version.Usable: VALIDATION_FAILED: One
// or more request fields failed validation (…)" — which is right for a log and
// wrong here twice over: it buries the sentence that matters behind two lines of
// machinery, and its generic half is untrue, since nothing about a version
// nobody has looked at is a failed request field. Version.Usable writes the
// real sentence into Detail; this is the field that reads it.
//
// Surfaced by the workbench's Checks panel, which shows this string against
// every version in a project — where the prefix repeated N times was the whole
// panel.
func usableWhy(err error) string {
	if err == nil {
		return "verified by a machine and accepted by a person"
	}
	var e *errs.Error
	if errors.As(err, &e) && strings.TrimSpace(e.Detail) != "" {
		return e.Detail
	}
	return err.Error()
}

type disposeRequest struct {
	Disposition string `json:"disposition"`
	Reason      string `json:"reason,omitempty"`
}

// Dispose handles POST /v1/workspace/versions/{id}/disposition.
//
// The deciding person is the authenticated caller, never a field in the body.
// PRD SAF-05: a sign-off whose author can be asserted by the writer signs off
// on nothing.
func (h *WorkspaceHandlers) Dispose(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())

	var req disposeRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	version, err := h.svc.Repo().FindVersion(r.Context(), h.deps.Pool, r.PathValue("id"))
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	artifact, err := h.svc.Repo().FindArtifact(r.Context(), h.deps.Pool, version.ArtifactID)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	// Accepting or rejecting an artifact version is a human sign-off (PRD WRK-04,
	// SAF-05), so it needs artifact.dispose. A contributor who produced the work
	// is not automatically the person who signs it off.
	if err := h.deps.requirePermission(r, artifact.ProjectID, user.ID, access.PermArtifactDispose); err != nil {
		if errs.Is(err, errs.CodeNotFound) {
			WriteError(w, r, h.deps.Log, errs.New("httpapi.Dispose", errs.CodeNotFound).
				WithDetail("no version %s", r.PathValue("id")))
			return
		}
		WriteError(w, r, h.deps.Log, err)
		return
	}
	if err := h.svc.Dispose(r.Context(), version.ID,
		workspace.Disposition(req.Disposition), user.ID, req.Reason); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"version": version.Version, "disposition": req.Disposition, "by": user.ID,
	})
}

// ---------------------------------------------------------------------------
// Authorisation
// ---------------------------------------------------------------------------

func (h *WorkspaceHandlers) authoriseProject(r *http.Request, projectID, userID string) error {
	if projectID == "" {
		return errs.New("httpapi.workspace", errs.CodeValidationFailed).
			WithDetail("project_id is required; the graph is project-scoped")
	}
	// A project the caller is not a member of reads exactly like one that does
	// not exist, so the endpoint is not a membership oracle. That rule lives in
	// the access service now, along with everything else that decides it.
	return h.deps.requirePermission(r, projectID, userID, access.PermProjectRead)
}

// authoriseNode resolves a node and checks the caller may act on it, through the
// project the node actually belongs to rather than one the caller named.
func (h *WorkspaceHandlers) authoriseNode(r *http.Request, nodeID, userID string) (*workspace.Node, error) {
	return h.nodeFor(r, nodeID, userID, access.PermContentWrite)
}

// nodeFor resolves a node and checks a permission in ITS project, taken from the
// row rather than from the request: a caller must not be able to name a project
// they can write to and a node they cannot.
func (h *WorkspaceHandlers) nodeFor(r *http.Request, nodeID, userID string, p access.Permission) (*workspace.Node, error) {
	notFound := errs.New("httpapi.nodeFor", errs.CodeNotFound).WithDetail("no node %s", nodeID)

	node, err := h.svc.Repo().FindNode(r.Context(), h.deps.Pool, nodeID)
	if err != nil {
		return nil, notFound
	}
	if err := h.deps.requirePermission(r, node.ProjectID, userID, p); err != nil {
		if errs.Is(err, errs.CodeNotFound) {
			return nil, notFound
		}
		return nil, err
	}
	return node, nil
}

func (h *WorkspaceHandlers) findEdge(r *http.Request, edgeID string) (*workspace.Edge, error) {
	var e workspace.Edge
	var kind string
	err := h.deps.Pool.QueryRow(r.Context(),
		`select id, project_id, kind, from_id, to_id, note, created_by, created_at
		   from forge_edges where id = $1`, edgeID).
		Scan(&e.ID, &e.ProjectID, &kind, &e.FromID, &e.ToID, &e.Note, &e.CreatedBy, &e.CreatedAt)
	if err != nil {
		return nil, errs.New("httpapi.findEdge", errs.CodeNotFound).WithDetail("no edge %s", edgeID)
	}
	e.Kind = workspace.EdgeKind(kind)
	return &e, nil
}
