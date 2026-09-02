package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// maxReadBytes bounds what a single read returns.
//
// Not a performance limit. An unbounded read of a large file lands the whole
// thing in a model request, which costs the goal's token budget and often
// exceeds the context window — so the failure appears as a confusing model error
// far from the tool that caused it.
const maxReadBytes = 256 << 10

// resolveInWorkspace turns a caller-supplied path into an absolute path proven
// to be inside the workspace.
//
// # Why EvalSymlinks
//
// filepath.Clean removes "..", which stops the obvious traversal. It does not
// stop a symlink inside the workspace pointing at /etc — the cleaned path looks
// local and resolves elsewhere. Resolving symlinks and re-checking the prefix is
// what actually contains the tool, and it is the check people leave out.
func resolveInWorkspace(workspace, rel string) (string, error) {
	const op = "tools.resolveInWorkspace"

	if workspace == "" {
		return "", errs.New(op, errs.CodeInvariantViolated).
			WithDetail("no workspace is configured for this task; a filesystem tool has nowhere it is allowed to act")
	}
	root, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", errs.Wrap(op, errs.CodeInternal, err).
			WithDetail("the workspace path %q could not be resolved", workspace)
	}
	if rel == "" {
		return root, nil
	}
	if filepath.IsAbs(rel) {
		return "", errs.New(op, errs.CodeForbidden).
			WithDetail("path %q is absolute; give a path relative to the workspace root", rel)
	}

	joined := filepath.Join(root, rel)

	// Resolve the deepest ancestor that exists.
	//
	// A file being created does not exist yet, and neither may its parent —
	// workspace_write creates directories, so "docs/notes.md" in an empty
	// workspace has NO existing ancestor until the root. Checking only one level
	// up made every write into a new subdirectory fail with "does not exist",
	// which is both wrong and confusing, since the tool's whole job was to
	// create it.
	//
	// Walking up is also what keeps the containment check honest: whatever the
	// deepest real ancestor resolves to is where the new path will actually
	// live, symlinks included.
	probe := joined
	var resolved string
	for {
		r, statErr := filepath.EvalSymlinks(probe)
		if statErr == nil {
			resolved = r
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			// Walked past the filesystem root without finding anything real.
			return "", errs.New(op, errs.CodeNotFound).
				WithDetail("no part of path %q exists, not even its workspace root", rel)
		}
		probe = parent
	}
	// Compare with a separator so /work-other does not pass as inside /work.
	if resolved != root && !strings.HasPrefix(resolved, root+string(os.PathSeparator)) {
		return "", errs.New(op, errs.CodeForbidden).
			WithDetail("path %q resolves to %q, which is outside the workspace. "+
				"A symlink pointing out of the sandbox is still outside it.", rel, resolved)
	}
	return joined, nil
}

// ---------------------------------------------------------------------------
// workspace.list
// ---------------------------------------------------------------------------

// ListTool enumerates a directory inside the workspace.
type ListTool struct{}

// Contract implements Tool.
func (ListTool) Contract() Contract {
	return Contract{
		Name: "workspace_list",
		Description: "List the files and directories at a path inside the project workspace. " +
			"Use this to find out what exists before reading or writing. " +
			"Paths are relative to the workspace root; '.' is the root itself.",
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{
				"path":{"type":"string","description":"Directory relative to the workspace root. Use \".\" for the root."},
				"recursive":{"type":"boolean","description":"Descend into subdirectories. Off by default."}
			},
			"required":["path"],
			"additionalProperties":false}`),
		Capabilities:  []Capability{CapRead},
		RiskTier:      engine.RiskR0,
		Reversibility: ReversibleNone,
		Timeout:       15 * time.Second,
		Idempotent:    true,
		Available:     true,
	}
}

type listInput struct {
	Path      string `json:"path"`
	Recursive bool   `json:"recursive"`
}

type listEntry struct {
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size_bytes,omitempty"`
}

