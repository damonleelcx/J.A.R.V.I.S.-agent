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
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
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

// Kernel is a pool of build123d processes, each started on demand and kept warm.
//
// Each process still serves one request at a time: it has one stdin, and a second
// writer would interleave two JSON documents into one line. What the pool changes
// is that a build waits for A process rather than for THE process, so one slow
// assembly no longer holds up every other build in the deployment. One process is
// the default; see WithPool and sidecar_process.go.
type Kernel struct {
	python string
	log    *logx.Logger
	// scripts says whether this deployment runs model-written build123d.
	// OFF unless a deployment turns it on, because it is the one feature here
	// that executes text a model produced — see script.go for what the sandbox
	// does and does not promise.
	scripts bool

	// size is how many processes serve builds at once. The pool is made on
	// first use, so a kernel nobody builds with owns nothing.
	size  int
	once  sync.Once
	slots chan *sidecar
	all   []*sidecar
}

// New returns a kernel that runs through the given Python interpreter.
//
// An empty path means this deployment has no kernel, which is the DEFAULT and is
// not an error. Nothing starts here; the process is started on the first build,
// so a deployment that never exports a parametric file never pays for one.
func New(python string, log *logx.Logger) *Kernel {
	return &Kernel{python: strings.TrimSpace(python), log: log, size: 1}
}

