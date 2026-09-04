package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/access"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Geometry over HTTP (PRD VIS-04, VIS-05).
//
// # Why the six links are rendered server-side
//
// VIS-04 requires every render to link to its geometry version, inputs, units,
// assumptions, generator and verification status. All six could be shipped as
// raw fields and assembled in the browser — and then the workbench, forgectl and
// any other client would each decide for themselves what "unverified" means
// beside "accepted", which is the SAF-05 distinction being re-derived in three
// places by people who have not read it. So the wording of a fact travels with
// the fact.

// GeometryHandlers serve variants, comparison and export.
type GeometryHandlers struct {
	deps Deps
	svc  *geometry.Service
}

// NewGeometryHandlers wires the geometry endpoints.
func NewGeometryHandlers(d Deps) *GeometryHandlers {
	return &GeometryHandlers{deps: d, svc: geometry.NewService(d.Pool, d.Clock, d.Log)}
}

// VariantDTO is one render as a reader sees it, with VIS-04's six resolved.
type VariantDTO struct {
	// 1. geometry version
	VersionID string `json:"version_id"`
	ProjectID string `json:"project_id"`
	Path      string `json:"path"`
	Version   int    `json:"version"`
	Name      string `json:"name"`

	// 2. inputs — what it was made from.
	Inputs json.RawMessage `json:"inputs"`

	// 3. units, resolved and as declared, plus the sentence explaining any gap
	// between them so no client has to compose its own.
	Units         string `json:"units"`
	UnitsDeclared string `json:"units_declared"`
	UnitsNote     string `json:"units_note,omitempty"`
	Frame         string `json:"frame"`

	// 4. assumptions — always an array, never null. "Nothing was assumed" and
	// "nobody recorded them" must not arrive looking the same.
	Assumptions []string `json:"assumptions"`
	NotVerified []string `json:"not_verified"`

	// 5. generator
	Generator string `json:"generator"`
	Agent     string `json:"agent"`

	// 6. verification status — two fields, never merged (PRD SAF-05).
	Verification     string  `json:"verification"`
	VerificationNote string  `json:"verification_note,omitempty"`
	Disposition      string  `json:"disposition"`
	DispositionedBy  *string `json:"dispositioned_by"`

	InitiatorID string `json:"initiator_id"`
	CreatedAt   string `json:"created_at"`

	// Document is the geometry itself, in the shape the viewport already draws,
	// so a variant can be loaded into the studio without a translation layer.
	// Its Overlays are the ones somebody AUTHORED.
	Document geometry.Document `json:"document"`

	// Measured is what FORGE derived from the parts, computed here rather than
	// stored (PRD VIS-03).
	//
	// A separate field on purpose. Merging derived dimensions into the document
	// would make "somebody measured this part" and "FORGE did arithmetic on its
	// own guess" arrive in one array, distinguishable only by an epistemic label
	// a client might not render — and the whole point of VIS-03 is that a
	// dimension line is the most authoritative mark on a drawing. Two fields
	// mean a client cannot show them identically by accident.
	Measured []geometry.Overlay `json:"measured"`
}

func toVariantDTO(v geometry.Variant) VariantDTO {
	return VariantDTO{
		VersionID: v.VersionID, ProjectID: v.ProjectID, Path: v.Path, Version: v.Version,
		Name: v.Name, Inputs: json.RawMessage(v.Inputs),
		Units: string(v.Units), UnitsDeclared: v.UnitsDeclared, UnitsNote: v.UnitsNote(),
		Frame:       string(v.Frame),
		Assumptions: v.Assumptions(), NotVerified: v.NotVerified(),
		Generator: v.Generator, Agent: string(v.Agent),
		Verification: string(v.Verification), VerificationNote: v.VerificationNote,
		Disposition: string(v.Disposition), DispositionedBy: v.DispositionedBy,
		InitiatorID: v.InitiatorID,
		CreatedAt:   v.CreatedAt.UTC().Format(time.RFC3339),
		Document:    v.Document,
		Measured:    measuredOrEmpty(v),
	}
}

