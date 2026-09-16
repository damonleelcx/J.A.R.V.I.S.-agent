package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/cad"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/blob"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/text"
)

// An off-node STEP export: a design written as STEP by forge-worker, with the
// worker's own kernel, and kept in blob storage.
//
// # The problem this solves
//
// STEP was only ever written inside forged's request (GET /v1/geometry/{id}/export),
// refused whole above the 4,096-part building ceiling. Past that ceiling there is
// no way to get a STEP file out at all, and the ceiling cannot simply be raised in
// the request: #89 measured XDE export at 21 s and 1.3 GB for 90,880 occurrences,
// and 307 s and 3.4 GB at 302,560, in a pod (forged) that shares 1 GiB with the API
// and the audio server. The owner decision was that STEP at 1M stays refused until
// an export job exists off-node. This is that job.
//
// # Why an engine task and not a queue of its own
//
// What an export needs from a queue is exactly what the engine already has and
// already fences: a lease a dead worker lets lapse, a heartbeat for work longer
// than the lease, a reaper that hands the work back, attempts, backoff, and a
// release when a worker stops cleanly. A second queue would be a second copy of
// all of that, and the copy is where the lease reclaim would be subtly different.
// The engine also gives the export a timeline on the hash chain — who asked for
// the design to leave the building, and when it did.
//
// What does not fit is the goal's planning half: no statement to plan, no model to
// call. So an export writes its goal already planned and already active, with
// exactly one task, the way the drill harness and a build step's plan do, and
// forge-worker dispatches the task by kind (runExportStep) before the tool loop,
// as it does a build step. The goal settles itself when the task does.
//
// # What a status means
//
// Status is DERIVED from the task on every read, never stored beside it: two
// fields for one fact is how an export ends up "succeeded" on one row and running
// on the other. The one thing an export row adds is the file: its blob key, size
// and what the kernel said about it. ‼️ The worker writes those and marks the task
// succeeded in ONE transaction, after checking it still holds the lease — so a
// worker killed or reclaimed mid-export can leave neither a success with no file
// nor a file under a success it did not earn (recordStoredExport).

// TaskKindExportStep marks a task that writes one design version as STEP.
const TaskKindExportStep = "geometry.export_step"

// ExportFormatSTEP is the one format an export job writes. Meshes (STL, OBJ,
// glTF) are tessellated in Go in the request, bounded by the 4,096 ceiling, and
// have no kernel to move off-node.
const ExportFormatSTEP = "step"

// Export statuses, as GET /v1/geometry/exports/{id} reports them.
const (
	ExportQueued    = "queued"
	ExportRunning   = "running"
	ExportSucceeded = "succeeded"
	ExportFailed    = "failed"
)

// exportJobAttempts is how many times an export task is tried.
//
// Three, not the planner's five. Each attempt can hold the kernel for up to its
// timeout (cad.exportJobTimeout) twice — the kernel restarts once on a process that
// died — and the likeliest reason an export dies repeatedly is a design that
// exhausts the worker every time, which a fourth and fifth attempt would only
// repeat, taking a build step's kernel with it on each pass.
const exportJobAttempts = 3

// exportStepInputs is what an export task carries.
type exportStepInputs struct {
	Kind      string `json:"kind"`
	ExportID  string `json:"export_id"`
	VersionID string `json:"version_id"`
	Format    string `json:"format"`
}

func exportStepOf(t *engine.Task) (exportStepInputs, bool) {
	var in exportStepInputs
	if t == nil || len(t.Inputs) == 0 || json.Unmarshal(t.Inputs, &in) != nil {
		return in, false
	}
	return in, in.Kind == TaskKindExportStep
}

// Export is one export job as a reader sees it.
type Export struct {
	ID          string
	VersionID   string
	ProjectID   string
	Format      string
	RequestedBy string
	GoalID      string
	TaskID      string
	// Filename is what the download is called, fixed when it was asked for.
	Filename string

	// Status is queued, running, succeeded or failed, derived from the task.
	Status string
	// Reason says why a job failed, or why a queued job is queued again.
	Reason string

	// BlobKey, SizeBytes and Parts are set once the file is stored.
	BlobKey   blob.Key
	SizeBytes int64
	Parts     int
	// Skipped and FeatureFailures are what the kernel left out of the file, and
	// travel with the download for the reason exportParametric's label does.
	Skipped         []string
	FeatureFailures []string

	Attempts    int
	MaxAttempts int
	CreatedAt   time.Time
	StoredAt    *time.Time
}

