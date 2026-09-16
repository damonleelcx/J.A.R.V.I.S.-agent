// Package cad is the CAD kernel, and the process it runs in.
//
// # Why this is a separate package from geometry
//
// internal/domain/geometry is the VOCABULARY: what a part is, what a unit is,
// what a document says. Its own header states that a CAD kernel is deliberately
// not in it, and that stays true — this package depends on geometry and geometry
// knows nothing about this one. The kernel is a subsystem that can be absent,
// and a vocabulary that imported it would make every consumer of the vocabulary
// depend on a Python process.
//
// # What changed, and what did not
//
// Until now this deployment had no kernel at all, and said so: STEP was DECLARED
// AND REFUSED because producing a parametric file from a bag of primitives is a
// kernel's job and not a serialiser's. The 2026-09-05 spike measured what it
// would actually cost to have one — build123d on OpenCASCADE, a valid 37-face
// B-Rep in 46 ms, real ISO-10303-21 out — and the answer was: much less than the
// 2026-09-02 Zoo spike's estimate, which was measuring an agent thinking rather
// than a kernel working.
//
// So the refusal is now CONDITIONAL rather than permanent, and the condition is
// visible: a deployment that has not configured a Python with build123d has no
// kernel and refuses exactly as before. That is the same shape as the vision
// model — absent by default, and absent LOUDLY, because a system that quietly
// substitutes something else for a capability it does not have is the thing this
// product exists not to be.
//
// # Why a long-running process
//
// Importing build123d takes 2.5 s and building a part takes 46 ms. A process per
// export would pay the import every time and put a kernel outside the range
// where it fits in a conversational turn. Kept warm, it is faster than the
// network hop that asked for it.
package cad

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "embed"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

//go:embed sidecar.py
var sidecarSource []byte

// buildTimeout bounds one build.
//
// The spike measured 46 ms for a part with a fillet and four holes. Thirty
// seconds is three orders of magnitude of headroom and still short enough that a
// wedged kernel is noticed by the person waiting rather than by a log.
const buildTimeout = 30 * time.Second

// startTimeout bounds the import.
//
// Measured at 2.5 s on the machine the spike ran on. A minute allows for a cold
// filesystem and a slower box, and distinguishes "starting" from "will never
// start", which is the distinction the caller actually needs.
const startTimeout = 60 * time.Second

// Kernel is a build123d process, started on demand and kept warm.
//
// Requests are serialised: it is one process with one stdin, and a second writer
// would interleave two JSON documents into one line. Serialising is honest about
// what the resource is — a pool is a later problem and would be a wrong answer to
// a question nobody has asked yet.
type Kernel struct {
	python string
	log    *logx.Logger
	// scripts says whether this deployment runs model-written build123d.
	// OFF unless a deployment turns it on, because it is the one feature here
	// that executes text a model produced — see script.go for what the sandbox
	// does and does not promise.
	scripts bool
	// timeout is how long one build may take: buildTimeout, except in the fences
	// that need a limit short enough to cross on purpose. Not a setting.
	timeout time.Duration

	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	script  string
	started bool
}

// New returns a kernel that runs through the given Python interpreter.
//
// An empty path means this deployment has no kernel, which is the DEFAULT and is
// not an error. Nothing starts here; the process is started on the first build,
// so a deployment that never exports a parametric file never pays for one.
func New(python string, log *logx.Logger) *Kernel {
	return &Kernel{python: strings.TrimSpace(python), log: log, timeout: buildTimeout}
}

// WithScripts turns on running model-written build123d.
//
// Off unless a deployment asks, and asking is a decision: it is the one feature
// here that executes text a model produced. script.go says exactly what the
// sandbox does — an AST whitelist refusing imports and dunder attributes before
// anything runs, and a stripped short-lived process with no environment and hard
// CPU and memory limits — and, just as importantly, that it is a defence in
// depth rather than a container.
func (k *Kernel) WithScripts(on bool) *Kernel { k.scripts = on; return k }

// ScriptsEnabled reports whether this deployment runs model-written scripts, so
// a caller can leave the vocabulary out of a prompt rather than offering
// something that will be refused.
func (k *Kernel) ScriptsEnabled() bool { return k != nil && k.scripts }

// Available reports whether this deployment has a kernel configured.
//
// It does NOT report whether the kernel works: that needs starting it, and a
// health check that lied by omission would be worse than none. Build says
// whether it works, by working.
func (k *Kernel) Available() bool { return k != nil && k.python != "" }