// measuredOrEmpty derives this variant's dimensions in its own resolved unit.
//
// Never null. "This model has no derivable extents" and "nobody computed any"
// must not arrive looking the same — the same rule Assumptions follows above.
func measuredOrEmpty(v geometry.Variant) []geometry.Overlay {
	out := geometry.Measure(v.Document, v.Units)
	if out == nil {
		return []geometry.Overlay{}
	}
	return out
}

// Formats handles GET /v1/geometry/formats.
//
// Unauthenticated: it describes what this BUILD can write, not anybody's
// geometry. The unavailable formats are in the list on purpose — a person who
// cannot find STEP concludes it was forgotten, and a model that cannot find it
// invents something. Both are answered by it being present and saying why not.
func (h *GeometryHandlers) Formats(w http.ResponseWriter, r *http.Request) {
	type dto struct {
		Name      string `json:"name"`
		Extension string `json:"extension"`
		MediaType string `json:"media_type"`
		Kind      string `json:"kind"`
		Available bool   `json:"available"`
		Gloss     string `json:"gloss"`
		Reason    string `json:"reason,omitempty"`
	}
	out := []dto{}
	for _, f := range geometry.Formats() {
		out = append(out, dto{f.Name, f.Extension, f.MediaType, string(f.Kind), f.Available, f.Gloss, f.Reason})
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"formats": out,
		"note": "This deployment has no CAD kernel. Mesh export is real; parametric export is declared " +
			"and refused, because a STEP file full of tessellated facets would be treated downstream " +
			"as an exact solid.",
		"max_compare": geometry.MaxCompare,
	})
}

// List handles GET /v1/geometry?project_id=&limit=.
func (h *GeometryHandlers) List(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	projectID := r.URL.Query().Get("project_id")

	if err := h.deps.requirePermission(r, projectID, user.ID, access.PermProjectRead); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	variants, err := h.svc.List(r.Context(), projectID, limit)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	out := make([]VariantDTO, 0, len(variants))
	for _, v := range variants {
		out = append(out, toVariantDTO(v))
	}
	WriteJSON(w, http.StatusOK, map[string]any{"variants": out})
}

// Get handles GET /v1/geometry/{id}.
func (h *GeometryHandlers) Get(w http.ResponseWriter, r *http.Request) {
	v, err := h.authorisedVariant(r)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"variant": toVariantDTO(*v)})
}

// Compare handles GET /v1/geometry/compare?ids=a,b,c.
//
// A GET because it reads and stores nothing: the comparison is derived from the
// variants it names, every time. A POST would suggest something was created, and
// somebody would eventually try to fetch it back.
func (h *GeometryHandlers) Compare(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())

	ids := splitIDs(r.URL.Query().Get("ids"))
	cmp, err := h.svc.Compare(r.Context(), ids)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	// Authorised AFTER the read and against the project the variants actually
	// belong to, not one the caller named. The service has already refused a set
	// spanning two projects, so one check covers every column.
	if err := h.deps.requirePermission(r, cmp.ProjectID, user.ID, access.PermProjectRead); err != nil {
		WriteError(w, r, h.deps.Log, errs.New("httpapi.Compare", errs.CodeNotFound).
			WithDetail("no geometry variant %s", strings.Join(ids, ", ")))
		return
	}

	variants := make([]VariantDTO, 0, len(cmp.Variants))
	for _, v := range cmp.Variants {
		variants = append(variants, toVariantDTO(v))
	}
	provenance := make([]map[string]any, 0, len(cmp.Provenance))
	for _, f := range cmp.Provenance {
		provenance = append(provenance, map[string]any{
			"field": f.Field, "values": f.Values, "differs": f.Differs, "why": f.Why,
		})
	}
	parts := make([]map[string]any, 0, len(cmp.Parts))
	for _, p := range cmp.Parts {
		cells := make([]map[string]any, 0, len(p.Cells))
		for _, c := range p.Cells {
			cells = append(cells, map[string]any{
				"present": c.Present, "shape": c.Shape,
				"dimensions": c.Dimensions, "position": c.Position,
			})
		}
		parts = append(parts, map[string]any{
			"part_id": p.PartID, "label": p.Label, "cells": cells,
			// How this part was decided to be the same part across variants.
			// Never omitted: a name match is a guess, and a reader who is not
			// told will read it as identity.
			"matched_by":  string(p.MatchedBy),
			"differences": orEmptyStrings(p.Differences),
			// 1-based column numbers, matching what the differences say, so a
			// reader never has to translate between two indexing schemes.
			"missing_from": orEmptyInts(p.MissingFrom),
			"differs":      p.Differs(),
		})
	}
	h.deps.Log.Info(r.Context(), logx.EventGeometryCompared,
		"user_id", user.ID, "project_id", cmp.ProjectID, "variants", len(cmp.Variants))

	WriteJSON(w, http.StatusOK, map[string]any{
		"project_id": cmp.ProjectID,
		"variants":   variants,
		"provenance": provenance,
		"parts":      parts,
		// Three lists, never merged. "These differ" is a finding; "these could
		// not be compared" is a judgement withheld; "these were matched by
		// name" is a judgement qualified. A reader scanning a box headed "not
		// compared" and finding rows that WERE compared learns to skim the box.
		"not_comparable": orEmptyStrings(cmp.NotComparable),
		"match_notes":    orEmptyStrings(cmp.MatchNotes),
	})
}