// SHA256 is the file's digest in hex, or "" before it is stored.
func (e *Export) SHA256() string { return e.BlobKey.Hex() }

// exportReport is what the kernel said about a stored file.
type exportReport struct {
	Skipped         []string         `json:"skipped,omitempty"`
	FeatureFailures []string         `json:"feature_failures,omitempty"`
	Inferred        []string         `json:"inferred,omitempty"`
	PhasesMS        map[string]int64 `json:"phases_ms,omitempty"`
}

// StepExports is where export jobs are asked for and read back. forged holds one.
type StepExports struct {
	pool  *db.Pool
	repo  *engine.Repository
	queue *engine.Queue
	clock clock.Clock
	log   *logx.Logger
}

// NewStepExports returns the export jobs of this deployment's database.
func NewStepExports(pool *db.Pool, clk clock.Clock, log *logx.Logger) *StepExports {
	if log == nil {
		log = logx.Discard()
	}
	return &StepExports{pool: pool, repo: engine.NewRepository(), queue: engine.NewQueue(), clock: clk, log: log}
}

// errLostRace is a second request for the same export inserting at the same
// moment as the first, and losing to the unique index.
var errLostRace = errors.New("another request created this export first")

// Request asks forge-worker to write a version as STEP, or returns the export
// already asked for.
//
// The caller has checked the person may; this checks the design may. created is
// false when an export of this version and format was already queued, running or
// done — the idempotent answer, so a double click or a retried request costs
// nothing. A FAILED export is not returned: it is superseded, and a fresh one is
// queued, because asking again is how a person retries.
func (s *StepExports) Request(ctx context.Context, v *geometry.Variant, format, userID string) (*Export, bool, error) {
	const op = "agent.StepExports.Request"

	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" {
		format = ExportFormatSTEP
	}
	if format != ExportFormatSTEP {
		return nil, false, errs.New(op, errs.CodeValidationFailed).
			WithDetail("an export job writes STEP only, not %q: the mesh formats are tessellated in the "+
				"request, at GET /v1/geometry/%s/export?format=%s", format, v.VersionID, format)
	}
	// The same refusal the request path gives, for the same reason: a STEP file
	// declares its own unit.
	if !v.Units.Known() {
		return nil, false, errs.New(op, errs.CodeValidationFailed).
			WithDetail("this variant has no unit FORGE can convert (%s), and a STEP file declares "+
				"its own unit — writing one would put a guess about scale inside the file. "+
				"Ask FORGE to restate the assembly in mm, cm, m or in, then export that variant.",
				strings.ToLower(strings.TrimSuffix(v.UnitsNote(), ".")))
	}
	// Refused HERE, before a goal exists, and again by the worker before the
	// kernel. A job queued only to fail minutes later is a refusal delivered late.
	if refusal := v.Document.ExportJobRefusal(); refusal != "" {
		return nil, false, errs.New(op, errs.CodeValidationFailed).WithDetail("%s", refusal)
	}

	// Twice at most: the second pass finds the export the racing request made.
	for pass := 0; ; pass++ {
		var out *Export
		created := false
		err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
			live, err := s.find(ctx, tx, `e.version_id = $1 and e.format = $2 and e.superseded_at is null`,
				v.VersionID, format)
			if err != nil && !errs.Is(err, errs.CodeNotFound) {
				return err
			}
			if live != nil && live.Status != ExportFailed {
				out = live
				return nil
			}
			now := s.clock.Now()
			if live != nil {
				if _, err := tx.Exec(ctx,
					`update forge_geometry_exports set superseded_at = $2 where id = $1 and superseded_at is null`,
					live.ID, now); err != nil {
					return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
				}
			}
			out, err = s.create(ctx, tx, v, format, userID, now)
			created = err == nil
			return err
		})
		if errors.Is(err, errLostRace) && pass == 0 {
			continue
		}
		if err != nil {
			return nil, false, err
		}
		return out, created, nil
	}
}

