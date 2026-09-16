package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/agent"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/access"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/blob"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Off-node STEP export over HTTP.
//
//	POST /v1/geometry/{id}/exports                ask forge-worker to write the version as STEP
//	GET  /v1/geometry/exports/{exportID}          queued, running, succeeded or failed, and why
//	GET  /v1/geometry/exports/{exportID}/file     the file, streamed from blob storage through forged
//
// # Why the file comes through forged and not from a bucket URL
//
// A presigned URL would hand the download to S3 and skip forged's permission
// check for as long as the URL lives: anyone the link reaches could read the
// design, including somebody removed from the project after it was minted. The
// production role cannot mint one scoped to a person anyway. So the download is
// authorised on every request, like the export route it extends, and forged pays
// the bytes' transit — 62 MB at 90,880 occurrences (#89), streamed, never held.
//
// See internal/agent/stepexport.go for the job itself.

// ExportDTO is an export job as a client sees it.
type ExportDTO struct {
	ID        string `json:"id"`
	VersionID string `json:"version_id"`
	ProjectID string `json:"project_id"`
	Format    string `json:"format"`
	// Status is queued, running, succeeded or failed.
	Status string `json:"status"`
	// Reason says why a job failed, or why a queued one is queued again.
	Reason   string `json:"reason,omitempty"`
	Filename string `json:"filename"`
	// SizeBytes, SHA256 and Parts are null until the file is stored. Null, not
	// zero: "an empty file" and "no file yet" must not arrive looking the same.
	SizeBytes *int64  `json:"size_bytes"`
	SHA256    *string `json:"sha256"`
	Parts     *int    `json:"parts"`
	// What the kernel left out of the file. Always arrays.
	Skipped         []string `json:"skipped"`
	FeatureFailures []string `json:"feature_failures"`
	Attempts        int      `json:"attempts"`
	MaxAttempts     int      `json:"max_attempts"`
	GoalID          string   `json:"goal_id"`
	RequestedBy     string   `json:"requested_by"`
	CreatedAt       string   `json:"created_at"`
	StoredAt        *string  `json:"stored_at"`
	StatusURL       string   `json:"status_url"`
	// DownloadURL is set only once there is something to download.
	DownloadURL string `json:"download_url,omitempty"`
}

func toExportDTO(e *agent.Export) ExportDTO {
	dto := ExportDTO{
		ID: e.ID, VersionID: e.VersionID, ProjectID: e.ProjectID, Format: e.Format,
		Status: e.Status, Reason: e.Reason, Filename: e.Filename,
		Skipped: orEmptyStrings(e.Skipped), FeatureFailures: orEmptyStrings(e.FeatureFailures),
		Attempts: e.Attempts, MaxAttempts: e.MaxAttempts, GoalID: e.GoalID, RequestedBy: e.RequestedBy,
		CreatedAt: e.CreatedAt.UTC().Format(time.RFC3339),
		StatusURL: "/v1/geometry/exports/" + e.ID,
	}
	if e.BlobKey != "" {
		size, sum, parts := e.SizeBytes, e.SHA256(), e.Parts
		dto.SizeBytes, dto.SHA256, dto.Parts = &size, &sum, &parts
	}
	if e.StoredAt != nil {
		at := e.StoredAt.UTC().Format(time.RFC3339)
		dto.StoredAt = &at
	}
	if e.Status == agent.ExportSucceeded {
		dto.DownloadURL = "/v1/geometry/exports/" + e.ID + "/file"
	}
	return dto
}

func (h *GeometryHandlers) stepExports() *agent.StepExports {
	return agent.NewStepExports(h.deps.Pool, h.deps.Clock, h.deps.Log)
}

// blobStore is this deployment's store, or the one that refuses.
func (h *GeometryHandlers) blobStore() blob.Store {
	if h.deps.Blobs == nil {
		return blob.Disabled()
	}
	return h.deps.Blobs
}

