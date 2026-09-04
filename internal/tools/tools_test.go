package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

func workspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// EvalSymlinks up front: on macOS t.TempDir() is under /var, which is itself
	// a symlink to /private/var. Comparing an unresolved root against a resolved
	// path would make every containment check fail for the wrong reason.
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resolved, "README.md"), []byte("# project\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(resolved, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resolved, "src", "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return resolved
}

func inv(ws, tool string, input any) Invocation {
	raw, _ := json.Marshal(input)
	return Invocation{Tool: tool, Input: raw, Workspace: ws, TaskID: "tsk_test", GoalID: "gol_test"}
}

// TestSandboxContainsEveryEscapeAttempt is the security fence of this package.
//
// A filesystem tool driven by model output is the most direct path from "the
// agent misunderstood something" to "the agent read /etc/shadow". Cleaning ".."
// out of the path is the obvious half; the half people leave out is that a
// SYMLINK inside the workspace pointing outward produces a clean-looking path
// that resolves somewhere else entirely.
func TestSandboxContainsEveryEscapeAttempt(t *testing.T) {
	ws := workspace(t)
	outside := filepath.Join(filepath.Dir(ws), "outside-secret.txt")
	if err := os.WriteFile(outside, []byte("SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A symlink INSIDE the workspace pointing outside it. This is the case a
	// path-cleaning check passes and a resolving check catches.
	if err := os.Symlink(outside, filepath.Join(ws, "innocent.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink("/etc", filepath.Join(ws, "etc-link")); err != nil {
		t.Fatal(err)
	}

	attempts := map[string]string{
		"parent traversal":       "../outside-secret.txt",
		"deep traversal":         "../../../../etc/passwd",
		"absolute path":          "/etc/passwd",
		"traversal via subdir":   "src/../../outside-secret.txt",
		"symlink to a file":      "innocent.txt",
		"symlink to a directory": "etc-link/passwd",
		"dot-slash traversal":    "./../outside-secret.txt",
	}

	for name, path := range attempts {
		t.Run(name, func(t *testing.T) {
			res, err := ReadTool{}.Run(context.Background(), inv(ws, "workspace_read", map[string]string{"path": path}))
			if err == nil {
				t.Fatalf("read escaped the workspace via %q and returned: %s", path, res.Raw)
			}
			if strings.Contains(res.String(), "SECRET") {
				t.Fatal("the secret leaked")
			}
			code := errs.CodeOf(err)
			if code != errs.CodeForbidden && code != errs.CodeNotFound {
				t.Errorf("code = %v; expected FORBIDDEN or NOT_FOUND", code)
			}
		})
	}

	// And writes, which are worse: an escaped write corrupts something outside.
	for name, path := range attempts {
		t.Run("write "+name, func(t *testing.T) {
			_, err := WriteTool{}.Run(context.Background(),
				inv(ws, "workspace_write", map[string]string{"path": path, "content": "pwned"}))
			if err == nil {
				t.Fatalf("write escaped the workspace via %q", path)
			}
		})
	}
	// The secret is untouched.
	if b, _ := os.ReadFile(outside); string(b) != "SECRET" {
		t.Fatalf("the file outside the workspace was modified: %q", string(b))
	}
}

// TestSiblingDirectoryIsNotInsideTheWorkspace — comparing with a bare string
// prefix makes /work-other pass as inside /work. The separator is what stops it.
func TestSiblingDirectoryIsNotInsideTheWorkspace(t *testing.T) {
	ws := workspace(t)
	sibling := ws + "-other"
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sibling)
	if err := os.WriteFile(filepath.Join(sibling, "f.txt"), []byte("SIBLING"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sibling, filepath.Join(ws, "sib")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, err := (ReadTool{}).Run(context.Background(),
		inv(ws, "workspace_read", map[string]string{"path": "sib/f.txt"})); err == nil {
		t.Fatal("a sibling directory whose path shares the workspace's prefix was treated as inside it")
	}
}

func TestReadAndWriteRoundTrip(t *testing.T) {
	ws := workspace(t)
	ctx := context.Background()

	res, err := WriteTool{}.Run(ctx, inv(ws, "workspace_write",
		map[string]string{"path": "docs/notes.md", "content": "hello\n"}))
	if err != nil {
		t.Fatal(err)
	}
	var w struct {
		BytesWritten int  `json:"bytes_written"`
		Replaced     bool `json:"replaced"`
	}
	if err := json.Unmarshal(res.Output, &w); err != nil {
		t.Fatal(err)
	}
	if w.BytesWritten != 6 || w.Replaced {
		t.Errorf("write reported %+v", w)
	}
	// Parent directories are created.
	if _, err := os.Stat(filepath.Join(ws, "docs", "notes.md")); err != nil {
		t.Fatalf("the file was not created: %v", err)
	}
	// The result carries evidence, so a verification claim can point at
	// something rather than assert itself.
	if res.Evidence == "" {
		t.Error("a write produced no evidence string")
	}

	read, err := ReadTool{}.Run(ctx, inv(ws, "workspace_read", map[string]string{"path": "docs/notes.md"}))
	if err != nil {
		t.Fatal(err)
	}
	if read.Raw != "hello\n" {
		t.Errorf("read back %q", read.Raw)
	}

	// Overwriting reports that it replaced something, and how big it was —
	// otherwise a destructive write looks identical to a create in the log.
	res, err = WriteTool{}.Run(ctx, inv(ws, "workspace_write",
		map[string]string{"path": "docs/notes.md", "content": "replaced"}))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(res.Output, &w); err != nil {
		t.Fatal(err)
	}
	if !w.Replaced {
		t.Error("overwriting an existing file did not report replaced=true")
	}
}

func TestListFindsFilesAndRespectsRecursion(t *testing.T) {
	ws := workspace(t)
	ctx := context.Background()

	shallow, err := ListTool{}.Run(ctx, inv(ws, "workspace_list", map[string]any{"path": "."}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(shallow.Raw, "src/main.go") {
		t.Error("a non-recursive list descended into a subdirectory")
	}
	if !strings.Contains(shallow.Raw, "README.md") {
		t.Errorf("the list is missing a top-level file:\n%s", shallow.Raw)
	}

	deep, err := ListTool{}.Run(ctx, inv(ws, "workspace_list", map[string]any{"path": ".", "recursive": true}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(deep.Raw, "src/main.go") {
		t.Errorf("a recursive list missed a nested file:\n%s", deep.Raw)
	}
}

func TestReadReportsTruncationRatherThanHidingIt(t *testing.T) {
	ws := workspace(t)
	big := strings.Repeat("x", maxReadBytes+5000)
	if err := os.WriteFile(filepath.Join(ws, "big.txt"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := ReadTool{}.Run(context.Background(), inv(ws, "workspace_read", map[string]string{"path": "big.txt"}))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Truncated bool  `json:"truncated"`
		SizeBytes int64 `json:"size_bytes"`
	}
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatal(err)
	}
	if !out.Truncated {
		t.Error("a truncated read did not say so; the model would treat a partial file as the whole file")
	}
	if out.SizeBytes != int64(len(big)) {
		t.Error("the result does not report the file's real size, so the model cannot tell how much it missed")
	}
}

// ---------------------------------------------------------------------------
// shell
// ---------------------------------------------------------------------------

func TestShellRunsInTheWorkspace(t *testing.T) {
	ws := workspace(t)
	res, err := ShellTool{Allowed: []string{"ls"}}.Run(context.Background(), inv(ws, "shell_run",
		map[string]string{"command": "ls README.md && pwd", "reason": "confirm the working directory"}))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		ExitCode int    `json:"exit_code"`
		Stdout   string `json:"stdout"`
	}
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatal(err)
	}
	if out.ExitCode != 0 {
		t.Errorf("exit %d: %s", out.ExitCode, res.Raw)
	}
	if !strings.Contains(out.Stdout, ws) {
		t.Errorf("the command did not run in the workspace: %q", out.Stdout)
	}
}

// TestShellDoesNotInheritTheServersSecrets is the containment that matters most
// for this tool. The parent process holds the database URL, the LLM key, and the
// session secret; a command composed by a model must not be able to read them
// with `env` (PRD SEC-03).
func TestShellDoesNotInheritTheServersSecrets(t *testing.T) {
	ws := workspace(t)
	t.Setenv("FORGE_SESSION_SECRET", "SUPER-SECRET-VALUE-DO-NOT-LEAK")
	t.Setenv("FORGE_LLM_API_KEY", "sk-SHOULD-NOT-APPEAR")
	t.Setenv("FORGE_DATABASE_URL", "postgres://user:PGPASSWORD@host/db")

	res, err := ShellTool{Allowed: []string{"env"}}.Run(context.Background(), inv(ws, "shell_run",
		map[string]string{"command": "env", "reason": "attempt to read the parent environment"}))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"SUPER-SECRET-VALUE", "sk-SHOULD-NOT-APPEAR", "PGPASSWORD"} {
		if strings.Contains(res.Raw, secret) {
			t.Errorf("the shell tool inherited %q from the server's environment:\n%s", secret, res.Raw)
		}
	}
}

func TestShellReturnsNonZeroExitAsDataNotFailure(t *testing.T) {
	// A failing command is information the model must read and act on. Raising
	// it as a tool error would make "the test suite failed" indistinguishable
	// from "the tool broke".
	ws := workspace(t)
	res, err := ShellTool{Allowed: []string{"exit"}}.Run(context.Background(), inv(ws, "shell_run",
		map[string]string{"command": "exit 3", "reason": "check a non-zero exit"}))
	if err != nil {
		t.Fatalf("a non-zero exit was raised as a tool failure: %v", err)
	}
	var out struct {
		ExitCode int `json:"exit_code"`
	}
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatal(err)
	}
	if out.ExitCode != 3 {
		t.Errorf("exit_code = %d, want 3", out.ExitCode)
	}
	if res.Evidence != "" {
		t.Error("a failed command must not produce an evidence string; it proves nothing")
	}
}

func TestShellAllowListIsEnforced(t *testing.T) {
	ws := workspace(t)
	tool := ShellTool{Allowed: []string{"ls", "go"}}

	if _, err := tool.Run(context.Background(), inv(ws, "shell_run",
		map[string]string{"command": "ls", "reason": "allowed"})); err != nil {
		t.Fatalf("an allowed command was refused: %v", err)
	}
	_, err := tool.Run(context.Background(), inv(ws, "shell_run",
		map[string]string{"command": "curl http://evil.test", "reason": "not allowed"}))
	if err == nil {
		t.Fatal("a command outside the allow-list ran")
	}
	if errs.CodeOf(err) != errs.CodeForbidden {
		t.Errorf("code = %v, want FORBIDDEN", errs.CodeOf(err))
	}
	if !strings.Contains(err.Error(), "ls, go") {
		t.Error("the refusal should say what IS allowed, or the model cannot choose a legal alternative")
	}
}

// TestLimitedWriterAlwaysReportsTruncation tests the invariant directly, at the
// writer, rather than through a shell whose buffering this test cannot control.
//
// # Why not through shell_run
//
// CI failed with "truncated output did not say so" on a command that produces
// half a megabyte. It could not be reproduced locally through the same command,
// because os/exec delivers pipe output in ~32KB chunks here, so there is always
// a later Write and the old write-time notice always fired. Chasing the
// difference through two layers of buffering would be guessing at an
// environment; the invariant does not need either layer to be stated.
//
// The invariant: if ANY byte was dropped, the result says so. Whatever the write
// pattern — one enormous write, many small ones, exactly-at-the-boundary — a
// clipped result must never be indistinguishable from a complete one.
func TestLimitedWriterAlwaysReportsTruncation(t *testing.T) {
	const limit = 1024

	cases := map[string][]int{
		"single write far over":  {limit * 10},
		"single write just over": {limit + 1},
		"exactly at the limit":   {limit},
		"two writes crossing":    {limit - 10, 100},
		"many small writes":      {200, 200, 200, 200, 200, 200},
		"one over then silence":  {limit * 4},
		"under the limit":        {limit / 2},
		"empty":                  {},
	}

	for name, sizes := range cases {
		t.Run(name, func(t *testing.T) {
			var buf strings.Builder
			w := &limitedWriter{w: &buf, limit: limit}

			total := 0
			for _, n := range sizes {
				payload := make([]byte, n)
				for i := range payload {
					payload[i] = 'x'
				}
				written, err := w.Write(payload)
				if err != nil {
					t.Fatalf("Write returned an error: %v", err)
				}
				// The writer must always claim to have consumed everything, or
				// os/exec kills the command with EPIPE partway through.
				if written != n {
					t.Errorf("Write reported %d of %d bytes; the command would be killed with EPIPE", written, n)
				}
				total += n
			}
			out := w.text()

			if buf.Len() > limit {
				t.Errorf("kept %d bytes with a limit of %d", buf.Len(), limit)
			}

			clipped := total > limit
			said := strings.Contains(out, "truncated")

			if clipped && !said {
				t.Errorf("wrote %d bytes into a %d-byte budget and the result says nothing about it. "+
					"A silently clipped result is indistinguishable from a complete one, which is the "+
					"worst way for a tool result to be wrong.", total, limit)
			}
			if !clipped && said {
				t.Errorf("wrote %d bytes into a %d-byte budget and the result claims truncation. "+
					"A notice that always appears teaches the reader to ignore it.", total, limit)
			}
		})
	}
}

// TestShellOutputIsBounded is the integration half: whatever the environment's
// buffering, a large command output must not reach the model whole.
func TestShellOutputIsBounded(t *testing.T) {
	ws := workspace(t)
	res, err := (ShellTool{Allowed: []string{"yes"}}).Run(context.Background(), inv(ws, "shell_run",
		map[string]string{"command": "yes hello | head -c 500000", "reason": "produce a lot of output"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Raw) > 200<<10 {
		t.Errorf("output was %d bytes; unbounded command output lands in a model request and "+
			"spends the goal's whole token budget", len(res.Raw))
	}
	// Deliberately NOT asserting the truncation notice here. Whether this
	// specific command overruns the budget depends on the platform's coreutils
	// and pipe buffering — CI and this machine disagreed about it, and an
	// assertion that depends on that is a flaky test rather than a check.
	// TestLimitedWriterAlwaysReportsTruncation covers the invariant exactly.
	t.Logf("captured %d bytes", len(res.Raw))
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// ---------------------------------------------------------------------------
// contracts and the registry
// ---------------------------------------------------------------------------

func TestContractValidationCatchesUngovernableTools(t *testing.T) {
	base := ReadTool{}.Contract()

	cases := map[string]func(*Contract){
		"no name":         func(c *Contract) { c.Name = "" },
		"no description":  func(c *Contract) { c.Description = "" },
		"no schema":       func(c *Contract) { c.InputSchema = nil },
		"no capabilities": func(c *Contract) { c.Capabilities = nil },
		"unknown capability": func(c *Contract) {
			c.Capabilities = []Capability{"telepathy"}
		},
		"no timeout": func(c *Contract) { c.Timeout = 0 },
		"unavailable without a reason": func(c *Contract) {
			c.Available = false
			c.UnavailableReason = ""
		},
		// The copy-paste that matters: a deploy tool declared at a tier below the
		// approval threshold would run with no human gate.
		"deploy below R2": func(c *Contract) {
			c.Capabilities = []Capability{CapDeploy}
			c.RiskTier = engine.RiskR1
		},
		"transact below R2": func(c *Contract) {
			c.Capabilities = []Capability{CapTransact}
			c.RiskTier = engine.RiskR0
		},
		"irreversible below R2": func(c *Contract) {
			c.Reversibility = Irreversible
			c.RiskTier = engine.RiskR1
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := base
			mutate(&c)
			if err := c.Validate(); err == nil {
				t.Errorf("an ungovernable contract was accepted (%s)", name)
			}
		})
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("a valid contract was refused: %v", err)
	}
}

func TestGrantRequiresEveryCapability(t *testing.T) {
	// A tool that reads AND deploys is a deploy tool. Granting only 'read' must
	// not permit it.
	c := Contract{
		Name: "x", Description: "d", InputSchema: json.RawMessage(`{}`),
		Capabilities: []Capability{CapRead, CapDeploy},
		RiskTier:     engine.RiskR2, Timeout: 1, Available: true,
	}
	g := Grant{
		Capabilities: []Capability{CapRead},
		MaxRiskTier:  engine.RiskR3,
		Autonomy:     engine.AutonomyApprovalGated,
	}
	ok, why := g.Permits(c)
	if ok {
		t.Fatal("a grant of 'read' permitted a tool that also deploys")
	}
	if !strings.Contains(why, "deploy") {
		t.Errorf("the refusal should name the missing capability: %q", why)
	}
}

func TestGrantRefusalsExplainThemselves(t *testing.T) {
	// The reason is shown to the model. "That tool is above this goal's ceiling"
	// lets it pick another approach; a tool silently missing from the list leads
	// it to invent one.
	c := Contract{
		Name: "risky", Description: "d", InputSchema: json.RawMessage(`{}`),
		Capabilities: []Capability{CapWrite}, RiskTier: engine.RiskR3,
		Timeout: 1, Available: true,
	}
	for name, g := range map[string]Grant{
		"below ceiling": {Capabilities: []Capability{CapWrite}, MaxRiskTier: engine.RiskR1, Autonomy: engine.AutonomySandboxExecute},
		"no execution":  {Capabilities: []Capability{CapWrite}, MaxRiskTier: engine.RiskR4, Autonomy: engine.AutonomyDraft},
		"prohibited":    {Capabilities: []Capability{CapWrite}, MaxRiskTier: engine.RiskR4, Autonomy: engine.AutonomyProhibited},
	} {
		ok, why := g.Permits(c)
		if ok {
			t.Errorf("%s: permitted", name)
		}
		if why == "" {
			t.Errorf("%s: refused with no reason", name)
		}
	}
}

func TestR5IsRefusedEvenAtTheHighestGrant(t *testing.T) {
	c := Contract{
		Name: "prohibited-thing", Description: "d", InputSchema: json.RawMessage(`{}`),
		Capabilities: []Capability{CapControl}, RiskTier: engine.RiskR5,
		Timeout: 1, Available: true,
	}
	g := Grant{
		Capabilities: AllCapabilities(),
		MaxRiskTier:  engine.RiskR5,
		Autonomy:     engine.AutonomyApprovalGated,
	}
	ok, why := g.Permits(c)
	if ok {
		t.Fatal("an R5 tool was permitted; no grant authorises a prohibited action")
	}
	if !strings.Contains(strings.ToLower(why), "prohibited") {
		t.Errorf("the refusal should say it is prohibited rather than merely gated: %q", why)
	}
}

// TestUnavailableConnectorsAreOfferedAndFailLoudly is the anti-fabrication
// fence, and the reasoning is worth keeping next to it.
//
// Omitting a CAD or FEA connector entirely leaves the model with no tool that
// fits the task — and the most likely thing it then does is produce a plausible
// number from its own weights, presented exactly like a computed one. Declaring
// the connector and failing explicitly makes the gap visible instead.
func TestUnavailableConnectorsAreOfferedAndFailLoudly(t *testing.T) {
	r := NewRegistry()
	for _, tool := range StandardUnavailableConnectors() {
		if err := r.Register(tool); err != nil {
			t.Fatalf("registering %s: %v", tool.Contract().Name, err)
		}
	}
	g := Grant{
		Capabilities: AllCapabilities(),
		MaxRiskTier:  engine.RiskR4,
		Autonomy:     engine.AutonomyApprovalGated,
	}

	defs := r.Definitions(g)
	if len(defs) == 0 {
		t.Fatal("no unavailable connector was offered to the model; the model would have no tool " +
			"that fits the task and would answer from its own weights instead")
	}
	for _, d := range defs {
		if !strings.Contains(d.Function.Description, "UNAVAILABLE") {
			t.Errorf("tool %s does not tell the model it is unavailable", d.Function.Name)
		}
		if !strings.Contains(d.Function.Description, "Do not guess") {
			t.Errorf("tool %s does not tell the model NOT to invent the result", d.Function.Name)
		}
	}

	tool, err := r.Get("fea_solve")
	if err != nil {
		t.Fatal(err)
	}
	_, runErr := tool.Run(context.Background(), Invocation{Tool: "fea_solve"})
	if runErr == nil {
		t.Fatal("an unavailable connector returned a result; a value produced without a solver is not an analysis")
	}
	if errs.CodeOf(runErr) != errs.CodeConnectorUnavailable {
		t.Errorf("code = %v, want CONNECTOR_UNAVAILABLE", errs.CodeOf(runErr))
	}
	if !strings.Contains(runErr.Error(), "Do not estimate") {
		t.Errorf("the failure should forbid estimating in its place: %v", runErr)
	}
}

func TestRegistryRefusesDuplicateNames(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(ReadTool{}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(ReadTool{}); err == nil {
		t.Fatal("two tools were registered under one name; they would deduplicate each other's " +
			"calls in the idempotency ledger")
	}
}

func TestContractsAreSortedForCacheStability(t *testing.T) {
	// The tool list is part of a model request's prefix. An unstable order
	// invalidates the prompt cache on every single call for no benefit.
	r := NewRegistry()
	r.MustRegister(WriteTool{})
	r.MustRegister(ReadTool{})
	r.MustRegister(ListTool{})

	var last string
	for _, c := range r.Contracts() {
		if last != "" && c.Name < last {
			t.Fatalf("contracts are not sorted: %q came after %q", c.Name, last)
		}
		last = c.Name
	}
}

// String is a helper so a nil *Result can be logged in a failure message without
// a nil dereference inside the test itself.
func (r *Result) String() string {
	if r == nil {
		return "<no result>"
	}
	return r.Raw
}

// Replacing a file is not the same call as creating one (PRD SAF-01).
//
// # Why this is the fence for Result.RiskTierUsed
//
// The field has carried the promise "a tool may raise its tier per call" since
// the type was written, and until now nothing set it and nothing read it — a
// hook with neither end connected. This is the producing end.
//
// The distinction is real rather than decorative: creating a file adds
// something, and replacing one destroys the content that was there. The
// contract's R1 describes the first case, and declaring R2 on the contract to
// cover the second would gate every harmless write in the product.
func TestWritingOverAFileRaisesTheCallsTier(t *testing.T) {
	ws := t.TempDir()

	created, err := WriteTool{}.Run(context.Background(),
		inv(ws, "workspace_write", map[string]string{"path": "notes.md", "content": "first"}))
	if err != nil {
		t.Fatal(err)
	}
	if created.RiskTierUsed != "" {
		t.Errorf("creating a file raised the tier to %q; only replacement is destructive",
			created.RiskTierUsed)
	}

	replaced, err := WriteTool{}.Run(context.Background(),
		inv(ws, "workspace_write", map[string]string{"path": "notes.md", "content": "second"}))
	if err != nil {
		t.Fatal(err)
	}
	if replaced.RiskTierUsed != engine.RiskR2 {
		t.Errorf("replacing a file reported tier %q, want r2.\nThe content that was there is "+
			"gone, and the ledger will record this call at the tool's declared r1 as though "+
			"it had merely added a draft.", replaced.RiskTierUsed)
	}
	// The declared tier is unchanged: this is a property of the call, not a
	// redefinition of the tool.
	if (WriteTool{}).Contract().RiskTier != engine.RiskR1 {
		t.Error("the contract's declared tier moved; the raise must be per call")
	}
}

// An unconfigured shell refuses everything (PRD SEC-05).
//
// # Why the empty list denies instead of permitting
//
// It used to permit, and nothing ever set it: ShellTool.Allowed was a field only
// tests passed a value to, so every deployment ran a shell that would execute
// anything the host could run, while the code read as though a control existed.
//
// Wiring it up was necessary and not sufficient. Configuration gets forgotten;
// what matters is the direction of that failure. Empty-means-permit turns a
// forgotten line into an unrestricted shell nobody notices. Empty-means-refuse
// turns the same omission into a tool that refuses its first call and names the
// variable to set. One of those is found by an operator in a minute, the other
// by whoever goes looking for it.
func TestAnUnconfiguredShellRefusesEverything(t *testing.T) {
	ws := workspace(t)

	_, err := ShellTool{}.Run(context.Background(), inv(ws, "shell_run",
		map[string]string{"command": "ls", "reason": "anything at all"}))
	if err == nil {
		t.Fatal("a shell with no configured allow-list ran a command.\n" +
			"An empty list is the absence of a control, not a permissive one, and FORGE " +
			"confines no network egress — so this list is what stands between a " +
			"model-composed command and everything the host can reach")
	}
	if errs.CodeOf(err) != errs.CodeForbidden {
		t.Errorf("refused with %s, expected %s", errs.CodeOf(err), errs.CodeForbidden)
	}
	if !strings.Contains(err.Error(), "FORGE_SHELL_ALLOWED_COMMANDS") {
		t.Errorf("the refusal does not name the variable that fixes it: %v", err)
	}

	// The contract says so too, so the model does not spend an iteration
	// discovering it.
	if !strings.Contains(ShellTool{}.Contract().Description, "refused") {
		t.Error("the contract a model reads does not say the shell is unconfigured")
	}
}