type adoptRequest struct {
	Reason string `json:"reason"`
}

// Adopt handles POST /v1/geometry/{id}/adopt.
//
// # Why this endpoint exists at all
//
// Appending a version supersedes the previous one, and a superseded version can
// no longer be accepted or rejected. Correct for a file; wrong for variants,
// which are alternatives you choose between — so a person who compared v1 and v3
// and preferred v1 had the choice shown to them and refused when they made it.
//
// It needs content.write rather than artifact.dispose: adopting PROPOSES the
// earlier shape again, it does not sign anything off. The sign-off is the
// separate act on the version this creates, through the disposition endpoint
// that already exists — the same two-step separation PRD AGT-02 draws between
// planning work and starting it.
func (h *GeometryHandlers) Adopt(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())

	var req adoptRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	v, err := h.svc.Find(r.Context(), r.PathValue("id"))
	if err != nil {
		if errs.Is(err, errs.CodeNotFound) {
			WriteError(w, r, h.deps.Log, errs.New("httpapi.Adopt", errs.CodeNotFound).
				WithDetail("no geometry variant %s", r.PathValue("id")))
			return
		}
		WriteError(w, r, h.deps.Log, err)
		return
	}
	if err := h.deps.requirePermission(r, v.ProjectID, user.ID, access.PermContentWrite); err != nil {
		if errs.Is(err, errs.CodeNotFound) {
			WriteError(w, r, h.deps.Log, errs.New("httpapi.Adopt", errs.CodeNotFound).
				WithDetail("no geometry variant %s", r.PathValue("id")))
			return
		}
		WriteError(w, r, h.deps.Log, err)
		return
	}

	adopted, err := h.svc.Adopt(r.Context(), v.VersionID, user.ID, req.Reason)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	WriteJSON(w, http.StatusCreated, map[string]any{
		"variant": toVariantDTO(*adopted),
		"note": "This is a NEW version carrying the earlier variant's geometry, so a person can rule " +
			"on it. Nothing has been accepted yet — accept or reject it with " +
			"POST /v1/workspace/versions/" + adopted.VersionID + "/disposition.",
	})
}

// ExportLabel handles GET /v1/geometry/{id}/export/label?format=.
//
// Exists so a person is told what a conversion loses BEFORE they download it,
// and so the workbench can put it beside the button rather than after the click.
// It is also the machine-readable copy of what the OBJ header says, for the
// formats that cannot carry it.
func (h *GeometryHandlers) ExportLabel(w http.ResponseWriter, r *http.Request) {
	v, err := h.authorisedVariant(r)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	format := r.URL.Query().Get("format")
	label, mesh, err := geometry.LabelFor(v, format)
	if err != nil {
		h.logRefusal(r, v, format, err)
		WriteError(w, r, h.deps.Log, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"label":     labelDTO(label),
		"triangles": len(mesh.Triangles()),
	})
}

