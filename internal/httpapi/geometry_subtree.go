package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/cad"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// GET /v1/geometry/{id}/mesh?subtree=<occurrence path> — one subtree's surface.
// Phase 6, stage W2.
//
// # The problem this solves
//
// The workbench uploaded every occurrence of a design at load, because the only mesh
// there was answered for the whole design — and past the 4,096 parts FORGE builds at
// once it answered nothing at all. A 30,000-part car had no built surface, and the
// viewport had no way to fetch a part of one when somebody opened a row.
//
// This answers for one occurrence path, in the K4 shape the whole reply already has
// (definitions tessellated once, a 4×4 column-major matrix per copy, `parts` for what a
// feature changed), so the browser draws a subtree exactly as it draws a whole reply.
//
// # Which tessellator answers, and why the reply says so
//
// The KERNEL, when this deployment has one and the subtree places no more than the
// kernel builds at once: then a hole is a hole. Otherwise the GO tessellator
// (geometry.TessellateInstances): the primitives' triangles, each shape once, with the
// features that were not performed named. `source` says which and `source_note` why,
// because a surface with no holes in it and a surface that was built look identical on
// screen, and the provenance banner is the only place the difference can be said.
//
// # Limits are the subtree's own
//
// The kernel's ceiling and the viewport's are applied to the parts THIS PATH places,
// not the design's: a 400-part wheel of a 30,000-part car is buildable. Neither ceiling
// changes. Fence: TestMeshSubtree_LimitsAreTheSubtreesOwn.

const (
	sourceKernel = "kernel"
	sourceGo     = "go"
)

func (h *GeometryHandlers) meshSubtree(w http.ResponseWriter, r *http.Request, v *geometry.Variant) {
	if !v.Units.Known() {
		WriteError(w, r, h.deps.Log, errs.New("httpapi.Mesh", errs.CodeValidationFailed).
			WithDetail("this variant has no unit FORGE can convert (%s), and a mesh is stated in "+
				"millimetres", strings.ToLower(strings.TrimSuffix(v.UnitsNote(), "."))))
		return
	}
	body, err := subtreeMesh(r.Context(), h.deps.CAD, v.Document, v.Units, r.URL.Query().Get("subtree"))
	if err != nil {
		h.logRefusal(r, v, "mesh", err)
		WriteError(w, r, h.deps.Log, err)
		return
	}
	body["version_id"] = v.VersionID
	h.deps.Log.Info(r.Context(), logx.EventGeometryMeshed,
		"version_id", v.VersionID, "project_id", v.ProjectID, "subtree", body["subtree"],
		"source", body["source"], "occurrences", body["occurrences"], "triangles", body["triangles"])
	WriteJSON(w, http.StatusOK, body)
}

// subtreeSource is which tessellator answers for a subtree of this many parts, and why.
func subtreeSource(kernel bool, parts int) (source, note string) {
	switch {
	case !kernel:
		return sourceGo, "this deployment has no CAD kernel, so these are the primitives' triangles, " +
			"tessellated in Go; a feature is not performed on them"
	case parts > geometry.MaxBuiltParts():
		return sourceGo, fmt.Sprintf("this subtree places %d parts and the kernel builds at most %d at once, so "+
			"these are the primitives' triangles, tessellated in Go; a feature is not performed on them",
			parts, geometry.MaxBuiltParts())
	default:
		return sourceKernel, "built by the CAD kernel"
	}
}

// subtreeMesh is the subtree reply without its version id, shared with its fences.
func subtreeMesh(ctx context.Context, kernel *cad.Kernel, doc geometry.Document, unit geometry.Unit, path string) (map[string]any, error) {
	sub, err := doc.Subtree(path)
	if err != nil {
		return nil, err
	}
	if refusal := sub.Refusal(); refusal != "" {
		return nil, errs.New("httpapi.subtreeMesh", errs.CodeValidationFailed).WithDetail("%s", refusal)
	}
	source, note := subtreeSource(kernel.Available(), len(sub.Parts))
	var built *cad.Build
	if source == sourceKernel {
		built, err = kernel.BuildMesh(ctx, sub.Document(), unit)
		if err != nil {
			// Drawn from the Go tessellator instead, and said: a subtree the kernel
			// refused still has parts somebody opened a row to look at.
			source, note = sourceGo, "the CAD kernel could not build this subtree ("+errs.DetailOf(err)+
				"), so these are the primitives' triangles, tessellated in Go"
			built = nil
		}
	}
	if built == nil {
		built = goBuild(sub.Document(), unit)
	}
	parts, definitions, instances := meshPayload(built)
	return map[string]any{
		"subtree":     sub.Path,
		"occurrences": len(sub.Parts),
		"source":      source,
		"source_note": note,
		"parts":       parts,
		"definitions": definitions,
		"instances":   instances,
		"triangles":   built.Triangles,
		"deflection":  built.Deflection,
		"simplified":  built.Simplified,
		"skipped":     orEmptyStrings(built.Skipped),
		// A feature that reaches outside the path is not in this reply's surface, and
		// is named rather than dropped.
		"features_outside":   orEmptyStrings(sub.Outside),
		"feature_failures":   orEmptyStrings(built.FeatureFailures),
		"feature_reductions": orEmptyStrings(built.FeatureReductions),
		"mesh_error":         built.MeshError,
		"inferred":           orEmptyStrings(built.Inferred),
	}, nil
}

// goBuild is the Go tessellator's answer in the kernel's shape, so one payload writer
// serves both.
func goBuild(doc geometry.Document, unit geometry.Unit) *cad.Build {
	m := geometry.TessellateInstances(doc, unit)
	b := &cad.Build{Triangles: m.Triangles, Inferred: m.Inferences}
	for _, d := range m.Definitions {
		b.MeshDefinitions = append(b.MeshDefinitions, cad.MeshDefinition{Vertices: d.Vertices, Triangles: d.Triangles})
	}
	for _, in := range m.Instances {
		b.MeshInstances = append(b.MeshInstances, cad.MeshInstance{ID: in.ID, Label: in.Label,
			Definition: in.Definition, Matrix: in.Matrix})
	}
	return b
}
