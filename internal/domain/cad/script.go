package cad

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

//go:embed script.py
var scriptSource []byte

// builders.txt is the generated list of build123d names a script may use. It
// travels with the runner because the runner reads it from beside itself: a
// deployment is one binary, and the list must not be able to drift from the
// Python that enforces it.
//
//go:embed builders.txt
var builderManifest []byte

// Running a build123d script the model wrote.
//
// # Why this is a separate process and not the sidecar
//
// The sidecar is long-lived and holds the kernel for every request in the
// deployment. A script that loops forever, allocates until the node swaps, or
// segfaults OCCT would take that with it — so every script gets its own
// short-lived process, and the worst it can do is fail.
//
// # What the sandbox is, and what it is not
//
// Two layers, described in full in script.py: an AST whitelist that refuses
// imports, dunder attributes and everything else not on a list BEFORE anything
// runs, and a stripped process — no environment, a temporary working directory,
// CPU and address-space limits, stdin closed.
//
// It is NOT a container and does not claim to be. A CPython sandbox is a
// defence in depth, not a boundary: the AST check is the part that has to hold,
// and the process limits are what stands if it does not. On a node where the
// kernel already runs arbitrary OCCT operations on model-written geometry, this
// raises the ceiling of what a model can express without lowering the floor of
// what it can reach — but a deployment that cannot accept ANY of that should
// leave FORGE_ALLOW_SCRIPTS unset, and then this path does not exist.
//
// The environment is scrubbed rather than inherited because this one holds a
// database URL and a provider key.
type ScriptResult struct {
	// STEP is the solid the script built, in the one format both sides already
	// speak. Not a mesh: a tessellation thrown away the surfaces.
	STEP string
	// Volume is the solid's, and is positive. A script that produced a surface,
	// an empty compound or a self-intersecting body is refused before this.
	Volume float64
}

// ErrScriptsDisabled is returned when this deployment does not run scripts.
var ErrScriptsDisabled = errors.New("this deployment does not run model-written scripts")

// scriptTimeout is the wall clock a script gets. The CPU limit inside the
// process is lower, so an honest long computation is stopped by the CPU limit
// with a clear reason, and this catches a process that is stuck rather than busy.
const scriptTimeout = 30 * time.Second

const (
	scriptCPUSeconds  = 10
	scriptMemoryBytes = 1 << 30 // 1 GiB, which OCCT wants for a large tessellation
)

// ScriptParameters is the document's own numbers, in millimetres, ready to be
// names in a script.
//
// # Why the document is resolved here rather than passed whole
//
// A script needs NUMBERS. The document carries parameters, derived expressions,
// units and provenance, and resolving all of that is geometry's job and is
// already done — Resolve walks the dependency graph and reports what does not
// add up. This takes the answer and nothing else, so the sandbox never learns
// what a Provenance is.
//
// ‼️ Lengths are converted to MILLIMETRES, and that is not a detail. A script
// hands back STEP, which build123d writes in millimetres, and every other part
// in the document has its dimensions converted the same way before the kernel
// sees them. A parameter authored in cm injected as its raw number would build a
// part ten times too small, silently, in the one shape nobody can read
// dimensions off. Anything that is not a length — a count, an angle, a ratio —
// is passed through untouched, because there is nothing to convert it to.
func ScriptParameters(doc geometry.Document) map[string]float64 {
	out := map[string]float64{}
	for name, v := range doc.Resolve().Values {
		number := v.Number
		if q, ok := v.Quantity(); ok {
			if mm, converted := q.In(geometry.Millimetre); converted {
				number = mm.Value()
			}
		}
		out[name] = number
	}
	return out
}

