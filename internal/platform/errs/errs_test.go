package errs

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEveryCodeHasDefinition is the fence that keeps the registry complete.
//
// It works by parsing this package's own source for `Code = "..."` constants
// rather than by listing them in a slice. A fence that enumerates what it
// checks is vacuous: deleting a constant would just make the loop shorter.
// Parsing the source means a new code cannot hide from it.
func TestEveryCodeHasDefinition(t *testing.T) {
	declared := declaredCodes(t)
	if len(declared) < 20 {
		t.Fatalf("only found %d declared codes by parsing source; the parser is probably broken, which would make this fence vacuous", len(declared))
	}
	for name, value := range declared {
		if _, ok := Lookup(Code(value)); !ok {
			t.Errorf("code %s (%q) is declared but has no registry entry in registry{}", name, value)
		}
	}
}

// TestEveryDefinitionIsActionable enforces the contract that a failure must
// tell the reader what to do next. A dead-end error is how a long-running agent
// strands a human at 3am.
func TestEveryDefinitionIsActionable(t *testing.T) {
	for _, d := range All() {
		if strings.TrimSpace(d.Cause) == "" {
			t.Errorf("%s: Cause is empty", d.Code)
		}
		if strings.TrimSpace(d.Remedy) == "" {
			t.Errorf("%s: Remedy is empty; every failure must name the next action", d.Code)
		}
		if d.HTTPStatus < 100 || d.HTTPStatus > 599 {
			t.Errorf("%s: HTTPStatus %d is not a valid status", d.Code, d.HTTPStatus)
		}
		if d.Category == "" {
			t.Errorf("%s: Category is empty", d.Code)
		}
		// A remedy that just restates the cause is not a remedy.
		if strings.EqualFold(strings.TrimSpace(d.Cause), strings.TrimSpace(d.Remedy)) {
			t.Errorf("%s: Remedy merely restates Cause", d.Code)
		}
	}
}

// TestCodeNamingConvention enforces UPPER_SNAKE_CASE, because dashboards and
// client branches bind to these strings.
func TestCodeNamingConvention(t *testing.T) {
	for _, d := range All() {
		s := string(d.Code)
		if s == "" {
			t.Fatal("empty code in registry")
		}
		for i, r := range s {
			ok := (r >= 'A' && r <= 'Z') || r == '_' || (r >= '0' && r <= '9')
			if !ok {
				t.Errorf("code %q: character %q at %d is not UPPER_SNAKE_CASE", s, r, i)
			}
		}
		if strings.HasPrefix(s, "_") || strings.HasSuffix(s, "_") {
			t.Errorf("code %q must not start or end with an underscore", s)
		}
	}
}

// TestRegistryKeyMatchesDefinitionCode catches a copy-paste error where a row
// is filed under one key but carries another code — which would make Lookup and
// the definition disagree.
func TestRegistryKeyMatchesDefinitionCode(t *testing.T) {
	for key, d := range registry {
		if key != d.Code {
			t.Errorf("registry key %q holds a definition for %q", key, d.Code)
		}
	}
}

// TestRetryabilityIsDeliberate documents and fences the retry classification.
// The durable queue branches on Retryable, so a miscategorised code silently
// changes engine behaviour: a retryable validation error would be retried
// forever, and a non-retryable transient fault would be dropped permanently.
func TestRetryabilityIsDeliberate(t *testing.T) {
	mustNotRetry := []Code{
		CodeInvalidCredentials, CodeValidationFailed, CodeNotFound,
		CodeEmailAlreadyRegistered, CodeTokenExpired, CodeTokenInvalid,
		CodeTokenAlreadyUsed, CodeForbidden, CodePasswordTooWeak,
		CodeConfigInvalid, CodeMigrationFailed, CodeStateCorrupt,
		CodeInvariantViolated,
	}
	for _, c := range mustNotRetry {
		d, ok := Lookup(c)
		if !ok {
			t.Fatalf("%s missing from registry", c)
		}
		if d.Retryable {
			t.Errorf("%s is marked retryable; retrying it can never succeed and would spin the queue", c)
		}
	}
	mustRetry := []Code{CodeDatabaseUnavail, CodeInternal, CodeRateLimited, CodeMailDeliveryFail}
	for _, c := range mustRetry {
		d, ok := Lookup(c)
		if !ok {
			t.Fatalf("%s missing from registry", c)
		}
		if !d.Retryable {
			t.Errorf("%s is marked non-retryable; a transient fault would be dropped permanently", c)
		}
	}
}