// Unavailable is the error a caller gets when there is no kernel, with the one
// sentence that fixes it.
//
// Exported so the export path can refuse with the SAME words wherever the
// question is asked, rather than each caller composing its own account of why
// the file it promised is not coming.
func Unavailable(op string) error {
	return errs.New(op, errs.CodeConnectorUnavailable).
		WithDetail("this deployment has no CAD kernel, so it cannot produce a parametric file. " +
			"Set FORGE_CAD_PYTHON to a Python interpreter with the pinned kernel packages installed: " +
			"in the FORGE image that is /opt/cad/venv/bin/python; from a checkout, `make cad-venv` builds " +
			"one from internal/domain/cad/requirements.txt. It is unset by default: " +
			"writing a STEP file full of tessellated facets and calling it parametric is a lie " +
			"with a file extension on it.")
}

// Build is what the kernel made.
type Build struct {
	// Parts is how many solids the file contains — after tools are consumed, so
	// a plate with four holes cut in it is ONE part and not five.
	Parts int
	// Skipped names the parts that could not be built, and FeatureFailures the
	// operations that could not be applied. Both are named rather than dropped:
	// an assembly quietly missing a hole is wrong in a way nobody notices.
	Skipped         []string
	FeatureFailures []string
	// Volume is the assembly's, in CUBIC MILLIMETRES whatever the document
	// declared — the kernel works in millimetres because that is what a STEP
	// file states. Zero for an assembly of faces, which have none.
	Volume float64
	// Bounds is the assembly's extent in MILLIMETRES: minX, minY, minZ, maxX,
	// maxY, maxZ.
	//
	// The only value here that can reveal a part built in the wrong
	// ORIENTATION. Volume cannot — it is identical however a solid is turned —
	// so a cylinder along the wrong axis produces the same number, the same
	// file size, and a part that is wrong in the one way an exported file
	// cannot be labelled out of.
	Bounds [6]float64
	// STEP is the exported file, empty unless it was asked for.
	STEP []byte
	// Inferred is every dimension the document did not state, in the words the
	// mesh exporter uses. It travels with the file for the same reason: a
	// defaulted 1 and a stated 1 are indistinguishable once written.
	Inferred []string
	// Interferences is every pair of surviving parts that share material, worst
	// first, and Truncated says the pair budget stopped the search before the
	// end — so a caller never reads a partial answer as a clean one.
	//
	// Computed on the solids that SURVIVE the features, which is what makes it
	// trustworthy: a cut tool is consumed before this runs, so a bolt hole
	// cannot report as interference, and a part inside a hollow enclosure shares
	// nothing because the enclosure really is hollow by then. See
	// geometry/interference.go and cad/sidecar.py.
	Interferences          []geometry.Interference
	InterferencesTruncated bool
	// ShapeBuilds is how many distinct shapes the kernel built, and ScriptRuns how
	// many scripts it ran: each distinct shape and each distinct script ONCE per build,
	// however many occurrences place it (Phase 4, stage K1). Reported so "built once"
	// is a count a test and a budget can read, not a claim.
	ShapeBuilds int
	ScriptRuns  int
	// Mesh is the built solid's surface, one entry per surviving part, empty
	// unless it was asked for.
	//
	// # Why this exists
	//
	// The viewport has no boolean operations, so it drew the PRIMITIVES: a bolt
	// hole was a cylinder standing in a plate rather than a void through it, a
	// fillet was invisible, and a fuse of two bodies was two bodies. The STEP
	// file exported from the same document was correct — the divergence existed
	// only on screen, which is the one place a person judges the result.
	//
	// This is the same solid the exporter writes, tessellated. It is not a
	// second model that could disagree with the first.
	Mesh []MeshPart
	// Triangles is the whole mesh's count, and Deflection the tolerance in
	// millimetres it was reached with. Simplified says the tolerance was
	// COARSENED to fit the budget: the shape is the same shape, described less
	// finely, and a caller that shows the mesh should be able to say so rather
	// than presenting a coarse model as an exact one.
	Triangles  int
	Deflection float64
	Simplified bool
	// MeshError is why there is no mesh, when one was asked for and the solid
	// built. Reported rather than returned as an error: the build succeeded and
	// its volume, bounds and STEP are all still true.
	MeshError string
}