// WithPool sets how many kernel processes serve builds at once. Fewer than one is
// one. Set it before the first build: the pool is made once and never resized.
//
// # Why one is the default (Phase 4, stage K3)
//
// Every process holds its own build123d and OpenCASCADE in memory, and nothing
// caps a process below its pod's limit, so a process too many is an OOM that
// takes the whole container with it. forged shares a 1 GiB limit with the API and
// the audio server. A second process there is a number to measure first, not a
// default to guess.
func (k *Kernel) WithPool(n int) *Kernel {
	if n < 1 {
		n = 1
	}
	k.size = n
	return k
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
			"Set FORGE_CAD_PYTHON to a Python interpreter with build123d installed " +
			"(python3 -m venv venv && ./venv/bin/pip install build123d). It is unset by default: " +
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
	// InterferencesFound is how many pairs the check found sharing material, and
	// Interferences lists at most the worst _INTERFERENCE_LIST_LIMIT of them
	// (sidecar.py, 10,000). A reply
	// that found more is SUMMARIZED, never cut silently: InterferencesFound says how
	// many there were, and InterferencesSummarized says the list is the worst of
	// them rather than all of them.
	//
	// # Why a list has a bound at all
	//
	// A million-occurrence airframe barrel shares material in 1,760,000 pairs — every
	// rivet in its skin and its stringer — and those are 15 distinct answers reused.
	// Listed in full the reply is hundreds of megabytes that no reader uses: the turn
	// names three, and a repair that sent them all would send a model a prompt of
	// that size (docs/spikes/2026-09-15-next-scale-walls).
	//
	// This is not a truncated CHECK. Every pair was checked; what is bounded is how
	// many of the answers are written down. InterferencesTruncated keeps meaning
	// "not every pair was checked", and the two are said separately.
	InterferencesFound      int
	InterferencesSummarized bool
	// InterferenceBoxTests is how many pairs of bounding boxes the interference
	// check compared to choose which pairs pay for a boolean. Reported so the broad
	// phase's cost is a count a test can read (Phase 4, stage K2b): sorting and
	// sweeping keeps it near linear on an assembly that spreads out, where comparing
	// every pair is n(n-1)/2, 8.4 million at 4,096 parts. Since Phase 5, stage V1
	// the broad phase is a grid over all three axes.
	InterferenceBoxTests int
	// InterferencePairs is how many pairs' boxes overlap, InterferenceBooleans how
	// many exact booleans were paid for, and InterferenceReused how many pairs were
	// answered by a boolean already measured at the same pose (Phase 5, stage V1) —
	// or at a pose slid along a box it lies wholly inside, which shares the same
	// volume (sidecar.py, _INTERFERENCE_SLIDE; docs/spikes/2026-09-15-large-box-index).
	// The pair budget counts booleans, so Booleans + Reused below Pairs is exactly
	// a truncated check — the count "checked X of Y" is built from.
	InterferencePairs    int
	InterferenceBooleans int
	InterferenceReused   int
	// ShapeBuilds is how many distinct shapes the kernel built, and ScriptRuns how
	// many scripts it ran: each distinct shape and each distinct script ONCE per build,
	// however many occurrences place it (Phase 4, stage K1). Reported so "built once"
	// is a count a test and a budget can read, not a claim.
	ShapeBuilds int
	ScriptRuns  int
	// Phases is where the kernel spent this build. Reported so a slow build says
	// which step was slow, in the log and to a test, without timing the whole
	// build: the assembly and export steps are fenced to grow linearly
	// (TestKernel_ExportingManyOccurrencesGrowsLinearly, Phase 4 stage K2), and the
	// interference check by the box tests it counts (InterferenceBoxTests, K2b).
	Phases Phases
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
	//
	// Since Phase 4, stage K4 it holds only the parts a feature CHANGED. Every
	// other part is a placed copy of a shape built once, and arrives as one of
	// MeshInstances: its definition's triangles, tessellated once, and the matrix
	// that places them. WorldMeshes gives every part in assembly coordinates.
	Mesh []MeshPart
	// MeshDefinitions is each distinct shape's surface in its own frame, and
	// MeshInstances each placed copy of one. Empty unless a mesh was asked for.
	MeshDefinitions []MeshDefinition
	MeshInstances   []MeshInstance
	// Triangles is the whole mesh's count — each definition's triangles counted
	// once, however many copies place it — and Deflection the tolerance in
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
	// Properties is each surviving part's volume, centre of volume and box, in
	// millimetres. Empty unless asked for with BuildProperties (Phase 5, stage V3).
	Properties []geometry.SolidMeasure
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

// MeshDefinition is one shape's surface in its own frame, in millimetres, drawn
// once however many copies place it.
type MeshDefinition struct {
	Vertices  []float64
	Triangles []int32
}

// MeshInstance is one placed copy of a MeshDefinition.
//
// Matrix is the 4×4 placement in COLUMN-major order — the order WebGL reads a
// uniform or an instance attribute in — so a point p lands at
// (m[0]p.x + m[4]p.y + m[8]p.z + m[12], m[1]… + m[13], m[2]… + m[14]). A mirrored
// copy's reflection is in its definition, not its matrix: K1 builds the mirrored
// shape as a shape of its own.
type MeshInstance struct {
	ID         string
	Label      string
	Definition int
	Matrix     [16]float64
}

// WorldMeshes is every surviving part's surface in assembly coordinates: the
// parts a feature changed as the kernel sent them, then each placed copy of a
// definition moved by its matrix. It is what Mesh held before stage K4, for a
// reader that draws parts rather than instances.
func (b *Build) WorldMeshes() []MeshPart {
	out := make([]MeshPart, 0, len(b.Mesh)+len(b.MeshInstances))
	out = append(out, b.Mesh...)
	for _, in := range b.MeshInstances {
		if in.Definition < 0 || in.Definition >= len(b.MeshDefinitions) {
			continue
		}
		d, m := b.MeshDefinitions[in.Definition], in.Matrix
		v := make([]float64, len(d.Vertices))
		for i := 0; i+2 < len(d.Vertices); i += 3 {
			x, y, z := d.Vertices[i], d.Vertices[i+1], d.Vertices[i+2]
			v[i] = m[0]*x + m[4]*y + m[8]*z + m[12]
			v[i+1] = m[1]*x + m[5]*y + m[9]*z + m[13]
			v[i+2] = m[2]*x + m[6]*y + m[10]*z + m[14]
		}
		out = append(out, MeshPart{ID: in.ID, Label: in.Label, Vertices: v, Triangles: d.Triangles})
	}
	return out
}

type request struct {
	Solids     []geometry.Solid     `json:"solids"`
	Operations []geometry.Operation `json:"operations,omitempty"`
	Format     string               `json:"format,omitempty"`
	// Deflection is the tessellation tolerance in millimetres. Zero lets the
	// kernel choose from the model's own size — 0.1 mm is invisible on a
	// bracket and catastrophic on a car body, so a constant cannot serve both.
	Deflection float64 `json:"deflection,omitempty"`
	// Properties asks for each part's volume, centre and box (see BuildProperties).
	Properties bool `json:"properties,omitempty"`
}

type partProperties struct {
	ID       string      `json:"id"`
	Volume   float64     `json:"volume"`
	Centroid *[3]float64 `json:"centroid"`
	Bounds   *[6]float64 `json:"bounds"`
}

type reply struct {
	OK             bool         `json:"ok"`
	Ready          bool         `json:"ready"`
	Error          string       `json:"error,omitempty"`
	Trace          string       `json:"trace,omitempty"`
	Parts          int          `json:"parts"`
	Volume         float64      `json:"volume"`
	Bounds         [6]float64   `json:"bounds"`
	Skipped        []string     `json:"skipped,omitempty"`
	FeaturesFailed []string     `json:"features_failed,omitempty"`
	ShapeBuilds    int          `json:"shape_builds"`
	Phases         phaseSeconds `json:"phases"`

	Interferences          []geometry.Interference `json:"interferences,omitempty"`
	InterferencesTruncated bool                    `json:"interferences_truncated,omitempty"`
	// A pointer, so a reply that does not carry the count is told apart from one
	// that found nothing (see buildOf).
	InterferencesFound      *int `json:"interferences_found"`
	InterferencesSummarized bool `json:"interferences_summarized,omitempty"`
	InterferenceBoxTests    int  `json:"interference_box_tests"`
	InterferencePairs       int  `json:"interference_pairs"`
	InterferenceBooleans    int  `json:"interference_booleans"`
	InterferenceReused      int  `json:"interference_reused"`

	STEP            string           `json:"step,omitempty"`
	Mesh            []meshPart       `json:"mesh,omitempty"`
	MeshDefinitions []meshDefinition `json:"mesh_definitions,omitempty"`
	MeshInstances   []meshInstance   `json:"mesh_instances,omitempty"`
	MeshTriangles   int              `json:"mesh_triangles,omitempty"`
	MeshDeflection  float64          `json:"mesh_deflection,omitempty"`
	MeshSimplified  bool             `json:"mesh_simplified,omitempty"`
	MeshError       string           `json:"mesh_error,omitempty"`

	PartProperties []partProperties `json:"part_properties,omitempty"`
}

// phaseSeconds is the reply's "phases": seconds per phase of a build, written by
// _lap in sidecar.py. A phase the build never reached is absent and reads as zero.
type phaseSeconds struct {
	Shapes        float64 `json:"shapes"`
	Features      float64 `json:"features"`
	Assembly      float64 `json:"assembly"`
	Interferences float64 `json:"interferences"`
	Export        float64 `json:"export"`
	Mesh          float64 `json:"mesh"`
}

func (p phaseSeconds) durations() Phases {
	d := func(seconds float64) time.Duration { return time.Duration(seconds * float64(time.Second)) }
	return Phases{Shapes: d(p.Shapes), Features: d(p.Features), Assembly: d(p.Assembly),
		Interferences: d(p.Interferences), Export: d(p.Export), Mesh: d(p.Mesh)}
}

type meshPart struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	Vertices  []float64 `json:"vertices"`
	Triangles []int32   `json:"triangles"`
}