// TestNoErrorCodeLiteralsOutsideRegistry is the fence behind the package's
// central claim: codes exist in exactly one place. It scans sibling packages
// for string literals that look like a code being passed where a Code is
// expected.
func TestNoErrorCodeLiteralsOutsideRegistry(t *testing.T) {
	root := findRepoRoot(t)
	fset := token.NewFileSet()
	violations := 0
	scanned := 0

	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			base := filepath.Base(p)
			if base == ".git" || base == "web" || base == "docs" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		// The registry itself is the one legal home for these literals.
		if strings.HasSuffix(p, filepath.Join("platform", "errs", "code.go")) {
			return nil
		}
		f, perr := parser.ParseFile(fset, p, nil, 0)
		if perr != nil {
			return nil // not our business to fail on unparseable files here
		}
		scanned++
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			// errs.New(op, X) and errs.Wrap(op, X, err)
			var codeArgIdx int
			switch sel.Sel.Name {
			case "New":
				codeArgIdx = 1
			case "Wrap":
				codeArgIdx = 1
			default:
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "errs" {
				return true
			}
			if len(call.Args) <= codeArgIdx {
				return true
			}
			if lit, ok := call.Args[codeArgIdx].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				pos := fset.Position(call.Pos())
				t.Errorf("%s:%d: error code passed as a string literal %s; use a Code constant from the registry",
					pos.Filename, pos.Line, lit.Value)
				violations++
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk failed: %v", err)
	}
	if scanned == 0 {
		t.Fatal("scanned zero Go files; this fence would pass vacuously")
	}
	t.Logf("scanned %d Go files, %d violations", scanned, violations)
}

func TestErrorWrapping(t *testing.T) {
	base := errors.New("connection refused")
	e := Wrap("db.Connect", CodeDatabaseUnavail, base).WithDetail("host=%s", "localhost")

	if !errors.Is(e, base) {
		t.Error("errors.Is should find the wrapped cause")
	}
	if CodeOf(e) != CodeDatabaseUnavail {
		t.Errorf("CodeOf = %v, want %v", CodeOf(e), CodeDatabaseUnavail)
	}
	if !IsRetryable(e) {
		t.Error("DATABASE_UNAVAILABLE must be retryable")
	}
	if !strings.Contains(e.Error(), "host=localhost") {
		t.Errorf("detail missing from message: %s", e.Error())
	}
	if !strings.Contains(e.Error(), "db.Connect") {
		t.Errorf("op missing from message: %s", e.Error())
	}
}

func TestWrapNilReturnsNil(t *testing.T) {
	// Wrap must be safe to use directly in a return without a nil check, or
	// callers will write `if err != nil { return Wrap(...) }` inconsistently and
	// eventually return a non-nil error for a successful operation.
	if got := Wrap("op", CodeInternal, nil); got != nil {
		t.Errorf("Wrap(nil) = %v, want nil", got)
	}
}

func TestUnknownErrorsAreRetryable(t *testing.T) {
	// An unclassified transient fault dropped permanently is worse than one
	// wasted retry, so unknown errors default to retryable.
	if !IsRetryable(errors.New("who knows")) {
		t.Error("an unclassified error should default to retryable")
	}
	if CodeOf(errors.New("who knows")) != CodeInternal {
		t.Error("an unclassified error should report CodeInternal")
	}
}

func TestWithFieldDoesNotMutateOriginal(t *testing.T) {
	a := New("op", CodeInternal).WithField("k", 1)
	b := a.WithField("k2", 2)
	if _, exists := a.Fields["k2"]; exists {
		t.Error("WithField mutated the receiver; copies must be independent")
	}
	if b.Fields["k"] != 1 {
		t.Error("WithField lost inherited fields")
	}
}

// --- helpers ---------------------------------------------------------------

func declaredCodes(t *testing.T) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "code.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing code.go: %v", err)
	}
	out := map[string]string{}
	ast.Inspect(f, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok || len(vs.Names) == 0 || len(vs.Values) == 0 {
			return true
		}
		ident, ok := vs.Type.(*ast.Ident)
		if !ok || ident.Name != "Code" {
			return true
		}
		for i, name := range vs.Names {
			if i >= len(vs.Values) {
				break
			}
			lit, ok := vs.Values[i].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			out[name.Name] = strings.Trim(lit.Value, `"`)
		}
		return true
	})
	return out
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("could not locate repo root (no go.mod found walking up)")
	return ""
}

var _ = fmt.Sprint
