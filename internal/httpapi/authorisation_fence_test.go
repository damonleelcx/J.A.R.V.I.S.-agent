package httpapi

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strconv"
	"strings"
	"testing"
)

// TestNoHandlerAuthorisesByOwnerID keeps the wave's central decision from being
// quietly undone.
//
// # What it guards
//
// Authorisation reads forge_project_members. `forge_projects.owner_id` records
// who CREATED a project and is not an access path. Two authorisation paths means
// two answers to "may this person read this", and the day they disagree is the
// day somebody sees something they should not.
//
// The failure this prevents is ordinary and likely: somebody adds an endpoint,
// copies the shape of a neighbouring query from before this wave, and ships a
// handler that authorises against a column nothing else consults any more.
//
// # Why it parses rather than greps
//
// The first version scanned lines and flagged the comments explaining the rule,
// which is the kind of false positive that gets a fence deleted. A SQL clause
// lives in a string literal, so that is what is examined — the AST, not the text.
func TestNoHandlerAuthorisesByOwnerID(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}

	var scanned int
	var offenders []string
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			scanned++
			ast.Inspect(file, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				value, err := strconv.Unquote(lit.Value)
				if err != nil {
					value = lit.Value // a raw string with an odd escape; scan it as-is
				}
				if strings.Contains(value, "owner_id") {
					pos := fset.Position(lit.Pos())
					offenders = append(offenders,
						name+":"+strconv.Itoa(pos.Line)+"  "+firstLine(value))
				}
				return true
			})
		}
	}
	// Without this the fence passes on an empty parse, which is how a check over
	// "every file" goes quietly vacuous.
	if scanned == 0 {
		t.Fatal("no handler source was parsed; this fence would pass vacuously")
	}
	if len(offenders) > 0 {
		t.Fatalf("the HTTP surface has %d query referring to owner_id:\n  %s\n\n"+
			"owner_id records who created a project and is NOT an authorisation path (PRD SEC-02).\n"+
			"Use deps.requirePermission / requireGoalPermission / visibleProjects, which read\n"+
			"forge_project_members — the single source of truth for access.",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i]) + " …"
	}
	if len(s) > 90 {
		s = s[:89] + "…"
	}
	return s
}