// RequestExport handles POST /v1/geometry/{id}/exports?format=step.
//
// 202 with a new export, or 200 with the one already queued, running or done for
// this version: asking twice is not two jobs.
func (h *GeometryHandlers) RequestExport(w http.ResponseWriter, r *http.Request) {
	const op = "httpapi.RequestExport"
	user, _ := UserFrom(r.Context())

	// The export route's own check, reused rather than restated: project.read on
	// the project the variant actually belongs to, and a variant the caller cannot
	// reach reads as one that does not exist.
	v, err := h.authorisedVariant(r)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	// ‼️ And that is the WHOLE permission check: read access, nothing more —
	// though this does write a goal. Decided 2026-09-15, replacing the pair of
	// checks this route shipped with.
	//
	// Exporting is reading. A person who may open a design in the viewport may
	// take the same design away as a file; the file is the design, in another
	// notation. Requiring goal.create as well meant a viewer could read a design
	// on screen and not have it — "you may read this, on our screen, in this
	// building" — and it made the ONE thing a reader most often wants from a CAD
	// system the one thing a reader could not have.
	//
	// # Why this is not the defect of #91 coming back
	//
	// POST /v1/goals still requires goal.create, and must
	// (docs/bugfix/2026-09-15-a-goal-could-be-drafted-into-a-project-its-caller-was-not-in.md).
	// The difference is who AUTHORS the work. There, the caller writes the
	// statement, the planner's model is asked on their behalf, and whatever they
	// asked for lands in the project's console, timeline and budget as their work:
	// "may this person plan work here" is exactly the question, and a viewer's
	// answer is no.
	//
	// Here the caller authors nothing. FORGE writes the goal, its statement, its
	// plan and its single task itself (agent.StepExports.create), every one of them
	// derived from a version the caller may already read — at r0, with no model
	// call, and with the design unchanged when it finishes. The goal is FORGE's own
	// mechanism for doing the reading off-node, because forged's pod cannot hold a
	// 90,000-part kernel; it is not a thing a person is putting into the project. A
	// mechanism must not demand a permission stronger than the act it implements,
	// or the implementation detail becomes the policy.
	//
	// The requester is still recorded, and deliberately: the goal's created_by and
	// the export's requested_by are this caller, and the timeline names them
	// ("Asked for … as a STEP file"). A read-only caller cannot author work here,
	// but who asked for a design to leave the building is precisely the fact
	// accountability needs, so it is kept rather than attributed to the system.
	//
	// What this does NOT open, all still checked where they live: a viewer cannot
	// start, replan or cancel a goal, cannot write or adopt a version, and cannot
	// export a design they may not read — the check above is the whole gate, and a
	// design they cannot reach is reported as one that does not exist. The worker
	// time a reader can spend is bounded by agent.MaxLiveExportJobsPerRequester.
	format := r.URL.Query().Get("format")
	if format == "" {
		format = agent.ExportFormatSTEP
	}

	// Refused by name, before a goal exists. A job queued on a deployment with no
	// bucket would build for minutes and fail for the same reason, or — worse —
	// succeed at building and be lost.
	if !h.blobStore().Available() {
		err := blob.Unavailable(op)
		h.logRefusal(r, v, format, err)
		WriteError(w, r, h.deps.Log, err)
		return
	}

	exp, created, err := h.stepExports().Request(r.Context(), v, format, user.ID)
	if err != nil {
		h.logRefusal(r, v, format, err)
		WriteError(w, r, h.deps.Log, err)
		return
	}
	h.deps.Log.Info(r.Context(), logx.EventGeometryExportQueued,
		"user_id", user.ID, "export_id", exp.ID, "version_id", v.VersionID, "project_id", v.ProjectID,
		"goal_id", exp.GoalID, "created", created, "status", exp.Status)

	code := http.StatusOK
	message := fmt.Sprintf("This version already has a STEP export (%s). Nothing new was queued.", exp.Status)
	if created {
		code = http.StatusAccepted
		message = "Queued. forge-worker writes the file with its CAD kernel and keeps it in blob storage; " +
			"nothing runs until a forge-worker is running. Follow status_url."
	}
	WriteJSON(w, code, map[string]any{"export": toExportDTO(exp), "created": created, "message": message})
}

// ExportRoute handles GET /v1/geometry/{id}/{rest}, which exists to serve
// GET /v1/geometry/exports/{exportID}.
//
// ‼️ That pattern cannot be registered as written. net/http refuses it beside
// GET /v1/geometry/{id}/export — both match /v1/geometry/exports/export and
// neither is more specific — and refuses by panicking when the router is built,
// so forged would not start. {id}/{rest} is less specific than every
// {id}/<literal> route, so those keep their paths, and this answers only when
// {id} is the word "exports". An export id always starts "exp_", so it is never
// "export", "mesh" or "mass". Fenced by TestAPI_EveryGeometryRouteIsMountedAndRequiresASession
// through NewRouter, which is what panics.
func (h *GeometryHandlers) ExportRoute(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("id") != "exports" {
		WriteError(w, r, h.deps.Log, errs.New("httpapi.geometry", errs.CodeNotFound).
			WithDetail("no route %s %s", r.Method, r.URL.Path))
		return
	}
	r.SetPathValue("exportID", r.PathValue("rest"))
	h.ExportStatus(w, r)
}

// ExportStatus handles GET /v1/geometry/exports/{exportID}.
func (h *GeometryHandlers) ExportStatus(w http.ResponseWriter, r *http.Request) {
	e, err := h.authorisedExport(r)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"export": toExportDTO(e)})
}