type meshDefinition struct {
	Vertices  []float64 `json:"vertices"`
	Triangles []int32   `json:"triangles"`
}

type meshInstance struct {
	ID         string      `json:"id"`
	Label      string      `json:"label"`
	Definition int         `json:"definition"`
	Matrix     [16]float64 `json:"matrix"`
}

// Phases is the kernel's time per phase of one build. Scripts run before the
// kernel is asked and are not in it.
type Phases struct {
	// Shapes is building each distinct shape and placing every occurrence.
	Shapes time.Duration
	// Features is applying the document's operations.
	Features time.Duration
	// Assembly is gathering the kept solids and measuring their volume and extent.
	Assembly time.Duration
	// Interferences is the check for parts that share material.
	Interferences time.Duration
	// Export is writing the STEP file, zero unless one was asked for.
	Export time.Duration
	// Mesh is tessellating, zero unless a mesh was asked for.
	Mesh time.Duration
}

// LogFields is the phases as structured log fields, in milliseconds.
func (p Phases) LogFields() []any {
	ms := func(d time.Duration) int64 { return d.Milliseconds() }
	return []any{"kernel_shapes_ms", ms(p.Shapes), "kernel_features_ms", ms(p.Features),
		"kernel_assembly_ms", ms(p.Assembly), "kernel_interferences_ms", ms(p.Interferences),
		"kernel_export_ms", ms(p.Export), "kernel_mesh_ms", ms(p.Mesh)}
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
	return k.build(ctx, doc, unit, format, false)
}