// MeshPart is one built solid's surface, attributed to the part it came from.
//
// Vertices is flat — x, y, z, x, y, z — and Triangles indexes it in threes.
// Both in MILLIMETRES, like everything else the kernel returns.
//
// A part consumed as a tool by a cut has NO entry here, which is correct: it is
// no longer a body. That is the whole difference between this and the primitive
// drawing it replaces.
type MeshPart struct {
	ID        string
	Label     string
	Vertices  []float64
	Triangles []int32
}

type request struct {
	Solids     []geometry.Solid     `json:"solids"`
	Operations []geometry.Operation `json:"operations,omitempty"`
	Format     string               `json:"format,omitempty"`
	// Deflection is the tessellation tolerance in millimetres. Zero lets the
	// kernel choose from the model's own size — 0.1 mm is invisible on a
	// bracket and catastrophic on a car body, so a constant cannot serve both.
	Deflection float64 `json:"deflection,omitempty"`
}

type reply struct {
	OK             bool       `json:"ok"`
	Ready          bool       `json:"ready"`
	Error          string     `json:"error,omitempty"`
	Trace          string     `json:"trace,omitempty"`
	Parts          int        `json:"parts"`
	Volume         float64    `json:"volume"`
	Bounds         [6]float64 `json:"bounds"`
	Skipped        []string   `json:"skipped,omitempty"`
	FeaturesFailed []string   `json:"features_failed,omitempty"`
	ShapeBuilds    int        `json:"shape_builds"`

	Interferences          []geometry.Interference `json:"interferences,omitempty"`
	InterferencesTruncated bool                    `json:"interferences_truncated,omitempty"`

	STEP           string     `json:"step,omitempty"`
	Mesh           []meshPart `json:"mesh,omitempty"`
	MeshTriangles  int        `json:"mesh_triangles,omitempty"`
	MeshDeflection float64    `json:"mesh_deflection,omitempty"`
	MeshSimplified bool       `json:"mesh_simplified,omitempty"`
	MeshError      string     `json:"mesh_error,omitempty"`
}

type meshPart struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	Vertices  []float64 `json:"vertices"`
	Triangles []int32   `json:"triangles"`
}

// BuildDocument builds a document and, when format is "step", exports it.
//
// # Why a failure here is never fatal to the caller
//
// OCCT refuses to build invalid geometry rather than producing something wrong,
// which is the behaviour this kernel was chosen for — so "could not build" is a
// normal answer and arrives as an error the caller reports, not as a dead
// process. The kernel restarts itself on the next call if the process died.
// keepBuildable drops the parts marked unbuildable above, keeping order.
func keepBuildable(in []geometry.Solid) []geometry.Solid {
	out := in[:0]
	for _, s := range in {
		if s.Shape == "" {
			continue
		}
		out = append(out, s)
	}
	return out
}