// Export handles GET /v1/geometry/{id}/export?format=.
//
// Returns the file itself. The label travels in three places and none of them is
// enough on its own: inside the file where the format has room, in the
// X-Forge-Export-Label header for a machine, and at the label endpoint above for
// a person about to click.
func (h *GeometryHandlers) Export(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	v, err := h.authorisedVariant(r)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	format := r.URL.Query().Get("format")
	res, err := geometry.Export(v, format)
	if err != nil {
		h.logRefusal(r, v, format, err)
		WriteError(w, r, h.deps.Log, err)
		return
	}

	h.deps.Log.Info(r.Context(), logx.EventGeometryExported,
		"user_id", user.ID, "version_id", v.VersionID, "project_id", v.ProjectID,
		"format", res.Format.Name, "triangles", res.Triangles, "bytes", len(res.Content))

	w.Header().Set("Content-Type", res.Format.MediaType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", res.Filename))
	w.Header().Set("Content-Length", strconv.Itoa(len(res.Content)))
	// One line, because a header is not a place for a paragraph. It says the
	// thing that must not be missed and where the rest of it is.
	w.Header().Set("X-Forge-Export-Label", fmt.Sprintf(
		"unverified proposal; tessellated; full label at /v1/geometry/%s/export/label?format=%s",
		v.VersionID, res.Format.Name))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(res.Content)
}

func labelDTO(l *geometry.Label) map[string]any {
	tess := make([]map[string]any, 0, len(l.Tessellation))
	for _, d := range l.Tessellation {
		tess = append(tess, map[string]any{
			"part_id": d.PartID, "label": d.Label, "shape": d.Shape,
			"segments": d.Segments,
			// Rendered, not raw: a deviation that travels without its unit is
			// the exact failure WRK-05 exists to prevent, and it would be a
			// remarkable one to commit inside a label about honesty.
			"max_deviation": d.Max.String(),
		})
	}
	return map[string]any{
		"format": l.Format, "format_kind": string(l.FormatKind),
		"headline":     l.Headline(),
		"units":        string(l.Units),
		"frame":        string(l.Frame),
		"generator":    l.Generator,
		"verification": l.Verification,
		"disposition":  l.Disposition,
		"tessellation": tess,
		"inference":    orEmptyStrings(l.Inference),
		"lossy":        orEmptyStrings(l.Lossy),
		"assumptions":  orEmptyStrings(l.Assumptions),
		"not_verified": orEmptyStrings(l.NotVerified),
	}
}

// authorisedVariant resolves {id} and checks the caller may read the project it
// actually belongs to, rather than one they named.
//
// A variant the caller cannot reach reads exactly like one that does not exist,
// so the endpoint is not a way to enumerate other people's designs.
func (h *GeometryHandlers) authorisedVariant(r *http.Request) (*geometry.Variant, error) {
	const op = "httpapi.geometry"

	id := r.PathValue("id")
	notFound := errs.New(op, errs.CodeNotFound).WithDetail("no geometry variant %s", id)

	v, err := h.svc.Find(r.Context(), id)
	if err != nil {
		if errs.Is(err, errs.CodeNotFound) {
			return nil, notFound
		}
		return nil, err
	}
	user, _ := UserFrom(r.Context())
	if err := h.deps.requirePermission(r, v.ProjectID, user.ID, access.PermProjectRead); err != nil {
		if errs.Is(err, errs.CodeNotFound) {
			return nil, notFound
		}
		return nil, err
	}
	return v, nil
}

// logRefusal records a declined export.
//
// Logged at all because a refusal is the system working, and an operator asked
// "does anybody try to get STEP out of this?" has no other way to know. It is
// also how a wrongly-drawn boundary gets noticed: a refusal nobody ever meets is
// a different problem from one everybody meets daily.
func (h *GeometryHandlers) logRefusal(r *http.Request, v *geometry.Variant, format string, err error) {
	user, _ := UserFrom(r.Context())
	h.deps.Log.Info(r.Context(), logx.EventGeometryRefused,
		"user_id", user.ID, "version_id", v.VersionID, "format", format,
		"code", string(errs.CodeOf(err)))
}

// splitIDs parses a comma-separated id list.
func splitIDs(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// orEmptyStrings and orEmptyInts keep a JSON array an array.
//
// A nil slice encodes as null, and every client then needs its own opinion about
// whether null means "none" or "not computed". Settled here once.
func orEmptyStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func orEmptyInts(v []int) []int {
	if v == nil {
		return []int{}
	}
	return v
}