// create writes the goal, its plan, its one task and the export row, active and
// claimable, in the caller's transaction.
func (s *StepExports) create(ctx context.Context, tx pgx.Tx, v *geometry.Variant, format, userID string,
	now time.Time) (*Export, error) {
	const op = "agent.StepExports.create"

	name := strings.TrimSpace(v.Name)
	if name == "" {
		name = v.VersionID
	}
	f, err := geometry.FormatOf(format)
	if err != nil {
		return nil, err
	}
	e := &Export{
		ID: id.New(id.PrefixExport), VersionID: v.VersionID, ProjectID: v.ProjectID, Format: format,
		RequestedBy: userID, GoalID: id.New(id.PrefixGoal), TaskID: id.New(id.PrefixTask),
		Filename: geometry.Filename(v, f), Status: ExportQueued, MaxAttempts: exportJobAttempts, CreatedAt: now,
	}
	planID := id.New(id.PrefixPlan)

	// ‼️ The goal is written straight into 'active', not drafted and started. An
	// export is not a plan for a person to preview (PRD AGT-02): it is a read of a
	// design they may already read, the request IS the decision, and the draft →
	// start pair would be two round trips to express one click. Risk r0 and
	// autonomy draft: it produces a file and changes nothing.
	title := text.Clip(fmt.Sprintf("Export %s as STEP", name), 200)
	statement := fmt.Sprintf("Write version %s (%s) as a STEP file in forge-worker, with its CAD kernel, "+
		"and keep the file in blob storage. Nothing in the design changes.", v.VersionID, name)
	if _, err := tx.Exec(ctx, `
		insert into forge_goals (id, project_id, created_by, title, statement, status, autonomy, risk_tier,
			completion_criteria, tasks_created, started_at, created_at, updated_at)
		values ($1,$2,$3,$4,$5,'active',$6,$7,'[]'::jsonb,1,$8,$8,$8)`,
		e.GoalID, e.ProjectID, userID, title, statement,
		string(engine.AutonomyDraft), string(engine.RiskR0), now); err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	// Authored 'human', of the two authors a plan may have. No model planned it:
	// the one task is exactly what the person asked for, and 'planner' would
	// credit a model with a plan it never saw.
	if _, err := tx.Exec(ctx,
		`insert into forge_plans (id, goal_id, version, rationale, author, created_at) values ($1,$2,1,$3,'human',$4)`,
		planID, e.GoalID, "One task: write the version as STEP and store the file.", now); err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	inputs, _ := json.Marshal(exportStepInputs{Kind: TaskKindExportStep, ExportID: e.ID, VersionID: v.VersionID, Format: format})
	task := &engine.Task{
		ID: e.TaskID, GoalID: e.GoalID, PlanID: planID,
		Title: title, Instruction: statement, Inputs: inputs, ExpectedOutput: json.RawMessage(`{}`),
		Status: engine.StatusPending, IdempotencyKey: TaskKindExportStep,
		MaxAttempts: exportJobAttempts, NotBefore: now, Priority: 100,
		RiskTier: engine.RiskR0, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.repo.CreateTask(ctx, tx, task, nil); err != nil {
		return nil, err
	}
	if _, err := s.queue.PromoteReadyTasks(ctx, tx, e.GoalID, now); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		insert into forge_geometry_exports (id, version_id, project_id, format, requested_by, goal_id, task_id,
			filename, created_at)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		e.ID, e.VersionID, e.ProjectID, e.Format, e.RequestedBy, e.GoalID, e.TaskID, e.Filename, now); err != nil {
		if uniqueViolation(err) {
			return nil, errLostRace
		}
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}

	by := userID
	for _, ev := range []*engine.Event{
		{GoalID: e.GoalID, Kind: engine.EventGoalCreated, Actor: engine.ActorHuman, ActorID: &by,
			Summary: fmt.Sprintf("Asked for %s as a STEP file (export %s).", name, e.ID)},
		{GoalID: e.GoalID, Kind: engine.EventPlanCreated, Actor: engine.ActorSystem,
			Summary: "Plan v1: 1 task, write the version as STEP and store the file."},
		{GoalID: e.GoalID, Kind: engine.EventGoalActivated, Actor: engine.ActorHuman, ActorID: &by,
			Summary: "Goal activated. The export is claimable by any running forge-worker."},
	} {
		payload, _ := json.Marshal(map[string]any{"export_id": e.ID, "version_id": e.VersionID, "format": format})
		ev.Payload = payload
		if err := s.repo.AppendEvent(ctx, tx, ev, now); err != nil {
			return nil, err
		}
	}
	return e, nil
}