func (k *Kernel) BuildDocument(ctx context.Context, doc geometry.Document, unit geometry.Unit, format string) (*Build, error) {
	const op = "cad.Kernel.BuildDocument"
	if !k.Available() {
		return nil, Unavailable(op)
	}
	// The unit is checked HERE as well as at the HTTP door, because this is the
	// last point before numbers become a file that declares its own scale. A
	// caller that skipped the door must not be able to produce one silently.
	if !unit.Known() {
		return nil, errs.New(op, errs.CodeValidationFailed).
			WithDetail("this assembly declares no unit FORGE can convert, and a STEP file states " +
				"its own scale — writing one would put a guess about scale inside the file. " +
				"Restate the assembly in mm, cm, m or in.")
	}
	// The solids and the operations come from ONE expansion of the document.
	// They used to be taken separately — solids expanded, operations from the
	// authored document — so a feature naming a repeated part named an id the
	// kernel had never been sent, and was dropped from every export.
	// docs/bugfix/2026-09-13-features-on-repeated-parts-were-never-applied.md
	// A design too large to build is refused as an error, not built as nothing
	// (geometry/limits.go, Phase 3 stage S0).
	if refusal := doc.DrawRefusal(); refusal != "" {
		return nil, errs.New(op, errs.CodeValidationFailed).WithDetail("%s", refusal)
	}
	solids, operations, featureProblems, inferred := geometry.SolidsAndOperations(doc, unit)

	// Scripted parts are RUN here, and only here.
	//
	// The geometry package describes shapes and executes nothing — putting
	// process execution inside it would make a pure description of geometry the
	// thing that runs model-written code. So Solids marks a scripted part as
	// "step" with nothing in it, and this fills it in.
	//
	// A script that will not run leaves its part out, with the reason, exactly
	// like an outline that cannot be read: a part missing for a stated reason is
	// something a reader can act on, and one that silently became a box is not.
	// Phase 4, stage K1 (docs/plan-2026-09-13-millions-of-parts.md): a script is run
	// ONCE per distinct source, however many copies of its part are placed. Each run
	// is a Python process of seconds, and a repeated scripted part used to run once
	// per copy. A failure is remembered too, so N copies do not fail N times.
	type scriptOutcome struct {
		step, detail string
		failed       bool
	}
	scripts := map[string]scriptOutcome{}
	scriptRuns := 0
	for i := range solids {
		if solids[i].Shape != "step" {
			continue
		}
		// Carried on the solid, not looked up by id: a copy of a repeated
		// scripted part is "part-2", which the authored document never knew.
		source := solids[i].Script
		if source == "" {
			inferred = append(inferred, fmt.Sprintf(
				"%s is a scripted part with no script, so it is not in this file.", solids[i].Label))
			solids[i].Shape = ""
			continue
		}
		outcome, ran := scripts[source]
		if !ran {
			res, err := k.RunScript(ctx, source, ScriptParameters(doc))
			scriptRuns++
			if err != nil {
				outcome = scriptOutcome{failed: true, detail: errs.DetailOf(err)}
			} else {
				outcome = scriptOutcome{step: res.STEP}
			}
			scripts[source] = outcome
		}
		if outcome.failed {
			inferred = append(inferred, fmt.Sprintf(
				"%s: %s, so it is not in this file.", solids[i].Label, outcome.detail))
			solids[i].Shape = ""
			continue
		}
		solids[i].STEP = outcome.step
	}
	solids = keepBuildable(solids)

	if len(solids) == 0 {
		return nil, errs.New(op, errs.CodeValidationFailed).
			WithDetail("this assembly has no parts FORGE can build, so there is nothing to export")
	}
	// Features are validated in Go so the kernel receives operations whose names
	// all resolve and whose radii are numbers. One that does not check out is
	// DROPPED rather than approximated, and the problem travels with the build:
	// an assembly missing a hole is wrong in a way a reader is told about, and
	// one where the hole landed somewhere else is wrong in a way nobody sees.
	for _, p := range featureProblems {
		inferred = append(inferred, fmt.Sprintf("%s %s.", p.Name, p.Detail))
	}

	k.mu.Lock()
	defer k.mu.Unlock()

	req := request{Solids: solids, Operations: operations, Format: format}
	res, err := k.roundTrip(ctx, req)
	var late *lateError
	if err != nil && !errors.As(err, &late) {
		// One retry, and exactly one. The overwhelmingly likely cause of an I/O
		// failure is a process that died between requests — a machine asleep, an
		// OOM, somebody's pkill — and restarting answers that. Retrying twice
		// would turn a kernel that crashes on a particular document into a loop.
		//
		// ‼️ A build that ran out of time is NOT retried: its process was working,
		// and a fresh one takes as long again. Fences:
		// TestKernel_ABuildThatRunsOutOfTimeIsNotRetriedAndSaysSo, and
		// TestKernel_AProcessThatDiesMidBuildIsStillRetriedOnce for the retry.
		k.stopLocked()
		k.log.Warn(ctx, logx.EventCADRestarted, "detail", err.Error())
		res, err = k.roundTrip(ctx, req)
	}
	if err != nil {
		if errors.As(err, &late) {
			// The killed process is reaped and the kernel reset NOW, so the next
			// build starts a fresh process instead of spending its one retry
			// discovering this one is dead.
			// Fence: TestKernel_AfterATimeoutTheKernelStartsAFreshProcessForTheNextBuild.
			k.stopLocked()
			k.log.Warn(ctx, logx.EventCADTimedOut, "detail", late.Error())
			return nil, lateRefusal(op, late)
		}
		return nil, errs.Wrap(op, errs.CodeConnectorUnavailable, err).
			WithDetail("the CAD kernel did not answer, and restarting it did not help")
	}
	if !res.OK {
		detail := res.Error
		if detail == "" {
			detail = "the kernel refused the assembly without saying why"
		}
		if res.Trace != "" {
			k.log.Warn(ctx, logx.EventCADRefused, "detail", detail, "trace", res.Trace)
		}
		// ‼️ The sidecar names every part it refused, and why, in Skipped. This
		// error used to drop them and say only "no part could be built" — which
		// is how "unsupported shape 'step'" stayed hidden while every scripted
		// part was left out of every export
		// (docs/bugfix/2026-09-10-scripted-parts-never-exported.md).
		// Fence: TestKernel_ARefusedAssemblyNamesWhatItRefused.
		if len(res.Skipped) > 0 {
			detail += " — " + strings.Join(res.Skipped, "; ")
		}
		return nil, errs.New(op, errs.CodeValidationFailed).
			WithDetail("the CAD kernel could not build this assembly: %s", detail)
	}

	out := &Build{Parts: res.Parts, Volume: res.Volume, Bounds: res.Bounds,
		Skipped: res.Skipped, FeatureFailures: res.FeaturesFailed, Inferred: inferred,
		Interferences: res.Interferences, InterferencesTruncated: res.InterferencesTruncated,
		ShapeBuilds: res.ShapeBuilds, ScriptRuns: scriptRuns}
	if res.STEP != "" {
		decoded, err := base64.StdEncoding.DecodeString(res.STEP)
		if err != nil {
			return nil, errs.Wrap(op, errs.CodeExternalProtocol, err).
				WithDetail("the kernel returned a file this build could not read")
		}
		out.STEP = decoded
	}
	if len(res.Mesh) > 0 {
		out.Mesh = make([]MeshPart, 0, len(res.Mesh))
		for _, m := range res.Mesh {
			out.Mesh = append(out.Mesh, MeshPart{
				ID: m.ID, Label: m.Label, Vertices: m.Vertices, Triangles: m.Triangles,
			})
		}
	}
	out.Triangles = res.MeshTriangles
	out.Deflection = res.MeshDeflection
	out.Simplified = res.MeshSimplified
	out.MeshError = res.MeshError
	return out, nil
}

