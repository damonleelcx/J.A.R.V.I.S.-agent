package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/agent"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/access"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/cad"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/identity"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/blob"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Off-node STEP export on the wire: POST /v1/geometry/{id}/exports, its status,
// and its download, through the real handlers against live Postgres.
//
// A worker runs where a test needs one, as forge-worker does: agent.Worker's own
// loop, claiming from the same database. The kernel and the store are fakes except
// in TestExports_RequestJobWorkerBlobStatusAndDownloadAgree, which is the whole
// path with the real kernel and a real S3-compatible server.

type exportsHarness struct {
	d        Deps
	h        *GeometryHandlers
	svc      *geometry.Service
	pool     *db.Pool
	owner    *identity.User
	viewer   *identity.User
	stranger *identity.User
	project  string
}

func newExportsHarness(t *testing.T, blobs blob.Store) *exportsHarness {
	t.Helper()
	url := os.Getenv("FORGE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("FORGE_TEST_DATABASE_URL is unset; skipping live-database tests. " +
			"Run `make db-up` then `make test-integration`.")
	}
	ctx := context.Background()
	const schema = "forge_http_exports"
	cfg := func(u string) config.DBConfig {
		return config.DBConfig{URL: u, MaxConns: 8, MinConns: 1,
			MaxConnLifetime: time.Hour, MaxConnIdleTime: time.Minute, ConnectTimeout: 10 * time.Second}
	}
	admin, err := db.Connect(ctx, cfg(url), logx.Discard())
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	for _, q := range []string{"drop schema if exists " + schema + " cascade", "create schema " + schema} {
		if _, err := admin.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	pool, err := db.Connect(ctx, cfg(url+sep+"search_path="+schema), logx.Discard())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MigrateFS(ctx, pool, db.Files, db.MigrationsDir, logx.Discard()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	x := &exportsHarness{pool: pool}
	x.d = testDeps()
	x.d.Pool = pool
	x.d.Clock = clock.System{}
	x.d.Access = access.NewService(pool, x.d.Clock, logx.Discard())
	x.d.Blobs = blobs
	x.h = NewGeometryHandlers(x.d)
	x.svc = geometry.NewService(pool, x.d.Clock, logx.Discard())

	now := time.Now().UTC()
	x.owner = insertUser(t, pool, "export-owner@example.com")
	x.viewer = insertUser(t, pool, "export-viewer@example.com")
	x.stranger = insertUser(t, pool, "export-stranger@example.com")
	x.project = newProject(t, pool, x.d.Access, x.owner.ID, "P", now)
	if err := x.d.Access.SetRole(ctx, access.Grant{ProjectID: x.project, UserID: x.viewer.ID,
		Role: access.RoleViewer, By: x.owner.ID}); err != nil {
		t.Fatal(err)
	}
	return x
}

func (x *exportsHarness) save(t *testing.T, doc geometry.Document) *geometry.Variant {
	t.Helper()
	v, err := x.svc.Save(context.Background(), geometry.NewVariant{
		ProjectID: x.project, InitiatorID: x.owner.ID, Agent: workspace.AgentConverse, Generator: "test",
		Inputs: map[string]any{"message": doc.Name}, Document: doc,
	})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func exportPlate() geometry.Document {
	return geometry.Document{Name: "plate", Units: "mm",
		NotVerified: []string{"nothing about this plate has been checked"},
		Parts: []geometry.Part{{ID: "plate", Name: "Plate", Shape: "box",
			Size:     map[string]float64{"width": 60, "height": 5, "depth": 60},
			Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}}}
}

// exportRows is rows × per boxes, placed by repeats, 10 mm apart.
func exportRows(rows, per int) geometry.Document {
	d := geometry.Document{Name: "rows", Units: "mm",
		NotVerified: []string{"nothing about these boxes has been checked"}}
	for i := 0; i < rows; i++ {
		d.Parts = append(d.Parts, geometry.Part{ID: fmt.Sprintf("row%d", i), Name: "Box", Shape: "box",
			Size:     map[string]float64{"width": 4, "height": 4, "depth": 4},
			Position: []float64{0, float64(10 * i), 0}, Rotation: []float64{0, 0, 0},
			Repeat: &geometry.Repeat{Count: per, Offset: []float64{10, 0, 0}}})
	}
	return d
}

func (x *exportsHarness) request(user *identity.User, versionID string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r := postAs(user, "/v1/geometry/"+versionID+"/exports", "")
	r.SetPathValue("id", versionID)
	x.h.RequestExport(rec, r)
	return rec
}

func (x *exportsHarness) status(user *identity.User, exportID string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r := getAs(user, "/v1/geometry/exports/"+exportID)
	r.SetPathValue("exportID", exportID)
	x.h.ExportStatus(rec, r)
	return rec
}

func (x *exportsHarness) download(user *identity.User, exportID string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r := getAs(user, "/v1/geometry/exports/"+exportID+"/file")
	r.SetPathValue("exportID", exportID)
	x.h.DownloadExport(rec, r)
	return rec
}

func exportIn(t *testing.T, rec *httptest.ResponseRecorder) ExportDTO {
	t.Helper()
	var body struct {
		Export ExportDTO `json:"export"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Export.ID == "" {
		t.Fatalf("no export in the response (%v): %d %s", err, rec.Code, rec.Body.String())
	}
	return body.Export
}

// runWorker runs a forge-worker loop with this export runner until the test ends.
func (x *exportsHarness) runWorker(t *testing.T, kernel agent.StepExportKernel, store blob.Store) {
	t.Helper()
	cfg := config.EngineConfig{
		WorkerConcurrency: 1, LeaseDuration: time.Minute, LeaseHeartbeat: 15 * time.Second,
		PollInterval: 20 * time.Millisecond, MaxAttemptsPerTask: 3,
		BackoffBase: 10 * time.Millisecond, BackoffMax: 50 * time.Millisecond,
		MaxIterationsPerTask: 12, MaxToolCallsPerIteration: 8,
		MaxTokensPerGoal: 1_000_000, MaxWallClockPerGoal: time.Hour, MaxTaskDepth: 3, MaxTasksPerGoal: 50,
	}
	clk := clock.System{}
	repo, queue := engine.NewRepository(), engine.NewQueue()
	w := agent.NewWorker(agent.WorkerDeps{Pool: x.pool, Repo: repo, Queue: queue, Budget: engine.NewBudgetGuard(cfg),
		Assembler: agent.NewAssembler(repo, queue),
		Exports:   agent.NewStepExporter(kernel, store, x.svc, repo, x.pool, clk, logx.Discard()),
		Config:    cfg, WorkspaceRoot: t.TempDir(), Clock: clk, Log: logx.Discard()})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = w.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
}

// waitFinished polls the status route until the export has ended.
func (x *exportsHarness) waitFinished(t *testing.T, exportID string, within time.Duration) ExportDTO {
	t.Helper()
	return x.waitFinishedAs(t, x.owner, exportID, within)
}

// waitFinishedAs polls as one particular person, so a test can show that the
// status route answers the caller whose export it is.
func (x *exportsHarness) waitFinishedAs(t *testing.T, user *identity.User, exportID string,
	within time.Duration) ExportDTO {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		got := exportIn(t, x.status(user, exportID))
		if got.Status == agent.ExportSucceeded || got.Status == agent.ExportFailed {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("export %s is still %s after %s", exportID, got.Status, within)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// exportTestStore is a content-addressed store that does NOT check hashes on Get,
// so the handler's own check is the only one a corrupt blob meets.
type exportTestStore struct {
	mu      sync.Mutex
	objects map[blob.Key][]byte
}

func newExportTestStore() *exportTestStore { return &exportTestStore{objects: map[blob.Key][]byte{}} }

func (s *exportTestStore) Available() bool { return true }

func (s *exportTestStore) Put(_ context.Context, r io.ReadSeeker) (blob.Key, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	k, _, _ := blob.KeyOf(bytes.NewReader(b))
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[k] = b
	return k, nil
}

func (s *exportTestStore) Get(_ context.Context, k blob.Key) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.objects[k]
	if !ok {
		return nil, errs.New("exportTestStore.Get", errs.CodeNotFound)
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), b...))), nil
}

func (s *exportTestStore) Has(_ context.Context, k blob.Key) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.objects[k]
	return ok, nil
}

type exportTestKernel struct{}

func (exportTestKernel) Available() bool { return true }

func (exportTestKernel) ExportSTEPJob(_ context.Context, doc geometry.Document, _ geometry.Unit) (*cad.Build, error) {
	return &cad.Build{Parts: len(doc.Parts),
		STEP: []byte("ISO-10303-21;\nHEADER;\n/* " + doc.Name + " */\nENDSEC;\nEND-ISO-10303-21;\n")}, nil
}

// With no bucket, the request is refused naming the setting, and nothing is
// queued that could build and be lost.
func TestExports_AnUnconfiguredBlobStoreIsRefusedByNameAndNothingIsQueued(t *testing.T) {
	x := newExportsHarness(t, nil)
	v := x.save(t, exportPlate())

	rec := x.request(x.owner, v.VersionID)
	if rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), "FORGE_BLOB_BUCKET") {
		t.Fatalf("with no blob store the request answered %d: %s; want 501 naming FORGE_BLOB_BUCKET",
			rec.Code, rec.Body.String())
	}
	if n := goalsIn(t, x.pool, x.project); n != 0 {
		t.Errorf("a refused export left %d goal(s) queued", n)
	}
}

// Above the job's ceiling, refused by name before any goal exists.
func TestExports_ADesignAboveTheJobCeilingIsRefusedByName(t *testing.T) {
	x := newExportsHarness(t, newExportTestStore())
	v := x.save(t, exportRows(226, 400)) // 90,400 occurrences, under the storage door's 100,000

	rec := x.request(x.owner, v.VersionID)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "90000") {
		t.Fatalf("a 90,400-part export answered %d: %s; want 400 naming the 90000 ceiling", rec.Code, rec.Body.String())
	}
	if n := goalsIn(t, x.pool, x.project); n != 0 {
		t.Errorf("a refused export left %d goal(s) queued", n)
	}
}

// Somebody outside the project is told the design, and the export, do not exist —
// on every route, never that they are forbidden.
func TestExports_ANonMemberIsToldThereIsNoSuchDesignOrExport(t *testing.T) {
	x := newExportsHarness(t, newExportTestStore())
	v := x.save(t, exportPlate())

	if rec := x.request(x.stranger, v.VersionID); rec.Code != http.StatusNotFound {
		t.Fatalf("a non-member's request answered %d: %s; want 404", rec.Code, rec.Body.String())
	}
	if n := goalsIn(t, x.pool, x.project); n != 0 {
		t.Fatalf("a non-member's refused request left %d goal(s) in the project", n)
	}
	rec := x.request(x.owner, v.VersionID)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("the owner's request answered %d: %s", rec.Code, rec.Body.String())
	}
	e := exportIn(t, rec)
	if rec := x.status(x.stranger, e.ID); rec.Code != http.StatusNotFound {
		t.Errorf("a non-member read an export's status: %d %s", rec.Code, rec.Body.String())
	}
	if rec := x.download(x.stranger, e.ID); rec.Code != http.StatusNotFound {
		t.Errorf("a non-member's download answered %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

// Exporting is reading: a viewer asks for an export of a design they may read,
// follows it, and downloads the file (the 2026-09-15 decision). The goal FORGE
// wrote to do the work still records the viewer as the person who asked.
func TestExports_AViewerMayRequestFollowAndDownloadAnExportOfADesignTheyMayRead(t *testing.T) {
	store := newExportTestStore()
	x := newExportsHarness(t, store)
	v := x.save(t, exportPlate())

	rec := x.request(x.viewer, v.VersionID)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("a viewer's request answered %d: %s; want 202 — read access is the whole gate",
			rec.Code, rec.Body.String())
	}
	e := exportIn(t, rec)
	if e.RequestedBy != x.viewer.ID {
		t.Errorf("the export records %q as its requester, want the viewer %q", e.RequestedBy, x.viewer.ID)
	}
	// Accountability: the goal is FORGE's mechanism, but the person who asked for
	// the design to leave the building is recorded as its creator, not the system.
	var createdBy string
	if err := x.pool.QueryRow(context.Background(),
		`select created_by from forge_goals where id = $1`, e.GoalID).Scan(&createdBy); err != nil {
		t.Fatal(err)
	}
	if createdBy != x.viewer.ID {
		t.Errorf("the export's goal was created by %q; want the viewer %q, who asked for it", createdBy, x.viewer.ID)
	}

	x.runWorker(t, exportTestKernel{}, store)
	done := x.waitFinishedAs(t, x.viewer, e.ID, 30*time.Second)
	if done.Status != agent.ExportSucceeded {
		t.Fatalf("the viewer's export ended %s: %s", done.Status, done.Reason)
	}
	dl := x.download(x.viewer, e.ID)
	if dl.Code != http.StatusOK || dl.Body.Len() == 0 {
		t.Fatalf("a viewer's download answered %d with %d bytes: %s", dl.Code, dl.Body.Len(), dl.Body.String())
	}
	if done.SHA256 == nil {
		t.Fatal("the finished export reports no digest")
	}
	sum := sha256.Sum256(dl.Body.Bytes())
	if hex.EncodeToString(sum[:]) != *done.SHA256 {
		t.Errorf("the viewer downloaded %x, not the stored %s", sum, *done.SHA256)
	}
}

// A reader may spend a bounded amount of forge-worker's time: one person's live
// export jobs in a project are capped, and the refusal names the limit.
//
// The bound is per person, so one reader at the cap cannot refuse anybody else's
// exports, and it counts only jobs not yet finished.
func TestExports_MoreLiveExportJobsThanTheLimitAreRefusedNamingIt(t *testing.T) {
	store := newExportTestStore()
	x := newExportsHarness(t, store)

	// One version per job the viewer may hold, one to be refused, one for the
	// owner: asking twice for the SAME version is the idempotent answer, never a
	// refusal, so every job here has to be a different version.
	versions := make([]*geometry.Variant, 0, agent.MaxLiveExportJobsPerRequester+2)
	for i := 0; i < agent.MaxLiveExportJobsPerRequester+2; i++ {
		doc := exportPlate()
		doc.Name = fmt.Sprintf("plate %d", i)
		versions = append(versions, x.save(t, doc))
	}

	queued := make([]string, 0, agent.MaxLiveExportJobsPerRequester)
	for i := 0; i < agent.MaxLiveExportJobsPerRequester; i++ {
		rec := x.request(x.viewer, versions[i].VersionID)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("the viewer's request %d of %d answered %d: %s",
				i+1, agent.MaxLiveExportJobsPerRequester, rec.Code, rec.Body.String())
		}
		queued = append(queued, exportIn(t, rec).ID)
	}

	over := versions[agent.MaxLiveExportJobsPerRequester]
	rec := x.request(x.viewer, over.VersionID)
	if rec.Code != http.StatusTooManyRequests ||
		!strings.Contains(rec.Body.String(), "MaxLiveExportJobsPerRequester") {
		t.Fatalf("export job %d answered %d: %s; want 429 naming MaxLiveExportJobsPerRequester",
			agent.MaxLiveExportJobsPerRequester+1, rec.Code, rec.Body.String())
	}
	if n := goalsIn(t, x.pool, x.project); n != agent.MaxLiveExportJobsPerRequester {
		t.Fatalf("the project holds %d goal(s), want %d: the refused request queued one anyway",
			n, agent.MaxLiveExportJobsPerRequester)
	}

	// Asking again for a version already queued costs no worker time, so it is
	// answered with the export that exists rather than refused for being over.
	if rec := x.request(x.viewer, versions[0].VersionID); rec.Code != http.StatusOK {
		t.Errorf("asking again for an export already queued answered %d, want 200: %s", rec.Code, rec.Body.String())
	}
	// Per person: the owner is at nought and is not held up by the viewer's jobs.
	if rec := x.request(x.owner, versions[len(versions)-1].VersionID); rec.Code != http.StatusAccepted {
		t.Errorf("the owner's first export answered %d while the viewer was at the limit: %s",
			rec.Code, rec.Body.String())
	}

	// And a finished job frees the slot: the limit is on work not yet done.
	x.runWorker(t, exportTestKernel{}, store)
	for _, id := range queued {
		if got := x.waitFinishedAs(t, x.viewer, id, 30*time.Second); got.Status != agent.ExportSucceeded {
			t.Fatalf("export %s ended %s: %s", id, got.Status, got.Reason)
		}
	}
	if rec := x.request(x.viewer, over.VersionID); rec.Code != http.StatusAccepted {
		t.Errorf("with every earlier job finished, the viewer's next export answered %d, want 202: %s",
			rec.Code, rec.Body.String())
	}
}

// Asked for twice, one export and one goal.
func TestExports_AskingTwiceReturnsTheSameExport(t *testing.T) {
	x := newExportsHarness(t, newExportTestStore())
	v := x.save(t, exportPlate())

	first := x.request(x.owner, v.VersionID)
	second := x.request(x.owner, v.VersionID)
	if first.Code != http.StatusAccepted || second.Code != http.StatusOK {
		t.Fatalf("two requests answered %d then %d; want 202 then 200", first.Code, second.Code)
	}
	if a, b := exportIn(t, first), exportIn(t, second); a.ID != b.ID {
		t.Errorf("two requests for one version made two exports, %s and %s", a.ID, b.ID)
	}
	if created, _ := decode(t, second)["created"].(bool); created {
		t.Error("the second request says it created an export")
	}
	if n := goalsIn(t, x.pool, x.project); n != 1 {
		t.Errorf("two requests made %d goal(s), want 1", n)
	}
}

// The download is the file the job stored, byte for byte, and not before there is
// one. A stored file that no longer hashes to what the job recorded is never
// served whole: the client holds fewer bytes than it was promised.
func TestExports_TheDownloadIsTheStoredFileAndACorruptOneIsNeverServedWhole(t *testing.T) {
	store := newExportTestStore()
	x := newExportsHarness(t, store)
	v := x.save(t, exportPlate())
	e := exportIn(t, x.request(x.owner, v.VersionID))

	if rec := x.download(x.owner, e.ID); rec.Code != http.StatusConflict {
		t.Fatalf("a queued export's download answered %d, want 409: %s", rec.Code, rec.Body.String())
	}

	x.runWorker(t, exportTestKernel{}, store)
	done := x.waitFinished(t, e.ID, 30*time.Second)
	if done.Status != agent.ExportSucceeded || done.SHA256 == nil || done.SizeBytes == nil {
		t.Fatalf("the export ended %s (%s) with sha %v and size %v", done.Status, done.Reason, done.SHA256, done.SizeBytes)
	}

	rec := x.download(x.owner, e.ID)
	sum := sha256.Sum256(rec.Body.Bytes())
	if rec.Code != http.StatusOK || hex.EncodeToString(sum[:]) != *done.SHA256 ||
		int64(rec.Body.Len()) != *done.SizeBytes || rec.Header().Get("X-Forge-Export-SHA256") != *done.SHA256 {
		t.Fatalf("the download (%d, %d bytes, sha %x, header %q) is not the stored file (%d bytes, sha %s)",
			rec.Code, rec.Body.Len(), sum, rec.Header().Get("X-Forge-Export-SHA256"), *done.SizeBytes, *done.SHA256)
	}

	// Same length, one byte different: only the digest can tell.
	store.mu.Lock()
	for k, b := range store.objects {
		b[len(b)/2] ^= 0xff
		store.objects[k] = b
	}
	store.mu.Unlock()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(context.WithValue(r.Context(), ctxKeyUser, x.owner))
		r.SetPathValue("exportID", e.ID)
		x.h.DownloadExport(w, r)
	}))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		// Cut before the status line reached the client, because the file is
		// smaller than the chunk held back: not served at all, which is the point.
		return
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(resp.Body)
	if readErr == nil && int64(len(body)) == *done.SizeBytes {
		t.Fatalf("a corrupt file was served whole (%d bytes, no error): %q", len(body), body)
	}
	if resp.Header.Get("Content-Length") != strconv.FormatInt(*done.SizeBytes, 10) {
		t.Errorf("the download did not promise its length, so a short one cannot be told from a whole one")
	}
}

// The whole path, with the real kernel and a real S3-compatible server: a design
// the request refuses to build as STEP is asked for as a job, forge-worker writes
// it, the blob holds exactly what the status says, and the download is that blob.
//
// Asked for by the VIEWER, throughout. The decision that requesting an export
// needs only read access (2026-09-15) is worth little if it holds against a fake
// kernel and a fake bucket only, so the one fence that runs the real ones runs
// them for the caller who has nothing but read access.
func TestExports_RequestJobWorkerBlobStatusAndDownloadAgree(t *testing.T) {
	python := os.Getenv("FORGE_CAD_PYTHON")
	if python == "" {
		t.Skip("FORGE_CAD_PYTHON is unset; skipping the export job against the real CAD kernel")
	}
	endpoint := os.Getenv("FORGE_TEST_BLOB_ENDPOINT")
	if endpoint == "" {
		t.Skip("FORGE_TEST_BLOB_ENDPOINT is unset; skipping the export job against MinIO. " +
			"Run `make blob-up` and set the variables `make test-blob` sets")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	const bucket = "forge-test-geometry-exports"
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion("us-east-1"))
	if err != nil {
		t.Fatal(err)
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})
	if _, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		var owned *types.BucketAlreadyOwnedByYou
		if !errors.As(err, &owned) {
			t.Fatalf("creating test bucket %s on %s: %v", bucket, endpoint, err)
		}
	}
	store, err := blob.New(ctx, config.BlobConfig{Bucket: bucket, Region: "us-east-1", Endpoint: endpoint}, logx.Discard())
	if err != nil {
		t.Fatal(err)
	}

	x := newExportsHarness(t, store)
	v := x.save(t, exportRows(21, 200)) // 4,200 occurrences: over what a request builds

	// The request refuses it, with a kernel present: that is why the job exists.
	withKernel := x.d
	withKernel.CAD = cad.New(python, logx.Discard())
	t.Cleanup(withKernel.CAD.Close)
	rec := httptest.NewRecorder()
	r := getAs(x.viewer, "/v1/geometry/"+v.VersionID+"/export?format=step")
	r.SetPathValue("id", v.VersionID)
	NewGeometryHandlers(withKernel).Export(rec, r)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("the in-request STEP export of 4,200 parts answered %d, want the 4,096 ceiling's 400: %s",
			rec.Code, rec.Body.String())
	}

	rec = x.request(x.viewer, v.VersionID)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("the viewer's request: %d %s", rec.Code, rec.Body.String())
	}
	e := exportIn(t, rec)

	kernel := cad.New(python, logx.Discard())
	t.Cleanup(kernel.Close)
	x.runWorker(t, kernel, store)
	done := x.waitFinishedAs(t, x.viewer, e.ID, 6*time.Minute)
	if done.Status != agent.ExportSucceeded {
		t.Fatalf("the export ended %s: %s", done.Status, done.Reason)
	}
	if done.Parts == nil || *done.Parts != 4200 || done.SHA256 == nil || done.SizeBytes == nil {
		t.Fatalf("the finished export reports parts %v, sha %v, size %v", done.Parts, done.SHA256, done.SizeBytes)
	}

	key := blob.Key("sha256:" + *done.SHA256)
	if there, err := store.Has(ctx, key); err != nil || !there {
		t.Fatalf("the bucket does not hold %s (%v)", key, err)
	}
	stored, err := store.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	blobBytes, err := io.ReadAll(stored)
	_ = stored.Close()
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(blobBytes)
	if hex.EncodeToString(sum[:]) != *done.SHA256 || int64(len(blobBytes)) != *done.SizeBytes {
		t.Fatalf("the blob is %d bytes hashing to %x; the status says %d bytes, %s",
			len(blobBytes), sum, *done.SizeBytes, *done.SHA256)
	}
	if !bytes.HasPrefix(blobBytes, []byte("ISO-10303-21")) {
		t.Errorf("the stored file is not STEP: %q", blobBytes[:min(len(blobBytes), 40)])
	}

	rec = x.download(x.viewer, e.ID)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), blobBytes) {
		t.Fatalf("the viewer's download (%d, %d bytes) is not the blob (%d bytes)",
			rec.Code, rec.Body.Len(), len(blobBytes))
	}
	if !strings.Contains(rec.Header().Get("X-Forge-Export-Label"), "no interference check") {
		t.Errorf("the label does not say no interference check ran: %q", rec.Header().Get("X-Forge-Export-Label"))
	}

	// And the goal that carried it settles, so the console does not show it running.
	deadline := time.Now().Add(10 * time.Second)
	status := ""
	for time.Now().Before(deadline) && status != string(engine.GoalSucceeded) {
		if err := x.pool.QueryRow(ctx, `select status from forge_goals where id = $1`, done.GoalID).Scan(&status); err != nil {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if status != string(engine.GoalSucceeded) {
		t.Errorf("the export's goal is %s, want succeeded", status)
	}
	_ = id.PrefixExport
}