// Find reads one export, with its status derived from its task.
func (s *StepExports) Find(ctx context.Context, exportID string) (*Export, error) {
	return s.find(ctx, s.pool, `e.id = $1`, exportID)
}

// find reads the export a where clause names. NOT_FOUND when there is none.
func (s *StepExports) find(ctx context.Context, q db.Querier, where string, args ...any) (*Export, error) {
	const op = "agent.StepExports.Find"

	var (
		e                  Export
		key                *string
		size               *int64
		parts              *int
		report             []byte
		taskStatus, detail string
		attempts, maxTries int
	)
	err := q.QueryRow(ctx, `
		select e.id, e.version_id, e.project_id, e.format, e.requested_by, e.goal_id, e.task_id, e.filename,
		       e.blob_key, e.size_bytes, e.parts, e.report, e.created_at, e.stored_at,
		       t.status, coalesce(t.error_detail, ''), t.attempt_count, t.max_attempts
		  from forge_geometry_exports e
		  join forge_tasks t on t.id = e.task_id
		 where `+where+`
		 order by e.created_at desc
		 limit 1`, args...).
		Scan(&e.ID, &e.VersionID, &e.ProjectID, &e.Format, &e.RequestedBy, &e.GoalID, &e.TaskID, &e.Filename,
			&key, &size, &parts, &report, &e.CreatedAt, &e.StoredAt,
			&taskStatus, &detail, &attempts, &maxTries)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errs.New(op, errs.CodeNotFound).WithDetail("no export found")
	}
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	if key != nil {
		e.BlobKey = blob.Key(*key)
	}
	if size != nil {
		e.SizeBytes = *size
	}
	if parts != nil {
		e.Parts = *parts
	}
	var r exportReport
	_ = json.Unmarshal(report, &r)
	e.Skipped, e.FeatureFailures = r.Skipped, r.FeatureFailures
	e.Attempts, e.MaxAttempts = attempts, maxTries
	e.Status, e.Reason = exportStatus(engine.TaskStatus(taskStatus), e.BlobKey, detail, attempts)
	return &e, nil
}

// exportStatus is what a task's state means for the export it writes.
//
// ‼️ Succeeded needs BOTH the task succeeded and a file stored. They are written in
// one transaction, so either alone is a defect, and a defect is reported as the
// failure it is, never as a success with nothing to download.
func exportStatus(ts engine.TaskStatus, key blob.Key, detail string, attempts int) (string, string) {
	switch ts {
	case engine.StatusSucceeded:
		if key != "" {
			return ExportSucceeded, ""
		}
		return ExportFailed, "the export job was marked done with no file stored, which the worker never " +
			"writes; ask for the export again"
	case engine.StatusFailed:
		if detail == "" {
			detail = "the export job failed without saying why"
		}
		return ExportFailed, detail
	case engine.StatusCancelled, engine.StatusSkipped:
		return ExportFailed, "the export job was cancelled before it stored a file"
	case engine.StatusRunning, engine.StatusVerifying, engine.StatusAwaitingApproval:
		return ExportRunning, ""
	default: // pending, ready, claimed
		if attempts > 0 && ts == engine.StatusReady {
			why := detail
			if why == "" {
				why = "its worker stopped before it finished"
			}
			return ExportQueued, fmt.Sprintf("attempt %d did not finish (%s); it is queued to run again", attempts, why)
		}
		return ExportQueued, ""
	}
}

func uniqueViolation(err error) bool {
	var pgErr interface{ SQLState() string }
	return errors.As(err, &pgErr) && pgErr.SQLState() == "23505"
}

// StepExportKernel is the kernel call an export job makes. *cad.Kernel is one.
type StepExportKernel interface {
	Available() bool
	ExportSTEPJob(ctx context.Context, doc geometry.Document, unit geometry.Unit) (*cad.Build, error)
}