// BuildMesh builds a document and returns the surface of the solid it makes.
//
// Separate from BuildDocument only in what it asks the kernel for. Deliberately
// the same code path otherwise: a mesh that came from a different build than the
// STEP file could disagree with it, and then the picture and the file would be
// two claims about one design.
func (k *Kernel) BuildMesh(ctx context.Context, doc geometry.Document, unit geometry.Unit) (*Build, error) {
	return k.BuildDocument(ctx, doc, unit, "mesh")
}

// lateError is a build whose process was killed because time ran out while it was
// still working: the kernel's own limit, or the caller's context.
//
// # Why it is its own type
//
// The retry in BuildDocument is for a process that DIED — an OOM, a pkill, a
// machine asleep — and a fresh process answers that. A process killed for its time
// was alive and building, and a fresh one given the same build takes as long
// again: retrying turned a 31 s build into a 60 s wait, and the second kill into
// "the CAD kernel did not answer, and restarting it did not help" under
// CONNECTOR_UNAVAILABLE. BuildDocument asks errors.As for this, and does not
// retry it.
type lateError struct {
	// limit is the kernel's own limit, when that is what ran out.
	limit time.Duration
	// caller is the caller's context error, when that ended first.
	caller error
}

func (e *lateError) Error() string {
	if e.caller != nil {
		return "the request ended before the kernel answered: " + e.caller.Error()
	}
	return fmt.Sprintf("the kernel did not answer within %s", e.limit)
}

func (e *lateError) Unwrap() error { return e.caller }

// lateRefusal is the error for a build stopped because time ran out.
//
// CAD_KERNEL_TIMEOUT (504, not retryable — errs/code.go says why), with a detail
// naming which time ran out. A caller that CANCELLED rather than ran out of time
// has gone, and keeps CONNECTOR_UNAVAILABLE with a sentence that says what
// happened: nobody is reading that reply.
func lateRefusal(op string, late *lateError) error {
	switch {
	case late.caller == nil:
		return errs.Wrap(op, errs.CodeKernelTimeout, late).
			WithDetail("this build took longer than the CAD kernel's %s limit, so it was stopped and "+
				"not retried: the same build would take as long again. Build a smaller part of the "+
				"design, or split it into smaller subassemblies", late.limit)
	case errors.Is(late.caller, context.DeadlineExceeded):
		return errs.Wrap(op, errs.CodeKernelTimeout, late).
			WithDetail("the request's own deadline ended before the CAD kernel finished this build, " +
				"so it was stopped and not retried: the same build would take at least as long again")
	default:
		return errs.Wrap(op, errs.CodeConnectorUnavailable, late).
			WithDetail("the request ended before the CAD kernel finished, so the build was stopped")
	}
}

