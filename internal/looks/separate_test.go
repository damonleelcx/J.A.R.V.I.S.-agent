package looks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The repair loop cannot see a styling verdict. 2026-09-20, looks stage D.
//
// # The decision this holds
//
// damon, 2026-09-18: "looks designed" is a FORGE goal, AND the defect check stays
// closed to styling — `internal/agent/look.go` and `sketch.go` forbid styling
// comments because every styling complaint in testing was wrong and damaged good
// models, and the repair loop must never act on one. Looks are judged by a
// separate gate.
//
// "Separate" is easy to write in a comment and easy to lose in a refactor. Two
// facts make it structural instead:
//
//  1. no file that builds `internal/agent` — or anything it builds on — may
//     import this package. There is then no expression anywhere in the repair
//     loop that can name a Verdict, so a styling verdict cannot reach
//     repairGeometry however carelessly somebody wires things up. (Test files in
//     `agent_test` may import it: the benchmark lives there, it is an external
//     test package, and nothing it does is compiled into the product.)
//  2. the two closed prompts still say what they say. A judge that lives in its
//     own package is no defence at all if somebody pastes "and tell me if the
//     proportions look right" into lookSystem.
func TestTheRepairLoopCannotSeeAStylingVerdict(t *testing.T) {
	const judge = `internal/looks`

	// 1 — the import rule, over every non-test file of the packages the repair
	// loop is built from.
	root := filepath.Join("..", "..")
	closed := []string{
		filepath.Join("internal", "agent"),
		filepath.Join("internal", "agent", "cadbridge"),
		filepath.Join("internal", "domain", "geometry"),
		filepath.Join("internal", "domain", "cad"),
		filepath.Join("internal", "llm"),
		filepath.Join("internal", "eval"),
	}
	checked := 0
	for _, pkg := range closed {
		dir := filepath.Join(root, pkg)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("%s: %v", pkg, err)
		}
		found := false
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			found = true
			checked++
			raw, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), judge) {
				t.Errorf("%s/%s imports %s. The styling gate must not be reachable from the "+
					"code the repair loop is built out of: damon's 2026-09-18 decision is that a "+
					"styling verdict never drives a repair, and a package boundary is the only "+
					"form of that promise a refactor cannot quietly undo.", pkg, name, judge)
			}
		}
		if !found {
			t.Fatalf("%s holds no non-test Go files, so this fence read nothing there", pkg)
		}
	}
	if checked < 50 {
		t.Fatalf("only %d files were read; this fence is meant to sweep the whole repair loop's "+
			"dependencies and has evidently stopped finding them", checked)
	}

	// 2 — the closed prompts are still closed. Read from the shipped files, not
	// from a copy kept here, because a copy is the thing that goes stale.
	for name, must := range map[string][]string{
		"look.go": {
			"Do NOT answer any other question",
			"do NOT comment on PROPORTIONS",
			"materials, styling, realism",
			"Those are not defects and a change made for them is a change nobody asked for",
		},
		"sketch.go": {"shape", "Do NOT"},
	} {
		raw, err := os.ReadFile(filepath.Join(root, "internal", "agent", name))
		if err != nil {
			t.Fatal(err)
		}
		body := string(raw)
		for _, phrase := range must {
			if !strings.Contains(body, phrase) {
				t.Errorf("internal/agent/%s no longer says %q. The defect check is closed to "+
					"styling by damon's 2026-09-18 decision; opening it is what this whole "+
					"separate package exists to avoid.", name, phrase)
			}
		}
		// And nothing in the defect check may reach for this gate's vocabulary.
		for _, word := range []string{"looks better", "more designed", "looks worse"} {
			if strings.Contains(strings.ToLower(body), word) {
				t.Errorf("internal/agent/%s contains %q — that is the looks gate's question, "+
					"and it is asked in internal/looks or nowhere", name, word)
			}
		}
	}
}

// This gate cannot act on what it decides.
//
// The other half of the boundary: a Decision carries no document, no edit and no
// problem, so there is nothing in it for a repair to apply even if one were
// handed one. The strongest form of "it can only say better, worse or no
// difference" is that those three words are the only outcome the type can hold.
func TestTheGateCanOnlySayBetterWorseOrNoDifference(t *testing.T) {
	if Better == Worse || Worse == Same || Better == Same {
		t.Fatal("the three verdicts are not distinct")
	}
	// Read the package's own source: a field carrying a document or an edit would
	// be an instruction, and this gate issues none.
	raw, err := os.ReadFile("judge.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"geometry.Document", "geometry.Edit", "geometry.Problem", "Prototype"} {
		if strings.Contains(string(raw), forbidden) {
			t.Errorf("judge.go names %s. This gate returns a verdict and its evidence; the moment "+
				"it can return something a repair knows how to apply, it is the repair loop again.",
				forbidden)
		}
	}
}