// Run implements Tool.
func (t ListTool) Run(ctx context.Context, inv Invocation) (*Result, error) {
	const op = "tools.ListTool.Run"

	var in listInput
	if err := json.Unmarshal(inv.Input, &in); err != nil {
		return nil, errs.Wrap(op, errs.CodeValidationFailed, err)
	}
	abs, err := resolveInWorkspace(inv.Workspace, strings.TrimPrefix(in.Path, "./"))
	if err != nil {
		return nil, err
	}

	root, _ := filepath.EvalSymlinks(inv.Workspace)
	var entries []listEntry
	const maxEntries = 2000

	walk := func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable entry is skipped, not fatal
		}
		if len(entries) >= maxEntries {
			return filepath.SkipAll
		}
		if p == abs {
			return nil
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return nil
		}
		e := listEntry{Path: rel, IsDir: d.IsDir()}
		if info, statErr := d.Info(); statErr == nil && !d.IsDir() {
			e.Size = info.Size()
		}
		entries = append(entries, e)
		if d.IsDir() && !in.Recursive && p != abs {
			return filepath.SkipDir
		}
		return nil
	}

	if err := filepath.WalkDir(abs, walk); err != nil {
		return nil, errs.Wrap(op, errs.CodeInternal, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })

	out, _ := json.Marshal(map[string]any{
		"path":      in.Path,
		"entries":   entries,
		"truncated": len(entries) >= maxEntries,
	})
	var raw strings.Builder
	for _, e := range entries {
		kind := "file"
		if e.IsDir {
			kind = "dir "
		}
		fmt.Fprintf(&raw, "%s %s\n", kind, e.Path)
	}
	return &Result{Output: out, Raw: raw.String()}, nil
}

// ---------------------------------------------------------------------------
// workspace.read
// ---------------------------------------------------------------------------

// ReadTool reads a file inside the workspace.
type ReadTool struct{}

// Contract implements Tool.
func (ReadTool) Contract() Contract {
	return Contract{
		Name: "workspace_read",
		Description: "Read a text file from the project workspace. Returns the file's contents. " +
			"Large files are truncated and the result says so — never assume you have seen the whole file " +
			"when 'truncated' is true.",
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{
				"path":{"type":"string","description":"File relative to the workspace root."}
			},
			"required":["path"],
			"additionalProperties":false}`),
		Capabilities:  []Capability{CapRead},
		RiskTier:      engine.RiskR0,
		Reversibility: ReversibleNone,
		Timeout:       15 * time.Second,
		Idempotent:    true,
		Available:     true,
	}
}

// Run implements Tool.
func (t ReadTool) Run(ctx context.Context, inv Invocation) (*Result, error) {
	const op = "tools.ReadTool.Run"

	var in struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(inv.Input, &in); err != nil {
		return nil, errs.Wrap(op, errs.CodeValidationFailed, err)
	}
	abs, err := resolveInWorkspace(inv.Workspace, in.Path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeNotFound, err).WithDetail("cannot read %q", in.Path)
	}
	if info.IsDir() {
		return nil, errs.New(op, errs.CodeValidationFailed).
			WithDetail("%q is a directory; use workspace_list", in.Path)
	}

	f, err := os.Open(abs)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeInternal, err)
	}
	defer f.Close()

	buf := make([]byte, maxReadBytes)
	n, _ := f.Read(buf)
	content := string(buf[:n])
	truncated := info.Size() > int64(n)

	out, _ := json.Marshal(map[string]any{
		"path":           in.Path,
		"content":        content,
		"size_bytes":     info.Size(),
		"truncated":      truncated,
		"bytes_returned": n,
	})
	return &Result{Output: out, Raw: content}, nil
}

// ---------------------------------------------------------------------------
// workspace.write
// ---------------------------------------------------------------------------

// WriteTool writes a file inside the workspace.
type WriteTool struct{}

// Contract implements Tool.
func (WriteTool) Contract() Contract {
	return Contract{
		Name: "workspace_write",
		Description: "Write a text file in the project workspace, creating parent directories as needed. " +
			"Overwrites any existing file at that path. Returns the byte count written and whether a " +
			"file was replaced.",
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{
				"path":{"type":"string","description":"File relative to the workspace root."},
				"content":{"type":"string","description":"Full file contents. This replaces the file; it does not append."}
			},
			"required":["path","content"],
			"additionalProperties":false}`),
		Capabilities: []Capability{CapWrite},
		RiskTier:     engine.RiskR1,
		// Automatic rather than manual: the workspace is a git worktree in normal
		// deployments, so a write is recoverable by FORGE itself.
		Reversibility: ReversibleAutomatic,
		Timeout:       20 * time.Second,
		// Not idempotent in the ledger sense: writing the same content twice is
		// harmless, but the ledger check also guards against a retry that lands
		// AFTER a human edited the file.
		Idempotent: false,
		Available:  true,
	}
}