// StepExporter runs export tasks for a worker.
type StepExporter struct {
	kernel StepExportKernel
	blobs  blob.Store
	geo    *geometry.Service
	repo   *engine.Repository
	pool   *db.Pool
	clock  clock.Clock
	log    *logx.Logger
	// slot lets one export run in this process at a time.
	//
	// ‼️ Not the kernel pool, which already serialises the kernel. An export
	// expands its design and holds the expanded solids BEFORE it waits for a
	// kernel process, so FORGE_WORKER_CONCURRENCY=4 exports of 90,000 occurrences
	// would hold four expansions while one builds. The job ceiling's arithmetic
	// (geometry.MaxExportJobParts) counts ONE export's Go side, so one is what
	// runs; the rest wait here, before reading anything.
	slot chan struct{}
}

// NewStepExporter returns the runner a worker hands export tasks to. A nil store
// is the store that refuses, so a job on a deployment with no bucket fails by name.
func NewStepExporter(kernel StepExportKernel, blobs blob.Store, geo *geometry.Service, repo *engine.Repository,
	pool *db.Pool, clk clock.Clock, log *logx.Logger) *StepExporter {
	if blobs == nil {
		blobs = blob.Disabled()
	}
	if log == nil {
		log = logx.Discard()
	}
	return &StepExporter{kernel: kernel, blobs: blobs, geo: geo, repo: repo, pool: pool, clock: clk, log: log,
		slot: make(chan struct{}, 1)}
}

// storedExport is what an export task records as its result.
type storedExport struct {
	ExportID string   `json:"export_id"`
	BlobKey  blob.Key `json:"blob_key"`
	Bytes    int64    `json:"bytes"`
	SHA256   string   `json:"sha256"`
	Parts    int      `json:"parts"`
	report   exportReport
}

// run writes the file and stores it. It records nothing: recordStoredExport does,
// in one transaction with the task.
func (x *StepExporter) run(ctx context.Context, task *engine.Task, in exportStepInputs) (*storedExport, error) {
	const op = "agent.StepExporter.run"

	// The store first, and by name: minutes of kernel for a file with nowhere to
	// go would end in the same refusal, later.
	if !x.blobs.Available() {
		return nil, blob.Unavailable(op)
	}
	if x.kernel == nil || !x.kernel.Available() {
		return nil, cad.Unavailable(op)
	}
	select {
	case x.slot <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-x.slot }()

	v, err := x.geo.Find(ctx, in.VersionID)
	if err != nil {
		return nil, err
	}
	if refusal := v.Document.ExportJobRefusal(); refusal != "" {
		return nil, errs.New(op, errs.CodeValidationFailed).WithDetail("%s", refusal)
	}
	built, err := x.kernel.ExportSTEPJob(ctx, v.Document, v.Units)
	if err != nil {
		return nil, err
	}
	// Stopped is not finished: a file from a kernel killed by the stop is not kept.
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if len(built.STEP) == 0 {
		return nil, errs.New(op, errs.CodeExternalProtocol).
			WithDetail("the kernel built version %s and returned no STEP file", in.VersionID)
	}
	key, err := x.blobs.Put(ctx, bytes.NewReader(built.STEP))
	if err != nil {
		return nil, err
	}
	phases := map[string]int64{}
	fields := built.Phases.LogFields()
	for i := 0; i+1 < len(fields); i += 2 {
		if name, ok := fields[i].(string); ok {
			if ms, ok := fields[i+1].(int64); ok {
				phases[name] = ms
			}
		}
	}
	got := &storedExport{ExportID: in.ExportID, BlobKey: key, Bytes: int64(len(built.STEP)), SHA256: key.Hex(),
		Parts: built.Parts, report: exportReport{Skipped: built.Skipped, FeatureFailures: built.FeatureFailures,
			Inferred: built.Inferred, PhasesMS: phases}}
	x.log.Info(ctx, logx.EventGeometryExportStored, append([]any{
		"export_id", in.ExportID, "task_id", task.ID, "version_id", v.VersionID, "project_id", v.ProjectID,
		"parts", built.Parts, "bytes", got.Bytes, "blob_key", string(key)}, fields...)...)
	return got, nil
}