// DownloadExport handles GET /v1/geometry/exports/{exportID}/file.
func (h *GeometryHandlers) DownloadExport(w http.ResponseWriter, r *http.Request) {
	const op = "httpapi.DownloadExport"
	user, _ := UserFrom(r.Context())

	e, err := h.authorisedExport(r)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	if e.Status != agent.ExportSucceeded {
		detail := fmt.Sprintf("export %s is %s, so there is no file to download yet. Its status is at "+
			"/v1/geometry/exports/%s", e.ID, e.Status, e.ID)
		if e.Reason != "" {
			detail += ": " + e.Reason
		}
		WriteError(w, r, h.deps.Log, errs.New(op, errs.CodeConflict).WithDetail("%s", detail))
		return
	}
	store := h.blobStore()
	if !store.Available() {
		WriteError(w, r, h.deps.Log, blob.Unavailable(op))
		return
	}
	body, err := store.Get(r.Context(), e.BlobKey)
	if err != nil {
		WriteError(w, r, h.deps.Log, err)
		return
	}
	defer body.Close()

	f, _ := geometry.FormatOf(agent.ExportFormatSTEP)
	w.Header().Set("Content-Type", f.MediaType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", e.Filename))
	w.Header().Set("Content-Length", strconv.FormatInt(e.SizeBytes, 10))
	w.Header().Set("X-Forge-Export-SHA256", e.SHA256())
	w.Header().Set("X-Forge-Export-Label", exportJobLabel(e))
	w.WriteHeader(http.StatusOK)

	if err := streamVerified(w, body, e.SizeBytes, e.SHA256()); err != nil {
		// The status line has gone, so the only honest thing left is to cut the
		// connection: the client then holds fewer bytes than Content-Length, which
		// every HTTP client reports as a failed download, never as a finished one.
		h.deps.Log.ErrorWith(r.Context(), logx.EventBlobCorrupt, err,
			"user_id", user.ID, "export_id", e.ID, "blob_key", string(e.BlobKey))
		panic(http.ErrAbortHandler)
	}
	h.deps.Log.Info(r.Context(), logx.EventGeometryExported,
		"user_id", user.ID, "version_id", e.VersionID, "project_id", e.ProjectID, "export_id", e.ID,
		"format", e.Format, "parts", e.Parts, "bytes", e.SizeBytes, "off_node", true)
}

// exportJobLabel is exportParametric's label, saying one thing more: this file's
// build ran no interference check (cad.Kernel.ExportSTEPJob).
func exportJobLabel(e *agent.Export) string {
	label := fmt.Sprintf("unverified proposal; B-Rep, not tessellated; nothing about this shape has been "+
		"analysed or checked, and no interference check ran for this file; full label at "+
		"/v1/geometry/%s/export/label?format=step", e.VersionID)
	if n := len(e.FeatureFailures); n > 0 {
		label = fmt.Sprintf("%d feature(s) could NOT be applied, so this shape is not what the "+
			"design describes (%s); ", n, strings.Join(e.FeatureFailures, "; ")) + label
	}
	if len(e.Skipped) > 0 {
		label = fmt.Sprintf("%d part(s) could not be built and are NOT in this file; ", len(e.Skipped)) + label
	}
	return label
}

// streamVerified copies src to dst, holding back the last chunk until the bytes
// are known to be the file the job stored: its size, and the digest it recorded.
//
// # Why here and not only in the store
//
// The S3 store checks a blob's hash as it is read, but that is a property of one
// adapter, and a download is a claim this handler makes whatever store it holds.
// The check is against the digest the JOB recorded, not the key the bytes were
// fetched by, so it also refuses a row that names the wrong blob.
//
// ‼️ The last chunk is the one held, because a corrupt file whose every byte but
// the last megabyte reached the client is still a truncated download: the client
// holds fewer bytes than Content-Length and says so. Written in full, it would be
// a complete-looking file with the wrong content.
func streamVerified(dst io.Writer, src io.Reader, size int64, wantHex string) error {
	const op = "httpapi.streamVerified"
	h := sha256.New()
	buf := make([]byte, 1<<20)
	held := make([]byte, 0, 1<<20)
	var total int64
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			if len(held) > 0 {
				if _, err := dst.Write(held); err != nil {
					return err
				}
			}
			held = append(held[:0], buf[:n]...)
			_, _ = h.Write(buf[:n])
			total += int64(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != wantHex || total != size {
		return errs.New(op, errs.CodeStateCorrupt).
			WithDetail("the stored file is %d bytes hashing to sha256:%s, not the %d bytes hashing to "+
				"sha256:%s the export job recorded; it was not served", total, got, size, wantHex)
	}
	_, err := dst.Write(held)
	return err
}

// authorisedExport resolves {exportID} and checks the caller may read the project
// the export's version belongs to. An export the caller cannot reach reads exactly
// like one that does not exist, as a variant does.
func (h *GeometryHandlers) authorisedExport(r *http.Request) (*agent.Export, error) {
	const op = "httpapi.geometry"

	exportID := r.PathValue("exportID")
	notFound := errs.New(op, errs.CodeNotFound).WithDetail("no export %s", exportID)

	e, err := h.stepExports().Find(r.Context(), exportID)
	if err != nil {
		if errs.Is(err, errs.CodeNotFound) {
			return nil, notFound
		}
		return nil, err
	}
	user, _ := UserFrom(r.Context())
	if err := h.deps.requirePermission(r, e.ProjectID, user.ID, access.PermProjectRead); err != nil {
		if errs.Is(err, errs.CodeNotFound) {
			return nil, notFound
		}
		return nil, err
	}
	return e, nil
}