// Run implements Tool.
func (t WriteTool) Run(ctx context.Context, inv Invocation) (*Result, error) {
	const op = "tools.WriteTool.Run"

	var in struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(inv.Input, &in); err != nil {
		return nil, errs.Wrap(op, errs.CodeValidationFailed, err)
	}
	if in.Path == "" {
		return nil, errs.New(op, errs.CodeValidationFailed).WithDetail("path is required")
	}
	abs, err := resolveInWorkspace(inv.Workspace, in.Path)
	if err != nil {
		return nil, err
	}
	// The parent may not exist yet; resolveInWorkspace has already proven the
	// deepest existing ancestor is inside the workspace.
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, errs.Wrap(op, errs.CodeInternal, err).
			WithDetail("cannot create the parent directory for %q", in.Path)
	}

	replaced := false
	var previousBytes int64
	if info, statErr := os.Stat(abs); statErr == nil {
		replaced = true
		previousBytes = info.Size()
	}
	if err := os.WriteFile(abs, []byte(in.Content), 0o644); err != nil {
		return nil, errs.Wrap(op, errs.CodeInternal, err).WithDetail("cannot write %q", in.Path)
	}

	out, _ := json.Marshal(map[string]any{
		"path":           in.Path,
		"bytes_written":  len(in.Content),
		"replaced":       replaced,
		"previous_bytes": previousBytes,
	})
	verb := "created"
	if replaced {
		verb = fmt.Sprintf("replaced (was %d bytes)", previousBytes)
	}
	return &Result{
		Output:   out,
		Raw:      fmt.Sprintf("%s %s: %d bytes", verb, in.Path, len(in.Content)),
		Evidence: fmt.Sprintf("%s exists in the workspace and is %d bytes", in.Path, len(in.Content)),
	}, nil
}

// ---------------------------------------------------------------------------
// shell.run
// ---------------------------------------------------------------------------

// ShellTool runs a command inside the workspace.
//
// This is the sharpest tool in the set, so its contract is the strictest: it
// runs with the workspace as its working directory, under a hard timeout, with a
// bounded output, and it is NOT idempotent — the ledger check runs before it,
// because "re-run the build" and "re-run the deploy script" are indistinguishable
// from here.
type ShellTool struct {
	// Allowed, when non-empty, restricts the first word of a command. A
	// deployment that grants execute at all should still narrow it to the
	// commands its work actually needs.
	Allowed []string
}