// RunScript executes a script and returns the solid it built.
//
// The parameters are the document's own numbers (see ScriptParameters), in scope
// as names. nil is a script with none, which is what every script had before.
func (k *Kernel) RunScript(ctx context.Context, source string, parameters map[string]float64) (*ScriptResult, error) {
	const op = "cad.Kernel.RunScript"
	if !k.Available() {
		return nil, Unavailable(op)
	}
	if !k.scripts {
		return nil, errs.New(op, errs.CodeConnectorUnavailable).
			WithDetail("this deployment does not run model-written scripts. Set " +
				"FORGE_ALLOW_SCRIPTS=1 to enable them, having read what the sandbox does " +
				"and does not promise in internal/domain/cad/script.go")
	}

	dir, err := os.MkdirTemp("", "forge-script-")
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeInternal, err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	runner := filepath.Join(dir, "script.py")
	if err := os.WriteFile(runner, scriptSource, 0o600); err != nil {
		return nil, errs.Wrap(op, errs.CodeInternal, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "builders.txt"), builderManifest, 0o600); err != nil {
		return nil, errs.Wrap(op, errs.CodeInternal, err)
	}

	ctx, cancel := context.WithTimeout(ctx, scriptTimeout)
	defer cancel()

	request, err := json.Marshal(map[string]any{
		"source": source, "cpu_seconds": scriptCPUSeconds, "memory_bytes": scriptMemoryBytes,
		"parameters": parameters,
	})
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeSerializationFail, err)
	}

	// The interpreter is resolved to an ABSOLUTE path before Dir is set. Go
	// resolves a relative binary path against cmd.Dir, so a kernel configured
	// as ".cadvenv/bin/python" — which is how it is configured in development —
	// would be looked for inside the temp directory and every script would fail
	// with an empty stdout and no reason.
	python := k.python
	if abs, err := filepath.Abs(python); err == nil && !filepath.IsAbs(python) {
		if _, statErr := os.Stat(abs); statErr == nil {
			python = abs
		}
	}

	// -I is isolated mode: no user site-packages, no PYTHON* environment, and
	// the script's own directory kept off sys.path.
	cmd := exec.CommandContext(ctx, python, "-I", runner)
	cmd.Stdin = bytes.NewReader(request)
	// The environment is EMPTY apart from what Python cannot start without. This
	// process holds FORGE_DATABASE_URL and FORGE_LLM_API_KEY, and a script that
	// could read os.environ would be reading those — the AST check refuses the
	// import that reaches them, and this is what stands if it is ever wrong.
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "LC_ALL=C"}
	cmd.Dir = dir

	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, errs.New(op, errs.CodeExternalUnavailable).
				WithDetail("the script did not finish within %s. A drawing this expensive is "+
					"one nobody can wait for; build it from fewer, larger pieces", scriptTimeout)
		}
		// A non-zero exit with no JSON is the process being killed — the CPU or
		// memory limit, or OCCT crashing. Named, because "it failed" sends the
		// model back to try the same thing.
		if out.Len() == 0 {
			return nil, errs.New(op, errs.CodeExternalProtocol).
				WithDetail("the script was stopped before it finished: it used more time or "+
					"memory than a single part is allowed, or the kernel could not build what "+
					"it described. %s", tail(errOut.String(), 400))
		}
	}

	var reply struct {
		OK      bool    `json:"ok"`
		Refused bool    `json:"refused"`
		Error   string  `json:"error"`
		Trace   string  `json:"trace"`
		STEP    string  `json:"step"`
		Volume  float64 `json:"volume"`
	}
	if err := json.Unmarshal(out.Bytes(), &reply); err != nil {
		return nil, errs.Wrap(op, errs.CodeExternalProtocol, err).
			WithDetail("the script runner returned something unreadable: %s", tail(out.String(), 300))
	}
	if !reply.OK {
		code := errs.CodeValidationFailed
		if !reply.Refused {
			code = errs.CodeExternalProtocol
		}
		return nil, errs.New(op, code).WithDetail("%s", reply.Error)
	}
	return &ScriptResult{STEP: reply.STEP, Volume: reply.Volume}, nil
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