// roundTrip sends one request and reads one reply. Caller holds the mutex.
func (k *Kernel) roundTrip(ctx context.Context, req request) (*reply, error) {
	if err := k.startLocked(ctx); err != nil {
		return nil, err
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if _, err := k.stdin.Write(append(body, '\n')); err != nil {
		return nil, fmt.Errorf("writing to the kernel: %w", err)
	}

	// The deadline is enforced by a goroutine that kills the process, because a
	// blocking Read on a pipe does not observe a context. Killing is the only
	// thing that ends it, and it is also the right outcome: a kernel that has
	// not answered in thirty seconds is not going to.
	//
	// ‼️ The goroutine records WHY it killed the process before it does. To the
	// read below, a process killed for its time and one that crashed are the same
	// EOF, and until 2026-09-15 they were treated the same: retried on a fresh
	// process, killed again, reported as "no working backend". See lateError.
	done := make(chan struct{})
	defer close(done)
	var stopped atomic.Pointer[lateError]
	go func() {
		timer := time.NewTimer(k.timeout)
		defer timer.Stop()
		select {
		case <-done:
		case <-ctx.Done():
			stopped.Store(&lateError{caller: ctx.Err()})
			k.killLocked()
		case <-timer.C:
			stopped.Store(&lateError{limit: k.timeout})
			k.killLocked()
		}
	}()

	line, err := k.stdout.ReadBytes('\n')
	if err != nil {
		if late := stopped.Load(); late != nil {
			return nil, late
		}
		return nil, fmt.Errorf("reading from the kernel: %w", err)
	}
	var res reply
	if err := json.Unmarshal(line, &res); err != nil {
		return nil, fmt.Errorf("the kernel wrote something that is not a reply: %w", err)
	}
	return &res, nil
}

func (k *Kernel) startLocked(ctx context.Context) error {
	if k.started {
		return nil
	}
	// The sidecar is embedded and written out, so a deployment is one binary and
	// the script cannot drift from the Go that speaks to it.
	dir, err := os.MkdirTemp("", "forge-cad-")
	if err != nil {
		return err
	}
	script := filepath.Join(dir, "sidecar.py")
	if err := os.WriteFile(script, sidecarSource, 0o600); err != nil {
		return err
	}

	cmd := exec.Command(k.python, script)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	// stderr is drained rather than inherited: a Python warning on a shared
	// stderr interleaves with this process's own logs, and a full pipe nobody
	// reads blocks the child forever.
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting the CAD kernel with %q: %w", k.python, err)
	}
	go func() { _, _ = io.Copy(io.Discard, stderr) }()

	k.cmd, k.stdin, k.stdout, k.script = cmd, stdin, bufio.NewReaderSize(stdout, 1<<20), dir

	// The ready banner is written AFTER the import, so waiting for it is what
	// distinguishes "still starting" from "will never start".
	type banner struct {
		res *reply
		err error
	}
	ch := make(chan banner, 1)
	go func() {
		line, err := k.stdout.ReadBytes('\n')
		if err != nil {
			ch <- banner{err: err}
			return
		}
		var r reply
		if err := json.Unmarshal(line, &r); err != nil {
			ch <- banner{err: fmt.Errorf("the kernel's first line was not a banner: %w", err)}
			return
		}
		ch <- banner{res: &r}
	}()

	select {
	case b := <-ch:
		if b.err != nil || b.res == nil || !b.res.Ready {
			k.stopLocked()
			detail := "the kernel exited while starting"
			if b.err != nil {
				detail = b.err.Error()
			} else if b.res != nil && b.res.Error != "" {
				detail = b.res.Error
			}
			return errors.New(detail)
		}
	case <-time.After(startTimeout):
		k.stopLocked()
		return fmt.Errorf("the CAD kernel did not start within %s", startTimeout)
	case <-ctx.Done():
		k.stopLocked()
		return ctx.Err()
	}

	k.started = true
	k.log.Info(ctx, logx.EventCADStarted, "python", k.python)
	return nil
}

func (k *Kernel) killLocked() {
	if k.cmd != nil && k.cmd.Process != nil {
		_ = k.cmd.Process.Kill()
	}
}

func (k *Kernel) stopLocked() {
	k.killLocked()
	if k.cmd != nil {
		_ = k.cmd.Wait()
	}
	if k.stdin != nil {
		_ = k.stdin.Close()
	}
	if k.script != "" {
		_ = os.RemoveAll(k.script)
	}
	k.cmd, k.stdin, k.stdout, k.script, k.started = nil, nil, nil, "", false
}

// Close stops the kernel. Safe on a kernel that was never started.
func (k *Kernel) Close() {
	if k == nil {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.stopLocked()
}