// recordStoredExport keeps the file on the export and marks the task succeeded,
// together or not at all.
//
// ‼️ Three guards, each for a worker that no longer owns the task:
//   - the lease is checked FOR UPDATE: a worker whose lease lapsed while it was
//     exporting, and whose task another worker has since claimed and is running,
//     finds the task in 'running' exactly as it left it. Status alone would let it
//     finish somebody else's attempt;
//   - the transition re-checks the status, so a task reaped back to 'ready', or
//     already finished by the worker that reclaimed it, is not overwritten;
//   - the export row is written in the same transaction, so a refused transition
//     leaves no file on the export either.
//
// CONFLICT means another worker owns it: abandon, nothing was written.
func (x *StepExporter) recordStoredExport(ctx context.Context, task *engine.Task, workerID string,
	got *storedExport, result json.RawMessage) error {
	const op = "agent.StepExporter.recordStoredExport"

	now := x.clock.Now()
	moved := *task
	err := db.InTx(ctx, x.pool, func(tx pgx.Tx) error {
		var held bool
		if err := tx.QueryRow(ctx, `
			select coalesce(lease_owner = $2 and status = 'running', false)
			  from forge_tasks where id = $1 for update`, task.ID, workerID).Scan(&held); err != nil {
			return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
		}
		if !held {
			return errs.New(op, errs.CodeConflict).
				WithDetail("worker %s no longer holds task %s; the file it stored (%s) is not recorded, "+
					"because another worker owns this export now", workerID, task.ID, got.BlobKey)
		}
		report, _ := json.Marshal(got.report)
		tag, err := tx.Exec(ctx, `
			update forge_geometry_exports
			   set blob_key = $3, size_bytes = $4, parts = $5, report = $6, stored_at = $7
			 where id = $1 and task_id = $2`,
			got.ExportID, task.ID, string(got.BlobKey), got.Bytes, got.Parts, report, now)
		if err != nil {
			return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
		}
		if tag.RowsAffected() != 1 {
			return errs.New(op, errs.CodeStateCorrupt).
				WithDetail("export %s is not the export of task %s", got.ExportID, task.ID)
		}
		return x.repo.TransitionTask(ctx, tx, &moved, engine.StatusSucceeded, now, engine.TaskMutation{Result: result})
	})
	if err != nil {
		return err
	}
	task.Status = moved.Status
	return nil
}

// runExportStep runs one export task and records how it ended.
func (w *Worker) runExportStep(ctx context.Context, goal *engine.Goal, task *engine.Task, in exportStepInputs) {
	if w.exports == nil {
		w.failTask(ctx, task, errs.CodeConfigInvalid,
			"this worker cannot run a STEP export: it was started without an export runner (agent.WorkerDeps.Exports)")
		return
	}
	got, err := w.exports.run(ctx, task, in)
	summary := ""
	if err == nil {
		summary = fmt.Sprintf("Wrote version %s as STEP: %d part(s), %d bytes, stored as %s.",
			in.VersionID, got.Parts, got.Bytes, got.BlobKey)
		result, _ := json.Marshal(map[string]any{"summary": summary, "result": got})
		err = w.exports.recordStoredExport(ctx, task, w.ID, got, result)
		if errs.Is(err, errs.CodeConflict) {
			// Somebody else's now. Not failed and not released: it is not ours to
			// move, and whoever holds it will finish it.
			w.log.WarnWith(ctx, logx.EventTaskLeaseExpired, err, "task_id", task.ID, "worker_id", w.ID,
				"detail", "an export finished under a lease this worker no longer holds; nothing was recorded")
			return
		}
	}
	if err != nil {
		if ctx.Err() != nil {
			// Stopping, not failing: handed back now, as a build step is.
			if rerr := w.queue.Release(context.WithoutCancel(ctx), w.pool, task.ID, w.ID, w.clock.Now()); rerr != nil {
				w.log.WarnWith(ctx, logx.EventTaskCycleEnded, rerr, "task_id", task.ID,
					"detail", "a stopping worker could not hand its export back; the reaper will")
			}
			return
		}
		w.retryOrFail(ctx, goal, task, err)
		return
	}
	// Succeeded and NOT verified. Nothing about the shape was checked: the job
	// skips the interference check (cad.Kernel.ExportSTEPJob), and the download's
	// label says so.
	w.appendEvent(ctx, goal.ID, &task.ID, engine.EventTaskSucceeded, engine.ActorExecutor, summary,
		map[string]any{"verified": false, "blob_key": string(got.BlobKey), "bytes": got.Bytes,
			"reason": "an export writes a file; nothing about the shape is checked by writing it"})
}