// Contract implements Tool.
func (t ShellTool) Contract() Contract {
	desc := "Run a shell command with the project workspace as the working directory. " +
		"Returns stdout, stderr and the exit code. Output is truncated if very large. " +
		"A non-zero exit code is returned as data, not as a failure — read it and decide."
	if len(t.Allowed) > 0 {
		desc += " Only these commands are permitted: " + strings.Join(t.Allowed, ", ") + "."
	}
	return Contract{
		Name:        "shell_run",
		Description: desc,
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{
				"command":{"type":"string","description":"The command line to run, executed via sh -c."},
				"reason":{"type":"string","description":"One short sentence on what this is meant to establish. Recorded in the audit trail."}
			},
			"required":["command","reason"],
			"additionalProperties":false}`),
		Capabilities:  []Capability{CapExecute},
		RiskTier:      engine.RiskR1,
		Reversibility: ReversibleManual,
		Timeout:       2 * time.Minute,
		Idempotent:    false,
		Available:     true,
	}
}

// Run implements Tool.
func (t ShellTool) Run(ctx context.Context, inv Invocation) (*Result, error) {
	const op = "tools.ShellTool.Run"

	var in struct {
		Command string `json:"command"`
		Reason  string `json:"reason"`
	}
	if err := json.Unmarshal(inv.Input, &in); err != nil {
		return nil, errs.Wrap(op, errs.CodeValidationFailed, err)
	}
	if strings.TrimSpace(in.Command) == "" {
		return nil, errs.New(op, errs.CodeValidationFailed).WithDetail("command is required")
	}
	if len(t.Allowed) > 0 {
		first := strings.Fields(in.Command)
		if len(first) == 0 || !slicesContains(t.Allowed, filepath.Base(first[0])) {
			return nil, errs.New(op, errs.CodeForbidden).
				WithDetail("command %q is not in this deployment's allow-list (%s)",
					in.Command, strings.Join(t.Allowed, ", "))
		}
	}
	root, err := resolveInWorkspace(inv.Workspace, "")
	if err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, "sh", "-c", in.Command)
	cmd.Dir = root
	// A deliberately narrow environment. Inheriting the parent's environment
	// would hand every credential the server holds — database URL, LLM key,
	// session secret — to a command the model composed (PRD SEC-03).
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + root,
		"LANG=C.UTF-8",
	}

	var stdout, stderr strings.Builder
	outLimit := &limitedWriter{w: &stdout, limit: 64 << 10}
	errLimit := &limitedWriter{w: &stderr, limit: 32 << 10}
	cmd.Stdout = outLimit
	cmd.Stderr = errLimit

	start := time.Now()
	runErr := cmd.Run()
	elapsed := time.Since(start)

	// Append the truncation notices after the command exits, so they do not
	// depend on the shape of the output. Silently clipped output is
	// indistinguishable from complete output, which is the worst way for a tool
	// result to be wrong.
	stdout.WriteString(outLimit.note())
	stderr.WriteString(errLimit.note())

	exitCode := 0
	timedOut := ctx.Err() != nil
	if runErr != nil {
		if ee, ok := runErr.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else if !timedOut {
			return nil, errs.Wrap(op, errs.CodeInternal, runErr).
				WithDetail("the command could not be started")
		}
	}
	if timedOut {
		return nil, errs.New(op, errs.CodeExternalUnavailable).
			WithDetail("the command exceeded its %s timeout and was killed. "+
				"Partial output: %s", t.Contract().Timeout, truncateStr(stdout.String(), 500))
	}

	out, _ := json.Marshal(map[string]any{
		"command":     in.Command,
		"exit_code":   exitCode,
		"stdout":      stdout.String(),
		"stderr":      stderr.String(),
		"duration_ms": elapsed.Milliseconds(),
	})
	raw := fmt.Sprintf("$ %s\n(exit %d in %s)\n--- stdout ---\n%s\n--- stderr ---\n%s",
		in.Command, exitCode, elapsed.Round(time.Millisecond), stdout.String(), stderr.String())

	evidence := ""
	if exitCode == 0 {
		evidence = fmt.Sprintf("`%s` exited 0", in.Command)
	}
	return &Result{Output: out, Raw: raw, Evidence: evidence}, nil
}

// limitedWriter caps how much a command can write into memory.
//
// # Why truncation is recorded rather than announced inline
//
// An earlier version appended "output truncated" from inside Write, on the first
// call that found the budget already spent. That silently depends on there being
// a LATER write: a command whose output arrives in one burst and then ends is
// truncated with no marker at all, and the caller cannot tell a complete result
// from a clipped one. It passed locally, where `yes | head -c` produces many
// small writes, and failed on CI, where the buffering differed.
//
// The flag is now set at the moment of truncation and the notice is appended
// once by the caller after the command exits, which does not depend on the
// shape of the output at all.
type limitedWriter struct {
	w         *strings.Builder
	limit     int
	written   int
	dropped   int
	truncated bool
}

// Write implements io.Writer.
//
// It always reports having consumed the WHOLE slice, even when it keeps only
// part of it. That is not sloppiness — io.Writer's contract says a write
// returning n < len(p) must return an error, and os/exec's copier turns that
// into io.ErrShortWrite and KILLS the command. A limiter that reports a short
// write therefore does not truncate output, it truncates the process.
//
// An earlier version reassigned `p = p[:remaining]` and then returned `len(p)`,
// which reported the clipped length. Any command whose output arrived in a
// single chunk larger than the remaining budget was killed partway through, and
// because it died before writing more, the truncation notice was never reached
// either — so the result looked like a short, complete output. That is almost
// certainly what CI was seeing, and it was invisible on a machine where pipe
// chunks happened to be smaller than the budget.
func (l *limitedWriter) Write(p []byte) (int, error) {
	// Captured before any reslicing: this is what the caller must be told was
	// consumed.
	consumed := len(p)

	remaining := l.limit - l.written
	if remaining <= 0 {
		l.truncated = true
		l.dropped += consumed
		return consumed, nil // absorb silently rather than kill the command
	}
	if consumed > remaining {
		l.truncated = true
		l.dropped += consumed - remaining
		p = p[:remaining]
	}
	n, err := l.w.Write(p)
	l.written += n
	if err != nil {
		return n, err
	}
	return consumed, nil
}

// note returns the truncation notice, or "" when nothing was dropped.
func (l *limitedWriter) note() string {
	if !l.truncated {
		return ""
	}
	return fmt.Sprintf("\n… output truncated: kept %d bytes, dropped at least %d more\n",
		l.written, l.dropped)
}

func slicesContains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