// BuildProperties builds a document and returns each part's volume, centre of
// volume and box, for geometry.MassProperties (Phase 5, stage V3).
//
// The same build as every other reader's, asking for one more thing: a mass
// report from a different build than the export could disagree with it.
func (k *Kernel) BuildProperties(ctx context.Context, doc geometry.Document, unit geometry.Unit) (*Build, error) {
	return k.build(ctx, doc, unit, "", true)
}

// build is BuildDocument, optionally asking the kernel for each part's properties.
func (k *Kernel) build(ctx context.Context, doc geometry.Document, unit geometry.Unit, format string, properties bool) (*Build, error) {
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

	// A build waits HERE for a free process, not inside one, so a caller that gives
	// up stops waiting instead of queueing behind a lock that ignores it.
	s, err := k.acquire(ctx)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeConnectorUnavailable, err).
			WithDetail("no CAD kernel process became free before the request ended")
	}
	defer k.release(s)

	req := request{Solids: solids, Operations: operations, Format: format, Properties: properties}
	res, err := s.roundTrip(ctx, req)
	if err != nil {
		// One retry, and exactly one. The overwhelmingly likely cause of an I/O
		// failure is a process that died between requests — a machine asleep, an
		// OOM, somebody's pkill — and restarting answers that. Retrying twice
		// would turn a kernel that crashes on a particular document into a loop.
		// The retry replaces THIS slot's process; the others are untouched.
		s.stop()
		k.log.Warn(ctx, logx.EventCADRestarted, "slot", s.slot, "detail", err.Error())
		res, err = s.roundTrip(ctx, req)
		if err != nil {
			return nil, errs.Wrap(op, errs.CodeConnectorUnavailable, err).
				WithDetail("the CAD kernel did not answer, and restarting it did not help")
		}
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

	return buildOf(res, inferred, scriptRuns)
}

// buildOf is what a successful reply says, as a Build. Separate from build so the
// reply's cost at scale can be measured on exactly this path
// (TestScaleUp_MeasureTheInterferenceReply).
func buildOf(res *reply, inferred []string, scriptRuns int) (*Build, error) {
	const op = "cad.Kernel.BuildDocument"
	out := &Build{Parts: res.Parts, Volume: res.Volume, Bounds: res.Bounds,
		Skipped: res.Skipped, FeatureFailures: res.FeaturesFailed, Inferred: inferred,
		Interferences: res.Interferences, InterferencesTruncated: res.InterferencesTruncated, InterferenceBoxTests: res.InterferenceBoxTests,
		InterferencePairs: res.InterferencePairs, InterferenceBooleans: res.InterferenceBooleans, InterferenceReused: res.InterferenceReused,
		ShapeBuilds: res.ShapeBuilds, ScriptRuns: scriptRuns, Phases: res.Phases.durations()}
	// ‼️ Never fewer found than listed, and a list shorter than the count is a
	// summary whatever the flag says: a reader that sees N findings and a count of
	// N must be able to trust that nothing was left out.
	out.InterferencesFound = len(res.Interferences)
	if res.InterferencesFound != nil && *res.InterferencesFound > out.InterferencesFound {
		out.InterferencesFound = *res.InterferencesFound
	}
	out.InterferencesSummarized = res.InterferencesSummarized || out.InterferencesFound > len(res.Interferences)
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
	for _, d := range res.MeshDefinitions {
		out.MeshDefinitions = append(out.MeshDefinitions, MeshDefinition{Vertices: d.Vertices, Triangles: d.Triangles})
	}
	for _, in := range res.MeshInstances {
		out.MeshInstances = append(out.MeshInstances, MeshInstance{
			ID: in.ID, Label: in.Label, Definition: in.Definition, Matrix: in.Matrix,
		})
	}
	out.Triangles = res.MeshTriangles
	out.Deflection = res.MeshDeflection
	out.Simplified = res.MeshSimplified
	out.MeshError = res.MeshError
	for _, p := range res.PartProperties {
		m := geometry.SolidMeasure{ID: p.ID, Volume: p.Volume}
		// A part whose centre or box the kernel could not read is kept, marked
		// unmeasured, so the roll-up can name it rather than weigh a guess.
		if p.Centroid != nil && p.Bounds != nil {
			m.Centroid, m.Bounds, m.Measured = *p.Centroid, *p.Bounds, true
		}
		out.Properties = append(out.Properties, m)
	}
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
