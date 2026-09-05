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
	return &Kernel{python: strings.TrimSpace(python), log: log}
}

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
	// Parts is how many solids were placed, Skipped names any that could not be.
	Parts   int
	Skipped []string
	// Volume is the assembly's, in the cube of the document's declared unit.
	// Zero for an assembly of faces, which have none.
	Volume float64
	// Bounds is the assembly's extent: minX, minY, minZ, maxX, maxY, maxZ.
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
}

type request struct {
	Solids []geometry.Solid `json:"solids"`
	Format string           `json:"format,omitempty"`
}

type reply struct {
	OK      bool       `json:"ok"`
	Ready   bool       `json:"ready"`
	Error   string     `json:"error,omitempty"`
	Trace   string     `json:"trace,omitempty"`
	Parts   int        `json:"parts"`
	Volume  float64    `json:"volume"`
	Bounds  [6]float64 `json:"bounds"`
	Skipped []string   `json:"skipped,omitempty"`
	STEP    string     `json:"step,omitempty"`
}

// BuildDocument builds a document and, when format is "step", exports it.
//
// # Why a failure here is never fatal to the caller
//
// OCCT refuses to build invalid geometry rather than producing something wrong,
// which is the behaviour this kernel was chosen for — so "could not build" is a
// normal answer and arrives as an error the caller reports, not as a dead
// process. The kernel restarts itself on the next call if the process died.
func (k *Kernel) BuildDocument(ctx context.Context, doc geometry.Document, unit geometry.Unit, format string) (*Build, error) {
	const op = "cad.Kernel.BuildDocument"
	if !k.Available() {
		return nil, Unavailable(op)
	}
	solids, inferred := geometry.Solids(doc, unit)
	if len(solids) == 0 {
		return nil, errs.New(op, errs.CodeValidationFailed).
			WithDetail("this assembly has no parts FORGE can build, so there is nothing to export")
	}

	k.mu.Lock()
	defer k.mu.Unlock()

	res, err := k.roundTrip(ctx, request{Solids: solids, Format: format})
	if err != nil {
		// One retry, and exactly one. The overwhelmingly likely cause of an I/O
		// failure is a process that died between requests — a machine asleep, an
		// OOM, somebody's pkill — and restarting answers that. Retrying twice
		// would turn a kernel that crashes on a particular document into a loop.
		k.stopLocked()
		k.log.Warn(ctx, logx.EventCADRestarted, "detail", err.Error())
		res, err = k.roundTrip(ctx, request{Solids: solids, Format: format})
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
		return nil, errs.New(op, errs.CodeValidationFailed).
			WithDetail("the CAD kernel could not build this assembly: %s", detail)
	}

	out := &Build{Parts: res.Parts, Volume: res.Volume, Bounds: res.Bounds,
		Skipped: res.Skipped, Inferred: inferred}
	if res.STEP != "" {
		decoded, err := base64.StdEncoding.DecodeString(res.STEP)
		if err != nil {
			return nil, errs.Wrap(op, errs.CodeExternalProtocol, err).
				WithDetail("the kernel returned a file this build could not read")
		}
		out.STEP = decoded
	}
	return out, nil
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
	done := make(chan struct{})
	defer close(done)
	go func() {
		timer := time.NewTimer(buildTimeout)
		defer timer.Stop()
		select {
		case <-done:
		case <-ctx.Done():
			k.killLocked()
		case <-timer.C:
			k.killLocked()
		}
	}()

	line, err := k.stdout.ReadBytes('\n')
	if err != nil {
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
